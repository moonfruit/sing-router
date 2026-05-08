package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
	"github.com/moonfruit/sing-router/internal/install"
)

// confirmStdin is overridable for tests.
var confirmStdin io.Reader = os.Stdin

func newInstallCmd() *cobra.Command {
	var (
		rundir            string
		downloadSingBox   bool
		downloadCNList    bool
		autoStart         bool
		mirrorPrefix      string
		singBoxVersion    string
		firmwareFlag      string
		yesFlag           bool
		skipFirmwareHooks bool
		dryRun            bool
		debugOnly         bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sing-router on this router",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			cfg, _ := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
			if !cmd.Flags().Changed("download-sing-box") {
				downloadSingBox = cfg.Install.DownloadSingBox
			}
			if !cmd.Flags().Changed("download-cn-list") {
				downloadCNList = cfg.Install.DownloadCNList
			}
			if !cmd.Flags().Changed("start") {
				autoStart = cfg.Install.AutoStart
			}
			if mirrorPrefix == "" {
				mirrorPrefix = cfg.Download.MirrorPrefix
			}
			if singBoxVersion == "" {
				singBoxVersion = cfg.Download.SingBoxDefaultVersion
			}

			run := func(label string, fn func() error) error {
				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run]", label)
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), "→", label)
				return fn()
			}

			// 1. Resolve firmware up-front so it can be baked into the rendered daemon.toml.
			kind, err := resolveFirmware(firmwareFlag, cfg.Install.Firmware)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
				os.Exit(2)
			}

			// 2. Merlin warning gate (before any FS writes — abort cleanly if user declines).
			if kind == firmware.KindMerlin && !yesFlag {
				if !confirmMerlin(cmd.OutOrStdout(), confirmStdin) {
					return fmt.Errorf("aborted by user")
				}
			}

			// 3. Layout + seed (renders daemon.toml from template on first install only).
			if err := run("ensure rundir layout", func() error { return install.EnsureLayout(rundir) }); err != nil {
				return err
			}
			vars := install.TemplateVars{
				DownloadSingBox:   downloadSingBox,
				DownloadCNList:    downloadCNList,
				DownloadZashboard: cfg.Install.DownloadZashboard,
				AutoStart:         autoStart,
				Firmware:          string(kind),
			}
			if err := run("seed default config.d/* and render daemon.toml", func() error {
				return install.SeedDefaults(rundir, vars)
			}); err != nil {
				return err
			}
			if !debugOnly {
				if err := run("write /opt/etc/init.d/S99sing-router", func() error {
					return install.WriteInitd("/opt/etc/init.d/S99sing-router", rundir)
				}); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "→ skipped /opt/etc/init.d/S99sing-router (--debug-only)")
			}

			// 4. Install firmware hooks.
			switch {
			case skipFirmwareHooks:
				fmt.Fprintln(cmd.OutOrStdout(), "→ skipped firmware hook installation (--skip-firmware-hooks)")
			case debugOnly:
				fmt.Fprintln(cmd.OutOrStdout(), "→ skipped firmware hook installation (--debug-only)")
			default:
				target, err := firmware.ByName(string(kind))
				if err != nil {
					return err
				}
				if err := run("install firmware hooks ("+string(kind)+")", func() error {
					return target.InstallHooks(rundir)
				}); err != nil {
					return err
				}
			}

			// 5. Optional downloads.
			if downloadSingBox {
				version := singBoxVersion
				if version == "latest" {
					version = resolveLatestSingBoxVersion(mirrorPrefix)
				}
				if version == "" {
					return fmt.Errorf("cannot resolve sing-box version (provide --sing-box-version explicitly)")
				}
				url := install.RenderURL(mirrorPrefix, cfg.Download.SingBoxURLTemplate, version)
				tarball := filepath.Join(rundir, "var", "sing-box.tar.gz")
				if err := run("download sing-box "+url, func() error {
					return install.DownloadFile(url, tarball, cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
				}); err != nil {
					return err
				}
				if err := run("extract sing-box to bin/", func() error {
					return extractSingBox(tarball, filepath.Join(rundir, "bin", "sing-box"))
				}); err != nil {
					return err
				}
			}
			if downloadCNList {
				url := install.RenderURL(mirrorPrefix, cfg.Download.CNListURL, "")
				if err := run("download cn.txt "+url, func() error {
					return install.DownloadFile(url, filepath.Join(rundir, "var", "cn.txt"), cfg.Download.HTTPTimeoutSeconds, cfg.Download.HTTPRetries)
				}); err != nil {
					return err
				}
			}

			// 6. Auto-start.
			switch {
			case autoStart && debugOnly:
				fmt.Fprintln(cmd.OutOrStdout(), "→ ignoring --start because of --debug-only")
			case autoStart:
				if err := run("start init.d service", func() error {
					return runShell("/opt/etc/init.d/S99sing-router", "start")
				}); err != nil {
					return err
				}
			}

			fmt.Fprintln(cmd.OutOrStdout())
			if debugOnly {
				fmt.Fprintln(cmd.OutOrStdout(), "Debug seed complete. To run the daemon in the foreground:")
				fmt.Fprintln(cmd.OutOrStdout(), "  sing-router daemon -D", rundir)
				fmt.Fprintln(cmd.OutOrStdout(), "Logs:", filepath.Join(rundir, "log", "sing-router.log"))
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")
				fmt.Fprintln(cmd.OutOrStdout(), "  1. Edit", filepath.Join(rundir, "daemon.toml"), "to taste")
				fmt.Fprintln(cmd.OutOrStdout(), "  2. Place your zoo.json at", filepath.Join(rundir, "var", "zoo.raw.json"))
				fmt.Fprintln(cmd.OutOrStdout(), "  3. Run `S99sing-router start` (if --start not used) and `sing-router status`")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
	cmd.Flags().BoolVar(&downloadSingBox, "download-sing-box", false, "Download sing-box into bin/ (off on first install; pass --download-sing-box=true to opt in)")
	cmd.Flags().BoolVar(&downloadCNList, "download-cn-list", false, "Download cn.txt into var/ (off on first install; pass --download-cn-list=true to opt in)")
	cmd.Flags().BoolVar(&autoStart, "start", false, "Start init.d service after install (off on first install; pass --start=true to opt in)")
	cmd.Flags().StringVar(&mirrorPrefix, "mirror-prefix", "", "Download mirror prefix (e.g. https://ghproxy.com/)")
	cmd.Flags().StringVar(&singBoxVersion, "sing-box-version", "", "sing-box version to download (default latest)")
	cmd.Flags().StringVar(&firmwareFlag, "firmware", "auto", "Firmware target: auto | koolshare | merlin")
	cmd.Flags().BoolVar(&yesFlag, "yes", false, "Skip Merlin warning interactive confirmation")
	cmd.Flags().BoolVar(&skipFirmwareHooks, "skip-firmware-hooks", false, "Skip firmware-specific hook installation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	cmd.Flags().BoolVar(&debugOnly, "debug-only", false,
		"Seed rundir without writing /opt/etc/init.d or firmware hooks "+
			"(for manual 'sing-router daemon -D' runs; implies --start=false)")
	return cmd
}

// resolveFirmware applies the precedence: CLI flag > daemon.toml > Detect() > reject.
func resolveFirmware(flag, fromToml string) (firmware.Kind, error) {
	if flag != "" && flag != "auto" {
		_, err := firmware.ByName(flag)
		if err != nil {
			return "", err
		}
		return firmware.Kind(flag), nil
	}
	if fromToml != "" {
		_, err := firmware.ByName(fromToml)
		if err == nil {
			return firmware.Kind(fromToml), nil
		}
	}
	kind, err := firmware.Detect()
	if err == nil {
		return kind, nil
	}
	return "", fmt.Errorf(`cannot detect firmware. If this is a Merlin router, run with --firmware=merlin (note: Merlin path is untested, expect manual fixup). If you believe this IS a koolshare router, run with --firmware=koolshare to override the check`)
}

// confirmMerlin prints the warning and reads y/N from in. Returns true if user agrees.
func confirmMerlin(out io.Writer, in io.Reader) bool {
	fmt.Fprintln(out, "WARNING: Merlin firmware support is best-effort and untested.")
	fmt.Fprintln(out, "         The hook injection logic compiles and unit-tests pass, but no")
	fmt.Fprintln(out, "         real Merlin device has validated this path. File issues if")
	fmt.Fprintln(out, "         you hit problems.")
	fmt.Fprint(out, "Continue? [y/N] ")
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "y" || ans == "yes"
}

// payloadOnly removed — moved to internal/firmware/merlin.go (readSnippetPayload).

// resolveLatestSingBoxVersion currently returns a hardcoded fallback (Phase B will resolve via API).
func resolveLatestSingBoxVersion(_ string) string {
	return "1.13.5"
}

func extractSingBox(tarball, target string) error {
	if _, err := os.Stat(tarball); err != nil {
		return err
	}
	tmpDir := filepath.Join(filepath.Dir(target), ".extract")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	if err := runShell("tar", "-xzf", tarball, "-C", tmpDir); err != nil {
		return err
	}
	found, err := findSingBoxBinary(tmpDir)
	if err != nil {
		return err
	}
	if err := os.Rename(found, target+".new"); err != nil {
		return err
	}
	if err := os.Chmod(target+".new", 0o755); err != nil {
		return err
	}
	if err := os.Rename(target+".new", target); err != nil {
		return err
	}
	return os.RemoveAll(tmpDir)
}

func findSingBoxBinary(dir string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if filepath.Base(p) == "sing-box" {
			found = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("sing-box binary not found in tarball")
	}
	return found, nil
}

func runShell(name string, args ...string) error {
	return osexecCommand(name, args...).Run()
}

var osexecCommand = func(name string, args ...string) interface{ Run() error } {
	return exec.Command(name, args...)
}

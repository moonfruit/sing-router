package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/assets"
	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/install"
)

func newInstallCmd() *cobra.Command {
	var (
		rundir          string
		downloadSingBox bool
		downloadCNList  bool
		autoStart       bool
		mirrorPrefix    string
		singBoxVersion  string
		skipJffs        bool
		dryRun          bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sing-router on this router",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			// 读 daemon.toml（若存在），用其 [install] 默认填充未指定的 flag
			tomlPath := filepath.Join(rundir, "daemon.toml")
			cfg, _ := config.LoadDaemonConfig(tomlPath)
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

			if err := run("ensure rundir layout", func() error { return install.EnsureLayout(rundir) }); err != nil {
				return err
			}
			if err := run("seed default config.d/* and daemon.toml", func() error { return install.SeedDefaults(rundir) }); err != nil {
				return err
			}
			if err := run("write /opt/etc/init.d/S99sing-router", func() error {
				return install.WriteInitd("/opt/etc/init.d/S99sing-router", rundir)
			}); err != nil {
				return err
			}
			if !skipJffs {
				natPayload, _ := assets.ReadFile("jffs/nat-start.snippet")
				svcPayload, _ := assets.ReadFile("jffs/services-start.snippet")
				if err := run("inject /jffs/scripts/nat-start", func() error {
					return install.InjectHook("/jffs/scripts/nat-start", "sing-router", payloadOnly(string(natPayload)))
				}); err != nil {
					return err
				}
				if err := run("inject /jffs/scripts/services-start", func() error {
					return install.InjectHook("/jffs/scripts/services-start", "sing-router", payloadOnly(string(svcPayload)))
				}); err != nil {
					return err
				}
			}
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

			if autoStart {
				if err := run("start init.d service", func() error {
					return runShell("/opt/etc/init.d/S99sing-router", "start")
				}); err != nil {
					return err
				}
			}

			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")
			fmt.Fprintln(cmd.OutOrStdout(), "  1. Edit", filepath.Join(rundir, "daemon.toml"), "to taste")
			fmt.Fprintln(cmd.OutOrStdout(), "  2. Place your zoo.json at", filepath.Join(rundir, "var", "zoo.raw.json"))
			fmt.Fprintln(cmd.OutOrStdout(), "  3. Run `S99sing-router start` (if --start not used) and `sing-router status`")
			return nil
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
	cmd.Flags().BoolVar(&downloadSingBox, "download-sing-box", true, "Download sing-box into bin/")
	cmd.Flags().BoolVar(&downloadCNList, "download-cn-list", true, "Download cn.txt into var/")
	cmd.Flags().BoolVar(&autoStart, "start", false, "Start init.d service after install")
	cmd.Flags().StringVar(&mirrorPrefix, "mirror-prefix", "", "Download mirror prefix (e.g. https://ghproxy.com/)")
	cmd.Flags().StringVar(&singBoxVersion, "sing-box-version", "", "sing-box version to download (default latest)")
	cmd.Flags().BoolVar(&skipJffs, "skip-jffs", false, "Skip /jffs/scripts/* hook injection")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without executing")
	return cmd
}

// payloadOnly 从 snippet 文件里抽出 BEGIN/END 之间的内容（snippet 文件本身已包含
// 完整 BEGIN/END，但 InjectHook 期望只接收 payload）。
func payloadOnly(snippet string) string {
	var inside bool
	var out []string
	for _, l := range strings.Split(snippet, "\n") {
		if strings.HasPrefix(l, "# BEGIN") {
			inside = true
			continue
		}
		if strings.HasPrefix(l, "# END") {
			inside = false
			continue
		}
		if inside {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// resolveLatestSingBoxVersion 当前简单返回硬编码 fallback，避免在 install 阶段强依赖
// GitHub releases API（B 阶段会做完整版本解析）。
func resolveLatestSingBoxVersion(_ string) string {
	return "1.13.5"
}

// extractSingBox 简化：调用宿主 tar 命令解压并把 sing-box 移到目标位置。
// 在 RT-BE88U 与本地都假设 entware 提供 tar；不可用时报错。
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

// osexecCommand 默认走 os/exec；测试可替换为 mock。
var osexecCommand = func(name string, args ...string) interface{ Run() error } {
	return exec.Command(name, args...)
}

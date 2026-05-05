package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/firmware"
)

func newUninstallCmd() *cobra.Command {
	var (
		purge             bool
		skipFirmwareHooks bool
		keepInit          bool
		rundir            string
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall sing-router (init.d + firmware hooks; --purge to delete RUNDIR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}

			// 1. stop service if present
			if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err == nil {
				_ = runShell("/opt/etc/init.d/S99sing-router", "stop")
			}

			// 2. resolve firmware from daemon.toml; default to koolshare on missing
			tomlPath := filepath.Join(rundir, "daemon.toml")
			cfg, _ := config.LoadDaemonConfig(tomlPath)
			kindStr := cfg.Install.Firmware
			if kindStr == "" {
				kindStr = string(firmware.KindKoolshare)
			}

			// 3. remove firmware hooks
			if !skipFirmwareHooks {
				target, err := firmware.ByName(kindStr)
				if err != nil {
					return fmt.Errorf("uninstall: %w", err)
				}
				if err := target.RemoveHooks(); err != nil {
					return err
				}
			}

			// 4. remove init.d
			if !keepInit {
				_ = os.Remove("/opt/etc/init.d/S99sing-router")
			}
			// 5. purge rundir
			if purge {
				if err := os.RemoveAll(rundir); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled. /opt/sbin/sing-router binary preserved (delete manually if desired).")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also delete RUNDIR (lose all user config and downloaded artifacts)")
	cmd.Flags().BoolVar(&skipFirmwareHooks, "skip-firmware-hooks", false, "Don't touch firmware-specific hook files")
	cmd.Flags().BoolVar(&keepInit, "keep-init", false, "Don't delete /opt/etc/init.d/S99sing-router")
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (for --purge)")
	return cmd
}

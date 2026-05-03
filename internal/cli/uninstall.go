package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/install"
)

func newUninstallCmd() *cobra.Command {
	var (
		purge    bool
		skipJffs bool
		keepInit bool
		rundir   string
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall sing-router (init.d + jffs hooks; --purge to delete RUNDIR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			// 1. stop service if present
			if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err == nil {
				_ = runShell("/opt/etc/init.d/S99sing-router", "stop")
			}
			// 2. remove jffs hooks
			if !skipJffs {
				if err := install.RemoveHook("/jffs/scripts/nat-start", "sing-router"); err != nil {
					return err
				}
				if err := install.RemoveHook("/jffs/scripts/services-start", "sing-router"); err != nil {
					return err
				}
			}
			// 3. remove init.d
			if !keepInit {
				_ = os.Remove("/opt/etc/init.d/S99sing-router")
			}
			// 4. purge rundir
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
	cmd.Flags().BoolVar(&skipJffs, "skip-jffs", false, "Don't touch /jffs/scripts/")
	cmd.Flags().BoolVar(&keepInit, "keep-init", false, "Don't delete /opt/etc/init.d/S99sing-router")
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (for --purge)")
	return cmd
}

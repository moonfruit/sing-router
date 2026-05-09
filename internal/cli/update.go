package cli

import (
	"context"
	"fmt"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	syncpkg "github.com/moonfruit/sing-router/internal/sync"
)

// newUpdateCmd 提供 `sing-router update [sing-box|cn|zoo|all]`：从 gitee/公网拉
// 最新资源到 rundir。CLI 内部直接调 sync 包，不经 daemon HTTP API（同步是 IO
// 密集且独立于 sing-box 主进程的工作，无需通过 daemon 中转）。
func newUpdateCmd() *cobra.Command {
	var rundir string
	cmd := &cobra.Command{
		Use:   "update [sing-box|cn|zoo|all]",
		Short: "Pull latest sing-box / cn.txt / zoo.json from gitee (and public CDN for cn.txt)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "all"
			if len(args) == 1 {
				target = args[0]
			}
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			cfg, err := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
			if err != nil {
				return fmt.Errorf("load daemon.toml: %w", err)
			}
			if cfg.Gitee.Token == "" && (target == "sing-box" || target == "zoo" || target == "all") {
				return fmt.Errorf("gitee.token is empty; set it in daemon.toml or via SING_ROUTER_GITEE_TOKEN")
			}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			u := syncpkg.NewUpdater(cfg, rundir)

			out := cmd.OutOrStdout()
			switch target {
			case "sing-box":
				changed, version, err := u.UpdateSingBox(ctx)
				printItem(out, "sing-box", changed, version, err)
				return err
			case "cn":
				changed, err := u.UpdateCNList(ctx)
				printItem(out, "cn.txt", changed, "", err)
				return err
			case "zoo":
				changed, err := u.UpdateZoo(ctx)
				printItem(out, "zoo.json", changed, "", err)
				return err
			case "all":
				r := u.UpdateAll(ctx)
				printItem(out, "sing-box", r.SingBox.Changed, r.SingBox.Version, r.SingBox.Err)
				printItem(out, "cn.txt", r.CNList.Changed, "", r.CNList.Err)
				printItem(out, "zoo.json", r.Zoo.Changed, "", r.Zoo.Err)
				if r.HasError() {
					return fmt.Errorf("one or more resources failed to update")
				}
				return nil
			default:
				return fmt.Errorf("unknown target %q (want sing-box | cn | zoo | all)", target)
			}
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
	return cmd
}

func printItem(out interface {
	Write([]byte) (int, error)
}, name string, changed bool, version string, err error,
) {
	if err != nil {
		fmt.Fprintf(out, "✗ %-10s  %s\n", name, err.Error())
		return
	}
	status := "unchanged"
	if changed {
		status = "updated"
	}
	if version != "" {
		fmt.Fprintf(out, "✓ %-10s  %s  (version %s)\n", name, status, version)
	} else {
		fmt.Fprintf(out, "✓ %-10s  %s\n", name, status)
	}
}

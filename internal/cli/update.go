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

// newUpdateCmd 提供 `sing-router update [sing-box|cn|zoo|all] [--apply]`:
// 从 gitee/公网拉最新资源到 rundir。默认仅下载,保留手动节奏(用户自己调
// `restart` / `reload-cn-ipset` / `apply`);加 --apply 时下载完直接 POST
// /api/v1/apply 让 daemon 走 Applier 流程把变化落地(需要 daemon 在跑)。
//
// 对于 sing-box,--apply 路径不在 CLI 里 rename staging,而是把 bin/sing-box.new
// 留给 daemon 端 Applier 做 backup+rename(确保 restart 失败可 revert)。
// 默认路径仍走 UpdateSingBox 内部 rename 以保持向后兼容。
func newUpdateCmd() *cobra.Command {
	var rundir string
	var apply bool
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
			var anyChanged bool
			switch target {
			case "sing-box":
				if apply {
					_, changed, version, err := u.UpdateSingBoxStaging(ctx)
					printItem(out, "sing-box", changed, version, err)
					if err != nil {
						return err
					}
					anyChanged = changed
				} else {
					changed, version, err := u.UpdateSingBox(ctx)
					printItem(out, "sing-box", changed, version, err)
					if err != nil {
						return err
					}
				}
			case "cn":
				changed, err := u.UpdateCNList(ctx)
				printItem(out, "cn.txt", changed, "", err)
				if err != nil {
					return err
				}
				anyChanged = changed
			case "zoo":
				changed, err := u.UpdateZoo(ctx)
				printItem(out, "zoo.json", changed, "", err)
				if err != nil {
					return err
				}
				anyChanged = changed
			case "all":
				r := u.UpdateAll(ctx)
				printItem(out, "sing-box", r.SingBox.Changed, r.SingBox.Version, r.SingBox.Err)
				printItem(out, "cn.txt", r.CNList.Changed, "", r.CNList.Err)
				printItem(out, "zoo.json", r.Zoo.Changed, "", r.Zoo.Err)
				if r.HasError() {
					return fmt.Errorf("one or more resources failed to update")
				}
				if !apply && r.SingBox.Changed {
					// 默认路径需要把 sing-box staging 落到正式位置(与改造前一致)。
					if err := u.CommitSingBoxStaging(); err != nil {
						return fmt.Errorf("commit sing-box staging: %w", err)
					}
				}
				anyChanged = r.SingBox.Changed || r.CNList.Changed || r.Zoo.Changed
			default:
				return fmt.Errorf("unknown target %q (want sing-box | cn | zoo | all)", target)
			}

			if !apply {
				return nil
			}
			if !anyChanged {
				fmt.Fprintln(out, "ℹ --apply: nothing changed; daemon untouched")
				return nil
			}
			client := NewHTTPClient(getDaemonBase(cmd))
			if err := client.PostJSON("/api/v1/apply", nil, nil); err != nil {
				if IsDaemonNotRunning(err) {
					return fmt.Errorf("--apply requires the daemon to be running; start it first")
				}
				return fmt.Errorf("POST /api/v1/apply: %w", err)
			}
			fmt.Fprintln(out, "✓ apply: triggered daemon-side apply (see daemon logs for outcome)")
			return nil
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (default /opt/home/sing-router)")
	cmd.Flags().BoolVar(&apply, "apply", false, "After download, POST /api/v1/apply so daemon applies the changes (restart sing-box / reload ipset). Default: download only.")
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

package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/notify"
	"github.com/moonfruit/sing-router/internal/notify/bark"
)

// notifyTestSendTimeout 是 notify test 单渠道同步发送的超时。
const notifyTestSendTimeout = 15 * time.Second

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Notification channel utilities",
	}
	cmd.AddCommand(newNotifyTestCmd())
	return cmd
}

func newNotifyTestCmd() *cobra.Command {
	var (
		rundir  string
		channel string
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a test notification through configured channels",
		Long: "加载 daemon.toml，同步向各 [[notify.bark]] 渠道发一条测试通知，" +
			"逐渠道报成功/失败。不依赖 daemon 运行。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}
			cfg, err := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
			if err != nil {
				return err
			}
			return runNotifyTest(cmd.OutOrStdout(), cfg.Notify, channel)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory")
	cmd.Flags().StringVar(&channel, "channel", "", "Only test the channel with this name")
	return cmd
}

// runNotifyTest 构造（被筛选的）渠道并同步发一条测试通知，逐渠道打印结果。
// 无 --channel 时只测 enabled 的渠道；指定 --channel 时无视 enabled（用户显式点名）。
func runNotifyTest(w io.Writer, cfg config.NotifyConfig, channelFilter string) error {
	if !cfg.Enabled {
		fmt.Fprintln(w, "提示：[notify].enabled=false，daemon 运行时不会推送通知；本测试仅验证渠道配置。")
	}

	type namedChannel struct {
		name string
		ch   notify.Channel
	}
	var (
		channels []namedChannel
		failed   int
	)
	for _, b := range cfg.Bark {
		if channelFilter != "" && b.Name != channelFilter {
			continue
		}
		if channelFilter == "" && !b.Enabled {
			continue
		}
		ch, err := bark.New(barkConfigFrom(b))
		if err != nil {
			fmt.Fprintf(w, "  FAIL  bark/%s — %v\n", b.Name, err)
			failed++
			continue
		}
		channels = append(channels, namedChannel{name: ch.Name(), ch: ch})
	}
	if len(channels) == 0 && failed == 0 {
		return fmt.Errorf("no matching notification channel configured")
	}

	n := notify.Notification{
		Kind:     "notify.test",
		Title:    "🔔 sing-router 测试通知",
		Body:     "这是一条来自 `sing-router notify test` 的测试推送，收到即说明渠道配置可用。",
		Priority: notify.PriorityNormal,
		Time:     time.Now(),
	}
	for _, nc := range channels {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTestSendTimeout)
		err := nc.ch.Send(ctx, n)
		cancel()
		if err != nil {
			fmt.Fprintf(w, "  FAIL  %s — %v\n", nc.name, err)
			failed++
			continue
		}
		fmt.Fprintf(w, "  OK    %s\n", nc.name)
	}
	if failed > 0 {
		return fmt.Errorf("%d channel(s) failed", failed)
	}
	return nil
}

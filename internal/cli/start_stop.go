package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type postOnlyCmd struct {
	use   string
	short string
	path  string
}

func (p postOnlyCmd) build() *cobra.Command {
	return &cobra.Command{
		Use:   p.use,
		Short: p.short,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewHTTPClient(getDaemonBase(cmd))
			if err := client.PostJSON(p.path, nil, nil); err != nil {
				if IsDaemonNotRunning(err) {
					return fmt.Errorf("daemon not running; use `S99sing-router start` first")
				}
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
}

func newStartCmd() *cobra.Command {
	return postOnlyCmd{use: "start", short: "Start sing-box (from stopped state)", path: "/api/v1/start"}.build()
}

func newStopCmd() *cobra.Command {
	return postOnlyCmd{use: "stop", short: "Stop sing-box + uninstall iptables; daemon stays", path: "/api/v1/stop"}.build()
}

func newRestartCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart sing-box: shutdown (stop + teardown iptables) + startup",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewHTTPClient(getDaemonBase(cmd))
			path := "/api/v1/restart"
			if force {
				path += "?force=true"
			}
			if err := client.PostJSON(path, nil, nil); err != nil {
				if IsDaemonNotRunning(err) {
					return fmt.Errorf("daemon not running; use `S99sing-router start` first")
				}
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Bypass the 2s restart throttle window (use for hooks that MUST take effect, e.g. firmware nat-start after flushing iptables)")
	return cmd
}

func newCheckCmd() *cobra.Command {
	return postOnlyCmd{use: "check", short: "Validate config.d/* via sing-box check", path: "/api/v1/check"}.build()
}

func newShutdownCmd() *cobra.Command {
	return postOnlyCmd{use: "shutdown", short: "Shut down the daemon (equivalent to init.d stop)", path: "/api/v1/shutdown"}.build()
}

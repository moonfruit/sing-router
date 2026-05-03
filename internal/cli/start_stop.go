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
	return postOnlyCmd{use: "restart", short: "Restart sing-box (keep iptables)", path: "/api/v1/restart"}.build()
}

func newCheckCmd() *cobra.Command {
	return postOnlyCmd{use: "check", short: "Validate config.d/* via sing-box check", path: "/api/v1/check"}.build()
}

func newReapplyRulesCmd() *cobra.Command {
	return postOnlyCmd{use: "reapply-rules", short: "Reinstall iptables/ipset (nat-start hook)", path: "/api/v1/reapply-rules"}.build()
}

func newShutdownCmd() *cobra.Command {
	return postOnlyCmd{use: "shutdown", short: "Shut down the daemon (equivalent to init.d stop)", path: "/api/v1/shutdown"}.build()
}

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon + sing-box status",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := getDaemonBase(cmd)
			client := NewHTTPClient(base)
			var body map[string]any
			err := client.GetJSON("/api/v1/status", &body)
			if err != nil {
				if IsDaemonNotRunning(err) {
					return printOfflineStatus(cmd.OutOrStdout(), asJSON)
				}
				return err
			}
			return printStatus(cmd.OutOrStdout(), body, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of pretty text")
	return cmd
}

// getDaemonBase 解析 --daemon-url 全局 flag；默认 http://127.0.0.1:9998。
func getDaemonBase(cmd *cobra.Command) string {
	base, _ := cmd.Flags().GetString("daemon-url")
	if base == "" {
		base, _ = cmd.Root().PersistentFlags().GetString("daemon-url")
	}
	if base == "" {
		return "http://127.0.0.1:9998"
	}
	return base
}

func printStatus(w io.Writer, body map[string]any, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(body)
	}
	daemon, _ := body["daemon"].(map[string]any)
	sb, _ := body["sing_box"].(map[string]any)
	rules, _ := body["rules"].(map[string]any)
	fmt.Fprintf(w, "daemon:   state=%v  pid=%v  rundir=%v\n", daemon["state"], daemon["pid"], daemon["rundir"])
	fmt.Fprintf(w, "sing-box: pid=%v  restart_count=%v\n", sb["pid"], sb["restart_count"])
	fmt.Fprintf(w, "rules:    iptables_installed=%v\n", rules["iptables_installed"])
	return nil
}

func printOfflineStatus(w io.Writer, asJSON bool) error {
	snap := map[string]any{
		"daemon": map[string]any{
			"state":   "offline",
			"pid":     nil,
			"running": false,
		},
		"hint": "use `S99sing-router start` (Entware init.d) to launch the daemon",
	}
	if asJSON {
		return json.NewEncoder(w).Encode(snap)
	}
	fmt.Fprintln(w, "daemon: not running (use `S99sing-router start` to launch)")
	if _, err := os.Stat("/opt/etc/init.d/S99sing-router"); err != nil {
		fmt.Fprintln(w, "init.d script missing; run `sing-router install` first")
	}
	return nil
}

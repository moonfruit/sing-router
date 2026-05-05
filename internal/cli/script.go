package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/assets"
)

var scriptMap = map[string]string{
	"startup":               "shell/startup.sh",
	"teardown":              "shell/teardown.sh",
	"init.d":                "initd/S99sing-router",
	"merlin/nat-start":      "firmware/merlin/nat-start.snippet",
	"merlin/services-start": "firmware/merlin/services-start.snippet",
	"koolshare/N99":         "firmware/koolshare/N99sing-router.sh",
}

func newScriptCmd() *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use:   "script <name>",
		Short: "Print embedded script (startup|teardown|init.d|merlin/nat-start|merlin/services-start|koolshare/N99)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if remote {
				client := NewHTTPClient(getDaemonBase(cmd))
				resp, err := client.GetStream("/api/v1/script/"+name, nil)
				if err != nil {
					return err
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode >= 400 {
					return fmt.Errorf("daemon returned %d", resp.StatusCode)
				}
				_, err = io.Copy(cmd.OutOrStdout(), resp.Body)
				return err
			}
			path, ok := scriptMap[name]
			if !ok {
				return fmt.Errorf("unknown script %q (one of: startup, teardown, init.d, merlin/nat-start, merlin/services-start, koolshare/N99)", name)
			}
			data, err := assets.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(data)
			return err
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "fetch from daemon (HTTP) instead of embedded copy")
	return cmd
}

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/assets"
	"github.com/moonfruit/sing-router/internal/firmware"
	"github.com/moonfruit/sing-router/internal/install"
)

var scriptMap = map[string]string{
	"startup":               "shell/startup.sh",
	"teardown":              "shell/teardown.sh",
	"reload-cn-ipset":       "shell/reload-cn-ipset.sh",
	"reapply-routes":        "shell/reapply-routes.sh",
	"init.d":                "initd/S99sing-router",
	"merlin/nat-start":      "firmware/merlin/nat-start.snippet",
	"merlin/services-start": "firmware/merlin/services-start.snippet",
	"koolshare/N99":         "firmware/koolshare/N99sing-router.sh.tmpl",
}

// loadScript reads an embedded script by its scriptMap name and, when the
// underlying asset is a `.tmpl`, renders {{.Binary}} with the running
// sing-router's own path so the printed script is directly runnable.
// Otherwise pipelines like `sing-router script koolshare/N99 | sh -s start_nat`
// would receive an unrendered template and the BINARY guard would skip.
//
// Shared by the local `script` command and the daemon's /api/v1/script/ handler.
func loadScript(name string) ([]byte, error) {
	path, ok := scriptMap[name]
	if !ok {
		return nil, fmt.Errorf("unknown script %q", name)
	}
	data, err := assets.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".tmpl") {
		return data, nil
	}
	binary, err := install.ResolveSelfBinary()
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return firmware.RenderHookTemplate(path, data, binary)
}

func newScriptCmd() *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use:   "script <name>",
		Short: "Print embedded script (startup|teardown|reload-cn-ipset|reapply-routes|init.d|merlin/nat-start|merlin/services-start|koolshare/N99)",
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
			data, err := loadScript(name)
			if err != nil {
				if strings.HasPrefix(err.Error(), "unknown script") {
					return fmt.Errorf("%w (one of: startup, teardown, reload-cn-ipset, reapply-routes, init.d, merlin/nat-start, merlin/services-start, koolshare/N99)", err)
				}
				return err
			}
			_, err = os.Stdout.Write(data)
			return err
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "fetch from daemon (HTTP) instead of embedded copy")
	return cmd
}

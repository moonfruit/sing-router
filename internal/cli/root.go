package cli

import (
	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/version"
)

// NewRootCmd 构造顶层 cobra.Command，挂载所有子命令。
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sing-router",
		Short:         "Transparent router manager for sing-box on Asus Merlin/Entware",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.PersistentFlags().String("daemon-url", "http://127.0.0.1:9998", "Daemon HTTP base URL")
	cmd.AddCommand(
		newVersionCmd(),
		newStatusCmd(),
		newStartCmd(),
		newStopCmd(),
		newRestartCmd(),
		newCheckCmd(),
		newReapplyRulesCmd(),
		newShutdownCmd(),
		newLogsCmd(),
		newScriptCmd(),
		newDaemonCmd(),
		newInstallCmd(),
	)
	return cmd
}

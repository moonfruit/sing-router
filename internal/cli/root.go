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
	cmd.AddCommand(newVersionCmd())
	return cmd
}

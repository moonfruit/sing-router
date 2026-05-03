package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	var rundir string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as long-running supervisor (called by init.d)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd.Context(), rundir)
		},
	}
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "/opt/home/sing-router", "Runtime root directory")
	return cmd
}

// runDaemon 在 Phase 12 由 internal/cli/wireup_daemon.go 真正实现。
// 这里给一个占位，避免把守护进程依赖（supervisor / log writer / ...）引到所有 CLI 命令。
var runDaemon = func(_ context.Context, _ string) error {
	return errNotWired
}

var errNotWired = errors.New("daemon entry point not wired (run sing-router built from main)")

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/daemon"
	"github.com/moonfruit/sing-router/internal/firmware"
)

func newUninstallCmd() *cobra.Command {
	var (
		purge             bool
		skipFirmwareHooks bool
		keepInit          bool
		rundir            string
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall sing-router (init.d + firmware hooks; --purge to delete RUNDIR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rundir == "" {
				rundir = "/opt/home/sing-router"
			}

			// 1. stop daemon if running. We DON'T call `S99sing-router stop` because
			// entware's rc.func stops via `killall <PROC>` (PROC=sing-router), which
			// would also SIGTERM this very `sing-router uninstall` process. Read the
			// PID file written by the daemon (rundir/run/sing-router.pid) and signal
			// it directly.
			stopDaemonByPidFile(filepath.Join(rundir, "run", "sing-router.pid"))

			// teardown.sh 刻意保留 client_bypass 动态 set（租约要活过 Restart），
			// 所以这里是它唯一的清理点。此刻 daemon 已退出、teardown 已跑完，
			// 没有 iptables 规则引用它，destroy 必定成功。
			// best-effort：非 Linux 平台、set 本就不存在、ipset 未安装都属正常。
			destroyClientBypassSet()

			// 2. resolve firmware from daemon.toml; default to koolshare on missing
			tomlPath := filepath.Join(rundir, "daemon.toml")
			cfg, _ := config.LoadDaemonConfig(tomlPath)
			kindStr := cfg.Install.Firmware
			if kindStr == "" {
				kindStr = string(firmware.KindKoolshare)
			}

			// 3. remove firmware hooks
			if !skipFirmwareHooks {
				target, err := firmware.ByName(kindStr)
				if err != nil {
					return fmt.Errorf("uninstall: %w", err)
				}
				if err := target.RemoveHooks(); err != nil {
					return err
				}
			}

			// 4. remove init.d
			if !keepInit {
				_ = os.Remove("/opt/etc/init.d/S99sing-router")
			}
			// 5. purge rundir
			if purge {
				if err := os.RemoveAll(rundir); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled. /opt/sbin/sing-router binary preserved (delete manually if desired).")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Also delete RUNDIR (lose all user config and downloaded artifacts)")
	cmd.Flags().BoolVar(&skipFirmwareHooks, "skip-firmware-hooks", false, "Don't touch firmware-specific hook files")
	cmd.Flags().BoolVar(&keepInit, "keep-init", false, "Don't delete /opt/etc/init.d/S99sing-router")
	cmd.Flags().StringVarP(&rundir, "rundir", "D", "", "Runtime root directory (for --purge)")
	return cmd
}

// stopDaemonByPidFile signals SIGTERM to the daemon recorded in pidFile and
// waits up to ~5s for it to exit, then SIGKILLs as a fallback. Silently returns
// if the file is missing, malformed, or the process is already gone.
func stopDaemonByPidFile(pidFile string) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = proc.Signal(syscall.SIGKILL)
}

// destroyClientBypassSet 销毁动态 bypass ipset。所有失败都静默忽略——
// uninstall 不该因为一个可选功能的残留清理失败而中断。
func destroyClientBypassSet() {
	_ = exec.Command("ipset", "destroy", daemon.ClientBypassSet).Run()
}

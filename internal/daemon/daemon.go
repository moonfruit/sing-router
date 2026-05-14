package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/moonfruit/sing2seq/clef"

	syncpkg "github.com/moonfruit/sing-router/internal/sync"
)

// Options 是 daemon 入口接受的参数。
type Options struct {
	Rundir        string
	Listen        string
	Version       string
	Emitter       *clef.Emitter
	LogFile       string // sing-router.log 绝对路径；通过 /api/v1/status 暴露给 CLI logs 默认路径推断
	Supervisor    *Supervisor
	ReapplyRules  func(context.Context) error
	CheckConfig   func(context.Context) error
	ReloadCNIpset func(context.Context) error // 仅重建 cn ipset 不动 iptables 规则
	StatusExtra   func() map[string]any
	ScriptByName  func(name string) ([]byte, error)

	// ReopenLog 在收到 SIGHUP 时调用,用于 logrotate copytruncate 反向场景。
	// 为 nil 时 SIGHUP 仅被吞掉(不让 Go runtime 走默认终止)。
	ReopenLog func() error

	// Updater 与 Sync 控制 daemon 后台资源同步；Updater 为 nil 时不启动后台 loop。
	// Applier 为 nil 时即使 Sync.AutoApply=true 也只做日志(不会真 apply)。
	Updater *syncpkg.Updater
	Sync    SyncLoopConfig
	Applier *Applier
}

// Run 阻塞跑 daemon：HTTP listener + supervisor 主循环 + signal handling。
func Run(ctx context.Context, opts Options) error {
	if err := writePID(filepath.Join(opts.Rundir, "run", "sing-router.pid")); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer func() { _ = os.Remove(filepath.Join(opts.Rundir, "run", "sing-router.pid")) }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	deps := APIDeps{
		Supervisor:    opts.Supervisor,
		Emitter:       opts.Emitter,
		Version:       opts.Version,
		Rundir:        opts.Rundir,
		LogFile:       opts.LogFile,
		ReapplyRules:  opts.ReapplyRules,
		CheckConfig:   opts.CheckConfig,
		ReloadCNIpset: opts.ReloadCNIpset,
		StatusExtra:   opts.StatusExtra,
		ScriptByName:  opts.ScriptByName,
		ShutdownHook:  cancel,
	}
	if opts.Applier != nil {
		deps.ApplyPending = opts.Applier.ApplyPending
	}
	mux := NewMux(deps)

	// 后台资源同步（gitee → bin/sing-box / var/zoo.raw.json / var/cn.txt）。
	if opts.Updater != nil {
		StartSyncLoop(ctx, opts.Updater, opts.Sync, opts.Emitter, opts.Applier)
	}

	httpDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("daemon.http", r)
				opts.Emitter.Fatal("recover", "panic.recovered",
					"panic in {Name}: see stderr.log for stack",
					map[string]any{"Name": "daemon.http"})
				cancel() // 走主循环的 graceful Shutdown,确保 teardown.sh 跑
				httpDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		httpDone <- ServeHTTP(ctx, mux, opts.Listen)
	}()

	// 信号: TERM/INT → cancel ctx, HUP → reopen 日志(不退出), PIPE → 忽略
	// (fd 2 现在被 wireup 重定向到 stderr.log,EPIPE 不应该再撞到 Go 默认 sigpipe-on-fd-2)。
	signal.Ignore(syscall.SIGPIPE)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("daemon.hup", r)
			}
		}()
		for range hupCh {
			if opts.ReopenLog != nil {
				if err := opts.ReopenLog(); err != nil {
					opts.Emitter.Warn("log", "log.reopen.failed",
						"reopen log on SIGHUP: {Err}", map[string]any{"Err": err.Error()})
				} else {
					opts.Emitter.Info("log", "log.reopen.ok", "log reopened on SIGHUP", nil)
				}
			}
		}
	}()

	// Boot supervisor
	if err := opts.Supervisor.Boot(ctx); err != nil {
		opts.Emitter.Fatal("supervisor", "supervisor.boot.failed", "boot failed: {Err}", map[string]any{"Err": err.Error()})
		// fatal 状态保持 HTTP 存活，等待 SIGTERM 或 /shutdown
	}

	// 路由巡检：兜底外部 `kill -HUP <sing-box-pid>` 触发 sing-box reload→TUN 重建
	// 丢掉 device-bound 路由的场景（supervisor 状态机观察不到这次"换 utun"）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("supervisor.WatchRoutes", r)
				opts.Emitter.Fatal("recover", "panic.recovered",
					"panic in {Name}: see stderr.log for stack",
					map[string]any{"Name": "supervisor.WatchRoutes"})
			}
		}()
		opts.Supervisor.WatchRoutes(ctx)
	}()

	// 后台跑 supervisor restart loop
	runDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("supervisor.Run", r)
				opts.Emitter.Fatal("recover", "panic.recovered",
					"panic in {Name}: see stderr.log for stack",
					map[string]any{"Name": "supervisor.Run"})
				cancel()
				runDone <- fmt.Errorf("panic: %v", r)
			}
		}()
		runDone <- opts.Supervisor.Run(ctx)
	}()

	select {
	case <-sigCh:
		cancel()
	case <-ctx.Done():
	}

	// 优雅关停
	sctx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sCancel()
	_ = opts.Supervisor.Shutdown(sctx)
	<-runDone
	<-httpDone
	return nil
}

func writePID(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
}

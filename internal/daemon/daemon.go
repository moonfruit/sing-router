package daemon

import (
	"context"
	"fmt"
	"net/http"
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
	Rundir       string
	Listen       string
	Version      string
	Emitter      *clef.Emitter
	Bus          *clef.Bus // 给 /api/v1/logs?follow=true 的 SSE 订阅
	LogFile      string    // sing-router.log 绝对路径；给 /api/v1/logs 历史 tail
	Supervisor   *Supervisor
	ReapplyRules func(context.Context) error
	CheckConfig  func(context.Context) error
	StatusExtra  func() map[string]any
	ScriptByName func(name string) ([]byte, error)

	// GiteeProxy 是 /api/v1/proxy/gitee/ 路由的 handler；为 nil 表示未配置 gitee。
	GiteeProxy http.Handler

	// Updater 与 Sync 控制 daemon 后台资源同步；任一为 nil 时不启动后台 loop。
	Updater *syncpkg.Updater
	Sync    SyncLoopConfig
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
		Supervisor:   opts.Supervisor,
		Emitter:      opts.Emitter,
		Bus:          opts.Bus,
		Version:      opts.Version,
		Rundir:       opts.Rundir,
		LogFile:      opts.LogFile,
		Ctx:          ctx,
		ReapplyRules: opts.ReapplyRules,
		CheckConfig:  opts.CheckConfig,
		StatusExtra:  opts.StatusExtra,
		ScriptByName: opts.ScriptByName,
		ShutdownHook: cancel,
		GiteeProxy:   opts.GiteeProxy,
	}
	mux := NewMux(deps)

	// 后台资源同步（gitee → bin/sing-box / var/zoo.raw.json / var/cn.txt）。
	if opts.Updater != nil {
		StartSyncLoop(ctx, opts.Updater, opts.Sync, opts.Emitter)
	}

	httpDone := make(chan error, 1)
	go func() { httpDone <- ServeHTTP(ctx, mux, opts.Listen) }()

	// SIGTERM/SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	// Boot supervisor
	if err := opts.Supervisor.Boot(ctx); err != nil {
		opts.Emitter.Fatal("supervisor", "supervisor.boot.failed", "boot failed: {Err}", map[string]any{"Err": err.Error()})
		// fatal 状态保持 HTTP 存活，等待 SIGTERM 或 /shutdown
	}

	// 后台跑 supervisor restart loop
	runDone := make(chan error, 1)
	go func() { runDone <- opts.Supervisor.Run(ctx) }()

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

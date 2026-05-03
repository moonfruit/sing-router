package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/moonfruit/sing-router/assets"
	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/daemon"
	log "github.com/moonfruit/sing-router/internal/log"
	"github.com/moonfruit/sing-router/internal/shell"
	"github.com/moonfruit/sing-router/internal/version"
)

// init 把 daemon.go 里的 runDaemon 占位换成真实实现。
func init() {
	runDaemon = realRunDaemon
}

// realRunDaemon 是 daemon 子命令的真实入口。
func realRunDaemon(ctx context.Context, rundir string) error {
	if rundir == "" {
		rundir = "/opt/home/sing-router"
	}
	if err := os.Chdir(rundir); err != nil {
		return fmt.Errorf("chdir %s: %w", rundir, err)
	}
	cfg, err := config.LoadDaemonConfig(filepath.Join(rundir, "daemon.toml"))
	if err != nil {
		return err
	}

	level, _ := log.ParseLevel(cfg.Log.Level)
	logPath := filepath.Join(rundir, cfg.Log.File)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	writer, err := log.NewWriter(log.WriterConfig{
		Path:       logPath,
		MaxSize:    int64(cfg.Log.MaxSizeMB) * 1024 * 1024,
		MaxBackups: cfg.Log.MaxBackups,
		Gzip:       true,
	})
	if err != nil {
		return err
	}
	defer func() { _ = writer.Close() }()
	bus := log.NewBus(4096)
	defer bus.Close()
	em := log.NewEmitter(log.EmitterConfig{
		Source:   "daemon",
		MinLevel: level,
		Writer:   writer,
		Bus:      bus,
	})
	em.Info("supervisor", "supervisor.boot.started", "starting daemon at {Rundir}", map[string]any{"Rundir": rundir})

	routing := config.LoadRouting(cfg)
	cnPath := filepath.Join(rundir, "var", "cn.txt")
	runner := shell.NewRunner(shell.RunnerConfig{
		Bash: "/bin/bash",
		Env:  routing.EnvVars(cnPath),
	})
	runner.OnStderr = func(line string) {
		em.Info("shell", "shell.stderr", "{Line}", map[string]any{"Line": line})
	}
	startup := assets.MustReadFile("shell/startup.sh")
	teardown := assets.MustReadFile("shell/teardown.sh")

	sup := daemon.New(daemon.SupervisorConfig{
		Emitter:       em,
		SingBoxBinary: filepath.Join(rundir, cfg.Runtime.SingBoxBinary),
		SingBoxArgs:   []string{"run", "-D", rundir, "-C", cfg.Runtime.ConfigDir},
		SingBoxDir:    rundir,
		ReadyConfig: daemon.ReadyConfig{
			TCPDials: []string{
				fmt.Sprintf("127.0.0.1:%d", routing.DnsPort),
				fmt.Sprintf("127.0.0.1:%d", routing.RedirectPort),
			},
			ClashAPIURL:  "http://127.0.0.1:9999/version",
			TotalTimeout: 5 * time.Second,
			Interval:     200 * time.Millisecond,
		},
		StartupHook: func(ctx context.Context) error {
			em.Info("shell", "shell.startup.exec", "running startup.sh", nil)
			if err := runner.Run(ctx, string(startup), nil); err != nil {
				em.Error("shell", "shell.startup.failed", "startup failed: {Err}", map[string]any{"Err": err.Error()})
				return err
			}
			em.Info("shell", "shell.startup.completed", "iptables installed", nil)
			return nil
		},
		TeardownHook: func(ctx context.Context) error {
			em.Info("shell", "shell.teardown.exec", "running teardown.sh", nil)
			if err := runner.Run(ctx, string(teardown), nil); err != nil {
				em.Warn("shell", "shell.teardown.failed", "teardown failed: {Err}", map[string]any{"Err": err.Error()})
				return err
			}
			em.Info("shell", "shell.teardown.completed", "iptables removed", nil)
			return nil
		},
	})

	return daemon.Run(ctx, daemon.Options{
		Rundir:     rundir,
		Listen:     cfg.HTTP.Listen,
		Version:    version.String(),
		Emitter:    em,
		Supervisor: sup,
		ReapplyRules: func(ctx context.Context) error {
			if err := runner.Run(ctx, string(teardown), nil); err != nil {
				em.Warn("shell", "shell.teardown.failed", "teardown best-effort failed: {Err}", map[string]any{"Err": err.Error()})
			}
			return runner.Run(ctx, string(startup), nil)
		},
		CheckConfig: func(ctx context.Context) error {
			return config.CheckSingBoxConfig(ctx,
				filepath.Join(rundir, cfg.Runtime.SingBoxBinary),
				filepath.Join(rundir, cfg.Runtime.ConfigDir))
		},
		StatusExtra: func() map[string]any {
			return map[string]any{
				"config": map[string]any{
					"config_dir": filepath.Join(rundir, cfg.Runtime.ConfigDir),
				},
			}
		},
		ScriptByName: func(name string) ([]byte, error) {
			path, ok := scriptMap[name]
			if !ok {
				return nil, fmt.Errorf("unknown script %q", name)
			}
			return assets.ReadFile(path)
		},
	})
}

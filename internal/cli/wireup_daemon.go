package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/moonfruit/sing-router/assets"
	"github.com/moonfruit/sing-router/internal/config"
	"github.com/moonfruit/sing-router/internal/daemon"
	"github.com/moonfruit/sing-router/internal/gitee"
	log "github.com/moonfruit/sing-router/internal/log"
	"github.com/moonfruit/sing-router/internal/shell"
	syncpkg "github.com/moonfruit/sing-router/internal/sync"
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
		Path:          logPath,
		MaxSize:       int64(cfg.Log.MaxSizeMB) * 1024 * 1024,
		MaxBackups:    cfg.Log.MaxBackups,
		Gzip:          true,
		FlushInterval: 500 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	stack := log.NewEmitterStack(log.StackConfig{
		Source:   "daemon",
		MinLevel: level,
		Writer:   writer,
	})
	defer func() { _ = stack.Close() }()
	em := stack.Emitter
	em.Info("supervisor", "supervisor.boot.started", "starting daemon at {Rundir}", map[string]any{"Rundir": rundir})

	// 把 var/zoo.raw.json（由 sync.Updater 拉取）预处理写入 config.d/zoo.json。
	// 缺失即跳过，保留种子默认；预处理失败仅警告，daemon 继续用上次成功的 zoo.json。
	if stats, err := config.PreprocessZooFile(rundir, cfg.Runtime.ConfigDir); err != nil {
		em.Warn("config", "config.zoo.preprocess.failed", "preprocess zoo: {Err}", map[string]any{"Err": err.Error()})
	} else if stats != nil {
		em.Info("config", "config.zoo.preprocess.ok",
			"zoo preprocessed: outbounds={OutboundCount} rule_sets={RuleSetCount} dropped={DroppedFields}",
			map[string]any{
				"OutboundCount": stats.OutboundCount,
				"RuleSetCount":  stats.RuleSetCount,
				"DroppedFields": stats.DroppedFields,
			})
	}

	// 静态 fragment（dns.json 等）引用了一些 rule_set tag 但不再自行声明；
	// 若 zoo.json 也没补足，本步用真实 gitee URL（含 token）写一个补充 fragment。
	// 没有 token 时跳过——daemon 下面会再次警告。
	if cfg.Gitee.Token != "" {
		gc := gitee.NewClient(cfg.Gitee)
		ref := cfg.Gitee.Zoo.Ref
		if ref == "" {
			ref = "main"
		}
		if added, err := config.EnsureRequiredRuleSets(rundir, cfg.Runtime.ConfigDir, gc.RawURL, ref, config.DefaultRequiredRuleSets); err != nil {
			em.Warn("config", "config.rule_sets.supplement.failed", "supplement rule_sets: {Err}", map[string]any{"Err": err.Error()})
		} else if len(added) > 0 {
			em.Info("config", "config.rule_sets.supplement.ok",
				"supplemented {Count} rule_set tags via gitee: {Tags}",
				map[string]any{"Count": len(added), "Tags": added})
		}
	}

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

	// Gitee 客户端（一份）：反向代理 handler 与 sync.Updater 共享同一实例。
	// 缺少 token 时跳过反向代理与后台同步——仍允许 daemon 起动，便于排障。
	var (
		giteeProxy http.Handler
		updater    *syncpkg.Updater
	)
	if cfg.Gitee.Token != "" {
		gc := gitee.NewClient(cfg.Gitee)
		gc.Retries = cfg.Download.HTTPRetries
		giteeProxy = gc.NewProxyHandler()
		updater = syncpkg.NewUpdater(cfg, rundir)
	} else {
		em.Warn("daemon", "gitee.disabled", "gitee.token empty; reverse proxy and background sync disabled", nil)
	}

	return daemon.Run(ctx, daemon.Options{
		Rundir:     rundir,
		Listen:     cfg.HTTP.Listen,
		Version:    version.String(),
		Emitter:    em,
		Bus:        stack.Bus,
		LogFile:    logPath,
		Supervisor: sup,
		GiteeProxy: giteeProxy,
		Updater:    updater,
		Sync: daemon.SyncLoopConfig{
			IntervalSec:     cfg.Sync.SyncIntervalSeconds(),
			OnStartDelaySec: cfg.Sync.SyncOnStartDelaySec(),
		},
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
		StatusExtra: buildStatusExtra(rundir, cfg.Runtime.ConfigDir, cfg.Install.Firmware),
		ScriptByName: func(name string) ([]byte, error) {
			path, ok := scriptMap[name]
			if !ok {
				return nil, fmt.Errorf("unknown script %q", name)
			}
			return assets.ReadFile(path)
		},
	})
}

// buildStatusExtra produces the StatusExtra hook injected into APIDeps.
// Returned map keys are merged at the top level of /api/v1/status.
func buildStatusExtra(rundir, configDir, firmwareKind string) func() map[string]any {
	if firmwareKind == "" {
		firmwareKind = "unknown"
	}
	return func() map[string]any {
		return map[string]any{
			"config": map[string]any{
				"config_dir": filepath.Join(rundir, configDir),
			},
			"firmware": firmwareKind,
		}
	}
}

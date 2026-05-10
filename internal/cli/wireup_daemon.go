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
	// EnsureRequiredRuleSets 一定会跑一次：
	//   - 有 token → 写 remote entry（真实 gitee URL，自动跟随服务器更新）
	//   - 无 token → 写 local entry，指向 install 阶段落到 var/rules/ 的内嵌兜底
	var rawURL config.RawURLFunc
	ref := cfg.Gitee.Zoo.Ref
	if ref == "" {
		ref = "main"
	}
	if cfg.Gitee.Token != "" {
		gc := gitee.NewClient(cfg.Gitee)
		rawURL = gc.RawURL
	}
	if added, err := config.EnsureRequiredRuleSets(rundir, cfg.Runtime.ConfigDir, rawURL, ref, config.DefaultRequiredRuleSets); err != nil {
		em.Warn("config", "config.rule_sets.supplement.failed", "supplement rule_sets: {Err}", map[string]any{"Err": err.Error()})
	} else if len(added) > 0 {
		mode := "local"
		if rawURL != nil {
			mode = "remote"
		}
		em.Info("config", "config.rule_sets.supplement.ok",
			"supplemented {Count} rule_set tags ({Mode}): {Tags}",
			map[string]any{"Count": len(added), "Mode": mode, "Tags": added})
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

	wiring := buildSupervisorWiring(cfg.Supervisor)

	sup := daemon.New(daemon.SupervisorConfig{
		Emitter:                 em,
		SingBoxBinary:           filepath.Join(rundir, cfg.Runtime.SingBoxBinary),
		SingBoxArgs:             []string{"run", "-D", rundir, "-C", cfg.Runtime.ConfigDir},
		SingBoxDir:              rundir,
		ReadyConfig:             wiring.Ready,
		BackoffMs:               wiring.BackoffMs,
		IptablesKeepBackoffLtMs: wiring.IptablesKeepBackoffLtMs,
		StopGrace:               wiring.StopGrace,
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

	// Gitee 客户端：sync.Updater 用。缺少 token 时跳过后台同步——仍允许 daemon
	// 起动，便于排障；rule_set 由 EnsureRequiredRuleSets 用嵌入兜底。
	var updater *syncpkg.Updater
	if cfg.Gitee.Token != "" {
		updater = syncpkg.NewUpdater(cfg, rundir)
	} else {
		em.Warn("daemon", "gitee.disabled", "gitee.token empty; background sync disabled", nil)
	}

	return daemon.Run(ctx, daemon.Options{
		Rundir:     rundir,
		Listen:     cfg.HTTP.Listen,
		Version:    version.String(),
		Emitter:    em,
		Bus:        stack.Bus,
		LogFile:    logPath,
		Supervisor: sup,
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

// supervisorWiring 是 [supervisor] 节 + routing 端口 ↦ daemon.SupervisorConfig
// 的子集。抽出来便于单测覆盖默认值与 toml 覆盖。
type supervisorWiring struct {
	Ready                   daemon.ReadyConfig
	BackoffMs               []int
	IptablesKeepBackoffLtMs int
	StopGrace               time.Duration
}

// readyCheckDialMixedPort 是 ready check 默认要 dial 的本机端口。
//
// 必须与 assets/config.d.default/inbounds.json 中 mixed-in 的 listen_port 保持一致；
// 改 inbounds.json 时同步改这里（或反过来）。
//
// 为什么不 dial dns-in (1053) / redirect-in (7892)：那两个 inbound 是 transparent
// 重定向类型（dns-in 是 type=direct，redirect-in 是 type=redirect），本机自连时
// SO_ORIGINAL_DST 取到的目标就是 sing-box 自己的 listen 地址，sing-box 会按 route
// 规则把这个连接当成"用户要去 127.0.0.1:port"，命中 ip_is_private→DIRECT，
// outbound 又去 dial 同一个端口被自己接住，无限自繁殖 → CPU 100%。mixed-in 是
// 真正的协议入站（HTTP/SOCKS），裸 TCP dial 不发握手会在握手前被关掉，根本不
// 进路由阶段，不会回环。
const readyCheckDialMixedPort = 7890

// buildSupervisorWiring 把 daemon.toml [supervisor] 节翻译成 supervisor 运行参数。
// 默认 ready check 总超时 60s（容纳 sing-box 冷启：cache-file 加载 + rule-set
// 下载 + router 启动），用户可在 daemon.toml 覆盖。
func buildSupervisorWiring(s config.SupervisorConfig) supervisorWiring {
	totalTimeout := 60 * time.Second
	if v := s.ReadyCheckTimeoutMs; v != nil && *v > 0 {
		totalTimeout = time.Duration(*v) * time.Millisecond
	}
	interval := 200 * time.Millisecond
	if v := s.ReadyCheckIntervalMs; v != nil && *v > 0 {
		interval = time.Duration(*v) * time.Millisecond
	}
	clashURL := "http://127.0.0.1:9999/version"
	if v := s.ReadyCheckClashAPI; v != nil && !*v {
		clashURL = ""
	}
	var dials []string
	if v := s.ReadyCheckDialInbounds; v == nil || *v {
		dials = []string{fmt.Sprintf("127.0.0.1:%d", readyCheckDialMixedPort)}
	}
	stopGrace := time.Duration(0)
	if v := s.StopGraceSeconds; v != nil && *v > 0 {
		stopGrace = time.Duration(*v) * time.Second
	}
	iptablesKeepBackoff := 0
	if v := s.IptablesKeepWhenBackoffLtMs; v != nil {
		iptablesKeepBackoff = *v
	}
	return supervisorWiring{
		Ready: daemon.ReadyConfig{
			TCPDials:     dials,
			ClashAPIURL:  clashURL,
			TotalTimeout: totalTimeout,
			Interval:     interval,
		},
		BackoffMs:               s.CrashPostReadyBackoffMs,
		IptablesKeepBackoffLtMs: iptablesKeepBackoff,
		StopGrace:               stopGrace,
	}
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

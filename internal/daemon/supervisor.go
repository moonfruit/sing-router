package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

// restartThrottleWindow 是 Restart 节流闸门：上次尝试 < 此值内的 Restart 直接 skip。
// 防止同一瞬间多个 caller（用户 + sync_loop + WAN 钩子）撞到一起反复重启 sing-box。
// Applier 失败 revert 走 RestartForce 绕过节流。
const restartThrottleWindow = 2 * time.Second

// SupervisorConfig 控制 supervisor 行为。
type SupervisorConfig struct {
	Emitter       *clef.Emitter
	SingBoxBinary string
	SingBoxArgs   []string
	SingBoxDir    string // 子进程 cwd

	ReadyConfig ReadyConfig

	StartupHook  func(context.Context) error // 在 ready 后跑 startup.sh
	TeardownHook func(context.Context) error // 在 Shutdown 时拆 iptables

	// RouteHealthy 巡检 device-bound 路由（`default dev <TUN> table <N>`）是否还在。
	// 外部 `kill -HUP <sing-box-pid>` 让 sing-box reload→TUN 重建→内核删路由，但
	// supervisor pid 没变看不到。WatchRoutes 周期调它，返回 false 触发一次 Restart
	// 走完整 Shutdown+Startup 把路由重装。为 nil 时 WatchRoutes 不启动。
	RouteHealthy func(context.Context) bool
	// RouteWatchInterval 是 WatchRoutes 的巡检周期；<=0 时取默认 30s。
	RouteWatchInterval time.Duration

	BackoffMs             []int // 崩溃恢复退避序列；最后一档为封顶
	StopGrace             time.Duration
	StateHookOnTransition func(from, to State)
}

// Supervisor 串行化 sing-box 子进程的全部生命周期事件。
type Supervisor struct {
	cfg SupervisorConfig
	sm  *StateMachine

	mu                sync.Mutex
	cmd               *exec.Cmd
	procCtx           context.Context // sing-box 子进程生命周期绑定的 ctx；Boot 时捕获（daemon 级）
	iptablesInstalled bool
	nextBackoffIdx    int
	restartCount      int
	bootAt            time.Time
	readyAt           time.Time
	childExited       chan struct{}
	lastRestartAt     time.Time
	restartInFlight   bool

	// opMu 串行化 Shutdown / Startup 各自的内部步骤，防止并发 caller 撞 state machine。
	opMu sync.Mutex
	// restartMu 串行化整次 Restart（Shutdown+Startup 复合操作），保证 throttle 判断正确
	// 且不会与单独的 Shutdown/Startup 调用交织。
	restartMu sync.Mutex
}

// New 构造 Supervisor。
func New(cfg SupervisorConfig) *Supervisor {
	if len(cfg.BackoffMs) == 0 {
		cfg.BackoffMs = []int{1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 512000, 600000}
	}
	if cfg.StopGrace == 0 {
		cfg.StopGrace = 5 * time.Second
	}
	return &Supervisor{cfg: cfg, sm: NewStateMachine()}
}

// State 返回当前 state。
func (s *Supervisor) State() State { return s.sm.Current() }

// SingBoxPID 返回当前子进程 pid，0 表示无。
func (s *Supervisor) SingBoxPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// IptablesInstalled 报告 iptables 是否已装。
func (s *Supervisor) IptablesInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.iptablesInstalled
}

// RestartCount 返回累计重启次数。
func (s *Supervisor) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartCount
}

// RestartInFlight 报告当前是否正在执行 Restart（Shutdown→Startup 的复合操作中）。
// 给 /status 暴露的观测点，与 state machine 解耦。
func (s *Supervisor) RestartInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartInFlight
}

// Boot 是 daemon 首次启动入口：捕获 daemon 级 ctx 作为 sing-box 子进程的生命周期
// 锚点（后续 Startup / Restart 都复用），然后调 Startup 完整跑一次。
func (s *Supervisor) Boot(ctx context.Context) error {
	s.mu.Lock()
	s.bootAt = time.Now()
	// procCtx 是 daemon 级 ctx。后续 Startup 经 HTTP 触发时拿到的是请求级 ctx，
	// 绝不能把它喂给 exec.CommandContext —— 否则 HTTP 请求一结束子进程被 SIGKILL。
	s.procCtx = ctx
	s.mu.Unlock()
	return s.Startup(ctx)
}

// Startup：启 sing-box → 等 Ready → 跑 StartupHook → state=running。
// 失败任何一步进 Fatal 并返回 error；调用方可自行决定是否走 Shutdown 回收。
func (s *Supervisor) Startup(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	start := time.Now()
	if err := s.sm.Transition(StateBooting); err != nil {
		return err
	}
	if err := s.startSingBox(ctx); err != nil {
		s.toFatal()
		return err
	}
	if err := ReadyCheck(ctx, s.cfg.ReadyConfig); err != nil {
		s.killChild()
		s.toFatal()
		return fmt.Errorf("ready check: %w", err)
	}
	s.mu.Lock()
	s.readyAt = time.Now()
	s.mu.Unlock()

	if s.cfg.StartupHook != nil {
		if err := s.cfg.StartupHook(ctx); err != nil {
			s.killChild()
			s.toFatal()
			return fmt.Errorf("startup hook: %w", err)
		}
	}
	s.mu.Lock()
	s.iptablesInstalled = true
	s.mu.Unlock()
	s.transitionTo(StateRunning)
	if s.cfg.Emitter != nil {
		s.cfg.Emitter.Info("supervisor", "supervisor.startup.ok",
			"startup completed in {DurationMs}ms",
			map[string]any{"DurationMs": time.Since(start).Milliseconds()})
	}
	return nil
}

// Shutdown：拆 iptables + 停 sing-box → state=stopped。幂等：当前已 Stopping/Stopped
// 时仍 best-effort 跑 teardown + killChild。失败仅 warn 不中断流程。
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	start := time.Now()
	cur := s.sm.Current()
	if cur != StateStopping && cur != StateStopped {
		// best-effort: 已是 Fatal 等转移失败也继续拆 + kill
		_ = s.sm.Transition(StateStopping)
	}
	if s.cfg.TeardownHook != nil {
		if err := s.cfg.TeardownHook(ctx); err != nil && s.cfg.Emitter != nil {
			s.cfg.Emitter.Warn("supervisor", "supervisor.teardown.failed",
				"teardown failed (continuing shutdown): {Err}",
				map[string]any{"Err": err.Error()})
		}
	}
	s.killChild()
	s.mu.Lock()
	s.iptablesInstalled = false
	s.mu.Unlock()
	if s.sm.Current() != StateStopped {
		_ = s.sm.Transition(StateStopped)
	}
	if s.cfg.Emitter != nil {
		s.cfg.Emitter.Info("supervisor", "supervisor.shutdown.ok",
			"shutdown completed in {DurationMs}ms",
			map[string]any{"DurationMs": time.Since(start).Milliseconds()})
	}
	return nil
}

// ErrRestartThrottled 在 Restart 命中 2s 节流闸门时返回，表示本次 Restart 被跳过，
// sing-box 进程与 iptables 仍是上次操作后的状态。**调用方必须区分对待**：
//   - 必须真生效的路径（Applier 成功路径 / 失败 revert / 固件钩子刚拆完 iptables 等）
//     应改走 RestartForce 或对外报告（如 HTTP 429）让上层重试，绝不可当 nil 误以为已重启。
//   - 用户/外部脚本重复点击场景可以容忍此返回（throttle 的正常意图）。
var ErrRestartThrottled = errors.New("restart throttled")

// Restart：Shutdown + Startup 连续调用。受 2s 节流闸门保护：上次尝试 <2s 内的
// 调用返回 ErrRestartThrottled 让调用方决定如何处理。必须生效的路径请用 RestartForce。
func (s *Supervisor) Restart(ctx context.Context) error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	s.mu.Lock()
	since := time.Since(s.lastRestartAt)
	hasPrior := !s.lastRestartAt.IsZero()
	s.mu.Unlock()
	if hasPrior && since < restartThrottleWindow {
		if s.cfg.Emitter != nil {
			s.cfg.Emitter.Info("supervisor", "supervisor.restart.throttled",
				"restart throttled (last attempt {SinceMs}ms ago, window {WindowMs}ms)",
				map[string]any{
					"SinceMs":  since.Milliseconds(),
					"WindowMs": restartThrottleWindow.Milliseconds(),
				})
		}
		return ErrRestartThrottled
	}
	return s.doRestart(ctx)
}

// RestartForce 等价 Restart 但绕过 throttle。仅供 Applier 失败 revert 后调用，
// 保证「revert → 拉回旧配置」不会被节流卡住。不要从 HTTP/CLI 暴露。
func (s *Supervisor) RestartForce(ctx context.Context) error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.doRestart(ctx)
}

func (s *Supervisor) doRestart(ctx context.Context) error {
	start := time.Now()
	s.mu.Lock()
	s.restartInFlight = true
	s.restartCount++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.restartInFlight = false
		s.lastRestartAt = time.Now()
		s.mu.Unlock()
	}()

	_ = s.Shutdown(ctx)
	if err := s.Startup(ctx); err != nil {
		return err
	}
	if s.cfg.Emitter != nil {
		s.cfg.Emitter.Info("supervisor", "supervisor.restart.ok",
			"restart completed in {DurationMs}ms",
			map[string]any{"DurationMs": time.Since(start).Milliseconds()})
	}
	return nil
}

// Start 是 CLI/HTTP /start 的入口，等价 Startup。
func (s *Supervisor) Start(ctx context.Context) error { return s.Startup(ctx) }

// Stop 是 CLI/HTTP /stop 的入口，等价 Shutdown。
func (s *Supervisor) Stop(ctx context.Context) error { return s.Shutdown(ctx) }

func (s *Supervisor) startSingBox(ctx context.Context) error {
	// 子进程生命周期绑 daemon 级 procCtx，不绑调用方（可能是 HTTP 请求级）ctx。
	// procCtx 由 Boot 捕获；若 Boot 未跑过（测试可能直接构造后调 Startup），
	// 退化用调用方 ctx。
	s.mu.Lock()
	procCtx := s.procCtx
	s.mu.Unlock()
	if procCtx == nil {
		procCtx = ctx
	}
	cmd := exec.CommandContext(procCtx, s.cfg.SingBoxBinary, s.cfg.SingBoxArgs...)
	cmd.Dir = s.cfg.SingBoxDir
	// 把 sing-box 隔离到独立进程组：daemon 作为它的"信号防火墙"。
	// 否则 daemon 被 `sing-router daemon &` 启动后，daemon 是 shell job 进程组的
	// leader，sing-box 继承 daemon 的 pgid。当 shell 退出（或 sshd disconnect）
	// 发 `kill -HUP -<pgid>` 时 sing-box 也会收到 SIGHUP，触发 sing-box 内部
	// reload→TUN inbound 重建→旧 utun fd 关闭→内核自动删除 `default dev utun
	// table 7892`，但 supervisor 完全不知情（sing-box pid 没变），路由不会重装。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pr, pw := io.Pipe()
	cmd.Stderr = pw
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("start: %w", err)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.childExited = make(chan struct{})
	s.mu.Unlock()

	// stderr → CLEF
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("supervisor.consumeStderr", r)
				if s.cfg.Emitter != nil {
					s.cfg.Emitter.Fatal("recover", "panic.recovered",
						"panic in {Name}: see sing-router.err for stack",
						map[string]any{"Name": "supervisor.consumeStderr"})
				}
			}
		}()
		s.consumeStderr(pr)
	}()

	// wait goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				reportPanic("supervisor.reaper", r)
			}
		}()
		_ = cmd.Wait()
		_ = pw.Close()
		close(s.childExitedCh())
	}()
	return nil
}

func (s *Supervisor) childExitedCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childExited
}

func (s *Supervisor) consumeStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		ev := clef.ParseSingBoxLine(sc.Text())
		if ev == nil {
			continue
		}
		if s.cfg.Emitter != nil {
			s.cfg.Emitter.PublishExternal(ev)
		}
	}
}

func (s *Supervisor) killChild() {
	s.mu.Lock()
	cmd := s.cmd
	grace := s.cfg.StopGrace
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.childExitedCh():
	case <-time.After(grace):
		_ = cmd.Process.Signal(syscall.SIGKILL)
		<-s.childExitedCh()
	}
}

func (s *Supervisor) transitionTo(to State) {
	from := s.sm.Current()
	if err := s.sm.Transition(to); err != nil {
		return
	}
	if s.cfg.StateHookOnTransition != nil {
		s.cfg.StateHookOnTransition(from, to)
	}
}

func (s *Supervisor) toFatal() {
	_ = s.sm.Transition(StateFatal)
}

// ErrShutdownRequested 由 Run 在 ctx 取消时返回。
var ErrShutdownRequested = errors.New("shutdown requested")

// Run 监控 sing-box 子进程；崩溃后立即 Shutdown，退避，再 Startup。
// ctx 取消时返回 ErrShutdownRequested。
func (s *Supervisor) Run(ctx context.Context) error {
	for {
		// 快照「当前监控的子进程」+「它的退出 channel」。两者必须成对取，
		// 这样下面才能判断醒来时子进程是否已被 Restart/Startup 换成了新的。
		s.mu.Lock()
		ch := s.childExited
		watched := s.cmd
		s.mu.Unlock()

		// 等子进程退出
		select {
		case <-ctx.Done():
			return ErrShutdownRequested
		case <-ch:
		}

		// ch 关闭：watched 这个子进程退出了。需要区分「真崩溃」与
		// 「Restart / Shutdown / Stop 故意杀的」—— 否则 Restart 一来
		// killChild 关闭旧 channel，Run 看 state 不对就 return，监控循环死，
		// 新子进程从此裸奔。
		s.mu.Lock()
		replaced := s.cmd != watched
		inFlight := s.restartInFlight
		s.mu.Unlock()
		if replaced {
			// 子进程已被 Startup 换成新的 → 这次退出是预期的；回到循环监控新子进程。
			continue
		}
		if inFlight {
			// 正在 Restart 但还没换上新子进程（位于 Shutdown 与 Startup 之间）。
			// 短等再轮询，避免对着已关闭的旧 channel 空转。
			select {
			case <-ctx.Done():
				return ErrShutdownRequested
			case <-time.After(20 * time.Millisecond):
			}
			continue
		}
		switch s.sm.Current() {
		case StateRunning:
			// 真崩溃 → 下方走 Degraded + Shutdown + 退避 + Startup
		default:
			// Stopping / Stopped / Fatal / Booting / Degraded：子进程退出是预期的，
			// 或由其它 caller 接管，干净退出循环。
			return nil
		}
		if err := s.sm.Transition(StateDegraded); err != nil {
			return err
		}
		backoffMs := s.cfg.BackoffMs[min(s.nextBackoffIdx, len(s.cfg.BackoffMs)-1)]
		s.nextBackoffIdx++
		if s.cfg.Emitter != nil {
			s.cfg.Emitter.Warn("supervisor", "supervisor.child.crashed",
				"sing-box crashed (crash #{CrashCount} this storm); backing off {BackoffMs}ms before restart",
				map[string]any{"CrashCount": s.nextBackoffIdx, "BackoffMs": backoffMs})
		}
		// 立即拆 iptables：退避期间系统进入 DIRECT，避免「sing-box 死但 iptables
		// 仍 REDIRECT 到 7892」的连接黑洞。
		_ = s.Shutdown(ctx)
		select {
		case <-ctx.Done():
			return ErrShutdownRequested
		case <-time.After(time.Duration(backoffMs) * time.Millisecond):
		}
		if err := s.Startup(ctx); err != nil {
			// 失败 → state=Fatal，下一次循环顶部会从 Fatal 走 default → return nil。
			if s.cfg.Emitter != nil {
				s.cfg.Emitter.Error("supervisor", "supervisor.crash.unrecovered",
					"sing-box restart after crash failed (crash #{CrashCount} this storm); giving up: {Err}",
					map[string]any{"CrashCount": s.nextBackoffIdx, "Err": err.Error()})
			}
			continue
		}
		if s.cfg.Emitter != nil {
			s.cfg.Emitter.Info("supervisor", "supervisor.recovered",
				"sing-box recovered after {CrashCount} crash(es) this storm",
				map[string]any{"CrashCount": s.nextBackoffIdx})
		}
		s.nextBackoffIdx = 0
	}
}

// WatchRoutes 周期巡检 device-bound 路由，缺失即调 Restart 走完整循环重装。
// 兜底外部 `kill -HUP <sing-box-pid>` 触发的 sing-box reload→TUN 重建→
// 内核自动删除路由场景（supervisor pid 没变看不到）。
// RouteHealthy 为 nil 时直接返回（watcher 禁用）。
// 阻塞运行，ctx 取消即返回；只在 StateRunning 下巡检。
func (s *Supervisor) WatchRoutes(ctx context.Context) {
	if s.cfg.RouteHealthy == nil {
		return
	}
	interval := s.cfg.RouteWatchInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.sm.Current() != StateRunning {
			continue
		}
		if s.cfg.RouteHealthy(ctx) {
			continue
		}
		if s.cfg.Emitter != nil {
			s.cfg.Emitter.Warn("supervisor", "supervisor.route.missing",
				"device-bound route gone (likely external SIGHUP→sing-box reload); restarting", nil)
		}
		_ = s.Restart(ctx)
	}
}

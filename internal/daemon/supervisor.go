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

// SupervisorConfig 控制 supervisor 行为。
type SupervisorConfig struct {
	Emitter       *clef.Emitter
	SingBoxBinary string
	SingBoxArgs   []string
	SingBoxDir    string // 子进程 cwd

	ReadyConfig ReadyConfig

	StartupHook  func(context.Context) error // 在 ready 后跑 startup.sh
	TeardownHook func(context.Context) error // 在 stop/shutdown 时拆 iptables
	// ReapplyRoutesHook 仅装与 TUN 设备绑定的路由（default route + fwmark rule），
	// 不动 iptables / ipset。bootStep 走 skipStartupIfInstalled 分支（Restart /
	// RecoverFromFailedApply / Run 快速崩溃恢复）时，sing-box 杀掉重起会让
	// 旧 utun fd 关闭→内核自动删除 `default dev utun table 7890`。startup.sh
	// 不重跑就没人再装这条路由，所以 supervisor 在这条分支上必须主动调它。
	// 为 nil 时该分支退化为旧行为（路由会丢），调用方应在 wireup 时注入。
	ReapplyRoutesHook func(context.Context) error

	// RouteHealthy 巡检 device-bound 路由（`default dev <TUN> table <N>`）是否还在。
	// Setpgid 隔离了 shell 退出的 SIGHUP，但用户/外部脚本仍可直接
	// `kill -HUP <sing-box-pid>` 让 sing-box reload→TUN 重建→内核删路由，而
	// supervisor 全程不知情（pid 没变、子进程没退）。WatchRoutes 周期性调它兜底，
	// 返回 false 即调一次 ReapplyRoutesHook 补回。为 nil 时 WatchRoutes 直接退出。
	RouteHealthy func(context.Context) bool
	// RouteWatchInterval 是 WatchRoutes 的巡检周期；<=0 时取默认 30s。
	RouteWatchInterval time.Duration

	BackoffMs               []int // 崩溃恢复退避序列；最后一档为封顶
	IptablesKeepBackoffLtMs int   // < 此阈值时保持 iptables；>= 时拆
	StopGrace               time.Duration
	StateHookOnTransition   func(from, to State)
}

// Supervisor 串行化 sing-box 子进程的全部生命周期事件。
type Supervisor struct {
	cfg SupervisorConfig
	sm  *StateMachine

	mu                sync.Mutex
	cmd               *exec.Cmd
	iptablesInstalled bool
	nextBackoffIdx    int
	restartCount      int
	bootAt            time.Time
	readyAt           time.Time
	childExited       chan struct{}
}

// New 构造 Supervisor。
func New(cfg SupervisorConfig) *Supervisor {
	if len(cfg.BackoffMs) == 0 {
		cfg.BackoffMs = []int{1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 512000, 600000}
	}
	if cfg.IptablesKeepBackoffLtMs == 0 {
		cfg.IptablesKeepBackoffLtMs = 10000
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

// Boot 启动 sing-box → ready → 跑 StartupHook → state=running。
// 失败时进入 fatal。
func (s *Supervisor) Boot(ctx context.Context) error {
	s.mu.Lock()
	s.bootAt = time.Now()
	s.mu.Unlock()
	return s.bootStep(ctx, false /*runHookEvenIfInstalled*/)
}

func (s *Supervisor) bootStep(ctx context.Context, skipStartupIfInstalled bool) error {
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
	needHook := !(skipStartupIfInstalled && s.iptablesInstalled)
	s.mu.Unlock()

	if needHook && s.cfg.StartupHook != nil {
		if err := s.cfg.StartupHook(ctx); err != nil {
			s.toFatal()
			return fmt.Errorf("startup hook: %w", err)
		}
		s.mu.Lock()
		s.iptablesInstalled = true
		s.mu.Unlock()
	} else if !needHook && s.cfg.ReapplyRoutesHook != nil {
		// startup.sh 不重跑，但旧 utun fd 被 killChild 关掉时内核已经清掉
		// device-bound 路由 → 必须主动重装。失败仅 best-effort，不进入 fatal
		// （让 supervisor 继续运行，doctor / 用户手动 reapply-rules 仍可救场）。
		_ = s.cfg.ReapplyRoutesHook(ctx)
	}
	s.transitionTo(StateRunning)
	return nil
}

func (s *Supervisor) startSingBox(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, s.cfg.SingBoxBinary, s.cfg.SingBoxArgs...)
	cmd.Dir = s.cfg.SingBoxDir
	// 把 sing-box 隔离到独立进程组：daemon 作为它的"信号防火墙"。
	// 否则 daemon 被 `sing-router daemon &` 启动后，daemon 是 shell job 进程组的
	// leader，sing-box 继承 daemon 的 pgid。当 shell 退出（或 sshd disconnect）
	// 发 `kill -HUP -<pgid>` 时 sing-box 也会收到 SIGHUP，触发 sing-box 内部
	// reload→TUN inbound 重建→旧 utun fd 关闭→内核自动删除 `default dev utun
	// table 7890`，但 supervisor 完全不知情（sing-box pid 没变），路由不会重装。
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
						"panic in {Name}: see stderr.log for stack",
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

// Restart 走 reloading 路径。用户主动 → 不拆 iptables。
func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.sm.Transition(StateReloading); err != nil {
		return err
	}
	s.killChild()
	s.mu.Lock()
	s.restartCount++
	s.mu.Unlock()
	return s.bootStep(ctx, true /*skipStartupIfInstalled = iptables 已装时跳过*/)
}

// RecoverFromFailedApply 用于 daemon.Applier 在 auto-apply 失败 revert 旧文件
// 之后,把 sing-box 重新拉起来。等价于"按 revert 后的(旧)config 再来一次 Restart",
// 但允许从 Fatal/Reloading 入口(因为 Restart 失败后 bootStep 内部已经 toFatal)。
//
// 不要从 HTTP API 调:Fatal → Reloading 的转移仅为这条恢复路径开放,普通代码路径
// 仍应把 Fatal 当作终态处理。iptables 还在(Restart 路径不拆),所以走 skip-startup
// 分支,不会重跑 startup.sh / 触动 ipset。
func (s *Supervisor) RecoverFromFailedApply(ctx context.Context) error {
	if err := s.sm.Transition(StateReloading); err != nil {
		return err
	}
	s.killChild()
	s.mu.Lock()
	s.restartCount++
	s.mu.Unlock()
	return s.bootStep(ctx, true /*skipStartupIfInstalled*/)
}

// Stop 拆 iptables + 停 sing-box；进入 stopped。
func (s *Supervisor) Stop(ctx context.Context) error {
	if err := s.sm.Transition(StateStopping); err != nil {
		return err
	}
	if s.cfg.TeardownHook != nil {
		_ = s.cfg.TeardownHook(ctx)
	}
	s.mu.Lock()
	s.iptablesInstalled = false
	s.mu.Unlock()
	s.killChild()
	return s.sm.Transition(StateStopped)
}

// Start 从 stopped 恢复。
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.sm.Transition(StateBooting); err != nil {
		return err
	}
	return s.bootStep(ctx, false)
}

// Shutdown 拆 iptables + 停 sing-box；不维护 stopped 态（最后退出 daemon 进程）。
func (s *Supervisor) Shutdown(ctx context.Context) error {
	if cur := s.sm.Current(); cur != StateStopping {
		if err := s.sm.Transition(StateStopping); err != nil {
			// 已是 fatal/stopped 等终止性状态，仍尝试 best-effort 拆 + kill
			_ = err
		}
	}
	if s.cfg.TeardownHook != nil {
		_ = s.cfg.TeardownHook(ctx)
	}
	s.mu.Lock()
	s.iptablesInstalled = false
	s.mu.Unlock()
	s.killChild()
	return nil
}

// 反向恢复（degraded → running）的退避循环由 Run() 跑；测试中主要测 Boot。
// Run 是阻塞的；ctx 取消时返回。
var ErrShutdownRequested = errors.New("shutdown requested")

func (s *Supervisor) Run(ctx context.Context) error {
	for {
		// 等子进程退出
		select {
		case <-ctx.Done():
			return ErrShutdownRequested
		case <-s.childExitedCh():
		}
		// running 状态下子进程退出 → 进 degraded
		if s.sm.Current() != StateRunning {
			return nil
		}
		if err := s.sm.Transition(StateDegraded); err != nil {
			return err
		}
		backoffMs := s.cfg.BackoffMs[min(s.nextBackoffIdx, len(s.cfg.BackoffMs)-1)]
		s.nextBackoffIdx++
		if backoffMs >= s.cfg.IptablesKeepBackoffLtMs && s.cfg.TeardownHook != nil {
			_ = s.cfg.TeardownHook(ctx)
			s.mu.Lock()
			s.iptablesInstalled = false
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ErrShutdownRequested
		case <-time.After(time.Duration(backoffMs) * time.Millisecond):
		}
		if err := s.bootStep(ctx, true /*skip startup if iptables_installed*/); err != nil {
			// Stay in degraded; loop continues to wait for child exit again
			continue
		}
		s.nextBackoffIdx = 0
	}
}

// WatchRoutes 周期性巡检 device-bound 路由，发现丢失就调 ReapplyRoutesHook 补回。
// 这是对外部 `kill -HUP <sing-box-pid>` 的兜底：sing-box 收到 SIGHUP 会 reload→
// TUN inbound 重建→旧 utun fd 关闭→内核自动删 `default dev utun table 7890`，但
// sing-box pid 没变、子进程没退，supervisor 的状态机完全观察不到这次"换 utun"。
// RouteHealthy 或 ReapplyRoutesHook 为 nil 时直接返回（watcher 禁用）。
// 阻塞运行，ctx 取消即返回；只在 StateRunning 下巡检（其余状态由 bootStep 兜底）。
func (s *Supervisor) WatchRoutes(ctx context.Context) {
	if s.cfg.RouteHealthy == nil || s.cfg.ReapplyRoutesHook == nil {
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
				"device-bound route gone (likely external SIGHUP→sing-box reload); reapplying", nil)
		}
		_ = s.cfg.ReapplyRoutesHook(ctx)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

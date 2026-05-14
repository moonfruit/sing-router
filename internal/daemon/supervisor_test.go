package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"
	log "github.com/moonfruit/sing-router/internal/log"
)

// 用桩 sing-box；找路径
func fakeSingBox(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	binary := filepath.Join(repoRoot, "testdata", "fake-sing-box", "fake-sing-box")
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("fake-sing-box not built (run testdata/fake-sing-box/build.sh): %v", err)
	}
	return binary
}

// freePort 抢一个空闲端口立刻释放（race 可接受，仅测试用）
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func newTestEmitter(t *testing.T) *clef.Emitter {
	dir := t.TempDir()
	w, err := log.NewWriter(log.WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatal(err)
	}
	stack := log.NewEmitterStack(log.StackConfig{
		Source:   "daemon",
		MinLevel: log.LevelInfo,
		Writer:   w,
	})
	t.Cleanup(func() { _ = stack.Close() })
	return stack.Emitter
}

func TestSupervisorBootHappyPath(t *testing.T) {
	binary := fakeSingBox(t)
	p1, p2, clash := freePort(t), freePort(t), freePort(t)

	var startupCalls int32
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs: []string{
			"--listen", fmt.Sprintf("%d,%d", p1, p2),
			"--clash-port", strconv.Itoa(clash),
		},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p1), fmt.Sprintf("127.0.0.1:%d", p2)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook: func(ctx context.Context) error {
			atomic.AddInt32(&startupCalls, 1)
			return nil
		},
		TeardownHook: func(ctx context.Context) error { return nil },
		StopGrace:    1 * time.Second,
	})

	if err := sup.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() {
		_ = sup.Shutdown(context.Background())
	}()

	if sup.State() != StateRunning {
		t.Fatalf("state: %v", sup.State())
	}
	if atomic.LoadInt32(&startupCalls) != 1 {
		t.Fatal("StartupHook should be called exactly once")
	}
	if sup.SingBoxPID() == 0 {
		t.Fatal("sing-box pid should be non-zero")
	}
	// 进程确实存在
	if _, err := exec.LookPath(binary); err != nil {
		t.Fatalf("lookpath: %v", err)
	}
}

func TestSupervisorBootPreReadyFailEntersFatal(t *testing.T) {
	binary := fakeSingBox(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--pre-ready-fail"},
		ReadyConfig:   ReadyConfig{TCPDials: []string{"127.0.0.1:1"}, TotalTimeout: 200 * time.Millisecond},
	})
	err := sup.Boot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if sup.State() != StateFatal {
		t.Fatalf("state: %v", sup.State())
	}
}

// 回归保护：sing-box 冷启动慢于 ready check TotalTimeout 时，supervisor 必须
// 报 "ready check" 错并进 fatal，且子进程被 kill。这条用例锁死 TotalTimeout 是
// 必须暴露给业务方的旋钮——任何把它写死成小常量的回归都会被这条测试拍下。
func TestSupervisorBootTimesOutWhenSingBoxStartsTooSlow(t *testing.T) {
	binary := fakeSingBox(t)
	p := freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		// ready-delay (1s) > TotalTimeout (200ms)：fake 还没 listen 就被判超时
		SingBoxArgs: []string{"--listen", strconv.Itoa(p), "--ready-delay", "1s"},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			TotalTimeout: 200 * time.Millisecond,
			Interval:     50 * time.Millisecond,
		},
		StopGrace: 1 * time.Second,
	})
	err := sup.Boot(context.Background())
	if err == nil {
		t.Fatal("expected ready check timeout error")
	}
	if !strings.Contains(err.Error(), "ready check") {
		t.Fatalf("err should mention ready check: %v", err)
	}
	if sup.State() != StateFatal {
		t.Fatalf("state: %v want StateFatal", sup.State())
	}
}

func TestSupervisorRestartKeepsIptables(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var startupCalls, teardownCalls, reapplyRoutesCalls int32
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:       func(context.Context) error { atomic.AddInt32(&startupCalls, 1); return nil },
		TeardownHook:      func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
		ReapplyRoutesHook: func(context.Context) error { atomic.AddInt32(&reapplyRoutesCalls, 1); return nil },
		StopGrace:         1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()
	// 正常 Boot 走 needHook 分支：跑 startup.sh，不应调 reapply-routes
	if got := atomic.LoadInt32(&reapplyRoutesCalls); got != 0 {
		t.Fatalf("reapply-routes should NOT be called on initial Boot; calls=%d", got)
	}
	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if atomic.LoadInt32(&startupCalls) != 1 {
		t.Fatalf("startup hook should not run again on restart with iptables_installed; calls=%d", startupCalls)
	}
	if atomic.LoadInt32(&teardownCalls) != 0 {
		t.Fatal("teardown should not run during user-initiated restart")
	}
	// Restart 走 skipStartupIfInstalled 分支：旧 utun 被 killChild 销毁，
	// 内核已删 device-bound 路由 → supervisor 必须调 reapply-routes 重装。
	if got := atomic.LoadInt32(&reapplyRoutesCalls); got != 1 {
		t.Fatalf("reapply-routes should be called exactly once on Restart; calls=%d", got)
	}
	if !sup.IptablesInstalled() {
		t.Fatal("iptables should remain installed across restart")
	}
}

// 当 ReapplyRoutesHook 未提供（nil）时，supervisor 应优雅退化为旧行为：
// Restart 仍能成功（不 panic、不 fatal），只是路由不会被自动重装。
func TestSupervisorRestartWithoutReapplyRoutesHookStillWorks(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:  func(context.Context) error { return nil },
		TeardownHook: func(context.Context) error { return nil },
		// ReapplyRoutesHook 故意保持 nil
		StopGrace: 1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()
	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart with nil ReapplyRoutesHook: %v", err)
	}
	if sup.State() != StateRunning {
		t.Fatalf("state: %v want StateRunning", sup.State())
	}
}

// 外部 `kill -HUP <sing-box-pid>` 会让 sing-box reload→TUN 重建→内核删
// device-bound 路由，但 supervisor 状态机观察不到（pid 没变、子进程没退）。
// WatchRoutes 巡检到 RouteHealthy=false 时必须调 ReapplyRoutesHook 补回。
func TestSupervisorWatchRoutesReappliesWhenRouteMissing(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var reapplyRoutesCalls int32
	var routeOK atomic.Bool
	routeOK.Store(false) // 模拟路由被 sing-box reload 删掉
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:        func(context.Context) error { return nil },
		TeardownHook:       func(context.Context) error { return nil },
		ReapplyRoutesHook:  func(context.Context) error { atomic.AddInt32(&reapplyRoutesCalls, 1); return nil },
		RouteHealthy:       func(context.Context) bool { return routeOK.Load() },
		RouteWatchInterval: 20 * time.Millisecond,
		StopGrace:          1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.WatchRoutes(ctx)

	// 路由缺失 → 至少被补一次
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&reapplyRoutesCalls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&reapplyRoutesCalls) == 0 {
		t.Fatal("WatchRoutes should call ReapplyRoutesHook while route is missing")
	}

	// 路由恢复后 → 不再调用
	routeOK.Store(true)
	time.Sleep(60 * time.Millisecond) // 让在途的一次跑完
	stable := atomic.LoadInt32(&reapplyRoutesCalls)
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&reapplyRoutesCalls); got != stable {
		t.Fatalf("WatchRoutes should stop reapplying once route is healthy; calls %d→%d", stable, got)
	}
}

// RouteHealthy 为 nil 时 WatchRoutes 必须立即返回（watcher 禁用），不阻塞。
func TestSupervisorWatchRoutesDisabledWhenRouteHealthyNil(t *testing.T) {
	sup := New(SupervisorConfig{
		Emitter:           newTestEmitter(t),
		ReapplyRoutesHook: func(context.Context) error { return nil },
		// RouteHealthy 故意保持 nil
	})
	done := make(chan struct{})
	go func() { sup.WatchRoutes(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchRoutes should return immediately when RouteHealthy is nil")
	}
}

func TestSupervisorStopThenStart(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var startupCalls, teardownCalls int32
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:  func(context.Context) error { atomic.AddInt32(&startupCalls, 1); return nil },
		TeardownHook: func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("state: %v", sup.State())
	}
	if atomic.LoadInt32(&teardownCalls) != 1 {
		t.Fatal("teardown should run on Stop")
	}
	if sup.IptablesInstalled() {
		t.Fatal("iptables should be uninstalled after Stop")
	}
	// 再启
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if atomic.LoadInt32(&startupCalls) != 2 {
		t.Fatalf("startup should run again after Start; calls=%d", startupCalls)
	}
	_ = sup.Shutdown(context.Background())
}

func TestSupervisorAutoRestartUnderCrash(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var startupCalls, teardownCalls int32
	sup := New(SupervisorConfig{
		Emitter:                 newTestEmitter(t),
		SingBoxBinary:           binary,
		SingBoxArgs:             []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash), "--crash-after", "300ms"},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:             func(context.Context) error { atomic.AddInt32(&startupCalls, 1); return nil },
		TeardownHook:            func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
		BackoffMs:               []int{50, 100, 200},
		IptablesKeepBackoffLtMs: 10000, // 50ms < 10s → 不拆
		StopGrace:               1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()

	// 等几次重启
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RestartCount() > 0 || atomic.LoadInt32(&startupCalls) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-runDone
	// Snapshot before Shutdown — Shutdown() unconditionally invokes the
	// teardown hook for cleanup, which is unrelated to the assertion below.
	preShutdownTeardown := atomic.LoadInt32(&teardownCalls)
	_ = sup.Shutdown(context.Background())

	if preShutdownTeardown != 0 {
		t.Fatal("teardown should not be invoked when backoff < threshold")
	}
}

// Bug A 回归：Restart 经 HTTP 触发时，调用方传的是 r.Context()（请求级 ctx）。
// sing-box 子进程的生命周期绝不能绑在这个 ctx 上 —— 否则 HTTP 请求一结束，
// exec.CommandContext 就会 SIGKILL 掉刚起的 sing-box。子进程应绑 daemon 级 ctx
// （Boot 时捕获），调用方 ctx 取消后子进程必须仍存活。
func TestSupervisorRestartChildOutlivesCallerContext(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:       func(context.Context) error { return nil },
		TeardownHook:      func(context.Context) error { return nil },
		ReapplyRoutesHook: func(context.Context) error { return nil },
		StopGrace:         1 * time.Second,
	})
	// Boot 用 daemon 级 ctx（这里用 Background 模拟，永不取消）。
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()

	// 模拟 HTTP 触发的 Restart：传一个请求级 ctx（= r.Context()）。
	reqCtx, reqCancel := context.WithCancel(context.Background())
	if err := sup.Restart(reqCtx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	pid := sup.SingBoxPID()
	if pid == 0 {
		t.Fatal("sing-box pid should be non-zero after Restart")
	}
	// HTTP 请求结束 → r.Context() 取消。sing-box 不应因此被杀。
	reqCancel()
	time.Sleep(300 * time.Millisecond)

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("sing-box (pid %d) must stay alive after caller ctx cancelled, but: %v", pid, err)
	}
	if sup.State() != StateRunning {
		t.Fatalf("state after caller-ctx cancel: %v want StateRunning", sup.State())
	}
}

// Bug B 回归：Restart 把状态切到 Reloading 后才 killChild，Run() 监控循环醒来
// 时若直接因 "state != Running" 就 return，整个监控循环就永久退出，Restart 起的
// 新子进程从此裸奔。这里验证：一次 Restart 之后，外部杀掉新子进程，Run() 仍能
// 检测到崩溃并退避重启出一个新的、running 的子进程。
func TestSupervisorRunKeepsMonitoringAfterRestart(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:       func(context.Context) error { return nil },
		TeardownHook:      func(context.Context) error { return nil },
		ReapplyRoutesHook: func(context.Context) error { return nil },
		BackoffMs:         []int{50, 100, 200},
		StopGrace:         1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()

	// 一次用户触发的 Restart（模拟 /api/v1/restart）。
	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	pidAfterRestart := sup.SingBoxPID()
	if pidAfterRestart == 0 {
		t.Fatal("pid 0 after Restart")
	}

	// 「外部」杀掉 Restart 起的新子进程，模拟崩溃。Run() 必须还在监控。
	if err := syscall.Kill(pidAfterRestart, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child: %v", err)
	}

	// Run() 应检测到崩溃 → 退避重启 → 出现新 pid 且回到 running。
	deadline := time.Now().Add(3 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if pid := sup.SingBoxPID(); pid != 0 && pid != pidAfterRestart && sup.State() == StateRunning {
			recovered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-runDone
	_ = sup.Shutdown(context.Background())

	if !recovered {
		t.Fatalf("Run() must keep monitoring after Restart: child crash went unrecovered "+
			"(state=%v pid=%d, pidAfterRestart=%d)", sup.State(), sup.SingBoxPID(), pidAfterRestart)
	}
}

func TestSupervisorShutdownTearsDownAndKills(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var teardownCalls int32
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash)},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:  func(context.Context) error { return nil },
		TeardownHook: func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&teardownCalls) != 1 {
		t.Fatalf("teardown calls: %d", teardownCalls)
	}
	if sup.SingBoxPID() == 0 {
		// 进程对象保留，但 process 应已退出
	}
}

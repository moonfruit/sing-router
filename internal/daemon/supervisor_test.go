package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
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
	t.Cleanup(func() { _ = stack.Close(context.Background()) })
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
// 报 "ready check" 错并进 fatal，且子进程被 kill。
func TestSupervisorBootTimesOutWhenSingBoxStartsTooSlow(t *testing.T) {
	binary := fakeSingBox(t)
	p := freePort(t)
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--ready-delay", "1s"},
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
	if sup.State() != StateFatal {
		t.Fatalf("state: %v want StateFatal", sup.State())
	}
}

// 新行为：Restart 走 Shutdown + Startup 完整循环。
//   - teardown 必跑一次（Shutdown 阶段）
//   - startup 跑两次（Boot 一次 + Restart 一次）
//   - iptables 重新装回（Shutdown 期间被拆，Startup 期间重装）
func TestSupervisorRestartRunsShutdownThenStartup(t *testing.T) {
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
	defer func() { _ = sup.Shutdown(context.Background()) }()

	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := atomic.LoadInt32(&startupCalls); got != 2 {
		t.Fatalf("startup should run twice (boot + restart); got %d", got)
	}
	if got := atomic.LoadInt32(&teardownCalls); got != 1 {
		t.Fatalf("teardown should run once during restart; got %d", got)
	}
	if !sup.IptablesInstalled() {
		t.Fatal("iptables should be re-installed by Startup after Restart")
	}
	if sup.State() != StateRunning {
		t.Fatalf("state after restart: %v want StateRunning", sup.State())
	}
}

// Restart 节流：2s 窗口内的二次 Restart 直接 skip 返 nil，不调 Shutdown/Startup。
func TestSupervisorRestart_Throttled(t *testing.T) {
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
	defer func() { _ = sup.Shutdown(context.Background()) }()

	// 第一次 Restart 正常执行（lastRestartAt 此前是 zero）
	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart 1: %v", err)
	}
	startupAfter1 := atomic.LoadInt32(&startupCalls)
	teardownAfter1 := atomic.LoadInt32(&teardownCalls)

	// 立刻第二次 Restart → 命中 throttle，返 ErrRestartThrottled（必须可区分，不能误为 nil）
	if err := sup.Restart(context.Background()); !errors.Is(err, ErrRestartThrottled) {
		t.Fatalf("Restart 2 (throttled) should return ErrRestartThrottled, got %v", err)
	}
	if got := atomic.LoadInt32(&startupCalls); got != startupAfter1 {
		t.Fatalf("throttled Restart should NOT call StartupHook again: %d → %d", startupAfter1, got)
	}
	if got := atomic.LoadInt32(&teardownCalls); got != teardownAfter1 {
		t.Fatalf("throttled Restart should NOT call TeardownHook again: %d → %d", teardownAfter1, got)
	}
}

// RestartForce 绕过 throttle：连续两次 RestartForce 都跑完整循环。
func TestSupervisorRestartForce_BypassesThrottle(t *testing.T) {
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
	defer func() { _ = sup.Shutdown(context.Background()) }()

	if err := sup.RestartForce(context.Background()); err != nil {
		t.Fatalf("RestartForce 1: %v", err)
	}
	if err := sup.RestartForce(context.Background()); err != nil {
		t.Fatalf("RestartForce 2: %v", err)
	}
	// boot 一次 + 两次 RestartForce 各调 startup 一次 = 3
	if got := atomic.LoadInt32(&startupCalls); got != 3 {
		t.Fatalf("startup should run 3 times (boot + 2x RestartForce); got %d", got)
	}
	if got := atomic.LoadInt32(&teardownCalls); got != 2 {
		t.Fatalf("teardown should run twice; got %d", got)
	}
}

// 并发 Restart 应被 restartMu 串行化（不会撞 state machine 错），
// 二次及以后大概率因 throttle 而 skip。重点是不 panic、不报错、状态最终 Running。
func TestSupervisorRestart_ConcurrentSerialized(t *testing.T) {
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
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = sup.Restart(context.Background())
		}()
	}
	wg.Wait()
	if sup.State() != StateRunning {
		t.Fatalf("after concurrent Restart, state=%v want Running", sup.State())
	}
}

// Shutdown 幂等：已 Stopped 态下再调一次仍然 OK，TeardownHook 仍被 best-effort 调用。
func TestSupervisorShutdown_Idempotent(t *testing.T) {
	var teardownCalls int32
	sup := New(SupervisorConfig{
		Emitter:      newTestEmitter(t),
		TeardownHook: func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
	})
	// 直接 Shutdown（未启动）应不报错
	if err := sup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 1: %v", err)
	}
	// 再调一次仍 OK
	if err := sup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 2: %v", err)
	}
	if got := atomic.LoadInt32(&teardownCalls); got != 2 {
		t.Fatalf("teardown called %d; want 2 (idempotent best-effort)", got)
	}
}

// Shutdown 内 teardown hook 失败 → 仍走完后续 kill + state 转移；不报错。
func TestSupervisorShutdown_TeardownFailureContinues(t *testing.T) {
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
		TeardownHook: func(context.Context) error { return errors.New("synthetic teardown failure") },
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown should swallow teardown failure: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("state after Shutdown: %v want Stopped", sup.State())
	}
	if sup.IptablesInstalled() {
		t.Fatal("iptablesInstalled should be cleared even if teardown failed")
	}
}

// Startup hook 失败 → killChild + 进 Fatal + 返 error。
func TestSupervisorStartup_StartupHookFailureGoesFatal(t *testing.T) {
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
		StartupHook:  func(context.Context) error { return errors.New("synthetic startup hook failure") },
		TeardownHook: func(context.Context) error { return nil },
		StopGrace:    1 * time.Second,
	})
	err := sup.Boot(context.Background())
	if err == nil {
		t.Fatal("expected startup hook failure to surface")
	}
	if sup.State() != StateFatal {
		t.Fatalf("state: %v want Fatal", sup.State())
	}
}

// 路由探测：RouteHealthy=false → 触发 Restart（不再用 ReapplyRoutesHook）。
func TestSupervisorWatchRoutesTriggersRestart(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var routeOK atomic.Bool
	routeOK.Store(false)
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
		RouteHealthy:       func(context.Context) bool { return routeOK.Load() },
		RouteWatchInterval: 20 * time.Millisecond,
		StopGrace:          1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()

	startCount := sup.RestartCount()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.WatchRoutes(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RestartCount() > startCount {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sup.RestartCount() == startCount {
		t.Fatal("WatchRoutes should trigger Restart while route is missing")
	}
}

// RouteHealthy 为 nil 时 WatchRoutes 必须立即返回（watcher 禁用），不阻塞。
func TestSupervisorWatchRoutesDisabledWhenRouteHealthyNil(t *testing.T) {
	sup := New(SupervisorConfig{Emitter: newTestEmitter(t)})
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
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if atomic.LoadInt32(&startupCalls) != 2 {
		t.Fatalf("startup should run again after Start; calls=%d", startupCalls)
	}
	_ = sup.Shutdown(context.Background())
}

// crash recovery 序列：crash → Shutdown 立即拆 iptables → 退避 → Startup。
// 用 BackoffMs 短序列加速测试；断言 teardown 至少被调一次（每次 crash recovery 都拆）。
func TestSupervisorAutoRestartUnderCrash(t *testing.T) {
	binary := fakeSingBox(t)
	p, clash := freePort(t), freePort(t)
	var startupCalls, teardownCalls int32
	sup := New(SupervisorConfig{
		Emitter:       newTestEmitter(t),
		SingBoxBinary: binary,
		SingBoxArgs:   []string{"--listen", strconv.Itoa(p), "--clash-port", strconv.Itoa(clash), "--crash-after", "300ms"},
		ReadyConfig: ReadyConfig{
			TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
			ClashAPIURL:  fmt.Sprintf("http://127.0.0.1:%d/version", clash),
			TotalTimeout: 2 * time.Second,
			Interval:     50 * time.Millisecond,
		},
		StartupHook:  func(context.Context) error { atomic.AddInt32(&startupCalls, 1); return nil },
		TeardownHook: func(context.Context) error { atomic.AddInt32(&teardownCalls, 1); return nil },
		BackoffMs:    []int{50, 100, 200},
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&teardownCalls) >= 1 && atomic.LoadInt32(&startupCalls) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-runDone
	// 新行为：crash 后立即 Shutdown，所以 teardown 至少被调一次（每轮 crash 都拆）。
	preShutdownTeardown := atomic.LoadInt32(&teardownCalls)
	_ = sup.Shutdown(context.Background())
	if preShutdownTeardown < 1 {
		t.Fatalf("crash recovery should invoke teardown at least once before Run exits; got %d", preShutdownTeardown)
	}
}

// Bug A 回归：Restart 经 HTTP 触发时调用方传 r.Context()（请求级 ctx）。
// sing-box 子进程必须绑 daemon 级 procCtx，不能绑请求 ctx，否则 HTTP 请求结束就被 SIGKILL。
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
		StartupHook:  func(context.Context) error { return nil },
		TeardownHook: func(context.Context) error { return nil },
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sup.Shutdown(context.Background()) }()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	if err := sup.Restart(reqCtx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	pid := sup.SingBoxPID()
	if pid == 0 {
		t.Fatal("sing-box pid should be non-zero after Restart")
	}
	reqCancel()
	time.Sleep(300 * time.Millisecond)

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("sing-box (pid %d) must stay alive after caller ctx cancelled, but: %v", pid, err)
	}
	if sup.State() != StateRunning {
		t.Fatalf("state after caller-ctx cancel: %v want StateRunning", sup.State())
	}
}

// Bug B 回归：Restart 期间 Run() 监控循环不能因 state 不对就退出。
// 验证：一次 Restart 后外部杀掉新子进程，Run() 仍能检测崩溃并退避重启。
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
		StartupHook:  func(context.Context) error { return nil },
		TeardownHook: func(context.Context) error { return nil },
		BackoffMs:    []int{50, 100, 200},
		StopGrace:    1 * time.Second,
	})
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- sup.Run(ctx) }()

	if err := sup.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	pidAfterRestart := sup.SingBoxPID()
	if pidAfterRestart == 0 {
		t.Fatal("pid 0 after Restart")
	}

	if err := syscall.Kill(pidAfterRestart, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child: %v", err)
	}

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
		t.Fatalf("Run() must keep monitoring after Restart (state=%v pid=%d, pidAfterRestart=%d)",
			sup.State(), sup.SingBoxPID(), pidAfterRestart)
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
}

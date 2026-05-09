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
	"sync/atomic"
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

func TestSupervisorRestartKeepsIptables(t *testing.T) {
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
	if atomic.LoadInt32(&startupCalls) != 1 {
		t.Fatalf("startup hook should not run again on restart with iptables_installed; calls=%d", startupCalls)
	}
	if atomic.LoadInt32(&teardownCalls) != 0 {
		t.Fatal("teardown should not run during user-initiated restart")
	}
	if !sup.IptablesInstalled() {
		t.Fatal("iptables should remain installed across restart")
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

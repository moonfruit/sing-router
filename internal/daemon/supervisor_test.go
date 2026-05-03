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

func newTestEmitter(t *testing.T) *log.Emitter {
	dir := t.TempDir()
	w, err := log.NewWriter(log.WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return log.NewEmitter(log.EmitterConfig{
		Source:   "daemon",
		MinLevel: log.LevelInfo,
		Writer:   w,
		Bus:      log.NewBus(8),
	})
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

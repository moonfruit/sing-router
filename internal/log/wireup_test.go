package log

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

func TestEmitterStackWritesAndPublishes(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
	})

	stack.Emitter.Info("supervisor", "boot", "starting at {Path}", map[string]any{"Path": "/opt/x"})

	// Bus.Close drains, so by the time Close returns the Writer has received the event.
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.log"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 1 || lines[0] == "" {
		t.Fatalf("no lines written: %q", string(data))
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if ev["@l"] != "Information" || ev["Source"] != "daemon" || ev["EventID"] != "boot" {
		t.Fatalf("unexpected fields: %v", ev)
	}
}

// TestEmitterStackAttachReceivesAndCloses 验证 Attach 的双向合约：注册后
// 能收到 publish；EmitterStack.Close 时被先 unsubscribe 再调 closeFn，且
// Close 顺序保证 Bus 关闭前 sink 先 drain。
func TestEmitterStackAttachReceivesAndCloses(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
	})

	var (
		mu        sync.Mutex
		delivered int
		closed    int32
	)
	h := stack.Bus.Subscribe(clef.SubscriberFunc{
		MatchFn: func(*clef.Event) bool { return true },
		DeliverFn: func(*clef.Event) {
			mu.Lock()
			delivered++
			mu.Unlock()
		},
	})
	stack.Attach("fake-sink", h, func(ctx context.Context) error {
		atomic.StoreInt32(&closed, 1)
		return nil
	})

	stack.Emitter.Info("supervisor", "boot", "", nil)
	stack.Emitter.Info("supervisor", "boot2", "", nil)

	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if atomic.LoadInt32(&closed) != 1 {
		t.Fatalf("attach close not invoked")
	}
	mu.Lock()
	got := delivered
	mu.Unlock()
	if got != 2 {
		t.Fatalf("delivered = %d, want 2", got)
	}
}

// TestEmitterStackAttachCloseErrorPropagates 验证 closeFn 错误经 errors.Join
// 透出，且即便有错也不会卡住 Bus / Writer 的关闭。
func TestEmitterStackAttachCloseErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	sentinel := errors.New("drain failed")
	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
	})
	stack.Attach("broken", clef.SubscriptionHandle{}, func(ctx context.Context) error { return sentinel })
	err = stack.Close(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Close err = %v, want wrapping %v", err, sentinel)
	}
}

// TestEmitterStackAttachCloseRespectsContext 验证传给 closeFn 的 ctx 真的被
// forward；这里用一个会 select on ctx.Done 的假 sink，证明 ctx 截止时间
// 透到底——这是后续 seq.Sink 包装的关键保证。
func TestEmitterStackAttachCloseRespectsContext(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		Writer:   w,
	})
	stack.Attach("slow", clef.SubscriptionHandle{}, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = stack.Close(ctx)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Close did not respect ctx timeout, took %v", time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close err = %v, want DeadlineExceeded wrap", err)
	}
}

// TestEmitterStackWriterMinLevelFilter 验证 WriterMinLevel 真的把低于阈值
// 的事件挡在 writer 之外，同时不影响其他订阅方（这里用 Bus 直接观测）。
// 这是接入 seq.Sink 后实现"log 文件和 seq 各自独立 level"的基石。
func TestEmitterStackWriterMinLevelFilter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	w, err := NewWriter(WriterConfig{Path: logPath})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	stack := NewEmitterStack(StackConfig{
		Source:         "daemon",
		MinLevel:       LevelInfo, // emitter 通过所有 info+
		WriterMinLevel: LevelWarn, // writer 只收 warn+
		Writer:         w,
	})

	// 旁路观测：直接订一份 bus，看 emitter 通过了什么。
	var (
		mu      sync.Mutex
		busSeen []string
	)
	stack.Bus.Subscribe(clef.SubscriberFunc{
		MatchFn: func(*clef.Event) bool { return true },
		DeliverFn: func(ev *clef.Event) {
			raw, _ := ev.Get("@l")
			mu.Lock()
			busSeen = append(busSeen, raw.(string))
			mu.Unlock()
		},
	})

	stack.Emitter.Info("test", "info-1", "", nil)
	stack.Emitter.Warn("test", "warn-1", "", nil)
	stack.Emitter.Error("test", "error-1", "", nil)

	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// bus 上 emitter 应该过了 3 条（Info/Warn/Error 都 >= LevelInfo）
	if len(busSeen) != 3 {
		t.Fatalf("bus saw %d events, want 3: %v", len(busSeen), busSeen)
	}
	// 但 writer 文件只应该有 2 条（Warn/Error）
	data, _ := os.ReadFile(logPath)
	body := string(data)
	if strings.Contains(body, "info-1") {
		t.Fatalf("writer should drop info events when WriterMinLevel=Warn; log: %q", body)
	}
	if !strings.Contains(body, "warn-1") || !strings.Contains(body, "error-1") {
		t.Fatalf("writer should keep warn/error; log: %q", body)
	}
}

// TestEmitterStackWriterMinLevelZeroIsNoFilter 守护：StackConfig 不设
// WriterMinLevel（零值 LevelTrace）时，writer 收所有 emitter 通过的事件，
// 跟引入这字段前的旧行为一致。
func TestEmitterStackWriterMinLevelZeroIsNoFilter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	w, err := NewWriter(WriterConfig{Path: logPath})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	stack := NewEmitterStack(StackConfig{
		Source:   "daemon",
		MinLevel: LevelInfo,
		// WriterMinLevel 不设
		Writer: w,
	})
	stack.Emitter.Info("test", "info-1", "", nil)
	stack.Emitter.Warn("test", "warn-1", "", nil)
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	body := string(data)
	if !strings.Contains(body, "info-1") || !strings.Contains(body, "warn-1") {
		t.Fatalf("writer should receive both (no filter), log: %q", body)
	}
}

// TestEmitterStackDrainDiagnosticsReachWriter 回归守护：closeFn 在 drain 期间
// 通过共享 Bus 发出的诊断事件必须落到本地 writer 的日志文件——这是接入
// seq.Sink 的关键不变量（shutdown_post_failed 不丢）。如果 Close 顺序回退
// 到"先 unsubscribe writer 再 drain extras"，writer 会收不到这条事件，
// 测试就会失败。
func TestEmitterStackDrainDiagnosticsReachWriter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")
	w, err := NewWriter(WriterConfig{Path: logPath})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	stack := NewEmitterStack(StackConfig{
		Source: "daemon", MinLevel: LevelInfo, Writer: w,
	})
	// 模拟 seq.Sink 的 seqEmitter：与 daemon emitter 共用同一 bus，Source
	// 不同。closeFn 触发时往 bus 发一条 shutdown_post_failed 风格事件，
	// writer 必须收到。
	sinkEmitter := clef.NewEmitter(clef.EmitterConfig{Source: "sing2seq", Bus: stack.Bus})
	stack.Attach("fake-seq-sink", clef.SubscriptionHandle{}, func(ctx context.Context) error {
		sinkEmitter.Error("seq.sink", "shutdown_post_failed",
			"post failed during shutdown: simulated",
			map[string]any{"Pending": 3})
		return nil
	})

	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "shutdown_post_failed") {
		t.Fatalf("writer should capture sink drain diagnostics, log was: %q", string(data))
	}
	if !strings.Contains(string(data), `"Source":"sing2seq"`) {
		t.Fatalf("diagnostic should carry Source=sing2seq, log was: %q", string(data))
	}
}

// TestEmitterStackCloseIsIdempotent 守护：daemon shutdown 路径上 defer 会
// 调一次 Close；如果 panic recover 在另一条路径上又调一次，必须不能崩。
func TestEmitterStackCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(WriterConfig{Path: filepath.Join(dir, "test.log")})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	stack := NewEmitterStack(StackConfig{
		Source: "daemon", MinLevel: LevelInfo, Writer: w,
	})
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := stack.Close(context.Background()); err != nil {
		t.Fatalf("second Close should be no-op, got: %v", err)
	}
}

package notify

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeChannel 是测试用 Channel：记录收到的通知，可注入前 N 次失败与发送延迟。
type fakeChannel struct {
	name  string
	failN int           // 前 failN 次 Send 返回 error
	delay time.Duration // 每次 Send 的人为延迟

	mu    sync.Mutex
	calls int
	sent  []Notification
}

func (f *fakeChannel) Name() string { return f.name }

func (f *fakeChannel) Send(ctx context.Context, n Notification) error {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if call <= f.failN {
		return fmt.Errorf("%s: injected failure #%d", f.name, call)
	}
	f.mu.Lock()
	f.sent = append(f.sent, n)
	f.mu.Unlock()
	return nil
}

func (f *fakeChannel) received() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.sent...)
}

func (f *fakeChannel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// waitFor 轮询直到 cond 为真或超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func fastBackoffs() []time.Duration { return []time.Duration{time.Millisecond} }

func notif(kind string, p Priority) Notification {
	return Notification{Kind: kind, Title: "t", Body: "b", Priority: p}
}

func TestDispatchDelivers(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal))

	if !waitFor(t, time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatalf("notification not delivered, got %d", len(fc.received()))
	}
}

func TestDispatchGlobalMinPriorityFilter(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{
		Channels:    []ChannelSpec{{Channel: fc}},
		MinPriority: PriorityHigh,
	})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal)) // 低于阈值
	n.Dispatch(notif("supervisor.route.missing", PriorityHigh))

	if !waitFor(t, time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatalf("want exactly 1 delivered (High only), got %d", len(fc.received()))
	}
	if got := fc.received()[0].Kind; got != "supervisor.route.missing" {
		t.Errorf("delivered wrong notification: %s", got)
	}
}

func TestDispatchDisabledKind(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{
		Channels:      []ChannelSpec{{Channel: fc}},
		DisabledKinds: []string{"sync.item.failed"},
	})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("sync.item.failed", PriorityNormal))
	n.Dispatch(notif("apply.ok", PriorityNormal))

	if !waitFor(t, time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatalf("want 1 delivered (disabled kind dropped), got %d", len(fc.received()))
	}
	if got := fc.received()[0].Kind; got != "apply.ok" {
		t.Errorf("delivered wrong notification: %s", got)
	}
}

func TestPerChannelMinPriority(t *testing.T) {
	low := &fakeChannel{name: "low"}
	high := &fakeChannel{name: "high"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{
		{Channel: low},
		{Channel: high, MinPriority: PriorityHigh},
	}})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal))

	if !waitFor(t, time.Second, func() bool { return len(low.received()) == 1 }) {
		t.Fatalf("low channel should get Normal notification")
	}
	time.Sleep(20 * time.Millisecond)
	if len(high.received()) != 0 {
		t.Errorf("high-only channel should not get Normal notification")
	}
}

func TestRetrySucceedsAfterFailures(t *testing.T) {
	fc := &fakeChannel{name: "fc", failN: 2}
	n := NewNotifier(NotifierConfig{
		Channels:      []ChannelSpec{{Channel: fc}},
		MaxAttempts:   3,
		RetryBackoffs: fastBackoffs(),
	})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal))

	if !waitFor(t, 2*time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatalf("retry should eventually deliver, calls=%d sent=%d", fc.callCount(), len(fc.received()))
	}
	if c := fc.callCount(); c != 3 {
		t.Errorf("expected 3 send attempts, got %d", c)
	}
}

func TestChannelIsolation(t *testing.T) {
	bad := &fakeChannel{name: "bad", failN: 1 << 30} // 永远失败
	good := &fakeChannel{name: "good"}
	n := NewNotifier(NotifierConfig{
		Channels:      []ChannelSpec{{Channel: bad}, {Channel: good}},
		MaxAttempts:   3,
		RetryBackoffs: fastBackoffs(),
	})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal))

	// 坏渠道不应阻塞好渠道。
	if !waitFor(t, time.Second, func() bool { return len(good.received()) == 1 }) {
		t.Fatalf("good channel blocked by failing channel")
	}
}

func TestThrottleSuppressesRapidRepeats(t *testing.T) {
	// supervisor.child.crashed 在目录里带 5 分钟节流窗口。
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	defer func() { _ = n.Close(context.Background()) }()

	for range 5 {
		n.Dispatch(notif("supervisor.child.crashed", PriorityHigh))
	}
	// 首次发出，其余被窗口抑制。
	if !waitFor(t, time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatalf("throttle should pass exactly the first, got %d", len(fc.received()))
	}
	time.Sleep(30 * time.Millisecond)
	if got := len(fc.received()); got != 1 {
		t.Errorf("throttle window breached: delivered %d", got)
	}
}

func TestThrottleCarriesSuppressedCount(t *testing.T) {
	// 不节流的 Kind 每条都发，且不带 suppressed_count。
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	defer func() { _ = n.Close(context.Background()) }()

	n.Dispatch(notif("apply.ok", PriorityNormal))
	if !waitFor(t, time.Second, func() bool { return len(fc.received()) == 1 }) {
		t.Fatal("not delivered")
	}
	if _, ok := fc.received()[0].Fields["suppressed_count"]; ok {
		t.Error("un-throttled kind should not carry suppressed_count")
	}
}

func TestNotifySyncDeliversImmediately(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	defer func() { _ = n.Close(context.Background()) }()

	n.NotifySync(context.Background(), notif("panic.recovered", PriorityCritical))

	// NotifySync 返回时投递已完成。
	if got := len(fc.received()); got != 1 {
		t.Fatalf("NotifySync should deliver synchronously, got %d", got)
	}
}

func TestCloseDrainsQueue(t *testing.T) {
	fc := &fakeChannel{name: "fc", delay: 10 * time.Millisecond}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})

	for range 5 {
		n.Dispatch(notif("apply.ok", PriorityNormal))
	}
	if err := n.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(fc.received()); got != 5 {
		t.Errorf("Close should drain all queued notifications, got %d", got)
	}
}

func TestDispatchAfterCloseIsNoop(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	if err := n.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	n.Dispatch(notif("apply.ok", PriorityNormal)) // 不应 panic
	if got := len(fc.received()); got != 0 {
		t.Errorf("Dispatch after Close should be no-op, got %d", got)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	fc := &fakeChannel{name: "fc"}
	n := NewNotifier(NotifierConfig{Channels: []ChannelSpec{{Channel: fc}}})
	if err := n.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := n.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

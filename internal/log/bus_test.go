package log

import (
	"sync"
	"testing"
	"time"
)

func TestBusDeliversToSubscribers(t *testing.T) {
	b := NewBus(8)
	defer b.Close()

	var mu sync.Mutex
	received := []string{}

	b.Subscribe(SubscriberFunc{
		MatchFn: func(e *OrderedEvent) bool {
			v, _ := e.Get("EventID")
			id, _ := v.(string)
			return id == "supervisor.boot.ready"
		},
		DeliverFn: func(e *OrderedEvent) {
			mu.Lock()
			defer mu.Unlock()
			v, _ := e.Get("EventID")
			received = append(received, v.(string))
		},
	})

	e := NewEvent()
	e.Set("EventID", "supervisor.boot.ready")
	b.Publish(e)
	e2 := NewEvent()
	e2.Set("EventID", "http.request")
	b.Publish(e2)

	waitFor(t, 200*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	})
	if received[0] != "supervisor.boot.ready" {
		t.Fatalf("unexpected delivered events: %v", received)
	}
}

func TestBusDropsOnFullBuffer(t *testing.T) {
	b := NewBus(2) // 极小 buffer
	defer b.Close()

	block := make(chan struct{})
	b.Subscribe(SubscriberFunc{
		MatchFn:   func(*OrderedEvent) bool { return true },
		DeliverFn: func(*OrderedEvent) { <-block },
	})

	// Publish 不能阻塞：超出 buffer 的事件被丢弃。
	for i := 0; i < 100; i++ {
		e := NewEvent()
		e.Set("i", i)
		b.Publish(e) // 必须不阻塞
	}
	// 走到这里就说明没卡住。
	close(block)
}

func TestBusUnsubscribeStopsDelivery(t *testing.T) {
	b := NewBus(4)
	defer b.Close()

	var mu sync.Mutex
	var seen int

	sub := SubscriberFunc{
		MatchFn:   func(*OrderedEvent) bool { return true },
		DeliverFn: func(*OrderedEvent) { mu.Lock(); seen++; mu.Unlock() },
	}
	handle := b.Subscribe(sub)

	b.Publish(NewEvent())
	waitFor(t, 200*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen == 1
	})

	handle.Unsubscribe()
	b.Publish(NewEvent())
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if seen != 1 {
		t.Fatalf("seen %d, want 1 (no delivery after unsubscribe)", seen)
	}
}

func waitFor(t *testing.T, total time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("waitFor: condition not met within timeout")
}

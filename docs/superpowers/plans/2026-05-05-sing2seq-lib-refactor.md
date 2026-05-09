# sing2seq Library Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract sing2seq's parser, emitter, bus, and async seq sink into a public Go library at `github.com/moonfruit/sing2seq` (v1.3.0), and migrate sing-router from its source-copy of these primitives to a `require` dependency in two incremental steps (parser/Event first, then Emitter/Bus).

**Architecture:** Two-package library — `clef` (Event, Level, ParseSingBoxLine, Emitter, Bus) and `seq` (async batched HTTP Sink to Seq's `/ingest/clef`). sing2seq CLI moves to `cmd/sing2seq/` and uses Bus + Emitter to fan out parsed events (`Source="sing-box"`) and self-diagnostics (`Source="sing2seq"`) to a pretty stderr renderer plus either `seq.Sink` (when URL set) or stdout JSON sink (debug). sing-router replaces its local `OrderedEvent`/`ParseSingBoxLine`/`Emitter`/`Bus` with `clef` types via a type-alias-then-direct-import migration, keeping its project-specific `Writer`/`Pretty`/`Level` (alias-only) in `internal/log`.

**Tech Stack:** Go 1.26, cobra (existing CLI), stdlib only for clef/seq.

**Spec:** `docs/superpowers/specs/2026-05-05-sing2seq-lib-refactor-design.md`

**Repos involved:**
- `github.com/moonfruit/sing2seq` — local clone at `/Users/moon/Workspace.localized/go/mod/sing2seq`
- `github.com/moonfruit/sing-router` — current repo

**Notes on minor deviations from spec:**
- `clef.NewBus(perSubBuffer int)` keeps the buffer parameter (port from existing `internal/log/bus.go`); spec said no-arg. Strict superset — pass `0` for default 64. Also fixes the existing bug where `perSubBuffer` was accepted but ignored (channel was hardcoded to 64).
- The `clef.Emitter` <-> `clef.Bus` interface follows the existing sing-router design (`Subscriber` interface with `Match` + `Deliver`, `SubscriberFunc` for closures, `SubscriptionHandle.Unsubscribe`). Spec's "Subscribe(fn, filter) → unsubscribe" reads as a thin wrapper; we implement using the existing structured form so existing tests port unchanged.

---

## Phase 1 — sing2seq library (single sequence → tag v1.3.0)

> **Working directory:** `/Users/moon/Workspace.localized/go/mod/sing2seq`
> **Branch:** `feature/lib-refactor` (create at start)

### Task 1.0: Create working branch

**Files:** none

- [ ] **Step 1: Create branch**

```bash
cd /Users/moon/Workspace.localized/go/mod/sing2seq
git checkout -b feature/lib-refactor
git status
```

Expected: clean tree, on `feature/lib-refactor`.

---

### Task 1.1: Rename module path & scaffold package directories

**Files:**
- Modify: `go.mod`
- Create: `clef/.gitkeep`, `seq/.gitkeep`, `cmd/sing2seq/.gitkeep`

- [ ] **Step 1: Rewrite go.mod module path**

Replace the first line of `go.mod`:

```
module github.com/moonfruit/sing2seq
```

(Keep the rest of `go.mod` and `go.sum` unchanged.)

- [ ] **Step 2: Create empty package directories**

```bash
mkdir -p clef seq cmd/sing2seq
touch clef/.gitkeep seq/.gitkeep cmd/sing2seq/.gitkeep
```

- [ ] **Step 3: Verify build still passes**

```bash
go build ./...
```

Expected: builds (existing files in package main still compile).

- [ ] **Step 4: Commit**

```bash
git add go.mod clef/.gitkeep seq/.gitkeep cmd/sing2seq/.gitkeep
git commit -m "chore: rename module path, scaffold lib + cmd dirs"
```

---

### Task 1.2: Add `clef.Event` (port from sing-router/internal/log/clef.go)

**Files:**
- Create: `clef/event.go`
- Create: `clef/event_test.go`

- [ ] **Step 1: Create event.go**

```go
// Package clef 提供 Compact Log Event Format (CLEF) 原语，以及把
// sing-box 日志解析成 CLEF 事件的解析器。
package clef

import (
	"bytes"
	"encoding/json"
)

// Event 是 CLEF 事件，字段顺序由 Set 调用顺序决定。Seq UI 按顺序展示，
// 不要随便改字段写入顺序。
type Event struct {
	keys   []string
	values map[string]any
}

// NewEvent 创建一个空事件。
func NewEvent() *Event {
	return &Event{values: map[string]any{}}
}

// Set 添加或更新一个字段；首次写入追加到 keys 末尾，重复写仅更新值不动顺序。
func (e *Event) Set(k string, v any) {
	if _, ok := e.values[k]; !ok {
		e.keys = append(e.keys, k)
	}
	e.values[k] = v
}

// Get 返回字段值；不存在时第二个返回值为 false。
func (e *Event) Get(k string) (any, bool) {
	v, ok := e.values[k]
	return v, ok
}

// Keys 返回有序键列表的副本。
func (e *Event) Keys() []string {
	out := make([]string, len(e.keys))
	copy(out, e.keys)
	return out
}

// MarshalJSON 按插入顺序序列化字段。
func (e *Event) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range e.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(e.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
```

- [ ] **Step 2: Port sing-router's clef_test.go verbatim, swapping `OrderedEvent` → `Event`**

```bash
cp /Users/moon/Workspace.localized/proxy/sing-router/internal/log/clef_test.go clef/event_test.go
```

Then edit `clef/event_test.go`:
- Change `package log` → `package clef`
- Replace every `OrderedEvent` with `Event`
- Replace every `NewEvent()` call — already correct (constructor name same)

(If the existing test file uses `*OrderedEvent` parameters or `OrderedEvent{}` struct literals, swap those too.)

- [ ] **Step 3: Run tests**

```bash
go test ./clef/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add clef/event.go clef/event_test.go
git rm clef/.gitkeep
git commit -m "feat(clef): add Event with order-preserving MarshalJSON"
```

---

### Task 1.3: Add `clef.Level` (port from sing-router/internal/log/level.go)

**Files:**
- Create: `clef/level.go`
- Create: `clef/level_test.go`

- [ ] **Step 1: Create level.go**

```go
package clef

import (
	"fmt"
	"strings"
)

// Level 是 CLEF 日志级别；与 sing-box 同源。
type Level int

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

// CLEFName 返回 Serilog (Seq) 兼容的级别名称。
func (l Level) CLEFName() string {
	switch l {
	case LevelTrace:
		return "Verbose"
	case LevelDebug:
		return "Debug"
	case LevelInfo:
		return "Information"
	case LevelWarn:
		return "Warning"
	case LevelError:
		return "Error"
	case LevelFatal:
		return "Fatal"
	default:
		return "Information"
	}
}

// String 返回简短大写形式（与 sing-box 一致），用于 pretty 输出。
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelFatal:
		return "FATAL"
	default:
		return "INFO"
	}
}

// ParseLevel 解析配置文件里的级别字符串。
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info", "":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "fatal", "panic":
		return LevelFatal, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q", s)
	}
}

// FromCLEFName 把 Seq 级别字符串还原为 Level（用于 pretty 渲染）。
func FromCLEFName(s string) Level {
	switch s {
	case "Verbose":
		return LevelTrace
	case "Debug":
		return LevelDebug
	case "Information":
		return LevelInfo
	case "Warning":
		return LevelWarn
	case "Error":
		return LevelError
	case "Fatal":
		return LevelFatal
	default:
		return LevelInfo
	}
}
```

- [ ] **Step 2: Port level_test.go**

```bash
cp /Users/moon/Workspace.localized/proxy/sing-router/internal/log/level_test.go clef/level_test.go
```

Edit `clef/level_test.go`: change `package log` → `package clef`. No type renames needed (Level is the same name).

- [ ] **Step 3: Run tests**

```bash
go test ./clef/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add clef/level.go clef/level_test.go
git commit -m "feat(clef): add Level with CLEF/short-name mapping"
```

---

### Task 1.4: Move parser into `clef` package

**Files:**
- Create: `clef/parser.go`
- Create: `clef/parser_test.go`
- Delete: `parser.go`, `parser_test.go` (root)

- [ ] **Step 1: Create clef/parser.go**

Copy the full contents of the existing root `parser.go`, but make these changes:
- `package main` → `package clef`
- Replace every `*orderedEvent` with `*Event`
- Replace every `orderedEvent` with `Event`
- Replace every `newEvent()` with `NewEvent()`
- Replace every `ev.set(` with `ev.Set(`
- Replace every `dns.set(` with `dns.Set(`
- Replace every `ev.values["DNS"]` with the new accessor pattern (use `ev.Get("DNS")` returning `(any, bool)`):

```go
func dnsSet(ev *Event, k string, v any) {
	var dns *Event
	if raw, ok := ev.Get("DNS"); ok {
		dns, _ = raw.(*Event)
	}
	if dns == nil {
		dns = NewEvent()
		ev.Set("DNS", dns)
	}
	dns.Set(k, v)
}
```

(The other helpers — `setHost`, `setIPs`, `enrich`, `parseLine` — only need the lowercase→uppercase method name swaps and `*orderedEvent` → `*Event`.)

- After the existing `parseLine`, add the public wrapper:

```go
// ParseSingBoxLine 解析一行 sing-box stderr（含或不含 ANSI/CRLF）成 Event。
// 不可解析的行降级为 Parsed=false 的原始事件；空行返回 nil。
// 事件已含 Source="sing-box"。
func ParseSingBoxLine(line string) *Event {
	return parseLine(line)
}
```

- Keep `stripAnsi`, `namedMatches`, `isIP`, `setHost`, `setIPs`, `dnsSet`, `enrich`, `parseLine` as unexported.
- Keep all `var` regex and `levelMap` blocks unchanged.

- [ ] **Step 2: Port parser_test.go**

```bash
cp parser_test.go clef/parser_test.go
```

Edit `clef/parser_test.go`:
- `package main` → `package clef`
- Update any reference to `parseLine` (already lowercase, stays); references to `orderedEvent` → `Event`
- Public-facing tests can call either `parseLine` or `ParseSingBoxLine` interchangeably; leave the existing assertions untouched.

- [ ] **Step 3: Delete root parser.go and parser_test.go**

```bash
git rm parser.go parser_test.go
```

- [ ] **Step 4: Run tests**

```bash
go test ./clef/...
```

Expected: PASS (all parser tests green; assertions match new Event API).

- [ ] **Step 5: Verify root package still builds (it doesn't yet because batcher.go references orderedEvent)**

```bash
go build ./...
```

Expected: **FAIL** — root package's `batcher.go`, `main.go`, `log.go` reference `orderedEvent`/`parseLine`. This is expected; we're mid-migration. Subsequent tasks fix root package.

To unblock incremental commits, temporarily isolate root package by introducing a thin shim:

Create `internal_shim.go` (root):

```go
package main

import "github.com/moonfruit/sing2seq/clef"

type orderedEvent = clef.Event

func newEvent() *clef.Event { return clef.NewEvent() }

func parseLine(raw string) *clef.Event { return clef.ParseSingBoxLine(raw) }
```

Then re-run:

```bash
go build ./...
```

Expected: PASS (root main package compiles via aliases).

- [ ] **Step 6: Commit**

```bash
git add clef/parser.go clef/parser_test.go internal_shim.go
git rm parser.go parser_test.go
git commit -m "feat(clef): move parser into package; add ParseSingBoxLine"
```

---

### Task 1.5: Add `clef.Bus` (port from sing-router with bug fix)

**Files:**
- Create: `clef/bus.go`
- Create: `clef/bus_test.go`

- [ ] **Step 1: Create clef/bus.go**

Port from `internal/log/bus.go`, replacing `*OrderedEvent` → `*Event` and **fixing the perSubBuffer bug** (line `make(chan *OrderedEvent, 64)` was hardcoded; should use the configured buffer):

```go
package clef

import "sync"

// Subscriber 是 Bus 的订阅方接口。
type Subscriber interface {
	Match(e *Event) bool
	Deliver(e *Event)
}

// SubscriberFunc 是基于函数字面量的便捷实现。
type SubscriberFunc struct {
	MatchFn   func(*Event) bool
	DeliverFn func(*Event)
}

func (s SubscriberFunc) Match(e *Event) bool { return s.MatchFn(e) }
func (s SubscriberFunc) Deliver(e *Event)   { s.DeliverFn(e) }

// SubscriptionHandle 由 Subscribe 返回，调用 Unsubscribe 停止派发。
type SubscriptionHandle struct {
	bus *Bus
	id  uint64
}

func (h SubscriptionHandle) Unsubscribe() {
	if h.bus != nil {
		h.bus.unsubscribe(h.id)
	}
}

// Bus 是 lossy 内存事件总线。Publish 永远不阻塞：当订阅方处理慢、buffer 满时
// 新事件被丢弃。需要持久化的订阅方应调高 perSubBuffer 或在订阅方内部走非
// lossy 队列。
type Bus struct {
	mu           sync.Mutex
	subs         map[uint64]*subscription
	nextID       uint64
	closed       bool
	perSubBuffer int
}

type subscription struct {
	sub  Subscriber
	ch   chan *Event
	done chan struct{}
}

// NewBus 创建总线；perSubBuffer 是每个订阅方的内部 channel 容量，<= 0 时取 64。
func NewBus(perSubBuffer int) *Bus {
	if perSubBuffer <= 0 {
		perSubBuffer = 64
	}
	return &Bus{subs: map[uint64]*subscription{}, perSubBuffer: perSubBuffer}
}

// Subscribe 注册一个订阅方；返回 handle 用于撤销。
func (b *Bus) Subscribe(s Subscriber) SubscriptionHandle {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return SubscriptionHandle{}
	}
	b.nextID++
	id := b.nextID
	sub := &subscription{
		sub:  s,
		ch:   make(chan *Event, b.perSubBuffer),
		done: make(chan struct{}),
	}
	b.subs[id] = sub
	go b.run(sub)
	return SubscriptionHandle{bus: b, id: id}
}

func (b *Bus) unsubscribe(id uint64) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.subs, id)
	b.mu.Unlock()
	close(sub.done)
}

// Publish 投递事件给所有匹配的订阅方；不阻塞。
func (b *Bus) Publish(e *Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	targets := make([]*subscription, 0, len(b.subs))
	for _, s := range b.subs {
		targets = append(targets, s)
	}
	b.mu.Unlock()

	for _, s := range targets {
		if !s.sub.Match(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// buffer 满 → 丢弃（lossy 设计）
		}
	}
}

// Close 停止所有订阅方；之后的 Publish 与 Subscribe 是 no-op。
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*subscription, 0, len(b.subs))
	for id, s := range b.subs {
		subs = append(subs, s)
		delete(b.subs, id)
	}
	b.mu.Unlock()
	for _, s := range subs {
		close(s.done)
	}
}

func (b *Bus) run(s *subscription) {
	for {
		select {
		case <-s.done:
			return
		case e := <-s.ch:
			s.sub.Deliver(e)
		}
	}
}
```

- [ ] **Step 2: Port bus_test.go**

```bash
cp /Users/moon/Workspace.localized/proxy/sing-router/internal/log/bus_test.go clef/bus_test.go
```

Edit `clef/bus_test.go`: `package log` → `package clef`; `*OrderedEvent` → `*Event`.

- [ ] **Step 3: Run tests**

```bash
go test ./clef/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add clef/bus.go clef/bus_test.go
git commit -m "feat(clef): add lossy Bus with subscriber match/deliver"
```

---

### Task 1.6: Add `clef.Emitter`

**Files:**
- Create: `clef/emitter.go`
- Create: `clef/emitter_test.go`

- [ ] **Step 1: Create clef/emitter.go**

Port from `internal/log/emitter.go` but **drop the Writer field** — Writer becomes a Bus subscriber in callers (sing-router wireup) per spec §1 / §5:

```go
package clef

import "time"

// EmitterConfig 配置 Emitter。
type EmitterConfig struct {
	Source   string // 事件 Source 字段，例如 "sing2seq" / "daemon"
	MinLevel Level  // 低于此级别的事件被丢弃；不影响 PublishExternal
	Bus      *Bus   // 可选；nil 时事件被丢弃
}

// Emitter 是结构化事件的入口。所有调用都构造 Event 并发布到 Bus。
type Emitter struct {
	cfg EmitterConfig
}

// NewEmitter 创建 emitter。
func NewEmitter(cfg EmitterConfig) *Emitter {
	if cfg.Source == "" {
		cfg.Source = "app"
	}
	return &Emitter{cfg: cfg}
}

func (e *Emitter) Trace(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelTrace, module, eventID, mt, fields)
}
func (e *Emitter) Debug(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelDebug, module, eventID, mt, fields)
}
func (e *Emitter) Info(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelInfo, module, eventID, mt, fields)
}
func (e *Emitter) Warn(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelWarn, module, eventID, mt, fields)
}
func (e *Emitter) Error(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelError, module, eventID, mt, fields)
}
func (e *Emitter) Fatal(module, eventID, mt string, fields map[string]any) {
	e.emit(LevelFatal, module, eventID, mt, fields)
}

// PublishExternal 接收已经构建好的事件（如 ParseSingBoxLine 返回的），
// 走与 Emitter 同一条出口（Bus）。MinLevel 不过滤外部事件。
func (e *Emitter) PublishExternal(ev *Event) {
	if e.cfg.Bus != nil {
		e.cfg.Bus.Publish(ev)
	}
}

func (e *Emitter) emit(level Level, module, eventID, mt string, fields map[string]any) {
	if level < e.cfg.MinLevel {
		return
	}
	ev := NewEvent()
	ev.Set("@t", time.Now().Format(time.RFC3339Nano))
	ev.Set("@l", level.CLEFName())
	if mt != "" {
		ev.Set("@mt", mt)
	}
	ev.Set("Source", e.cfg.Source)
	if module != "" {
		ev.Set("Module", module)
	}
	if eventID != "" {
		ev.Set("EventID", eventID)
	}
	for k, v := range fields {
		ev.Set(k, v)
	}
	if e.cfg.Bus != nil {
		e.cfg.Bus.Publish(ev)
	}
}
```

- [ ] **Step 2: Create clef/emitter_test.go**

```go
package clef

import (
	"sync"
	"testing"
	"time"
)

func TestEmitterPublishesToBus(t *testing.T) {
	bus := NewBus(8)
	defer bus.Close()

	var mu sync.Mutex
	var got []*Event
	bus.Subscribe(SubscriberFunc{
		MatchFn:   func(*Event) bool { return true },
		DeliverFn: func(e *Event) { mu.Lock(); got = append(got, e); mu.Unlock() },
	})

	em := NewEmitter(EmitterConfig{Source: "sing2seq", MinLevel: LevelInfo, Bus: bus})
	em.Info("seq.sink", "buffer_overflow", "buffer overflow: dropped {Dropped}", map[string]any{"Dropped": 100})

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	ev := got[0]
	checkField(t, ev, "@l", "Information")
	checkField(t, ev, "Source", "sing2seq")
	checkField(t, ev, "Module", "seq.sink")
	checkField(t, ev, "EventID", "buffer_overflow")
	checkField(t, ev, "Dropped", 100)
}

func TestEmitterDropsBelowMinLevel(t *testing.T) {
	bus := NewBus(8)
	defer bus.Close()

	var mu sync.Mutex
	var got []*Event
	bus.Subscribe(SubscriberFunc{
		MatchFn:   func(*Event) bool { return true },
		DeliverFn: func(e *Event) { mu.Lock(); got = append(got, e); mu.Unlock() },
	})

	em := NewEmitter(EmitterConfig{Source: "sing2seq", MinLevel: LevelWarn, Bus: bus})
	em.Info("m", "skip", "should be dropped", nil)
	em.Warn("m", "kept", "should be kept", nil)

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want 1 event after MinLevel filter, got %d", len(got))
	}
	checkField(t, got[0], "EventID", "kept")
}

func TestEmitterPublishExternalBypassesMinLevel(t *testing.T) {
	bus := NewBus(8)
	defer bus.Close()

	var mu sync.Mutex
	var got []*Event
	bus.Subscribe(SubscriberFunc{
		MatchFn:   func(*Event) bool { return true },
		DeliverFn: func(e *Event) { mu.Lock(); got = append(got, e); mu.Unlock() },
	})

	em := NewEmitter(EmitterConfig{Source: "daemon", MinLevel: LevelFatal, Bus: bus})
	external := NewEvent()
	external.Set("@t", "2026-05-05T00:00:00Z")
	external.Set("@l", "Information")
	external.Set("Source", "sing-box")
	em.PublishExternal(external)

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want 1 external event, got %d", len(got))
	}
}

func checkField(t *testing.T, e *Event, key string, want any) {
	t.Helper()
	v, ok := e.Get(key)
	if !ok {
		t.Fatalf("key %q missing", key)
	}
	if v != want {
		t.Fatalf("key %q = %v, want %v", key, v, want)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./clef/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add clef/emitter.go clef/emitter_test.go
git commit -m "feat(clef): add Emitter publishing to Bus"
```

---

### Task 1.7: Move Sink into `seq` package with Emitter-based diagnostics

**Files:**
- Create: `seq/sink.go`
- Modify: `internal_shim.go` (add aliases for Sink during transition)

- [ ] **Step 1: Create seq/sink.go**

Port from existing `batcher.go`. Key changes:
- Package `seq`; type `Batcher` → `Sink`; constructor `NewBatcher` → `NewSink(Config)`.
- `*orderedEvent` → `*clef.Event`.
- Add `Config` struct with all knobs + optional `*clef.Emitter`.
- Replace `logfFunc` callbacks with `Emitter` calls; fall back to stderr when Emitter is nil.
- `Close()` now returns `error` (last post error during shutdown).
- All HTTP behavior, batch size, retry, drop-oldest unchanged.

```go
// Package seq 提供异步批量把 CLEF 事件投递到 Seq 的 sink。
package seq

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

const (
	defaultBatchSize      = 200
	defaultChannelBuffer  = 1024
	defaultMaxPending     = 50000
	defaultDropTarget     = 25000
	defaultInitialBackoff = 1 * time.Second
	defaultMaxBackoff     = 60 * time.Second
)

// Config 配置 Sink。零值字段套默认。
type Config struct {
	URL        string
	APIKey     string
	Insecure   bool
	HTTPClient *http.Client
	Emitter    *clef.Emitter // 可选；用于发出 sink 自身诊断；nil 时 fallback 到 stderr

	BatchSize      int
	ChannelBuffer  int
	MaxPending     int
	DropTarget     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// Sink 是异步批量 HTTP sink。
type Sink struct {
	cfg Config

	ch       chan *clef.Event
	done     chan struct{}
	startOnce sync.Once

	closeMu  sync.Mutex
	closed   bool
	closeErr error
}

// NewSink 构造 Sink；不启动 manager。
func NewSink(cfg Config) *Sink {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.ChannelBuffer <= 0 {
		cfg.ChannelBuffer = defaultChannelBuffer
	}
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = defaultMaxPending
	}
	if cfg.DropTarget <= 0 || cfg.DropTarget >= cfg.MaxPending {
		cfg.DropTarget = cfg.MaxPending / 2
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = defaultInitialBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	if cfg.HTTPClient == nil {
		tr := &http.Transport{}
		if cfg.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: tr}
	}
	return &Sink{
		cfg:  cfg,
		ch:   make(chan *clef.Event, cfg.ChannelBuffer),
		done: make(chan struct{}),
	}
}

// Start 启动 manager goroutine。caller 应该只调用一次。
func (s *Sink) Start() {
	s.startOnce.Do(func() { go s.run() })
}

// Submit 投递事件；O(1) 不阻塞；nil 忽略。
func (s *Sink) Submit(ev *clef.Event) {
	if ev == nil {
		return
	}
	s.closeMu.Lock()
	closed := s.closed
	s.closeMu.Unlock()
	if closed {
		return
	}
	s.ch <- ev
}

// Close 停止接受新事件；阻塞直到 pending 排空；返回 drain 期间最后一个 post error。
func (s *Sink) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		<-s.done
		return s.closeErr
	}
	s.closed = true
	close(s.ch)
	s.closeMu.Unlock()
	<-s.done
	return s.closeErr
}

type postResult struct {
	n   int
	err error
}

// run 是 manager goroutine。其 select 只做 O(1) 工作，永不阻塞 I/O，
// 确保 ch 总能及时清空、Submit 实际上从不阻塞。
func (s *Sink) run() {
	defer close(s.done)

	var pending []*clef.Event
	var inflight bool
	var droppedTotal int
	backoff := s.cfg.InitialBackoff
	var retryC <-chan time.Time
	resultC := make(chan postResult, 1)
	closed := false

	dispatch := func() {
		if inflight || retryC != nil || len(pending) == 0 {
			return
		}
		n := min(len(pending), s.cfg.BatchSize)
		batch := make([]*clef.Event, n)
		copy(batch, pending[:n])
		inflight = true
		go func() {
			resultC <- postResult{n: n, err: s.post(batch)}
		}()
	}

	for {
		var inC <-chan *clef.Event
		if !closed {
			inC = s.ch
		}

		select {
		case ev, ok := <-inC:
			if !ok {
				closed = true
				retryC = nil
				dispatch()
				break
			}
			pending = append(pending, ev)
			if len(pending) > s.cfg.MaxPending {
				drop := len(pending) - s.cfg.DropTarget
				pending = pending[:copy(pending, pending[drop:])]
				droppedTotal += drop
				s.diag(clef.LevelWarn, "buffer_overflow",
					"buffer overflow: dropped {Dropped} oldest events (total dropped={TotalDropped})",
					map[string]any{"Dropped": drop, "TotalDropped": droppedTotal})
			}
			dispatch()

		case r := <-resultC:
			inflight = false
			if r.err == nil {
				pending = pending[:copy(pending, pending[r.n:])]
				backoff = s.cfg.InitialBackoff
				dispatch()
			} else if closed {
				s.closeErr = r.err
				s.diag(clef.LevelError, "shutdown_post_failed",
					"post failed during shutdown (pending={Pending}): {Error}; dropping remaining events",
					map[string]any{"Pending": len(pending), "Error": r.err.Error()})
				droppedTotal += len(pending)
				pending = pending[:0]
			} else {
				s.diag(clef.LevelWarn, "post_failed",
					"post failed (pending={Pending}): {Error}; retry in {RetryIn}",
					map[string]any{"Pending": len(pending), "Error": r.err.Error(), "RetryIn": backoff.String()})
				retryC = time.After(backoff)
				backoff = min(backoff*2, s.cfg.MaxBackoff)
			}

		case <-retryC:
			retryC = nil
			dispatch()
		}

		if closed && !inflight && retryC == nil && len(pending) == 0 {
			return
		}
	}
}

func (s *Sink) post(events []*clef.Event) error {
	var body bytes.Buffer
	for i, ev := range events {
		if i > 0 {
			body.WriteByte('\n')
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		body.Write(data)
	}
	url := strings.TrimRight(s.cfg.URL, "/") + "/ingest/clef"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.serilog.clef")
	if s.cfg.APIKey != "" {
		req.Header.Set("X-Seq-ApiKey", s.cfg.APIKey)
	}
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seq ingest failed: %d %q", resp.StatusCode, data)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (s *Sink) diag(level clef.Level, eventID, mt string, fields map[string]any) {
	if s.cfg.Emitter != nil {
		switch level {
		case clef.LevelWarn:
			s.cfg.Emitter.Warn("seq.sink", eventID, mt, fields)
		case clef.LevelError:
			s.cfg.Emitter.Error("seq.sink", eventID, mt, fields)
		default:
			s.cfg.Emitter.Info("seq.sink", eventID, mt, fields)
		}
		return
	}
	// fallback: stderr
	rendered := mt
	for k, v := range fields {
		rendered = strings.ReplaceAll(rendered, "{"+k+"}", fmt.Sprintf("%v", v))
	}
	_, _ = fmt.Fprintf(os.Stderr, "[seq.sink] %s %s: %s\n", level.CLEFName(), eventID, rendered)
}
```

- [ ] **Step 2: Update internal_shim.go to remove root package's batcher dependency**

Edit `internal_shim.go` to add a Sink alias for any straggler references during this phase (we'll delete this file entirely in Task 1.9):

```go
package main

import (
	"github.com/moonfruit/sing2seq/clef"
	"github.com/moonfruit/sing2seq/seq"
)

type orderedEvent = clef.Event

func newEvent() *clef.Event { return clef.NewEvent() }

func parseLine(raw string) *clef.Event { return clef.ParseSingBoxLine(raw) }

// 保留以兼容 main.go / batcher.go 旧引用
var _ = seq.NewSink
```

- [ ] **Step 3: Verify clef and seq build**

```bash
go build ./clef/... ./seq/...
```

Expected: PASS.

- [ ] **Step 4: Verify root build still works (it won't, because batcher.go still defines Batcher type)**

```bash
go build ./...
```

Expected: may FAIL on duplicate symbols. To unblock, **delete the root `batcher.go`** since `seq.Sink` replaces it:

```bash
git rm batcher.go
```

Then re-run:

```bash
go build ./...
```

Expected: now FAIL on `main.go` references like `b := NewBatcher(...)`. Don't fix those yet — Task 1.9 rewrites the CLI entirely. Move on to commit.

If you want a clean build between commits, also temporarily comment-out / stub the broken sink construction in `main.go`'s `batcherSink()`. Acceptable since this is a transient state inside Phase 1.

- [ ] **Step 5: Commit**

```bash
git add seq/sink.go internal_shim.go
git rm batcher.go
git commit -m "feat(seq): add async batched Sink with Emitter diagnostics"
```

---

### Task 1.8: Add `seq/sink_test.go` with full coverage

**Files:**
- Create: `seq/sink_test.go`

- [ ] **Step 1: Write failing test scaffold**

```go
package seq

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

func newEvent(id string) *clef.Event {
	e := clef.NewEvent()
	e.Set("@t", "2026-05-05T00:00:00Z")
	e.Set("@l", "Information")
	e.Set("Source", "sing-box")
	e.Set("EventID", id)
	return e
}

// captureBus returns (bus, &events slice, mutex). Caller can read events under mu.
func captureBus(t *testing.T) (*clef.Bus, *[]*clef.Event, *sync.Mutex) {
	t.Helper()
	bus := clef.NewBus(64)
	t.Cleanup(bus.Close)
	var mu sync.Mutex
	got := make([]*clef.Event, 0)
	bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   func(*clef.Event) bool { return true },
		DeliverFn: func(e *clef.Event) { mu.Lock(); got = append(got, e); mu.Unlock() },
	})
	return bus, &got, &mu
}

func TestSinkPostsBatch(t *testing.T) {
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/vnd.serilog.clef" {
			t.Errorf("content-type = %q", got)
		}
		atomic.AddInt32(&received, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewSink(Config{URL: srv.URL, BatchSize: 5})
	s.Start()
	for i := 0; i < 5; i++ {
		s.Submit(newEvent("e"))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if atomic.LoadInt32(&received) == 0 {
		t.Fatal("expected at least one POST")
	}
}

func TestSinkRetriesOnTransientFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bus, got, mu := captureBus(t)
	em := clef.NewEmitter(clef.EmitterConfig{Source: "sing2seq", Bus: bus})

	s := NewSink(Config{
		URL:            srv.URL,
		Emitter:        em,
		BatchSize:      1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	s.Start()
	s.Submit(newEvent("retry-me"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&attempts) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	postFailed := 0
	for _, e := range *got {
		v, _ := e.Get("EventID")
		if v == "post_failed" {
			postFailed++
		}
	}
	if postFailed < 2 {
		t.Fatalf("expected >= 2 post_failed diagnostics, got %d", postFailed)
	}
}

func TestSinkBufferOverflowDropsOldest(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(block)

	bus, got, mu := captureBus(t)
	em := clef.NewEmitter(clef.EmitterConfig{Source: "sing2seq", Bus: bus})

	s := NewSink(Config{
		URL:           srv.URL,
		Emitter:       em,
		BatchSize:     10,
		ChannelBuffer: 16,
		MaxPending:    20,
		DropTarget:    10,
	})
	s.Start()
	for i := 0; i < 200; i++ {
		s.Submit(newEvent("flood"))
	}

	// Wait for at least one buffer_overflow diagnostic
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		hit := false
		for _, e := range *got {
			if v, _ := e.Get("EventID"); v == "buffer_overflow" {
				hit = true
				break
			}
		}
		mu.Unlock()
		if hit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("did not observe buffer_overflow diagnostic event")
}

func TestSinkCloseDrainsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewSink(Config{URL: srv.URL, BatchSize: 50})
	s.Start()
	for i := 0; i < 100; i++ {
		s.Submit(newEvent("drain"))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSinkCloseReturnsLastErrorOnFailedDrain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewSink(Config{
		URL:            srv.URL,
		BatchSize:      10,
		InitialBackoff: 10 * time.Millisecond,
	})
	s.Start()
	s.Submit(newEvent("fail-on-shutdown"))
	err := s.Close()
	if err == nil {
		t.Fatal("expected error from Close on shutdown post failure")
	}
	if !strings.Contains(err.Error(), "seq ingest failed") {
		t.Fatalf("error = %v, want seq ingest failed", err)
	}
}
```

- [ ] **Step 2: Run tests**

```bash
go test ./seq/...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add seq/sink_test.go
git commit -m "test(seq): cover Sink post/retry/overflow/drain"
```

---

### Task 1.9: Move CLI into `cmd/sing2seq/` with bus-driven wireup

**Files:**
- Create: `cmd/sing2seq/main.go`
- Create: `cmd/sing2seq/pipe.go`
- Create: `cmd/sing2seq/run.go`
- Create: `cmd/sing2seq/pretty.go`
- Create: `cmd/sing2seq/stdout.go`
- Delete: `main.go`, `log.go`, `internal_shim.go` (root)

- [ ] **Step 1: Create cmd/sing2seq/pretty.go**

Internal renderer that subscribes to the bus and writes colored output to stderr. Source-aware formatting:

```go
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/moonfruit/sing2seq/clef"
)

var levelColor = map[string]string{
	"WARN":  "\x1b[33m",
	"ERROR": "\x1b[31m",
	"FATAL": "\x1b[35m",
}

const colorReset = "\x1b[0m"

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type prettyRenderer struct {
	timestamp    bool
	disableColor bool
}

func newPrettyRenderer(timestamp, disableColor bool) *prettyRenderer {
	return &prettyRenderer{timestamp: timestamp, disableColor: disableColor}
}

func (r *prettyRenderer) Match(*clef.Event) bool { return true }

func (r *prettyRenderer) Deliver(e *clef.Event) {
	level := levelShort(getString(e, "@l"))
	source := getString(e, "Source")
	module := getString(e, "Module")

	var head string
	if r.timestamp {
		now := parseRFC3339(getString(e, "@t"))
		head = fmt.Sprintf("%s %s %s %s",
			now.Format("-0700"),
			now.Format("2006-01-02"),
			now.Format("15:04:05.000"),
			r.colorize(level))
	} else {
		head = r.colorize(level)
	}

	body := r.renderBody(e, source, module)
	_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", head, body)
}

// renderBody:
//   sing-box: <Module>[/<Type>][[<Tag>]]: <Detail>  (existing @mt template handles this)
//   sing2seq: sing2seq[/<Module>]: <Detail>
//   else:     fall back to @mt template
func (r *prettyRenderer) renderBody(e *clef.Event, source, module string) string {
	mt := getString(e, "@mt")
	switch source {
	case "sing-box":
		if mt != "" {
			return renderTemplate(e, mt)
		}
		return getString(e, "Detail")
	case "sing2seq":
		var b strings.Builder
		b.WriteString("sing2seq")
		if module != "" {
			b.WriteByte('/')
			b.WriteString(module)
		}
		b.WriteString(": ")
		if mt != "" {
			b.WriteString(renderTemplate(e, mt))
		} else {
			b.WriteString(getString(e, "Detail"))
		}
		return b.String()
	default:
		if mt != "" {
			return renderTemplate(e, mt)
		}
		return getString(e, "Detail")
	}
}

func (r *prettyRenderer) colorize(level string) string {
	if r.disableColor {
		return level
	}
	if c, ok := levelColor[level]; ok {
		return c + level + colorReset
	}
	return level
}

func renderTemplate(e *clef.Event, tmpl string) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := match[1 : len(match)-1]
		if v, ok := e.Get(name); ok {
			return fmt.Sprintf("%v", v)
		}
		return match
	})
}

func getString(e *clef.Event, key string) string {
	if v, ok := e.Get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func levelShort(clefName string) string {
	switch clefName {
	case "Verbose":
		return "TRACE"
	case "Debug":
		return "DEBUG"
	case "Information":
		return "INFO"
	case "Warning":
		return "WARN"
	case "Error":
		return "ERROR"
	case "Fatal":
		return "FATAL"
	default:
		return "INFO"
	}
}

func parseRFC3339(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Now()
}
```

- [ ] **Step 2: Create cmd/sing2seq/stdout.go**

```go
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"

	"github.com/moonfruit/sing2seq/clef"
)

type stdoutSink struct {
	mu  sync.Mutex
	bw  *bufio.Writer
	enc *json.Encoder
}

func newStdoutSink() *stdoutSink {
	bw := bufio.NewWriter(os.Stdout)
	return &stdoutSink{bw: bw, enc: json.NewEncoder(bw)}
}

func (s *stdoutSink) Match(*clef.Event) bool { return true }

func (s *stdoutSink) Deliver(e *clef.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(e)
}

func (s *stdoutSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.bw.Flush()
}
```

- [ ] **Step 3: Create cmd/sing2seq/pipe.go**

```go
package main

import (
	"bufio"
	"io"

	"github.com/moonfruit/sing2seq/clef"
	"github.com/moonfruit/sing2seq/seq"
	"github.com/spf13/pflag"
)

type Pipe struct {
	URL          string
	APIKey       string
	Insecure     bool
	Timestamp    bool
	DisableColor bool
}

func (p *Pipe) Bind(flags *pflag.FlagSet) {
	flags.StringVarP(&p.URL, "url", "u", "", "Seq base URL; if empty, write CLEF JSON to stdout")
	flags.StringVarP(&p.APIKey, "api-key", "k", "", "Seq API key")
	flags.BoolVar(&p.Insecure, "insecure", false, "skip TLS verification")
	flags.BoolVar(&p.Timestamp, "timestamp", false, "include timestamp in pretty stderr output")
	flags.BoolVar(&p.DisableColor, "disable-color", false, "disable color in pretty stderr output")
}

// Run reads sing-box stderr from r line-by-line, parses each line, and fans events
// out via a clef.Bus to:
//   - pretty renderer (always, stderr)
//   - either seq.Sink (when URL != "") or stdoutSink (URL == "")
//
// sing2seq's own diagnostics also flow through the same emitter+bus with
// Source="sing2seq". Returns the last sink error (if any) on shutdown.
func (p *Pipe) Run(r io.Reader) error {
	bus := clef.NewBus(256)
	em := clef.NewEmitter(clef.EmitterConfig{Source: "sing2seq", MinLevel: clef.LevelInfo, Bus: bus})

	// Pretty subscriber (stderr)
	bus.Subscribe(newPrettyRenderer(p.Timestamp, p.DisableColor))

	// Output sink subscriber: stdout JSON or seq.Sink
	var sinkClose func() error
	if p.URL == "" {
		stdout := newStdoutSink()
		bus.Subscribe(stdout)
		sinkClose = func() error { stdout.Flush(); return nil }
	} else {
		sk := seq.NewSink(seq.Config{
			URL: p.URL, APIKey: p.APIKey, Insecure: p.Insecure,
			Emitter: em,
		})
		sk.Start()
		bus.Subscribe(clef.SubscriberFunc{
			MatchFn:   func(*clef.Event) bool { return true },
			DeliverFn: func(e *clef.Event) { sk.Submit(e) },
		})
		sinkClose = sk.Close
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if ev := clef.ParseSingBoxLine(scanner.Text()); ev != nil {
			em.PublishExternal(ev)
		}
	}

	// Close order: sink first, then bus. Sink's drain-time diagnostics still
	// flow to pretty via the still-open bus.
	err := sinkClose()
	bus.Close()
	return err
}
```

- [ ] **Step 4: Create cmd/sing2seq/run.go**

Port from existing `main.go`'s `RunCmd` (exec sing-box, signal forwarding, stderr fan-out via io.Pipe). Key changes: package `main` (cmd subdir is its own main), reference `Pipe` from same package. `errors.AsType` does not exist in stdlib — replace with `errors.As` (the existing code uses a homegrown helper; verify in source and replace).

```go
package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

type RunCmd struct {
	Pipe
	SingBox         string
	Config          []string
	ConfigDirectory []string
	Directory       string
}

func (o *RunCmd) Run(args []string) {
	runArgs := o.buildRunArgs(args)
	cmd := exec.Command(o.SingBox, runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout

	pr, pw := io.Pipe()
	cmd.Stderr = io.MultiWriter(os.Stderr, pw)

	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(os.Stderr, "FATAL failed to start "+o.SingBox+": "+err.Error()+"\n")
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
		_ = pw.Close()
	}()

	_ = o.Pipe.Run(pr)

	err := <-waitErr
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "ERROR "+o.SingBox+" exited: "+err.Error()+"\n")
		os.Exit(1)
	}
}

func (o *RunCmd) buildRunArgs(args []string) []string {
	runArgs := []string{"run"}
	if o.Timestamp {
		f, err := os.CreateTemp("", "sing2seq-timestamp-*.json")
		if err != nil {
			_, _ = io.WriteString(os.Stderr, "FATAL failed to create timestamp config: "+err.Error()+"\n")
			os.Exit(1)
		}
		if _, err := f.WriteString(`{"log":{"timestamp":true}}`); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			_, _ = io.WriteString(os.Stderr, "FATAL failed to write timestamp config: "+err.Error()+"\n")
			os.Exit(1)
		}
		_ = f.Close()
		defer func() { _ = os.Remove(f.Name()) }()
		runArgs = append(runArgs, "-c", f.Name())
	}
	for _, c := range o.Config {
		runArgs = append(runArgs, "-c", c)
	}
	for _, c := range o.ConfigDirectory {
		runArgs = append(runArgs, "-C", c)
	}
	if o.Directory != "" {
		runArgs = append(runArgs, "-D", o.Directory)
	}
	if o.DisableColor {
		runArgs = append(runArgs, "--disable-color")
	}
	return append(runArgs, args...)
}
```

- [ ] **Step 5: Create cmd/sing2seq/main.go**

```go
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "main"

func main() {
	pipe := &Pipe{}
	pipeRun := func(cmd *cobra.Command, args []string) { _ = pipe.Run(os.Stdin) }

	pipeCmd := &cobra.Command{
		Use:   "pipe",
		Short: "Read sing-box logs from stdin and forward to Seq",
		Run:   pipeRun,
	}
	pipe.Bind(pipeCmd.Flags())

	runOpts := &RunCmd{}
	runCmd := &cobra.Command{
		Use:   "run [flags] [-- sing-box-args...]",
		Short: "Spawn sing-box and forward its stderr logs to Seq",
		Run:   func(cmd *cobra.Command, args []string) { runOpts.Run(args) },
	}
	runOpts.Pipe.Bind(runCmd.Flags())
	runCmd.Flags().StringVarP(&runOpts.SingBox, "sing-box", "p", "sing-box", "sing-box command to spawn")
	runCmd.Flags().StringArrayVarP(&runOpts.Config, "config", "c", nil, "sing-box configuration file path")
	runCmd.Flags().StringArrayVarP(&runOpts.ConfigDirectory, "config-directory", "C", nil, "sing-box configuration directory path")
	runCmd.Flags().StringVarP(&runOpts.Directory, "directory", "D", "", "sing-box working directory")

	rootCmd := &cobra.Command{
		Use:     "sing2seq",
		Short:   "Forward sing-box logs to Seq",
		Version: version,
		Run:     pipeRun,
	}
	rootCmd.Flags().AddFlagSet(pipeCmd.Flags())
	rootCmd.AddCommand(pipeCmd, runCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 6: Delete root-level legacy files**

```bash
git rm main.go log.go internal_shim.go cmd/sing2seq/.gitkeep
```

- [ ] **Step 7: Verify whole module builds & tests pass**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/sing2seq/main.go cmd/sing2seq/pipe.go cmd/sing2seq/run.go cmd/sing2seq/pretty.go cmd/sing2seq/stdout.go
git rm main.go log.go internal_shim.go cmd/sing2seq/.gitkeep
git commit -m "feat(cmd): move CLI into cmd/sing2seq with bus-driven wireup"
```

---

### Task 1.10: Smoke-test the binary end-to-end

**Files:** none (manual verification)

- [ ] **Step 1: Build the binary**

```bash
go build -o sing2seq ./cmd/sing2seq
```

- [ ] **Step 2: Test pipe mode with stdin (no Seq)**

Feed a known sing-box-format line to stdin and confirm pretty stderr + stdout JSON both fire:

```bash
printf '+0800 2026-05-05 10:00:00 INFO router/route: matched rule\n' | ./sing2seq
```

Expected:
- stdout: a single CLEF JSON line with `"Source":"sing-box"` and `"@mt":"router/route: {Detail}"` (or similar)
- stderr: a colored line like `INFO  router/route: matched rule`

- [ ] **Step 3: Test pipe mode with mock Seq**

```bash
# Terminal A: minimal mock
python3 -c '
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n=int(self.headers.get("Content-Length") or 0); body=self.rfile.read(n)
        sys.stderr.write(f"got {n} bytes: {body[:120]!r}...\n")
        self.send_response(200); self.end_headers()
http.server.HTTPServer(("127.0.0.1",5341),H).serve_forever()
'

# Terminal B:
printf '+0800 2026-05-05 10:00:00 INFO router/route: matched rule\n' | ./sing2seq -u http://127.0.0.1:5341
```

Expected: Terminal A logs `got NN bytes:` line(s) showing CLEF ndjson. Terminal B prints colored stderr only (no stdout JSON).

- [ ] **Step 4: Verify shutdown drain by quitting and checking sink Close error**

Above commands exit with status 0 (no shutdown errors).

- [ ] **Step 5: Cleanup binary, no commit**

```bash
rm -f sing2seq
```

(Optionally update `.gitignore` to exclude built binary.)

---

### Task 1.11: Update README and CLAUDE.md, push, tag v1.3.0

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update README.md**

Replace existing usage section with new public API surface. Add:
- Library import paths (`github.com/moonfruit/sing2seq/clef`, `github.com/moonfruit/sing2seq/seq`)
- Brief usage example for each
- Note module path migration: pre-v1.3.0 was `module sing2seq` (unimportable); v1.3.0+ is `github.com/moonfruit/sing2seq`
- Note CLI behavior change: `-u ""` debug mode now prints both stdout JSON and pretty stderr
- Diagnostic event schema (`Source=sing2seq, Module=seq.sink, EventID ∈ {buffer_overflow, post_failed, shutdown_post_failed}`)

- [ ] **Step 2: Update CLAUDE.md**

Replace the architecture section with: `clef/` (Event, Level, parser, Emitter, Bus), `seq/` (Sink), `cmd/sing2seq/` (CLI). Drop the old "all in main package" descriptions.

- [ ] **Step 3: Run full test suite once more**

```bash
go test ./...
go vet ./...
```

Expected: PASS.

- [ ] **Step 4: Commit docs**

```bash
git add README.md CLAUDE.md
git commit -m "docs: update for clef/seq library + cmd/sing2seq layout"
```

- [ ] **Step 5: Merge feature branch to main, tag, push**

```bash
git checkout main
git merge --no-ff feature/lib-refactor -m "Release v1.3.0: library refactor"
git tag -a v1.3.0 -m "v1.3.0: extract clef and seq packages"
git push origin main
git push origin v1.3.0
```

Expected: tag visible at `https://github.com/moonfruit/sing2seq/releases/tag/v1.3.0`.

---

## Phase 2 — sing-router Step A (parser/Event replacement)

> **Working directory:** `/Users/moon/Workspace.localized/proxy/sing-router`
> **Branch:** `feature/clef-parser-import` (create at start)
> **Precondition:** Phase 1 tag `v1.3.0` is published to `github.com/moonfruit/sing2seq`.

### Task 2.0: Create working branch

- [ ] **Step 1**

```bash
cd /Users/moon/Workspace.localized/proxy/sing-router
git checkout -b feature/clef-parser-import
git status
```

Expected: clean tree.

---

### Task 2.1: Add `require` for sing2seq with local `replace`

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add require + local replace**

```bash
go mod edit -require=github.com/moonfruit/sing2seq@v1.3.0
go mod edit -replace=github.com/moonfruit/sing2seq=/Users/moon/Workspace.localized/go/mod/sing2seq
go mod tidy
```

- [ ] **Step 2: Verify module resolves**

```bash
go list -m github.com/moonfruit/sing2seq
```

Expected: `github.com/moonfruit/sing2seq v1.3.0 => /Users/moon/Workspace.localized/go/mod/sing2seq`.

- [ ] **Step 3: Commit (with replace; will drop later)**

```bash
git add go.mod go.sum
git commit -m "chore: require github.com/moonfruit/sing2seq v1.3.0 (local replace)"
```

---

### Task 2.2: Replace `OrderedEvent` with type alias to `clef.Event`

**Files:**
- Modify: `internal/log/clef.go`
- Delete: `internal/log/parser.go`, `internal/log/parser_test.go`

- [ ] **Step 1: Replace clef.go contents with alias**

```go
// Package log 提供 sing-router 的结构化日志原语（Writer/Pretty/Level/wireup）。
// 事件类型与解析器来自 github.com/moonfruit/sing2seq/clef。
package log

import "github.com/moonfruit/sing2seq/clef"

// OrderedEvent 是 clef.Event 的别名，保留旧名以最小化本步骤的改动面。
// Step B 完成后此别名会被删除，调用方直接使用 clef.Event。
type OrderedEvent = clef.Event

// NewEvent 别名 clef.NewEvent。
var NewEvent = clef.NewEvent

// ParseSingBoxLine 别名 clef.ParseSingBoxLine。
var ParseSingBoxLine = clef.ParseSingBoxLine
```

- [ ] **Step 2: Delete the now-redundant parser**

```bash
git rm internal/log/parser.go internal/log/parser_test.go
```

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./...
```

Expected: PASS. (Existing supervisor / cli / daemon code uses `log.OrderedEvent` and `log.ParseSingBoxLine` which now resolve via aliases. `log.NewEvent` calls work via var alias.)

- [ ] **Step 4: Commit**

```bash
git add internal/log/clef.go
git rm internal/log/parser.go internal/log/parser_test.go
git commit -m "refactor(log): alias OrderedEvent/Parser to clef package"
```

---

### Task 2.3: Byte-level regression check

**Files:** none

- [ ] **Step 1: Capture deterministic input/output sample**

Pick a representative sing-box stderr line (or several). Run them through the *current* `log.ParseSingBoxLine` (post-aliasing) and through the *previous* implementation by checking out main into a temp dir.

```bash
# Build a tiny harness.
cat > /tmp/parse_check.go <<'EOF'
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/moonfruit/sing-router/internal/log"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		ev := log.ParseSingBoxLine(sc.Text())
		if ev == nil {
			continue
		}
		b, _ := json.Marshal(ev)
		fmt.Println(string(b))
	}
}
EOF

# Run on this branch
go run /tmp/parse_check.go < testdata/sample-singbox.log > /tmp/after.jsonl

# Run on main
git stash
git checkout main
go run /tmp/parse_check.go < testdata/sample-singbox.log > /tmp/before.jsonl
git checkout -
git stash pop

diff /tmp/before.jsonl /tmp/after.jsonl
```

Expected: empty diff. If there are differences in field order or content, investigate before proceeding.

If `testdata/sample-singbox.log` does not exist, generate a small fixture from a real daemon run, or skip this step and rely on the test suite (which contains parser_test.go cases that already exist on main). The parser was ported verbatim, so functional regression is unlikely; this is a belt-and-suspenders check.

- [ ] **Step 2: No commit; cleanup**

```bash
rm -f /tmp/parse_check.go /tmp/before.jsonl /tmp/after.jsonl
```

---

### Task 2.4: Drop `replace`, push, merge

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Drop replace**

```bash
go mod edit -dropreplace=github.com/moonfruit/sing2seq
go mod tidy
```

- [ ] **Step 2: Verify build & test against published v1.3.0**

```bash
go build ./...
go test ./...
```

Expected: PASS (now resolving against `github.com/moonfruit/sing2seq@v1.3.0` from the module proxy).

- [ ] **Step 3: Commit & merge**

```bash
git add go.mod go.sum
git commit -m "chore: drop local replace for sing2seq; pin v1.3.0"
git checkout main
git merge --no-ff feature/clef-parser-import -m "Step A: parser/Event imported from clef"
git push origin main
```

---

## Phase 3 — sing-router Step B (Emitter/Bus retirement)

> **Working directory:** `/Users/moon/Workspace.localized/proxy/sing-router`
> **Branch:** `feature/clef-emitter-bus` (create at start, base on main with Step A merged)

### Task 3.0: Create working branch

- [ ] **Step 1**

```bash
cd /Users/moon/Workspace.localized/proxy/sing-router
git checkout main
git pull
git checkout -b feature/clef-emitter-bus
```

---

### Task 3.1: Convert `internal/log/level.go` to alias

**Files:**
- Modify: `internal/log/level.go`

- [ ] **Step 1: Replace level.go contents**

```go
package log

import "github.com/moonfruit/sing2seq/clef"

type Level = clef.Level

const (
	LevelTrace = clef.LevelTrace
	LevelDebug = clef.LevelDebug
	LevelInfo  = clef.LevelInfo
	LevelWarn  = clef.LevelWarn
	LevelError = clef.LevelError
	LevelFatal = clef.LevelFatal
)

var (
	ParseLevel    = clef.ParseLevel
	FromCLEFName  = clef.FromCLEFName
)
```

- [ ] **Step 2: Verify level_test.go still passes**

The existing `internal/log/level_test.go` asserts behavior of `Level.String()`, `Level.CLEFName()`, `ParseLevel`. These all delegate to `clef.Level` now via the alias.

```bash
go test ./internal/log/... -run TestLevel
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/log/level.go
git commit -m "refactor(log): alias Level to clef.Level"
```

---

### Task 3.2: Add `internal/log/wireup.go` (new helper)

**Files:**
- Create: `internal/log/wireup.go`

- [ ] **Step 1: Define wireup that constructs Bus + Emitter and subscribes Writer/Pretty**

```go
package log

import (
	"github.com/moonfruit/sing2seq/clef"
)

// EmitterStack 是 daemon 的事件管道：一个 Bus 居中，Emitter 入口、Writer + 可选
// pretty subscriber 出口。Close 关闭顺序：先 Bus（drain subscribers），再 Writer。
type EmitterStack struct {
	Bus     *clef.Bus
	Emitter *clef.Emitter
	Writer  *Writer

	writerSub clef.SubscriptionHandle
}

// StackConfig 配置 EmitterStack。
type StackConfig struct {
	Source       string  // 必填
	MinLevel     Level   // Emitter 过滤
	Writer       *Writer // 必填；订阅 Bus 接收所有事件
	BusBuffer    int     // 每订阅方 buffer；<= 0 时取默认 256（高于 clef 默认 64，防止 Writer drop）
}

// NewEmitterStack 构造 Bus + Emitter，并把 Writer 注册为 bus subscriber。
func NewEmitterStack(cfg StackConfig) *EmitterStack {
	if cfg.BusBuffer <= 0 {
		cfg.BusBuffer = 256
	}
	bus := clef.NewBus(cfg.BusBuffer)
	em := clef.NewEmitter(clef.EmitterConfig{
		Source:   cfg.Source,
		MinLevel: cfg.MinLevel,
		Bus:      bus,
	})
	stack := &EmitterStack{Bus: bus, Emitter: em, Writer: cfg.Writer}
	stack.writerSub = bus.Subscribe(clef.SubscriberFunc{
		MatchFn:   func(*clef.Event) bool { return true },
		DeliverFn: func(e *clef.Event) { _ = cfg.Writer.Write(e) },
	})
	return stack
}

// Close 取消 writer 订阅，关闭 Bus，最后关闭 Writer。
// 关闭后调用 Emitter 的方法是 no-op。
func (s *EmitterStack) Close() error {
	s.writerSub.Unsubscribe()
	s.Bus.Close()
	if s.Writer != nil {
		return s.Writer.Close()
	}
	return nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/log/...
```

Expected: PASS.

- [ ] **Step 3: Add a wireup_test.go**

```go
package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = w.Sync()
		data, _ := os.ReadFile(filepath.Join(dir, "test.log"))
		if strings.Contains(string(data), "supervisor") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := stack.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.log"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 1 {
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
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/log/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/log/wireup.go internal/log/wireup_test.go
git commit -m "feat(log): add EmitterStack helper wiring Bus + Writer subscriber"
```

---

### Task 3.3: Migrate `internal/cli/wireup_daemon.go` to use `EmitterStack`

**Files:**
- Modify: `internal/cli/wireup_daemon.go`

- [ ] **Step 1: Read current wireup**

Look at `internal/cli/wireup_daemon.go` lines around the existing emitter construction (`log.NewEmitter`/`log.EmitterConfig`/`log.NewBus`). Replace the construction block with a call to `log.NewEmitterStack`. Wherever the old code separately created `log.Bus` and passed it to `log.NewEmitter`, the new code constructs both via `EmitterStack`.

Example diff sketch (exact lines depend on current code):

```go
// Before:
//   level, _ := log.ParseLevel(cfg.Log.Level)
//   writer, err := log.NewWriter(log.WriterConfig{...})
//   bus := log.NewBus(256)
//   em := log.NewEmitter(log.EmitterConfig{Source: "daemon", MinLevel: level, Writer: writer, Bus: bus})

// After:
level, _ := log.ParseLevel(cfg.Log.Level)
writer, err := log.NewWriter(log.WriterConfig{
    Path:       cfg.Log.Path,
    MaxSize:    cfg.Log.MaxSize,
    MaxBackups: cfg.Log.MaxBackups,
    Gzip:       cfg.Log.Gzip,
})
if err != nil {
    return nil, err
}
stack := log.NewEmitterStack(log.StackConfig{
    Source:   "daemon",
    MinLevel: level,
    Writer:   writer,
})
// Pass stack.Emitter / stack.Bus into daemon.Config; close stack on shutdown.
```

Ensure shutdown path calls `stack.Close()` instead of `writer.Close()` directly (stack.Close handles both).

- [ ] **Step 2: Build & test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/wireup_daemon.go
git commit -m "refactor(cli): use log.EmitterStack for daemon wireup"
```

---

### Task 3.4: Migrate daemon types to `clef.Emitter` direct references

**Files:**
- Modify: `internal/daemon/daemon.go`, `internal/daemon/api.go`, `internal/daemon/supervisor.go`, `internal/daemon/supervisor_test.go`

- [ ] **Step 1: Replace `*log.Emitter` with `*clef.Emitter`**

In each daemon file, change struct fields and function signatures:

```go
// Before:
type Config struct {
    Emitter *log.Emitter
    ...
}

// After:
type Config struct {
    Emitter *clef.Emitter
    ...
}
```

Also update imports: `"github.com/moonfruit/sing-router/internal/log"` may still be needed for `log.Writer` etc.; add `"github.com/moonfruit/sing2seq/clef"` where Emitter is referenced.

In `supervisor_test.go`, the `newTestEmitter` helper currently does:
```go
em := log.NewEmitter(log.EmitterConfig{Source: "test", MinLevel: log.LevelInfo, Writer: w, Bus: bus})
```
Change to use `log.NewEmitterStack` and return `stack.Emitter`, with cleanup via `t.Cleanup(func(){ _ = stack.Close() })`.

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/
git commit -m "refactor(daemon): use *clef.Emitter directly"
```

---

### Task 3.5: Migrate `internal/cli/logs.go` and any remaining `log.OrderedEvent` references

**Files:**
- Modify: `internal/cli/logs.go`

- [ ] **Step 1: Replace `log.OrderedEvent` with `clef.Event`**

Find all references:

```bash
rg -n "log\\.OrderedEvent|log\\.NewEvent" internal/
```

For each, change to `clef.Event` / `clef.NewEvent`. Add `"github.com/moonfruit/sing2seq/clef"` to imports.

For example, `decodeOrderedEvent(line string) (*log.OrderedEvent, error)` → `decodeEvent(line string) (*clef.Event, error)` (and update the caller).

- [ ] **Step 2: Build & test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/cli/logs.go
git commit -m "refactor(cli): use clef.Event in logs decode path"
```

---

### Task 3.6: Delete obsolete `internal/log` files

**Files:**
- Delete: `internal/log/clef.go`, `internal/log/clef_test.go`, `internal/log/emitter.go`, `internal/log/emitter_test.go`, `internal/log/bus.go`, `internal/log/bus_test.go`

- [ ] **Step 1: Verify no callers remain**

```bash
rg -n "log\\.OrderedEvent|log\\.NewEvent|log\\.NewEmitter|log\\.EmitterConfig|log\\.NewBus|log\\.SubscriberFunc|log\\.SubscriptionHandle|log\\.Subscriber\\b|log\\.ParseSingBoxLine" .
```

Expected: no hits (they've all been migrated). If any survive, fix them first.

- [ ] **Step 2: Delete files**

```bash
git rm internal/log/clef.go internal/log/clef_test.go
git rm internal/log/emitter.go internal/log/emitter_test.go
git rm internal/log/bus.go internal/log/bus_test.go
```

- [ ] **Step 3: Build & test**

```bash
go build ./...
go test ./...
```

Expected: PASS. `internal/log` now contains only: `level.go` (alias), `writer.go`, `writer_test.go`, `pretty.go`, `pretty_test.go`, `wireup.go`, `wireup_test.go`.

- [ ] **Step 4: Commit**

```bash
git commit -m "refactor(log): remove duplicated Event/Emitter/Bus; clef package authoritative"
```

---

### Task 3.7: End-to-end smoke and merge

**Files:** none

- [ ] **Step 1: Build daemon**

```bash
go build -o /tmp/sing-router ./cmd/sing-router
```

- [ ] **Step 2: Run daemon against a test config and inspect CLEF file output**

(Specific procedure depends on dev setup — typical: launch daemon, generate a small amount of sing-box traffic, examine `<rundir>/sing-router.log`. Confirm:
- Events have ordered fields (`@t`, `@l`, `@mt?`, `Source`, `Module?`, `EventID?`, ...).
- `Source` is `"daemon"` for daemon's own events and `"sing-box"` for parsed events.
- `logs` command renders pretty output as before.)

- [ ] **Step 3: Run full test suite**

```bash
go test ./...
go vet ./...
```

Expected: PASS.

- [ ] **Step 4: Merge to main**

```bash
git checkout main
git merge --no-ff feature/clef-emitter-bus -m "Step B: retire local Emitter/Bus; use clef package"
git push origin main
```

---

## Self-Review

**Spec coverage check** — every spec section maps to tasks:

| Spec section | Task(s) |
|---|---|
| §1 Architecture (sing2seq layout) | 1.0–1.11 |
| §1 Architecture (sing-router changes) | Phase 2 + Phase 3 |
| §2 clef.Event API | 1.2 |
| §2 clef.Level API | 1.3 |
| §2 ParseSingBoxLine | 1.4 |
| §2 clef.Emitter API | 1.6 |
| §2 clef.Bus API | 1.5 |
| §2 Recursion safety | 1.7 (sink.diag invariant) |
| §3 seq.Sink API | 1.7, 1.8 |
| §3 Diagnostic event schema | 1.7 (sink.diag), 1.11 (README) |
| §4 CLI wireup, close order | 1.9 |
| §4 Pretty rules (Source-aware) | 1.9 (cmd/sing2seq/pretty.go) |
| §4 -u "" both pretty + stdout | 1.9 |
| §5 Step A | 2.1–2.4 |
| §5 Step B | 3.1–3.7 |
| §5 replace flow | 2.1, 2.4 |
| §6 Testing strategy (sing2seq) | 1.4 (parser), 1.5 (bus), 1.6 (emitter), 1.8 (sink) |
| §6 Testing (sing-router) | 3.2 (wireup_test), existing tests under 2.x and 3.x verification steps |
| §6 Regression bytes | 2.3 |
| §7 v1.3.0 release | 1.11 |

**Placeholder scan**: searched for "TBD"/"TODO"/"implement later" — none in plan.

**Type consistency**:
- `clef.Event` (Task 1.2) — used in 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 2.2, 3.5 ✓
- `clef.NewBus(perSubBuffer int)` (Task 1.5) — used in 1.6, 1.7, 1.8, 1.9, 3.2 ✓
- `clef.Subscriber` / `SubscriberFunc{MatchFn, DeliverFn}` (Task 1.5) — used in 1.6 test, 1.7, 1.8 captureBus, 1.9 stdout/seq subscribers, 3.2 ✓
- `clef.Emitter.PublishExternal` (Task 1.6) — used in 1.9, daemon supervisor (existing) ✓
- `seq.NewSink(seq.Config{...})` / `Start()` / `Submit(*clef.Event)` / `Close() error` (Task 1.7) — used in 1.9 ✓
- `log.EmitterStack`, `log.StackConfig` (Task 3.2) — used in 3.3, 3.4 ✓

**Risks / known gaps**:
- Task 1.9 references `errors.AsType` from existing sing2seq main.go; replaced with stdlib `errors.As`. If the existing source uses a homegrown helper of a different name, swap accordingly.
- Task 2.3 byte-diff regression check requires a pre-existing fixture or a manual run. The parser is ported verbatim, so the test suite alone provides reasonable coverage; the byte diff is a belt-and-suspenders check.
- Task 3.3 wireup migration depends on the exact layout of `internal/cli/wireup_daemon.go`, which the executor must read first. Sketch supplied; concrete edits land at execution time.

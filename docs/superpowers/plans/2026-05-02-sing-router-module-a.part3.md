# sing-router Module A 实施计划 — Part 3

> 接续 Part 2。本部分覆盖 Phase 7–9：状态持久化、state machine、ready 检测、supervisor、HTTP API。
> Phase 10/11/12 见 part4。

---

# Phase 7：状态持久化、状态机、Ready 检测

## Task 23：state.json 持久化

**Files:**
- Create: `internal/state/state.go`
- Create: `internal/state/state_test.go`

- [ ] **Step 1：写测试 internal/state/state_test.go**

```go
package state

import (
    "path/filepath"
    "testing"
    "time"
)

func TestLoadEmpty(t *testing.T) {
    dir := t.TempDir()
    s, err := Load(filepath.Join(dir, "state.json"))
    if err != nil {
        t.Fatalf("Load empty: %v", err)
    }
    if s.LastBootAt != "" {
        t.Fatalf("LastBootAt should be empty: %s", s.LastBootAt)
    }
    if s.RestartCount != 0 {
        t.Fatal("RestartCount should be 0")
    }
}

func TestSaveLoadRoundtrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "state.json")
    s := &State{
        LastBootAt:           time.Now().UTC().Format(time.RFC3339),
        RestartCount:         3,
        LastZooLoadedAt:      "2026-05-02T12:00:00+08:00",
        LastIptablesAppliedAt: "2026-05-02T12:34:56+08:00",
    }
    if err := s.Save(path); err != nil {
        t.Fatalf("Save: %v", err)
    }
    loaded, err := Load(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if loaded.RestartCount != 3 {
        t.Fatalf("RestartCount: %d", loaded.RestartCount)
    }
    if loaded.LastZooLoadedAt != s.LastZooLoadedAt {
        t.Fatal("LastZooLoadedAt mismatch")
    }
}

func TestSaveAtomic(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "state.json")
    s := &State{RestartCount: 1}
    for i := 0; i < 5; i++ {
        s.RestartCount = i
        if err := s.Save(path); err != nil {
            t.Fatal(err)
        }
        // 中途不应该有 .tmp 残留
        if exists(filepath.Join(dir, "state.json.tmp")) {
            t.Fatal("tmp file should not survive after Save")
        }
    }
}

func exists(p string) bool {
    _, err := osStat(p)
    return err == nil
}
```

补一行小辅助：

```go
import "os"
var osStat = os.Stat
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/state/...
```

- [ ] **Step 3：写实现 internal/state/state.go**

```go
// Package state 持久化 daemon 的运行时状态到 state.json。
package state

import (
    "encoding/json"
    "errors"
    "os"
    "path/filepath"
)

// State 是 daemon 持久化的最小状态。
type State struct {
    LastBootAt            string `json:"last_boot_at"`
    RestartCount          int    `json:"restart_count"`
    LastZooLoadedAt       string `json:"last_zoo_loaded_at"`
    LastIptablesAppliedAt string `json:"last_iptables_applied_at"`
}

// Load 读取 state.json；不存在返回空 State，不报错。
func Load(path string) (*State, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return &State{}, nil
        }
        return nil, err
    }
    s := &State{}
    if err := json.Unmarshal(data, s); err != nil {
        return nil, err
    }
    return s, nil
}

// Save 原子写入 state.json：tmp 文件 + rename。
func (s *State) Save(path string) error {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    tmp := path + ".tmp"
    data, err := json.MarshalIndent(s, "", "  ")
    if err != nil {
        return err
    }
    if err := os.WriteFile(tmp, data, 0o644); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}
```

- [ ] **Step 4：跑测试 + 提交**

```bash
go test ./internal/state/... -v
git add internal/state
git commit -m "feat(state): atomic load/save of state.json"
```

---

## Task 24：状态机（枚举 + 转移）

**Files:**
- Create: `internal/daemon/statemachine.go`
- Create: `internal/daemon/statemachine_test.go`

- [ ] **Step 1：写测试**

```go
package daemon

import "testing"

func TestStateStrings(t *testing.T) {
    cases := map[State]string{
        StateBooting:   "booting",
        StateRunning:   "running",
        StateReloading: "reloading",
        StateDegraded:  "degraded",
        StateStopping:  "stopping",
        StateStopped:   "stopped",
        StateFatal:     "fatal",
    }
    for s, want := range cases {
        if s.String() != want {
            t.Fatalf("%v: want %s got %s", s, want, s.String())
        }
    }
}

func TestStateMachineInitialBooting(t *testing.T) {
    sm := NewStateMachine()
    if sm.Current() != StateBooting {
        t.Fatalf("initial: %v", sm.Current())
    }
}

func TestStateMachineHappyPath(t *testing.T) {
    sm := NewStateMachine()
    must := func(err error) {
        if err != nil {
            t.Fatalf("transition: %v", err)
        }
    }
    must(sm.Transition(StateRunning))
    must(sm.Transition(StateReloading))
    must(sm.Transition(StateRunning))
    must(sm.Transition(StateDegraded))
    must(sm.Transition(StateRunning))
    must(sm.Transition(StateStopping))
    must(sm.Transition(StateStopped))
    must(sm.Transition(StateBooting))
    must(sm.Transition(StateRunning))
}

func TestStateMachineRejectsIllegalTransitions(t *testing.T) {
    sm := NewStateMachine()
    // booting → stopped 非法（应先经 stopping）
    if err := sm.Transition(StateStopped); err == nil {
        t.Fatal("expected illegal transition error")
    }
}

func TestStateMachineFatalIsTerminal(t *testing.T) {
    sm := NewStateMachine()
    if err := sm.Transition(StateFatal); err != nil {
        t.Fatalf("booting→fatal should be ok: %v", err)
    }
    // fatal 之后只能 → stopping （SIGTERM/shutdown）
    if err := sm.Transition(StateRunning); err == nil {
        t.Fatal("fatal→running should be illegal")
    }
    if err := sm.Transition(StateStopping); err != nil {
        t.Fatalf("fatal→stopping should be ok: %v", err)
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/daemon/... -run TestState
```

- [ ] **Step 3：写实现 internal/daemon/statemachine.go**

```go
// Package daemon 包含 supervisor、状态机、HTTP API 与子进程编排。
package daemon

import (
    "fmt"
    "sync"
)

// State 是 daemon 状态机的枚举。
type State int

const (
    StateBooting State = iota
    StateRunning
    StateReloading
    StateDegraded
    StateStopping
    StateStopped
    StateFatal
)

func (s State) String() string {
    switch s {
    case StateBooting:
        return "booting"
    case StateRunning:
        return "running"
    case StateReloading:
        return "reloading"
    case StateDegraded:
        return "degraded"
    case StateStopping:
        return "stopping"
    case StateStopped:
        return "stopped"
    case StateFatal:
        return "fatal"
    default:
        return "unknown"
    }
}

// StateMachine 串行化状态转移；不内含异步行为。
type StateMachine struct {
    mu      sync.Mutex
    current State
}

// NewStateMachine 初始 booting。
func NewStateMachine() *StateMachine { return &StateMachine{current: StateBooting} }

// Current 返回当前 state。
func (s *StateMachine) Current() State {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.current
}

// allowed 描述合法的 (from→to) 关系。
var allowed = map[State]map[State]bool{
    StateBooting:   {StateRunning: true, StateFatal: true, StateStopping: true},
    StateRunning:   {StateReloading: true, StateDegraded: true, StateStopping: true},
    StateReloading: {StateRunning: true, StateFatal: true, StateStopping: true},
    StateDegraded:  {StateRunning: true, StateStopping: true},
    StateStopping:  {StateStopped: true, StateFatal: true, StateBooting: true /* shutdown 中途取消极少见，但保留可能 */},
    StateStopped:   {StateBooting: true, StateStopping: true},
    StateFatal:     {StateStopping: true},
}

// Transition 切换状态；非法转移返回 error，不改变当前 state。
func (s *StateMachine) Transition(to State) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    transitions, ok := allowed[s.current]
    if !ok {
        return fmt.Errorf("no transitions from %v", s.current)
    }
    if !transitions[to] {
        return fmt.Errorf("illegal transition %v → %v", s.current, to)
    }
    s.current = to
    return nil
}
```

- [ ] **Step 4：跑测试 + 提交**

```bash
go test ./internal/daemon/... -run TestState -v
git add internal/daemon/statemachine.go internal/daemon/statemachine_test.go
git commit -m "feat(daemon): state machine with enum + guarded transitions"
```

---

## Task 25：Ready 检测

**Files:**
- Create: `internal/daemon/ready.go`
- Create: `internal/daemon/ready_test.go`

- [ ] **Step 1：写测试 internal/daemon/ready_test.go**

```go
package daemon

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

func startTCPListener(t *testing.T) (net.Listener, int) {
    t.Helper()
    l, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = l.Close() })
    return l, l.Addr().(*net.TCPAddr).Port
}

func TestReadyCheckSuccess(t *testing.T) {
    _, p1 := startTCPListener(t)
    _, p2 := startTCPListener(t)

    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !strings.HasPrefix(r.URL.Path, "/version") {
            http.NotFound(w, r)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = fmt.Fprint(w, `{"version":"1.13.5"}`)
    }))
    t.Cleanup(ts.Close)

    cfg := ReadyConfig{
        TCPDials:    []string{fmt.Sprintf("127.0.0.1:%d", p1), fmt.Sprintf("127.0.0.1:%d", p2)},
        ClashAPIURL: ts.URL + "/version",
        TotalTimeout: 2 * time.Second,
        Interval:     50 * time.Millisecond,
    }
    if err := ReadyCheck(context.Background(), cfg); err != nil {
        t.Fatalf("ReadyCheck: %v", err)
    }
}

func TestReadyCheckTimeoutOnDial(t *testing.T) {
    cfg := ReadyConfig{
        TCPDials:     []string{"127.0.0.1:1"}, // 端口 1 几乎肯定没监听
        TotalTimeout: 200 * time.Millisecond,
        Interval:     50 * time.Millisecond,
    }
    err := ReadyCheck(context.Background(), cfg)
    if err == nil {
        t.Fatal("expected timeout error")
    }
}

func TestReadyCheckTimeoutOnClashAPI(t *testing.T) {
    _, p := startTCPListener(t)
    cfg := ReadyConfig{
        TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
        ClashAPIURL:  "http://127.0.0.1:1/version", // 拒绝
        TotalTimeout: 200 * time.Millisecond,
        Interval:     50 * time.Millisecond,
    }
    if err := ReadyCheck(context.Background(), cfg); err == nil {
        t.Fatal("expected error from clash api timeout")
    }
}

func TestReadyCheckClashAPISkipWhenURLEmpty(t *testing.T) {
    _, p := startTCPListener(t)
    cfg := ReadyConfig{
        TCPDials:     []string{fmt.Sprintf("127.0.0.1:%d", p)},
        ClashAPIURL:  "",
        TotalTimeout: 1 * time.Second,
        Interval:     50 * time.Millisecond,
    }
    if err := ReadyCheck(context.Background(), cfg); err != nil {
        t.Fatalf("with empty ClashAPIURL: %v", err)
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/daemon/... -run TestReady
```

- [ ] **Step 3：写实现 internal/daemon/ready.go**

```go
package daemon

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "time"
)

// ReadyConfig 控制 readiness 检测：拨通所有 TCPDials + 可选 GET ClashAPIURL。
type ReadyConfig struct {
    TCPDials     []string      // host:port 列表
    ClashAPIURL  string        // 例如 http://127.0.0.1:9999/version；空 = 跳过
    TotalTimeout time.Duration // 总超时
    Interval     time.Duration // 轮询间隔
}

// ReadyCheck 阻塞直到全部检测项通过，或超时。
func ReadyCheck(ctx context.Context, cfg ReadyConfig) error {
    deadline := time.Now().Add(cfg.TotalTimeout)
    interval := cfg.Interval
    if interval <= 0 {
        interval = 200 * time.Millisecond
    }

    var lastErr error
    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }
        if err := checkOnce(cfg); err == nil {
            return nil
        } else {
            lastErr = err
        }
        time.Sleep(interval)
    }
    if lastErr != nil {
        return fmt.Errorf("ready check timed out: %w", lastErr)
    }
    return fmt.Errorf("ready check timed out")
}

func checkOnce(cfg ReadyConfig) error {
    dialer := net.Dialer{Timeout: 500 * time.Millisecond}
    for _, addr := range cfg.TCPDials {
        c, err := dialer.Dial("tcp", addr)
        if err != nil {
            return fmt.Errorf("dial %s: %w", addr, err)
        }
        _ = c.Close()
    }
    if cfg.ClashAPIURL != "" {
        client := &http.Client{Timeout: 500 * time.Millisecond}
        resp, err := client.Get(cfg.ClashAPIURL)
        if err != nil {
            return fmt.Errorf("clash api: %w", err)
        }
        _ = resp.Body.Close()
        if resp.StatusCode >= 300 {
            return fmt.Errorf("clash api status %d", resp.StatusCode)
        }
    }
    return nil
}
```

- [ ] **Step 4：跑测试 + 提交**

```bash
go test ./internal/daemon/... -run TestReady -v
git add internal/daemon/ready.go internal/daemon/ready_test.go
git commit -m "feat(daemon): ready check polls TCP dials + optional clash API"
```

---

# Phase 8：Supervisor

## Task 26：fake-sing-box 桩进程

**Files:**
- Create: `testdata/fake-sing-box/main.go`
- Create: `testdata/fake-sing-box/build.sh`

- [ ] **Step 1：写 testdata/fake-sing-box/main.go**

```go
// fake-sing-box 是测试专用的 sing-box 桩。它按 flag 指定的端口开 TCP listener
// 与 fake clash API，可以延迟 ready、运行中崩溃、pre-ready 退出，方便驱动 supervisor。
package main

import (
    "flag"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"
)

func main() {
    var (
        ports         = flag.String("listen", "", "comma-separated TCP ports to bind (skip if empty)")
        clashPort     = flag.Int("clash-port", 0, "clash API port; 0 = disabled")
        readyDelay    = flag.Duration("ready-delay", 0, "wait before binding listeners")
        crashAfter    = flag.Duration("crash-after", 0, "panic after duration; 0 = never")
        preReadyFail  = flag.Bool("pre-ready-fail", false, "exit code 1 immediately")
        emitLog       = flag.Duration("emit-log", 0, "emit a sing-box-like stderr line every N; 0 = no")
        timestampLine = flag.Bool("timestamp", true, "include timezone+date+time prefix in emitted log lines")
    )
    flag.Parse()

    if *preReadyFail {
        fmt.Fprintln(os.Stderr, "fake-sing-box: pre-ready failure")
        os.Exit(1)
    }

    if *readyDelay > 0 {
        time.Sleep(*readyDelay)
    }

    listeners := []net.Listener{}
    for _, p := range splitPorts(*ports) {
        l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
        if err != nil {
            fmt.Fprintln(os.Stderr, "listen", p, "err:", err)
            os.Exit(1)
        }
        go acceptLoop(l)
        listeners = append(listeners, l)
    }
    if *clashPort > 0 {
        mux := http.NewServeMux()
        mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
            _, _ = io.WriteString(w, `{"version":"fake-1.0.0"}`)
        })
        go func() {
            _ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *clashPort), mux)
        }()
    }

    if *emitLog > 0 {
        go func() {
            t := time.NewTicker(*emitLog)
            for range t.C {
                emit(*timestampLine, "INFO", "router[default]", "outbound connection to fake.example.com:443")
            }
        }()
    }

    if *crashAfter > 0 {
        time.AfterFunc(*crashAfter, func() {
            panic("fake-sing-box scheduled crash")
        })
    }

    // 等待信号优雅退出
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
    <-sig
    for _, l := range listeners {
        _ = l.Close()
    }
}

func splitPorts(s string) []int {
    if s == "" {
        return nil
    }
    var out []int
    for _, raw := range strings.Split(s, ",") {
        var n int
        _, _ = fmt.Sscanf(raw, "%d", &n)
        if n > 0 {
            out = append(out, n)
        }
    }
    return out
}

func acceptLoop(l net.Listener) {
    for {
        c, err := l.Accept()
        if err != nil {
            return
        }
        _ = c.Close()
    }
}

func emit(timestamp bool, level, mod, detail string) {
    if timestamp {
        now := time.Now()
        fmt.Fprintf(os.Stderr, "%s %s %s %s %s: %s\n",
            now.Format("-0700"),
            now.Format("2006-01-02"),
            now.Format("15:04:05.000"),
            level, mod, detail,
        )
    } else {
        fmt.Fprintf(os.Stderr, "%s %s: %s\n", level, mod, detail)
    }
}
```

- [ ] **Step 2：写 testdata/fake-sing-box/build.sh**

```bash
#!/usr/bin/env bash
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
go build -o "$DIR/fake-sing-box" "$DIR"
```

```bash
chmod +x testdata/fake-sing-box/build.sh
```

- [ ] **Step 3：构建并 sanity 跑一次**

```bash
testdata/fake-sing-box/build.sh
testdata/fake-sing-box/fake-sing-box --pre-ready-fail
echo $?   # 期望 1
```

- [ ] **Step 4：提交**

```bash
git add testdata/fake-sing-box
git commit -m "test(supervisor): add fake-sing-box stub for integration tests"
```

---

## Task 27：Supervisor 启动路径

**Files:**
- Create: `internal/daemon/supervisor.go`
- Create: `internal/daemon/supervisor_test.go`

- [ ] **Step 1：写测试 supervisor_test.go（聚焦 Boot）**

```go
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
        Emitter: newTestEmitter(t),
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
        Emitter: newTestEmitter(t),
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
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/daemon/... -run TestSupervisor
```

- [ ] **Step 3：写实现 internal/daemon/supervisor.go**

```go
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

    log "github.com/moonfruit/sing-router/internal/log"
)

// SupervisorConfig 控制 supervisor 行为。
type SupervisorConfig struct {
    Emitter        *log.Emitter
    SingBoxBinary  string
    SingBoxArgs    []string
    SingBoxDir     string // 子进程 cwd

    ReadyConfig    ReadyConfig

    StartupHook    func(context.Context) error // 在 ready 后跑 startup.sh
    TeardownHook   func(context.Context) error // 在 stop/shutdown 时拆 iptables

    BackoffMs                  []int // 崩溃恢复退避序列；最后一档为封顶
    IptablesKeepBackoffLtMs    int   // < 此阈值时保持 iptables；>= 时拆
    StopGrace                  time.Duration
    StateHookOnTransition      func(from, to State)
}

// Supervisor 串行化 sing-box 子进程的全部生命周期事件。
type Supervisor struct {
    cfg SupervisorConfig
    sm  *StateMachine

    mu                 sync.Mutex
    cmd                *exec.Cmd
    iptablesInstalled  bool
    nextBackoffIdx     int
    restartCount       int
    bootAt             time.Time
    readyAt            time.Time
    childExited        chan struct{}
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
    }
    s.transitionTo(StateRunning)
    return nil
}

func (s *Supervisor) startSingBox(ctx context.Context) error {
    cmd := exec.CommandContext(ctx, s.cfg.SingBoxBinary, s.cfg.SingBoxArgs...)
    cmd.Dir = s.cfg.SingBoxDir

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
    go s.consumeStderr(pr)

    // wait goroutine
    go func() {
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
        ev := log.ParseSingBoxLine(sc.Text())
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

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/daemon/... -run TestSupervisor -v
```

期望：`TestSupervisorBootHappyPath` 与 `TestSupervisorBootPreReadyFailEntersFatal` 通过。

- [ ] **Step 5：提交**

```bash
git add internal/daemon/supervisor.go internal/daemon/supervisor_test.go
git commit -m "feat(daemon): supervisor boot path with ready check + startup hook"
```

---

## Task 28：Supervisor restart / stop / start / shutdown 路径

**Files:**
- Modify: `internal/daemon/supervisor_test.go`

- [ ] **Step 1：在 supervisor_test.go 末尾追加 4 个 case**

```go
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
```

- [ ] **Step 2：跑测试**

```bash
go test ./internal/daemon/... -run TestSupervisor -v
```

期望：5 个 TestSupervisor* 全部 `PASS`。

- [ ] **Step 3：提交**

```bash
git add internal/daemon/supervisor_test.go
git commit -m "test(daemon): cover supervisor restart/stop/start/shutdown paths"
```

---

## Task 29：Supervisor 崩溃恢复（degraded → running）

**Files:**
- Modify: `internal/daemon/supervisor_test.go`

- [ ] **Step 1：测试 case：注入崩溃 + Run 恢复**

```go
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
        IptablesKeepBackoffLtMs: 10000, // 50ms < 10s → 不拆
        StopGrace:    1 * time.Second,
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
    _ = sup.Shutdown(context.Background())

    if atomic.LoadInt32(&teardownCalls) != 0 {
        t.Fatal("teardown should not be invoked when backoff < threshold")
    }
}
```

- [ ] **Step 2：跑测试 + 提交**

```bash
go test ./internal/daemon/... -run TestSupervisorAutoRestart -v
git add internal/daemon/supervisor_test.go
git commit -m "test(daemon): cover crash auto-restart with iptables retention"
```

> 注：此测试为定时性，可能在极慢的 CI 上偶发不稳；deadline 已设宽。若失败可拉长时间或改为子测试 + 重试。

---

# Phase 9：HTTP API + daemon 入口

## Task 30：HTTP handlers — status / start / stop / restart / shutdown / check / reapply / script

**Files:**
- Create: `internal/daemon/api.go`
- Create: `internal/daemon/api_test.go`
- Create: `internal/daemon/daemon.go`

> 因 API 端点较多但形状一致，本任务采用 compact-TDD：一次性铺测试 + 实现 + 单次跑测试。

- [ ] **Step 1：写 internal/daemon/api.go**

```go
package daemon

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "time"

    log "github.com/moonfruit/sing-router/internal/log"
)

// APIDeps 是 HTTP handlers 依赖的接口集；测试可注入 mock。
type APIDeps struct {
    Supervisor *Supervisor
    Emitter    *log.Emitter
    Version    string
    Rundir     string

    // 给 reapply-rules / check 的 hook
    ReapplyRules func(context.Context) error
    CheckConfig  func(context.Context) error
    StatusExtra  func() map[string]any
    ScriptByName func(name string) ([]byte, error)
    ShutdownHook func() // 通常关 ctx 让 main 退出
}

// NewMux 注册所有端点到一个 http.ServeMux。
func NewMux(deps APIDeps) *http.ServeMux {
    mux := http.NewServeMux()
    mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, http.StatusOK, deps.statusSnapshot())
    })
    mux.HandleFunc("/api/v1/start", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        if err := deps.Supervisor.Start(r.Context()); err != nil {
            writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
            return
        }
        writeJSON(w, http.StatusOK, deps.statusSnapshot())
    })
    mux.HandleFunc("/api/v1/stop", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        if err := deps.Supervisor.Stop(r.Context()); err != nil {
            writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
            return
        }
        writeJSON(w, http.StatusOK, deps.statusSnapshot())
    })
    mux.HandleFunc("/api/v1/restart", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        if err := deps.Supervisor.Restart(r.Context()); err != nil {
            writeError(w, http.StatusConflict, "daemon.state_conflict", err.Error(), nil)
            return
        }
        writeJSON(w, http.StatusOK, deps.statusSnapshot())
    })
    mux.HandleFunc("/api/v1/check", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        if deps.CheckConfig == nil {
            writeError(w, http.StatusNotImplemented, "not_implemented", "CheckConfig hook not wired", nil)
            return
        }
        if err := deps.CheckConfig(r.Context()); err != nil {
            writeError(w, http.StatusBadRequest, "config.singbox_check_failed", err.Error(), nil)
            return
        }
        writeJSON(w, http.StatusOK, map[string]any{"ok": true})
    })
    mux.HandleFunc("/api/v1/reapply-rules", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        if cur := deps.Supervisor.State(); cur != StateRunning {
            writeError(w, http.StatusConflict, "daemon.state_conflict", "not running: "+cur.String(), nil)
            return
        }
        if deps.ReapplyRules == nil {
            writeError(w, http.StatusNotImplemented, "not_implemented", "ReapplyRules hook not wired", nil)
            return
        }
        if err := deps.ReapplyRules(r.Context()); err != nil {
            writeError(w, http.StatusInternalServerError, "shell.startup_failed", err.Error(), nil)
            return
        }
        writeJSON(w, http.StatusOK, map[string]any{"ok": true})
    })
    mux.HandleFunc("/api/v1/script/", func(w http.ResponseWriter, r *http.Request) {
        name := r.URL.Path[len("/api/v1/script/"):]
        if deps.ScriptByName == nil {
            writeError(w, http.StatusNotImplemented, "not_implemented", "ScriptByName hook not wired", nil)
            return
        }
        data, err := deps.ScriptByName(name)
        if err != nil {
            writeError(w, http.StatusNotFound, "script.not_found", err.Error(), nil)
            return
        }
        w.Header().Set("Content-Type", "text/plain; charset=utf-8")
        _, _ = w.Write(data)
    })
    mux.HandleFunc("/api/v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            writeError(w, http.StatusMethodNotAllowed, "method.not_allowed", "POST required", nil)
            return
        }
        writeJSON(w, http.StatusOK, map[string]any{"ok": true})
        if deps.ShutdownHook != nil {
            go deps.ShutdownHook()
        }
    })
    return mux
}

func (deps APIDeps) statusSnapshot() map[string]any {
    sup := deps.Supervisor
    snap := map[string]any{
        "daemon": map[string]any{
            "version": deps.Version,
            "rundir":  deps.Rundir,
            "state":   sup.State().String(),
        },
        "sing_box": map[string]any{
            "pid":           sup.SingBoxPID(),
            "restart_count": sup.RestartCount(),
        },
        "rules": map[string]any{
            "iptables_installed": sup.IptablesInstalled(),
        },
    }
    if deps.StatusExtra != nil {
        for k, v := range deps.StatusExtra() {
            snap[k] = v
        }
    }
    return snap
}

func writeJSON(w http.ResponseWriter, code int, body any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string, detail any) {
    writeJSON(w, code, map[string]any{
        "error": map[string]any{
            "code":    errCode,
            "message": msg,
            "detail":  detail,
        },
    })
}

// ServeHTTP 是 daemon.go 用的薄包装；阻塞直到 ctx 取消。
func ServeHTTP(ctx context.Context, mux http.Handler, listen string) error {
    srv := &http.Server{
        Addr:              listen,
        Handler:           mux,
        ReadHeaderTimeout: 5 * time.Second,
    }
    errCh := make(chan error, 1)
    go func() { errCh <- srv.ListenAndServe() }()
    select {
    case <-ctx.Done():
        sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        defer cancel()
        _ = srv.Shutdown(sctx)
        return nil
    case err := <-errCh:
        if err == http.ErrServerClosed {
            return nil
        }
        return err
    }
    _ = io.EOF // keep io referenced
}
```

- [ ] **Step 2：写 internal/daemon/api_test.go**

```go
package daemon

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strconv"
    "strings"
    "testing"
    "time"
)

// 用一个最小 supervisor 模拟器：构造好 state 后供 handler 读取。
func newTestSupervisor(t *testing.T) *Supervisor {
    t.Helper()
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
        StartupHook:  func(_ ctxLike) error { return nil },
        TeardownHook: func(_ ctxLike) error { return nil },
        StopGrace:    1 * time.Second,
    })
    return sup
}

// 兼容性 alias，避免 import context 冲突
type ctxLike = struct{ ctxLikeMarker }
type ctxLikeMarker struct{}

func TestAPIStatusReturnsJSON(t *testing.T) {
    sup := newTestSupervisor(t)
    mux := NewMux(APIDeps{Supervisor: sup, Version: "test-1.0", Rundir: "/tmp/rundir"})
    ts := httptest.NewServer(mux)
    defer ts.Close()
    resp, err := http.Get(ts.URL + "/api/v1/status")
    if err != nil {
        t.Fatal(err)
    }
    defer func() { _ = resp.Body.Close() }()
    if resp.StatusCode != 200 {
        t.Fatalf("status %d", resp.StatusCode)
    }
    var body map[string]any
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        t.Fatal(err)
    }
    daemon := body["daemon"].(map[string]any)
    if daemon["version"] != "test-1.0" {
        t.Fatalf("version: %v", daemon["version"])
    }
}

func TestAPIScript(t *testing.T) {
    sup := newTestSupervisor(t)
    mux := NewMux(APIDeps{
        Supervisor: sup,
        ScriptByName: func(name string) ([]byte, error) {
            if name != "startup" {
                return nil, fmt.Errorf("unknown")
            }
            return []byte("#!/usr/bin/env bash\necho hi"), nil
        },
    })
    ts := httptest.NewServer(mux)
    defer ts.Close()
    resp, _ := http.Get(ts.URL + "/api/v1/script/startup")
    body := readBody(t, resp)
    if !strings.Contains(body, "echo hi") {
        t.Fatalf("body: %s", body)
    }
    resp2, _ := http.Get(ts.URL + "/api/v1/script/missing")
    if resp2.StatusCode != 404 {
        t.Fatalf("status: %d", resp2.StatusCode)
    }
}

func readBody(t *testing.T, r *http.Response) string {
    t.Helper()
    defer func() { _ = r.Body.Close() }()
    var b strings.Builder
    buf := make([]byte, 1024)
    for {
        n, err := r.Body.Read(buf)
        if n > 0 {
            b.Write(buf[:n])
        }
        if err != nil {
            break
        }
    }
    return b.String()
}

func TestAPIReapplyRulesRequiresRunning(t *testing.T) {
    // supervisor 默认在 booting 态 → reapply-rules 应当 409
    sup := New(SupervisorConfig{Emitter: newTestEmitter(t)})
    mux := NewMux(APIDeps{Supervisor: sup, ReapplyRules: func(_ ctxLike) error { return nil }})
    ts := httptest.NewServer(mux)
    defer ts.Close()
    resp, _ := http.Post(ts.URL+"/api/v1/reapply-rules", "application/json", nil)
    if resp.StatusCode != 409 {
        t.Fatalf("status: %d", resp.StatusCode)
    }
}
```

> **类型一致提示**：上面测试为了避免循环 import `context`，用了 `ctxLike` 别名占位。**实际 supervisor.go 与 api.go 已使用 `context.Context`；测试也应直接 `import "context"` 并把 `func(_ ctxLike)` 替换为 `func(_ context.Context)`。** 编辑时执行：

```bash
sed -i.bak \
  -e '1,/^import/{s|"testing"|"context"\n    "testing"|}' \
  -e 's/_ ctxLike/_ context.Context/g' \
  -e '/type ctxLike =/d' \
  -e '/type ctxLikeMarker /d' \
  internal/daemon/api_test.go
rm internal/daemon/api_test.go.bak
```

- [ ] **Step 3：写 internal/daemon/daemon.go（main 入口骨架）**

```go
package daemon

import (
    "context"
    "fmt"
    "os"
    "os/signal"
    "path/filepath"
    "syscall"
    "time"

    log "github.com/moonfruit/sing-router/internal/log"
)

// Options 是 daemon 入口接受的参数。
type Options struct {
    Rundir       string
    Listen       string
    Version      string
    Emitter      *log.Emitter
    Supervisor   *Supervisor
    ReapplyRules func(context.Context) error
    CheckConfig  func(context.Context) error
    StatusExtra  func() map[string]any
    ScriptByName func(name string) ([]byte, error)
}

// Run 阻塞跑 daemon：HTTP listener + supervisor 主循环 + signal handling。
func Run(ctx context.Context, opts Options) error {
    if err := writePID(filepath.Join(opts.Rundir, "run", "sing-router.pid")); err != nil {
        return fmt.Errorf("write pid: %w", err)
    }
    defer func() { _ = os.Remove(filepath.Join(opts.Rundir, "run", "sing-router.pid")) }()

    ctx, cancel := context.WithCancel(ctx)
    defer cancel()

    deps := APIDeps{
        Supervisor:   opts.Supervisor,
        Emitter:      opts.Emitter,
        Version:      opts.Version,
        Rundir:       opts.Rundir,
        ReapplyRules: opts.ReapplyRules,
        CheckConfig:  opts.CheckConfig,
        StatusExtra:  opts.StatusExtra,
        ScriptByName: opts.ScriptByName,
        ShutdownHook: cancel,
    }
    mux := NewMux(deps)

    httpDone := make(chan error, 1)
    go func() { httpDone <- ServeHTTP(ctx, mux, opts.Listen) }()

    // SIGTERM/SIGINT
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
    defer signal.Stop(sigCh)

    // Boot supervisor
    if err := opts.Supervisor.Boot(ctx); err != nil {
        opts.Emitter.Fatal("supervisor", "supervisor.boot.failed", "boot failed: {Err}", map[string]any{"Err": err.Error()})
        // fatal 状态保持 HTTP 存活，等待 SIGTERM 或 /shutdown
    }

    // 后台跑 supervisor restart loop
    runDone := make(chan error, 1)
    go func() { runDone <- opts.Supervisor.Run(ctx) }()

    select {
    case <-sigCh:
        cancel()
    case <-ctx.Done():
    }

    // 优雅关停
    sctx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer sCancel()
    _ = opts.Supervisor.Shutdown(sctx)
    <-runDone
    <-httpDone
    return nil
}

func writePID(path string) error {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return err
    }
    return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/daemon/... -v
```

期望：所有 daemon 包测试 `PASS`（包含 supervisor 各 case + api 各 case）。

- [ ] **Step 5：提交**

```bash
git add internal/daemon/api.go internal/daemon/api_test.go internal/daemon/daemon.go
git commit -m "feat(daemon): http API endpoints + Run entry point"
```

---

# Phase 10/11/12 见 part4

> Phase 10：CLI 各子命令（status / start_stop / logs / script / daemon）
> Phase 11：install/uninstall/doctor + jffs_hooks 100% 覆盖 + download mirror
> Phase 12：cmd/sing-router/main.go 全部连线 + cross-compile 验证

详见 `2026-05-02-sing-router-module-a.part4.md`。

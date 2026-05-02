# sing-router Module A 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建 `sing-router` Module A —— 一个用 Go 编写的单文件可执行，作为常驻 supervisor 托管 sing-box 子进程，并通过 HTTP 控制平面 + CLI 完成在 Asus RT-BE88U（Merlin + Entware）上的全生命周期管理。

**Architecture:** Cobra 子命令分发 → daemon 模式作为长驻 supervisor（fork sing-box，stderr 经 vendored sing2seq parser 转 CLEF）；config.d/ 由 sing-box 自身 `-C` 深度合并，仅 `zoo.json` 由 daemon 预处理；HTTP loopback (127.0.0.1:9998) 暴露控制；`go:embed` 内嵌 shell 与 init 脚本，`sing-router install` 一键铺好系统级与 $RUNDIR 文件。

**Tech Stack:** Go 1.26+ / spf13/cobra / spf13/pflag / BurntSushi/toml / 标准库 net/http、encoding/json、io/fs、os/exec

**Spec:** `docs/superpowers/specs/2026-05-02-sing-router-module-a-design.md`

---

## 文件结构总览

```
go.mod
go.sum
cmd/sing-router/main.go

internal/version/version.go

internal/log/
  clef.go            # orderedEvent + MarshalJSON
  level.go           # 级别枚举 + 字符串映射
  parser.go          # vendored sing2seq parser
  parser_test.go     # vendored
  writer.go          # JSON Lines + 大小轮转 + gzip
  writer_test.go
  bus.go             # 内存 ring buffer pubsub
  bus_test.go
  pretty.go          # JSON → 人类可读渲染
  pretty_test.go
  emitter.go         # 上层 helper：log.Info/Warn/Error 等 + 自动塞 EventID

internal/config/
  daemon_toml.go     # 解析 daemon.toml
  daemon_toml_test.go
  routing.go         # 路由参数结构 + 默认值 + env 注入
  routing_test.go
  singbox.go         # sing-box check 命令 wrapper
  singbox_test.go
  zoo.go             # ★ zoo.json 预处理器
  zoo_test.go        # 100% 覆盖

assets/
  embed.go           # //go:embed 声明
  shell/
    startup.sh       # env-driven; 由当前 bin/startup.sh 改造
    teardown.sh
  initd/
    S99sing-router
  jffs/
    nat-start.snippet
    services-start.snippet
  config.d.default/
    clash.json
    dns.json
    inbounds.json
    log.json
    cache.json
    certificate.json
    http.json
    outbounds.json
  daemon.toml.default

internal/shell/
  runner.go          # bash exec wrapper：env + stdin + stderr → CLEF
  runner_test.go

internal/state/
  state.go           # state.json 持久化

internal/daemon/
  statemachine.go    # 状态枚举 + 转移
  statemachine_test.go
  ready.go           # readiness 检测
  ready_test.go
  supervisor.go      # 核心 supervisor 主循环
  supervisor_test.go
  api.go             # HTTP handlers
  api_test.go
  daemon.go          # supervisor + http server entrypoint

internal/install/
  layout.go          # mkdir $RUNDIR 子目录
  layout_test.go
  seed.go            # 落盘默认配置
  seed_test.go
  initd.go           # /opt/etc/init.d/S99sing-router 写入
  initd_test.go
  jffs_hooks.go      # BEGIN/END 幂等块
  jffs_hooks_test.go # 100% 覆盖
  download.go        # mirror_prefix 下载器
  download_test.go

internal/cli/
  root.go            # cobra root + 全局 flag
  httpclient.go      # 共享 HTTP 客户端
  daemon.go          # daemon 子命令
  install.go
  uninstall.go
  doctor.go
  status.go
  start_stop.go      # start/stop/restart/check/reapply-rules/shutdown
  logs.go
  script.go
  version.go

testdata/
  fake-sing-box/main.go
```

---

## 实施提示

- **TDD 优先级**：`internal/config/zoo.go`、`internal/install/jffs_hooks.go`、`internal/log/parser.go`、`internal/log/writer.go` 严格 TDD（写测试 → 看失败 → 实现 → 看通过）。其他偏机械的结构（CLI 子命令、配置 struct）以"compact TDD"呈现：单步内同时写测试与实现，但仍要在下一步运行测试确认。
- **分支策略**：每完成一个 Phase 创建一次合并提交（`feat(phase-N): ...`），每个 Task 内的 step 用小提交（`feat: ...` / `test: ...`）。
- **测试隔离**：所有需要文件系统的测试用 `t.TempDir()`；所有需要外部命令的测试用接口注入 mock（`internal/shell/runner.go` 暴露 `ExecFunc` 类型供测试替换）。
- **构建目标**：本地 `go build ./cmd/sing-router` 在 darwin/arm64 跑通；最终交付时 `GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' ./cmd/sing-router` 产出 release 二进制。

---

# Phase 1：项目骨架

## Task 1：初始化 Go module 与目录骨架

**Files:**
- Create: `go.mod`
- Create: `cmd/sing-router/main.go`
- Create: `internal/version/version.go`
- Create: `internal/version/version_test.go`
- Create: `.gitignore`（追加）

- [ ] **Step 1：初始化 module**

```bash
cd /Users/moon/Workspace.localized/proxy/sing-router
go mod init github.com/moonfruit/sing-router
```

期望：生成 `go.mod`，内容含 `module github.com/moonfruit/sing-router` 与 `go 1.26`。

- [ ] **Step 2：扩展 .gitignore**

把以下内容追加到根 `.gitignore`（保留现有）：

```gitignore
/sing-router
/sing-router-*
/dist/
/coverage.out
/coverage.html
/testdata/fake-sing-box/fake-sing-box
```

- [ ] **Step 3：写 internal/version/version.go**

```go
package version

// Version 在编译时通过 -ldflags 注入，留空时取 "dev"。
var Version = "dev"

// String 返回带前缀的版本字符串。
func String() string {
    if Version == "" {
        return "dev"
    }
    return Version
}
```

- [ ] **Step 4：写 internal/version/version_test.go**

```go
package version

import "testing"

func TestStringDefaults(t *testing.T) {
    Version = ""
    if got := String(); got != "dev" {
        t.Fatalf("want dev, got %q", got)
    }
}

func TestStringRespectsLdflag(t *testing.T) {
    Version = "0.1.0+abcdef"
    if got := String(); got != "0.1.0+abcdef" {
        t.Fatalf("want injected version, got %q", got)
    }
}
```

- [ ] **Step 5：跑测试验证**

```bash
go test ./internal/version/...
```

期望：`PASS`。

- [ ] **Step 6：写 cmd/sing-router/main.go（最小可执行）**

```go
package main

import (
    "fmt"
    "os"

    "github.com/moonfruit/sing-router/internal/version"
)

func main() {
    if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version") {
        fmt.Println(version.String())
        return
    }
    fmt.Fprintln(os.Stderr, "sing-router: subcommands not yet wired")
    os.Exit(2)
}
```

- [ ] **Step 7：构建并跑一次**

```bash
go build -o sing-router ./cmd/sing-router
./sing-router version
```

期望：输出 `dev`。

- [ ] **Step 8：清理本地构建产物并提交**

```bash
rm sing-router
git add go.mod .gitignore cmd internal
git commit -m "feat(phase-1): init module + version subcommand skeleton"
```

---

## Task 2：引入 cobra，把 root 命令搭起来

**Files:**
- Modify: `cmd/sing-router/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/version.go`
- Create: `internal/cli/version_test.go`

- [ ] **Step 1：拉 cobra 依赖**

```bash
go get github.com/spf13/cobra@v1.10.2
go get github.com/spf13/pflag@v1.0.10
go mod tidy
```

期望：`go.mod` 与 `go.sum` 更新。

- [ ] **Step 2：写 internal/cli/root.go**

```go
package cli

import (
    "github.com/spf13/cobra"

    "github.com/moonfruit/sing-router/internal/version"
)

// NewRootCmd 构造顶层 cobra.Command，挂载所有子命令。
func NewRootCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:           "sing-router",
        Short:         "Transparent router manager for sing-box on Asus Merlin/Entware",
        Version:       version.String(),
        SilenceUsage:  true,
        SilenceErrors: false,
    }
    cmd.AddCommand(newVersionCmd())
    return cmd
}
```

- [ ] **Step 3：写 internal/cli/version.go**

```go
package cli

import (
    "fmt"

    "github.com/spf13/cobra"

    "github.com/moonfruit/sing-router/internal/version"
)

func newVersionCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "version",
        Short: "Print version",
        Run: func(cmd *cobra.Command, args []string) {
            fmt.Fprintln(cmd.OutOrStdout(), version.String())
        },
    }
}
```

- [ ] **Step 4：写 internal/cli/version_test.go**

```go
package cli

import (
    "bytes"
    "strings"
    "testing"

    "github.com/moonfruit/sing-router/internal/version"
)

func TestVersionSubcommand(t *testing.T) {
    version.Version = "1.2.3"
    cmd := NewRootCmd()
    cmd.SetArgs([]string{"version"})

    var buf bytes.Buffer
    cmd.SetOut(&buf)
    cmd.SetErr(&buf)

    if err := cmd.Execute(); err != nil {
        t.Fatalf("execute: %v", err)
    }
    if got := strings.TrimSpace(buf.String()); got != "1.2.3" {
        t.Fatalf("want 1.2.3, got %q", got)
    }
}
```

- [ ] **Step 5：把 main.go 改为转发到 NewRootCmd**

替换 `cmd/sing-router/main.go` 全部内容：

```go
package main

import (
    "os"

    "github.com/moonfruit/sing-router/internal/cli"
)

func main() {
    if err := cli.NewRootCmd().Execute(); err != nil {
        os.Exit(1)
    }
}
```

- [ ] **Step 6：跑测试 + 构建**

```bash
go test ./...
go build -o /tmp/sing-router-build-check ./cmd/sing-router
/tmp/sing-router-build-check version
rm /tmp/sing-router-build-check
```

期望：测试 `PASS`；二进制输出 `dev`（未注入 ldflag 时）。

- [ ] **Step 7：提交**

```bash
git add go.mod go.sum cmd internal
git commit -m "feat(cli): wire cobra root + version subcommand"
```

---

# Phase 2：日志原语

> 本阶段构建 CLEF emitter、parser（vendored）、writer、bus、pretty 五件。`internal/log` 包不依赖任何其他 internal 包。

## Task 3：CLEF orderedEvent

**Files:**
- Create: `internal/log/clef.go`
- Create: `internal/log/clef_test.go`

- [ ] **Step 1：写 internal/log/clef.go**

```go
// Package log 提供 sing-router 的结构化日志原语。
//
// CLEF (Compact Log Event Format) 用于与 Seq 兼容；事件字段保持插入顺序，
// 因为 Seq UI 按顺序展示。orderedEvent 是与 sing2seq 一致的轻量实现。
package log

import (
    "bytes"
    "encoding/json"
)

// OrderedEvent 是 CLEF 事件，字段顺序由 Set 调用顺序决定。
type OrderedEvent struct {
    keys   []string
    values map[string]any
}

// NewEvent 创建一个空事件。
func NewEvent() *OrderedEvent {
    return &OrderedEvent{values: map[string]any{}}
}

// Set 添加或更新一个字段。后写不动顺序。
func (e *OrderedEvent) Set(k string, v any) {
    if _, ok := e.values[k]; !ok {
        e.keys = append(e.keys, k)
    }
    e.values[k] = v
}

// Get 返回字段值；不存在时第二个返回值为 false。
func (e *OrderedEvent) Get(k string) (any, bool) {
    v, ok := e.values[k]
    return v, ok
}

// Keys 返回有序键列表的副本。
func (e *OrderedEvent) Keys() []string {
    out := make([]string, len(e.keys))
    copy(out, e.keys)
    return out
}

// MarshalJSON 按插入顺序序列化字段。
func (e *OrderedEvent) MarshalJSON() ([]byte, error) {
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

- [ ] **Step 2：写 internal/log/clef_test.go**

```go
package log

import (
    "encoding/json"
    "testing"
)

func TestOrderedEventPreservesInsertionOrder(t *testing.T) {
    e := NewEvent()
    e.Set("@t", "2026-05-02T12:00:00+08:00")
    e.Set("@l", "Information")
    e.Set("Source", "daemon")
    e.Set("EventID", "supervisor.boot.ready")

    out, err := json.Marshal(e)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    want := `{"@t":"2026-05-02T12:00:00+08:00","@l":"Information","Source":"daemon","EventID":"supervisor.boot.ready"}`
    if string(out) != want {
        t.Fatalf("\nwant %s\ngot  %s", want, out)
    }
}

func TestOrderedEventOverwriteKeepsPosition(t *testing.T) {
    e := NewEvent()
    e.Set("a", 1)
    e.Set("b", 2)
    e.Set("a", 99)

    out, _ := json.Marshal(e)
    want := `{"a":99,"b":2}`
    if string(out) != want {
        t.Fatalf("want %s, got %s", want, out)
    }
}

func TestOrderedEventGetMissing(t *testing.T) {
    e := NewEvent()
    if _, ok := e.Get("missing"); ok {
        t.Fatal("missing key reported as present")
    }
}

func TestOrderedEventNestedEvent(t *testing.T) {
    inner := NewEvent()
    inner.Set("kind", "fatal")
    outer := NewEvent()
    outer.Set("Source", "daemon")
    outer.Set("Error", inner)

    out, _ := json.Marshal(outer)
    want := `{"Source":"daemon","Error":{"kind":"fatal"}}`
    if string(out) != want {
        t.Fatalf("want %s, got %s", want, out)
    }
}
```

- [ ] **Step 3：跑测试**

```bash
go test ./internal/log/...
```

期望：4 个测试 `PASS`。

- [ ] **Step 4：提交**

```bash
git add internal/log
git commit -m "feat(log): add CLEF OrderedEvent with insertion-ordered JSON marshaling"
```

---

## Task 4：日志级别

**Files:**
- Create: `internal/log/level.go`
- Create: `internal/log/level_test.go`

- [ ] **Step 1：写 internal/log/level.go**

```go
package log

import (
    "fmt"
    "strings"
)

// Level 是 sing-router 内部日志级别；与 sing-box 同源。
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

// ParseLevel 解析 daemon.toml [log].level 字符串。
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

- [ ] **Step 2：写 internal/log/level_test.go**

```go
package log

import "testing"

func TestParseLevel(t *testing.T) {
    cases := map[string]Level{
        "trace": LevelTrace,
        "DEBUG": LevelDebug,
        "":      LevelInfo,
        "warn":  LevelWarn,
        "warning": LevelWarn,
        "Error": LevelError,
        "Fatal": LevelFatal,
        "panic": LevelFatal,
    }
    for in, want := range cases {
        got, err := ParseLevel(in)
        if err != nil {
            t.Fatalf("%q: unexpected err %v", in, err)
        }
        if got != want {
            t.Fatalf("%q: want %v got %v", in, want, got)
        }
    }
    if _, err := ParseLevel("bogus"); err == nil {
        t.Fatal("expected error for bogus level")
    }
}

func TestLevelCLEFAndShort(t *testing.T) {
    if LevelInfo.CLEFName() != "Information" {
        t.Fatal("CLEFName mismatch")
    }
    if LevelWarn.String() != "WARN" {
        t.Fatal("String mismatch")
    }
}

func TestFromCLEFName(t *testing.T) {
    if FromCLEFName("Warning") != LevelWarn {
        t.Fatal("FromCLEFName failed")
    }
    if FromCLEFName("unknown") != LevelInfo {
        t.Fatal("default mismatch")
    }
}
```

- [ ] **Step 3：跑测试 + 提交**

```bash
go test ./internal/log/...
git add internal/log/level.go internal/log/level_test.go
git commit -m "feat(log): add Level enum with CLEF and short-name mappings"
```

---

## Task 5：Vendor sing2seq 的 parser

**Files:**
- Create: `internal/log/parser.go`
- Create: `internal/log/parser_test.go`

> 这一步把 `/Users/moon/Workspace.localized/go/mod/sing2seq/parser.go` 与 `parser_test.go` 复制进来，调整包名为 `log`、`orderedEvent` 改为公开 `OrderedEvent`、并适配本包内 `NewEvent`/`Set` 名称。

- [ ] **Step 1：复制 parser.go 并改包名**

把 sing2seq 的 `parser.go` 内容粘到 `internal/log/parser.go`，做以下修改：

1. 顶部包声明改为 `package log`（去掉 `package main`）
2. 把所有出现的 `*orderedEvent` 改为 `*OrderedEvent`
3. 把所有出现的 `newEvent()` 改为 `NewEvent()`
4. 把所有出现的 `ev.set(...)` 改为 `ev.Set(...)`
5. `levelMap` 不变（仍用字符串 → CLEF 字符串）
6. 删除 `import "errors"` 等本包未用的 import；保留 `net`、`path/filepath`、`regexp`、`strconv`、`strings`、`time`
7. 函数名保留小写（包内使用）：`stripAnsi / namedMatches / isIP / setHost / dnsSet / setIPs / enrich / parseLine`
8. 新增导出包装：

```go
// ParseSingBoxLine 从 sing-box stderr 的一行（含或不含 ANSI 色码、CR/LF）解析为
// CLEF 事件。无法解析的行降级为 Parsed=false 的原始事件。返回 nil 表示空行。
func ParseSingBoxLine(line string) *OrderedEvent {
    return parseLine(line)
}
```

- [ ] **Step 2：复制 parser_test.go**

把 sing2seq 的 `parser_test.go` 整体复制为 `internal/log/parser_test.go`，做以下修改：

1. 顶部 `package log`（去掉 `package main`）
2. 所有 `parseLine(...)` 调用改为 `ParseSingBoxLine(...)`
3. 所有对私有字段 `ev.values["..."]` 的访问，改用 `v, _ := ev.Get("..."); _ = v` 风格

由于具体的测试函数较多，**保留 sing2seq 已有的所有 case**，逐一修订。如有 import `bytes`、`encoding/json`、`testing`、`time` 等保持。

- [ ] **Step 3：跑测试**

```bash
go test ./internal/log/...
```

期望：所有 vendored case 通过；与 sing2seq 当前测试覆盖率持平。

- [ ] **Step 4：提交**

```bash
git add internal/log/parser.go internal/log/parser_test.go
git commit -m "feat(log): vendor sing2seq parser as ParseSingBoxLine"
```

---

## Task 6：log writer（JSON Lines + 大小轮转 + gzip）

**Files:**
- Create: `internal/log/writer.go`
- Create: `internal/log/writer_test.go`

- [ ] **Step 1：写测试 internal/log/writer_test.go（先失败）**

```go
package log

import (
    "compress/gzip"
    "encoding/json"
    "io"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func newTestWriter(t *testing.T, maxSize int64, maxBackups int) (*Writer, string) {
    dir := t.TempDir()
    path := filepath.Join(dir, "sing-router.log")
    w, err := NewWriter(WriterConfig{
        Path:       path,
        MaxSize:    maxSize,
        MaxBackups: maxBackups,
        Gzip:       true,
    })
    if err != nil {
        t.Fatalf("NewWriter: %v", err)
    }
    t.Cleanup(func() { _ = w.Close() })
    return w, path
}

func TestWriterAppendsLines(t *testing.T) {
    w, path := newTestWriter(t, 1024, 3)
    e := NewEvent()
    e.Set("@l", "Information")
    e.Set("Source", "daemon")
    e.Set("@mt", "hello {Name}")
    e.Set("Name", "world")
    if err := w.Write(e); err != nil {
        t.Fatalf("Write: %v", err)
    }
    if err := w.Sync(); err != nil {
        t.Fatalf("Sync: %v", err)
    }

    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("ReadFile: %v", err)
    }
    if !strings.HasSuffix(string(data), "\n") {
        t.Fatal("expected trailing newline")
    }
    var parsed map[string]any
    if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &parsed); err != nil {
        t.Fatalf("invalid JSON: %v", err)
    }
    if parsed["@l"] != "Information" {
        t.Fatal("@l mismatch")
    }
}

func TestWriterRotatesAtMaxSize(t *testing.T) {
    w, path := newTestWriter(t, 200, 3)
    big := strings.Repeat("x", 80)
    for i := 0; i < 10; i++ {
        e := NewEvent()
        e.Set("@l", "Information")
        e.Set("@mt", big)
        if err := w.Write(e); err != nil {
            t.Fatalf("write %d: %v", i, err)
        }
    }
    if err := w.Sync(); err != nil {
        t.Fatalf("Sync: %v", err)
    }
    if err := w.WaitGzip(); err != nil {
        t.Fatalf("WaitGzip: %v", err)
    }

    entries, err := os.ReadDir(filepath.Dir(path))
    if err != nil {
        t.Fatalf("ReadDir: %v", err)
    }
    var gzCount int
    var sawActive bool
    for _, e := range entries {
        if e.Name() == "sing-router.log" {
            sawActive = true
        }
        if strings.HasSuffix(e.Name(), ".gz") {
            gzCount++
        }
    }
    if !sawActive {
        t.Fatal("active log file missing")
    }
    if gzCount == 0 {
        t.Fatal("expected at least one gzipped backup")
    }
}

func TestWriterPrunesOldBackups(t *testing.T) {
    w, path := newTestWriter(t, 100, 2)
    big := strings.Repeat("y", 60)
    for i := 0; i < 30; i++ {
        e := NewEvent()
        e.Set("@l", "Information")
        e.Set("@mt", big)
        _ = w.Write(e)
    }
    _ = w.Sync()
    _ = w.WaitGzip()

    entries, _ := os.ReadDir(filepath.Dir(path))
    var gzCount int
    for _, e := range entries {
        if strings.HasSuffix(e.Name(), ".gz") {
            gzCount++
        }
    }
    if gzCount > 2 {
        t.Fatalf("expected at most 2 gz backups (max_backups=2), got %d", gzCount)
    }
}

func TestWriterGzipBackupReadable(t *testing.T) {
    w, path := newTestWriter(t, 80, 3)
    for i := 0; i < 6; i++ {
        e := NewEvent()
        e.Set("@l", "Information")
        e.Set("@mt", strings.Repeat("z", 40))
        _ = w.Write(e)
    }
    _ = w.Sync()
    _ = w.WaitGzip()

    f, err := os.Open(filepath.Join(filepath.Dir(path), "sing-router.log.1.gz"))
    if err != nil {
        t.Fatalf("open backup: %v", err)
    }
    defer func() { _ = f.Close() }()
    gr, err := gzip.NewReader(f)
    if err != nil {
        t.Fatalf("gzip reader: %v", err)
    }
    defer func() { _ = gr.Close() }()
    body, err := io.ReadAll(gr)
    if err != nil {
        t.Fatalf("ReadAll: %v", err)
    }
    if !strings.Contains(string(body), "@l") {
        t.Fatal("backup content not JSON Lines")
    }
}

func TestWriterReopenOnSIGUSR1Equivalent(t *testing.T) {
    w, path := newTestWriter(t, 1024, 3)
    e := NewEvent()
    e.Set("@l", "Information")
    e.Set("@mt", "first")
    _ = w.Write(e)

    // 模拟外部轮转：重命名 active 文件后调用 Reopen
    rotated := path + ".moved"
    if err := os.Rename(path, rotated); err != nil {
        t.Fatalf("rename: %v", err)
    }
    if err := w.Reopen(); err != nil {
        t.Fatalf("Reopen: %v", err)
    }
    e2 := NewEvent()
    e2.Set("@l", "Information")
    e2.Set("@mt", "after-reopen")
    _ = w.Write(e2)
    _ = w.Sync()

    after, _ := os.ReadFile(path)
    if !strings.Contains(string(after), "after-reopen") {
        t.Fatal("after-reopen content missing in new active file")
    }
    moved, _ := os.ReadFile(rotated)
    if !strings.Contains(string(moved), "first") {
        t.Fatal("rotated file content missing")
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/log/... -run TestWriter
```

期望：编译失败（`Writer` / `NewWriter` 等未定义）。

- [ ] **Step 3：写实现 internal/log/writer.go**

```go
package log

import (
    "bufio"
    "compress/gzip"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "sync"
)

// WriterConfig 配置 Writer 的轮转行为。
type WriterConfig struct {
    Path       string // active 文件绝对路径
    MaxSize    int64  // 字节；> 0 触发大小轮转，<= 0 表示不轮转
    MaxBackups int    // 保留的旧文件数量；超出按 .N 编号删除最旧
    Gzip       bool   // 是否在轮转后异步把 .1 压成 .1.gz
}

// Writer 写 CLEF JSON Lines 到 active 文件，按大小阈值轮转，可选 gzip。
// 并发安全。
type Writer struct {
    cfg WriterConfig

    mu         sync.Mutex
    f          *os.File
    bw         *bufio.Writer
    size       int64

    gzipWg     sync.WaitGroup
}

// NewWriter 打开（或创建）active 文件。父目录必须已存在。
func NewWriter(cfg WriterConfig) (*Writer, error) {
    if cfg.Path == "" {
        return nil, fmt.Errorf("Writer.Path is required")
    }
    w := &Writer{cfg: cfg}
    if err := w.openActive(); err != nil {
        return nil, err
    }
    return w, nil
}

func (w *Writer) openActive() error {
    f, err := os.OpenFile(w.cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        return err
    }
    info, err := f.Stat()
    if err != nil {
        _ = f.Close()
        return err
    }
    w.f = f
    w.bw = bufio.NewWriter(f)
    w.size = info.Size()
    return nil
}

// Write 序列化事件并追加一行；必要时触发轮转。
func (w *Writer) Write(e *OrderedEvent) error {
    data, err := json.Marshal(e)
    if err != nil {
        return err
    }
    data = append(data, '\n')

    w.mu.Lock()
    defer w.mu.Unlock()

    if w.cfg.MaxSize > 0 && w.size+int64(len(data)) > w.cfg.MaxSize && w.size > 0 {
        if err := w.rotateLocked(); err != nil {
            return err
        }
    }
    n, err := w.bw.Write(data)
    w.size += int64(n)
    return err
}

// Sync 刷新缓冲到磁盘。
func (w *Writer) Sync() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    if err := w.bw.Flush(); err != nil {
        return err
    }
    return w.f.Sync()
}

// Reopen 关闭当前 active 文件并重新打开同一路径；用于 logrotate copytruncate
// 的反向场景或 SIGUSR1 处理。
func (w *Writer) Reopen() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    if err := w.bw.Flush(); err != nil {
        return err
    }
    if err := w.f.Close(); err != nil {
        return err
    }
    return w.openActive()
}

// Close 刷新并关闭 active 文件，等待所有异步 gzip 完成。
func (w *Writer) Close() error {
    w.mu.Lock()
    var firstErr error
    if w.bw != nil {
        if err := w.bw.Flush(); err != nil {
            firstErr = err
        }
    }
    if w.f != nil {
        if err := w.f.Close(); err != nil && firstErr == nil {
            firstErr = err
        }
    }
    w.bw = nil
    w.f = nil
    w.mu.Unlock()
    w.gzipWg.Wait()
    return firstErr
}

// WaitGzip 阻塞直到所有未完成的 gzip 后台任务结束（仅供测试使用）。
func (w *Writer) WaitGzip() error {
    w.gzipWg.Wait()
    return nil
}

func (w *Writer) rotateLocked() error {
    if err := w.bw.Flush(); err != nil {
        return err
    }
    if err := w.f.Close(); err != nil {
        return err
    }

    // 顺延：.N → .N+1 (倒序，避免覆盖)
    backups := listBackups(w.cfg.Path)
    sort.Sort(sort.Reverse(byIndex(backups)))
    for _, b := range backups {
        next := backupNameAt(w.cfg.Path, b.idx+1, b.gz)
        _ = os.Rename(b.path, next)
    }

    // active → .1
    if err := os.Rename(w.cfg.Path, w.cfg.Path+".1"); err != nil {
        return fmt.Errorf("rename active: %w", err)
    }

    // 异步 gzip
    if w.cfg.Gzip {
        w.gzipWg.Add(1)
        go w.gzipBackground(w.cfg.Path + ".1")
    }

    // 修剪：删除超过 MaxBackups 的旧文件
    pruneBackups(w.cfg.Path, w.cfg.MaxBackups)

    if err := w.openActive(); err != nil {
        return err
    }
    w.size = 0
    return nil
}

func (w *Writer) gzipBackground(src string) {
    defer w.gzipWg.Done()
    in, err := os.Open(src)
    if err != nil {
        return
    }
    defer func() { _ = in.Close() }()
    out, err := os.OpenFile(src+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
    if err != nil {
        return
    }
    defer func() { _ = out.Close() }()
    gw := gzip.NewWriter(out)
    if _, err := io.Copy(gw, in); err != nil {
        return
    }
    if err := gw.Close(); err != nil {
        return
    }
    _ = os.Remove(src)
    pruneBackups(w.cfg.Path, w.cfg.MaxBackups)
}

type backup struct {
    idx  int
    gz   bool
    path string
}

type byIndex []backup

func (b byIndex) Len() int           { return len(b) }
func (b byIndex) Less(i, j int) bool { return b[i].idx < b[j].idx }
func (b byIndex) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func listBackups(active string) []backup {
    dir := filepath.Dir(active)
    base := filepath.Base(active)
    entries, err := os.ReadDir(dir)
    if err != nil {
        return nil
    }
    var out []backup
    for _, e := range entries {
        name := e.Name()
        if name == base || !startsWith(name, base+".") {
            continue
        }
        rest := name[len(base)+1:]
        gz := false
        if hasSuffix(rest, ".gz") {
            rest = rest[:len(rest)-3]
            gz = true
        }
        idx := atoi(rest)
        if idx <= 0 {
            continue
        }
        out = append(out, backup{idx: idx, gz: gz, path: filepath.Join(dir, name)})
    }
    return out
}

func backupNameAt(active string, idx int, gz bool) string {
    name := fmt.Sprintf("%s.%d", active, idx)
    if gz {
        name += ".gz"
    }
    return name
}

func pruneBackups(active string, maxBackups int) {
    if maxBackups <= 0 {
        return
    }
    backups := listBackups(active)
    sort.Sort(byIndex(backups))
    excess := len(backups) - maxBackups
    if excess <= 0 {
        return
    }
    // 删除 idx 最大的（最旧的）—— 等等，反过来：编号越大越旧。
    // 实际：active 旋转后 .1 是最新；.N 是最旧。所以删除 idx 大的。
    sort.Sort(sort.Reverse(byIndex(backups)))
    for i := 0; i < excess; i++ {
        _ = os.Remove(backups[i].path)
    }
}

// 小工具：避免 strings 包重复导入造成的循环（writer.go 不引入 strings 包就能用）
func startsWith(s, prefix string) bool {
    return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func hasSuffix(s, suffix string) bool {
    return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
func atoi(s string) int {
    if s == "" {
        return 0
    }
    n := 0
    for _, c := range s {
        if c < '0' || c > '9' {
            return 0
        }
        n = n*10 + int(c-'0')
    }
    return n
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/log/... -run TestWriter -v
```

期望：5 个 TestWriter* 全部 `PASS`；其它已有测试不变。

- [ ] **Step 5：提交**

```bash
git add internal/log/writer.go internal/log/writer_test.go
git commit -m "feat(log): add Writer with size-based rotation and async gzip"
```

---

## Task 7：内存事件总线

**Files:**
- Create: `internal/log/bus.go`
- Create: `internal/log/bus_test.go`

- [ ] **Step 1：写测试 internal/log/bus_test.go**

```go
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
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/log/... -run TestBus
```

期望：编译失败（`NewBus` 等未定义）。

- [ ] **Step 3：写实现 internal/log/bus.go**

```go
package log

import "sync"

// Subscriber 是 Bus 的订阅方接口。Match 与 Deliver 由订阅方各自实现。
type Subscriber interface {
    Match(e *OrderedEvent) bool
    Deliver(e *OrderedEvent)
}

// SubscriberFunc 是基于函数字面量的便捷实现，便于测试与轻量订阅。
type SubscriberFunc struct {
    MatchFn   func(*OrderedEvent) bool
    DeliverFn func(*OrderedEvent)
}

func (s SubscriberFunc) Match(e *OrderedEvent) bool { return s.MatchFn(e) }
func (s SubscriberFunc) Deliver(e *OrderedEvent)    { s.DeliverFn(e) }

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
// 新事件被丢弃（CLEF 文件仍是事实源；订阅是旁路）。
type Bus struct {
    mu     sync.Mutex
    subs   map[uint64]*subscription
    nextID uint64
    closed bool
}

type subscription struct {
    sub  Subscriber
    ch   chan *OrderedEvent
    done chan struct{}
}

// NewBus 创建总线；perSubBuffer 是每个订阅方的内部 channel 容量。
func NewBus(perSubBuffer int) *Bus {
    if perSubBuffer <= 0 {
        perSubBuffer = 64
    }
    return &Bus{subs: map[uint64]*subscription{}}
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
        ch:   make(chan *OrderedEvent, 64),
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
func (b *Bus) Publish(e *OrderedEvent) {
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

// Close 停止所有订阅方；之后的 Publish 与 Subscribe 调用是 no-op。
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

- [ ] **Step 4：跑测试**

```bash
go test ./internal/log/... -run TestBus -v
```

期望：3 个 TestBus* 全部 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/log/bus.go internal/log/bus_test.go
git commit -m "feat(log): add lossy in-memory event bus for B/C/E/F subscriptions"
```

---

## Task 8：Pretty printer

**Files:**
- Create: `internal/log/pretty.go`
- Create: `internal/log/pretty_test.go`

- [ ] **Step 1：写测试 internal/log/pretty_test.go**

```go
package log

import (
    "encoding/json"
    "strings"
    "testing"
    "time"
)

func mustParse(t *testing.T, s string) *OrderedEvent {
    t.Helper()
    var raw map[string]json.RawMessage
    if err := json.Unmarshal([]byte(s), &raw); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    e := NewEvent()
    // 重新构建 OrderedEvent 时无法保证顺序；测试只关心字段值，顺序在 Pretty 中由
    // 模板渲染再恢复。
    var any map[string]any
    _ = json.Unmarshal([]byte(s), &any)
    keys := orderedKeys(s)
    for _, k := range keys {
        e.Set(k, any[k])
    }
    return e
}

// orderedKeys 仅供测试：从 JSON 文本中按出现顺序提取顶层 key。
func orderedKeys(s string) []string {
    var out []string
    decoder := json.NewDecoder(strings.NewReader(s))
    decoder.UseNumber()
    if _, err := decoder.Token(); err != nil {
        return out
    }
    for decoder.More() {
        tok, err := decoder.Token()
        if err != nil {
            return out
        }
        out = append(out, tok.(string))
        var v json.RawMessage
        _ = decoder.Decode(&v)
    }
    return out
}

func TestPrettyDaemonEvent(t *testing.T) {
    in := `{"@t":"2026-05-02T12:34:56.789+08:00","@l":"Information","@mt":"supervisor: sing-box ready in {ReadyDurationMs}ms","Source":"daemon","Module":"supervisor","ReadyDurationMs":1218}`
    e := mustParse(t, in)
    loc, _ := time.LoadLocation("Asia/Shanghai")
    out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
    want := "2026-05-02 12:34:56.789 INFO  [daemon] supervisor: sing-box ready in 1218ms"
    if out != want {
        t.Fatalf("\nwant %q\ngot  %q", want, out)
    }
}

func TestPrettyShowsDifferentTZ(t *testing.T) {
    in := `{"@t":"2026-05-02T04:34:56.789+00:00","@l":"Information","@mt":"hello","Source":"daemon"}`
    e := mustParse(t, in)
    loc, _ := time.LoadLocation("Asia/Shanghai")
    out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
    if !strings.HasPrefix(out, "+0000 ") {
        t.Fatalf("expected TZ prefix, got %q", out)
    }
}

func TestPrettySingBoxEvent(t *testing.T) {
    in := `{"@t":"2026-05-02T12:34:57.001+08:00","@l":"Information","@mt":"{Module}/{Type}: {Detail}","Source":"sing-box","Module":"router","Type":"default","Detail":"outbound connection to www.example.com:443"}`
    e := mustParse(t, in)
    loc, _ := time.LoadLocation("Asia/Shanghai")
    out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
    want := "2026-05-02 12:34:57.001 INFO  [sing-box] router/default: outbound connection to www.example.com:443"
    if out != want {
        t.Fatalf("\nwant %q\ngot  %q", want, out)
    }
}

func TestPrettyMissingTemplate(t *testing.T) {
    in := `{"@t":"2026-05-02T12:34:56+08:00","@l":"Warning","Source":"daemon"}`
    e := mustParse(t, in)
    loc, _ := time.LoadLocation("Asia/Shanghai")
    out := Pretty(e, PrettyOptions{LocalTZ: loc, DisableColor: true})
    if !strings.Contains(out, "WARN") || !strings.Contains(out, "[daemon]") {
        t.Fatalf("pretty fallback missing: %q", out)
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/log/... -run TestPretty
```

期望：编译失败。

- [ ] **Step 3：写实现 internal/log/pretty.go**

```go
package log

import (
    "fmt"
    "regexp"
    "strings"
    "time"
)

// PrettyOptions 控制 Pretty 渲染。
type PrettyOptions struct {
    LocalTZ      *time.Location // 与守护进程当前时区相同时省略 TZ 段
    DisableColor bool
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Pretty 把 CLEF 事件渲染为人类可读的一行（不含末尾换行）。
func Pretty(e *OrderedEvent, opts PrettyOptions) string {
    if opts.LocalTZ == nil {
        opts.LocalTZ = time.Local
    }

    var sb strings.Builder

    // 时区段
    tsStr, _ := getString(e, "@t")
    var ts time.Time
    if tsStr != "" {
        if parsed, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
            ts = parsed
        }
    }
    showTZ := false
    if !ts.IsZero() {
        wantOff := offsetSeconds(opts.LocalTZ, ts)
        gotOff := offsetSeconds(ts.Location(), ts)
        showTZ = wantOff != gotOff
    }
    if showTZ {
        sb.WriteString(formatOffset(ts))
        sb.WriteByte(' ')
    }

    // 日期与时间
    if !ts.IsZero() {
        sb.WriteString(ts.Format("2006-01-02 15:04:05.000"))
    } else {
        sb.WriteString("???? ??:??:??.???")
    }
    sb.WriteByte(' ')

    // 级别
    levelName, _ := getString(e, "@l")
    lvl := FromCLEFName(levelName)
    sb.WriteString(padRight(lvl.String(), 5))
    sb.WriteByte(' ')

    // Source 段
    src, _ := getString(e, "Source")
    if src != "" {
        sb.WriteByte('[')
        sb.WriteString(src)
        sb.WriteByte(']')
        sb.WriteByte(' ')
    }

    // 消息：把 @mt 模板里的 {Field} 替换为对应值
    mt, _ := getString(e, "@mt")
    if mt == "" {
        // 退化：拼 Module: Detail / 或 Detail
        mod, _ := getString(e, "Module")
        det, _ := getString(e, "Detail")
        if mod != "" && det != "" {
            sb.WriteString(mod)
            sb.WriteString(": ")
            sb.WriteString(det)
        } else if det != "" {
            sb.WriteString(det)
        }
    } else {
        sb.WriteString(renderTemplate(e, mt))
    }
    return sb.String()
}

func renderTemplate(e *OrderedEvent, tmpl string) string {
    return placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
        name := match[1 : len(match)-1]
        if v, ok := e.Get(name); ok {
            return fmt.Sprintf("%v", v)
        }
        return match
    })
}

func getString(e *OrderedEvent, key string) (string, bool) {
    v, ok := e.Get(key)
    if !ok {
        return "", false
    }
    s, ok := v.(string)
    return s, ok
}

func offsetSeconds(loc *time.Location, ref time.Time) int {
    _, off := ref.In(loc).Zone()
    return off
}

func formatOffset(t time.Time) string {
    _, off := t.Zone()
    sign := byte('+')
    if off < 0 {
        sign = '-'
        off = -off
    }
    h := off / 3600
    m := (off % 3600) / 60
    return fmt.Sprintf("%c%02d%02d", sign, h, m)
}

func padRight(s string, width int) string {
    if len(s) >= width {
        return s
    }
    return s + strings.Repeat(" ", width-len(s))
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/log/... -run TestPretty -v
```

期望：4 个 TestPretty* 全部 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/log/pretty.go internal/log/pretty_test.go
git commit -m "feat(log): add Pretty renderer with timezone-aware suppression"
```

---

## Task 9：上层 emitter（Info/Warn/...）

**Files:**
- Create: `internal/log/emitter.go`
- Create: `internal/log/emitter_test.go`

- [ ] **Step 1：写测试 internal/log/emitter_test.go**

```go
package log

import (
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
)

func TestEmitterFormatsAndWritesJSONL(t *testing.T) {
    dir := t.TempDir()
    w, err := NewWriter(WriterConfig{
        Path:       filepath.Join(dir, "sing-router.log"),
        MaxSize:    0,
        MaxBackups: 0,
    })
    if err != nil {
        t.Fatalf("NewWriter: %v", err)
    }
    defer func() { _ = w.Close() }()

    bus := NewBus(8)
    defer bus.Close()

    em := NewEmitter(EmitterConfig{
        Source:   "daemon",
        MinLevel: LevelInfo,
        Writer:   w,
        Bus:      bus,
    })

    em.Info("supervisor", "supervisor.boot.started", "starting daemon at {Rundir}", map[string]any{"Rundir": "/opt/home/sing-router"})

    // bus 应该至少投递一次
    var mu sync.Mutex
    delivered := 0
    bus.Subscribe(SubscriberFunc{
        MatchFn:   func(*OrderedEvent) bool { return true },
        DeliverFn: func(*OrderedEvent) { mu.Lock(); delivered++; mu.Unlock() },
    })
    em.Warn("zoo", "zoo.preprocess.dropped_field", "dropped field {Field}", map[string]any{"Field": "experimental"})

    if err := w.Sync(); err != nil {
        t.Fatalf("Sync: %v", err)
    }
    data, _ := os.ReadFile(filepath.Join(dir, "sing-router.log"))
    lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
    if len(lines) < 2 {
        t.Fatalf("want at least 2 lines, got %d", len(lines))
    }

    var first map[string]any
    if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
        t.Fatalf("invalid json: %v", err)
    }
    if first["@l"] != "Information" || first["Source"] != "daemon" || first["EventID"] != "supervisor.boot.started" {
        t.Fatalf("unexpected fields: %v", first)
    }
}

func TestEmitterDropsBelowMinLevel(t *testing.T) {
    dir := t.TempDir()
    w, _ := NewWriter(WriterConfig{Path: filepath.Join(dir, "x.log")})
    defer func() { _ = w.Close() }()

    em := NewEmitter(EmitterConfig{
        Source:   "daemon",
        MinLevel: LevelWarn,
        Writer:   w,
        Bus:      NewBus(4),
    })
    em.Info("supervisor", "noop", "msg", nil)
    em.Debug("supervisor", "noop", "msg", nil)
    em.Warn("supervisor", "kept", "msg", nil)

    _ = w.Sync()
    data, _ := os.ReadFile(filepath.Join(dir, "x.log"))
    if !strings.Contains(string(data), "kept") {
        t.Fatal("Warn missing")
    }
    if strings.Contains(string(data), "noop") {
        t.Fatal("Info/Debug should be filtered out")
    }
}
```

- [ ] **Step 2：跑测试看失败**

```bash
go test ./internal/log/... -run TestEmitter
```

期望：编译失败。

- [ ] **Step 3：写实现 internal/log/emitter.go**

```go
package log

import (
    "time"
)

// EmitterConfig 配置 Emitter。
type EmitterConfig struct {
    Source   string  // 事件 Source 字段，例如 "daemon"
    MinLevel Level   // 低于此级别的事件被丢弃
    Writer   *Writer // 必须；CLEF 落盘
    Bus      *Bus    // 可选；nil 时不广播
}

// Emitter 是 daemon 自身事件的入口。所有调用都经此构造 OrderedEvent，
// 写入 Writer 并广播到 Bus。
type Emitter struct {
    cfg EmitterConfig
}

// NewEmitter 创建 emitter。
func NewEmitter(cfg EmitterConfig) *Emitter {
    if cfg.Source == "" {
        cfg.Source = "daemon"
    }
    return &Emitter{cfg: cfg}
}

// Trace/Debug/Info/Warn/Error/Fatal 是常用 helper。
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

// PublishExternal 接收已经构建好的事件（如 sing-box stderr 解析得到），
// 走与 Emitter 同一条出口（Writer + Bus）。MinLevel 不过滤外部事件。
func (e *Emitter) PublishExternal(ev *OrderedEvent) {
    if e.cfg.Writer != nil {
        _ = e.cfg.Writer.Write(ev)
    }
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
    if e.cfg.Writer != nil {
        _ = e.cfg.Writer.Write(ev)
    }
    if e.cfg.Bus != nil {
        e.cfg.Bus.Publish(ev)
    }
}
```

- [ ] **Step 4：跑测试**

```bash
go test ./internal/log/... -v
```

期望：log 包所有测试 `PASS`。

- [ ] **Step 5：提交**

```bash
git add internal/log/emitter.go internal/log/emitter_test.go
git commit -m "feat(log): add Emitter helper that writes CLEF + publishes to Bus"
```

---

# Phase 3：配置与路由参数

## Task 10：daemon.toml 解析

**Files:**
- Create: `internal/config/daemon_toml.go`
- Create: `internal/config/daemon_toml_test.go`

- [ ] **Step 1：拉 toml 依赖**

```bash
go get github.com/BurntSushi/toml@v1.4.0
go mod tidy
```

- [ ] **Step 2：写测试 internal/config/daemon_toml_test.go**

```go
package config

import (
    "os"
    "path/filepath"
    "testing"
)

const sampleTOML = `
[runtime]
sing_box_binary = "bin/sing-box"
config_dir      = "config.d"
ui_dir          = "ui"

[http]
listen = "127.0.0.1:9998"

[log]
level         = "debug"
file          = "log/sing-router.log"
rotate        = "internal"
max_size_mb   = 5
max_backups   = 3
disable_color = false

[zoo]
extract_keys              = ["outbounds", "route.rules", "route.rule_set", "route.final"]
rule_set_dedup_strategy   = "builtin_wins"
outbound_collision_action = "reject"

[download]
mirror_prefix          = ""
sing_box_url_template  = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"
sing_box_default_version = "latest"
cn_list_url            = "https://example.com/cn.txt"
http_timeout_seconds   = 60
http_retries           = 3

[install]
download_sing_box  = true
download_cn_list   = true
download_zashboard = false
auto_start         = false
`

func TestLoadDaemonConfig(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "daemon.toml")
    if err := os.WriteFile(path, []byte(sampleTOML), 0644); err != nil {
        t.Fatal(err)
    }
    cfg, err := LoadDaemonConfig(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if cfg.Runtime.SingBoxBinary != "bin/sing-box" {
        t.Fatal("SingBoxBinary mismatch")
    }
    if cfg.HTTP.Listen != "127.0.0.1:9998" {
        t.Fatal("HTTP.Listen mismatch")
    }
    if cfg.Log.Level != "debug" {
        t.Fatal("Log.Level mismatch")
    }
    if cfg.Log.MaxSizeMB != 5 {
        t.Fatal("Log.MaxSizeMB mismatch")
    }
    if cfg.Zoo.RuleSetDedupStrategy != "builtin_wins" {
        t.Fatal("Zoo.RuleSetDedupStrategy mismatch")
    }
    if cfg.Install.DownloadSingBox != true {
        t.Fatal("Install.DownloadSingBox mismatch")
    }
}

func TestLoadDaemonConfigDefaults(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "daemon.toml")
    // 空文件 → 应得到全默认
    if err := os.WriteFile(path, []byte(""), 0644); err != nil {
        t.Fatal(err)
    }
    cfg, err := LoadDaemonConfig(path)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if cfg.Runtime.SingBoxBinary != "bin/sing-box" {
        t.Fatalf("default SingBoxBinary mismatch: %q", cfg.Runtime.SingBoxBinary)
    }
    if cfg.HTTP.Listen != "127.0.0.1:9998" {
        t.Fatal("default HTTP.Listen mismatch")
    }
    if cfg.Log.Level != "info" {
        t.Fatal("default Log.Level mismatch")
    }
    if cfg.Log.MaxSizeMB != 10 {
        t.Fatal("default Log.MaxSizeMB mismatch")
    }
    if cfg.Log.MaxBackups != 5 {
        t.Fatal("default Log.MaxBackups mismatch")
    }
    if cfg.Zoo.RuleSetDedupStrategy != "builtin_wins" {
        t.Fatal("default RuleSetDedupStrategy mismatch")
    }
    if cfg.Download.HTTPTimeoutSeconds != 60 {
        t.Fatal("default http_timeout mismatch")
    }
    if cfg.Download.CNListURL == "" {
        t.Fatal("default cn_list_url should not be empty")
    }
}

func TestLoadDaemonConfigMissingFile(t *testing.T) {
    cfg, err := LoadDaemonConfig("/nonexistent/path/daemon.toml")
    if err != nil {
        t.Fatalf("missing file should default-load, got err: %v", err)
    }
    if cfg.HTTP.Listen != "127.0.0.1:9998" {
        t.Fatal("missing file: defaults expected")
    }
}
```

- [ ] **Step 3：跑测试看失败**

```bash
go test ./internal/config/... -run TestLoadDaemonConfig
```

期望：编译失败。

- [ ] **Step 4：写实现 internal/config/daemon_toml.go**

```go
// Package config 包含 sing-router 的配置加载与 zoo.json 预处理。
package config

import (
    "errors"
    "os"

    "github.com/BurntSushi/toml"
)

// DefaultCNListURL 是 daemon.toml 缺省时的 cn.txt 拉取地址。
const DefaultCNListURL = "https://raw.githubusercontent.com/17mon/china_ip_list/master/china_ip_list.txt"

// DefaultSingBoxURLTemplate 是 sing-box 二进制下载模板。
const DefaultSingBoxURLTemplate = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"

// DaemonConfig 反映 daemon.toml 的全部字段；未来 B/C/E/F 模块各自加自己的 section。
type DaemonConfig struct {
    Runtime    RuntimeConfig    `toml:"runtime"`
    HTTP       HTTPConfig       `toml:"http"`
    Log        LogConfig        `toml:"log"`
    Supervisor SupervisorConfig `toml:"supervisor"`
    Zoo        ZooConfig        `toml:"zoo"`
    Download   DownloadConfig   `toml:"download"`
    Router     RouterConfig     `toml:"router"`
    Install    InstallConfig    `toml:"install"`
}

type RuntimeConfig struct {
    Rundir         string `toml:"rundir"`
    SingBoxBinary  string `toml:"sing_box_binary"`
    ConfigDir      string `toml:"config_dir"`
    UIDir          string `toml:"ui_dir"`
}

type HTTPConfig struct {
    Listen string `toml:"listen"`
    Token  string `toml:"token"`
}

type LogConfig struct {
    Level        string `toml:"level"`
    File         string `toml:"file"`
    Rotate       string `toml:"rotate"`
    MaxSizeMB    int    `toml:"max_size_mb"`
    MaxBackups   int    `toml:"max_backups"`
    DisableColor bool   `toml:"disable_color"`
    IncludeStack bool   `toml:"include_stack"`
}

type SupervisorConfig struct {
    ReadyCheckDialInbounds         *bool  `toml:"ready_check_dial_inbounds"`
    ReadyCheckClashAPI             *bool  `toml:"ready_check_clash_api"`
    ReadyCheckTimeoutMs            *int   `toml:"ready_check_timeout_ms"`
    ReadyCheckIntervalMs           *int   `toml:"ready_check_interval_ms"`
    CrashPreReadyAction            string `toml:"crash_pre_ready_action"`
    CrashPostReadyBackoffMs        []int  `toml:"crash_post_ready_backoff_ms"`
    IptablesKeepWhenBackoffLtMs    *int   `toml:"iptables_keep_when_backoff_lt_ms"`
    StopGraceSeconds               *int   `toml:"stop_grace_seconds"`
}

type ZooConfig struct {
    ExtractKeys             []string `toml:"extract_keys"`
    RuleSetDedupStrategy    string   `toml:"rule_set_dedup_strategy"`
    OutboundCollisionAction string   `toml:"outbound_collision_action"`
}

type DownloadConfig struct {
    MirrorPrefix          string `toml:"mirror_prefix"`
    SingBoxURLTemplate    string `toml:"sing_box_url_template"`
    SingBoxDefaultVersion string `toml:"sing_box_default_version"`
    CNListURL             string `toml:"cn_list_url"`
    HTTPTimeoutSeconds    int    `toml:"http_timeout_seconds"`
    HTTPRetries           int    `toml:"http_retries"`
}

type RouterConfig struct {
    DnsPort      *int    `toml:"dns_port"`
    RedirectPort *int    `toml:"redirect_port"`
    RouteMark    *string `toml:"route_mark"`
    BypassMark   *string `toml:"bypass_mark"`
    Tun          *string `toml:"tun"`
    FakeIP       *string `toml:"fakeip"`
    LAN          *string `toml:"lan"`
    RouteTable   *int    `toml:"route_table"`
    ProxyPorts   *string `toml:"proxy_ports"`
}

type InstallConfig struct {
    DownloadSingBox   bool `toml:"download_sing_box"`
    DownloadCNList    bool `toml:"download_cn_list"`
    DownloadZashboard bool `toml:"download_zashboard"`
    AutoStart         bool `toml:"auto_start"`
}

// LoadDaemonConfig 从给定路径加载 daemon.toml。文件不存在时返回全默认 config，
// 不报错（首次 install 之前 status 命令也能用）。
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
    cfg := defaultConfig()

    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return cfg, nil
        }
        return nil, err
    }
    if _, err := toml.Decode(string(data), cfg); err != nil {
        return nil, err
    }
    applyDefaults(cfg)
    return cfg, nil
}

func defaultConfig() *DaemonConfig {
    cfg := &DaemonConfig{
        Runtime: RuntimeConfig{
            SingBoxBinary: "bin/sing-box",
            ConfigDir:     "config.d",
            UIDir:         "ui",
        },
        HTTP: HTTPConfig{Listen: "127.0.0.1:9998"},
        Log: LogConfig{
            Level:      "info",
            File:       "log/sing-router.log",
            Rotate:     "internal",
            MaxSizeMB:  10,
            MaxBackups: 5,
        },
        Zoo: ZooConfig{
            ExtractKeys:             []string{"outbounds", "route.rules", "route.rule_set", "route.final"},
            RuleSetDedupStrategy:    "builtin_wins",
            OutboundCollisionAction: "reject",
        },
        Download: DownloadConfig{
            SingBoxURLTemplate:    DefaultSingBoxURLTemplate,
            SingBoxDefaultVersion: "latest",
            CNListURL:             DefaultCNListURL,
            HTTPTimeoutSeconds:    60,
            HTTPRetries:           3,
        },
        Install: InstallConfig{
            DownloadSingBox:   true,
            DownloadCNList:    true,
            DownloadZashboard: false,
            AutoStart:         false,
        },
    }
    return cfg
}

// applyDefaults 在解码后填补未提供的字段。
func applyDefaults(cfg *DaemonConfig) {
    if cfg.Runtime.SingBoxBinary == "" {
        cfg.Runtime.SingBoxBinary = "bin/sing-box"
    }
    if cfg.Runtime.ConfigDir == "" {
        cfg.Runtime.ConfigDir = "config.d"
    }
    if cfg.Runtime.UIDir == "" {
        cfg.Runtime.UIDir = "ui"
    }
    if cfg.HTTP.Listen == "" {
        cfg.HTTP.Listen = "127.0.0.1:9998"
    }
    if cfg.Log.Level == "" {
        cfg.Log.Level = "info"
    }
    if cfg.Log.File == "" {
        cfg.Log.File = "log/sing-router.log"
    }
    if cfg.Log.Rotate == "" {
        cfg.Log.Rotate = "internal"
    }
    if cfg.Log.MaxSizeMB == 0 {
        cfg.Log.MaxSizeMB = 10
    }
    if cfg.Log.MaxBackups == 0 {
        cfg.Log.MaxBackups = 5
    }
    if len(cfg.Zoo.ExtractKeys) == 0 {
        cfg.Zoo.ExtractKeys = []string{"outbounds", "route.rules", "route.rule_set", "route.final"}
    }
    if cfg.Zoo.RuleSetDedupStrategy == "" {
        cfg.Zoo.RuleSetDedupStrategy = "builtin_wins"
    }
    if cfg.Zoo.OutboundCollisionAction == "" {
        cfg.Zoo.OutboundCollisionAction = "reject"
    }
    if cfg.Download.SingBoxURLTemplate == "" {
        cfg.Download.SingBoxURLTemplate = DefaultSingBoxURLTemplate
    }
    if cfg.Download.SingBoxDefaultVersion == "" {
        cfg.Download.SingBoxDefaultVersion = "latest"
    }
    if cfg.Download.CNListURL == "" {
        cfg.Download.CNListURL = DefaultCNListURL
    }
    if cfg.Download.HTTPTimeoutSeconds == 0 {
        cfg.Download.HTTPTimeoutSeconds = 60
    }
    if cfg.Download.HTTPRetries == 0 {
        cfg.Download.HTTPRetries = 3
    }
}
```

- [ ] **Step 5：跑测试**

```bash
go test ./internal/config/... -v
```

期望：所有 TestLoadDaemonConfig* 通过。

- [ ] **Step 6：提交**

```bash
git add go.mod go.sum internal/config/daemon_toml.go internal/config/daemon_toml_test.go
git commit -m "feat(config): load daemon.toml with sane defaults across sections"
```

---

# 后续 Phase 总览（详见后续追加文档）

> 本文档因篇幅限制按 Phase 切分；Phase 4 起的细节作为后续追加文件 `2026-05-02-sing-router-module-a.part2.md` ...等。每一份后续文件遵循相同的 TDD 节奏与提交粒度。

接下来要在后续文件中分别覆盖：

- **Phase 3（续）**：Task 11（routing.go 路由参数 + env 注入），Task 12（singbox.go check wrapper）
- **Phase 4**：Task 13–14（assets/ 目录与 embed.go；从当前 repo 的 `config/*.json` 派生默认 fragment）
- **Phase 5**：Task 15（startup.sh 改造为 env-driven），Task 16（teardown.sh），Task 17（shell.runner + tests）
- **Phase 6**：Task 18–22（zoo preprocessor 全分支 TDD：filter / dedup / rewrite / outbound collision / atomic write + last-good rollback）
- **Phase 7**：Task 23（state.json），Task 24（statemachine 转移），Task 25（ready 检测）
- **Phase 8**：Task 26（fake-sing-box 桩），Task 27–30（supervisor boot/reload/crash/stop+start/shutdown）
- **Phase 9**：Task 31–35（HTTP API：status/start_stop/check/reapply/logs/script/shutdown）+ daemon.go 入口
- **Phase 10**：Task 36–43（CLI 各子命令）
- **Phase 11**：Task 44（layout）, 45（seed）, 46（initd）, 47（jffs_hooks 100%）, 48（download mirror_prefix）, 49（cli/install）, 50（cli/uninstall）, 51（cli/doctor）
- **Phase 12**：Task 52（main.go 全部 wire），Task 53（cross-compile linux/arm64 release 验证）

每份后续文件都要遵守：

1. 文件路径、代码与命令完整呈现；不留 placeholder
2. 严格 TDD：测试先 → 看失败 → 实现 → 看通过 → 提交
3. 跨任务的类型与方法名严格一致（特别是 `OrderedEvent`、`Emitter`、`DaemonConfig` 这些贯穿全 plan 的类型）
4. `internal/config/zoo.go` 与 `internal/install/jffs_hooks.go` 必须达到 100% 行覆盖

为避免计划文档无限膨胀，**后续 Phase 的逐 Task 展开**与当前文件**配套提交**；执行 plan 时按编号顺序推进，每个 Phase 完成后做一次"集成 sanity check"（`go test ./... && go build ./cmd/sing-router`）。

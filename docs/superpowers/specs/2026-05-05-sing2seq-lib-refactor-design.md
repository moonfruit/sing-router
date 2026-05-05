# sing2seq 库化重构 + sing-router 接入设计

- 日期：2026-05-05
- 范围：`github.com/moonfruit/sing-router`、`github.com/moonfruit/sing2seq`
- 状态：设计已经过用户逐节确认，等待 spec 审阅后进入 writing-plans

## 背景

`sing2seq` 是一个独立的 Go CLI（仓库：`/Users/moon/Workspace.localized/go/mod/sing2seq`），把 sing-box stderr 解析为 CLEF 事件并异步批量投递到 Seq。它的 module 名是 `sing2seq`，所有代码在 `package main`，无任何对外公开 API。

`sing-router` 在 `internal/log` 把 sing2seq 的解析逻辑（`parser.go`、`OrderedEvent`）整体复制了一份，并把 `orderedEvent` 改名 `OrderedEvent`、加了 `ParseSingBoxLine` 公开入口，以便 `internal/daemon/supervisor.go` 把 sing-box stderr 喂进 daemon 的 emitter。`internal/log` 还有一套自己的 `Emitter` / `Bus` / `Writer` / `Pretty` / `Level`，前两者也是从 sing2seq 思路迁移过来的，后三者是 sing-router 专属（按 daemon 的需要设计：CLEF 文件落盘、`logs` 命令的人读渲染、Level 与字符串互转）。

目前两个仓库间有以下问题：

1. **解析逻辑双份维护**：sing2seq 的 `parser.go` 演进时，sing-router 这边需要手工同步。
2. **Event 类型双份维护**：`orderedEvent` 与 `OrderedEvent` 是同一个东西，分两处实现。
3. **sing-router 没有向 Seq 直接发送的能力**：现状只能通过外部部署 sing2seq CLI 桥接。
4. **sing2seq 自身诊断（buffer overflow、retry、shutdown 错误）只能走 stderr 字符串**，不能进入 Seq；丢失了"sink 自身健康"这一信号。

## 目标

1. sing2seq 改造为可被 `go get` 引用的库（同仓保留 CLI），暴露 sing-box → CLEF 的解析 + emitter + bus + 异步 seq sink 等公开 API。
2. sing-router 通过 `require` 替换源码拷贝；先替换 parser/Event，再替换 Emitter/Bus，最终 `internal/log` 只剩项目专属的 Writer/Pretty/Level/wireup。
3. 在重构同时把 sing2seq 自身诊断也作为 CLEF 事件（`Source="sing2seq"`），与解析事件（`Source="sing-box"`）在同一条 bus 上 fan-out 到 pretty stderr 与 seq sink。
4. 异步发送的合约清晰、内存上限可证：`Submit` 不阻塞应用、`pending` 满则丢最旧、有诊断事件可观测。
5. 保留 sing2seq CLI 的彩色 stderr 渲染能力，作为 cmd 内部实现，暂不导出。

## 非目标

- 本轮不做 sing-router 端的 seq sink 接入（仅写"未来工作"）。
- 不重写 sing-router 的 Writer/Pretty/Level；它们与 daemon 的需求耦合，保留在 `internal/log`。
- 不改 sing2seq 解析行为本身（regex、字段名、字段顺序保持现状），保证 v1.3.0 对 Seq 上既有 dashboard/查询语法零冲击。
- 不引入 `slog`。两边项目当前都没在用，引入会扩散到不该被扩散的地方。

## 决策摘要

| ID | 决策 | 含义 |
|---|---|---|
| D1 | 发布到 `github.com/moonfruit/sing2seq` | sing-router 通过 require 引用；本地开发期 `replace` |
| D2 | 保留 CLI，仓库 lib + cmd | `cmd/sing2seq/` 放二进制入口；`pipe`/`run` 子命令不变 |
| D3 | 两个公开包：`clef`、`seq` | clef 含 Event/parser/Emitter/Bus/Level；seq 含 Sink |
| D4 | sing-router 直接用 `clef.Event` | `internal/log.OrderedEvent` 改 type 别名后逐步退役 |
| D5 | 本轮实现 seq 包但不接 sing-router | sing2seq CLI 直接吃 `seq.Sink`；sing-router 接入下一轮 spec |
| D6 | seq 沿用 drop-oldest | `MaxPending=50000`、`DropTarget=25000`、`BatchSize=200`、退避 1s→60s |
| D7 | Emitter+Bus 上提到 clef 包 | sing-router 的 Emitter/Bus 退役；Level 通过别名兼容存量代码 |
| D8 | sing2seq 内部诊断走 emitter | `Source="sing2seq", Module="seq.sink"`，与解析事件并入 bus |
| D9 | 下一版 tag `v1.3.0` | minor bump；module 路径变更不算 break 因为零外部 importer |

## 架构

### 仓库布局

**sing2seq（v1.3.0 之后）**

```
sing2seq/
├── go.mod                # module github.com/moonfruit/sing2seq
├── clef/                 # 公开库
│   ├── event.go          # Event + MarshalJSON
│   ├── parser.go         # ParseSingBoxLine
│   ├── parser_test.go
│   ├── emitter.go        # Emitter
│   ├── bus.go            # Bus
│   ├── level.go          # Level + CLEFName / ParseLevel / FromCLEFName
│   └── *_test.go
├── seq/
│   ├── sink.go           # Sink（原 Batcher）
│   └── sink_test.go      # 新增覆盖
├── cmd/sing2seq/
│   ├── main.go           # cobra 入口
│   ├── run.go            # RunCmd
│   └── pretty.go         # 内部彩色 stderr 渲染器（暂不导出）
├── compose.yaml
├── go.sum
├── LICENSE
└── README.md
```

**sing-router 改造面**

| 文件 | 变化 |
|---|---|
| `go.mod` | 加 `require github.com/moonfruit/sing2seq v1.3.0`；本地开发期 `replace` |
| `internal/log/parser.go` | 删除 |
| `internal/log/parser_test.go` | 删除 |
| `internal/log/clef.go` | 步骤 A：`OrderedEvent` 改为 `type OrderedEvent = clef.Event`；步骤 B：删除 |
| `internal/log/emitter.go` / `emitter_test.go` | 步骤 B：删除 |
| `internal/log/bus.go` / `bus_test.go` | 步骤 B：删除 |
| `internal/log/level.go` | 步骤 B：保留为 `type Level = clef.Level` + const 别名（兼容存量调用） |
| `internal/log/writer.go` / `pretty.go` 及对应 test | 保留；按需更新 import 与字段类型 |
| `internal/log/wireup.go` | 步骤 B：新增；把 daemon 的 emitter+bus+writer+pretty 串起来的胶水搬出来 |
| `internal/cli/wireup_daemon.go` | 步骤 B：emitter/bus 构造改用 clef |
| `internal/daemon/{daemon,api,supervisor}.go` | 步骤 B：`*log.Emitter` → `*clef.Emitter`；步骤 A 不动（别名兼容） |
| `internal/daemon/supervisor.go` | 步骤 A：`log.ParseSingBoxLine` → `clef.ParseSingBoxLine` |
| `internal/cli/logs.go` | 步骤 B：`log.OrderedEvent` → `clef.Event` |

### 信号流（sing2seq CLI 视角）

```
sing-box stderr ─→ scanner ─→ clef.ParseSingBoxLine ─→ Event(Source=sing-box) ─┐
                                                                               ├─→ clef.Bus ─→ ┌─ pretty(stderr)
sing2seq Emitter(Source=sing2seq) ──→ Event(Source=sing2seq) ──────────────────┘              ├─ seq.Sink (URL≠"")
                                                                                              └─ stdout JSON sink (URL="")
                                                                                                       │
                                          drop/retry/shutdown 警告 ◀────────── Sink 内部回路（通过 emitter，回流 bus）
```

`-u ""` debug 模式下 pretty 渲染器与 stdout JSON sink 同时存在；pretty 写 stderr、JSON 写 stdout，互不干扰。

## 公开 API

### `clef` 包

```go
package clef

// ---------- Event ----------

type Event struct{ /* keys/values 私有 */ }
func NewEvent() *Event
func (e *Event) Set(k string, v any)
func (e *Event) Get(k string) (any, bool)
func (e *Event) Keys() []string
func (e *Event) MarshalJSON() ([]byte, error)

// ---------- Level ----------

type Level int
const (
    LevelTrace Level = iota
    LevelDebug
    LevelInfo
    LevelWarn
    LevelError
    LevelFatal
)
func (l Level) CLEFName() string                  // "Verbose"/"Debug"/"Information"/"Warning"/"Error"/"Fatal"
func ParseLevel(s string) (Level, error)
func FromCLEFName(s string) (Level, bool)

// ---------- Parser ----------

// ParseSingBoxLine 解析一行 sing-box stderr（含或不含 ANSI/CRLF）。
// 不可解析的行降级为 Parsed=false 的原始事件；空行返回 nil。
// 事件已含 Source="sing-box"。
func ParseSingBoxLine(line string) *Event

// ---------- Emitter ----------

type EmitterConfig struct {
    Source   string  // 必填，写入每条事件的 Source 字段
    MinLevel Level   // 低于此级别的事件被丢弃；不影响 PublishExternal
    Bus      *Bus    // 可选；事件发布到此 Bus；nil 则丢弃
}

type Emitter struct{ /* … */ }
func NewEmitter(cfg EmitterConfig) *Emitter

func (e *Emitter) Trace(module, eventID, mt string, fields map[string]any)
func (e *Emitter) Debug(module, eventID, mt string, fields map[string]any)
func (e *Emitter) Info(module, eventID, mt string, fields map[string]any)
func (e *Emitter) Warn(module, eventID, mt string, fields map[string]any)
func (e *Emitter) Error(module, eventID, mt string, fields map[string]any)
func (e *Emitter) Fatal(module, eventID, mt string, fields map[string]any)

// PublishExternal 发布已构造好的事件（如 ParseSingBoxLine 结果），不应用 MinLevel。
func (e *Emitter) PublishExternal(ev *Event)

// ---------- Bus ----------

type Bus struct{ /* … */ }
func NewBus() *Bus

// Subscribe 注册 subscriber；返回 unsubscribe。投递在内部 goroutine 上，
// subscriber 函数不应阻塞。filter 为 nil 表示全收。
func (b *Bus) Subscribe(fn func(*Event), filter func(*Event) bool) (unsubscribe func())

// Publish 非阻塞地把事件发给所有 subscriber。subscriber 拿到的是同一个 *Event 指针；
// 合约：subscriber 不可修改事件（只读消费）。
func (b *Bus) Publish(ev *Event)

// Close 停止接受新发布；等待已入队事件 drain；之后 Publish 变 no-op。
func (b *Bus) Close()
```

字段顺序合约：`Set` 第一次写入某个 key 时追加到 `keys` 末尾，重复写仅更新值不动顺序。`MarshalJSON` 按 `keys` 顺序输出。**Seq UI 按 JSON 字段顺序展示，不要随便改字段写入顺序。**

Emitter 内部按以下顺序写字段，与现 sing-router `internal/log/emitter.go` 一致：
`@t` → `@l` → `@mt`（若非空）→ `Source` → `Module`（若非空）→ `EventID`（若非空）→ user fields

### `seq` 包

```go
package seq

import (
    "context"
    "net/http"
    "time"
    "github.com/moonfruit/sing2seq/clef"
)

type Config struct {
    URL        string
    APIKey     string
    Insecure   bool
    HTTPClient *http.Client
    Emitter    *clef.Emitter // 可选；nil 时诊断 fallback 到 stderr

    BatchSize      int           // default 200
    ChannelBuffer  int           // default 1024
    MaxPending     int           // default 50000
    DropTarget     int           // default 25000
    InitialBackoff time.Duration // default 1s
    MaxBackoff     time.Duration // default 60s
}

type Sink struct{ /* … */ }

func NewSink(cfg Config) *Sink   // 套默认值；不启动 goroutine
func (s *Sink) Start()           // 启动 manager；幂等性不保证（call once）
func (s *Sink) Submit(ev *clef.Event) // O(1)、不阻塞；nil 忽略
func (s *Sink) Close() error     // 阻塞 drain pending；返回 drain 期间最后一个 post error
```

行为合约：

- `Submit` 从不阻塞。manager goroutine 的 `select` 只做 O(1) 工作，channel 不会满。
- 满 `MaxPending` 时裁到 `DropTarget`，丢最旧；同时通过 `Emitter` 发出诊断事件。
- HTTP 失败指数退避 `InitialBackoff` → `MaxBackoff`；同一时刻最多一个 in-flight POST。
- shutdown（`Close()` 后）期间 post 失败直接放弃 pending，发 `shutdown_post_failed` 事件。
- POST `<URL>/ingest/clef`，header `Content-Type: application/vnd.serilog.clef`、`X-Seq-ApiKey`（可选）；body 是批内事件 ndjson。

诊断事件字段约定（写死，便于 Seq 端配置 dashboard）：

| EventID | @l | fields | @mt 模板 |
|---|---|---|---|
| `buffer_overflow` | Warning | `Dropped`, `TotalDropped` | `buffer overflow: dropped {Dropped} oldest events (total dropped={TotalDropped})` |
| `post_failed` | Warning | `Pending`, `Error`, `RetryIn` | `post failed (pending={Pending}): {Error}; retry in {RetryIn}` |
| `shutdown_post_failed` | Error | `Pending`, `Error` | `post failed during shutdown (pending={Pending}): {Error}; dropping remaining events` |

所有诊断事件都带 `Module="seq.sink"`，Source 由 caller 给 Emitter 时决定。

### 回路安全性

`seq.Sink` 自己**不**订阅 bus；caller 负责 `bus.Subscribe(sink.Submit, filter)`。Sink 内部诊断事件经外部 emitter → bus → sink.Submit，不会无限循环：

- emitter 自己不订阅 bus；
- sink.Submit 仅 O(1) append 到 pending；
- 只有 manager goroutine 的失败/丢弃路径产生诊断事件，**Submit 路径绝不产生**——这是切断潜在循环的关键不变量。

## sing2seq CLI wireup（cmd/sing2seq）

```go
// 伪代码
func runPipe(opts Options) error {
    bus := clef.NewBus()
    em := clef.NewEmitter(clef.EmitterConfig{
        Source:   "sing2seq",
        MinLevel: clef.LevelInfo,
        Bus:      bus,
    })

    var sinkClose func() error
    if opts.URL == "" {
        unsub := bus.Subscribe(stdoutJSONLineWriter(os.Stdout), nil)
        sinkClose = func() error { unsub(); return nil }
    } else {
        sk := seq.NewSink(seq.Config{
            URL: opts.URL, APIKey: opts.APIKey, Insecure: opts.Insecure,
            Emitter: em,
        })
        sk.Start()
        bus.Subscribe(sk.Submit, nil)
        sinkClose = sk.Close
    }

    bus.Subscribe(pretty.NewRenderer(opts.Timestamp, opts.DisableColor).Render, nil)

    scanner := bufio.NewScanner(r)
    scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
    for scanner.Scan() {
        if ev := clef.ParseSingBoxLine(scanner.Text()); ev != nil {
            em.PublishExternal(ev)
        }
    }

    err := sinkClose()
    bus.Close()
    return err
}
```

### Pretty 渲染器规则

- `Source="sing-box"`：保持现状格式（`<Module>[/<Type>][[<Tag>]]: <Detail>`，level/timestamp/颜色按现 `log.go`）。
- `Source="sing2seq"`：`sing2seq[/<Module>]: <Detail>`，Module 字段（如 `seq.sink`）作为可选后缀。
- 颜色映射保持现状（仅 WARN/ERROR/FATAL 上色）；`--timestamp` 决定是否带 `-0700 YYYY-MM-DD HH:MM:SS.mmm` 前缀。

### 关闭顺序

1. scanner 退出（stdin EOF 或 sing-box 子进程退出）。
2. `sinkClose()`：sink 排空 pending HTTP 队列。drain 期间产生的 `shutdown_post_failed` 诊断事件经 emitter → bus 给 pretty 渲染（此时 bus 仍开）。
3. `bus.Close()`：停止接受新 publish，drain 各 subscriber 队列；pretty 把最后的积压事件渲染完。

> 注：必须先 sink 后 bus。反过来的话 sink drain 期间的诊断事件会因为 bus.Publish 变 no-op 而丢失，stderr 也无法兜底（fallback 到 stderr 仅在 caller 没传 Emitter 时触发，是 §3 的合约）。

## sing-router 接入（增量两步）

### 步骤 A — Parser/Event 替换

| 文件 | 动作 |
|---|---|
| `go.mod` | `require github.com/moonfruit/sing2seq v1.3.0` + 本地 `replace` |
| `internal/log/parser.go` | 删除 |
| `internal/log/parser_test.go` | 删除 |
| `internal/log/clef.go` | `OrderedEvent` 改 `type OrderedEvent = clef.Event`；`NewEvent` 改 `var NewEvent = clef.NewEvent` |
| `internal/log/clef_test.go` | 保留（验证别名行为） |
| `internal/daemon/supervisor.go` | `log.ParseSingBoxLine` → `clef.ParseSingBoxLine` |

验收：`go build ./...` + `go test ./...` 全绿；与改造前 sing-box stderr 解析产物逐字节相同。

### 步骤 B — Emitter/Bus 退役

| 文件 | 动作 |
|---|---|
| `internal/log/emitter.go` / `emitter_test.go` | 删除 |
| `internal/log/bus.go` / `bus_test.go` | 删除 |
| `internal/log/clef.go` | `OrderedEvent` 别名删除（迁移完成） |
| `internal/log/level.go` | 简化为 `type Level = clef.Level` + const 别名 |
| `internal/log/wireup.go` | 新文件；构造 bus + emitter + writer subscriber + pretty subscriber |
| `internal/cli/wireup_daemon.go` | 改用 `clef.NewEmitter`/`clef.NewBus` + 调 wireup helper |
| `internal/cli/logs.go` | `log.OrderedEvent` → `clef.Event` |
| `internal/daemon/*.go` | `*log.Emitter` → `*clef.Emitter` |

验收：`go build ./...` + `go test ./...` 全绿；端到端冒烟（启 daemon、看 CLEF 文件、`logs` 命令 pretty 渲染）。

### Replace 流程

```bash
# 本地开发期：
go mod edit -replace github.com/moonfruit/sing2seq=/Users/moon/Workspace.localized/go/mod/sing2seq

# 推 sing2seq v1.3.0 后：
go mod edit -dropreplace github.com/moonfruit/sing2seq
go get github.com/moonfruit/sing2seq@v1.3.0
```

合并到 sing-router main 之前必须 `dropreplace`。

## 测试策略

### sing2seq

- 解析、Event、Emitter、Bus、Level 测试从 sing-router `internal/log/*_test.go` 整体搬过去。
- `clef/bus_test.go` 加并发 publish + 动态 sub/unsub 的 race 测试。
- `seq/sink_test.go` 新增覆盖（用 `httptest.NewServer`）：
  - 正常路径：连续 Submit → POST 收到 ndjson body
  - 失败重试：mock server 前两次 500、第三次 200，验证退避后成功
  - buffer overflow：注入大量事件 + 永远阻塞的 server，验证 drop 数量、`buffer_overflow` 诊断事件被发出
  - shutdown drain：Close 阻塞直到 pending 排空；server 200 时返回 nil；shutdown 期间 server 500 时返回最后一个 error
  - emitter 回路：诊断事件能通过外部 emitter 投回 bus（用 capturing subscriber 验证）

### sing-router

- 步骤 A：跑现有测试，验证别名替换无回归。
- 步骤 B：删除已搬到上游的 `parser_test.go`/`emitter_test.go`/`bus_test.go`；`writer_test.go`、`pretty_test.go` 更新 import；新加 `wireup_test.go` 验证 bus/writer/pretty 订阅装配。
- daemon 端到端：`internal/daemon/supervisor_test.go` 的 `newTestEmitter` 改用 `clef.NewEmitter`。

### 回归基准

- 步骤 A：sing-box stderr 解析产物字节对比改造前。
- 步骤 B：daemon 写入 CLEF 文件的字段顺序、`Source`/`Module`/`@t` 与改造前一致。

### CI

- sing2seq：`go test ./...` + `go vet ./...`。
- sing-router：保持现 CI；`replace` 不进 main 分支。

## 版本与发布

- `v1.3.0`：本轮一次性发布。包含 module path 变更、`clef`/`seq` 新增、`cmd/sing2seq` 取代根 main、emitter+bus wireup、`-u ""` 模式 pretty + stdout 同开。README 注明行为差异与 module 路径变更。
- `v1.3.x`：bug fix、补测试、注释；不动公开 API。
- 未来 `v1.4.x`：解析新 sing-box 字段、Emitter fields builder 等增量；保持向后兼容。
- 未来 `v2.x`：任何破坏式 API 变更升 module path 到 `/v2`（Go module 规则）。

sing-router 步骤 A、步骤 B 各自走 minor bump；daemon 对外接口零变化。

## 未来工作（不在本轮范围）

- **sing-router 接入 seq.Sink**：在新 spec 里设计。涉及配置 schema（`daemon.toml` 加 `[seq]` 段）、信号生命周期（daemon stop 时 drain seq sink 的超时/兜底）、生产环境网络可达性（DNS、TLS、代理）、Source 分配（daemon 自身用 `daemon`，转发的 sing-box stderr 仍用 `sing-box`）。
- **Pretty 渲染器上提到 clef 包**：仅当确认 sing-router 想替换自己 pretty 时再考虑；目前两边 pretty 需求不同（sing-router 有 daemon 专属字段、人读 CLI）。
- **Emitter fields builder**：当前 fields 用 `map[string]any`，无法保证多次写入的字段间顺序。如果将来发现某些 caller 要严格控制 user fields 顺序，再加一个 `EventBuilder` API。

## 风险

- **Module path 变更后的 import 升级**：sing2seq 上游使用者需要改 import。当前 sing-router 是源码拷贝、不受影响；其它使用者按 README 迁移说明操作。
- **字段顺序回归**：搬包过程中如果 Emitter 写字段顺序与原 sing-router 不一致，CLEF 文件向后不兼容（Seq UI 列顺序变）。回归基准用字节级 diff 兜住。
- **Bus subscribe 模型改动**：sing-router 现 `internal/log/bus.go` 的具体内部实现（每个 subscriber 一条 goroutine + 自己的 channel + 满了丢最旧）必须如实搬到 clef，否则可能在高负载下暴露行为差异。spec 落地阶段先逐行对比现 `bus.go`。
- **shutdown 期间诊断 fallback 路径**：sink 在 bus 已关后产生诊断事件不能再走 bus，需要 fallback 到 stderr。实现时要在 sink 内部明确这条分支并加测试。

# 设计：本地生成 ui/zashboard.json（source-ip-label-list）

- 日期：2026-06-25
- 状态：已批准，待实施
- 范围：在 sing-router daemon 内本地生成 zashboard 的 `source-ip-label-list` 导入文件，落到 `ui_dir`，运行周期与其它在线资源同步一致。

## 背景与动机

参考脚本 `~/Workspace.localized/env/package/yyscripts/zashboard-iplabels.py` 通过 SSH（Fabric）从 ASUS 路由器读取本地数据，生成 zashboard web 可导入的 `config/source-ip-label-list`（IP → 带 emoji 的设备标签）。

sing-router daemon 本身就运行在路由器上，可以**本地直读**同样的数据源，无需 SSH/Fabric。本设计把该能力内化进 daemon：

- 周期与在线资源更新一致（随 `StartSyncLoop` 每轮触发）。
- 同时提供手动触发的 CLI 子命令。
- 顺便移除目前空壳、无人消费的 `download_zashboard` 配置项。

数据源（与 Python 脚本完全对齐，4 源）：

| 源 | 采集方式（on-box） | 用途 |
|---|---|---|
| `nvram get custom_clientlist` | exec | MAC → 自定义设备名（带 emoji） |
| `/proc/net/arp` | 文件读 | MAC → IPv4（覆盖静态 IP 设备） |
| `/var/lib/misc/dnsmasq.leases` | 文件读 | MAC → IPv4（DHCP fallback） |
| `ip -6 neigh show` | exec | MAC → IPv6 全局单播地址（GUA） |

任一源失败 → 该源置空 + 记一条 warning，**不中断**生成（降级）。

## 输出格式（精确匹配 Python）

落盘文件 `<rundir>/<ui_dir>/zashboard.json`，内容：

```json
{
  "config/source-ip-label-list": "<entries 的 compact JSON 字符串>"
}
```

- 外层对象 indent=2；`config/source-ip-label-list` 的值是 entries 数组序列化成的紧凑 JSON 字符串（`separators=(",",":")` 等价，即 `ensure_ascii=false` + 无空格分隔）。
- 每个 entry：`{"key": <IP>, "label": <name>, "id": <uuid5>}`。
- 排序：IPv4 按数值在前，IPv6 居中，其它最后（与 Python `sort_key` 一致）。

## 合并优先级

1. 路由器数据：`custom_clientlist` 的 MAC→name，映射到该 MAC 的 IPv4（leases 先、ARP 覆盖）+ 所有 IPv6 GUA。路由器数据**优先**。
2. 静态表（`[zashboard.static_labels]`）：只填路由器缺失的 key（`setdefault` 语义）。

## 组件设计

新包 `internal/zashboard/`：

### `labels.go` — 纯函数（无副作用、易测）

- `Entry struct { Key, Label, ID string }`
- `RawData struct { Clients, ARP, Leases, Neigh string }`
- `parseCustomClientlist(text) map[string]string` — 条目以 `<` 分隔、字段以 `>` 分隔，`[0]`=名 `[1]`=MAC（归一化大写）。
- `parseARP(text) map[string]string` — 列 `IP HWtype Flags HWaddress Mask Device`；跳过表头、`flags==0x0`、全零 MAC；`setdefault` 语义。
- `parseLeases(text) map[string]string` — 列 `expiry MAC IP hostname clientid`；`setdefault`。
- `parseIPv6Neigh(text) map[string][]string` — 列含 `lladdr`，仅取全局单播地址（跳过 fe80 链路本地、ULA 等）；去重。
- `sortKey(key) (int, *big.Int/...)` — IPv4=(0,值)、IPv6=(1,值)、其它=(2,字符串)。
- `BuildEntries(raw RawData, static map[string]string) []Entry` — 串起上述解析 + 合并优先级 + 排序，对每个 key 生成 `id`。

### `uuid` 派生（用 `github.com/google/uuid`）

- `NAMESPACE = uuid.MustParse("6f1d4c2a-9b3e-5a7c-8d6f-2e4a1b0c9d8e")`（与 Python 常量逐字节一致）。
- `makeID(key) = uuid.NewSHA1(NAMESPACE, []byte(key)).String()` —— 等价 Python `uuid.uuid5(NAMESPACE, key)`。
- go.mod 新增 direct dependency `github.com/google/uuid`。

### `collect.go` — 本地采集（impure 边界）

- `Collect(ctx) (RawData, []string)`：
  - exec `nvram get custom_clientlist`、`ip -6 neigh show`（`os/exec`，带 ctx）。
  - 读 `/proc/net/arp`、`/var/lib/misc/dnsmasq.leases`。
  - 任一失败 → 对应字段置空，append 一条 warning 字符串；不返回 error。
- 跨平台：非 Linux（dev/mac）上命令/文件缺失即降级为空 + warning，不 panic。测试不覆盖 Collect（覆盖纯解析函数）。

### `generate.go` — 生成 + 原子写

- `Result struct { Skipped bool; Changed bool; Count int; Warnings []string }`
- `Generate(ctx, uiDir string, static map[string]string) (Result, error)`：
  1. `uiDir` 不存在（`os.Stat` 失败）→ `Result{Skipped:true}`，nil error（"ui_dir 存在即生成"）。
  2. `Collect` → `BuildEntries` → marshal payload。
  3. 计算新内容 sha256，与现有 `zashboard.json` 内容 sha256 比对：相同 → `Changed:false`，跳过写盘。
  4. 不同 → 原子写（`.tmp` + `os.Rename`，权限 0o644）→ `Changed:true, Count:len(entries)`。

## 配置

新增 `[zashboard]` 段（`internal/config/daemon_toml.go`）：

```go
type ZashboardConfig struct {
    StaticLabels map[string]string `toml:"static_labels"`
}
```

- 挂在 `DaemonConfig` 上；段缺失 → `StaticLabels` 为 nil/空，仅用路由器数据。
- **无独立 enable flag**——`ui_dir` 存在即生成。
- `daemon.toml.tmpl` 内置默认静态表（与 Python `STATIC_LABELS` 一致），新装可见可编辑：

```toml
[zashboard.static_labels]
"127.0.0.1"         = "💻本机"
"11.0.0.1"          = "🌐TUN"
"fdfe:dcba:9876::1" = "🌐TUN"
"192.168.50.1"      = "🛜主路由器"
"192.168.50.2"      = "🛜副路由器"
"192.168.50.3"      = "🛜小路由器"
```

uiDir 取 `cfg.Runtime.UIDir`（相对 rundir，默认 `ui`）。

## 集成

### sync loop（`internal/daemon/sync_loop.go`）

- `SyncLoopConfig` 新增字段：`ZashboardUIDir string`、`ZashboardStaticLabels map[string]string`。
- `runSyncOnce` 在资源 apply 之后**无条件**调用一次 `zashboard.Generate`（独立轻量步骤，**不进 Applier、不触发 restart**）。
- 结果走日志：`Skipped` → debug；`Changed` → info（含 count）；error → warn；warnings 逐条 debug/warn。
- `wireup_daemon.go` 用 `cfg.Runtime.UIDir`（拼成绝对路径）+ `cfg.Zashboard.StaticLabels` 填充 `SyncLoopConfig`。

### CLI（`internal/cli/update.go`）

- `update` 新增 target `zashboard`：本地生成，**跳过 gitee token 检查**（与 sing-box/zoo/all 的 token 前置不同）。
- `update all` 末尾也跑一次生成。
- 复用 `zashboard.Generate`，输出条目数/skipped/warnings 到 stdout。

## 移除空壳 `download_zashboard`

删除以下全部引用：

- `internal/config/daemon_toml.go`：`Install.DownloadZashboard` 字段 + 默认值。
- `internal/install/seed.go`：`TemplateVars.DownloadZashboard` 字段。
- `internal/cli/install.go`：传 `DownloadZashboard` 的那行。
- `assets/daemon.toml.tmpl`：`download_zashboard = {{ .DownloadZashboard }}` 行。
- 测试：`internal/config/daemon_toml_test.go`、`internal/install/seed_test.go` 中的 `download_zashboard` / `DownloadZashboard` 断言。

## 错误处理

- 采集源缺失：降级（空源 + warning），继续生成；不影响其它 sync 资源。
- ui_dir 缺失：跳过，不报错（正常态——zashboard 前端尚未被 clash_api 下载时）。
- 生成失败（marshal/写盘）：返回 error，sync loop 记 warn，不影响主流程与其它资源；CLI 返回非零退出。
- goroutine 安全：生成在 `runSyncOnce` 内，已被 sync loop 的 `defer recover()` 兜底。

## 测试

- `internal/zashboard/labels_test.go`：
  - 各 parse 函数样例（移植 Python：表头跳过、flags 过滤、`<>` 分隔、IPv6 GUA-only + 去重）。
  - `BuildEntries` 合并优先级（路由器优先、静态表补缺）。
  - uuid5 已知向量：固定 key → 期望 UUID（与 Python `uuid.uuid5` 输出比对的固定值）。
  - 排序顺序（IPv4 < IPv6 < 其它）。
- `internal/zashboard/generate_test.go`：
  - ui_dir 缺失 → `Skipped:true`。
  - ui_dir 存在 → 写出文件，格式（外层 indent=2 + 内层 compact 字符串）正确。
  - 内容 sha256 不变 → `Changed:false`，不重写。
- 更新现有测试去掉 `download_zashboard` 断言。
- `go test ./...` + `go vet ./...` 全绿。

## 非目标（YAGNI）

- 不下载/管理 zashboard 前端静态资源本身（仍由 sing-box clash_api `external_ui` 负责）。
- 不做静态表的动态网关 IP 推导（沿用配置里写定的值）。
- 不为生成提供独立的 daemon HTTP 端点（CLI + sync loop 已覆盖）。

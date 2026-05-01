# sing-router Module A — 服务管理核心 设计文档

- 日期：2026-05-02
- 范围：Module A —— 服务管理核心 + 配置布局
- 目标设备：Asus RT-BE88U（aarch64 / Merlin 固件 / Entware）
- 状态：待实施

---

## 0. 范围与定位

`sing-router` 整体目标是为华硕路由器（梅林固件 + Entware）提供透明路由管理能力。需求被分解为多个独立的子模块；本设计文档只覆盖 **Module A：服务管理核心**。

后续模块各自独立 spec/plan/实施：

| 模块 | 内容 | 状态 |
|---|---|---|
| **A** | sing-box 生命周期、CLI、路由脚本编排、配置布局、HTTP 控制平面、结构化日志骨架、install/uninstall | 本文 |
| B | 定时更新管线（cn.txt、zoo.json、sing-box 二进制；统一"下载→校验→替换→失败回滚"） | 后续 |
| C | 日志投递到 Seq（in-memory CLEF batcher） | 后续 |
| D | Web 管理界面（嵌入 zashboard 或独立） | 后续 |
| E | Bark 通知（订阅守护进程内部事件总线） | 后续 |
| F | 自动备份 | 后续 |
| G | 自动化测试 + 文档 | 后续 |

A 模块的设计原则之一是**为 B/C/D/E/F 留好接口**：daemon.toml 区段、HTTP 端点空间、内存事件总线、状态机事件 ID 等已经在 A 阶段稳定下来，避免后续返工。

---

## 1. 架构总览

### 1.1 一句话

`sing-router` 是一个用 Go 编写的**单文件可执行**，作为常驻 supervisor 托管 sing-box 子进程；通过本机 HTTP API 暴露控制平面（CLI / 未来 Web UI）；通过 `go:embed` 内嵌 startup/teardown shell、init.d 脚本、默认配置等所有"散件"；以"单文件 + `sing-router install`"的方式部署到 Asus RT-BE88U（Merlin + Entware）。

### 1.2 进程拓扑

```
┌─────────────────────────────────────────────────────────────────────┐
│                  /opt/sbin/sing-router (single binary)              │
│                                                                     │
│  Subcommand dispatcher (cobra) ──┬──> CLI mode (HTTP client)        │
│                                  │      ↳ status / start / stop /   │
│                                  │        restart / check / logs /  │
│                                  │        reapply-rules / script    │
│                                  │                                  │
│                                  ├──> install / uninstall / doctor  │
│                                  │      ↳ 写 init.d、注入 jffs 钩子 │
│                                  │        落盘默认 config.d         │
│                                  │                                  │
│                                  └──> daemon (init.d 调用)          │
│                                         ↓                           │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                Supervisor (long-running)                    │    │
│  │                                                             │    │
│  │  HTTP server (loopback:9998)   ────────► CLI / future UI    │    │
│  │      │                                                      │    │
│  │      ↓                                                      │    │
│  │  StateMachine: booting/running/reloading/degraded/          │    │
│  │                stopping/stopped/fatal                       │    │
│  │      │                                                      │    │
│  │      ├─► ConfigPreprocessor (zoo.json filter/dedup/rewrite) │    │
│  │      ├─► sing-box subprocess (fork + stderr pipe)           │    │
│  │      │       ↓                                              │    │
│  │      │   sing2seq parser (vendored) → CLEF events           │    │
│  │      ├─► RoutingScript runner (exec embedded startup.sh)    │    │
│  │      └─► Logger (CLEF JSON Lines → log/sing-router.log)     │    │
│  │                                                             │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘

External processes (in $RUNDIR):
  $RUNDIR/bin/sing-box              ← child process, transparent proxy engine
                ↳ uses config.d/    ← merged via -C
                ↳ exposes clash_api on :9999 (zashboard ui served from $RUNDIR/ui/)

Shell layer (Go exec):
  embedded startup.sh + teardown.sh ← run with env vars from Go (DNS_PORT etc.)
                                      iptables / ipset / ip rule 安装与卸载
```

### 1.3 设计原则

1. **Single source of truth**：路由参数源于 sing-box 配置；A 阶段以 Go 内置常量为权威，shell 永远从 env 取，**不**在 shell 脚本里硬编码任何端口/mark/CIDR。
2. **嵌入而非分发**：所有 shell 脚本、init 脚本、默认配置都通过 `go:embed` 进入二进制；安装时按需落盘，运行时所有 supervisor 流程从内存。
3. **职责拆分**：cli/daemon/config/log/shell/install 各自独立可测试；daemon 是集成点。
4. **向后兼容设计**：daemon.toml 的 section 留好扩展位（`[update]/[seq]/[bark]/[backup]`）；HTTP 端点保留 `/api/v1/zoo/reload` 等给 B/C/D/E/F；事件总线接口稳定。

### 1.4 Go 包结构

```
github.com/moonfruit/sing-router/
├── cmd/sing-router/         # main.go：cobra 注册子命令
├── internal/
│   ├── cli/                 # CLI 子命令实现，HTTP 客户端调用 daemon
│   │   ├── status.go
│   │   ├── start_stop.go
│   │   ├── install.go
│   │   ├── uninstall.go
│   │   ├── doctor.go
│   │   ├── script.go        # 直接打印内嵌资源
│   │   └── logs.go          # SSE 客户端 + pretty printer
│   ├── daemon/              # supervisor 主循环
│   │   ├── daemon.go        # 入口，组装 supervisor + http server
│   │   ├── statemachine.go  # 状态枚举 + 转移
│   │   ├── supervisor.go    # fork sing-box / readiness / 崩溃恢复 / iptables 调度
│   │   ├── ready.go         # 拨通 inbounds + clash api 健康检查
│   │   └── api.go           # HTTP handlers
│   ├── config/              # 配置加载与预处理
│   │   ├── daemon_toml.go   # 解析 daemon.toml
│   │   ├── routing.go       # 路由参数（端口/mark/cidr）+ env 注入
│   │   ├── singbox.go       # 调 sing-box format/check 的 wrapper
│   │   └── zoo.go           # ★ 关键：zoo.json filter/dedup/rewrite
│   ├── log/                 # 日志：CLEF emitter + 轮转
│   │   ├── clef.go          # 与 sing2seq 一致的 orderedEvent
│   │   ├── parser.go        # 内嵌的 sing2seq parser（vendored）
│   │   ├── writer.go        # JSON lines + 轮转
│   │   ├── bus.go           # 内存事件总线（B/C/E/F 订阅入口）
│   │   └── pretty.go        # logs -f 的反向格式化
│   ├── shell/               # embed.FS + bash exec wrapper
│   │   ├── embed.go         # //go:embed assets/*
│   │   └── runner.go        # 用 env 跑 bash -c 脚本，捕获 stderr 转 CLEF
│   ├── install/             # install/uninstall 的具体动作
│   │   ├── layout.go        # 创建 $RUNDIR 子目录
│   │   ├── seed.go          # 落盘默认 config.d/*、daemon.toml
│   │   ├── initd.go         # 写 /opt/etc/init.d/S99sing-router
│   │   ├── jffs_hooks.go    # 幂等 BEGIN/END 块注入与回收
│   │   └── download.go      # mirror_prefix 支持的下载器
│   └── state/               # 持久化（state.json、last-good 等）
│       └── state.go
├── assets/                  # embed.FS 源
│   ├── shell/
│   │   ├── startup.sh
│   │   └── teardown.sh
│   ├── initd/
│   │   └── S99sing-router
│   ├── jffs/
│   │   ├── nat-start.snippet
│   │   └── services-start.snippet
│   └── config.d.default/    # 当前 repo 的 config/*.json 作为默认种子
│       ├── clash.json
│       ├── dns.json
│       ├── inbounds.json
│       ├── log.json
│       ├── cache.json
│       ├── certificate.json
│       ├── http.json
│       └── outbounds.json
├── testdata/
│   └── fake-sing-box/       # 集成测试用的桩进程
└── docs/superpowers/specs/  # 设计文档
```

---

## 2. 磁盘布局

### 2.1 固定位置（系统级）

| 路径 | 写入者 | 用途 |
|---|---|---|
| `/opt/sbin/sing-router` | 用户 scp（一次性） | 单文件 Go 二进制 |
| `/opt/etc/init.d/S99sing-router` | `sing-router install` | Entware 启动脚本，调用 `sing-router daemon -D /opt/home/sing-router` |
| `/jffs/scripts/nat-start` | `sing-router install` | 含 BEGIN/END 块；wan/防火墙重启后调用 `sing-router reapply-rules` |
| `/jffs/scripts/services-start` | `sing-router install` | 含 BEGIN/END 块；保 init.d 链路存在的兜底（可选） |

### 2.2 运行时根 `$RUNDIR`

默认 `/opt/home/sing-router`，可由 `--rundir` / `-D` 覆盖；CLI 与 daemon 都 `chdir($RUNDIR)`。

```
$RUNDIR/
  daemon.toml                       # 守护进程自身设置（含 [install] 默认）
  config.d/                         # sing-box -C 目标目录；深度合并入口
    clash.json                      # 静态：clash_api + external_ui
    dns.json                        # 静态：dns 配置
    inbounds.json                   # 静态：dns-in / mixed-in / redirect-in / global-in / tun-in
    log.json                        # 静态：log level（必须保持 timestamp=true）
    cache.json                      # 静态
    certificate.json                # 静态
    http.json                       # 静态
    outbounds.json                  # 静态：DIRECT / REJECT + 基础 route
    zoo.json                        # 动态：守护进程预处理后原子写入；初始可不存在
  ui/                               # zashboard 静态资源；首次启动由 sing-box 自身依据
                                    # clash.json 的 external_ui_download_url 自动下载到此。
                                    # install 不创建此目录。
  bin/
    sing-box                        # 由 install --download-sing-box 或 B 阶段更新写入
  var/
    cn.txt                          # ipset cn 集合源；可不存在（startup.sh 已兼容）
    zoo.raw.json                    # 最近一次接收到的 zoo 原文（未预处理）
    zoo.last-good.json              # 最近一次成功上线的预处理产物（B 阶段回滚用）
    sing-box.last-good              # 最近一次成功上线的 sing-box 二进制副本（B 阶段回滚用）
    state.json                      # 守护进程持久化运行时状态（启动次数、最近更新时间等）
  run/
    sing-router.pid                 # 守护进程 pid（启动写入，退出删除）
    sing-box.pid                    # sing-box 子进程 pid（守护进程 fork 后写入）
  log/
    sing-router.log                 # CLEF JSON Lines；含 daemon 自身 + sing-box stderr 解析事件
    sing-router.log.1.gz            # 内置轮转的旧文件
    ...
```

### 2.3 写权限边界

| 路径 | 写入者 |
|---|---|
| `/opt/sbin/sing-router` | 用户 scp（一次性） |
| `/opt/etc/init.d/S99sing-router` | `sing-router install` |
| `/jffs/scripts/nat-start[+services-start]` | `sing-router install`（仅 BEGIN/END 块内） |
| `$RUNDIR/daemon.toml` | `sing-router install`（首次创建；之后用户编辑） |
| `$RUNDIR/config.d/{clash,dns,inbounds,...}.json` | `sing-router install`（首次创建；之后用户编辑） |
| `$RUNDIR/config.d/zoo.json` | `sing-router daemon`（每次预处理后原子替换） |
| `$RUNDIR/ui/` | sing-box 自己（首次启动时下载） |
| `$RUNDIR/bin/sing-box` | `sing-router install --download-sing-box` 或 B 阶段更新 |
| `$RUNDIR/var/*` | `sing-router daemon` |
| `$RUNDIR/run/*` | `sing-router daemon` |
| `$RUNDIR/log/*` | `sing-router daemon` |

### 2.4 卸载策略

```
sing-router uninstall                  # 拆 init.d + 拆 jffs 钩子；保留 $RUNDIR
sing-router uninstall --purge          # + 删 $RUNDIR；不删 /opt/sbin/sing-router 二进制（用户自决）
```

---

## 3. 配置组装管线

### 3.1 总体思路

sing-box 通过 `-C config.d/` 自己完成最终深度合并；守护进程**只在 zoo.json 这个高变化输入上**做预处理。其它 fragment（dns/inbounds/log/...）一旦由 install 落盘，由用户手动维护。

```
┌─ 输入 ─────────────────────────────────────────────────────────────┐
│  $RUNDIR/var/zoo.raw.json     ← 用户手动放置 / B 阶段下载落地         │
│  $RUNDIR/config.d/{clash,dns,inbounds,...}.json (静态 fragments)    │
└────────────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─ Go 预处理（每次 boot/restart 都跑一次） ──────────────────────────────┐
│  1) 解析 zoo.raw.json（jsonc 兼容；utf-8）                             │
│  2) 仅保留：outbounds, route.rules, route.rule_set, route.final         │
│     其余字段一律丢弃，事件日志记录被丢字段名                                │
│  3) 收集所有静态 fragment 的 route.rule_set，建 url→tag 映射               │
│  4) 对 zoo 的 route.rule_set 逐项按 url 与映射比对：                      │
│       a) url 命中 → 该项整体丢弃；记 (zoo_tag → builtin_tag) 改写映射     │
│       b) url 未命中 → 保留该项；其 tag 不动                               │
│     注：仅做 zoo↔builtin 跨边界去重；zoo 内部多个 rule_set 共享同 url     │
│         不做归并（属用户源头数据问题，sing-box 加载时若 tag 撞名会报错）  │
│  5) 检查 zoo.outbounds[*].tag 与静态 outbounds.tag 是否撞名：              │
│       撞名 → 整个 zoo 视为 invalid，回滚到 zoo.last-good.json，           │
│             记日志 + 在 status.config.zoo.outbound_collision_rejected      │
│             字段累加                                                     │
│  6) 用步骤 4a 的改写映射，扫描 zoo.route.rules[*].rule_set 的字符串引用      │
│     并替换；只扫这一个字段（不递归 logical 复合规则；当前不支持）           │
│  7) 输出最终 zoo（仅 outbounds + 净化后的 route.rules + 去重后的            │
│     route.rule_set + route.final）                                       │
│  8) 原子写：tmp 文件 + rename 到 $RUNDIR/config.d/zoo.json                │
│  9) 同步更新 $RUNDIR/var/zoo.last-good.json                              │
│  10) 跑 `sing-box check -C config.d/`；若失败 → 回滚 + fatal 状态         │
└────────────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─ sing-box 启动 ─────────────────────────────────────────────────────┐
│  $RUNDIR/bin/sing-box run -D $RUNDIR -C config.d/                   │
│  sing-box 内部按文件名字典序深度合并所有 *.json                            │
└────────────────────────────────────────────────────────────────────┘
```

### 3.2 zoo 预处理失败的兜底

| 失败类型 | 行为 |
|---|---|
| `zoo.raw.json` 不存在 | 跳过预处理；`config.d/zoo.json` 不存在或保留上次版本均可；sing-box 仍能启动（但没有用户 outbounds，全走 DIRECT） |
| JSON 解析失败 | 回滚到 `zoo.last-good.json`；记 ERROR；`status.config.zoo.last_loaded_at` 不更新 |
| outbound tag 撞名 | 同上 |
| `sing-box check` 失败 | 同上 |
| `zoo.last-good.json` 也不存在 | 跳过 zoo（视作"无 zoo"），sing-box 仍能起；`status.daemon.state = "degraded"` |

### 3.3 触发预处理的入口

| 入口 | 触发点 |
|---|---|
| daemon 启动 | 每次 `sing-router daemon` 启动时跑一次 |
| `restart` API/CLI | 走 reload 流程，跑一次 |
| `start` API/CLI（从 stopped 恢复） | 走 boot 流程，跑一次 |
| `check` API/CLI | 跑一次但仅 dry-run，**不**写 `config.d/`，结果以 JSON 返回 |
| （B 阶段）`POST /zoo/reload` | 用户上传 / 定时拉取后跑一次 |

### 3.4 路由参数注入

A 阶段，路由参数（端口/mark/cidr）以 Go 内常量为权威，通过环境变量注入 shell：

```go
// internal/config/routing.go
type Routing struct {
    DnsPort      int    // 1053
    RedirectPort int    // 7892
    RouteMark    string // "0x7892"
    BypassMark   string // "0x7890"
    Tun          string // "utun"
    FakeIP       string // "28.0.0.0/8"   ★ 必须与 dns.json inet4_range 一致
    LAN          string // "192.168.50.0/24"
    RouteTable   int    // 111
    ProxyPorts   string // "22,80,443,8080,8443"
}

func DefaultRouting() Routing { ... }
func LoadRouting(toml *DaemonConfig) Routing { /* 用 toml.[router] 覆盖默认 */ }
```

启动 startup.sh 前 daemon 把这些值导出为环境变量；shell 脚本仅 `: "${DNS_PORT:?}"` 等读取，不再硬编码。

> **注意**：当前 repo 的 `bin/startup.sh` 中 `FAKEIP=28.0.0.0/8` 与 `config/dns.json` 的 `inet4_range: "22.0.0.0/8"` 不一致，是已有 bug；A 阶段重写 startup.sh 时统一以 Go 默认 `28.0.0.0/8` 为准，并要求 `dns.json` 的 `inet4_range` 同步修改。

---

## 4. Supervisor 生命周期状态机

### 4.1 状态枚举

| 状态 | 含义 |
|---|---|
| `booting` | 守护进程启动到 sing-box ready 之间的窗口 |
| `running` | sing-box ready，iptables 已装；正常服务态 |
| `reloading` | 用户主动 restart 中 |
| `degraded` | sing-box 崩溃后退避中（iptables 视退避档位决定是否拆） |
| `stopping` | 收到 SIGTERM 或 stop API；过渡态 |
| `stopped` | sing-box 已停、iptables 已拆，守护进程仍服务 status / start API |
| `fatal` | 启动期失败（pre-ready crash） / 配置无效；**不**自动恢复 |

### 4.2 合法转移

```
                                  ┌──────────────► fatal
                                  │ ready failed (pre-ready)
                                  │
init.d start ──► booting ─────────┴─ ready ok ──► running
                                                    │  ▲
                            ┌───── /restart ────────┤  │
                            ▼                       │  │
                        reloading ── ready ok ──────┘  │
                            │                          │
                            └──── ready failed ──► fatal
                                                       │
                                              crash    │
                                                ▼      │
                                            degraded ──┘ ready 恢复

running ──/stop──► stopping ──► stopped ──/start──► booting

任意态 ──/shutdown 或 SIGTERM──► stopping ──► (process exit)
```

### 4.3 Boot 序列（详细）

```
1. cobra dispatch → daemon 子命令
2. parse flags + 加载 daemon.toml；解析 routing 参数
3. chdir($RUNDIR)；写 run/sing-router.pid
4. 初始化 logger（CLEF writer + 轮转 + event bus）
5. 启动 HTTP listener (loopback:9998)
   ★ HTTP 先开，CLI 立刻能 status 看到 booting 态
6. zoo 预处理（见 §3）
7. 验证 config.d/：sing-box check -C config.d/
   失败 → state=fatal；保持 HTTP 存活让用户 status 排查；不 exec sing-box
8. fork sing-box：
   exec.Command("$RUNDIR/bin/sing-box", "run", "-D", "$RUNDIR", "-C", "config.d/")
   stderr 通过 io.Pipe → sing2seq parser → CLEF writer
   写 run/sing-box.pid
9. ready 检测（并发执行，全部满足才返回 ready）：
   - dial 127.0.0.1:1053 (dns-in) ok
   - dial 127.0.0.1:7892 (redirect-in) ok
   - dial 127.0.0.1:7890 (mixed-in) ok
   - dial 127.0.0.1:7898 (global-in) ok
   - GET http://127.0.0.1:9999/version → 200
   总超时 5s；轮询间隔 200ms
   超时 → state=fatal（视为 pre-ready 失败）
10. exec embedded startup.sh（环境变量带路由参数）
    成功 → 标记 iptables_installed=true；state=running
    失败 → 标记 fatal；不停 sing-box（让用户能看 clash dashboard 排查）
```

### 4.4 Reload（用户主动 restart）

```
state: running → reloading

1. 不拆 iptables（用户主动绝不拆）
2. SIGTERM sing-box → 等待至多 stop_grace_seconds=5s → 必要时 SIGKILL
3. cmd.Wait → 删 sing-box.pid
4. 重做 boot 第 6-10 步
5. state → running（成功）或 fatal（失败）
```

### 4.5 Crash 恢复（被动）

退避序列（max 600s 封顶）：

```
1s → 2s → 4s → 8s → 16s → 32s → 64s → 128s → 256s → 512s → 600s（封顶；之后每次都 600s）
```

iptables 决策：

- 当前 backoff < 10000ms → 保持 iptables 不动
- 当前 backoff ≥ 10000ms → 调 teardown.sh 拆掉；标记 `iptables_installed=false`

序列：

```
state: running → degraded

1. cmd.Wait 返回非 0
2. 计算下一档 backoff
3. 决策 iptables（按上面阈值）
4. sleep(backoff)
5. 重做 boot 第 6-10 步：
   - ready 成功：
       若 iptables_installed=false → 重新跑 startup.sh
       若 iptables_installed=true  → 跳过 startup.sh
       state → running；backoff 重置到第 0 档
   - ready 失败：
       backoff 升档；继续退避；不直接 fatal（区别于 pre-ready 首次启动）
```

退避总时长不设上限（容忍长时间不可恢复故障，状态保持可见，等待人工介入或 E 阶段 Bark 通知）。

### 4.6 Stop（用户主动暂停代理）

```
state: running → stopping → stopped

触发：POST /api/v1/stop 或 CLI sing-router stop

1. 拆 iptables：exec teardown.sh；标记 iptables_installed=false
2. SIGTERM sing-box → grace 5s → SIGKILL
3. cmd.Wait → 删 sing-box.pid
4. state → stopped；HTTP 仍服务
```

### 4.7 Start（从 stopped 恢复）

```
state: stopped → booting → running

触发：POST /api/v1/start 或 CLI sing-router start

执行 §4.3 第 6-10 步。
```

### 4.8 Shutdown（守护进程整体退出）

```
state: any → stopping → process exit

触发：SIGTERM (init.d stop) 或 POST /api/v1/shutdown

1. 关闭 HTTP listener（不再接受新连接，正在处理的允许完成）
2. 若 iptables_installed=true：拆 iptables
3. SIGTERM sing-box → grace 5s → SIGKILL
4. cmd.Wait → 删 sing-box.pid
5. flush log writer（确保所有 CLEF 事件落盘）
6. 删 sing-router.pid
7. 进程退出，exit code 0
```

### 4.9 `reapply-rules`（nat-start 钩子触发）

```
触发：jffs/nat-start 调 sing-router reapply-rules（即 POST /api/v1/reapply-rules）

行为：
  - 仅当 state=running 时有意义
  - 不动 sing-box；仅 best-effort 跑 teardown.sh → startup.sh
  - iptables_installed 始终标记为 true
  - 任何 state ≠ running 时返回 409 Conflict + 当前 state
```

---

## 5. HTTP API + CLI

### 5.1 控制平面

- 监听：`127.0.0.1:9998`（默认；可 daemon.toml `[http].listen` 改）
- 鉴权：A 阶段无；D 阶段引入 token
- 单一传输：HTTP（无 unix socket fallback）

### 5.2 端点（A 阶段）

| 端点 | 用途 |
|---|---|
| `GET /api/v1/status` | 全量状态快照 |
| `GET /api/v1/status/stream` | SSE 推流，同 schema |
| `POST /api/v1/start` | 从 stopped 起 sing-box + 装 iptables |
| `POST /api/v1/stop` | 拆 iptables + 停 sing-box；守护进程留守 |
| `POST /api/v1/restart` | 走 reload 状态机（不拆 iptables） |
| `POST /api/v1/check` | 仅 dry-run 校验当前 config.d/，不写盘 |
| `POST /api/v1/reapply-rules` | 仅重灌 iptables/ipset；nat-start 钩子专用 |
| `GET /api/v1/logs?...` | 历史尾部 + 可选 SSE follow |
| `GET /api/v1/script/{name}` | 把嵌入资源以 text/plain 输出 |
| `POST /api/v1/shutdown` | 关闭整个守护进程（init.d stop 用） |

### 5.3 未来模块预留端点

```
POST /api/v1/zoo/reload              # B：用户给定 / 重新拉取 zoo.json
POST /api/v1/cn/reload               # B：重新加载 cn.txt 进 ipset
POST /api/v1/sing-box/upgrade        # B：下载 + 兼容性检查 + 替换 + 回滚
GET  /api/v1/metrics                 # C
POST /api/v1/notify/test             # E
POST /api/v1/backup/snapshot         # F
```

### 5.4 `/api/v1/status` 返回 schema

```json
{
  "daemon": {
    "version": "0.1.0+abcdef",
    "pid": 1234,
    "uptime_seconds": 3725,
    "rundir": "/opt/home/sing-router",
    "state": "running",
    "last_error": null
  },
  "sing_box": {
    "pid": 1235,
    "binary": "/opt/home/sing-router/bin/sing-box",
    "version": "1.13.5",
    "ready": true,
    "uptime_seconds": 3720,
    "restart_count": 0,
    "next_backoff_seconds": null,           // 仅 state=degraded 时为整数，其它态为 null
    "listening": {
      "dns_in":      "tcp://[::]:1053 ok",
      "redirect_in": "tcp://[::]:7892 ok",
      "mixed_in":    "tcp://[::]:7890 ok",
      "global_in":   "tcp://[::]:7898 ok",
      "tun_in":      "utun ok",
      "clash_api":   "http://[::]:9999/version 200"
    }
  },
  "rules": {
    "iptables_installed": true,
    "ipset_cn_size": 8732,
    "last_applied_at": "2026-05-02T12:34:56+08:00"
  },
  "config": {
    "config_dir": "/opt/home/sing-router/config.d",
    "fragments": ["clash.json", "dns.json", "inbounds.json", "log.json", "outbounds.json", "zoo.json", "..."],
    "zoo": {
      "raw_sha256": "ab12...",
      "rendered_sha256": "cd34...",
      "last_loaded_at": "2026-05-02T12:34:50+08:00",
      "outbound_count": 42,
      "rule_set_count": 8,
      "rule_set_dedup_dropped": 1,
      "outbound_collision_rejected": 0
    },
    "cn_txt": {
      "sha256": "ef56...",
      "lines": 8732
    }
  }
}
```

### 5.5 错误返回约定

```json
{
  "error": {
    "code": "config.zoo.outbound_collision",
    "message": "human-readable",
    "detail": { "...": "任意结构化上下文" }
  }
}
```

错误码命名（domain.subdomain.error_kind）：

```
daemon.not_ready
daemon.state_conflict
config.zoo.parse_failed
config.zoo.outbound_collision
config.singbox_check_failed
shell.startup_failed
shell.teardown_failed
download.network                     # B 阶段
... 等
```

### 5.6 CLI ↔ 端点对应

```
sing-router status [--json] [--watch]   → GET  /status[/stream]
sing-router start                       → POST /start
sing-router stop                        → POST /stop
sing-router restart                     → POST /restart
sing-router check                       → POST /check
sing-router reapply-rules               → POST /reapply-rules
sing-router logs [-f] [--source ...]    → GET  /logs
sing-router script <name>               → 优先本地内嵌资源 → stdout
                                          --remote 时 GET /script/<name>
sing-router shutdown                    → POST /shutdown   （等价 init.d stop）

sing-router daemon [-D <rundir>]        → 不走 HTTP，直接进 supervisor 主循环
sing-router install [flags]             → 不走 HTTP（守护进程未跑），落盘 + 注入钩子
sing-router uninstall [--purge]         → 不走 HTTP
sing-router doctor                      → 不走 HTTP（只读检测）
sing-router version                     → 不走 HTTP（打印版本号）
```

### 5.7 CLI 在守护进程未跑时的行为

| 命令 | 行为 |
|---|---|
| `status` | 降级为本地探测：rundir / binary version / config.d 是否存在；exit 0 |
| `start / stop / restart / check / reapply-rules / shutdown` | 报错退出，提示 `init.d S99sing-router start` |
| `logs` | 历史走文件直接读，能拿到；`--follow` 报错（无 SSE 源） |
| `script` | 直接打印内嵌资源（不依赖守护进程） |
| `daemon / install / uninstall / doctor / version` | 与守护进程无关 |

`status` 不报错而是降级输出，是为了让 init.d 等脚本中 `sing-router status` 可用作幂等探测。

---

## 6. 日志管线

### 6.1 总览

```
┌──── Daemon 自身事件 ───────────────────────────────┐
│  log.Info(EventID, ...) 等 helper                  │
│       ↓                                            │
│  CLEF emitter（构建 orderedEvent）                  │
└────────────────────┬───────────────────────────────┘
                     │
                     ▼
              ┌──────────────┐         ┌───────────────────────┐
              │ Log multiplex│ ──────► │ JSON-Lines writer     │
              │              │         │  + size-based rotation│
              └──────▲───────┘         │  + gzip 压缩          │
                     │                 └───────────────────────┘
                     │                            │
                     │                            ▼
                     │              $RUNDIR/log/sing-router.log[.N.gz]
                     │
┌──── sing-box stderr ──────────────┐
│  io.Pipe → bufio.Scanner          │
│  parser.go (vendored sing2seq)    │
│  → orderedEvent                    │
└────────────────────────────────────┘

旁路（仅内存，下游模块用）：
              ┌──────────────┐
              │ Event bus    │ ──► (B/C/E/F 模块订阅)
              └──────────────┘
```

### 6.2 CLEF 字段约定

所有事件都至少有：

```
@t       ISO8601 with offset
@l       Verbose|Debug|Information|Warning|Error|Fatal
@mt      消息模板（含 {Field} 占位符）
Source   "daemon" | "sing-box"
```

`Source="sing-box"` 的事件由 vendored sing2seq parser 直接生成（与 sing2seq 现有的字段集一致：`Module/Type/Tag/ConnectionId/Duration/Detail/Domain/IP/...`）。

`Source="daemon"` 的事件由守护进程内部 helper 产出，约定：

| 字段 | 说明 |
|---|---|
| `EventID` | 事件类型字符串，全小写带点（用于 Seq 过滤） |
| `Module` | `supervisor` / `zoo` / `shell` / `http` / `state` / `install` |
| `Type` | 可选；细分子类型 |

EventID 命名空间（保持稳定，未来 B/C/E/F 订阅依赖于此）：

```
supervisor.boot.started
supervisor.boot.ready
supervisor.boot.failed
supervisor.crash
supervisor.recovered
supervisor.degraded.tearing_down
zoo.preprocess.started
zoo.preprocess.dropped_field
zoo.preprocess.rule_set_dedup
zoo.preprocess.outbound_collision_rejected
zoo.preprocess.completed
shell.startup.exec
shell.startup.completed
shell.startup.failed
shell.teardown.exec
shell.teardown.completed
shell.teardown.failed
http.request                   # debug 级
state.transition               # state 转移都打一条
install.* / uninstall.*
```

事件示例：

```json
{
  "@t": "2026-05-02T12:34:56.789+08:00",
  "@l": "Information",
  "@mt": "supervisor: sing-box ready in {ReadyDurationMs}ms",
  "Source": "daemon",
  "Module": "supervisor",
  "EventID": "supervisor.boot.ready",
  "ReadyDurationMs": 1218,
  "SingBoxPid": 4521,
  "SingBoxVersion": "1.13.5"
}
```

### 6.3 文件路径与轮转

```
$RUNDIR/log/sing-router.log              # 当前 active
$RUNDIR/log/sing-router.log.1.gz         # 上一份（gzip）
...
$RUNDIR/log/sing-router.log.5.gz         # 最旧（max_backups=5）
```

- 写入触发：每写完一行检查 size > `[log].max_size_mb*1024*1024` 则轮转
- 轮转动作：active 文件 `rename` 到 `.1`，旧 `.N` 顺延，超 `max_backups` 的最旧文件删除；`.1` 在新写入开始前异步 gzip
- 守护进程重启时自动清理多余旧文件
- 外部轮转（`[log].rotate = "external"`）：响应 SIGUSR1 关闭并重新 open active 文件，配合 `logrotate copytruncate`

### 6.4 sing-box stderr 处理

vendored sing2seq parser **硬依赖** sing-box 的 `log.timestamp = true`（无时间戳的行会降级为 `Parsed=false` 原始事件，丢失结构化字段）。当前 `assets/config.d.default/log.json` 必须保持 `timestamp=true`；用户改 `config.d/log.json` 关掉时间戳会导致日志解析降级，doctor 检查项里加一条提醒。

- 子进程 stderr 通过 `io.Pipe` 输出
- `bufio.Scanner` 显式 buffer 4 MiB（与 sing2seq 一致；某些日志行超 64 KiB 默认值）
- 每行经 `parseLine` 转 `orderedEvent`，标 `Source="sing-box"`
- 与 daemon 自身事件汇入同一份日志文件

### 6.5 事件总线（内存通路）

CLEF emitter 写文件之前先广播到内存 channel（lossy ring buffer，容量 4096）：

```go
type Event = orderedEvent

type Subscriber interface {
    Match(e *Event) bool
    Deliver(e *Event)
}

bus.Subscribe(s)   // B/C/E/F 模块用
```

A 阶段没有订阅方；接口稳定下来后 B/C/E/F 直接接入：

- B：`state.transition` 事件触发条件式定时任务
- C：订阅全部，投递 Seq
- E：订阅 `supervisor.crash` / `supervisor.recovered` / `*_failed` / `*_rejected`
- F：订阅 `state.transition` 进入 `running` 态做备份触发

### 6.6 日志查询（CLI / API）

`GET /api/v1/logs`：

| 参数 | 说明 |
|---|---|
| `source` | `daemon\|sing-box\|all`，默认 `all` |
| `n` | 尾部 N 行（默认 100；与 `--follow` 互斥时仅返回历史） |
| `follow` | `true` 进 SSE，持续推送新事件 |
| `level` | 至少 `Information`（默认）；`debug` 看更详细 |
| `event_id` | 模糊匹配 `EventID` 前缀 |

实现：历史从 `sing-router.log[.N.gz]` 顺序读 + 反向尾部；follow 从 event bus 订阅。

### 6.7 Pretty printer（CLI `logs` 子命令）

读 SSE / 文件，每行 JSON 反向格式化为：

```
2026-05-02 12:34:56.789 INFO  [daemon] supervisor: sing-box ready in 1.2s
2026-05-02 12:34:57.001 INFO  [sing-box] router[default]: outbound connection to www.example.com:443
```

- `[Source=daemon]` → `[daemon]`、`[Source=sing-box]` → `[sing-box]`
- 时区段：与守护进程当前时区一致时省略；不一致才显示
- 跨时区示例：`+0000 2026-05-02 04:34:56.789 INFO  [daemon] supervisor: ...`
- `--json` flag 透传原始 JSON 行；`-f / --follow` SSE 跟随

---

## 7. daemon.toml

```toml
# /opt/home/sing-router/daemon.toml
# Module A 范围内的全部设置；后续模块（B 定时更新 / C Seq / E Bark / F 备份）
# 会以 [update]、[seq]、[bark]、[backup] 等独立 section 增量加入。

[runtime]
# 启动 cwd 不一定就是 $RUNDIR；这里允许显式覆盖
# rundir = "/opt/home/sing-router"   # 默认 = -D 参数 / 启动 cwd
sing_box_binary = "bin/sing-box"     # 相对 rundir 或绝对路径
config_dir      = "config.d"         # sing-box -C 目标
ui_dir          = "ui"               # zashboard 路径（sing-box 自行下载到此）

[http]
listen          = "127.0.0.1:9998"   # CLI / Web UI 控制平面端口
# token         = ""                 # A 阶段不启用，D 阶段加

[log]
level           = "info"             # trace|debug|info|warn|error
file            = "log/sing-router.log"
rotate          = "internal"         # internal | external
max_size_mb     = 10
max_backups     = 5
disable_color   = false              # 影响 pretty printer，不影响 JSON 文件
include_stack   = false              # panic 是否打印 Go 栈

[supervisor]
# sing-box 启动后认定 ready 的判定，所有项注释默认值
# ready_check_dial_inbounds = true     # 拨通 inbounds.json 里所有非 tun 的 listen_port
# ready_check_clash_api     = true     # GET /version
# ready_check_timeout_ms    = 5000     # 总超时
# ready_check_interval_ms   = 200      # 轮询间隔

# 崩溃恢复
# crash_pre_ready_action      = "fatal"          # 装不起来 → fatal，不重启
# crash_post_ready_backoff_ms = [1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000, 512000, 600000]
# iptables_keep_when_backoff_lt_ms = 10000       # < 10s 保持 iptables；≥ 10s 拆

# 优雅停的等待时长
# stop_grace_seconds = 5

[zoo]
# zoo.json 预处理策略
extract_keys = ["outbounds", "route.rules", "route.rule_set", "route.final"]
rule_set_dedup_strategy   = "builtin_wins"     # builtin_wins | zoo_wins | reject
outbound_collision_action = "reject"           # reject | skip | rename

[download]
# c/d 的镜像支持
mirror_prefix     = ""                # 形如 "https://ghproxy.com/"，留空 = 直连
sing_box_url_template = "https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-linux-arm64.tar.gz"
sing_box_default_version = "latest"   # latest | 1.13.5 等具体版本
cn_list_url       = "https://raw.githubusercontent.com/17mon/china_ip_list/master/china_ip_list.txt"
http_timeout_seconds = 60
http_retries         = 3

[router]
# 路由参数；A 阶段先固化在 Go 内的常量也允许这里覆盖
# 留空 = 用 Go 默认（与 startup.sh 内嵌脚本里那些常量保持一致）
# dns_port      = 1053
# redirect_port = 7892
# route_mark    = "0x7892"
# bypass_mark   = "0x7890"
# tun           = "utun"
# fakeip        = "28.0.0.0/8"   # ★ 注意要与 dns.json inet4_range 一致
# lan           = "192.168.50.0/24"
# route_table   = 111
# proxy_ports   = "22,80,443,8080,8443"

[install]
# install 命令的默认行为；可被命令行 flag 覆盖
download_sing_box   = true
download_cn_list    = true
download_zashboard  = false           # A 阶段交给 sing-box clash_api 自行下载
auto_start          = false
```

---

## 8. install / uninstall

### 8.1 `sing-router install`

```
sing-router install [flags]
  -D, --rundir              运行时根目录                默认 /opt/home/sing-router
      --download-sing-box   下载 sing-box 到 bin/      默认按 daemon.toml [install] 决定
      --download-cn-list    下载 cn.txt 到 var/        默认同上
      --start               安装完成后调 init.d start  默认 false
      --mirror-prefix       下载镜像前缀               覆盖 daemon.toml [download].mirror_prefix
      --sing-box-version    指定 sing-box 版本         默认 latest
      --skip-jffs           不动 /jffs/scripts/        给只用 Entware 的用户
      --dry-run             仅打印将做的事，不实际执行
```

执行顺序（每一步都幂等）：

```
1. 决议 $RUNDIR：CLI -D > toml 不存在时为默认 /opt/home/sing-router
2. mkdir -p $RUNDIR/{config.d,bin,var,run,log}
   注意：不创建 ui/，留给 sing-box clash_api 首次下载时自行创建。
3. 落盘默认 daemon.toml（如不存在）
4. 落盘默认 config.d/*.json（每个文件单独判断：如不存在则写）
   - 静态来自 embed.FS assets/config.d.default/
5. 写 /opt/etc/init.d/S99sing-router（始终覆盖；从 embed.FS 取，cd $RUNDIR 后 exec sing-router daemon -D $RUNDIR）
   - 修正可执行权限 0755
6. 注入 /jffs/scripts/nat-start：
   - 文件不存在 → 创建 + chmod +x + 写入 BEGIN/END 块
   - 已存在 → 扫已有 BEGIN/END 块；有则替换块内容；无则追加
   - 块内容固定为 `command -v sing-router >/dev/null && sing-router reapply-rules >/dev/null 2>&1 &`
7. 注入 /jffs/scripts/services-start（兜底确保 S99sing-router 被运行；同上幂等模式）
   - 块内容：`/opt/etc/init.d/S99sing-router start &`
8. 按 daemon.toml [install] + flag 决定可选下载：
   - sing-box → $RUNDIR/bin/sing-box（解压 tar.gz、chmod +x、原子 rename）
   - cn.txt   → $RUNDIR/var/cn.txt（HEAD 校验 + 原子 rename）
   - zashboard 不下载
9. 若 --start 或 [install].auto_start：调 /opt/etc/init.d/S99sing-router start
10. 输出 next-steps 提示（编辑 daemon.toml / 上传 zoo.json / 跑 status 等）
```

### 8.2 BEGIN/END 块格式

```
# BEGIN sing-router (managed by `sing-router install`; do not edit)
command -v sing-router >/dev/null && sing-router reapply-rules >/dev/null 2>&1 &
# END sing-router
```

幂等扫描算法：

```
对目标文件做 line-by-line 扫描，匹配两种状态：
  - 行 == "# BEGIN sing-router..." → 进入 sing-router 块；记录块起止行号
  - 行 == "# END sing-router"      → 结束块
若已存在我们的块：替换块内容（保留块外的所有内容）
若不存在：在文件末尾追加完整块（行尾加 \n 兜底）
其他第三方块（# BEGIN xxx-other / # END xxx-other）一律不动
```

### 8.3 `sing-router uninstall`

```
sing-router uninstall [flags]
      --purge               删 $RUNDIR（含所有用户编辑的 config 与 cn.txt）
      --skip-jffs           不动 jffs 钩子（恢复时再装回）
      --keep-init           不删 /opt/etc/init.d/S99sing-router

执行：
1. /opt/etc/init.d/S99sing-router stop（如服务在跑）
2. 移除 /jffs/scripts/nat-start 与 services-start 中我们的 BEGIN/END 块
   - 整个块整段删掉（含 BEGIN/END 注释行）；保留其他用户内容
   - 如果文件因此变成全空白：保留文件存在；不删
3. 删 /opt/etc/init.d/S99sing-router（除非 --keep-init）
4. --purge 时 rm -rf $RUNDIR
5. 不删 /opt/sbin/sing-router 二进制（用户自决；提示打印路径）
```

### 8.4 `sing-router doctor`

只读体检，不修改任何东西。扫描项：

```
扫描项                                                   期望
-------------------------------------------------------------------------
/opt/sbin/sing-router 存在且可执行                        yes
二进制版本与 daemon.toml 兼容（无 minor 不匹配）            yes
$RUNDIR 存在 + 子目录齐全                                 yes
$RUNDIR/bin/sing-box 存在且可执行                          yes
$RUNDIR/config.d/*.json 至少含 inbounds + dns + log       yes
$RUNDIR/config.d/zoo.json 存在                             warn 即可
$RUNDIR/var/cn.txt 存在                                    warn 即可
/opt/etc/init.d/S99sing-router 存在 + 可执行              yes
/jffs/scripts/nat-start 含我们的 BEGIN/END 块              yes（如未跳过）
/jffs/scripts/services-start 含我们的 BEGIN/END 块         yes（如未跳过）
iptables 当前是否有 sing-box / sing-box-mark / sing-box-dns 链  与 status.rules 一致
ipset cn 当前 size 与 cn.txt 行数偏差                      < 1%
ports 9998 / 7892 / 7890 / 1053 当前监听情况               与 status.sing_box.listening 一致

输出：默认彩色表格；--json 机器可读
```

### 8.5 下载器

```
完整 URL = mirror_prefix + raw_url

  raw_url for sing-box  = sing_box_url_template 渲染 {version}
  raw_url for cn.txt    = cn_list_url

mirror_prefix 直接做字符串前缀拼接（如 ghproxy.com/ 风格代理）。
若 mirror_prefix 末尾不带 "/" 自动补一个。
带重试：`http_retries`，每次 timeout `http_timeout_seconds` 秒。
落盘走 tmp 文件 + 原子 rename；下载中断不影响现有文件。
```

---

## 9. 测试策略

| 层级 | 内容 | 工具 |
|---|---|---|
| **单元（Go）** | `internal/config/zoo.go` 全分支：白名单过滤 / url 去重 / 引用改写 / outbound 撞名 / route.final 引用映射 | 标准 `testing` + table-driven |
| | `internal/log/parser.go`（vendored sing2seq parser_test.go 一并搬来） | 同上 |
| | `internal/log/writer.go` 轮转触发与 gzip | 临时目录 + assert |
| | `internal/install/jffs_hooks.go` BEGIN/END 块幂等注入与回收（含已存在第三方块的混合场景） | 同上 |
| | `internal/install/initd.go` 模板渲染 | 同上 |
| **组件（Go）** | supervisor 状态机：使用 fake-sing-box 桩可执行 | 启动子进程驱动整个 boot/crash/recover 流程 |
| | HTTP API：httptest server + supervisor mock | net/http/httptest |
| **集成** | docker-compose 拉一个轻量 alpine arm64 容器（qemu-user-static），把 install/start/restart/status/stop/uninstall 全跑通 | docker + qemu |
| **端到端（手动）** | 真实 RT-BE88U 上 install + 流量测试（curl ipinfo.io、DNS 泄漏检查、cn 域名直连验证） | 文档清单形式 |

A 阶段不引入 CI；自动化测试在 G 阶段补 GitHub Actions。Go 测试本地 `go test ./...` 跑齐即合格。

### 9.1 fake-sing-box 桩进程

`testdata/fake-sing-box/main.go`（编译产物 `testdata/fake-sing-box`）：

```
flag                      作用
--listen-redirect <port>  开 redirect 端口的 TCP listener（dial ok 即返回）
--listen-dns <port>       同上
--listen-mixed <port>     同上
--listen-global <port>    同上
--listen-clash <port>     伪 clash API：GET /version 返回 200
--ready-delay <duration>  延迟 N 秒才开始 listen，模拟启动慢
--crash-after <duration>  N 秒后 panic 退出，模拟运行中崩溃
--pre-ready-fail          立即退出码 1，模拟 pre-ready crash
--emit-log                周期性吐 sing-box 风格 stderr
处理 SIGTERM 优雅退出；超时 SIGKILL
```

`internal/daemon/supervisor_test.go` 用此桩驱动 boot/crash/recover/iptables-skip 各路径。

### 9.2 iptables/ipset 隔离

`internal/shell/runner.go` 通过接口注入 exec 命令；测试里替换为记录调用的 mock，不真碰防火墙。

### 9.3 单元测试覆盖目标

- `internal/config/zoo.go`：100%
- `internal/install/jffs_hooks.go`：100%
- `internal/log/parser.go`：与 sing2seq 同等水准（>90%）
- 其它包：>70%

---

## 10. 现存 bug 修复清单

A 阶段实施过程中要顺手修复的现存问题：

1. `bin/startup.sh` 的 `FAKEIP=28.0.0.0/8` 与 `config/dns.json` 的 `inet4_range: "22.0.0.0/8"` 不一致。
   - A 阶段以 Go 默认 `28.0.0.0/8` 为准（与 startup.sh 现状一致）
   - 同步修改 `assets/config.d.default/dns.json` 的 `inet4_range` 为 `28.0.0.0/8`
   - 若用户已有 `$RUNDIR/config.d/dns.json`，install 不会覆盖；doctor 中可加一项检查"inet4_range 与 routing.fakeip 是否一致"作为提醒

2. `bin/startup.sh` 当前直接硬编码所有路由参数，重写为读 env：
   - 必须设置：`DNS_PORT REDIRECT_PORT ROUTE_MARK BYPASS_MARK TUN ROUTE_TABLE PROXY_PORTS FAKEIP LAN`
   - 可选：`CN_IP_CIDR`（默认 `var/cn.txt`，对应 `[router]` 不暴露）
   - 启动 shell 前 daemon 用 `os.Setenv` 注入；脚本开头 `: "${DNS_PORT:?DNS_PORT not set}"` 等校验

3. 当前 repo 的 `config/clash.json` 中 `external_ui_download_url` 用的是 zashboard 的某个 cdn-fonts zip。`assets/config.d.default/clash.json` 保留同样链接；用户可在 daemon.toml `[download].zashboard_url` 里覆盖（这条留给 D 阶段加，A 阶段不引入）。

---

## 11. 范围之外（明确不做）

A 阶段**不**包含的功能（避免范围蔓延）：

- 自动定时下载 zoo.json / cn.txt / sing-box（B 阶段）
- 自动版本兼容性检查 + 回滚（B 阶段）
- 投递日志到远程 Seq（C 阶段）
- 自定义 Web UI（D 阶段；A 阶段可访问的是 sing-box 自带 clash_api + zashboard）
- Bark 通知（E 阶段）
- 自动备份（F 阶段）
- amtm 集成 / 安装菜单（永远不做，除非未来明确决定）
- IPv6 透明代理 / TPROXY 替代 REDIRECT（未来另立 spec）
- 多个 Asus 路由器型号适配（仅 RT-BE88U / aarch64）

---

## 12. 实施风险与缓解

| 风险 | 缓解 |
|---|---|
| Merlin 升级后 nat-start 钩子机制变化 | 钩子只调 `sing-router reapply-rules`，钩子内容极简；升级后若钩子不再触发，degrades 到"用户手动 reapply-rules"，不影响主路径 |
| sing-box 升级后日志格式变化 | parser 失败的行降级为 `Parsed=false` 原始事件，不丢；C 阶段 Seq 中 Parsed=false 比例可作为告警 |
| zoo.json 来源端 schema 变化 | 预处理只白名单提取 4 个字段，对源端添加新字段宽容；新字段被丢，记 `dropped_field` 事件供观察 |
| 守护进程崩溃（panic）导致 sing-box 失控 | sing-router 自身 fatal 时不杀 sing-box；init.d stop 才会拆 iptables；用户可 ssh 上去手动救 |
| 单文件二进制过大（embed 资源） | 默认配置 fragment 仅几 KB；assets 总量预计 < 200 KB；二进制总大小 < 15MB（不含 sing-box） |
| `go:embed` 的脚本被恶意修改 | 二进制本身被替换前提下任何防御都失效；不在威胁模型内 |

---

## 13. 实施完成的标准（A 阶段验收）

- [ ] `go test ./...` 全部通过；`internal/config/zoo.go` 与 `internal/install/jffs_hooks.go` 覆盖率 100%
- [ ] `sing-router install` 在干净的 RT-BE88U 上一键铺好所有文件；`status` 立即可用
- [ ] `sing-router start` 后路由透明代理正常工作（curl ipinfo.io 看到代理出口；DNS 查询不泄漏）
- [ ] `sing-router restart` 期间客户端 connection refused 但 1-2s 后恢复；DNS 不泄漏
- [ ] 模拟 sing-box 崩溃（kill -9 子进程）后守护进程按退避序列恢复；< 10s 档位 iptables 不拆
- [ ] `nat-start` 模拟（手工 `iptables -F` 后 `sing-router reapply-rules`）能恢复全部规则
- [ ] `sing-router uninstall --purge` 后系统干净（无残留 init.d / jffs 块 / $RUNDIR）
- [ ] `sing-router logs -f` 实时显示 daemon + sing-box 事件，时区在本地省略；`--json` 输出 CLEF
- [ ] zoo.json 各种异常输入（语法错、撞名、url 撞车）按预处理规则正确处理；status 字段反映对应 stats

---

文档结束。

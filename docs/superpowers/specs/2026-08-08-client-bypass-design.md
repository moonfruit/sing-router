# 设计：LAN 客户端动态 bypass 白名单（心跳续约 + ipset TTL）

- 日期：2026-08-08
- 状态：待批准
- 范围：让「自己已经跑了透明代理的 LAN 客户端」把自身 IP 注册到路由器，使其流量不被 sing-router 二次代理。租约由客户端心跳续约，过期由内核 ipset timeout 负责。本次仅 IPv4。

## 背景与动机

MacBook 本机跑 sing-box（TUN + `auto_route`），同时以路由器为默认网关。路由器的 sing-router 不认识「这个客户端已经代理过自己了」，于是产生三类内耗：

1. **双重代理**：Mac 把流量加密后发往机场节点，若节点端口落在 `proxy_ports`（`22,80,443,8080,8443`）内，路由器再套一层自己的节点。实测该机场 16 个节点在 443 端口，全部命中。延迟叠加、路由器 ARM 白扛一份加解密、两边机场各计一份流量。
2. **直连意图被覆盖**：Mac 侧 `Direct` / `FakeIpBypass` 判定为直连的境外目标，出网卡后被路由器 REDIRECT 代理掉。客户端精心维护的直连白名单在路由器面前完全无效。
3. **DNS 污染**：`sing-box-dns` 链对 LAN 源的 tcp/udp 53 一律 REDIRECT，无 CN 豁免。Mac 的明文上游查询由路由器应答，非 CN 域名会拿到路由器的 fakeip `28.x`，Mac 当作真实地址去连接必然失败。

### 为什么不能用 IP 或 MAC 做身份

MacBook 通过 Dock 的以太网口接入，而**该 Dock 会被换到另一台 PC 使用**。Dock 的 MAC 属于 Dock，不属于 MacBook，因此：

- `-m mac --mac-source` 匹配不到「是哪台主机」
- DHCP MAC 保留会把同一个 IP 发给两台不同的机器

身份必须来自跟随操作系统而非跟随硬件的东西。本设计选择 **token**：token 存在 MacBook 上，PC 插上同一个 Dock 也拿不到，注册不了。

### 为什么不是「sing-box 起停时注册/注销」

客户端的 sing-box 是常驻的（LaunchDaemon + `KeepAlive`），从插 Dock 到拔 Dock 全程不重启。以进程生命周期为触发点，钩子一次都不会触发。真正在变的是**网络位置**，所以模型必须是持续续约而非一次性事件。

## 非目标

- **IPv6**：路由器当前对 LAN 的 v6 只有 53 / 853 的 REJECT，没有任何代理链；而客户端的 DNS 上游均为 v4 直连 IP 或 h3/DoH（443），撞不到那两条 REJECT。v6 bypass 当前收益接近零，留到「IPv6 REDIRECT 兜底」待办落地时一并设计。本次 API 对 v6 地址**显式 400 拒绝**，不静默丢弃。
- **客户端 agent 的 Go 化**：agent 保持 shell + launchd，不做成 sing-router 子命令，避免 sing-router 从「路由器管理器」变成双端产品。
- **租约持久化**：daemon 不落盘、不维护内存租约表。过期完全交给内核。

## 信任模型

| 边界 | 机制 |
|---|---|
| 客户端身份 | token（`[http].token`）。持有 token 即可为 LAN 内任意 IP 开门——这是刻意接受的：token 就是信任边界。 |
| 注册内容 | **采信 body 中的 IP 列表**，不从 TCP 连接源地址推导。必需，因为 listener 只监听 v4 而 agent 未来要一次注册多个地址。 |
| 本机调用 | 来源为 loopback 时免 token 且可访问全部端点，CLI 行为完全不变。 |
| LAN 调用 | 必须带 token，且**只能访问显式白名单内的路径**。 |
| 监听面 | `tcp4` 强制。v6 下路由器地址公网可达，绝不监听。 |

**两条必须写进代码注释的约束**：

1. 绝不能在该 listener 前挂反向代理，否则 `RemoteAddr` 全部变成 `127.0.0.1`，等于全世界免 token。
2. 显式不信任 `X-Forwarded-For` 等转发头。

不额外为 9998 补 `iptables INPUT` 限制：路由器的 clash api 已经是 `[::]:9999` 双栈全接口监听，9998 是 tcp4-only，暴露面小于既有姿态，两者同靠 NAT + 固件 v6 防火墙兜底。单独给 9998 加保护是不对称的，只会给出虚假的安全感。

## 命名约定

`Routing.EnvVars` 中**已存在 `BYPASS_MARK`**（fwmark `0x7890`，语义是「打了这个 mark 的包不走代理」），与本功能的客户端白名单完全无关。为避免 `startup.sh` 中两类 `BYPASS_*` 混淆：

| 层 | 名称 |
|---|---|
| toml 段 | `[bypass]`（顶层，与 `[router].bypass_mark` 层级不同） |
| 环境变量 | `CLIENT_BYPASS_ENABLED` / `_TTL` / `_STATIC_IPS` / `_STATIC_MACS` |
| ipset | `client_bypass` / `client_bypass_static` / `client_bypass_mac` |

## 配置

```toml
[http]
listen = "127.0.0.1:9998"  # 模板默认值，保持现状不变；强制 tcp4
token  = ""                # 仅对非 loopback 来源生效

[bypass]
enabled         = false    # 默认关，opt-in
default_ttl_sec = 120
max_ttl_sec     = 600
static_ips      = []       # → client_bypass_static
static_macs     = []       # → client_bypass_mac；为空则该 set 与规则都不存在
```

`daemon.toml.tmpl` 的默认值保持 `listen = "127.0.0.1:9998"` 与 `[bypass].enabled = false`，即**不装本功能时行为与今天完全一致**。放开监听面是 install 时的显式动作：

- `install --http-token <token>`（不传值则自动生成 32 hex）写入 `[http].token`，同时把 `[http].listen` 渲染成 `0.0.0.0:9998`、`[bypass].enabled` 渲染成 `true`。flag 命名跟随其落点 `[http].token`，而非跟随本功能。
- `enabled = true` 且 `[http].token` 为空 → daemon 启动失败并给出明确错误。token 是本功能唯一的身份来源，不允许空跑。
- `enabled = true` 但 `[http].listen` 仍是 loopback → 启动时记一条 warn 事件（LAN 客户端无法注册），但不阻止启动。

新增 `config.Bypass` 结构体承载 `[bypass]` 段，自带 `EnvVars() map[string]string`。`wireup_daemon.go` 中与 `routing.EnvVars(cnPath)` 合并（`maps.Copy`），两个结构体各管各的，互不侵入。

## API 契约

```
POST   /api/v1/bypass    { "ips": ["192.168.50.80"], "ttl_sec": 120 }
DELETE /api/v1/bypass    { "ips": ["192.168.50.80"] }
GET    /api/v1/bypass    → 当前 set 内容 + 剩余 TTL

Header: Authorization: Bearer <token>
```

`ttl_sec` 省略时取 `default_ttl_sec`。

### 鉴权中间件

**仍是单 listener、单 mux**：bypass 端点注册到现有 `NewMux` 返回的 mux 上，整个 mux 外面包一层鉴权中间件。不引入第二个监听端口或第二个 mux。

关键是**白名单而非黑名单**——新增端点默认不在表里，失败模式从「忘了加检查就全暴露」反转成「忘了加白名单就用不了」：

```go
// 只有列在这里的路径才允许 LAN 访问。
var lanAllowed = map[string]bool{"/api/v1/bypass": true}

if isLoopback(r.RemoteAddr) {
    next(w, r)                       // 本地全权
    return
}
if !bypassEnabled || !lanAllowed[r.URL.Path] { 403 }
if r.Method == http.MethodGet { 403 }          // 读只给 loopback
if !subtle.ConstantTimeCompare(bearer(r), token) { 401 }
next(w, r)
```

`GET` 不给 LAN：持有 token 的客户端只能写不能读，枚举不出当前放行了谁。`doctor` 在路由器本机跑，走 loopback，不受影响。

### 校验顺序与错误码

全部返回 400（除鉴权），错误码可区分：

1. JSON 解析 → `bypass.bad_request`
2. `ips` 为空或超过条数上限（16）→ `bypass.too_many_ips`
3. 每个 IP 过 `net.ParseIP`；解析失败 → `bypass.bad_ip`
4. **v6 地址 → `bypass.ipv6_unsupported`**（显式拒绝，不静默丢弃）
5. `ttl_sec > max_ttl_sec` 或 `< 1` → `bypass.bad_ttl`

任一条目不合法则整个请求失败，不做部分接受——避免客户端以为注册成功了却少了一个地址。

## ipset 布局与生命周期

三种生命周期各不相同，因此是三个 set：

| set | 类型 | 谁写 | teardown | 无配置时 |
|---|---|---|---|---|
| `client_bypass` | `hash:ip timeout N` | daemon 动态注册 | **不销毁** | 随 `enabled` |
| `client_bypass_static` | `hash:ip` | startup.sh 从配置 flush + 重填 | 销毁 | 不建 |
| `client_bypass_mac` | `hash:mac` | startup.sh 从配置 flush + 重填 | 销毁 | **不建，也不插规则** |

**动态与静态必须分家**：静态条目要支持「配置里删掉后立刻失效」，就得 `flush` 重填；而 `flush` 会连带清掉动态租约。分开后 `client_bypass` 永不 flush，只靠 TTL 自然淘汰。

**`client_bypass` 在 teardown 时刻意保留**：租约是客户端持续声明的状态，不是路由器的状态。iptables 规则归路由器（teardown 拆干净），租约归客户端。若跟着销毁，`Restart`（`Shutdown` + `Startup`，ready check 最长 60s）加上下一轮心跳（30s），客户端最坏约 90s 被误代理。保留后规则装回来的瞬间即生效。代价是 daemon 停止后 set 悬留内核（几 KB），由 `uninstall` 清理；daemon 长期停止时条目也会自行 TTL 过期。

## 脚本改动

### `startup.sh`

新增段落紧接现有 `ipset cn` 段之后：

```bash
# ===================== ipset：客户端 bypass 白名单 =====================
if [ -n "${CLIENT_BYPASS_ENABLED:-}" ]; then
    # 动态租约 set：create 幂等且【不清空】。租约是客户端持续声明的状态，
    # 不能被 Restart(Shutdown+Startup) 冲掉。过期完全交给内核 timeout。
    ipset create client_bypass hash:ip timeout "$CLIENT_BYPASS_TTL" 2>/dev/null || true

    # 静态 set：配置驱动，flush 后重填，保证配置里删掉的条目立刻失效。
    if [ -n "$CLIENT_BYPASS_STATIC_IPS" ]; then
        ipset create client_bypass_static hash:ip 2>/dev/null || true
        ipset flush client_bypass_static
        for ip in $CLIENT_BYPASS_STATIC_IPS; do
            ipset -exist add client_bypass_static "$ip"
        done
    fi
    if [ -n "$CLIENT_BYPASS_STATIC_MACS" ]; then
        ipset create client_bypass_mac hash:mac 2>/dev/null || true
        ipset flush client_bypass_mac
        for mac in $CLIENT_BYPASS_STATIC_MACS; do
            ipset -exist add client_bypass_mac "$mac"
        done
    fi
fi
```

RETURN 规则用 helper 避免三处重复：

```bash
# 往 <table> <chain> 追加 bypass RETURN。必须在各链创建后、其余 -A 之前调用。
# 装在链首：省掉后续 match 开销，语义也最直白（"这个源与我们无关，立即返回"）。
# 注意：一律用 if/fi 而非 `[ -n "$X" ] && cmd`——脚本开头是 set -eu，而
# AND-list 在条件为假时整体退出码为 1，会直接掀掉整个 startup。
add_client_bypass_returns() {
    if [ -z "${CLIENT_BYPASS_ENABLED:-}" ]; then return 0; fi
    iptables -t "$1" -A "$2" -m set --match-set client_bypass src -j RETURN
    if [ -n "$CLIENT_BYPASS_STATIC_IPS" ]; then
        iptables -t "$1" -A "$2" -m set --match-set client_bypass_static src -j RETURN
    fi
    if [ -n "$CLIENT_BYPASS_STATIC_MACS" ]; then
        iptables -t "$1" -A "$2" -m set --match-set client_bypass_mac src -j RETURN
    fi
}
```

调用点三处，均紧跟对应链的 `-N … || -F …` 之后：

- `add_client_bypass_returns nat sing-box`
- `add_client_bypass_returns mangle sing-box-mark`
- `add_client_bypass_returns nat sing-box-dns`

一条 iptables 规则只能引用一个 set，故三个 set 即三条规则——这也是「无静态 MAC 就不建 set」能直接省掉一条规则的原因。

### `teardown.sh`

链的 `-F` + `-X` 已经带走 RETURN 规则，只需处理 set。现有代码已是「先拆 iptables 再 destroy ipset」的顺序，满足 `ipset destroy` 对「无内核组件引用」的要求：

```bash
ipset destroy cn 2>/dev/null || true
ipset destroy client_bypass_static 2>/dev/null || true
ipset destroy client_bypass_mac 2>/dev/null || true
# client_bypass 动态 set 刻意保留——见 startup.sh 注释。此刻已无规则引用它，
# 下次 startup 直接复用；daemon 长期停止时条目会自行 TTL 过期。
```

### `uninstall`

`uninstall` 不调用 `teardown.sh`——它 SIGTERM daemon，由 daemon 的 defer 触发 teardown。因此保留下来的 `client_bypass` 需要显式清理点：在 `stopDaemonByPidFile` 之后补一次 best-effort `ipset destroy client_bypass`（此时 daemon 已退出、teardown 已跑完，无引用）。失败静默——非 Linux 平台或 set 本就不存在都属正常。

## daemon 组件设计

新文件 `internal/daemon/bypass.go`：

```go
type BypassDeps struct {
    Enabled    bool
    DefaultTTL time.Duration
    MaxTTL     time.Duration
    // ipsetRun 可注入，测试用 fake 断言 argv。
    ipsetRun   func(ctx context.Context, args ...string) error
}
```

用 `os/exec` 直接传 argv，不经 shell 解析，天然免疫注入（IP 另有 `net.ParseIP` 前置校验）：

```
add:  ipset -exist add client_bypass 192.168.50.80 timeout 120
del:  ipset -exist del client_bypass 192.168.50.80
list: ipset list client_bypass
```

不新建 `internal/ipset` 包——当前只有三个固定命令，抽象层不划算。

`ServeHTTP` 需要改造：现有实现把 `listen` 字符串直接交给 `http.Server.Addr`（走 `net.Listen("tcp", …)`，双栈）。改为显式 `net.Listen("tcp4", listen)` 后 `srv.Serve(ln)`，确保 `0.0.0.0:9998` 不会收到 v6 映射连接。

## 客户端 agent（macOS）

交付到 `~/Workspace.localized/env/etc/sing-box/`，sing-router 仓库 `contrib/macos/` 放参考副本（不嵌入二进制）：

- `bypass-agent.sh`
- `bypass-agent.conf`（`chmod 600 root:wheel`，含 token）
- `moonfruit.sing-bypass.plist` → `/Library/LaunchDaemons/`

**必须是 LaunchDaemon 而非 LaunchAgent**：用户登出或未登录时 sing-box 仍在跑（它是 LaunchDaemon），agent 若是 LaunchAgent 就会停 → 租约过期 → 客户端被路由器代理 → 双重代理。两者生命周期必须对齐。

**token 不进 plist**：`/Library/LaunchDaemons/*.plist` 是 644，token 单独放 `bypass-agent.conf`，plist 仅通过 `EnvironmentVariables` 传配置文件路径。

`bypass-agent.conf` 字段：

| 字段 | 含义 | 示例 |
|---|---|---|
| `ROUTER_URL` | 路由器 API 基址 | `http://192.168.50.1:9998` |
| `TOKEN` | `[http].token` 的值 | — |
| `GATEWAY` | 用于反查出口接口的网关地址 | `192.168.50.1` |
| `LOCAL_CLASH_API` | 本机 sing-box clash api | `http://127.0.0.1:9999` |
| `TTL` | 请求的租约秒数 | `120` |
| `STATE_FILE` | 上次注册 IP 的记录路径 | `/var/run/bypass-agent.ip` |

**调度用 `StartInterval` 而非常驻循环**：脚本跑完即退，launchd 负责节拍，比 `while true; do sleep 30; done` 更 launchd-native，也不会因脚本崩溃而永久失联。

```xml
<key>StartInterval</key><integer>30</integer>
<key>RunAtLoad</key><true/>
<key>WatchPaths</key>
<array>
  <string>/etc/resolv.conf</string>
  <string>/Library/Preferences/SystemConfiguration/</string>
</array>
```

`WatchPaths` 把插拔 Dock 的切换窗口从 30s 压到约 1s；`StartInterval` 是兜底节拍。

主流程：

```bash
# 1. 探活：用 clash api /version，与 sing-router 自己 ready check 的信号保持一致
curl -sf --max-time 2 "$LOCAL_CLASH_API/version" >/dev/null || revoke_and_exit

# 2. 找通往网关的接口与源地址（不用 default 路由——TUN 的 auto_route 会接管它；
#    网关是同网段私有地址，走直连路由，拿到的必定是物理口）
iface=$(route -n get "$GATEWAY" | awk '/interface:/{print $2}')
ip=$(ifconfig "$iface" inet | awk '/inet /{print $2; exit}')

# 3. IP 变了先注销旧的，把切换窗口从 TTL 压到接近 0
prev=$(cat "$STATE_FILE" 2>/dev/null || true)
if [ -n "$prev" ] && [ "$prev" != "$ip" ]; then revoke "$prev"; fi

# 4. 续约并记录
curl -sf -X POST -H "Authorization: Bearer $TOKEN" \
     -d "{\"ips\":[\"$ip\"],\"ttl_sec\":$TTL}" "$ROUTER_URL/api/v1/bypass"
echo "$ip" > "$STATE_FILE"
```

**两条失败语义必须区分**：

- **sing-box 不健康** → 主动 revoke。此刻仍在 LAN 内，注销请求发得出去，路由器立刻接管，客户端不裸奔。revoke 请求本身失败（路由器同时也不可达）时静默退出，不重试——TTL 会兜住。
- **路由器不可达**（daemon 停了 / 设备离开 LAN）→ **不发 revoke，静默退出**。此时请求本来也发不出去，靠 TTL 自然过期即可。

**日志只在状态变化时写**（IP 变更、注册失败、探活失败），稳态静默——否则每天 2880 次心跳会把日志刷爆。

### 场景收敛表

| 场景 | agent 行为 | 结果 |
|---|---|---|
| 插 Dock 拿到 `.80` | 续约 `.80` | 放行 `.80` |
| 拔 Dock，Wi-Fi 接管 `.81` | 注销 `.80`，续约 `.81` | 切换窗口 ≈ 0 |
| PC 插上同一 Dock 拿到 `.80` | PC 无 token，不会续约 | TTL 到期后彻底失效 |
| 合盖睡眠 / 断电 / 崩溃 | 停止续约 | 过期，路由器重新接管 |
| 设备离开 LAN | 续约失败，不 revoke | 同上 |
| 手动停 sing-box 调试 | 探活失败 → 主动 revoke | 路由器立刻接管，不裸奔 |

## 可观测性

`sing-router doctor` 新增一节（`doctor_routing.go`，走 loopback 故可读 `GET`）：

- `[bypass].enabled` 状态与 listen 地址
- 三个 set 的存在性与条目数
- 三条链的 RETURN 规则是否就位、顺序是否在 `-j REDIRECT` 之前
- 当前动态租约列表与各自剩余 TTL

这是本功能唯一的可观测手段，否则排障只能 ssh 上去手敲 `ipset list`。

## 测试策略

### Go 单测（`internal/daemon/bypass_test.go`）

鉴权矩阵是重点：

| 用例 | 期望 |
|---|---|
| loopback 无 token 访问任意端点 | 200，CLI 行为完全不变 |
| LAN + 正确 token → `POST /api/v1/bypass` | 200 |
| LAN + 正确 token → `/api/v1/shutdown` | **403**（不在白名单） |
| LAN + 正确 token → `GET /api/v1/bypass` | **403**（读只给 loopback） |
| LAN + 错 token / 无 token | 401 |
| `enabled=false` 时 LAN 访问 bypass | 403 |
| body 含 v6 地址 | 400 `bypass.ipv6_unsupported` |
| `ttl_sec` 越界 / `ips` 超条数 | 400 |

`ipsetRun` 注入 fake，逐字断言 argv 为 `-exist add client_bypass 192.168.50.80 timeout 120`。

### config 单测

`[bypass]` 段解析与默认值；`enabled=true` 且 token 为空 → 启动错误。

### `assets/embed_test.go`（静态特征）

前两条防「看起来对实际不能跑」，第三条把设计决定钉死：

- 三条链各含 `--match-set client_bypass src -j RETURN`，且位置在 `-j REDIRECT` 之前
- 静态段被 `if [ -n "$CLIENT_BYPASS_STATIC_MACS" ]` 包住，**不出现 `[ -n "$X" ] && iptables`** 这种 AND-list 写法
- **断言 `teardown.sh` 中不存在 `ipset destroy client_bypass`**（只有 `_static` / `_mac`）——防止日后有人"顺手补全"，把刻意保留改成销毁

### `docker-test`（端到端）

镜像已装 `ipset`，无需改 Dockerfile。从容器 eth0 地址（非 loopback）发请求：

1. 起 daemon（`enabled=true` + token）
2. POST → `ipset list client_bypass` 含该 IP，timeout 递减
3. 错 token → 401；LAN 访问 `/api/v1/shutdown` → 403
4. DELETE → 条目消失
5. **`sing-router restart` 后 `client_bypass` 内容存活** ← 整个设计最关键的行为
6. teardown 后 `client_bypass_static` / `_mac` 消失、`client_bypass` 仍在

## 风险与已知限制

- **误放行窗口**：TTL 120s 意味着 PC 插上 Dock 后最长 2 分钟内可能被误放行。误放行的后果只是多绕一层代理，非安全问题。缩短 TTL 与心跳间隔可压缩该窗口。
- **token 泄露**：持有者可为 LAN 内任意 IP 开门。缓解手段是 `bypass-agent.conf` 的 600 权限；不做额外的 IP 范围校验，因为 `sing-box` 链本就只对 `-s $LAN` 生效，为 LAN 外地址开门没有实际效果。
- **反向代理会击穿鉴权**：见「信任模型」。以代码注释固化，不做运行时检测。
- **client_bypass 悬留**：daemon 停止后 set 留在内核，直到 `uninstall` 或路由器重启。成本为几 KB。
- **v6 未覆盖**：客户端的 v6 流量当前本就不被路由器代理，无回归风险；待 v6 代理落地时需同步扩展本设计。

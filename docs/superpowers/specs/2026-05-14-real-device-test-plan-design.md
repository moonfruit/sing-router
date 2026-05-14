# sing-router 路由器实机测试方案：不间断服务回归

> 设计稿 · 2026-05-14 · 状态：待评审

## 1. 目的与范围

本方案是 sing-router 在华硕路由器（koolshare + Entware 为主）上的**实机回归清单 + 故障注入手册**，每次发版前手动跑一遍，同时作为未来 Docker 化的需求来源。

**唯一关注点：不间断服务。** 不论运行中发生什么故障或事件，LAN 客户端的网络访问都不应被硬中断；即使最坏情况下 sing-box 完全退出，也必须把 iptables / 路由规则拆干净，干净回退到直连。

**范围**：服务**已正常运行**之后可能遇到的一系列运行时故障与事件。

**明确不在范围**：

- install / uninstall / 首次部署
- 冷启动（init.d 冷启）/ reboot 自启
- 日志相关（rotate、`.gz` 归档、`stderr.log`）
- `doctor` / `status` 字段细节、CLI 子命令边角行为

`status` / `logs` / `iptables -L` 等只作为**观测手段**出现，不作为被测对象。

## 2. 核心不变量与验收标准

整套方案只有一个验收标准 —— 核心不变量：

> **任意时刻、任意场景，LAN 客户端流量只允许处于两种状态之一：**
> **(a) 经 sing-box 代理（代理通）；**
> **(b) iptables / 路由已拆净的干净直连（直连通）。**
>
> **第三态 —— iptables 仍在重定向，但目标端口已无 sing-box 监听 —— 即「黑洞」，是 FAIL。**
>
> **瞬时过渡窗口允许存在，但必须「有界」且「量化」（给出实测时长上限）。**

三态判定（见 §7 探针参考实现）：

| 状态 | LAN 客户端可达 | iptables `sing-box` 链 / 路由表 | 结论 |
|---|---|---|---|
| 代理通 | ✅ | 存在 + sing-box 在监听 | PASS |
| 直连通 | ✅ | 已拆净 | PASS |
| **黑洞** | ❌（超时 / 拒绝） | 存在但无监听 | **FAIL** |

过渡窗口的 PASS 条件：窗口有明确终点（最终收敛到代理通或直连通），且实测时长被记录、在文档中评估为可接受或登记为待优化项。

## 3. 测试环境与前置

- **目标固件**：koolshare 为主；merlin 差异在相关用例内单列。
- **SSH 通道**：会话内提供可直达路由器的 SSH 别名 / 凭证，Claude Code 经 `ssh <router> '...'` 执行命令并读取输出做断言。
- **统一前置条件**：每个用例开始前，服务必须处于 `state=running`、代理通（探针确认）。用例结束后需将环境恢复到该状态。
- **三态探针**：路由器本机 `curl` + 一个 LAN 侧客户端 `curl` / `ping`，见 §7。
- **观测手段**（只读，不作被测对象）：
  - `sing-router status [--json]` —— 状态机、daemon pid、sing-box pid
  - `iptables -t nat -nL sing-box` / `iptables -t nat -nL sing-box-dns` / `iptables -t mangle -nL sing-box-mark`
  - `ip rule` / `ip route show table $ROUTE_TABLE`（默认 7892）
  - `ipset list cn`
  - `ps | grep sing-box` —— 区分 daemon 与子进程 pid
  - `sing-router logs` / `log/sing-router.log` / `/tmp/sing-router-nat-start.log`（仅排障观测）
- **风险与带外恢复**：标记 🔴 的用例可能切断 SSH 自身或使 LAN 全断，执行前必须确认有带外恢复手段（路由器物理可达、固件 Web 后台、串口等）。

## 4. 测试用例

每个用例字段：**触发方式 / 前置 / 执行步骤 / 期望结果（三态）/ 观测点 / Claude 协助度 / Docker 可虚拟化**。

协助度图例：🟢 Claude 全自动（SSH 注入 + 探针断言）｜🟡 需人工配合｜🔴 风险操作，需带外恢复。

---

### S 组 —— 退出 / 崩溃必须拆净

核心：任何形式的退出都必须 teardown 干净，回到直连通；绝不留黑洞。

#### S1 正常 `stop` → teardown 全清

- **触发**：`sing-router stop`
- **前置**：代理通
- **步骤**：
  1. 记录当前 iptables 链 / 路由表 / ipset 快照
  2. `sing-router stop`
  3. 探针 + 复查全部规则
- **期望**：状态机进入 stopping → 子进程退出 → `teardown.sh` 跑完；`sing-box` / `sing-box-dns` / `sing-box-mark` 链、`FORWARD -o $TUN`、`ip rule fwmark`、`ip route table $ROUTE_TABLE`、`cn` ipset **全部清空**；三态 = **直连通**。
- **观测点**：`iptables -nL`、`ip rule`、`ip route show table $ROUTE_TABLE`、`ipset list cn`
- **协助度**：🟢
- **Docker**：✅ 完全可虚拟化

#### S2 sing-box 反复崩溃 → `StateFatal` → 已拆净

- **触发**：连续 `kill` sing-box 子进程，跨越退避 ladder 直到 `IptablesKeepBackoffLtMs`（默认 10s）以上
- **前置**：代理通
- **步骤**：
  1. 反复 `kill $(sing-box 子进程 pid)`，每次等 supervisor 重启后再 kill，直到退避档位 ≥ 10s
  2. 让其耗尽 ladder（或持续 kill）直到 `StateFatal`
  3. 探针 + 复查规则
- **期望**：退避档位 ≥ 10s 起 supervisor 调 `TeardownHook` 拆 iptables；进入 `StateFatal` 后 iptables 已拆净；三态 = **直连通**，不黑洞。
- **观测点**：`sing-router status`（状态流转 degraded→…→fatal）、退避日志、iptables 快照
- **协助度**：🟢
- **Docker**：✅

#### S3 daemon 收 `SIGTERM`

- **触发**：`kill -TERM $(daemon pid)`
- **前置**：代理通
- **步骤**：`kill -TERM` daemon → 探针 + 复查规则 + `ps`
- **期望**：daemon 优雅退出，sing-box 子进程被收掉，`teardown.sh` 跑完，规则全清；三态 = **直连通**。
- **观测点**：`ps`、iptables 快照
- **协助度**：🟢
- **Docker**：✅

#### S4 daemon 被 `kill -9`（最坏情况拷问）

- **触发**：`kill -9 $(daemon pid)`
- **前置**：代理通
- **步骤**：
  1. `kill -9` daemon
  2. 立即探针 + `ps` 确认 sing-box 子进程是否存活
  3. 随后 `kill -9` 该孤儿 sing-box，再次探针
- **期望（拷问点）**：
  - daemon 被 `kill -9` 后无机会跑 teardown；sing-box 子进程因 setpgid 隔离**成为孤儿、继续运行** → iptables 仍在 + 监听仍在 → 此刻**代理通**，不黑洞。
  - 但**孤儿 sing-box 后续一旦崩溃，无 supervisor 拆规则 → 黑洞**。本用例量化这条链，并评估是否需要兜底机制（如 init.d `check` / cron 看门狗周期校验 daemon pid 存活）。
- **观测点**：`ps`（daemon pid 消失、sing-box pid 仍在）、iptables 快照、两次探针对比
- **协助度**：🔴（孤儿链最终可能黑洞使 LAN 全断；需带外恢复）
- **Docker**：✅（且 Docker 是安全复现此用例的首选环境）

#### S5 sing-box 子进程被 `kill -9`，daemon 健在

- **触发**：`kill -9 $(sing-box 子进程 pid)`，仅一次
- **前置**：代理通
- **步骤**：`kill -9` 子进程 → 观察 supervisor 退避重启 → 探针
- **期望**：supervisor 检测子进程退出 → 进 `degraded` → 按 ladder 退避重启 → 重新 ready → 回到代理通。单次崩溃且退避档位 < 10s 时 iptables 保留（见 W1 窗口量化）。
- **观测点**：`status`、退避日志、`ps`（新 sing-box pid）
- **协助度**：🟢
- **Docker**：✅

---

### W 组 —— 过渡窗口黑洞量化（重点）

核心：以下场景 iptables 不被拆、但 sing-box 短暂不可服务，必须**量化窗口时长**并评估可接受性。

#### W1 崩溃退避 `< 10s` 保留 iptables 的窗口

- **触发**：`kill` sing-box 子进程（退避档位 < `IptablesKeepBackoffLtMs`）
- **前置**：代理通
- **步骤**：
  1. `kill` 子进程
  2. 高频探针（间隔 ≤ 0.5s）从子进程退出到新进程 ready，记录黑洞持续时长
- **期望**：窗口内 iptables 保留但无监听 → 短暂黑洞；窗口在退避时长 + ready 时间内收敛回代理通。**量化实测黑洞时长**，评估「赌快速回归而不拆 iptables」是否可接受。
- **观测点**：探针时间序列、退避档位日志
- **协助度**：🟢
- **Docker**：✅（Docker 适合精确测时）

#### W2 资源更新触发 restart 的窗口（最大风险点）

- **触发**：applier 因资源变化执行 `Restart`（`skipStartupIfInstalled` 路径，不拆 iptables）
- **前置**：代理通
- **步骤**：
  1. 触发一次有效资源更新（见 D1）使 applier 走 restart
  2. 高频探针，记录从旧 sing-box 被杀到新 sing-box ready 的黑洞时长
- **期望**：restart 路径不拆 iptables，窗口内黑洞；ready check 总超时默认 60s，**窗口上限可达 30s+**。量化实测时长，评估是否需优化（如 restart 期间临时拆 iptables 回退直连，或先 ready 新进程再切）。
- **观测点**：探针时间序列、`status`（reloading→running）、ready 日志
- **协助度**：🟢
- **Docker**：✅（但需真 sing-box，fake-box 无法复现真实 ready 耗时）

#### W3 `kill -HUP sing-box` → TUN / 路由重建窗口

- **触发**：`kill -HUP $(sing-box 子进程 pid)`
- **前置**：代理通
- **步骤**：
  1. `kill -HUP` sing-box → sing-box 内部 reload → TUN 设备重建 → 内核自动删除 device-bound 默认路由
  2. 高频探针，观察 `WatchRoutes`（默认 30s 巡检）检测到路由缺失并调 `reapply-routes.sh` 补回
  3. 记录 device-bound 路由缺失窗口时长
- **期望**：HUP 后 supervisor 全程不知情（pid 未变、子进程未退）；`WatchRoutes` 周期巡检发现 `default dev $TUN table $ROUTE_TABLE` 丢失 → 调 `ReapplyRoutesHook` 补回。**量化路由缺失窗口（最坏 ≈ 巡检周期 30s）**，评估是否需缩短巡检周期或让 sing-box reload 事件主动通知。
- **观测点**：`ip route show table $ROUTE_TABLE` 时间序列、`shell.reapply_routes.exec` 日志、探针
- **协助度**：🟢
- **Docker**：✅（需 `--privileged` + `/dev/net/tun` + 真 sing-box）

#### W4 `supervisor.Restart` 快速重启路径窗口

- **触发**：`sing-router restart`
- **前置**：代理通
- **步骤**：`sing-router restart` → 高频探针记录窗口
- **期望**：走 `skipStartupIfInstalled` 快速重启（不拆 iptables，不重跑 startup.sh，但跑 `reapply-routes.sh` 补 device-bound 路由）；量化窗口时长，与 W2 对比（restart 子命令通常比资源更新触发的 restart 更快）。
- **观测点**：探针时间序列、`status`、`reapply-routes` 日志
- **协助度**：🟢
- **Docker**：✅

---

### R 组 —— 重装规则不能装半套

核心：`reapply-rules` 在 sing-box 不可服务时绝不能装出「规则在、监听不在」的半套黑洞。

#### R1 sing-box 健在时 `reapply-rules` / koolshare `start_nat`

- **触发**：`sing-router reapply-rules`；或手动跑 `N99sing-router.sh start_nat`
- **前置**：代理通
- **步骤**：
  1. （可选）先 `iptables -t nat -F` 模拟规则被清
  2. 跑 `reapply-rules`（或 N99 钩子）
  3. 探针 + 复查规则
- **期望**：teardown + startup 重跑，iptables / 路由规则装回，三态 = 代理通；N99 钩子在 `/tmp/sing-router-nat-start.log` 留下 `reapply-rules ok`。
- **观测点**：iptables 快照、`/tmp/sing-router-nat-start.log`
- **协助度**：🟢
- **Docker**：✅

#### R2 sing-box 不健康时 `reapply-rules` —— 必须失败不留半套

- **触发**：在 daemon 未运行、或 sing-box 处于 `fatal` / 未 ready 状态下跑 `reapply-rules`
- **前置**：人为制造 sing-box 不可服务（如先 `stop`，或杀到 `StateFatal`）
- **步骤**：
  1. 使 sing-box 不可服务（规则此时应已为直连状态）
  2. 跑 `sing-router reapply-rules`
  3. 探针 + 复查规则
- **期望**：`reapply-rules` **失败并返回非 0**，**不留下半套规则**（不出现「装了 iptables 重定向但无监听」的黑洞）；三态保持 **直连通**。
- **观测点**：命令退出码、iptables 快照、`/tmp/sing-router-nat-start.log` 的 `reapply-rules FAILED`
- **协助度**：🟢
- **Docker**：✅

#### R3 真实 WAN 重拨

- **触发**：物理拔插 WAN 网线 / 固件触发拨号重连
- **前置**：代理通
- **步骤**：
  1. 触发真实 WAN 重拨
  2. 重拨期间探针（固件清 iptables → 应为直连通，不黑洞）
  3. 等 koolshare `start_nat` → N99 → `reapply-rules` → 探针确认代理恢复
- **期望**：WAN 重拨期间固件清空 iptables → 干净直连（不黑洞）；NAT 恢复后 N99 钩子触发 `reapply-rules` → 代理恢复。merlin 走 `nat-start` snippet 同理。
- **观测点**：探针时间序列、`/tmp/sing-router-nat-start.log`、iptables 快照
- **协助度**：🟡（真实 WAN 重拨需人工物理操作；Claude 准备探针脚本与断言，钩子被触发后接管验证）
- **Docker**：⚠️ 部分（WAN 重拨事件需脚本模拟调用 N99，无法复现固件真实清表时序）

---

### D 组 —— 运行中资源同步 / 更新

核心：后台 sync loop 或 CLI `update` 在运行中拉到新资源时，正常更新走可控窗口，**坏资源必须能退回上一次正常配置，绝不把服务搞挂**。

#### D1 sync 拉到健康的新 sing-box / zoo → restart

- **触发**：后台 sync loop（或 `update --apply`）拉到内容真变化且能通过 check 的新 sing-box / `zoo.raw.json`
- **前置**：代理通，`auto_apply=true`
- **步骤**：
  1. 构造一个有效的新资源（替换 `var/zoo.raw.json` 或 staging sing-box）
  2. 触发 sync（等周期或直接 `POST /api/v1/apply`）
  3. 探针观察走 applier restart 流程
- **期望**：applier 备份 → 落盘 → sha256 闸门通过 → `CheckConfig` 通过 → `Restart` → 新配置代理通。restart 窗口同 W2，需量化。
- **观测点**：`apply.*` 日志、`status`（reloading→running）、探针
- **协助度**：🟢
- **Docker**：✅

#### D2 sync 拉到新 cn.txt → `reload-cn-ipset` 轻量重载

- **触发**：sync 拉到内容真变化的新 `cn.txt`
- **前置**：代理通
- **步骤**：
  1. 构造新 `cn.txt`
  2. 触发 sync / `POST /api/v1/reload-cn-ipset`
  3. 全程高频探针
- **期望**：走 `reload-cn-ipset.sh` 仅重建 `cn` ipset 内容，**不 restart sing-box、不拆 iptables**；**全程无窗口、不中断**，三态始终代理通。
- **观测点**：`ipset list cn`（条目数变化）、`shell.reload_cn_ipset.*` 日志、探针（应全程通）
- **协助度**：🟢
- **Docker**：✅

#### D3 etag 变但 sha256 不变 → no-op

- **触发**：上游给出新 etag 但内容字节完全相同
- **前置**：代理通
- **步骤**：
  1. 制造「新 etag、相同内容」的资源（或重复触发同一已 apply 的资源）
  2. 触发 sync
  3. 探针 + 确认未发生 restart
- **期望**：applier 以最终落盘内容的 sha256 与 `var/apply-state.json` 快照比对，相同则 **no-op**；**不触发任何 restart 窗口**，三态始终代理通。
- **观测点**：`apply` 日志（应为 no-op / unchanged）、`status`（pid 不变）、探针
- **协助度**：🟢
- **Docker**：✅

#### D4 上游不可达 / sync 失败

- **触发**：断网或配置错误的上游地址使 sync 拉取失败
- **前置**：代理通
- **步骤**：使上游不可达 → 触发 sync → 探针 + 看日志
- **期望**：sync 失败**仅记录日志**，不影响 daemon 主流程；sing-box 持续运行，三态始终代理通。
- **观测点**：`sync.item.failed` 日志、`status`（不变）、探针
- **协助度**：🟢
- **Docker**：✅

#### D5 拉到的资源通不过 check → revert，旧配置不中断

- **触发**：sync / `update --apply` 拉到一个内容变化、但 **`CheckConfig` 通不过**的坏 sing-box / `zoo.raw.json`（如损坏的二进制、非法的 zoo 结构）
- **前置**：代理通
- **步骤**：
  1. 构造一个能下载但 `sing-box check` 会失败的坏资源
  2. 触发 apply
  3. 全程高频探针 + 复查落盘文件
- **期望**：applier 备份 → 落盘 → sha256 闸门 → `CheckConfig` **失败** → **revert 全部备份**（zoo/rule-set 从内存字节回写、sing-box bin rename 回原位）→ **不执行 Restart** → 仅 warn。**旧配置 sing-box 全程未被打断，无窗口、不中断**，三态始终代理通。`config.d` / `bin/sing-box` 落盘内容恢复为旧版本。
- **观测点**：`apply.check.failed` + `reverted` 日志、落盘文件 sha256（应回到旧值）、`status`（pid 不变）、探针（应全程通）
- **协助度**：🟢
- **Docker**：✅（构造坏资源即可，无需真上游）

#### D6 资源通过 check 但 Restart 失败 → revert + 用旧 config 拉回

- **触发**：拉到一个能通过 `CheckConfig`、但新 sing-box 实际**起不来 / Restart 失败**的资源（如新二进制 ABI 不兼容、新配置运行期才暴露的问题）
- **前置**：代理通
- **步骤**：
  1. 构造此类资源
  2. 触发 apply
  3. 探针观察 revert + 恢复过程，记录窗口时长
- **期望**：applier 落盘 → check 通过 → `Restart` **失败** → **revert 全部备份** → 走 `RecoverFromFailedApply`（`Fatal/Reloading → Reloading → Running`）用 revert 后的旧 config 把 sing-box 拉回 → 最终回到**代理通**。**不黑洞、不卡死在 `Fatal`**。窗口（失败 restart + revert + recover）需量化。
- **观测点**：`apply` 失败 + `reverted and recovered with previous config` 日志、`status`（最终 running）、落盘文件（回到旧值）、探针时间序列
- **协助度**：🟢
- **Docker**：✅（可用一个「check 通过但 run 立即退出」的假 sing-box 复现）

---

## 5. Claude Code 完成可能性汇总

前提：会话内有可直达路由器的 SSH 通道。

| 协助度 | 用例 | 说明 |
|---|---|---|
| 🟢 全自动 | S1 S2 S3 S5、W1 W2 W3 W4、R1 R2、D1–D6 | Claude 经 SSH 注入故障（`kill` / `kill -HUP` / `kill -9` 子进程 / 构造坏资源 / `iptables -F`）、跑三态探针断言、量化窗口时长，全程无需人工介入 |
| 🟡 需人工配合 | R3 | 真实 WAN 重拨需物理拔插网线或固件操作；Claude 准备探针与断言脚本，钩子被触发后接管验证。端到端验证需真实 LAN 客户端 |
| 🔴 风险操作 | S4 | `kill -9` daemon 后孤儿 sing-box 链最终可能黑洞使 LAN 全断；需带外恢复手段在场 |

**结论**：本方案共 18 个用例（S 组 5 + W 组 4 + R 组 3 + D 组 6）。有 SSH 通道后，其中 16 个 Claude Code 可独立跑完，仅 R3 需人工配合、S4 需带外恢复在场。

## 6. 未来 Docker 虚拟化

**现状**：`tests/docker/` 已模拟 Entware + busybox ash 环境，但用 fake sing-box，缺：真实 iptables / conntrack、真实 TUN 设备、WAN 重拨与 SIGHUP 事件、真实 ready 耗时。

**缺口与扩展路线**：

| 能力 | 现状 | 扩展方案 |
|---|---|---|
| 真实 iptables / 路由表 | ❌ | 容器加 `--privileged` + `--cap-add=NET_ADMIN` |
| 真实 TUN 设备（W3 必需） | ❌ | 挂载 `/dev/net/tun`，用真 sing-box 替代 fake-box |
| 真实 ready 耗时（W2 必需） | ❌ | 用真 sing-box；或可配置延迟的 fake-box |
| LAN / WAN 拓扑与三态探针 | ❌ | 用 netns 模拟 LAN client + WAN，client 侧跑探针 |
| WAN 重拨事件（R3） | ❌ | 脚本模拟：清 iptables + 调用 N99 钩子（无法完全复现固件清表时序） |
| SIGHUP / kill 故障注入 | ✅ 可做 | 容器内直接 `kill` 即可 |

**按用例组的虚拟化价值**：

- **W 组（窗口量化）**：Docker 高价值 —— 可无人值守反复跑、精确测窗口时长，CI 友好。需真 sing-box + TUN。
- **S4（孤儿链）/ S2（反复崩溃）**：Docker 高价值 —— 安全复现破坏性用例，不会真把路由器搞挂。
- **D 组（资源更新 / revert）**：Docker 完全可虚拟化 —— 构造坏资源即可，无需真上游。
- **R3（真实 WAN 重拨）**：Docker 仅部分可虚拟化 —— 事件时序与真机有差异，仍需保留实机验证。

**目标**：Docker 场景的核心价值是「CI 里无人值守跑回归」+「安全跑破坏性 / 风险用例」，而非「让 Claude 够得着设备」（SSH 已解决后者）。

## 7. 三态探针参考实现

探针目标：一次调用判定当前处于 代理通 / 直连通 / 黑洞。

```sh
# probe.sh —— 在 LAN 客户端或路由器本机执行
# 出参：PROXY / DIRECT / BLACKHOLE
probe() {
    # 1. 连通性：能否在 5s 内拿到 generate_204
    code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' \
        http://www.gstatic.com/generate_204 2>/dev/null)
    if [ "$code" != "204" ]; then
        echo BLACKHOLE        # 不通 = 黑洞（前提：直连本身可用）
        return
    fi
    # 2. 区分代理 vs 直连：iptables sing-box 链是否存在
    if iptables -t nat -nL sing-box >/dev/null 2>&1; then
        echo PROXY            # 通 + 规则在 = 代理通
    else
        echo DIRECT           # 通 + 规则不在 = 干净直连
    fi
}
```

窗口量化：故障注入后以 ≤ 0.5s 间隔循环调用 `probe`，记录 `BLACKHOLE` 的连续持续时长，即为该场景的过渡窗口实测值。

> 注：探针需先确认「直连本身可用」（路由器 WAN 正常），否则无法区分黑洞与上游故障。每个用例执行前由前置条件保证。

## 8. 待评审 / 开放问题

- S4：是否引入 daemon 看门狗（init.d `check` + cron / 固件钩子周期校验 daemon pid）以兜底「孤儿 sing-box 崩溃后黑洞」？
- W2：资源更新触发的 restart 窗口最坏可达 30s+，是否需要「restart 期间临时拆 iptables 回退直连」或「新进程 ready 后再切换」？
- W3：`WatchRoutes` 默认 30s 巡检使 HUP 后路由缺失窗口偏大，是否缩短周期或改为事件驱动？

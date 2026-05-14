# 路由器实机不间断服务测试套件

把 `docs/superpowers/specs/2026-05-14-real-device-test-plan-design.md` 落成可执行用例。
核心不变量：**任意场景，LAN 流量只允许「代理通」或「干净直连」，绝不「黑洞」**。

## 前置

- 一台已 **正常运行** sing-router 的华硕路由器（koolshare 固件为主），`ssh 192.168.50.1` 可达。
- 一台 **LAN 侧客户端** `ssh 192.168.50.12` 可达、且其流量经路由器转发 —— 连通性探针从这里发起。客户端需有 `curl`（或支持 https 的 `wget`）。
- 开发机已装 `jq`、`ssh`、`bash`，且与 LAN 客户端同网段（探针 ssh 不经 WAN，路由器黑洞时仍可达）。
- 路由器侧具备 `iptables`、`ip`、`ipset`、`nslookup`、`sha256sum`、`curl`（多为 Entware/busybox 标配；缺 `curl` 时 `opkg install curl` —— `apply_via_api` 需要它 POST 本机 API）。

## 三态判定

探针从 LAN 客户端分别请求两个目标，组合 + iptables 真值得出状态：

| direct（baidu）| proxy（google）| iptables 规则 | 状态 |
|---|---|---|---|
| ✅ | ✅ | — | **PROXY** 代理通 |
| ✅ | ❌ | — | **DIRECT** 干净直连 |
| ❌ | — | 仍在 | **BLACKHOLE** 黑洞（FAIL）|
| ❌ | — | 已拆 | **WANDOWN** WAN 本身断（环境问题）|

## 配置

```sh
cp tests/realdevice/config.example.sh tests/realdevice/config.sh
# 编辑 config.sh：核对 ROUTER_SSH / LAN_CLIENT_SSH 等
```

`config.sh` 已被 `.gitignore` 忽略。建议在 `~/.ssh/config` 给路由器与客户端配好别名与免密。

## 运行

```sh
make realdevice-lint                    # 纯逻辑单测 + 用例语法检查（无需路由器）
make realdevice-test                    # 跑全部 18 个用例（需 config.sh + 可达路由器）
make realdevice-test CASES="S W"        # 只跑 S、W 两组
bash tests/realdevice/run.sh S2 D5      # 跑指定用例
bash tests/realdevice/run.sh --dry-run  # 仅语法检查
```

每个用例退出码：`0`=PASS、`1`=FAIL、`2`=SKIP（前置不满足/能力缺失）。
`run.sh` 末尾打印 `RESULT <ID> <PASS|FAIL|SKIP> <说明>` 汇总。

## 用例分组

- **S 组**（S1–S5）退出/崩溃必须拆净
- **W 组**（W1–W4）过渡窗口黑洞量化
- **R 组**（R1–R3）重装规则不能装半套
- **D 组**（D1–D6）运行中资源同步/更新

## 注意事项

- **S4 / R2** 会让服务短暂中断甚至（S4）可能使 LAN 失联 —— 确保有带外恢复手段（路由器物理可达 / Web 后台）再跑。
- 每个用例自带 `trap ... EXIT` best-effort 把服务恢复到 running；若中途强杀脚本，恢复不保证，需手动 `S99sing-router restart`。
- **R3** 自动化的是钩子链路模拟；真实 WAN 重拨（拔网线）请手动触发后观察 `/tmp/sing-router-nat-start.log` 与探针。
- 窗口时长（W 组的 `≈Nms`）是「最长游程 × 总耗时 / 样本数」的估算，采样粒度 ≈1s + ssh 往返，**用于量级评估而非精确测量**；精确测量见 spec §6 的 Docker 化路线。
- 用例假设「直连本身可用」（路由器 WAN 正常）；`require_running` 在用例开始时确认 PROXY 已通来保证这一点（PROXY = baidu 通 + google 通，baidu 通即说明直连基准可用）。

## 架构

- `lib/probe.sh` —— busybox-ash 双目标探针，经 ssh 推到 LAN 客户端执行：分别请求 baidu（直连基准）与 google（代理验证），输出两个 OK/FAIL token。
- `lib/harness.sh` —— 开发机侧驱动函数库：三态判定、SSH/状态助手、故障注入、资源操作。
- `cases/*.sh` —— 单个用例，声明式调用 harness。
- `run.sh` —— 发现/过滤/汇总。
- 纯逻辑（三态分类、窗口游程、用例选择）由 `lib/*_test.sh` 单测覆盖。

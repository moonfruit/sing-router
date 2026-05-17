# TODO

- [x] sing-box 默认下载地址
- [x] cn.txt 默认下载地址
- [x] zoo.json:
  - [x] 默认出口
- [ ] 其它 config 文件随配置更新
- [ ] 设备白名单/黑名单
  - [ ] 自动化
- [x] stderr.log -> sing-router.err
- [x] router table: 111 -> 7890 -> 7892
- [ ] 清理 daemon.toml 中不再使用的字段
- [ ] 简化重启流程：所有触发路径（supervisor 崩溃重启 / CLI restart / reapply-rules / applier 变更触发等）统一走「拆路由 → 停 sing-box → 启 sing-box → 应用路由规则」完整循环，放弃当前「尽量少动作」的增量策略（含 `IptablesKeepBackoffLtMs` 等保留 iptables 的闸门）
- [ ] 路由守护线程：daemon 内起 watchdog goroutine 周期性巡检 iptables/ip rule/ip route 是否仍是预期状态，被外部（固件 nat 重启、第三方脚本等）拆掉时自动触发上面的完整重启流程
- [ ] 调查 ShellCrash UDP 透明代理处理模式，比较 TPROXY 与 TUN 方案的优缺点（性能、兼容性、CPU 占用、对 IPv6 / fakeip / fwmark 的支持、与现有 sing-router iptables 编排的整合成本），输出选型结论并决定是否切换默认 inbound
- [ ] 调查 ShellCrash 启用 IPv6 且内核 `ip6tables` 不支持 REDIRECT（缺 `ip6t_REDIRECT` 模块）时的兜底处理逻辑：是降级为 TPROXY / TUN、跳过 IPv6 透明代理、还是有别的策略？产出可借鉴的检测 + fallback 方案给 sing-router
- [ ] 调查 IPv6 DNS 用 `ip6tables -t nat -j DNAT` 把 53 重定向到 sing-box tun 接口的 IPv6 + 1053 端口是否可行（作为 `ip6t_REDIRECT` 缺失时的替代方案）；验证 `ip6t_DNAT` 在目标固件内核可用、目标地址用 LLA / GUA / fakeip-v6 各自的可达性与回包路径、与 dns-in 现有监听形态的兼容性
- [ ] 探索在 sing-box 内置 route/dns 规则里加防回环逻辑的可行性：例如按 `inbound in [redirect-in, dns-in, tproxy-in] + 目的端口 ∈ inbound 自身端口集合` 直接 reject，或按 `process_name == sing-box`（若可用）兜底，目标是把目前"ready check 只 dial mixed-in 端口（`readyCheckDialMixedPort=7890`）"这条隐式约束**内化到 zoo 默认规则**，让用户配置出错时也不至于把整机 CPU 打到 100%

## bug fix

- [x] sing-router daemon crash
- [x] ip rule del 执行多次直到失败
- [x] No token docker test Phase E error

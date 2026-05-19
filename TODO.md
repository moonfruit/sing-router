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
- [x] 清理 daemon.toml 中不再使用的字段（随简化重启流程，删除 `iptables_keep_when_backoff_lt_ms`）
- [x] 简化重启流程：所有触发路径统一收敛到 `Supervisor` 的 `Shutdown` / `Startup` / `Restart`（=Shutdown+Startup）三个原子方法，2s 节流闸门；删除 `reapply-rules` / `reload-cn-ipset` CLI/HTTP 端点与对应 shell 脚本；Applier 改为 4 阶段 `Apply(ctx, kinds)`，一轮 sync 最多调 1 次 Restart
- [ ] 路由守护线程：daemon 内起 watchdog goroutine 周期性巡检 iptables/ip rule/ip route 是否仍是预期状态，被外部（固件 nat 重启、第三方脚本等）拆掉时自动触发上面的完整重启流程
- [x] 调查 ShellCrash UDP 透明代理处理模式，比较 TPROXY 与 TUN 方案的优缺点（性能、兼容性、CPU 占用、对 IPv6 / fakeip / fwmark 的支持、与现有 sing-router iptables 编排的整合成本），输出选型结论并决定是否切换默认 inbound
- [x] 调查 ShellCrash 启用 IPv6 且内核 `ip6tables` 不支持 REDIRECT（缺 `ip6t_REDIRECT` 模块）时的兜底处理逻辑：是降级为 TPROXY / TUN、跳过 IPv6 透明代理、还是有别的策略？产出可借鉴的检测 + fallback 方案给 sing-router
- [ ] 调查 IPv6 DNS 用 `ip6tables -t nat -j DNAT` 把 53 重定向到 sing-box tun 接口的 IPv6 + 1053 端口是否可行（作为 `ip6t_REDIRECT` 缺失时的替代方案）；验证 `ip6t_DNAT` 在目标固件内核可用、目标地址用 LLA / GUA / fakeip-v6 各自的可达性与回包路径、与 dns-in 现有监听形态的兼容性
- [ ] 探索在 sing-box 内置 route/dns 规则里加防回环逻辑的可行性：例如按 `inbound in [redirect-in, dns-in, tproxy-in] + 目的端口 ∈ inbound 自身端口集合` 直接 reject，或按 `process_name == sing-box`（若可用）兜底，目标是把目前"ready check 只 dial mixed-in 端口（`readyCheckDialMixedPort=7890`）"这条隐式约束**内化到 zoo 默认规则**，让用户配置出错时也不至于把整机 CPU 打到 100%
- [x] 抓 PS Portal ↔ PS5 实际流量定位 PSN P2P 卡顿原因：场景是 ShellCrash 混合模式（启用 v6 透明代理）下 Portal 远程游玩 PS5 流畅，切到 sing-router（v6 仅透传、不做透明代理、AAAA 不回 fakeip）后卡顿、疑似走 Sony relay；但 MacBook PS Remote Play 同条件直连流畅。代码侧确认 sing-router iptables 不会阻断 LAN-to-LAN（v4 BYPASS 覆盖 RFC1918，v6 仅 INPUT REJECT 53），Portal 已确认拿到 GUA，理论上 v4 LAN 或 v6 GUA 任一条路都应直连。**路由器上 tcpdump 看不到真实流量——同段 LAN-to-LAN 走硬件转发（NSS/CTF）旁路 CPU**。可行方案：在 PS Portal/PS5 与路由器之间串一台支持端口镜像的交换机，把 Portal 端口镜像到抓包机；抓包目标：(1) Portal 卡顿时实际目的 IP 分布（PS5 LAN/GUA vs Sony AS33353/AS27506 relay）；(2) 是否有 ICMPv6 ND 请求 PS5 GUA 但无回应；(3) 端口集合是否包含未在 PROXY_PORTS 内的特殊端口。结果决定下一步是 WiFi 链路问题、ICE 协商问题，还是 sing-router 需要为 PSN 特定流量加规则

## bug fix

- [x] sing-router daemon crash
- [x] ip rule del 执行多次直到失败
- [x] No token docker test Phase E error
- [ ] sing-router logs -F 切换文件时退出问题

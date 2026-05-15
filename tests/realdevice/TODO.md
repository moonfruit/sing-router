# 实机测试套件 —— 待改进项

对照 `docs/superpowers/specs/2026-05-14-real-device-test-plan-design.md` 的覆盖率审查（2026-05-15）。
spec 的 18 个用例在 ID 级别已全部落地（另加 spec 外的 S6 ip rule 幂等回归），
以下是与 spec「执行步骤 / 期望 / 观测点」对照后仍存在的缺口，暂不修改，留待日后改进。

## A. 整组未覆盖的场景

- [ ] **merlin 固件全链路** —— 套件 koolshare-only。R3 在非 koolshare 上直接 SKIP，
      spec 多处提到的 merlin `nat-start` / `services-start` nvram snippet 钩子链无等价用例。
- [ ] **真实 WAN 物理重拨** —— R3 只自动化「flush nat + 调 N99 钩子」的模拟；
      物理拔插网线 + 固件真实清表时序未覆盖（spec 本就标 🟡，属已知）。
- [ ] **后台 sync loop 周期触发本身** —— D 组所有用例都直接 `POST /api/v1/apply` 或 CLI
      `update`，周期 loop 的 wiring（`interval_seconds` / `on_start_delay_sec`）从未被实机跑到。
      spec D1/D4 的触发方式明确包含「后台 sync loop」。

## B. 用例内部覆盖弱于 spec 意图

- [ ] **teardown 完整性只验 3 项** —— `assert_rules_absent` 只查 nat `sing-box` 链 + 策略路由
      default + `cn` ipset。spec S1 明确列出的 `sing-box-dns` 链、mangle `sing-box-mark` 链、
      `FORWARD -o $TUN` 规则都没验（`ip rule fwmark` 仅靠 S6 单独覆盖）。残留这几类规则的
      黑洞不会被 S1/S2/S3 抓到 —— 对核心不变量的实质漏检。建议扩 `assert_rules_absent`。
- [ ] **日志断言全面缺失** —— spec 把 `apply.check.failed` / `reverted` /
      `reverted and recovered with previous config` / `sync.item.failed` /
      `shell.reapply_routes.exec` / 退避日志 / state 流转都列为观测点；实现只查终态
      （pid / probe / sha256 / rules）。后果是「对的终态、错的路径」也会 PASS：典型 D5
      （apply 静默没做事 vs 真 revert 分不出来）、S2（≥10s 退避档真的调了 `TeardownHook`
      还是只在 fatal 才拆，分不出来）。建议加 `assert_log_contains` 助手。
- [ ] **D2 没验 cn ipset 内容真变化** —— spec 观测点要求 `ipset list cn` 条目数变化，
      实现只验「没重启 + 没黑洞」，不验 `reload-cn-ipset` 是否真生效。
- [ ] **过渡窗口只 note 不 assert** —— W1/W2/W3/W4/D6 量化了 `≈Nms` 但无上界断言
      （README 说明是「量级评估」），意味着窗口回归（如 W2 从 5s 退化到 40s）不会 FAIL。

## C. 小问题

- [ ] **W4** 没验 spec 要求的「跑了 `reapply-routes.sh` 补 device-bound 路由、且没重跑
      `startup.sh`」，只测了窗口 + PROXY。
- [ ] **R1** 中间态 `mid` 只 `note` 不 `assert`（R3 同位置有 `[ "$mid" = BLACKHOLE ] && fail`，
      两者不一致）。

---

优先级建议：先补 **B 的 teardown 完整性** 与 **日志断言**——前者关系核心不变量的检出能力，
后者决定用例能否区分「路径正确」还是「碰巧终态对」。

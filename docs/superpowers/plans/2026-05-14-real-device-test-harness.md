# 路由器实机不间断服务测试套件 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `tests/realdevice/` 下建一套可重复运行的实机测试套件，把 `docs/superpowers/specs/2026-05-14-real-device-test-plan-design.md` 里的 18 个用例落成可执行脚本，每个用例自动注入故障、用三态探针断言「代理通 / 干净直连 / 黑洞」，并量化过渡窗口。

**Architecture:** 纯 shell。**驱动层**（`run.sh` + `lib/harness.sh`）跑在开发机（darwin/bash），通过 `ssh 192.168.50.1` 操控路由器；**探针**（`lib/probe.sh`）是 busybox-ash 兼容脚本，经 `ssh 192.168.50.12 sh -s` 推到 LAN 客户端执行，每次探测分别请求 `https://www.baidu.com`（直连基准，任意时刻都应通 —— 不通即黑洞信号）与 `https://www.google.com`（仅 sing-box 代理正常工作时可通）。每个用例是 `cases/<ID>_<name>.sh`，source `config.sh` + `harness.sh`，以退出码 0/1/2 = PASS/FAIL/SKIP 收尾并打印 `RESULT` 行。`run.sh` 发现、过滤、汇总。纯逻辑函数（三态分类、最长连续窗口、用例选择匹配）用 TDD；SSH/路由器交互的胶水代码用 `bash -n` + `--dry-run` + 真机运行验证。

**Tech Stack:** bash（驱动，darwin）、POSIX/busybox sh（探针，路由器侧）、`ssh`（含 ControlMaster 多路复用）、`jq`（开发机侧解析 status JSON）、`curl`/`wget`（探针）、`shellcheck`（可选 lint）。

**关键运行时事实（已核对源码，用例据此构造触发）：**
- `POST /api/v1/apply` → `Applier.ApplyPending`：检测 `bin/sing-box.new` 是否存在作为 bin staging，然后 `ApplySingBoxOrZoo(zooChanged=true, ...)`（内部 sha256 闸门挡无变化），再 `ApplyCNList`。
- `ApplySingBoxOrZoo` 流程：备份 → 提交 staging + `PreprocessZoo` → **sha256 闸门**（产物全无变化 → `apply.noop`，不重启）→ `CheckConfig`（失败 → revert + 返回 nil，不重启）→ `Restart`（失败 → revert + `Recover` 用旧配置拉回 + 返回 err）。
- 因此：改 `var/zoo.raw.json` 后 `POST /apply` 会触发重启；不改任何东西 `POST /apply` 必为 no-op；坏配置（preprocess 过但 check 不过）走 revert 且**不重启**；check 过但 run 失败走 revert + recover。
- `/api/v1/reapply-rules`、`/api/v1/reload-cn-ipset` 在 `state != running` 时返回 **409**；daemon 不在时 CLI 得到连接拒绝 —— 这是 R2「不装半套规则」的机制保证。
- `apply` 端点未接线（无 token / `auto_apply` 关）时返回 **501** —— 相关用例需据此 SKIP。
- `status --json` 结构：`.daemon.{state,pid,rundir}`、`.sing_box.{pid,restart_count}`、`.rules.iptables_installed`、`.firmware`。
- supervisor 退避 ladder 默认 `[1s,2s,4s,8s,16s,32s,64s,128s,256s,512s,600s]`；`IptablesKeepBackoffLtMs` 默认 10000（退避 <10s 保留 iptables，≥10s 拆）。

---

## File Structure

```
tests/realdevice/
  README.md                  # 配置与使用说明
  run.sh                     # 用例发现 / 过滤 / 汇总；--dry-run 仅语法检查
  config.example.sh          # 配置模板（用户复制为 config.sh，gitignored）
  lib/
    probe.sh                 # 双目标连通性探针（busybox-ash，LAN 客户端侧执行）
    probe_test.sh            # probe.sh 单测（stub curl）
    harness.sh               # 驱动层函数库（开发机侧，sourced by cases & run.sh）
    harness_test.sh          # harness.sh 纯逻辑单测（classify / max_contiguous / case_matches）
  cases/
    S1_stop_teardown.sh          S2_crashloop_fatal.sh       S3_daemon_sigterm.sh
    S4_daemon_kill9_orphan.sh    S5_singbox_kill9.sh
    W1_backoff_keep_window.sh    W2_apply_restart_window.sh  W3_sighup_route_window.sh
    W4_supervisor_restart_window.sh
    R1_reapply_healthy.sh        R2_reapply_unhealthy.sh     R3_koolshare_nat_start.sh
    D1_apply_valid_restart.sh    D2_cn_reload_uninterrupted.sh  D3_apply_noop_gate.sh
    D4_upstream_unreachable.sh   D5_bad_check_revert.sh      D6_run_fail_revert_recover.sh
```
Modify: `Makefile`（加 `realdevice-lint` / `realdevice-test` 目标）、`.gitignore`（忽略 `config.sh`）。

**分层职责：** `probe.sh` 只判双目标连通性（baidu/google → 两个 OK/FAIL token），不碰路由器状态；`harness.sh` 组合双目标连通性 + iptables 真值得出三态，并提供所有 SSH/状态/故障注入/资源操作函数；`cases/*.sh` 只声明式调用 harness 函数；`run.sh` 只做编排。纯逻辑（`classify`/`max_contiguous`/`case_matches`）独立可测。

---

## Task 1: 脚手架 + 三态探针 probe.sh（TDD）

**Files:**
- Create: `tests/realdevice/lib/probe.sh`
- Test: `tests/realdevice/lib/probe_test.sh`

- [ ] **Step 1: 写失败测试 `lib/probe_test.sh`**

```bash
#!/usr/bin/env bash
# tests/realdevice/lib/probe_test.sh — unit test for probe.sh.
# probe.sh 探两个 URL，输出 "<direct> <proxy>"（各 OK|FAIL）。
# 这里 stub curl：由 $OKURLS 列出哪些 URL 视为可达，验证两 token 的组合。
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
assert_eq() { # <got> <want> <desc>
    if [ "$1" = "$2" ]; then pass=$((pass + 1))
    else fail=$((fail + 1)); echo "FAIL: [$3] want=[$2] got=[$1]"; fi
}

# stub curl：在参数里找 http(s) URL，命中 $OKURLS 则 exit 0，否则 exit 1
cat > "$TMP/curl" <<'STUB'
#!/bin/sh
url=""
for a in "$@"; do case "$a" in http://*|https://*) url="$a" ;; esac; done
case " $OKURLS " in *" $url "*) exit 0 ;; *) exit 1 ;; esac
STUB
chmod +x "$TMP/curl"

run() { PATH="$TMP:$PATH" OKURLS="$1" sh "$HERE/probe.sh" 2; }

assert_eq "$(run 'https://www.baidu.com https://www.google.com')" "OK OK"     "两者都通 → OK OK（代理通）"
assert_eq "$(run 'https://www.baidu.com')"                        "OK FAIL"   "仅 baidu → OK FAIL（干净直连）"
assert_eq "$(run '')"                                             "FAIL FAIL" "都不通 → FAIL FAIL（黑洞/断网）"

echo "probe_test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
```

- [ ] **Step 2: 跑测试确认失败**

Run: `mkdir -p tests/realdevice/lib && bash tests/realdevice/lib/probe_test.sh`
Expected: FAIL —— `sh: .../probe.sh: No such file or directory`，退出码非 0

- [ ] **Step 3: 写 `lib/probe.sh`**

```bash
#!/bin/sh
# tests/realdevice/lib/probe.sh
# 双目标连通性探针。busybox-ash 兼容；在 LAN 客户端上执行（流量经路由器转发）。
# 输出恰好一行两个 token：<direct> <proxy>，各为 OK 或 FAIL。
#   - direct（默认 https://www.baidu.com）：任意时刻都应可达 —— FAIL 即「黑洞/断网」信号
#   - proxy （默认 https://www.google.com）：仅当 sing-box 代理正常工作时可达
# 用法：sh probe.sh [curl_timeout_seconds]   默认 5
# URL 可经环境变量 PROBE_DIRECT_URL / PROBE_PROXY_URL 覆盖（主要供单测用）。
set -u
TIMEOUT="${1:-5}"
DIRECT_URL="${PROBE_DIRECT_URL:-https://www.baidu.com}"
PROXY_URL="${PROBE_PROXY_URL:-https://www.google.com}"

_check() {  # <url> -> OK|FAIL（拿到任意 HTTP 响应即 OK，连接失败/超时即 FAIL）
    if command -v curl >/dev/null 2>&1; then
        if curl -sS -m "$TIMEOUT" -o /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    else
        # busybox wget 兜底（需支持 https）
        if wget -q -T "$TIMEOUT" -O /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    fi
}

echo "$(_check "$DIRECT_URL") $(_check "$PROXY_URL")"
```

- [ ] **Step 4: 跑测试确认通过**

Run: `bash tests/realdevice/lib/probe_test.sh`
Expected: PASS —— `probe_test: 3 passed, 0 failed`，退出码 0

- [ ] **Step 5: 提交**

```bash
git add tests/realdevice/lib/probe.sh tests/realdevice/lib/probe_test.sh
git commit -m "test(realdevice): 新增三态连通性探针 probe.sh + 单测"
```

---

## Task 2: harness.sh 纯逻辑（TDD）

**Files:**
- Create: `tests/realdevice/lib/harness.sh`
- Test: `tests/realdevice/lib/harness_test.sh`

纯函数：`classify`（连通性 + iptables 真值 → 三态）、`max_contiguous`（采样序列里某 token 的最长连续游程）、`max_contiguous_blackhole`（薄包装）、`case_matches`（用例选择器匹配，支持组前缀）。

- [ ] **Step 1: 写失败测试 `lib/harness_test.sh`**

```bash
#!/usr/bin/env bash
# tests/realdevice/lib/harness_test.sh — harness.sh 纯逻辑单测。
# 只测不碰 SSH/路由器的纯函数；source harness.sh 不应有副作用。
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/harness.sh"
pass=0; fail=0
assert_eq() { # <got> <want> <desc>
    if [ "$1" = "$2" ]; then pass=$((pass + 1))
    else fail=$((fail + 1)); echo "FAIL: [$3] want=[$2] got=[$1]"; fi
}

# classify <direct> <proxy> <rules_rc>
assert_eq "$(classify OK OK 0)"     "PROXY"     "direct+proxy 都通 → PROXY"
assert_eq "$(classify OK OK 1)"     "PROXY"     "proxy 通即 PROXY（忽略 rules_rc）"
assert_eq "$(classify OK FAIL 1)"   "DIRECT"    "仅 direct 通 + 无规则 → DIRECT"
assert_eq "$(classify OK FAIL 0)"   "DIRECT"    "仅 direct 通 + 有规则 → DIRECT（降级但非黑洞）"
assert_eq "$(classify FAIL FAIL 0)" "BLACKHOLE" "direct 不通 + 有规则 → BLACKHOLE"
assert_eq "$(classify FAIL FAIL 1)" "WANDOWN"   "direct 不通 + 无规则 → WANDOWN"
assert_eq "$(classify FAIL OK 0)"   "BLACKHOLE" "direct 不通即异常 + 有规则 → BLACKHOLE"

# max_contiguous <token> <samples...>
assert_eq "$(max_contiguous BLACKHOLE PROXY BLACKHOLE BLACKHOLE PROXY)" "2" "longest run = 2"
assert_eq "$(max_contiguous BLACKHOLE PROXY PROXY)"                     "0" "no run = 0"
assert_eq "$(max_contiguous BLACKHOLE BLACKHOLE BLACKHOLE BLACKHOLE)"   "4" "all run = 4"
assert_eq "$(max_contiguous GONE PRESENT GONE PRESENT GONE GONE GONE)"  "3" "token param respected"
assert_eq "$(max_contiguous_blackhole PROXY BLACKHOLE PROXY)"           "1" "wrapper works"

# case_matches <id> <selector...>
case_matches S2 && r=0 || r=1;            assert_eq "$r" "0" "no selectors → match all"
case_matches S2 S && r=0 || r=1;          assert_eq "$r" "0" "group prefix S matches S2"
case_matches S2 S2 && r=0 || r=1;         assert_eq "$r" "0" "exact id matches"
case_matches S2 W D && r=0 || r=1;        assert_eq "$r" "1" "non-matching selectors → no match"
case_matches W3 W4 W3 && r=0 || r=1;      assert_eq "$r" "0" "matches any in list"

echo "harness_test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
```

- [ ] **Step 2: 跑测试确认失败**

Run: `bash tests/realdevice/lib/harness_test.sh`
Expected: FAIL —— `harness.sh: No such file or directory`（共 17 个断言，文件不存在时全部失败）

- [ ] **Step 3: 写 `lib/harness.sh`（纯逻辑部分）**

```bash
#!/usr/bin/env bash
# tests/realdevice/lib/harness.sh
# 实机测试套件的驱动层函数库。在开发机上 source（由 cases/*.sh 与 run.sh）。
# 调用方负责先 source config.sh、并自行 `set -u`。本文件 source 时无副作用。
#
# 分三段：
#   1. 纯逻辑（classify / max_contiguous / case_matches）—— 有单测
#   2. SSH / 状态 / 探针 / 断言 I/O 助手
#   3. 故障注入与资源操作助手

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================
# 1. 纯逻辑（harness_test.sh 覆盖）
# ============================================================

# classify <direct:OK|FAIL> <proxy:OK|FAIL> <rules_rc:0|1> → PROXY|DIRECT|BLACKHOLE|WANDOWN
# 三态判定核心：
#   direct   = 能否访问 https://www.baidu.com（任意时刻都应可达）
#   proxy    = 能否访问 https://www.google.com（仅 sing-box 代理正常时可达）
#   rules_rc = 0 表示 iptables 'sing-box' 链存在
# direct 通 → 看 proxy 区分 PROXY / DIRECT；
# direct 不通 → 看 rules 区分 BLACKHOLE（规则仍在=黑洞）/ WANDOWN（无规则=WAN 本身断）。
classify() {
    if [ "$1" = OK ]; then
        if [ "$2" = OK ]; then echo PROXY; else echo DIRECT; fi
    else
        if [ "$3" -eq 0 ]; then echo BLACKHOLE; else echo WANDOWN; fi
    fi
}

# max_contiguous <token> <sample>... → 该 token 的最长连续游程长度
max_contiguous() {
    local tok="$1"; shift
    local max=0 cur=0 s
    for s in "$@"; do
        if [ "$s" = "$tok" ]; then
            cur=$((cur + 1))
            [ "$cur" -gt "$max" ] && max=$cur
        else
            cur=0
        fi
    done
    echo "$max"
}

# max_contiguous_blackhole <sample>... → BLACKHOLE 最长游程
max_contiguous_blackhole() { max_contiguous BLACKHOLE "$@"; }

# case_matches <case_id> <selector>... → rc 0 若匹配（无 selector 时全匹配）
# selector 可为完整 ID（S2）或组前缀（S）。
case_matches() {
    local id="$1"; shift
    [ "$#" -eq 0 ] && return 0
    local s
    for s in "$@"; do
        [ "$id" = "$s" ] && return 0
        case "$id" in "$s"*) return 0 ;; esac
    done
    return 1
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `bash tests/realdevice/lib/harness_test.sh`
Expected: PASS —— `harness_test: 17 passed, 0 failed`

- [ ] **Step 5: 提交**

```bash
git add tests/realdevice/lib/harness.sh tests/realdevice/lib/harness_test.sh
git commit -m "test(realdevice): 新增 harness 纯逻辑(三态分类/窗口/选择器)+单测"
```

---

## Task 3: harness.sh —— SSH / 状态 / 探针 / 断言 I/O 助手

**Files:**
- Modify: `tests/realdevice/lib/harness.sh`（追加第 2 段）

这一段是路由器交互胶水，依赖真机，无法单测；验证靠 `bash -n` + 后续 `--dry-run` + 真机运行。

- [ ] **Step 1: 在 `lib/harness.sh` 末尾追加 I/O 助手**

把以下整段追加到 `tests/realdevice/lib/harness.sh` 文件末尾：

```bash

# ============================================================
# 2. SSH / 状态 / 探针 / 断言 I/O 助手
#    依赖 config.sh 提供：ROUTER_SSH LAN_CLIENT_SSH SINGROUTER INITD
#                         RUNDIR CONFIG_DIR ROUTE_TABLE
# ============================================================

# 共用 ssh 选项：BatchMode 禁交互、ControlMaster 多路复用降低每次往返延迟。
_RD_SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=8
    -o ControlMaster=auto -o ControlPath="/tmp/.rd_ssh_%r@%h:%p" -o ControlPersist=60)

# rsh <cmd...> : 在路由器上执行命令
rsh() { ssh "${_RD_SSH_OPTS[@]}" "$ROUTER_SSH" "$@"; }

# probe_conn [timeout_s] → "<direct> <proxy>" : 在 LAN 客户端上跑 probe.sh（双目标）
probe_conn() {
    local to="${1:-5}"
    ssh "${_RD_SSH_OPTS[@]}" "$LAN_CLIENT_SSH" sh -s -- "$to" < "$HARNESS_DIR/probe.sh"
}

# rules_present : rc 0 若 nat 表 'sing-box' 链存在（iptables 真值）
rules_present() { rsh "iptables -t nat -nL sing-box >/dev/null 2>&1"; }

# probe [timeout_s] → PROXY|DIRECT|BLACKHOLE|WANDOWN|INCONCL : 完整三态判定
probe() {
    local to="${1:-5}" line direct proxy rc
    line="$(probe_conn "$to")"
    [ -n "$line" ] || { echo INCONCL; return; }   # ssh 到 LAN 客户端失败
    direct="${line%% *}"
    proxy="${line##* }"
    if rules_present; then rc=0; else rc=1; fi
    classify "$direct" "$proxy" "$rc"
}

# status_json → daemon status JSON（daemon 不在时为空）
status_json() { rsh "$SINGROUTER status --json" 2>/dev/null; }

# daemon_state → running|degraded|fatal|stopping|booting|reloading|offline
daemon_state() { status_json | jq -r '.daemon.state // "offline"' 2>/dev/null || echo offline; }

# singbox_pid / daemon_pid → 纯数字 pid 或空串
singbox_pid() { status_json | jq -r '(.sing_box.pid // empty)|tostring' 2>/dev/null | grep -E '^[0-9]+$' || true; }
daemon_pid()  { status_json | jq -r '(.daemon.pid  // empty)|tostring' 2>/dev/null | grep -E '^[0-9]+$' || true; }

# wait_state <state> <timeout_s> : 轮询直到 daemon_state 命中，rc 0 成功
wait_state() {
    local want="$1" timeout="$2" start now
    start="$(date +%s)"
    while :; do
        [ "$(daemon_state)" = "$want" ] && return 0
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        sleep 2
    done
}

# wait_singbox_restart <old_pid> <timeout_s> : 轮询直到出现新 pid 且 state=running
wait_singbox_restart() {
    local old="$1" timeout="$2" start now np
    start="$(date +%s)"
    while :; do
        np="$(singbox_pid)"
        if [ -n "$np" ] && [ "$np" != "$old" ] && [ "$(daemon_state)" = running ]; then
            return 0
        fi
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        sleep 2
    done
}

# route_table_has_default : rc 0 若策略路由表里有 default 路由
route_table_has_default() { rsh "ip route show table $ROUTE_TABLE 2>/dev/null | grep -q '^default'"; }

# ipset_cn_exists : rc 0 若 'cn' ipset 存在
ipset_cn_exists() { rsh "ipset list cn >/dev/null 2>&1"; }

# assert_rules_absent : rc 0 当 iptables 链 / 策略路由 / ipset 全部已拆净
assert_rules_absent() {
    local bad=0
    if rules_present;            then note "✗ nat 'sing-box' 链仍存在"; bad=1; fi
    if route_table_has_default;  then note "✗ 路由表 $ROUTE_TABLE 仍有 default"; bad=1; fi
    if ipset_cn_exists;          then note "✗ 'cn' ipset 仍存在"; bad=1; fi
    return $bad
}

# assert_rules_present : rc 0 当 nat 'sing-box' 链存在
assert_rules_present() {
    rules_present || { note "✗ nat 'sing-box' 链缺失"; return 1; }
    return 0
}

# measure_blackhole_ms <timeout_s> [probe_timeout_s] → 估算最长连续 BLACKHOLE 毫秒数
# 在 timeout_s 内尽快连续 probe（每次 probe 自带 probe_timeout_s 网络等待），
# 用「最长游程 × 总耗时 / 样本数」估算窗口时长。采样粒度 ≈ probe_timeout + ssh 往返。
measure_blackhole_ms() {
    local timeout="$1" pto="${2:-1}" start now samples="" total runs elapsed
    start="$(date +%s)"
    while :; do
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && break
        samples="$samples $(probe "$pto")"
    done
    elapsed=$(( $(date +%s) - start ))
    total="$(echo "$samples" | wc -w | tr -d ' ')"
    [ "$total" -gt 0 ] || { echo 0; return; }
    runs="$(max_contiguous_blackhole $samples)"
    echo $(( runs * elapsed * 1000 / total ))
}

# measure_route_gone_ms <timeout_s> [interval_s] → 估算最长连续「default 路由缺失」毫秒数
measure_route_gone_ms() {
    local timeout="$1" iv="${2:-1}" start now samples="" total runs elapsed tok
    start="$(date +%s)"
    while :; do
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && break
        if route_table_has_default; then tok=PRESENT; else tok=GONE; fi
        samples="$samples $tok"
        sleep "$iv"
    done
    elapsed=$(( $(date +%s) - start ))
    total="$(echo "$samples" | wc -w | tr -d ' ')"
    [ "$total" -gt 0 ] || { echo 0; return; }
    runs="$(max_contiguous GONE $samples)"
    echo $(( runs * elapsed * 1000 / total ))
}

# ---- 用例结果助手（每个都 exit）----
CASE_ID="${CASE_ID:-?}"
note() { echo "  $*"; }
pass() { echo "RESULT $CASE_ID PASS ${1:-}"; exit 0; }
fail() { echo "RESULT $CASE_ID FAIL ${1:-}"; exit 1; }
skip() { echo "RESULT $CASE_ID SKIP ${1:-}"; exit 2; }

# require_running : 用例前置闸门 —— daemon 必须 running 且探针 PROXY，否则 SKIP
require_running() {
    local st p
    st="$(daemon_state)"
    [ "$st" = running ] || skip "前置不满足：daemon state=$st（需 running）"
    p="$(probe)"
    [ "$p" = PROXY ] || skip "前置不满足：probe=$p（需 PROXY）"
}

# restore_to_running : 用例结束 best-effort 把服务恢复到 running（trap EXIT 用）
restore_to_running() {
    local st
    st="$(daemon_state)"
    case "$st" in
        running) return 0 ;;
        offline) rsh "$INITD restart >/dev/null 2>&1 || $INITD start >/dev/null 2>&1 || true" ;;
        *)       rsh "$SINGROUTER restart >/dev/null 2>&1 || $INITD restart >/dev/null 2>&1 || true" ;;
    esac
    wait_state running 120 || note "restore_to_running: 120s 后仍非 running —— 需人工检查"
}
```

- [ ] **Step 2: 语法检查**

Run: `bash -n tests/realdevice/lib/harness.sh && echo OK`
Expected: `OK`

- [ ] **Step 3: 确认纯逻辑单测仍通过（追加内容不应破坏 source）**

Run: `bash tests/realdevice/lib/harness_test.sh`
Expected: PASS —— `harness_test: 17 passed, 0 failed`

- [ ] **Step 4: 提交**

```bash
git add tests/realdevice/lib/harness.sh
git commit -m "test(realdevice): harness 增加 SSH/状态/探针/断言 I/O 助手"
```

---

## Task 4: harness.sh —— 故障注入与资源操作助手

**Files:**
- Modify: `tests/realdevice/lib/harness.sh`（追加第 3 段）

- [ ] **Step 1: 在 `lib/harness.sh` 末尾追加故障注入与资源助手**

把以下整段追加到 `tests/realdevice/lib/harness.sh` 文件末尾：

```bash

# ============================================================
# 3. 故障注入与资源操作助手
# ============================================================

# crashloop_to_fatal <timeout_s> : 反复 kill -9 sing-box 子进程直到 StateFatal。rc 0 成功
crashloop_to_fatal() {
    local timeout="$1" start now pid
    start="$(date +%s)"
    while :; do
        [ "$(daemon_state)" = fatal ] && return 0
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        pid="$(singbox_pid)"
        [ -n "$pid" ] && rsh "kill -9 $pid 2>/dev/null || true"
        sleep 3
    done
}

# stage_modified_zoo : 备份 var/zoo.raw.json，追加一个无害的 direct outbound
#   → 触发 applier 真正重启（合法、能过 check、易回滚）。rc 1 若 zoo.raw.json 不存在。
#   jq 在开发机侧执行，路由器只需 cat。
stage_modified_zoo() {
    local raw="$RUNDIR/var/zoo.raw.json" nonce
    rsh "test -f $raw" || return 1
    rsh "cp $raw $raw.testbak"
    nonce="$(date +%s)"
    rsh "cat $raw.testbak" \
        | jq ".outbounds += [{\"type\":\"direct\",\"tag\":\"_rdtest_${nonce}\"}]" \
        | rsh "cat > $raw"
}

# stage_bad_check_zoo : 备份并写入一个 preprocess 能过、sing-box check 必失败的 zoo
#   （selector 引用不存在的 outbound tag）。rc 1 若 zoo.raw.json 不存在。
stage_bad_check_zoo() {
    local raw="$RUNDIR/var/zoo.raw.json"
    rsh "test -f $raw" || return 1
    rsh "cp $raw $raw.testbak"
    rsh "cat $raw.testbak" \
        | jq '.outbounds += [{"type":"selector","tag":"_rdtest_bad","outbounds":["does-not-exist-tag"]}]' \
        | rsh "cat > $raw"
}

# restore_zoo : 从 .testbak 还原 var/zoo.raw.json（trap EXIT 用）
restore_zoo() {
    rsh "test -f $RUNDIR/var/zoo.raw.json.testbak && mv -f $RUNDIR/var/zoo.raw.json.testbak $RUNDIR/var/zoo.raw.json || true"
}

# stage_checkok_runfail_box : 在 bin/sing-box.new 放一个假 sing-box
#   —— `check`/`version` 退出 0，`run` 立即退出 1。用于 D6 的 restart 失败 → revert+recover。
stage_checkok_runfail_box() {
    local stg="$RUNDIR/bin/sing-box.new"
    rsh "cat > $stg" <<'FAKE'
#!/bin/sh
# real-device test fake sing-box: check/version succeed, run fails immediately.
case "$1" in
    check|version) exit 0 ;;
    run) echo "fake sing-box: simulated run failure" >&2; exit 1 ;;
    *) exit 0 ;;
esac
FAKE
    rsh "chmod +x $stg"
}

# restore_box : 清掉残留的 bin/sing-box.new（trap EXIT 用）
restore_box() { rsh "rm -f $RUNDIR/bin/sing-box.new"; }

# modify_cn_txt : 备份 var/cn.txt 并追加一条无害 TEST-NET CIDR。rc 1 若不存在。
modify_cn_txt() {
    local cn="$RUNDIR/var/cn.txt"
    rsh "test -f $cn" || return 1
    rsh "cp $cn $cn.testbak && echo '198.51.100.0/24' >> $cn"
}

# restore_cn_txt : 从 .testbak 还原 var/cn.txt（trap EXIT 用）
restore_cn_txt() {
    rsh "test -f $RUNDIR/var/cn.txt.testbak && mv -f $RUNDIR/var/cn.txt.testbak $RUNDIR/var/cn.txt || true"
}

# apply_via_api → 打印 POST /api/v1/apply 的 HTTP code（200/500/501...）
apply_via_api() {
    rsh "curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:9998/api/v1/apply"
}

# block_host <hostname> : 解析 host 的 IP 并在路由器 OUTPUT 链 REJECT（仅影响路由器自身出站）
#   解析结果记到 /tmp/.rd_blocked_<host> 供 unblock 用。rc 1 若解析不到。
block_host() {
    local host="$1" ips ip
    ips="$(rsh "nslookup $host 2>/dev/null | awk '/^Name:/{f=1;next} f&&/Address/{print \$NF}'")"
    [ -n "$ips" ] || return 1
    for ip in $ips; do
        rsh "iptables -I OUTPUT -d $ip -j REJECT 2>/dev/null || true"
    done
    printf '%s\n' "$ips" | rsh "cat > /tmp/.rd_blocked_${host}"
}

# unblock_host <hostname> : 撤销 block_host 装的 REJECT 规则（trap EXIT 用）
unblock_host() {
    local host="$1" ips ip
    ips="$(rsh "cat /tmp/.rd_blocked_${host} 2>/dev/null")"
    for ip in $ips; do
        rsh "iptables -D OUTPUT -d $ip -j REJECT 2>/dev/null || true"
    done
    rsh "rm -f /tmp/.rd_blocked_${host}"
}
```

- [ ] **Step 2: 语法检查**

Run: `bash -n tests/realdevice/lib/harness.sh && echo OK`
Expected: `OK`

- [ ] **Step 3: 确认纯逻辑单测仍通过**

Run: `bash tests/realdevice/lib/harness_test.sh`
Expected: PASS —— `harness_test: 17 passed, 0 failed`

- [ ] **Step 4: 提交**

```bash
git add tests/realdevice/lib/harness.sh
git commit -m "test(realdevice): harness 增加故障注入与资源操作助手"
```

---

## Task 5: 配置模板 + 运行器 run.sh + Makefile/.gitignore

**Files:**
- Create: `tests/realdevice/config.example.sh`
- Create: `tests/realdevice/run.sh`
- Modify: `Makefile`
- Modify: `.gitignore`

- [ ] **Step 1: 写 `config.example.sh`**

```bash
# tests/realdevice/config.example.sh
# 复制为 config.sh 后按实机核对。config.sh 已被 .gitignore 忽略 —— 切勿提交凭证。
ROUTER_SSH="192.168.50.1"              # 路由器 ssh 目标（host 或 ssh_config 别名）
LAN_CLIENT_SSH="192.168.50.12"         # 必填：连通性探针从这台 LAN 客户端发起
SINGROUTER="sing-router"               # 路由器上的 sing-router 二进制（PATH 名或绝对路径）
INITD="/opt/etc/init.d/S99sing-router" # Entware init.d 脚本（恢复 daemon 用）
RUNDIR="/opt/home/sing-router"         # daemon 运行根目录
CONFIG_DIR="config.d"                  # 配置目录，相对 RUNDIR
ROUTE_TABLE="7892"                     # sing-router 使用的策略路由表 id
```

- [ ] **Step 2: 写 `run.sh`**

```bash
#!/usr/bin/env bash
# tests/realdevice/run.sh — 发现、过滤、运行实机测试用例并汇总。
# 用法：
#   ./run.sh                  跑全部用例
#   ./run.sh S W              跑 S 组与 W 组
#   ./run.sh S2 D5            跑指定用例
#   ./run.sh --dry-run        仅对所有用例脚本做语法检查（无需路由器/config.sh）
set -u
ROOT="$(cd "$(dirname "$0")" && pwd)"
. "$ROOT/lib/harness.sh"
CASES_DIR="$ROOT/cases"

DRY=0
SELECT=()
for a in "$@"; do
    case "$a" in
        --dry-run) DRY=1 ;;
        -h|--help) echo "usage: run.sh [--dry-run] [GROUP|CASE_ID ...]"; exit 0 ;;
        *) SELECT+=("$a") ;;
    esac
done

if [ "$DRY" -eq 1 ]; then
    rc=0
    for f in "$CASES_DIR"/*.sh; do
        [ -e "$f" ] || continue   # cases/ 为空时 glob 不展开，跳过
        if bash -n "$f"; then echo "ok     $(basename "$f")"
        else echo "BADSYN $(basename "$f")"; rc=1; fi
    done
    exit $rc
fi

[ -f "$ROOT/config.sh" ] || {
    echo "ERROR: 缺少 $ROOT/config.sh —— 复制 config.example.sh 并按实机修改"
    exit 1
}

total=0; passed=0; failed=0; skipped=0
SUMMARY=()
for f in $(ls "$CASES_DIR"/*.sh 2>/dev/null | sort); do
    id="$(basename "$f" .sh | cut -d_ -f1)"
    # bash 3.2（macOS 默认）下空数组 + set -u 会报错，用 +"" 惯用法保护
    case_matches "$id" ${SELECT[@]+"${SELECT[@]}"} || continue
    total=$((total + 1))
    echo "=== $id  ($(basename "$f")) ==="
    out="$(bash "$f" 2>&1)"; ec=$?
    echo "$out"
    line="$(echo "$out" | grep '^RESULT ' | tail -1)"
    [ -n "$line" ] || line="RESULT $id ???  (无 RESULT 行；exit=$ec)"
    case "$ec" in
        0) passed=$((passed + 1)) ;;
        2) skipped=$((skipped + 1)) ;;
        *) failed=$((failed + 1)) ;;
    esac
    SUMMARY+=("$line")
    echo
done

echo "================== SUMMARY =================="
[ "${#SUMMARY[@]}" -gt 0 ] && printf '%s\n' "${SUMMARY[@]}"
echo "total=$total passed=$passed failed=$failed skipped=$skipped"
[ "$failed" -eq 0 ]
```

- [ ] **Step 3: 设可执行位并语法检查**

Run: `chmod +x tests/realdevice/run.sh && bash -n tests/realdevice/run.sh && echo OK`
Expected: `OK`

- [ ] **Step 4: 验证 `--dry-run` 在零用例时不报错**

Run: `bash tests/realdevice/run.sh --dry-run; echo "exit=$?"`
Expected: 无输出（`cases/` 尚为空），`exit=0`

- [ ] **Step 5: 改 `.gitignore` —— 忽略 config.sh**

在 `.gitignore` 末尾追加一行：

```
tests/realdevice/config.sh
```

- [ ] **Step 6: 改 `Makefile` —— 加 realdevice 目标**

在 `Makefile` 末尾追加（与文件已有缩进风格一致，用 TAB）：

```makefile
.PHONY: realdevice-lint realdevice-test
# 实机测试套件：lint 跑纯逻辑单测 + 用例语法检查（无需路由器）
realdevice-lint:
	bash tests/realdevice/lib/probe_test.sh
	bash tests/realdevice/lib/harness_test.sh
	bash tests/realdevice/run.sh --dry-run
# 跑实机用例（需 tests/realdevice/config.sh + 可达路由器）；CASES 可选，如 `make realdevice-test CASES="S W"`
realdevice-test:
	bash tests/realdevice/run.sh $(CASES)
```

- [ ] **Step 7: 验证 lint 目标**

Run: `make realdevice-lint`
Expected: probe_test 3 passed、harness_test 17 passed、dry-run 无用例输出，整体退出码 0

- [ ] **Step 8: 提交**

```bash
git add tests/realdevice/config.example.sh tests/realdevice/run.sh .gitignore Makefile
git commit -m "test(realdevice): 新增配置模板/运行器 run.sh + make 目标"
```

---

## Task 6: S 组用例 —— 退出/崩溃必须拆净

**Files:**
- Create: `tests/realdevice/cases/S1_stop_teardown.sh`
- Create: `tests/realdevice/cases/S2_crashloop_fatal.sh`
- Create: `tests/realdevice/cases/S3_daemon_sigterm.sh`
- Create: `tests/realdevice/cases/S4_daemon_kill9_orphan.sh`
- Create: `tests/realdevice/cases/S5_singbox_kill9.sh`

- [ ] **Step 1: 写 `cases/S1_stop_teardown.sh`**

```bash
#!/usr/bin/env bash
# S1 正常 stop → teardown 全清 → 直连通
set -u
CASE_ID="S1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "stop daemon，预期 teardown 拆净所有规则 → DIRECT"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline（可能已退出）；继续检查规则"
sleep 2

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "规则已拆净但 probe=$st（预期 DIRECT）"
    pass "teardown 干净；直连可用"
fi
fail "stop 后规则未完全拆净 —— 见上方 ✗"
```

- [ ] **Step 2: 写 `cases/S2_crashloop_fatal.sh`**

```bash
#!/usr/bin/env bash
# S2 sing-box 反复崩溃 → StateFatal → iptables 已拆净 → 直连通
set -u
CASE_ID="S2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "反复 kill -9 sing-box 直到 StateFatal（最多 300s）"
crashloop_to_fatal 300 || skip "300s 内未达 StateFatal（退避 ladder 较长，可重跑或延长超时）"
sleep 3

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "StateFatal 且规则已拆，但 probe=$st（预期 DIRECT）"
    pass "StateFatal 时规则已拆净；直连可用"
fi
fail "StateFatal 但规则未拆净 —— 黑洞风险"
```

- [ ] **Step 3: 写 `cases/S3_daemon_sigterm.sh`**

```bash
#!/usr/bin/env bash
# S3 daemon 收 SIGTERM → 子进程被收掉 + 规则全清 → 直连通
set -u
CASE_ID="S3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

dpid="$(daemon_pid)"
[ -n "$dpid" ] || skip "status 未给出 daemon pid"
note "kill -TERM daemon ($dpid)"
rsh "kill -TERM $dpid 2>/dev/null || true"
sleep 5

rsh "kill -0 $dpid 2>/dev/null" && fail "SIGTERM 后 daemon ($dpid) 仍存活"
if rsh "ps w 2>/dev/null | grep -v grep | grep -q 'sing-box run'"; then
    fail "daemon SIGTERM 后 sing-box 子进程仍存活（未被收掉）"
fi

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "probe=$st（预期 DIRECT）"
    pass "优雅 SIGTERM：子进程被收、规则拆净"
fi
fail "SIGTERM 后规则未拆净"
```

- [ ] **Step 4: 写 `cases/S4_daemon_kill9_orphan.sh`**

```bash
#!/usr/bin/env bash
# S4 daemon 被 kill -9（最坏情况）：拷问孤儿 sing-box 链 —— 孤儿后续崩溃是否黑洞。
# 本用例是「特征刻画」型：完成刻画即 PASS，黑洞结论以 note 显著标出（见 spec §8）。
set -u
CASE_ID="S4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

dpid="$(daemon_pid)"; spid="$(singbox_pid)"
[ -n "$dpid" ] && [ -n "$spid" ] || skip "缺少 daemon/sing-box pid"

note "kill -9 daemon ($dpid)；sing-box 子进程 ($spid) 因 setpgid 隔离，预期成孤儿"
rsh "kill -9 $dpid 2>/dev/null || true"
sleep 3

if rsh "kill -0 $spid 2>/dev/null"; then
    p1="$(probe)"
    note "phase1：孤儿 sing-box 存活，probe=$p1"
    [ "$p1" = PROXY ] || note "  WARN phase1 probe=$p1（孤儿服务期预期 PROXY）"
else
    note "phase1：sing-box 子进程未在 daemon kill -9 后存活"
fi

note "phase2：kill -9 孤儿 sing-box ($spid) —— 已无 supervisor 拆规则"
rsh "kill -9 $spid 2>/dev/null || true"
sleep 3
p2="$(probe)"
note "phase2：孤儿被杀后 probe=$p2"
case "$p2" in
    BLACKHOLE) pass "已刻画：phase2=BLACKHOLE —— 孤儿崩溃使 iptables 滞留，证实 spec §8 看门狗缺口" ;;
    DIRECT)    pass "已刻画：phase2=DIRECT —— 规则不知何故被清" ;;
    PROXY)     pass "已刻画：phase2=PROXY —— sing-box 被重新拉起（存在 init.d/cron 看门狗？）" ;;
    *)         fail "phase2 不确定：probe=$p2" ;;
esac
```

- [ ] **Step 5: 写 `cases/S5_singbox_kill9.sh`**

```bash
#!/usr/bin/env bash
# S5 sing-box 子进程被 kill -9、daemon 健在 → supervisor 退避重启 → 回到代理通
set -u
CASE_ID="S5"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
note "kill -9 sing-box 子进程 ($spid)，预期 supervisor 退避重启"
rsh "kill -9 $spid 2>/dev/null || true"

if wait_singbox_restart "$spid" 90; then
    st="$(probe)"
    [ "$st" = PROXY ] || fail "已重启但 probe=$st（预期 PROXY）"
    pass "单次崩溃已恢复；新 pid=$(singbox_pid)"
fi
fail "90s 内 supervisor 未把 sing-box 重启到 running"
```

- [ ] **Step 6: 语法检查全部 S 组**

Run: `for f in tests/realdevice/cases/S*.sh; do bash -n "$f" && echo "ok $f"; done`
Expected: 5 行 `ok ...`

- [ ] **Step 7: 提交**

```bash
git add tests/realdevice/cases/S1_stop_teardown.sh tests/realdevice/cases/S2_crashloop_fatal.sh tests/realdevice/cases/S3_daemon_sigterm.sh tests/realdevice/cases/S4_daemon_kill9_orphan.sh tests/realdevice/cases/S5_singbox_kill9.sh
git commit -m "test(realdevice): 新增 S 组用例(退出/崩溃必须拆净)"
```

---

## Task 7: W 组用例 —— 过渡窗口黑洞量化

**Files:**
- Create: `tests/realdevice/cases/W1_backoff_keep_window.sh`
- Create: `tests/realdevice/cases/W2_apply_restart_window.sh`
- Create: `tests/realdevice/cases/W3_sighup_route_window.sh`
- Create: `tests/realdevice/cases/W4_supervisor_restart_window.sh`

- [ ] **Step 1: 写 `cases/W1_backoff_keep_window.sh`**

```bash
#!/usr/bin/env bash
# W1 崩溃退避 <10s 保留 iptables 的窗口 —— 量化黑洞时长
set -u
CASE_ID="W1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
note "kill -9 sing-box ($spid)，测量「保留 iptables」退避窗口的黑洞时长"
rsh "kill -9 $spid 2>/dev/null || true"

ms="$(measure_blackhole_ms 30 1)"
note "连续 BLACKHOLE 窗口 ≈ ${ms}ms（采样粒度 ≈1s，偏粗）"

wait_singbox_restart "$spid" 60 || fail "60s 内未恢复到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=$st（预期 PROXY）"
pass "窗口≈${ms}ms；已恢复 PROXY —— 据此评估「赌快速回归」是否可接受（阈值 IptablesKeepBackoffLtMs=10s）"
```

- [ ] **Step 2: 写 `cases/W2_apply_restart_window.sh`**

```bash
#!/usr/bin/env bash
# W2 资源更新触发 applier restart 的窗口（不拆 iptables，ready-check 最长 60s）—— 最大风险点
set -u
CASE_ID="W2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_modified_zoo || skip "var/zoo.raw.json 不存在 —— 无法触发 applier restart"
old="$(singbox_pid)"
note "后台 POST /api/v1/apply，同时测量 restart 窗口"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 75 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"

case "$code" in
    501*) skip "apply 未接线（HTTP 501）：需 ApplyPending（auto_apply + gitee token）" ;;
    200*) : ;;
    *)    note "apply HTTP code=$code（非 200，继续按 pid 变化判断）" ;;
esac

new="$(singbox_pid)"
[ -n "$new" ] && [ "$new" != "$old" ] || skip "未观察到重启（apply 可能 no-op）；窗口测量不适用"
note "applier restart 期间连续 BLACKHOLE 窗口 ≈ ${ms}ms"
wait_state running 30 || fail "apply 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=$st（预期 PROXY）"
pass "applier restart 窗口≈${ms}ms；已恢复 PROXY（受 ready-check 约束，可达 30s+）"
```

- [ ] **Step 3: 写 `cases/W3_sighup_route_window.sh`**

```bash
#!/usr/bin/env bash
# W3 kill -HUP sing-box → TUN/路由重建，WatchRoutes(默认30s) 补回 —— 量化路由缺失窗口
set -u
CASE_ID="W3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
route_table_has_default || skip "起始时路由表 $ROUTE_TABLE 无 default 路由"

note "kill -HUP sing-box ($spid)，测量 device-bound 路由缺失窗口"
rsh "kill -HUP $spid 2>/dev/null || true"
gone_ms="$(measure_route_gone_ms 75 1)"
note "路由缺失窗口 ≈ ${gone_ms}ms（WatchRoutes 巡检默认 30s）"

ok=0
for _ in $(seq 1 40); do
    route_table_has_default && { ok=1; break; }
    sleep 2
done
[ "$ok" -eq 1 ] || fail "80s 内 default 路由未被 WatchRoutes 补回"

now_pid="$(singbox_pid)"
[ "$now_pid" = "$spid" ] || note "WARN sing-box pid 变了 ($spid → $now_pid)；HUP 不应重启进程"
st="$(probe)"
[ "$st" = PROXY ] || fail "路由已补回但 probe=$st（预期 PROXY）"
pass "路由缺失≈${gone_ms}ms；WatchRoutes 已补回；probe PROXY"
```

- [ ] **Step 4: 写 `cases/W4_supervisor_restart_window.sh`**

```bash
#!/usr/bin/env bash
# W4 supervisor.Restart 快速重启路径窗口（sing-router restart）—— 与 W2 对比
set -u
CASE_ID="W4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

old="$(singbox_pid)"
note "后台 sing-router restart，同时测量快速重启窗口"
tmp="$(mktemp)"
( rsh "$SINGROUTER restart" > "$tmp" 2>&1 ) &
rpid=$!
ms="$(measure_blackhole_ms 75 1)"
wait "$rpid" || true
rm -f "$tmp"

new="$(singbox_pid)"
[ -n "$new" ] && [ "$new" != "$old" ] || fail "restart 未产生新的 sing-box pid"
wait_state running 30 || fail "restart 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=$st（预期 PROXY）"
pass "supervisor.Restart 窗口≈${ms}ms；已恢复 PROXY（与 W2 对比快慢）"
```

- [ ] **Step 5: 语法检查全部 W 组**

Run: `for f in tests/realdevice/cases/W*.sh; do bash -n "$f" && echo "ok $f"; done`
Expected: 4 行 `ok ...`

- [ ] **Step 6: 提交**

```bash
git add tests/realdevice/cases/W1_backoff_keep_window.sh tests/realdevice/cases/W2_apply_restart_window.sh tests/realdevice/cases/W3_sighup_route_window.sh tests/realdevice/cases/W4_supervisor_restart_window.sh
git commit -m "test(realdevice): 新增 W 组用例(过渡窗口黑洞量化)"
```

---

## Task 8: R 组用例 —— 重装规则不能装半套

**Files:**
- Create: `tests/realdevice/cases/R1_reapply_healthy.sh`
- Create: `tests/realdevice/cases/R2_reapply_unhealthy.sh`
- Create: `tests/realdevice/cases/R3_koolshare_nat_start.sh`

- [ ] **Step 1: 写 `cases/R1_reapply_healthy.sh`**

```bash
#!/usr/bin/env bash
# R1 sing-box 健在时 reapply-rules：规则装回、代理恢复
set -u
CASE_ID="R1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "flush nat 表模拟规则丢失，再 reapply-rules"
rsh "iptables -t nat -F" || skip "无法 flush nat 表"
sleep 1
mid="$(probe)"
note "nat flush 后（类 WAN 重拨）：probe=$mid"

rsh "$SINGROUTER reapply-rules" >/dev/null 2>&1 || fail "reapply-rules 退出非 0"
sleep 2
st="$(probe)"
[ "$st" = PROXY ] || fail "reapply-rules 后 probe=$st（预期 PROXY）"
pass "reapply-rules 恢复代理（中间态为 $mid）"
```

- [ ] **Step 2: 写 `cases/R2_reapply_unhealthy.sh`**

```bash
#!/usr/bin/env bash
# R2 sing-box 不健康时 reapply-rules → 必须失败且不留半套规则（黑洞）
set -u
CASE_ID="R2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "stop daemon，再尝试 reapply-rules —— 必须失败、且不装出半套规则"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline；继续"
sleep 1
before="$(probe)"
note "daemon 已停：probe=$before"

out="$(rsh "$SINGROUTER reapply-rules" 2>&1)"; rc=$?
note "reapply-rules rc=$rc out=[$out]"
[ "$rc" -ne 0 ] || fail "daemon 停止时 reapply-rules 返回 0（必须失败）"
sleep 1

if rules_present; then
    fail "reapply-rules 在无 daemon 时留下了 'sing-box' 链 —— 半套规则黑洞风险"
fi
after="$(probe)"
case "$after" in
    DIRECT) pass "reapply-rules 干净失败；未留半套规则；probe DIRECT" ;;
    PROXY)  fail "daemon 停止时 probe=PROXY —— 状态不一致" ;;
    *)      fail "失败的 reapply-rules 之后 probe=$after" ;;
esac
```

- [ ] **Step 3: 写 `cases/R3_koolshare_nat_start.sh`**

```bash
#!/usr/bin/env bash
# R3 koolshare nat-start 钩子链路：模拟 WAN 重拨（flush nat + 调 N99 钩子）→ 代理恢复
# 真实 WAN 重拨（拔网线）为手动变体；本用例自动化模拟钩子链路本身。
set -u
CASE_ID="R3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

HOOK="/koolshare/init.d/N99sing-router.sh"
rsh "test -f $HOOK" || skip "koolshare 钩子 $HOOK 不存在（merlin 固件？请手动跑 R3）"

note "模拟 WAN 重拨：flush nat 表，再调用 koolshare nat-start 钩子"
rsh "iptables -t nat -F"
sleep 1
mid="$(probe)"
note "nat flush 后（类 WAN 重拨）：probe=$mid"
[ "$mid" = BLACKHOLE ] && fail "flush 后即 BLACKHOLE（预期 DIRECT）—— 需排查"

rsh "sh $HOOK start_nat" >/dev/null 2>&1 || true
sleep 3
hooklog="$(rsh "tail -3 /tmp/sing-router-nat-start.log 2>/dev/null")"
note "nat-start.log 末尾：$hooklog"
echo "$hooklog" | grep -q "reapply-rules ok" || fail "钩子未记录 'reapply-rules ok'"

st="$(probe)"
[ "$st" = PROXY ] || fail "钩子执行后 probe=$st（预期 PROXY）"
pass "koolshare 钩子重装规则成功；probe PROXY（真实 WAN 重拨为手动变体）"
```

- [ ] **Step 4: 语法检查全部 R 组**

Run: `for f in tests/realdevice/cases/R*.sh; do bash -n "$f" && echo "ok $f"; done`
Expected: 3 行 `ok ...`

- [ ] **Step 5: 提交**

```bash
git add tests/realdevice/cases/R1_reapply_healthy.sh tests/realdevice/cases/R2_reapply_unhealthy.sh tests/realdevice/cases/R3_koolshare_nat_start.sh
git commit -m "test(realdevice): 新增 R 组用例(重装规则不能装半套)"
```

---

## Task 9: D 组用例 —— 运行中资源同步/更新

**Files:**
- Create: `tests/realdevice/cases/D1_apply_valid_restart.sh`
- Create: `tests/realdevice/cases/D2_cn_reload_uninterrupted.sh`
- Create: `tests/realdevice/cases/D3_apply_noop_gate.sh`
- Create: `tests/realdevice/cases/D4_upstream_unreachable.sh`
- Create: `tests/realdevice/cases/D5_bad_check_revert.sh`
- Create: `tests/realdevice/cases/D6_run_fail_revert_recover.sh`

- [ ] **Step 1: 写 `cases/D1_apply_valid_restart.sh`**

```bash
#!/usr/bin/env bash
# D1 sync 拉到健康的新 zoo → applier check 通过 → restart → 代理通
set -u
CASE_ID="D1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_modified_zoo || skip "var/zoo.raw.json 不存在"
old="$(singbox_pid)"
code="$(apply_via_api)"
note "apply HTTP code=$code"
case "$code" in
    200*) : ;;
    501*) skip "apply 未接线（HTTP 501）" ;;
    *)    fail "apply 返回 $code" ;;
esac

wait_singbox_restart "$old" 90 || fail "合法资源 apply 后未发生重启"
st="$(probe)"
[ "$st" = PROXY ] || fail "apply 后 probe=$st（预期 PROXY）"
pass "合法 zoo 变更已应用；sing-box 已重启；probe PROXY"
```

- [ ] **Step 2: 写 `cases/D2_cn_reload_uninterrupted.sh`**

```bash
#!/usr/bin/env bash
# D2 sync 拉到新 cn.txt → reload-cn-ipset 轻量重载 → 全程不中断、不重启
set -u
CASE_ID="D2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_cn_txt; restore_to_running' EXIT

modify_cn_txt || skip "var/cn.txt 不存在"
old="$(singbox_pid)"
note "改 cn.txt 后后台 POST /apply，全程探针监测是否中断"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 20 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "sing-box pid 变了 ($old→$new) —— cn.txt 变更不应重启 sing-box"
[ "$ms" -eq 0 ] || fail "cn.txt reload 期间出现 ${ms}ms BLACKHOLE —— 必须全程不中断"
st="$(probe)"
[ "$st" = PROXY ] || fail "cn reload 后 probe=$st（预期 PROXY）"
pass "cn.txt 轻量重载：未重启、无中断、probe PROXY"
```

- [ ] **Step 3: 写 `cases/D3_apply_noop_gate.sh`**

```bash
#!/usr/bin/env bash
# D3 无资源变化时 POST /apply → sha256 闸门 no-op → 不触发任何 restart 窗口
set -u
CASE_ID="D3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

old="$(singbox_pid)"
note "不改任何资源直接 POST /apply —— 预期 sha256 闸门判定 no-op，不重启"
code="$(apply_via_api)"
note "apply HTTP code=$code"
case "$code" in
    200*) : ;;
    501*) skip "apply 未接线（HTTP 501）" ;;
    *)    fail "apply 返回 $code" ;;
esac
sleep 3

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "no-op apply 却重启了 sing-box ($old→$new) —— sha256 闸门失效"
st="$(probe)"
[ "$st" = PROXY ] || fail "probe=$st（预期 PROXY）"
pass "no-op apply：sha256 闸门生效，未重启，probe PROXY"
```

- [ ] **Step 4: 写 `cases/D4_upstream_unreachable.sh`**

```bash
#!/usr/bin/env bash
# D4 上游不可达 → sync/update 失败仅报错 → daemon 主流程与代理持续可用
set -u
CASE_ID="D4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

GITEE_HOST="gitee.com"
require_running
trap 'unblock_host "$GITEE_HOST"; restore_to_running' EXIT

note "在路由器 OUTPUT 链 REJECT 到 $GITEE_HOST，再跑 sing-router update all（预期干净失败）"
block_host "$GITEE_HOST" || skip "无法解析/封锁 $GITEE_HOST"
before="$(probe)"

out="$(rsh "$SINGROUTER update all" 2>&1)"; rc=$?
note "update rc=$rc"
[ "$rc" -ne 0 ] || note "WARN update 在封锁下仍返回 0（可能 token 为空或命中缓存）"
sleep 2

[ "$(daemon_state)" = running ] || fail "失败的 update 之后 daemon 状态变了"
after="$(probe)"
{ [ "$before" = PROXY ] && [ "$after" = PROXY ]; } \
    || fail "失败的 update 扰动了代理（before=$before after=$after）"
pass "上游不可达时 update 干净失败；daemon 与代理均未受影响"
```

- [ ] **Step 5: 写 `cases/D5_bad_check_revert.sh`**

```bash
#!/usr/bin/env bash
# D5 拉到的资源通不过 sing-box check → applier revert → 不重启 → 旧配置全程不中断
set -u
CASE_ID="D5"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_bad_check_zoo || skip "var/zoo.raw.json 不存在"
old="$(singbox_pid)"
cfg_before="$(rsh "sha256sum $RUNDIR/$CONFIG_DIR/zoo.json 2>/dev/null | cut -d' ' -f1")"
note "POST /apply 一个 check 必失败的 zoo —— 预期 revert、不重启、代理零扰动"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 20 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code（CheckConfig 失败路径返回 200；daemon 日志记 apply.check.failed）"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "sing-box 被重启 ($old→$new) —— 坏配置不应触发重启"
[ "$ms" -eq 0 ] || fail "坏配置 apply 期间出现 ${ms}ms BLACKHOLE —— 必须完全透明"
cfg_after="$(rsh "sha256sum $RUNDIR/$CONFIG_DIR/zoo.json 2>/dev/null | cut -d' ' -f1")"
[ "$cfg_before" = "$cfg_after" ] || fail "config.d/zoo.json 未被 revert（$cfg_before → $cfg_after）"
st="$(probe)"
[ "$st" = PROXY ] || fail "probe=$st（预期 PROXY）"
pass "check 失败资源已 revert；未重启；config.d/zoo.json 完好；probe PROXY"
```

- [ ] **Step 6: 写 `cases/D6_run_fail_revert_recover.sh`**

```bash
#!/usr/bin/env bash
# D6 资源过 check 但 sing-box run 失败 → applier revert + RecoverFromFailedApply 用旧配置拉回
set -u
CASE_ID="D6"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_box; restore_to_running' EXIT

stage_checkok_runfail_box || fail "无法在 bin/sing-box.new 放置假 sing-box"
old="$(singbox_pid)"
note "POST /apply 一个 check 过 / run 失败的 sing-box —— 预期 revert + recover 回旧配置"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 90 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code（restart 失败路径预期 500）"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

wait_state running 120 || fail "revert+recover 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "revert+recover 后 probe=$st（预期 PROXY）"
note "restart 失败 → revert → recover 窗口 ≈ ${ms}ms"
rsh "$RUNDIR/bin/sing-box version >/dev/null 2>&1" \
    || fail "revert 后 bin/sing-box 不是可用二进制（假 box 未被换回）"
pass "check 过/run 失败资源：已 revert + 用旧配置 recover；probe PROXY（窗口≈${ms}ms）"
```

- [ ] **Step 7: 语法检查全部 D 组**

Run: `for f in tests/realdevice/cases/D*.sh; do bash -n "$f" && echo "ok $f"; done`
Expected: 6 行 `ok ...`

- [ ] **Step 8: 提交**

```bash
git add tests/realdevice/cases/D1_apply_valid_restart.sh tests/realdevice/cases/D2_cn_reload_uninterrupted.sh tests/realdevice/cases/D3_apply_noop_gate.sh tests/realdevice/cases/D4_upstream_unreachable.sh tests/realdevice/cases/D5_bad_check_revert.sh tests/realdevice/cases/D6_run_fail_revert_recover.sh
git commit -m "test(realdevice): 新增 D 组用例(运行中资源同步/更新)"
```

---

## Task 10: README + 全量 lint 验收

**Files:**
- Create: `tests/realdevice/README.md`

- [ ] **Step 1: 写 `tests/realdevice/README.md`**

````markdown
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
# 编辑 config.sh：填 ROUTER_SSH，按需填 LAN_CLIENT_SSH 等
```

`config.sh` 已被 `.gitignore` 忽略。建议在 `~/.ssh/config` 给路由器配好别名与免密。

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
- 用例假设「直连本身可用」（路由器 WAN 正常）；`require_running` 在用例开始时确认 PROXY 已通来保证这一点。

## 架构

- `lib/probe.sh` —— busybox-ash 双目标探针，经 ssh 推到 LAN 客户端执行：分别请求 baidu（直连基准）与 google（代理验证），输出两个 OK/FAIL token。
- `lib/harness.sh` —— 开发机侧驱动函数库：三态判定、SSH/状态助手、故障注入、资源操作。
- `cases/*.sh` —— 单个用例，声明式调用 harness。
- `run.sh` —— 发现/过滤/汇总。
- 纯逻辑（三态分类、窗口游程、用例选择）由 `lib/*_test.sh` 单测覆盖。
````

- [ ] **Step 2: 全量 lint 验收**

Run: `make realdevice-lint`
Expected:
- `probe_test: 3 passed, 0 failed`
- `harness_test: 17 passed, 0 failed`
- 18 行 `ok     <case>.sh`（run.sh --dry-run）
- 整体退出码 0

- [ ] **Step 3: 确认 shellcheck 干净（若已装）**

Run: `command -v shellcheck >/dev/null && shellcheck -s bash tests/realdevice/run.sh tests/realdevice/lib/harness.sh tests/realdevice/cases/*.sh || echo "shellcheck 未安装，跳过"`
Expected: 无 error 级告警，或 `shellcheck 未安装，跳过`（warning 可酌情修，error 必须修）

- [ ] **Step 4: 提交**

```bash
git add tests/realdevice/README.md
git commit -m "test(realdevice): 新增实机测试套件 README"
```

---

## 实机验证说明（无法在 CI / 本会话完成）

Task 6–9 的用例脚本只能用 `bash -n` + `run.sh --dry-run` 做语法验证；它们的**行为正确性必须在一台已配置 `config.sh`、可达且运行中的路由器上跑 `make realdevice-test` 才能确认**。本地实现阶段到「lint 全绿」为止即视为计划完成；首轮真机运行作为独立的验证活动，按 README 在实机执行，并据 W 组实测窗口回填 spec §8 的开放问题。

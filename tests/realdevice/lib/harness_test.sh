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
assert_eq "$(max_contiguous BLACKHOLE BLACKHOLE BLACKHOLE BLACKHOLE BLACKHOLE)" "4" "all run = 4"
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

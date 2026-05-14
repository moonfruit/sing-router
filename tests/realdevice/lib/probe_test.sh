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

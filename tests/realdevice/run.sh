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

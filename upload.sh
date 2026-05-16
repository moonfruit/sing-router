#!/usr/bin/env bash
# upload.sh - scp -O 的简单包装，仅负责自动选择路由器 host
# 用法: upload.sh -d destination source...
#   host 探测：优先 192.168.50.1，ping 不通则回落到 router.dkmooncat.heiyu.space。
#   可用 UPLOAD_HOST=[user@]host 环境变量强制覆盖。
# scp 选项：-O 强制旧 SCP 协议（路由器 BusyBox/dropbear 没有 sftp-server）；
#            -p 保留本地 mode + mtime。

set -euo pipefail

DEST=""
SOURCES=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        -d) DEST="${2:?}"; shift 2 ;;
        --) shift; SOURCES+=("$@"); break ;;
        -*) echo "未知选项: $1" >&2; exit 1 ;;
        *)  SOURCES+=("$1"); shift ;;
    esac
done

[[ -z "$DEST" ]] && { echo "用法: $(basename "$0") -d destination source..." >&2; exit 1; }
[[ ${#SOURCES[@]} -eq 0 ]] && { echo "至少需要一个源" >&2; exit 1; }

HOST="${UPLOAD_HOST:-}"
if [[ -z "$HOST" ]]; then
    # macOS: -t 总超时秒数；Linux: -W 单包超时秒数。两者都试一下。
    if ping -c 1 -t 1 192.168.50.1 >/dev/null 2>&1 \
        || ping -c 1 -W 1 192.168.50.1 >/dev/null 2>&1; then
        HOST=192.168.50.1
    else
        HOST=router.dkmooncat.heiyu.space
    fi
fi

echo "[upload] ${SOURCES[*]} -> $HOST:$DEST" >&2
exec scp -O -p -- "${SOURCES[@]}" "$HOST:$DEST"

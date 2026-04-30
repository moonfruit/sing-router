#!/usr/bin/env bash
# upload.sh - 通过纯 ssh 流式上传文件/目录（不依赖 sftp/scp）
# 用法: upload.sh [-p port] [-h [user@]host] -d destination file1 [file2 dir1 ...]

set -euo pipefail

PORT=""
HOST=""
DEST=""
SOURCES=()
GZIP_THRESHOLD=$((1024 * 1024))  # >1MB 的文件用 gzip 流压缩

usage() {
    cat >&2 <<EOF
Usage: $(basename "$0") [-p port] [-h [user@]host] -d destination file1 [file2 dir1 ...]

  -p port         SSH 端口 (省略则由 ssh 自行决定，例如读取 ssh_config)
  -h host         [user@]host; 省略时优先使用 192.168.50.1,
                  ping 不通则回落到 router.dkmooncat.heiyu.space
  -d destination  远端目标路径（文件名或目录）
EOF
    exit 1
}

# ---------- 解析参数 ----------
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p) PORT="${2:?}"; shift 2 ;;
        -h) HOST="${2:?}"; shift 2 ;;
        -d) DEST="${2:?}"; shift 2 ;;
        --) shift; while [[ $# -gt 0 ]]; do SOURCES+=("$1"); shift; done ;;
        -\?|--help) usage ;;
        -*) echo "未知选项: $1" >&2; usage ;;
        *) SOURCES+=("$1"); shift ;;
    esac
done

[[ -z "$DEST" ]] && { echo "缺少 -d destination" >&2; usage; }
[[ ${#SOURCES[@]} -eq 0 ]] && { echo "至少需要一个源文件/目录" >&2; usage; }

# ---------- 自动选择 host ----------
if [[ -z "$HOST" ]]; then
    # macOS: -t 总超时秒数；Linux: -W 单包超时秒数。两者都尝试一下。
    if ping -c 1 -t 1 192.168.50.1 >/dev/null 2>&1 \
        || ping -c 1 -W 1 192.168.50.1 >/dev/null 2>&1; then
        HOST="192.168.50.1"
    else
        HOST="router.dkmooncat.heiyu.space"
    fi
    echo "[upload] auto host: $HOST" >&2
fi

SSH=(ssh -o ConnectTimeout=10)
[[ -n "$PORT" ]] && SSH+=(-p "$PORT")
SSH+=("$HOST")

# ---------- 校验本地源 ----------
for s in "${SOURCES[@]}"; do
    [[ -e "$s" ]] || { echo "源不存在: $s" >&2; exit 1; }
done

# ---------- 远端转义（用于嵌入单引号字符串） ----------
shq() {
    # 把字符串包成可安全嵌入到 '...' 中的形式
    local s=${1//\'/\'\\\'\'}
    printf "'%s'" "$s"
}

# ---------- 查询远端 umask（一次），本地计算文件/目录的基础权限 ----------
REMOTE_UMASK=$("${SSH[@]}" 'umask' | tr -d '[:space:]')
[[ -z "$REMOTE_UMASK" ]] && REMOTE_UMASK=022
# bash 里把 umask 当 8 进制；产生 0666 & ~umask、0777 & ~umask
FILE_BASE=$(printf '%o' $(( 0666 & ~8#$REMOTE_UMASK )))
DIR_MODE=$(printf '%o' $(( 0777 & ~8#$REMOTE_UMASK )))
echo "[upload] remote umask=$REMOTE_UMASK file_base=$FILE_BASE dir_mode=$DIR_MODE" >&2

# ---------- 探测远端 destination ----------
DEST_Q=$(shq "$DEST")
probe=$("${SSH[@]}" "if [ -d $DEST_Q ]; then echo DIR; elif [ -e $DEST_Q ]; then echo FILE; else echo NONE; fi")
echo "[upload] remote $DEST: $probe" >&2

# 是否包含目录源 / 多源
has_dir=0
for s in "${SOURCES[@]}"; do [[ -d "$s" ]] && has_dir=1; done
n=${#SOURCES[@]}

# ---------- 本地权限工具 ----------
get_size() { stat -f%z "$1" 2>/dev/null || stat -c%s "$1"; }

# 返回 ls -l 的 mode 字符串（如 -rwxr-xr-x），用于跨平台读取 u/g/o 的 x 位
mode_str() { ls -ldn "$1" 2>/dev/null | awk '{print $1; exit}'; }

# 输出本地文件的 x 位归属，形如 "u g o" / "u" / 空串
local_xbits() {
    local m c out=""
    m=$(mode_str "$1")
    [[ -z "$m" ]] && return
    c="${m:3:1}"; [[ "$c" == "x" || "$c" == "s" ]] && out="$out u"
    c="${m:6:1}"; [[ "$c" == "x" || "$c" == "s" ]] && out="$out g"
    c="${m:9:1}"; [[ "$c" == "x" || "$c" == "t" ]] && out="$out o"
    echo "$out"
}

# 拼接 chmod 所需的符号串（如 "u+x,g+x"）；若无 x 位则输出空
xbits_to_chmod() {
    local s out=""
    for s in $1; do out="${out:+$out,}${s}+x"; done
    echo "$out"
}

# ---------- 上传单文件 ----------
# cat > dest 由远端 shell 创建文件，rw 位天然遵循远端 umask；
# 若本地源具有 x 位，则按本地 u/g/o 的 x 归属在远端补上（不受 umask 削减）。
upload_file() {
    local src="$1" remote="$2"
    local size remote_q xb chm
    size=$(get_size "$src")
    remote_q=$(shq "$remote")
    if (( size > GZIP_THRESHOLD )); then
        echo "[upload] file (gzip) $src -> $HOST:$remote (${size}B)" >&2
        gzip -c "$src" | "${SSH[@]}" "gunzip -c > $remote_q"
    else
        echo "[upload] file        $src -> $HOST:$remote (${size}B)" >&2
        "${SSH[@]}" "cat > $remote_q" < "$src"
    fi
    xb=$(local_xbits "$src")
    if [[ -n "$xb" ]]; then
        chm=$(xbits_to_chmod "$xb")
        echo "[upload] chmod $chm $remote" >&2
        "${SSH[@]}" "chmod $chm $remote_q"
    fi
}

# ---------- 上传目录（tar+gzip 流） ----------
# 解包后将权限重映射为远端 umask 推导的 file_base / dir_mode；
# 同时保留原文件 u/g/o 的 x 位（不受 umask 削减）。
upload_dir() {
    local src="$1" remote_parent="$2"
    src="${src%/}"
    local base parent parent_q remote_base remote_base_q
    base=$(basename "$src")
    parent=$(dirname "$src")
    parent_q=$(shq "$remote_parent")
    remote_base="$remote_parent/$base"
    remote_base_q=$(shq "$remote_base")
    echo "[upload] dir  (tar.gz) $src -> $HOST:$remote_base" >&2

    # 远端：建父目录 -> 解包并保留归档内权限 -> normalize 权限
    # normalize 步骤：
    #   1) 先记录原来带 u+x / g+x / o+x 的文件
    #   2) 把所有普通文件 chmod 成 file_base，目录 chmod 成 dir_mode
    #   3) 再把 1 中记录的文件分别加回 u+x / g+x / o+x
    local remote_cmd="
set -e
mkdir -p $parent_q
tar -C $parent_q -xzpf -
B=$remote_base_q

# 优先用 entware 的 GNU find/xargs（支持 -type / -perm / -print0），
# 退回到 PATH 里的版本时由 -type 探测一次能力，不支持就直接报错。
if [ -x /opt/bin/find ]; then FIND=/opt/bin/find; else FIND=find; fi
if [ -x /opt/bin/xargs ]; then XARGS=/opt/bin/xargs; else XARGS=xargs; fi
if ! \"\$FIND\" / -maxdepth 0 -type d >/dev/null 2>&1; then
    echo \"远端 find 不支持 -type，请安装 entware findutils (/opt/bin/find)\" >&2
    exit 1
fi

TMPD=/tmp/upload.\$\$.\$(date +%s 2>/dev/null || echo 0)
mkdir -p \"\$TMPD\"
trap 'rm -rf \"\$TMPD\"' EXIT INT TERM
\"\$FIND\" \"\$B\" -type f -perm -u+x -print0 > \"\$TMPD/ux\" 2>/dev/null || :
\"\$FIND\" \"\$B\" -type f -perm -g+x -print0 > \"\$TMPD/gx\" 2>/dev/null || :
\"\$FIND\" \"\$B\" -type f -perm -o+x -print0 > \"\$TMPD/ox\" 2>/dev/null || :
\"\$FIND\" \"\$B\" -type f -print0 | \"\$XARGS\" -0 -r chmod $FILE_BASE 2>/dev/null || :
\"\$FIND\" \"\$B\" -type d -print0 | \"\$XARGS\" -0 -r chmod $DIR_MODE 2>/dev/null || :
[ -s \"\$TMPD/ux\" ] && \"\$XARGS\" -0 -r chmod u+x < \"\$TMPD/ux\" 2>/dev/null || :
[ -s \"\$TMPD/gx\" ] && \"\$XARGS\" -0 -r chmod g+x < \"\$TMPD/gx\" 2>/dev/null || :
[ -s \"\$TMPD/ox\" ] && \"\$XARGS\" -0 -r chmod o+x < \"\$TMPD/ox\" 2>/dev/null || :
"
    tar -C "$parent" -czf - "$base" | "${SSH[@]}" "$remote_cmd"
}

# ---------- 调度 ----------
if (( n > 1 )) || (( has_dir == 1 )); then
    # 多源 或 含目录 -> destination 必须是目录
    case "$probe" in
        FILE) echo "目标已存在且为文件，但多源/目录上传需要目录: $DEST" >&2; exit 1 ;;
        NONE) "${SSH[@]}" "mkdir -p $DEST_Q" ;;
        DIR)  : ;;
    esac
    for s in "${SOURCES[@]}"; do
        if [[ -d "$s" ]]; then
            upload_dir "$s" "$DEST"
        else
            upload_file "$s" "$DEST/$(basename "$s")"
        fi
    done
else
    # 单文件
    src="${SOURCES[0]}"
    case "$probe" in
        DIR)
            upload_file "$src" "$DEST/$(basename "$src")"
            ;;
        FILE)
            upload_file "$src" "$DEST"
            ;;
        NONE)
            parent_q=$(shq "$(dirname "$DEST")")
            "${SSH[@]}" "mkdir -p $parent_q"
            upload_file "$src" "$DEST"
            ;;
    esac
fi

echo "[upload] done." >&2

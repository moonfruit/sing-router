#!/usr/bin/env bash
# tests/docker/docker-test.sh — 端到端集成测试驱动。
#
# 流程：交叉编译 → 构建镜像 → 起容器 → install / ash 体检 / update / daemon / uninstall。
# 容器里 /bin/sh 仍是 dash（debian 默认，POSIX 严格）；同时 /opt/bin/{sh,bash} 软链
# 指向 busybox（PATH 最前面），所以 #!/usr/bin/env bash 与 PATH-lookup 的 bash/sh
# 调用都会落到 busybox ash，模拟真机。
#
# Token 解析顺序：
#   1. 已设的 SING_ROUTER_GITEE_TOKEN（最优先）
#   2. 否则尝试 sops -d tests/docker/secrets.env，提取里面的 SING_ROUTER_GITEE_TOKEN
#   3. 都没有 → 走无 token 路径（fake-sing-box）；WITH_UPDATE=1 时改为 fail
#
# 环境变量：
#   SING_ROUTER_GITEE_TOKEN  显式 token；与 daemon 运行时复用同一个变量名
#   WITH_UPDATE=1            强制要求 update 阶段，无 token 则 fail
#   KEEP=1                   测试退出后保留容器，便于 docker exec 进去手验
#   IMAGE_TAG                镜像 tag，默认 sing-router-test:aarch64
#   CONTAINER_NAME           容器名，默认 sr-test
#
# 用法：
#   make docker-test
#   SING_ROUTER_GITEE_TOKEN=... make docker-test
#   WITH_UPDATE=1 make docker-test           # 依赖 secrets.env 已加密 + 私钥可用
#   KEEP=1 make docker-test
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

IMAGE_TAG="${IMAGE_TAG:-sing-router-test:aarch64}"
CONTAINER_NAME="${CONTAINER_NAME:-sr-test}"
SECRETS_FILE="tests/docker/secrets.env"

step() { printf '\n=== %s ===\n' "$*"; }

# Token 兜底解密：只解析 SING_ROUTER_GITEE_TOKEN= 开头的那一行，避免 eval 任意内容。
if [ -z "${SING_ROUTER_GITEE_TOKEN:-}" ] \
   && [ -f "$SECRETS_FILE" ] \
   && command -v sops >/dev/null 2>&1; then
    if decrypted=$(sops -d "$SECRETS_FILE" 2>/dev/null); then
        line=$(printf '%s\n' "$decrypted" | grep -E '^SING_ROUTER_GITEE_TOKEN=' | head -1 || true)
        if [ -n "$line" ]; then
            SING_ROUTER_GITEE_TOKEN="${line#SING_ROUTER_GITEE_TOKEN=}"
            # 去掉首尾引号（如 sops 解密出来是 SING_ROUTER_GITEE_TOKEN="abc"）
            SING_ROUTER_GITEE_TOKEN="${SING_ROUTER_GITEE_TOKEN%\"}"
            SING_ROUTER_GITEE_TOKEN="${SING_ROUTER_GITEE_TOKEN#\"}"
            export SING_ROUTER_GITEE_TOKEN
            echo "info: SING_ROUTER_GITEE_TOKEN decrypted from $SECRETS_FILE via sops"
        fi
    else
        echo "info: sops -d $SECRETS_FILE failed (no key, file unencrypted, or absent) — continuing without token"
    fi
fi

# ------------------------------------------------------------------ Phase 0
step "Phase 0  build host artefacts"
make build-arm64
make fakebox

# ------------------------------------------------------------------ Phase 1
step "Phase 1  build image $IMAGE_TAG"
docker build -f tests/docker/Dockerfile -t "$IMAGE_TAG" .

# ------------------------------------------------------------------ Phase 2
step "Phase 2  start container $CONTAINER_NAME"
docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
cid=$(docker run -d --name "$CONTAINER_NAME" \
    --cap-add=NET_ADMIN \
    --device=/dev/net/tun:/dev/net/tun \
    "$IMAGE_TAG")
echo "container id: $cid"

cleanup() {
    rc=$?
    echo
    echo "--- container logs (tail 100) ---"
    docker logs "$cid" 2>&1 | tail -100 || true
    if [ "${KEEP:-0}" = 1 ]; then
        echo
        echo "[KEEP=1] container kept: docker exec -it $CONTAINER_NAME /opt/bin/sh   (busybox ash)"
        echo "                          docker exec -it $CONTAINER_NAME /bin/sh        (debian dash)"
    else
        docker rm -f "$cid" >/dev/null 2>&1 || true
    fi
    exit "$rc"
}
trap cleanup EXIT

# fake-sing-box 进容器；当 update 跳过时，作为 daemon 阶段的兜底二进制。
docker cp testdata/fake-sing-box/fake-sing-box "$cid:/tmp/fake-sing-box"

# 包装：通过 /opt/bin/sh = busybox ash 跑容器内命令，匹配真机 /bin/sh 行为。
ex() { docker exec "$cid" /opt/bin/sh -c "$1"; }

# ------------------------------------------------------------------ Phase A
step "Phase A  detection signals + busybox in /opt/bin"
ex 'test -L /jffs/.asusrouter && test -x /koolshare/bin/kscore.sh'
# /opt/bin/sh 与 /opt/bin/bash 必须落到 busybox（PATH 优先级 → 模拟真机）
ex 'readlink /opt/bin/sh   | grep -q busybox'
ex 'readlink /opt/bin/bash | grep -q busybox'
# /bin/sh 保持 debian dash（避免破坏 dpkg/postinst）
ex 'sing-router doctor || true'

# ------------------------------------------------------------------ Phase B
step "Phase B  install (default no-download; gitee-token via flag if provided)"
token_flag=""
if [ -n "${SING_ROUTER_GITEE_TOKEN:-}" ]; then
    token_flag="--gitee-token=${SING_ROUTER_GITEE_TOKEN}"
fi
ex "sing-router install --yes --rundir /opt/home/sing-router ${token_flag}"

ex 'test -f /opt/home/sing-router/daemon.toml'
ex 'test -d /opt/home/sing-router/config.d'
ex 'test -x /opt/etc/init.d/S99sing-router'
ex 'test -x /koolshare/init.d/N99sing-router.sh'

if [ -n "${SING_ROUTER_GITEE_TOKEN:-}" ]; then
    ex 'grep -q "^token = \"..*\"" /opt/home/sing-router/daemon.toml'
    ex '! grep -q "^token = \"\"" /opt/home/sing-router/daemon.toml'
fi

# ------------------------------------------------------------------ Phase C
step "Phase C  busybox ash compatibility (static -n + dynamic exec)"
# 静态：实际落盘的 #!/bin/sh 脚本必须能被 busybox ash 解析
ex 'busybox sh -n /opt/etc/init.d/S99sing-router'
ex 'busybox sh -n /koolshare/init.d/N99sing-router.sh'
# 动态：N99sing-router.sh 直接 exec（其内部走 reapply-rules 路径）
ex '/koolshare/init.d/N99sing-router.sh start_nat'
# startup.sh / teardown.sh 嵌在二进制里，runner 直接读 embed → 不落盘；
# 它们的 ash 兼容性由 assets/embed_test.go 的单元测试守住。

# ------------------------------------------------------------------ Phase D
step "Phase D  update (gitee real download, conditional)"
sing_box_source="fake"
if [ -n "${SING_ROUTER_GITEE_TOKEN:-}" ]; then
    ex 'sing-router update'
    ex 'test -x /opt/home/sing-router/bin/sing-box'
    ex 'test -s /opt/home/sing-router/var/cn.txt'
    sing_box_source="real (gitee download)"
elif [ "${WITH_UPDATE:-0}" = 1 ]; then
    echo "FAIL: WITH_UPDATE=1 requires SING_ROUTER_GITEE_TOKEN (or decryptable secrets.env)" >&2
    exit 1
else
    echo "SKIP: update — no SING_ROUTER_GITEE_TOKEN; daemon will use fake-sing-box"
    ex 'mkdir -p /opt/home/sing-router/bin'
    ex 'cp /tmp/fake-sing-box /opt/home/sing-router/bin/sing-box'
    ex 'chmod 0755 /opt/home/sing-router/bin/sing-box'
fi
echo "daemon will run with sing-box source: ${sing_box_source}"

# ------------------------------------------------------------------ Phase E
# 验证 init.d 启停生命周期。
#   - 真 sing-box：等 supervisor 进入 state=running，再跑 doctor/status 严格校验
#   - fake-sing-box：不开 clash API，只验子进程被拉起 + 优雅停止
step "Phase E  daemon start/stop via init.d (under ash)"
ex '/opt/etc/init.d/S99sing-router start'

if [ "$sing_box_source" = "real (gitee download)" ]; then
    echo "waiting up to 30s for daemon to reach state=running..."
    ready=0
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
        if docker exec "$cid" sing-router status 2>/dev/null | grep -qE 'state=running'; then
            ready=1
            break
        fi
        sleep 2
    done
    if [ "$ready" != 1 ]; then
        echo "FAIL: daemon did not reach state=running within 30s; recent log:"
        docker exec "$cid" tail -40 /opt/home/sing-router/log/sing-router.log 2>&1 || true
        exit 1
    fi
    ex 'sing-router status'
    # 严格 doctor：不允许任何 FAIL（doctor 始终 exit 0，靠 grep 反向断言）
    if docker exec "$cid" sing-router doctor 2>&1 | tee /tmp/sr-doctor.out | grep -qE '^[[:space:]]*FAIL[[:space:]]'; then
        echo "FAIL: sing-router doctor reports failures:"
        grep -E '^[[:space:]]*FAIL[[:space:]]' /tmp/sr-doctor.out
        exit 1
    fi
    ex 'sing-router status | grep -qE "state=running"'
    ex 'sing-router status | grep -qE "sing-box: pid=[0-9]+"'
else
    sleep 3
    ex 'sing-router status'
    ex 'sing-router status | grep -qE "sing-box: pid=[0-9]+"'
fi

ex '/opt/etc/init.d/S99sing-router stop'
sleep 2
# 停止后：要么 daemon 已退出（status 报错），要么状态非 running——两者都 OK。
ex 'sing-router status; true'
ex '! sing-router status 2>/dev/null | grep -qE "sing-box: pid=[1-9][0-9]*"'

# ------------------------------------------------------------------ Phase F
step "Phase F  uninstall (rolls back hooks; rundir kept unless --purge)"
ex 'sing-router uninstall'
ex '! test -e /opt/etc/init.d/S99sing-router'
ex '! test -e /koolshare/init.d/N99sing-router.sh'
# RUNDIR 仍在（无 --purge），用户的 daemon.toml 受保护——这是 uninstall 的约定。
ex 'test -f /opt/home/sing-router/daemon.toml'

echo
echo "OK: docker-test passed (sing-box source: ${sing_box_source})"

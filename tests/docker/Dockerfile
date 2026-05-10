# 多阶段构建：
#   base   ── 重负载（apt + entware ~80MB + busybox 软链 + 假 koolshare 树），
#             只随 install-entware.sh / rootfs / 系统包列表变动而失效。
#   final  ── 只 COPY sing-router 二进制；改 sing-router 代码时仅本阶段重跑。
#
# Docker BuildKit 会按层缓存，base 命中后 final 仅几百毫秒即可重建。

# =========================================================================
# Stage 1: koolshare/entware 仿真环境
# =========================================================================
FROM debian:stable-slim AS base

# 系统包：entware generic.sh + sing-router 运行 + 测试断言。
# busybox-static 提供 /usr/bin/busybox（最后一步 symlink 到 /opt/bin/{sh,bash,killall}）。
# tini 用 entrypoint 接收信号；procps 给 ps；ca-certificates 让 https 走 gitee；
# iproute2/iptables/ipset 给 startup.sh 真实 teardown 时用。
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        bash curl wget ca-certificates tzdata procps tini busybox-static \
        coreutils findutils grep sed iproute2 iptables ipset \
    && rm -rf /var/lib/apt/lists/*

# entware：uname -m 自适应；安装失败即构建失败（不静默回落）。
# ENTWARE_BASE_URL 默认 bin.entware.net；如要走国内镜像传 --build-arg ENTWARE_BASE_URL=...
ARG ENTWARE_BASE_URL=https://bin.entware.net
COPY tests/docker/install-entware.sh /tmp/install-entware.sh
RUN chmod +x /tmp/install-entware.sh && ENTWARE_BASE_URL="$ENTWARE_BASE_URL" /tmp/install-entware.sh

# busybox sh / bash / killall 放到 /opt/bin（PATH 最前面），模拟真机：
#   - `#!/usr/bin/env bash` → 通过 PATH 找到 /opt/bin/bash → busybox ash applet
#   - 命令行 `sh script.sh` / `bash script.sh` 走 PATH → 同样落到 busybox
#   - rc.func 用 `killall <PROC>` 停服务；dash 不内置 killall，需要 PATH 命中 busybox
# 系统 /bin/sh (= dash) 与 /bin/bash (= 真 bash) 保持原样，避免破坏 dpkg/postinst。
RUN ln -sf /usr/bin/busybox /opt/bin/sh \
    && ln -sf /usr/bin/busybox /opt/bin/bash \
    && ln -sf /usr/bin/busybox /opt/bin/killall

# 假 koolshare 检测信号（firmware.Detect 的两条判据）。
COPY tests/docker/rootfs/koolshare/ /koolshare/
RUN chmod 0755 /koolshare/bin/kscore.sh \
    && mkdir -p /jffs \
    && ln -sf /koolshare /jffs/.asusrouter

ENV PATH=/opt/sbin:/opt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]

# =========================================================================
# Stage 2: 安装被测二进制
# =========================================================================
FROM base

COPY sing-router-linux-arm64 /opt/sbin/sing-router
RUN chmod 0755 /opt/sbin/sing-router

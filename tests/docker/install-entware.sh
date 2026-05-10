#!/usr/bin/env bash
# 在 Dockerfile 构建期内调用：根据 uname -m 选 entware 安装目录、跑 generic.sh。
# 失败即非零退出，让 docker build 立刻失败——不静默回落。
#
# ENTWARE_BASE_URL 默认指向上游 bin.entware.net（实测 BFSU / TUNA 都不托管 entware
# 安装器；如有可用国内镜像，传入 --build-arg ENTWARE_BASE_URL=https://your-mirror/entware）。
set -euo pipefail

case "$(uname -m)" in
    aarch64) arch=aarch64-k3.10 ;;
    armv7l)  arch=armv7sf-k3.2 ;;
    x86_64)  arch=x64-k3.2 ;;
    *)
        echo "install-entware: unsupported arch $(uname -m)" >&2
        exit 1
        ;;
esac

base_url="${ENTWARE_BASE_URL:-https://bin.entware.net}"
url="${base_url}/${arch}/installer/generic.sh"
echo "install-entware: arch=${arch} url=${url}"

wget -qO- "$url" | sh

if [ ! -x /opt/bin/opkg ]; then
    echo "install-entware: /opt/bin/opkg missing — generic.sh did not finish" >&2
    exit 1
fi
if [ ! -f /opt/etc/init.d/rc.func ]; then
    echo "install-entware: /opt/etc/init.d/rc.func missing — entware layout incomplete" >&2
    exit 1
fi

echo "install-entware: ok"

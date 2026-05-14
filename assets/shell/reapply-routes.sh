#!/usr/bin/env bash
# shellcheck shell=busybox
# ============================================================
# sing-router reapply-routes script (managed; embedded into the Go binary).
# 只重装与 TUN 设备绑定的路由部分（不动 iptables / ipset）：
#   - `ip route default dev $TUN table $ROUTE_TABLE`（设备绑定，会被内核
#     在 TUN fd 关闭时自动删除；sing-box 收到 SIGHUP reload 或 supervisor.
#     Restart 杀子再起都会触发）
#   - `ip rule fwmark $ROUTE_MARK lookup $ROUTE_TABLE`（与设备无关，幂等添加
#     兜底防止意外丢失）
# 由 supervisor 在 sing-box 重启而 startup.sh 不重跑的场景下调用（即
# bootStep(skipStartupIfInstalled=true) 分支），保住"快速重启路径"上的
# 路由可用性。
# ============================================================

set -eu

: "${TUN:?TUN not set}"
: "${ROUTE_TABLE:?ROUTE_TABLE not set}"
: "${ROUTE_MARK:?ROUTE_MARK not set}"

# 等待 sing-box 把新 TUN 设备真创建出来。sing-box ready check 只验
# mixed-in 端口和 clash API；TUN inbound 建好通常稍晚一点。最多等 10s。
i=0
while [ "$i" -lt 50 ]; do
    if ip link show "$TUN" >/dev/null 2>&1; then
        break
    fi
    i=$((i + 1))
    sleep 0.2
done

ip route replace default dev "$TUN" table "$ROUTE_TABLE"
ip rule add fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null || true

echo "sing-router reapply-routes: default route + fwmark rule restored"

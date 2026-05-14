#!/bin/sh
# sing-router — koolshare nat-start hook (managed by `sing-router install`; do not edit)
# Invoked by /koolshare/bin/ks-nat-start.sh after NAT/firewall comes up.
# $1 is the action passed by /jffs/scripts/nat-start (currently always "start_nat").

ACTION=$1
LOG=/tmp/sing-router-nat-start.log

# log 同时落 /tmp 文件与 syslog；这条链路在 sing-router.log 里看不到，
# WAN 重拨后 iptables 没补回时，先看这里确认钩子有没有被触发、reapply 结果如何。
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') N99sing-router: $*" >> "$LOG"
    logger -t sing-router-nat-start "$*" 2>/dev/null
}

# Guard: if /opt isn't mounted yet (early boot before entware), no-op silently.
if ! command -v sing-router >/dev/null 2>&1; then
    log "skipped (action=$ACTION): sing-router not on PATH (entware not mounted yet?)"
    exit 0
fi

case "$ACTION" in
    start_nat|"" )
        log "reapply-rules start (action=$ACTION)"
        OUT=$(sing-router reapply-rules 2>&1)
        RC=$?
        if [ "$RC" -eq 0 ]; then
            log "reapply-rules ok (action=$ACTION)"
        else
            log "reapply-rules FAILED rc=$RC (action=$ACTION): $OUT"
        fi
        ;;
    * )
        log "ignored action=$ACTION"
        ;;
esac
exit 0

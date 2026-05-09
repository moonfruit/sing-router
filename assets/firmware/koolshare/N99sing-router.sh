#!/bin/sh
# sing-router — koolshare nat-start hook (managed by `sing-router install`; do not edit)
# Invoked by /koolshare/bin/ks-nat-start.sh after NAT/firewall comes up.
# $1 is the action passed by /jffs/scripts/nat-start (currently always "start_nat").

ACTION=$1

# Guard: if /opt isn't mounted yet (early boot before entware), no-op silently.
command -v sing-router >/dev/null 2>&1 || exit 0

case "$ACTION" in
    start_nat|"" )
        sing-router reapply-rules >/dev/null
        ;;
esac
exit 0

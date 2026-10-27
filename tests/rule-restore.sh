#!/system/bin/sh
# Delete only the module's known rules, before login, and observe recovery.
set -eu
MOD=/data/adb/modules/shellscaled
[ "$(id -u)" = 0 ] || exit 1
[ "$(getprop ro.kernel.qemu)" = 1 ] || [ "$(getprop ro.boot.qemu)" = 1 ] || exit 1
"$MOD/bin/tailscale" status --json | grep -q '"BackendState": "NeedsLogin"' || exit 1
DONE=false
cleanup() { "$DONE" || "$MOD/ctl.sh" restart >/dev/null 2>&1 || true; }
trap cleanup EXIT
trap 'exit 1' INT TERM HUP
for family in 4 6; do
    case "$family" in
        4) prefix=100.64.0.0/10 ;;
        6) prefix=fd7a:115c:a1e0::/48 ;;
    esac
    ip -"$family" rule show | grep -F "to $prefix fwmark 0x0/0x1e000000 lookup 52" >/dev/null
    ip -"$family" rule del pref 12500 to "$prefix" fwmark 0x0/0x1e000000 table 52
    restored=false
    for i in $(seq 1 10); do
        if ip -"$family" rule show | grep -F "to $prefix fwmark 0x0/0x1e000000 lookup 52" >/dev/null; then
            restored=true
            break
        fi
        sleep 1
    done
    "$restored" || { echo "FAIL: IPv$family rule did not recover." >&2; exit 1; }
    echo "PASS: IPv$family policy rule recovered."
done
DONE=true

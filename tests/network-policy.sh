#!/system/bin/sh
# Emulator-only, pre-login integration check against the real Android FIB.
# Requires the separately built dev.shellscaled.vpntest fixture.
set -eu
MOD=/data/adb/modules/shellscaled
V4=100.127.255.253
V6=fd7a:115c:a1e0:ffff::1
OWN_V4=false
OWN_V6=false
OWN_VPN=false
[ "$(id -u)" = 0 ] || exit 1
[ "$(getprop ro.kernel.qemu)" = 1 ] || [ "$(getprop ro.boot.qemu)" = 1 ] || exit 1
"$MOD/bin/tailscale" status --json | grep -q '"BackendState": "NeedsLogin"' || {
    echo 'Run this synthetic-route check only before login.' >&2
    exit 1
}
[ -z "$(ip -4 route show table 52)" ] && [ -z "$(ip -6 route show table 52)" ] || {
    echo 'Table 52 must be empty; refusing to overlap real account routes.' >&2
    exit 1
}
cleanup() {
    "$OWN_VPN" && am force-stop dev.shellscaled.vpntest >/dev/null 2>&1 || true
    "$OWN_V4" && ip -4 route del "$V4/32" dev tailscale0 table 52 2>/dev/null || true
    "$OWN_V6" && ip -6 route del "$V6/128" dev tailscale0 table 52 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP
ip -4 route add "$V4/32" dev tailscale0 table 52
OWN_V4=true
ip -6 route add "$V6/128" dev tailscale0 table 52
OWN_V6=true
cmd appops set dev.shellscaled.vpntest ACTIVATE_VPN allow
OWN_VPN=true
am start -n dev.shellscaled.vpntest/.MainActivity --ez full true >/dev/null
VPN_IF=
for i in $(seq 1 20); do
    VPN_IF=$(ip -o link show | sed -n 's/^[0-9]*: \(tun[0-9][0-9]*\):.*/\1/p' | head -n 1)
    [ -z "$VPN_IF" ] || break
    sleep 1
done
[ -n "$VPN_IF" ] || { echo 'VPN fixture did not establish a TUN.' >&2; exit 1; }
for address in "$V4" "$V6"; do
    ip route get "$address" uid 10000 | grep -q 'dev tailscale0' || {
        echo 'Tailnet test route did not precede Android VPN.' >&2; exit 1;
    }
    bypass=$(ip route get "$address" mark 0x10020000 uid 0)
    case "$bypass" in *"dev tailscale0"*|*"dev $VPN_IF"*) echo 'Transport bypass loop detected.' >&2; exit 1 ;; esac
done
for address in 8.8.8.8 2001:4860:4860::8888; do
    ip route get "$address" uid 10000 | grep -q "dev $VPN_IF" || {
        echo 'Ordinary traffic did not retain the Android VPN route.' >&2; exit 1;
    }
done
"$MOD/ctl.sh" restart >/dev/null
ip link show "$VPN_IF" >/dev/null
echo 'PASS: real Android IPv4/IPv6 policy routes prefer Tailnet, protect daemon transport, retain VPN internet routes, and preserve VpnService across daemon restart.'
echo 'This synthetic-route check does not prove authenticated packet delivery.'

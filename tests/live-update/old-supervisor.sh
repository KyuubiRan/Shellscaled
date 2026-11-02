#!/system/bin/sh
# Model the pre-live-update identity check, which accepts only the live path.
SC_MODDIR=$TEST_LIVE
. "$SC_MODDIR/common.sh" || exit 1
sc_daemon_running() {
    sc_read_pid "$SC_DAEMON_PID" || return 1
    sc_test_exe=$(readlink "/proc/$SC_PID/exe" 2>/dev/null) || return 1
    [ "${sc_test_exe%" (deleted)"}" = "$SC_MODDIR/bin/tailscaled" ]
}
trap 'exit 0' TERM INT HUP
while true; do
    sc_daemon_running || : > "$TEST_ROOT/lost-daemon-ownership"
    sleep 0.1
done

#!/system/bin/sh
# Run only inside an Android emulator as root. Does not authenticate, log out,
# erase state, or install/uninstall any module.

set -u
MODDIR=/data/adb/modules/shellscaled
SC_MODDIR=$MODDIR
DATA=/data/adb/shellscaled
TEST_SLEEP_PID=
TEST_SLEEP_START=
OWNS_DISABLE=false
OWNS_REMOVE=false
SENTINEL=$DATA/state/.shellscaled-smoke-sentinel
TOKEN="smoke-$$-$(date +%s)"
PASSED=0

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

pass() {
    PASSED=$((PASSED + 1))
    printf 'PASS: %s\n' "$*"
}

ctl() {
    /system/bin/sh "$MODDIR/ctl.sh" "$@"
}

[ "$(id -u)" = 0 ] || fail 'root is required'
[ "$(getprop ro.kernel.qemu)" = 1 ] || [ "$(getprop ro.boot.qemu)" = 1 ] || fail 'this script only runs on Android Emulator'
[ -f "$MODDIR/common.sh" ] || fail 'Shellscaled is not installed'
[ ! -e "$MODDIR/disable" ] && [ ! -e "$MODDIR/remove" ] || fail 'enable the module before testing'
. "$MODDIR/common.sh" || fail 'cannot load runtime helpers'

cleanup() {
    trap '' INT TERM HUP
    "$OWNS_DISABLE" && rm -f "$MODDIR/disable"
    "$OWNS_REMOVE" && rm -f "$MODDIR/remove"
    if [ -n "$TEST_SLEEP_PID" ] && [ "$(sc_start_time "$TEST_SLEEP_PID" 2>/dev/null)" = "$TEST_SLEEP_START" ]; then
        kill -TERM "$TEST_SLEEP_PID" 2>/dev/null || true
    fi
    if [ -f "$SENTINEL" ] && [ "$(cat "$SENTINEL")" = "$TOKEN" ]; then
        rm -f "$SENTINEL"
    fi
    ctl start >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

wait_stopped() {
    sc_test_wait=0
    while [ "$sc_test_wait" -lt 25 ]; do
        if ! sc_daemon_running && ! sc_supervisor_running; then
            return 0
        fi
        sleep 1
        sc_test_wait=$((sc_test_wait + 1))
    done
    return 1
}

ctl start || fail 'start failed'
ctl status || fail 'status did not report the supervised daemon'
sc_daemon_running || fail 'daemon identity was not verified'
FIRST_PID=$SC_PID
FIRST_START=$SC_PID_START
sc_supervisor_running || fail 'supervisor identity was not verified'
FIRST_SUPERVISOR=$SC_PID
ctl start || fail 'idempotent start failed'
sc_daemon_running && [ "$SC_PID:$SC_PID_START" = "$FIRST_PID:$FIRST_START" ] || fail 'start duplicated or replaced the daemon'
sc_supervisor_running && [ "$SC_PID" = "$FIRST_SUPERVISOR" ] || fail 'start duplicated the supervisor'
pass 'start/status and repeated start preserve one daemon and supervisor'

for TEST_DIR in "$DATA" "$DATA/state" "$DATA/run" "$DATA/logs"; do
    [ "$(stat -c '%a' "$TEST_DIR")" = 700 ] || fail "private directory permissions: $TEST_DIR"
done
[ -S "$DATA/run/tailscaled.sock" ] || fail 'local API socket is missing'
pass 'private directories are mode 0700 and the local socket exists'

[ ! -e "$SENTINEL" ] || fail 'a previous smoke sentinel exists; inspect it before testing'
umask 077
printf '%s\n' "$TOKEN" > "$SENTINEL" || fail 'cannot create the test-owned state sentinel'
ctl stop || fail 'stop failed'
ctl stop || fail 'idempotent stop failed'
wait_stopped || fail 'processes remained after stop'
if ctl status >/dev/null 2>&1; then
    fail 'status reported running after stop'
fi
ctl start || fail 'start after stop failed'
[ "$(cat "$SENTINEL")" = "$TOKEN" ] || fail 'stop/start changed persistent state'
pass 'stop is idempotent; start preserves persistent state'

# Forging a valid PID/start-time pair for a harmless process must not allow
# ctl stop to kill it: its /proc/exe does not match the module daemon.
ctl stop || fail 'stop before PID isolation test failed'
/system/bin/sleep 180 &
TEST_SLEEP_PID=$!
TEST_SLEEP_START=$(sc_start_time "$TEST_SLEEP_PID") || fail 'cannot inspect the test process'
printf '%s %s\n' "$TEST_SLEEP_PID" "$TEST_SLEEP_START" > "$DATA/run/daemon.pid"
printf '%s %s\n' "$TEST_SLEEP_PID" "$TEST_SLEEP_START" > "$DATA/run/supervisor.pid"
ctl stop || fail 'stop with a foreign PID record failed'
kill -0 "$TEST_SLEEP_PID" 2>/dev/null || fail 'stop killed a foreign process'
[ "$(sc_start_time "$TEST_SLEEP_PID")" = "$TEST_SLEEP_START" ] || fail 'the foreign test process changed identity'
kill -TERM "$TEST_SLEEP_PID"
wait "$TEST_SLEEP_PID" 2>/dev/null || true
TEST_SLEEP_PID=
TEST_SLEEP_START=
ctl start || fail 'start after PID isolation test failed'
pass 'forged daemon and supervisor PID records cannot kill another process'

# Crash recovery verifies supervision without changing keys or preferences.
sc_daemon_running || fail 'daemon missing before crash recovery'
CRASH_PID=$SC_PID
CRASH_START=$SC_PID_START
kill -KILL "$CRASH_PID" || fail 'could not kill the verified test daemon'
RECOVERED=false
sc_test_wait=0
while [ "$sc_test_wait" -lt 20 ]; do
    if sc_daemon_running && [ "$SC_PID:$SC_PID_START" != "$CRASH_PID:$CRASH_START" ] && [ -S "$DATA/run/tailscaled.sock" ]; then
        RECOVERED=true
        break
    fi
    sleep 1
    sc_test_wait=$((sc_test_wait + 1))
done
"$RECOVERED" || fail 'the supervisor did not recover the daemon'
pass 'the supervisor recovers its crashed daemon'

OWNS_DISABLE=true
touch "$MODDIR/disable"
wait_stopped || fail 'disable did not stop the daemon and supervisor'
if ctl start >/dev/null 2>&1; then
    fail 'start succeeded while the module was disabled'
fi
rm -f "$MODDIR/disable"
OWNS_DISABLE=false
ctl start || fail 'start after re-enabling failed'
pass 'module disable stops supervision and blocks start'

OWNS_REMOVE=true
touch "$MODDIR/remove"
wait_stopped || fail 'remove did not stop the daemon and supervisor'
if ctl start >/dev/null 2>&1; then
    fail 'start succeeded while removal was pending'
fi
rm -f "$MODDIR/remove"
OWNS_REMOVE=false
ctl start || fail 'start after cancelling removal failed'
[ "$(cat "$SENTINEL")" = "$TOKEN" ] || fail 'supervision tests changed persistent state'
pass 'pending removal stops supervision while preserving state'

printf 'Completed %s emulator smoke checks. No login was initiated.\n' "$PASSED"

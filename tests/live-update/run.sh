#!/system/bin/sh
# Invoke through scripts/test-live-update.py, which guards the Medium_Phone AVD.
# No fixture path is under /data/adb; no IP, firewall, account or cgroup operations.
[ "$(id -u)" = 0 ] || exit 1
[ "$(dirname -- "$TEST_BASE")" = /data/local/tmp ] || exit 1
case "${TEST_BASE##*/}" in sc-lu-????????????) ;; *) exit 1 ;; esac
case "${TEST_BASE##*/sc-lu-}" in *[!0-9a-f]*) exit 1 ;; esac

sc_test_fail() {
    printf 'FAIL %s: %s\n' "$TEST_CASE" "$*"
    [ -z "${sc_test_daemon:-}" ] || sc_test_cleanup
    exit 1
}
sc_test_cleanup() {
    trap - EXIT
    trap '' TERM INT HUP
    # Recovery-failure tests deliberately leave the old supervisor stopped.
    # Restore its fake tree before cleanup is allowed to resume it.
    if [ "${sc_test_needs_restore:-}" = 1 ]; then
        if [ ! -e "$TEST_STAGE" ] && [ -e "$TEST_LIVE/.sc-upgrade" ]; then
            /system/bin/mv "$TEST_LIVE" "$TEST_STAGE"
        fi
        for sc_test_trash in "$SC_DATA/trash/"*; do
            [ ! -e "$TEST_LIVE" ] && [ -d "$sc_test_trash" ] &&
                /system/bin/mv "$sc_test_trash" "$TEST_LIVE"
        done
    fi
    if sc_supervisor_running; then
        sc_test_sup=$SC_PID
        sc_test_sup_start=$SC_PID_START
        builtin kill -CONT "$sc_test_sup" 2>/dev/null
        builtin kill -TERM "$sc_test_sup" 2>/dev/null
        sc_test_wait=0
        while sc_supervisor_running && [ "$SC_PID" = "$sc_test_sup" ] &&
            [ "$SC_PID_START" = "$sc_test_sup_start" ] && [ "$sc_test_wait" -lt 50 ]; do
            sleep 0.1
            sc_test_wait=$((sc_test_wait + 1))
        done
        if sc_supervisor_running && [ "$SC_PID" = "$sc_test_sup" ] && [ "$SC_PID_START" = "$sc_test_sup_start" ]; then
            builtin kill -KILL "$sc_test_sup" 2>/dev/null
        fi
    fi
    sc_stop_daemon >/dev/null 2>&1
    # A failed rollback may prevent normal PID-file based cleanup. Independently
    # stop the original synthetic daemon by its saved identity and exact root.
    if [ "$(sc_start_time "$sc_test_daemon")" = "$sc_test_daemon_start" ]; then
        sc_test_exe=$(readlink "/proc/$sc_test_daemon/exe" 2>/dev/null)
        case "$sc_test_exe" in
            "$TEST_ROOT"/*)
                builtin kill -CONT "$sc_test_daemon" 2>/dev/null
                builtin kill -TERM "$sc_test_daemon" 2>/dev/null
                sc_test_wait=0
                while [ "$(sc_start_time "$sc_test_daemon")" = "$sc_test_daemon_start" ] && [ "$sc_test_wait" -lt 20 ]; do
                    sleep 0.1
                    sc_test_wait=$((sc_test_wait + 1))
                done
                if [ "$(sc_start_time "$sc_test_daemon")" = "$sc_test_daemon_start" ] &&
                    [ "$(readlink "/proc/$sc_test_daemon/exe" 2>/dev/null)" = "$sc_test_exe" ]; then
                    builtin kill -KILL "$sc_test_daemon" 2>/dev/null
                fi
                ;;
        esac
    fi
    wait "$sc_test_daemon" "$TEST_OLD_SUPERVISOR" 2>/dev/null
}

sc_test_case() {
    export TEST_CASE=$1
    export TEST_ROOT=$TEST_BASE/$TEST_CASE
    export TEST_LIVE=$TEST_ROOT/live TEST_STAGE=$TEST_ROOT/staged
    mkdir -p "$TEST_LIVE/bin" "$TEST_STAGE/bin" || exit 1
    cp "$TEST_BASE/common.sh" "$TEST_LIVE/common.sh" || exit 1
    cp "$TEST_BASE/common.sh" "$TEST_STAGE/common.sh" || exit 1
    cp "$TEST_BASE/old-supervisor.sh" "$TEST_LIVE/ctl.sh" || exit 1
    cp "$TEST_BASE/ctl.sh" "$TEST_STAGE/ctl.sh" || exit 1
    : > "$TEST_LIVE/cli.sh"
    : > "$TEST_STAGE/cli.sh"
    printf 'version=old\n' > "$TEST_LIVE/module.prop"
    printf 'version=new\n' > "$TEST_STAGE/module.prop"
    printf '0.0\n' > "$TEST_STAGE/.sc-upgrade"
    for sc_test_dir in "$TEST_LIVE" "$TEST_STAGE"; do
        cp "$TEST_BASE/fixture" "$sc_test_dir/bin/tailscaled" || exit 1
        cp "$TEST_BASE/fixture" "$sc_test_dir/bin/shellscaled-web" || exit 1
        chmod 0700 "$sc_test_dir/bin/"* || exit 1
    done
    SC_MODDIR=$TEST_LIVE
    . "$SC_MODDIR/common.sh" || exit 1
    sc_prepare || exit 1
    : > "$SC_STATE/defaults-initialized"
    # Fixed fixture paths contain no spaces, just like SC_DAEMON_ARGS.
    case "$TEST_CASE" in
        mode-missing) unset TS_ANDROID_TAILDROP_INBOX ;;
        mode-zero) export TS_ANDROID_TAILDROP_INBOX=0 ;;
        *) export TS_ANDROID_TAILDROP_INBOX=1 ;;
    esac
    "$TEST_LIVE/bin/tailscaled" $SC_DAEMON_ARGS </dev/null >> "$TEST_ROOT/fixture.log" 2>&1 &
    sc_test_daemon=$!
    sc_record_pid "$SC_DAEMON_PID" "$sc_test_daemon" || exit 1
    sc_test_daemon_start=$sc_record_start
    sc_record_pid "$TEST_ROOT/original-daemon.pid" "$sc_test_daemon" || exit 1
    /system/bin/sh "$TEST_LIVE/ctl.sh" supervise </dev/null >> "$TEST_ROOT/fixture.log" 2>&1 &
    export TEST_OLD_SUPERVISOR=$!
    sc_record_pid "$SC_SUPERVISOR_PID" "$TEST_OLD_SUPERVISOR" || exit 1
    trap sc_test_cleanup EXIT
    sc_test_wait=0
    until [ -S "$SC_SOCKET" ]; do
        [ "$sc_test_wait" -lt 50 ] || sc_test_fail 'synthetic daemon did not initialize'
        sleep 0.1
        sc_test_wait=$((sc_test_wait + 1))
    done
    sleep 0.2
    [ ! -e "$TEST_ROOT/lost-daemon-ownership" ] || sc_test_fail 'baseline lost daemon ownership'
    /system/bin/sh "$TEST_STAGE/ctl.sh" upgrade 0.0 0
    sc_test_result=$?
    sleep 0.2
    case "$TEST_CASE" in
        rollback-*)
            sc_test_needs_restore=1
            [ "$sc_test_result" -ne 0 ] || sc_test_fail 'failed rollback returned success'
            sc_supervisor_running && [ "$SC_PID" = "$TEST_OLD_SUPERVISOR" ] || sc_test_fail 'old supervisor identity changed'
            sc_test_state=$(cat "/proc/$SC_PID/stat")
            sc_test_state=${sc_test_state##*) }
            [ "${sc_test_state%% *}" = T ] || sc_test_fail 'failed rollback resumed the old supervisor'
            ;;
        normal|retire-*|mode-*)
            [ "$(cat "$TEST_LIVE/module.prop")" = version=new ] || sc_test_fail 'new tree is not live'
            [ ! -e "$TEST_STAGE" ] || sc_test_fail 'staged tree remains after commit'
            sc_supervisor_running || sc_test_fail 'replacement supervisor is not running'
            [ "$SC_PID" != "$TEST_OLD_SUPERVISOR" ] || sc_test_fail 'old supervisor was not retired'
            ;;
        *)
            [ "$sc_test_result" -ne 0 ] || sc_test_fail 'cancelled/failed worker returned success'
            [ "$(cat "$TEST_LIVE/module.prop")" = version=old ] || sc_test_fail 'old tree was not restored'
            [ "$(cat "$TEST_STAGE/.sc-upgrade")" = 0.0 ] || sc_test_fail 'new tree was not returned to staging'
            sc_supervisor_running && [ "$SC_PID" = "$TEST_OLD_SUPERVISOR" ] || sc_test_fail 'old supervisor is not running'
            sc_test_state=$(cat "/proc/$SC_PID/stat")
            sc_test_state=${sc_test_state##*) }
            [ "${sc_test_state%% *}" != T ] || sc_test_fail 'old supervisor remains stopped'
            ;;
    esac
    case "$TEST_CASE" in
        flag-*) [ -e "$TEST_LIVE/${TEST_CASE#flag-}" ] || sc_test_fail 'rollback lost a manager flag' ;;
    esac
    [ ! -e "$SC_LOCK" ] || sc_test_fail 'worker left its lock'
    [ ! -e "$TEST_ROOT/lost-daemon-ownership" ] || sc_test_fail 'old supervisor resumed over the wrong tree'
    sc_daemon_running || sc_test_fail 'daemon is not running'
    case "$TEST_CASE" in
        mode-*)
            [ "$SC_PID" != "$sc_test_daemon" ] || sc_test_fail 'daemon with incompatible inbox mode was adopted'
            tr '\000' '\n' < "/proc/$SC_PID/environ" | grep -qx 'TS_ANDROID_TAILDROP_INBOX=1' ||
                sc_test_fail 'replacement daemon did not enable its private inbox'
            ;;
        *)
            [ "$SC_PID" = "$sc_test_daemon" ] && [ "$SC_PID_START" = "$sc_test_daemon_start" ] ||
                sc_test_fail 'unchanged compatible daemon was replaced or lost'
            ;;
    esac
    [ "$TEST_CASE" != normal ] || [ "$sc_test_result" = 0 ] || sc_test_fail 'normal worker failed'
    sc_test_current_daemon=$SC_PID
    sc_test_current_start=$SC_PID_START
    sc_supervisor_running || sc_test_fail 'supervisor identity vanished before cleanup'
    sc_test_current_supervisor=$SC_PID
    sc_test_current_supervisor_start=$SC_PID_START
}

if [ "${1:-}" = --case ]; then
    sc_test_case "$2"
    sc_test_result=$?
    # Run explicitly before exit: mksh can warn about stopped jobs before it
    # reaches EXIT traps. The trap remains a fallback for assertion failures.
    sc_test_cleanup
    if [ "$(sc_start_time "$sc_test_daemon")" = "$sc_test_daemon_start" ]; then
        printf 'FAIL %s: original daemon remained after cleanup\n' "$TEST_CASE"
        exit 1
    fi
    if [ "$(sc_start_time "$sc_test_current_daemon")" = "$sc_test_current_start" ]; then
        sc_test_exe=$(readlink "/proc/$sc_test_current_daemon/exe" 2>/dev/null)
        case "$sc_test_exe" in "$TEST_ROOT"/*)
            printf 'FAIL %s: current daemon remained after cleanup\n' "$TEST_CASE"
            exit 1
            ;;
        esac
    fi
    if [ "$(sc_start_time "$sc_test_current_supervisor")" = "$sc_test_current_supervisor_start" ]; then
        sc_test_cmdline=$(tr '\000' ' ' < "/proc/$sc_test_current_supervisor/cmdline" 2>/dev/null)
        if [ "${sc_test_cmdline% }" = "/system/bin/sh $TEST_LIVE/ctl.sh supervise" ]; then
            printf 'FAIL %s: supervisor remained after cleanup\n' "$TEST_CASE"
            exit 1
        fi
    fi
    [ "$sc_test_result" = 0 ] && printf 'PASS %s\n' "$TEST_CASE"
    exit "$sc_test_result"
fi
sc_test_failures=0
for sc_test_name in normal mode-missing mode-zero fail-first fail-second rollback-new rollback-old flag-disable flag-remove \
    rename1-TERM rename1-INT rename1-HUP rename2-TERM rename2-INT rename2-HUP \
    retire-before-TERM retire-before-INT retire-before-HUP \
    retire-after-TERM retire-after-INT retire-after-HUP; do
    /system/bin/sh "$0" --case "$sc_test_name" || sc_test_failures=$((sc_test_failures + 1))
done
printf 'DONE failures=%s\n' "$sc_test_failures"
[ "$sc_test_failures" = 0 ]

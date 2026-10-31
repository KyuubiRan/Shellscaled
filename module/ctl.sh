#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
. "$SC_MODDIR/common.sh" || exit 1
sc_root || exit 1

sc_usage() {
    printf '%s\n' \
        'Usage: ctl.sh start|stop|restart|status|logs [daemon|service]' \
        'CLI:   /data/adb/modules/shellscaled/bin/tailscale <command>'
}

sc_status() {
    sc_status_result=0
    if sc_supervisor_running; then
        printf 'Supervisor: running (PID %s)\n' "$SC_PID"
    else
        printf 'Supervisor: stopped\n'
        sc_status_result=1
    fi
    if sc_daemon_running; then
        printf 'Daemon: running (PID %s)\n' "$SC_PID"
        sc_backend=$(timeout 5 "$SC_MODDIR/bin/tailscale" status --json 2>/dev/null |
            sed -n 's/^[[:space:]]*"BackendState":[[:space:]]*"\([A-Za-z]*\)".*/\1/p' | head -n 1)
        [ -z "$sc_backend" ] || printf 'Backend: %s\n' "$sc_backend"
    else
        printf 'Daemon: stopped\n'
        sc_status_result=1
    fi
    if [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ]; then
        printf 'Module: disabled or pending removal\n'
    fi
    return "$sc_status_result"
}

sc_start() {
    if [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ]; then
        sc_error 'The module is disabled or pending removal.'
        return 1
    fi
    if sc_legacy_active; then
        sc_error 'Disable magisk-tailscaled and reboot before starting Shellscaled.'
        return 1
    fi
    if sc_supervisor_running; then
        printf 'Shellscaled is already supervised.\n'
        return 0
    fi
    # Adopt a daemon whose binary and arguments are unchanged and whose
    # initialization finished; otherwise stop it so defaults are applied.
    if sc_daemon_running && [ -S "$SC_SOCKET" ] && [ -f "$SC_STATE/defaults-initialized" ] &&
        cmp -s "/proc/$SC_PID/exe" "$SC_MODDIR/bin/tailscaled" &&
        [ "$(tr '\000' ' ' < "/proc/$SC_PID/cmdline")" = "$SC_MODDIR/bin/tailscaled $SC_DAEMON_ARGS " ]; then
        :
    else
        sc_stop_daemon || return 1
    fi
    if sc_magicdns_running && ! { cmp -s "/proc/$SC_PID/exe" "$SC_MODDIR/bin/shellscaled-web" && sc_magicdns_jumps; }; then
        sc_magicdns_term
    fi
    rm -f "$SC_STOP" "$SC_SUPERVISOR_PID"
    sc_bound_logs
    sc_root_cgroup
    nohup /system/bin/sh "$SC_MODDIR/ctl.sh" supervise \
        </dev/null >> "$SC_LOGS/service.log" 2>&1 &
    sc_new_supervisor=$!
    if ! sc_record_pid "$SC_SUPERVISOR_PID" "$sc_new_supervisor"; then
        sc_error 'Could not record the supervisor identity.'
        return 1
    fi
    sc_start_wait=0
    while [ "$sc_start_wait" -lt 125 ]; do
        if ! sc_supervisor_running; then
            sc_error 'The supervisor exited. Inspect ctl.sh logs service.'
            return 1
        fi
        if sc_daemon_running && [ -S "$SC_SOCKET" ] && [ -f "$SC_STATE/defaults-initialized" ]; then
            printf 'Shellscaled started. Use ctl.sh status for connection state.\n'
            return 0
        fi
        sleep 0.2
        sc_start_wait=$((sc_start_wait + 1))
    done
    sc_error 'Startup is still retrying. Inspect ctl.sh status and logs.'
    return 1
}

sc_stop() {
    : > "$SC_STOP"
    if sc_supervisor_running; then
        sc_old_supervisor=$SC_PID
        sc_old_supervisor_start=$SC_PID_START
        kill -TERM "$sc_old_supervisor" 2>/dev/null || true
        sc_stop_wait=0
        while [ "$sc_stop_wait" -lt 20 ]; do
            if ! sc_supervisor_running || [ "$SC_PID" != "$sc_old_supervisor" ] || [ "$SC_PID_START" != "$sc_old_supervisor_start" ]; then
                break
            fi
            sleep 1
            sc_stop_wait=$((sc_stop_wait + 1))
        done
        if sc_supervisor_running && [ "$SC_PID" = "$sc_old_supervisor" ] && [ "$SC_PID_START" = "$sc_old_supervisor_start" ]; then
            kill -KILL "$sc_old_supervisor" 2>/dev/null || true
            sleep 1
        fi
    fi
    sc_stop_daemon || return 1
    if sc_supervisor_running; then
        sc_error 'The supervisor did not stop.'
        return 1
    fi
    rm -f "$SC_SUPERVISOR_PID"
    sc_log 'Service stopped.'
    sc_refresh_description
    printf 'Shellscaled stopped.\n'
}

sc_supervise_cleanup() {
    trap '' TERM INT HUP
    sc_magicdns_stop
    if [ -n "${sc_taildrop_pid:-}" ]; then
        kill "$sc_taildrop_pid" 2>/dev/null
        wait "$sc_taildrop_pid" 2>/dev/null
    fi
    sc_stop_daemon
    if sc_read_pid "$SC_SUPERVISOR_PID" && [ "$SC_PID" = "$$" ]; then
        rm -f "$SC_SUPERVISOR_PID"
    fi
    sc_log 'Supervisor exited.'
    sc_refresh_description
}

sc_supervise() {
    # start records our PID immediately after spawning us; do not run without
    # that ownership record or when invoked directly by accident.
    sc_register_wait=0
    while [ "$sc_register_wait" -lt 50 ]; do
        if sc_supervisor_running && [ "$SC_PID" = "$$" ]; then
            break
        fi
        sleep 0.1
        sc_register_wait=$((sc_register_wait + 1))
    done
    sc_supervisor_running && [ "$SC_PID" = "$$" ] || return 1
    trap 'exit 0' TERM INT HUP
    trap sc_supervise_cleanup EXIT
    sc_log 'Supervisor started.'
    "$SC_MODDIR/bin/shellscaled-web" taildrop-auto &
    sc_taildrop_pid=$!
    sc_failures=0
    sc_description_tick=0
    sc_magicdns_daemon_identity=
    # Keep a proxy that sc_start adopted together with its daemon.
    if sc_magicdns_running && sc_daemon_running; then
        sc_magicdns_daemon_identity="$SC_PID.$SC_PID_START"
    fi
    while ! sc_disabled; do
        if sc_legacy_active; then
            sc_log 'Conflicting magisk-tailscaled module is enabled; stopping.'
            break
        fi
        if sc_daemon_running; then
            sc_failures=0
            sc_current_daemon_identity="$SC_PID.$SC_PID_START"
            if [ "$sc_magicdns_daemon_identity" != "$sc_current_daemon_identity" ]; then
                sc_magicdns_term
                sc_magicdns_start || sc_log 'MagicDNS split resolver could not start.'
                sc_magicdns_daemon_identity="$sc_current_daemon_identity"
            else
                sc_magicdns_start || sc_log 'MagicDNS split resolver could not start.'
            fi
        elif sc_start_daemon; then
            continue
        else
            sc_failures=$((sc_failures + 1))
            sc_log "Daemon startup failed (attempt $sc_failures)."
            if [ "$sc_failures" -ge 5 ]; then
                sc_log 'Stopping automatic retries after five consecutive startup failures.'
                break
            fi
            sleep 5
        fi
        sc_bound_logs
        if ! kill -0 "$sc_taildrop_pid" 2>/dev/null; then
            "$SC_MODDIR/bin/shellscaled-web" taildrop-auto &
            sc_taildrop_pid=$!
        fi
        if [ "$sc_description_tick" -eq 0 ]; then
            sc_refresh_description
        fi
        sc_description_tick=$(((sc_description_tick + 1) % 5))
        sleep 3
    done
}

sc_upgrade_alive() {
    [ -n "$1" ] && sc_alive_start=$(sc_start_time "${1%%.*}") && [ "$sc_alive_start" = "${1#*.}" ]
}

sc_upgrade_checks() {
    [ "$(cat "$SC_STAGED/.sc-upgrade" 2>/dev/null)" = "$1" ] ||
        { sc_log 'Live update gave up: the staged module belongs to another install.'; return 1; }
    [ ! -e "$SC_STAGED/customize.sh" ] ||
        { sc_log 'Live update gave up: the installer did not finish.'; return 1; }
    # Three equal device numbers collapse to one line: mv is then a rename.
    case "$(stat -c %d /data/adb/modules /data/adb/modules_update "$SC_DATA" | sort -u)" in
        ''|*[!0-9]*) sc_log 'Live update gave up: the module directories are on different filesystems.'; return 1 ;;
    esac
}

# Live update worker (docs/hot-update.md, "Sequence"): customize.sh starts it
# from the staged tree in init's mount namespace with the installer's
# pid.starttime token and parent PID. The live update activates at reboot
# whenever this gives up before the swap.
sc_upgrade() {
    SC_STAGED=$SC_MODDIR
    SC_MODDIR=/data/adb/modules/shellscaled
    sc_root_cgroup
    sc_up_parent=
    sc_up_exe=$(readlink "/proc/$2/exe" 2>/dev/null)
    case "${sc_up_exe##*/}" in
        ksud|apd|magisk*) sc_up_parent=$2.$(sc_start_time "$2") ;;
    esac
    sc_up_wait=0
    while sc_upgrade_alive "$1" || sc_upgrade_alive "$sc_up_parent"; do
        [ "$sc_up_wait" -lt 600 ] ||
            { sc_log 'Live update gave up: the installer did not exit within 120 seconds.'; return 1; }
        sleep 0.2
        sc_up_wait=$((sc_up_wait + 1))
    done
    sc_lock || return 1
    trap sc_unlock EXIT
    sc_upgrade_checks "$1" || return 1
    rm -rf "$SC_DATA/trash"
    mkdir -p "$SC_DATA/trash"
    sc_up_sup=
    if sc_supervisor_running; then
        sc_up_sup=$SC_PID
        # mksh has no builtin sleep: a sleep child marks a safe point.
        sc_up_wait=0
        until pgrep -P "$sc_up_sup" -x sleep >/dev/null; do
            [ "$sc_up_wait" -lt 50 ] ||
                { sc_log 'Live update gave up: the supervisor did not reach a safe point.'; return 1; }
            sleep 0.2
            sc_up_wait=$((sc_up_wait + 1))
        done
        kill -STOP "$sc_up_sup"
        sc_up_taildrop=$(pgrep -P "$sc_up_sup" -f 'shellscaled-web taildrop-auto')
    fi
    sc_up_trash=
    [ ! -d "$SC_MODDIR" ] || sc_up_trash=$SC_DATA/trash/$(date +%s)
    # Rename live to trash (when present), then staged to live; undo on failure.
    if ! { [ -z "$sc_up_trash" ] || mv "$SC_MODDIR" "$sc_up_trash"; } || ! mv "$SC_STAGED" "$SC_MODDIR"; then
        [ -z "$sc_up_trash" ] || [ -d "$SC_MODDIR" ] || mv "$sc_up_trash" "$SC_MODDIR"
        [ -z "$sc_up_sup" ] || kill -CONT "$sc_up_sup"
        sc_log 'Live update gave up: a rename failed; the update activates at reboot.'
        return 1
    fi
    if [ -n "$sc_up_sup" ]; then
        # SIGKILL runs no EXIT trap: the daemon, proxy and DNS jumps stay.
        kill -KILL "$sc_up_sup"
        sc_up_wait=0
        while [ -d "/proc/$sc_up_sup" ] && [ "$sc_up_wait" -lt 50 ]; do
            sleep 0.1
            sc_up_wait=$((sc_up_wait + 1))
        done
        rm -f "$SC_SUPERVISOR_PID"
        # shellcheck disable=SC2086
        [ -z "$sc_up_taildrop" ] || kill -TERM $sc_up_taildrop 2>/dev/null
    fi
    rm -f "$SC_MODDIR/.sc-upgrade"
    # A flag in the old tree was set after the installer finished.
    for sc_up_flag in disable remove; do
        [ -z "$sc_up_trash" ] || [ ! -e "$sc_up_trash/$sc_up_flag" ] || : > "$SC_MODDIR/$sc_up_flag"
    done
    sc_cli_install
    # A fresh install has no old ctl.sh (Magisk leaves only module.prop and update).
    if [ -n "$sc_up_sup" ] || [ ! -f "$sc_up_trash/ctl.sh" ]; then
        if ! sc_start && { [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ]; }; then
            sc_magicdns_stop
            sc_stop_daemon
        fi
    fi
    sc_refresh_description
    sc_log "Live update to $(sed -n 's/^version=//p' "$SC_MODDIR/module.prop") complete."
}

# A binary update holds the normal control mutex across stop/replace/start.
# Only a direct child of this module's real helper may reuse its parent's lock;
# an environment variable alone never grants an unlocked control operation.
sc_binary_update_lock() {
    case "${SC_BINARY_UPDATE_OWNER:-}" in
        ''|*[!0-9.]*|.*|*.) return 1 ;;
    esac
    [ -d "$SC_LOCK" ] && [ ! -L "$SC_LOCK" ] || return 1
    sc_read_pid "$SC_LOCK/$SC_BINARY_UPDATE_OWNER" || return 1
    [ "$SC_PID" = "$PPID" ] || return 1
    [ "$SC_BINARY_UPDATE_OWNER" = "$SC_PID.$SC_PID_START" ] || return 1
    sc_update_parent_exe=$(readlink "/proc/$SC_PID/exe" 2>/dev/null) || return 1
    [ "$sc_update_parent_exe" = "$SC_MODDIR/bin/shellscaled-web" ]
}

case "${1:-help}" in
    status)
        sc_status
        ;;
    logs)
        case "${2:-daemon}" in
            daemon) sc_target_log=$SC_LOGS/daemon.log ;;
            service) sc_target_log=$SC_LOGS/service.log ;;
            *) sc_usage; exit 2 ;;
        esac
        [ -f "$sc_target_log" ] || { sc_error 'No log file yet.'; exit 1; }
        tail -n 80 "$sc_target_log"
        ;;
    supervise)
        sc_prepare && sc_supervise
        ;;
    upgrade)
        [ "$SC_MODDIR" = /data/adb/modules_update/shellscaled ] || exit 1
        sc_prepare || exit 1
        exec >> "$SC_LOGS/service.log" 2>&1
        sc_upgrade "${2:-}" "${3:-}"
        ;;
    start|stop|restart)
        # Run lifecycle commands in init's mount namespace (docs/hot-update.md, "Lifecycle commands").
        if ! [ /proc/$$/ns/mnt -ef /proc/1/ns/mnt ]; then
            for sc_bb in /data/adb/ksu/bin/busybox /data/adb/magisk/busybox /data/adb/ap/bin/busybox; do
                [ -x "$sc_bb" ] && exec "$sc_bb" nsenter -t 1 -m -- /system/bin/sh "$SC_MODDIR/ctl.sh" "$@"
            done
        fi
        sc_prepare || exit 1
        if ! sc_binary_update_lock; then
            sc_lock || exit 1
            trap sc_unlock EXIT
        fi
        trap 'exit 1' INT TERM HUP
        case "$1" in
            start) sc_start ;;
            stop) sc_stop ;;
            restart) sc_stop && sc_start ;;
        esac
        ;;
    help|--help|-h)
        sc_usage
        ;;
    *)
        sc_usage
        exit 2
        ;;
esac

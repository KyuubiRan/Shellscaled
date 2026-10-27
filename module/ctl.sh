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
    # A previous supervisor may have been killed during initialization.
    # Stop its verified orphan so the new supervisor applies defaults before
    # reporting readiness instead of silently adopting partial startup.
    sc_stop_daemon || return 1
    rm -f "$SC_STOP" "$SC_SUPERVISOR_PID"
    sc_bound_logs
    nohup /system/bin/sh "$SC_MODDIR/ctl.sh" supervise \
        </dev/null >> "$SC_LOGS/service.log" 2>&1 &
    sc_new_supervisor=$!
    if ! sc_record_pid "$SC_SUPERVISOR_PID" "$sc_new_supervisor"; then
        sc_error 'Could not record the supervisor identity.'
        return 1
    fi
    sc_start_wait=0
    while [ "$sc_start_wait" -lt 25 ]; do
        if ! sc_supervisor_running; then
            sc_error 'The supervisor exited. Inspect ctl.sh logs service.'
            return 1
        fi
        if sc_daemon_running && [ -S "$SC_SOCKET" ] && [ -f "$SC_STATE/defaults-initialized" ]; then
            printf 'Shellscaled started. Use ctl.sh status for connection state.\n'
            return 0
        fi
        sleep 1
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
    printf 'Shellscaled stopped.\n'
}

sc_supervise_cleanup() {
    trap '' TERM INT HUP
    sc_stop_daemon
    if sc_read_pid "$SC_SUPERVISOR_PID" && [ "$SC_PID" = "$$" ]; then
        rm -f "$SC_SUPERVISOR_PID"
    fi
    sc_log 'Supervisor exited.'
}

sc_supervise() {
    # start records our PID immediately after spawning us; do not run without
    # that ownership record or when invoked directly by accident.
    sc_register_wait=0
    while [ "$sc_register_wait" -lt 5 ]; do
        if sc_supervisor_running && [ "$SC_PID" = "$$" ]; then
            break
        fi
        sleep 1
        sc_register_wait=$((sc_register_wait + 1))
    done
    sc_supervisor_running && [ "$SC_PID" = "$$" ] || return 1
    trap 'exit 0' TERM INT HUP
    trap sc_supervise_cleanup EXIT
    sc_log 'Supervisor started.'
    sc_failures=0
    while ! sc_disabled; do
        if sc_legacy_active; then
            sc_log 'Conflicting magisk-tailscaled module is enabled; stopping.'
            break
        fi
        if sc_daemon_running; then
            sc_failures=0
        elif ! sc_start_daemon; then
            sc_failures=$((sc_failures + 1))
            sc_log "Daemon startup failed (attempt $sc_failures)."
            if [ "$sc_failures" -ge 5 ]; then
                sc_log 'Stopping automatic retries after five consecutive startup failures.'
                break
            fi
            sleep 5
        fi
        sc_bound_logs
        sleep 3
    done
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
    start|stop|restart)
        sc_prepare && sc_lock || exit 1
        trap sc_unlock EXIT
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

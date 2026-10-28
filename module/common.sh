#!/system/bin/sh
# Shared runtime helpers. The caller sets SC_MODDIR to its module directory.

SC_DATA=/data/adb/shellscaled
SC_STATE=$SC_DATA/state
SC_RUN=$SC_DATA/run
SC_LOGS=$SC_DATA/logs
SC_SOCKET=$SC_RUN/tailscaled.sock
SC_DAEMON_PID=$SC_RUN/daemon.pid
SC_SUPERVISOR_PID=$SC_RUN/supervisor.pid
SC_STOP=$SC_RUN/stop
SC_LOCK=$SC_RUN/control.lock
SC_LOCK_OWNER=

sc_error() {
    printf 'Shellscaled: %s\n' "$*" >&2
}

sc_root() {
    [ "$(id -u)" = 0 ] || { sc_error 'Run this command as root.'; return 1; }
}

sc_prepare() {
    umask 077
    for sc_dir in "$SC_DATA" "$SC_STATE" "$SC_RUN" "$SC_LOGS"; do
        if [ -L "$sc_dir" ]; then
            sc_error "Refusing a symbolic link at $sc_dir."
            return 1
        fi
        mkdir -p "$sc_dir" || return 1
        chmod 0700 "$sc_dir" || return 1
    done
}

sc_log() {
    printf '%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >> "$SC_LOGS/service.log"
}

sc_refresh_description() {
    [ -x "$SC_MODDIR/bin/shellscaled-web" ] || return 0
    timeout 8 "$SC_MODDIR/bin/shellscaled-web" refresh-description >/dev/null 2>&1 || true
}

# Field 22 is the kernel process start time. Removing '(comm)' first also
# handles spaces in the process name without matching unrelated processes.
sc_start_time() {
    [ -r "/proc/$1/stat" ] || return 1
    sc_stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
    sc_stat=${sc_stat##*) }
    printf '%s\n' "$sc_stat" | cut -d ' ' -f 20
}

sc_record_pid() {
    sc_record_start=$(sc_start_time "$2") || return 1
    [ -n "$sc_record_start" ] || return 1
    printf '%s %s\n' "$2" "$sc_record_start" > "$1.new" || return 1
    chmod 0600 "$1.new"
    mv -f "$1.new" "$1"
}

sc_read_pid() {
    SC_PID=
    SC_PID_START=
    [ -f "$1" ] && [ ! -L "$1" ] || return 1
    read -r SC_PID SC_PID_START sc_pid_extra < "$1" || return 1
    case "$SC_PID:$SC_PID_START" in *[!0-9:]*|:*|*:) return 1 ;; esac
    [ -z "$sc_pid_extra" ] && [ "$SC_PID" -gt 1 ] || return 1
    kill -0 "$SC_PID" 2>/dev/null || return 1
    [ "$(sc_start_time "$SC_PID")" = "$SC_PID_START" ]
}

sc_daemon_running() {
    sc_read_pid "$SC_DAEMON_PID" || return 1
    sc_exe=$(readlink "/proc/$SC_PID/exe" 2>/dev/null) || return 1
    case "$sc_exe" in
        "$SC_MODDIR/bin/tailscaled"|"$SC_MODDIR/bin/tailscaled (deleted)") return 0 ;;
    esac
    return 1
}

sc_supervisor_running() {
    sc_read_pid "$SC_SUPERVISOR_PID" || return 1
    sc_cmdline=$(tr '\000' ' ' < "/proc/$SC_PID/cmdline" 2>/dev/null) || return 1
    [ "$sc_cmdline" = "/system/bin/sh $SC_MODDIR/ctl.sh supervise " ]
}

sc_disabled() {
    if [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ]; then
        sc_cli_remove >/dev/null 2>&1 || true
        return 0
    fi
    [ -e "$SC_STOP" ]
}

sc_legacy_active() {
    [ -d /data/adb/modules/magisk-tailscaled ] &&
        [ ! -e /data/adb/modules/magisk-tailscaled/disable ] &&
        [ ! -e /data/adb/modules/magisk-tailscaled/remove ]
}

# Only the process that owns an exact PID/start-time token removes its lock.
# Empty locks are left in place rather than stealing a lock from a process
# between mkdir and writing its owner; the next boot clears an empty lock.
sc_lock() {
    sc_lock_attempt=0
    sc_own_start=$(sc_start_time "$$") || return 1
    SC_LOCK_OWNER="$$.$sc_own_start"
    while [ "$sc_lock_attempt" -lt 15 ]; do
        if mkdir "$SC_LOCK" 2>/dev/null; then
            printf '%s %s\n' "$$" "$sc_own_start" > "$SC_LOCK/$SC_LOCK_OWNER" || return 1
            return 0
        fi
        for sc_owner_file in "$SC_LOCK"/*; do
            [ -f "$sc_owner_file" ] && [ ! -L "$sc_owner_file" ] || continue
            if ! sc_read_pid "$sc_owner_file"; then
                rm -f "$sc_owner_file"
                rmdir "$SC_LOCK" 2>/dev/null || true
            fi
        done
        sc_lock_attempt=$((sc_lock_attempt + 1))
        sleep 1
    done
    sc_error 'Another control command is running, or its lock is incomplete. Retry after it exits or reboot.'
    return 1
}

sc_unlock() {
    [ -n "$SC_LOCK_OWNER" ] || return 0
    rm -f "$SC_LOCK/$SC_LOCK_OWNER"
    rmdir "$SC_LOCK" 2>/dev/null || true
    SC_LOCK_OWNER=
}

# Truncate the existing inode: the daemon opens its log in append mode,
# so truncation cannot create a sparse file or leave an unlinked growing log.
sc_bound_logs() {
    for sc_log_file in "$SC_LOGS/daemon.log" "$SC_LOGS/service.log"; do
        [ -f "$sc_log_file" ] && [ ! -L "$sc_log_file" ] || continue
        sc_log_size=$(wc -c < "$sc_log_file")
        if [ "$sc_log_size" -gt 1048576 ]; then
            tail -c 65536 "$sc_log_file" > "$sc_log_file.trim" &&
                cat "$sc_log_file.trim" > "$sc_log_file"
            rm -f "$sc_log_file.trim"
        fi
    done
}

sc_stop_daemon() {
    if ! sc_daemon_running; then
        rm -f "$SC_DAEMON_PID"
        return 0
    fi
    sc_stop_pid=$SC_PID
    sc_stop_start=$SC_PID_START
    kill -TERM "$sc_stop_pid" 2>/dev/null || true
    sc_wait=0
    while [ "$sc_wait" -lt 15 ]; do
        if ! sc_daemon_running || [ "$SC_PID" != "$sc_stop_pid" ] || [ "$SC_PID_START" != "$sc_stop_start" ]; then
            rm -f "$SC_DAEMON_PID" "$SC_SOCKET"
            return 0
        fi
        sleep 1
        sc_wait=$((sc_wait + 1))
    done
    if sc_daemon_running && [ "$SC_PID" = "$sc_stop_pid" ] && [ "$SC_PID_START" = "$sc_stop_start" ]; then
        sc_log 'Daemon did not exit after SIGTERM; sending SIGKILL to its verified PID.'
        kill -KILL "$sc_stop_pid" 2>/dev/null || true
        sleep 1
    fi
    if sc_daemon_running; then
        sc_error 'The verified daemon did not stop.'
        return 1
    fi
    rm -f "$SC_DAEMON_PID" "$SC_SOCKET"
}

sc_start_daemon() {
    sc_daemon_running && return 0
    [ -x "$SC_MODDIR/bin/tailscaled" ] || { sc_log 'The daemon binary is missing.'; return 1; }
    rm -f "$SC_DAEMON_PID" "$SC_SOCKET"
    "$SC_MODDIR/bin/tailscaled" \
        --tun=tailscale0 \
        --state="$SC_STATE/tailscaled.state" \
        --statedir="$SC_STATE" \
        --socket="$SC_SOCKET" \
        --no-logs-no-support \
        </dev/null >> "$SC_LOGS/daemon.log" 2>&1 &
    sc_new_pid=$!
    sc_record_pid "$SC_DAEMON_PID" "$sc_new_pid" || return 1
    sc_ready_deadline=$(($(date +%s) + 20))
    while [ "$(date +%s)" -lt "$sc_ready_deadline" ]; do
        sc_daemon_running || { sc_log 'Daemon exited before becoming ready.'; return 1; }
        sc_disabled && { sc_stop_daemon; return 1; }
        if [ -S "$SC_SOCKET" ]; then
            if [ -f "$SC_STATE/defaults-initialized" ] ||
                timeout 2 "$SC_MODDIR/bin/tailscale" set --accept-dns=false --netfilter-mode=off >/dev/null 2>&1; then
                : > "$SC_STATE/defaults-initialized"
                chmod 0600 "$SC_STATE/defaults-initialized"
                sc_log 'Daemon ready. DNS integration and netfilter management default to disabled.'
                return 0
            fi
        fi
        sleep 1
    done
    sc_log 'Daemon did not initialize within 20 seconds.'
    sc_stop_daemon
    return 1
}

. "$SC_MODDIR/cli.sh"

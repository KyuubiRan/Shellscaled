#!/system/bin/sh
# Only manager-provided PATH locations are eligible. No profile, system
# overlay, shared SDK, or general service directory is modified.

sc_cli_owned_link() {
    [ -L "$1" ] && [ "$(/system/bin/readlink -- "$1")" = "$SC_MODDIR/cli-entry.sh" ]
}

sc_cli_mount_info() {
    local sc_cli_mount_id sc_cli_mount_parent sc_cli_device sc_cli_root sc_cli_point sc_cli_flags sc_cli_tail sc_cli_fs sc_cli_source
    while IFS=' ' read -r sc_cli_mount_id sc_cli_mount_parent sc_cli_device sc_cli_root sc_cli_point sc_cli_flags sc_cli_tail; do
        [ "$sc_cli_point" = "$1" ] || continue
        case "$sc_cli_tail" in
            '- '*) sc_cli_tail=${sc_cli_tail#- } ;;
            *' - '*) sc_cli_tail=${sc_cli_tail#* - } ;;
            *) continue ;;
        esac
        sc_cli_fs=${sc_cli_tail%% *}
        sc_cli_tail=${sc_cli_tail#* }
        sc_cli_source=${sc_cli_tail%% *}
        printf '%s %s %s %s\n' "$sc_cli_device" "$sc_cli_root" "$sc_cli_fs" "$sc_cli_source"
        return 0
    done < /proc/self/mountinfo
}

sc_cli_magisk_directory() {
    case "$1" in /system/*|/system_ext/*|/product/*|/vendor/*) ;; *) return 1 ;; esac
    [ -d "$1" ] && [ ! -L "$1" ] || return 1
    [ "$(readlink -- "$1/su" 2>/dev/null)" = ./magisk ] || return 1
    [ -x "$1/magisk" ] || return 1
    local sc_cli_info sc_cli_tmp sc_cli_bin_inode sc_cli_tmp_inode
    sc_cli_info=$(sc_cli_mount_info "$1")
    case "$sc_cli_info" in *' tmpfs magisk') ;; *) return 1 ;; esac
    sc_cli_tmp=$("$1/magisk" --path 2>/dev/null) || return 1
    case "$sc_cli_tmp" in /*) ;; *) return 1 ;; esac
    sc_cli_bin_inode=$(stat -Lc '%d:%i' "$1/magisk" 2>/dev/null) || return 1
    sc_cli_tmp_inode=$(stat -Lc '%d:%i' "$sc_cli_tmp/magisk" 2>/dev/null) || return 1
    [ "$sc_cli_bin_inode" = "$sc_cli_tmp_inode" ]
}

sc_cli_find_directory() {
    # KSU sets this for module scripts and appends BINARY_DIR in both its su
    # shell and su -c implementation. A leftover KSU directory proves nothing.
    if [ "${KSU:-}" = true ] || [ "${KSU:-}" = 1 ]; then
        [ -d /data/adb/ksu/bin ] && [ ! -L /data/adb/ksu/bin ] || return 1
        printf '%s\n' /data/adb/ksu/bin
        return 0
    fi
    local sc_cli_search sc_cli_dir sc_cli_old_ifs
    sc_cli_search="$PATH:/system/bin:/system/xbin:/system_ext/bin:/product/bin:/vendor/bin:/vendor/xbin"
    sc_cli_old_ifs=$IFS
    IFS=:
    for sc_cli_dir in $sc_cli_search; do
        IFS=$sc_cli_old_ifs
        if sc_cli_magisk_directory "$sc_cli_dir"; then
            printf '%s\n' "$sc_cli_dir"
            return 0
        fi
        IFS=:
    done
    IFS=$sc_cli_old_ifs
    return 1
}

sc_cli_no_mounts() {
    local sc_cli_mount_id sc_cli_mount_parent sc_cli_device sc_cli_root sc_cli_point sc_cli_tail
    [ -r /proc/self/mountinfo ] || return 1
    while IFS=' ' read -r sc_cli_mount_id sc_cli_mount_parent sc_cli_device sc_cli_root sc_cli_point sc_cli_tail; do
        case "$sc_cli_point" in "$SC_DATA"|"$SC_DATA"/*) return 1 ;; esac
    done < /proc/self/mountinfo || return 1
    return 0
}

sc_cli_path_conflict() {
    local sc_cli_check_name sc_cli_check_dir sc_cli_check_ifs
    sc_cli_check_ifs=$IFS
    for sc_cli_check_name in tailscale tailscaled; do
        IFS=:
        for sc_cli_check_dir in $PATH; do
            IFS=$sc_cli_check_ifs
            [ -n "$sc_cli_check_dir" ] || sc_cli_check_dir=.
            if [ -f "$sc_cli_check_dir/$sc_cli_check_name" ] &&
                [ -x "$sc_cli_check_dir/$sc_cli_check_name" ] &&
                ! sc_cli_owned_link "$sc_cli_check_dir/$sc_cli_check_name"; then
                sc_error "Preserving an existing $sc_cli_check_name command in PATH."
                return 0
            fi
            IFS=:
        done
        IFS=$sc_cli_check_ifs
    done
    return 1
}

sc_cli_alias_cleanup() {
    local sc_cli_alias="$SC_RUN/cli-mount" sc_cli_alias_info sc_cli_source_inode sc_cli_alias_inode
    sc_cli_alias_info=$(sc_cli_mount_info "$sc_cli_alias")
    if [ -n "$sc_cli_alias_info" ]; then
        # Never detach an unknown mount, even at our private staging path.
        case "$sc_cli_alias_info" in *' tmpfs magisk') ;; *) return 1 ;; esac
        sc_cli_source_inode=$(stat -Lc '%d:%i' "$sc_cli_target" 2>/dev/null) || return 1
        sc_cli_alias_inode=$(stat -Lc '%d:%i' "$sc_cli_alias" 2>/dev/null) || return 1
        [ "$sc_cli_source_inode" = "$sc_cli_alias_inode" ] || return 1
        /system/bin/umount "$sc_cli_alias" || return 1
    fi
    [ ! -e "$sc_cli_alias" ] || rmdir "$sc_cli_alias" || return 1
}

sc_cli_open_directory() {
    [ -d "$sc_cli_target" ] && [ ! -L "$sc_cli_target" ] || return 1
    [ "$(readlink -f -- "$sc_cli_target")" = "$sc_cli_target" ] || return 1
    if [ "$sc_cli_target" = /data/adb/ksu/bin ]; then
        sc_cli_work=$sc_cli_target
        return 0
    fi
    sc_cli_magisk_directory "$sc_cli_target" || return 1
    [ ! -L "$SC_RUN/cli-mount" ] || return 1
    sc_cli_alias_cleanup || return 1
    mkdir -m 0700 "$SC_RUN/cli-mount" || return 1
    # The original manager injection remains read-only. Only this temporary
    # alias has its per-mount flag changed. SC_RUN is always root-only (0700).
    /system/bin/mount --bind "$sc_cli_target" "$SC_RUN/cli-mount" || return 1
    /system/bin/mount -o remount,bind,rw "$SC_RUN/cli-mount" || return 1
    sc_cli_work=$SC_RUN/cli-mount
}

sc_cli_unlock() {
    [ -n "${sc_cli_owner:-}" ] || return 0
    rm -f "$SC_RUN/cli-link.lock/$sc_cli_owner"
    rmdir "$SC_RUN/cli-link.lock" 2>/dev/null || true
}

sc_cli_lock() {
    local sc_cli_pid sc_cli_rest sc_cli_start sc_cli_file sc_cli_old_pid sc_cli_old_start
    # A builtin read resolves /proc/self for this subshell, unlike $$ (which
    # some shells retain from the parent).
    read -r sc_cli_pid sc_cli_rest < /proc/self/stat || return 1
    sc_cli_start=$(sc_start_time "$sc_cli_pid") || return 1
    sc_cli_owner=$sc_cli_pid.$sc_cli_start
    if ! mkdir "$SC_RUN/cli-link.lock" 2>/dev/null; then
        for sc_cli_file in "$SC_RUN/cli-link.lock"/*; do
            [ -f "$sc_cli_file" ] && [ ! -L "$sc_cli_file" ] || continue
            sc_cli_old_pid=${sc_cli_file##*/}
            sc_cli_old_start=${sc_cli_old_pid#*.}
            sc_cli_old_pid=${sc_cli_old_pid%%.*}
            case "$sc_cli_old_pid:$sc_cli_old_start" in *[!0-9:]*|:*|*:) continue ;; esac
            # Respect even a not-yet-written owner file while its exact
            # process is alive. Never steal an incomplete empty directory.
            if ! kill -0 "$sc_cli_old_pid" 2>/dev/null ||
                [ "$(sc_start_time "$sc_cli_old_pid")" != "$sc_cli_old_start" ]; then
                rm -f "$sc_cli_file"
                rmdir "$SC_RUN/cli-link.lock" 2>/dev/null || true
            fi
        done
        mkdir "$SC_RUN/cli-link.lock" 2>/dev/null || return 1
    fi
    printf '%s %s\n' "$sc_cli_pid" "$sc_cli_start" > "$SC_RUN/cli-link.lock/$sc_cli_owner" || return 1
}

sc_cli_change_links() (
    sc_cli_operation=$1
    sc_cli_target=$2
    sc_cli_owner=
    sc_cli_work=
    case "$sc_cli_target" in /*) ;; *) exit 1 ;; esac
    case "$sc_cli_target" in *[!a-zA-Z0-9_./-]*) exit 1 ;; esac
    [ ! -L "$SC_DATA/cli-link-dir" ] && [ ! -L "$SC_DATA/cli-link-dir.new" ] || exit 1
    sc_prepare && sc_cli_lock || exit 1
    trap 'sc_cli_result=$?; sc_cli_alias_cleanup || sc_cli_result=1; sc_cli_unlock; exit "$sc_cli_result"' EXIT
    trap 'exit 1' HUP INT TERM
    case "$sc_cli_operation" in
        install)
            [ ! -e "$SC_MODDIR/disable" ] && [ ! -e "$SC_MODDIR/remove" ] || exit 1
            sc_cli_path_conflict && exit 1
            for sc_cli_command in tailscale tailscaled; do
                sc_cli_path=$sc_cli_target/$sc_cli_command
                if { [ -e "$sc_cli_path" ] || [ -L "$sc_cli_path" ]; } && ! sc_cli_owned_link "$sc_cli_path"; then
                    sc_error "Preserving an existing $sc_cli_command entry in $sc_cli_target."
                    exit 1
                fi
            done
            # Record ownership before creating links, so an interrupted
            # installation remains recoverable without guessing directories.
            printf '%s\n' "$sc_cli_target" > "$SC_DATA/cli-link-dir.new" &&
                chmod 0600 "$SC_DATA/cli-link-dir.new" &&
                mv -f "$SC_DATA/cli-link-dir.new" "$SC_DATA/cli-link-dir" || exit 1
            sc_cli_open_directory || exit 1
            for sc_cli_command in tailscale tailscaled; do
                sc_cli_owned_link "$sc_cli_work/$sc_cli_command" && continue
                if ! ln -s "$SC_MODDIR/cli-entry.sh" "$sc_cli_work/$sc_cli_command"; then
                    for sc_cli_command in tailscale tailscaled; do
                        sc_cli_owned_link "$sc_cli_work/$sc_cli_command" && rm -f "$sc_cli_work/$sc_cli_command"
                    done
                    exit 1
                fi
            done
            ;;
        remove)
            if sc_cli_owned_link "$sc_cli_target/tailscale" || sc_cli_owned_link "$sc_cli_target/tailscaled"; then
                sc_cli_open_directory || exit 1
                for sc_cli_command in tailscale tailscaled; do
                    if sc_cli_owned_link "$sc_cli_work/$sc_cli_command"; then
                        rm -f "$sc_cli_work/$sc_cli_command" || exit 1
                    fi
                done
            fi
            # Do not discard recovery metadata while a private bind alias
            # could still expose the manager's directory to recursive rm.
            sc_cli_alias_cleanup && sc_cli_no_mounts || exit 1
            rm -f "$SC_DATA/cli-link-dir"
            ;;
        *) exit 1 ;;
    esac
)

sc_cli_remove() {
    if [ ! -e "$SC_DATA/cli-link-dir" ] && [ ! -L "$SC_DATA/cli-link-dir" ]; then
        sc_cli_no_mounts && return 0
        # A killed installer may have mounted its alias before writing any
        # marker. Recover only if today's actual manager independently proves
        # the source; an unknown mount always leaves private data intact.
        local sc_cli_recovery
        sc_cli_recovery=$(sc_cli_find_directory) || return 1
        sc_cli_change_links remove "$sc_cli_recovery"
        return $?
    fi
    [ -f "$SC_DATA/cli-link-dir" ] && [ ! -L "$SC_DATA/cli-link-dir" ] || return 1
    local sc_cli_record
    sc_cli_record=$(cat "$SC_DATA/cli-link-dir") || return 1
    sc_cli_change_links remove "$sc_cli_record"
}

sc_cli_install() {
    local sc_cli_destination
    [ ! -e "$SC_MODDIR/disable" ] && [ ! -e "$SC_MODDIR/remove" ] || return 1
    sc_cli_destination=$(sc_cli_find_directory) || {
        sc_error 'No verified root-manager PATH directory is available for global commands.'
        return 1
    }
    if [ -e "$SC_DATA/cli-link-dir" ] && [ "$(cat "$SC_DATA/cli-link-dir")" != "$sc_cli_destination" ]; then
        sc_cli_remove || return 1
    fi
    sc_cli_change_links install "$sc_cli_destination"
}

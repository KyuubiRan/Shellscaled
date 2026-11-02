#!/system/bin/sh
# Kill only fixture ELF images below each exact, validated task directory.
# This also works after an earlier runner removed the directory: /proc/exe
# retains its original path with a " (deleted)" suffix.
[ "$(id -u)" = 0 ] || exit 1
sc_cleanup_start() {
    sc_cleanup_stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
    sc_cleanup_stat=${sc_cleanup_stat##*) }
    printf '%s\n' "$sc_cleanup_stat" | cut -d ' ' -f 20
}
sc_cleanup_owned() {
    [ "$(sc_cleanup_start "$1")" = "$2" ] || return 1
    sc_cleanup_exe=$(readlink "/proc/$1/exe" 2>/dev/null) || return 1
    case "$sc_cleanup_exe" in "$sc_cleanup_root"/*) return 0 ;; esac
    return 1
}
sc_cleanup_collect() {
    sc_cleanup_targets=
    for sc_cleanup_proc in /proc/[0-9]*; do
        sc_cleanup_exe=$(readlink "$sc_cleanup_proc/exe" 2>/dev/null) || continue
        case "$sc_cleanup_exe" in "$sc_cleanup_root"/*) ;; *) continue ;; esac
        sc_cleanup_pid=${sc_cleanup_proc##*/}
        sc_cleanup_time=$(sc_cleanup_start "$sc_cleanup_pid") || continue
        sc_cleanup_owned "$sc_cleanup_pid" "$sc_cleanup_time" || continue
        sc_cleanup_targets="$sc_cleanup_targets $sc_cleanup_pid.$sc_cleanup_time"
    done
}
sc_cleanup_failed=0
for sc_cleanup_root in "$@"; do
    [ "$(dirname -- "$sc_cleanup_root")" = /data/local/tmp ] || exit 1
    sc_cleanup_base=${sc_cleanup_root##*/}
    case "$sc_cleanup_base" in
        sc-lu-????????????) sc_cleanup_hex=${sc_cleanup_base#sc-lu-} ;;
        shellscaled-live-update-????????????????????????????????)
            sc_cleanup_hex=${sc_cleanup_base#shellscaled-live-update-} ;;
        *) exit 1 ;;
    esac
    case "$sc_cleanup_hex" in *[!0-9a-f]*) exit 1 ;; esac
    sc_cleanup_collect
    for sc_cleanup_token in $sc_cleanup_targets; do
        sc_cleanup_pid=${sc_cleanup_token%%.*}
        sc_cleanup_time=${sc_cleanup_token#*.}
        if sc_cleanup_owned "$sc_cleanup_pid" "$sc_cleanup_time"; then
            kill -CONT "$sc_cleanup_pid" 2>/dev/null
            kill -TERM "$sc_cleanup_pid" 2>/dev/null
        fi
    done
    sc_cleanup_wait=0
    while [ -n "$sc_cleanup_targets" ] && [ "$sc_cleanup_wait" -lt 10 ]; do
        sleep 0.1
        sc_cleanup_collect
        sc_cleanup_wait=$((sc_cleanup_wait + 1))
    done
    for sc_cleanup_token in $sc_cleanup_targets; do
        sc_cleanup_pid=${sc_cleanup_token%%.*}
        sc_cleanup_time=${sc_cleanup_token#*.}
        sc_cleanup_owned "$sc_cleanup_pid" "$sc_cleanup_time" && kill -KILL "$sc_cleanup_pid" 2>/dev/null
    done
    sc_cleanup_wait=0
    while [ -n "$sc_cleanup_targets" ] && [ "$sc_cleanup_wait" -lt 10 ]; do
        sleep 0.1
        sc_cleanup_collect
        sc_cleanup_wait=$((sc_cleanup_wait + 1))
    done
    if [ -n "$sc_cleanup_targets" ]; then
        printf 'CLEANUP remaining fixture binaries\n' >&2
        sc_cleanup_failed=1
    else
        printf 'CLEANUP remaining=0\n'
        rm -rf "$sc_cleanup_root"
    fi
done
[ "$sc_cleanup_failed" = 0 ]

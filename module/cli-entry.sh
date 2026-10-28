#!/system/bin/sh
# PATH entries point here rather than directly at bin/tailscale: that wrapper
# locates its payload relative to its invocation path.

[ "$(id -u)" = 0 ] || { printf '%s\n' 'Shellscaled: Run this command as root.' >&2; exit 1; }
sc_cli_name=${0##*/}
sc_cli_entry=$(readlink -f -- "$0") || exit 1
SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$sc_cli_entry")" && pwd -P) || exit 1
[ -f "$SC_MODDIR/module.prop" ] &&
    [ "$(sed -n 's/^id=//p' "$SC_MODDIR/module.prop")" = shellscaled ] || exit 1
if [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ]; then
    . "$SC_MODDIR/common.sh"
    sc_cli_remove >/dev/null 2>&1 || true
    sc_error 'The module is disabled or marked for removal.'
    exit 1
fi

case "$sc_cli_name" in
    tailscale)
        for sc_cli_arg in "$@"; do
            case "$sc_cli_arg" in
                --socket|--socket=*|-socket|-socket=*)
                    printf '%s\n' 'Shellscaled: This command uses the module socket; --socket cannot be overridden.' >&2
                    exit 2
                    ;;
            esac
        done
        exec "$SC_MODDIR/bin/tailscale" "$@"
        ;;
    tailscaled)
        export TS_BE_CLI=0
        [ "$#" != 0 ] || exec /system/bin/sh "$SC_MODDIR/ctl.sh" start
        case "${1:-}" in
            start|stop|restart|status)
                [ "$#" != 1 ] || exec /system/bin/sh "$SC_MODDIR/ctl.sh" "$@"
                ;;
            logs)
                [ "$#" != 1 ] || exec /system/bin/sh "$SC_MODDIR/ctl.sh" logs
                if [ "$#" = 2 ]; then
                    case "$2" in daemon|service) exec /system/bin/sh "$SC_MODDIR/ctl.sh" "$@" ;; esac
                fi
                ;;
            --help|-h|--version)
                if [ "$#" = 1 ]; then
                    exec "$SC_MODDIR/bin/tailscaled" "$@"
                fi
                ;;
        esac
        printf '%s\n' \
            'Shellscaled: tailscaled is managed by the module.' \
            'Use: tailscaled [start|stop|restart|status|logs [daemon|service]]' \
            'Diagnostics: tailscaled --help | --version' \
            'Other daemon flags require the module-local raw binary and are not accepted by this managed entry.' >&2
        exit 2
        ;;
    *) printf '%s\n' 'Shellscaled: Invoke this entry as tailscale or tailscaled.' >&2; exit 2 ;;
esac

#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
sc_boot_wait=0
while [ "$(getprop sys.boot_completed)" != 1 ]; do
    [ -e "$SC_MODDIR/disable" ] || [ -e "$SC_MODDIR/remove" ] && exit 0
    [ "$sc_boot_wait" -lt 120 ] || exit 1
    sleep 1
    sc_boot_wait=$((sc_boot_wait + 1))
done

# An empty lock can only result from interruption before its owner was
# recorded. At boot no previous control process can still own it.
rmdir /data/adb/shellscaled/run/control.lock 2>/dev/null || true
rmdir /data/adb/shellscaled/run/cli-link.lock 2>/dev/null || true
# Old module trees kept by the last live update (docs/hot-update.md).
rm -rf /data/adb/shellscaled/trash
. "$SC_MODDIR/common.sh" || exit 1
sc_prepare || exit 1
sc_cli_install || sc_error 'Global commands were not installed; module-local commands remain available.'
exec /system/bin/sh "$SC_MODDIR/ctl.sh" start

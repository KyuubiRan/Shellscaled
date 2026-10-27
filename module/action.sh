#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
/system/bin/sh "$SC_MODDIR/ctl.sh" status
printf '\n%s\n' \
    'Start: su -c /data/adb/modules/shellscaled/ctl.sh start' \
    'Login: su -c "/data/adb/modules/shellscaled/bin/tailscale login"' \
    'Open the URL printed by login to authorize this device.'
exit 0

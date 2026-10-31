#!/system/bin/sh
# The manager extracts the offline payload before sourcing this script.
SKIPMOUNT=true
export MODULE_HOT_INSTALL_REQUEST=false

[ "$(id -u)" = 0 ] || abort '! Root is required.'
[ "${BOOTMODE:-false}" = true ] || abort '! Install from Magisk or KernelSU Manager while Android is running.'
[ -n "${KSU:-}" ] || [ -n "${MAGISK_VER_CODE:-}" ] || abort '! A supported root manager was not detected.'
[ -n "${MODPATH:-}" ] && [ -d "$MODPATH" ] || abort '! Missing module install directory.'

if [ -d /data/adb/modules/magisk-tailscaled ] &&
    [ ! -e /data/adb/modules/magisk-tailscaled/disable ] &&
    [ ! -e /data/adb/modules/magisk-tailscaled/remove ]; then
    abort '! Disable magisk-tailscaled and reboot before installing Shellscaled.'
fi

[ -f "$MODPATH/SHA256SUMS" ] || abort '! Missing SHA256SUMS.'
(cd "$MODPATH" && sha256sum -c SHA256SUMS >/dev/null 2>&1) || abort '! Offline payload checksum verification failed.'

sc_payload_arch=$(sed -n 's/^architecture=//p' "$MODPATH/module.prop")
case "$(getprop ro.product.cpu.abi)" in
    arm64-v8a) sc_device_arch=arm64 ;;
    armeabi-v7a|armeabi) sc_device_arch=arm ;;
    x86_64) sc_device_arch=amd64 ;;
    x86) sc_device_arch=386 ;;
    *) abort '! Unsupported device architecture.' ;;
esac
[ "$sc_payload_arch" = "$sc_device_arch" ] || abort '! This ZIP was built for a different architecture.'
[ -s "$MODPATH/bin/tailscaled" ] && [ -f "$MODPATH/bin/tailscale" ] || abort '! Missing Tailscale payload.'

# KernelSU sets webroot permissions and context itself. Leave that tree alone.
for sc_item in "$MODPATH"/*; do
    [ "${sc_item##*/}" = webroot ] && continue
    if [ -d "$sc_item" ]; then
        set_perm_recursive "$sc_item" 0 0 0755 0644
    else
        set_perm "$sc_item" 0 0 0644
    fi
done
for sc_executable in service.sh action.sh ctl.sh uninstall.sh cli-entry.sh bin/tailscaled bin/tailscale bin/shellscaled-web; do
    set_perm "$MODPATH/$sc_executable" 0 0 0755
done
touch "$MODPATH/skip_mount"

ui_print '- Offline payload and architecture verified.'
ui_print '- Private state is preserved across upgrades.'
# Live update (docs/hot-update.md): flags are read now; the installer clears them later.
if [ -e /data/adb/modules/shellscaled/disable ] || [ -e /data/adb/modules/shellscaled/remove ]; then
    ui_print '- Module is disabled or pending removal: the update activates on reboot.'
else
    sc_bb=$(readlink /proc/$$/exe)
    sc_tok="$$.$(sed 's/.*) //' /proc/$$/stat | cut -d' ' -f20)"
    echo "$sc_tok" > "$MODPATH/.sc-upgrade"
    "$sc_bb" setsid "$sc_bb" nsenter -t 1 -m -- /system/bin/sh "$MODPATH/ctl.sh" \
        upgrade "$sc_tok" "$PPID" </dev/null >/dev/null 2>&1 &
    ui_print '- Live update scheduled: applies seconds after installation, no reboot.'
fi
ui_print '- Tap Run in the module list to connect or disconnect.'

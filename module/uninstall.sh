#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
if ! /system/bin/sh "$SC_MODDIR/ctl.sh" stop; then
    printf '%s\n' 'Shellscaled could not stop. Its private state was retained.' >&2
    exit 1
fi
. "$SC_MODDIR/common.sh" || exit 1
if ! sc_cli_remove; then
    sc_error 'Global command cleanup could not finish. Private ownership metadata was retained.'
    exit 1
fi
# Serialize the final mount check and deletion with any boot installer that
# was already in flight before the manager marked this module for removal.
if ! sc_prepare || ! sc_cli_lock; then
    sc_error 'Global command setup is still active or incomplete. Private data was retained.'
    exit 1
fi
trap sc_cli_unlock EXIT
trap 'exit 1' HUP INT TERM
if ! sc_cli_no_mounts; then
    sc_error 'Private data contains an active mount. Unmount it or reboot before uninstalling; data was retained.'
    exit 1
fi

# This is the only persistent directory owned by this module. Never remove
# legacy-module state, shared service directories, or unrelated binaries.
rm -rf /data/adb/shellscaled

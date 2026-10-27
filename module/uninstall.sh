#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
if ! /system/bin/sh "$SC_MODDIR/ctl.sh" stop; then
    printf '%s\n' 'Shellscaled could not stop. Its private state was retained.' >&2
    exit 1
fi

# This is the only persistent directory owned by this module. Never remove
# legacy-module state, shared service directories, or unrelated binaries.
rm -rf /data/adb/shellscaled

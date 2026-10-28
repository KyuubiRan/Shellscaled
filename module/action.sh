#!/system/bin/sh

SC_MODDIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P) || exit 1
exec "$SC_MODDIR/bin/shellscaled-web" toggle

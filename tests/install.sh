#!/system/bin/sh
# Run through scripts/emulator.py. Emulate manager callbacks using a private
# payload; never install a real module, start a daemon or access account state.
set -eu
[ "$(getprop ro.kernel.qemu)" = 1 ] || { echo 'Emulator required.' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'Root required.' >&2; exit 1; }
sc_test_payload=${1:-/data/adb/modules/shellscaled}
sc_test_root=/data/local/tmp/shellscaled-install-test
[ ! -e "$sc_test_root" ] && [ ! -L "$sc_test_root" ] || { echo 'Test directory already exists.' >&2; exit 1; }
mkdir -m 0700 "$sc_test_root"
trap 'rm -rf "$sc_test_root"' EXIT
trap 'exit 1' HUP INT TERM
mkdir "$sc_test_root/module" "$sc_test_root/module/bin"
MODPATH=$sc_test_root/module
printf '%s\n' id=shellscaled architecture=arm64 > "$MODPATH/module.prop"
for sc_test_file in service.sh action.sh ctl.sh uninstall.sh cli-entry.sh bin/tailscaled bin/tailscale bin/shellscaled-web; do
    printf '%s\n' '#!/system/bin/sh' 'exit 0' > "$MODPATH/$sc_test_file"
done
sc_test_checksums() {
    (cd "$MODPATH" && sha256sum module.prop *.sh bin/* > SHA256SUMS)
}
sc_test_checksums
sc_test_install() (
    APATCH=$1 KSU=$2 MAGISK_VER_CODE=$3 BOOTMODE=${4:-true}
    abort() { printf '%s\n' "$*" >&2; exit 1; }
    ui_print() { :; }
    getprop() { printf '%s\n' arm64-v8a; }
    set_perm_recursive() { :; }
    set_perm() { chmod "$4" "$1"; }
    . "$sc_test_payload/customize.sh"
)
sc_test_reject() {
    if sc_test_install "$@" > "$sc_test_root/rejected" 2>&1; then
        echo 'Unexpected installer success.' >&2
        exit 1
    fi
    grep -q "$sc_test_error" "$sc_test_root/rejected"
}

# APatch works without either manager's compatibility variables. KSU and
# Magisk retain their existing installer behavior.
sc_test_install true '' ''
[ -f "$MODPATH/skip_mount" ] && [ -x "$MODPATH/bin/tailscaled" ]
sc_test_install 1 '' ''
sc_test_install '' true ''
sc_test_install '' '' 30000
sc_test_error='supported root manager'
sc_test_reject '' '' ''
sc_test_reject false '' ''
sc_test_error='while Android is running'
sc_test_reject true '' '' false

# Adding a manager must not bypass offline integrity/architecture checks.
printf '%s\n' tampered >> "$MODPATH/bin/tailscaled"
sc_test_error='checksum verification failed'
sc_test_reject true '' ''
sc_test_checksums
printf '%s\n' id=shellscaled architecture=amd64 > "$MODPATH/module.prop"
sc_test_checksums
sc_test_error='different architecture'
sc_test_reject true '' ''
printf '%s\n' id=shellscaled architecture=arm64 > "$MODPATH/module.prop"
rm "$MODPATH/bin/tailscaled"
sc_test_checksums
sc_test_error='Missing Tailscale payload'
sc_test_reject true '' ''

printf '%s\n' 'PASS: installer manager selection, offline integrity and architecture checks'

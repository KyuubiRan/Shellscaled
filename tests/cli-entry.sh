#!/system/bin/sh
# Run through scripts/emulator.py. All commands below use a temporary mock
# payload; no real daemon, socket, login, manager directory, or mount is used.
set -eu
[ "$(getprop ro.kernel.qemu)" = 1 ] || { echo 'Emulator required.' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'Root required.' >&2; exit 1; }
sc_test_payload=${1:-/data/adb/modules/shellscaled}
sc_test_root=/data/local/tmp/shellscaled-cli-test
[ ! -e "$sc_test_root" ] && [ ! -L "$sc_test_root" ] || { echo 'Test directory already exists.' >&2; exit 1; }
mkdir -m 0700 "$sc_test_root"
trap 'rm -rf "$sc_test_root"' EXIT
trap 'exit 1' HUP INT TERM
mkdir "$sc_test_root/module" "$sc_test_root/module/bin" "$sc_test_root/path-bin"
cp "$sc_test_payload/cli-entry.sh" "$sc_test_root/module/cli-entry.sh"
cp "$sc_test_payload/bin/tailscale" "$sc_test_root/module/bin/tailscale"
printf '%s\n' id=shellscaled > "$sc_test_root/module/module.prop"
cat > "$sc_test_root/module/bin/tailscaled" <<'EOF'
#!/system/bin/sh
printf '%s\n' core "cli=${TS_BE_CLI:-}" "$@"
EOF
cat > "$sc_test_root/module/ctl.sh" <<'EOF'
#!/system/bin/sh
printf '%s\n' ctl "$@"
EOF
cat > "$sc_test_root/module/common.sh" <<'EOF'
sc_cli_remove() { :; }
sc_error() { printf '%s\n' "$*" >&2; }
EOF
chmod 0755 "$sc_test_root/module/cli-entry.sh" "$sc_test_root/module/bin/"* "$sc_test_root/module/ctl.sh"
ln -s "$sc_test_root/module/cli-entry.sh" "$sc_test_root/path-bin/tailscale"
ln -s "$sc_test_root/module/cli-entry.sh" "$sc_test_root/path-bin/tailscaled"
PATH=$sc_test_root/path-bin:$PATH
export PATH
sc_test_groups=0
sc_test_assert() {
    if ! "$@"; then printf 'Assertion failed in group %s\n' "$sc_test_groups" >&2; exit 1; fi
}
sc_test_equal() { sc_test_assert test "$1" = "$2"; }
sc_test_reject() {
    if "$@" > "$sc_test_root/rejected" 2>&1; then
        printf 'Unexpected success in group %s\n' "$sc_test_groups" >&2
        exit 1
    fi
}

sc_test_groups=$((sc_test_groups + 1))
sc_test_equal "$(tailscale status)" "$(printf '%s\n' core cli=1 --socket=/data/adb/shellscaled/run/tailscaled.sock status)"
sc_test_reject tailscale --socket=/other status
sc_test_reject tailscale status --socket /other
sc_test_reject tailscale -socket=/other status

sc_test_groups=$((sc_test_groups + 1))
sc_test_equal "$(tailscaled)" "$(printf '%s\n' ctl start)"
sc_test_equal "$(tailscaled status)" "$(printf '%s\n' ctl status)"
sc_test_equal "$(tailscaled logs service)" "$(printf '%s\n' ctl logs service)"
sc_test_reject tailscaled start --state=/other
sc_test_reject tailscaled --state=/other
sc_test_reject tailscaled logs arbitrary
sc_test_reject tailscaled ''

sc_test_groups=$((sc_test_groups + 1))
sc_test_equal "$(TS_BE_CLI=1 tailscaled --version)" "$(printf '%s\n' core cli=0 --version)"
sc_test_equal "$(tailscaled --help)" "$(printf '%s\n' core cli=0 --help)"
sc_test_reject tailscaled --version --tun=other

sc_test_groups=$((sc_test_groups + 1))
touch "$sc_test_root/module/disable"
sc_test_reject tailscale status
sc_test_reject tailscaled --version
rm "$sc_test_root/module/disable"
touch "$sc_test_root/module/remove"
sc_test_reject tailscaled
rm "$sc_test_root/module/remove"
cat > "$sc_test_root/path-bin/id" <<'EOF'
#!/system/bin/sh
printf '%s\n' 2000
EOF
chmod 0755 "$sc_test_root/path-bin/id"
sc_test_reject tailscale status
rm "$sc_test_root/path-bin/id"

# Exercise the real ownership/transaction/locking functions, replacing only
# manager selection and privileged mounting with this private mock directory.
SC_MODDIR=$sc_test_root/module
SC_DATA=$sc_test_root/state
SC_RUN=$SC_DATA/run
mkdir -m 0700 "$SC_DATA" "$SC_RUN"
. "$sc_test_payload/cli.sh"
sc_error() { printf '%s\n' "$*" >&2; }
sc_prepare() { :; }
sc_start_time() {
    sc_test_stat=$(cat "/proc/$1/stat") || return 1
    sc_test_stat=${sc_test_stat##*) }
    printf '%s\n' "$sc_test_stat" | cut -d ' ' -f 20
}
sc_cli_find_directory() { printf '%s\n' "$sc_test_root/path-bin"; }
sc_cli_open_directory() { sc_cli_work=$sc_cli_target; }
sc_cli_alias_cleanup() { :; }

sc_test_groups=$((sc_test_groups + 1))
PATH=$sc_test_root/path-bin sc_test_reject sc_cli_path_conflict
rm "$sc_test_root/path-bin/tailscaled"
printf '%s\n' '#!/system/bin/sh' 'exit 0' > "$sc_test_root/path-bin/tailscaled"
chmod 0755 "$sc_test_root/path-bin/tailscaled"
PATH=$sc_test_root/path-bin sc_test_assert sc_cli_path_conflict
rm "$sc_test_root/path-bin/tailscaled"
ln -s "$SC_MODDIR/cli-entry.sh" "$sc_test_root/path-bin/tailscaled"
# Actual manager links elsewhere in PATH belong to the production module,
# not this mock. Keep later transaction tests isolated from that inventory.
sc_cli_path_conflict() { return 1; }
rm "$sc_test_root/path-bin/tailscale" "$sc_test_root/path-bin/tailscaled"

sc_test_groups=$((sc_test_groups + 1))
sc_cli_install
sc_test_assert sc_cli_owned_link "$sc_test_root/path-bin/tailscale"
sc_test_assert sc_cli_owned_link "$sc_test_root/path-bin/tailscaled"
sc_test_equal "$(cat "$SC_DATA/cli-link-dir")" "$sc_test_root/path-bin"
sc_cli_install
sc_test_assert test ! -e "$SC_RUN/cli-link.lock"

sc_test_groups=$((sc_test_groups + 1))
rm "$sc_test_root/path-bin/tailscaled"
printf '%s\n' unrelated > "$sc_test_root/path-bin/tailscaled"
sc_cli_remove
sc_test_assert test ! -L "$sc_test_root/path-bin/tailscale"
sc_test_equal "$(cat "$sc_test_root/path-bin/tailscaled")" unrelated
sc_test_reject sc_cli_install
sc_test_assert test ! -L "$sc_test_root/path-bin/tailscale"
sc_test_equal "$(cat "$sc_test_root/path-bin/tailscaled")" unrelated
rm "$sc_test_root/path-bin/tailscaled"

sc_test_groups=$((sc_test_groups + 1))
mkdir "$SC_RUN/cli-link.lock"
read -r sc_test_pid sc_test_rest < /proc/self/stat
sc_test_start=$(sc_start_time "$sc_test_pid")
: > "$SC_RUN/cli-link.lock/$sc_test_pid.$sc_test_start"
sc_test_reject sc_cli_install
sc_test_assert test -f "$SC_RUN/cli-link.lock/$sc_test_pid.$sc_test_start"
rm "$SC_RUN/cli-link.lock/$sc_test_pid.$sc_test_start"
rmdir "$SC_RUN/cli-link.lock"
mkdir "$SC_RUN/cli-link.lock"
sc_test_reject sc_cli_install
sc_test_assert test -d "$SC_RUN/cli-link.lock"
rmdir "$SC_RUN/cli-link.lock"

sc_test_groups=$((sc_test_groups + 1))
mkdir "$SC_RUN/cli-link.lock"
: > "$SC_RUN/cli-link.lock/99999999.1"
sc_cli_install
sc_test_assert test ! -e "$SC_RUN/cli-link.lock"
sc_cli_remove
sc_test_assert test ! -e "$SC_DATA/cli-link-dir"
sc_test_assert test ! -L "$sc_test_root/path-bin/tailscale"
sc_test_assert test ! -L "$sc_test_root/path-bin/tailscaled"

sc_test_groups=$((sc_test_groups + 1))
SC_DATA=/proc sc_test_reject sc_cli_no_mounts
SC_DATA=/pro sc_test_assert sc_cli_no_mounts
sc_cli_install
sc_cli_alias_cleanup() { return 1; }
sc_test_reject sc_cli_remove
sc_test_assert test -f "$SC_DATA/cli-link-dir"
sc_cli_alias_cleanup() { :; }
sc_cli_no_mounts() { return 1; }
sc_test_reject sc_cli_remove
sc_test_assert test -f "$SC_DATA/cli-link-dir"
rm "$SC_DATA/cli-link-dir"
sc_test_reject sc_cli_remove
sc_test_assert test -d "$SC_DATA"

printf 'PASS: %s anonymous global CLI fixture groups\n' "$sc_test_groups"

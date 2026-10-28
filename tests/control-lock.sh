#!/system/bin/sh
# Run through scripts/emulator.py. Exercise the real control lock in a private
# directory without starting services or opening the module's state/socket.
set -eu
[ "$(getprop ro.kernel.qemu)" = 1 ] || exit 1
payload=$1
SC_MODDIR=$payload
. "$payload/common.sh"

if [ "${2:-}" = contender ]; then
    SC_LOCK=$3/control.lock
    # Shorten only retry delays; all ownership checks and file operations are real.
    sleep() { /system/bin/sleep 0.02; }
    if sc_lock; then
        trap sc_unlock EXIT
        [ "$4" = acquire ] || { echo 'FAIL: live or unidentifiable lock stolen.' >&2; exit 1; }
        sc_read_pid "$SC_LOCK/$SC_LOCK_OWNER"
        [ "$SC_LOCK_OWNER" = "$SC_PID.$SC_PID_START" ]
        : > "$3/acquired"
    else
        [ "$4" = reject ] || { echo 'FAIL: released or stale lock not acquired.' >&2; exit 1; }
    fi
    exit 0
fi

fixture=/data/local/tmp/shellscaled-control-lock-test-$$
[ ! -e "$fixture" ] && [ ! -L "$fixture" ] || exit 1
mkdir -m 0700 "$fixture"
trap 'if [ -n "${child:-}" ]; then wait "$child" || true; fi; rm -rf "$fixture"' EXIT
trap 'exit 1' HUP INT TERM
SC_LOCK=$fixture/control.lock
read -r owner_pid owner_rest < /proc/self/stat
owner_start=$(sc_start_time "$owner_pid")
owner=$owner_pid.$owner_start
contend() { /system/bin/sh "$0" "$payload" contender "$fixture" "$1"; }

# Pause a real shell redirect before printf: an empty file still has a live
# owner. Also cover a partially written and fully written ownership record.
mkdir "$SC_LOCK"
exec 9> "$SC_LOCK/$owner"
contend reject
[ -f "$SC_LOCK/$owner" ]
printf '%s ' "$owner_pid" >&9
contend reject
[ -f "$SC_LOCK/$owner" ]
printf '%s\n' "$owner_start" >&9
exec 9>&-
contend reject
printf '%s\n' 'PASS: empty, partial and complete live owner records are respected.'

# A real competing process acquires only after the original releases its lock.
contend acquire &
child=$!
/system/bin/sleep 0.05
[ ! -e "$fixture/acquired" ]
SC_LOCK_OWNER=$owner
sc_unlock
wait "$child"
child=
[ -f "$fixture/acquired" ] && [ ! -e "$SC_LOCK" ]
rm "$fixture/acquired"
printf '%s\n' 'PASS: contender acquires after release and removes only its own token.'

# A dead process and a reused PID are both reclaimable, even if the former
# died between creating the filename and writing the record.
for stale in 99999999.1 "$owner_pid.0"; do
    mkdir "$SC_LOCK"
    : > "$SC_LOCK/$stale"
    contend acquire
    [ ! -e "$SC_LOCK" ]
    rm "$fixture/acquired"
done
printf '%s\n' 'PASS: dead and reused process identities are reclaimed.'

mkdir "$SC_LOCK"
contend reject
[ -d "$SC_LOCK" ]
for unknown in unknown "$owner_pid.1:2"; do
    : > "$SC_LOCK/$unknown"
    contend reject
    [ -f "$SC_LOCK/$unknown" ]
    rm "$SC_LOCK/$unknown"
done
rmdir "$SC_LOCK"
printf '%s\n' 'PASS: empty directories and unknown ownership remain fail closed.'

mkdir "$fixture/foreign"
: > "$fixture/foreign/99999999.1"
ln -s "$fixture/foreign" "$SC_LOCK"
contend reject
[ -L "$SC_LOCK" ] && [ -f "$fixture/foreign/99999999.1" ]
rm "$SC_LOCK"
mkdir "$SC_LOCK"
ln -s "$fixture/foreign/99999999.1" "$SC_LOCK/99999999.1"
contend reject
[ -L "$SC_LOCK/99999999.1" ] && [ -f "$fixture/foreign/99999999.1" ]
printf '%s\n' 'PASS: symbolic-link locks and tokens are preserved and rejected.'

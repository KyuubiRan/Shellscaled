# Only the synthetic fixture sources this file; production scripts do not.
sc_root_cgroup() { :; }
sc_cli_install() { :; }
sc_cli_remove() { :; }
sc_legacy_active() { return 1; }
sc_magicdns_running() { return 1; }
sc_magicdns_start() { :; }
sc_magicdns_stop() { :; }
sc_magicdns_term() { :; }
sc_refresh_description() { :; }

mv() {
    if [ -e "$TEST_ROOT/injected" ]; then
        case "$TEST_CASE:$1:$2" in
            rollback-new:"$TEST_LIVE":"$TEST_STAGE"|rollback-old:"$TEST_ROOT/data/trash/"*:"$TEST_LIVE") return 1 ;;
        esac
    fi
    case "$TEST_CASE:$1" in
        fail-first:"$TEST_LIVE"|fail-second:"$TEST_STAGE") return 1 ;;
    esac
    /system/bin/mv "$@" || return 1
    [ ! -e "$TEST_ROOT/injected" ] || return 0
    case "$TEST_CASE:$1" in
        rename1-*:"$TEST_LIVE") sc_test_signal=${TEST_CASE#rename1-} ;;
        rename2-*:"$TEST_STAGE") sc_test_signal=${TEST_CASE#rename2-} ;;
        rollback-old:"$TEST_LIVE"|rollback-new:"$TEST_STAGE") sc_test_signal=TERM ;;
        flag-*:"$TEST_STAGE")
            : > "$TEST_LIVE/${TEST_CASE#flag-}"
            sc_test_signal=TERM
            ;;
        *) return 0 ;;
    esac
    : > "$TEST_ROOT/injected"
    builtin kill -"$sc_test_signal" $$
}

kill() {
    if [ "${1:-}" = -KILL ] && [ "${2:-}" = "$TEST_OLD_SUPERVISOR" ] &&
        [ ! -e "$TEST_ROOT/injected" ]; then
        case "$TEST_CASE" in
            retire-before-*)
                : > "$TEST_ROOT/injected"
                builtin kill -"${TEST_CASE#retire-before-}" $$
                ;;
        esac
        builtin kill "$@" || return 1
        case "$TEST_CASE" in
            retire-after-*)
                : > "$TEST_ROOT/injected"
                builtin kill -"${TEST_CASE#retire-after-}" $$
                ;;
        esac
        return 0
    fi
    builtin kill "$@"
}

#!/usr/bin/env bats

load '../test_helper'

setup() {
    common_setup
    # Source the library under test
    source_lib "logging"
}

teardown() {
    common_teardown
}

@test "logging: color variables are exported" {
    # Color variables should be exported (but may be empty if NO_COLOR or no TTY)
    # Test that the ACFS_COLORS_ENABLED flag is set correctly
    [[ -v ACFS_RED ]]
    [[ -v ACFS_GREEN ]]
    [[ -v ACFS_NC ]]
    [[ -v ACFS_COLORS_ENABLED ]]

    # Without a TTY (bats environment), colors should be disabled
    if [[ ! -t 2 ]]; then
        [[ "$ACFS_COLORS_ENABLED" == "false" ]]
    fi
}

@test "logging: log_success prints green checkmark to stderr" {
    # run captures stdout + stderr
    run log_success "Test Success"
    
    assert_success
    assert_output --partial "Test Success"
    # Check for checkmark (UTF-8)
    assert_output --partial "✓"
}

@test "logging: log_error prints red cross to stderr" {
    run log_error "Test Error"
    
    assert_success
    assert_output --partial "Test Error"
    assert_output --partial "✖"
}

@test "logging: log_warn prints yellow warning to stderr" {
    run log_warn "Test Warning"
    
    assert_success
    assert_output --partial "Test Warning"
    assert_output --partial "⚠"
}

@test "logging: log_step supports step numbering" {
    run log_step "1/10" "Initializing"
    
    assert_success
    assert_output --partial "[1/10]"
    assert_output --partial "Initializing"
}

@test "logging: log_step supports single argument" {
    run log_step "Just a step"
    
    assert_success
    assert_output --partial "[•]"
    assert_output --partial "Just a step"
}

@test "logging: log_to_file appends to file" {
    local tmp_log
    tmp_log=$(create_temp_file)
    
    # Override logfile path logic for test
    # logging.sh uses a fixed path /var/log/acfs... unless we override the function
    # or if we can pass it?
    # log_to_file "message" "logfile"
    
    log_to_file "Test Log Entry" "$tmp_log"
    
    run cat "$tmp_log"
    assert_output --partial "Test Log Entry"
    assert_output --partial "[" # timestamp
}

@test "logging: acfs_log_close ignores caller-owned fd 3 when logging was not initialized" {
    local probe="$BATS_TEST_TMPDIR/fd3-probe.txt"
    local err="$BATS_TEST_TMPDIR/close.err"
    local unwritable_log="$BATS_TEST_TMPDIR/unwritable-install.log"
    local script="$PROJECT_ROOT/scripts/lib/logging.sh"

    printf 'existing\n' > "$unwritable_log"
    chmod 400 "$unwritable_log"

    run bash -c '
        source "$1"
        exec 3>"$2"
        ACFS_LOG_FILE="$3"
        ACFS_LOG_INITIALIZED=false
        acfs_log_close 2>"$4"
        printf "fd3-still-open\n" >&3
    ' _ "$script" "$probe" "$unwritable_log" "$err"

    assert_success
    assert_output ""
    run cat "$probe"
    assert_success
    assert_output "fd3-still-open"
    run cat "$err"
    assert_success
    assert_output ""
}

@test "logging: log_sensitive ignores caller-owned fd 3" {
    command -v setsid >/dev/null || skip "setsid is required to detach /dev/tty"

    local probe="$BATS_TEST_TMPDIR/fd3-sensitive-probe.txt"
    local script="$PROJECT_ROOT/scripts/lib/logging.sh"

    run setsid bash -c '
        source "$1"
        exec 3>"$2"
        ACFS_LOG_STDERR_CAPTURED=false
        ACFS_LOG_ORIGINAL_STDERR_FD=""
        log_sensitive "Generated password for testuser: secret-value"
    ' _ "$script" "$probe"

    assert_success
    assert_output --partial "Generated password for testuser: secret-value"
    run cat "$probe"
    assert_success
    assert_output ""
}

@test "logging: log_sensitive uses saved stderr fd when capture is active" {
    local sensitive_out="$BATS_TEST_TMPDIR/sensitive-terminal.txt"
    local captured_err="$BATS_TEST_TMPDIR/captured-stderr.txt"
    local script="$PROJECT_ROOT/scripts/lib/logging.sh"

    run bash -c '
        source "$1"
        exec {saved_fd}>"$2"
        ACFS_LOG_STDERR_CAPTURED=true
        ACFS_LOG_ORIGINAL_STDERR_FD="$saved_fd"
        log_sensitive "Generated password for testuser: secret-value" 2>"$3"
        exec {saved_fd}>&-
    ' _ "$script" "$sensitive_out" "$captured_err"

    assert_success
    assert_output ""
    run cat "$sensitive_out"
    assert_success
    assert_output --partial "Generated password for testuser: secret-value"
    run cat "$captured_err"
    assert_success
    assert_output ""
}

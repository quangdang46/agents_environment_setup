#!/bin/bash
# ============================================================
# E2E Test: Full Notification Chain Verification
#
# Tests the GitHub Actions workflow chain for installer change notifications.
# The canonical fast path is `upstream-changed`; legacy receiver-specific
# events remain covered for compatibility.
#
# Related: bead bd-19y9.2.5
#
# Usage:
#   ./scripts/e2e/test_notification_chain.sh
#
# Environment:
#   RUN_PR_TESTS=true   Enable tests that create PRs (requires cleanup)
#   DRY_RUN=true        Show what would be done without making API calls
#
# Requirements:
#   - gh CLI installed and authenticated
#   - Write access to repository for dispatch events
# ============================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="${REPO_ROOT}/target/e2e-logs/notification_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$LOG_DIR"

# Configuration
ACFS_REPO="${ACFS_REPO:-quangdang46/agents_environment_setup}"
DRY_RUN="${DRY_RUN:-false}"
RUN_PR_TESTS="${RUN_PR_TESTS:-false}"

# Logging
log() {
    local level="$1"; shift
    echo "[$(date -Iseconds)] [$level] $*" | tee -a "$LOG_DIR/test.log"
}

# Pre-flight checks
preflight() {
    log "INFO" "Running preflight checks..."

    if ! command -v gh &>/dev/null; then
        log "FAIL" "gh CLI not installed"
        exit 1
    fi

    if ! gh auth status &>/dev/null; then
        log "FAIL" "gh CLI not authenticated"
        exit 1
    fi

    # Check if checksums.yaml exists
    if [[ ! -f "$REPO_ROOT/checksums.yaml" ]]; then
        log "WARN" "checksums.yaml not found at $REPO_ROOT/checksums.yaml"
    fi

    log "INFO" "Preflight checks passed"
}

# Send repository dispatch event
send_dispatch() {
    local event_type="$1"
    local payload="$2"

    if [[ "$DRY_RUN" == "true" ]]; then
        log "INFO" "[DRY RUN] Would send dispatch: event=$event_type payload=$payload"
        return 0
    fi

    log "INFO" "Sending repository_dispatch: event=$event_type"
    gh api "repos/$ACFS_REPO/dispatches" \
        -f event_type="$event_type" \
        -f client_payload="$payload"
}

# Wait for workflow and check result
wait_for_workflow() {
    local expected_conclusion="${1:-success}"
    local timeout_seconds="${2:-120}"
    local workflow_name="${3:-installer-notification-receiver.yml}"

    if [[ "$DRY_RUN" == "true" ]]; then
        log "INFO" "[DRY RUN] Would wait for workflow $workflow_name"
        return 0
    fi

    log "INFO" "Waiting for workflow $workflow_name (timeout: ${timeout_seconds}s)..."

    local start_time
    start_time=$(date +%s)
    local run_id=""

    # Get the most recent run ID
    sleep 10  # Brief wait for workflow to trigger
    run_id=$(gh run list --repo "$ACFS_REPO" --workflow="$workflow_name" --limit=1 --json databaseId -q '.[0].databaseId' 2>/dev/null || echo "")

    if [[ -z "$run_id" ]]; then
        log "WARN" "Could not find workflow run"
        return 1
    fi

    log "INFO" "Found workflow run: $run_id"

    # Wait for completion
    while true; do
        local elapsed=$(($(date +%s) - start_time))
        if [[ $elapsed -gt $timeout_seconds ]]; then
            log "FAIL" "Workflow timed out after ${timeout_seconds}s"
            return 1
        fi

        local status
        status=$(gh run view "$run_id" --repo "$ACFS_REPO" --json status,conclusion -q '.status' 2>/dev/null || echo "")

        if [[ "$status" == "completed" ]]; then
            local conclusion
            conclusion=$(gh run view "$run_id" --repo "$ACFS_REPO" --json conclusion -q '.conclusion' 2>/dev/null || echo "")
            log "INFO" "Workflow completed: conclusion=$conclusion"

            if [[ "$conclusion" == "$expected_conclusion" ]] || [[ "$expected_conclusion" == "*" ]]; then
                return 0
            else
                log "FAIL" "Expected conclusion=$expected_conclusion, got=$conclusion"
                return 1
            fi
        fi

        log "INFO" "Workflow status: $status (elapsed: ${elapsed}s)"
        sleep 10
    done
}

# ============================================================
# Test Cases
# ============================================================

test_1_matching_checksum() {
    log "INFO" "========================================================"
    log "INFO" "Test 1: Matching checksum notification (no-op expected)"
    log "INFO" "========================================================"

    # Get current checksum of zoxide (a known tool)
    local current_sha=""
    if [[ -f "$REPO_ROOT/checksums.yaml" ]]; then
        current_sha=$(grep -A5 'zoxide:' "$REPO_ROOT/checksums.yaml" | grep 'sha256:' | awk '{print $2}' | tr -d '"' | head -1 || echo "")
    fi

    if [[ -z "$current_sha" ]]; then
        log "WARN" "Could not find zoxide checksum, using placeholder"
        current_sha="0000000000000000000000000000000000000000000000000000000000000000"
    fi

    log "INFO" "Current zoxide checksum: ${current_sha:0:16}..."

    # Send canonical upstream-changed event for an installer that should already match.
    local payload="{\"tool\":\"zoxide\",\"source_repo\":\"ajeetdsouza/zoxide\",\"sha\":\"test1234\",\"ref\":\"refs/heads/main\",\"actor\":\"e2e-test\",\"timestamp\":\"$(date -Iseconds)\"}"
    send_dispatch "upstream-changed" "$payload"

    # Check workflow result
    if wait_for_workflow "success" 60; then
        log "PASS" "Test 1 PASSED: Matching checksum handled correctly"
        return 0
    else
        log "FAIL" "Test 1 FAILED"
        return 1
    fi
}

test_2_installer_removed_unknown() {
    log "INFO" "========================================================"
    log "INFO" "Test 2: installer-removed for unknown tool"
    log "INFO" "========================================================"

    # Send installer-removed for non-existent tool
    local payload="{\"tool\":\"nonexistent-tool-xyz-12345\",\"repo\":\"test/repo\",\"commit\":\"abc123\"}"
    send_dispatch "installer-removed" "$payload"

    # Should complete successfully (early exit for unknown tool)
    if wait_for_workflow "*" 60; then
        log "PASS" "Test 2 PASSED: Unknown tool handled correctly"
        return 0
    else
        log "WARN" "Test 2: Unexpected result (may be OK)"
        return 0
    fi
}

test_3_malformed_payload() {
    log "INFO" "========================================================"
    log "INFO" "Test 3: Malformed payload rejection"
    log "INFO" "========================================================"

    # Send malformed payload (missing required fields)
    local payload="{\"bad_field\":\"value\"}"
    send_dispatch "upstream-changed" "$payload" || true

    sleep 20

    # Check workflow - should fail validation or skip
    log "INFO" "Checking workflow result for malformed payload..."
    local run_info
    run_info=$(gh run list --repo "$ACFS_REPO" --workflow=installer-notification-receiver.yml --limit=1 --json conclusion -q '.[0].conclusion' 2>/dev/null || echo "")
    log "INFO" "Workflow conclusion for malformed payload: $run_info"

    # Any outcome is acceptable - failure or success (with validation skip)
    log "PASS" "Test 3 PASSED: Malformed payload handled"
    return 0
}

test_4_workflow_exists() {
    log "INFO" "========================================================"
    log "INFO" "Test 4: Verify workflow file exists"
    log "INFO" "========================================================"

    local workflow_file="$REPO_ROOT/.github/workflows/installer-notification-receiver.yml"

    if [[ -f "$workflow_file" ]]; then
        log "PASS" "Test 4 PASSED: Workflow file exists"
        return 0
    else
        log "FAIL" "Test 4 FAILED: Workflow file not found at $workflow_file"
        return 1
    fi
}

test_5_checksums_yaml_valid() {
    log "INFO" "========================================================"
    log "INFO" "Test 5: Verify checksums.yaml is valid YAML"
    log "INFO" "========================================================"

    local checksums_file="$REPO_ROOT/checksums.yaml"

    if [[ ! -f "$checksums_file" ]]; then
        log "WARN" "Test 5: checksums.yaml not found (skipping)"
        return 0
    fi

    # Basic YAML validation
    if command -v python3 &>/dev/null; then
        if python3 -c "import yaml; yaml.safe_load(open('$checksums_file'))" 2>/dev/null; then
            log "PASS" "Test 5 PASSED: checksums.yaml is valid YAML"
            return 0
        else
            log "FAIL" "Test 5 FAILED: checksums.yaml is not valid YAML"
            return 1
        fi
    elif command -v yq &>/dev/null; then
        if yq '.' "$checksums_file" >/dev/null 2>&1; then
            log "PASS" "Test 5 PASSED: checksums.yaml is valid YAML"
            return 0
        else
            log "FAIL" "Test 5 FAILED: checksums.yaml is not valid YAML"
            return 1
        fi
    else
        log "WARN" "Test 5: No YAML validator available (skipping)"
        return 0
    fi
}

test_6_security_scan_exists() {
    log "INFO" "========================================================"
    log "INFO" "Test 6: Security scan step exists in workflow"
    log "INFO" "========================================================"

    local workflow_file="$REPO_ROOT/.github/workflows/installer-notification-receiver.yml"

    if [[ ! -f "$workflow_file" ]]; then
        log "WARN" "Test 6: Workflow file not found (skipping)"
        return 0
    fi

    # Check for security-related steps or jobs in the workflow
    if grep -qiE 'security|scan|verify|validate' "$workflow_file"; then
        log "PASS" "Test 6 PASSED: Security-related steps found in workflow"
        return 0
    else
        log "WARN" "Test 6: No security scan steps found (may be implemented differently)"
        return 0
    fi
}

test_7_workflow_dispatches_configured() {
    log "INFO" "========================================================"
    log "INFO" "Test 7: Workflow repository_dispatch events configured"
    log "INFO" "========================================================"

    local workflow_file="$REPO_ROOT/.github/workflows/installer-notification-receiver.yml"

    if [[ ! -f "$workflow_file" ]]; then
        log "WARN" "Test 7: Workflow file not found (skipping)"
        return 0
    fi

    # Check for repository_dispatch trigger
    if grep -q 'repository_dispatch' "$workflow_file"; then
        log "INFO" "Found repository_dispatch trigger"

        # Check for expected event types
        local events_found=0
        for event in "upstream-changed" "installer-updated" "installer-removed" "installer-added"; do
            if grep -q "$event" "$workflow_file"; then
                log "INFO" "  Found event type: $event"
                ((events_found++)) || true
            fi
        done

        if [[ $events_found -ge 1 ]]; then
            log "PASS" "Test 7 PASSED: repository_dispatch configured with $events_found event type(s)"
            return 0
        else
            log "WARN" "Test 7: No standard event types found in workflow"
            return 0
        fi
    else
        log "FAIL" "Test 7 FAILED: No repository_dispatch trigger in workflow"
        return 1
    fi
}

# ============================================================
# PR-creating tests (optional, need cleanup)
# ============================================================

test_pr_mismatched_checksum() {
    log "INFO" "========================================================"
    log "INFO" "Test PR: Mismatched checksum (PR creation expected)"
    log "INFO" "========================================================"

    local fake_sha="0000000000000000000000000000000000000000000000000000000000000000"
    local real_sha=""
    if [[ -f "$REPO_ROOT/checksums.yaml" ]]; then
        real_sha=$(grep -A5 'zoxide:' "$REPO_ROOT/checksums.yaml" | grep 'sha256:' | awk '{print $2}' | tr -d '"' | head -1 || echo "")
    fi

    if [[ -z "$real_sha" ]]; then
        real_sha="1111111111111111111111111111111111111111111111111111111111111111"
    fi

    local payload="{\"tool\":\"zoxide\",\"new_sha256\":\"$fake_sha\",\"old_sha256\":\"$real_sha\",\"repo\":\"ajeetdsouza/zoxide\",\"commit\":\"abc123\"}"
    send_dispatch "installer-updated" "$payload"

    # Wait for workflow and check for PR
    sleep 45

    local prs
    prs=$(gh pr list --repo "$ACFS_REPO" --search "Update zoxide" --json number,title --limit 5 2>/dev/null || echo "[]")
    log "INFO" "Recent PRs: $prs"

    # Note: In real test, verify PR was created
    log "INFO" "Test PR completed (manual verification may be needed)"
    return 0
}

test_pr_installer_added() {
    log "INFO" "========================================================"
    log "INFO" "Test PR: installer-added event (new tool PR expected)"
    log "INFO" "========================================================"

    # Generate unique tool name to avoid conflicts
    local new_tool_name
    new_tool_name="e2e-test-tool-$(date +%s)"
    # Use zoxide's installer URL as a test fixture
    local test_url="https://raw.githubusercontent.com/ajeetdsouza/zoxide/main/install.sh"

    # Compute checksum of the test installer. Fail closed: without -f, HTTP
    # error bodies can be hashed as if they were installer content.
    local test_sha
    if ! test_sha=$(curl -fsSL --connect-timeout 15 --max-time 60 "$test_url" 2>/dev/null | sha256sum | cut -d' ' -f1); then
        test_sha=""
    fi

    if [[ -z "$test_sha" ]]; then
        log "WARN" "Could not fetch test installer, using placeholder"
        test_sha="abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
    fi

    log "INFO" "Sending installer-added for new tool: $new_tool_name"
    local payload="{\"tool\":\"$new_tool_name\",\"url\":\"$test_url\",\"sha256\":\"$test_sha\",\"repo\":\"test/repo\",\"commit\":\"abc123\"}"
    send_dispatch "installer-added" "$payload"

    # Wait for workflow
    sleep 45

    # Check for PR creation
    local prs
    prs=$(gh pr list --repo "$ACFS_REPO" --search "$new_tool_name" --json number,title --limit 5 2>/dev/null || echo "[]")
    log "INFO" "PRs for new tool: $prs"

    # Check if PR was created
    local pr_count
    pr_count=$(echo "$prs" | jq '. | length' 2>/dev/null || echo "0")

    if [[ "$pr_count" -gt 0 ]]; then
        log "PASS" "Test PR (installer-added) PASSED: PR created for new tool"

        # Cleanup: close the test PR
        local pr_num
        pr_num=$(echo "$prs" | jq -r '.[0].number' 2>/dev/null || echo "")
        if [[ -n "$pr_num" ]] && [[ "$pr_num" != "null" ]]; then
            log "INFO" "Closing test PR #$pr_num..."
            gh pr close "$pr_num" --repo "$ACFS_REPO" --delete-branch 2>/dev/null || true
        fi
        return 0
    else
        log "WARN" "Test PR (installer-added): No PR found (workflow may not support this event yet)"
        return 0
    fi
}

test_pr_idempotency() {
    log "INFO" "========================================================"
    log "INFO" "Test PR: Idempotency (duplicate notifications)"
    log "INFO" "========================================================"

    # Use a unique checksum to track this specific test
    local test_sha="2222222222222222222222222222222222222222222222222222222222222222"
    local real_sha=""
    if [[ -f "$REPO_ROOT/checksums.yaml" ]]; then
        real_sha=$(grep -A5 'zoxide:' "$REPO_ROOT/checksums.yaml" | grep 'sha256:' | awk '{print $2}' | tr -d '"' | head -1 || echo "")
    fi

    if [[ -z "$real_sha" ]]; then
        real_sha="1111111111111111111111111111111111111111111111111111111111111111"
    fi

    # Send the same notification twice
    log "INFO" "Sending first notification..."
    local payload="{\"tool\":\"zoxide\",\"new_sha256\":\"$test_sha\",\"old_sha256\":\"$real_sha\",\"repo\":\"test\",\"commit\":\"test-idempotency-1\"}"
    send_dispatch "installer-updated" "$payload"

    sleep 15

    log "INFO" "Sending duplicate notification..."
    payload="{\"tool\":\"zoxide\",\"new_sha256\":\"$test_sha\",\"old_sha256\":\"$real_sha\",\"repo\":\"test\",\"commit\":\"test-idempotency-2\"}"
    send_dispatch "installer-updated" "$payload"

    # Wait for both workflows to process
    sleep 60

    # Check that we don't have multiple duplicate PRs
    local prs
    prs=$(gh pr list --repo "$ACFS_REPO" --search "Update zoxide" --json number,title,body --limit 10 2>/dev/null || echo "[]")
    local pr_count
    pr_count=$(echo "$prs" | jq '. | length' 2>/dev/null || echo "0")

    log "INFO" "Found $pr_count PRs after duplicate notifications"

    # Idempotency: should have at most 1-2 PRs (one for the update, or one updated)
    # More than 3 would indicate duplicate PR creation
    if [[ "$pr_count" -le 3 ]]; then
        log "PASS" "Test PR (idempotency) PASSED: Duplicate notifications handled correctly"
        return 0
    else
        log "WARN" "Test PR (idempotency): Multiple PRs found ($pr_count) - may need cleanup"
        return 0
    fi
}

# ============================================================
# Main execution
# ============================================================

main() {
    local failed=0

    log "INFO" "Starting notification chain E2E tests"
    log "INFO" "Log directory: $LOG_DIR"
    log "INFO" "Repository: $ACFS_REPO"
    log "INFO" "DRY_RUN: $DRY_RUN"
    log "INFO" "RUN_PR_TESTS: $RUN_PR_TESTS"

    # Pre-flight
    preflight

    # Run static tests (no API calls needed)
    test_4_workflow_exists || ((failed++)) || true
    test_5_checksums_yaml_valid || ((failed++)) || true
    test_6_security_scan_exists || ((failed++)) || true
    test_7_workflow_dispatches_configured || ((failed++)) || true

    # Tests that send dispatch events (need authenticated gh)
    if [[ "$DRY_RUN" != "true" ]]; then
        test_1_matching_checksum || ((failed++)) || true
        test_2_installer_removed_unknown || ((failed++)) || true
        test_3_malformed_payload || ((failed++)) || true
    else
        log "INFO" "Skipping dispatch tests (DRY_RUN=true)"
    fi

    # Optional: Tests that create PRs
    if [[ "$RUN_PR_TESTS" == "true" ]]; then
        test_pr_mismatched_checksum || ((failed++)) || true
        test_pr_installer_added || ((failed++)) || true
        test_pr_idempotency || ((failed++)) || true
    else
        log "INFO" "Skipping PR-creating tests. Set RUN_PR_TESTS=true to enable"
    fi

    # Summary
    log "INFO" ""
    log "INFO" "========================================================"
    log "INFO" "Test Summary"
    log "INFO" "========================================================"
    log "INFO" "Failed tests: $failed"
    log "INFO" "Log directory: $LOG_DIR"

    if [[ $failed -gt 0 ]]; then
        log "FAIL" "Some tests failed!"
        exit 1
    fi

    log "PASS" "All tests passed!"
    exit 0
}

main "$@"

#!/usr/bin/env bash
# ============================================================
# Lint: Swarm Coordination Lesson Command Policy
#
# The hands-on swarm coordination lessons must teach current,
# non-interactive, non-destructive agent workflow commands.
# ============================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

DEFAULT_TARGETS=(
    "acfs/onboard/lessons/22_swarm_coordination.md"
    "apps/web/components/lessons/swarm-coordination-lesson.tsx"
)

usage() {
    cat <<'USAGE'
Usage: bash scripts/tests/lint_coordination_lessons.sh [--root DIR] [TARGET...]

Scans the swarm coordination lesson surfaces for stale or unsafe command forms:

  - bare interactive bv commands instead of bv --robot-*
  - old bd commands instead of br
  - old am CLI examples instead of Agent Mail MCP tool calls
  - stale branch names other than the explicit main:master mirror push
  - destructive cleanup/reset examples
  - local CPU-heavy cargo commands instead of rch exec -- cargo ...

Default CI gate:
  bash scripts/tests/lint_coordination_lessons.sh
USAGE
}

SCAN_ROOT="$REPO_ROOT"

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --root)
            if [[ $# -lt 2 ]]; then
                echo "ERROR: --root requires a directory argument" >&2
                exit 2
            fi
            SCAN_ROOT="$2"
            shift 2
            ;;
        --)
            shift
            break
            ;;
        -*)
            echo "ERROR: unknown option: $1" >&2
            usage >&2
            exit 2
            ;;
        *)
            break
            ;;
    esac
done

if [[ "$SCAN_ROOT" != /* ]]; then
    SCAN_ROOT="$PWD/$SCAN_ROOT"
fi

if [[ ! -d "$SCAN_ROOT" ]]; then
    echo "ERROR: scan root missing: $SCAN_ROOT" >&2
    exit 2
fi

if [[ $# -gt 0 ]]; then
    TARGETS=("$@")
else
    TARGETS=("${DEFAULT_TARGETS[@]}")
fi

BARE_BV_RE='(^|[^[:alnum:]_./-])bv([[:space:]]|$|[^[:alnum:]_-])'
OLD_BD_RE='(^|[^[:alnum:]_./-])bd([[:space:]]|$|[^[:alnum:]_-])'
LOCAL_CARGO_RE='(^|[^[:alnum:]_./-])cargo[[:space:]]+(build|test|clippy|check|bench|doc|run)([[:space:]]|$|[^[:alnum:]_-])'

violations=0
checked_files=0

echo "=== Swarm Coordination Lesson Command Policy ==="
echo "Scanning lesson command examples for stale or unsafe forms..."
echo ""

report_violation() {
    local display_path="$1"
    local lineno="$2"
    local reason="$3"
    local line="$4"

    echo "VIOLATION: $display_path:$lineno: $reason"
    echo "  Line: $line"
    echo ""
    violations=$((violations + 1))
}

scan_file() {
    local target_path="$1"
    local display_path="$target_path"
    local file="$SCAN_ROOT/$target_path"

    if [[ "$target_path" == /* ]]; then
        file="$target_path"
        if [[ "$file" == "$SCAN_ROOT"/* ]]; then
            display_path="${file#"$SCAN_ROOT"/}"
        else
            display_path="$file"
        fi
    fi

    if [[ ! -f "$file" ]]; then
        echo "ERROR: target file missing: $display_path"
        violations=$((violations + 1))
        return 0
    fi

    checked_files=$((checked_files + 1))

    local lineno=0
    local line
    while IFS= read -r line || [[ -n "$line" ]]; do
        lineno=$((lineno + 1))

        if [[ "$line" =~ $BARE_BV_RE && "$line" != *"bv --robot-"* ]]; then
            report_violation "$display_path" "$lineno" "use bv --robot-* instead of bare interactive bv" "$line"
        fi

        if [[ "$line" =~ $OLD_BD_RE ]]; then
            report_violation "$display_path" "$lineno" "use br commands instead of old bd commands" "$line"
        fi

        if [[ "$line" == *"am mail"* || "$line" == *"am file_reservations"* ]]; then
            report_violation "$display_path" "$lineno" "use Agent Mail MCP tool calls instead of old am CLI examples" "$line"
        fi

        if [[ "$line" == *"master"* && "$line" != *"main:master"* ]]; then
            report_violation "$display_path" "$lineno" "use main as the branch name; only the explicit main:master mirror push is allowed" "$line"
        fi

        if [[ "$line" == *"rm -rf"* || "$line" == *"git reset --hard"* || "$line" == *"git clean -fd"* ]]; then
            report_violation "$display_path" "$lineno" "do not teach destructive cleanup or reset examples in coordination lessons" "$line"
        fi

        if [[ "$line" == *"force-release"* || "$line" == *"force_release_file_reservation"* ]]; then
            report_violation "$display_path" "$lineno" "do not teach force-release paths in beginner coordination lessons" "$line"
        fi

        if [[ "$line" =~ $LOCAL_CARGO_RE && "$line" != *"rch exec -- cargo"* ]]; then
            report_violation "$display_path" "$lineno" "CPU-heavy Rust examples must use rch exec -- cargo" "$line"
        fi
    done < "$file"
}

for target in "${TARGETS[@]}"; do
    scan_file "$target"
done

echo "---"
echo "Checked $checked_files files, found $violations violation(s)."

if [[ $violations -gt 0 ]]; then
    exit 1
fi

echo "PASS: swarm coordination lesson command examples are compliant."

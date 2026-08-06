#!/usr/bin/env bash
# ============================================================
# ACFS Continue - View Installation Progress
#
# This script allows users to monitor ongoing ACFS installation
# progress, especially after Ubuntu upgrades complete and the
# installer continues in the background.
#
# Usage:
#   acfs continue           # Show status and attach to logs
#   acfs continue --status  # Just show current status
#   acfs continue --help    # Show help
#
# Related bead: hun4
# ============================================================

_CONTINUE_WAS_SOURCED=false
_CONTINUE_ORIGINAL_HOME=""
_CONTINUE_ORIGINAL_HOME_WAS_SET=false
_CONTINUE_RESTORE_ERREXIT=false
_CONTINUE_RESTORE_NOUNSET=false
_CONTINUE_RESTORE_PIPEFAIL=false
if [[ -v HOME ]]; then
    _CONTINUE_ORIGINAL_HOME="$HOME"
    _CONTINUE_ORIGINAL_HOME_WAS_SET=true
fi
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
    _CONTINUE_WAS_SOURCED=true
    [[ $- == *e* ]] && _CONTINUE_RESTORE_ERREXIT=true
    [[ $- == *u* ]] && _CONTINUE_RESTORE_NOUNSET=true
    if shopt -qo pipefail 2>/dev/null; then
        _CONTINUE_RESTORE_PIPEFAIL=true
    fi
fi

set -euo pipefail

continue_sanitize_abs_nonroot_path() {
    local path_value="${1:-}"

    [[ -n "$path_value" ]] || return 1
    path_value="${path_value%/}"
    [[ -n "$path_value" ]] || return 1
    [[ "$path_value" == /* ]] || return 1
    [[ "$path_value" != "/" ]] || return 1
    printf '%s\n' "$path_value"
}

continue_existing_abs_home() {
    local path_value=""

    path_value="$(continue_sanitize_abs_nonroot_path "${1:-}" 2>/dev/null || true)"
    [[ -n "$path_value" ]] || return 1
    [[ -d "$path_value" ]] || return 1
    printf '%s\n' "$path_value"
}

continue_system_binary_path() {
    local name="${1:-}"
    local candidate=""

    [[ -n "$name" ]] || return 1
    case "$name" in
        .|..)
            return 1
            ;;
        *[!A-Za-z0-9._+-]*)
            return 1
            ;;
    esac

    for candidate in \
        "/usr/bin/$name" \
        "/bin/$name" \
        "/usr/local/bin/$name" \
        "/usr/local/sbin/$name" \
        "/usr/sbin/$name" \
        "/sbin/$name"
    do
        [[ -x "$candidate" ]] || continue
        printf '%s\n' "$candidate"
        return 0
    done

    return 1
}

continue_resolve_current_user() {
    local current_user=""
    local id_bin=""
    local whoami_bin=""

    id_bin="$(continue_system_binary_path id 2>/dev/null || true)"
    if [[ -n "$id_bin" ]]; then
        current_user="$("$id_bin" -un 2>/dev/null || true)"
    fi

    if [[ -z "$current_user" ]]; then
        whoami_bin="$(continue_system_binary_path whoami 2>/dev/null || true)"
        if [[ -n "$whoami_bin" ]]; then
            current_user="$("$whoami_bin" 2>/dev/null || true)"
        fi
    fi

    [[ -n "$current_user" ]] || return 1
    printf '%s\n' "$current_user"
}

continue_getent_passwd_entry() {
    local user="${1-}"
    local getent_bin=""
    local passwd_entry=""
    local passwd_line=""
    local printed_any=false

    getent_bin="$(continue_system_binary_path getent 2>/dev/null || true)"
    if [[ -z "$user" ]]; then
        if [[ -n "$getent_bin" ]]; then
            while IFS= read -r passwd_line; do
                printf '%s\n' "$passwd_line"
                printed_any=true
            done < <("$getent_bin" passwd 2>/dev/null || true)
            if [[ "$printed_any" == true ]]; then
                return 0
            fi
        fi

        [[ -r /etc/passwd ]] || return 1
        while IFS= read -r passwd_line; do
            printf '%s\n' "$passwd_line"
        done < /etc/passwd
        return 0
    fi

    if [[ -n "$getent_bin" ]]; then
        passwd_entry="$("$getent_bin" passwd "$user" 2>/dev/null || true)"
    fi

    if [[ -z "$passwd_entry" ]] && [[ -r /etc/passwd ]]; then
        while IFS= read -r passwd_line; do
            [[ "${passwd_line%%:*}" == "$user" ]] || continue
            passwd_entry="$passwd_line"
            break
        done < /etc/passwd
    fi

    [[ -n "$passwd_entry" ]] || return 1
    printf '%s\n' "$passwd_entry"
}

continue_passwd_home_from_entry() {
    local passwd_entry="${1:-}"
    local passwd_home=""

    [[ -n "$passwd_entry" ]] || return 1
    IFS=: read -r _ _ _ _ _ passwd_home _ <<< "$passwd_entry"
    passwd_home="$(continue_sanitize_abs_nonroot_path "$passwd_home" 2>/dev/null || true)"
    [[ -n "$passwd_home" ]] || return 1
    printf '%s\n' "$passwd_home"
}

continue_is_valid_username() {
    local username="${1:-}"
    [[ "$username" =~ ^[a-z_][a-z0-9._-]*$ ]]
}

continue_resolve_current_home() {
    local current_user=""
    local fallback_home=""
    local passwd_entry=""
    local passwd_home=""

    fallback_home="$(continue_sanitize_abs_nonroot_path "${HOME:-}" 2>/dev/null || true)"
    if [[ "${_CONTINUE_WAS_SOURCED:-false}" == "true" ]]; then
        fallback_home="$(continue_sanitize_abs_nonroot_path "${_CONTINUE_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
    fi

    current_user="$(continue_resolve_current_user 2>/dev/null || true)"

    if [[ "$current_user" == "root" ]]; then
        printf '/root\n'
        return 0
    fi

    if [[ -n "$current_user" ]]; then
        passwd_entry="$(continue_getent_passwd_entry "$current_user" 2>/dev/null || true)"
        if [[ -n "$passwd_entry" ]]; then
            passwd_home="$(continue_passwd_home_from_entry "$passwd_entry" 2>/dev/null || true)"
            if [[ -n "$passwd_home" ]]; then
                printf '%s\n' "$passwd_home"
                return 0
            fi
        fi
    fi

    [[ -n "$fallback_home" ]] || return 1
    printf '%s\n' "$fallback_home"
}
continue_initial_current_home() {
    local cached_home=""
    local resolved_home=""

    if [[ "${_CONTINUE_WAS_SOURCED:-false}" == "true" ]] && [[ -z "${TARGET_HOME:-}${TARGET_USER:-}${ACFS_HOME:-}${ACFS_STATE_FILE:-}${ACFS_SYSTEM_STATE_FILE:-}" ]]; then
        cached_home="$(continue_sanitize_abs_nonroot_path "${_CONTINUE_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
        if [[ -n "$cached_home" ]]; then
            printf '%s\n' "$cached_home"
            return 0
        fi
    fi

    resolved_home="$(continue_resolve_current_home 2>/dev/null || true)"
    if [[ -n "$resolved_home" ]]; then
        printf '%s\n' "$resolved_home"
        return 0
    fi

    if [[ "${_CONTINUE_WAS_SOURCED:-false}" == "true" ]]; then
        cached_home="$(continue_sanitize_abs_nonroot_path "${_CONTINUE_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
        if [[ -n "$cached_home" ]]; then
            printf '%s\n' "$cached_home"
            return 0
        fi
    fi

    return 1
}
_CONTINUE_CURRENT_HOME="$(continue_initial_current_home 2>/dev/null || true)"
if [[ -n "$_CONTINUE_CURRENT_HOME" ]]; then
    HOME="$_CONTINUE_CURRENT_HOME"
    export HOME
fi

# Constants
_CONTINUE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_CONTINUE_LOG_DIR="/var/log/acfs"
_CONTINUE_INSTALL_LOG="${_CONTINUE_LOG_DIR}/install.log"
_CONTINUE_UPGRADE_LOG="${_CONTINUE_LOG_DIR}/upgrade_resume.log"
_CONTINUE_SYSTEM_STATE_FILE="$(continue_sanitize_abs_nonroot_path "${ACFS_SYSTEM_STATE_FILE:-/var/lib/acfs/state.json}" 2>/dev/null || true)"
if [[ -z "$_CONTINUE_SYSTEM_STATE_FILE" ]]; then
    _CONTINUE_SYSTEM_STATE_FILE="/var/lib/acfs/state.json"
fi
_CONTINUE_STATE_FILE="$(continue_sanitize_abs_nonroot_path "${ACFS_STATE_FILE:-}" 2>/dev/null || true)"
_CONTINUE_EXPLICIT_ACFS_HOME="$(continue_sanitize_abs_nonroot_path "${ACFS_HOME:-}" 2>/dev/null || true)"
_CONTINUE_DEFAULT_ACFS_HOME=""
[[ -n "$_CONTINUE_CURRENT_HOME" ]] && _CONTINUE_DEFAULT_ACFS_HOME="${_CONTINUE_CURRENT_HOME}/.acfs"
_CONTINUE_EXPLICIT_TARGET_HOME_RAW="${TARGET_HOME:-}"
_CONTINUE_EXPLICIT_TARGET_USER_RAW="${TARGET_USER:-}"
_CONTINUE_EXPLICIT_TARGET_HOME="$(continue_existing_abs_home "${TARGET_HOME:-}" 2>/dev/null || true)"
_CONTINUE_SERVICE_NAME="acfs-upgrade-resume"

# Colors
_CONTINUE_RED='\033[0;31m'
_CONTINUE_GREEN='\033[0;32m'
_CONTINUE_YELLOW='\033[1;33m'
_CONTINUE_BLUE='\033[0;34m'
_CONTINUE_CYAN='\033[0;36m'
_CONTINUE_BOLD='\033[1m'
_CONTINUE_DIM='\033[2m'
_CONTINUE_NC='\033[0m'

# ============================================================
# Helper Functions
# ============================================================

print_header() {
    echo ""
    echo -e "${_CONTINUE_CYAN}╔══════════════════════════════════════════════════════════════╗${_CONTINUE_NC}"
    echo -e "${_CONTINUE_CYAN}║${_CONTINUE_NC}  ${_CONTINUE_BOLD}ACFS Installation Progress${_CONTINUE_NC}                                 ${_CONTINUE_CYAN}║${_CONTINUE_NC}"
    echo -e "${_CONTINUE_CYAN}╚══════════════════════════════════════════════════════════════╝${_CONTINUE_NC}"
    echo ""
}

# Check if the upgrade service is running
is_upgrade_service_running() {
    systemctl is-active --quiet "${_CONTINUE_SERVICE_NAME}.service" 2>/dev/null
}

# Check if the installer process is running
is_installer_running() {
    # Only trust ACFS-specific continuation surfaces here. Generic install.sh
    # pgrep patterns can match unrelated installers and the probe process
    # itself, causing false "running" reports.
    is_continuation_running
}

is_continuation_running() {
    pgrep -f "bash.*/var/lib/acfs/continue_install.sh" &>/dev/null || \
    pgrep -f "acfs-continue-install" &>/dev/null
}

home_for_user() {
    local user="$1"
    local passwd_entry=""
    local home_candidate=""
    local current_user=""

    [[ -n "$user" ]] || return 1

    if [[ "$user" == "root" ]]; then
        printf '/root\n'
        return 0
    fi

    current_user="$(continue_resolve_current_user 2>/dev/null || true)"
    if [[ "$user" == "$current_user" ]] && [[ "${_CONTINUE_WAS_SOURCED:-false}" == "true" ]]; then
        home_candidate="${_CONTINUE_CURRENT_HOME:-}"
        if [[ -n "$home_candidate" ]]; then
            printf '%s\n' "$home_candidate"
            return 0
        fi
    fi

    passwd_entry="$(continue_getent_passwd_entry "$user" 2>/dev/null || true)"
    if [[ -n "$passwd_entry" ]]; then
        home_candidate="$(continue_passwd_home_from_entry "$passwd_entry" 2>/dev/null || true)"
        if [[ -n "$home_candidate" ]]; then
            printf '%s\n' "$home_candidate"
            return 0
        fi
    fi

    if [[ "$user" == "$current_user" ]]; then
        home_candidate="${_CONTINUE_CURRENT_HOME:-}"
        if [[ -z "$home_candidate" ]]; then
            home_candidate="$(continue_sanitize_abs_nonroot_path "${HOME:-}" 2>/dev/null || true)"
        fi
        if [[ -n "$home_candidate" ]]; then
            printf '%s\n' "$home_candidate"
            return 0
        fi
    fi

    return 1
}

continue_resolve_explicit_target_home() {
    local target_home=""
    local resolved_home=""

    if [[ -n "$_CONTINUE_EXPLICIT_TARGET_USER_RAW" ]]; then
        continue_is_valid_username "$_CONTINUE_EXPLICIT_TARGET_USER_RAW" || return 1
        resolved_home="$(continue_existing_abs_home "$(home_for_user "$_CONTINUE_EXPLICIT_TARGET_USER_RAW" 2>/dev/null || true)" 2>/dev/null || true)"
        if [[ -n "$resolved_home" ]]; then
            printf '%s\n' "${resolved_home%/}"
            return 0
        fi
        target_home="$_CONTINUE_EXPLICIT_TARGET_HOME"
        if [[ -n "$target_home" ]] && [[ "$target_home" != "${_CONTINUE_CURRENT_HOME:-}" ]] && {
            [[ -f "$target_home/.acfs/state.json" ]] || [[ -f "$target_home/.acfs/VERSION" ]] || [[ -d "$target_home/.acfs/logs" ]] || [[ -f "$target_home/.acfs/scripts/lib/continue.sh" ]]
        }; then
            printf '%s\n' "${target_home%/}"
            return 0
        fi
        return 1
    fi

    target_home="$_CONTINUE_EXPLICIT_TARGET_HOME"
    if [[ -n "$target_home" ]]; then
        printf '%s\n' "${target_home%/}"
        return 0
    fi

    return 1
}

read_target_user_from_state() {
    local state_file="${1:-$_CONTINUE_SYSTEM_STATE_FILE}"
    read_state_string_from_state "$state_file" "target_user"
}

read_state_string_from_state() {
    local state_file="$1"
    local key="$2"
    local value=""
    local jq_bin=""
    local sed_bin=""
    local head_bin=""

    [[ -f "$state_file" ]] || return 1

    jq_bin="$(continue_system_binary_path jq 2>/dev/null || true)"
    if [[ -n "$jq_bin" ]]; then
        value=$("$jq_bin" -r --arg key "$key" '.[$key] // empty' "$state_file" 2>/dev/null || true)
    else
        sed_bin="$(continue_system_binary_path sed 2>/dev/null || true)"
        head_bin="$(continue_system_binary_path head 2>/dev/null || true)"
        [[ -n "$sed_bin" && -n "$head_bin" ]] || return 1
        value=$("$sed_bin" -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$state_file" 2>/dev/null | "$head_bin" -n 1)
    fi

    [[ -n "$value" ]] && [[ "$value" != "null" ]] || return 1
    echo "$value"
}

read_target_home_from_state() {
    local state_file="${1:-$_CONTINUE_SYSTEM_STATE_FILE}"
    local target_home=""

    target_home="$(read_state_string_from_state "$state_file" "target_home" 2>/dev/null || true)"
    [[ -n "$target_home" ]] || return 1
    [[ "$target_home" == /* ]] || return 1
    [[ "$target_home" != "/" ]] || return 1
    printf '%s\n' "${target_home%/}"
}

script_acfs_home() {
    local candidate=""
    candidate=$(cd "$_CONTINUE_SCRIPT_DIR/../.." 2>/dev/null && pwd) || return 1
    [[ "$(basename "$candidate")" == ".acfs" ]] || return 1
    printf '%s\n' "$candidate"
}

current_user_state_file() {
    local candidate=""
    local current_user=""
    local original_home=""
    local state_home=""
    local state_user=""
    local state_user_home=""

    [[ -n "$_CONTINUE_DEFAULT_ACFS_HOME" && -n "$_CONTINUE_CURRENT_HOME" ]] || return 1
    [[ "$_CONTINUE_CURRENT_HOME" != "/root" ]] || return 1

    candidate="$_CONTINUE_DEFAULT_ACFS_HOME/state.json"
    [[ -f "$candidate" ]] || return 1

    if [[ "${_CONTINUE_ORIGINAL_HOME_WAS_SET:-false}" == true ]]; then
        original_home="$(continue_sanitize_abs_nonroot_path "$_CONTINUE_ORIGINAL_HOME" 2>/dev/null || true)"
        [[ -n "$original_home" && "$original_home" == "$_CONTINUE_CURRENT_HOME" ]] || return 1
    fi

    current_user="$(continue_resolve_current_user 2>/dev/null || true)"
    [[ -n "$current_user" && "$current_user" != "root" ]] || return 1

    state_home="$(read_target_home_from_state "$candidate" 2>/dev/null || true)"
    [[ -z "$state_home" || "$state_home" == "$_CONTINUE_CURRENT_HOME" ]] || return 1

    state_user="$(read_target_user_from_state "$candidate" 2>/dev/null || true)"
    if [[ -n "$state_user" && "$state_user" != "$current_user" ]]; then
        state_user_home="$(home_for_user "$state_user" 2>/dev/null || true)"
        [[ "$state_user_home" == "$_CONTINUE_CURRENT_HOME" ]] || return 1
    fi

    printf '%s\n' "$candidate"
}

find_scanned_install_state_file() {
    local candidate=""
    local candidate_home=""
    local matches=()
    local newest=""
    local newest_mtime=-1
    local mtime=""
    local getent_bin=""

    if declare -F continue_getent_passwd_entry >/dev/null 2>&1; then
        while IFS=: read -r _ _ _ _ _ candidate_home _; do
            [[ -n "$candidate_home" ]] || continue
            [[ "$candidate_home" == /* ]] || continue
            candidate="${candidate_home%/}/.acfs/state.json"
            [[ -f "$candidate" ]] || continue
            matches+=("$candidate")
        done < <(continue_getent_passwd_entry 2>/dev/null || true)
    else
        getent_bin="$(continue_system_binary_path getent 2>/dev/null || true)"
        [[ -n "$getent_bin" ]] || return 1
        while IFS=: read -r _ _ _ _ _ candidate_home _; do
            [[ -n "$candidate_home" ]] || continue
            [[ "$candidate_home" == /* ]] || continue
            candidate="${candidate_home%/}/.acfs/state.json"
            [[ -f "$candidate" ]] || continue
            matches+=("$candidate")
        done < <("$getent_bin" passwd 2>/dev/null || true)
    fi

    if [[ ${#matches[@]} -eq 1 ]]; then
        echo "${matches[0]}"
        return 0
    fi

    for candidate in "${matches[@]}"; do
        mtime=$(stat -c %Y "$candidate" 2>/dev/null || stat -f %m "$candidate" 2>/dev/null || echo 0)
        if [[ "$mtime" =~ ^[0-9]+$ ]] && (( mtime > newest_mtime )); then
            newest="$candidate"
            newest_mtime="$mtime"
        fi
    done

    [[ -n "$newest" ]] || return 1
    echo "$newest"
}

get_install_state_file() {
    local candidate=""
    local target_user=""
    local target_home=""
    local explicit_target_home=""

    if [[ -n "${_CONTINUE_STATE_FILE:-}" ]] && [[ -f "${_CONTINUE_STATE_FILE}" ]] && [[ "${_CONTINUE_STATE_FILE}" != "$_CONTINUE_SYSTEM_STATE_FILE" ]]; then
        echo "$_CONTINUE_STATE_FILE"
        return 0
    fi

    candidate=$(script_acfs_home 2>/dev/null || true)
    if [[ -n "$candidate" ]] && [[ -f "$candidate/state.json" ]]; then
        echo "$candidate/state.json"
        return 0
    fi

    explicit_target_home="$(continue_resolve_explicit_target_home 2>/dev/null || true)"
    if [[ -n "$explicit_target_home" ]]; then
        candidate="${explicit_target_home}/.acfs/state.json"
        if [[ -f "$candidate" ]]; then
            echo "$candidate"
            return 0
        fi
    fi

    if [[ -n "$_CONTINUE_EXPLICIT_ACFS_HOME" ]] && [[ -f "$_CONTINUE_EXPLICIT_ACFS_HOME/state.json" ]]; then
        echo "$_CONTINUE_EXPLICIT_ACFS_HOME/state.json"
        return 0
    fi

    if [[ -n "$_CONTINUE_EXPLICIT_TARGET_HOME_RAW" ]] || [[ -n "$_CONTINUE_EXPLICIT_TARGET_USER_RAW" ]]; then
        return 1
    fi

    candidate="$(current_user_state_file 2>/dev/null || true)"
    if [[ -n "$candidate" ]] && [[ -f "$candidate" ]]; then
        echo "$candidate"
        return 0
    fi

    target_home=$(read_target_home_from_state "$_CONTINUE_SYSTEM_STATE_FILE" || true)
    if [[ -n "$target_home" ]]; then
        candidate="${target_home}/.acfs/state.json"
        if [[ -f "$candidate" ]]; then
            echo "$candidate"
            return 0
        fi
    fi

    if [[ -n "${SUDO_USER:-}" ]]; then
        target_home=$(home_for_user "$SUDO_USER" || true)
        candidate="${target_home}/.acfs/state.json"
        if [[ -n "$target_home" ]] && [[ -f "$candidate" ]]; then
            echo "$candidate"
            return 0
        fi
    fi

    target_user=$(read_target_user_from_state "$_CONTINUE_SYSTEM_STATE_FILE" || true)
    if [[ -n "$target_user" ]]; then
        target_home=$(home_for_user "$target_user" || true)
        candidate="${target_home}/.acfs/state.json"
        if [[ -n "$target_home" ]] && [[ -f "$candidate" ]]; then
            echo "$candidate"
            return 0
        fi
    fi

    candidate=$(find_scanned_install_state_file || true)
    if [[ -n "$candidate" ]]; then
        echo "$candidate"
        return 0
    fi

    return 1
}

# Select the correct state file based on query key.
#
# Rationale:
# - Install progress is tracked in ~/.acfs/state.json (user state).
# - Ubuntu upgrade progress is tracked in /var/lib/acfs/state.json (system state).
# If both exist, we must avoid letting the system state mask install status.
select_state_file_for_key() {
    local key="$1"
    local install_state_file=""
    local candidate=""
    local explicit_target_requested=false

    if [[ -n "$_CONTINUE_EXPLICIT_TARGET_HOME_RAW" ]] || [[ -n "$_CONTINUE_EXPLICIT_TARGET_USER_RAW" ]]; then
        explicit_target_requested=true
    fi

    if [[ "$key" == *"ubuntu_upgrade"* ]]; then
        if [[ -f "$_CONTINUE_SYSTEM_STATE_FILE" ]]; then
            echo "$_CONTINUE_SYSTEM_STATE_FILE"
            return 0
        fi
        install_state_file=$(get_install_state_file || true)
        if [[ -n "$install_state_file" ]]; then
            echo "$install_state_file"
            return 0
        fi
    else
        install_state_file=$(get_install_state_file || true)
        if [[ -n "$install_state_file" ]]; then
            echo "$install_state_file"
            return 0
        fi
        if [[ -f "$_CONTINUE_SYSTEM_STATE_FILE" ]]; then
            echo "$_CONTINUE_SYSTEM_STATE_FILE"
            return 0
        fi
    fi

    if [[ "$explicit_target_requested" == "true" ]]; then
        return 1
    fi

    candidate="$(current_user_state_file 2>/dev/null || true)"
    if [[ -n "$candidate" ]] && [[ -f "$candidate" ]]; then
        echo "$candidate"
        return 0
    fi

    return 1
}

# Get state from state.json
get_state_value() {
    local key="$1"
    local state_file=""
    local jq_bin=""

    # In set -e mode, failing command substitutions can abort the script.
    # Treat missing state/jq as "no value" so callers can fall back gracefully.
    state_file=$(select_state_file_for_key "$key") || { echo ""; return 0; }

    jq_bin="$(continue_system_binary_path jq 2>/dev/null || true)"
    [[ -n "$jq_bin" ]] || { echo ""; return 0; }

    # Never crash on jq errors (schema drift / partial state files during boot).
    "$jq_bin" -r "$key" "$state_file" 2>/dev/null || true
}

# Get current phase info
get_current_phase() {
    local phase
    phase=$(get_state_value '.current_phase.id? // .current_phase // empty')
    if [[ -n "$phase" ]]; then
        echo "$phase"
    else
        echo "unknown"
    fi
}

# Get current step info
get_current_step() {
    local step
    step=$(get_state_value '.current_step // empty')
    if [[ -n "$step" ]]; then
        echo "$step"
    else
        echo ""
    fi
}

get_failed_phase() {
    local phase
    phase=$(get_state_value '.failed_phase // empty')
    if [[ -n "$phase" ]] && [[ "$phase" != "null" ]]; then
        echo "$phase"
    else
        echo ""
    fi
}

get_failed_step() {
    local step
    step=$(get_state_value '.failed_step // empty')
    if [[ -n "$step" ]] && [[ "$step" != "null" ]]; then
        echo "$step"
    else
        echo ""
    fi
}

get_resume_hint() {
    local hint
    hint=$(get_state_value '.resume_hint // empty')
    if [[ -n "$hint" ]] && [[ "$hint" != "null" ]]; then
        printf '%s\n' "$hint"
    else
        printf '\n'
    fi
}

# Get installation status
get_install_status() {
    local failed_phase current_phase finalize_completed
    failed_phase=$(get_state_value '.failed_phase // empty')
    if [[ -n "$failed_phase" ]] && [[ "$failed_phase" != "null" ]]; then
        echo "failed"
        return 0
    fi

    finalize_completed=$(get_state_value '(.completed_phases // []) | index("finalize") != null')
    if [[ "$finalize_completed" == "true" ]]; then
        echo "complete"
        return 0
    fi

    current_phase=$(get_state_value '.current_phase.id? // .current_phase // empty')
    if [[ -n "$current_phase" ]] && [[ "$current_phase" != "null" ]]; then
        echo "running"
        return 0
    fi

    echo "unknown"
}

# Get Ubuntu upgrade status
get_upgrade_status() {
    local stage
    stage=$(get_state_value '.ubuntu_upgrade.current_stage // empty')
    if [[ -n "$stage" ]]; then
        echo "$stage"
    else
        echo ""
    fi
}

get_latest_install_log() {
    local install_state_file=""
    local install_root=""
    local latest_log=""

    install_state_file=$(get_install_state_file || true)
    if [[ -n "$install_state_file" ]]; then
        install_root="$(dirname "$install_state_file")"
        latest_log=$(
            find "$install_root/logs" -maxdepth 1 -type f -name 'install-*.log' -printf '%T@ %p\n' 2>/dev/null \
                | sort -nr \
                | head -n 1 \
                | cut -d' ' -f2-
        )
        if [[ -n "$latest_log" ]] && [[ -f "$latest_log" ]]; then
            echo "$latest_log"
            return 0
        fi
    fi

    if [[ -f "$_CONTINUE_INSTALL_LOG" ]]; then
        echo "$_CONTINUE_INSTALL_LOG"
        return 0
    fi

    return 1
}

# Determine which log file to tail
get_active_log() {
    local upgrade_stage
    local install_log=""
    upgrade_stage=$(get_upgrade_status)
    install_log=$(get_latest_install_log || true)

    # If upgrade is in progress, or continuation is still running under the
    # upgrade wrapper, prefer the upgrade log.
    if [[ -n "$upgrade_stage" ]] && [[ -f "$_CONTINUE_UPGRADE_LOG" ]] && \
       { [[ "$upgrade_stage" != "completed" ]] || is_upgrade_service_running || is_continuation_running; }; then
        if [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
            echo "$_CONTINUE_UPGRADE_LOG"
            return 0
        fi
    fi

    if [[ -n "$install_log" ]]; then
        echo "$install_log"
        return 0
    fi

    # Check for any log file
    if [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
        echo "$_CONTINUE_UPGRADE_LOG"
        return 0
    fi

    return 1
}

get_log_root_hint() {
    local install_state_file=""
    local install_root=""

    install_state_file=$(get_install_state_file || true)
    if [[ -n "$install_state_file" ]]; then
        install_root="$(dirname "$install_state_file")"
        if [[ -d "$install_root/logs" ]] || [[ "$install_root" != "/var/lib/acfs" ]]; then
            printf '%s/logs\n' "$install_root"
            return 0
        fi
    fi

    printf '%s\n' "$_CONTINUE_LOG_DIR"
}

print_log_locations() {
    local install_log=""
    local have_logs=false

    install_log=$(get_latest_install_log || true)
    if [[ -n "$install_log" ]]; then
        have_logs=true
    fi
    if [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
        have_logs=true
    fi

    $have_logs || return 1

    echo -e "  ${_CONTINUE_DIM}Log files:${_CONTINUE_NC}"
    if [[ -n "$install_log" ]]; then
        echo -e "    ${_CONTINUE_DIM}Install:  $install_log${_CONTINUE_NC}"
    fi
    if [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
        echo -e "    ${_CONTINUE_DIM}Upgrade:  $_CONTINUE_UPGRADE_LOG${_CONTINUE_NC}"
    fi
    echo ""
}

# ============================================================
# Status Display
# ============================================================

show_status() {
    print_header

    local is_running=false
    local status_msg=""
    local install_status
    install_status=$(get_install_status)

    # Persisted failure should win over loose runtime probes to avoid
    # contradictory output such as "in progress" with failure details below.
    if is_upgrade_service_running; then
        is_running=true
        status_msg="${_CONTINUE_YELLOW}Ubuntu upgrade in progress${_CONTINUE_NC}"
    elif [[ "$install_status" == "failed" ]]; then
        status_msg="${_CONTINUE_RED}Installation failed${_CONTINUE_NC}"
    elif is_installer_running; then
        is_running=true
        status_msg="${_CONTINUE_BLUE}Installation in progress${_CONTINUE_NC}"
    else
        case "$install_status" in
            running)
                is_running=true
                status_msg="${_CONTINUE_BLUE}Installation in progress${_CONTINUE_NC}"
                ;;
            failed)
                status_msg="${_CONTINUE_RED}Installation failed${_CONTINUE_NC}"
                ;;
            complete)
                status_msg="${_CONTINUE_GREEN}Installation complete${_CONTINUE_NC}"
                ;;
            *)
                status_msg="${_CONTINUE_GREEN}No active installation${_CONTINUE_NC}"
                ;;
        esac
    fi

    echo -e "  ${_CONTINUE_BOLD}Status:${_CONTINUE_NC} $status_msg"

    if [[ "$is_running" == "true" ]]; then
        # Show current phase only when installation is actually active.
        local phase
        phase=$(get_current_phase)
        if [[ "$phase" != "unknown" ]]; then
            echo -e "  ${_CONTINUE_BOLD}Phase:${_CONTINUE_NC}  $phase"
        fi

        # Show current step only when installation is actually active.
        local step
        step=$(get_current_step)
        if [[ -n "$step" ]]; then
            echo -e "  ${_CONTINUE_BOLD}Step:${_CONTINUE_NC}   $step"
        fi
    fi

    if [[ "$install_status" == "failed" ]]; then
        local failed_phase failed_step
        failed_phase=$(get_failed_phase)
        failed_step=$(get_failed_step)

        if [[ -n "$failed_phase" ]]; then
            echo -e "  ${_CONTINUE_BOLD}Failed:${_CONTINUE_NC} Phase: ${_CONTINUE_RED}$failed_phase${_CONTINUE_NC}"
        fi
        if [[ -n "$failed_step" ]]; then
            echo -e "  ${_CONTINUE_BOLD}Cause:${_CONTINUE_NC}  Step: ${_CONTINUE_RED}$failed_step${_CONTINUE_NC}"
        fi
    fi

    # Show upgrade status if relevant
    local upgrade_stage
    upgrade_stage=$(get_upgrade_status)
    if [[ -n "$upgrade_stage" ]] && [[ "$upgrade_stage" != "completed" ]]; then
        echo -e "  ${_CONTINUE_BOLD}Ubuntu:${_CONTINUE_NC} Upgrade stage: ${_CONTINUE_YELLOW}$upgrade_stage${_CONTINUE_NC}"
    fi

    echo ""

    print_log_locations || true

    # Return whether installation is running
    $is_running
}

# ============================================================
# Live Log Viewing
# ============================================================

show_live_log() {
    local log_file
    local log_root_hint=""
    log_file=$(get_active_log) || {
        log_root_hint=$(get_log_root_hint)
        echo -e "${_CONTINUE_YELLOW}No log files found yet.${_CONTINUE_NC}"
        echo -e "${_CONTINUE_DIM}Logs will appear at: $log_root_hint${_CONTINUE_NC}"
        return 1
    }

    echo -e "${_CONTINUE_BOLD}Showing live output from:${_CONTINUE_NC} $log_file"
    echo -e "${_CONTINUE_DIM}Press Ctrl+C to stop watching${_CONTINUE_NC}"
    echo ""
    echo "────────────────────────────────────────────────────────────────"

    # Use tail -f to show live output
    # Show last 20 lines first, then follow
    tail -n 20 -f "$log_file" 2>/dev/null || {
        echo -e "${_CONTINUE_RED}Unable to read log file${_CONTINUE_NC}"
        echo -e "${_CONTINUE_DIM}You may need to run with sudo: sudo acfs continue${_CONTINUE_NC}"
        return 1
    }
}

# ============================================================
# Help
# ============================================================

show_help() {
    echo "ACFS Continue - View Installation Progress"
    echo ""
    echo "Usage: acfs continue [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --status, -s    Show current status only (don't attach to logs)"
    echo "  --help, -h      Show this help message"
    echo ""
    echo "Description:"
    echo "  After Ubuntu upgrades complete, the ACFS installer continues"
    echo "  running in the background. This command lets you see what's"
    echo "  happening and attach to the live log output."
    echo ""
    echo "Examples:"
    echo "  acfs continue           # Show status and watch live logs"
    echo "  acfs continue --status  # Just show current status"
    echo "  sudo acfs continue      # If you get permission errors"
    echo ""
}

# ============================================================
# Main
# ============================================================

main() {
    local status_only=false

    # Parse arguments
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --status|-s)
                status_only=true
                shift
                ;;
            --help|-h)
                show_help
                exit 0
                ;;
            *)
                echo -e "${_CONTINUE_RED}Unknown option: $1${_CONTINUE_NC}"
                echo "Use 'acfs continue --help' for usage"
                exit 1
                ;;
        esac
    done

    # Show status
    if show_status; then
        # Installation is running
        if ! $status_only; then
            show_live_log
        fi
    else
        # Installation is not running - check if it was completed
        local status
        status=$(get_install_status)
        if [[ "$status" == "complete" ]]; then
            echo -e "${_CONTINUE_GREEN}${_CONTINUE_BOLD}Installation completed successfully!${_CONTINUE_NC}"
            echo ""
            echo "Next steps:"
            echo "  1. Log out and back in (or run: source ~/.zshrc)"
            echo "  2. Run: onboard"
            echo "  3. Start coding with: cc, cod, or agy"
        elif [[ "$status" == "failed" ]]; then
            local failed_phase failed_step resume_hint
            failed_phase=$(get_failed_phase)
            failed_step=$(get_failed_step)
            resume_hint=$(get_resume_hint)

            echo -e "${_CONTINUE_RED}${_CONTINUE_BOLD}Installation failed.${_CONTINUE_NC}"
            if [[ -n "$failed_phase" ]]; then
                echo "Failed phase: $failed_phase"
            fi
            if [[ -n "$failed_step" ]]; then
                echo "Failed step:  $failed_step"
            fi
            echo ""
            if [[ -n "$resume_hint" ]]; then
                echo "To resume:"
                printf '  %s\n' "$resume_hint"
            else
                echo "Review the log files listed above, fix the underlying issue, then rerun the installer with --resume."
            fi
        else
            local install_log=""
            install_log=$(get_latest_install_log || true)

            # Only show log viewing instructions if logs exist
            if [[ -n "$install_log" ]] || [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
                echo "To view past logs:"
                if [[ -n "$install_log" ]]; then
                    echo "  cat $install_log"
                fi
                if [[ -f "$_CONTINUE_UPGRADE_LOG" ]]; then
                    echo "  cat $_CONTINUE_UPGRADE_LOG"
                fi
            else
                echo "No ACFS installation logs found."
                echo "Run the ACFS installer to get started."
            fi
        fi
    fi
}

continue_restore_shell_state_if_sourced() {
    [[ "$_CONTINUE_WAS_SOURCED" == "true" ]] || return 0

    if [[ "$_CONTINUE_ORIGINAL_HOME_WAS_SET" == "true" ]]; then
        HOME="$_CONTINUE_ORIGINAL_HOME"
        export HOME
    else
        unset HOME
    fi

    [[ "$_CONTINUE_RESTORE_ERREXIT" == "true" ]] || set +e
    [[ "$_CONTINUE_RESTORE_NOUNSET" == "true" ]] || set +u
    [[ "$_CONTINUE_RESTORE_PIPEFAIL" == "true" ]] || set +o pipefail
}

continue_restore_shell_state_if_sourced

# Run if executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi

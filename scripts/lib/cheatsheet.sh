#!/usr/bin/env bash
# ============================================================
# ACFS Cheatsheet - discover installed aliases/commands
# Source of truth: ~/.acfs/zsh/acfs.zshrc
# ============================================================

_CHEATSHEET_WAS_SOURCED=false
_CHEATSHEET_ORIGINAL_HOME=""
_CHEATSHEET_ORIGINAL_HOME_WAS_SET=false
_CHEATSHEET_RESTORE_ERREXIT=false
_CHEATSHEET_RESTORE_NOUNSET=false
_CHEATSHEET_RESTORE_PIPEFAIL=false
if [[ -v HOME ]]; then
  _CHEATSHEET_ORIGINAL_HOME="$HOME"
  _CHEATSHEET_ORIGINAL_HOME_WAS_SET=true
fi
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  _CHEATSHEET_WAS_SOURCED=true
  [[ $- == *e* ]] && _CHEATSHEET_RESTORE_ERREXIT=true
  [[ $- == *u* ]] && _CHEATSHEET_RESTORE_NOUNSET=true
  if shopt -qo pipefail 2>/dev/null; then
    _CHEATSHEET_RESTORE_PIPEFAIL=true
  fi
fi

set -euo pipefail

cheatsheet_sanitize_abs_nonroot_path() {
  local path_value="${1:-}"

  [[ -n "$path_value" ]] || return 1
  path_value="${path_value%/}"
  [[ -n "$path_value" ]] || return 1
  [[ "$path_value" == /* ]] || return 1
  [[ "$path_value" != "/" ]] || return 1
  printf '%s\n' "$path_value"
}

cheatsheet_existing_abs_home() {
  local path_value=""

  path_value="$(cheatsheet_sanitize_abs_nonroot_path "${1:-}" 2>/dev/null || true)"
  [[ -n "$path_value" ]] || return 1
  [[ -d "$path_value" ]] || return 1
  printf '%s\n' "$path_value"
}

cheatsheet_system_binary_path() {
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

cheatsheet_resolve_current_user() {
  local current_user=""
  local id_bin=""
  local whoami_bin=""

  id_bin="$(cheatsheet_system_binary_path id 2>/dev/null || true)"
  if [[ -n "$id_bin" ]]; then
      current_user="$("$id_bin" -un 2>/dev/null || true)"
  fi

  if [[ -z "$current_user" ]]; then
      whoami_bin="$(cheatsheet_system_binary_path whoami 2>/dev/null || true)"
      if [[ -n "$whoami_bin" ]]; then
          current_user="$("$whoami_bin" 2>/dev/null || true)"
      fi
  fi

  [[ -n "$current_user" ]] || return 1
  printf '%s\n' "$current_user"
}

cheatsheet_getent_passwd_entry() {
  local user="${1-}"
  local getent_bin=""
  local passwd_entry=""
  local passwd_line=""
  local printed_any=false

  getent_bin="$(cheatsheet_system_binary_path getent 2>/dev/null || true)"
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

cheatsheet_passwd_home_from_entry() {
  local passwd_entry="${1:-}"
  local passwd_home=""

  [[ -n "$passwd_entry" ]] || return 1
  IFS=: read -r _ _ _ _ _ passwd_home _ <<< "$passwd_entry"
  passwd_home="$(cheatsheet_sanitize_abs_nonroot_path "$passwd_home" 2>/dev/null || true)"
  [[ -n "$passwd_home" ]] || return 1
  printf '%s\n' "$passwd_home"
}
cheatsheet_is_valid_username() {
  local username="${1:-}"
  [[ "$username" =~ ^[a-z_][a-z0-9._-]*$ ]]
}

cheatsheet_resolve_current_home() {
  local current_user=""
  local fallback_home=""
  local passwd_entry=""
  local passwd_home=""
  fallback_home="$(cheatsheet_sanitize_abs_nonroot_path "${HOME:-}" 2>/dev/null || true)"
  if [[ "${_CHEATSHEET_WAS_SOURCED:-false}" == "true" ]]; then
      fallback_home="$(cheatsheet_sanitize_abs_nonroot_path "${_CHEATSHEET_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
  fi
  current_user="$(cheatsheet_resolve_current_user 2>/dev/null || true)"

  if [[ "$current_user" == "root" ]]; then
      printf '/root\n'
      return 0
  fi

  if [[ -n "$current_user" ]]; then
      passwd_entry="$(cheatsheet_getent_passwd_entry "$current_user" 2>/dev/null || true)"
      if [[ -n "$passwd_entry" ]]; then
          passwd_home="$(cheatsheet_passwd_home_from_entry "$passwd_entry" 2>/dev/null || true)"
          if [[ -n "$passwd_home" ]]; then
              printf '%s\n' "$passwd_home"
              return 0
          fi
      fi
  fi

  [[ -n "$fallback_home" ]] || return 1
  printf '%s\n' "$fallback_home"
}
cheatsheet_initial_current_home() {
  local cached_home=""
  local resolved_home=""

  if [[ "${_CHEATSHEET_WAS_SOURCED:-false}" == "true" ]] && [[ -z "${TARGET_HOME:-}${TARGET_USER:-}${ACFS_HOME:-}${ACFS_STATE_FILE:-}${ACFS_SYSTEM_STATE_FILE:-}" ]]; then
      cached_home="$(cheatsheet_sanitize_abs_nonroot_path "${_CHEATSHEET_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
      if [[ -n "$cached_home" ]]; then
          printf '%s\n' "$cached_home"
          return 0
      fi
  fi

  resolved_home="$(cheatsheet_resolve_current_home 2>/dev/null || true)"
  if [[ -n "$resolved_home" ]]; then
      printf '%s\n' "$resolved_home"
      return 0
  fi

  if [[ "${_CHEATSHEET_WAS_SOURCED:-false}" == "true" ]]; then
      cached_home="$(cheatsheet_sanitize_abs_nonroot_path "${_CHEATSHEET_ORIGINAL_HOME:-${HOME:-}}" 2>/dev/null || true)"
      if [[ -n "$cached_home" ]]; then
          printf '%s\n' "$cached_home"
          return 0
      fi
  fi

  return 1
}
_CHEATSHEET_CURRENT_HOME="$(cheatsheet_initial_current_home 2>/dev/null || true)"
if [[ -n "$_CHEATSHEET_CURRENT_HOME" ]]; then
  HOME="$_CHEATSHEET_CURRENT_HOME"
  export HOME
fi

_CHEATSHEET_EXPLICIT_ACFS_HOME="$(cheatsheet_sanitize_abs_nonroot_path "${ACFS_HOME:-}" 2>/dev/null || true)"
_CHEATSHEET_DEFAULT_ACFS_HOME=""
[[ -n "$_CHEATSHEET_CURRENT_HOME" ]] && _CHEATSHEET_DEFAULT_ACFS_HOME="${_CHEATSHEET_CURRENT_HOME}/.acfs"
_CHEATSHEET_ACFS_HOME="${_CHEATSHEET_EXPLICIT_ACFS_HOME:-$_CHEATSHEET_DEFAULT_ACFS_HOME}"
_CHEATSHEET_SYSTEM_STATE_WAS_EXPLICIT=false
[[ -n "${ACFS_SYSTEM_STATE_FILE:-}" ]] && [[ "${ACFS_SYSTEM_STATE_FILE%/}" != "/var/lib/acfs/state.json" ]] && _CHEATSHEET_SYSTEM_STATE_WAS_EXPLICIT=true
_CHEATSHEET_VERSION="${ACFS_VERSION:-0.1.0}"
CHEATSHEET_DELIM=$'\t'
_CHEATSHEET_SYSTEM_STATE_FILE="$(cheatsheet_sanitize_abs_nonroot_path "${ACFS_SYSTEM_STATE_FILE:-/var/lib/acfs/state.json}" 2>/dev/null || true)"
if [[ -z "$_CHEATSHEET_SYSTEM_STATE_FILE" ]]; then
  _CHEATSHEET_SYSTEM_STATE_FILE="/var/lib/acfs/state.json"
fi
_CHEATSHEET_EXPLICIT_TARGET_HOME_RAW="${TARGET_HOME:-}"
_CHEATSHEET_EXPLICIT_TARGET_USER_RAW="${TARGET_USER:-}"
_CHEATSHEET_EXPLICIT_TARGET_HOME="$(cheatsheet_existing_abs_home "${TARGET_HOME:-}" 2>/dev/null || true)"
_CHEATSHEET_RESOLVED_ACFS_HOME=""
_CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE=""
_CHEATSHEET_RESOLVED_TARGET_USER=""
_CHEATSHEET_RESOLVED_TARGET_HOME=""

_CHEATSHEET_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source output formatting library (for TOON support)
if [[ -f "$_CHEATSHEET_SCRIPT_DIR/output.sh" ]]; then
    # shellcheck source=output.sh
    source "$_CHEATSHEET_SCRIPT_DIR/output.sh"
fi

# Global format options (set by argument parsing)
_CHEATSHEET_OUTPUT_FORMAT=""
_CHEATSHEET_SHOW_STATS=false

if [[ -f "$_CHEATSHEET_SCRIPT_DIR/../../VERSION" ]]; then
  _CHEATSHEET_VERSION="$(cat "$_CHEATSHEET_SCRIPT_DIR/../../VERSION" 2>/dev/null || echo "$_CHEATSHEET_VERSION")"
elif [[ -n "$_CHEATSHEET_ACFS_HOME" ]] && [[ -f "$_CHEATSHEET_ACFS_HOME/VERSION" ]]; then
  _CHEATSHEET_VERSION="$(cat "$_CHEATSHEET_ACFS_HOME/VERSION" 2>/dev/null || echo "$_CHEATSHEET_VERSION")"
fi

_CHEATSHEET_HAS_GUM=false
command -v gum &>/dev/null && _CHEATSHEET_HAS_GUM=true

print_help() {
  cat <<'EOF'
ACFS Cheatsheet (aliases + quick commands)

Usage:
  acfs cheatsheet [query]
  acfs cheatsheet --category <name>
  acfs cheatsheet --search <pattern>
  acfs cheatsheet --json
  acfs cheatsheet --format <json|toon>
  acfs cheatsheet --stats
  acfs cheatsheet --zshrc <path>

Options:
  --json           Output as JSON
  --format <fmt>   Output format: json or toon (env: ACFS_OUTPUT_FORMAT, TOON_DEFAULT_FORMAT)
  --toon, -t       Shorthand for --format toon
  --stats          Show token savings statistics (JSON vs TOON bytes)

Examples:
  acfs cheatsheet
  acfs cheatsheet git
  acfs cheatsheet "push"
  acfs cheatsheet --category Agents
  acfs cheatsheet --search docker
  acfs cheatsheet --format toon --stats
EOF
}

cheatsheet_home_for_user() {
  local user="$1"
  local passwd_entry=""
  local home_candidate=""
  local current_user=""

  [[ -n "$user" ]] || return 1

  if [[ "$user" == "root" ]]; then
      printf '/root\n'
      return 0
  fi

  passwd_entry="$(cheatsheet_getent_passwd_entry "$user" 2>/dev/null || true)"
  if [[ -n "$passwd_entry" ]]; then
      home_candidate="$(cheatsheet_passwd_home_from_entry "$passwd_entry" 2>/dev/null || true)"
      if [[ -n "$home_candidate" ]]; then
          printf '%s\n' "$home_candidate"
          return 0
      fi
  fi

  current_user="$(cheatsheet_resolve_current_user 2>/dev/null || true)"
  if [[ "$user" == "$current_user" ]]; then
      home_candidate="${_CHEATSHEET_CURRENT_HOME:-}"
      if [[ -z "$home_candidate" ]]; then
          home_candidate="$(cheatsheet_sanitize_abs_nonroot_path "${HOME:-}" 2>/dev/null || true)"
      fi
      if [[ -n "$home_candidate" ]]; then
          printf '%s\n' "$home_candidate"
          return 0
      fi
  fi

  return 1
}

cheatsheet_resolve_explicit_target_home() {
  local target_home=""
  local resolved_home=""

  if [[ -n "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW" ]]; then
    cheatsheet_is_valid_username "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW" || return 1
    resolved_home="$(cheatsheet_existing_abs_home "$(cheatsheet_home_for_user "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW" 2>/dev/null || true)" 2>/dev/null || true)"
    if [[ -n "$resolved_home" ]]; then
      printf '%s\n' "${resolved_home%/}"
      return 0
    fi
    target_home="$_CHEATSHEET_EXPLICIT_TARGET_HOME"
    if [[ -n "$target_home" ]] && [[ "$target_home" != "${_CHEATSHEET_CURRENT_HOME:-}" ]] && cheatsheet_candidate_has_acfs_data "$target_home/.acfs"; then
      printf '%s\n' "${target_home%/}"
      return 0
    fi
    return 1
  fi

  target_home="$_CHEATSHEET_EXPLICIT_TARGET_HOME"
  if [[ -n "$target_home" ]]; then
    printf '%s\n' "${target_home%/}"
    return 0
  fi

  return 1
}

cheatsheet_read_user_for_home() {
  local user_home="$1"
  local candidate_user=""
  local current_home=""
  local passwd_line=""
  local passwd_home=""
  local state_file=""

  user_home="$(cheatsheet_sanitize_abs_nonroot_path "$user_home" 2>/dev/null || true)"
  [[ -n "$user_home" ]] || return 1

  while IFS= read -r passwd_line; do
    passwd_home="$(cheatsheet_passwd_home_from_entry "$passwd_line" 2>/dev/null || true)"
    [[ "$passwd_home" == "$user_home" ]] || continue
    candidate_user="${passwd_line%%:*}"
    if [[ "$candidate_user" =~ ^[a-z_][a-z0-9._-]*$ ]]; then
      printf '%s\n' "$candidate_user"
      return 0
    fi
  done < <(cheatsheet_getent_passwd_entry 2>/dev/null || true)

  current_home="${_CHEATSHEET_CURRENT_HOME:-}"
  if [[ -n "$current_home" ]] && [[ "$user_home" == "$current_home" ]]; then
    candidate_user="$(cheatsheet_resolve_current_user 2>/dev/null || true)"
    if [[ "$candidate_user" =~ ^[a-z_][a-z0-9._-]*$ ]]; then
      printf '%s\n' "$candidate_user"
      return 0
    fi
  fi

  if [[ "$user_home" == "/root" ]]; then
    printf 'root\n'
    return 0
  fi

  state_file="$user_home/.acfs/state.json"
  candidate_user="$(cheatsheet_read_state_string "$state_file" "target_user" 2>/dev/null || true)"
  if [[ -n "$candidate_user" ]]; then
    current_home="$(cheatsheet_home_for_user "$candidate_user" 2>/dev/null || true)"
    if [[ -n "$current_home" ]] && [[ "$current_home" == "$user_home" ]]; then
      printf '%s\n' "$candidate_user"
      return 0
    fi
  fi

  return 1
}

cheatsheet_validate_bin_dir_for_home() {
  local bin_dir="${1:-}"
  local base_home="${2:-}"
  local passwd_line=""
  local passwd_home=""
  local hinted_home=""

  bin_dir="$(cheatsheet_sanitize_abs_nonroot_path "$bin_dir" 2>/dev/null || true)"
  [[ -n "$bin_dir" ]] || return 1
  base_home="$(cheatsheet_sanitize_abs_nonroot_path "$base_home" 2>/dev/null || true)"

  if [[ -n "$base_home" ]] && [[ "$bin_dir" == "$base_home" || "$bin_dir" == "$base_home/"* ]]; then
    printf '%s\n' "$bin_dir"
    return 0
  fi

  case "$bin_dir" in
    */.local/bin) hinted_home="${bin_dir%/.local/bin}" ;;
    */.acfs/bin) hinted_home="${bin_dir%/.acfs/bin}" ;;
    */.bun/bin) hinted_home="${bin_dir%/.bun/bin}" ;;
    */.cargo/bin) hinted_home="${bin_dir%/.cargo/bin}" ;;
    */.atuin/bin) hinted_home="${bin_dir%/.atuin/bin}" ;;
    */go/bin) hinted_home="${bin_dir%/go/bin}" ;;
    */google-cloud-sdk/bin) hinted_home="${bin_dir%/google-cloud-sdk/bin}" ;;
  esac
  hinted_home="$(cheatsheet_sanitize_abs_nonroot_path "$hinted_home" 2>/dev/null || true)"
  if [[ -n "$hinted_home" ]] && [[ -n "$base_home" ]] && [[ "$hinted_home" != "$base_home" ]]; then
    return 1
  fi

  while IFS= read -r passwd_line; do
    passwd_home="$(cheatsheet_passwd_home_from_entry "$passwd_line" 2>/dev/null || true)"
    [[ -n "$passwd_home" ]] || continue
    [[ -n "$base_home" && "$passwd_home" == "$base_home" ]] && continue
    if [[ "$bin_dir" == "$passwd_home" || "$bin_dir" == "$passwd_home/"* ]]; then
      return 1
    fi
  done < <(cheatsheet_getent_passwd_entry 2>/dev/null || true)

  printf '%s\n' "$bin_dir"
}
cheatsheet_read_state_string() {
  local state_file="$1"
  local key="$2"
  local value=""
  local jq_bin=""
  local sed_bin=""
  local head_bin=""

  [[ -f "$state_file" ]] || return 1

  jq_bin="$(cheatsheet_system_binary_path jq 2>/dev/null || true)"
  if [[ -n "$jq_bin" ]]; then
    value=$("$jq_bin" -r --arg key "$key" '.[$key] // empty' "$state_file" 2>/dev/null || true)
  else
    sed_bin="$(cheatsheet_system_binary_path sed 2>/dev/null || true)"
    head_bin="$(cheatsheet_system_binary_path head 2>/dev/null || true)"
    [[ -n "$sed_bin" && -n "$head_bin" ]] || return 1
    value=$("$sed_bin" -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$state_file" 2>/dev/null | "$head_bin" -n 1)
  fi

  [[ -n "$value" ]] && [[ "$value" != "null" ]] || return 1
  printf '%s\n' "$value"
}

cheatsheet_read_target_home_from_state() {
  local state_file="$1"
  local target_home=""

  target_home="$(cheatsheet_read_state_string "$state_file" "target_home" 2>/dev/null || true)"
  [[ -n "$target_home" ]] || return 1
  [[ "$target_home" == /* ]] || return 1
  [[ "$target_home" != "/" ]] || return 1
  printf '%s\n' "${target_home%/}"
}

cheatsheet_candidate_has_acfs_data() {
  local candidate="$1"
  [[ -n "$candidate" ]] || return 1
  [[ -f "$candidate/state.json" || -f "$candidate/VERSION" || -f "$candidate/zsh/acfs.zshrc" ]]
}

cheatsheet_script_acfs_home() {
  local candidate=""
  candidate=$(cd "$_CHEATSHEET_SCRIPT_DIR/../.." 2>/dev/null && pwd) || return 1
  [[ "$(basename "$candidate")" == ".acfs" ]] || return 1
  printf '%s\n' "$candidate"
}

cheatsheet_current_home_acfs_candidate() {
  local candidate="$_CHEATSHEET_DEFAULT_ACFS_HOME"
  local current_home="$_CHEATSHEET_CURRENT_HOME"
  local current_user=""
  local original_home=""
  local state_home=""
  local state_user=""
  local state_user_home=""

  [[ -n "$candidate" && -n "$current_home" ]] || return 1
  [[ "$current_home" != "/root" ]] || return 1
  cheatsheet_candidate_has_acfs_data "$candidate" || return 1

  if [[ "${_CHEATSHEET_ORIGINAL_HOME_WAS_SET:-false}" == true ]]; then
    original_home="$(cheatsheet_sanitize_abs_nonroot_path "$_CHEATSHEET_ORIGINAL_HOME" 2>/dev/null || true)"
    [[ -n "$original_home" && "$original_home" == "$current_home" ]] || return 1
  fi

  current_user="$(cheatsheet_resolve_current_user 2>/dev/null || true)"
  [[ -n "$current_user" && "$current_user" != "root" ]] || return 1

  if [[ -f "$candidate/state.json" ]]; then
    state_home="$(cheatsheet_read_target_home_from_state "$candidate/state.json" 2>/dev/null || true)"
    [[ -z "$state_home" || "$state_home" == "$current_home" ]] || return 1

    state_user="$(cheatsheet_read_state_string "$candidate/state.json" "target_user" 2>/dev/null || true)"
    if [[ -n "$state_user" && "$state_user" != "$current_user" ]]; then
      state_user_home="$(cheatsheet_home_for_user "$state_user" 2>/dev/null || true)"
      [[ "$state_user_home" == "$current_home" ]] || return 1
    fi
  fi

  printf '%s\n' "$candidate"
}

cheatsheet_resolve_acfs_home() {
  if [[ -n "$_CHEATSHEET_RESOLVED_ACFS_HOME" ]]; then
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  local candidate=""
  local target_home=""
  local target_user=""
  local explicit_target_home=""

  _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE=""

  candidate=$(cheatsheet_script_acfs_home 2>/dev/null || true)
  if cheatsheet_candidate_has_acfs_data "$candidate"; then
    _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
    _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="script_acfs_home"
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  explicit_target_home="$(cheatsheet_resolve_explicit_target_home 2>/dev/null || true)"
  if [[ -n "$explicit_target_home" ]]; then
    candidate="${explicit_target_home}/.acfs"
    if cheatsheet_candidate_has_acfs_data "$candidate"; then
      _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
      _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="explicit_target_home"
      printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
      return 0
    fi
  fi

  if [[ ! -f "$_CHEATSHEET_SYSTEM_STATE_FILE" ]] && [[ -n "$_CHEATSHEET_EXPLICIT_ACFS_HOME" ]] && cheatsheet_candidate_has_acfs_data "$_CHEATSHEET_EXPLICIT_ACFS_HOME"; then
    _CHEATSHEET_RESOLVED_ACFS_HOME="$_CHEATSHEET_EXPLICIT_ACFS_HOME"
    _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="explicit_acfs_home"
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  candidate="$(cheatsheet_current_home_acfs_candidate 2>/dev/null || true)"
  if [[ -n "$candidate" ]]; then
    _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
    _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="current_home"
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  if [[ "$_CHEATSHEET_SYSTEM_STATE_WAS_EXPLICIT" == true ]]; then
    target_home=$(cheatsheet_read_target_home_from_state "$_CHEATSHEET_SYSTEM_STATE_FILE" 2>/dev/null || true)
    candidate="${target_home}/.acfs"
    if [[ -n "$target_home" ]] && cheatsheet_candidate_has_acfs_data "$candidate"; then
      _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
      _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="system_state_target_home"
      printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
      return 0
    fi

    target_user=$(cheatsheet_read_state_string "$_CHEATSHEET_SYSTEM_STATE_FILE" "target_user" 2>/dev/null || true)
    if [[ -n "$target_user" ]]; then
      target_home=$(cheatsheet_home_for_user "$target_user" 2>/dev/null || true)
      candidate="${target_home}/.acfs"
      if [[ -n "$target_home" ]] && cheatsheet_candidate_has_acfs_data "$candidate"; then
        _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
        _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="system_state_target_user"
        printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
        return 0
      fi
    fi
  fi

  if [[ -n "$_CHEATSHEET_EXPLICIT_ACFS_HOME" ]] && cheatsheet_candidate_has_acfs_data "$_CHEATSHEET_EXPLICIT_ACFS_HOME"; then
    _CHEATSHEET_RESOLVED_ACFS_HOME="$_CHEATSHEET_EXPLICIT_ACFS_HOME"
    _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="explicit_acfs_home"
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  if [[ -n "$_CHEATSHEET_EXPLICIT_TARGET_HOME_RAW" ]] || [[ -n "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW" ]]; then
    return 1
  fi

  if [[ -n "${SUDO_USER:-}" ]]; then
    target_home=$(cheatsheet_home_for_user "$SUDO_USER" 2>/dev/null || true)
    candidate="${target_home}/.acfs"
    if [[ -n "$target_home" ]] && cheatsheet_candidate_has_acfs_data "$candidate"; then
      _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
      _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="sudo_user_home"
      printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
      return 0
    fi
  fi

  target_home=$(cheatsheet_read_target_home_from_state "$_CHEATSHEET_SYSTEM_STATE_FILE" 2>/dev/null || true)
  candidate="${target_home}/.acfs"
  if [[ -n "$target_home" ]] && cheatsheet_candidate_has_acfs_data "$candidate"; then
    _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
    _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="system_state_target_home"
    printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
    return 0
  fi

  target_user=$(cheatsheet_read_state_string "$_CHEATSHEET_SYSTEM_STATE_FILE" "target_user" 2>/dev/null || true)
  if [[ -n "$target_user" ]]; then
    if [[ -z "$target_home" ]]; then
      target_home=$(cheatsheet_home_for_user "$target_user" 2>/dev/null || true)
    fi
    candidate="${target_home}/.acfs"
    if [[ -n "$target_home" ]] && cheatsheet_candidate_has_acfs_data "$candidate"; then
      _CHEATSHEET_RESOLVED_ACFS_HOME="$candidate"
      _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="system_state_target_user"
      printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
      return 0
    fi
  fi

  _CHEATSHEET_RESOLVED_ACFS_HOME="$_CHEATSHEET_DEFAULT_ACFS_HOME"
  _CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE="current_home"
  printf '%s\n' "$_CHEATSHEET_RESOLVED_ACFS_HOME"
}

cheatsheet_resolve_state_file() {
  local candidate=""

  if [[ -n "$_CHEATSHEET_ACFS_HOME" ]]; then
    candidate="${_CHEATSHEET_ACFS_HOME}/state.json"
  fi

  if [[ -n "$candidate" ]] && [[ -f "$candidate" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  if [[ -f "$_CHEATSHEET_SYSTEM_STATE_FILE" ]]; then
    printf '%s\n' "$_CHEATSHEET_SYSTEM_STATE_FILE"
    return 0
  fi

  printf '%s\n' "$candidate"
}

cheatsheet_prepend_user_paths() {
  local base_home="$1"
  local dir=""
  local primary_bin_dir="${ACFS_BIN_DIR:-$base_home/.local/bin}"
  local current_path="${PATH:-}"
  local seen_path=":${current_path}:"
  local -a to_prepend=()

  [[ -n "$base_home" ]] || return 0
  if declare -f cheatsheet_validate_bin_dir_for_home >/dev/null 2>&1; then
    primary_bin_dir="$(cheatsheet_validate_bin_dir_for_home "$primary_bin_dir" "$base_home" 2>/dev/null || true)"
  fi
  [[ -n "$primary_bin_dir" ]] || primary_bin_dir="$base_home/.local/bin"

  for dir in \
    "$primary_bin_dir" \
    "$base_home/.local/bin" \
    "$base_home/.acfs/bin" \
    "$base_home/.bun/bin" \
    "$base_home/.cargo/bin" \
    "$base_home/go/bin" \
    "$base_home/.atuin/bin" \
    "$base_home/google-cloud-sdk/bin"; do
    [[ -d "$dir" ]] || continue
    case "$seen_path" in
      *":$dir:"*) ;;
      *)
        to_prepend+=("$dir")
        seen_path="${seen_path}${dir}:"
        ;;
    esac
  done

  if [[ ${#to_prepend[@]} -gt 0 ]]; then
    local prefix=""
    prefix="$(IFS=:; printf '%s' "${to_prepend[*]}")"
    export PATH="$prefix${current_path:+:$current_path}"
  fi
}

cheatsheet_infer_target_home_from_acfs_home() {
  local acfs_home_candidate=""
  local inferred_home=""

  acfs_home_candidate="$(cheatsheet_sanitize_abs_nonroot_path "${_CHEATSHEET_ACFS_HOME:-}" 2>/dev/null || true)"
  [[ -n "$acfs_home_candidate" ]] || return 1
  [[ "$(basename "$acfs_home_candidate")" == ".acfs" ]] || return 1
  [[ -f "$acfs_home_candidate/state.json" || -f "$acfs_home_candidate/VERSION" || -f "$acfs_home_candidate/zsh/acfs.zshrc" ]] || return 1

  if [[ -n "$_CHEATSHEET_EXPLICIT_ACFS_HOME" ]] && [[ "$acfs_home_candidate" == "$_CHEATSHEET_EXPLICIT_ACFS_HOME" ]]; then
    :
  elif [[ -n "$_CHEATSHEET_DEFAULT_ACFS_HOME" ]] && [[ "$acfs_home_candidate" == "$_CHEATSHEET_DEFAULT_ACFS_HOME" ]]; then
    :
  elif [[ "${_CHEATSHEET_RESOLVED_ACFS_HOME_SOURCE:-}" == "explicit_target_home" ]]; then
    :
  else
    return 1
  fi

  inferred_home="${acfs_home_candidate%/.acfs}"
  inferred_home="$(cheatsheet_sanitize_abs_nonroot_path "$inferred_home" 2>/dev/null || true)"
  [[ -n "$inferred_home" ]] || return 1
  printf '%s\n' "$inferred_home"
}
cheatsheet_prepare_context() {
  local state_file=""
  local path_home=""
  local detected_user=""
  local state_target_user=""
  local resolved_target_home=""
  local explicit_target_home=""
  local target_home_source=""

  _CHEATSHEET_RESOLVED_TARGET_USER=""
  _CHEATSHEET_RESOLVED_TARGET_HOME=""
  _CHEATSHEET_ACFS_HOME="$(cheatsheet_resolve_acfs_home 2>/dev/null || true)"
  state_file="$(cheatsheet_resolve_state_file)"
  path_home="$(cheatsheet_infer_target_home_from_acfs_home 2>/dev/null || true)"
  explicit_target_home="$(cheatsheet_resolve_explicit_target_home 2>/dev/null || true)"
  if [[ -n "$path_home" ]]; then
    state_target_user="$(cheatsheet_read_state_string "$state_file" "target_user" 2>/dev/null || true)"
  fi
  if [[ -z "$state_target_user" ]]; then
    state_target_user="$(cheatsheet_read_state_string "$_CHEATSHEET_SYSTEM_STATE_FILE" "target_user" 2>/dev/null ||     cheatsheet_read_state_string "$state_file" "target_user" 2>/dev/null || true)"
  fi

  if [[ -n "$_CHEATSHEET_EXPLICIT_TARGET_HOME_RAW" ]] || [[ -n "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW" ]]; then
    if [[ -n "$explicit_target_home" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_HOME="$explicit_target_home"
      target_home_source="explicit_target_home"
    else
      echo "Error: explicit TARGET_HOME/TARGET_USER did not resolve to an installed home; refusing to fall back to current HOME" >&2
      return 1
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] && [[ -n "$path_home" ]]; then
    _CHEATSHEET_RESOLVED_TARGET_HOME="$path_home"
    target_home_source="path_home"
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]]; then
    resolved_target_home="$(cheatsheet_read_target_home_from_state "$_CHEATSHEET_SYSTEM_STATE_FILE" 2>/dev/null ||       cheatsheet_read_target_home_from_state "$state_file" 2>/dev/null || true)"
    if [[ -n "$resolved_target_home" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_HOME="$resolved_target_home"
      target_home_source="state_target_home"
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] && [[ -n "$state_target_user" ]]; then
    resolved_target_home="$(cheatsheet_existing_abs_home "$(cheatsheet_home_for_user "$state_target_user" 2>/dev/null || true)" 2>/dev/null || true)"
    if [[ -n "$resolved_target_home" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_HOME="$resolved_target_home"
      target_home_source="state_target_user"
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] && [[ -n "$path_home" ]]; then
    _CHEATSHEET_RESOLVED_TARGET_HOME="$path_home"
    target_home_source="path_home"
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] && [[ "$_CHEATSHEET_ACFS_HOME" == */.acfs ]]; then
    resolved_target_home="$(cheatsheet_existing_abs_home "${_CHEATSHEET_ACFS_HOME%/.acfs}" 2>/dev/null || true)"
    if [[ -n "$resolved_target_home" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_HOME="$resolved_target_home"
      target_home_source="acfs_home_path"
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]]; then
    resolved_target_home="$(cheatsheet_existing_abs_home "${_CHEATSHEET_CURRENT_HOME:-}" 2>/dev/null || true)"
    if [[ -n "$resolved_target_home" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_HOME="$resolved_target_home"
      target_home_source="current_home"
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_USER" ]] && [[ -n "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]]; then
    detected_user="$(cheatsheet_read_user_for_home "$_CHEATSHEET_RESOLVED_TARGET_HOME" 2>/dev/null || true)"
    if [[ -n "$detected_user" ]]; then
      _CHEATSHEET_RESOLVED_TARGET_USER="$detected_user"
    fi
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_USER" ]] && [[ "$target_home_source" != "explicit_target_home" ]] && [[ -n "$state_target_user" ]]; then
    _CHEATSHEET_RESOLVED_TARGET_USER="$state_target_user"
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_USER" ]] && cheatsheet_is_valid_username "$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW"; then
    _CHEATSHEET_RESOLVED_TARGET_USER="$_CHEATSHEET_EXPLICIT_TARGET_USER_RAW"
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_USER" ]] && [[ -n "${SUDO_USER:-}" ]] && [[ "${SUDO_USER}" != "root" ]]; then
    _CHEATSHEET_RESOLVED_TARGET_USER="$SUDO_USER"
  fi

  if [[ -z "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] || [[ "$_CHEATSHEET_RESOLVED_TARGET_HOME" == "$_CHEATSHEET_CURRENT_HOME" ]]; then
    cheatsheet_prepend_user_paths "$_CHEATSHEET_CURRENT_HOME"
  fi
  if [[ -n "$_CHEATSHEET_RESOLVED_TARGET_HOME" ]] && [[ "$_CHEATSHEET_RESOLVED_TARGET_HOME" != "$_CHEATSHEET_CURRENT_HOME" ]]; then
    cheatsheet_prepend_user_paths "$_CHEATSHEET_RESOLVED_TARGET_HOME"
  fi
}

json_escape() {
  local s="${1:-}"
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\r'/\\r}
  s=${s//$'\t'/\\t}
  printf '%s' "$s"
}

normalize_category() {
  local raw="${1:-}"
  raw="${raw%% (*}"
  raw="${raw#--- }"
  raw="${raw% ---}"
  raw="${raw//aliases/}"
  raw="${raw//alias/}"
  raw="${raw//  / }"
  raw="${raw#"${raw%%[![:space:]]*}"}"
  raw="${raw%"${raw##*[![:space:]]}"}"

  case "${raw,,}" in
    *agent*) echo "Agents" ;;
    *git*) echo "Git" ;;
    *docker*) echo "Docker" ;;
    *directory*) echo "Directories" ;;
    bun*) echo "Bun" ;;
    *ubuntu*|*debian*|*convenience*) echo "System" ;;
    *modern*cli*) echo "Modern CLI" ;;
    *) [[ -n "$raw" ]] && echo "$raw" || echo "Misc" ;;
  esac
}

infer_category() {
  local name="${1:-}"
  local cmd="${2:-}"
  case "$name" in
    cc|cod|agy|gmi|am) echo "Agents" ;;
    br|bl|bt) echo "Bun" ;;
    dev|proj|dots|p) echo "Directories" ;;
    g*) [[ "$cmd" == git* ]] && { echo "Git"; return 0; } ;;
    d*) [[ "$cmd" == docker* ]] && { echo "Docker"; return 0; } ;;
  esac
  echo "Misc"
}

cheatsheet_parse_zshrc() {
  local zshrc="${1:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"
  [[ -f "$zshrc" ]] || return 1

  local current_category="Misc"
  local line rest
  local overall_active=true
  local -a if_parent_active=()
  local -a if_branch_taken=()

  while IFS= read -r line || [[ -n "$line" ]]; do
    # Section markers
    if [[ "$line" =~ ^#[[:space:]]*---[[:space:]]*(.+)[[:space:]]*---[[:space:]]*$ ]]; then
      current_category="$(normalize_category "${BASH_REMATCH[1]}")"
      continue
    fi
    if [[ "$line" =~ ^#[[:space:]]*===[[:space:]]*(.+)[[:space:]]*===[[:space:]]*$ ]]; then
      current_category="$(normalize_category "${BASH_REMATCH[1]}")"
      continue
    fi

    # Track simple conditional blocks used in acfs.zshrc (command -v ...; then / elif / else / fi).
    # This keeps the cheatsheet aligned with what will actually be active on the current system.
    if [[ "$line" =~ ^[[:space:]]*if[[:space:]]+command[[:space:]]+-v[[:space:]]+([[:alnum:]_.+-]+) ]]; then
      local tool="${BASH_REMATCH[1]}"
      local cond=false
      command -v "$tool" &>/dev/null && cond=true

      if_parent_active+=("$overall_active")
      if_branch_taken+=("$cond")

      if [[ "$overall_active" == "true" && "$cond" == "true" ]]; then
        overall_active=true
      else
        overall_active=false
      fi
      continue
    fi

    if [[ "${#if_parent_active[@]}" -gt 0 && "$line" =~ ^[[:space:]]*elif[[:space:]]+command[[:space:]]+-v[[:space:]]+([[:alnum:]_.+-]+) ]]; then
      local tool="${BASH_REMATCH[1]}"
      local idx=$(( ${#if_parent_active[@]} - 1 ))
      local parent_active="${if_parent_active[idx]}"
      local already_taken="${if_branch_taken[idx]}"

      if [[ "$already_taken" == "true" ]]; then
        overall_active=false
        continue
      fi

      local cond=false
      command -v "$tool" &>/dev/null && cond=true
      if_branch_taken[idx]="$cond"

      if [[ "$parent_active" == "true" && "$cond" == "true" ]]; then
        overall_active=true
      else
        overall_active=false
      fi
      continue
    fi

    if [[ "${#if_parent_active[@]}" -gt 0 && "$line" =~ ^[[:space:]]*else([[:space:]]*#.*)?$ ]]; then
      local idx=$(( ${#if_parent_active[@]} - 1 ))
      local parent_active="${if_parent_active[idx]}"
      local already_taken="${if_branch_taken[idx]}"

      if [[ "$parent_active" == "true" && "$already_taken" != "true" ]]; then
        overall_active=true
      else
        overall_active=false
      fi
      if_branch_taken[idx]=true
      continue
    fi

    if [[ "${#if_parent_active[@]}" -gt 0 && "$line" =~ ^[[:space:]]*fi([[:space:]]*#.*)?$ ]]; then
      local idx=$(( ${#if_parent_active[@]} - 1 ))
      overall_active="${if_parent_active[idx]}"
      unset 'if_parent_active[idx]'
      unset 'if_branch_taken[idx]'
      continue
    fi

    local line_active="$overall_active"
    # Handle one-line conditionals: `command -v tool ... && alias name='cmd'`
    # shellcheck disable=SC2250  # Regex pattern stored in variable for portability
    local oneliner_pattern='^[[:space:]]*command[[:space:]]+-v[[:space:]]+([[:alnum:]_.+-]+)[^#]*&&[[:space:]]*alias[[:space:]]'
    if [[ "$line" =~ $oneliner_pattern ]]; then
      local tool="${BASH_REMATCH[1]}"
      if ! command -v "$tool" &>/dev/null; then
        line_active=false
      fi
    fi
    [[ "$line_active" == "true" ]] || continue

    # Pre-process line to protect escaped quotes so basic parsing works.
    # Zsh/Bash aliases often use '\'' to embed single quotes inside single-quoted strings.
    # We replace this sequence with a placeholder to avoid splitting on the inner quotes.
    local safe_line
    safe_line="${line//\'\\\'\'/__ACFS_SQ__}"
    # Protect \" inside double quotes
    safe_line="${safe_line//\\\"/__ACFS_DQ__}"

    rest="$safe_line"
    while [[ "$rest" == *"alias "* ]]; do
      # Move to the next alias segment.
      rest="${rest#*alias }"

      local name="${rest%%=*}"
      name="${name%%[[:space:]]*}"
      [[ -n "$name" ]] || break

      local value="${rest#*=}"
      [[ -n "$value" ]] || break

      local cmd="" remainder=""
      if [[ "$value" == \'* ]]; then
        value="${value#\'}"
        if [[ "$value" == *"'"* ]]; then
          cmd="${value%%\'*}"
          remainder="${value#*\'}"
        else
          cmd="$value"
          remainder=""
        fi
      elif [[ "$value" == \"* ]]; then
        value="${value#\"}"
        if [[ "$value" == *"\""* ]]; then
          cmd="${value%%\"*}"
          remainder="${value#*\"}"
        else
          cmd="$value"
          remainder=""
        fi
      else
        cmd="${value%%[[:space:]]*}"
        remainder="${value#"$cmd"}"
      fi

      # Restore placeholders
      cmd="${cmd//__ACFS_SQ__/\'}"
      cmd="${cmd//__ACFS_DQ__/\"}"

      local category="$current_category"
      [[ -z "$category" || "$category" == "Misc" ]] && category="$(infer_category "$name" "$cmd")"

      printf '%s%s%s%s%s%s%s\n' "$category" "$CHEATSHEET_DELIM" "$name" "$CHEATSHEET_DELIM" "$cmd" "$CHEATSHEET_DELIM" "alias"

      # Continue searching for more aliases in the same line.
      rest="$remainder"
    done
  done < "$zshrc"
}

cheatsheet_collect_entries() {
  local zshrc="${1:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"
  local -a entries=()
  local line

  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    entries+=("$line")
  done < <(cheatsheet_parse_zshrc "$zshrc" || true)

  # De-dupe by name keeping the last definition (matches shell alias overriding behavior).
  local -A seen=()
  local -a dedup_rev=()
  local i
  for ((i=${#entries[@]}-1; i>=0; i--)); do
    IFS="$CHEATSHEET_DELIM" read -r _cat name _cmd _kind <<<"${entries[$i]}"
    if [[ -z "$name" || -n "${seen[$name]:-}" ]]; then
      continue
    fi
    seen[$name]=1
    dedup_rev+=("${entries[$i]}")
  done

  for ((i=${#dedup_rev[@]}-1; i>=0; i--)); do
    echo "${dedup_rev[$i]}"
  done
}

cheatsheet_filter_entries() {
  local category_filter="${1:-}"
  local search_filter="${2:-}"
  local zshrc="${3:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"

  local line cat name cmd kind
  while IFS= read -r line; do
    IFS="$CHEATSHEET_DELIM" read -r cat name cmd kind <<<"$line"

    if [[ -n "$category_filter" ]]; then
      if [[ "${cat,,}" != "${category_filter,,}" ]]; then
        continue
      fi
    fi

    if [[ -n "$search_filter" ]]; then
      local hay="${cat} ${name} ${cmd}"
      if [[ "${hay,,}" != *"${search_filter,,}"* ]]; then
        continue
      fi
    fi

    echo "$line"
  done < <(cheatsheet_collect_entries "$zshrc")
}

cheatsheet_render_plain() {
  local category_filter="${1:-}"
  local search_filter="${2:-}"
  local zshrc="${3:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"

  echo "ACFS Cheatsheet v$_CHEATSHEET_VERSION"
  echo "Source: $zshrc"
  echo ""

  local current=""
  local cat name cmd kind line
  while IFS= read -r line; do
    IFS="$CHEATSHEET_DELIM" read -r cat name cmd kind <<<"$line"
    if [[ "$cat" != "$current" ]]; then
      current="$cat"
      echo "$current"
    fi
    printf '  %-8s %s\n' "$name" "$cmd"
  done < <(cheatsheet_filter_entries "$category_filter" "$search_filter" "$zshrc")
}

cheatsheet_render_gum() {
  local category_filter="${1:-}"
  local search_filter="${2:-}"
  local zshrc="${3:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"

  gum style --bold --foreground "#89b4fa" "ACFS Cheatsheet v$_CHEATSHEET_VERSION"
  gum style --foreground "#6c7086" "Source: $zshrc"
  echo ""

  local current=""
  local cat name cmd kind line
  while IFS= read -r line; do
    IFS="$CHEATSHEET_DELIM" read -r cat name cmd kind <<<"$line"
    if [[ "$cat" != "$current" ]]; then
      current="$cat"
      echo ""
      gum style --bold --foreground "#cba6f7" "$current"
    fi
    printf '  %-8s %s\n' "$name" "$cmd"
  done < <(cheatsheet_filter_entries "$category_filter" "$search_filter" "$zshrc")
}

cheatsheet_render_json() {
  local category_filter="${1:-}"
  local search_filter="${2:-}"
  local zshrc="${3:-$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc}"

  local json_output
  json_output=$(
    local first=true
    printf '{'
    printf '"version":"%s",' "$(json_escape "$_CHEATSHEET_VERSION")"
    printf '"source":"%s",' "$(json_escape "$zshrc")"
    printf '"entries":['

    local cat name cmd kind line
    while IFS= read -r line; do
      IFS="$CHEATSHEET_DELIM" read -r cat name cmd kind <<<"$line"
      if [[ "$first" == "true" ]]; then
        first=false
      else
        printf ','
      fi
      printf '{'
      printf '"category":"%s",' "$(json_escape "$cat")"
      printf '"name":"%s",' "$(json_escape "$name")"
      printf '"command":"%s",' "$(json_escape "$cmd")"
      printf '"kind":"%s"' "$(json_escape "$kind")"
      printf '}'
    done < <(cheatsheet_filter_entries "$category_filter" "$search_filter" "$zshrc")

    printf ']'
    printf '}'
  )

  # Use output formatting library if available
  if type -t acfs_format_output &>/dev/null; then
    local resolved_format
    resolved_format=$(acfs_resolve_format "$_CHEATSHEET_OUTPUT_FORMAT")
    acfs_format_output "$json_output" "$resolved_format" "$_CHEATSHEET_SHOW_STATS"
  else
    # Fallback: direct JSON output
    printf '%s\n' "$json_output"
  fi
}

main() {
  local zshrc=""
  local category_filter=""
  local search_filter=""
  local json_mode=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --help|-h)
        print_help
        return 0
        ;;
      --json)
        json_mode=true
        shift
        ;;
      --format|-f)
        if [[ -z "${2:-}" || "$2" == -* ]]; then
          echo "Error: --format requires a value (json or toon)" >&2
          return 1
        fi
        _CHEATSHEET_OUTPUT_FORMAT="$2"
        json_mode=true
        shift 2
        ;;
      --format=*)
        _CHEATSHEET_OUTPUT_FORMAT="${1#*=}"
        if [[ -z "$_CHEATSHEET_OUTPUT_FORMAT" ]]; then
          echo "Error: --format requires a value (json or toon)" >&2
          return 1
        fi
        json_mode=true
        shift
        ;;
      --toon|-t)
        _CHEATSHEET_OUTPUT_FORMAT="toon"
        json_mode=true
        shift
        ;;
      --stats)
        _CHEATSHEET_SHOW_STATS=true
        shift
        ;;
      --category)
        if [[ -z "${2:-}" ]]; then
          echo "Error: --category requires a value" >&2
          return 1
        fi
        category_filter="$2"
        shift 2
        ;;
      --search)
        if [[ -z "${2:-}" ]]; then
          echo "Error: --search requires a value" >&2
          return 1
        fi
        search_filter="$2"
        shift 2
        ;;
      --zshrc)
        if [[ -z "${2:-}" ]]; then
          echo "Error: --zshrc requires a path" >&2
          return 1
        fi
        zshrc="$2"
        shift 2
        ;;
      *)
        # Treat positional arg as either category match or a search term.
        local q="$1"
        shift
        case "${q,,}" in
          agents|git|docker|directories|system|bun|modern\ cli)
            category_filter="$q"
            ;;
          *)
            search_filter="$q"
            ;;
        esac
        ;;
    esac
  done

  cheatsheet_prepare_context

  if [[ -z "$zshrc" ]]; then
    zshrc="$_CHEATSHEET_ACFS_HOME/zsh/acfs.zshrc"
  fi

  if [[ ! -f "$zshrc" ]]; then
    echo "Error: zshrc not found: $zshrc" >&2
    echo "Hint: re-run the ACFS installer, or pass --zshrc <path> / set ACFS_HOME." >&2
    return 1
  fi

  if [[ "$json_mode" == "true" ]]; then
    cheatsheet_render_json "$category_filter" "$search_filter" "$zshrc"
  elif [[ "$_CHEATSHEET_HAS_GUM" == "true" ]]; then
    cheatsheet_render_gum "$category_filter" "$search_filter" "$zshrc"
  else
    cheatsheet_render_plain "$category_filter" "$search_filter" "$zshrc"
  fi
}

cheatsheet_restore_shell_options_if_sourced() {
  [[ "$_CHEATSHEET_WAS_SOURCED" == "true" ]] || return 0

  if [[ "$_CHEATSHEET_ORIGINAL_HOME_WAS_SET" == "true" ]]; then
    HOME="$_CHEATSHEET_ORIGINAL_HOME"
    export HOME
  else
    unset HOME
  fi

  [[ "$_CHEATSHEET_RESTORE_ERREXIT" == "true" ]] || set +e
  [[ "$_CHEATSHEET_RESTORE_NOUNSET" == "true" ]] || set +u
  [[ "$_CHEATSHEET_RESTORE_PIPEFAIL" == "true" ]] || set +o pipefail
}

cheatsheet_restore_shell_options_if_sourced

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi

#!/bin/bash
# Install ACFS notification workflow in a repository
#
# Usage:
#   ./install-acfs-workflow.sh [TOOL_NAME] [INSTALLER_PATH]
#
# Examples:
#   ./install-acfs-workflow.sh                    # Uses directory name and install.sh
#   ./install-acfs-workflow.sh beads_rust         # Custom tool name
#   ./install-acfs-workflow.sh my_tool scripts/install.sh  # Custom path
#
# This script:
# 1. Downloads the latest notify-acfs workflow template from ACFS
# 2. Customizes it with your tool name and installer path
# 3. Saves it to .github/workflows/notify-acfs.yml
#
# After running, you still need to:
# 1. Create the ACFS_REPO_DISPATCH_TOKEN secret in your repository settings
# 2. Verify the tool name matches your entry in ACFS checksums.yaml
# 3. Commit and push the workflow file

set -euo pipefail

# Do not allow Bash's optional '& means matched text' replacement mode to
# corrupt YAML values that legitimately contain ampersands.
shopt -u patsub_replacement 2>/dev/null || true

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

workflow_system_binary_path() {
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
        if [[ -x "$candidate" ]]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done

    return 1
}

workflow_require_binary() {
    local name="${1:-}"
    local binary=""

    binary="$(workflow_system_binary_path "$name" 2>/dev/null || true)"
    if [[ -z "$binary" ]]; then
        echo -e "${RED}Error: Required system command '$name' was not found${NC}" >&2
        exit 1
    fi

    printf '%s\n' "$binary"
}

workflow_validate_single_line_value() {
    local label="$1"
    local value="$2"

    if [[ -z "$value" ]]; then
        echo -e "${RED}Error: $label must not be empty${NC}" >&2
        exit 1
    fi

    case "$value" in
        *$'\n'*|*$'\r'*)
            echo -e "${RED}Error: $label must be a single-line value${NC}" >&2
            exit 1
            ;;
    esac
}

workflow_yaml_single_quote_escape() {
    local value="$1"
    printf '%s\n' "${value//\'/\'\'}"
}

workflow_render_template() {
    local template_path="$1"
    local output_path="$2"
    local template=""
    local tool_name_yaml=""
    local installer_path_yaml=""
    local installer_placeholder="INSTALLER_PATH: 'install.sh'"
    local installer_replacement=""

    template="$(<"$template_path")"
    tool_name_yaml="$(workflow_yaml_single_quote_escape "$TOOL_NAME")"
    installer_path_yaml="$(workflow_yaml_single_quote_escape "$INSTALLER_PATH")"
    installer_replacement="INSTALLER_PATH: '$installer_path_yaml'"

    template="${template//\{\{ TOOL_NAME_PLACEHOLDER \}\}/$tool_name_yaml}"
    template="${template//$installer_placeholder/$installer_replacement}"

    printf '%s\n' "$template" > "$output_path"
}

workflow_download_and_render() {
    local url="$1"
    local destination="$2"
    local download_tmp=""
    local render_tmp=""

    download_tmp="$("$MKTEMP_BIN" ".github/workflows/.acfs-template.XXXXXX")"
    render_tmp="$("$MKTEMP_BIN" ".github/workflows/.acfs-rendered.XXXXXX")"

    if ! "$CURL_BIN" -fsSL "$url" > "$download_tmp"; then
        "$RM_BIN" -f -- "$download_tmp" "$render_tmp"
        return 1
    fi

    if ! workflow_render_template "$download_tmp" "$render_tmp"; then
        "$RM_BIN" -f -- "$download_tmp" "$render_tmp"
        return 1
    fi

    if ! "$MV_BIN" -f -- "$render_tmp" "$destination"; then
        "$RM_BIN" -f -- "$download_tmp" "$render_tmp"
        return 1
    fi

    "$RM_BIN" -f -- "$download_tmp"
}

# Default values
PWD_BASENAME="${PWD%/}"
PWD_BASENAME="${PWD_BASENAME##*/}"
TOOL_NAME="${1:-$PWD_BASENAME}"
INSTALLER_PATH="${2:-install.sh}"
ACFS_TEMPLATE_URL="${ACFS_TEMPLATE_URL:-https://raw.githubusercontent.com/quangdang46/agents_environment_setup/main/acfs/templates/workflows/notify-acfs-workflow.yml}"
VALIDATE_TEMPLATE_URL="${VALIDATE_TEMPLATE_URL:-https://raw.githubusercontent.com/quangdang46/agents_environment_setup/main/acfs/templates/workflows/validate-acfs-workflow.yml}"

workflow_validate_single_line_value "TOOL_NAME" "$TOOL_NAME"
workflow_validate_single_line_value "INSTALLER_PATH" "$INSTALLER_PATH"

MKDIR_BIN="$(workflow_require_binary mkdir)"
MKTEMP_BIN="$(workflow_require_binary mktemp)"
MV_BIN="$(workflow_require_binary mv)"
RM_BIN="$(workflow_require_binary rm)"
CURL_BIN="$(workflow_require_binary curl)"
SHA256SUM_BIN="$(workflow_require_binary sha256sum)"

echo "Installing ACFS notification workflow..."
echo ""
echo "  Tool name:      $TOOL_NAME"
echo "  Installer path: $INSTALLER_PATH"
echo ""

# Check if installer exists
if [[ ! -f "$INSTALLER_PATH" ]]; then
    echo -e "${YELLOW}Warning: Installer not found at '$INSTALLER_PATH'${NC}"
    echo "Make sure the path is correct before pushing the workflow."
    echo ""
fi

# Check if we're in a git repo
if [[ ! -d ".git" ]]; then
    echo -e "${RED}Error: Not in a git repository${NC}"
    echo "Run this script from the root of your repository."
    exit 1
fi

# Create workflows directory if needed
"$MKDIR_BIN" -p .github/workflows

# Check if workflow already exists
if [[ -f ".github/workflows/notify-acfs.yml" ]]; then
    echo -e "${YELLOW}Warning: .github/workflows/notify-acfs.yml already exists${NC}"
    read -p "Overwrite? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted."
        exit 0
    fi
fi

# Download and customize template
echo "Downloading template from ACFS..."
if ! workflow_download_and_render "$ACFS_TEMPLATE_URL" ".github/workflows/notify-acfs.yml"; then
    echo -e "${RED}Error: Failed to download template${NC}"
    exit 1
fi

echo -e "${GREEN}Workflow installed at .github/workflows/notify-acfs.yml${NC}"
echo ""

# Compute current checksum
if [[ -f "$INSTALLER_PATH" ]]; then
    read -r CURRENT_SHA256 _ < <("$SHA256SUM_BIN" "$INSTALLER_PATH")
    echo "Current installer SHA256: $CURRENT_SHA256"
    echo ""
fi

echo "Next steps:"
echo ""
echo "  1. Create ACFS_REPO_DISPATCH_TOKEN secret:"
echo "     - Go to your repository Settings > Secrets > Actions"
echo "     - Create a new secret named 'ACFS_REPO_DISPATCH_TOKEN'"
echo "     - Use a GitHub PAT with 'repo' scope for:"
echo "       quangdang46/agents_environment_setup"
echo ""
echo "  2. Verify your tool is in ACFS checksums.yaml:"
echo "     - Check: https://github.com/quangdang46/agents_environment_setup/blob/main/checksums.yaml"
echo "     - Look for entry: $TOOL_NAME"
echo ""
echo "  3. Commit and push the workflow:"
echo "     git add .github/workflows/notify-acfs.yml"
echo "     git commit -m 'chore: add ACFS installer notification workflow'"
echo "     git push"
echo ""
echo "  4. Test the workflow:"
echo "     gh workflow run notify-acfs.yml -f dry_run=true"
echo ""

# Also install validation workflow
echo "Installing validation workflow..."
if workflow_download_and_render "$VALIDATE_TEMPLATE_URL" ".github/workflows/validate-acfs.yml" 2>/dev/null; then
    echo -e "${GREEN}Validation workflow installed at .github/workflows/validate-acfs.yml${NC}"
    echo ""
    echo "To validate your setup:"
    echo "  gh workflow run validate-acfs.yml"
else
    echo -e "${YELLOW}Warning: Validation workflow template could not be downloaded${NC}" >&2
fi

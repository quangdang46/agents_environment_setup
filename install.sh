#!/bin/sh
#
# AES tier-0 bootstrap.
#
#   curl -fsSL <url> | sh
#
# This script does exactly five things and nothing else:
#
#   1. detect OS and architecture
#   2. download the binary from GitHub Releases
#   3. verify its sha256
#   4. place it at $AES_PREFIX/bin/aes
#   5. run `aes --version`
#
# It installs no tools. That is `aes setup`'s job, and growing a second
# responsibility here is how a bootstrapper becomes untrustworthy.
#
# Everything informational goes to stderr so that piping this script's stdout
# into something else never captures log noise.

set -eu

REPO="${AES_REPO:-quangdang46/agents_environment_setup}"
VERSION="${AES_VERSION:-latest}"
PREFIX="${AES_PREFIX:-$HOME/.aes}"
BASE_URL="${AES_BASE_URL:-}"

# ---------------------------------------------------------------- utilities

log()  { printf '%s\n' "$*" >&2; }
step() { printf '==> %s\n' "$*" >&2; }
die()  { printf 'aes: error: %s\n' "$*" >&2; exit 1; }

usage() {
    cat >&2 <<EOF
Usage: install.sh [options]

  --version <v>   release to install (default: latest, or \$AES_VERSION)
  --prefix <dir>  install root (default: \$HOME/.aes, or \$AES_PREFIX)
  --base-url <u>  download base URL, for testing against a fixture server
  -h, --help      show this help

Environment: AES_REPO, AES_VERSION, AES_PREFIX, AES_BASE_URL
EOF
}

# sha256_of FILE -> lowercase hex digest on stdout.
# Linux ships sha256sum; macOS ships shasum. Either is fine, and the checksum
# is mandatory, so having neither is a hard error rather than a warning.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        die "no sha256sum or shasum found; cannot verify the download"
    fi
}

# ------------------------------------------------------------ 1. detection

# The uname indirection exists so the arch trap below can be tested. Running
# the real detection logic against a real machine proves nothing about a
# machine that is not this one — and an unsupported arch must be tested on a
# machine that is otherwise perfectly supported.
uname_s() { printf '%s' "${AES_UNAME_S:-$(uname -s)}"; }
uname_m() { printf '%s' "${AES_UNAME_M:-$(uname -m)}"; }

detect_os() {
    _os=$(uname_s)
    case "$_os" in
        Darwin) printf 'darwin' ;;
        Linux)  printf 'linux' ;;
        *)      die "unsupported OS '$_os' (aes supports darwin and linux)" ;;
    esac
}

# uname speaks a different architecture vocabulary than Go and than the
# release assets. Translate exactly once, here, and refuse anything unknown:
# a silent fallback would download the wrong binary and fail much later with
# a far less obvious error.
detect_arch() {
    _m=$(uname_m)
    case "$_m" in
        x86_64|amd64)  printf 'amd64' ;;
        arm64|aarch64) printf 'arm64' ;;
        *)             die "unsupported architecture '$_m' (aes supports amd64 and arm64)" ;;
    esac
}

# ------------------------------------------------------------- 2. download

# asset_url ASSET
asset_url() {
    if [ -n "$BASE_URL" ]; then
        printf '%s/%s' "${BASE_URL%/}" "$1"
    elif [ "$VERSION" = "latest" ]; then
        printf 'https://github.com/%s/releases/latest/download/%s' "$REPO" "$1"
    else
        printf 'https://github.com/%s/releases/download/%s/%s' "$REPO" "$VERSION" "$1"
    fi
}

download() {
    _url=$1
    _dest=$2
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$_url" -o "$_dest"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$_dest" "$_url"
    else
        die "neither curl nor wget found; cannot download"
    fi
}

# ------------------------------------------------------------- 3. verify

# The checksum is not optional and it is checked before anything is
# extracted. A mismatch aborts with the binary never written to $PREFIX.
verify_checksum() {
    _file=$1
    _expected=$2

    _actual=$(sha256_of "$_file")
    if [ "$_actual" != "$_expected" ]; then
        die "checksum mismatch for $(basename "$_file")
  expected: $_expected
  actual:   $_actual
The binary was NOT installed."
    fi
    log "    checksum ok ($_actual)"
}

# A tar entry naming an absolute path or containing a '..' component can write
# outside the destination. That is code execution, not a cosmetic warning, so
# the archive is inspected before a single byte is extracted.
assert_no_path_traversal() {
    _tarball=$1
    if tar -tzf "$_tarball" | grep -qE '(^/|(^|/)\.\.(/|$)|^-)'; then
        die "archive contains an unsafe path entry; refusing to extract"
    fi
}

# -------------------------------------------------------- 4/5. place + run

main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --version) VERSION=$2; shift 2 ;;
            --prefix)  PREFIX=$2;  shift 2 ;;
            --base-url) BASE_URL=$2; shift 2 ;;
            -h|--help) usage; exit 0 ;;
            *) die "unknown option '$1' (try --help)" ;;
        esac
    done

    step "detecting platform"
    OS=$(detect_os)
    ARCH=$(detect_arch)
    log "    $OS/$ARCH"

    ASSET="aes-$VERSION-$OS-$ARCH.tar.gz"
    BIN_DIR="$PREFIX/bin"
    DEST="$BIN_DIR/aes"

    TMP=$(mktemp -d "${TMPDIR:-/tmp}/aes-install.XXXXXX")
    # The temp directory is removed on every exit path, so a failed
    # verification leaves nothing behind for the next run to trip over.
    trap 'rm -rf "$TMP"' EXIT INT TERM

    step "downloading $ASSET"
    download "$(asset_url "$ASSET")" "$TMP/aes.tar.gz" \
        || die "download failed: $ASSET (is version '$VERSION' published?)"

    step "fetching checksums.txt"
    download "$(asset_url "checksums.txt")" "$TMP/checksums.txt" \
        || die "download failed: checksums.txt"

    step "verifying checksum"
    # Match on the asset filename, not on line order: checksums.txt covers
    # every asset in the release and only one of them is ours.
    EXPECTED=$(awk -v want="$ASSET" '$2 == want || $2 == "*"want { print $1; exit }' "$TMP/checksums.txt")
    [ -n "$EXPECTED" ] || die "no checksum listed for $ASSET in checksums.txt"
    verify_checksum "$TMP/aes.tar.gz" "$EXPECTED"

    step "extracting"
    assert_no_path_traversal "$TMP/aes.tar.gz"
    mkdir -p "$TMP/extract"
    tar -xzf "$TMP/aes.tar.gz" -C "$TMP/extract"
    [ -f "$TMP/extract/aes" ] || die "archive did not contain an 'aes' binary"

    step "installing to $DEST"
    mkdir -p "$BIN_DIR"
    chmod +x "$TMP/extract/aes"
    mv "$TMP/extract/aes" "$DEST"
    chmod +x "$DEST"

    step "verifying installation"
    "$DEST" --version >&2
    log ""
    log "aes installed at $DEST"
    log "add it to your PATH:"
    log "    export PATH=\"$BIN_DIR:\$PATH\""
    log ""
    log "then run:  aes setup"
}

main "$@"

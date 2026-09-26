#!/bin/sh
#
# Shell-level tests for the tier-0 bootstrap.
#
# These run install.sh end to end against a fixture served over file://, so
# nothing here touches the network and nothing needs root. The real Go binary
# is built and installed, not a stub: the point is to prove the whole path
# works, and a fake would only prove the fake works.

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INSTALL_SH="$ROOT/install.sh"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/aes-boot-test.XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM

PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  FAIL %s\n' "$1"; [ $# -gt 1 ] && printf '       %s\n' "$2"; }
skip() { printf '  skip %s (%s)\n' "$1" "$2"; }

# detect mirrors install.sh's own mapping so the fixture is named the way the
# script will look for it.
case "$(uname -s)" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *)      printf 'unsupported test host: %s\n' "$(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *)            printf 'unsupported test arch: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

ASSET="aes-test-$OS-$ARCH.tar.gz"
SERVE="$WORK/serve"
BIN_DIR="$WORK/build"

mkdir -p "$SERVE" "$BIN_DIR"

# --------------------------------------------------------------- fixture

command -v go >/dev/null 2>&1 || { echo "go is required to build the fixture" >&2; exit 1; }

echo "building fixture ($OS/$ARCH)"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN_DIR/aes" ./cmd/aes)
tar -czf "$SERVE/$ASSET" -C "$BIN_DIR" aes

digest() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# A correct checksums.txt covering the real asset.
(cd "$SERVE" && printf '%s  %s\n' "$(digest "$SERVE/$ASSET")" "$ASSET" > checksums.txt)

run_install() {
    # $1 = prefix dir. Assets come from the file:// fixture, never GitHub.
    sh "$INSTALL_SH" --base-url "file://$SERVE" --version test --prefix "$1" 2>&1
}

# ----------------------------------------------------------------- tests

echo
echo "1. matching checksum -> binary placed and runs"
PREFIX="$WORK/ok"
OUT=$(run_install "$PREFIX" || true)
if [ -x "$PREFIX/bin/aes" ] && "$PREFIX/bin/aes" --version >/dev/null 2>&1; then
    ok "binary placed at $PREFIX/bin/aes and executes"
else
    bad "binary placed and runs" "$OUT"
fi

echo
echo "2. checksum mismatch -> aborts, binary NOT placed"
BAD="$WORK/badserver"
cp -r "$SERVE" "$BAD"
# Corrupt the recorded digest, not the asset: the download succeeds and the
# checksum gate is what must catch it.
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$ASSET" > "$BAD/checksums.txt"
PREFIX="$WORK/mismatch"
OUT=$(sh "$INSTALL_SH" --base-url "file://$BAD" --version test --prefix "$PREFIX" 2>&1 || true)
# The absence of the file is the contract, not the error text.
if [ ! -e "$PREFIX/bin/aes" ]; then
    ok "no binary placed on checksum mismatch"
else
    bad "no binary placed on checksum mismatch" "binary exists at $PREFIX/bin/aes"
fi
case "$OUT" in
    *"checksum mismatch"*) ok "error names the checksum mismatch" ;;
    *)                     bad "error names the checksum mismatch" "$OUT" ;;
esac

echo
echo "3. unknown architecture -> clear error, no download attempted"
EMPTY="$WORK/empty"
mkdir -p "$EMPTY"
PREFIX="$WORK/badarch"
# An empty server means any attempt to download would produce a different
# error. Asserting the arch error therefore proves nothing was fetched.
OUT=$(AES_UNAME_M=ppc64 sh "$INSTALL_SH" --base-url "file://$EMPTY" --version test --prefix "$PREFIX" 2>&1 || true)
case "$OUT" in
    *"unsupported architecture"*) ok "error names the unsupported architecture" ;;
    *)                            bad "error names the unsupported architecture" "$OUT" ;;
esac
case "$OUT" in
    *"checksums.txt"*) bad "no download was attempted" "script got as far as downloading" ;;
    *)                 ok "no download was attempted" ;;
esac

echo
echo "4. unsupported OS -> clear error"
PREFIX="$WORK/bados"
OUT=$(AES_UNAME_S=Plan9 sh "$INSTALL_SH" --base-url "file://$EMPTY" --version test --prefix "$PREFIX" 2>&1 || true)
case "$OUT" in
    *"unsupported OS"*) ok "error names the unsupported OS" ;;
    *)                  bad "error names the unsupported OS" "$OUT" ;;
esac

echo
echo "5. path traversal in the archive -> rejected, nothing extracted"
if command -v python3 >/dev/null 2>&1; then
    EVIL="$WORK/evilserver"
    mkdir -p "$EVIL"
    python3 - "$SERVE/$ASSET" "$EVIL/$ASSET" <<'PY'
import sys, tarfile, io, os
src, dst = sys.argv[1], sys.argv[2]
# A correct tarball plus one '../evil' entry. The checksum stays valid on
# purpose: the archive-inspection gate, not the digest, is what must catch
# this one.
with tarfile.open(src) as tin, tarfile.open(dst, "w:gz") as tout:
    for m in tin.getmembers():
        data = tin.extractfile(m).read() if m.isfile() else b""
        tout.addfile(m, io.BytesIO(data))
    payload = b"pwned\n"
    info = tarfile.TarInfo("../evil")
    info.size = len(payload)
    tout.addfile(info, io.BytesIO(payload))
PY
    (cd "$EVIL" && printf '%s  %s\n' "$(digest "$EVIL/$ASSET")" "$ASSET" > checksums.txt)
    PREFIX="$WORK/evil"
    OUT=$(sh "$INSTALL_SH" --base-url "file://$EVIL" --version test --prefix "$PREFIX" 2>&1 || true)
    case "$OUT" in
        *"unsafe path"*) ok "archive with a ../ entry is rejected" ;;
        *)               bad "archive with a ../ entry is rejected" "$OUT" ;;
    esac
    if [ ! -e "$PREFIX/bin/aes" ] && [ ! -e "$WORK/evil" ]; then
        ok "nothing was extracted"
    else
        bad "nothing was extracted" "binary or ../evil present"
    fi
else
    skip "archive with a ../ entry is rejected" "python3 not available"
fi

echo
echo "6. temp directory is left clean"
if [ -z "$(find "${TMPDIR:-/tmp}" -maxdepth 1 -name 'aes-install.*' 2>/dev/null)" ]; then
    ok "no aes-install.* temp directories remain"
else
    bad "no aes-install.* temp directories remain" "found leftovers in ${TMPDIR:-/tmp}"
fi

# --------------------------------------------------------------- summary

echo
echo "passed: $PASS   failed: $FAIL"
[ "$FAIL" -eq 0 ] || exit 1

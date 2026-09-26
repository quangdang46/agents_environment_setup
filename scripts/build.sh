#!/bin/sh
#
# Cross-compile the AES release matrix and produce the artifacts install.sh
# expects. The asset naming is fixed by the release contract:
#
#   aes-<version>-<goos>-<goarch>.tar.gz
#   checksums.txt                    (sha256sum format, covers every asset)
#
# install.sh resolves both without reading a manifest, so these names are an
# interface, not a convention. Renaming one breaks the bootstrap.

set -eu

VERSION="${AES_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST="${AES_DIST:-dist}"
LDFLAGS="-s -w -X main.version=$VERSION"

# The bootstrap promise is that the binary is self-contained: no Go, brew, or
# apt on the target machine. CGO off is what makes that true for every target
# rather than only the host's.
export CGO_ENABLED=0

TARGETS="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"

log() { printf '==> %s\n' "$*" >&2; }

log "building $VERSION for: $TARGETS"

rm -rf "$DIST"
mkdir -p "$DIST"

ASSETS=""
for target in $TARGETS; do
    GOOS=${target%/*}
    GOARCH=${target#*/}
    NAME="aes-$VERSION-$GOOS-$GOARCH.tar.gz"

    log "  $GOOS/$GOARCH"
    GOOS=$GOOS GOARCH=$GOARCH go build \
        -trimpath \
        -ldflags "$LDFLAGS" \
        -o "$DIST/aes" \
        ./cmd/aes

    # The archive holds a single 'aes' at its root: install.sh stages the
    # extraction and moves that one file into place, so anything else in
    # here would be a surprise.
    tar -czf "$DIST/$NAME" -C "$DIST" aes
    rm -f "$DIST/aes"

    ASSETS="$ASSETS $NAME"
done

# Written in sha256sum format, one '<digest>  <name>' line per asset, so
# both `sha256sum -c` and the awk lookup in install.sh accept it.
log "generating checksums.txt"
(cd "$DIST" && sha256sum $ASSETS > checksums.txt) 2>/dev/null \
    || (cd "$DIST" && shasum -a 256 $ASSETS > checksums.txt)

log "artifacts in $DIST:"
(cd "$DIST" && ls -1 $ASSETS checksums.txt) >&2

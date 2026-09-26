// Package aessetup embeds the tool catalog and the profile files into the
// aes binary.
//
// # Why this exists
//
// install.sh places exactly one file on the user's machine: the binary. A
// release tarball contains no `tools/` and no `profiles/`. So a binary that
// discovers its data by walking up from the working directory finds nothing
// unless the user happens to be standing in a git checkout — which makes
// `curl … | sh` followed by `aes setup` fail, i.e. the one path the whole
// product is built around.
//
// Embedding is also the honest answer to "where does the data come from". A
// catalog that lives outside the binary can be edited between the release
// that shipped it and the machine that runs it; a catalog that is *inside* it
// is the one that was reviewed and checksummed. That is the same reasoning
// that makes github-release verify sha256 before extracting, applied to our
// own data instead of an upstream's.
//
// It lives at the module root because go:embed cannot reach outside its own
// directory: patterns may not start with ".." and symlinks are not followed,
// so a package under internal/ could not see the root-level tools/ and
// profiles/ trees at all.
//
// This package is a leaf. It imports nothing but embed.
package aessetup

import (
	"embed"
	"io/fs"
)

//go:embed tools profiles
var assets embed.FS

// Tools returns the embedded tool catalog, rooted so that the tree looks like
// the repository's tools/ directory (i.e. entries are <category>/<name>/tool.yaml).
func Tools() (fs.FS, error) { return sub("tools") }

// Profiles returns the embedded profile files, rooted so entries are
// <name>.yaml.
func Profiles() (fs.FS, error) { return sub("profiles") }

func sub(dir string) (fs.FS, error) {
	return fs.Sub(assets, dir)
}

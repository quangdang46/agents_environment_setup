package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// ErrLinkTargetMissing is returned when a declared link names a file that is
// not on disk. Linking to it would put a broken binary on PATH, which reads as
// "aes installed something that does not work" rather than "the install step
// did not produce the file the manifest expected".
var ErrLinkTargetMissing = errors.New("link target does not exist")

// EnsureLinks creates the symlinks a target declares, in dest.
//
// It exists because a strategy can succeed and leave nothing on PATH. The
// measured case is the macOS `hex` cask: it installs /Applications/Hex.app and
// creates no bin entry, so the verifier reported the tool missing immediately
// after a successful install. The binary inside the bundle cannot be copied
// out (the same bytes are SIGKILLed outside their bundle — exit 137, measured
// 2026-11-04), but a symlink to the in-bundle path runs correctly. So a link,
// not a copy.
//
// The declared target is checked before anything is written: a link to a
// missing file is a broken command on PATH, and reporting it here names the
// real problem (the install produced no such file) instead of surfacing later
// as a mysterious "missing" from the verifier.
//
// It is idempotent, and it refuses to overwrite a regular file. AES owns
// dest, but "owns the directory" is not "may replace anything in it": a file
// someone else put there is theirs, and silently deleting it is the class of
// loss this project's config handling exists to avoid.
func EnsureLinks(dest string, links map[string]string) ([]string, error) {
	if len(links) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dest, err)
	}

	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)

	created := make([]string, 0, len(names))
	for _, name := range names {
		target := links[name]
		link := filepath.Join(dest, name)

		if _, err := os.Stat(target); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return created, fmt.Errorf("%w: %s -> %s", ErrLinkTargetMissing, name, target)
			}
			return created, fmt.Errorf("stat %s: %w", target, err)
		}

		info, err := os.Lstat(link)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			// Replace only a link, and only when it points somewhere else.
			// Re-creating an identical link every run would still be correct,
			// but a no-op keeps mtime stable and makes "did anything change"
			// answerable from the filesystem.
			if old, err := os.Readlink(link); err == nil && old == target {
				continue
			}
			if err := os.Remove(link); err != nil {
				return created, fmt.Errorf("replace link %s: %w", link, err)
			}
		case err == nil:
			return created, fmt.Errorf("%s exists and is not a symlink; leaving it untouched", link)
		case !errors.Is(err, fs.ErrNotExist):
			return created, fmt.Errorf("lstat %s: %w", link, err)
		}

		if err := os.Symlink(target, link); err != nil {
			return created, fmt.Errorf("link %s -> %s: %w", link, target, err)
		}
		created = append(created, link)
	}
	return created, nil
}

// RemoveLinks deletes the symlinks a target declares, and only if they are
// still links to the declared targets.
//
// The condition is the whole safety property: uninstall must not delete a file
// someone else placed under the same name. A link that now points elsewhere was
// changed by the user, and removing it would undo their work while reporting a
// clean removal.
func RemoveLinks(dest string, links map[string]string) []error {
	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		link := filepath.Join(dest, name)
		info, err := os.Lstat(link)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Errorf("lstat %s: %w", link, err))
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			// Not ours to remove.
			continue
		}
		if old, err := os.Readlink(link); err != nil || old != links[name] {
			continue
		}
		if err := os.Remove(link); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", link, err))
		}
	}
	return errs
}

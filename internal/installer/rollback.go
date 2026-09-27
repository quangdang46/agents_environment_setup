package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// prevSuffix is the ACFS convention: the predecessor lives beside the binary it
// replaced, at `<binary>.prev`. Beside rather than in a state directory,
// because the restore has to work when everything else is wrong — including
// the state file that would otherwise be the natural place to look.
const prevSuffix = ".prev"

// Retain copies an existing binary to <binary>.prev before something replaces
// it in place.
//
// It is a no-op, returning nil, when there is no binary to retain. That is the
// first install, and there is nothing to roll back to — the caller decides
// what to do with a restore that has no predecessor, and pretending otherwise
// here would turn "no predecessor" into a confusing error much later.
//
// The copy is file mode + content rather than a link: the ecosystem installer
// writes through the same path, and a hard link would be modified in place
// along with it, leaving the "backup" holding the very bytes that broke.
func Retain(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("retain %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("retain %s: %w", path, err)
	}
	if err := writeFileAtomic(PrevPath(path), data, info.Mode().Perm()); err != nil {
		return fmt.Errorf("retain %s: %w", path, err)
	}
	return nil
}

// Restore puts the retained predecessor back over path.
//
// The restore is a rename from a temp file, the same discipline every other
// write in this package uses. A partially-copied binary is worse than none: it
// would be on PATH and would fail, which is the state this whole bead exists
// to leave the machine out of.
//
// A missing predecessor is not an error. Restore is called when a probe came
// back broken, and "there was nothing to restore" means the install simply did
// not work — which the caller already knows and is about to report.
func Restore(path string) (bool, error) {
	prev := PrevPath(path)
	data, err := os.ReadFile(prev)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("restore %s: %w", path, err)
	}
	info, err := os.Stat(prev)
	if err != nil {
		return false, fmt.Errorf("restore %s: %w", path, err)
	}
	if err := writeFileAtomic(path, data, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("restore %s: %w", path, err)
	}
	// The predecessor has been consumed. Leaving it would mean a second
	// restore puts the same old binary back over a newer one, and a stale
	// `.prev` is indistinguishable from a fresh one.
	if err := os.Remove(prev); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("restore %s: remove %s: %w", path, prev, err)
	}
	return true, nil
}

// Discard drops a retained predecessor without restoring it.
//
// It is the other half of the decision: a probe that came back OK, or merely
// unknown, must not leave a `.prev` lying around. The next install would
// retain over it, but a tool that is never reinstalled would carry a stale
// copy of itself indefinitely, and someone reading the directory would have no
// way to tell which of the two files is the truth.
func Discard(path string) error {
	if err := os.Remove(PrevPath(path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("discard %s: %w", path, err)
	}
	return nil
}

// PrevPath is where the predecessor of path is retained.
func PrevPath(path string) string { return path + prevSuffix }

// writeFileAtomic writes through a temp file in the destination directory and
// renames it into place.
//
// The temp file is in the same directory rather than os.TempDir because rename
// is only atomic within a filesystem, and a cross-device rename is a copy —
// which is the non-atomic write this is here to avoid.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".aes-restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// CreateTemp makes 0600. The mode is the retained file's, because a
	// restored binary that lost its execute bit is a different failure from the
	// one being repaired.
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

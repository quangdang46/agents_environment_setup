package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeTarget creates a file standing in for an app bundle's binary.
func writeTarget(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestEnsureLinksCreatesDeclaredSymlinks(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "bin")
	target := writeTarget(t, filepath.Join(tmp, "App.app", "Contents", "MacOS"), "apptool")

	created, err := EnsureLinks(dest, map[string]string{"apptool": target})
	if err != nil {
		t.Fatalf("EnsureLinks: %v", err)
	}
	if len(created) != 1 || created[0] != filepath.Join(dest, "apptool") {
		t.Fatalf("created = %v, want one link at %s", created, filepath.Join(dest, "apptool"))
	}
	got, err := os.Readlink(filepath.Join(dest, "apptool"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("link -> %q, want %q", got, target)
	}
	// A symlink, never a copy: the measured hex binary cannot be relocated.
	info, err := os.Lstat(filepath.Join(dest, "apptool"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link is not a symlink")
	}
}

// A second run must not churn the link. mtime stability is what makes "did
// anything change" answerable from the filesystem.
func TestEnsureLinksIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "bin")
	target := writeTarget(t, filepath.Join(tmp, "bundle"), "apptool")

	if _, err := EnsureLinks(dest, map[string]string{"apptool": target}); err != nil {
		t.Fatalf("first: %v", err)
	}
	before, err := os.Lstat(filepath.Join(dest, "apptool"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	created, err := EnsureLinks(dest, map[string]string{"apptool": target})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("second run reported creating %v; an unchanged link is a no-op", created)
	}
	after, err := os.Lstat(filepath.Join(dest, "apptool"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the link was re-created on an unchanged second run")
	}
}

// A link to a file that does not exist is refused. Creating it would put a
// broken command on PATH and report an uninstallable tool as installed.
func TestEnsureLinksRefusesAMissingTarget(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "bin")
	missing := filepath.Join(t.TempDir(), "nope", "apptool")

	_, err := EnsureLinks(dest, map[string]string{"apptool": missing})
	if !errors.Is(err, ErrLinkTargetMissing) {
		t.Fatalf("error = %v, want ErrLinkTargetMissing", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "apptool")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a link was created despite the missing target")
	}
}

// AES owns the directory, not everything in it. A regular file at the link
// path is somebody else's, and deleting it is the class of loss the config
// handling exists to avoid.
func TestEnsureLinksRefusesToOverwriteARegularFile(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "bin")
	target := writeTarget(t, filepath.Join(tmp, "bundle"), "apptool")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := filepath.Join(dest, "apptool")
	if err := os.WriteFile(existing, []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := EnsureLinks(dest, map[string]string{"apptool": target}); err == nil {
		t.Fatal("EnsureLinks overwrote a regular file")
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "mine\n" {
		t.Errorf("the user's file was modified: %q", data)
	}
}

// Uninstall removes the links AES created, and leaves alone one the user has
// since repointed: removing that would undo their work while reporting a
// clean removal.
func TestRemoveLinksOnlyRemovesMatchingLinks(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "bin")
	ours := writeTarget(t, filepath.Join(tmp, "a"), "apptool")
	themed := writeTarget(t, filepath.Join(tmp, "b"), "apptool")

	if _, err := EnsureLinks(dest, map[string]string{"apptool": ours, "other": ours}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// The user repoints one of them.
	if err := os.Remove(filepath.Join(dest, "other")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(themed, filepath.Join(dest, "other")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	errs := RemoveLinks(dest, map[string]string{"apptool": ours, "other": ours})
	if len(errs) != 0 {
		t.Fatalf("RemoveLinks: %v", errs)
	}
	if _, err := os.Lstat(filepath.Join(dest, "apptool")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the link AES created was not removed")
	}
	if _, err := os.Lstat(filepath.Join(dest, "other")); err != nil {
		t.Errorf("RemoveLinks removed a link the user had repointed: %v", err)
	}
}

// A regular file at the link path is not AES's, so removal must not touch it.
func TestRemoveLinksLeavesRegularFilesAlone(t *testing.T) {
	dest := t.TempDir()
	p := filepath.Join(dest, "apptool")
	if err := os.WriteFile(p, []byte("mine\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	RemoveLinks(dest, map[string]string{"apptool": "/somewhere/apptool"})
	if _, err := os.Stat(p); err != nil {
		t.Errorf("RemoveLinks deleted a regular file: %v", err)
	}
}

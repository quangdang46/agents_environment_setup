package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of this bead: an ecosystem strategy REPLACES a binary in
// place. `go install`, `npm -g`, `cargo install` and `uv` all write over the
// existing file, so if the thing they produce crashes we have destroyed a
// working install with no way back. Every other bug in this tool reports a
// problem; this one breaks the user's machine.
//
// ACFS handles it by keeping the predecessor at <binary>.prev and restoring
// it when a post-install probe comes back broken. These tests are the
// mechanism; the wiring into setup is in internal/cli.
func TestRetainKeepsThePredecessorBeforeAnOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	const original = "#!/bin/sh\necho the working version\n"
	if err := os.WriteFile(bin, []byte(original), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	// Now do what an ecosystem installer does: replace it with something broken.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	got, err := os.ReadFile(PrevPath(bin))
	if err != nil {
		t.Fatalf("the predecessor is gone: %v", err)
	}
	if string(got) != original {
		t.Errorf("the retained copy is %q, want the original %q", got, original)
	}
}

// A hard link would satisfy that test and fail in production: the ecosystem
// installer writes through the same inode, so a linked "backup" would be
// modified along with the file it was supposed to protect.
func TestRetainCopiesRatherThanLinks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat live: %v", err)
	}
	prevInfo, err := os.Stat(PrevPath(bin))
	if err != nil {
		t.Fatalf("stat prev: %v", err)
	}
	if os.SameFile(info, prevInfo) {
		t.Error("the predecessor is the same inode as the binary; an in-place " +
			"overwrite would destroy the backup along with the file")
	}
}

// The first install has no predecessor, and that is not an error. Turning it
// into one would make a routine first-time install fail.
func TestRetainIsANoOpWhenThereIsNothingToRetain(t *testing.T) {
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "never-installed")
	if err := Retain(bin); err != nil {
		t.Errorf("Retain on a missing binary = %v, want nil", err)
	}
	if _, err := os.Stat(PrevPath(bin)); !os.IsNotExist(err) {
		t.Error("Retain created a .prev for a binary that never existed")
	}
}

func TestRestorePutsThePredecessorBack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	const original = "#!/bin/sh\necho the working version\n"
	if err := os.WriteFile(bin, []byte(original), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	restored, err := Restore(bin)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored {
		t.Fatal("Restore reported nothing was restored, but a predecessor existed")
	}
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != original {
		t.Errorf("after restore the binary is %q, want %q", got, original)
	}
}

// A restored binary that lost its execute bit is a different failure from the
// one being repaired, and it would pass a content-only assertion.
func TestRestorePreservesTheExecutableBit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := os.WriteFile(bin, []byte("broken"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	if _, err := Restore(bin); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("restored mode is %v; the binary is on PATH and cannot run", info.Mode().Perm())
	}
}

// The predecessor is consumed by a restore. Leaving it means a second restore
// puts the same old binary back over a newer one, and a stale .prev is
// indistinguishable from a fresh one.
func TestRestoreConsumesThePredecessor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, err := Restore(bin); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(PrevPath(bin)); !os.IsNotExist(err) {
		t.Error("the .prev survived a restore; a later one would revert a newer install")
	}
}

// Restore with nothing retained is not a failure. The caller reached it
// because a probe came back broken, and "there was no predecessor" is a fact
// about the install, not an error in the restore.
func TestRestoreWithNoPredecessorReportsFalseAndSucceeds(t *testing.T) {
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(bin, []byte("broken"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	restored, err := Restore(bin)
	if err != nil {
		t.Errorf("Restore with no predecessor = %v, want nil", err)
	}
	if restored {
		t.Error("Restore claimed to restore something with no predecessor")
	}
	// And it must not have touched the binary that is there.
	if got, _ := os.ReadFile(bin); string(got) != "broken" {
		t.Errorf("the broken binary was modified by a restore that had nothing to do: %q", got)
	}
}

// A probe that came back OK, or merely unknown, must not leave a .prev lying
// around. A tool that is never reinstalled would otherwise carry a stale copy
// of itself indefinitely, with nothing in the directory saying which file is
// the truth.
func TestDiscardDropsThePredecessorWithoutRestoring(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	const fresh = "#!/bin/sh\necho the new version\n"
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Retain(bin); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := os.WriteFile(bin, []byte(fresh), 0o755); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := Discard(bin); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(PrevPath(bin)); !os.IsNotExist(err) {
		t.Error("Discard left a .prev behind")
	}
	// The new binary is untouched — discarding a backup is not a restore.
	if got, _ := os.ReadFile(bin); string(got) != fresh {
		t.Errorf("Discard modified the installed binary: %q", got)
	}
}

func TestDiscardOnNothingRetainedIsANoOp(t *testing.T) {
	t.Parallel()
	if err := Discard(filepath.Join(t.TempDir(), "tool")); err != nil {
		t.Errorf("Discard with no .prev = %v, want nil", err)
	}
}

// The restore must be atomic: a machine that is being repaired must never be
// left with a half-written binary, which is a worse state than the broken one
// because it is a new failure nobody has seen before.
func TestWriteFileAtomicLeavesNoTempFilesBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := writeFileAtomic(bin, []byte("content"), 0o755); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".aes-restore-") {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}
	if len(entries) != 1 || entries[0].Name() != "tool" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want just [tool]", names)
	}
}

// A binary name is not a path fragment. The tool name comes from a manifest,
// and a manifest is data — but data still should not be concatenated into a
// path unquoted.
func TestPrevPathStaysBesideTheBinary(t *testing.T) {
	t.Parallel()
	got, want := PrevPath("/usr/local/bin/tool"), "/usr/local/bin/tool.prev"
	if got != want {
		t.Errorf("PrevPath = %q, want %q", got, want)
	}
}

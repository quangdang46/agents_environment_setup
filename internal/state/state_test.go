package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "aes", "state.json")
}

func seed(t *testing.T, path string, entries map[string]Installed) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(File{Version: Version, Installed: entries})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, data, DefaultFileMode); err != nil {
		t.Fatalf("write seed: %v", err)
	}
}

// A first run has no state file, and that is not a failure.
func TestOpenMissingFileIsEmptyNotError(t *testing.T) {
	s, err := Open(statePath(t))
	if err != nil {
		t.Fatalf("Open on missing file: %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
	if s.file.Version != Version {
		t.Errorf("Version = %d, want %d", s.file.Version, Version)
	}
}

// A corrupt file is an error, and specifically not an empty state: reading it
// as "nothing installed" is what causes the reinstall storm (I9).
func TestOpenCorruptIsErrorNotEmptyState(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"truncated", `{"version":1,"installed":{"ripgrep":{"strat`},
		{"not json", `this is not json at all`},
		{"empty", ``},
		{"wrong type", `{"version":"one","installed":{}}`},
		{"missing version field", `{"installed":{"ripgrep":{"strategy":"github-release"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := statePath(t)
			if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.body), DefaultFileMode); err != nil {
				t.Fatalf("write: %v", err)
			}

			s, err := Open(path)
			if err == nil {
				t.Fatal("Open on corrupt file returned no error")
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("error = %v, want ErrCorrupt", err)
			}
			// The load-bearing assertion: a corrupt file must not look clean.
			if s != nil && s.Len() > 0 {
				t.Errorf("corrupt file produced %d phantom entries", s.Len())
			}
		})
	}
}

// A file from a newer aes must be reported, never silently rewritten — doing
// so would drop entries the user still has.
func TestOpenVersionTooNewIsErrorAndPreservesBytes(t *testing.T) {
	path := statePath(t)
	original := `{"version":99,"installed":{"ripgrep":{"strategy":"package","requires_privilege":true}}}`
	if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(original), DefaultFileMode); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("Open on newer version returned no error")
	}
	if !errors.Is(err, ErrVersionTooNew) {
		t.Errorf("error = %v, want ErrVersionTooNew", err)
	}
	// The message has to name both versions, or the user cannot act on it.
	for _, want := range []string{"99", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name version %s", err, want)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if string(after) != original {
		t.Errorf("file was modified:\n got %s\nwant %s", after, original)
	}
}

func TestRoundTripPreservesAllEntries(t *testing.T) {
	path := statePath(t)
	entries := map[string]Installed{
		"ripgrep": {Strategy: "github-release", Version: "14.1.1", InstalledAt: "2026-09-27T00:00:00Z"},
		"jq":      {Strategy: "package", Privileged: true, InstalledAt: "2026-09-26T12:30:00Z"},
		"tmux":    {Strategy: "brew", Version: "3.4", InstalledAt: "2026-09-25T09:15:00Z"},
	}
	seed(t, path, entries)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Installed()
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for name, want := range entries {
		if got[name] != want {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
}

// Identical input must produce identical bytes, or a rewritten state file would
// show up as spurious drift.
func TestSaveIsByteDeterministic(t *testing.T) {
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Insert in a deliberately non-alphabetical order; map ordering must not
	// leak into the file.
	for _, name := range []string{"zoxide", "ripgrep", "claude", "jq"} {
		s.Set(name, Installed{Strategy: "github-release", InstalledAt: "2026-09-27T00:00:00Z"})
	}
	if err := s.Save(); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("Save is not deterministic:\n%s\n%s", first, second)
	}
}

func TestSaveLeavesNoTempFile(t *testing.T) {
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Set("ripgrep", Installed{Strategy: "github-release"})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tmp := tempFilesIn(t, filepath.Dir(path)); len(tmp) != 0 {
		t.Errorf("temp files left behind: %v", tmp)
	}
}

// The real test of atomicity: fail the write halfway and check the previous
// file is still there, still parseable, and no debris is left.
func TestSaveInterruptedMidWriteLeavesOriginalIntact(t *testing.T) {
	path := statePath(t)
	entries := map[string]Installed{
		"ripgrep": {Strategy: "github-release", Version: "14.1.1", InstalledAt: "2026-09-27T00:00:00Z"},
	}
	seed(t, path, entries)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Write a truncated prefix, then fail: the shape of a full disk or a
	// killed process, not an error before anything was written.
	s.writeTemp = func(f *os.File, b []byte) error {
		if _, err := f.Write(b[:len(b)/2]); err != nil {
			return err
		}
		return errors.New("simulated mid-write failure")
	}
	s.Set("jq", Installed{Strategy: "package", Privileged: true})

	if err := s.Save(); err == nil {
		t.Fatal("Save with a failing write returned no error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("original file is gone after a failed Save: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("original was modified by a failed Save:\n got %s\nwant %s", after, before)
	}
	// And it must still parse — that is the whole point.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("original no longer parses after a failed Save: %v", err)
	}
	if reopened.Len() != 1 {
		t.Errorf("original has %d entries, want 1", reopened.Len())
	}
	if tmp := tempFilesIn(t, filepath.Dir(path)); len(tmp) != 0 {
		t.Errorf("failed Save left temp files: %v", tmp)
	}
}

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not meaningful on windows")
	}
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Set("ripgrep", Installed{Strategy: "github-release"})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != DefaultDirMode {
		t.Errorf("dir mode = %o, want %o", got, DefaultDirMode)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != DefaultFileMode {
		t.Errorf("file mode = %o, want %o", got, DefaultFileMode)
	}
}

// Two writers racing must never produce a file that fails to parse. Run with
// -race for the data-race half.
func TestConcurrentSaveAlwaysParses(t *testing.T) {
	path := statePath(t)
	seed(t, path, map[string]Installed{
		"ripgrep": {Strategy: "github-release", InstalledAt: "2026-09-27T00:00:00Z"},
	})

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				t.Errorf("goroutine %d Open: %v", n, err)
				return
			}
			s.Set(toolName(n), Installed{Strategy: "github-release", InstalledAt: "2026-09-27T00:00:00Z"})
			if err := s.Save(); err != nil {
				t.Errorf("goroutine %d Save: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// Whatever the interleaving, the result must be readable.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("state unreadable after concurrent saves: %v", err)
	}
	if s.Len() == 0 {
		t.Error("concurrent saves produced an empty state")
	}
}

func TestSetDefaultsInstalledAt(t *testing.T) {
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Set("ripgrep", Installed{Strategy: "github-release"})
	got, ok := s.Get("ripgrep")
	if !ok {
		t.Fatal("Get after Set returned not-found")
	}
	if got.InstalledAt == "" {
		t.Error("Set did not default InstalledAt")
	}
}

func TestSetPreservesExplicitInstalledAt(t *testing.T) {
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Set("ripgrep", Installed{Strategy: "github-release", InstalledAt: "2020-01-02T03:04:05Z"})
	got, _ := s.Get("ripgrep")
	if got.InstalledAt != "2020-01-02T03:04:05Z" {
		t.Errorf("InstalledAt = %q, want the caller's value", got.InstalledAt)
	}
}

func TestRemove(t *testing.T) {
	path := statePath(t)
	seed(t, path, map[string]Installed{
		"ripgrep": {Strategy: "github-release", InstalledAt: "2026-09-27T00:00:00Z"},
	})
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.Remove("ripgrep") {
		t.Error("Remove of a known tool returned false")
	}
	if s.Remove("ripgrep") {
		t.Error("Remove of an already-removed tool returned true")
	}
	if _, ok := s.Get("ripgrep"); ok {
		t.Error("tool still present after Remove")
	}
}

// The accessor must not hand out the live map.
func TestInstalledReturnsACopy(t *testing.T) {
	path := statePath(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Set("ripgrep", Installed{Strategy: "github-release"})

	snapshot := s.Installed()
	delete(snapshot, "ripgrep")
	snapshot["injected"] = Installed{Strategy: "package"}

	if _, ok := s.Get("ripgrep"); !ok {
		t.Error("mutating the snapshot removed an entry from the store")
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 — snapshot mutation leaked into the store", s.Len())
	}
}

func toolName(n int) string { return "tool" + string(rune('a'+n)) }

// tempFilesIn lists leftover temp files in dir. Deliberately not a glob on
// ".state-*" alone: it should catch anything the store failed to clean up.
func tempFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func TestPathReportsWhereTheStoreReadsAndWrites(t *testing.T) {
	p := statePath(t)
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Path() != p {
		t.Errorf("Path() = %q, want %q", s.Path(), p)
	}
	// The path must be the one Save actually wrote, or a caller redirecting
	// state would inspect the wrong file.
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Errorf("Save did not write to the reported path: %v", err)
	}
}

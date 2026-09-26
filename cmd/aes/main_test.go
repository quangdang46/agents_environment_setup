package main

import (
	"os"
	"path/filepath"
	"testing"

	aessetup "github.com/quangdang46/agents_environment_setup"
	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/profile"
)

// TestEmbeddedAssetsAreLoadable is the point of embed.go: a binary that ships
// alone must still find a usable catalog. Without this, `curl … | sh` then
// `aes setup` fails with "catalog root: stat tools: no such file or
// directory" — which is the one path the product exists to support.
func TestEmbeddedAssetsAreLoadable(t *testing.T) {
	t.Parallel()

	// They must not merely exist — they must survive the round trip through
	// the real loaders, which is where a schema or path bug would show.
	dir := t.TempDir()
	toolDir := filepath.Join(dir, "tools")
	profDir := filepath.Join(dir, "profiles")
	materialise(aessetup.Tools, toolDir)
	materialise(aessetup.Profiles, profDir)

	cat, err := catalog.Load(toolDir)
	if err != nil {
		t.Fatalf("embedded catalog does not load: %v", err)
	}
	if cat.Len() == 0 {
		t.Fatal("embedded catalog is empty")
	}
	if _, err := profile.LoadDir(profDir); err != nil {
		t.Fatalf("embedded profiles do not load: %v", err)
	}
}

// TestResolveDataPrefersACheckout keeps the developer workflow working: a
// source checkout is used directly so editing a tool.yaml takes effect
// without a rebuild.
func TestResolveDataPrefersACheckout(t *testing.T) {
	// Not parallel: it chdirs.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdir(t, dir)

	// macOS hands back /var/folders/... but Getwd resolves the symlink to
	// /private/var/folders/..., so compare against the resolved form.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}

	catRoot, profDir, err := resolveData(filepath.Join(t.TempDir(), ".aes"))
	if err != nil {
		t.Fatalf("resolveData: %v", err)
	}
	if catRoot != filepath.Join(resolved, "tools") {
		t.Errorf("catalogRoot = %q, want %q", catRoot, filepath.Join(resolved, "tools"))
	}
	if profDir != filepath.Join(resolved, "profiles") {
		t.Errorf("profileDir = %q, want %q", profDir, filepath.Join(resolved, "profiles"))
	}
}

// TestResolveDataFallsBackToEmbedded covers the installed-binary case.
func TestResolveDataFallsBackToEmbedded(t *testing.T) {
	chdir(t, t.TempDir()) // no go.mod anywhere above this temp dir

	aesHome := t.TempDir()
	catRoot, profDir, err := resolveData(aesHome)
	if err != nil {
		t.Fatalf("resolveData: %v", err)
	}
	if !isPopulated(catRoot) {
		t.Errorf("catalog was not materialised: %q", catRoot)
	}
	if !isPopulated(profDir) {
		t.Errorf("profiles were not materialised: %q", profDir)
	}
	// The materialised catalog must actually be loadable, not just present.
	if _, err := catalog.Load(catRoot); err != nil {
		t.Errorf("materialised catalog does not load: %v", err)
	}
}

// TestResolveDataIsIdempotent: a second run must reuse what the first wrote,
// and must not leave a staging directory behind.
func TestResolveDataIsIdempotent(t *testing.T) {
	chdir(t, t.TempDir())
	aesHome := t.TempDir()

	firstRoot, _, err := resolveData(aesHome)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	secondRoot, _, err := resolveData(aesHome)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if firstRoot != secondRoot {
		t.Errorf("paths differ between runs: %q vs %q", firstRoot, secondRoot)
	}

	staging := firstRoot + ".tmp"
	if _, err := os.Stat(staging); err == nil {
		t.Error("a staging directory was left behind")
	}
}

// TestResolveDataSelfHealsFromAnEmptyCache: an interrupted earlier run can
// leave a directory that exists but is empty, which must be treated as absent
// rather than as a valid empty catalog.
func TestResolveDataSelfHealsFromAnEmptyCache(t *testing.T) {
	chdir(t, t.TempDir())
	aesHome := t.TempDir()

	share := filepath.Join(aesHome, "share", version)
	if err := os.MkdirAll(filepath.Join(share, "tools"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(share, "profiles"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	catRoot, _, err := resolveData(aesHome)
	if err != nil {
		t.Fatalf("resolveData: %v", err)
	}
	if !isPopulated(catRoot) {
		t.Error("an empty cache directory was not repopulated")
	}
}

func TestFindRepoRootReturnsEmptyOutsideACheckout(t *testing.T) {
	chdir(t, t.TempDir())
	if got := findRepoRoot(); got != "" {
		t.Errorf("findRepoRoot() = %q, want \"\" outside a checkout", got)
	}
}

// chdir moves into dir for the duration of the test. It deliberately does not
// use t.Chdir so the Go version requirement is explicit and the restore is
// unconditional.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

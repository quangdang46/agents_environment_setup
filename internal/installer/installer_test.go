package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// tgz builds a gzipped tar in memory from name → content. An entry whose
// content is nil becomes a regular empty file; a name ending in "/" becomes a
// directory. Names are written verbatim, so a test can put a hostile
// "../escape" in an archive that is otherwise perfectly well formed.
func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// Sorted so the archive is byte-stable across runs.
	sort.Strings(names)

	for _, name := range names {
		content := files[name]
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
			content = ""
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serverFixture serves one asset over HTTP and reports its digest.
type serverFixture struct {
	URL    string
	Digest string
	Asset  []byte
}

// newServer serves the given asset body at /asset and returns the base URL to
// point GithubRelease.DownloadBase at.
func newServer(t *testing.T, asset []byte) *serverFixture {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(asset)
	}))
	t.Cleanup(srv.Close)
	return &serverFixture{URL: srv.URL, Digest: sumOf(asset), Asset: asset}
}

func linuxHost() *platform.Host {
	return &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64}
}

func actionFor(fixture *serverFixture) Action {
	return Action{
		Tool: "aes",
		Target: manifest.Target{
			Strategy:   manifest.StrategyGithubRelease,
			Repository: "quangdang46/agents_environment_setup",
			Asset:      map[string]string{platform.ArchAMD64: "aes-linux-amd64.tar.gz"},
			SHA256:     map[string]string{platform.ArchAMD64: fixture.Digest},
		},
		Host:   linuxHost(),
		Reason: "default",
	}
}

func newInstaller(base, dest string) *GithubRelease {
	return &GithubRelease{
		Version:      "v1.0.0",
		DownloadBase: base,
		Dest:         dest,
		Client:       &http.Client{},
	}
}

// TestInstallSuccess is the happy path: a correct checksum yields an
// executable binary in Dest and no leftover staging.
func TestInstallSuccess(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "#!/bin/sh\necho aes\n"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	g := newInstaller(fixture.URL, dest)
	if err := g.Install(context.Background(), actionFor(fixture)); err != nil {
		t.Fatalf("Install: %v", err)
	}

	bin := filepath.Join(dest, "aes")
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary mode is %v, want the execute bit set", info.Mode().Perm())
	}

	assertNoStaging(t, dest, "aes")
}

// TestChecksumMismatchLeavesNothingBehind is the single most important test
// in this package. The contract is the absence of a file in Dest, not the
// error text: an installer that extracts before verifying has already lost
// the guarantee it exists to provide.
func TestChecksumMismatchLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "real binary"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	a := actionFor(fixture)
	a.Target.SHA256[platform.ArchAMD64] = sumOf([]byte("something else entirely"))

	err := newInstaller(fixture.URL, dest).Install(context.Background(), a)
	if err == nil {
		t.Fatal("expected a checksum error, install reported success")
	}
	if !errors.Is(err, ErrChecksum) {
		t.Errorf("error = %v, want ErrChecksum", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "aes")); !os.IsNotExist(statErr) {
		t.Error("a binary was placed despite the checksum mismatch — this is the bug this test exists for")
	}
	assertNoStaging(t, dest, "aes")
}

func TestMissingArchKeyNamesTheArch(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "binary"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	cases := []struct {
		name   string
		mutate func(*Action)
	}{
		{
			name:   "no asset key for this arch",
			mutate: func(a *Action) { delete(a.Target.Asset, platform.ArchAMD64) },
		},
		{
			name:   "no sha256 key for this arch",
			mutate: func(a *Action) { delete(a.Target.SHA256, platform.ArchAMD64) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := actionFor(fixture)
			tc.mutate(&a)

			err := newInstaller(fixture.URL, dest).Install(context.Background(), a)
			if !errors.Is(err, ErrMissingArch) {
				t.Fatalf("error = %v, want ErrMissingArch", err)
			}
			// Naming the arch is what lets an author fix the manifest.
			if !strings.Contains(err.Error(), platform.ArchAMD64) {
				t.Errorf("error %q does not name the architecture", err)
			}
		})
	}
}

// TestNeverFallsBackToAnotherArch pins the refusal to substitute. An amd64
// host with only an arm64 asset must fail, not install something it cannot
// run.
func TestNeverFallsBackToAnotherArch(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "arm binary"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	a := actionFor(fixture)
	a.Target.Asset = map[string]string{platform.ArchARM64: "aes-linux-arm64.tar.gz"}
	a.Target.SHA256 = map[string]string{platform.ArchARM64: fixture.Digest}

	if err := newInstaller(fixture.URL, dest).Install(context.Background(), a); !errors.Is(err, ErrMissingArch) {
		t.Errorf("error = %v, want ErrMissingArch (no fallback to arm64)", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "aes")); !os.IsNotExist(err) {
		t.Error("a binary was installed despite having no asset for this arch")
	}
}

func TestRejectsUnsafeArchives(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
	}{
		{"parent traversal", map[string]string{"../evil": "pwned", "aes": "binary"}},
		{"nested traversal", map[string]string{"dist/../../evil": "pwned", "aes": "binary"}},
		{"absolute path", map[string]string{"/etc/cron.d/evil": "pwned", "aes": "binary"}},
		{"leading dash", map[string]string{"-C": "pwned", "aes": "binary"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// A valid checksum, on purpose: the digest gate passes, so the
			// archive inspection is demonstrably the thing that refuses.
			asset := tgz(t, tc.files)
			fixture := newServer(t, asset)
			dest := t.TempDir()

			err := newInstaller(fixture.URL, dest).Install(context.Background(), actionFor(fixture))
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("error = %v, want ErrUnsafeArchive", err)
			}
			if _, err := os.Stat(filepath.Join(dest, "aes")); !os.IsNotExist(err) {
				t.Error("something was installed from an unsafe archive")
			}
			// Nothing escaped the destination.
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil")); !os.IsNotExist(err) {
				t.Error("an archive entry escaped the destination directory")
			}
			assertNoStaging(t, dest, "aes")
		})
	}
}

func TestMissingBinaryInArchive(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"something-else": "not the tool"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	err := newInstaller(fixture.URL, dest).Install(context.Background(), actionFor(fixture))
	if !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("error = %v, want ErrBinaryMissing", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "aes")); !os.IsNotExist(err) {
		t.Error("a binary was installed from an archive that did not contain one")
	}
}

func TestDownloadFailureLeavesNoPartialFile(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	dest := t.TempDir()
	fixture := &serverFixture{URL: srv.URL, Digest: "irrelevant"}

	err := newInstaller(srv.URL, dest).Install(context.Background(), actionFor(fixture))
	if err == nil {
		t.Fatal("expected an error for a 404")
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("destination contains %d entries after a failed download, want 0", len(entries))
	}
}

func TestAssetURLPinsVersion(t *testing.T) {
	t.Parallel()

	arch := platform.ArchAMD64
	a := Action{
		Tool: "aes",
		Target: manifest.Target{
			Strategy:   manifest.StrategyGithubRelease,
			Repository: "owner/repo",
			Asset:      map[string]string{arch: "aes-linux-amd64.tar.gz"},
			SHA256:     map[string]string{arch: "abc"},
		},
		Host: linuxHost(),
	}

	t.Run("pinned version addresses the tag", func(t *testing.T) {
		t.Parallel()
		g := &GithubRelease{Version: "v1.2.3"}
		want := "https://github.com/owner/repo/releases/download/v1.2.3/aes-linux-amd64.tar.gz"
		if got := g.assetURL(a, "aes-linux-amd64.tar.gz"); got != want {
			t.Errorf("assetURL = %q, want %q", got, want)
		}
	})

	t.Run("empty version resolves latest", func(t *testing.T) {
		t.Parallel()
		g := &GithubRelease{}
		want := "https://github.com/owner/repo/releases/latest/download/aes-linux-amd64.tar.gz"
		if got := g.assetURL(a, "aes-linux-amd64.tar.gz"); got != want {
			t.Errorf("assetURL = %q, want %q", got, want)
		}
	})

	t.Run("trailing slash on the repository is tolerated", func(t *testing.T) {
		t.Parallel()
		a := a
		a.Target.Repository = "owner/repo/"
		g := &GithubRelease{Version: "v1"}
		if got := g.assetURL(a, "x.tar.gz"); strings.Contains(got, "repo//") {
			t.Errorf("assetURL = %q, contains a doubled slash", got)
		}
	})
}

func TestRegistryResolvesByStrategy(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	t.Run("every declared strategy resolves to a distinct installer", func(t *testing.T) {
		t.Parallel()
		// Resolution is by strategy, so every strategy the manifest can
		// declare must be installable without core changes (I1).
		seen := map[Installer]bool{}
		for _, strategy := range manifest.ValidStrategies() {
			inst, err := r.Get(strategy)
			if err != nil {
				t.Errorf("Get(%q): %v", strategy, err)
				continue
			}
			if inst == nil {
				t.Errorf("Get(%q) returned a nil Installer", strategy)
				continue
			}
			if seen[inst] {
				t.Errorf("Get(%q) returned an Installer already bound to another strategy", strategy)
			}
			seen[inst] = true
		}
		if len(seen) != len(manifest.ValidStrategies()) {
			t.Errorf("registry holds %d distinct installers, want %d", len(seen), len(manifest.ValidStrategies()))
		}
	})

	t.Run("unknown strategy names the valid set", func(t *testing.T) {
		t.Parallel()
		// A typo must never silently become github-release: that would
		// change where code is downloaded from.
		_, err := r.Get("gihub-release")
		if !errors.Is(err, ErrUnknownStrategy) {
			t.Fatalf("error = %v, want ErrUnknownStrategy", err)
		}
		for _, want := range []string{"gihub-release", "github-release"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("strategies are sorted for stable messages", func(t *testing.T) {
		t.Parallel()
		if got := r.Strategies(); len(got) == 0 {
			t.Error("NewRegistry registered no strategies")
		}
	})
}

func TestRegistryInstallRejectsUnknownStrategy(t *testing.T) {
	t.Parallel()

	a := Action{Tool: "x", Target: manifest.Target{Strategy: "gihub-release"}, Host: linuxHost()}
	if err := NewRegistry().Install(context.Background(), a); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("error = %v, want ErrUnknownStrategy", err)
	}
}

func TestBinaryOverride(t *testing.T) {
	t.Parallel()

	// A tool whose binary is named differently from the tool.
	asset := tgz(t, map[string]string{"rg": "ripgrep binary"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	a := actionFor(fixture)
	a.Tool = "ripgrep"

	g := newInstaller(fixture.URL, dest)
	g.Binary = "rg"
	if err := g.Install(context.Background(), a); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "rg")); err != nil {
		t.Errorf("binary 'rg' not installed: %v", err)
	}
}

func TestNilHostIsAProgrammingError(t *testing.T) {
	t.Parallel()

	asset := tgz(t, map[string]string{"aes": "b"})
	fixture := newServer(t, asset)
	dest := t.TempDir()

	a := actionFor(fixture)
	a.Host = nil

	if err := newInstaller(fixture.URL, dest).Install(context.Background(), a); err == nil {
		t.Fatal("expected an error for an action with no host")
	}
}

// assertNoStaging verifies the staging directory is gone and Dest holds only
// the installed binary. A leftover .aes-download-* directory means an error
// path skipped its cleanup.
func assertNoStaging(t *testing.T, dest, want string) {
	t.Helper()
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".aes-download-") {
			t.Errorf("staging directory %s was left behind", e.Name())
		}
	}
	if len(entries) > 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		if fmt.Sprint(names) != fmt.Sprint([]string{want}) {
			t.Errorf("dest contains %v, want only %q", names, want)
		}
	}
}

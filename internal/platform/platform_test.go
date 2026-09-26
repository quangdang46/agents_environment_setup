package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// toolWith builds a manifest declaring an install target only for the given
// GOOS keys, so Supports can be exercised without a real catalog.
func toolWith(oses ...string) *manifest.Tool {
	t := &manifest.Tool{
		Name: "t", Description: "fixture", Provides: []string{"t"},
		Install: map[string]manifest.Target{},
	}
	for _, osName := range oses {
		t.Install[osName] = manifest.Target{Strategy: manifest.StrategyGo, GoPackage: "example.com/t"}
	}
	return t
}

// TestKeyIsTheInstallMapKey pins Key to the manifest install: vocabulary.
// The table covers every supported GOOS explicitly rather than asserting
// against the host, so this test is identical on a Mac and in a Linux
// container.
func TestKeyIsTheInstallMapKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		host Host
		want string
	}{
		{"darwin", Host{OS: OSDarwin, Arch: ArchARM64}, OSDarwin},
		{"linux", Host{OS: OSLinux, Arch: ArchAMD64}, OSLinux},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.host.Key(); got != tc.want {
				t.Errorf("Key() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOSConstantsMatchRuntime guards against the vocabulary drifting away
// from Go's own: the install: key is runtime.GOOS, spelled the same way.
func TestOSConstantsMatchRuntime(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ constant, runtimeValue string }{
		{OSDarwin, "darwin"},
		{OSLinux, "linux"},
		{ArchAMD64, "amd64"},
		{ArchARM64, "arm64"},
	} {
		if tc.constant != tc.runtimeValue {
			t.Errorf("constant %q does not match runtime value %q", tc.constant, tc.runtimeValue)
		}
	}
}

// TestSupports is deliberately host-independent: every case constructs its
// Host explicitly, so a darwin-only tool is skipped on a linux host whether
// or not the tests happen to be running on that host.
func TestSupports(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		host Host
		tool *manifest.Tool
		want bool
	}{
		{
			name: "darwin-only tool on linux is skipped",
			host: Host{OS: OSLinux, Arch: ArchAMD64},
			tool: toolWith(OSDarwin),
			want: false,
		},
		{
			name: "darwin-only tool on darwin is supported",
			host: Host{OS: OSDarwin, Arch: ArchARM64},
			tool: toolWith(OSDarwin),
			want: true,
		},
		{
			name: "tool with no install entry for this platform",
			host: Host{OS: OSLinux, Arch: ArchAMD64},
			tool: toolWith(OSDarwin),
			want: false,
		},
		{
			name: "tool with an empty install map",
			host: Host{OS: OSLinux, Arch: ArchAMD64},
			tool: toolWith(),
			want: false,
		},
		{
			name: "cross-platform tool is supported everywhere",
			host: Host{OS: OSLinux, Arch: ArchARM64},
			tool: toolWith(OSDarwin, OSLinux),
			want: true,
		},
		{
			name: "nil tool is not supported",
			host: Host{OS: OSDarwin},
			tool: nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.host.Supports(tc.tool); got != tc.want {
				t.Errorf("Supports() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTargetReturnsTheHostRecipe proves the lookup selects the recipe for
// this host rather than returning any recipe. The same tool legitimately
// installs by release on Linux and by package manager on macOS.
func TestTargetReturnsTheHostRecipe(t *testing.T) {
	t.Parallel()

	tool := &manifest.Tool{
		Name: "rg", Description: "ripgrep", Provides: []string{"rg"},
		Install: map[string]manifest.Target{
			OSLinux:  {Strategy: manifest.StrategyGithubRelease, Repository: "BurntSushi/ripgrep"},
			OSDarwin: {Strategy: manifest.StrategyPackage, Manager: manifest.ManagerBrew, Package: "ripgrep"},
		},
	}

	linux := &Host{OS: OSLinux, Arch: ArchAMD64}
	target, ok := linux.Target(tool)
	if !ok {
		t.Fatal("linux target not found")
	}
	if target.Strategy != manifest.StrategyGithubRelease {
		t.Errorf("linux strategy = %q, want github-release", target.Strategy)
	}

	darwin := &Host{OS: OSDarwin, Arch: ArchARM64, Manager: ManagerBrew}
	target, ok = darwin.Target(tool)
	if !ok {
		t.Fatal("darwin target not found")
	}
	if target.Strategy != manifest.StrategyPackage || target.Manager != manifest.ManagerBrew {
		t.Errorf("darwin target = %+v, want package/brew", target)
	}
}

func TestHas(t *testing.T) {
	t.Parallel()

	// The host is unused by Has, but keeping the method on Host is what
	// lets the verifier and installer share one interface.
	h := &Host{OS: runtime.GOOS, Arch: runtime.GOARCH}

	t.Run("empty name is false", func(t *testing.T) {
		t.Parallel()
		// Callers pass user input; an empty string must never be read as
		// a PATH element or the current directory.
		if h.Has("") {
			t.Error(`Has("") = true, want false`)
		}
		if h.Path("") != "" {
			t.Error(`Path("") should be empty`)
		}
	})

	t.Run("sh resolves", func(t *testing.T) {
		t.Parallel()
		if !h.Has("sh") {
			t.Error(`Has("sh") = false, want true`)
		}
		if h.Path("sh") == "" {
			t.Error(`Path("sh") is empty`)
		}
	})

	t.Run("absent binary is false", func(t *testing.T) {
		t.Parallel()
		const missing = "aes-definitely-not-a-real-binary-9f3a"
		if h.Has(missing) {
			t.Errorf("Has(%q) = true, want false", missing)
		}
		if h.Path(missing) != "" {
			t.Errorf("Path(%q) should be empty", missing)
		}
	})
}

// TestUnameArchToGo locks the one translation table in AES. install.sh
// mirrors it in shell; if these drift, the two frontends disagree about what
// asset to download.
func TestUnameArchToGo(t *testing.T) {
	t.Parallel()

	cases := []struct{ uname, want string }{
		{"x86_64", ArchAMD64},
		{"aarch64", ArchARM64},
		{"amd64", ArchAMD64},
		{"arm64", ArchARM64},
		// Unknown input is empty, not a guess: downloading the wrong
		// architecture is worse than refusing to download.
		{"i386", ""},
		{"", ""},
		{"riscv64", ""},
	}
	for _, tc := range cases {
		t.Run("uname "+tc.uname, func(t *testing.T) {
			t.Parallel()
			if got := unameArchToGo(tc.uname); got != tc.want {
				t.Errorf("unameArchToGo(%q) = %q, want %q", tc.uname, got, tc.want)
			}
		})
	}
}

// TestHomebrewPrefixOrdering runs against a real temporary filesystem so the
// result never depends on which prefix the machine running the tests has.
func TestHomebrewPrefixOrdering(t *testing.T) {
	t.Parallel()

	// fakeBrew creates prefix/bin/brew as an executable file under root.
	fakeBrew := func(t *testing.T, root, prefix string) {
		t.Helper()
		dir := filepath.Join(root, prefix, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "brew"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write brew: %v", err)
		}
	}

	t.Run("apple silicon prefix wins when both exist", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		fakeBrew(t, root, "/opt/homebrew")
		fakeBrew(t, root, "/usr/local")

		got, ok := homebrewPrefixIn(root)
		if !ok {
			t.Fatal("no prefix found")
		}
		if want := filepath.Join(root, "opt", "homebrew"); got != want {
			t.Errorf("prefix = %q, want %q (Apple Silicon must win)", got, want)
		}
	})

	t.Run("intel prefix found when it is the only one", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		fakeBrew(t, root, "/usr/local")

		got, ok := homebrewPrefixIn(root)
		if !ok {
			t.Fatal("no prefix found")
		}
		if want := filepath.Join(root, "usr", "local"); got != want {
			t.Errorf("prefix = %q, want %q", got, want)
		}
	})

	t.Run("no brew in either prefix", func(t *testing.T) {
		t.Parallel()
		if got, ok := homebrewPrefixIn(t.TempDir()); ok {
			t.Errorf("prefix = %q, want no prefix", got)
		}
	})

	t.Run("non-executable brew is not a working install", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dir := filepath.Join(root, "opt", "homebrew", "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "brew"), []byte("not executable"), 0o644); err != nil {
			t.Fatalf("write brew: %v", err)
		}
		if got, ok := homebrewPrefixIn(root); ok {
			t.Errorf("prefix = %q, want no prefix for a non-executable brew", got)
		}
	})

	t.Run("directory named brew is not a working install", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "opt", "homebrew", "bin", "brew"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got, ok := homebrewPrefixIn(root); ok {
			t.Errorf("prefix = %q, want no prefix for a directory named brew", got)
		}
	})
}

func TestCurrent(t *testing.T) {
	t.Parallel()

	h, err := Current()
	if err != nil {
		t.Fatalf("Current() on %s: %v", runtime.GOOS, err)
	}
	if h.OS != runtime.GOOS {
		t.Errorf("OS = %q, want runtime.GOOS %q", h.OS, runtime.GOOS)
	}
	// Host.Arch is runtime.GOARCH verbatim: no translation layer exists
	// inside AES, so these must be identical.
	if h.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want runtime.GOARCH %q", h.Arch, runtime.GOARCH)
	}
	switch h.Manager {
	case "", ManagerBrew, ManagerApt:
	default:
		t.Errorf("Manager = %q, want one of \"\", %q, %q", h.Manager, ManagerBrew, ManagerApt)
	}
	// Homebrew is a macOS concept; reporting it on Linux would send the
	// package strategy at a manager that is not there.
	if h.OS == OSLinux && h.Manager == ManagerBrew {
		t.Error("Manager = brew on linux")
	}
	if h.OS == OSLinux && h.Prefix != "" {
		t.Errorf("Prefix = %q, want empty on linux", h.Prefix)
	}
}

// TestHostIsComparableAndSerializable guards the reason Host is all strings:
// resolver.Action embeds it by pointer and must serialize deterministically.
func TestHostIsComparableAndSerializable(t *testing.T) {
	t.Parallel()

	a := Host{OS: OSDarwin, Arch: ArchARM64, Manager: ManagerBrew, Prefix: "/opt/homebrew"}
	b := Host{OS: OSDarwin, Arch: ArchARM64, Manager: ManagerBrew, Prefix: "/opt/homebrew"}
	if a != b {
		t.Error("Hosts with equal fields are not comparable with ==")
	}

	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Host
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(a, decoded) {
		t.Errorf("round trip changed the host: %+v -> %+v", a, decoded)
	}
}

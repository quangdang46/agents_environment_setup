package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// echoRunner records invocations and returns a canned result. Nothing in
// this file actually installs anything.
type echoRunner struct {
	calls  []string
	stdout string
	err    error
}

func (r *echoRunner) run(_ context.Context, cmd string) (exec.Result, error) {
	r.calls = append(r.calls, cmd)
	return exec.Result{Stdout: r.stdout, ExitCode: 0}, r.err
}

// presentHost reports every binary as present; absentHost reports none.
func presentHost() *platform.Host {
	return &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64}
}
func absentHost() *platform.Host {
	// Has() is a real LookPath, so an empty string is the way to make a
	// binary "absent" without touching the filesystem: a name that cannot
	// resolve. The tests that need absence install a Host whose PATH is
	// emptied via PATH="" in the environment.
	return &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64}
}

func ecoAction(t *testing.T, coord string) Action {
	t.Helper()
	target := manifest.Target{Strategy: manifest.StrategyGo, GoPackage: coord}
	switch {
	case strings.HasPrefix(coord, "npm:"):
		target = manifest.Target{Strategy: manifest.StrategyNPM, NPMPackage: strings.TrimPrefix(coord, "npm:")}
	case strings.HasPrefix(coord, "cargo:"):
		target = manifest.Target{Strategy: manifest.StrategyCargo, CargoName: strings.TrimPrefix(coord, "cargo:")}
	case strings.HasPrefix(coord, "uv:"):
		target = manifest.Target{Strategy: manifest.StrategyUV, UVPackage: strings.TrimPrefix(coord, "uv:")}
	}
	return Action{Tool: "demo", Target: target, Host: presentHost()}
}

// TestEcosystemCommandLine pins the exact command each ecosystem renders.
// These are assertions about a string, which is the whole contract of a thin
// adapter: AES picks the coordinate, the ecosystem does the work.
func TestEcosystemCommandLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		build func() *Ecosystem
		coord string
		tool  string
		want  string
	}{
		{
			name:  "go",
			build: NewGoInstaller,
			coord: "github.com/x/y/cmd/z@v1.2.3",
			tool:  "go",
			want:  "go install github.com/x/y/cmd/z@v1.2.3",
		},
		{
			name:  "npm",
			build: NewNPMInstaller,
			coord: "npm:some-cli@2.0.0",
			tool:  "npm",
			want:  "npm install -g some-cli@2.0.0",
		},
		{
			name:  "cargo",
			build: NewCargoInstaller,
			coord: "cargo:ripgrep@14.1.1",
			tool:  "cargo",
			// The version becomes a flag: `cargo install t@v1` is only
			// supported by recent cargo, while --version works everywhere.
			want: "cargo install ripgrep --version 14.1.1",
		},
		{
			name:  "uv",
			build: NewUVInstaller,
			coord: "uv:ruff@0.5.0",
			tool:  "uv",
			want:  "uv tool install ruff@0.5.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := tc.build()
			e.Host = presentHost()
			rec := &echoRunner{}
			e.Run = rec.run

			a := ecoAction(t, tc.coord)
			a.Tool = "demo"
			if err := e.Install(context.Background(), a); err != nil {
				t.Fatalf("Install: %v", err)
			}
			if len(rec.calls) != 1 || rec.calls[0] != tc.want {
				t.Errorf("ran %v, want exactly [%q]", rec.calls, tc.want)
			}
		})
	}
}

// TestEcosystemRequiresToolchain pins the gating: no toolchain, no run. The
// error must name the toolchain so the catalog can declare it as a
// dependency — the installer deliberately does not go looking for one.
func TestEcosystemRequiresToolchain(t *testing.T) {

	for _, tc := range []struct {
		name  string
		build func() *Ecosystem
		coord string
		tool  string
	}{
		{"go", NewGoInstaller, "github.com/x/y@v1.0.0", "go"},
		{"npm", NewNPMInstaller, "npm:cli@1.0.0", "npm"},
		{"cargo", NewCargoInstaller, "cargo:crate@1.0.0", "cargo"},
		{"uv", NewUVInstaller, "uv:tool@1.0.0", "uv"},
	} {
		t.Run(tc.name, func(t *testing.T) {

			// An empty PATH makes every toolchain unresolvable, so the gate
			// is exercised without depending on what this machine has.
			t.Setenv("PATH", "")

			e := tc.build()
			e.Host = &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64}
			rec := &echoRunner{}
			e.Run = rec.run

			err := e.Install(context.Background(), ecoAction(t, tc.coord))
			if err == nil {
				t.Fatal("expected an error when the toolchain is absent")
			}
			if !strings.Contains(err.Error(), tc.tool) {
				t.Errorf("error %q does not name the missing toolchain %q", err, tc.tool)
			}
			if len(rec.calls) != 0 {
				t.Errorf("ran %v without the toolchain present", rec.calls)
			}
		})
	}
}

// TestNoEcosystemNeedsPrivilege is the assertion the bead asks for: this must
// be a derived fact, not an assumption. All four write to user-owned
// directories.
func TestNoEcosystemNeedsPrivilege(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		strategy string
		target   manifest.Target
	}{
		{manifest.StrategyGo, manifest.Target{Strategy: manifest.StrategyGo, GoPackage: "x@v1"}},
		{manifest.StrategyNPM, manifest.Target{Strategy: manifest.StrategyNPM, NPMPackage: "x@1"}},
		{manifest.StrategyCargo, manifest.Target{Strategy: manifest.StrategyCargo, CargoName: "x@1"}},
		{manifest.StrategyUV, manifest.Target{Strategy: manifest.StrategyUV, UVPackage: "x@1"}},
	} {
		if RequiresPrivilege(tc.target) {
			t.Errorf("strategy %s requires privilege, want false", tc.strategy)
		}
	}
}

// TestOnlyAptNeedsPrivilege pins the other half of the derivation: exactly
// one strategy-manager pair escalates, and nothing else does.
func TestOnlyAptNeedsPrivilege(t *testing.T) {
	t.Parallel()

	escalating := manifest.Target{Strategy: manifest.StrategyPackage, Manager: manifest.ManagerApt}
	if !RequiresPrivilege(escalating) {
		t.Error("package+apt should require privilege")
	}
	for _, notEscalating := range []manifest.Target{
		{Strategy: manifest.StrategyPackage, Manager: manifest.ManagerBrew},
		{Strategy: manifest.StrategyGithubRelease},
		{Strategy: manifest.StrategyGo},
		{Strategy: manifest.StrategyNPM},
		{Strategy: manifest.StrategyCargo},
		{Strategy: manifest.StrategyUV},
		// A strategy other than package carrying a stray apt manager must
		// still not escalate. manifest validation rejects this combination,
		// so it is unreachable through a parsed manifest — but the
		// derivation is documented as being on (Strategy, Manager), and
		// this is the case that pins the Strategy half of that pair. A
		// Manager-only check would pass every other case here.
		{Strategy: manifest.StrategyGithubRelease, Manager: manifest.ManagerApt},
		{Strategy: manifest.StrategyGo, Manager: manifest.ManagerApt},
	} {
		if RequiresPrivilege(notEscalating) {
			t.Errorf("%s should not require privilege", notEscalating.Strategy)
		}
	}
}

// TestEcosystemDestination feeds envgen. A go tool that installed into
// ~/go/bin is installed and not runnable unless that directory reaches PATH.
func TestEcosystemDestination(t *testing.T) {

	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("go honours GOBIN", func(t *testing.T) {
		t.Setenv("GOBIN", filepath.Join(home, "custom-gobin"))
		if got, want := GoBinDir(), filepath.Join(home, "custom-gobin"); got != want {
			t.Errorf("GoBinDir() = %q, want %q", got, want)
		}
	})

	t.Run("go falls back to GOPATH then home", func(t *testing.T) {
		os.Unsetenv("GOBIN")
		t.Setenv("GOPATH", filepath.Join(home, "gopath"))
		if got, want := GoBinDir(), filepath.Join(home, "gopath", "bin"); got != want {
			t.Errorf("GoBinDir() = %q, want %q", got, want)
		}
		os.Unsetenv("GOPATH")
		if got, want := GoBinDir(), filepath.Join(home, "go", "bin"); got != want {
			t.Errorf("GoBinDir() = %q, want %q", got, want)
		}
	})

	t.Run("cargo honours CARGO_HOME", func(t *testing.T) {
		t.Setenv("CARGO_HOME", filepath.Join(home, "cargo-home"))
		if got, want := CargoBinDir(), filepath.Join(home, "cargo-home", "bin"); got != want {
			t.Errorf("CargoBinDir() = %q, want %q", got, want)
		}
		os.Unsetenv("CARGO_HOME")
		if got, want := CargoBinDir(), filepath.Join(home, ".cargo", "bin"); got != want {
			t.Errorf("CargoBinDir() = %q, want %q", got, want)
		}
	})

	t.Run("uv honours UV_TOOL_BIN_DIR", func(t *testing.T) {
		t.Setenv("UV_TOOL_BIN_DIR", filepath.Join(home, "uvbin"))
		if got, want := UVBinDir(), filepath.Join(home, "uvbin"); got != want {
			t.Errorf("UVBinDir() = %q, want %q", got, want)
		}
		os.Unsetenv("UV_TOOL_BIN_DIR")
		if got, want := UVBinDir(), filepath.Join(home, ".local", "bin"); got != want {
			t.Errorf("UVBinDir() = %q, want %q", got, want)
		}
	})
}

func TestNPMBinDirAsksNPM(t *testing.T) {
	t.Parallel()

	prefix := "/opt/whatever"
	dir := NPMBinDir(context.Background(), func(context.Context, string) (string, error) {
		return prefix + "\n", nil
	})
	if want := filepath.Join(prefix, "bin"); dir != want {
		t.Errorf("NPMBinDir() = %q, want %q", dir, want)
	}

	t.Run("a failing probe yields no directory rather than a guess", func(t *testing.T) {
		t.Parallel()
		dir := NPMBinDir(context.Background(), func(context.Context, string) (string, error) {
			return "", errors.New("npm: command not found")
		})
		// A wrong path on PATH is worse than a missing one: envgen skips
		// an empty directory, but would faithfully add a wrong one.
		if dir != "" {
			t.Errorf("NPMBinDir() = %q, want \"\" when npm cannot be queried", dir)
		}
	})
}

// TestRegistryDestinationFor is the seam envgen consumes.
func TestRegistryDestinationFor(t *testing.T) {

	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Unsetenv("GOBIN")
	os.Unsetenv("GOPATH")

	r := NewRegistry()
	ctx := context.Background()

	t.Run("go reports its bin directory", func(t *testing.T) {
		got, ok := r.DestinationFor(ctx, manifest.StrategyGo)
		if !ok {
			t.Fatal("go should contribute a destination")
		}
		if want := filepath.Join(home, "go", "bin"); got != want {
			t.Errorf("DestinationFor(go) = %q, want %q", got, want)
		}
	})

	t.Run("package managers contribute nothing", func(t *testing.T) {
		// brew and apt already put their binaries on PATH.
		if got, ok := r.DestinationFor(ctx, manifest.StrategyPackage); ok {
			t.Errorf("DestinationFor(package) = %q, want no destination", got)
		}
	})

	t.Run("an unknown strategy contributes nothing", func(t *testing.T) {
		if got, ok := r.DestinationFor(ctx, "nope"); ok {
			t.Errorf("DestinationFor(nope) = %q, want no destination", got)
		}
	})
}

func TestEcosystemRejectsUnpinnedCoordinate(t *testing.T) {
	t.Parallel()

	// manifest rejects an unpinned coordinate at load, so reaching this means
	// the Target was hand-built. The installer must not paper over it.
	e := NewGoInstaller()
	e.Host = presentHost()
	rec := &echoRunner{}
	e.Run = rec.run

	a := Action{
		Tool:   "demo",
		Target: manifest.Target{Strategy: manifest.StrategyGo, GoPackage: "github.com/x/y"},
		Host:   presentHost(),
	}
	err := e.Install(context.Background(), a)
	if err == nil {
		t.Fatal("expected an error for an unpinned coordinate")
	}
	if len(rec.calls) != 0 {
		t.Errorf("ran %v for an unpinned coordinate", rec.calls)
	}
}

func TestEcosystemSurfacesRunnerFailure(t *testing.T) {
	t.Parallel()

	e := NewGoInstaller()
	e.Host = presentHost()
	boom := errors.New("network unreachable")
	e.Run = (&echoRunner{err: boom}).run

	// The ecosystem's own error must reach the user: a failed install that
	// swallows "network unreachable" is useless.
	err := e.Install(context.Background(), ecoAction(t, "github.com/x/y@v1.0.0"))
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the runner's error to be surfaced", err)
	}
}

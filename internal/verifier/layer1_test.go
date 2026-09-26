package verifier

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// Layer 1 proves detection against the tools actually on this machine. Install
// is skipped; verify is the assertion.
//
// The expectations here are floors, not pins. Each `min` sits below what a
// reasonably current machine has, so the assertion — "StatusOK, with a version
// read and above this floor" — holds on any host where the tool is installed
// and on whatever version it has next. Pinning an exact version would turn this
// into a test that fails the day a tool ships a release, which is a test about
// the internet, not about the verifier.
//
// Nothing here reads runtime.GOOS and compares it to runtime.GOOS. A test that
// does that passes on the machine that wrote it and fails on everyone else's,
// or worse passes everywhere while checking nothing.
type layer1Fixture struct {
	name string
	bin  string
	// versionCmd prints the tool's version, and min is the floor asserted
	// against it.
	versionCmd string
	min        string
}

var layer1Fixtures = []layer1Fixture{
	{"ripgrep", "rg", "rg --version", "14.0"},
	{"git", "git", "git --version", "2.30"},
	{"fzf", "fzf", "fzf --version", "0.50"},
	{"jq", "jq", "jq --version", "1.6"},
	{"tmux", "tmux", "tmux -V", "3.4"},
	{"zoxide", "zoxide", "zoxide --version", "0.9.0"},
	{"gh", "gh", "gh --version", "2.0"},
	{"claude", "claude", "claude --version", "1.0"},
}

// toolFor builds the manifest a real tool.yaml would carry for this fixture.
func (f layer1Fixture) tool() *manifest.Tool {
	return &manifest.Tool{
		Name: f.name,
		Verify: &manifest.Verify{
			Command: f.bin,
			Version: &manifest.VersionCheck{Command: f.versionCmd, Min: f.min},
		},
	}
}

func TestLayer1VerifyFixtures(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	for _, f := range layer1Fixtures {
		t.Run(f.name, func(t *testing.T) {
			// LookPath, not a shell: `command -v` in zsh reports function and
			// alias definitions, and this machine aliases tmux to a plugin
			// wrapper. A shell check would pass a tool that has no binary.
			if _, err := exec.LookPath(f.bin); err != nil {
				t.Skipf("%s is not installed on this machine", f.bin)
			}

			got := Verify(f.tool())

			if got.Status != StatusOK {
				t.Errorf("Status = %q, want %q (version %q, stderr from the probe may explain it)",
					got.Status, StatusOK, got.Version)
			}
			if got.Version == "" {
				t.Error("Version is empty; a version command was declared, so one was expected")
			}
			if got.Path == "" {
				t.Error("Path was not resolved for a tool found on PATH")
			}
			if got.Tool != f.name {
				t.Errorf("Tool = %q, want %q", got.Tool, f.name)
			}
		})
	}
}

// Every fixture's version string must actually parse. This is the check that
// caught tmux 3.6b being read as "3", which made a tmux with a 3.x minimum
// report stale on every single run.
func TestLayer1FixturesParseRealVersionOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	for _, f := range layer1Fixtures {
		t.Run(f.name, func(t *testing.T) {
			if _, err := exec.LookPath(f.bin); err != nil {
				t.Skipf("%s is not installed on this machine", f.bin)
			}
			run, err := exec.Command("sh", "-c", f.versionCmd).Output()
			if err != nil {
				t.Skipf("%s %q failed: %v", f.bin, f.versionCmd, err)
			}
			got := ExtractVersion(string(run))
			if got == "" {
				t.Errorf("%q printed %q, from which no version could be extracted",
					f.versionCmd, strings.TrimSpace(string(run)))
			}
			if c := CompareVersions(got, f.min); c < 0 {
				t.Errorf("%s reports %q, below the asserted floor %q", f.name, got, f.min)
			}
		})
	}
}

// The catalog's `tested` flag and this fixture are one coupled claim: a tool
// marked tested:true that fails here is lying. This check is what enforces it
// once the catalog exists (bead 0ls); until then it has nothing to inspect.
func TestTestedToolsPassLayer1(t *testing.T) {
	if testing.Short() {
		t.Skip("layer 1 fixtures exercise the real machine; skipped under -short")
	}
	root := filepath.Join("..", "..", "tools")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("no tools/ directory yet; the catalog is bead 0ls")
	}
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	for _, tool := range c.Tested() {
		t.Run(tool.Name, func(t *testing.T) {
			got := Verify(tool)
			// Unknown is the honest failure here: a tested:true tool whose
			// version cannot be read has not had its minimum verified, which is
			// the whole claim the flag makes.
			if got.Status != StatusOK {
				t.Errorf("%s is marked tested:true but verifies as %q (version %q) — flip it to tested:false",
					tool.Name, got.Status, got.Version)
			}
		})
	}
}

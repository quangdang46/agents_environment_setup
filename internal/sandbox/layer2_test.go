package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Layer 2 runs the real thing: it downloads, extracts, chmods a binary and
// writes state. It is therefore opt-in, behind AES_LAYER2=1.
//
// This is not only about the network. A default `go test ./...` that installs
// software is a test nobody can run twice, and one that silently no-ops is
// worse: it looks like coverage. Making it explicit means a CI job that wants
// it says so, and a developer running the suite is not surprised by a tool
// appearing on their machine.
const layer2Env = "AES_LAYER2"

// buildAes compiles the binary under test from the working tree, so the layer
// being proven is the one in this checkout rather than whatever happens to be
// installed on the machine.
func buildAes(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "aes")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/aes")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build aes: %v\n%s", err, out)
	}
	return bin
}

func layer2Enabled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("Layer 2 installs real software; skipped under -short")
	}
	if os.Getenv(layer2Env) != "1" {
		t.Skipf("Layer 2 installs real software; set %s=1 to run it", layer2Env)
	}
}

// layer2Tools spans the strategies a run can prove on this host.
//
// The bead asks for three. On macOS two are reachable: github-release installs
// a real archive, and package/brew runs a real brew. apt is deliberately
// absent rather than faked — it is Linux-only, and a test that pretends
// otherwise would prove nothing about the privilege model, which is the entire
// reason to exercise that path. The Ubuntu container in a Linux CI job is
// where apt gets proven.
var layer2Tools = []struct {
	name     string
	strategy string
}{
	// github-release: a real archive, downloaded, checksum-verified, extracted.
	{"zellij", manifest.StrategyGithubRelease},
	{"opentofu", manifest.StrategyGithubRelease},
	{"starship", manifest.StrategyGithubRelease},
	// package/brew: a real brew invocation, so the run covers more than one
	// strategy on macOS. delta and gping are package on darwin; several other
	// package tools are already installed here, which requireAbsent rejects.
	{"delta", manifest.StrategyPackage},
	{"gping", manifest.StrategyPackage},
}

func TestLayer2RealInstall(t *testing.T) {
	layer2Enabled(t)

	host, err := platform.Current()
	if err != nil {
		t.Fatalf("detect host: %v", err)
	}
	c, err := catalog.Load(filepath.Join("..", "..", "tools"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	bin := buildAes(t)

	// The isolation the whole package rests on, asserted around a REAL run
	// rather than only around stubs.
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	hostAesHome := filepath.Join(realHome, ".aes")
	before, err := TreeFingerprint(hostAesHome)
	if err != nil {
		t.Fatalf("fingerprint host ~/.aes: %v", err)
	}

	strategiesProven := map[string]bool{}
	for _, tc := range layer2Tools {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := c.ByName(tc.name)
			if !ok {
				t.Fatalf("%s is not in the catalog", tc.name)
			}
			target, ok := host.Target(tool)
			if !ok {
				t.Skipf("%s has no %s target", tc.name, host.Key())
			}
			if target.Strategy != tc.strategy {
				t.Errorf("%s uses %s on %s, expected %s", tc.name, target.Strategy, host.Key(), tc.strategy)
			}
			if target.Strategy == manifest.StrategyPackage && target.Manager == manifest.ManagerBrew {
				if _, err := exec.LookPath("brew"); err != nil {
					t.Skip("brew is not installed")
				}
			}

			res, err := Run(context.Background(), tool, Config{
				Binary: bin,
				Keep:   os.Getenv("AES_KEEP_SANDBOX") == "1",
			})
			if err != nil {
				if errors.Is(err, ErrToolPresent) {
					t.Skipf("%v", err)
				}
				t.Fatalf("Run: %v", err)
			}
			if !res.Passed {
				t.Errorf("layer 2 sequence failed:\n%s", res.String())
				return
			}
			strategiesProven[target.Strategy] = true
		})
	}

	after, err := TreeFingerprint(hostAesHome)
	if err != nil {
		t.Fatalf("fingerprint host ~/.aes after: %v", err)
	}
	if before != after {
		t.Errorf("the layer 2 run modified the developer's real ~/.aes:\nbefore %s\nafter  %s\n"+
			"every tool ran with its own AES_HOME; something bypassed it", before, after)
	}

	if runtime.GOOS == "darwin" && len(strategiesProven) < 2 {
		t.Errorf("only %d strategy proven (%v); a layer 2 pass that exercises one path "+
			"is not much of a pass", len(strategiesProven), strategiesProven)
	}
	t.Logf("strategies proven: %v", strategiesProven)
}

// TestLayer2BrokenManifestInstallsNothing proves the failure path, which the
// happy-path sequence never reaches: a manifest the loader rejects must
// produce no binary and no state entry.
func TestLayer2BrokenManifestInstallsNothing(t *testing.T) {
	layer2Enabled(t)
	bin := buildAes(t)
	home := t.TempDir()

	// aes finds its catalog by walking up from the working directory for a
	// go.mod, falling back to the embedded copy only when there is none. So
	// exercising a hand-written manifest means standing up a real checkout,
	// not pointing the binary at a directory — and adding a flag purely to make
	// a test convenient would put a test-only code path in the product.
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module sandboxfixture\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	broken := filepath.Join(repo, "tools", "search", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// asset declares arm64 with no matching sha256 entry.
	manifestBody := "name: broken\n" +
		"description: deliberately invalid\n" +
		"category: search\n" +
		"tested: false\n" +
		"install:\n" +
		"  darwin:\n" +
		"    strategy: github-release\n" +
		"    repository: example/nothing\n" +
		"    asset:\n" +
		"      arm64: broken-arm64.tar.gz\n" +
		"verify:\n" +
		"  command: broken\n"
	if err := os.WriteFile(filepath.Join(broken, "tool.yaml"), []byte(manifestBody), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "setup", "--only", "broken", "--non-interactive")
	cmd.Env = sandboxEnv(home)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("a manifest with no checksum installed successfully:\n%s", out)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3 (catalog or manifest invalid)", exitErr.ExitCode())
	}
	if _, statErr := os.Stat(filepath.Join(home, "bin", "broken")); statErr == nil {
		t.Error("a rejected manifest still produced a binary")
	}
	if stateHas(home, "broken") {
		t.Error("a rejected manifest still wrote a state entry")
	}
	t.Logf("exit=%v output=%s", err, firstLine(string(out)))
}

// firstLine is the first non-empty line of output, for log lines.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

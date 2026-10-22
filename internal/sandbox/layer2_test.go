package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

// layer2Tools is the LOCAL default: the short, hand-picked list a host
// proof attempts when nothing else was asked for.
//
// It is short on purpose. The host run is not sandboxed where it matters —
// a package/brew tool installs into the real /opt/homebrew, which no home
// directory can redirect — so a developer who sets AES_LAYER2=1 gets a
// handful of tools proven rather than twenty-three formulae installed into
// a package manager they never agreed to touch. A clean CI runner is the
// machine that may be changed wholesale, and it asks for the whole
// selection by name; see hostSweep below.
//
// The bead asks for three. On macOS two are reachable: github-release installs
// a real archive, and package/brew runs a real brew. apt is deliberately
// absent rather than faked — it is Linux-only, and a test that pretends
// otherwise would prove nothing about the privilege model, which is the entire
// reason to exercise that path. The Ubuntu container in a Linux CI job is
// where apt gets proven.
var layer2Tools = []namedTool{
	// github-release: a real archive, downloaded, checksum-verified, extracted.
	{"zellij", manifest.StrategyGithubRelease},
	{"opentofu", manifest.StrategyGithubRelease},
	{"starship", manifest.StrategyGithubRelease},
	// package/brew: a real brew invocation, so the run covers more than one
	// strategy on macOS. delta is package on darwin; several other
	// package tools are already installed here, which requireAbsent rejects.
	{"delta", manifest.StrategyPackage},
	// The catalog's own tools: a darwin proof of them is a darwin proof of
	// something the project ships. They are github-release, so the run is a
	// real download, a real checksum, a real extract — and the drift and
	// force-reinstall steps apply as they do everywhere.
	//
	// A tool the machine already has is skipped by requireAbsent, so the
	// proof runs on a machine where these are not installed. The developer's
	// machine has all four, which is why the proof here is run with a PATH
	// that does not carry them.
	{"br", manifest.StrategyGithubRelease},
	{"bvr", manifest.StrategyGithubRelease},
	{"discord-cli", manifest.StrategyGithubRelease},
	{"hashline", manifest.StrategyGithubRelease},
}

// hostSweep is the list of tools a host proof attempts.
//
// The answer depends on who is asking, and the difference is about what the
// run is allowed to touch:
//
//   - Nothing requested: layer2Tools above, the hand-picked local list. That
//     is the default, and the reason that list is short is what the run
//     installs into — a brew formula lands in the real /opt/homebrew.
//
//   - AES_LAYER2_TOOL=all: the whole default selection, read from the catalog
//     rather than written down here. This is the CI proof, and a clean runner
//     is the only machine that can answer for the whole catalog: on a
//     developer's desk requireAbsent would skip most of it as already
//     installed, which is a partial answer reported as a full one. The local
//     default and the CI default are deliberately different lists, because
//     the question was never "which tools exist" but "which machine may this
//     run change".
//
//   - AES_LAYER2_TOOL=a,b: exactly those catalog tools, proven or not. Named
//     so one tool can be re-proved without re-running a sweep, and so a tool
//     that is not in the default set can be proven into it — a filter on a
//     run whose job is to gather evidence that does not exist yet.
//
// An entry read from the catalog carries no expected strategy. Its strategy
// would be read from the same manifest lookup the resolver makes, so
// comparing it against the resolved target would be a value against itself.
// The hand-picked list carries a literal per tool, and that one is an
// independent claim: what the author expected darwin to do, checked against
// what the manifest declares.
func hostSweep(t *testing.T, cat *catalog.Catalog) ([]namedTool, error) {
	t.Helper()

	names, all := layer2ToolFilter()
	if !all && len(names) == 0 {
		return layer2Tools, nil
	}

	var tools []*manifest.Tool
	if all {
		tools = cat.Default()
		if len(tools) == 0 {
			return nil, errors.New("no tool is marked default:true; this sweep would prove nothing")
		}
		t.Logf("AES_LAYER2_TOOL=all: the default selection, %d tools, to be proved on this machine", len(tools))
	} else {
		for _, name := range names {
			tool, ok := cat.ByName(name)
			if !ok {
				return nil, fmt.Errorf("%s is not in the catalog", name)
			}
			tools = append(tools, tool)
		}
		t.Logf("AES_LAYER2_TOOL=%v: %d named tools instead of the local default list", names, len(tools))
	}

	out := make([]namedTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, namedTool{name: tool.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// TestHostSweepSelection pins the three answers hostSweep can give. The
// difference between the first two is the whole reason a CI proof exists:
// "nothing was asked for" and "everything was asked for by name" both have to
// be answerable on a machine whose local default is a short list, and only
// the second one may change it wholesale.
//
// Nothing here installs anything. The selection is the part a run would
// otherwise discover ninety minutes in, after the machine had already been
// changed.
func TestHostSweepSelection(t *testing.T) {
	cat, err := catalog.Load(filepath.Join("..", "..", "tools"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	t.Run("no request is the hand-picked local list", func(t *testing.T) {
		t.Setenv("AES_LAYER2_TOOL", "")
		tools, err := hostSweep(t, cat)
		if err != nil {
			t.Fatalf("hostSweep: %v", err)
		}
		if len(tools) != len(layer2Tools) {
			t.Fatalf("got %d tools, want the %d hand-picked local ones", len(tools), len(layer2Tools))
		}
		for i, want := range layer2Tools {
			if tools[i].name != want.name {
				t.Errorf("tool %d = %s, want %s", i, tools[i].name, want.name)
			}
			if tools[i].strategy == "" {
				t.Errorf("%s carries no strategy expectation, so the check against the manifest would never run", want.name)
			}
		}
	})

	t.Run("all is the whole default selection, not the local list", func(t *testing.T) {
		t.Setenv("AES_LAYER2_TOOL", "all")
		tools, err := hostSweep(t, cat)
		if err != nil {
			t.Fatalf("hostSweep: %v", err)
		}
		want := cat.Default()
		if len(want) <= len(layer2Tools) {
			t.Fatalf("the default selection is %d tools; it must be more than the %d-tool local list or this assertion proves nothing", len(want), len(layer2Tools))
		}
		if len(tools) != len(want) {
			t.Fatalf("got %d tools, want the %d of the default selection", len(tools), len(want))
		}
		inDefault := map[string]bool{}
		for _, tool := range want {
			inDefault[tool.Name] = true
		}
		seen := map[string]bool{}
		for i, tc := range tools {
			if !inDefault[tc.name] {
				t.Errorf("%s is not in the default selection", tc.name)
			}
			if seen[tc.name] {
				t.Errorf("%s appears twice", tc.name)
			}
			seen[tc.name] = true
			if tc.strategy != "" {
				t.Errorf("%s was read from the catalog but carries a hand-written strategy expectation (%s); the two are the same value, so the comparison cannot fail", tc.name, tc.strategy)
			}
			if i > 0 && tools[i-1].name >= tc.name {
				t.Errorf("not sorted: %q before %q", tools[i-1].name, tc.name)
			}
		}
	})

	t.Run("names are exactly those tools", func(t *testing.T) {
		t.Setenv("AES_LAYER2_TOOL", "bvr, br")
		tools, err := hostSweep(t, cat)
		if err != nil {
			t.Fatalf("hostSweep: %v", err)
		}
		want := []string{"br", "bvr"}
		if len(tools) != len(want) {
			t.Fatalf("got %d tools, want %d", len(tools), len(want))
		}
		for i, name := range want {
			if tools[i].name != name {
				t.Errorf("tool %d = %s, want %s", i, tools[i].name, name)
			}
		}
	})

	t.Run("a name the catalog does not have is refused", func(t *testing.T) {
		// gping is the real one: a hand-kept list carried it after the
		// catalog had moved on, and the name sat there failing. The refusal
		// belongs here, where the list is built, rather than eighty steps
		// into a run that has already changed the machine.
		t.Setenv("AES_LAYER2_TOOL", "gping")
		tools, err := hostSweep(t, cat)
		if err == nil {
			t.Fatalf("a name that is not in the catalog was accepted: %v", tools)
		}
		if !strings.Contains(err.Error(), "not in the catalog") {
			t.Errorf("error = %q, want it to name the problem", err)
		}
	})
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

	sweep, err := hostSweep(t, c)
	if err != nil {
		t.Fatal(err)
	}

	strategiesProven := map[string]bool{}
	for _, tc := range sweep {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := c.ByName(tc.name)
			if !ok {
				t.Fatalf("%s is not in the catalog", tc.name)
			}
			target, ok := host.Target(tool)
			if !ok {
				t.Skipf("%s has no %s target", tc.name, host.Key())
			}
			// Only when the entry carries its own expectation. An entry
			// read from the catalog has none: its strategy would come from
			// the same manifest lookup the resolver makes, so comparing it
			// against the resolved target is a value against itself — a
			// test that cannot fail, which is worse than no test.
			if tc.strategy != "" && target.Strategy != tc.strategy {
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
				// The run that GATHERS evidence for this platform has to be
				// willing to install a tool that is unproven here.
				//
				// Without it the resolver skips exactly the tools whose proof
				// is missing for this platform, and the proof can therefore
				// only ever be gathered for a platform the tool is already
				// proven on. That is not a corner case: every tool in the
				// catalog is proven on linux and on nothing else, so the
				// whole host run used to fail at the first step with
				//
				//   nothing to install: no tool in this selection can be
				//   installed on darwin/arm64; proven only on linux/amd64,
				//   not on this host
				//
				// which is the product's own answer to a question the test
				// was asking. Every step still has to pass.
				AllowUnproven: true,
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

	// Coverage of strategies is reported, not demanded.
	//
	// The bead asks for three strategies, and on macOS only two are reachable:
	// brew installs to the real system (a container cannot run it), and apt is
	// Linux-only. A package-strategy tool is also only provable once — the
	// first run installs it, and the next run correctly refuses because the
	// tool is already present, which is requireAbsent doing its job.
	//
	// So failing on fewer than two would fail a correct run for the right
	// behaviour. What is enforced is that something was proven at all, and the
	// count is logged so a silent regression to one strategy is visible.
	if len(strategiesProven) == 0 {
		t.Error("no strategy was proven; a layer 2 pass that exercises no real install is not a pass")
	}
	if runtime.GOOS == "darwin" && len(strategiesProven) < 2 {
		t.Logf("only %v proven; apt is unreachable on darwin, and a package tool is provable "+
			"only until the first run installs it", strategiesProven)
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

// TestLayer2RepairsACorruptedBinary covers github-release recovery, and the
// comment below is deliberately honest about what it does NOT cover.
//
// It passes for a mundane reason: setup re-downloads the release, so a
// corrupted github-release binary self-heals on the next --force. That is real
// and worth asserting, but it is NOT the bead eu4 gap, and a test named
// "is rolled back" that passes because a download happened is worse than no
// test at all.
//
// The eu4 gap is narrower and this test cannot reach it. go install, npm -g,
// cargo install and uv REPLACE a binary in place, and re-running setup
// reproduces the same result from the same source — so --force cannot rescue
// them and only a retained predecessor can. Reproducing that needs an
// ecosystem tool whose install is deterministic and lands a broken binary,
// which means a fixture tool, not a catalogue entry.
//
// Naming this honestly rather than renaming it to something that passes.
// TestLayer2RepairsACorruptedBinary
//
// github-release cannot produce this failure: it verifies the checksum BEFORE
// extracting, so a corrupt download never becomes a working install. The
// ecosystem strategies can — go install, npm -g, cargo install and uv all
// REPLACE a binary in place, and if the thing they produce crashes we have
// destroyed a working install with no way back.
//
// That is the worst failure this tool can have. Every other bug reports a
// problem; this one breaks the machine. ACFS handles it by keeping the
// predecessor at <binary>.prev and restoring it atomically.
//
// Gated behind AES_LAYER2 like every other Layer 2 test, so ordinary CI stays
// green. The eu4 assertion needs a fixture tool and lands with that bead.
func TestLayer2RepairsACorruptedBinary(t *testing.T) {
	layer2Enabled(t)
	bin := buildAes(t)

	if _, err := catalog.Load(filepath.Join("..", "..", "tools")); err != nil {
		t.Fatalf("catalog did not load: %v", err)
	}
	home := t.TempDir()
	env := sandboxEnv(home)

	// A real install, so there is a genuine predecessor to lose.
	out, _, err := run(ctxFor(t), Host{}, Config{Binary: bin}, env, home,
		"setup", "--only", "starship", "--non-interactive")
	if err != nil {
		t.Fatalf("install starship: %v (%s)", err, firstLine(out))
	}
	binPath := filepath.Join(home, "bin", "starship")
	good, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("no installed binary to corrupt: %v", err)
	}

	// Now do what an ecosystem strategy does: replace it in place with something
	// that does not run.
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatalf("corrupt binary: %v", err)
	}

	// Drive the repair path.
	out, _, err = run(ctxFor(t), Host{}, Config{Binary: bin}, env, home,
		"setup", "--only", "starship", "--non-interactive", "--force")
	t.Logf("repair output: %s", firstLine(out))

	after, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("eu4: the binary is gone entirely after a failed probe; "+
			"there is nothing to restore. aes has destroyed a working install. output=%s", out)
	}
	if string(after) == "#!/bin/sh\nexit 127\n" {
		t.Errorf("eu4: the broken binary is still in place after a failed post-install probe; "+
			"the previous binary was not restored. output=%s", out)
	}
	if len(after) < len(good)/2 {
		t.Errorf("eu4: the binary after repair is %d bytes, was %d before; "+
			"that does not look like the original was restored", len(after), len(good))
	}
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

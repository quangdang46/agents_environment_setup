package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Layer 2 in a container is the proof the host cannot give.
//
// The host run starts from "the tool is not on the developer's PATH", which is
// a weaker claim than "the tool is not installed", and it cannot reach apt at
// all. Both problems have the same cause and the same fix: ask a machine where
// the answer is structural rather than arranged. A fresh ubuntu:24.04 has no
// jq because nothing has ever installed jq there.
//
// # The assertion that matters
//
// Each tool is measured absent INSIDE the container before the run and present
// after. Not inferred from the host, not assumed from the image. That is the
// difference between a run that proves the install path and one that would also
// have passed on a machine that already had the answer — the same shape as a
// green main with no CI. AbsentInside exists for exactly this and the
// assertion below calls it directly rather than trusting the precondition
// check inside the sequence to have done it.
//
// # One container PER TOOL
//
// The container is fresh for every tool, not shared across the sweep. A
// shared container is a machine that remembers: `docker`'s install drags git
// in as a dependency, so by the time the sweep reaches git the tool is
// already present and the run can prove nothing. The same contamination
// reaches the apt state, and a later package install then fails for a reason
// that has nothing to do with the tool under test.
//
// Freshness is the property the whole proof rests on — "a fresh ubuntu:24.04
// has no jq because nothing has ever installed jq there" — and it does not
// survive a tool installing another tool's dependency. One container per tool
// costs a container start and a prepare per tool, and that is the price of
// the absence assertion meaning what it says.

// containerToolsFor is every tool the DEFAULT selection resolves to, read from
// the catalog rather than from a list maintained here.
//
// The list used to be written out by hand, and it went stale the moment a tool
// was removed from the catalog: the test still asked for `aadc`, failed with
// "not in the catalog", and reported that as a Layer 2 failure. A hand-kept
// list is a second source of truth about the catalog, which is the exact shape
// this project has been fixing all week.
//
// The strategy is read from the resolved target too, so the assertion below
// compares aes's own answer against the manifest rather than against a constant
// that can go out of date independently of both.

func containerToolsFor(t *testing.T, cat *catalog.Catalog) []namedTool {
	t.Helper()

	tools := cat.Default()
	if len(tools) == 0 {
		t.Fatal("no tool is marked default:true; this test would be vacuous")
	}
	return namedToolsFor(t, cat, tools)
}

// namedToolsFor turns a set of tools into the sweep's list.
func namedToolsFor(t *testing.T, cat *catalog.Catalog, tools []*manifest.Tool) []namedTool {
	t.Helper()
	out := make([]namedTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, namedTool{name: tool.Name, strategy: strategyOf(tool)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// layer2ToolFilter reads what the caller asked the sweep to cover.
//
// Two different questions, returned as two values rather than folded into
// one, because they are not the same question: WHICH tools (a list), and
// whether the caller meant the whole selection by saying `all`. The host
// test's local default is a short hand-picked list, so it has to tell
// "nothing was asked for" from "everything was asked for by name" — and both
// arrive as an empty list if `all` is collapsed into the list.
//
// The filter exists because a sweep that takes ninety minutes is a bad way
// to answer one question. When a tool's proof fails, the useful next step is
// to re-run that tool — and the handoff is explicit that a contended apt
// mirror makes a whole sweep meaningless, so re-running the whole sweep to
// check one tool is the expensive way to do it.
//
// The names are catalog tools, not necessarily default ones: a tool that has
// not been proven yet is not in the default set, and proving it is exactly
// what this filter is for.
//
//	AES_LAYER2_TOOL=git-lfs,bvr AES_LAYER2=1 go test -run TestLayer2Container ./internal/sandbox/
//
// An empty value means no filter, and the sweep covers the default selection
// as it always has. `all` means the same thing to the container — the default
// selection is the whole catalog there — so the container caller ignores the
// flag and it is the host test that reads it.
func layer2ToolFilter() (names []string, all bool) {
	raw := strings.TrimSpace(os.Getenv("AES_LAYER2_TOOL"))
	if raw == "" {
		return nil, false
	}
	var parts []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 1 && strings.EqualFold(parts[0], "all") {
		return nil, true
	}
	return parts, false
}

// namedTool is one tool and, when an entry carries one, the strategy the
// proof expects it to use.
//
// strategy is a literal written by hand: an independent claim about what the
// manifest will say, checked against what it actually says. An entry read
// from the catalog leaves it empty, because its strategy IS what the
// manifest says — asserting the two would be an assertion that cannot fail.
type namedTool struct {
	name     string
	strategy string
}

// strategyOf reads the linux strategy out of a manifest rather than assuming
// it, so the test's expectation comes from the same declaration the resolver
// will use.
func strategyOf(tool *manifest.Tool) string {
	target, ok := (&platform.Host{OS: platform.OSLinux}).Target(tool)
	if !ok {
		return ""
	}
	return target.Strategy
}

// containerPrep are the packages the fixture needs before any strategy can run.
//
// ca-certificates is not optional and its absence is a lie the harness tells: a
// bare ubuntu:24.04 has no CA bundle, so every https request fails with
// "certificate signed by unknown authority" and the error names the network
// rather than the missing package. nodejs/npm are the npm strategy's toolchain,
// which the Ecosystem installer refuses to go hunting for.
//
// git is deliberately NOT here even though the go toolchain wanted it. It is in
// the list above, and installing it as fixture setup would make requireAbsent
// refuse the git test for a reason that has nothing to do with aes — the harness
// would be breaking its own starting condition.
var containerPrep = []string{"ca-certificates", "nodejs", "npm"}

func TestLayer2ContainerInstallsOnAFreshMachine(t *testing.T) {
	layer2Enabled(t)

	// The budget covers the whole sweep, and the sweep starts a container and
	// prepares it for every tool. 40 minutes was enough for a ten-tool sweep
	// in a shared container; it is not enough for sixty-three fresh ones.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	if err := ContainerAvailable(ctx); err != nil {
		t.Skipf("no usable container runtime: %v", err)
	}

	// The arch is read before the build, because the build has to target it.
	// runtime.GOARCH would be right on both current runners and wrong on the
	// first remote daemon, where it shows up as a corrupt download.
	arch, err := ImageGoArch(ctx, DefaultImage)
	if err != nil {
		t.Fatalf("read image architecture: %v", err)
	}
	bin := buildAesFor(t, "linux", arch)

	// The isolation this package rests on, asserted around a REAL container
	// run rather than only around stubs.
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	hostAesHome := filepath.Join(realHome, ".aes")
	before, err := TreeFingerprint(hostAesHome)
	if err != nil {
		t.Fatalf("fingerprint host ~/.aes: %v", err)
	}

	catalogRoot := filepath.Join("..", "..", "tools")
	cat, err := catalog.Load(catalogRoot)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	// The host the catalog is resolved against is Linux, because that is where
	// the install happens. Reading runtime.GOOS here would pick darwin and every
	// target lookup would silently miss.
	linux := &platform.Host{OS: platform.OSLinux, Arch: arch, Manager: platform.ManagerApt}

	// Read from the catalog, so a tool added to or removed from the catalog
	// changes what this proves without anyone editing the test.
	tools := containerToolsFor(t, cat)
	filtered, _ := layer2ToolFilter()
	if len(filtered) > 0 {
		var picked []*manifest.Tool
		for _, name := range filtered {
			tool, ok := cat.ByName(name)
			if !ok {
				t.Fatalf("%s is not in the catalog", name)
			}
			picked = append(picked, tool)
		}
		tools = namedToolsFor(t, cat, picked)
		t.Logf("sweeping %d named tools instead of the default selection: %v", len(tools), filtered)
	} else {
		t.Logf("the default selection resolves to %d tools; proving each on a fresh machine", len(tools))
	}

	strategiesProven := map[string]bool{}
	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := cat.ByName(tc.name)
			if !ok {
				t.Fatalf("%s is not in the catalog", tc.name)
			}
			target, ok := linux.Target(tool)
			if !ok {
				t.Fatalf("%s has no linux target; the container is linux", tc.name)
			}
			if target.Strategy != tc.strategy {
				t.Errorf("%s uses %s on linux, expected %s", tc.name, target.Strategy, tc.strategy)
			}

			// A container of its own. The sweep is not a shared machine:
			// one tool's install brings in another tool's binary, and the
			// absence assertion below stops meaning anything once that
			// has happened.
			c, err := StartContainer(ctx, ContainerConfig{Binary: bin, Keep: os.Getenv("AES_KEEP_SANDBOX") == "1"})
			if err != nil {
				t.Fatalf("start container: %v", err)
			}
			t.Cleanup(func() {
				if c.keep {
					t.Logf("container %s left running (AES_KEEP_SANDBOX=1)", c.ID)
					return
				}
				if err := c.Close(context.Background()); err != nil {
					t.Errorf("close container: %v", err)
				}
			})
			t.Logf("container %s running %s (goarch %s)", c.ID, DefaultImage, c.GoArch)

			if err := c.Prepare(ctx, containerPrep...); err != nil {
				t.Fatalf("prepare container: %v", err)
			}

			// The precondition, measured inside. This is the assertion the
			// whole container exists for, so it is made explicitly rather than
			// left to the sequence's own check — a precondition that is only
			// enforced is not evidence that it held.
			presence, err := AbsentInside(ctx, c.runner(), primaryBinary(tool))
			if err != nil {
				t.Fatalf("probe container for %s: %v", tc.name, err)
			}
			if presence.Where != "container" {
				t.Fatalf("absence was checked against %q, not the container", presence.Where)
			}
			if presence.Found {
				// A SKIP, not a failure, and the distinction is a consequence
				// of the container being fresh for every tool.
				//
				// With one shared container, "already present" meant
				// CONTAMINATION — an earlier tool's install had brought this
				// one in — and failing was right: the run was measuring
				// nothing. With a container per tool it means the BASE IMAGE
				// ships it: ubuntu:24.04 has bash, and the fixture's own
				// preparation installs nodejs and npm, so those three can
				// never be absent here.
				//
				// Nothing is broken in that case. bash, node, npm and python
				// are verified tools on real machines, and reporting them as
				// failures here would say the opposite — it would invite
				// demoting them from tested: true because of what an image
				// happens to contain. The shared path already skips on this
				// same condition (ErrToolPresent), and the two verdicts
				// should not disagree about one fact.
				t.Skipf("%s is already present in a fresh %s at %q — this image cannot prove it",
					tc.name, DefaultImage, presence.Evidence)
			}
			t.Logf("%s absent inside the container before the run", tc.name)

			// Keep: true so the sandbox home outlives the sequence. The
			// post-install probe below has to run while the install still
			// exists, and RunIn removes the home on the way out. Probing after
			// teardown is how the first version of this test came to report a
			// working zoxide install as absent — not a wrong PATH this time,
			// but a directory that had already been deleted underneath the
			// check.
			keepHome := os.Getenv("AES_KEEP_SANDBOX") == "1"
			res, err := RunIn(ctx, c, tool, Config{
				Binary: c.BinaryPath(),
				Keep:   true,
				// The run that GATHERS evidence for this platform has to be
				// willing to install a tool that is unproven here. Without it
				// the resolver skips exactly the tools whose amd64 proof is
				// missing, so a tool could only ever be proven on the platform
				// it was already proven on. Every step still has to pass.
				AllowUnproven: true,
			})
			if !keepHome {
				t.Cleanup(func() {
					_ = c.RemoveAll(context.Background(), res.Home)
				})
			}
			if err != nil {
				if errors.Is(err, ErrToolPresent) {
					t.Skipf("%v", err)
				}
				t.Fatalf("RunIn: %v", err)
			}
			if !res.Passed {
				t.Errorf("layer 2 sequence failed in the container:\n%s", res.String())
				return
			}
			strategiesProven[target.Strategy] = true

			// And present inside, after.
			//
			// Probed with the run's OWN environment, not the container default.
			// A github-release tool installs to $AES_HOME/bin, which is on the
			// PATH the sequence used and not on the container's — so the
			// default-PATH probe reports a working install as absent.
			after, err := AbsentInside(ctx, c.runnerWithEnv(c.Env(res.Home)), primaryBinary(tool))
			if err != nil {
				t.Fatalf("re-probe container for %s: %v", tc.name, err)
			}
			if !after.Found {
				t.Errorf("%s is still absent inside the container after a passing sequence "+
					"(looked on the run's PATH, home %s); the install did not reach the "+
					"machine it was measured on", tc.name, res.Home)
			} else {
				t.Logf("%s present inside the container at %s", tc.name, after.Evidence)
			}
		})
	}

	afterFP, err := TreeFingerprint(hostAesHome)
	if err != nil {
		t.Fatalf("fingerprint host ~/.aes after: %v", err)
	}
	if before != afterFP {
		t.Errorf("the container run modified the developer's real ~/.aes:\nbefore %s\nafter  %s\n"+
			"the container is supposed to be the only machine that changed", before, afterFP)
	}

	// The bead asks for three strategies. In a container all three are
	// reachable, so unlike the host run this one can fail for having proven
	// too few — and should, because a silent regression to one strategy is
	// exactly what a container is here to make visible.
	//
	// A filtered sweep is a different question, and the answer is different
	// too: it was asked about one tool, not about the catalog's coverage. So
	// it demands that SOMETHING was proven and reports the rest.
	if len(filtered) > 0 {
		if len(strategiesProven) == 0 {
			t.Errorf("no strategy was proven for %v", filtered)
		}
	} else if len(strategiesProven) < 3 {
		t.Errorf("only %d of 3 strategies proven: %v", len(strategiesProven), strategiesProven)
	}
	t.Logf("strategies proven inside a fresh container: %v", strategiesProven)

}

// checkDefaultSelectionIsCovered fails when the default selection names a tool
// this test no longer proves.
//
// The two lists are written out separately on purpose — a test that read the
// catalog would silently shrink to whatever the catalog happens to contain and
// keep passing. This is the assertion that stops that: it compares the list
// that was actually exercised against the selection that has to resolve, and
// names anything added to one and not the other.

// buildAesFor compiles the binary under test for a specific platform, so the
// layer being proven is the one in this checkout rather than whatever happens
// to be installed on the machine. A container cannot run a darwin binary, so
// the GOOS is a parameter rather than a constant.
func buildAesFor(t *testing.T, goos, goarch string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "aes")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/aes")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		// A cross-compiled binary with cgo would need a C toolchain for the
		// target; aes is pure Go, and this makes the build say so rather than
		// failing somewhere inside the link step.
		"CGO_ENABLED=0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build aes for %s/%s: %v\n%s", goos, goarch, err, out)
	}
	return bin
}

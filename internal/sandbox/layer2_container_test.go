package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

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

// containerToolsFor is every tool the DEFAULT profile selects, read from the
// profile and the catalog rather than from a list maintained here.
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

	data, err := os.ReadFile(defaultProfilePath)
	if err != nil {
		t.Fatalf("read %s: %v", defaultProfilePath, err)
	}
	var prof struct {
		Include []string `yaml:"include"`
		Tags    []string `yaml:"tags"`
	}
	if err := yaml.Unmarshal(data, &prof); err != nil {
		t.Fatalf("parse %s: %v", defaultProfilePath, err)
	}
	if len(prof.Include) == 0 && len(prof.Tags) == 0 {
		t.Fatalf("%s selects nothing; this test would be vacuous", defaultProfilePath)
	}

	// The same union Resolve performs: names plus category plus tag.
	seen := map[string]bool{}
	for _, name := range prof.Include {
		seen[name] = true
	}
	for _, selector := range prof.Tags {
		for _, tool := range cat.ByCategory(selector) {
			seen[tool.Name] = true
		}
		for _, tool := range cat.ByTag(selector) {
			seen[tool.Name] = true
		}
	}

	out := make([]namedTool, 0, len(seen))
	for name := range seen {
		tool, ok := cat.ByName(name)
		if !ok {
			t.Errorf("the default profile selects %q, which is not in the catalog", name)
			continue
		}
		out = append(out, namedTool{name: name, strategy: strategyOf(tool)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) == 0 {
		t.Fatal("the profile selected no installable tool; nothing would be proven")
	}
	return out
}

// namedTool is one tool and the strategy its manifest declares for linux.
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

const defaultProfilePath = "../../profiles/default.yaml"

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

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
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
	linux := &platform.Host{OS: platform.OSLinux, Arch: c.GoArch, Manager: platform.ManagerApt}

	// Read from the profile, so a tool added to or removed from the catalog
	// changes what this proves without anyone editing the test.
	tools := containerToolsFor(t, cat)
	t.Logf("the default profile selects %d tools; proving each on a fresh machine", len(tools))

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
				t.Fatalf("%s is already present in a fresh %s at %q — the run would prove nothing",
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
	if len(strategiesProven) < 3 {
		t.Errorf("only %d of 3 strategies proven: %v", len(strategiesProven), strategiesProven)
	}
	t.Logf("strategies proven inside a fresh container: %v", strategiesProven)

}

// checkDefaultProfileIsCovered fails when the default profile names a tool this
// test no longer proves.
//
// The two lists are written out separately on purpose — a test that read the
// profile would silently shrink to whatever the profile happens to contain and
// keep passing. This is the assertion that stops that: it compares the list
// that was actually exercised against the profile that has to resolve, and
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

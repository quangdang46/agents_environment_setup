package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// containerTools spans the strategies a run can prove on a fresh Linux box.
//
// Three, per the bead, and they are the three that differ in kind rather than
// in spelling: an archive that is downloaded and checksum-verified, a package
// manager that needs root, and an ecosystem installer that needs its own
// toolchain. A fourth strategy would be the same argument again.
var containerTools = []struct {
	name     string
	strategy string
}{
	// github-release: a real archive, downloaded, checksum-verified, extracted.
	{"zoxide", manifest.StrategyGithubRelease},
	// package + apt: the privilege path, and the reason a container exists —
	// brew cannot run here and on a macOS host this strategy is unreachable.
	{"jq", manifest.StrategyPackage},
	// npm: an ecosystem installer, which writes to its own global prefix
	// rather than to $AES_HOME/bin.
	{"codex", manifest.StrategyNPM},
}

// containerPrep are the packages the fixture needs before any strategy can run.
//
// ca-certificates is not optional and its absence is a lie the harness tells: a
// bare ubuntu:24.04 has no CA bundle, so every https request fails with
// "certificate signed by unknown authority" and the error names the network
// rather than the missing package. nodejs/npm are the npm strategy's
// toolchain, which the Ecosystem installer refuses to go hunting for.
var containerPrep = []string{"ca-certificates", "git", "nodejs", "npm"}

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

	strategiesProven := map[string]bool{}
	for _, tc := range containerTools {
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

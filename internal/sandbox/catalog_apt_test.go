package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Bead 8tv. A declared floor is a claim about a number, and the only way to
// know whether a platform can supply it is to ask that platform.
//
// The failure this exists to prevent has already happened twice, in two
// different shapes:
//
//   - `node` declared min 20.0 against Ubuntu 24.04, which offers nodejs
//     18.19.1 and nothing else. The install succeeded, verification reported
//     stale forever, and every tool depending on node was uninstallable.
//   - `31f0fe5` shipped eighteen apt-backed tools whose package names exist in
//     no Debian index at all, because packages.debian.org answers HTTP 200 for
//     the page it renders to say the package does not exist. That check could
//     not fail, and its output was reported as verification.
//
// Both are the same error one layer apart: a check performed from a laptop,
// about a registry the laptop was guessing at. This one runs inside a
// container and asks apt.
//
// It is deliberately NOT behind AES_LAYER2. It installs nothing, so it can be
// run on a schedule or by hand without the full Layer 2 ceremony, and a check
// that is expensive to run is a check that stops being run.

const catalogCheckEnv = "AES_CATALOG_CHECK"

// TestAptCoordinatesMatchWhatAptOffers asks apt about every apt-backed tool in
// the catalog and fails if the answer disagrees with the manifest.
func TestAptCoordinatesMatchWhatAptOffers(t *testing.T) {
	if os.Getenv(catalogCheckEnv) != "1" {
		t.Skipf("asks apt about the real archive; set %s=1 to run it", catalogCheckEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if err := ContainerAvailable(ctx); err != nil {
		t.Skipf("no usable container runtime: %v", err)
	}

	c, err := catalog.Load(filepath.Join("..", "..", "tools"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	// The image index has to be current before apt will answer for anything.
	image := DefaultImage
	arch, err := ImageGoArch(ctx, image)
	if err != nil {
		t.Fatalf("read image architecture: %v", err)
	}
	box, err := StartContainer(ctx, ContainerConfig{Binary: mustBuildAes(t, "linux", arch)})
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() { _ = box.Close(context.Background()) })
	t.Logf("asking %s (goarch %s) about every apt coordinate", image, box.GoArch)

	if _, _, err := box.Sh(ctx, "apt-get update -qq", nil); err != nil {
		t.Fatalf("apt-get update: %v", err)
	}

	var missing, belowFloor []string
	checked := 0

	for _, tool := range c.All() {
		target, ok := hostFor(box.GoArch).Target(tool)
		if !ok || target.Strategy != manifest.StrategyPackage || target.Manager != manifest.ManagerApt {
			continue
		}
		checked++
		name, pkg := tool.Name, target.Package
		if strings.TrimSpace(pkg) == "" {
			missing = append(missing, fmt.Sprintf("%s: apt target names no package", name))
			continue
		}

		candidate, err := aptCandidate(ctx, box, pkg)
		if err != nil {
			t.Errorf("asking apt about %s (%s): %v", name, pkg, err)
			continue
		}
		if candidate == "" {
			// Two different failures that look alike in a manifest. The
			// 31f0fe5 one is a name that resolves nowhere. The one found here is
			// a package that EXISTS and still cannot be installed, because it
			// carries no candidate — typically a transitional package whose
			// contents moved. `apt-get install -y` fails on both, and the error
			// names neither cause, so the manifest looks well-formed either way.
			missing = append(missing, fmt.Sprintf(
				"%s: %q is not installable on %s — %s", name, pkg, DefaultImage, aptWhy(ctx, box, pkg)))
			continue
		}

		// The 8tv failure: a floor above what the platform can supply. This
		// compares the number apt WILL install against the number the manifest
		// demands, which is the only comparison that means anything.
		min := ""
		if tool.Verify != nil && tool.Verify.Version != nil {
			min = tool.Verify.Version.Min
		}
		if min == "" {
			continue
		}
		if compareDotted(candidate, min) < 0 {
			belowFloor = append(belowFloor, fmt.Sprintf(
				"%s: apt offers %s (%s) but the manifest requires %s — install succeeds and then reports stale forever",
				name, candidate, pkg, min))
		}
	}

	if checked == 0 {
		t.Fatal("no apt-backed tools were checked; the assertion would be vacuous")
	}
	t.Logf("asked apt about %d apt-backed tools", checked)

	for _, m := range missing {
		t.Error("apt coordinate does not exist: " + m)
	}
	for _, m := range belowFloor {
		t.Error("declared floor exceeds what the platform offers: " + m)
	}
}

// hostFor builds the host a linux container is, so Target() resolves the same
// way it will inside. Reading runtime.GOOS here would pick darwin and every
// apt target would silently miss.
func hostFor(goarch string) *platform.Host {
	return &platform.Host{OS: platform.OSLinux, Arch: goarch, Manager: platform.ManagerApt}
}

func mustBuildAes(t *testing.T, goos, goarch string) string {
	t.Helper()
	return buildAesFor(t, goos, goarch)
}

// aptCandidate asks apt for the version it would install, and returns "" when
// the package does not exist.
//
// `apt-cache policy` is the right question rather than a search index: it
// answers about the configured archive and the machine's pinning, which is
// what would actually be installed. A 200 from a package website proves
// nothing about either.
func aptCandidate(ctx context.Context, box *Container, pkg string) (string, error) {
	out, _, err := box.Sh(ctx, fmt.Sprintf("apt-cache policy %s 2>/dev/null", shellQuote(pkg)), nil)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Candidate:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
			// A package with no installation candidate prints
			// "Candidate: (none)", which is a different answer from a version.
			if v == "" || v == "(none)" {
				return "", nil
			}
			return v, nil
		}
	}
	return "", nil
}

// aptWhy asks apt to say why a package has no candidate, so the failure names a
// cause rather than a symptom.
func aptWhy(ctx context.Context, box *Container, pkg string) string {
	out, _, err := box.Sh(ctx, fmt.Sprintf("apt-get install -s %s 2>&1 | tail -2", shellQuote(pkg)), nil)
	if err != nil {
		return "apt could not be asked"
	}
	return strings.TrimSpace(strings.ReplaceAll(out, "\n", "; "))
}

// compareDotted compares dotted-integer versions, matching the verifier's rule:
// missing components are zero, so 14.1 == 14.1.0.
//
// A DEBIAN EPOCH is stripped first. apt writes a version as
// `[epoch:]upstream-revision`, so git is `1:2.43.0-1ubuntu7.3` — and reading
// the leading digits naively makes that 1.43.0, which is below a 2.30 floor
// and reports a healthy package as unusable. The epoch is a packaging
// mechanism for downgrades and says nothing about the software's version.
func compareDotted(got, want string) int {
	got = stripEpoch(got)
	want = stripEpoch(want)
	g := leadingDotted(got)
	w := leadingDotted(want)
	gs := strings.Split(g, ".")
	ws := strings.Split(w, ".")
	for i := 0; i < len(gs) || i < len(ws); i++ {
		var gi, wi int
		if i < len(gs) {
			fmt.Sscanf(gs[i], "%d", &gi)
		}
		if i < len(ws) {
			fmt.Sscanf(ws[i], "%d", &wi)
		}
		if gi != wi {
			if gi < wi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// stripEpoch removes a leading `N:` from a Debian version. Only a colon that
// appears before any digit-boundary of the upstream version counts, and in
// practice a colon never appears anywhere else in an apt version string.
func stripEpoch(v string) string {
	if i := strings.Index(v, ":"); i > 0 {
		if _, err := strconv.Atoi(v[:i]); err == nil {
			return v[i+1:]
		}
	}
	return v
}

func leadingDotted(v string) string {
	var out []string
	for _, part := range strings.Split(v, ".") {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		if len(out) > 0 && n == 0 && part == "" {
			break
		}
		out = append(out, fmt.Sprint(n))
		if len(out) == 4 {
			break
		}
	}
	if len(out) == 0 {
		return "0"
	}
	return strings.Join(out, ".")
}

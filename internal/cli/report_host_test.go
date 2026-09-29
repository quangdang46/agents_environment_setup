package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// report() is the one status function behind `aes list`, `aes verify` and
// `aes doctor`, and it must ask the verifier about the machine the run is on.
//
// It did not. It built Target{AESHome: ...} with no Host, so a tool declaring
// `min: {darwin: 26.0, linux: 18.0}` — which has no scalar default — resolved
// its floor to "" and was reported ok on presence alone. Measured: a node 16
// binary on PATH passed `aes verify node` and exited 0, while
// `aes setup --only node` called the same binary stale and reinstalled it. Two
// commands, one binary, two answers.
func TestReportAppliesTheHostsFloor(t *testing.T) {
	// Not parallel: it sets PATH for the process.
	linux := &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64, Manager: platform.ManagerApt}

	tool := &manifest.Tool{
		Name: "node",
		Verify: &manifest.Verify{
			Command: "node",
			Version: &manifest.VersionCheck{
				Command: "node --version",
				// The map form, with no scalar default: this is the shape that
				// makes the Host load-bearing. A scalar floor would be applied
				// either way and the test would pass for the wrong reason.
				Min: manifest.Floor{ByOS: map[string]string{"linux": "18.0", "darwin": "26.0"}},
			},
		},
	}

	h := newLifeHarness(t)
	h.app.Home = t.TempDir()

	// Seeded into $AES_HOME/bin, which is where aes installs and which the
	// verifier prepends to the probe's PATH. Seeding it into some other
	// directory on PATH would leave the version probe unable to run — and a
	// probe that cannot run reports StatusUnknown, a third answer that hides
	// which of the two paths was taken.
	bin := filepath.Join(h.app.Home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\necho v16.20.2\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := report(tool, nil, verifier.Target{Host: linux, AESHome: h.app.Home})
	if got.Status != string(verifier.StatusStale) {
		t.Errorf("aes list/verify/doctor reported node 16 as %q, want stale; "+
			"the per-platform floor was dropped, so presence alone counted as ok", got.Status)
	}
}

// The construction itself, asserted separately from report().
//
// The test above calls report() directly, so it would still pass if rc.target()
// dropped the Host — and that is exactly what happened: the first version of
// this fix was caught by a mutation that removed the Host from target() and
// left the whole suite green. A test that only exercises the consumer cannot
// see a producer that stops producing.
func TestRunContextTargetCarriesTheHost(t *testing.T) {
	linux := &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64, Manager: platform.ManagerApt}
	h := newLifeHarness(t)
	h.app.Home = t.TempDir()

	rc := &runContext{App: h.app, Host: linux}
	got := rc.target()

	if got.Host == nil {
		t.Fatal("rc.target() built a Target with no Host; a tool with a per-platform " +
			"floor and no scalar default will be reported ok on presence alone")
	}
	if got.Host.OS != platform.OSLinux {
		t.Errorf("Host.OS = %q, want linux", got.Host.OS)
	}
	if got.AESHome != h.app.Home {
		t.Errorf("AESHome = %q, want the App's own %q", got.AESHome, h.app.Home)
	}
}

// The other half: with no host the same tool is reported ok. That is the
// documented behaviour for a caller that has no platform, and it is why the
// Host is a field rather than something the verifier invents — but it means a
// caller that forgets to pass one gets a floor-free answer, which is the bug
// this file exists to prevent.
func TestReportWithoutAHostSkipsTheFloor(t *testing.T) {
	// Not parallel: it sets PATH for the process.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\necho v16.20.2\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("PATH", dir)

	h := newLifeHarness(t)
	h.app.Home = t.TempDir()

	tool := &manifest.Tool{
		Name: "node",
		Verify: &manifest.Verify{
			Command: "node",
			Version: &manifest.VersionCheck{
				Command: "node --version",
				Min:     manifest.Floor{ByOS: map[string]string{"linux": "18.0"}},
			},
		},
	}

	if got := report(tool, nil, verifier.Target{AESHome: h.app.Home}); got.Status != string(verifier.StatusOK) {
		t.Errorf("with no host the floor is skipped and presence is reported as %q, want ok", got.Status)
	}
}

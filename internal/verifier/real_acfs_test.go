package verifier

import (
	"os/exec"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// The ACFS ecosystem tools print their versions in ways worth pinning, because
// two of them put something that looks like a version before the version.
//
//	ubs   -> "UBS Meta-Runner v5.3.2 (git 28b9b37)"
//	casr  -> "casr 0.3.1 (929c5e7 2026-09-06T16:06:53.264214000Z aarch64-apple)"
//
// The second one is the interesting case: the output carries a date with
// dots in it, and a parser that grabbed the last number-like run rather than
// the first would report a year. "First dotted run on the first line" is the
// rule, and this is the reason for it.
//
// Skipped per tool when it is not installed, so the suite still runs on a
// machine that has none of them.
func TestRealACFSBinariesReportAVersion(t *testing.T) {
	for _, name := range []string{"ubs", "ms", "pt", "cm", "sbh", "casr", "pi", "aadc"} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(name); err != nil {
				t.Skipf("%s is not installed on this machine", name)
			}
			tool := &manifest.Tool{
				Name: name,
				Verify: &manifest.Verify{
					Command: name,
					Version: &manifest.VersionCheck{Command: name + " --version", Min: "0.1"},
				},
			}
			got := Verify(tool)
			if got.Status != StatusOK {
				t.Errorf("status = %q, want %q", got.Status, StatusOK)
			}
			if got.Version == "" {
				t.Fatalf("no version extracted from the real output of %s --version", name)
			}
			t.Logf("%-6s -> %q", name, got.Version)
		})
	}
}

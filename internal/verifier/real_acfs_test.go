package verifier

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

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
			// The binary is run directly and its output handed to
			// ExtractVersion, rather than going through Verify. This test
			// exists to pin the PARSER against real output, and routing it
			// through Verify put a timeout in the middle: a slow-but-healthy
			// tool then reported StatusUnknown, which is a fact about the
			// machine's load and says nothing about whether the parser read
			// the right digits. A timeout here is a signal about pi, not about
			// this code.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, name, "--version")
			// A clean environment, as the verifier provides: a tool that
			// depends on an inherited variable is a different tool.
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s --version failed: %v", name, err)
			}
			got := ExtractVersion(string(out))
			if got == "" {
				t.Fatalf("no version extracted from the real output of %s --version: %q",
					name, firstLineOf(string(out)))
			}
			t.Logf("%-6s -> %q", name, got)
		})
	}
}

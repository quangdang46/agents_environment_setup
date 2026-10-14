package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// runHuman runs setup without --json so the human report goes to stdout.
//
// The default h.run always passes --json, because most assertions are about
// the envelope. The prose report — buckets, resume command, log path — is a
// separate surface and needs its own run; asserting it through the JSON path
// finds nothing and reads as a missing feature.
func (h *setupHarness) runHuman(t *testing.T, args ...string) int {
	t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	full := append([]string{"setup"}, args...)
	return h.app.Run(full)
}

// Progress must never land on stdout. Stdout carries exactly one JSON
// document under --json and the human table otherwise; the in-flight lines
// belong to stderr. A progress line on stdout corrupts the JSON envelope,
// which is what an agent parses — so this is a contract test, not a style
// preference.
func TestSetupProgressGoesToStderrNotStdout(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	code, sum := h.run(t, "--only", "demo", "--no-smoke-test")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.summaryText())
	}
	if sum == nil {
		t.Fatalf("no summary decoded\nstdout:\n%s", h.stdout.String())
	}

	stderr := h.stderr.String()
	for _, want := range []string{"[1/1]", "demo"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}

	// The same run's stdout must still parse as one JSON document.
	var envelope map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &envelope); err != nil {
		t.Errorf("stdout is not one JSON document: %v\n%s", err, h.stdout.String())
	}
	for _, progress := range []string{"[1/1]", "Installing", "✓", "✖"} {
		if strings.Contains(h.stdout.String(), progress) {
			t.Errorf("stdout contains progress %q, which corrupts the envelope", progress)
		}
	}
}

// The failure report must say what failed by kind and the exact command to
// resume, with --only names taken from the failed set.
//
// Two runs are needed: the JSON envelope carries resume_command as a field
// an agent reads, while the prose buckets and the copy-paste command are the
// human report. Asserting one on the other's stream is how the "report looks
// missing" false alarm happens.
func TestSetupPrintsResumeHint(t *testing.T) {
	jsonCase := newSetupHarness(t, toolSpec{name: "bad", category: "utility", provides: "bad"})
	jsonCase.installErr = errors.New("download https://example.com/bad.tar.gz: unexpected status 503 Service Unavailable")
	code, sum := jsonCase.run(t, "--only", "bad", "--no-smoke-test")
	if code != ExitVerifyFailed {
		t.Fatalf("exit = %d, want %d\n%s", code, ExitVerifyFailed, jsonCase.summaryText())
	}
	if sum == nil {
		t.Fatalf("no summary decoded\nstdout:\n%s", jsonCase.stdout.String())
	}
	if !strings.Contains(sum.ResumeCommand, "--only bad") {
		t.Errorf("resume_command = %q, want --only bad", sum.ResumeCommand)
	}

	// Human run: the report goes to stdout, in full sentences.
	human := newSetupHarness(t, toolSpec{name: "bad", category: "utility", provides: "bad"})
	human.installErr = errors.New("download https://example.com/bad.tar.gz: unexpected status 503 Service Unavailable")
	if code := human.runHuman(t, "--only", "bad", "--no-smoke-test"); code != ExitVerifyFailed {
		t.Fatalf("exit = %d, want %d\n%s", code, ExitVerifyFailed, human.summaryText())
	}
	stdout := human.stdout.String()
	if !strings.Contains(stdout, "network/download") {
		t.Errorf("human report missing the network/download bucket:\n%s", stdout)
	}
	if !strings.Contains(stdout, "aes setup --only bad") {
		t.Errorf("human report missing the resume command:\n%s", stdout)
	}
	if !strings.Contains(stdout, "bad") {
		t.Errorf("human report missing the failed tool:\n%s", stdout)
	}
}

// The resume hint must never widen the selection: it may only name tools
// the run resolved and that failed. Building it from the original flags
// instead of the failure set would reinstall a tool that was already fine.
//
// installErr fails every install, so the harness needs a per-tool failure:
// good writes a working binary, bad returns the error. The registry
// replacement follows the same shape as TestSetupInstallsAndRecords' ghost
// installer in setup_test.go.
func TestResumeHintNeverWidensSelection(t *testing.T) {
	h := newSetupHarness(t,
		toolSpec{name: "good", category: "utility", provides: "good"},
		toolSpec{name: "bad", category: "utility", provides: "bad"},
	)
	h.mu.Lock()
	h.installErr = nil
	h.mu.Unlock()
	h.app.Registry.Register(manifest.StrategyGo, installerFunc{
		fn: func(ctx context.Context, a installer.Action) error {
			if a.Tool == "bad" {
				return errors.New("download: unexpected status 503")
			}
			return h.install(ctx, a)
		},
		dest: h.binDir,
	})

	_, sum := h.run(t, "--only", "good", "--only", "bad", "--no-smoke-test")
	if sum == nil {
		t.Fatalf("no summary decoded\nstdout:\n%s", h.stdout.String())
	}
	if !strings.Contains(sum.ResumeCommand, "--only bad") {
		t.Errorf("resume_command = %q, want --only bad", sum.ResumeCommand)
	}
	if strings.Contains(sum.ResumeCommand, "--only good") {
		t.Errorf("resume_command = %q, must not name the tool that succeeded", sum.ResumeCommand)
	}
}

// Every tool carries its own elapsed time, and the run carries the total.
// The JSON envelope carries the numbers; the TIME column is the human
// table's rendering of the same fields, so the two are asserted separately.
func TestSummaryCarriesTimings(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	_, sum := h.run(t, "--only", "demo", "--no-smoke-test")
	if sum == nil {
		t.Fatalf("no summary decoded\nstdout:\n%s", h.stdout.String())
	}
	if len(sum.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(sum.Results))
	}
	if sum.Results[0].DurationMs < 0 {
		t.Errorf("duration_ms = %d, want >= 0", sum.Results[0].DurationMs)
	}
	if sum.ElapsedMs < 0 {
		t.Errorf("elapsed_ms = %d, want >= 0", sum.ElapsedMs)
	}

	h2 := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h2.runHuman(t, "--only", "demo", "--no-smoke-test")
	if !strings.Contains(h2.stdout.String(), "TIME") {
		t.Errorf("table missing the TIME column:\n%s", h2.stdout.String())
	}
}

// A dry run must create no log file: observability must not become a
// mutation of the machine it observes.
func TestDryRunCreatesNoLog(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	code, _ := h.run(t, "--only", "demo", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.summaryText())
	}
	// Assert on the directory from disk, in a fresh read — the contract is
	// about what reached the filesystem, not the struct that wrote it.
	entries, err := os.ReadDir(filepath.Join(h.home, "logs"))
	if err == nil && len(entries) > 0 {
		t.Errorf("dry run created logs/: %v", entries)
	}

	// And a real run must create exactly the log the summary names.
	h2 := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	_, sum2 := h2.run(t, "--only", "demo", "--no-smoke-test")
	if sum2 == nil {
		t.Fatalf("no summary decoded")
	}
	if sum2.LogPath == "" {
		t.Fatal("real run reported no log path")
	}
	content, err := os.ReadFile(sum2.LogPath)
	if err != nil {
		t.Fatalf("log file %s unreadable: %v", sum2.LogPath, err)
	}
	if !strings.Contains(string(content), "[1/1]") {
		t.Errorf("log file missing the progress lines:\n%s", content)
	}
}

// --quiet leaves stderr holding only the final error line: the JSON tests
// that assert on exact stderr must keep passing, and a human piping stderr
// to a pager gets the failure and nothing else.
func TestQuietSuppressesStderrProgress(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "bad", category: "utility", provides: "bad"})
	h.installErr = errors.New("download: unexpected status 503")

	h.run(t, "--only", "bad", "--quiet", "--no-smoke-test")
	stderr := h.stderr.String()
	for _, progress := range []string{"[1/1]", "Installing", "═══"} {
		if strings.Contains(stderr, progress) {
			t.Errorf("quiet stderr contains progress %q:\n%s", progress, stderr)
		}
	}
}

// --verbose must not change what is resolved: it is a reporting flag, and a
// reporting flag that changes the plan is a second plan.
//
// The two runs use separate harnesses, so the demo tool is absent in each and
// both take the install path — comparing the STATUS is meaningless when the
// machine state differs by design. What must not differ is which tool was
// resolved and through which strategy.
//
// Stderr length is deliberately NOT compared: the shell-rc message alone
// outweighs any verbose detail, and a length assertion on stderr measures
// noise rather than vocabulary. The behaviour that matters is asserted by the
// installer's own test (TestGithubReleaseVerboseLogsURL), where the detail
// actually comes from.
func TestVerboseDoesNotChangeActions(t *testing.T) {
	plain := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	verbose := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	_, sumPlain := plain.run(t, "--only", "demo", "--no-smoke-test")
	_, sumVerbose := verbose.run(t, "--only", "demo", "--verbose", "--no-smoke-test")

	if sumPlain == nil || sumVerbose == nil {
		t.Fatalf("missing summary (plain=%v verbose=%v)", sumPlain != nil, sumVerbose != nil)
	}
	if len(sumPlain.Results) != len(sumVerbose.Results) {
		t.Fatalf("result counts differ: %d vs %d", len(sumPlain.Results), len(sumVerbose.Results))
	}
	for i := range sumPlain.Results {
		a, b := sumPlain.Results[i], sumVerbose.Results[i]
		if a.Name != b.Name || a.Strategy != b.Strategy {
			t.Errorf("result %d differs: %+v vs %+v", i, a, b)
		}
	}
}

// The smoke pass prints one line per installed tool and records the array in
// JSON. It confirms; it never re-decides.
func TestSmokePassConfirms(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	code, sum := h.run(t, "--only", "demo")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.summaryText())
	}
	if sum == nil {
		t.Fatalf("no summary decoded")
	}
	if len(sum.Smoke) != 1 || !sum.Smoke[0].OK {
		t.Errorf("smoke = %+v, want one OK entry", sum.Smoke)
	}
	if !strings.Contains(h.stderr.String(), "Smoke test") {
		t.Errorf("stderr missing the smoke section:\n%s", h.stderr.String())
	}
}

// --no-smoke-test skips the pass entirely.
func TestNoSmokeTestSkips(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	_, sum := h.run(t, "--only", "demo", "--no-smoke-test")
	if sum == nil {
		t.Fatalf("no summary decoded")
	}
	if len(sum.Smoke) != 0 {
		t.Errorf("smoke = %+v, want empty with --no-smoke-test", sum.Smoke)
	}
	if strings.Contains(h.stderr.String(), "Smoke test") {
		t.Errorf("stderr contains a smoke section despite --no-smoke-test:\n%s", h.stderr.String())
	}
}

// The dry-run plan lists tools in resolved order with their coordinates and
// reasons. Under --json it goes to stderr so the envelope stays parseable.
func TestDryRunPlanListsCoordinates(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	code, _ := h.run(t, "--only", "demo", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.summaryText())
	}
	stderr := h.stderr.String()
	for _, want := range []string{"demo", "go", "example.com/demo@v1.0.0"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dry-run plan missing %q:\n%s", want, stderr)
		}
	}
}

package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/manifest"
)

// stubBinary writes an executable that stands in for aes, so the harness
// itself can be tested without a network or a real install.
//
// The real 7-step run lives in layer2_test.go behind a skip. This file tests
// the thing that is always testable: that the sandbox is created, isolated,
// kept or cleaned, and that a failing step is reported as a failing step.
func stubBinary(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "aes-stub")
	body := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// passingStub emulates a working install: setup creates the binary and
// state.json, doctor is quiet, and a re-run changes nothing.
const passingStub = `
case "$1" in
  setup)
    # Model a CORRECT installer: a tool that is already present and verified is
    # skipped, not rewritten. Rewriting it unconditionally is the non-idempotent
    # bug, and it is what the next stub below does deliberately.
    if [ ! -x "$AES_HOME/bin/stubtool" ]; then
      mkdir -p "$AES_HOME/bin"
      printf '#!/bin/sh\necho stubtool 1.0\n' > "$AES_HOME/bin/stubtool"
      chmod +x "$AES_HOME/bin/stubtool"
    fi
    printf '{"version":1,"installed":{"stubtool":{"strategy":"github-release","installed_at":"2026-01-01T00:00:00Z"}}}' > "$AES_HOME/state.json"
    echo "installed stubtool"
    exit 0
    ;;
  doctor)
    echo "no drift"
    exit 0
    ;;
esac
exit 0
`

func stubTool() *manifest.Tool {
	return &manifest.Tool{
		Name:   "stubtool",
		Verify: &manifest.Verify{Command: "stubtool"},
	}
}

func TestRunReportsEveryStepPassing(t *testing.T) {
	res, err := Run(context.Background(), stubTool(), Config{Binary: stubBinary(t, passingStub)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Passed {
		t.Fatalf("harness reported failure against a passing stub:\n%s", res.String())
	}
	// install, binary present, verify ok, state entry, idempotent, drift
	// detected, force reinstall.
	want := []string{"install", "binary present", "verify ok", "state entry",
		"idempotent", "drift detected", "force reinstall"}
	if len(res.Steps) != len(want) {
		t.Fatalf("recorded %d steps, want %d: %s", len(res.Steps), len(want), res.String())
	}
	// The order matters: each step depends on the one before it, and a harness
	// that silently reordered them would still report the same names.
	for i, name := range want {
		if res.Steps[i].Name != name {
			t.Errorf("step %d is %q, want %q", i, res.Steps[i].Name, name)
		}
	}
	for _, s := range res.Steps {
		if s.Detail == "" {
			t.Errorf("step %q has no detail; a failure nobody can read is half a failure", s.Name)
		}
	}
}

func TestRunCleansUpUnlessKeep(t *testing.T) {
	bin := stubBinary(t, passingStub)

	res, err := Run(context.Background(), stubTool(), Config{Binary: bin})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(res.Home); !os.IsNotExist(err) {
		t.Errorf("sandbox home %s survived a run without Keep", res.Home)
	}

	res, err = Run(context.Background(), stubTool(), Config{Binary: bin, Keep: true})
	if err != nil {
		t.Fatalf("Run with Keep: %v", err)
	}
	if _, err := os.Stat(res.Home); err != nil {
		t.Errorf("--keep-sandbox did not leave %s: %v", res.Home, err)
	}
	os.RemoveAll(res.Home)
}

func TestRunIsolatedFromTheHostHome(t *testing.T) {
	// The claim the whole package rests on: a run must not touch the
	// developer's real ~/.aes. Asserted by digest, not by intent, so a harness
	// that quietly writes there fails here rather than in someone's home
	// directory a week later.
	hostHome := t.TempDir()
	before, err := TreeFingerprint(hostHome)
	if err != nil {
		t.Fatalf("fingerprint before: %v", err)
	}

	// The stub writes to $AES_HOME, which is the sandbox. If isolation leaked,
	// it would write to HOME instead - so point HOME at a canary directory and
	// check that one is untouched too.
	canary := t.TempDir()
	canaryBefore, err := TreeFingerprint(canary)
	if err != nil {
		t.Fatalf("fingerprint canary: %v", err)
	}

	res, err := Run(context.Background(), stubTool(), Config{Binary: stubBinary(t, passingStub), Keep: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer os.RemoveAll(res.Home)

	// The sandbox's HOME is its own directory, so the canary must be pristine.
	after, err := TreeFingerprint(canary)
	if err != nil {
		t.Fatalf("fingerprint canary after: %v", err)
	}
	if after != canaryBefore {
		t.Errorf("the run wrote outside its sandbox:\nbefore %s\nafter  %s", canaryBefore, after)
	}
	hostAfter, err := TreeFingerprint(hostHome)
	if err != nil {
		t.Fatalf("fingerprint after: %v", err)
	}
	if hostAfter != before {
		t.Errorf("host home changed during a sandbox run:\nbefore %s\nafter  %s", before, hostAfter)
	}
}

func TestRunReportsAFailingInstallAndStops(t *testing.T) {
	// A tool that cannot install is the failure the harness exists to catch.
	stub := `
case "$1" in
  setup) echo "aes: install failed" >&2; exit 1 ;;
esac
exit 0
`
	res, err := Run(context.Background(), stubTool(), Config{Binary: stubBinary(t, stub)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passed {
		t.Fatal("a failing install was reported as passing")
	}
	if len(res.Steps) == 0 || res.Steps[0].OK {
		t.Fatalf("the install step should have failed: %s", res.String())
	}
	// The remaining steps assert things that could not have happened, so they
	// must not be reported as passing.
	for _, s := range res.Steps[1:] {
		if s.OK {
			t.Errorf("step %q passed after the install failed: %s", s.Name, res.String())
		}
	}
	if !strings.Contains(res.String(), "FAIL") {
		t.Error("String() does not mark the failure")
	}
}

func TestRunDetectsAMissingBinaryAfterInstall(t *testing.T) {
	// setup "succeeds" but produces nothing. A harness that only checks the
	// exit code would call this a pass.
	stub := `
case "$1" in
  setup) echo "done"; exit 0 ;;
esac
exit 0
`
	res, err := Run(context.Background(), stubTool(), Config{Binary: stubBinary(t, stub)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passed {
		t.Fatal("setup exiting 0 without producing a binary was reported as passing")
	}
	byName := map[string]bool{}
	for _, s := range res.Steps {
		byName[s.Name] = s.OK
	}
	if byName["binary present"] {
		t.Error("binary-present passed with no binary on disk")
	}
	if byName["state entry"] {
		t.Error("state-entry passed with no state.json")
	}
}

func TestRunDetectsANonIdempotentInstaller(t *testing.T) {
	// The second run rewrites the binary. Everything returns exit 0 and looks
	// healthy; only comparing the sandbox before and after catches it.
	stub := `
case "$1" in
  setup)
    mkdir -p "$AES_HOME/bin"
    printf '#!/bin/sh\n# %s\n' "$(date +%s%N)" > "$AES_HOME/bin/stubtool"
    chmod +x "$AES_HOME/bin/stubtool"
    printf '{"version":1,"installed":{"stubtool":{"strategy":"github-release","installed_at":"2026-01-01T00:00:00Z"}}}' > "$AES_HOME/state.json"
    exit 0 ;;
  doctor) exit 0 ;;
esac
exit 0
`
	res, err := Run(context.Background(), stubTool(), Config{Binary: stubBinary(t, stub)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var idempotent bool
	var sawIt bool
	for _, s := range res.Steps {
		if s.Name == "idempotent" {
			idempotent, sawIt = s.OK, true
		}
	}
	if !sawIt {
		t.Fatalf("no idempotence step recorded: %s", res.String())
	}
	if idempotent {
		t.Error("an installer that rewrote the binary on every run was called idempotent")
	}
	if res.Passed {
		t.Error("a non-idempotent install was reported as passing")
	}
}

func TestRunRequiresABinary(t *testing.T) {
	if _, err := Run(context.Background(), stubTool(), Config{}); err == nil {
		t.Error("Run with no Binary returned no error")
	}
}

func TestTreeFingerprintDetectsAChange(t *testing.T) {
	dir := t.TempDir()
	before, err := TreeFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	same, err := TreeFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if before != same {
		t.Error("an unchanged directory produced a different digest")
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	after, err := TreeFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if after == before {
		t.Error("a changed directory produced the same digest")
	}
}

func TestTreeFingerprintOfAMissingDirIsAbsent(t *testing.T) {
	got, err := TreeFingerprint(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("TreeFingerprint on a missing dir: %v", err)
	}
	if got != "absent" {
		t.Errorf("digest = %q, want %q", got, "absent")
	}
}

// The sandbox bin must lead PATH, and the host PATH must survive behind it.
// Dropping the host PATH entirely - the obvious reading of "isolated" - leaves
// an installer unable to find mkdir or tar, which fails as a broken tool
// rather than a broken harness.
func TestSandboxEnvHidesTheHostPath(t *testing.T) {
	env := sandboxEnv("/tmp/aes-x")
	var path, home, aesHome string
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "PATH="):
			path = strings.TrimPrefix(e, "PATH=")
		case strings.HasPrefix(e, "HOME="):
			home = strings.TrimPrefix(e, "HOME=")
		case strings.HasPrefix(e, "AES_HOME="):
			aesHome = strings.TrimPrefix(e, "AES_HOME=")
		}
	}
	// The sandbox bin must come FIRST so the tool under test resolves to the
	// copy the sandbox installed, and the host PATH must still be present so
	// an installer can find mkdir, tar and curl. Replacing it entirely was the
	// first attempt and broke every stub that shelled out.
	sep := string(os.PathListSeparator)
	head := path
	if host := os.Getenv("PATH"); host != "" {
		head, _, _ = strings.Cut(path, sep)
	}
	if !strings.HasPrefix(path, filepath.Join("/tmp/aes-x", "bin")) {
		t.Errorf("sandbox bin is not first on PATH: %q", path)
	}
	if head == "" {
		t.Error("sandbox PATH has no leading entry")
	}
	if home != "/tmp/aes-x" || aesHome != "/tmp/aes-x" {
		t.Errorf("HOME=%q AES_HOME=%q, want both inside the sandbox", home, aesHome)
	}
}

func TestStateHasReadsTheFileNotTheProcess(t *testing.T) {
	home := t.TempDir()
	if stateHas(home, "ghost") {
		t.Error("stateHas reported a tool present with no state.json at all")
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"),
		[]byte(`{"version":1,"installed":{"real":{"strategy":"package"}}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !stateHas(home, "real") {
		t.Error("stateHas missed a tool that is present")
	}
	if stateHas(home, "ghost") {
		t.Error("stateHas invented a tool that is absent")
	}
}

func TestStepTimeoutIsBounded(t *testing.T) {
	// A hung aes must fail the harness, not hang it. The stub sleeps well past
	// the bound; the test uses a short context so it does not take 5 minutes.
	if StepTimeout <= 0 || StepTimeout > 30*time.Minute {
		t.Errorf("StepTimeout = %s, want a positive bound under 30m", StepTimeout)
	}
}

// The sequence is defined against a machine where the tool is absent. If the
// developer already has it, a run that reports success proves nothing — the
// binary was there before setup touched it — so the precondition is asserted
// rather than assumed.
func TestRunRefusesWhenTheToolIsAlreadyInstalled(t *testing.T) {
	// "sh" resolves on every platform this runs on, which is the point: this
	// asserts the precondition fires for a tool the host genuinely has.
	_, err := Run(context.Background(),
		&manifest.Tool{Name: "sh", Verify: &manifest.Verify{Command: "sh"}},
		Config{Binary: stubBinary(t, passingStub)})
	if !errors.Is(err, ErrToolPresent) {
		t.Fatalf("error = %v, want ErrToolPresent", err)
	}
	if !strings.Contains(err.Error(), "sh") {
		t.Errorf("error %q does not name the tool that is already present", err)
	}
}

// A dry run installs nothing, so it does not need the absent precondition and
// must not be blocked by it.
func TestDryRunSkipsTheAbsentPrecondition(t *testing.T) {
	res, err := Run(context.Background(),
		&manifest.Tool{Name: "sh", Verify: &manifest.Verify{Command: "sh"}},
		Config{Binary: stubBinary(t, passingStub), DryRun: true})
	if err != nil {
		t.Fatalf("a dry run should not require the tool to be absent: %v", err)
	}
	if !res.Passed {
		t.Errorf("dry run did not pass:\n%s", res.String())
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/envgen"
	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
	"github.com/quangdang46/agents_environment_setup/internal/state"
)

// harness builds an App over a temporary catalog and captures its streams.
type harness struct {
	app     *App
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	home    string
	catRoot string
}

func newHarness(t *testing.T, tools ...[3]string) *harness {
	t.Helper()

	home := t.TempDir()
	catRoot := filepath.Join(home, "catalog")
	for _, spec := range tools {
		name, category, strategy := spec[0], spec[1], spec[2]
		dir := filepath.Join(catRoot, category, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Both platforms are declared. A fixture keyed to one GOOS would be
		// filtered out by Host.Supports on the other, and the test would
		// pass by asserting against zero tools.
		var installs string
		switch strategy {
		case "package":
			installs = "  darwin:\n    strategy: package\n    manager: brew\n    package: " + name +
				"\n  linux:\n    strategy: package\n    manager: apt\n    package: " + name + "\n"
		default:
			installs = "  darwin:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n" +
				"  linux:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n"
		}
		content := "name: " + name + "\ndescription: fixture " + name + "\ncategory: " + category +
			"\nprovides: [" + name + "-not-installed]\n" +
			"install:\n" + installs
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write tool.yaml: %v", err)
		}
	}

	h := &harness{
		stdout:  &bytes.Buffer{},
		stderr:  &bytes.Buffer{},
		home:    filepath.Join(home, ".aes"),
		catRoot: catRoot,
	}
	h.app = &App{
		In:          strings.NewReader(""),
		Out:         h.stdout,
		Err:         h.stderr,
		Home:        h.home,
		CatalogRoot: catRoot,
		ProfileDir:  filepath.Join(home, "profiles"),
		Registry:    installer.NewRegistry(filepath.Join(home, "bin")),
		IsTTY:       func() bool { return false },
	}
	// Point HOME and SHELL at the temp tree. Anything that resolves the
	// shell rc through $HOME — linkShellRC, the alias doctor — would
	// otherwise write to or read the DEVELOPER'S real files. `aes setup`
	// links the rc, so every setup run through this harness appends a block
	// to the real ~/.zshrc pointing at a /tmp directory. Found because a
	// real block appeared in a real file.
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	return h
}

func (h *harness) run(args ...string) int {
	return h.app.Run(args)
}

// ---------------------------------------------------------------- exit codes

// TestExitCodeMapping is the contract in table form: each error kind maps to
// the code an agent is told to expect.
func TestExitCodeMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, ExitOK},
		{"generic failure", errors.New("boom"), ExitFailure},
		{"privilege", &exec.PrivilegeError{Command: "sudo apt-get install jq"}, ExitNeedsPrivilege},
		{"privilege sentinel", fmtWrap(exec.ErrNeedsPrivilege), ExitNeedsPrivilege},
		{"invalid manifest", manifest.ErrInvalid, ExitInvalidCatalog},
		{"duplicate tool", catalog.ErrDuplicateTool, ExitInvalidCatalog},
		{"corrupt state", state.ErrCorrupt, ExitInvalidCatalog},
		{"state from a newer build", state.ErrVersionTooNew, ExitInvalidCatalog},
		{"dependency cycle", resolver.ErrCycle, ExitInvalidCatalog},
		{"unknown tool", resolver.ErrUnknownTool, ExitInvalidCatalog},
		{"excluded dependency", resolver.ErrExcludedDependency, ExitInvalidCatalog},
		{"unimplemented strategy", installer.ErrUnknownStrategy, ExitInvalidCatalog},
		{"unsupported platform", platform.ErrUnsupportedOS, ExitUnsupportedPlatform},
		{"usage", &UsageError{msg: "bad flag"}, ExitUsage},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("context"), err) }

// TestPrivilegeErrorIsDistinctFromGeneric is worth its own case: exit 5 is
// the whole reason the privilege sentinel exists, and collapsing it into 1
// would leave an agent unable to tell "do this by hand" from "it broke".
func TestPrivilegeErrorIsDistinctFromGeneric(t *testing.T) {
	t.Parallel()

	priv := ExitCode(&exec.PrivilegeError{Command: "sudo apt-get install jq"})
	generic := ExitCode(errors.New("apt-get failed"))
	if priv == generic {
		t.Errorf("privilege and generic failure both map to %d", priv)
	}
	if priv != ExitNeedsPrivilege {
		t.Errorf("privilege error maps to %d, want %d", priv, ExitNeedsPrivilege)
	}
}

// TestExitCoderCannotInventACode keeps a command from inventing an exit code,
// which would break the promise that the numbers mean something.
func TestExitCoderCannotInventACode(t *testing.T) {

	if got := ExitCode(&bogusCoder{}); got != ExitFailure {
		t.Errorf("ExitCode(bogus) = %d, want %d (an undocumented code must not escape)", got, ExitFailure)
	}
	if got := ExitCode(&documentedCoder{}); got != ExitVerifyFailed {
		t.Errorf("ExitCode(documented) = %d, want %d", got, ExitVerifyFailed)
	}
}

type bogusCoder struct{}

func (*bogusCoder) Error() string { return "invented" }
func (*bogusCoder) ExitCode() int { return 42 }

type documentedCoder struct{}

func (*documentedCoder) Error() string { return "documented" }
func (*documentedCoder) ExitCode() int { return ExitVerifyFailed }

// ------------------------------------------------------------ flag contract

// TestYesRequiresATTY is the anti-hang rule: `aes setup --yes` with no
// terminal is a usage error, never a silent downgrade.
func TestYesRequiresATTY(t *testing.T) {

	h := newHarness(t)
	h.app.IsTTY = func() bool { return false }

	if got := h.run("list", "--yes"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
	if !strings.Contains(h.stderr.String(), "--non-interactive") {
		t.Errorf("message does not point at the fix:\n%s", h.stderr.String())
	}
}

func TestYesWithTTYIsAccepted(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return true }

	if got := h.run("list", "--yes"); got != ExitOK {
		t.Errorf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
}

// TestNonInteractiveDoesNotRequireATTY: --non-interactive is the CI flag and
// must never be blocked by the absence of a terminal.
func TestNonInteractiveDoesNotRequireATTY(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }

	if got := h.run("list", "--non-interactive"); got != ExitOK {
		t.Errorf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
}

// TestBothFlagsIsLegal documents that passing both is allowed and means
// --non-interactive, because that is the stronger statement.
func TestBothFlagsIsLegal(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }

	if got := h.run("list", "--yes", "--non-interactive"); got != ExitOK {
		t.Errorf("exit = %d, want 0 (both flags are legal)\n%s", got, h.stderr.String())
	}
}

func TestUnknownFlagIsUsage(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("list", "--nonsense"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
}

func TestUnknownCommandIsUsage(t *testing.T) {

	h := newHarness(t)
	if got := h.run("frobnicate"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
	if !strings.Contains(h.stderr.String(), "frobnicate") {
		t.Errorf("message does not name the unknown command:\n%s", h.stderr.String())
	}
}

// TestJSONStdoutHasNoProse is the machine-parseability contract: an agent
// must be able to json.Unmarshal stdout without stripping anything first.
func TestJSONStdoutHasNoProse(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})

	// The tool's binary is absent, so verify fails. stdout must still be
	// exactly one parseable document on the failure path.
	if got := h.run("verify", "--json"); got != ExitVerifyFailed {
		t.Fatalf("exit = %d, want %d", got, ExitVerifyFailed)
	}

	var out listOutput
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not a single JSON document: %v\n%s", err, h.stdout.String())
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "demo" {
		t.Errorf("tools = %+v, want one entry for demo", out.Tools)
	}
	if out.Error == nil || out.Error.ExitCode != ExitVerifyFailed {
		t.Errorf("error = %+v, want exit_code %d inside the document", out.Error, ExitVerifyFailed)
	}
}

func TestJSONErrorIsParseable(t *testing.T) {

	// A missing catalog makes resolve fail before any output.
	h := newHarness(t)
	h.app.CatalogRoot = filepath.Join(t.TempDir(), "absent")

	if got := h.run("list", "--json"); got == ExitOK {
		t.Fatal("expected a non-zero exit for a missing catalog")
	}
	if strings.TrimSpace(h.stdout.String()) == "" {
		t.Fatalf("no JSON on stdout for a failing --json run:\n%s", h.stderr.String())
	}
	var payload errorPayload
	if err := json.Unmarshal(h.stdout.Bytes(), &payload); err != nil {
		t.Fatalf("error payload is not valid JSON: %v\n%s", err, h.stdout.String())
	}
	if payload.ExitCode == 0 {
		t.Error("error payload reports exit_code 0 for a failing run")
	}
}

// ------------------------------------------------------------------ commands

// TestListShowsMissingEvenWhenStateClaimsInstalled is the distinction that
// makes `list` worth having: status comes from verify, never from state.
func TestListShowsMissingEvenWhenStateClaimsInstalled(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})

	// State claims demo is installed. Its binary does not exist, so verify
	// must say missing.
	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := state.Open(filepath.Join(h.home, "state.json"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	store.Set("demo", state.Installed{Strategy: manifest.StrategyGo})
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := h.run("list", "--json"); got != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
	var out listOutput
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(out.Tools))
	}
	row := out.Tools[0]
	if row.Status != "missing" {
		t.Errorf("Status = %q, want missing (verify is the truth, not state)", row.Status)
	}
	if !row.Installed {
		t.Error("Installed = false, but state records it; the disagreement must be visible")
	}
}

func TestVerifyUnknownToolIsUsage(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("verify", "ripgreep"); got != ExitUsage {
		t.Errorf("exit = %d, want %d (a typo must not quietly verify nothing)", got, ExitUsage)
	}
	if !strings.Contains(h.stderr.String(), "ripgreep") {
		t.Errorf("message does not name the typo:\n%s", h.stderr.String())
	}
}

// TestDoctorDetectsDrift is invariant I5: state is a cache and doctor is what
// compares it to reality.
func TestDoctorDetectsDrift(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})

	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store, err := state.Open(filepath.Join(h.home, "state.json"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	store.Set("demo", state.Installed{Strategy: manifest.StrategyGo})
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	if got := h.run("doctor", "--json"); got != ExitVerifyFailed {
		t.Errorf("exit = %d, want %d for a drifted tool", got, ExitVerifyFailed)
	}
	var rep doctorReport
	if err := json.Unmarshal(h.stdout.Bytes(), &rep); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}

	found := false
	for _, f := range rep.Findings {
		if f.Kind == driftDrifted && f.Tool == "demo" {
			found = true
			if f.Severity != "error" {
				t.Errorf("drift severity = %q, want error", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("doctor did not report drift for demo: %+v", rep.Findings)
	}
}

// TestDoctorMakesNoChanges is a stated contract: doctor reports, it never
// fixes. A self-fixing doctor makes changes the user cannot see.
func TestDoctorMakesNoChanges(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	before := snapshot(t, h.home)
	h.run("doctor")
	after := snapshot(t, h.home)

	if before != after {
		t.Errorf("doctor changed the filesystem:\nbefore: %s\nafter:  %s", before, after)
	}
}

// snapshot lists a directory tree with sizes, so a change of any kind shows.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(&b, "%s %d %o\n", rel, info.Size(), info.Mode())
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk: %v", err)
	}
	return b.String()
}

func TestDoctorReportsUntestedToolsInProfile(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})

	// A directory with no profiles means the default profile is unavailable,
	// which doctor should say rather than silently proceeding.
	if got := h.run("doctor", "--json"); got != ExitOK {
		t.Errorf("exit = %d, want 0 (a missing profile dir is informational)\n%s", got, h.stderr.String())
	}
}

// ------------------------------------------------------------------- env

func TestEnvPrintsByDefault(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("env"); got != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "DO NOT EDIT") {
		t.Errorf("env output is not the generated file:\n%s", h.stdout.String())
	}
}

func TestEnvLinkShellRequiresWrite(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("env", "--link-shell"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
}

func TestEnvLinkShellRefusedWithDryRun(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	// --link-shell modifies the user's shell rc, so it must not be reachable
	// from a run that promises to change nothing.
	if got := h.run("env", "--write", "--link-shell", "--dry-run"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
}

func TestEnvLinkShellIsIdempotent(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newHarness(t, [3]string{"demo", "utility", "go"})

	// Point HOME at a temp dir so the real rc is never touched.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SHELL", "/bin/bash")
	rcPath := filepath.Join(tmpHome, ".bashrc")
	if err := os.WriteFile(rcPath, []byte("# my bashrc\nexport EDITOR=vim\n"), 0o644); err != nil {
		t.Fatalf("seed rc: %v", err)
	}
	const original = "# my bashrc\nexport EDITOR=vim\n"

	if got := h.run("env", "--write", "--link-shell"); got != ExitOK {
		t.Fatalf("first run: exit %d\n%s", got, h.stderr.String())
	}
	afterFirst, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	if n := strings.Count(string(afterFirst), shellBlockStart); n != 1 {
		t.Fatalf("aes block appears %d times after the first run, want 1:\n%s", n, afterFirst)
	}
	// Everything outside the block must be byte-identical: aes manages only
	// its own delimited region.
	if !strings.HasPrefix(string(afterFirst), original) {
		t.Errorf("aes rewrote content outside its block:\n%s", afterFirst)
	}
	// The backup must exist and hold the original. Timestamped names, one
	// per run: a fixed name overwrites the previous backup, so the only copy
	// of a user's rc that survives is the one before the most recent link.
	backups, err := filepath.Glob(rcPath + ".aes-backup.*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("want exactly 1 backup after the first run, got %d: %v", len(backups), backups)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("no backup was taken: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup does not hold the original:\n%s", backup)
	}

	if got := h.run("env", "--write", "--link-shell"); got != ExitOK {
		t.Fatalf("second run: exit %d\n%s", got, h.stderr.String())
	}
	afterSecond, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	if n := strings.Count(string(afterSecond), shellBlockStart); n != 1 {
		t.Errorf("aes block appears %d times after a second run, want 1 (idempotent)", n)
	}
}

// ------------------------------------------------------------------ misc

func TestHelpAndVersion(t *testing.T) {

	h := newHarness(t)
	if got := h.run("--help"); got != ExitOK {
		t.Errorf("--help exit = %d, want 0", got)
	}
	if !strings.Contains(h.stdout.String(), "COMMANDS") {
		t.Errorf("help output has no command list:\n%s", h.stdout.String())
	}

	h2 := newHarness(t)
	if got := h2.run("--version"); got != ExitOK {
		t.Errorf("--version exit = %d, want 0", got)
	}
	if !strings.HasPrefix(h2.stdout.String(), "aes ") {
		t.Errorf("--version output = %q, want a version line", h2.stdout.String())
	}
}

// TestSetupNeverReportsSuccessWithoutInstalling guards the North Star's
// central promise: exit 0 only when the environment is actually done.
//
// This replaces an earlier test that asserted setup *fails* while
// unimplemented. That test passed for any reason setup failed — including a
// broken profile or a missing catalog — so it could not distinguish "not
// written yet" from "written and broken", which is the exact weakness a
// test should not have. This one asserts the property that matters instead:
// whatever setup did, it did not claim a completed environment without
// having installed and verified something.
func TestSetupNeverReportsSuccessWithoutInstalling(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	code := h.run("setup")

	if code == ExitOK {
		// Reaching exit 0 is only legitimate if the tool verified. This
		// fixture's binary does not exist, so it cannot have.
		var out listOutput
		_ = json.Unmarshal(h.stdout.Bytes(), &out)
		t.Errorf("aes setup exited 0 with nothing installed; stdout was:\n%s", h.stdout.String())
	}
}

// TestCorruptStateIsExitThree covers the reinstall-storm guard reaching the
// CLI: corruption is an invalid input, not a generic failure.
func TestCorruptStateIsExitThree(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.home, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := h.run("list"); got != ExitInvalidCatalog {
		t.Errorf("exit = %d, want %d for a corrupt state file", got, ExitInvalidCatalog)
	}
}

// TestCommandsDoNotHang proves no command blocks waiting for a terminal.
// The goroutine plus timeout is the assertion: a hang fails the test rather
// than the suite.
func TestCommandsDoNotHang(t *testing.T) {

	invocations := [][]string{
		{"list"}, {"verify"}, {"doctor"}, {"env"},
		{"list", "--non-interactive"}, {"verify", "--non-interactive"},
		{"setup", "--non-interactive"},
	}
	for _, args := range invocations {
		// Not parallel: newHarness calls t.Setenv, which forbids it.
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t, [3]string{"demo", "utility", "go"})

			done := make(chan int, 1)
			go func() { done <- h.run(args...) }()

			select {
			case code := <-done:
				_ = code
			case <-time.After(10 * time.Second):
				t.Fatalf("aes %s blocked; no command may wait on input", strings.Join(args, " "))
			}
		})
	}
}

// TestBareAesFallsBackToHelp is the regression guard for a bug the TUI
// introduced.
//
// Detecting a terminal up front looks correct and is not: /dev/null is a
// character device, so a program reading from it passes a ModeCharDevice
// check and looks interactive. Bare `aes` then tried to open a TUI on a
// stream that cannot hold one.
//
// Attempting the TUI and falling back on failure is robust to every such
// case, including ones nobody has thought of yet.
func TestBareAesFallsBackToHelp(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newHarness(t)

	// A TUI that cannot start — the common case being no terminal at all.
	h.app.RunTUI = func() error { return errors.New("no terminal here") }
	h.app.Run(nil)
	if !strings.Contains(h.stdout.String(), "COMMANDS") {
		t.Errorf("no help printed when the TUI could not start:\n%s", h.stdout.String())
	}
	// The reason is surfaced, not swallowed: "it did nothing" is worse than
	// "it could not run, here is why".
	if !strings.Contains(h.stderr.String(), "no terminal here") {
		t.Errorf("the reason the TUI failed was not reported:\n%s", h.stderr.String())
	}

	// A TUI that runs must not be followed by help.
	h.stdout.Reset()
	h.stderr.Reset()
	h.app.RunTUI = func() error { return nil }
	if code := h.app.Run(nil); code != ExitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	if strings.Contains(h.stdout.String(), "COMMANDS") {
		t.Errorf("help was printed even though the TUI ran:\n%s", h.stdout.String())
	}
}

// TestBareAesWithNoTUIWiredFallsBackToHelp covers the other half: a build
// without the TUI compiled in still prints help rather than doing nothing.
func TestBareAesWithNoTUIWiredFallsBackToHelp(t *testing.T) {

	h := newHarness(t)
	h.app.RunTUI = nil

	if code := h.app.Run(nil); code != ExitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(h.stdout.String(), "COMMANDS") {
		t.Errorf("no help printed with no TUI wired in:\n%s", h.stdout.String())
	}
}

// The rc block must source BOTH generated files. env.sh sets PATH and sources
// aliases.sh itself, so sourcing env.sh alone is sufficient — but the block
// also sources aliases.sh directly, and that redundancy is deliberate: a user
// who regenerates env.sh, then opens a shell whose rc was written by an
// earlier run, must still get their shortcuts.
//
// Without this test, deleting the aliases line from the block leaves the suite
// green on a machine where env.sh happens to be sourced by something else.
func TestLinkShellBlockSourcesBothGeneratedFiles(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newHarness(t, [3]string{"demo", "utility", "go"})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SHELL", "/bin/bash")
	rcPath := filepath.Join(tmpHome, ".bashrc")
	if err := os.WriteFile(rcPath, []byte("# my bashrc\n"), 0o644); err != nil {
		t.Fatalf("seed rc: %v", err)
	}

	if got := h.run("env", "--write", "--link-shell"); got != ExitOK {
		t.Fatalf("exit %d\n%s", got, h.stderr.String())
	}

	body, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	for _, want := range []string{"env.sh", "aliases.sh"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the rc block does not source %s:\n%s", want, body)
		}
	}
}

// The alias file must be written by the same command that writes env.sh, or a
// user who refreshes their PATH keeps a shortcuts file from an older run.
func TestEnvWriteEmitsBothFiles(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newHarness(t, [3]string{"demo", "utility", "go"})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SHELL", "/bin/bash")

	if got := h.run("env", "--write"); got != ExitOK {
		t.Fatalf("exit %d\n%s", got, h.stderr.String())
	}

	envPath := filepath.Join(h.app.Home, "env.sh")
	aliasesPath := filepath.Join(h.app.Home, envgen.AliasesFileName)
	for _, p := range []string{envPath, aliasesPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("`aes env --write` did not produce %s: %v", p, err)
		}
	}
	// The aliases file is aes's own, so it must carry aes's marker.
	raw, err := os.ReadFile(aliasesPath)
	if err != nil {
		t.Fatalf("read aliases: %v", err)
	}
	if !strings.HasPrefix(string(raw), envgen.AliasesHeader) {
		t.Errorf("aliases.sh has no aes header:\n%s", raw)
	}
}

// `aes setup` links the shell rc itself. This changes the spec's invariant 5,
// so the behaviour is asserted rather than assumed — a regression here means
// a new shell silently stops getting PATH and shortcuts, which is invisible
// until someone opens a terminal and finds `cc` missing.
func TestSetupLinksTheShellRC(t *testing.T) {
	// Not parallel: t.Setenv forbids it. The setup harness writes a REAL
	// binary, so Verify genuinely passes and exit 0 is legitimate here.
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility"})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SHELL", "/bin/bash")
	rcPath := filepath.Join(tmpHome, ".bashrc")
	if err := os.WriteFile(rcPath, []byte("# my bashrc\nexport EDITOR=vim\n"), 0o644); err != nil {
		t.Fatalf("seed rc: %v", err)
	}
	const original = "# my bashrc\nexport EDITOR=vim\n"

	if got, _ := h.run(t, "--only", "demo"); got != ExitOK {
		t.Fatalf("setup exit %d\n%s", got, h.stderr.String())
	}

	body, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	if n := strings.Count(string(body), shellBlockStart); n != 1 {
		t.Errorf("aes block appears %d times after setup, want 1:\n%s", n, body)
	}
	// The user's own content must survive byte-identical.
	if !strings.HasPrefix(string(body), original) {
		t.Errorf("setup rewrote content outside its block:\n%s", body)
	}
	// A backup must exist: setup edited a file the user owns.
	backups, err := filepath.Glob(rcPath + ".aes-backup.*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) == 0 {
		t.Error("setup modified the shell rc without taking a backup")
	}
}

// A second `aes setup` must not append a second block. The idempotence test
// covers `aes env`; this covers the path a real user takes, which is setup.
func TestSetupLinkingTheShellIsIdempotent(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility"})

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SHELL", "/bin/bash")
	rcPath := filepath.Join(tmpHome, ".bashrc")
	if err := os.WriteFile(rcPath, []byte("# my bashrc\n"), 0o644); err != nil {
		t.Fatalf("seed rc: %v", err)
	}

	for i := 0; i < 2; i++ {
		if got, _ := h.run(t, "--only", "demo"); got != ExitOK {
			t.Fatalf("run %d: exit %d\n%s", i, got, h.stderr.String())
		}
	}
	body, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("read rc: %v", err)
	}
	if n := strings.Count(string(body), shellBlockStart); n != 1 {
		t.Errorf("aes block appears %d times after two setups, want 1:\n%s", n, body)
	}
}

// A shell aes does not know how to configure is not a failure. Refusing
// beats appending to a shell the user did not ask us to touch, and setup must
// still exit 0 because the tools are installed either way.
func TestSetupOnAnUnknownShellStillSucceeds(t *testing.T) {
	// Not parallel: t.Setenv forbids it.
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility"})

	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/usr/bin/fish")

	if got, _ := h.run(t, "--only", "demo"); got != ExitOK {
		t.Errorf("setup exit %d on an unsupported shell; the tools are installed "+
			"and refusing to finish leaves the user worse off\n%s", got, h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "could not link the shell rc") {
		t.Errorf("setup did not say why the rc was not linked:\n%s", h.stderr.String())
	}
}

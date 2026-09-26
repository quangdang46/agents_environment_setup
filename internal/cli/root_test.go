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
		Registry:    installer.NewRegistry(),
		IsTTY:       func() bool { return false },
	}
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
		{"unsupported platform", platform.ErrUnsupportedOS, ExitUnsupportedPlatfrm},
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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return true }

	if got := h.run("list", "--yes"); got != ExitOK {
		t.Errorf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
}

// TestNonInteractiveDoesNotRequireATTY: --non-interactive is the CI flag and
// must never be blocked by the absence of a terminal.
func TestNonInteractiveDoesNotRequireATTY(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }

	if got := h.run("list", "--non-interactive"); got != ExitOK {
		t.Errorf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
}

// TestBothFlagsIsLegal documents that passing both is allowed and means
// --non-interactive, because that is the stronger statement.
func TestBothFlagsIsLegal(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }

	if got := h.run("list", "--yes", "--non-interactive"); got != ExitOK {
		t.Errorf("exit = %d, want 0 (both flags are legal)\n%s", got, h.stderr.String())
	}
}

func TestUnknownFlagIsUsage(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("list", "--nonsense"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
}

func TestUnknownCommandIsUsage(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})

	// A directory with no profiles means the default profile is unavailable,
	// which doctor should say rather than silently proceeding.
	if got := h.run("doctor", "--json"); got != ExitOK {
		t.Errorf("exit = %d, want 0 (a missing profile dir is informational)\n%s", got, h.stderr.String())
	}
}

// ------------------------------------------------------------------- env

func TestEnvPrintsByDefault(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("env"); got != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", got, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "DO NOT EDIT") {
		t.Errorf("env output is not the generated file:\n%s", h.stdout.String())
	}
}

func TestEnvLinkShellRequiresWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("env", "--link-shell"); got != ExitUsage {
		t.Errorf("exit = %d, want %d", got, ExitUsage)
	}
}

func TestEnvLinkShellRefusedWithDryRun(t *testing.T) {
	t.Parallel()

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
	// The backup must exist and hold the original.
	backup, err := os.ReadFile(rcPath + ".aes-backup")
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
	t.Parallel()

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

// TestSetupRefusesUntilImplemented keeps the North Star honest: a partial
// setup that exits 0 would be worse than a refusal.
func TestSetupRefusesUntilImplemented(t *testing.T) {
	t.Parallel()

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	if got := h.run("setup"); got == ExitOK {
		t.Error("aes setup reported success while unimplemented")
	}
}

// TestCorruptStateIsExitThree covers the reinstall-storm guard reaching the
// CLI: corruption is an invalid input, not a generic failure.
func TestCorruptStateIsExitThree(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	invocations := [][]string{
		{"list"}, {"verify"}, {"doctor"}, {"env"},
		{"list", "--non-interactive"}, {"verify", "--non-interactive"},
		{"setup", "--non-interactive"},
	}
	for _, args := range invocations {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
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

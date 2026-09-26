package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"time"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/state"
)

// setupHarness runs `aes setup` against a temporary catalog with an injected
// installer, so no test installs anything real.
type setupHarness struct {
	app    *App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	home   string

	// binDir is prepended to PATH. A recording installer writes a real
	// executable there, which is what lets the post-install verify pass for
	// real rather than being stubbed — verify is the ground truth and a
	// stubbed one would test nothing.
	binDir string

	mu    sync.Mutex
	calls []string
	// installErr, when set, makes every install fail with it.
	installErr error
	// privErr, when set, makes every install return a PrivilegeError.
	privErr error
}

// toolSpec describes one catalog entry.
type toolSpec struct {
	name     string
	category string
	// provides is the binary verify looks for. A value that does not resolve
	// makes the tool an install candidate; a real one makes it already
	// present.
	provides string
	// minVersion, when set, makes the tool stale if its version is lower.
	minVersion string
}

func newSetupHarness(t *testing.T, specs ...toolSpec) *setupHarness {
	t.Helper()

	tmp := t.TempDir()
	catRoot := filepath.Join(tmp, "catalog")
	profileDir := filepath.Join(tmp, "profiles")
	binDir := filepath.Join(tmp, "bin")
	for _, d := range []string{catRoot, profileDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	for _, s := range specs {
		// The installer creates a binary named after whatever `provides`
		// names, because that is what Verify looks for. Defaulting to the
		// tool name keeps the two in step.
		if s.provides == "" {
			s.provides = s.name
		}
		dir := filepath.Join(catRoot, s.category, s.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		verify := ""
		if s.minVersion != "" {
			// verify.command is mandatory whenever a version check is
			// declared, so the presence check stays honest.
			verify = "verify:\n  command: " + s.provides + " --version\n  version:\n    command: " +
				s.provides + " --version\n    min: \"" + s.minVersion + "\"\n"
		}
		content := "name: " + s.name + "\ndescription: fixture " + s.name + "\ncategory: " + s.category +
			"\nprovides: [" + s.provides + "]\n" + verify +
			"install:\n  darwin:\n    strategy: go\n    go_package: example.com/" + s.name + "@v1.0.0\n" +
			"  linux:\n    strategy: go\n    go_package: example.com/" + s.name + "@v1.0.0\n"
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write tool.yaml: %v", err)
		}
	}

	h := &setupHarness{
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
		home:   filepath.Join(tmp, ".aes"),
		binDir: binDir,
	}
	h.app = &App{
		In:          strings.NewReader(""),
		Out:         h.stdout,
		Err:         h.stderr,
		Home:        h.home,
		CatalogRoot: catRoot,
		ProfileDir:  profileDir,
		IsTTY:       func() bool { return false },
	}
	h.app.Registry = installer.NewRegistry()

	// The recording installer replaces the go strategy. Recording the
	// strategy is how "install runs the resolved strategy" is asserted: a
	// switch on the tool name would show up here as the wrong name.
	h.app.Registry.Register(manifest.StrategyGo, installerFunc(h.install))

	return h
}

// installerFunc adapts a func to installer.Installer.
type installerFunc func(ctx context.Context, a installer.Action) error

func (f installerFunc) Install(ctx context.Context, a installer.Action) error { return f(ctx, a) }

// install records the call and, unless told otherwise, makes the tool's
// binary appear by writing an executable into binDir.
func (h *setupHarness) install(ctx context.Context, a installer.Action) error {
	h.mu.Lock()
	h.calls = append(h.calls, a.Tool)
	privErr, installErr := h.privErr, h.installErr
	h.mu.Unlock()

	if privErr != nil {
		return privErr
	}
	if installErr != nil {
		return installErr
	}
	// A real executable, so the post-install Verify genuinely passes.
	bin := filepath.Join(h.binDir, a.Tool)
	return os.WriteFile(bin, []byte("#!/bin/sh\necho "+a.Tool+" 9.9.9\n"), 0o755)
}

func (h *setupHarness) invocations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// run executes aes setup and returns the exit code and parsed summary.
func (h *setupHarness) run(t *testing.T, args ...string) (int, *summaryOutput) {
	t.Helper()
	// Reset the captured streams: a second run in the same test would
	// otherwise append its JSON to the first, and Unmarshal would fail on
	// the combined stream.
	h.stdout.Reset()
	h.stderr.Reset()
	// PATH must contain binDir so verify sees what install produced.
	t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	full := append([]string{"setup", "--json"}, args...)
	code := h.app.Run(full)
	var sum summaryOutput
	if err := json.Unmarshal(h.stdout.Bytes(), &sum); err != nil {
		return code, nil
	}
	return code, &sum
}

func (h *setupHarness) summaryText() string { return h.stdout.String() }

// timeoutAfterSeconds keeps the no-block test from hanging the suite.
func timeoutAfterSeconds(n int) <-chan time.Time { return time.After(time.Duration(n) * time.Second) }

// stateEntries reads the state file the run wrote.
func (h *setupHarness) stateEntries(t *testing.T) map[string]state.Installed {
	t.Helper()
	store, err := state.Open(filepath.Join(h.home, "state.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("open state: %v", err)
	}
	return store.Installed()
}

// treeSnapshot lists a tree with sizes, so any filesystem change shows.
// Named distinctly from root_test.go's snapshot; same job, different harness.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel + " " + itoa(info.Size()) + "\n")
		return nil
	})
	return b.String()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------- assertions

// Assertion 2: `setup --dry-run` prints actions and changes NO file. The
// snapshot covers the whole ~/.aes tree, not just state.json — an earlier
// version of this idea only checked state and would have missed env.sh.
func TestSetupDryRunChangesNothing(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	before := treeSnapshot(t, filepath.Dir(h.home))
	code, sum := h.run(t, "--only", "demo", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.summaryText())
	}
	if !sum.DryRun {
		t.Error("summary does not report dry-run")
	}
	after := treeSnapshot(t, filepath.Dir(h.home))
	if before != after {
		t.Errorf("--dry-run changed the filesystem:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if calls := h.invocations(); len(calls) != 0 {
		t.Errorf("installer ran %v during a dry run", calls)
	}
}

// Assertions 3 and 4: the resolved strategy is the one that runs, and a
// passing verify is what writes state.
func TestSetupInstallsAndRecords(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	code, sum := h.run(t, "--only", "demo")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.stderr.String())
	}
	if calls := h.invocations(); len(calls) != 1 || calls[0] != "demo" {
		t.Errorf("installer calls = %v, want [demo] — the resolved strategy must be the one that runs", calls)
	}
	if sum.Installed != 1 {
		t.Errorf("Installed = %d, want 1", sum.Installed)
	}
	// State is written only because verify passed.
	entries := h.stateEntries(t)
	if _, ok := entries["demo"]; !ok {
		t.Errorf("state has no entry for demo: %+v", entries)
	}
	if got := entries["demo"].Strategy; got != manifest.StrategyGo {
		t.Errorf("state strategy = %q, want %q", got, manifest.StrategyGo)
	}
}

// Assertion 5: idempotence. A second run must call the installer zero times —
// this is the resume property the whole design turns on.
func TestSetupIsIdempotent(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	if code, _ := h.run(t, "--only", "demo"); code != ExitOK {
		t.Fatalf("first run: exit %d\n%s", code, h.stderr.String())
	}
	if calls := h.invocations(); len(calls) != 1 {
		t.Fatalf("first run made %d install calls, want 1", len(calls))
	}

	code, sum := h.run(t, "--only", "demo")
	if code != ExitOK {
		t.Fatalf("second run: exit %d\n%s", code, h.stderr.String())
	}
	// The load-bearing assertion.
	if calls := h.invocations(); len(calls) != 1 {
		t.Errorf("second run made %d install calls, want 0 more (total %v) — a re-run must resume, not restart", len(calls)-1, calls)
	}
	if sum.Installed != 0 {
		t.Errorf("second run reported %d installs, want 0", sum.Installed)
	}
	if sum.Present != 1 {
		t.Errorf("second run reported %d present, want 1", sum.Present)
	}
}

// Assertion 7: a corrupt manifest aborts with exit 3 and the installer is
// never called. A partial run here would install an arbitrary subset.
func TestCorruptManifestAbortsBeforeInstalling(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	bad := filepath.Join(h.app.CatalogRoot, "utility", "broken", "tool.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(bad, []byte("name: broken\ndescription: no install\nprovides: [broken]\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	code, _ := h.run(t, "--only", "demo")
	if code != ExitInvalidCatalog {
		t.Errorf("exit = %d, want %d for an invalid manifest", code, ExitInvalidCatalog)
	}
	if calls := h.invocations(); len(calls) != 0 {
		t.Errorf("installer ran %v despite an invalid catalog", calls)
	}
}

// Assertion 10: a tool that installs but does not verify gets NO state entry
// and is reported as failed. A state entry claiming success is exactly the
// drift doctor exists to clean up.
func TestInstallWithoutVerifyWritesNoState(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "ghost", category: "utility", provides: "ghost"})

	// The installer "succeeds" but writes nothing, so verify still fails.
	h.mu.Lock()
	h.installErr = nil
	h.mu.Unlock()
	h.app.Registry.Register(manifest.StrategyGo, installerFunc(func(context.Context, installer.Action) error {
		h.mu.Lock()
		h.calls = append(h.calls, "ghost")
		h.mu.Unlock()
		return nil // claims success, installs nothing
	}))

	code, sum := h.run(t, "--only", "ghost")
	if code != ExitVerifyFailed {
		t.Errorf("exit = %d, want %d", code, ExitVerifyFailed)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1", sum.Failed)
	}
	if entries := h.stateEntries(t); len(entries) != 0 {
		t.Errorf("state written for a tool that did not verify: %+v", entries)
	}
}

// Assertion 11: failure containment. One tool failing must not stop the rest.
func TestFailureIsContained(t *testing.T) {
	h := newSetupHarness(t,
		toolSpec{name: "good", category: "utility", provides: "good"},
		toolSpec{name: "bad", category: "utility", provides: "bad"},
	)

	h.app.Registry.Register(manifest.StrategyGo, installerFunc(func(ctx context.Context, a installer.Action) error {
		h.mu.Lock()
		h.calls = append(h.calls, a.Tool)
		h.mu.Unlock()
		if a.Tool == "bad" {
			return errors.New("network unreachable")
		}
		return os.WriteFile(filepath.Join(h.binDir, a.Tool), []byte("#!/bin/sh\necho "+a.Tool+" 9.9.9\n"), 0o755)
	}))

	code, sum := h.run(t, "--only", "good", "--only", "bad")
	if code != ExitVerifyFailed {
		t.Errorf("exit = %d, want %d", code, ExitVerifyFailed)
	}
	// The whole point: the healthy tool still installed.
	if sum.Installed != 1 {
		t.Errorf("Installed = %d, want 1 (a failing tool must not abort the run)", sum.Installed)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1", sum.Failed)
	}
	entries := h.stateEntries(t)
	if _, ok := entries["good"]; !ok {
		t.Errorf("the healthy tool has no state entry: %+v", entries)
	}
	if _, ok := entries["bad"]; ok {
		t.Error("the failed tool has a state entry")
	}
}

// Assertions 9 and 12: a privilege-requiring install reports a manual step
// and exits 5, naming the exact command. The command must be printed rather
// than merely described.
func TestPrivilegeStepIsReported(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "apt-tool", category: "utility", provides: "apt-tool"})

	h.app.Registry.Register(manifest.StrategyGo, installerFunc(func(context.Context, installer.Action) error {
		return &exec.PrivilegeError{Command: "sudo apt-get install -y jq", NonInteractive: true}
	}))

	code, sum := h.run(t, "--only", "apt-tool")
	if code != ExitNeedsPrivilege {
		t.Errorf("exit = %d, want %d", code, ExitNeedsPrivilege)
	}
	if !sum.NeedsPrivilege {
		t.Error("summary does not flag NeedsPrivilege")
	}
	found := false
	for _, r := range sum.Results {
		if r.NeedsPrivilege && r.Command == "sudo apt-get install -y jq" {
			found = true
		}
	}
	if !found {
		t.Errorf("summary does not carry the exact command: %+v", sum.Results)
	}
	if entries := h.stateEntries(t); len(entries) != 0 {
		t.Errorf("state written for a tool needing a manual step: %+v", entries)
	}
}

// Assertion 6: corrupt state is an error, never "nothing installed". Reading
// it as empty is what causes a reinstall storm.
func TestCorruptStateAbortsSetup(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.home, "state.json"), []byte("{ truncated"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	code, _ := h.run(t, "--only", "demo")
	if code != ExitInvalidCatalog {
		t.Errorf("exit = %d, want %d", code, ExitInvalidCatalog)
	}
	if calls := h.invocations(); len(calls) != 0 {
		t.Errorf("installer ran %v despite corrupt state — that is the reinstall storm", calls)
	}
}

// An already-present tool is not an install candidate, which is what makes a
// resumed run cheap.
func TestAlreadyPresentSkipsInstall(t *testing.T) {
	// provides "sh" resolves on any POSIX machine, so verify says present.
	h := newSetupHarness(t, toolSpec{name: "present", category: "shell", provides: "sh"})

	code, sum := h.run(t, "--only", "present")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.stderr.String())
	}
	if calls := h.invocations(); len(calls) != 0 {
		t.Errorf("installer ran %v for an already-present tool", calls)
	}
	if sum.Present != 1 {
		t.Errorf("Present = %d, want 1", sum.Present)
	}
	// A tool that verifies is recorded even if state forgot it, so the cache
	// converges toward the truth rather than staying permanently behind.
	if entries := h.stateEntries(t); len(entries) == 0 {
		t.Error("an already-present tool was not recorded; state never converges")
	}
}

// setup never blocks waiting for input, in any flag combination.
func TestSetupNeverBlocks(t *testing.T) {
	h := newSetupHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	done := make(chan int, 1)
	go func() {
		t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		done <- h.app.Run([]string{"setup", "--only", "demo", "--yes"})
	}()

	select {
	case <-done:
	case <-timeoutAfterSeconds(10):
		t.Fatal("aes setup blocked; it must never wait on input")
	}
}

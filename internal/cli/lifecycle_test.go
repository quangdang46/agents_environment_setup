package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/state"
	"gopkg.in/yaml.v3"
)

// indentYAML re-indents a marshalled fragment so it can be nested under a
// platform key in a hand-built manifest.
func indentYAML(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// lifeHarness runs the lifecycle commands against a temp catalog and state.
type lifeHarness struct {
	app    *App
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	in     *strings.Reader
	home   string
	binDir string
	cat    string
}

func newLifeHarness(t *testing.T, specs ...toolSpec) *lifeHarness {
	t.Helper()
	tmp := t.TempDir()
	catRoot := filepath.Join(tmp, "catalog")
	binDir := filepath.Join(tmp, "bin")
	for _, d := range []string{catRoot, binDir, filepath.Join(tmp, ".aes")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	for _, s := range specs {
		dir := filepath.Join(catRoot, s.category, s.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		target := manifest.Target{Strategy: s.strategy()}
		switch s.strategy() {
		case manifest.StrategyPackage:
			target.Manager, target.Package = manifest.ManagerApt, s.name
		default:
			target.GoPackage = "example.com/" + s.name + "@v1.0.0"
		}
		encoded, err := yaml.Marshal(target)
		if err != nil {
			t.Fatalf("encode target: %v", err)
		}
		content := "name: " + s.name + "\ndescription: fixture\ncategory: " + s.category +
			"\nprovides: [" + s.name + "]\ninstall:\n  darwin:\n" +
			indentYAML(string(encoded), "    ") +
			"  linux:\n" + indentYAML(string(encoded), "    ")
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	h := &lifeHarness{
		stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		in:     strings.NewReader(""),
		home:   filepath.Join(tmp, ".aes"),
		binDir: binDir,
		cat:    catRoot,
	}
	h.app = &App{
		In: h.in, Out: h.stdout, Err: h.stderr, Home: h.home,
		CatalogRoot: catRoot, ProfileDir: filepath.Join(tmp, "profiles"),
		IsTTY: func() bool { return false },
	}
	return h
}

func (s toolSpec) strategy() string {
	if s.strategyName != "" {
		return s.strategyName
	}
	return manifest.StrategyGo
}

// track writes a state entry for name.
func (h *lifeHarness) track(t *testing.T, name, strategy string) {
	t.Helper()
	store, err := state.Open(filepath.Join(h.home, "state.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.Set(name, state.Installed{Strategy: strategy})
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func (h *lifeHarness) tracked(t *testing.T, name string) bool {
	t.Helper()
	store, err := state.Open(filepath.Join(h.home, "state.json"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, ok := store.Get(name)
	return ok
}

// run executes a command with the harness bin dir on PATH, so verify sees
// the binaries the tests seed there.
func (h *lifeHarness) run(t *testing.T, args ...string) int {
	t.Helper()
	t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return h.app.Run(args)
}

// The load-bearing case: a strategy that claims success while the binary
// survives must NOT lose its state entry. Removing state for a tool still on
// disk is how drift gets created in the first place.
func TestUninstallKeepsStateWhenBinarySurvives(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h.track(t, "demo", manifest.StrategyGithubRelease)

	// Install a real binary, then replace the remover with one that claims
	// success and removes nothing.
	if err := os.WriteFile(filepath.Join(h.binDir, "demo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	h.app.Registry = lieRegistry(t, lieRemover{})

	code := h.run(t, "uninstall", "demo", "--confirm")
	if code != ExitVerifyFailed {
		t.Errorf("exit = %d, want %d when the binary survives a claimed removal", code, ExitVerifyFailed)
	}
	if !h.tracked(t, "demo") {
		t.Error("state entry was dropped even though the binary is still present — this creates drift by hand")
	}
	if !strings.Contains(h.stderr.String(), "still present") {
		t.Errorf("no explanation given; stderr was:\n%s", h.stderr.String())
	}
}

// lieRemover reports success and does nothing. It stands in for a strategy
// whose removal silently fails.
type lieRemover struct{}

func (lieRemover) Install(context.Context, installer.Action) error { return nil }

func (lieRemover) Uninstall(context.Context, installer.Action) (installer.Outcome, error) {
	return installer.Removed(), nil
}

// lieRegistry replaces the go strategy with one that claims a removal
// succeeded and does nothing — the failure mode this test exists for.
func lieRegistry(t *testing.T, rem installer.Remover) *installer.Registry {
	t.Helper()
	r := installer.NewRegistry()
	r.Register(manifest.StrategyGo, removerInstaller{inner: installer.NewGoInstaller(), rem: rem})
	return r
}

// removerInstaller delegates installation to a real installer and removal to
// the injected stand-in.
type removerInstaller struct {
	inner installer.Installer
	rem   installer.Remover
}

func (r removerInstaller) Install(ctx context.Context, a installer.Action) error {
	return r.inner.Install(ctx, a)
}

func (r removerInstaller) Uninstall(ctx context.Context, a installer.Action) (installer.Outcome, error) {
	return r.rem.Uninstall(ctx, a)
}

func TestForgetLeavesTheBinary(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h.track(t, "demo", manifest.StrategyGo)

	bin := filepath.Join(h.binDir, "demo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if code := h.run(t, "forget", "demo", "--confirm"); code != ExitOK {
		t.Fatalf("exit = %d, want 0\n%s", code, h.stderr.String())
	}
	// The binary is the whole point: forget tracks nothing, removes nothing.
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("forget removed the binary; it must only drop the state entry: %v", err)
	}
	if h.tracked(t, "demo") {
		t.Error("forget did not drop the state entry")
	}
}

func TestEcosystemUninstallReportsNotRemoved(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo", strategyName: manifest.StrategyGo})
	h.track(t, "demo", manifest.StrategyGo)

	if code := h.run(t, "uninstall", "demo", "--confirm"); code != ExitOK {
		t.Fatalf("exit = %d, want 0 (not-removing is not a failure)\n%s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "not removed") {
		t.Errorf("output does not say it did not remove anything:\n%s", out)
	}
	// It must name what the user can do instead.
	if !strings.Contains(out, "example.com/demo@v1.0.0") {
		t.Errorf("output does not name the package to remove by hand:\n%s", out)
	}
	if !h.tracked(t, "demo") {
		t.Error("state entry dropped even though nothing was removed")
	}
}

func TestUntrackedToolIsANoOp(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})

	for _, cmd := range []string{"uninstall", "forget"} {
		t.Run(cmd, func(t *testing.T) {
			h.stdout.Reset()
			h.stderr.Reset()
			code := h.run(t, cmd, "demo", "--confirm")
			if code == ExitOK {
				t.Errorf("exit = 0 for a tool with no state entry; it should report there was nothing to do")
			}
			combined := h.stdout.String() + h.stderr.String()
			if !strings.Contains(combined, "not tracked") {
				t.Errorf("message does not explain there was nothing to do:\n%s", combined)
			}
		})
	}
}

// Confirmation is required unless --confirm or --yes. EOF counts as no.
func TestConfirmationIsRequired(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"closed stdin", "", []string{"forget", "demo"}},
		{"explicit no", "n\n", []string{"forget", "demo"}},
		{"bare enter", "\n", []string{"forget", "demo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
			h.track(t, "demo", manifest.StrategyGo)
			h.app.In = strings.NewReader(tc.stdin)

			if code := h.run(t, tc.args...); code != ExitOK {
				t.Fatalf("exit = %d, want 0 (declining is not an error)", code)
			}
			if !h.tracked(t, "demo") {
				t.Error("acted without confirmation")
			}
			if !strings.Contains(h.stdout.String(), "cancelled") {
				t.Errorf("did not report the cancellation:\n%s", h.stdout.String())
			}
		})
	}
}

func TestConfirmationAccepted(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h.track(t, "demo", manifest.StrategyGo)
	h.app.In = strings.NewReader("y\n")

	if code := h.run(t, "forget", "demo"); code != ExitOK {
		t.Fatalf("exit = %d\n%s", code, h.stderr.String())
	}
	if h.tracked(t, "demo") {
		t.Error("a confirmed forget did not act")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h.track(t, "demo", manifest.StrategyGo)

	before := treeSnapshot(t, h.home)
	for _, cmd := range []string{"uninstall", "forget"} {
		if code := h.run(t, cmd, "demo", "--confirm", "--dry-run"); code != ExitOK {
			t.Fatalf("%s --dry-run: exit %d\n%s", cmd, code, h.stderr.String())
		}
	}
	if after := treeSnapshot(t, h.home); after != before {
		t.Error("a --dry-run changed the filesystem")
	}
	if !h.tracked(t, "demo") {
		t.Error("--dry-run dropped the state entry")
	}
}

func TestUninstallListsDependents(t *testing.T) {
	tmp := t.TempDir()
	catRoot := filepath.Join(tmp, "catalog")
	// depender declares demo; the catalog must show that relationship.
	for name, deps := range map[string]string{"demo": "", "depender": "  - demo\n"} {
		dir := filepath.Join(catRoot, "utility", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		content := "name: " + name + "\ndescription: f\ncategory: utility\nprovides: [" + name + "]\n" +
			"dependencies:\n" + deps +
			"install:\n  darwin:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n" +
			"  linux:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n"
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	h := newLifeHarness(t, toolSpec{name: "demo", category: "utility", provides: "demo"})
	h.app.CatalogRoot = catRoot
	h.track(t, "demo", manifest.StrategyGo)

	h.run(t, "uninstall", "demo", "--confirm")
	if !strings.Contains(h.stderr.String(), "depender") {
		t.Errorf("did not mention the dependent tool:\n%s", h.stderr.String())
	}
	if !strings.Contains(h.stderr.String(), "not be removed automatically") {
		t.Errorf("did not say dependents are left alone:\n%s", h.stderr.String())
	}
}

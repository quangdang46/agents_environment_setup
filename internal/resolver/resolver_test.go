package resolver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// spec describes one tool for the fixture builder.
type spec struct {
	name      string
	deps      []string
	isDefault bool
	// platforms lists the GOOS keys to declare under install. Empty means
	// darwin only, which is how a tool that does not support this host is
	// built.
	platforms []string
	strategy  string
	manager   string
}

func (s spec) yaml() string {
	platforms := s.platforms
	if len(platforms) == 0 {
		platforms = []string{"darwin"}
	}
	strategy := s.strategy
	if strategy == "" {
		strategy = "package"
	}
	manager := s.manager
	if manager == "" {
		manager = "brew"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", s.name)
	fmt.Fprintf(&b, "description: fixture tool %s\n", s.name)
	b.WriteString("category: util\n")
	if s.isDefault {
		b.WriteString("default: true\n")
	}
	// The key is written once, not per dependency — repeating it would make
	// the mapping a duplicate key rather than a list.
	if len(s.deps) > 0 {
		b.WriteString("dependencies:\n")
		for _, d := range s.deps {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
	}
	b.WriteString("install:\n")
	for _, p := range platforms {
		fmt.Fprintf(&b, "  %s:\n    strategy: %s\n", p, strategy)
		switch strategy {
		case "package":
			fmt.Fprintf(&b, "    manager: %s\n    package: %s\n", manager, s.name)
		case "github-release":
			// github-release requires repository, asset and sha256, with the
			// asset and checksum keys covering exactly the same arch set.
			b.WriteString("    repository: aes/fixture\n")
			b.WriteString("    asset:\n      amd64: " + s.name + "-linux-amd64.tar.gz\n")
			b.WriteString("    sha256:\n      amd64: " + strings.Repeat("a", 64) + "\n")
		}
	}
	fmt.Fprintf(&b, "verify:\n  command: %s\n", s.name)
	return b.String()
}

func build(t *testing.T, specs ...spec) *catalog.Catalog {
	t.Helper()
	root := t.TempDir()
	for _, s := range specs {
		dir := filepath.Join(root, "util", s.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(s.yaml()), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

// Hosts are constructed explicitly, never via Current(), so these tests assert
// the resolver's behaviour rather than the machine's identity.
func darwin() *platform.Host {
	return &platform.Host{OS: platform.OSDarwin, Arch: platform.ArchAMD64, Manager: platform.ManagerBrew}
}

func linuxApt() *platform.Host {
	return &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64, Manager: "apt"}
}

func toolOrder(actions []Action) string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Tool)
	}
	return strings.Join(out, ",")
}

// mustNotHang runs fn and fails if it does not return promptly. A cycle that
// loops instead of erroring is the specific failure this guards (I11).
func mustNotHang(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return within 5s — probable infinite loop", what)
	}
}

func TestResolveCycleErrorsAndDoesNotHang(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []spec
		only  []string
	}{
		{
			name:  "two-node cycle",
			specs: []spec{{name: "a", deps: []string{"b"}}, {name: "b", deps: []string{"a"}}},
			only:  []string{"a"},
		},
		{
			name:  "three-node cycle",
			specs: []spec{{name: "a", deps: []string{"b"}}, {name: "b", deps: []string{"c"}}, {name: "c", deps: []string{"a"}}},
			only:  []string{"a"},
		},
		{
			name:  "cycle behind an acyclic prefix",
			specs: []spec{{name: "root"}, {name: "a", deps: []string{"root", "b"}}, {name: "b", deps: []string{"a"}}},
			only:  []string{"a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := build(t, tc.specs...)
			var err error
			mustNotHang(t, "Resolve on a cyclic catalog", func() {
				_, err = Resolve(c, Request{Only: tc.only}, darwin())
			})
			if !errors.Is(err, ErrCycle) {
				t.Fatalf("error = %v, want ErrCycle", err)
			}
		})
	}
}

// A self-dependency never reaches the resolver: manifest validation rejects it
// at load time, naming the tool. That is the better place for it — the error
// arrives before any resolution work rather than after it.
func TestSelfDependencyIsRejectedAtCatalogLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "util", "selfish")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := spec{name: "selfish", deps: []string{"selfish"}}.yaml()
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := catalog.Load(root)
	if err == nil {
		t.Fatal("a self-dependent tool passed catalog validation")
	}
	if !strings.Contains(err.Error(), "selfish") {
		t.Errorf("error %q does not name the offending tool", err)
	}
}

func TestCycleErrorNamesTheCycle(t *testing.T) {
	c := build(t,
		spec{name: "a", deps: []string{"b"}},
		spec{name: "b", deps: []string{"a"}},
	)
	_, err := Resolve(c, Request{Only: []string{"a"}}, darwin())
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	// The message has to name the participants or the author is left hunting.
	for _, want := range []string{"a", "b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A diamond must produce exactly one action for the shared dependency.
func TestDiamondDependencyAppearsOnce(t *testing.T) {
	c := build(t,
		spec{name: "a", deps: []string{"b", "c"}},
		spec{name: "b", deps: []string{"d"}},
		spec{name: "c", deps: []string{"d"}},
		spec{name: "d"},
	)
	actions, err := Resolve(c, Request{Only: []string{"a"}}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	counts := map[string]int{}
	for _, a := range actions {
		counts[a.Tool]++
	}
	if counts["d"] != 1 {
		t.Errorf("d appeared %d times, want exactly 1 (full order: %s)", counts["d"], toolOrder(actions))
	}
	if len(actions) != 4 {
		t.Errorf("got %d actions, want 4: %s", len(actions), toolOrder(actions))
	}
}

// Dependencies beat selection.
func TestOnlyPullsInDependencies(t *testing.T) {
	c := build(t,
		spec{name: "claude", deps: []string{"git", "node"}},
		spec{name: "git"},
		spec{name: "node"},
	)
	actions, err := Resolve(c, Request{Only: []string{"claude"}}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := toolOrder(actions)
	if !strings.Contains(got, "git") || !strings.Contains(got, "node") {
		t.Errorf("actions = %s, want claude's dependencies included", got)
	}
}

func TestExcludeRequiredDependencyIsAnError(t *testing.T) {
	c := build(t,
		spec{name: "claude", deps: []string{"node"}},
		spec{name: "node"},
	)
	_, err := Resolve(c, Request{Only: []string{"claude"}, Exclude: []string{"node"}}, darwin())
	if !errors.Is(err, ErrExcludedDependency) {
		t.Fatalf("error = %v, want ErrExcludedDependency", err)
	}
	// The message must name both the tool and who needs it.
	if !strings.Contains(err.Error(), "node") || !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %q does not name the tool and its requirer", err)
	}
}

// Excluding something nothing needs is a normal outcome.
func TestExcludeUnusedToolIsSilent(t *testing.T) {
	c := build(t,
		spec{name: "a", isDefault: true},
		spec{name: "b"},
	)
	actions, err := Resolve(c, Request{Exclude: []string{"b"}}, darwin())
	if err != nil {
		t.Fatalf("Resolve with an unused exclusion: %v", err)
	}
	if got := toolOrder(actions); got != "a" {
		t.Errorf("actions = %s, want a", got)
	}
}

func TestUnknownToolIsAnError(t *testing.T) {
	c := build(t, spec{name: "a"})
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"only", Request{Only: []string{"nope"}}},
		{"exclude", Request{Exclude: []string{"nope"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(c, tc.req, darwin()); !errors.Is(err, ErrUnknownTool) {
				t.Errorf("error = %v, want ErrUnknownTool", err)
			}
		})
	}
}

// Identical input must serialize to identical bytes (I8).
func TestResolveIsDeterministic(t *testing.T) {
	c := build(t,
		spec{name: "a", deps: []string{"b", "c", "d"}, isDefault: true},
		spec{name: "b", deps: []string{"e"}, isDefault: true},
		spec{name: "c", isDefault: true},
		spec{name: "d", isDefault: true},
		spec{name: "e", isDefault: true},
		spec{name: "unrelated"},
	)
	first, err := Resolve(c, Request{}, darwin())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	firstJSON, err := JSON(first)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for range 20 {
		again, err := Resolve(c, Request{}, darwin())
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		againJSON, err := JSON(again)
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
		if string(againJSON) != string(firstJSON) {
			t.Fatalf("resolution is not deterministic:\n%s\n%s", firstJSON, againJSON)
		}
	}
	// And again through a different selection shape, to catch order that only
	// happens to be stable for the default profile.
	sel := Request{Only: []string{"a", "b"}}
	s1, _ := Resolve(c, sel, darwin())
	s2, _ := Resolve(c, sel, darwin())
	j1, _ := JSON(s1)
	j2, _ := JSON(s2)
	if string(j1) != string(j2) {
		t.Errorf("--only resolution is not deterministic:\n%s\n%s", j1, j2)
	}
}

func TestEmptyProfileResolvesToDefaultTools(t *testing.T) {
	c := build(t,
		spec{name: "a", isDefault: true},
		spec{name: "b", isDefault: true},
		spec{name: "c"},
	)
	actions, err := Resolve(c, Request{}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := toolOrder(actions); got != "a,b" {
		t.Errorf("actions = %s, want a,b", got)
	}
}

// A tool with no install entry for this host is skipped, not an error (I12).
func TestUnsupportedPlatformIsSkippedNotAnError(t *testing.T) {
	c := build(t,
		spec{name: "maconly", isDefault: true, platforms: []string{"darwin"}},
		spec{name: "linonly", isDefault: true, platforms: []string{"linux"}},
		spec{name: "both", isDefault: true, platforms: []string{"darwin", "linux"}},
	)
	actions, err := Resolve(c, Request{}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := toolOrder(actions); got != "both,maconly" {
		t.Errorf("darwin actions = %s, want both,maconly", got)
	}
	actions, err = Resolve(c, Request{}, linuxApt())
	if err != nil {
		t.Fatalf("Resolve on linux: %v", err)
	}
	if got := toolOrder(actions); got != "both,linonly" {
		t.Errorf("linux actions = %s, want both,linonly", got)
	}
}

func TestReasonIsPopulatedOnEveryPath(t *testing.T) {
	c := build(t,
		spec{name: "defaulted", isDefault: true},
		spec{name: "git"},
		spec{name: "claude", deps: []string{"git"}},
	)
	actions, err := Resolve(c, Request{Only: []string{"claude"}, Exclude: nil}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// defaulted is not in the --only closure, so ask for it separately.
	withDefault, err := Resolve(c, Request{}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	actions = append(actions, withDefault...)

	reasons := map[string]string{}
	for _, a := range actions {
		reasons[a.Tool] = a.Reason
	}
	if got := reasons["claude"]; got != "only:claude" {
		t.Errorf("claude reason = %q, want %q", got, "only:claude")
	}
	if got := reasons["git"]; got != "dependency:claude" {
		t.Errorf("git reason = %q, want %q", got, "dependency:claude")
	}
	if got := reasons["defaulted"]; got != "default" {
		t.Errorf("defaulted reason = %q, want %q", got, "default")
	}
}

// Every dependency must appear strictly before its dependent, checked across
// the whole slice rather than pairwise spot checks.
func TestActionsAreTopologicallyOrdered(t *testing.T) {
	c := build(t,
		spec{name: "a", deps: []string{"b", "c"}},
		spec{name: "b", deps: []string{"d"}},
		spec{name: "c", deps: []string{"d"}},
		spec{name: "d", deps: []string{"e"}},
		spec{name: "e"},
		spec{name: "standalone"},
	)
	actions, err := Resolve(c, Request{Only: []string{"a", "standalone"}}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	pos := make(map[string]int, len(actions))
	for i, a := range actions {
		pos[a.Tool] = i
	}
	for _, a := range actions {
		tool, ok := c.ByName(a.Tool)
		if !ok {
			t.Fatalf("action %q is not in the catalog", a.Tool)
		}
		for _, dep := range tool.Dependencies {
			p, included := pos[dep]
			if !included {
				t.Errorf("%s depends on %s but %s is missing from the actions", a.Tool, dep, dep)
				continue
			}
			if p >= pos[a.Tool] {
				t.Errorf("%s appears at %d but its dependency %s appears at %d — must be earlier",
					a.Tool, pos[a.Tool], dep, p)
			}
		}
	}
}

// Privilege is derived from (strategy, manager), never from YAML (I2).
func TestRequiresPrivilegeIsDerived(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy string
		manager  string
		host     *platform.Host
		want     bool
	}{
		{"package + apt", "package", "apt", linuxApt(), true},
		{"package + brew", "package", "brew", darwin(), false},
		{"github-release", "github-release", "", darwin(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec{name: "t", strategy: tc.strategy, manager: tc.manager, platforms: []string{"linux", "darwin"}}
			c := build(t, s)
			actions, err := Resolve(c, Request{Only: []string{"t"}}, tc.host)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(actions) != 1 {
				t.Fatalf("got %d actions, want 1", len(actions))
			}
			if actions[0].RequiresPrivilege != tc.want {
				t.Errorf("RequiresPrivilege = %t, want %t", actions[0].RequiresPrivilege, tc.want)
			}
		})
	}
}

func TestActionsAreNonNilWhenEmpty(t *testing.T) {
	c := build(t, spec{name: "a"})
	actions, err := Resolve(c, Request{Only: []string{"a"}}, &platform.Host{OS: "plan9"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if actions == nil {
		t.Fatal("Resolve returned a nil slice; callers must not need a nil check")
	}
	if len(actions) != 0 {
		t.Errorf("got %d actions, want 0", len(actions))
	}
	// And it must still serialize as an empty array, not null.
	b, err := JSON(actions)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("JSON = %s, want []", b)
	}
}

// All actions in one resolution share the same Host pointer.
func TestActionsShareOneHost(t *testing.T) {
	c := build(t,
		spec{name: "a", deps: []string{"b"}},
		spec{name: "b"},
	)
	h := darwin()
	actions, err := Resolve(c, Request{Only: []string{"a"}}, h)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, a := range actions {
		if a.Host != h {
			t.Errorf("%s has its own Host copy, want the shared pointer", a.Tool)
		}
	}
}

func TestResolveRejectsNilInputs(t *testing.T) {
	if _, err := Resolve(nil, Request{}, darwin()); err == nil {
		t.Error("nil catalog was accepted")
	}
	if _, err := Resolve(build(t, spec{name: "a"}), Request{}, nil); err == nil {
		t.Error("nil host was accepted")
	}
}

func TestStringRendersEveryAction(t *testing.T) {
	c := build(t, spec{name: "a", isDefault: true})
	actions, err := Resolve(c, Request{}, darwin())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := String(actions)
	if !strings.Contains(got, "a") || !strings.Contains(got, "install") {
		t.Errorf("String() = %q", got)
	}
}

package tui

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/plan"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
)

// fixtureCatalog writes a small catalog with a dependency edge, so the
// resolver has something to do.
func fixtureCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	root := t.TempDir()
	specs := map[string]string{
		// rust depends on cargo: ticking rust must pull cargo in.
		"rust":  "runtime",
		"cargo": "runtime",
		"rg":    "search",
		"fzf":   "search",
		"zsh":   "shell",
	}
	for name, category := range specs {
		dir := filepath.Join(root, category, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		deps := ""
		if name == "rust" {
			deps = "dependencies: [cargo]\n"
		}
		content := "name: " + name + "\ndescription: fixture " + name + "\ncategory: " + category +
			"\nprovides: [" + name + "]\n" + deps +
			"install:\n  darwin:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n" +
			"  linux:\n    strategy: go\n    go_package: example.com/" + name + "@v1.0.0\n"
		if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	c, err := catalog.Load(root)
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

func testHost() *platform.Host {
	return &platform.Host{OS: platform.OSDarwin, Arch: platform.ArchARM64}
}

// TestTUIPlanMatchesCLI is invariant I13, and the reason plan exists.
//
// A TUI selection of {rust} and `aes setup --only rust` must produce the
// same Actions. They are compared through the same function both frontends
// call, so this test is really asserting that the TUI has not grown a
// private path — which is the failure that would otherwise only show up as
// the two frontends quietly disagreeing.
func TestTUIPlanMatchesCLI(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)
	h := testHost()
	m := New(c, h)

	m.Toggle("rust")
	fromTUI, err := m.Plan()
	if err != nil {
		t.Fatalf("TUI plan: %v", err)
	}

	// What the CLI does with the same words on the command line.
	fromCLI, err := plan.Resolve(c, h, plan.Selection{Only: []string{"rust"}})
	if err != nil {
		t.Fatalf("CLI plan: %v", err)
	}
	if len(fromCLI) == 0 {
		t.Fatal("CLI plan is empty; the fixture is not exercising the resolver")
	}

	if len(fromTUI) != len(fromCLI) {
		t.Fatalf("TUI produced %v, CLI produced %v", fromTUI, cliNames(fromCLI))
	}
	for i := range fromCLI {
		if fromTUI[i] != fromCLI[i].Tool {
			t.Errorf("position %d: TUI %q, CLI %q", i, fromTUI[i], fromCLI[i].Tool)
		}
	}
}

// cliNames extracts tool names for a readable failure message.
func cliNames(actions []resolver.Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Tool)
	}
	return out
}

// TestTUIDependencyClosure proves constraint 2 in substance: ticking rust
// pulls cargo in without the TUI knowing how.
func TestTUIDependencyClosure(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	m.Toggle("rust")

	got, err := m.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sawRust, sawCargo bool
	for _, n := range got {
		switch n {
		case "rust":
			sawRust = true
		case "cargo":
			sawCargo = true
		}
	}
	if !sawRust {
		t.Error("rust is missing from its own plan")
	}
	if !sawCargo {
		t.Errorf("cargo was not pulled in as a dependency: %v", got)
	}
}

// TestEmptySelectionIsANoOp: pressing Enter with nothing ticked must not be
// an error.
func TestEmptySelectionIsANoOp(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	if !m.Selection().Empty() {
		t.Fatal("a fresh model should have an empty selection")
	}
	got, err := m.Plan()
	if err != nil {
		t.Errorf("empty selection errored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty selection produced a plan: %v", got)
	}
}

// TestQuittingMutatesNothing is the safety property: leaving mid-selection
// must not have installed or recorded anything.
func TestQuittingMutatesNothing(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)
	m := New(c, testHost())
	m.Toggle("rg")
	m.Toggle("fzf")

	// One directory, snapshotted twice. Calling t.TempDir() twice would
	// compare two different trees and always differ.
	scratch := t.TempDir()
	before := treeSnapshot(scratch)
	m.Quit = true
	m.Toggle("zsh") // still just state

	if len(m.Selected) != 3 {
		t.Errorf("selection changed on quit: %v", m.Selected)
	}
	if after := treeSnapshot(scratch); after != before {
		t.Errorf("quitting mutated the filesystem:\nbefore: %q\nafter:  %q", before, after)
	}
}

func treeSnapshot(root string) string {
	var b strings.Builder
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			b.WriteString(p)
		}
		return nil
	})
	return b.String()
}

func TestToggleRejectsUnknownTool(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	if m.Toggle("not-a-tool") {
		t.Error("Toggle accepted a tool that is not in the catalog")
	}
	if len(m.Selected) != 0 {
		t.Errorf("an unknown tool got ticked: %v", m.Selected)
	}
}

func TestFilterNarrowsAndClears(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	all := len(m.All)

	m.SetFilter("sea")
	if len(m.Tools) == 0 || len(m.Tools) >= all {
		t.Errorf("filter did not narrow: %d of %d", len(m.Tools), all)
	}
	m.SetFilter("no-such-thing")
	if len(m.Tools) != 0 {
		t.Errorf("unmatched filter showed %d tools", len(m.Tools))
	}
	m.SetFilter("")
	if len(m.Tools) != all {
		t.Errorf("clearing the filter showed %d tools, want %d", len(m.Tools), all)
	}
}

func TestFilterMatchesNameDescriptionAndCategory(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	for _, term := range []string{"rg", "fixture rg", "search"} {
		m.SetFilter(term)
		if len(m.Tools) == 0 {
			t.Errorf("filter %q matched nothing", term)
		}
	}
}

func TestCursorStaysInRange(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	m.Screen = ScreenTools
	m.Move(-5)
	if m.Cursor != 0 {
		t.Errorf("cursor went negative: %d", m.Cursor)
	}
	m.Move(1000)
	if m.Cursor >= len(m.Tools) {
		t.Errorf("cursor ran past the end: %d of %d", m.Cursor, len(m.Tools))
	}
}

func TestSelectionIsOrderIndependent(t *testing.T) {
	t.Parallel()

	// Ticking in a different order must give the same selection, or the two
	// frontends diverge on nothing but map iteration order.
	a := New(fixtureCatalog(t), testHost())
	a.Toggle("rg")
	a.Toggle("fzf")
	a.Toggle("zsh")

	b := New(fixtureCatalog(t), testHost())
	b.Toggle("zsh")
	b.Toggle("rg")
	b.Toggle("fzf")

	if a.Selection().String() != b.Selection().String() {
		t.Errorf("selection depends on tick order: %q vs %q", a.Selection(), b.Selection())
	}
}

func TestClearSelectionAlsoClearsHiddenTicks(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	m.SelectAllVisible()
	m.SetFilter("search")
	m.ClearSelection()
	// A tick hidden by the filter would otherwise survive "clear", which is
	// the kind of thing a user reports as a bug with no reproduction.
	if len(m.Selected) != 0 {
		t.Errorf("clear left %d ticks behind: %v", len(m.Selected), m.Selected)
	}
}

// ---------------------------------------------------------------- keys

func TestDecodeKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    []byte
		wantK Key
		wantR rune
		wantN int
	}{
		{"up arrow", []byte("\x1b[A"), KeyUp, 0, 3},
		{"down arrow", []byte("\x1b[B"), KeyDown, 0, 3},
		{"app-mode up", []byte("\x1bOA"), KeyUp, 0, 3},
		{"bare escape", []byte{0x1b}, KeyEsc, 0, 1},
		{"enter cr", []byte("\r"), KeyEnter, 0, 1},
		{"enter lf", []byte("\n"), KeyEnter, 0, 1},
		{"space", []byte(" "), KeySpace, 0, 1},
		{"slash", []byte("/"), KeySlash, 0, 1},
		{"backspace", []byte{0x7f}, KeyBackspace, 0, 1},
		{"q", []byte("q"), KeyRune, 'q', 1},
		{"empty", nil, KeyNone, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k, r, n := DecodeKey(tc.in)
			if k != tc.wantK || r != tc.wantR || n != tc.wantN {
				t.Errorf("DecodeKey(%q) = (%v, %q, %d), want (%v, %q, %d)",
					tc.in, k, r, n, tc.wantK, tc.wantR, tc.wantN)
			}
		})
	}
}

// A UTF-8 rune must decode as one keypress, not as its individual bytes.
func TestDecodeKeyHandlesMultiByteRunes(t *testing.T) {
	t.Parallel()

	// "é" is two bytes; "→" is three.
	for _, r := range []rune{'é', '→', '😀'} {
		buf := []byte(string(r))
		k, got, n := DecodeKey(buf)
		if k != KeyRune || got != r {
			t.Errorf("DecodeKey(%q) = (%v, %q), want KeyRune %q", r, k, got, r)
		}
		if n != len(buf) {
			t.Errorf("DecodeKey(%q) consumed %d bytes, want %d", r, n, len(buf))
		}
	}
}

// An incomplete rune must not be decoded as garbage.
func TestDecodeKeyIncompleteRune(t *testing.T) {
	t.Parallel()

	// Lead byte of a 3-byte rune with nothing after it.
	if k, _, n := DecodeKey([]byte{0xe2}); k != KeyNone || n != 1 {
		t.Errorf("DecodeKey(incomplete) = (%v, %d), want (KeyNone, 1)", k, n)
	}
}

func TestDecodeKeyConsumesSequentialInput(t *testing.T) {
	t.Parallel()

	// Three keys in one read, as a terminal delivers them.
	buf := []byte("\x1b[A" + "q" + " ")
	keys := make([]Key, 0, 3)
	for len(buf) > 0 {
		k, _, n := DecodeKey(buf)
		if n == 0 {
			break
		}
		keys = append(keys, k)
		buf = buf[n:]
	}
	if len(keys) != 3 || keys[0] != KeyUp || keys[1] != KeyRune || keys[2] != KeySpace {
		t.Errorf("decoded %v, want [Up Rune Space]", keys)
	}
}

// ------------------------------------------------------------- rendering

func TestRenderToolsAlignsColumns(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	m.Screen = ScreenTools
	m.Toggle("rg")

	var buf bytes.Buffer
	m.Render(&buf)
	out := buf.String()

	if !strings.Contains(out, "[x]") {
		t.Errorf("ticked tool does not show its tick:\n%s", out)
	}
	if !strings.Contains(out, "1 selected: rg") {
		t.Errorf("footer does not report the selection:\n%s", out)
	}
	// The locked keybindings must be discoverable from inside the UI.
	for _, help := range []string{"navigate", "toggle", "search", "install", "esc", "quit"} {
		if !strings.Contains(out, help) {
			t.Errorf("footer omits %q:\n%s", help, out)
		}
	}
}

func TestRenderEmptyFilterIsReadable(t *testing.T) {
	t.Parallel()

	m := New(fixtureCatalog(t), testHost())
	m.Screen = ScreenTools
	m.SetFilter("nothing-matches-this")

	var buf bytes.Buffer
	m.Render(&buf)
	if !strings.Contains(buf.String(), "no tools match") {
		t.Errorf("an empty result set is not explained:\n%s", buf.String())
	}
}

// ---------------------------------------------------------------- structure

// TestNoInstallerImport enforces hard constraint 3: the TUI must not be able
// to install anything.
//
// It parses the imports rather than grepping, because a comment mentioning
// the package is exactly as likely as an import and only one of them is a
// violation. A text search here would either miss a real import behind a
// build tag or fail on the documentation.
func TestNoInstallerImport(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	forbidden := []string{
		// Constraint 3: nothing that changes the machine.
		"github.com/quangdang46/agents_environment_setup/internal/installer",
		"github.com/quangdang46/agents_environment_setup/internal/exec",
		"github.com/quangdang46/agents_environment_setup/internal/state",
		"github.com/quangdang46/agents_environment_setup/internal/envgen",
		// Constraint 2: the resolver is reached only through plan. Calling
		// it directly would produce identical output for identical input,
		// so no behavioural test could catch it — the divergence would
		// appear later, when the two paths start to differ. This check is
		// the only thing that actually holds the line.
		"github.com/quangdang46/agents_environment_setup/internal/resolver",
	}
	// The TUI may read the catalog and the manifest, because listing tools
	// is what it does. Everything else is off limits.

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			for _, bad := range forbidden {
				if p == bad {
					reason := "the TUI must not be able to change the machine directly"
					if strings.HasSuffix(bad, "/resolver") {
						reason = "the resolver is reached through plan only, so the TUI and CLI cannot diverge"
					}
					t.Errorf("%s imports %s: %s", name, bad, reason)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files were checked; the guard is vacuous")
	}
}

// TestNoFreeFormCommandEntry enforces hard constraint 1 at the source
// level: there must be no code path that reads a command line from the user
// and hands it to a shell.
func TestNoFreeFormCommandEntry(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("model.go")
	if err != nil {
		t.Fatalf("read model.go: %v", err)
	}
	src := string(data)
	for _, forbidden := range []string{"exec.Command", "os/exec", "sh -c", "system("} {
		if strings.Contains(src, forbidden) {
			t.Errorf("model.go references %q; the TUI must never accept or run a free-form command", forbidden)
		}
	}
}

// TestLogicFilesDoNotShellOut is the precise version of the guard.
//
// The earlier version banned os/exec across the whole package, which was
// wrong: the terminal driver legitimately runs `stty` to enter and leave raw
// mode, and that is terminal control, not install logic. Banning it there
// would have pushed a termios ioctl into the codebase for no benefit.
//
// What actually matters is that the TUI's *logic* — the model, the key
// decoding, the rendering — never shells out, and that the one place that
// does can only run a fixed command.
func TestLogicFilesDoNotShellOut(t *testing.T) {
	t.Parallel()

	logic := []string{"model.go", "keys.go", "render.go"}
	for _, name := range logic {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, forbidden := range []string{`"os/exec"`, "exec.Command("} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("%s references %q; the TUI's logic must never shell out", name, forbidden)
			}
		}
	}
}

// TestTerminalDriverRunsOnlyFixedCommands is the other half. The driver may
// run a subprocess for raw mode, but every invocation must name a literal
// binary — a command assembled from anything the user typed would be free-form
// command entry wearing a disguise, which is hard constraint 1.
func TestTerminalDriverRunsOnlyFixedCommands(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("term.go")
	if err != nil {
		t.Fatalf("read term.go: %v", err)
	}
	src := string(data)

	// Every exec.Command must be followed by a quoted string literal.
	for _, m := range regexp.MustCompile(`exec\.Command\(([^,)]*)`).FindAllStringSubmatch(src, -1) {
		arg := strings.TrimSpace(m[1])
		if !strings.HasPrefix(arg, `"`) {
			t.Errorf("exec.Command called with a non-literal %q; the TUI must only run fixed commands", arg)
		}
	}

	// And the only binary it runs is stty.
	for _, m := range regexp.MustCompile(`exec\.Command\("([^"]+)"`).FindAllStringSubmatch(src, -1) {
		if m[1] != "stty" {
			t.Errorf("the TUI runs %q; the only subprocess it should ever run is stty", m[1])
		}
	}
}

// The catalog the model exposes must be usable, proving the fixtures the
// other tests rely on are real.
func TestModelCatalogIsUsable(t *testing.T) {
	t.Parallel()

	c := fixtureCatalog(t)
	m := New(c, testHost())
	if m.Catalog().Len() == 0 {
		t.Fatal("model has an empty catalog")
	}
	if _, ok := m.Catalog().ByName("rg"); !ok {
		t.Error("catalog is missing a fixture tool")
	}
	var _ []*manifest.Tool = m.All
}

// ---------------------------------------------------------- keybindings

// The bindings are locked by the spec, so they are tested as transitions
// rather than as bytes: apply() is the whole table.
func TestKeybindings(t *testing.T) {
	t.Parallel()

	t.Run("space toggles the highlighted tool", func(t *testing.T) {
		t.Parallel()
		m := New(fixtureCatalog(t), testHost())
		m.Screen = ScreenTools
		name := m.Tools[0].Name
		apply(m, KeySpace, 0)
		if !m.Selected[name] {
			t.Errorf("space did not tick %s", name)
		}
		apply(m, KeySpace, 0)
		if m.Selected[name] {
			t.Errorf("space did not untick %s", name)
		}
	})

	t.Run("arrows move the cursor and clamp", func(t *testing.T) {
		t.Parallel()
		m := New(fixtureCatalog(t), testHost())
		m.Screen = ScreenTools
		apply(m, KeyDown, 0)
		apply(m, KeyDown, 0)
		if m.Cursor != 2 {
			t.Errorf("cursor = %d, want 2", m.Cursor)
		}
		for i := 0; i < 100; i++ {
			apply(m, KeyUp, 0)
		}
		if m.Cursor != 0 {
			t.Errorf("cursor = %d, want 0", m.Cursor)
		}
	})

	t.Run("enter enters a menu screen", func(t *testing.T) {
		t.Parallel()
		m := New(fixtureCatalog(t), testHost())
		m.Cursor = 1 // Tools
		apply(m, KeyEnter, 0)
		if m.Screen != ScreenTools {
			t.Errorf("Screen = %v, want Tools", m.Screen)
		}
	})

	t.Run("esc clears the filter before leaving the screen", func(t *testing.T) {
		t.Parallel()
		// One key should not discard a filter the user may want back.
		m := New(fixtureCatalog(t), testHost())
		m.Screen = ScreenTools
		m.SetFilter("sea")
		apply(m, KeyEsc, 0)
		if m.Screen != ScreenTools {
			t.Errorf("the first esc left the Tools screen (filter was %q)", m.Filter)
		}
		if m.Filter != "" {
			t.Errorf("the first esc did not clear the filter: %q", m.Filter)
		}
		apply(m, KeyEsc, 0)
		if m.Screen != ScreenMenu {
			t.Errorf("the second esc did not go back, Screen = %v", m.Screen)
		}
	})

	t.Run("q quits from the menu", func(t *testing.T) {
		t.Parallel()
		m := New(fixtureCatalog(t), testHost())
		apply(m, KeyRune, 'q')
		if !m.Quit {
			t.Error("q did not quit from the menu")
		}
	})

	// Without this you cannot search for any tool containing a q.
	t.Run("q types a letter while a search is active", func(t *testing.T) {
		t.Parallel()
		m := New(fixtureCatalog(t), testHost())
		m.Screen = ScreenTools
		m.SetFilter("s")
		apply(m, KeyRune, 'q')
		if m.Quit {
			t.Error("q quit while a search was active; it should have been typed")
		}
		if !strings.Contains(m.Filter, "q") {
			t.Errorf("q was not typed into the filter: %q", m.Filter)
		}
	})
}

// Package tui is the human frontend: bare `aes` opens it.
//
// # What it is allowed to do
//
// Collect a selection, call the core, display the result. That is the whole
// job, and the constraints below exist to stop it becoming a second
// implementation of aes.
//
//  1. No free-form command entry. There is no prompt that accepts a shell
//     string, because a manifest is data and never a program (I3). The
//     catalog is the only vocabulary.
//  2. No fast path that bypasses the resolver. Selection becomes
//     plan.Selection and goes through plan.Resolve — the same call the CLI
//     makes. Invariant I13 is therefore structural: there is no second
//     resolver to compare against.
//  3. No install business logic. The package does not import
//     internal/installer, and a test enforces that, because the temptation
//     to "just run it from here" is exactly how a TUI starts diverging.
//
// # Why it is a pure state machine
//
// The model holds no terminal, reads no input and writes no output. Keys go
// in, a new model and a render instruction come out. That is what makes the
// interesting properties testable on a machine with no TTY — and the TUI's
// only real logic is selection state, so there is very little left that
// genuinely needs one.
package tui

import (
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/plan"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Screen is which view is showing.
type Screen int

const (
	ScreenMenu Screen = iota
	ScreenTools
	ScreenProfiles
	ScreenDoctor
	ScreenEnvironment
)

func (s Screen) String() string {
	switch s {
	case ScreenTools:
		return "Tools"
	case ScreenProfiles:
		return "Profiles"
	case ScreenDoctor:
		return "Doctor"
	case ScreenEnvironment:
		return "Environment"
	default:
		return "Menu"
	}
}

// MenuItem is one row of the main menu.
type MenuItem struct {
	Screen Screen
	Label  string
}

// Menu is the top-level list.
var Menu = []MenuItem{
	{ScreenMenu, "Setup"},
	{ScreenTools, "Tools"},
	{ScreenProfiles, "Profiles"},
	{ScreenDoctor, "Doctor"},
	{ScreenEnvironment, "Environment"},
}

// Model is the entire TUI state. It is a value: copying it is a valid way to
// try a transition and discard it.
type Model struct {
	Screen Screen
	Cursor int

	// Tools holds the catalog, filtered.
	All    []*manifest.Tool
	Tools  []*manifest.Tool
	Filter string
	// Selected is the set of ticked tool names.
	Selected map[string]bool

	// Result holds the message shown after an action completes.
	Result string
	// Err holds an error message shown in place of a crash.
	Err string

	// Quit is set once the user has asked to leave.
	Quit bool

	// OnInstall hands the selection to the core. The TUI does not install
	// anything itself — it cannot, because it must not import the installer
	// — so the host wires this to the same pipeline `aes setup` runs. Nil
	// means Enter on the Tools screen does nothing.
	OnInstall func(plan.Selection)

	catalog *catalog.Catalog
	host    *platform.Host
}

// New builds a model over a catalog and host.
func New(c *catalog.Catalog, h *platform.Host) *Model {
	m := &Model{
		Screen:   ScreenMenu,
		Selected: map[string]bool{},
		catalog:  c,
		host:     h,
	}
	m.All = c.All()
	m.Tools = m.All
	return m
}

// Catalog exposes the catalog for callers that need it. It is read-only by
// convention: nothing in this package mutates it.
func (m *Model) Catalog() *catalog.Catalog { return m.catalog }

// Host exposes the detected host.
func (m *Model) Host() *platform.Host { return m.host }

// MenuCursor reports the highlighted menu row.
func (m *Model) MenuCursor() int { return m.Cursor }

// Toggle ticks or unticks a tool.
//
// It reports whether the name was a real tool, so a caller can complain
// rather than silently ticking something that does not exist.
func (m *Model) Toggle(name string) bool {
	if _, ok := m.catalog.ByName(name); !ok {
		return false
	}
	if m.Selected[name] {
		delete(m.Selected, name)
	} else {
		m.Selected[name] = true
	}
	return true
}

// SelectAllVisible ticks everything currently listed, which is what a user
// means after typing a filter.
func (m *Model) SelectAllVisible() {
	for _, t := range m.Tools {
		m.Selected[t.Name] = true
	}
}

// ClearSelection unticks everything, including tools filtered out of view —
// otherwise "clear" would leave hidden ticks behind.
func (m *Model) ClearSelection() { m.Selected = map[string]bool{} }

// Selection converts the ticks into the shared selection type.
//
// This is the boundary where the TUI stops being a UI. Everything after this
// point is core code the CLI also runs.
func (m *Model) Selection() plan.Selection {
	names := make([]string, 0, len(m.Selected))
	for name := range m.Selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return plan.Selection{Only: names}
}

// Plan is the resolved plan for the current selection.
//
// It goes through plan.Resolve — the same function the CLI uses — so a TUI
// selection and `aes setup --only <selection>` are the same plan by
// construction rather than by agreement.
//
// The return type deliberately does not name resolver.Action. Combined with
// the import guard below, that means the TUI cannot reach the resolver at
// all: not directly, and not by accident through a convenient type.
func (m *Model) Plan() ([]string, error) {
	actions, err := plan.Resolve(m.catalog, m.host, m.Selection())
	if err != nil {
		return nil, err
	}
	// Only the tool names: the TUI displays intent, it does not re-decide it.
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Tool)
	}
	return out, nil
}

// SetFilter narrows the visible tools. An empty filter shows everything.
func (m *Model) SetFilter(f string) {
	m.Filter = f
	m.Tools = m.filtered()
	m.Cursor = 0
}

func (m *Model) filtered() []*manifest.Tool {
	if m.Filter == "" {
		return m.All
	}
	needle := strings.ToLower(m.Filter)
	out := make([]*manifest.Tool, 0, len(m.All))
	for _, t := range m.All {
		if matches(t, needle) {
			out = append(out, t)
		}
	}
	return out
}

// matches reports whether a tool matches a search term, searching the fields
// a user would actually type.
func matches(t *manifest.Tool, needle string) bool {
	if needle == "" {
		return true
	}
	for _, hay := range append([]string{t.Name, t.Description, t.Category}, t.Tags...) {
		if strings.Contains(strings.ToLower(hay), needle) {
			return true
		}
	}
	for _, p := range t.Provides {
		if strings.Contains(strings.ToLower(p), needle) {
			return true
		}
	}
	return false
}

// Move changes the cursor by delta, clamped to the visible rows.
func (m *Model) Move(delta int) {
	m.Cursor += delta
	if n := m.rowCount(); n > 0 {
		if m.Cursor < 0 {
			m.Cursor = 0
		}
		if m.Cursor >= n {
			m.Cursor = n - 1
		}
	} else {
		m.Cursor = 0
	}
}

// rowCount is how many rows the current screen shows.
func (m *Model) rowCount() int {
	switch m.Screen {
	case ScreenTools:
		return len(m.Tools)
	case ScreenMenu, ScreenProfiles:
		return len(Menu)
	default:
		return 0
	}
}

// CurrentTool is the tool under the cursor on the Tools screen, or nil.
func (m *Model) CurrentTool() *manifest.Tool {
	if m.Screen != ScreenTools || len(m.Tools) == 0 {
		return nil
	}
	if m.Cursor < 0 || m.Cursor >= len(m.Tools) {
		return nil
	}
	return m.Tools[m.Cursor]
}

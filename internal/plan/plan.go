// Package plan is the single path from "what the user selected" to "what
// will happen".
//
// It exists so that invariant I13 — the TUI and the CLI must produce the
// same Actions — is a structural property rather than a test that has to be
// remembered. Both frontends build a Selection and call Resolve. There is no
// second implementation to drift, so there is nothing to compare.
//
// This is the same discipline as ssh not configuring its own port forwarding:
// the selection UI collects intent, the core decides.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
)

// Selection is what a user chose: the tools they want, and the ones they
// do not.
type Selection struct {
	// Only names tools to install. A dependency of one of these is pulled in
	// automatically — "dependencies beat selection".
	Only []string
	// Exclude names tools to leave out. Excluding something a selected tool
	// depends on is an error, not a silent omission.
	Exclude []string
	// Profile is the name a profile was expanded from, if any. It is carried
	// for diagnostics only and never affects resolution: the CLI expands a
	// profile into Only before calling here, so the resolver has no knowledge
	// of profile files.
	Profile string
}

// Names returns the selected tool names, sorted and deduplicated.
//
// Sorting is not cosmetic. Two frontends that built the same selection in a
// different order must still produce identical output, and map iteration
// order is the classic way that breaks.
func (s Selection) Names() []string {
	seen := make(map[string]bool, len(s.Only))
	out := make([]string, 0, len(s.Only))
	for _, n := range s.Only {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Empty reports whether the selection names nothing.
func (s Selection) Empty() bool { return len(s.Only) == 0 }

// String renders the selection the way a user would say it, for display.
func (s Selection) String() string {
	names := s.Names()
	if len(names) == 0 {
		return "(nothing selected)"
	}
	return strings.Join(names, ", ")
}

// Resolve turns a selection into ordered, deduplicated Actions.
//
// An empty selection is a no-op rather than an error: pressing Enter in the
// TUI with nothing ticked should do nothing, not fail. An explicitly named
// tool that is not in the catalog IS an error, because that is a typo the
// user needs to know about.
func Resolve(c *catalog.Catalog, h *platform.Host, sel Selection) ([]resolver.Action, error) {
	if c == nil {
		return nil, fmt.Errorf("plan: no catalog")
	}
	if h == nil {
		return nil, fmt.Errorf("plan: no host")
	}
	if sel.Empty() {
		return nil, nil
	}
	for _, name := range sel.Names() {
		if _, ok := c.ByName(name); !ok {
			return nil, fmt.Errorf("plan: no tool named %q in the catalog", name)
		}
	}
	return resolver.Resolve(c, resolver.Request{
		Only:    sel.Names(),
		Exclude: sel.Exclude,
	}, h)
}

// Package resolver turns a request into the exact list of actions to perform.
//
// Resolve is pure. It reads a catalog and a request and returns values; it
// touches no files, spawns no processes, and never consults the state file. That
// is what makes the whole plan testable without a machine, and it is why the
// verifier is the only thing allowed to look at reality.
//
// The pipeline always runs in one order:
//
//	selection -> exclusion -> dependency closure -> cycle check -> platform filter -> Actions
//
// Dependencies beat selection. Asking for one tool pulls in what it needs, and
// excluding something a selected tool needs is an error naming who needs it —
// never a silently broken graph.
//
// # Actions, not plans
//
// The value between the resolver and the installer is an Action. There is no
// plan type anywhere in this package, and there should not be one: the feature
// was cut, and letting it regrow under another name would smuggle it back in.
package resolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// Operation is what an action does to a tool.
type Operation string

const (
	OpInstall Operation = "install"
	// OpUpgrade exists for the update path. Resolve only ever emits OpInstall:
	// an update run decides per tool whether an upgrade is warranted, and that
	// decision belongs to the caller, not here.
	OpUpgrade Operation = "upgrade"
)

var (
	// ErrCycle reports a dependency cycle. A cycle is a catalog bug, and it is
	// reported rather than tolerated — a hang would be indistinguishable from a
	// slow install.
	ErrCycle = errors.New("dependency cycle")

	// ErrUnknownTool reports a name that is not in the catalog.
	ErrUnknownTool = errors.New("unknown tool")

	// ErrExcludedDependency reports an exclusion that would break a selected
	// tool's dependency graph. Silently dropping the dependency would produce
	// an install that fails much later, for a reason with no obvious link back
	// to the --exclude that caused it.
	ErrExcludedDependency = errors.New("required tool was excluded")
)

// Action is a resolved snapshot of one thing to do on one specific host.
//
// It is a value describing what should happen, not a context for carrying
// execution state around. Two consequences are deliberate:
//
//   - Host is a pointer shared across every action in one resolution. Embedding
//     a value would copy the struct per action and make the serialized form
//     noisy for no gain.
//   - The installer consumes this Host and does not re-resolve. If the machine
//     turns out to differ at execution time, that is drift — and drift is
//     doctor's job, not the installer's.
//
// Action carries no slices, so identical input serializes to identical bytes.
type Action struct {
	Tool      string
	Strategy  string
	Operation Operation
	Host      *platform.Host
	// RequiresPrivilege is derived from (Strategy, Manager), never read from
	// YAML. A manifest cannot ask for privilege; it can only name a strategy
	// that happens to need it.
	RequiresPrivilege bool
	// Reason is diagnostic text for doctor: "default", "only:<tool>", or
	// "dependency:<tool>". It is never parsed.
	Reason string
}

// Request is what the user asked for.
type Request struct {
	// Profile names a profile. The resolver does not read profile files — the
	// profile loader (internal/cli) expands a profile into Only before calling
	// here, so this package never has to know what a profile is. The field is
	// kept for the caller to act on and is otherwise unused.
	Profile string
	Only    []string
	Exclude []string
}

// Resolve produces the actions for req against host h.
//
// The returned slice is never nil, so callers can range over it without a nil
// check even when nothing was selected. A tool with no install entry for this
// host is skipped without error: an unsupported platform is a skip, not a
// failure (I12).
func Resolve(c *catalog.Catalog, req Request, h *platform.Host) ([]Action, error) {
	if c == nil {
		return nil, errors.New("resolve: nil catalog")
	}
	if h == nil {
		return nil, errors.New("resolve: nil host")
	}

	excluded, err := exclusionSet(c, req.Exclude)
	if err != nil {
		return nil, err
	}
	roots, err := selectTools(c, req)
	if err != nil {
		return nil, err
	}

	closure, err := dependencyClosure(c, roots, excluded)
	if err != nil {
		return nil, err
	}

	// Build the dependency graph over the closure and order it. Cycle
	// detection runs before the platform filter: a cycle is a defect in the
	// catalog, and reporting it on a platform where the tools happen not to
	// install would only hide it until someone else hit it.
	order, err := TopoSort(dependencyGraph(closure, c))
	if err != nil {
		return nil, err
	}

	actions := make([]Action, 0, len(order))
	for _, name := range order {
		tool, _ := c.ByName(name)
		target, ok := h.Target(tool)
		if !ok {
			continue // I12: this tool does not run here.
		}
		actions = append(actions, Action{
			Tool:              name,
			Strategy:          target.Strategy,
			Operation:         OpInstall,
			Host:              h,
			RequiresPrivilege: requiresPrivilege(target),
			Reason:            closure[name],
		})
	}
	return actions, nil
}

// requiresPrivilege is the whole of the privilege model: a strategy needs root
// or it does not, and the catalog never gets a say (I2).
func requiresPrivilege(t manifest.Target) bool {
	return t.Strategy == manifest.StrategyPackage && t.Manager == manifest.ManagerApt
}

// selectTools picks the roots of the resolution.
//
// An explicit --only replaces the profile rather than adding to it, because
// "install exactly this" is the only reading of a list that is not an
// incremental request.
func selectTools(c *catalog.Catalog, req Request) (map[string]string, error) {
	roots := make(map[string]string)

	if len(req.Only) > 0 {
		for _, name := range dedupe(req.Only) {
			if _, ok := c.ByName(name); !ok {
				return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
			}
			roots[name] = "only:" + name
		}
		return roots, nil
	}

	for _, t := range c.Default() {
		roots[t.Name] = "default"
	}
	return roots, nil
}

// exclusionSet validates the requested exclusions up front so a typo is
// reported before any work happens.
func exclusionSet(c *catalog.Catalog, names []string) (map[string]bool, error) {
	out := make(map[string]bool, len(names))
	for _, name := range dedupe(names) {
		if _, ok := c.ByName(name); !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
		}
		out[name] = true
	}
	return out, nil
}

// dependencyClosure walks out from the roots, returning every tool that has to
// be installed and why.
//
// The traversal is breadth-first over a sorted queue and sorted edge lists, so
// when several tools need the same dependency the recorded reason is always the
// same one. An already-visited node is never revisited, which is what keeps a
// cycle from looping here — the cycle is caught properly by TopoSort afterwards,
// where it can be reported by name.
func dependencyClosure(c *catalog.Catalog, roots map[string]string, excluded map[string]bool) (map[string]string, error) {
	out := make(map[string]string, len(roots))
	for name, reason := range roots {
		out[name] = reason
	}

	queue := make([]string, 0, len(out))
	for name := range out {
		queue = append(queue, name)
	}
	sort.Strings(queue)

	for i := 0; i < len(queue); i++ {
		name := queue[i]
		tool, ok := c.ByName(name)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
		}
		for _, dep := range sortedCopy(tool.Dependencies) {
			if excluded[dep] {
				// Name the tool that wants it, or the user is left guessing
				// which of their exclusions caused the failure.
				return nil, fmt.Errorf("%w: %q is required by %q", ErrExcludedDependency, dep, name)
			}
			if _, seen := out[dep]; seen {
				continue
			}
			out[dep] = "dependency:" + name
			queue = append(queue, dep)
		}
	}
	return out, nil
}

// dependencyGraph extracts the edge set TopoSort needs from a closure. The
// lookup is by name, so a tool missing from the catalog yields no edges rather
// than a nil entry — TopoSort still emits the name, and the closure above is
// where a missing tool is reported.
func dependencyGraph(closure map[string]string, c *catalog.Catalog) map[string][]string {
	graph := make(map[string][]string, len(closure))
	for name := range closure {
		tool, ok := c.ByName(name)
		if !ok {
			graph[name] = nil
			continue
		}
		graph[name] = sortedCopy(tool.Dependencies)
	}
	return graph
}

// dedupe returns names with repeats removed, order preserved.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sortedCopy returns a sorted copy, leaving the caller's slice untouched.
// Manifests are shared across resolutions, so sorting one in place would make
// the result depend on what ran before.
func sortedCopy(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

// String renders the actions for diagnostics, one per line.
func String(actions []Action) string {
	var b strings.Builder
	for _, a := range actions {
		fmt.Fprintf(&b, "%-20s %-16s %-8s priv=%-5t %s\n",
			a.Tool, a.Strategy, a.Operation, a.RequiresPrivilege, a.Reason)
	}
	return b.String()
}

// JSON renders actions in their canonical form. Two resolutions of the same
// request must produce identical bytes (I8), which is what makes the
// determinism testable from outside the package.
func JSON(actions []Action) ([]byte, error) {
	if actions == nil {
		actions = []Action{}
	}
	return json.Marshal(actions)
}

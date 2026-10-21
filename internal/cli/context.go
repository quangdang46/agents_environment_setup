package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
	"github.com/quangdang46/agents_environment_setup/internal/selection"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// context is the resolved world a command operates on: a catalog, a host,
// and the flags for this run.
//
// It is built once per invocation and shared, so every command in a run
// agrees on what the catalog is and which host it is running against. A
// command that re-derived any of this would be one more place for the TUI and
// the CLI to disagree (I13).
type runContext struct {
	App     *App
	Flags   *Flags
	Catalog *catalog.Catalog
	Host    *platform.Host
}

// resolve loads the catalog and detects the host.
//
// Both failures are the caller's to report, not this function's to soften: a
// corrupt catalog is exit 3 and an unsupported platform is exit 4, and
// collapsing either into a generic failure would break the exit contract.
func (a *App) resolve(f *Flags) (*runContext, error) {
	cat, err := catalog.Load(a.CatalogRoot)
	if err != nil {
		return nil, err
	}
	host, err := platform.Current()
	if err != nil {
		return nil, err
	}
	return &runContext{App: a, Flags: f, Catalog: cat, Host: host}, nil
}

// target is the machine this run is about, for the verifier.
//
// It exists because `report()` is shared by `aes list`, `aes verify` and
// `aes doctor`, and all three were building the verifier's Target by hand —
// three copies of the same two fields, and one of them (the first) omitted the
// Host. That omission was a real bug, not a style complaint: a tool declaring
// `min: {darwin: 26.0, linux: 18.0}` has no scalar default, so with no host the
// floor resolved to "" and the tool was reported `ok` on presence alone. A
// node 16 binary therefore passed `aes verify node` and exited 0, while
// `aes setup --only node` called the same binary stale and reinstalled it.
//
// One construction, at the one place that already knows both values. The Host
// is load-bearing, not decorative: omitting it here is exactly the bug.
func (rc *runContext) target() verifier.Target {
	return verifier.Target{Host: rc.Host, AESHome: rc.App.Home}
}

// request turns the --only/--exclude flags into a resolver Request.
//
// An empty Only is not special-cased here: the resolver falls back to
// catalog.Default() on its own when nothing is given, so `aes setup` with no
// flags installs the catalog's default tools without this layer having to
// know that.
func (rc *runContext) request() (resolver.Request, error) {
	return resolver.Request{Only: rc.Flags.Only, Exclude: rc.Flags.Exclude}, nil
}

// actions resolves the selection against the catalog and host.
//
// It goes through selection.Resolve — the same entry point the TUI uses — so
// "the TUI and the CLI produce the same Actions" is a property of the code
// rather than a comparison a test has to keep making. The resolver is not
// called directly from here for exactly that reason.
//
// Naming no tool is the CLI's way of saying "the defaults", and it is
// recorded as such: the TUI's empty selection means nothing at all, and the
// two are different intents that must not be re-decided downstream.
func (rc *runContext) actions() ([]resolver.Action, error) {
	req, err := rc.request()
	if err != nil {
		return nil, err
	}
	return selection.Resolve(rc.Catalog, rc.Host, selection.Selection{
		Only:          req.Only,
		Exclude:       req.Exclude,
		Defaults:      len(req.Only) == 0,
		AllowUnproven: rc.Flags != nil && rc.Flags.AllowUnproven,
	})
}

// writeJSON emits v as indented JSON followed by a newline.
//
// Indented rather than compact on purpose: the most common consumer is a
// human running `aes list --json` to see what an agent saw, and one level of
// indentation costs nothing for a machine reader.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// errorPayload is the machine-readable form of a failure.
type errorPayload struct {
	Error    string `json:"error"`
	ExitCode int    `json:"exit_code"`
	// NeedsPrivilege marks the case an agent must act on manually. It is
	// duplicated out of the message so a caller can branch on it without
	// parsing prose — which is the entire reason exit code 5 is distinct.
	NeedsPrivilege bool `json:"needs_privilege,omitempty"`
	// Command names the command to run by hand, when one is known.
	Command string `json:"command,omitempty"`
}

// newErrorPayload builds the machine-readable form of a failure.
func newErrorPayload(message string, code int) *errorPayload {
	return &errorPayload{
		Error:          message,
		ExitCode:       code,
		NeedsPrivilege: code == ExitNeedsPrivilege,
	}
}

// writeJSONError reports a failure on stdout in machine-readable form.
//
// It is used only when a command wrote nothing at all: a command that
// already emitted an envelope carries the error inside it, and a second
// document on stdout would not parse.
func writeJSONError(w io.Writer, err error, code int) {
	_ = writeJSON(w, newErrorPayload(err.Error(), code))
}

// table renders rows as aligned columns.
//
// aes has no third-party dependencies and is not going to acquire one for
// this: the layout is a fixed-width column join, which is what every table
// in the spec's CLI surface shows anyway.
func table(w io.Writer, headers []string, rows [][]string) error {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			if i == len(cells)-1 {
				fmt.Fprint(w, cell)
				break
			}
			fmt.Fprintf(w, "%-*s", widths[i], cell)
		}
		fmt.Fprintln(w)
	}
	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
	return nil
}

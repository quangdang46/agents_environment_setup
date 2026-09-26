package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/profile"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
)

// context is the resolved world a command operates on: a catalog, a host,
// and the profile expansion of the flags.
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

// request turns the profile and the --only/--exclude flags into a resolver
// Request.
//
// A profile is expanded here, into Only, rather than being handed to the
// resolver. That is deliberate: it keeps the resolver's locked
// Resolve(c, req, h) signature free of profile-file knowledge, so a TUI can
// drive the same resolver without loading a single profile file.
func (rc *runContext) request() (resolver.Request, error) {
	req := resolver.Request{Only: rc.Flags.Only, Exclude: rc.Flags.Exclude}

	name := rc.Flags.Profile
	if name == "" {
		// An empty --profile means the shipped default, not "no profile".
		// `aes setup` with no flags doing nothing would make the North
		// Star a lie.
		name = DefaultProfile
	}
	if name == "" {
		return req, nil
	}

	set, err := profile.LoadDir(rc.App.ProfileDir)
	if err != nil {
		return req, err
	}
	p, err := set.Get(name)
	if err != nil {
		return req, err
	}
	tools, err := p.Resolve(rc.Catalog)
	if err != nil {
		return req, err
	}
	for _, t := range tools {
		req.Only = append(req.Only, t.Name)
	}
	return req, nil
}

// DefaultProfile is the profile used when --profile is not given.
const DefaultProfile = "default"

// actions resolves the request against the catalog and host.
func (rc *runContext) actions() ([]resolver.Action, error) {
	req, err := rc.request()
	if err != nil {
		return nil, err
	}
	return resolver.Resolve(rc.Catalog, req, rc.Host)
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

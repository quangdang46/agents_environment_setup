package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/catalog"
	"github.com/quangdang46/agents_environment_setup/internal/envgen"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
	"github.com/quangdang46/agents_environment_setup/internal/state"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// toolReport is one row of `aes list` and `aes verify`.
//
// Status comes from Verify, never from state. That distinction is the whole
// point of the command: a tool whose state says "installed" while its binary
// is gone must show as missing, because state is a cache and the binary is
// the truth (I5). Installed is carried alongside Status precisely so a reader
// can see the disagreement rather than having to go and look.
// listOutput is the single document `aes list` and `aes verify` emit under
// --json. Wrapping the rows rather than emitting a bare array means a failing
// run can carry its error without putting a second document on stdout.
type listOutput struct {
	Tools []toolReport  `json:"tools"`
	Error *errorPayload `json:"error,omitempty"`
}

type toolReport struct {
	Name      string `json:"name"`
	Category  string `json:"category,omitempty"`
	Status    string `json:"status"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Installed bool   `json:"installed"`
	Tested    bool   `json:"tested"`
}

func report(t *manifest.Tool, st *state.Store) toolReport {
	res := verifier.Verify(t)
	row := toolReport{
		Name:     t.Name,
		Category: t.Category,
		Status:   string(res.Status),
		Version:  res.Version,
		Path:     res.Path,
		Tested:   t.Tested,
	}
	if st != nil {
		_, row.Installed = st.Get(t.Name)
	}
	return row
}

// selectTools returns the named tools, or the whole catalog when args is
// empty.
//
// An unknown name is a usage error rather than a skipped row: `aes verify
// ripgreep` should say the name is wrong, not quietly verify nothing and
// exit 0.
func selectTools(c *catalog.Catalog, args []string) ([]*manifest.Tool, error) {
	if len(args) == 0 {
		return c.All(), nil
	}
	out := make([]*manifest.Tool, 0, len(args))
	for _, name := range args {
		t, ok := c.ByName(name)
		if !ok {
			return nil, usagef("no tool named %q", name)
		}
		out = append(out, t)
	}
	return out, nil
}

// openState opens state.json.
//
// A missing file is not an error — a first run on a clean machine has nothing
// installed. A *corrupt* file is, and Open returns one: reading it as an
// empty state is what makes aes reinstall everything on the machine (I9).
func openState(app *App) (*state.Store, error) {
	return state.Open(filepath.Join(app.Home, "state.json"))
}

// applicable returns the tools that declare an install recipe for this host.
//
// Unsupported platform is a skip, not a failure (I12): the catalog is shared
// across platforms, so "not here" is the normal case for any given tool.
func applicable(c *catalog.Catalog, h *platform.Host, args []string) ([]*manifest.Tool, error) {
	tools, err := selectTools(c, args)
	if err != nil {
		return nil, err
	}
	out := make([]*manifest.Tool, 0, len(tools))
	for _, t := range tools {
		if h.Supports(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func runList(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	rc, err := app.resolve(f)
	if err != nil {
		return err
	}
	st, err := openState(app)
	if err != nil {
		return err
	}
	tools, err := applicable(rc.Catalog, rc.Host, args)
	if err != nil {
		return err
	}

	rows := make([]toolReport, 0, len(tools))
	for _, t := range tools {
		rows = append(rows, report(t, st))
	}

	if f.JSON {
		return writeJSON(app.Out, listOutput{Tools: rows})
	}
	if len(rows) == 0 {
		fmt.Fprintln(app.Out, "no tools apply to this platform")
		return nil
	}

	cells := make([][]string, 0, len(rows))
	for _, r := range rows {
		cells = append(cells, []string{r.Name, r.Category, r.Status, orDash(r.Version), r.Path})
	}
	return table(app.Out, []string{"NAME", "CATEGORY", "STATUS", "VERSION", "PATH"}, cells)
}

// verifyFailure classifies the tools that are not in a good state, so a CI
// consumer can tell the classes apart: a stale tool needs a version bump, a
// missing one needs an install.
type verifyFailure struct {
	Stale   []string
	Missing []string
	// Unknown is present with an undeterminable version. It is reported and
	// is deliberately NOT a failure: a flaky version command must not fail
	// CI, or setup's "one command runs to completion" promise breaks.
	Unknown []string
}

func (e *verifyFailure) Error() string {
	parts := make([]string, 0, 3)
	if len(e.Missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(e.Missing, ", "))
	}
	if len(e.Stale) > 0 {
		parts = append(parts, "stale: "+strings.Join(e.Stale, ", "))
	}
	if len(e.Unknown) > 0 {
		parts = append(parts, "version undeterminable: "+strings.Join(e.Unknown, ", "))
	}
	return strings.Join(parts, "; ")
}

func runVerify(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	rc, err := app.resolve(f)
	if err != nil {
		return err
	}
	st, err := openState(app)
	if err != nil {
		return err
	}
	tools, err := applicable(rc.Catalog, rc.Host, args)
	if err != nil {
		return err
	}

	rows := make([]toolReport, 0, len(tools))
	failure := &verifyFailure{}
	for _, t := range tools {
		r := report(t, st)
		rows = append(rows, r)
		switch verifier.Status(r.Status) {
		case verifier.StatusMissing:
			failure.Missing = append(failure.Missing, r.Name)
		case verifier.StatusStale:
			failure.Stale = append(failure.Stale, r.Name)
		case verifier.StatusUnknown:
			failure.Unknown = append(failure.Unknown, r.Name)
		}
	}

	if f.JSON {
		out := listOutput{Tools: rows}
		if len(failure.Missing) > 0 || len(failure.Stale) > 0 {
			// The error travels inside the document, not after it: stdout
			// must stay a single parseable value, and a CI consumer needs
			// to know which tool failed as much as it needs the rows.
			gate := &verifyGateError{failure: failure}
			out.Error = newErrorPayload(gate.Error(), ExitVerifyFailed)
		}
		if err := writeJSON(app.Out, out); err != nil {
			return err
		}
	} else if len(rows) > 0 {
		cells := make([][]string, 0, len(rows))
		for _, r := range rows {
			cells = append(cells, []string{r.Name, r.Status, orDash(r.Version), r.Path})
		}
		if err := table(app.Out, []string{"NAME", "STATUS", "VERSION", "PATH"}, cells); err != nil {
			return err
		}
	}

	for _, name := range failure.Unknown {
		fmt.Fprintf(app.Err, "aes: warning: %s declares a minimum but its version could not be read\n", name)
	}
	if len(failure.Missing) > 0 || len(failure.Stale) > 0 {
		return &verifyGateError{failure: failure}
	}
	return nil
}

// verifyGateError makes a non-ok verification exit 6, distinct from the code
// for a broken tool or a bad invocation, so `aes verify` works as a CI gate.
type verifyGateError struct{ failure *verifyFailure }

func (e *verifyGateError) Error() string {
	return fmt.Sprintf("verification failed (%s)", e.failure.Error())
}

func (e *verifyGateError) ExitCode() int { return ExitVerifyFailed }

// driftKind classifies a state-versus-reality disagreement.
type driftKind string

const (
	driftNone      driftKind = ""
	driftDrifted   driftKind = "drifted"   // state says installed, binary is gone
	driftOutdated  driftKind = "outdated"  // state says installed, below declared min
	driftUnmanaged driftKind = "unmanaged" // present, but aes does not track it
)

// finding is one line of `aes doctor` output.
type finding struct {
	// Severity is "error", "warning", or "info". Only errors make the
	// command fail; the rest report on a healthy machine without turning it
	// red.
	Severity string    `json:"severity"`
	Kind     driftKind `json:"kind,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Message  string    `json:"message"`
}

// doctorReport is the whole `aes doctor` result.
type doctorReport struct {
	Findings []finding     `json:"findings"`
	OK       bool          `json:"ok"`
	Error    *errorPayload `json:"error,omitempty"`
}

func runDoctor(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	rc, err := app.resolve(f)
	if err != nil {
		return err
	}
	st, err := openState(app)
	if err != nil {
		return err
	}

	rep := doctorReport{OK: true}
	add := func(sev string, kind driftKind, tool, msg string) {
		rep.Findings = append(rep.Findings, finding{Severity: sev, Kind: kind, Tool: tool, Message: msg})
		if sev == "error" {
			rep.OK = false
		}
	}

	for _, t := range rc.Catalog.All() {
		if !rc.Host.Supports(t) {
			continue
		}
		r := report(t, st)
		switch {
		case r.Installed && verifier.Status(r.Status) == verifier.StatusMissing:
			// The signature of drift: state remembers a tool whose binary
			// is gone. --force is the repair.
			add("error", driftDrifted, r.Name,
				"state records it as installed but the binary does not resolve; run: aes setup --force")
		case r.Installed && verifier.Status(r.Status) == verifier.StatusStale:
			add("warning", driftOutdated, r.Name, "installed but below the declared minimum")
		case !r.Installed && verifier.Status(r.Status) == verifier.StatusOK:
			// Informational, not an error: a tool the user installed some
			// other way is not a broken machine.
			add("info", driftUnmanaged, r.Name, "present on this machine but not tracked by aes")
		}
	}
	addEnvFindings(app, rc, add)

	if f.JSON {
		if !rep.OK {
			rep.Error = newErrorPayload("environment has problems", ExitVerifyFailed)
		}
		if err := writeJSON(app.Out, rep); err != nil {
			return err
		}
	} else {
		if len(rep.Findings) == 0 {
			fmt.Fprintln(app.Out, "no problems found")
		}
		for _, fi := range rep.Findings {
			label := string(fi.Kind)
			if fi.Tool != "" {
				label = fi.Tool
			}
			fmt.Fprintf(app.Out, "%-7s %-11s %s\n", fi.Severity, label, fi.Message)
		}
	}

	if !rep.OK {
		return &doctorError{n: countErrors(rep.Findings)}
	}
	return nil
}

func countErrors(findings []finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == "error" {
			n++
		}
	}
	return n
}

// doctorError makes an unhealthy environment exit 6.
//
// Doctor reports and never fixes. There is no --fix in the MVP, and that is
// deliberate: a self-fixing doctor makes changes the user cannot see, which
// is the opposite of what a diagnostic is for.
type doctorError struct{ n int }

func (e *doctorError) Error() string {
	return fmt.Sprintf("%d problem(s) found; drifted tools are repaired with 'aes setup --force'", e.n)
}

func (e *doctorError) ExitCode() int { return ExitVerifyFailed }

// addEnvFindings reports environment-level problems that belong to no single
// tool.
func addEnvFindings(app *App, rc *runContext, add func(string, driftKind, string, string)) {
	binDir := filepath.Join(app.Home, "bin")
	if !pathContains(os.Getenv("PATH"), binDir) {
		add("warning", driftNone, "", fmt.Sprintf(
			"PATH does not contain %s; run: aes env --write", binDir))
	}

	// A tool whose install strategy needs a package manager that is not here
	// cannot be installed. Reporting it up front beats failing mid-setup.
	if rc.Host.Manager == "" {
		switch rc.Host.OS {
		case platform.OSDarwin:
			add("warning", driftNone, "", "no Homebrew installation found; package-managed tools cannot be installed")
		case platform.OSLinux:
			add("warning", driftNone, "", "apt-get not found; package-managed tools cannot be installed")
		}
	}

	// An untested tool in the default profile is the failure invariant I15
	// exists to prevent. The profile loader refuses to resolve one, so
	// reaching here means the profile directory does not exist yet.
	if _, err := os.Stat(app.ProfileDir); err != nil {
		add("info", driftNone, "", fmt.Sprintf("no profiles directory at %s; the default profile is unavailable", app.ProfileDir))
	}
}

// pathContains reports whether dir is an element of the PATH value.
//
// It compares whole elements. A substring test would report
// /home/u/.aes/bin as present when only /home/u/.aes/bin-old is on PATH —
// exactly the kind of false all-clear a diagnostic must not give.
func pathContains(pathValue, dir string) bool {
	if dir == "" {
		return false
	}
	for _, elem := range filepath.SplitList(pathValue) {
		if elem == dir {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// envFlags are the flags specific to `aes env`.
type envFlags struct {
	Write     bool
	LinkShell bool
}

func registerEnvFlags(fs *flag.FlagSet) any {
	e := &envFlags{}
	fs.BoolVar(&e.Write, "write", false, "write ~/.aes/env.sh atomically")
	fs.BoolVar(&e.LinkShell, "link-shell", false, "also append a sourced block to the shell rc, after backing it up")
	return e
}

func runEnv(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	e, _ := extra.(*envFlags)
	if e == nil {
		e = &envFlags{}
	}

	// --link-shell modifies the user's shell startup, so it is never
	// implied: it requires --write too, and the pair is checked before any
	// file is touched.
	if e.LinkShell && !e.Write {
		return usagef("--link-shell requires --write: there is nothing to source otherwise")
	}
	if e.LinkShell && f.DryRun {
		return usagef("--link-shell cannot be combined with --dry-run: it would modify the shell rc")
	}

	rc, err := app.resolve(f)
	if err != nil {
		return err
	}
	tools, err := trackedTools(app, rc)
	if err != nil {
		return err
	}
	envTools := make([]envgen.Tool, 0, len(tools))
	for _, t := range tools {
		envTools = append(envTools, envgen.Tool{Name: t.Name, Strategy: strategyFor(rc, t)})
	}

	home := homeOf(app)
	envPath := filepath.Join(app.Home, "env.sh")

	if !e.Write {
		content, err := envgen.Generate(envTools, home)
		if err != nil {
			return err
		}
		_, err = app.Out.Write(content)
		return err
	}

	if err := envgen.Write(envPath, envTools, home); err != nil {
		return err
	}
	fmt.Fprintf(app.Err, "aes: wrote %s\n", envPath)

	if e.LinkShell {
		return linkShellRC(app, envPath)
	}
	return nil
}

// homeOf returns the user's home directory, which is distinct from AES_HOME:
// tool destinations live under the real home, not under ~/.aes.
func homeOf(app *App) string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return app.Home
}

// strategyFor reports the strategy a tool would install with on this host,
// which is what selects its PATH destination.
func strategyFor(rc *runContext, t *manifest.Tool) string {
	target, ok := rc.Host.Target(t)
	if !ok {
		return ""
	}
	return target.Strategy
}

// trackedTools returns the catalog tools aes is tracking, sorted by name.
func trackedTools(app *App, rc *runContext) ([]*manifest.Tool, error) {
	st, err := openState(app)
	if err != nil {
		return nil, err
	}
	installed := st.Installed()
	out := make([]*manifest.Tool, 0, len(installed))
	for name := range installed {
		if t, ok := rc.Catalog.ByName(name); ok {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// shellBlockStart and shellBlockEnd delimit what aes owns inside a shell rc.
// The rc is only ever modified between them; everything outside is the
// user's and stays byte-identical.
const (
	shellBlockStart = "# >>> aes env >>>"
	shellBlockEnd   = "# <<< aes env <<<"
)

// linkShellRC appends a sourced block to the user's shell rc.
//
// The order is the contract: identify the rc, back it up FIRST, check for an
// existing block, then append. A backup taken after the edit is not a backup.
func linkShellRC(app *App, envPath string) error {
	rcPath, err := shellRCPath()
	if err != nil {
		return err
	}

	existing, err := os.ReadFile(rcPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("aes: read %s: %w", rcPath, err)
	}
	if bytesContain(existing, shellBlockStart) {
		// Idempotent: a second run must not append a duplicate block.
		fmt.Fprintf(app.Err, "aes: %s already sources aes env; nothing to do\n", rcPath)
		return nil
	}

	backup := rcPath + ".aes-backup"
	if err := os.WriteFile(backup, existing, 0o600); err != nil {
		return fmt.Errorf("aes: back up %s: %w", rcPath, err)
	}
	fmt.Fprintf(app.Err, "aes: backed up %s to %s\n", rcPath, backup)

	f, err := os.OpenFile(rcPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("aes: open %s: %w", rcPath, err)
	}
	defer f.Close()

	// The guard means a deleted env.sh leaves the shell working rather than
	// erroring on every command.
	block := fmt.Sprintf("\n%s\n[ -f %s ] && . %s\n%s\n",
		shellBlockStart, shellSingleQuote(envPath), shellSingleQuote(envPath), shellBlockEnd)
	if _, err := f.WriteString(block); err != nil {
		return fmt.Errorf("aes: write %s: %w", rcPath, err)
	}
	fmt.Fprintf(app.Err, "aes: %s now sources aes env (restore with: cp %s %s)\n",
		rcPath, backup, rcPath)
	return nil
}

// shellRCPath returns the rc file for the user's login shell.
func shellRCPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("aes: cannot determine home directory: %w", err)
	}
	shell := os.Getenv("SHELL")
	switch {
	case strings.HasSuffix(shell, "zsh"):
		return filepath.Join(home, ".zshrc"), nil
	case strings.HasSuffix(shell, "bash"):
		return filepath.Join(home, ".bashrc"), nil
	default:
		// Refusing beats guessing: appending to the wrong rc breaks a shell
		// the user did not ask us to touch.
		return "", fmt.Errorf("aes: cannot tell which rc to use from SHELL=%q; supported: bash, zsh", shell)
	}
}

func bytesContain(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}

// shellSingleQuote renders s as a single-quoted POSIX shell word.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

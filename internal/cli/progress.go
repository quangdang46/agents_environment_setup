package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/progress"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// openRunLog opens the run's log file under $AES_HOME/logs and tees app.Err
// into it, so everything on stderr is also on disk for post-mortem. It
// returns a restore function.
//
// The log is observability, never state: nothing reads it back, and a
// dry run creates nothing at all. A failure to open the log is reported to
// the user and swallowed — the run must not fail because its observability
// did.
func openRunLog(app *App, f *Flags, path *string) (restore func(), ok bool) {
	if f.DryRun {
		return func() {}, true
	}
	dir := filepath.Join(app.Home, "logs")
	name := "setup-" + time.Now().UTC().Format("20060102T150405Z") + ".log"
	full := filepath.Join(dir, name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: could not create the log directory: %v\n", err)
		return func() {}, true
	}
	file, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(app.Err, "aes: warning: could not open the run log: %v\n", err)
		return func() {}, true
	}
	*path = full

	prev := app.Err
	app.Err = io.MultiWriter(prev, file)
	return func() {
		app.Err = prev
		file.Close()
	}, true
}

// verboseLogger is the seam through which --verbose reaches the installers:
// anything that can take a log writer implements it. It is asserted with a
// type switch in one place so the registry map stays generic and
// installer.Action stays untouched (I8: verbosity must not change the
// resolved actions).
type verboseLogger interface {
	SetVerboseLog(verbose bool, w io.Writer)
}

func setInstallerVerbosity(app *App, rc *runContext, actions []resolver.Action, f *Flags, pr *progress.Printer) {
	if app.Registry == nil {
		return
	}
	seen := map[string]bool{}
	for _, a := range actions {
		tool, ok := rc.Catalog.ByName(a.Tool)
		if !ok {
			continue
		}
		target, _ := rc.Host.Target(tool)
		if seen[target.Strategy] {
			continue
		}
		seen[target.Strategy] = true
		inst, err := app.Registry.Get(target.Strategy)
		if err != nil {
			continue
		}
		if vl, ok := inst.(verboseLogger); ok {
			vl.SetVerboseLog(f.Verbose, app.Err)
		}
	}
	// The package installer prints its privilege prompts to its own Out,
	// which defaults to the process stderr and bypasses App.Err — so without
	// this, the prompts would miss both the run log and --quiet.
	if inst, err := app.Registry.Get(manifest.StrategyPackage); err == nil {
		if pi, ok := inst.(*installer.PackageInstaller); ok && pi.Out == nil {
			pi.Out = app.Err
		}
	}
	_ = pr
}

// runSmoke re-verifies every installed-or-present tool and prints one styled
// line each. It is confirmation, never a second verdict: a disagreement with
// the install outcome is at most a warning, and the status is left alone.
func runSmoke(ctx context.Context, app *App, rc *runContext, pr *progress.Printer, results []toolResult) []smokeCheck {
	out := make([]smokeCheck, 0, len(results))
	for _, r := range results {
		if r.Status != outcomeInstalled && r.Status != outcomePresent {
			continue
		}
		tool, ok := rc.Catalog.ByName(r.Name)
		if !ok {
			continue
		}
		res := verifier.VerifyIn(verifier.Target{Host: rc.Host, AESHome: app.Home}, tool)
		check := smokeCheck{Name: r.Name, OK: res.Status == verifier.StatusOK}
		if check.OK {
			check.Detail = displayVersion(res.Version)
			// The marker is the printer's, not the message's: Success
			// already prints ✓, and a second ✅ in the text reads as a
			// defect in the first.
			pr.Success("%s %s", r.Name, check.Detail)
		} else {
			check.Detail = fmt.Sprintf("install reported %s but the binary does not verify (%s)", r.Status, res.Status)
			pr.Warn("%s: %s", r.Name, check.Detail)
		}
		out = append(out, check)
	}
	return out
}

// printDryRunPlan prints what would run and why, in resolved order. It is a
// preview of the pipeline's own plan — the same Actions the run would
// execute — not a new product surface.
//
// Under --json the plan goes to stderr: stdout must remain exactly one JSON
// document, and human prose there is what broke the envelope in the first
// place. This is the same stderr-only rule the progress printer follows.
func printDryRunPlan(app *App, actions []resolver.Action, rc *runContext, toJSON bool) {
	out := app.Out
	if toJSON {
		out = app.Err
	}
	fmt.Fprintln(out, "dry run — nothing will be installed")
	fmt.Fprintf(out, "\n%d tools selected", len(actions))
	if name := rc.Flags.Profile; name != "" {
		fmt.Fprintf(out, " (profile: %s)", name)
	}
	fmt.Fprintln(out)

	cells := make([][]string, 0, len(actions))
	for i, a := range actions {
		tool, ok := rc.Catalog.ByName(a.Tool)
		if !ok {
			cells = append(cells, []string{fmt.Sprintf("%d.", i+1), a.Tool, "?", "(not in catalog)"})
			continue
		}
		target, _ := rc.Host.Target(tool)
		coord := coordinateOf(tool, target)
		reason := ""
		if a.Reason != "" {
			reason = "(" + a.Reason + ")"
		}
		cells = append(cells, []string{
			fmt.Sprintf("%d.", i+1),
			"[" + tool.Category + "]",
			a.Tool,
			"-> " + coord,
			reason,
		})
	}
	_ = table(out, []string{"", "CATEGORY", "TOOL", "INSTALL", "WHY"}, cells)
	fmt.Fprintln(out, "\nThis is a preview. Run without --dry-run to execute.")
}

// coordinateOf names the pinned coordinate a strategy would install, for the
// dry-run plan. It reads the same fields the installer reads —
// PackageCoordinate is the validator-checked accessor both share — so the
// preview cannot drift from the run.
func coordinateOf(tool *manifest.Tool, target manifest.Target) string {
	switch target.Strategy {
	case manifest.StrategyPackage:
		return fmt.Sprintf("package(%s) %s", target.Manager, target.Package)
	case manifest.StrategyGithubRelease:
		return target.Repository + "@" + target.ReleaseTag
	default:
		if coord := target.PackageCoordinate(); coord != "" {
			return fmt.Sprintf("%s %s", target.Strategy, coord)
		}
		return target.Strategy
	}
}

// failedToolNames lists the tools whose outcome was failure, in the order
// they appear in the results. The resume command's --only set is exactly
// this list.
func failedToolNames(results []toolResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if r.Status == outcomeFailed {
			out = append(out, r.Name)
		}
	}
	return out
}

// buildResumeCmd returns the exact command that reinstalls only what failed.
// The --only names come exclusively from the failed results — never widened,
// never re-derived from the original flags — so the hint cannot install
// something the run did not resolve.
func buildResumeCmd(f *Flags, failed []string, needsPriv bool) string {
	if len(failed) == 0 && !needsPriv {
		return ""
	}
	var b strings.Builder
	b.WriteString("aes setup")
	if f.NonInteractive {
		b.WriteString(" --non-interactive")
	}
	if f.Profile != "" {
		b.WriteString(" --profile " + f.Profile)
	}
	for _, name := range failed {
		b.WriteString(" --only " + name)
	}
	for _, x := range f.Exclude {
		b.WriteString(" --exclude " + x)
	}
	if f.AllowUnproven {
		b.WriteString(" --allow-unproven")
	}
	if f.Force {
		b.WriteString(" --force")
	}
	return b.String()
}

// failureBuckets groups failures for the report, derived from the error
// text — reporting only, so no manifest field and no resolver logic is
// involved. Every failure here affects the exit code; there is no "optional"
// bucket because nothing optional fails a run.
func failureBuckets(results []toolResult) []failureBucket {
	groups := map[string][]string{}
	order := []string{}
	// Sorted at the end rather than kept in first-seen order, so the report's
	// shape depends on the failure kinds and not on which tool failed first
	// — the same property I8 gives the resolved actions.
	add := func(bucket, name string) {
		if _, ok := groups[bucket]; !ok {
			order = append(order, bucket)
		}
		groups[bucket] = append(groups[bucket], name)
	}
	for _, r := range results {
		if r.Status != outcomeFailed {
			continue
		}
		switch {
		case r.NeedsPrivilege:
			add("privileged — needs a human", r.Name)
		case isNetworkError(r.Error):
			add("network/download — likely transient, re-run first", r.Name)
		case strings.Contains(r.Error, "installed but verification says"):
			add("installed but unverifiable", r.Name)
		default:
			add("other", r.Name)
		}
	}
	out := make([]failureBucket, 0, len(order))
	sort.Strings(order)
	for _, name := range order {
		out = append(out, failureBucket{Name: name, Tools: groups[name]})
	}
	return out
}

type failureBucket struct {
	Name  string
	Tools []string
}

// isNetworkError matches the errors the retry layer also treats as
// transient: downloads, statuses, timeouts, and exhausted retries.
func isNetworkError(s string) bool {
	lower := strings.ToLower(s)
	for _, sub := range []string{"download", "unexpected status", "timeout", "retry", "connection", "tls", "certificate", "dns", "eof", "reset by peer", "429", " 5"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

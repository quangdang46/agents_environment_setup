package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/envgen"
	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/resolver"
	"github.com/quangdang46/agents_environment_setup/internal/state"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// toolOutcome is what happened to one tool during setup.
type toolOutcome string

const (
	outcomePresent   toolOutcome = "present" // already verified before install
	outcomeInstalled toolOutcome = "installed"
	outcomeSkipped   toolOutcome = "skipped" // not supported on this host
	outcomeFailed    toolOutcome = "failed"
	outcomeDryRun    toolOutcome = "dry-run"
)

// toolResult records one tool's journey through the pipeline.
type toolResult struct {
	Name   string      `json:"name"`
	Status toolOutcome `json:"status"`
	Error  string      `json:"error,omitempty"`
	// NeedsPrivilege marks a tool blocked on a manual privileged step, which
	// is what turns a run's exit code into 5.
	NeedsPrivilege bool `json:"needs_privilege,omitempty"`
	// Command is the exact command a human should run, when one is known.
	Command string `json:"command,omitempty"`
	Version string `json:"version,omitempty"`
}

// summaryOutput is what `aes setup` prints. It is the last thing a user
// reads, so it states plainly what happened and never implies more than did.
type summaryOutput struct {
	DryRun    bool         `json:"dry_run,omitempty"`
	Results   []toolResult `json:"results"`
	Installed int          `json:"installed"`
	Present   int          `json:"present"`
	Skipped   int          `json:"skipped"`
	Failed    int          `json:"failed"`
	// EnvPath is where env.sh landed, or "" when none was written.
	EnvPath string `json:"env_path,omitempty"`
	// NeedsPrivilege is true when a manual privileged step is outstanding.
	NeedsPrivilege bool          `json:"needs_privilege,omitempty"`
	OK             bool          `json:"ok"`
	Error          *errorPayload `json:"error,omitempty"`
}

// setupError carries the exit code a finished run should return.
//
// setup continues past a failed tool by design, so the failure cannot be
// returned as an ordinary error at the point it happens — it is accumulated
// and turned into an exit code at the end.
type setupError struct{ code int }

func (e *setupError) Error() string {
	switch e.code {
	case ExitVerifyFailed:
		return "one or more tools did not verify; run 'aes doctor' for detail"
	case ExitNeedsPrivilege:
		return "a privileged step must be run by hand; see the commands above"
	default:
		return "setup did not complete"
	}
}

func (e *setupError) ExitCode() int { return e.code }

// runSetup is the North Star: one command from nothing to a complete,
// verified environment, exiting 0 only when that is actually true.
//
// The order below is the contract, not an implementation detail. In
// particular, state is written only for tools that passed a post-install
// verification, and the pre-install verification is what makes a re-run
// resume rather than restart.
func runSetup(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	// 1-3: detect, load the profile, resolve.
	rc, err := app.resolve(f)
	if err != nil {
		// A catalog or manifest failure aborts immediately: nothing can
		// proceed, and a partial run would install an arbitrary subset.
		return err
	}
	actions, err := rc.actions()
	if err != nil {
		return err
	}

	// A profile that resolves to nothing is a failure, not a quiet success.
	if len(actions) == 0 {
		return usagef("nothing to install: the resolved profile selected no tools on this platform")
	}

	// state is a cache, opened once. A corrupt file is an error here and
	// aborts the run — treating corruption as "nothing installed" is what
	// causes a reinstall storm.
	store, err := openState(app)
	if err != nil {
		return err
	}

	results := make([]toolResult, 0, len(actions))
	for _, a := range actions {
		results = append(results, setupOne(ctx, app, rc, store, a, f))
	}

	sum := summarise(results)
	sum.DryRun = f.DryRun

	// Persist the cache. This is easy to forget and the symptom is subtle:
	// within a single run the in-memory store still knows everything, so
	// idempotence tests pass and the run looks healthy — but the next
	// process starts blind, and doctor reports every tool as unmanaged.
	if !f.DryRun {
		if err := store.Save(); err != nil {
			return fmt.Errorf("aes: could not write state: %w", err)
		}
	}

	// 8: generate the environment. This is best-effort in the sense that a
	// failure to write env.sh is reported, not fatal: the tools are already
	// installed, and refusing to finish would leave the user worse off.
	if !f.DryRun {
		envPath, envErr := writeEnv(app, rc, results)
		if envErr != nil {
			fmt.Fprintf(app.Err, "aes: warning: could not write the environment file: %v\n", envErr)
		} else {
			sum.EnvPath = envPath
		}
	}

	sum.OK = sum.Failed == 0 && !sum.NeedsPrivilege
	if f.JSON {
		if !sum.OK {
			code := ExitVerifyFailed
			if sum.NeedsPrivilege {
				code = ExitNeedsPrivilege
			}
			sum.Error = newErrorPayload((&setupError{code: code}).Error(), code)
		}
		if err := writeJSON(app.Out, sum); err != nil {
			return err
		}
	} else {
		printSummary(app, sum)
	}

	if !sum.OK {
		code := ExitVerifyFailed
		if sum.NeedsPrivilege {
			code = ExitNeedsPrivilege
		}
		return &setupError{code: code}
	}
	return nil
}

// setupOne runs the install half of the pipeline for a single action and
// returns its outcome. It never aborts the run: containment is the point.
func setupOne(ctx context.Context, app *App, rc *runContext, store *state.Store, a resolver.Action, f *Flags) toolResult {
	res := toolResult{Name: a.Tool}

	// An action whose tool does not apply to this host is skipped, which is
	// normal rather than an error (I12). The resolver already filters these,
	// so reaching here means a manifest changed under us.
	tool, ok := rc.Catalog.ByName(a.Tool)
	if !ok {
		res.Status, res.Error = outcomeFailed, "tool is not in the catalog"
		return res
	}
	target, supported := rc.Host.Target(tool)
	if !supported {
		res.Status = outcomeSkipped
		return res
	}

	// 4: verify the CURRENT state, before installing anything. This is what
	// makes a re-run resume: a tool that already verifies is not an install
	// candidate, so a run that died at tool 27 does not redo 1-26.
	//
	// The decision is made from verify, never from state.json. State is a
	// cache and can disagree with reality in either direction.
	current := verifier.Verify(tool)
	res.Version = current.Version
	if current.Status == verifier.StatusOK {
		res.Status = outcomePresent
		// A tool that verifies is recorded even if state forgot it, so the
		// cache converges toward the truth.
		if !f.DryRun {
			recordInstalled(store, tool, target, current.Version)
		}
		return res
	}

	// Stale and drifted both mean the action is needed, but --force governs
	// whether a *drift* may be corrected silently.
	drifted := current.Status == verifier.StatusMissing
	if _, recorded := store.Get(a.Tool); recorded && drifted && !f.Force && !f.JSON {
		res.Status = outcomeFailed
		res.Error = "state records it as installed but the binary is gone; re-run with --force"
		return res
	}

	if f.DryRun {
		res.Status = outcomeDryRun
		return res
	}

	// 5: install through the registry, resolved by strategy. A privilege
	// failure is not a tool failure: it means a human has to act, which is
	// exit 5 and a printed command.
	inst, err := app.registryFor(target.Strategy)
	if err != nil {
		res.Status, res.Error = outcomeFailed, err.Error()
		return res
	}
	installErr := inst.Install(ctx, installer.Action{
		Tool:   a.Tool,
		Target: target,
		Host:   rc.Host,
		Reason: a.Reason,
	})
	if installErr != nil {
		var pe *exec.PrivilegeError
		if errors.As(installErr, &pe) {
			res.Status = outcomeFailed
			res.NeedsPrivilege = true
			res.Command = pe.Command
			res.Error = "needs a privileged step"
			return res
		}
		res.Status, res.Error = outcomeFailed, installErr.Error()
		return res
	}

	// 6: verify AGAIN. This gates the state write: a tool that installed but
	// does not verify gets no state entry, because a state entry claiming
	// success is exactly the drift doctor exists to clean up.
	after := verifier.Verify(tool)
	if after.Status != verifier.StatusOK {
		res.Status = outcomeFailed
		res.Version = after.Version
		res.Error = fmt.Sprintf("installed but verification says %s", after.Status)
		return res
	}

	// 7: state, only now.
	res.Status = outcomeInstalled
	res.Version = after.Version
	recordInstalled(store, tool, target, after.Version)
	return res
}

func recordInstalled(store *state.Store, tool *manifest.Tool, target manifest.Target, version string) {
	store.Set(tool.Name, state.Installed{
		Strategy:    target.Strategy,
		Privileged:  installer.RequiresPrivilege(target),
		Version:     version,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// registryFor resolves a strategy, tolerating an app with no registry so
// tests can supply their own.
func (a *App) registryFor(strategy string) (installer.Installer, error) {
	if a.Registry == nil {
		return nil, fmt.Errorf("no installer registry configured")
	}
	return a.Registry.Get(strategy)
}

// writeEnv generates the environment file for everything that is now
// installed or already present.
func writeEnv(app *App, rc *runContext, results []toolResult) (string, error) {
	envTools := make([]envgen.Tool, 0, len(results))
	for _, r := range results {
		if r.Status != outcomeInstalled && r.Status != outcomePresent {
			continue
		}
		tool, ok := rc.Catalog.ByName(r.Name)
		if !ok {
			continue
		}
		envTools = append(envTools, envgen.Tool{Name: r.Name, Strategy: strategyFor(rc, tool)})
	}
	path := filepath.Join(app.Home, "env.sh")
	if err := envgen.Write(path, envTools, homeOf(app)); err != nil {
		return "", err
	}
	return path, nil
}

func summarise(results []toolResult) summaryOutput {
	sum := summaryOutput{Results: results}
	for _, r := range results {
		switch r.Status {
		case outcomeInstalled:
			sum.Installed++
		case outcomePresent:
			sum.Present++
		case outcomeSkipped:
			sum.Skipped++
		case outcomeFailed:
			sum.Failed++
		case outcomeDryRun:
			sum.Skipped++
		}
		if r.NeedsPrivilege {
			sum.NeedsPrivilege = true
		}
	}
	sort.SliceStable(sum.Results, func(i, j int) bool { return sum.Results[i].Name < sum.Results[j].Name })
	return sum
}

// printSummary states plainly what happened. It never implies more success
// than occurred, and it prints the exact command when a human has to act.
func printSummary(app *App, sum summaryOutput) {
	if sum.DryRun {
		fmt.Fprintln(app.Out, "dry run — nothing was installed and nothing was changed")
	} else {
		fmt.Fprintf(app.Out, "installed %d · already present %d · skipped %d · failed %d\n",
			sum.Installed, sum.Present, sum.Skipped, sum.Failed)
	}

	cells := make([][]string, 0, len(sum.Results))
	for _, r := range sum.Results {
		cells = append(cells, []string{r.Name, string(r.Status), r.Error})
	}
	_ = table(app.Out, []string{"TOOL", "STATUS", "DETAIL"}, cells)

	if sum.EnvPath != "" {
		fmt.Fprintf(app.Out, "\nenvironment: %s\n", sum.EnvPath)
		fmt.Fprintf(app.Out, "activate it with:  . %s\n", sum.EnvPath)
	}

	// A manual step is printed before the run exits, because exit 5 means
	// the user has to do something and the whole point is telling them what.
	for _, r := range sum.Results {
		if r.NeedsPrivilege && r.Command != "" {
			fmt.Fprintf(app.Out, "\n%s needs a privileged step. Run this yourself:\n\n    %s\n", r.Name, r.Command)
		}
	}

	if sum.Failed > 0 {
		fmt.Fprintf(app.Out, "\n%d tool(s) did not verify. Run 'aes doctor' for detail.\n", sum.Failed)
	}
}

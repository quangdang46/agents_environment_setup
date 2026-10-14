package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/envgen"
	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/progress"
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
	// Strategy is the install strategy that produced this result. It feeds
	// the failure buckets and the dry-run plan, never the resolver.
	Strategy string `json:"strategy,omitempty"`
	// DurationMs is how long setupOne spent on this tool, in milliseconds.
	// It exists so a slow profile can say which tools dominate the run
	// rather than guessing.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Warnings are things that went wrong without making the tool's outcome
	// wrong: a backup that could not be taken, a restore that could not
	// complete. They are reported rather than swallowed because each one is
	// the reason a future failure will have nothing to roll back to — silent
	// here means discovered during an outage.
	Warning []string `json:"warnings,omitempty"`
}

// smokeCheck records one tool's post-install line. It is confirmation, not a
// verdict: install already decided the tool's status, and a smoke line that
// disagrees is at most a warning.
type smokeCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
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
	NeedsPrivilege bool `json:"needs_privilege,omitempty"`
	// ResumeCommand is the exact command that reinstalls only what failed,
	// derived from the failure set. Present only when something failed.
	ResumeCommand string `json:"resume_command,omitempty"`
	// Smoke holds the post-install pass, one line per tool.
	Smoke []smokeCheck `json:"smoke,omitempty"`
	// LogPath is where the run log landed, or "" when none was written
	// (dry-run never writes one).
	LogPath string `json:"log_path,omitempty"`
	// ElapsedMs is the run's wall time in milliseconds.
	ElapsedMs int64         `json:"elapsed_ms,omitempty"`
	OK        bool          `json:"ok"`
	Error     *errorPayload `json:"error,omitempty"`
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
	// aes installs into $AES_HOME/bin, which is not on the user's PATH until
	// they source env.sh. Without this, every install lands correctly and is
	// then reported as "did not verify" — the verifier resolves through
	// exec.LookPath, which cannot see a directory nobody has added yet. The
	// install worked, the run claimed it failed, and state was never written.
	//
	// Scoped to setup, and deliberately not applied to every command: env.sh
	// is the mechanism by which aes puts these directories on PATH, and
	// silently amending the user's PATH from a program whose whole job is
	// modifying a machine would be the wrong kind of helpful. This is narrower
	// — the run knows where it just put things, so it looks there.
	restore := withBinOnPath(filepath.Join(app.Home, "bin"))
	defer restore()

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

	// A selection that resolves to nothing is a failure, not a quiet success.
	//
	// It is deliberately NOT a usage error. `aes setup --only git` on a machine
	// where git is proven only on linux/arm64 is a well-formed command naming a
	// real tool; exit 2 is documented as "invalid usage / bad flags" and an
	// agent acting on that code rewrites the invocation, which cannot help,
	// because the invocation was never wrong. The spec's exit table has no code
	// for "you asked for something this machine has not verified", and 1 is
	// the honest one: the run could not proceed.
	//
	// The message names what was asked for and what this host is, because the
	// resolver comment claims "doctor can say why the tool is absent" — which
	// leaves a user with a refusal and no explanation until they go looking.
	if len(actions) == 0 {
		return fmt.Errorf("nothing to install: %s",
			explainEmptySelection(f, rc))
	}

	// state is a cache, opened once. A corrupt file is an error here and
	// aborts the run — treating corruption as "nothing installed" is what
	// causes a reinstall storm.
	store, err := openState(app)
	if err != nil {
		return err
	}

	// The run log is observability, never state: it is opened after the
	// state so a corrupt state aborts before anything is created, and it is
	// never read back. A dry run creates no log at all.
	var logPath string
	restoreErr, restoreErrOK := openRunLog(app, f, &logPath)
	if restoreErrOK {
		defer restoreErr()
	}

	pr := progress.New(app.Err, progress.Options{Verbose: f.Verbose, TTY: app.tty(), Quiet: f.Quiet})
	setInstallerVerbosity(app, rc, actions, f, pr)

	profileName := f.Profile
	if profileName == "" {
		profileName = "default"
	}
	pr.Section("aes setup %s — %d tools (profile: %s)", Version, len(actions), profileName)
	if logPath != "" {
		pr.Info("→ logging to %s", logPath)
	}

	if f.DryRun {
		printDryRunPlan(app, actions, rc, f.JSON)
	}
	pr.Section("Installing %d tools", len(actions))

	runStart := time.Now()
	results := make([]toolResult, 0, len(actions))
	for i, a := range actions {
		pr.Step(i+1, len(actions), "Installing %s", a.Tool)
		// The spinner announces work that is actually about to start. It is
		// raised by onInstallStart from inside setupOneInner, immediately
		// before the installer runs, so a tool that is already present —
		// which is most of a re-run — never gets told it is being
		// installed.
		results = append(results, setupOneTimed(ctx, app, rc, store, a, f, pr))
	}

	sum := summarise(results)
	sum.DryRun = f.DryRun
	sum.ElapsedMs = time.Since(runStart).Milliseconds()
	sum.LogPath = logPath

	// The smoke pass re-verifies every installed-or-present tool and prints
	// one styled line each. It is confirmation, never a second verdict: a
	// failure here cannot change a status the install already decided, and
	// is at most a warning (doctor will surface it as drift next run).
	if !f.DryRun && !f.NoSmokeTest {
		pr.Section("Smoke test")
		sum.Smoke = runSmoke(ctx, app, rc, pr, results)
	}

	// The resume command is derived here, once, and consumed by both the
	// JSON envelope and the human report — a value computed in two places
	// will eventually disagree with itself.
	if sum.Failed > 0 {
		sum.ResumeCommand = buildResumeCmd(f, failedToolNames(sum.Results), sum.NeedsPrivilege)
	}

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

	// 9: link the shell rc, so a new shell gets PATH and shortcuts with no
	// further step.
	//
	// This changes the spec's invariant 5 ("no shell modification by
	// default") — deliberately, and on the user's explicit instruction, not
	// quietly. The old opt-in shape meant `aes setup` ended one command
	// short of a working environment, and every extra manual step is a place
	// a beginner fails. The link is guarded and backed up: it sources
	// generated files, never inlines content into the rc, and a second run
	// is a no-op.
	if !f.DryRun && sum.EnvPath != "" {
		if err := linkShellRC(app, sum.EnvPath); err != nil {
			fmt.Fprintf(app.Err, "aes: warning: could not link the shell rc: %v\n", err)
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

// explainEmptySelection says WHY a selection resolved to nothing, in terms the
// user can act on.
//
// The previous message was "the resolved profile selected no tools on this
// platform" — which names neither the tools nor the platform and offers no
// remedy, so a user who typed `aes setup --only git` and got a refusal had to
// go and run `aes doctor` to find out why. The commonest cause is a tool
// proven on another platform, and that case has a remedy worth naming.
func explainEmptySelection(f *Flags, rc *runContext) string {
	here := rc.Host.Key() + "/" + rc.Host.Arch

	if len(f.Only) == 0 {
		return "the resolved profile selected no tools on this platform " +
			"(" + here + "). `aes list` shows which tools this host can install; " +
			"pass --profile or --only to choose explicitly."
	}

	// Name the tools that were dropped for being unproven HERE, which is the
	// case where --allow-unproven is the answer. Anything else (no install
	// target for this OS, say) is named as the general case rather than
	// guessing which it was.
	var unprovenHere, other []string
	for _, name := range f.Only {
		tool, ok := rc.Catalog.ByName(name)
		switch {
		case !ok:
			other = append(other, name)
		case tool.Tested && !rc.Host.ProvenOn(tool):
			unprovenHere = append(unprovenHere, name)
		default:
			other = append(other, name)
		}
	}

	parts := []string{"no tool in this selection can be installed on " + here}
	if len(unprovenHere) > 0 {
		parts = append(parts, "proven only on "+
			strings.Join(provenElsewhere(rc, unprovenHere), ", ")+
			", not on this host: "+strings.Join(unprovenHere, ", ")+
			" — re-run with --allow-unproven to install them anyway")
	}
	if len(other) > 0 {
		parts = append(parts, "not installable here: "+strings.Join(other, ", "))
	}
	return strings.Join(parts, "; ")
}

// provenElsewhere is the set of platforms the named tools WERE proven on,
// which is the thing the user has to weigh against --allow-unproven.
func provenElsewhere(rc *runContext, names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		tool, ok := rc.Catalog.ByName(name)
		if !ok {
			continue
		}
		for _, where := range tool.TestedOn {
			if !seen[where] {
				seen[where] = true
				out = append(out, where)
			}
		}
	}
	sort.Strings(out)
	return out
}

// setupOneTimed wraps setupOneInner with wall-clock timing and progress
// reporting. The body keeps its return sites untouched: timing and
// presentation are the wrapper's job, threaded at the boundary rather than
// through every exit. The line a user sees is derived from the outcome, so a
// new outcome tomorrow gets some line rather than silence.
func setupOneTimed(ctx context.Context, app *App, rc *runContext, store *state.Store, a resolver.Action, f *Flags, pr *progress.Printer) toolResult {
	start := time.Now()
	res := setupOneInner(ctx, app, rc, store, a, f, func() {
		pr.Spin("installing %s", a.Tool)
	})
	res.DurationMs = time.Since(start).Milliseconds()
	reportToolOutcome(pr, res)
	return res
}

// reportToolOutcome prints the one line a tool's outcome deserves. The
// detail stays in the summary: this line answers "what happened", the table
// answers "why".
func reportToolOutcome(pr *progress.Printer, res toolResult) {
	elapsed := formatDuration(time.Duration(res.DurationMs) * time.Millisecond)
	switch res.Status {
	case outcomePresent:
		pr.Success("%s already present (%s)", res.Name, displayVersion(res.Version))
	case outcomeInstalled:
		pr.Success("%s installed (%s, %s)", res.Name, displayVersion(res.Version), elapsed)
	case outcomeSkipped:
		pr.Success("%s skipped (not supported here)", res.Name)
	case outcomeDryRun:
		// The dry-run plan already named this tool; the loop stays silent so
		// the preview is the thing a reader remembers.
	case outcomeFailed:
		if res.NeedsPrivilege {
			pr.Fail("%s failed: needs a privileged step", res.Name)
		} else if res.Error != "" {
			pr.Fail("%s failed: %s", res.Name, firstLine(res.Error))
		} else {
			pr.Fail("%s failed", res.Name)
		}
	}
	for _, w := range res.Warning {
		pr.Warn("%s: %s", res.Name, w)
	}
}

// formatDuration renders a duration the way a human reads a run: seconds
// with one decimal below a minute, minutes above.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}

// displayVersion guards against an empty version reading as "(..)".
func displayVersion(v string) string {
	if v == "" {
		return "unknown version"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// firstLine keeps a failure line to its first sentence: the full error is in
// the summary table, where wrapping is expected.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// setupOneInner runs the install half of the pipeline for a single action
// and returns its outcome. It never aborts the run: containment is the point.
func setupOneInner(ctx context.Context, app *App, rc *runContext, store *state.Store, a resolver.Action, f *Flags, onStart func()) toolResult {
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
	// The strategy is recorded for the failure buckets and the dry-run plan.
	// It is reporting, read from the same Target the installer receives, and
	// never branches on a tool name.
	res.Strategy = target.Strategy

	// 4: verify the CURRENT state, before installing anything. This is what
	// makes a re-run resume: a tool that already verifies is not an install
	// candidate, so a run that died at tool 27 does not redo 1-26.
	//
	// The decision is made from verify, never from state.json. State is a
	// cache and can disagree with reality in either direction.
	current := verifier.VerifyIn(verifier.Target{Host: rc.Host, AESHome: app.Home}, tool)
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
	// Retain the predecessor before anything can replace the binary in place.
	//
	// The strategies differ in how badly they need this. github-release
	// verifies the checksum BEFORE extracting, so a corrupt download never
	// becomes a working install. go install, npm -g, cargo install and uv
	// REPLACE the file, and if the result crashes we have destroyed a working
	// install with no way back. Retaining is a no-op on a first install, so
	// the cost is one file copy on a reinstall and nothing otherwise.
	prevPath := ""
	if dest, ok := app.registry(f).DestinationFor(ctx, target.Strategy); ok && dest != "" {
		binPath := filepath.Join(dest, installNameOf(tool))
		if err := installer.Retain(binPath); err != nil {
			// Failing to retain is worth saying out loud but is not a reason to
			// refuse an install: the user still gets the tool they asked for,
			// and the alternative is turning a backup problem into an outage.
			res.Warning = append(res.Warning, "could not retain the previous binary: "+err.Error())
		} else {
			prevPath = binPath
		}
	}

	// Announce real work, immediately before it starts. Everything above is
	// verification, which is fast and not worth a spinner; a tool that is
	// already present returns before ever reaching here.
	if onStart != nil {
		onStart()
	}
	installErr := inst.Install(ctx, installer.Action{
		Tool: a.Tool,
		// The installed filename comes from the manifest's own verify.command,
		// so the installer and the verifier are reading the same declaration
		// rather than each deriving a name independently. Deriving it twice is
		// how yq ended up installed as yq_darwin_arm64 and unrunnable.
		Name:   installNameOf(tool),
		Target: target,
		Host:   rc.Host,
		Reason: a.Reason,
		// Force is derived from `drifted`, NOT from the --force flag. The flag
		// only decides whether a drift may be corrected; what makes this a
		// repair is that the verifier saw no binary.
		//
		// The distinction matters for package strategies, where the two
		// commands are different. When the binary is gone but the package
		// manager still has the package registered, `apt-get install -y` is
		// a no-op that reports the newest version and restores nothing — so
		// the install has to be told to re-extract. Deriving Force from the
		// flag instead would reinstall every tool on the machine whenever
		// anyone passed --force for one of them.
		Force: drifted,
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
	after := verifier.VerifyIn(verifier.Target{Host: rc.Host, AESHome: app.Home}, tool)
	if after.Status != verifier.StatusOK {
		// A probe that ran and exited non-zero means the binary is BROKEN, not
		// merely unreadable, and the difference decides what happens to the
		// predecessor. StatusUnknown covers both cases; ProbeFailed is what
		// tells them apart.
		//
		// A timeout never sets it. A slow binary is not a broken one, and
		// reverting a working install over a slow probe is how a repair turns
		// into damage — which is the failure this whole mechanism exists to
		// prevent, arriving by the opposite route.
		if prevPath != "" {
			if after.ProbeFailed {
				if restored, rerr := installer.Restore(prevPath); rerr != nil {
					res.Warning = append(res.Warning, "could not restore the previous binary: "+rerr.Error())
					// Nothing came back, so the report says so rather than
					// implying a rollback that did not happen. A failure that
					// claims a restore sends the next reader looking for a
					// backup that was never written.
					res.Error = "the installed binary does not run, and the previous one could not be restored"
				} else if restored {
					res.Error = "the installed binary does not run; the previous one has been restored"
					// Deliberately not "working": the predecessor may well be a
					// stale build, since that is the state in which an ecosystem
					// install overwrites a file at all. Calling it working
					// would be a claim nobody checked.
					res.Warning = append(res.Warning,
						"the installed binary does not run, so the previous one was put back "+
							"(it may be older than what was just installed); reinstall once the cause is fixed")
				} else {
					res.Warning = append(res.Warning,
						"the installed binary does not run and there was no previous one to restore")
				}
			} else {
				// OK, stale, or unknown-but-running: the new binary is not
				// broken, so the predecessor has served its purpose. Leaving it
				// would leave a stale copy nobody can distinguish from truth.
				if derr := installer.Discard(prevPath); derr != nil {
					res.Warning = append(res.Warning, "could not remove the retained binary: "+derr.Error())
				}
			}
		}
		res.Status = outcomeFailed
		res.Version = after.Version
		if res.Error == "" {
			// A binary that cannot be executed at all is a different fact
			// from one that runs and prints the wrong version, and the
			// difference decides what the user does next.
			//
			// Measured on ubuntu:24.04 under Docker Desktop's default seccomp
			// profile: vault 2.1.x installs cleanly through apt and then
			// fails to exec with EPERM ("Operation not permitted", rc 126),
			// because v2's static binary makes a syscall the profile blocks.
			// "installed but verification says unknown" names none of that.
			// The binary is real, the environment is the refusal, and
			// `aes doctor` will keep reporting it until the profile changes.
			switch {
			case after.ProbeError != "" && after.Path != "":
				hint := "the refusal is from the environment (a seccomp profile blocking a syscall this binary needs is the usual cause)"
				// A runtime the binary cannot parse is a different refusal
				// from a runtime that refuses to run it, and the interpreter's
				// version is the one thing a reader can act on.
				//
				// Measured on ubuntu:24.04: pi 1.2.0 installs fine and then
				// dies with "node:module does not provide an export named
				// 'enableCompileCache'" — a node 22 symbol on a node 18 host,
				// because apt's nodejs is what AES's node tool installs. The
				// generic text would have blamed the machine; naming the
				// runtime sends the reader to the real constraint.
				if verifier.LooksLikeRuntimeRefusal(after.ProbeError) {
					hint = "this binary needs a newer interpreter than the one on PATH " +
						"(node/rust/python versions are checked by `aes verify <tool> --json`)"
				}
				res.Error = fmt.Sprintf(
					"installed to %s but it could not be run (%s) — the binary is present, so %s",
					after.Path, after.ProbeError, hint)
			default:
				res.Error = fmt.Sprintf("installed but verification says %s", after.Status)
			}
		}
		return res
	}
	if prevPath != "" {
		// Verified working: the retained copy is now dead weight.
		if derr := installer.Discard(prevPath); derr != nil {
			res.Warning = append(res.Warning, "could not remove the retained binary: "+derr.Error())
		}
	}

	// 7: state, only now.
	res.Status = outcomeInstalled
	res.Version = after.Version
	recordInstalled(store, tool, target, after.Version)
	return res
}

// recordInstalled writes the entry for a tool that is present and verified.
//
// installed_at is PRESERVED when the entry already describes the same
// strategy. Stamping time.Now() unconditionally made the field mean "when aes
// last looked at this", not "when it was installed", and it rewrote
// state.json on every run even when nothing had changed — so a plain `aes
// setup` on an up-to-date machine dirtied a file it had no reason to touch.
// The Layer 2 idempotence check caught it by diffing the sandbox.
//
// When the strategy changes the entry is a different install, so the stamp
// moves. That is the one case where the old value is genuinely wrong.
func recordInstalled(store *state.Store, tool *manifest.Tool, target manifest.Target, version string) {
	installedAt := time.Now().UTC().Format(time.RFC3339)
	if prev, ok := store.Get(tool.Name); ok &&
		prev.Strategy == target.Strategy && prev.InstalledAt != "" {
		installedAt = prev.InstalledAt
	}
	store.Set(tool.Name, state.Installed{
		Strategy:    target.Strategy,
		Privileged:  manifest.RequiresPrivilege(target),
		Version:     version,
		InstalledAt: installedAt,
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
		// Aliases travel from the manifest to the generated file here. The
		// catalog owns the declaration; envgen owns the rendering; this line
		// is the only place the two meet.
		envTools = append(envTools, envgen.Tool{
			Name:     r.Name,
			Strategy: strategyFor(rc, tool),
			Aliases:  tool.Aliases,
		})
	}
	path := filepath.Join(app.Home, "env.sh")
	if err := envgen.Write(path, envTools, app.Home); err != nil {
		return "", err
	}
	// The aliases file is written alongside env.sh, every time env.sh is
	// written. Two generated files at the same moment, from the same tools,
	// or the shortcut a user types lags what the environment declares.
	aliasesPath := filepath.Join(app.Home, envgen.AliasesFileName)
	if err := envgen.WriteAliases(aliasesPath, envTools); err != nil {
		return "", err
	}
	writeToolConfigs(app, rc, results)
	return path, nil
}

// writeToolConfigs copies each installed tool's declared config files into
// ~/.aes/config/<tool>/, then activates the one that has an obvious activation
// path.
//
// Two properties are the whole design:
//
//   - The copy always lands under AES_HOME. Activation is separate and
//     conditional: ~/.tmux.conf is symlinked ONLY when it does not already
//     exist. ACFS does exactly this, and it is the difference between a
//     config that helps and one that overwrites work a user did.
//   - A tool with no config declares none, which is nearly all of them.
//
// Failures are reported and skipped, never fatal: the tools are installed,
// and refusing to finish over a config file leaves the user worse off.
func writeToolConfigs(app *App, rc *runContext, results []toolResult) {
	configDir := filepath.Join(app.Home, "config")
	for _, r := range results {
		if r.Status != outcomeInstalled && r.Status != outcomePresent {
			continue
		}
		tool, ok := rc.Catalog.ByName(r.Name)
		if !ok || (len(tool.Config) == 0 && len(tool.Settings) == 0) {
			continue
		}
		// The source is beside tool.yaml: <category>/<name>/<file>.
		srcDir := filepath.Join(app.CatalogRoot, tool.Category, tool.Name)
		dstDir := filepath.Join(configDir, tool.Name)
		for _, name := range tool.Config {
			src := filepath.Join(srcDir, name)
			data, err := os.ReadFile(src)
			if err != nil {
				fmt.Fprintf(app.Err, "aes: warning: %s declares config %q but %s is unreadable: %v\n",
					tool.Name, name, src, err)
				continue
			}
			if err := os.MkdirAll(dstDir, 0o755); err != nil {
				fmt.Fprintf(app.Err, "aes: warning: could not create %s: %v\n", dstDir, err)
				continue
			}
			dst := filepath.Join(dstDir, name)
			// Not atomic: this is a static asset copied from the same binary
			// that declared it, and a partial write is fixed by the next run.
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				fmt.Fprintf(app.Err, "aes: warning: could not write %s: %v\n", dst, err)
				continue
			}
			activateConfig(app, tool.Name, name, dst)
		}
		// Settings are merged, never copied. The user's file is theirs.
		for _, s := range tool.Settings {
			mergeSetting(app, tool.Name, s)
		}
	}
}

// mergeSetting writes one key into a tool's own config file, only when the
// key is absent.
//
// The contract is ACFS's, and it is the one that makes writing outside
// ~/.aes safe:
//
//   - A symlink is resolved before writing. A dotfiles-managed symlink must
//     be edited in place; replacing it with a regular file orphans the
//     user's dotfiles repo.
//   - The write is atomic: temp file beside the destination, then rename. A
//     crash mid-write must not leave a truncated JSON file.
//   - A key is written only when absent. A value the user set deliberately
//     survives, which is the whole difference between a merge and a copy.
//
// Failures are reported and skipped, never fatal: the tool is installed,
// and refusing to finish over a settings file leaves the user worse off.
func mergeSetting(app *App, tool string, s manifest.Setting) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot merge %s settings: %v\n", tool, err)
		return
	}
	dst := filepath.Join(home, s.Path)

	// Resolve a symlink to its target, and refuse to follow one that points
	// outside the user's home. A symlink into /etc or /tmp is not a file the
	// user owns, and writing through it would write there.
	if target, err := os.Readlink(dst); err == nil {
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(dst), resolved)
		}
		if !strings.HasPrefix(resolved, home+string(os.PathSeparator)) {
			fmt.Fprintf(app.Err, "aes: %s is a symlink to %s, outside the home; left untouched\n", dst, target)
			return
		}
		dst = resolved
	}

	existing, err := os.ReadFile(dst)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(app.Err, "aes: warning: cannot read %s: %v\n", dst, err)
		return
	}

	var doc map[string]any
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			// Not JSON, or not an object. Refusing beats guessing: a
			// settings file that will not parse is a file the user should
			// look at, and silently replacing it is not a recovery.
			fmt.Fprintf(app.Err, "aes: %s is not a JSON object; left untouched "+
				"(aes would merge %d key(s) into it)\n", dst, len(s.Merge))
			return
		}
	} else {
		doc = map[string]any{}
	}

	changed := false
	for k, v := range s.Merge {
		if _, present := doc[k]; present {
			continue
		}
		doc[k] = v
		changed = true
	}
	if !changed {
		return
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot encode merged settings: %v\n", err)
		return
	}
	out = append(out, '\n')

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot create %s: %v\n", dir, err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json.tmp")
	if err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot create temp file in %s: %v\n", dir, err)
		return
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	if _, err := tmp.Write(out); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot write %s: %v\n", tmpName, err)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot chmod %s: %v\n", tmpName, err)
		return
	}
	if err := tmp.Sync(); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot sync %s: %v\n", tmpName, err)
		return
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot close %s: %v\n", tmpName, err)
		return
	}
	if err := os.Rename(tmpName, dst); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: cannot install %s: %v\n", dst, err)
		return
	}
	fmt.Fprintf(app.Err, "aes: merged %d setting(s) into %s\n", len(s.Merge), dst)
}

// activateConfig points a tool's config at its well-known path, but ONLY when
// nothing is there.
//
// The `-e` test is deliberate: an existing file OR an existing symlink both
// count as "the user has an opinion", and `-e` catches the broken-symlink
// case that `-f` would silently overwrite. This is the rule that makes it
// safe for aes to ship a config at all.
func activateConfig(app *App, tool, name, src string) {
	target, ok := configActivation[tool]
	if !ok {
		// No known path: the copy is the deliverable, and the user links it
		// themselves. Saying so is better than a config that quietly does
		// nothing.
		fmt.Fprintf(app.Err, "aes: wrote %s (link it yourself to use it)\n", src)
		return
	}
	dst := filepath.Join(os.Getenv("HOME"), target)
	if _, err := os.Lstat(dst); err == nil {
		fmt.Fprintf(app.Err, "aes: %s already exists; left untouched (aes ships %s)\n", dst, src)
		return
	}
	if err := os.Symlink(src, dst); err != nil {
		fmt.Fprintf(app.Err, "aes: warning: could not link %s: %v\n", dst, err)
		return
	}
	fmt.Fprintf(app.Err, "aes: %s now symlinked to %s\n", dst, src)
}

// configActivation is the ONE place a tool's config gets a well-known path.
// A map rather than logic: a tool that needs activation logic to install a
// config is a tool whose config is doing more than a config file should, and
// it belongs in the catalog review rather than in a switch statement here.
var configActivation = map[string]string{
	"tmux": ".tmux.conf",
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
		// Elapsed goes in the counts line rather than its own footer: a slow
		// run is a number a reader looks for first, and one line saying
		// everything beats two lines half-saying it.
		fmt.Fprintf(app.Out, "installed %d · already present %d · skipped %d · failed %d  (%s)\n",
			sum.Installed, sum.Present, sum.Skipped, sum.Failed, formatDuration(time.Duration(sum.ElapsedMs)*time.Millisecond))
	}

	cells := make([][]string, 0, len(sum.Results))
	for _, r := range sum.Results {
		// A warning is the tool's story in a way Error is not: the tool failed
		// for one reason, and the warnings say what else went wrong around it.
		// They go in the same cell so a human running without --json sees
		// them at all — the field used to be JSON-only, while its own comment
		// claimed they were reported rather than swallowed, which is a comment
		// describing behaviour the code did not have.
		detail := r.Error
		if len(r.Warning) > 0 {
			if detail != "" {
				detail += " "
			}
			detail += "(" + strings.Join(r.Warning, "; ") + ")"
		}
		cells = append(cells, []string{r.Name, string(r.Status), formatDuration(time.Duration(r.DurationMs) * time.Millisecond), detail})
	}
	_ = table(app.Out, []string{"TOOL", "STATUS", "TIME", "DETAIL"}, cells)

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
		printFailureReport(app, sum)
	}
	if sum.LogPath != "" {
		fmt.Fprintf(app.Out, "\nfull run log: %s\n", sum.LogPath)
	}
}

// printFailureReport says what kind of failure each tool hit and the exact
// command that reinstalls just those. Both are derived from the same results
// the summary printed, and the resume command's --only names come from the
// failed set alone — never widened, so the hint cannot install something the
// run did not resolve.
func printFailureReport(app *App, sum summaryOutput) {
	for _, b := range failureBuckets(sum.Results) {
		fmt.Fprintf(app.Out, "\n  %s (%d): %s\n", b.Name, len(b.Tools), strings.Join(b.Tools, ", "))
	}
	if sum.ResumeCommand != "" {
		fmt.Fprintf(app.Out, "\nto resume only what failed (re-verifies everything, reinstalls just these):\n\n    %s\n", sum.ResumeCommand)
	}
}

// withBinOnPath prepends dir to PATH for the duration of a run and returns a
// function restoring the previous value.
//
// WARNING: this mutates process-global state. Go's race detector does NOT
// instrument os.Setenv, so a test that runs in parallel with one of these
// commands will not be reported — the failure is a wrong PATH, not a data race.
//
// No test that calls t.Parallel() may reach a command using this. That is
// enforced by TestNoParallelTestReachesPathMutation, not by the compiler, and
// nothing else in the type system would notice.
func withBinOnPath(dir string) func() {
	old, had := os.LookupEnv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+old); err != nil {
		return func() {}
	}
	return func() {
		if had {
			os.Setenv("PATH", old)
			return
		}
		os.Unsetenv("PATH")
	}
}

// installNameOf is the filename a tool is installed as, read from the one place
// the manifest states it. verify.command is that place: it is what the verifier
// resolves on PATH, so installing under the same string makes it impossible for
// the two to disagree.
func installNameOf(tool *manifest.Tool) string {
	if tool.Verify != nil && tool.Verify.Command != "" {
		return tool.Verify.Command
	}
	if len(tool.Provides) > 0 {
		return tool.Provides[0]
	}
	return tool.Name
}

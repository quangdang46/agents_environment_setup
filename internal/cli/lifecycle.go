package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/quangdang46/agents_environment_setup/internal/installer"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/plan"
	"github.com/quangdang46/agents_environment_setup/internal/verifier"
)

// lifecycleCommands are `uninstall` and `forget`, which sound interchangeable
// and are not. The split is the point: a command called `remove` that only
// drops a state entry is a lie about what it did, and saying what you did is
// the whole product.
var lifecycleCommands = []*Command{
	{
		Name:     "uninstall",
		Summary:  "remove a tool from the machine, through its own strategy",
		Register: registerLifecycleFlags,
		Run:      runUninstall,
	},
	{
		Name:     "forget",
		Summary:  "stop tracking a tool, leaving the binary in place",
		Register: registerLifecycleFlags,
		Run:      runForget,
	},
}

// errNotTracked means the tool has no state entry, so there is nothing to do.
var errNotTracked = errors.New("not tracked by aes")

// lifecycleFlags are the flags only these two commands take. Keeping them
// here rather than in the shared set means `aes list --confirm` is a usage
// error rather than a silently accepted flag.
type lifecycleFlags struct {
	Confirm bool
}

func registerLifecycleFlags(fs *flag.FlagSet) any {
	l := &lifecycleFlags{}
	fs.BoolVar(&l.Confirm, "confirm", false, "skip the confirmation prompt")
	return l
}

// dependentsOf returns the catalog tools that list name as a dependency.
//
// Uninstalling a tool others depend on breaks them, and discovering that
// afterwards is the bad outcome. aes does not remove dependents automatically
// — that would be a cascade nobody asked for — but it must say what would
// break.
func dependentsOf(catalogTools []*manifest.Tool, name string) []string {
	var out []string
	for _, t := range catalogTools {
		for _, dep := range t.Dependencies {
			if dep == name {
				out = append(out, t.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// runUninstall removes a tool through its strategy and then, only if the
// binary is genuinely gone, drops the state entry.
//
// The ordering is the contract. Removing state for a tool that is still on
// disk is precisely how drift gets created: the record says one thing, the
// machine says another, and doctor is left to reconcile a mess the installer
// made itself.
func runUninstall(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	if len(args) != 1 {
		return usagef("uninstall takes exactly one tool name")
	}
	name := args[0]

	rc, err := app.resolve(f)
	if err != nil {
		return err
	}
	store, err := openState(app)
	if err != nil {
		return err
	}
	if _, tracked := store.Get(name); !tracked {
		// Nothing to do, and saying so plainly is better than a no-op that
		// looks like a success.
		return fmt.Errorf("%w: %s is not tracked by aes; nothing to uninstall (try 'aes forget' if you want aes to stop checking it)", errNotTracked, name)
	}

	tool, ok := rc.Catalog.ByName(name)
	if !ok {
		return usagef("no tool named %q in the catalog", name)
	}
	target, supported := rc.Host.Target(tool)
	if !supported {
		return fmt.Errorf("%s is not supported on %s", name, rc.Host.OS)
	}

	deps := dependentsOf(rc.Catalog.All(), name)
	if len(deps) > 0 {
		fmt.Fprintf(app.Err, "aes: note: %s is a dependency of %s\n", name, strings.Join(deps, ", "))
		fmt.Fprintf(app.Err, "aes: they will not be removed automatically, and may stop working\n")
	}

	if f.DryRun {
		fmt.Fprintf(app.Out, "would uninstall %s (strategy: %s)\n", name, target.Strategy)
		return nil
	}

	if !lifecycleConfirmed(app, f, extra, fmt.Sprintf("uninstall %s", name)) {
		fmt.Fprintln(app.Out, "cancelled")
		return nil
	}

	outcome, err := app.registry().Uninstall(ctx, installer.Action{
		Tool: name, Target: target, Host: rc.Host, Reason: "uninstall",
	})
	if err != nil {
		// A privilege error keeps its own exit code: the user must act.
		return err
	}
	if !outcome.Removed {
		// Not an error — the tool is still fine. It is an honest "I did not
		// remove this", and the state entry stays so aes keeps tracking it.
		fmt.Fprintf(app.Out, "%s: not removed. %s\n", name, outcome.Reason)
		return nil
	}

	// Verify before forgetting. This is the whole point of the ordering.
	after := verifier.Verify(tool)
	if after.Status != verifier.StatusMissing {
		// It claimed success but the binary is still there. Keep the state
		// entry and report the disagreement rather than hiding it.
		fmt.Fprintf(app.Err, "aes: %s reported success but the binary is still present (%s); keeping the state entry so doctor can report the disagreement\n", name, after.Path)
		return &verifyGateError{failure: &verifyFailure{Missing: []string{name}}}
	}

	store.Remove(name)
	if err := store.Save(); err != nil {
		return fmt.Errorf("aes: could not write state: %w", err)
	}
	fmt.Fprintf(app.Out, "removed %s\n", name)
	return nil
}

// runForget drops the state entry and leaves the binary exactly where it is.
//
// This is the correct command for a tool aes did not install — one that was
// already on the machine, which is the common case on a developer laptop.
func runForget(ctx context.Context, app *App, f *Flags, extra any, args []string) error {
	if len(args) != 1 {
		return usagef("forget takes exactly one tool name")
	}
	name := args[0]

	store, err := openState(app)
	if err != nil {
		return err
	}
	if _, tracked := store.Get(name); !tracked {
		return fmt.Errorf("%w: %s is not tracked by aes; nothing to forget", errNotTracked, name)
	}

	if f.DryRun {
		fmt.Fprintf(app.Out, "would forget %s (the binary is left in place)\n", name)
		return nil
	}

	if !lifecycleConfirmed(app, f, extra, fmt.Sprintf("forget %s", name)) {
		fmt.Fprintln(app.Out, "cancelled")
		return nil
	}

	store.Remove(name)
	if err := store.Save(); err != nil {
		return fmt.Errorf("aes: could not write state: %w", err)
	}
	fmt.Fprintf(app.Out, "forgot %s; the binary was not touched\n", name)
	return nil
}

// lifecycleConfirmed asks before acting, unless --confirm or --yes was given.
//
// Reading from a caller-supplied reader rather than os.Stdin keeps this
// testable, and EOF counts as "no": a command that acts on a closed stdin
// would remove things nobody approved.
func lifecycleConfirmed(app *App, f *Flags, extra any, what string) bool {
	if f.Yes {
		return true
	}
	if l, ok := extra.(*lifecycleFlags); ok && l.Confirm {
		return true
	}
	fmt.Fprintf(app.Err, "aes: about to %s. This modifies your machine.\nContinue? [y/N] ", what)
	line, err := bufio.NewReader(app.In).ReadString('\n')
	if err != nil && line == "" {
		// EOF: nobody answered, so nothing was approved.
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// registry resolves the installer registry, tolerating an unconfigured app.
func (a *App) registry() *installer.Registry {
	if a.Registry == nil {
		a.Registry = installer.NewRegistry()
	}
	return a.Registry
}

// SetupSelection runs the North Star pipeline for an explicit selection.
//
// It exists so the TUI can install what the user ticked without acquiring a
// second installer. The TUI cannot import internal/installer — that is one of
// its three hard constraints — so it calls in here and gets exactly the
// pipeline `aes setup --only <selection>` runs, by construction rather than
// by agreement.
func (a *App) SetupSelection(ctx context.Context, sel plan.Selection, f *Flags) error {
	flags := &Flags{}
	if f != nil {
		*flags = *f
	}
	flags.Only = sel.Names()
	flags.Exclude = sel.Exclude
	// An explicit selection is the selection: no profile is layered on top.
	// Otherwise picking one tool in the TUI would install the whole default
	// profile, which is the same surprise `aes setup --only` was fixed to
	// avoid.
	flags.Profile = ""
	flags.JSON = false // the TUI renders results itself
	return runSetup(ctx, a, flags, nil, nil)
}

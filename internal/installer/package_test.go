package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/exec"
	"github.com/quangdang46/agents_environment_setup/internal/manifest"
	"github.com/quangdang46/agents_environment_setup/internal/platform"
)

// recorder captures what the installer actually tried to run, split by which
// runner it reached. Asserting on this rather than on an error value is the
// point: a test that only checked the error would still pass if the command
// had already been executed.
type recorder struct {
	unprivileged []string
	privileged   []string
	authAsked    []string
	privErr      error
	unprivErr    error
}

func (r *recorder) installers() (unpriv, priv func(context.Context, string) (exec.Result, error)) {
	unpriv = func(_ context.Context, cmd string) (exec.Result, error) {
		r.unprivileged = append(r.unprivileged, cmd)
		return exec.Result{ExitCode: 0}, r.unprivErr
	}
	priv = func(_ context.Context, cmd string) (exec.Result, error) {
		r.privileged = append(r.privileged, cmd)
		return exec.Result{ExitCode: 0}, r.privErr
	}
	return unpriv, priv
}

func pkgAction(manager, pkg string) Action {
	return Action{
		Tool: "jq",
		Target: manifest.Target{
			Strategy: manifest.StrategyPackage,
			Manager:  manager,
			Package:  pkg,
		},
		Host: &platform.Host{OS: platform.OSLinux, Arch: platform.ArchAMD64},
	}
}

func TestBrewNeverEscalates(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerBrew, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(rec.unprivileged) != 1 || rec.unprivileged[0] != "brew install jq" {
		t.Errorf("unprivileged calls = %v, want [\"brew install jq\"]", rec.unprivileged)
	}
	if len(rec.privileged) != 0 {
		t.Errorf("privileged runner was reached for brew: %v", rec.privileged)
	}
	if rec.authAsked != nil {
		t.Errorf("brew asked for authorization: %v", rec.authAsked)
	}
}

// TestAptInteractiveWithoutConfirmationDoesNotRun is invariant I14 in test
// form: the command is printed, but nothing runs.
func TestAptInteractiveWithoutConfirmationDoesNotRun(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	var out strings.Builder
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		In:              strings.NewReader(""), // EOF: nobody answered
		Out:             &out,
	}

	err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq"))
	if err == nil {
		t.Fatal("expected an error when confirmation is absent")
	}
	if !errors.Is(err, exec.ErrNeedsPrivilege) {
		t.Errorf("error = %v, want a PrivilegeError", err)
	}
	if len(rec.privileged) != 0 {
		t.Fatalf("privileged runner ran %v despite no confirmation — this is the bug this test exists for", rec.privileged)
	}
	// The command must still have been printed: the user has to be able to
	// see what was declined.
	if !strings.Contains(out.String(), "apt-get install -y jq") {
		t.Errorf("command was not printed; output was:\n%s", out.String())
	}
}

func TestAptInteractiveDeclined(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	var out strings.Builder
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		In:              strings.NewReader("n\n"),
		Out:             &out,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); !errors.Is(err, exec.ErrNeedsPrivilege) {
		t.Errorf("error = %v, want a PrivilegeError", err)
	}
	if len(rec.privileged) != 0 {
		t.Errorf("privileged runner ran %v after an explicit no", rec.privileged)
	}
}

func TestAptInteractiveConfirmedRuns(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	var out strings.Builder
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		In:              strings.NewReader("y\n"),
		Out:             &out,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := "sudo apt-get install -y jq"
	if len(rec.privileged) != 1 || rec.privileged[0] != want {
		t.Errorf("privileged calls = %v, want [%q]", rec.privileged, want)
	}
	// Printed before running — the ordering is the guarantee.
	if strings.Index(out.String(), "apt-get install -y jq") < 0 {
		t.Errorf("command was not printed; output was:\n%s", out.String())
	}
}

func TestAptNonInteractiveUsesSudoDashN(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		NonInteractive:  true,
		IsRoot:          func() bool { return false },
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := "sudo -n apt-get install -y jq"
	if len(rec.privileged) != 1 || rec.privileged[0] != want {
		t.Errorf("privileged calls = %v, want [%q]", rec.privileged, want)
	}
	if rec.authAsked != nil {
		t.Errorf("non-interactive mode prompted: %v", rec.authAsked)
	}
}

// TestAptNonInteractiveFailureDoesNotRetryOrHang pins term 2: a failing
// sudo -n stops the run. Exactly one attempt, and the command is printed so
// the user can finish it by hand.
func TestAptNonInteractiveFailureDoesNotRetryOrHang(t *testing.T) {
	t.Parallel()

	rec := &recorder{privErr: errors.New("sudo: a password is required")}
	unpriv, priv := rec.installers()

	var out strings.Builder
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		NonInteractive:  true,
		IsRoot:          func() bool { return false },
		Out:             &out,
	}

	done := make(chan error, 1)
	go func() { done <- p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")) }()

	select {
	case err := <-done:
		if !errors.Is(err, exec.ErrNeedsPrivilege) {
			t.Errorf("error = %v, want a PrivilegeError (the CLI maps this to exit 5)", err)
		}
		var pe *exec.PrivilegeError
		if !errors.As(err, &pe) {
			t.Fatalf("error = %v, want *exec.PrivilegeError", err)
		}
		if !pe.NonInteractive {
			t.Error("PrivilegeError.NonInteractive = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Install blocked — non-interactive mode must never wait for a password")
	}

	// Exactly one attempt: a retry loop is how this hangs in CI.
	if len(rec.privileged) != 1 {
		t.Errorf("privileged runner called %d times, want exactly 1 (no retry loop)", len(rec.privileged))
	}
	if !strings.Contains(out.String(), "apt-get install -y jq") {
		t.Errorf("the manual command was not printed; output was:\n%s", out.String())
	}
}

func TestAptAsRootNeedsNoSudo(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return true },
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Already root: no escalation question, so no sudo anywhere. This is the
	// container path that makes the sandbox tests work without a password.
	if len(rec.privileged) != 0 {
		t.Errorf("privileged calls = %v, want none: as root there is no escalation", rec.privileged)
	}
	if len(rec.unprivileged) != 1 || rec.unprivileged[0] != "apt-get install -y jq" {
		t.Errorf("unprivileged calls = %v, want [\"apt-get install -y jq\"] with no sudo", rec.unprivileged)
	}
	if strings.Contains(rec.unprivileged[0], "sudo") {
		t.Errorf("command %q escalates despite already being root", rec.unprivileged[0])
	}
}

// A forced install is a repair, and apt cannot be asked to repair with the
// command a first install uses.
//
// dpkg records "jq installed" from its own database, not from the filesystem.
// Deleting /usr/bin/jq without telling dpkg leaves the two disagreeing, and
// `apt-get install -y jq` then answers "jq is already the newest version",
// exits 0, and restores nothing. `--force` promises to correct exactly that
// disagreement, so with the first-install command it is a promise the strategy
// cannot keep: the run succeeds, the binary is still missing, and the tool is
// still broken.
//
// Measured in a fresh ubuntu:24.04 container, which is where this was found:
//
//	rm -f /usr/bin/jq
//	apt-get install -y jq             -> "already the newest version", still gone
//	apt-get install -y --reinstall jq -> /usr/bin/jq restored, 67512 bytes
func TestAptForceReinstallsRatherThanTrustingDpkg(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return true },
		Out:             io.Discard,
	}

	action := pkgAction(manifest.ManagerApt, "jq")
	action.Force = true
	if err := p.Install(context.Background(), action); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Already root means the command carries no sudo, so it runs on the
	// UNprivileged path. Asserting on which runner it took is not a detail:
	// a command that needs no elevation has no business claiming an
	// Authorization, and routing it through the privileged runner would make
	// the gate mean nothing the first time it was handed something true.
	if len(rec.privileged) != 0 {
		t.Errorf("privileged calls = %v, want none: as root there is no escalation",
			rec.privileged)
	}
	if len(rec.unprivileged) != 1 {
		t.Fatalf("unprivileged calls = %v, want exactly one", rec.unprivileged)
	}
	if !strings.Contains(rec.unprivileged[0], "--reinstall") {
		t.Errorf("forced apt command = %q, want --reinstall.\n"+
			"Without it apt trusts dpkg's database, reports the package as already "+
			"newest, and leaves a deleted binary deleted -- so --force repairs nothing",
			rec.unprivileged[0])
	}
}

// The negative control: the ordinary first install must NOT carry --reinstall.
// Adding it unconditionally would re-unpack every package on every setup run,
// which is slower and rewrites files that were fine.
func TestAptWithoutForceDoesNotReinstall(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return true },
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(rec.unprivileged) != 1 {
		t.Fatalf("unprivileged calls = %v, want exactly one", rec.unprivileged)
	}
	if strings.Contains(rec.unprivileged[0], "--reinstall") {
		t.Errorf("a first install ran %q; --reinstall is for a repair only", rec.unprivileged[0])
	}
}

// The privilege model has to survive the new flag. A forced apt install on a
// non-root machine must still go through the printed-and-confirmed path, and
// must still use `sudo -n` when nobody can answer.
func TestAptForceRespectsThePrivilegeModel(t *testing.T) {
	t.Parallel()

	t.Run("interactive is confirmed before anything runs", func(t *testing.T) {
		rec := &recorder{}
		unpriv, priv := rec.installers()
		var out bytes.Buffer
		p := &PackageInstaller{
			RunUnprivileged: unpriv,
			RunPrivileged:   priv,
			IsRoot:          func() bool { return false },
			In:              strings.NewReader("y\n"),
			Out:             &out,
		}
		action := pkgAction(manifest.ManagerApt, "jq")
		action.Force = true
		if err := p.Install(context.Background(), action); err != nil {
			t.Fatalf("Install: %v", err)
		}
		if len(rec.privileged) != 1 || !strings.Contains(rec.privileged[0], "sudo apt-get install") {
			t.Fatalf("privileged = %v, want a confirmed sudo apt-get", rec.privileged)
		}
		if !strings.Contains(rec.privileged[0], "--reinstall") {
			t.Errorf("forced command %q lost --reinstall on the escalated path", rec.privileged[0])
		}
		// The command is printed before the question, so what is about to run
		// is visible before anyone types anything.
		if !strings.Contains(out.String(), "--reinstall") {
			t.Errorf("the printed command does not mention --reinstall:\n%s", out.String())
		}
	})

	t.Run("non-interactive still uses sudo -n", func(t *testing.T) {
		rec := &recorder{}
		unpriv, priv := rec.installers()
		p := &PackageInstaller{
			RunUnprivileged: unpriv,
			RunPrivileged:   priv,
			IsRoot:          func() bool { return false },
			NonInteractive:  true,
			Out:             io.Discard,
		}
		action := pkgAction(manifest.ManagerApt, "jq")
		action.Force = true
		if err := p.Install(context.Background(), action); err != nil {
			t.Fatalf("Install: %v", err)
		}
		if !strings.HasPrefix(rec.privileged[0], "sudo -n apt-get install") {
			t.Errorf("non-interactive command = %q, want it to start with `sudo -n`", rec.privileged[0])
		}
		if !strings.Contains(rec.privileged[0], "--reinstall") {
			t.Errorf("forced command %q lost --reinstall on the sudo -n path", rec.privileged[0])
		}
	})
}

// I14 — "sudo never runs silently" — is an ORDERING claim, and ordering is the
// only part of it that is not a mechanism. The command must reach the user
// BEFORE a privileged process exists; everything else about the confirmation
// flow is just how that is achieved.
//
// Nothing tested the ordering. authorize() was exercised, and the code did
// print first, but "it printed first" is not the same claim as "it printed
// before the process was spawned", and a refactor that moved the print after
// the call would have kept every other test in this file green.
//
// The check is made inside the privileged runner, because that is the only
// moment the ordering can be observed: by the time Install returns, both
// things have happened and their order is unrecoverable.
func TestPrivilegedCommandIsPrintedBeforeAnythingRuns(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	// The exact string the user is shown, which is the sudo form — not `base`,
	// which is what actually gets executed.
	const shown = "sudo apt-get install -y jq"

	var sawPrint bool
	var ranWithoutPrinting bool
	var order []string

	p := &PackageInstaller{
		IsRoot:          func() bool { return false },
		In:              strings.NewReader("y\n"),
		Out:             &out,
		RunUnprivileged: func(context.Context, string) (exec.Result, error) { return exec.Result{}, nil },
		RunPrivileged: func(_ context.Context, cmd string) (exec.Result, error) {
			order = append(order, "run")
			sawPrint = strings.Contains(out.String(), shown)
			if !sawPrint {
				ranWithoutPrinting = true
			}
			return exec.Result{}, nil
		},
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if ranWithoutPrinting {
		t.Errorf("a privileged process was spawned before the command reached the user.\n"+
			"order=%v\nprinted so far: %q\n"+
			"I14 is that the user sees what is about to run; a process that exists "+
			"before the print is a command that already ran, whatever it does next",
			order, out.String())
	}
	if !sawPrint {
		t.Error("the printed output never contained the command that was run")
	}
}

// The negative control, and the reason the first test is not vacuous: when the
// answer is no, nothing runs at all. An ordering test that passes because the
// privileged path was never reached proves nothing about order.
func TestNothingRunsWhenConfirmationIsDeclined(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	spawned := false

	p := &PackageInstaller{
		IsRoot:          func() bool { return false },
		In:              strings.NewReader("n\n"),
		Out:             &out,
		RunUnprivileged: func(context.Context, string) (exec.Result, error) { return exec.Result{}, nil },
		RunPrivileged: func(context.Context, string) (exec.Result, error) {
			spawned = true
			return exec.Result{}, nil
		},
	}

	err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq"))
	if err == nil {
		t.Fatal("declining the confirmation still reported success")
	}
	if spawned {
		t.Error("a privileged process ran after the user said no")
	}
	// Printed before the question, so the user could see what they refused.
	if !strings.Contains(out.String(), "apt-get install -y jq") {
		t.Errorf("the command was not shown to the user before asking:\n%s", out.String())
	}
}

func TestUnknownManagerIsNotDefaulted(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		Out:             io.Discard,
	}

	err := p.Install(context.Background(), pkgAction("port", "jq"))
	if err == nil {
		t.Fatal("expected an error for an unknown manager")
	}
	if len(rec.privileged) != 0 || len(rec.unprivileged) != 0 {
		t.Errorf("a command ran for an unknown manager: %v %v", rec.unprivileged, rec.privileged)
	}
}

func TestMissingPackageIsRejected(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		IsRoot:          func() bool { return false },
		Out:             io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerBrew, "  ")); err == nil {
		t.Fatal("expected an error for a blank package name")
	}
	if len(rec.unprivileged) != 0 {
		t.Errorf("ran %v for a blank package", rec.unprivileged)
	}
}

func TestInjectedAuthorizeIsConsulted(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	unpriv, priv := rec.installers()

	var asked []string
	p := &PackageInstaller{
		RunUnprivileged: unpriv,
		RunPrivileged:   priv,
		Authorize: func(_ context.Context, command string) error {
			asked = append(asked, command)
			return nil
		},
		IsRoot: func() bool { return false },
		Out:    io.Discard,
	}

	if err := p.Install(context.Background(), pkgAction(manifest.ManagerApt, "jq")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Authorize receives the command without the sudo prefix: it is the
	// thing being authorized, not the wrapper.
	if len(asked) != 1 || asked[0] != "apt-get install -y jq" {
		t.Errorf("Authorize saw %v, want [\"apt-get install -y jq\"]", asked)
	}
}

// The regression this guarded is now exec's, not ours. A truncated apt-get
// transcript once dropped the truncation marker, so a log shown to a user
// about to type a password read as complete when it was not. The mechanism that
// appends the marker now lives in internal/exec alongside the 1 MiB cap, and
// exec_test.go asserts both directions: a capped stream ends with the marker,
// and a complete one does not carry it. Deleting the local cappedWriter removes
// the second implementation rather than the coverage.

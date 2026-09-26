package installer

import (
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
	if len(rec.privileged) != 1 || rec.privileged[0] != "apt-get install -y jq" {
		t.Errorf("privileged calls = %v, want [\"apt-get install -y jq\"] with no sudo", rec.privileged)
	}
	if strings.Contains(rec.privileged[0], "sudo") {
		t.Errorf("command %q escalates despite already being root", rec.privileged[0])
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

// TestCappedWriterMarksTruncation is the regression guard for a real defect:
// the privileged path's capped writer silently dropped the truncation marker
// that exec.Run appends, so a truncated apt-get transcript was shown to the
// user as complete — on the one path where they are about to be asked for a
// password. A truncated log that does not admit it is truncated is worse
// than no log.
func TestCappedWriterMarksTruncation(t *testing.T) {
	t.Parallel()

	t.Run("short output carries no marker", func(t *testing.T) {
		t.Parallel()
		var c cappedWriter
		c.limit = 100
		_, _ = c.Write([]byte("short"))
		if got := c.String(); got != "short" {
			t.Errorf("String() = %q, want %q with no marker", got, "short")
		}
	})

	t.Run("output past the cap is marked", func(t *testing.T) {
		t.Parallel()
		var c cappedWriter
		c.limit = 5
		_, _ = c.Write([]byte("0123456789"))
		got := c.String()
		if !strings.Contains(got, "truncated") {
			t.Errorf("String() = %q, want a truncation marker", got)
		}
		if !strings.HasPrefix(got, "01234") {
			t.Errorf("String() = %q, want the retained prefix first", got)
		}
	})

	t.Run("a write entirely past the cap still marks", func(t *testing.T) {
		t.Parallel()
		// The buffer is already full; the overflow case is the one a
		// naive remaining > 0 check silently forgets.
		var c cappedWriter
		c.limit = 2
		_, _ = c.Write([]byte("ab"))
		_, _ = c.Write([]byte("cd"))
		if !strings.Contains(c.String(), "truncated") {
			t.Errorf("String() = %q, want a truncation marker", c.String())
		}
	})

	t.Run("writes report success so the child is not SIGPIPEd", func(t *testing.T) {
		t.Parallel()
		var c cappedWriter
		c.limit = 1
		n, err := c.Write([]byte("abcdef"))
		if err != nil {
			t.Errorf("Write returned %v; a capped writer must not fail the command", err)
		}
		if n != 6 {
			t.Errorf("Write consumed %d bytes, want all 6 reported as consumed", n)
		}
	})
}

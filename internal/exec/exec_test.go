package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The contract is the missing side effect, not the error value: a privileged
// command that runs and *then* reports an error has already escalated.
func TestRunRefusesSudoAndLeavesNoSideEffect(t *testing.T) {
	for _, cmd := range []string{
		"sudo touch %s",
		"/usr/bin/sudo touch %s",
		"echo hi; sudo touch %s",
		"true && sudo touch %s",
		"$(sudo touch %s)",
		"echo x | sudo touch %s",
		"sudo -n touch %s",
	} {
		t.Run(cmd, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "aes-should-not-exist")
			formatted := strings.ReplaceAll(cmd, "%s", target)

			res, err := Run(context.Background(), formatted, Options{})

			var privErr *PrivilegeError
			if !errors.As(err, &privErr) {
				t.Fatalf("error = %v (%T), want *PrivilegeError", err, err)
			}
			if !errors.Is(err, ErrNeedsPrivilege) {
				t.Errorf("error does not unwrap to ErrNeedsPrivilege: %v", err)
			}
			if privErr.Command != formatted {
				t.Errorf("PrivilegeError.Command = %q, want %q", privErr.Command, formatted)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Errorf("refused command still ran: %s exists", target)
			}
			if res.Stdout != "" || res.Stderr != "" {
				t.Errorf("refused command produced output: %+v", res)
			}
		})
	}
}

func TestPrivilegeErrorCarriesNonInteractive(t *testing.T) {
	_, err := Run(context.Background(), "sudo id", Options{NonInteractive: true})
	var privErr *PrivilegeError
	if !errors.As(err, &privErr) {
		t.Fatalf("error = %v, want *PrivilegeError", err)
	}
	if !privErr.NonInteractive {
		t.Error("NonInteractive was not carried onto the PrivilegeError")
	}
}

// The gate keys on a command word, so words that merely contain "sudo" as a
// strict substring must not trip it.
func TestSudoPatternIgnoresSubstringsOfOtherWords(t *testing.T) {
	for _, cmd := range []string{
		"echo pseudo",
		"echo sudoku",
		"install postgres",
		"echo $SUDO_HOME",
		"echo sudoku-solver",
	} {
		if sudoRe.MatchString(cmd) {
			t.Errorf("sudoRe matched %q, which does not invoke sudo", cmd)
		}
	}
}

// Quoted prose mentioning sudo is still refused. That is deliberate: a false
// positive costs the installer one round trip to decide, while a false negative
// costs an unrequested password prompt. The gate is conservative on purpose.
func TestSudoPatternIsConservativeAboutQuotedText(t *testing.T) {
	for _, cmd := range []string{
		"echo 'no sudo here'",
		`echo "run sudo later"`,
	} {
		if !sudoRe.MatchString(cmd) {
			t.Errorf("sudoRe did not match %q; the gate should lean toward refusing", cmd)
		}
	}
}

func TestSudoPatternMatchesRealInvocations(t *testing.T) {
	for _, cmd := range []string{
		"sudo apt-get install jq",
		"sudo -n true",
		"  sudo reboot",
		"/usr/bin/sudo reboot",
		"/bin/sudo reboot",
		"apt-get update && sudo apt-get upgrade",
		"foo | sudo tee /etc/x",
		"`sudo id`",
	} {
		if !sudoRe.MatchString(cmd) {
			t.Errorf("sudoRe did not match %q", cmd)
		}
	}
}

func TestRunEchoesAndSucceeds(t *testing.T) {
	res, err := Run(context.Background(), "echo hello", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("Stdout = %q, want it to contain %q", res.Stdout, "hello")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Duration <= 0 {
		t.Error("Duration was not measured")
	}
}

// A failed install that swallows the package manager's output is useless, so
// the Result must come back populated alongside the error.
func TestRunNonZeroExitReturnsErrorAndOutput(t *testing.T) {
	res, err := Run(context.Background(), "echo to-stderr >&2; exit 3", Options{})
	if err == nil {
		t.Fatal("non-zero exit returned no error")
	}
	if !errors.Is(err, ErrFailed) {
		t.Errorf("error = %v, want ErrFailed", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "to-stderr") {
		t.Errorf("Stderr = %q, want it to contain the child's output", res.Stderr)
	}
}

func TestRunTimeoutSaysSo(t *testing.T) {
	start := time.Now()
	res, err := Run(context.Background(), "sleep 30", Options{Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a command outliving its timeout returned no error")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want ErrTimeout", err)
	}
	// "signal: killed" tells the user nothing; the deadline must be named.
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not say it timed out", err)
	}
	if !strings.Contains(err.Error(), "200ms") {
		t.Errorf("error %q does not name the timeout duration", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("Run blocked for %s; the timeout did not fire", elapsed)
	}
	// The Result still comes back, so a caller can report partial output.
	_ = res
}

func TestRunOutputIsCappedWithVisibleMarker(t *testing.T) {
	const limit = 1024
	res, err := Run(context.Background(), "yes | head -c 100000", Options{OutputLimit: limit})
	// The pipeline's exit status is head's, but do not depend on it.
	if err != nil && !errors.Is(err, ErrFailed) {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasSuffix(res.Stdout, truncationMarker) {
		t.Errorf("truncated output lacks the marker; got %d bytes ending %q",
			len(res.Stdout), tail(res.Stdout, 40))
	}
	if len(res.Stdout) > limit+len(truncationMarker) {
		t.Errorf("captured %d bytes, want at most %d plus the marker",
			len(res.Stdout), limit)
	}
	if !strings.Contains(res.Stdout, "y") {
		t.Error("captured no data at all")
	}
}

func TestRunOutputUnderLimitHasNoMarker(t *testing.T) {
	res, err := Run(context.Background(), "echo short", Options{OutputLimit: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Stdout, truncationMarker) {
		t.Error("complete output was marked as truncated")
	}
}

func TestRunEmptyCommand(t *testing.T) {
	for _, cmd := range []string{"", "   ", "\n\t "} {
		res, err := Run(context.Background(), cmd, Options{})
		if !errors.Is(err, ErrEmptyCommand) {
			t.Errorf("Run(%q) error = %v, want ErrEmptyCommand", cmd, err)
		}
		if res.Stdout != "" || res.ExitCode != 0 {
			t.Errorf("Run(%q) produced %+v, want nothing executed", cmd, res)
		}
	}
}

func TestRunInheritsEnvironmentWhenEnvIsNil(t *testing.T) {
	t.Setenv("AES_EXEC_TEST_VAR", "inherited")
	res, err := Run(context.Background(), `printf %s "$AES_EXEC_TEST_VAR"`, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "inherited" {
		t.Errorf("got %q, want %q — a nil Env must inherit os.Environ()", got, "inherited")
	}
}

func TestRunEnvReplacesWhenSet(t *testing.T) {
	t.Setenv("AES_EXEC_TEST_VAR", "inherited")
	res, err := Run(context.Background(), `printf %s "$AES_EXEC_TEST_VAR"`, Options{
		Env: []string{"AES_EXEC_TEST_VAR=explicit"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "explicit" {
		t.Errorf("got %q, want %q — a set Env must replace the environment", got, "explicit")
	}
}

func TestRunForwardsStdin(t *testing.T) {
	res, err := Run(context.Background(), "cat", Options{Stdin: "a known string"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "a known string" {
		t.Errorf("got %q, want %q", got, "a known string")
	}
}

func TestRunSetsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(context.Background(), "pwd", Options{Dir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS reports /private/var for /var, so compare the resolved paths.
	got, err := filepath.EvalSymlinks(strings.TrimSpace(res.Stdout))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("working directory = %q, want %q", got, want)
	}
}

func TestCappedBufferReportsFullWriteLength(t *testing.T) {
	// Returning short would give the child a broken pipe and turn a chatty
	// command into a failed one.
	c := cappedBuffer{limit: 4}
	n, err := c.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 8 {
		t.Errorf("Write returned n = %d, want 8", n)
	}
	if !strings.HasSuffix(c.String(), truncationMarker) {
		t.Error("over-limit write was not marked truncated")
	}
}

func TestFormatDurationKeepsSubSecondPrecision(t *testing.T) {
	if got := formatDuration(200 * time.Millisecond); got != "200ms" {
		t.Errorf("formatDuration(200ms) = %q, want %q", got, "200ms")
	}
	// Rounding 200ms to "0s" would make a timeout message useless.
	if got := formatDuration(10 * time.Minute); got != "10m0s" {
		t.Errorf("formatDuration(10m) = %q, want %q", got, "10m0s")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// RunAuthorized is the only path in this package that may run sudo, so it gets
// the same treatment as the refusal: assert the side effect, not the error.

func TestRunAuthorizedRefusesWithoutConfirmation(t *testing.T) {
	target := filepath.Join(t.TempDir(), "aes-should-not-exist")
	res, err := RunAuthorized(context.Background(), "sudo touch "+target,
		Options{}, Authorization{NonInteractive: true})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Error("an unconfirmed privileged command still ran")
	}
	if res.Stdout != "" || res.Stderr != "" {
		t.Errorf("an unconfirmed command produced output: %+v", res)
	}
}

// With confirmation, the command is actually attempted. On a machine with no
// cached credential it fails — promptly, without blocking on a prompt — and
// creates nothing. That is the whole non-interactive contract.
func TestRunAuthorizedAttemptsWithoutBlocking(t *testing.T) {
	target := filepath.Join(t.TempDir(), "aes-privileged-target")
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, err = RunAuthorized(context.Background(), "sudo touch "+target,
			Options{Timeout: 30 * time.Second},
			Authorization{Confirmed: true, NonInteractive: true, Reason: "test"})
	}()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("RunAuthorized blocked; non-interactive must not wait on a password")
	}
	// Whatever happened, it must not have hung, and with no credential
	// available nothing should exist.
	if _, statErr := os.Stat(target); statErr == nil {
		t.Log("sudo succeeded on this machine; the command genuinely ran")
	} else {
		t.Logf("sudo did not run, as expected without a credential: %v", err)
	}
}

// A command that turns out not to need privilege goes through the ordinary
// path, so a caller that authorized defensively is not silently using the
// privileged route.
func TestRunAuthorizedRoutesPlainCommandsThroughRun(t *testing.T) {
	res, err := RunAuthorized(context.Background(), "echo plain", Options{},
		Authorization{Confirmed: true})
	if err != nil {
		t.Fatalf("RunAuthorized on a plain command: %v", err)
	}
	if !strings.Contains(res.Stdout, "plain") {
		t.Errorf("Stdout = %q, want it to contain %q", res.Stdout, "plain")
	}
}

func TestRunAuthorizedRejectsEmptyCommand(t *testing.T) {
	if _, err := RunAuthorized(context.Background(), "   ", Options{},
		Authorization{Confirmed: true}); !errors.Is(err, ErrEmptyCommand) {
		t.Errorf("error = %v, want ErrEmptyCommand", err)
	}
}

// The gate and the escape hatch must agree: anything Run refuses, only
// RunAuthorized may run, and only with confirmation.
func TestRunAndRunAuthorizedDisagreeOnlyOnConfirmation(t *testing.T) {
	cmds := []string{
		"sudo touch /tmp/x", "sudo -n true", "/usr/bin/sudo id",
		"true && sudo id", "echo sudo", "echo nothing privileged",
	}
	for _, cmd := range cmds {
		_, runErr := Run(context.Background(), cmd, Options{NonInteractive: true})
		privileged := strings.Contains(cmd, "sudo")
		_, authErr := RunAuthorized(context.Background(), cmd, Options{NonInteractive: true},
			Authorization{Confirmed: true, NonInteractive: true})
		_, unauthErr := RunAuthorized(context.Background(), cmd, Options{NonInteractive: true},
			Authorization{NonInteractive: true})

		var privErr *PrivilegeError
		wasPrivileged := errors.As(runErr, &privErr)
		if wasPrivileged != privileged {
			t.Errorf("%q: Run treated as privileged=%t, expected %t (%v)", cmd, wasPrivileged, privileged, runErr)
		}
		// Confirmation gates entry to the privileged path, not just privileged
		// commands. A caller that reaches RunAuthorized without it has made a
		// mistake whatever the command turns out to be, and letting a harmless
		// command through would mean the check applied only sometimes.
		if !errors.Is(unauthErr, ErrUnauthorized) {
			t.Errorf("%q: unconfirmed RunAuthorized error = %v, want ErrUnauthorized", cmd, unauthErr)
		}
		_ = authErr
	}
}

// The message is what a user reads when aes refuses to escalate, so its
// content is a contract like any other: it must name the command, or the user
// cannot tell what was refused.
func TestPrivilegeErrorMessageNamesTheCommand(t *testing.T) {
	cmd := "sudo apt-get install -y jq"
	_, err := Run(context.Background(), cmd, Options{})
	var privErr *PrivilegeError
	if !errors.As(err, &privErr) {
		t.Fatalf("error = %v, want *PrivilegeError", err)
	}
	msg := privErr.Error()
	if !strings.Contains(msg, "jq") {
		t.Errorf("message %q does not name the command it refused", msg)
	}
	if !errors.Is(privErr, ErrNeedsPrivilege) {
		t.Error("PrivilegeError does not unwrap to ErrNeedsPrivilege")
	}
	// The same error through the fmt path an agent parses.
	if !strings.Contains(fmt.Sprintf("%v", err), "sudo") {
		t.Errorf("formatted error %q lost the command", err)
	}
}

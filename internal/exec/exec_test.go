package exec

import (
	"context"
	"errors"
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

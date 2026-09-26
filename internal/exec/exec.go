// Package exec runs the external commands AES is built out of.
//
// The single most important thing in this file is the sudo gate. Run refuses
// any command containing sudo and returns a PrivilegeError *without executing
// it*, so escalation can never happen as a side effect of this package (I10).
// The decision of whether to escalate belongs one level up, in the installer:
// print the command, ask, and only then run. This package exists so that
// nobody can accidentally skip that step.
//
// The gate scans for sudo as a command word. It is not a sandbox: a determined
// obfuscation ("S=su; $S ...") would slip past. That is acceptable, because the
// only commands reaching Run are ones AES itself constructed from a validated
// strategy — a tool.yaml cannot inject arbitrary text (I3, no run field). The
// gate is defence in depth for AES's own code, not a jail for hostile input.
// It leans toward refusing: a false positive costs the installer one round trip
// to decide, while a false negative costs an unrequested password prompt. So
// quoted prose that merely mentions "sudo" is refused too, while words that
// contain it as a strict substring ("pseudo", "postgres") are not.
//
// # Shell commands
//
// cmd is a shell command string run through `sh -c`, because that is what a
// package-manager invocation is ("brew install ripgrep" is a pipeline and
// argument list, not an argv). Callers pass strings AES built; they are not a
// general-purpose command interface.
//
// # Timeouts
//
// Every command has one. A hung apt-get must not hang AES forever. Zero means
// DefaultTimeout, sized for an install; version probes should pass VerifyTimeout.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Timeouts. Install is the slow case; a version probe must not sit on a network
// round trip for ten minutes.
const (
	DefaultTimeout = 10 * time.Minute
	VerifyTimeout  = 5 * time.Second
)

// DefaultOutputLimit caps each output stream. A runaway command must not
// exhaust memory.
const DefaultOutputLimit = 1 << 20 // 1 MiB

// truncationMarker is appended to a stream that hit the cap, so a truncated
// capture is never mistaken for a complete one.
const truncationMarker = "\n[output truncated by aes]\n"

var (
	// ErrFailed is returned when a command ran and exited non-zero. The Result
	// is returned alongside it, populated, so the caller can show the user what
	// the command actually said.
	ErrFailed = errors.New("command failed")

	// ErrTimeout is returned when a command outlived its deadline.
	ErrTimeout = errors.New("command timed out")

	// ErrEmptyCommand is returned for a blank command string.
	ErrEmptyCommand = errors.New("empty command")

	// ErrNeedsPrivilege is what PrivilegeError unwraps to, so callers can test
	// for "this needs sudo" without knowing the concrete type.
	ErrNeedsPrivilege = errors.New("requires elevated privileges")
)

// PrivilegeError reports a command this package refused to run because it
// contains sudo. It is returned instead of running, never alongside it.
type PrivilegeError struct {
	Command string
	// NonInteractive mirrors the caller's mode, so the layer above knows
	// whether it may ask the user or must report and stop.
	NonInteractive bool
}

func (e *PrivilegeError) Error() string {
	return fmt.Sprintf("refusing to run privileged command: %s", e.Command)
}

func (e *PrivilegeError) Unwrap() error { return ErrNeedsPrivilege }

// Authorization is the evidence that a privileged command was printed and
// agreed to.
//
// It is a struct rather than a bool so that it greps. A bare `true` at a call
// site is indistinguishable from a plausible-looking argument, whereas
// `Authorization{Confirmed: true}` names what was confirmed, and grep for this
// type finds every place in the codebase that is allowed to escalate.
type Authorization struct {
	// Confirmed records that the command was printed to the user and they
	// agreed, or that non-interactive `sudo -n` is in force. It is a promise
	// from the caller, not proof — which is the same trust level as any API,
	// and the reason the type exists is to make the promise visible.
	Confirmed bool
	// NonInteractive mirrors the caller's mode, so the command can be rewritten
	// to `sudo -n` rather than left to block on a password nobody will type.
	NonInteractive bool
	// Reason is recorded for diagnostics: which policy produced this, e.g.
	// "apt install". It never affects behaviour.
	Reason string
}

// ErrUnauthorized is returned when RunAuthorized is called without evidence
// that the command was authorized. It is a programming error, not a runtime
// condition: the whole point of the gate is that no unapproved privileged
// command runs.
var ErrUnauthorized = errors.New("privileged command was not authorized")

// RunAuthorized executes a command that contains sudo, and is the ONLY way in
// this package to do so.
//
// It lives beside the refusal it bypasses rather than in the caller's package,
// for two reasons. There is one place that constructs a privileged command, so
// `grep -n sudo internal/exec/exec.go` shows both the thing that forbids it and
// the one thing permitted to, and the invariant is auditable in one screen. And
// the mechanism — timeout, 1 MiB output cap, shell invocation, Result assembly
// — is identical to the unprivileged path, so duplicating it would give a
// changed timeout default two places to be forgotten in.
//
// The DECISION to escalate does not move here. Printing the command, asking,
// or trying `sudo -n` when nobody is there to ask all stay in the installer;
// this function only executes what that decision produced.
func RunAuthorized(ctx context.Context, cmd string, opts Options, auth Authorization) (Result, error) {
	if !auth.Confirmed {
		return Result{}, fmt.Errorf("%w: %s", ErrUnauthorized, cmd)
	}
	if strings.TrimSpace(cmd) == "" {
		return Result{}, ErrEmptyCommand
	}
	if !sudoRe.MatchString(cmd) {
		// Not privileged after all. Running it through Run keeps one code path
		// and means a caller that authorized "just in case" is not silently
		// using the privileged route.
		return Run(ctx, cmd, opts)
	}
	// Non-interactive must never block on a password prompt: `sudo -n` fails
	// immediately instead, and the caller reports rather than waiting.
	if auth.NonInteractive {
		cmd = strings.Replace(cmd, "sudo ", "sudo -n ", 1)
	}
	opts.NonInteractive = auth.NonInteractive
	return run(ctx, cmd, opts)
}

// Options controls a single Run.
type Options struct {
	// Dir is the working directory. Empty means inherit.
	Dir string
	// Timeout bounds the whole run. Zero means DefaultTimeout.
	Timeout time.Duration
	// Stdin is written to the process's standard input, then closed.
	Stdin string
	// Env replaces the environment. Nil inherits os.Environ().
	Env []string
	// NonInteractive is recorded on any PrivilegeError. It does not change
	// this package's behaviour — Run refuses either way — it only tells the
	// caller how to proceed.
	NonInteractive bool
	// OutputLimit caps stdout and stderr separately. Zero means
	// DefaultOutputLimit.
	OutputLimit int
}

// Result is the outcome of a run. It is returned even on error, so a failed
// install can show the user what the package manager said.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// sudoRe matches sudo at a command position: at the start of the string, or
// after a shell separator, optionally as a full path. \b keeps "pseudo" and
// "sudoku" from matching, and requires a real token boundary.
//
// RE2 has no lookahead, so trailing-word cases are handled by \b rather than a
// negative assertion.
var sudoRe = regexp.MustCompile("(?:^|[\\s;&|(`$])(?:[\\w./-]*/)?sudo\\b")

// Run executes cmd through the shell, subject to the sudo gate.
//
// It returns a PrivilegeError, and executes nothing, if cmd contains sudo.
// Otherwise it returns a populated Result plus an error describing how the
// command failed: ErrTimeout when it ran out of time, ErrFailed when it exited
// non-zero.
func Run(ctx context.Context, cmd string, opts Options) (Result, error) {
	if strings.TrimSpace(cmd) == "" {
		return Result{}, ErrEmptyCommand
	}

	// The gate. Before anything is spawned, and before any output is produced.
	if sudoRe.MatchString(cmd) {
		return Result{}, &PrivilegeError{Command: cmd, NonInteractive: opts.NonInteractive}
	}
	return run(ctx, cmd, opts)
}

// run is the mechanism, with no policy: timeout, output cap, shell, Result.
// Run and RunAuthorized are the only two ways to reach it, and both are in this
// file, so there is exactly one implementation of the parts that are easy to
// get subtly wrong twice.
func run(ctx context.Context, cmd string, opts Options) (Result, error) {
	if strings.TrimSpace(cmd) == "" {
		return Result{}, ErrEmptyCommand
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	limit := opts.OutputLimit
	if limit <= 0 {
		limit = DefaultOutputLimit
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := exec.CommandContext(runCtx, "sh", "-c", cmd)
	shell.Dir = opts.Dir
	shell.Env = opts.Env
	if shell.Env == nil {
		shell.Env = os.Environ()
	}

	var stdout, stderr cappedBuffer
	stdout.limit = limit
	stderr.limit = limit
	shell.Stdout = &stdout
	shell.Stderr = &stderr

	if opts.Stdin != "" {
		shell.Stdin = strings.NewReader(opts.Stdin)
	}

	started := time.Now()
	err := shell.Run()
	elapsed := time.Since(started)

	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: shell.ProcessState.ExitCode(),
		Duration: elapsed,
	}

	// A deadline kill surfaces as "signal: killed", which tells the user
	// nothing. Name the timeout instead: that is the actionable half.
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("%w after %s: %s", ErrTimeout, formatDuration(timeout), cmd)
	}
	if err != nil {
		return result, fmt.Errorf("%w with exit code %d: %s", ErrFailed, result.ExitCode, cmd)
	}
	return result, nil
}

// formatDuration renders a duration for an error message. Sub-second deadlines
// keep their precision, because rounding 200ms to "0s" would make the message
// useless.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// cappedBuffer collects up to limit bytes and then discards the rest. Writes
// past the cap are reported as fully consumed rather than as an error: a real
// io.Writer returning an error here would give the child a SIGPIPE and turn a
// chatty command into a failed one.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:remaining])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

// String is the captured output, with a marker appended if anything was
// dropped, so a truncated capture never reads as a complete one.
func (c *cappedBuffer) String() string {
	if !c.truncated {
		return c.buf.String()
	}
	return c.buf.String() + truncationMarker
}

var _ io.Writer = (*cappedBuffer)(nil)

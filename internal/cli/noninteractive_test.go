package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quangdang46/agents_environment_setup/internal/installer"
)

// blockingReader never returns. It stands in for a stdin that is open and
// silent — a terminal with nobody at it, or a pipe held open by a parent —
// which is the case that actually hangs.
//
// A CLOSED stdin is the easy one: it returns io.EOF immediately, every
// "anything but yes" path refuses, and the run finishes. That is why this
// defect survived review: every test that fed a closed reader saw correct
// behaviour and concluded the flag worked.
type blockingReader struct{ ch chan struct{} }

func newBlockingReader() *blockingReader { return &blockingReader{ch: make(chan struct{})} }

func (b *blockingReader) Read([]byte) (int, error) {
	<-b.ch // never returns
	return 0, io.EOF
}

// The contract is not "prints a useful message". It is that the run FINISHES.
// --non-interactive is the flag that promises never to block on input, and a
// read on a silent-but-open stdin is exactly the block it promises to prevent.
//
// Measured before the fix: `sleep 600 | aes setup --only pi --non-interactive`
// hung until killed, on an apt-backed tool needing sudo.
// Not parallel: newHarness calls t.Setenv, which forbids it.
func TestNonInteractiveNeverBlocksOnAnOpenSilentStdin(t *testing.T) {
	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }
	h.app.In = newBlockingReader()

	// --yes is deliberately NOT passed: the point is that the flag alone is
	// enough, which is the promise the docs make.
	done := make(chan int, 1)
	go func() { done <- h.run("uninstall", "demo", "--non-interactive") }()

	select {
	case got := <-done:
		if got == ExitOK {
			t.Error("the run acted on a machine without confirmation; " +
				"--non-interactive must refuse, not proceed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--non-interactive blocked reading stdin; the flag that promises " +
			"never to wait on input is waiting on input")
	}
}

// The same guarantee for `forget`, which has its own call site. Two call sites
// means two chances to forget the check, and a missing one is a hang.
func TestNonInteractiveNeverBlocksOnForget(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }
	h.app.In = newBlockingReader()

	done := make(chan int, 1)
	go func() { done <- h.run("forget", "demo", "--non-interactive") }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("`forget --non-interactive` blocked reading stdin")
	}
}

// lifecycleConfirmed is the function that reads stdin, so it is the one place
// the guarantee can be broken. The command-level tests above cannot reach it
// (an untracked tool returns before the confirmation), which is exactly why
// the guard needs a test of its own: a check that is only exercised on a path
// nothing takes is a check that does not exist.
func TestLifecycleConfirmedRefusesUnderNonInteractive(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }
	h.app.In = newBlockingReader()

	done := make(chan bool, 1)
	go func() { done <- lifecycleConfirmed(h.app, &Flags{NonInteractive: true}, nil, "uninstall demo") }()

	select {
	case got := <-done:
		if got {
			t.Error("--non-interactive confirmed a destructive action; " +
				"it must refuse rather than wait for an answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycleConfirmed blocked reading stdin under --non-interactive")
	}
}

// The other half: without the flag it must still ASK. A guard that always
// refuses would pass every test above and break the interactive path, which is
// the one that exists to get a yes.
func TestLifecycleConfirmedStillAsksWithoutTheFlag(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }
	// A closed stdin: EOF is "no", so the answer is a refusal — but the point
	// is that it READ, which is what the interactive path is for.
	h.app.In = strings.NewReader("")

	if lifecycleConfirmed(h.app, &Flags{}, nil, "uninstall demo") {
		t.Error("an empty stdin confirmed a destructive action")
	}
	if !strings.Contains(h.stderr.String(), "Continue?") {
		t.Errorf("the interactive path did not ask; stderr was %q", h.stderr.String())
	}
}

// The privilege prompt in the installer is a second gate, and it is reached
// only when PackageInstaller.NonInteractive is false. That field existed and
// the code that read it existed, and nothing ever set it — the flag was parsed
// and threaded nowhere. This asserts the wiring, at the level where argv is
// known: App.Run, because main builds the registry before flags are parsed and
// therefore cannot configure it.
func TestNonInteractiveReachesThePackageInstaller(t *testing.T) {

	build := func(args ...string) *installer.PackageInstaller {
		h := newHarness(t, [3]string{"demo", "utility", "go"})
		h.app.IsTTY = func() bool { return false }
		h.run(args...)
		inst, err := h.app.Registry.Get("package")
		if err != nil {
			t.Fatalf("package strategy missing from the registry: %v", err)
		}
		pi, ok := inst.(*installer.PackageInstaller)
		if !ok {
			t.Fatalf("package strategy is %T, want *installer.PackageInstaller", inst)
		}
		return pi
	}

	if got := build("list"); got.NonInteractive {
		t.Error("an ordinary run configured the package installer as non-interactive; " +
			"it would stop asking to confirm a privileged install")
	}
	if got := build("list", "--non-interactive"); !got.NonInteractive {
		t.Error("--non-interactive never reached the package installer; " +
			"a privileged install will prompt and block on a silent stdin")
	}
}

// Guard against the failure mode where the check exists but is unreachable —
// the bug in the code this file was written for. Authorize is the function
// that reads stdin, so assert it is genuinely not called.
func TestNonInteractiveNeverCallsAuthorize(t *testing.T) {

	h := newHarness(t, [3]string{"demo", "utility", "go"})
	h.app.IsTTY = func() bool { return false }

	var asked bool
	h.app.Registry.Register("package", &installer.PackageInstaller{
		NonInteractive: true,
		Authorize: func(context.Context, string) error {
			asked = true
			return errors.New("should never be reached")
		},
		IsRoot: func() bool { return false },
	})
	h.app.In = newBlockingReader()

	done := make(chan int, 1)
	go func() { done <- h.run("setup", "--only", "demo", "--non-interactive") }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("setup --non-interactive blocked on the privilege confirmation")
	}
	if asked {
		t.Error("the privilege confirmation was asked for under --non-interactive")
	}
}

// A tool that is proven only on another platform must not be reported as a bad
// invocation.
//
// The spec's exit table says 2 is "invalid usage / bad flags". `aes setup
// --only git` on a machine where git is proven only on linux/arm64 is a
// well-formed command naming a real tool, and an agent acting on exit 2 goes
// and rewrites the invocation — which cannot help, because the invocation was
// never wrong. The resolver's own comment claimed "doctor can say why the
// tool is absent", which leaves the user with a refusal and no reason.
func TestEmptySelectionIsAFailureNotAUsageError(t *testing.T) {

	// A tool marked tested for a platform this test is definitely not on.
	h := newHarness(t, [3]string{"demo", "utility", "go"})
	dir := filepath.Join(h.catRoot, "utility", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "name: demo\ndescription: fixture demo\ncategory: utility\n" +
		"tested: true\ntested_on:\n  - darwin/arm64\n" +
		"verify:\n  command: demo\n" +
		"install:\n  darwin:\n    strategy: go\n    go_package: example.com/demo@v1.0.0\n" +
		"  linux:\n    strategy: go\n    go_package: example.com/demo@v1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "tool.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tool.yaml: %v", err)
	}

	got := h.run("setup", "--only", "demo", "--dry-run")
	if got == ExitUsage {
		t.Errorf("exit = %d; a tool proven only elsewhere is not a usage error", got)
	}
	if got != ExitFailure {
		t.Errorf("exit = %d, want %d (a failure to proceed)", got, ExitFailure)
	}
	for _, want := range []string{"demo", "darwin/arm64", "--allow-unproven"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("the message does not mention %q, so the user cannot tell "+
				"what was refused or what to do: %q", want, h.stderr.String())
		}
	}
}

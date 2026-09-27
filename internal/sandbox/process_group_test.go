package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The contract Host.Run owes its caller: when the deadline fires, the whole
// process group dies, not just the direct child.
//
// The failure was confirmed by inspection against the same mechanism that
// internal/exec's timeout tests pin: no Setpgid, no WaitDelay, and pipes wired
// to in-memory buffers. cmd.Run blocks until every descendant closes those
// pipes, so a command that forks outlives its deadline and leaves an installer
// process group running on the developer's machine while the harness moves on.
//
// Host.Run hardcodes StepTimeout to 5 minutes, so a timing test here would
// either take 5 minutes or measure a command that finished on its own. The
// load-bearing behaviour is therefore asserted directly: hostIsolate puts the
// child in its own group, and the group kill reaches a grandchild.

// hostIsolate must put the child in a NEW process group. A negative-pid kill
// only addresses the child's group; if the child shared ours, the kill would
// take down the test binary and the developer's shell.
func TestHostIsolatePutsTheChildInItsOwnGroup(t *testing.T) {
	t.Parallel()

	own := syscall.Getpgrp()
	// A long-lived context: cancelling it would kill the child we are
	// inspecting, and CommandContext is required because a non-nil Cancel
	// panics on Start otherwise.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "echo $$; sleep 30")
	hostIsolate(cmd)
	cmd.WaitDelay = hostWaitDelay

	// A mutex, not a bare string: os/exec copies the child's stdout on its own
	// goroutine while this test polls, so a plain variable is a data race that
	// -race reports and a loaded machine may report as a torn read.
	var mu sync.Mutex
	var out string
	cmd.Stdout = writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		out += string(b)
		return len(b), nil
	})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// Give the shell a moment to report its own pid.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := strings.TrimSpace(out)
		mu.Unlock()
		if got != "" || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	childPid, err := strconv.Atoi(strings.TrimSpace(out))
	mu.Unlock()
	if err != nil {
		t.Skipf("the shell reported %q, not a pid: %v", out, err)
	}
	childPgid, err := syscall.Getpgid(childPid)
	if err != nil {
		t.Skipf("cannot read the child's group: %v", err)
	}

	if childPgid == own {
		t.Errorf("the child is in OUR process group (pgid %d); the group kill "+
			"would target the test binary and the user's shell", own)
	}
	if childPgid != childPid {
		t.Errorf("child pgid = %d, want the child's own pid %d so that a "+
			"negative-pid kill addresses only what we spawned", childPgid, childPid)
	}
}

// The kill must reach a grandchild, not just the direct child. This is the
// orphan: a `sleep` that survives the harness and keeps running on the
// developer's machine.
func TestHostIsolateKillsTheWholeGroup(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "aes-grandchild-marker")

	// The grandchild outlives the deadline if the group kill misses it.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c",
		"sh -c 'sleep 3; touch "+marker+"' & wait")
	hostIsolate(cmd)
	cmd.WaitDelay = hostWaitDelay
	_ = cmd.Run()

	// If the grandchild survived it writes the marker within its own lifetime.
	// Waiting past that is what makes this an assertion and not a race.
	time.Sleep(4 * time.Second)
	if fileExists(marker) {
		t.Errorf("the grandchild survived the group kill and wrote %s; "+
			"an installer process group would still be running on this machine", marker)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

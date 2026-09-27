package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The contract is that a deadline bounds the whole call, not merely that an
// error is eventually returned.
//
// `sh -c "sleep 30"` forks a grandchild and waits for it. Go's CommandContext
// kills only the direct child, so the deadline fired and the kill landed while
// the grandchild kept the pipe open — measured on this host as a 200ms deadline
// that blocked 30s and returned `signal: killed` the whole time. Asserting only
// on the error passes in both worlds, which is why the original test could not
// see it: it passed for the wrong reason, taking the full 30s to do it.
//
// These tests assert on elapsed time and on the absence of an orphan, because
// those are the two things that were actually wrong.
func TestTimeoutBoundsTheWholeCallWhenTheCommandForks(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), "echo before; sleep 30", Options{Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	// waitDelay is the slack a child gets after the deadline, so the bound is
	// the timeout plus it. 10s leaves room for that without hiding a regression
	// to "waited for the command to finish".
	if elapsed > 10*time.Second {
		t.Errorf("Run blocked for %s on a 200ms deadline; the timeout did not bound the call", elapsed)
	}
}

// Output produced before the deadline must still come back, or a timeout
// discards the evidence a user needs to see what the command was doing.
func TestTimeoutStillReturnsOutputProducedBeforeTheDeadline(t *testing.T) {
	res, err := Run(context.Background(), "echo before; sleep 30", Options{Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if !strings.Contains(res.Stdout, "before") {
		t.Errorf("Stdout = %q, want the output written before the deadline", res.Stdout)
	}
}

// A timeout that leaves a running grandchild behind is a leak, not a timeout:
// the next install inherits a process nobody is waiting on, and on a real
// package manager the survivor can still be writing to dpkg's database.
//
// This asserts the negative space — nothing is running afterwards — which is the
// property the group kill provides and a bare Process.Kill does not.
func TestTimeoutLeavesNoOrphanBehind(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "aes-orphan-marker")

	// The grandchild outlives the deadline if it is not killed with the group.
	_, err := Run(context.Background(),
		fmt.Sprintf("sh -c 'sleep 3; touch %s' & wait", marker),
		Options{Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}

	// If the grandchild survived, the marker appears within its own lifetime.
	// Waiting past that lifetime is what makes this an assertion rather than a
	// race that happens to pass.
	time.Sleep(4 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the grandchild survived the timeout and wrote %s; the kill did not reach the process group", marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", marker, err)
	}
}

// WaitDelay earns its place against a grandchild that escapes the process group.
// `setsid` puts the sleeper in a new session, so the group kill cannot reach it
// and WaitDelay is the only thing left bounding the call. Measured: 20s without
// it, 2.2s with it. Without this test the constant is untested defensiveness —
// deleting it left the whole suite green.
func TestWaitDelayBoundsAGrandchildThatEscapedTheGroup(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid is not installed: %v", err)
	}

	start := time.Now()
	_, err := Run(context.Background(), "setsid sleep 20 & wait", Options{Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("Run blocked for %s; WaitDelay did not bound a grandchild outside the process group", elapsed)
	}
}

// The same leak, measured through the process table rather than a side effect.
// Kept separate from the marker test so that a failure names which of the two
// properties broke.
func TestTimeoutDoesNotLeaveTheGrandchildRunning(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "aes-grandchild.pid")

	_, err := Run(context.Background(),
		fmt.Sprintf("sh -c 'echo $$ > %s; sleep 30' & wait", pidfile),
		Options{Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}

	raw, err := os.ReadFile(pidfile)
	if err != nil {
		t.Skipf("the shell wrote no pid before the deadline: %v", err)
	}
	pid := strings.TrimSpace(string(raw))
	if pid == "" {
		t.Skip("the shell wrote an empty pid")
	}

	// signal 0 is the existence check: it proves liveness without a second
	// signal landing on a pid that may have been recycled.
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Skipf("pid %q is not a number: %v", pid, err)
	}
	if processExists(n) {
		t.Errorf("grandchild %s is still running after the timeout; the kill did not reach the process group", pid)
	}
}

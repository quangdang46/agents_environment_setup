//go:build linux || darwin || freebsd || netbsd || openbsd

package exec

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// groupKillWorksHere reports whether this environment actually delivers a
// signal addressed to a process group.
//
// It exists because Docker Desktop's default seccomp profile returns nil from
// kill(-pid, SIGKILL) without delivering anything. A suite asserting that
// group isolation works reports a leak there no matter how correct the code
// is, and the failure reads as a defect rather than as the environment
// refusing the mechanism.
//
// The probe is a real fork, a real group and a real kill, with no test
// framework in between: what passes here is what production gets.
func groupKillWorksHere(t *testing.T) bool {
	t.Helper()

	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a process to probe group signalling: %v", err)
	}
	pid := cmd.Process.Pid

	// Give the kernel a moment to settle, then signal the group.
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return false
	}

	// Neither answer from the kill itself proves delivery, so poll for the
	// process actually leaving. A delivered SIGKILL is not negotiable.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			cmd.Wait()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}

	cmd.Process.Kill()
	cmd.Wait()
	return false
}

//go:build linux || darwin || freebsd || netbsd || openbsd

package exec

import (
	"os/exec"
	"syscall"
)

// isolate puts cmd in its own process group and arranges for a deadline to kill
// the whole group rather than only the direct child.
//
// It exists because `sh -c "sleep 30"` forks a grandchild and waits for it.
// Killing only the sh leaves the grandchild alive, holding the pipe the caller
// is reading, so the call blocks until the grandchild exits — long after the
// deadline fired. Killing the group takes the grandchild with it.
//
// The group is the child's own, never the caller's: Setpgid makes the child a
// group leader, so a negative pid addresses only the process AES spawned. A
// command that never starts has no pid to signal, and Cancel is then simply
// never consulted.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid addresses the whole group. Fall back to the direct child
		// if the group is already gone, which is the common case for a command
		// that exited between the deadline and the kill.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// processExists reports whether pid names a live process. It is the liveness
// check the orphan test needs: signal 0 probes without delivering anything, so
// it cannot kill or otherwise disturb the process it asks about.
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

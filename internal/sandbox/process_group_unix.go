//go:build linux || darwin || freebsd || netbsd || openbsd

package sandbox

import (
	"os/exec"
	"syscall"
	"time"
)

// hostWaitDelay bounds Wait itself, the same way internal/exec's waitDelay
// does. It is deliberately shorter than StepTimeout: it exists so a child that
// outlives SIGKILL cannot hold the call open after the deadline, not to
// shorten a legitimate run.
const hostWaitDelay = 2 * time.Second

// hostIsolate puts cmd in its own process group and arranges for a deadline to
// kill the whole group rather than only the direct child.
//
// This is the same mechanism internal/exec uses, and for the same reason: a
// command that forks leaves a grandchild holding the pipe the caller reads,
// so the deadline fires and the kill lands while the call still blocks. The
// sandbox is where the installer actually runs, so the orphan would be an
// installer process group left running on the developer's machine.
//
// It is named hostIsolate rather than isolate because this package is not
// internal/exec and the two must not be confused: one bounds a probe, the
// other bounds an install, and a future reader should not have to work out
// which is which from the name alone.
func hostIsolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid addresses the whole group. Fall back to the direct
		// child if the group is already gone, which is the common case for a
		// command that exited between the deadline and the kill.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

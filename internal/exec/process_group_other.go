//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package exec

import "os/exec"

// isolate is a no-op where process groups are not available. WaitDelay in run
// still bounds the call, so the deadline is honoured even though a grandchild
// may outlive it. AES targets darwin and linux; this exists so the package
// builds elsewhere rather than being a portability claim it cannot keep.
func isolate(cmd *exec.Cmd) {}

// processExists has no portable signal-0 equivalent. Reporting false keeps the
// orphan test from asserting liveness on a platform where it cannot be
// measured; the timing assertion still covers the call being bounded.
func processExists(pid int) bool { return false }

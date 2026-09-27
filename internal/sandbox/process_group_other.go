//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package sandbox

import (
	"os/exec"
	"time"
)

// hostWaitDelay bounds Wait itself. Declared here so the portable build
// compiles; same value and same reason as the unix file: it exists so a child
// that outlives SIGKILL cannot hold the call open after the deadline, not to
// shorten a legitimate run.
const hostWaitDelay = 2 * time.Second

// hostIsolate is a no-op where process groups are not available. WaitDelay
// still bounds the call, so the deadline is honoured even though a grandchild
// may outlive it. AES targets darwin and linux; this exists so the package
// builds elsewhere rather than being a portability claim it cannot keep.
func hostIsolate(cmd *exec.Cmd) {}

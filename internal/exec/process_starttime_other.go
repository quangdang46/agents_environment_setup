//go:build !linux

package exec

import "syscall"

// processStartTime has no portable implementation. /proc is Linux-only, and
// the orphan test already covers the property through the marker test and the
// timing assertion on every platform. Report the limitation as "cannot tell"
// rather than as false, so this file can never make a true leak read as a
// pass — and the caller skips, which is the honest answer on a platform where
// liveness-by-start-time cannot be measured.
func processStartTime(pid int) (string, error) {
	if syscall.Kill(pid, 0) != nil {
		return "", syscall.ESRCH
	}
	return "", syscall.ENOSYS
}

// processExistsAt defers to the plain liveness check where starttimes are
// unavailable. Recycled-pid flakiness is a Linux-under-load shape; everywhere
// else this is no worse than what signal 0 always was.
func processExistsAt(pid int, when string) (bool, error) {
	return syscall.Kill(pid, 0) == nil, nil
}

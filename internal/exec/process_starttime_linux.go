//go:build linux

package exec

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// processStartTime reads the time a process started, from /proc. It exists so
// the orphan test can tell a surviving grandchild apart from a pid that the
// kernel recycled onto an unrelated process between the kill and the check.
//
// A bare `syscall.Kill(pid, 0)` answers "is some process alive in this slot",
// which under a loaded -race suite fired roughly once in three full-suite runs
// and never in isolation — the slot was alive, the process was not ours.
//
// The returned value is the raw starttime field from /proc/pid/stat: ticks
// since boot. It is compared for equality, never interpreted, so no clock
// rate conversion is needed and none is attempted.
func processStartTime(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// The comm field is parenthesised and may itself contain spaces and
	// parens, so the split starts after its closing paren.
	s := string(raw)
	end := strings.LastIndex(s, ")")
	if end < 0 {
		return "", fmt.Errorf("cannot parse /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[end+1:])
	// field index, counting from 1 after the comm: starttime is 22.
	if len(fields) < 22 {
		return "", fmt.Errorf("/proc/%d/stat has %d fields, want at least 22", pid, len(fields))
	}
	return fields[21], nil
}

// processExistsAt reports whether the process in slot pid is the one that
// started at when. False means one of two true things: the slot is empty, or
// it holds a process that started later. Both are "not our grandchild".
func processExistsAt(pid int, when string) (bool, error) {
	if syscall.Kill(pid, 0) != nil {
		return false, nil
	}
	now, err := processStartTime(pid)
	if err != nil {
		return false, err
	}
	return now == when, nil
}

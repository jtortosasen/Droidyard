package platform

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
)

// ErrNoProcess is StartTime's answer for a pid no process holds.
var ErrNoProcess = errors.New("no such process")

// StartTime is the kernel's start time of the process pid, an opaque value compared
// only for equality: a record whose pid is alive with another start time names a
// process that reused the pid (ADR-0021 §Pid records). It is p_starttime in
// microseconds on macOS and field 22 of /proc/<pid>/stat, in clock ticks since boot,
// on Linux. A pid no process holds gives ErrNoProcess.
func StartTime(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("pid %d is not a process", pid)
	}
	return startTime(pid)
}

// parseStat reads the start time, field 22, from a Linux /proc/<pid>/stat line. The
// second field is the command name in parentheses, which may itself hold spaces and
// parentheses, so the fields are counted after the last ')': the first of them is
// field 3.
func parseStat(line []byte) (int64, error) {
	end := bytes.LastIndexByte(line, ')')
	if end < 0 {
		return 0, fmt.Errorf("stat line has no ')': %q", line)
	}
	fields := bytes.Fields(line[end+1:])
	const after = 22 - 3
	if len(fields) <= after {
		return 0, fmt.Errorf("stat line has %d fields after the command name, want more than %d", len(fields), after)
	}
	return strconv.ParseInt(string(fields[after]), 10, 64)
}

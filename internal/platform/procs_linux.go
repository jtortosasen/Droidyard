package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

func systemProcs() procSource {
	return procSource{list: linuxList, read: linuxRead, state: linuxState}
}

// linuxList lists the numeric entries of /proc whose real uid is the daemon's.
func linuxList() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("listing the user's processes: %w", err)
	}
	uid := os.Getuid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/status")
		if err != nil {
			continue // gone since the listing
		}
		if real, ok := parseRealUID(b); ok && real == uid {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// linuxRead reads /proc/<pid>/exe and /proc/<pid>/cmdline.
func linuxRead(pid int) (string, []string, error) {
	dir := "/proc/" + strconv.Itoa(pid)
	exe, err := os.Readlink(dir + "/exe")
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		// The kernel refuses ptrace read access: the process is not dumpable (it gained
		// privileges or opted out) or lives in another user namespace (ADR-0132).
		return "", nil, fmt.Errorf("%w: %v", errReadDenied, err)
	}
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return "", nil, err
	}
	raw = bytes.TrimSuffix(raw, []byte{0})
	var args []string
	if len(raw) > 0 {
		for _, a := range bytes.Split(raw, []byte{0}) {
			args = append(args, string(a))
		}
	}
	return exe, args, nil
}

// linuxState re-reads /proc/<pid>/status: absent when it is gone.
func linuxState(pid int) (procState, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return procState{}, nil
	}
	if err != nil {
		return procState{}, err
	}
	return parseStatus(b)
}

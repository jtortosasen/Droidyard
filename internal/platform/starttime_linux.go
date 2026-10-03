package platform

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// startTime reads field 22 of /proc/<pid>/stat (ADR-0021 §Pid records).
func startTime(pid int) (int64, error) {
	line, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return 0, ErrNoProcess
	}
	if err != nil {
		return 0, err
	}
	return parseStat(line)
}

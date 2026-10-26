package platform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// szomb is the kernel's process state for a zombie (sys/proc.h SZOMB).
const szomb = 5

func systemProcs() procSource {
	return procSource{list: darwinList, read: procargs, state: darwinState}
}

// darwinList lists the pids whose real uid is the daemon's (sysctl kern.proc.ruid).
func darwinList() ([]int, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.ruid", os.Getuid())
	if err != nil {
		return nil, fmt.Errorf("listing the user's processes: %w", err)
	}
	pids := make([]int, 0, len(kps))
	for _, kp := range kps {
		pids = append(pids, int(kp.Proc.P_pid))
	}
	return pids, nil
}

// darwinState re-reads a pid with kern.proc.pid in the slice form, which tells a missing
// pid (an empty answer) from a failure, as starttime_darwin.go does.
func darwinState(pid int) (procState, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return procState{}, err
	}
	if len(kps) == 0 {
		return procState{}, nil
	}
	kp := kps[0]
	return procState{Present: true, Zombie: kp.Proc.P_stat == szomb, SavedUID: int(kp.Eproc.Pcred.P_svuid)}, nil
}

// procargs reads the executable path and the arguments of a pid from kern.procargs2:
// argc as a native-endian int32, the executable path, NUL padding, then argc strings.
func procargs(pid int) (string, []string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil, err
	}
	if len(b) < 4 {
		return "", nil, fmt.Errorf("kern.procargs2 answered %d bytes", len(b))
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	rest := b[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return "", nil, errors.New("kern.procargs2 has no executable path")
	}
	exe := string(rest[:i])
	rest = bytes.TrimLeft(rest[i:], "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			j = len(rest)
		}
		args = append(args, string(rest[:j]))
		if j < len(rest) {
			rest = rest[j+1:]
		} else {
			rest = nil
		}
	}
	return exe, args, nil
}

package platform

import "golang.org/x/sys/unix"

// startTime reads p_starttime through sysctl kern.proc.pid (ADR-0019, amended
// 2026-09-30). The slice form is used because it tells a missing pid, an empty
// answer, apart from a failure; the single-record form reports both as EIO.
func startTime(pid int) (int64, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if len(procs) == 0 {
		return 0, ErrNoProcess
	}
	t := procs[0].Proc.P_starttime
	return t.Sec*1_000_000 + int64(t.Usec), nil
}

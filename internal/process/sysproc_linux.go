package process

import "syscall"

// sysProcAttr puts a child in a process group of its own and, on Linux, asks for
// SIGTERM when the thread that forked it exits (process-lifecycle.md §Shutdown,
// layers 1 and 2): the spawner thread lives as long as the supervisor.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

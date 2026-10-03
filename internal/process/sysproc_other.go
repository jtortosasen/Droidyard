//go:build !linux

package process

import "syscall"

// sysProcAttr puts a child in a process group of its own. macOS has no Pdeathsig:
// stdin EOF and the pid records cover a daemon that dies (process-lifecycle.md
// §Shutdown, layers 1 and 3).
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

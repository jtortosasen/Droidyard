//go:build !linux

package process

import "syscall"

// sysProcAttr puts a child in a process group of its own. macOS has no Pdeathsig, so
// the per-kind choice makes no runtime difference here (deathSignal is still the
// decision the Linux mapping reads, and the tests assert it): stdin EOF and the pid
// records cover a daemon that dies (process-lifecycle.md §Shutdown, layers 1 and 3).
func sysProcAttr(kind Kind) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

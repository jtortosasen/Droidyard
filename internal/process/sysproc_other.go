//go:build !linux

package process

import "syscall"

// sysProcAttr puts a child in a process group of its own. macOS has no Pdeathsig, so
// the per-kind choice makes no runtime difference here (deathSignal is still the
// decision the Linux mapping reads, and the tests assert it): stdin EOF and the pid
// records cover a daemon that dies (process-lifecycle.md §Shutdown, layers 1 and 3).
//
// A git write (GitWrite) is its own session leader instead (Setsid, which makes it the
// leader of a new group too: pid == pgid == sid) and has no terminal for an ssh or a
// credential helper to prompt on. Setsid with Setpgid fails at fork, since a session
// leader cannot setpgid, so the kind is given Setsid alone (plan m2n P1, item 6).
func sysProcAttr(kind Kind) *syscall.SysProcAttr {
	if kind == GitWrite {
		return &syscall.SysProcAttr{Setsid: true}
	}
	return &syscall.SysProcAttr{Setpgid: true}
}

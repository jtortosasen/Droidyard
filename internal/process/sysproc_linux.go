package process

import "syscall"

// sysProcAttr puts a child in a process group of its own and, for every kind but
// shell-command and git-write, asks for SIGTERM when the thread that forked it exits
// (Pdeathsig; process-lifecycle.md §Shutdown, layers 1 and 2): the spawner thread lives as
// long as the supervisor. A shell.exec command and a git write are the exceptions, so
// their leaders survive the daemon and Reconcile can reap their groups (plan m1k D5,
// plan m2n P2; deathSignal).
//
// A git write (GitWrite) is its own session leader instead (Setsid, which makes it the
// leader of a new group too: pid == pgid == sid) and has no terminal for an ssh or a
// credential helper to prompt on. Setsid with Setpgid fails at fork, since a session
// leader cannot setpgid, so the kind is given Setsid alone (plan m2n P1, item 6).
func sysProcAttr(kind Kind) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if kind == GitWrite {
		attr = &syscall.SysProcAttr{Setsid: true}
	}
	if deathSignal(kind) {
		attr.Pdeathsig = syscall.SIGTERM
	}
	return attr
}

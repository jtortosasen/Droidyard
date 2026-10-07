package process

import "syscall"

// sysProcAttr puts a child in a process group of its own and, for every kind but
// shell-command, asks for SIGTERM when the thread that forked it exits (Pdeathsig;
// process-lifecycle.md §Shutdown, layers 1 and 2): the spawner thread lives as long as
// the supervisor. A shell.exec command is the one exception, so its leader survives the
// daemon and Reconcile can reap its group (plan m1k D5; deathSignal).
func sysProcAttr(kind Kind) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if deathSignal(kind) {
		attr.Pdeathsig = syscall.SIGTERM
	}
	return attr
}

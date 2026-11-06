package process

import "syscall"

// sysProcAttr puts a child in a process group of its own and, for every kind but
// sdk-tool, asks for SIGTERM when the thread that forked it exits (Pdeathsig;
// process-lifecycle.md §Shutdown, layers 1 and 2): the spawner thread lives as long as the
// supervisor. An avdmanager run is the exception, so its leader survives the daemon and
// Reconcile can reap its group (plan m3f step 6; deathSignal).
//
// An avdmanager run (SDKTool) is its own session leader instead (Setsid, which makes it the
// leader of a new group too: pid == pgid == sid) and has no terminal for a
// prompt to land on. Setsid with Setpgid fails at fork, since a session
// leader cannot setpgid, so the kind is given Setsid alone (plan m2n P1, item 6; m3f step 6).
func sysProcAttr(kind Kind) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if kind == SDKTool {
		attr = &syscall.SysProcAttr{Setsid: true}
	}
	if deathSignal(kind) {
		attr.Pdeathsig = syscall.SIGTERM
	}
	return attr
}

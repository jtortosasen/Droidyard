//go:build unix

package chats

import (
	"os/exec"
	"syscall"
)

// startGroup is the supervisor's spawner in miniature: the child gets a process group of
// its own, which the reader kills as a whole.
func startGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

// processGone reports whether no process has the pid any more.
func processGone(pid int) bool {
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

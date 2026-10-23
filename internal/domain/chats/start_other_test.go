//go:build !unix

package chats

import "os/exec"

func startGroup(cmd *exec.Cmd) error { return cmd.Start() }

func processGone(pid int) bool { return true }

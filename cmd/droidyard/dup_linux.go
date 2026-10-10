//go:build linux

package main

import "golang.org/x/sys/unix"

// dupOnto makes newfd refer to what oldfd refers to, closing what newfd held. Dup3,
// because linux/arm64 has no dup2 system call.
func dupOnto(oldfd, newfd int) error { return unix.Dup3(oldfd, newfd, 0) }

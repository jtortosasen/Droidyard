//go:build darwin

package main

import "golang.org/x/sys/unix"

// dupOnto makes newfd refer to what oldfd refers to, closing what newfd held.
func dupOnto(oldfd, newfd int) error { return unix.Dup2(oldfd, newfd) }

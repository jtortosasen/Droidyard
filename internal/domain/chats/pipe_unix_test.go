//go:build unix

package chats

import (
	"syscall"
	"testing"
)

// makePipe makes a named pipe at path; false (after skipping) where it cannot.
func makePipe(t *testing.T, path string) bool {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
		return false
	}
	return true
}

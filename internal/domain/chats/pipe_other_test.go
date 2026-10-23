//go:build !unix

package chats

import "testing"

func makePipe(t *testing.T, path string) bool {
	t.Helper()
	t.Skip("named pipes are made on unix only")
	return false
}

package platform

import (
	"runtime"
	"testing"
)

// TestTotalMemory: the host's memory reads as a plausible amount on the systems the daemon
// supports (ADR-0002); the AVD RAM limit is half of it.
func TestTotalMemory(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("memory is read on macOS and Linux")
	}
	n, err := TotalMemory()
	if err != nil {
		t.Fatal(err)
	}
	if n < 256<<20 || n > 64<<40 {
		t.Errorf("TotalMemory = %d bytes, not a plausible amount of memory", n)
	}
}

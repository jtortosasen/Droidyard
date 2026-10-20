// Package flaky is the stress tool's fixture (../../stress.go): five tests whose
// failure or cost depends on the configuration the tool runs them under, so the tool
// can be shown to catch each. Nothing here is Droidyard code.
package flaky

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// burnEnv makes this test binary, re-executed, burn CPU instead of running tests.
const burnEnv = "FLAKY_BURN_CPU"

func TestMain(m *testing.M) {
	if os.Getenv(burnEnv) != "" {
		burn(time.Second)
		return
	}
	os.Exit(m.Run())
}

// burn spins until the process has used d of CPU time (user plus system), however
// loaded the machine is, so a run's CPU cost does not depend on the load.
func burn(d time.Duration) {
	var ru syscall.Rusage
	n := 0
	for {
		n++
		if n%1_000_000 != 0 {
			continue
		}
		if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
			return
		}
		used := time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
		if used >= d {
			return
		}
	}
}

// TestNeedsTwoCPUs fails only when the test binary runs on one CPU (-cpu 1).
func TestNeedsTwoCPUs(t *testing.T) {
	if runtime.GOMAXPROCS(0) == 1 {
		t.Fatal("GOMAXPROCS is 1: this test needs two CPUs")
	}
}

// TestExclusive fails only beside a copy of itself: it takes a non-blocking flock on a
// fixed file for 200 ms. The kernel releases the lock if the process is killed, so a
// killed run leaves nothing behind.
func TestExclusive(t *testing.T) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "flaky-exclusive.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("another copy of this test holds the lock: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
}

// TestSlow costs wall time and no CPU.
func TestSlow(t *testing.T) { time.Sleep(time.Second) }

// TestBurns waits for a child process that burns about a second of CPU: wall time and
// CPU time both show it, and the child's CPU belongs to the waited-for test binary.
func TestBurns(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), burnEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("burner: %v\n%s", err, out)
	}
}

// TestStable depends on nothing.
func TestStable(t *testing.T) {}

package device

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/process"
)

func newRunner(t *testing.T, env []string) *ADB {
	t.Helper()
	sup := process.New(process.Options{})
	t.Cleanup(sup.Shutdown)
	return NewADB(env, sup.Spawn)
}

type forked struct{ client, clientPgid, child int }

func readInfo(t *testing.T, path string) forked {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake adb left no info: %v", err)
	}
	f := strings.Fields(string(b))
	return forked{atoi(t, f[0]), atoi(t, f[1]), atoi(t, f[2])}
}

func gone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// TestRunnerSparesTheForkedServer pins R8 and adb-device.md §Lifecycle and
// concurrency: a client that forks a long-lived child in its own process group and
// leaves it holding the output pipe (as `adb` forks the shared server) must neither
// hold Run past its timeout nor take the child down with it.
//
//	hang:  Run returns within timeout + 1 s with ErrTimeout, the client is dead, the
//	       child is alive (same pid, same start time), and the client ran in a process
//	       group of its own, not the test's.
//	exit0: the client exits 0 at once: Run reports success (not exec.ErrWaitDelay)
//	       with its output within 2 s (the plan says 1 s; the fake's own start under
//	       -race takes most of it), the child alive.
func TestRunnerSparesTheForkedServer(t *testing.T) {
	for _, mode := range []string{modeForkHang, modeForkExit} {
		t.Run(mode, func(t *testing.T) {
			info := filepath.Join(t.TempDir(), "info")
			a := newRunner(t, fakeEnvFor(t, mode, "1", fakeInfo+"="+info))
			res := a.Resolve()
			if !res.Found {
				t.Fatalf("fake adb not resolved: %+v", res)
			}
			start := time.Now()
			// The timeout must outlast the fake's start (slow under -race) in the exit
			// case: there it is the cap WaitDelay is measured against.
			timeout := time.Second
			if mode == modeForkExit {
				timeout = 10 * time.Second
			}
			out, err := a.Run(context.Background(), res.Path, timeout, 1<<10, "devices")
			took := time.Since(start)
			f := readInfo(t, info)
			t.Cleanup(func() { syscall.Kill(f.child, syscall.SIGKILL) })
			childStart, serr := platform.StartTime(f.child)
			if serr != nil {
				t.Fatalf("the forked child is not running after Run: %v", serr)
			}
			if gone(f.child) {
				t.Fatal("the forked child died with the client")
			}
			if mode == modeForkHang {
				if !errors.Is(err, ErrTimeout) {
					t.Errorf("err = %v, want ErrTimeout", err)
				}
				if took > timeout+time.Second {
					t.Errorf("Run took %v, want within %v", took, timeout+time.Second)
				}
				if !gone(f.client) {
					t.Error("the client survived its timeout")
				}
				mine, _ := syscall.Getpgid(0)
				if f.clientPgid == mine {
					t.Errorf("the client's pgid %d is the test process's: it did not get a group of its own", mine)
				}
			} else {
				if err != nil {
					t.Errorf("err = %v, want success (a client that exited 0 while a child holds the pipe)", err)
				}
				if errors.Is(err, exec.ErrWaitDelay) {
					t.Error("Run reported exec.ErrWaitDelay for a client that exited 0")
				}
				if took > 2*time.Second {
					t.Errorf("Run took %v, want well under its %v timeout (the forked child holds the pipe for 2 minutes)", took, timeout)
				}
				if string(out.Stdout) != "hello\n" || out.ExitCode != 0 {
					t.Errorf("output %q exit %d, want %q and 0", out.Stdout, out.ExitCode, "hello\n")
				}
			}
			// The same process, not a reused pid: start time unchanged a moment later.
			time.Sleep(100 * time.Millisecond)
			if again, err := platform.StartTime(f.child); err != nil || again != childStart {
				t.Errorf("the child changed or died: %v, start %d then %d", err, childStart, again)
			}
		})
	}
}

// TestRunnerCancelKillsOnlyTheClient pins "kills only the client's pid on cancel":
// cancelling ctx ends Run with the context's error, not ErrTimeout, and the forked
// child lives.
func TestRunnerCancelKillsOnlyTheClient(t *testing.T) {
	info := filepath.Join(t.TempDir(), "info")
	a := newRunner(t, fakeEnvFor(t, modeForkHang, "1", fakeInfo+"="+info))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(info); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := a.Run(ctx, a.Resolve().Path, time.Minute, 1<<10)
	f := readInfo(t, info)
	t.Cleanup(func() { syscall.Kill(f.child, syscall.SIGKILL) })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !gone(f.client) || gone(f.child) {
		t.Errorf("client gone %v (want true), child gone %v (want false)", gone(f.client), gone(f.child))
	}
}

// TestRunnerCapsOutputWhileReading pins adb-device.md §Transport to the device's
// output cap: more than the cap is dropped, flagged, and the client is not blocked.
func TestRunnerCapsOutputWhileReading(t *testing.T) {
	a := newRunner(t, fakeEnvFor(t, modeVersion, "1", fakeBuild+"=41"))
	out, err := a.Run(context.Background(), a.Resolve().Path, 5*time.Second, 10, "version")
	if err != nil || len(out.Stdout) != 10 || !out.Truncated {
		t.Errorf("err %v, %d bytes, truncated %v; want 10 bytes, truncated", err, len(out.Stdout), out.Truncated)
	}
}

func TestRunnerWithoutAStarterDoesNotRun(t *testing.T) {
	a := NewADB(fakeEnvFor(t, modeVersion, "1"), nil)
	if _, err := a.Run(context.Background(), "x", time.Second, 1); !errors.Is(err, ErrNoStarter) {
		t.Errorf("err = %v, want ErrNoStarter", err)
	}
}

// TestInProcessRunnerKeepsTheRunnersContract pins the seam the fake adb goes through
// (NewADBWithExec): Run's timeout, cancellation, output cap and exit-code semantics are
// the process runner's, so a test over the fake proves what it would over a process. A
// timeout is ErrTimeout and exit -1; a cancel is the context's error; a non-zero exit
// is in Output, not an error; the cap drops the rest and flags it.
func TestInProcessRunnerKeepsTheRunnersContract(t *testing.T) {
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.hang", nil)
	a := newFake(t, dir, closedPort(t))
	path := a.Resolve().Path

	start := time.Now()
	out, err := a.Run(context.Background(), path, 100*time.Millisecond, 1<<10, "devices", "-l")
	if !errors.Is(err, ErrTimeout) || out.ExitCode != -1 || time.Since(start) > 5*time.Second {
		t.Errorf("hang: err %v exit %d after %v; want ErrTimeout, -1, promptly", err, out.ExitCode, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, path, time.Minute, 1<<10, "devices", "-l"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Errorf("cancelled: err %v, want context.Canceled and not ErrTimeout", err)
	}

	os.Remove(filepath.Join(dir, "devices.hang"))
	devicetest.Write(t, dir, "devices.out", []byte("0123456789abcdef"))
	devicetest.Write(t, dir, "devices.err", []byte("oops"))
	devicetest.Write(t, dir, "devices.exit", []byte("3"))
	out, err = a.Run(context.Background(), path, time.Minute, 10, "devices", "-l")
	if err != nil || out.ExitCode != 3 || string(out.Stdout) != "0123456789" || !out.Truncated || string(out.Stderr) != "oops" {
		t.Errorf("answer: err %v exit %d stdout %q stderr %q truncated %v; want exit 3 in Output, the first 10 bytes, truncated", err, out.ExitCode, out.Stdout, out.Stderr, out.Truncated)
	}
}

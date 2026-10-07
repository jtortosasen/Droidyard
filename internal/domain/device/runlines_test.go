package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// RunLines is Run for a stream consumed as it arrives (plan m1i step 2 builds it, step 3
// uses it for the bounded follow). These tests pin the decisions it makes: lines reach
// the callback as they arrive and without their newline, the callback's error ends the
// call and kills the client, stderr is capped on its own, and the timeout and exit-code
// mapping are Run's.

// Decision: stdout is handed over line by line as each arrives, not after the client
// exits, and the trailing CR of a \r\n line is not part of the line. Seen failing with
// the stream buffered until exit (the gap below would be ~0) and with the CR kept.
func TestRunLinesDeliversLinesAsTheyArrive(t *testing.T) {
	a := newRunner(t, fakeEnvFor(t, modeLines, "1"))
	var got []string
	var firstAt, endAt time.Time
	out, err := a.RunLines(context.Background(), a.Resolve().Path, 10*time.Second, 1<<10, func(line []byte) error {
		if firstAt.IsZero() {
			firstAt = time.Now()
		}
		got = append(got, string(line))
		return nil
	}, "logcat")
	endAt = time.Now()
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if want := []string{"first", "second", "third"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q (CR stripped, in order)", got, want)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", out.ExitCode)
	}
	// The first line arrived while the client still had two 300 ms pauses to go: a
	// buffered implementation would deliver everything at the end, immediately before it
	// returned.
	if endAt.Sub(firstAt) < 400*time.Millisecond {
		t.Errorf("the first line reached the callback only %v before the call ended: stdout was not streamed", endAt.Sub(firstAt))
	}
}

// Decision: the callback's own error ends the call at once, and the client is killed,
// not left writing into a pipe no one reads. Seen failing with the error swallowed (the
// call runs to its timeout).
func TestRunLinesCallbackErrorEndsTheCall(t *testing.T) {
	info := filepath.Join(t.TempDir(), "info")
	a := newRunner(t, fakeEnvFor(t, modeLinesOut, "1", fakeInfo+"="+info))
	sentinel := errors.New("enough")
	_, err := a.RunLines(context.Background(), a.Resolve().Path, 10*time.Second, 1<<10, func([]byte) error {
		return sentinel
	}, "logcat")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's own error", err)
	}
	pid := waitPID(t, info)
	if !gone(pid) {
		t.Errorf("the client %d is still writing after the callback stopped the call", pid)
	}
}

// Decision: a non-zero exit is not an error (it is in Output, as in Run) and stderr is
// capped on its own without cutting stdout, which the callback sees in full.
func TestRunLinesCapsStderrAndLeavesANonZeroExitAsData(t *testing.T) {
	a := newRunner(t, fakeEnvFor(t, modeLinesErr, "1"))
	var got []string
	out, err := a.RunLines(context.Background(), a.Resolve().Path, 10*time.Second, 10, func(line []byte) error {
		got = append(got, string(line))
		return nil
	}, "logcat")
	if err != nil {
		t.Fatalf("err = %v, want none for a non-zero exit", err)
	}
	if out.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", out.ExitCode)
	}
	if len(out.Stderr) != 10 || !out.Truncated {
		t.Errorf("stderr %d bytes truncated %v, want 10 and true", len(out.Stderr), out.Truncated)
	}
	if strings.Join(got, "|") != "one|two" {
		t.Errorf("lines = %q, want one|two", got)
	}
}

// Decision: the timeout is Run's: ErrTimeout, exit -1, and the client killed though it
// would print forever.
func TestRunLinesTimesOutAndKillsTheClient(t *testing.T) {
	info := filepath.Join(t.TempDir(), "info")
	a := newRunner(t, fakeEnvFor(t, modeLinesOut, "1", fakeInfo+"="+info))
	out, err := a.RunLines(context.Background(), a.Resolve().Path, time.Second, 1<<10, func([]byte) error { return nil }, "logcat")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if out.ExitCode != -1 {
		t.Errorf("exit = %d, want -1", out.ExitCode)
	}
	pid := waitPID(t, info)
	if !gone(pid) {
		t.Errorf("the client %d survived its timeout", pid)
	}
}

// Decision: the in-process seam (NewADBWithExec) keeps RunLines' contract — the lines
// are split and their CR stripped, and stderr is capped with the flag set — so a test
// over the fake proves what a test over a process would.
func TestRunLinesOverTheInProcessExec(t *testing.T) {
	a := NewADBWithExec(nil, func(context.Context, string, []string) ([]byte, []byte, int, error) {
		return []byte("a\r\nb\n"), []byte("stderr"), 0, nil
	})
	var got []string
	out, err := a.RunLines(context.Background(), "x", time.Second, 2, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if strings.Join(got, "|") != "a|b" {
		t.Errorf("lines = %q, want a|b", got)
	}
	if string(out.Stderr) != "st" || !out.Truncated {
		t.Errorf("stderr = %q truncated %v, want %q and true", out.Stderr, out.Truncated, "st")
	}
}

// waitPID reads the pid a fake wrote to info, waiting for the file: the fake writes it
// once it runs, and the call under test may end first.
func waitPID(t *testing.T, info string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(info); err == nil && len(b) > 0 {
			return atoi(t, strings.TrimSpace(string(b)))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the fake adb never wrote its pid to %s", info)
	return 0
}

package platform

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// TestStartTimeTellsProcessesApart: the start time of a live process is stable across
// reads, two processes started apart differ, and a reaped pid reads as no such
// process. This is what tells our orphan from a process that reused its pid
// (ADR-0021 §Pid records).
func TestStartTimeTellsProcessesApart(t *testing.T) {
	first := sleeper(t)
	time.Sleep(20 * time.Millisecond)
	second := sleeper(t)

	a1, err := StartTime(first.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := StartTime(first.Process.Pid)
	if err != nil || a1 != a2 {
		t.Fatalf("two reads of pid %d: %d, then %d (%v)", first.Process.Pid, a1, a2, err)
	}
	b, err := StartTime(second.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if a1 == b {
		t.Errorf("two processes started 20 ms apart have one start time, %d", a1)
	}
	if self, err := StartTime(os.Getpid()); err != nil || self == 0 || self >= a1 {
		t.Errorf("own start time %d (%v); want non-zero and before its children's, %d", self, err, a1)
	}

	first.Process.Kill()
	first.Wait()
	if _, err := StartTime(first.Process.Pid); !errors.Is(err, ErrNoProcess) {
		t.Errorf("a reaped pid gave %v; want ErrNoProcess", err)
	}
}

// TestParseStatCountsAfterTheLastParen: on Linux the start time is field 22 of
// /proc/<pid>/stat, and the command name, field 2, may hold spaces and parentheses.
// The lines are written by hand in proc(5)'s format, not captured on a Linux host.
func TestParseStatCountsAfterTheLastParen(t *testing.T) {
	cases := map[string]struct {
		line string
		want int64
	}{
		"plain name": {
			"1234 (cat) R 1000 1234 1000 34816 1234 4194304 98 0 0 0 0 0 0 0 20 0 1 0 5423891 8384512 186 18446744073709551615 94374283014144 0 0 0 17 3 0 0\n",
			5423891,
		},
		"name holding ) ( and spaces": {
			"4242 (a) 99 (b c) S 1 4242 4242 0 -1 4194560 120 0 0 0 1 0 0 0 20 0 1 0 777 1052672 80 18446744073709551615 1 1 0 0 0\n",
			777,
		},
	}
	for name, c := range cases {
		got, err := parseStat([]byte(c.line))
		if err != nil || got != c.want {
			t.Errorf("%s: %d, %v; want %d", name, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "12 (x S 1", "12 (x) S 1 2 3"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

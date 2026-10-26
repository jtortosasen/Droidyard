package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeLsof writes an executable shell script as lsof and returns an Lsof that runs it.
func fakeLsof(t *testing.T, body string) Lsof {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lsof")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Lsof{Path: path, Timeout: 5 * time.Second, MaxOutput: lsofMaxOutput}
}

// lsofSample is step 1 (f)'s sanitised real output for the owner's adb server (pid 72901):
// one file under the SDK, the adb binary as its text file, everything else <other>. The
// <SDK> placeholder is replaced by sdk.
func lsofSample(t *testing.T, sdk string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/lsof_sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "<SDK>", sdk)
}

// catFile is a script body printing a file.
func catFile(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return "cat '" + f + "'"
}

// TestOpenUnderAcceptsRealOutput: what lsof really printed for a live pid, with its f
// lines, is well formed and yields the one file under the SDK. A parser that read the
// always-selected f field as malformed would refuse every real answer, so every check
// would be a permanent doubt.
func TestOpenUnderAcceptsRealOutput(t *testing.T) {
	sdk := "/h/Android/sdk"
	l := fakeLsof(t, catFile(t, lsofSample(t, sdk)))
	got, err := l.OpenUnder(context.Background(), []int{72901}, []string{sdk + "/platform-tools", sdk + "/emulator"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pid != 72901 || got[0].FD != "txt" || got[0].Path != sdk+"/platform-tools/adb" {
		t.Fatalf("got %+v, want the adb text file of pid 72901", got)
	}
	// A directory the file is not in: nothing.
	got, err = l.OpenUnder(context.Background(), []int{72901}, []string{sdk + "/emulator"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v for a package nothing holds", got, err)
	}
}

// TestOpenUnderDoesNotReadExitStatus: lsof exits 1 for a pid that vanished and still
// prints the others (spike m3f step 1 (f)); a non-zero exit is not a doubt, a pid listed
// is enough.
func TestOpenUnderDoesNotReadExitStatus(t *testing.T) {
	l := fakeLsof(t, catFile(t, lsofSample(t, "/s"))+"\nexit 1")
	got, err := l.OpenUnder(context.Background(), []int{72901}, []string{"/s/platform-tools"})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v; lsof's exit status 1 must not be read", got, err)
	}
}

// TestOpenUnderDoubts: every way lsof's answer cannot be trusted is an error, and the
// caller reads an error as "in use". The own pid stands for a live candidate.
func TestOpenUnderDoubts(t *testing.T) {
	self := os.Getpid()
	cases := []struct {
		name string
		l    func(t *testing.T) Lsof
		want string
	}{
		{"absent", func(t *testing.T) Lsof {
			return Lsof{Path: filepath.Join(t.TempDir(), "no-lsof"), Timeout: time.Second, MaxOutput: 1 << 20}
		}, "could not run"},
		{"too slow", func(t *testing.T) Lsof {
			l := fakeLsof(t, "exec sleep 30")
			l.Timeout = 300 * time.Millisecond
			return l
		}, "did not finish"},
		{"killed by a signal of its own", func(t *testing.T) Lsof {
			return fakeLsof(t, "kill -KILL $$")
		}, "killed by signal"},
		{"a line that is not a field", func(t *testing.T) Lsof {
			return fakeLsof(t, "printf 'p1\\nx3\\n'")
		}, "not a p, f or n field"},
		{"a name before any process", func(t *testing.T) Lsof {
			return fakeLsof(t, "printf 'n/x\\n'")
		}, "before any process"},
		{"an empty line", func(t *testing.T) Lsof {
			return fakeLsof(t, "printf 'p1\\n\\n'")
		}, "empty line"},
		{"over the cap", func(t *testing.T) Lsof {
			l := fakeLsof(t, "yes p1")
			l.MaxOutput = 64 << 10
			return l
		}, "more than"},
		{"a live candidate it did not list", func(t *testing.T) Lsof {
			return fakeLsof(t, "printf 'p1\\nfcwd\\nn/\\n'")
		}, "which is alive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.l(t).OpenUnder(context.Background(), []int{self}, []string{"/s"})
			if err == nil {
				t.Fatalf("no doubt; got %+v", got)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// TestOpenUnderLetsAGonePidBeMissing: a candidate that exited since it was listed has no p
// line and is not a doubt; only a live one is.
func TestOpenUnderLetsAGonePidBeMissing(t *testing.T) {
	l := fakeLsof(t, "exit 1")
	// A pid no process holds: the largest the kernel allows is far past what is in use.
	got, err := l.OpenUnder(context.Background(), []int{99999999}, []string{"/s"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v for a pid that is gone", got, err)
	}
}

// TestOpenUnderNothingToAsk: no candidates, no lsof run.
func TestOpenUnderNothingToAsk(t *testing.T) {
	l := Lsof{Path: "/nonexistent/lsof", Timeout: time.Second, MaxOutput: 1}
	if got, err := l.OpenUnder(context.Background(), nil, []string{"/s"}); err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// TestOpenUnderRealLsof: the real lsof over a real process that holds a real file, so the
// parser meets its true output. Skipped when this system has no lsof.
func TestOpenUnderRealLsof(t *testing.T) {
	l := DefaultLsof()
	if _, err := os.Stat(l.Path); err != nil {
		t.Skipf("no lsof at %s: %v", l.Path, err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "held.img")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := startHelper(t, "", "", "open:"+f)
	got, err := l.OpenUnder(context.Background(), []int{cmd.Process.Pid}, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != f || got[0].Pid != cmd.Process.Pid {
		t.Fatalf("got %+v, want %s held by pid %d", got, f, cmd.Process.Pid)
	}
}

package platform

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv names the mode a re-executed test binary runs in.
const helperEnv = "DROIDYARD_PLATFORM_HELPER"

// TestHelperProcess is not a test: the test binary re-executed as a long-lived child. In
// "sleep" mode it says so and sleeps; in "open:<path>" mode it opens the file first.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	if path, ok := strings.CutPrefix(mode, "open:"); ok {
		f, err := os.Open(path)
		if err != nil {
			os.Stdout.WriteString("error " + err.Error() + "\n")
			os.Exit(2)
		}
		defer f.Close()
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

// startHelper runs the test binary (copied to exe when exe is not empty, argv[0] argv0
// when it is not empty) in mode, waits until it says it is ready and kills it at cleanup.
func startHelper(t *testing.T, exe, argv0, mode string) *exec.Cmd {
	t.Helper()
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		exe = self
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperProcess$")
	if argv0 != "" {
		cmd.Args[0] = argv0
	}
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper said %q (%v)", line, err)
	}
	return cmd
}

func hasPid(procs []Proc, pid int) (Proc, bool) {
	for _, p := range procs {
		if p.Pid == pid {
			return p, true
		}
	}
	return Proc{}, false
}

// TestUserProcessesReadsExecutablePath: a process is known by its executable's full path,
// not by the kernel's 16-character command name, which cuts qemu-system-aarch64-headless
// to a prefix that an adb of another SDK shares (ADR-0129 §8, "in use by process names"
// rejected).
func TestUserProcessesReadsExecutablePath(t *testing.T) {
	const name = "a-thirty-character-long-name-0" // 30 characters
	if len(name) != 30 {
		t.Fatalf("the test name is %d characters", len(name))
	}
	cmd := startHelper(t, "", name, "sleep")
	procs, err := UserProcesses()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := hasPid(procs, cmd.Process.Pid)
	if !ok {
		t.Fatalf("pid %d is not among the user's processes", cmd.Process.Pid)
	}
	if p.Unreadable != "" {
		t.Fatalf("the helper was read as unreadable: %s", p.Unreadable)
	}
	self, _ := os.Executable()
	want, _ := filepath.EvalSymlinks(self)
	got, _ := filepath.EvalSymlinks(p.Exe)
	if got != want {
		t.Fatalf("exe = %q, want the full path %q", p.Exe, want)
	}
	if len(p.Args) == 0 || p.Args[0] != name {
		t.Fatalf("args = %q, want argv[0] %q", p.Args, name)
	}
}

// TestUserProcessesSkipsZombies: a child that exited and was not waited for is a zombie,
// which the kernel refuses to show (EINVAL on macOS, ENOENT on Linux) and which runs
// nothing and holds no file. Left in as "unreadable" it would refuse every update for as
// long as the parent lingers. Skipped by re-reading its state.
func TestUserProcessesSkipsZombies(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := processState(pid)
		if err == nil && st.Present && st.Zombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never became a zombie (state %+v, %v)", st, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	procs, err := UserProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := hasPid(procs, pid); ok {
		t.Fatalf("a zombie was listed: %+v", p)
	}
}

// TestUserProcessesSkipsVanished: a pid that exits between the listing and the read is
// gone, not a doubt. The seam between the two kills and reaps a real child.
func TestUserProcessesSkipsVanished(t *testing.T) {
	child := startHelper(t, "", "", "sleep")
	pid := child.Process.Pid
	src := systemProcs()
	read := src.read
	src.read = func(p int) (string, []string, error) {
		if p == pid {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
		return read(p)
	}
	// An absent pid answers a zero state, whose saved uid is 0: the uid compared against
	// is 0 as well, so that only the absent rule, not the setuid one, can skip it.
	procs, err := userProcesses(0, src)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := hasPid(procs, pid); ok {
		t.Fatalf("a vanished pid was listed: %+v", p)
	}
	if _, ok := hasPid(procs, os.Getpid()); !ok {
		t.Fatal("the test's own process is missing: the listing was cut short")
	}
}

// TestUserProcessesSkipsSetuid: a live process whose read fails and whose saved uid is not
// the user's is a setuid process such as login (spike m3f step 1 (f), ADR-0131 §1); every
// terminal session has them, so counting them as doubts would refuse every update. One
// whose saved uid is the user's is unreadable for a reason nobody knows, and a doubt.
func TestUserProcessesSkipsSetuid(t *testing.T) {
	uid := os.Getuid()
	fail := errors.New("invalid argument")
	src := func(saved int) procSource {
		return procSource{
			list:  func() ([]int, error) { return []int{9001}, nil },
			read:  func(int) (string, []string, error) { return "", nil, fail },
			state: func(int) (procState, error) { return procState{Present: true, SavedUID: saved}, nil },
		}
	}
	got, err := userProcesses(uid, src(uid+1))
	if err != nil || len(got) != 0 {
		t.Fatalf("setuid process: got %+v, %v; want it skipped", got, err)
	}
	got, err = userProcesses(uid, src(uid))
	if err != nil || len(got) != 1 || got[0].Pid != 9001 || got[0].Unreadable == "" {
		t.Fatalf("a process of the user's own saved uid: got %+v, %v; want it kept as unreadable", got, err)
	}
}

// TestUserProcessesStateReadFails: when even the re-read fails, nothing is known of the
// process and it stays a doubt.
func TestUserProcessesStateReadFails(t *testing.T) {
	src := procSource{
		list:  func() ([]int, error) { return []int{9002}, nil },
		read:  func(int) (string, []string, error) { return "", nil, syscall.EINVAL },
		state: func(int) (procState, error) { return procState{}, syscall.EPERM },
	}
	got, err := userProcesses(os.Getuid(), src)
	if err != nil || len(got) != 1 || got[0].Unreadable == "" {
		t.Fatalf("got %+v, %v; want one unreadable process", got, err)
	}
}

// TestParseStatus is the Linux reader's decision, checked on any system: the saved uid is
// the third field of Uid and a zombie is state Z.
func TestParseStatus(t *testing.T) {
	st, err := parseStatus([]byte("Name:\tlogin\nState:\tS (sleeping)\nUid:\t501\t0\t0\t0\n"))
	if err != nil || !st.Present || st.Zombie || st.SavedUID != 0 {
		t.Fatalf("got %+v, %v", st, err)
	}
	st, err = parseStatus([]byte("Name:\tx\nState:\tZ (zombie)\nUid:\t501\t501\t501\t501\n"))
	if err != nil || !st.Zombie || st.SavedUID != 501 {
		t.Fatalf("got %+v, %v", st, err)
	}
	if _, err := parseStatus([]byte("Name:\tx\n")); err == nil {
		t.Fatal("a status without State and Uid was accepted")
	}
	if n, ok := parseRealUID([]byte("Name:\tx\nUid:\t501\t0\t0\t0\n")); !ok || n != 501 {
		t.Fatalf("real uid = %d, %v", n, ok)
	}
}

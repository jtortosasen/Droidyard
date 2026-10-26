package sdk

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The in-use detector (plan m3f step 5) is tested against real processes: the test binary
// copied to where an SDK executable would be and re-executed as a helper that sleeps,
// listed by the real kernel interface and, where a test needs it, held-open files read by
// the real lsof. A fake stands in only where the subject is lsof's failure.

const inUseHelperEnv = "DROIDYARD_SDK_INUSE_HELPER"

// TestInUseHelperProcess is not a test: the test binary re-executed as a long-lived child.
// "sleep" says it is ready and sleeps; "open:<path>" opens the file first.
func TestInUseHelperProcess(t *testing.T) {
	mode := os.Getenv(inUseHelperEnv)
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

// helperAt copies the test binary to path, runs it in mode, waits until it is ready and
// kills it at cleanup. argv are the arguments after the program name, replacing none of
// the helper's own.
func helperAt(t *testing.T, path, mode string, argv ...string) *exec.Cmd {
	t.Helper()
	helperCopy(t, path)
	return startHelper(t, exec.Command(path, append([]string{"-test.run=^TestInUseHelperProcess$"}, argv...)...), mode)
}

// helperRelAt is helperAt started as ./<base> from the helper's directory, as a person
// runs a tool from inside platform-tools: the kernel then records a relative executable.
func helperRelAt(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	helperCopy(t, path)
	cmd := exec.Command("./"+filepath.Base(path), "-test.run=^TestInUseHelperProcess$")
	cmd.Dir = filepath.Dir(path)
	return startHelper(t, cmd, mode)
}

// helperCopy copies the test binary to path, executable.
func helperCopy(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

func startHelper(t *testing.T, cmd *exec.Cmd, mode string) *exec.Cmd {
	t.Helper()
	path := cmd.Path
	cmd.Env = append(os.Environ(), inUseHelperEnv+"="+mode)
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
		t.Fatalf("helper %s said %q (%v)", path, line, err)
	}
	return cmd
}

// realProcs is the host's processes with the ones the kernel will not show dropped: they
// are the subject of the setuid and doubt tests, not of a test about one helper.
func realProcs() ([]platform.Proc, error) {
	all, err := platform.UserProcesses()
	var out []platform.Proc
	for _, p := range all {
		if p.Unreadable == "" {
			out = append(out, p)
		}
	}
	return out, err
}

func noFiles(context.Context, []int, []string) ([]platform.OpenFile, error) { return nil, nil }

func pidOf(cmd *exec.Cmd) string { return "(pid " + strconv.Itoa(cmd.Process.Pid) + ")" }

// TestInUseByExecutable pins A10 (a): a process of the user running an executable that
// lies inside a package's directory uses it. lsof is replaced by an empty answer so only
// the executable's path can name the user; with (a) removed the package reads as free.
func TestInUseByExecutable(t *testing.T) {
	root := t.TempDir()
	cmd := helperAt(t, filepath.Join(root, "platform-tools", "adb"), "sleep")
	detect := NewInUse(InUseDeps{Procs: realProcs, Open: noFiles})
	got := detect(context.Background(), root, []string{"platform-tools", "emulator"})
	by := got["platform-tools"]
	if len(by) != 1 || !strings.Contains(by[0], "adb "+pidOf(cmd)) {
		t.Fatalf("platform-tools used by %q, want adb %s", by, pidOf(cmd))
	}
	if _, ok := got["emulator"]; ok {
		t.Errorf("emulator is named as used: %q", got["emulator"])
	}
}

// TestInUseSeesThroughASymlinkedRoot: the SDK reached through a link and a process running
// from the real path (or the reverse) is the same directory, so both spellings are
// compared. A detector that compared one spelling would call the package free.
func TestInUseSeesThroughASymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "sdk")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cmd := helperAt(t, filepath.Join(real, "platform-tools", "adb"), "sleep")
	detect := NewInUse(InUseDeps{Procs: realProcs, Open: noFiles})
	for _, root := range []string{real, link} {
		got := detect(context.Background(), root, []string{"platform-tools"})
		if by := got["platform-tools"]; len(by) != 1 || !strings.Contains(by[0], pidOf(cmd)) {
			t.Errorf("root %s: platform-tools used by %q, want %s", root, by, pidOf(cmd))
		}
	}
}

// TestInUseByOpenFile pins A10 (b): a candidate process outside the SDK that holds a file
// open inside it uses the package. The helper is named qemu-system-aarch64, as the
// emulator's is, and lives outside the root so its path cannot give it away; the real
// lsof finds the file. With (b) removed the image reads as free.
func TestInUseByOpenFile(t *testing.T) {
	root := t.TempDir()
	imgDir := filepath.Join(root, "system-images", "android-30", "default", "arm64-v8a")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(imgDir, "system.img")
	if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "platforms", "android-35"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(platform.DefaultLsof().Path); err != nil {
		t.Skipf("no lsof on this system: %v", err)
	}
	cmd := helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "open:"+img)
	detect := SystemInUse(nil)
	got := detect(context.Background(), root, []string{"system-images;android-30;default;arm64-v8a", "platforms;android-35"})
	by := got["system-images;android-30;default;arm64-v8a"]
	if len(by) != 1 || by[0] != "an emulator "+pidOf(cmd) {
		t.Fatalf("the system image is used by %q, want an emulator %s", by, pidOf(cmd))
	}
	if _, ok := got["platforms;android-35"]; ok {
		t.Errorf("platforms;android-35 is named as used: %q", got["platforms;android-35"])
	}
}

// TestInUseByARelativeExecutable: kern.procargs2 records the path given to execve, so a
// tool run as ./aapt2 from inside build-tools has a relative executable that (a) cannot
// place. It is a candidate, and lsof's absolute txt entry names the package (the close's
// verifier, B2).
func TestInUseByARelativeExecutable(t *testing.T) {
	if _, err := os.Stat(platform.DefaultLsof().Path); err != nil {
		t.Skipf("no lsof on this system: %v", err)
	}
	root := t.TempDir()
	cmd := helperRelAt(t, filepath.Join(root, "build-tools", "35.0.0", "aapt2x"), "sleep")
	got := SystemInUse(nil)(context.Background(), root, []string{"build-tools;35.0.0"})
	if by := got["build-tools;35.0.0"]; len(by) != 1 || by[0] != "process aapt2x "+pidOf(cmd) {
		t.Fatalf("build-tools;35.0.0 is used by %q, want process aapt2x %s", by, pidOf(cmd))
	}
}

type none struct{}

// TestInUseByOperation pins A10 (c) with the daemon's own operations: an emulator.start in
// flight uses the emulator and the system images, a build.assemble the build tools and the
// platforms, and neither uses the other's. They end with the operation.
func TestInUseByOperation(t *testing.T) {
	reg := capability.NewRegistry()
	started := make(chan string, 2)
	release := make(chan struct{})
	for _, name := range []string{"emulator.start", "build.assemble"} {
		reg.MustRegister(capability.Descriptor{
			Name: name, Kind: capability.Command, Scope: capability.Global, LongRunning: true, Since: "0.1.0",
			Description: "A blocked fixture.", Params: none{}, Result: none{},
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				started <- call.OperationID
				select {
				case <-release:
				case <-ctx.Done():
				}
				return none{}, nil
			},
		})
	}
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	var released atomic.Bool
	free := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(func() {
		free()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ops.Shutdown(ctx)
	})
	detect := NewInUse(InUseDeps{
		Procs:  func() ([]platform.Proc, error) { return nil, nil },
		Open:   noFiles,
		Active: ops.Active,
	})
	ids := []string{"emulator", "system-images;android-30;default;arm64-v8a", "build-tools;35.0.0", "platforms;android-35"}
	if got := detect(context.Background(), t.TempDir(), ids); len(got) != 0 {
		t.Fatalf("with nothing in flight, %v are in use", got)
	}

	start := func(capName string) string {
		id, err := ops.Start(context.Background(), capability.Request{Capability: capName, Client: capability.ClientMCP})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-started:
			if got != id {
				t.Fatalf("handler ran for %s, started %s", got, id)
			}
		case <-time.After(testtime.Bound(10 * time.Second)):
			t.Fatal("the handler never ran")
		}
		return id
	}
	boot := start("emulator.start")
	got := detect(context.Background(), t.TempDir(), ids)
	want := "an emulator boot in Droidyard (operation " + boot + ")"
	for _, id := range ids[:2] {
		if by := got[id]; len(by) != 1 || by[0] != want {
			t.Errorf("%s used by %q, want %q", id, by, want)
		}
	}
	for _, id := range ids[2:] {
		if by, ok := got[id]; ok {
			t.Errorf("%s is named as used by a boot: %q", id, by)
		}
	}

	build := start("build.assemble")
	got = detect(context.Background(), t.TempDir(), ids)
	wantBuild := "a build in Droidyard (operation " + build + ")"
	for _, id := range ids[2:] {
		if by := got[id]; len(by) != 1 || by[0] != wantBuild {
			t.Errorf("%s used by %q, want %q", id, by, wantBuild)
		}
	}
	if by := got["emulator"]; len(by) != 1 || by[0] != want {
		t.Errorf("emulator used by %q once a build also runs, want the boot's %q", by, want)
	}

	free()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the operations never ended", func() bool {
		return len(detect(context.Background(), t.TempDir(), ids)) == 0
	})
}

// fakeLsofScript writes an executable script as lsof and returns an Lsof that runs it.
func fakeLsofScript(t *testing.T, body string) platform.Lsof {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lsof")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return platform.Lsof{Path: path, Timeout: 5 * time.Second, MaxOutput: 4 << 20}
}

// TestInUseDoubts pins "any doubt counts as in use": whatever stops lsof's answer from
// being trusted names the package "could not check", never leaves it free. The candidate
// is the test's own process listed as a java, so it is alive. The last case is the
// counter-decision: lsof exiting 1 with a complete answer is not a doubt, because it does
// so for a pid that vanished.
func TestInUseDoubts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "platform-tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	procs := func() ([]platform.Proc, error) {
		return []platform.Proc{{Pid: self, Exe: "/usr/lib/jvm/bin/java", Args: []string{"java"}}}, nil
	}
	pkg := []string{"platform-tools"}
	check := func(t *testing.T, l platform.Lsof, want string) {
		t.Helper()
		got := NewInUse(InUseDeps{Procs: procs, Open: l.OpenUnder})(context.Background(), root, pkg)
		by := got["platform-tools"]
		if want == "" {
			if len(by) != 0 {
				t.Fatalf("a complete answer was read as a doubt: %q", by)
			}
			return
		}
		if len(by) != 1 || !strings.HasPrefix(by[0], "could not check: ") || !strings.Contains(by[0], want) {
			t.Fatalf("got %q, want could not check: ...%s...", by, want)
		}
	}
	t.Run("lsof absent", func(t *testing.T) {
		check(t, platform.Lsof{Path: filepath.Join(t.TempDir(), "no-lsof"), Timeout: time.Second, MaxOutput: 1 << 20}, "could not run")
	})
	t.Run("lsof too slow", func(t *testing.T) {
		l := fakeLsofScript(t, "exec sleep 30")
		l.Timeout = 300 * time.Millisecond
		check(t, l, "did not finish")
	})
	t.Run("lsof killed by a signal of its own", func(t *testing.T) {
		check(t, fakeLsofScript(t, "kill -KILL $$"), "killed by signal")
	})
	t.Run("lsof printing a line of no field", func(t *testing.T) {
		check(t, fakeLsofScript(t, "printf 'p"+strconv.Itoa(self)+"\\nx3\\n'"), "not a p, f or n field")
	})
	t.Run("lsof omitting a live candidate", func(t *testing.T) {
		check(t, fakeLsofScript(t, "printf 'p1\\nfcwd\\nn/\\n'"), "which is alive")
	})
	t.Run("lsof exiting 1 with a complete answer", func(t *testing.T) {
		check(t, fakeLsofScript(t, "printf 'p"+strconv.Itoa(self)+"\\nfcwd\\nn/\\n'\nexit 1"), "")
	})

	detect := func(procs func() ([]platform.Proc, error)) map[string][]string {
		return NewInUse(InUseDeps{Procs: procs, Open: noFiles})(context.Background(), root, pkg)
	}
	t.Run("a process that cannot be read", func(t *testing.T) {
		got := detect(func() ([]platform.Proc, error) {
			return []platform.Proc{{Pid: 4242, Unreadable: "invalid argument"}}, nil
		})
		if by := got["platform-tools"]; len(by) != 1 || !strings.Contains(by[0], "could not check: process 4242 could not be read") {
			t.Fatalf("got %q", by)
		}
	})
	t.Run("the process list failing", func(t *testing.T) {
		got := detect(func() ([]platform.Proc, error) { return nil, io.ErrUnexpectedEOF })
		if by := got["platform-tools"]; len(by) != 1 || !strings.HasPrefix(by[0], "could not check: ") {
			t.Fatalf("got %q", by)
		}
	})
	t.Run("a definite user wins over a doubt", func(t *testing.T) {
		got := detect(func() ([]platform.Proc, error) {
			return []platform.Proc{
				{Pid: 4242, Unreadable: "invalid argument"},
				{Pid: 7, Exe: filepath.Join(root, "platform-tools", "adb"), Args: []string{"adb", "fork-server", "server"}},
			}, nil
		})
		if by := got["platform-tools"]; len(by) != 1 || by[0] != "the adb server (pid 7)" {
			t.Fatalf("got %q, want the process that is named", by)
		}
	})
	t.Run("a string that is not a package id", func(t *testing.T) {
		got := NewInUse(InUseDeps{Procs: procs, Open: noFiles})(context.Background(), root, []string{"../etc"})
		if by := got["../etc"]; len(by) != 1 || !strings.HasPrefix(by[0], "could not check: ") {
			t.Fatalf("got %q", by)
		}
	})
}

// TestCandidates pins A10's candidate set: the processes that run SDK code or load SDK
// files, by where the executable lies, what the arguments name or the executable's base
// name. The base name is the executable's, not the kernel's 16-character one.
func TestCandidates(t *testing.T) {
	root := t.TempDir()
	procs := []platform.Proc{
		{Pid: 1, Exe: "/Applications/Android Studio.app/Contents/MacOS/studio"},
		{Pid: 2, Exe: "/usr/lib/jvm/bin/java"},
		{Pid: 3, Exe: "/opt/emu/qemu-system-aarch64-headless"},
		{Pid: 4, Exe: filepath.Join(root, "build-tools", "35", "aapt2")},
		{Pid: 5, Exe: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
		{Pid: 6, Exe: "/usr/bin/python3", Args: []string{"python3", filepath.Join(root, "tools", "x.py")}},
		{Pid: 7, Exe: "/usr/bin/vim", Args: []string{"vim", "notes.txt"}},
		{Pid: 8, Unreadable: "invalid argument"},
		{Pid: 9, Exe: "/opt/sdk2/platform-tools/adb"},
		{Pid: 10, Exe: "/opt/x/emulator"},
	}
	var got []int
	for _, p := range Candidates(root, procs) {
		got = append(got, p.Pid)
	}
	want := []int{1, 2, 3, 4, 6, 9, 10}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}

// TestDescribeProc: what a person reads in SDK_PACKAGE_IN_USE says what to stop.
func TestDescribeProc(t *testing.T) {
	cases := []struct {
		p    platform.Proc
		want string
	}{
		{platform.Proc{Pid: 1, Exe: "/s/platform-tools/adb", Args: []string{"adb", "-L", "tcp:5037", "fork-server", "server", "--reply-fd", "4"}}, "the adb server (pid 1)"},
		{platform.Proc{Pid: 2, Exe: "/s/platform-tools/adb", Args: []string{"adb", "devices"}}, "process adb (pid 2)"},
		{platform.Proc{Pid: 3, Exe: "/s/emulator/qemu-system-aarch64-headless"}, "an emulator (pid 3)"},
		{platform.Proc{Pid: 4, Exe: "/s/emulator/emulator"}, "an emulator (pid 4)"},
		{platform.Proc{Pid: 5, Exe: "/jdk/bin/java", Args: []string{"java", "-cp", "gradle.jar", "org.gradle.launcher.daemon.bootstrap.GradleDaemon", "8.9"}}, "a Gradle daemon (pid 5)"},
		{platform.Proc{Pid: 6, Exe: "/jdk/bin/java", Args: []string{"java", "-jar", "x.jar"}}, "process java (pid 6)"},
		{platform.Proc{Pid: 7, Exe: "/Applications/Android Studio.app/Contents/MacOS/studio"}, "Android Studio (pid 7)"},
	}
	for _, c := range cases {
		if got := describeProc(c.p); got != c.want {
			t.Errorf("describeProc(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// TestFreeWhenNothingUsesIt: a package nothing runs from, nothing holds open and no
// operation needs is absent from the answer, with the real processes and the real lsof.
func TestFreeWhenNothingUsesIt(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"platform-tools", "platforms/android-35"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(platform.DefaultLsof().Path); err != nil {
		t.Skipf("no lsof on this system: %v", err)
	}
	l := platform.DefaultLsof()
	got := NewInUse(InUseDeps{Procs: realProcs, Open: l.OpenUnder})(context.Background(), root, []string{"platform-tools", "platforms;android-35"})
	if len(got) != 0 {
		t.Fatalf("free packages named as used: %v", got)
	}
}

// TestDefaultIsNotChecked keeps the other side of the seam honest: without a detector an
// update is refused, as notChecked says.
func TestDefaultIsNotChecked(t *testing.T) {
	got := notChecked(context.Background(), "/s", []string{"a"})
	if by := got["a"]; len(by) != 1 || by[0] != "in use: not checked yet" {
		t.Fatalf("got %q", by)
	}
}

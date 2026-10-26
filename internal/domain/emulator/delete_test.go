package emulator

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// TestLauncherDir pins the launcher's rule as step 1 (d) observed it (ADR-0129 §7): the one
// directory the launcher lists is the first that exists of ANDROID_AVD_HOME,
// ANDROID_SDK_HOME/.android/avd and HOME/.android/avd; an ANDROID_AVD_HOME that does not
// exist falls back; none existing, HOME's is the answer; ANDROID_EMULATOR_HOME/avd and
// ANDROID_USER_HOME/avd are never it, though avdDirs reads them. Seen failing with avdDirs'
// first entry.
func TestLauncherDir(t *testing.T) {
	mk := func(dirs ...string) {
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	root := t.TempDir()
	avdHome := filepath.Join(root, "avdhome")
	sdkHome := filepath.Join(root, "sdkhome")
	home := filepath.Join(root, "home")
	emuHome := filepath.Join(root, "emuhome")
	userHome := filepath.Join(root, "userhome")
	envOf := func(extra ...string) []string { return append([]string{"HOME=" + home}, extra...) }
	all := envOf("ANDROID_AVD_HOME="+avdHome, "ANDROID_SDK_HOME="+sdkHome, "ANDROID_EMULATOR_HOME="+emuHome, "ANDROID_USER_HOME="+userHome)

	// Nothing exists: the HOME default, where the launcher would look.
	if got, want := launcherDir(all), filepath.Join(home, ".android", "avd"); got != want {
		t.Errorf("nothing exists: %q, want %q", got, want)
	}
	// ANDROID_EMULATOR_HOME/avd and ANDROID_USER_HOME/avd exist and are never chosen.
	mk(filepath.Join(emuHome, "avd"), filepath.Join(userHome, "avd"))
	if got, want := launcherDir(all), filepath.Join(home, ".android", "avd"); got != want {
		t.Errorf("only the two the launcher never reads exist: %q, want %q", got, want)
	}
	if first := avdDirs(all)[0].path; first == launcherDir(all) {
		t.Fatalf("avdDirs' first entry %q is the launcher's here, the fixture proves nothing", first)
	}
	// Each of the three, in order, as it comes to exist.
	mk(filepath.Join(home, ".android", "avd"))
	if got, want := launcherDir(all), filepath.Join(home, ".android", "avd"); got != want {
		t.Errorf("HOME's exists: %q, want %q", got, want)
	}
	mk(filepath.Join(sdkHome, ".android", "avd"))
	if got, want := launcherDir(all), filepath.Join(sdkHome, ".android", "avd"); got != want {
		t.Errorf("ANDROID_SDK_HOME's exists: %q, want %q", got, want)
	}
	mk(avdHome)
	if got := launcherDir(all); got != avdHome {
		t.Errorf("ANDROID_AVD_HOME exists: %q, want %q", got, avdHome)
	}
	// An ANDROID_AVD_HOME that is a file, or does not exist, falls back.
	if got, want := launcherDir(envOf("ANDROID_AVD_HOME="+filepath.Join(root, "missing"), "ANDROID_SDK_HOME="+sdkHome)), filepath.Join(sdkHome, ".android", "avd"); got != want {
		t.Errorf("a missing ANDROID_AVD_HOME: %q, want the fallback %q", got, want)
	}
	if got := launcherDir([]string{"PATH=/bin"}); got != "" {
		t.Errorf("no variable set: %q, want none", got)
	}
}

// ---- emulator.delete ---------------------------------------------------------------

// avdIn puts an AVD in the launcher's directory, as avdmanager would, and returns that
// directory.
func (f *avdFx) avdIn(t *testing.T, dir, name string) string {
	t.Helper()
	writeAVD(t, dir, name, "avd.ini.encoding=UTF-8\npath="+filepath.Join(dir, name+".avd")+"\ntarget=android-35\n", "hw.device.name=pixel_9a\n")
	return dir
}

func (f *avdFx) del(t *testing.T, client capability.ClientKind, avd string) capability.Outcome {
	t.Helper()
	return f.call(t, client, "emulator.delete", `{"avd":`+quote(avd)+`}`)
}

func (f *avdFx) deletes(t *testing.T) []sdktest.Call {
	t.Helper()
	var out []sdktest.Call
	for _, c := range sdktest.Calls(t, f.toolDir) {
		if len(c.Args) > 0 && c.Args[0] == "delete" {
			out = append(out, c)
		}
	}
	return out
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// TestDeleteRemoves pins emulator.delete's happy path (ADR-0129 §7): avdmanager is run with
// delete avd --name, ANDROID_AVD_HOME the directory holding the AVD, stdin at end of file
// (it is asked nothing), and afterwards the ini and the files are gone and the launcher no
// longer lists the AVD.
func TestDeleteRemoves(t *testing.T) {
	f := newAVD(t, nil)
	home := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	out := f.del(t, capability.ClientUI, "dy")
	if out.State != capability.StateCompleted {
		t.Fatalf("emulator.delete %s: %+v", out.State, out.Error)
	}
	if r := out.Result.(DeleteResult); r.Avd != "dy" {
		t.Errorf("result = %+v", r)
	}
	calls := f.deletes(t)
	if len(calls) != 1 {
		t.Fatalf("avdmanager ran delete %d times, want 1", len(calls))
	}
	c := calls[0]
	if got := strings.Join(c.Args, "|"); got != "delete|avd|--name|dy" {
		t.Errorf("argv = %q", got)
	}
	if c.Env["ANDROID_AVD_HOME"] != home || c.Stdin != "" || c.Env["ANDROID_SDK_ROOT"] != "<unset>" {
		t.Errorf("env %v and stdin %q, want ANDROID_AVD_HOME %s and end of file", c.Env, c.Stdin, home)
	}
	if exists(filepath.Join(home, "dy.ini")) || exists(filepath.Join(home, "dy.avd")) {
		t.Error("the AVD's files are still there")
	}
	if l := f.list(t); len(l.AVDs) != 0 {
		t.Errorf("emulator.list = %+v after the delete", l.AVDs)
	}

	// A tool that exits 0 and leaves the ini is a failure, not a delete.
	f2 := newAVD(t, nil)
	f2.avdIn(t, filepath.Join(f2.home, ".android", "avd"), "dy")
	sdktest.Write(t, f2.toolDir, "delete.noop", "")
	ce := mustFail(t, f2.del(t, capability.ClientUI, "dy"), "SDK_TOOL_FAILED")
	if !strings.Contains(ce.Cause, "dy.ini") {
		t.Errorf("cause = %q", ce.Cause)
	}
}

// TestDeleteNotListed: a name the launcher does not list is EMULATOR_AVD_NOT_FOUND with the
// names it lists; nothing is run.
func TestDeleteNotListed(t *testing.T) {
	f := newAVD(t, nil)
	f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	ce := mustFail(t, f.del(t, capability.ClientUI, "other"), ErrAVDNotFound)
	if names, _ := ce.Details["avds"].([]string); len(names) != 1 || names[0] != "dy" {
		t.Errorf("details = %v, want the listed AVD", ce.Details)
	}
	mustFail(t, f.del(t, capability.ClientUI, "../dy"), capability.ErrInvalidParams)
	if n := len(f.deletes(t)); n != 0 {
		t.Errorf("avdmanager ran delete %d times", n)
	}
}

// TestDeleteRefusesAmbiguous pins ADR-0129 §7: a name with an ini in two homes is not
// deleted at all, because which copy goes is a guess; the cause names the variables, never
// a path (m1i D13). The launcher lists the copy in ANDROID_AVD_HOME only, so deleting "the
// first match" would have been a plausible bug. Seen failing with the first home taken.
func TestDeleteRefusesAmbiguous(t *testing.T) {
	avdHome := t.TempDir()
	f := newAVD(t, nil, "ANDROID_AVD_HOME="+avdHome)
	f.avdIn(t, avdHome, "dy")
	other := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	ce := mustFail(t, f.del(t, capability.ClientUI, "dy"), ErrAVDAmbiguous)
	if !strings.Contains(ce.Cause, "ANDROID_AVD_HOME") || !strings.Contains(ce.Cause, "~/.android/avd") {
		t.Errorf("cause = %q, want both variables", ce.Cause)
	}
	if strings.Contains(ce.Cause, avdHome) || strings.Contains(ce.Cause, other) || strings.Contains(ce.NextStep, f.home) {
		t.Errorf("cause %q or next step %q names a path", ce.Cause, ce.NextStep)
	}
	if n := len(f.deletes(t)); n != 0 || !exists(filepath.Join(avdHome, "dy.ini")) || !exists(filepath.Join(other, "dy.ini")) {
		t.Errorf("a copy was deleted (%d runs)", n)
	}
	// The same directory spelled twice is one home.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(avdHome, link); err != nil {
		t.Fatal(err)
	}
	if homes := iniHomes("dy", []string{"ANDROID_AVD_HOME=" + avdHome, "ANDROID_SDK_HOME=" + filepath.Dir(link), "HOME=" + t.TempDir()}); len(homes) != 1 {
		t.Errorf("%d homes for one directory", len(homes))
	}
}

// TestDeleteRefusedOverMCP pins ADR-0129 §1: emulator.delete is a person's call. An agent's
// session is refused before anything runs and nothing is removed. Seen failing with the
// audience set to all.
func TestDeleteRefusedOverMCP(t *testing.T) {
	f := newAVD(t, nil)
	home := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
	_, err := f.ops.Start(context.Background(), capability.Request{Capability: "emulator.delete", Params: []byte(`{"avd":"dy"}`),
		Client: capability.ClientMCP, Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
	if codeOf(err) != capability.ErrNotAllowedHere {
		t.Fatalf("an agent's emulator.delete: %v, want %s", err, capability.ErrNotAllowedHere)
	}
	if !exists(filepath.Join(home, "dy.ini")) || len(f.deletes(t)) != 0 {
		t.Error("the AVD was deleted for an agent")
	}
	// emulator.create, by contrast, is an agent's tool.
	if d, _ := f.reg().Lookup("emulator.create"); d.Audience != capability.AudienceAll {
		t.Error("emulator.create is not for agents")
	}
}

func (f *avdFx) reg() *capability.Registry {
	r := capability.NewRegistry()
	Register(r, f.em)
	return r
}

// TestDeleteRefusesRunning pins ADR-0129 §7 and Q7: an AVD that runs is not deleted, however
// it is known to run: by this daemon's adb listing (the next step is emulator.stop of that
// serial), by an emulator process of the user that no adb listing shows, or by a candidate
// process holding a file of its directory open. Nothing is run and nothing removed.
func TestDeleteRefusesRunning(t *testing.T) {
	avdName, _ := os.ReadFile(filepath.Join("testdata", "emu-avd-name.txt"))
	t.Run("by adb", func(t *testing.T) {
		name := strings.TrimSpace(strings.SplitN(string(avdName), "\n", 2)[0])
		f := newAVD(t, map[string]string{"devices.out": listOf(emuLine), serial + ".avd": string(avdName)})
		f.avdIn(t, filepath.Join(f.home, ".android", "avd"), name)
		ce := mustFail(t, f.del(t, capability.ClientUI, name), ErrAVDRunning)
		if !strings.Contains(ce.NextStep, `emulator.stop {"device": "`+serial+`"}`) {
			t.Errorf("nextStep = %q, want emulator.stop of the serial", ce.NextStep)
		}
		if n := len(f.deletes(t)); n != 0 {
			t.Errorf("avdmanager ran delete %d times", n)
		}
	})
	t.Run("by an emulator process, no serial", func(t *testing.T) {
		f := newAVD(t, nil)
		home := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
		f.em.procs = realProcs
		helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "sleep", "-avd", "dy", "-no-window")
		ce := mustFail(t, f.del(t, capability.ClientUI, "dy"), ErrAVDRunning)
		if !strings.Contains(ce.NextStep, "where it was started") || strings.Contains(ce.NextStep, "emulator.stop") {
			t.Errorf("nextStep = %q, want to stop it where it was started", ce.NextStep)
		}
		if !exists(filepath.Join(home, "dy.ini")) || len(f.deletes(t)) != 0 {
			t.Error("a running AVD was deleted")
		}
	})
	t.Run("by an open file", func(t *testing.T) {
		f := newAVD(t, nil)
		f.em.procs = realProcs
		f.em.open = platform.DefaultLsof().OpenUnder
		home := f.avdIn(t, filepath.Join(f.home, ".android", "avd"), "dy")
		img := filepath.Join(home, "dy.avd", "userdata-qemu.img")
		if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(platform.DefaultLsof().Path); err != nil {
			t.Skipf("no lsof on this system: %v", err)
		}
		helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "open:"+img)
		mustFail(t, f.del(t, capability.ClientUI, "dy"), ErrAVDRunning)
		if len(f.deletes(t)) != 0 {
			t.Error("avdmanager ran for an AVD with a file held open")
		}
	})
}

// ---- running ------------------------------------------------------------------------

const runningHelperEnv = "DROIDYARD_EMULATOR_RUNNING_HELPER"

// TestRunningHelperProcess is not a test: the test binary re-executed as a long-lived child.
// "sleep" says it is ready and sleeps; "open:<path>" opens the file first.
func TestRunningHelperProcess(t *testing.T) {
	mode := os.Getenv(runningHelperEnv)
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

// helperAt copies the test binary to path, runs it in mode with argv after the test flags,
// waits until it is ready and kills it at cleanup.
func helperAt(t *testing.T, path, mode string, argv ...string) *exec.Cmd {
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
	cmd := exec.Command(path, append([]string{"-test.run=^TestRunningHelperProcess$", "--"}, argv...)...)
	cmd.Env = append(os.Environ(), runningHelperEnv+"="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper %s said %q (%v)", path, line, err)
	}
	return cmd
}

// realProcs is the host's processes with the ones the kernel will not show dropped: they
// are the subject of the doubt test, not of a test about one helper.
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

// TestRunningByProcess pins Q7's second sighting: an emulator process of the user whose
// executable is the launcher or a qemu-system-* and whose arguments name the AVD (-avd <name>
// or @<name>) is the AVD running, though no adb listing shows a serial (a second adb
// server, or an emulator whose console is closed). The helpers are the test binary copied to
// .../qemu-system-aarch64 and .../emulator and listed by the real kernel interface. Another
// AVD's process, or the AVD's name in an argument of another program, is not it. Seen
// failing with the adb check alone.
func TestRunningByProcess(t *testing.T) {
	f := newAVD(t, nil)
	f.em.procs = realProcs
	free := func(name string) {
		t.Helper()
		if st := f.em.running(context.Background(), name, nil); st.Why != "" {
			t.Errorf("%s: running (%s), want free", name, st.Why)
		}
	}
	free("dy")
	q := helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "sleep", "-avd", "dy", "-no-window")
	st := f.em.running(context.Background(), "dy", nil)
	if st.Why == "" || st.Doubt || st.Serial != "" || !strings.Contains(st.Why, "pid "+itoa(q.Process.Pid)) {
		t.Errorf("a qemu-system process started -avd dy: %+v, want running by its pid with no serial", st)
	}
	free("other")
	free("d")
	e := helperAt(t, filepath.Join(t.TempDir(), "emulator"), "sleep", "@dz")
	if st := f.em.running(context.Background(), "dz", nil); st.Why == "" || !strings.Contains(st.Why, "pid "+itoa(e.Process.Pid)) {
		t.Errorf("an emulator launched @dz: %+v, want running", st)
	}
	// A program that is neither, with the AVD's name among its arguments, is not the AVD.
	helperAt(t, filepath.Join(t.TempDir(), "editor"), "sleep", "-avd", "dx")
	free("dx")
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestRunningByOpenFile pins Q7's third sighting and ADR-0131 §2: a candidate process (here a
// qemu-system-aarch64 that no argument ties to the AVD) holding a file open inside the
// AVD's directory is the AVD running; a lock file nobody holds, as a crashed emulator
// leaves, is not (no file is matched by name). The real lsof finds the file.
func TestRunningByOpenFile(t *testing.T) {
	if _, err := os.Stat(platform.DefaultLsof().Path); err != nil {
		t.Skipf("no lsof on this system: %v", err)
	}
	f := newAVD(t, nil)
	f.em.procs = realProcs
	f.em.open = platform.DefaultLsof().OpenUnder
	avdDir := filepath.Join(t.TempDir(), "dy.avd")
	if err := os.MkdirAll(avdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"multiinstance.lock", "hardware-qemu.ini.lock"} {
		if err := os.WriteFile(filepath.Join(avdDir, n), []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	img := filepath.Join(avdDir, "userdata-qemu.img")
	if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale lock and nothing holding anything: free, though the candidate set is not empty.
	helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "sleep")
	if st := f.em.running(context.Background(), "dy", []string{avdDir}); st.Why != "" {
		t.Errorf("only lock files that nobody holds: running (%s), want free", st.Why)
	}
	h := helperAt(t, filepath.Join(t.TempDir(), "qemu-system-aarch64"), "open:"+img)
	st := f.em.running(context.Background(), "dy", []string{avdDir})
	if st.Why == "" || st.Doubt || !strings.Contains(st.Why, "pid "+itoa(h.Process.Pid)) {
		t.Errorf("a candidate holds %s open: %+v, want running by its pid", img, st)
	}
	// Another AVD's directory is not this one's.
	if st := f.em.running(context.Background(), "other", []string{filepath.Join(filepath.Dir(avdDir), "other.avd")}); st.Why != "" {
		t.Errorf("another directory: running (%s)", st.Why)
	}
}

// TestRunningDoubt pins "any doubt counts as running" (ADR-0129 §7): a process that could not
// be read, a listing that failed, and an lsof that failed are each the AVD running, marked
// as a doubt so the message does not claim to have seen it. A fake stands in only where the
// subject is the failure.
func TestRunningDoubt(t *testing.T) {
	f := newAVD(t, nil)
	check := func(what string) runState {
		t.Helper()
		st := f.em.running(context.Background(), "dy", []string{t.TempDir()})
		if st.Why == "" || !st.Doubt {
			t.Errorf("%s: %+v, want running as a doubt", what, st)
		}
		return st
	}
	f.em.procs = func() ([]platform.Proc, error) {
		return []platform.Proc{{Pid: 4242, Unreadable: "operation not permitted"}}, nil
	}
	if st := check("an unreadable process"); !strings.Contains(st.Why, "4242") {
		t.Errorf("why = %q, want the pid", st.Why)
	}
	f.em.procs = func() ([]platform.Proc, error) { return nil, io.ErrUnexpectedEOF }
	check("a listing that failed")
	f.em.procs = func() ([]platform.Proc, error) {
		return []platform.Proc{{Pid: 77, Exe: "/opt/sdk/emulator/qemu-system-aarch64", Args: []string{"qemu-system-aarch64", "-avd", "other"}}}, nil
	}
	f.em.open = func(context.Context, []int, []string) ([]platform.OpenFile, error) { return nil, io.ErrClosedPipe }
	check("an lsof that failed")
	// A sighting wins over a doubt: the message says what was seen.
	f.em.procs = func() ([]platform.Proc, error) {
		return []platform.Proc{{Pid: 1, Unreadable: "x"}, {Pid: 78, Exe: "/x/emulator", Args: []string{"emulator", "@dy"}}}, nil
	}
	if st := f.em.running(context.Background(), "dy", nil); st.Doubt || !strings.Contains(st.Why, "pid 78") {
		t.Errorf("a sighting and a doubt: %+v, want the sighting", st)
	}
	// Nothing at all: free.
	f.em.procs = func() ([]platform.Proc, error) { return nil, nil }
	f.em.open = func(context.Context, []int, []string) ([]platform.OpenFile, error) { return nil, nil }
	if st := f.em.running(context.Background(), "dy", nil); st.Why != "" {
		t.Errorf("nothing runs: %+v", st)
	}
}

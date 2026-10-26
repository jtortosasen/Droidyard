package sdk

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// The avdmanager runner (plan m3f step 6; ADR-0130 §5, ADR-0129 §6) is tested against the
// fake avdmanager (sdktest) started through a real supervisor, so the process kind, the
// session, the group and the pid record are the shipped ones.

type toolFixture struct {
	t     *testing.T
	root  string // the SDK
	dir   string // the fake's scenario directory
	jdk   string
	env   []string
	sup   *process.Supervisor
	st    *store.Store
	tool  *Tool
	paths ToolPaths
}

// supStarter adapts a supervisor to the runner's starter, as cmd/droidyard's does.
func supStarter(sup *process.Supervisor) RecordedStarter {
	return func(cmd *exec.Cmd, kind string) (Recorded, error) {
		r, err := sup.SpawnRecorded(cmd, process.Kind(kind))
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}

func jvmAt(home string) func() gradle.JVM {
	return func() gradle.JVM {
		return gradle.Discover(gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + home}, StudioHomes: func() []string { return nil }})
	}
}

// newTool is a Tool over an SDK holding the fake avdmanager as cmdline-tools/latest, the
// daemon's environment carrying a stale ANDROID_SDK_ROOT, a mark the fake logs, and a real
// supervisor. tweak adjusts the options.
func newTool(t *testing.T, tweak ...func(*ToolOptions)) *toolFixture {
	t.Helper()
	f := &toolFixture{t: t, root: t.TempDir(), dir: t.TempDir(), jdk: sdktest.JavaHome(t)}
	sdktest.InstallTools(t, f.root, "latest", "12.0")
	f.env = []string{"ANDROID_HOME=" + f.root, "ANDROID_SDK_ROOT=/not/the/sdk", sdktest.DirEnv + "=" + f.dir,
		"FAKESDK_MARK=daemon", "HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.st = st
	f.sup = process.New(process.Options{Store: st, Log: io.Discard})
	if err := f.sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.sup.Shutdown)
	o := ToolOptions{Env: f.env, JVM: jvmAt(f.jdk), Start: supStarter(f.sup)}
	for _, tw := range tweak {
		tw(&o)
	}
	f.tool = NewTool(o)
	f.paths, _ = f.tool.Resolve()
	return f
}

// records are the pid records on disk.
func (f *toolFixture) records() []process.Record {
	f.t.Helper()
	var recs process.Records
	if _, err := f.st.Read(process.RecordsFile, process.RecordsVersion, &recs); err != nil {
		f.t.Fatal(err)
	}
	return recs.Processes
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	var pid int
	testtime.Eventually(t, testtime.Bound(10*time.Second), path+" to name a pid", func() bool {
		b, err := os.ReadFile(path)
		n, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && perr == nil && n > 0
	})
	return pid
}

// gone reports whether no process has the pid.
func gone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

func (f *toolFixture) run(args ...string) (ToolOutput, error) {
	return f.tool.Run(context.Background(), ToolRun{Args: args})
}

// TestToolArgvEnv pins what avdmanager is started with (ADR-0129 §6, ADR-0130 §5): the
// arguments as given, the daemon's environment with ANDROID_SDK_ROOT removed, ANDROID_HOME
// the SDK root, JAVA_HOME the JVM, the call's ANDROID_AVD_HOME on top, and the consent
// prompt answered "no" by us and never by the caller.
func TestToolArgvEnv(t *testing.T) {
	f := newTool(t)
	avdHome := t.TempDir()
	_, err := f.tool.Run(context.Background(), ToolRun{
		Args:  []string{"create", "avd", "--name", "a b", "--package", "system-images;android-35;google_apis;arm64-v8a", "--device", "Nexus 5X"},
		Stdin: "no\n", Env: []string{"ANDROID_AVD_HOME=" + avdHome}})
	if err != nil {
		t.Fatal(err)
	}
	calls := sdktest.Calls(t, f.dir)
	if len(calls) != 1 {
		t.Fatalf("the fake was called %d times, want 1", len(calls))
	}
	c := calls[0]
	wantArgs := "create|avd|--name|a b|--package|system-images;android-35;google_apis;arm64-v8a|--device|Nexus 5X"
	if got := strings.Join(c.Args, "|"); got != wantArgs {
		t.Errorf("argv = %q, want %q (each argument one word, spaces kept)", got, wantArgs)
	}
	want := map[string]string{
		"ANDROID_HOME": f.root, "ANDROID_SDK_ROOT": "<unset>", "JAVA_HOME": f.jdk,
		"ANDROID_AVD_HOME": avdHome, "FAKESDK_MARK": "daemon",
	}
	for k, v := range want {
		if c.Env[k] != v {
			t.Errorf("%s = %q, want %q", k, c.Env[k], v)
		}
	}
	if c.Stdin != "no" {
		t.Errorf("stdin = %q, want the fixed \"no\"", c.Stdin)
	}
}

// TestToolStdinIsNotInherited pins ADR-0129 §2: avdmanager never reads the daemon's own
// stdin, which could carry an answer nobody gave it. os.Stdin is swapped for a pipe holding
// "y\n"; the fake must read EOF. Seen failing with cmd.Stdin = os.Stdin: it read "y".
func TestToolStdinIsNotInherited(t *testing.T) {
	f := newTool(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("y\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
	if _, err := f.run("list", "device"); err != nil {
		t.Fatal(err)
	}
	calls := sdktest.Calls(t, f.dir)
	if len(calls) != 1 {
		t.Fatalf("the fake was called %d times, want once", len(calls))
	}
	if calls[0].Stdin != "" {
		t.Fatalf("the fake read %q from stdin, want end of file", calls[0].Stdin)
	}
}

// TestToolPicksTheHighestRevision pins which avdmanager runs when cmdline-tools holds
// several (sdk.md §Reading what is installed): the highest revision, so `latest` at 12.0
// beside `latest-2` at 19.0 (what a self-update leaves) runs latest-2; on a tie `latest`;
// a directory whose revision cannot be read ranks below the others. Seen failing with the
// first match in directory order.
func TestToolPicksTheHighestRevision(t *testing.T) {
	root := t.TempDir()
	sdktest.InstallTools(t, root, "latest", "12.0")
	sdktest.InstallTools(t, root, "latest-2", "19.0")
	if p, ok := avdmanagerIn(root); !ok || p != filepath.Join(root, "cmdline-tools", "latest-2", "bin", "avdmanager") {
		t.Errorf("latest 12.0 and latest-2 19.0: picked %q, want latest-2", p)
	}
	tie := t.TempDir()
	sdktest.InstallTools(t, tie, "11.0", "12.0")
	sdktest.InstallTools(t, tie, "latest", "12.0")
	sdktest.InstallTools(t, tie, "13.0", "12.0")
	if p, _ := avdmanagerIn(tie); p != filepath.Join(tie, "cmdline-tools", "latest", "bin", "avdmanager") {
		t.Errorf("a tie: picked %q, want latest", p)
	}
	blind := t.TempDir()
	sdktest.InstallTools(t, blind, "old", "9.0")
	odd := sdktest.InstallTools(t, blind, "odd", "99.0")
	os.Remove(filepath.Join(filepath.Dir(filepath.Dir(odd)), "package.xml"))
	if p, _ := avdmanagerIn(blind); p != filepath.Join(blind, "cmdline-tools", "old", "bin", "avdmanager") {
		t.Errorf("one revision unreadable: picked %q, want the one that reads", p)
	}
	if _, ok := avdmanagerIn(t.TempDir()); ok {
		t.Error("an SDK without cmdline-tools has an avdmanager")
	}
}

// TestToolMissing pins the errors before any run: no SDK or no avdmanager is
// SDK_TOOLS_NOT_FOUND with the next step sdk.install cmdline-tools;latest (ADR-0130
// §Consequences); no JVM is SDK_JAVA_NOT_FOUND; nothing is started either way.
func TestToolMissing(t *testing.T) {
	bare := newTool(t)
	os.RemoveAll(filepath.Join(bare.root, "cmdline-tools"))
	os.MkdirAll(filepath.Join(bare.root, "platform-tools"), 0o755)
	_, err := bare.run("list", "device")
	ce := wantErr(t, err, ErrToolsNotFound)
	if !strings.Contains(ce.NextStep, `"cmdline-tools;latest"`) || !strings.Contains(ce.NextStep, "sdk.install") {
		t.Errorf("nextStep = %q, want sdk.install of cmdline-tools;latest", ce.NextStep)
	}

	nosdk := newTool(t)
	nosdk.tool = NewTool(ToolOptions{Env: []string{"HOME=" + t.TempDir()}, JVM: jvmAt(nosdk.jdk), Start: supStarter(nosdk.sup)})
	_, err = nosdk.run("list", "device")
	wantErr(t, err, ErrToolsNotFound)

	nojava := newTool(t, func(o *ToolOptions) {
		o.JVM = jvmAt(filepath.Join(t.TempDir(), "no-jdk"))
	})
	_, err = nojava.run("list", "device")
	ce = wantErr(t, err, ErrJavaNotFound)
	if !strings.Contains(ce.Cause, "JVM") {
		t.Errorf("cause = %q, want it to say what was tried", ce.Cause)
	}
	for _, f := range []*toolFixture{bare, nosdk, nojava} {
		if calls := sdktest.Calls(t, f.dir); len(calls) != 0 {
			t.Errorf("a run started although the tool or the JVM was missing: %+v", calls)
		}
	}
}

// TestToolRecordedWhileItRuns pins ADR-0129 §6: a run is a pid record of kind sdk-tool for
// as long as it lasts, its own session and group leader, and the record goes with it.
// Seen failing with the run started unrecorded (Spawn rather than SpawnRecorded).
func TestToolRecordedWhileItRuns(t *testing.T) {
	f := newTool(t)
	sdktest.Write(t, f.dir, "hang", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.tool.Run(ctx, ToolRun{Args: []string{"list", "device"}}); done <- err }()
	pid := readPid(t, filepath.Join(f.dir, "leader.pid"))
	recs := f.records()
	if len(recs) != 1 || recs[0].Kind != process.SDKTool || recs[0].PID != pid || recs[0].PGID != pid {
		t.Fatalf("records = %+v while the tool runs, want one sdk-tool record for pid %d leading its own group", recs, pid)
	}
	pgid, _ := syscall.Getpgid(pid)
	if sid, _ := unix.Getsid(pid); pgid != pid || sid != pid {
		t.Errorf("the tool's process group is %d and its session %d, want both its own pid %d", pgid, sid, pid)
	}
	if string(process.SDKTool) != ToolKind {
		t.Errorf("the runner spells the kind %q, the supervisor %q", ToolKind, process.SDKTool)
	}
	cancel()
	<-done
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("records = %+v after the run, want none", recs)
	}
}

// TestToolCancelEndsTheGroup pins ADR-0129 §6: at the call's cancel the tool's whole group
// ends, the JVM it started included, within the grace, and the record is removed. The fake
// hangs and has a child in its group. Seen failing with the leader killed alone (the child
// lived) and without Setsid (the group was the daemon's, and the signal missed it).
func TestToolCancelEndsTheGroup(t *testing.T) {
	f := newTool(t)
	sdktest.Write(t, f.dir, "hang", "")
	sdktest.Write(t, f.dir, "child", "")
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		out ToolOutput
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := f.tool.Run(ctx, ToolRun{Args: []string{"list", "device"}})
		done <- result{out, err}
	}()
	leader := readPid(t, filepath.Join(f.dir, "leader.pid"))
	child := readPid(t, filepath.Join(f.dir, "child.pid"))
	if gone(leader) || gone(child) {
		t.Fatalf("leader %d or child %d is not running before the cancel", leader, child)
	}
	start := time.Now()
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("Run = %v, want the context's cancel", r.err)
		}
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("Run did not return after the cancel")
	}
	testtime.Eventually(t, testtime.Bound(6*time.Second), "the leader and its child gone", func() bool { return gone(leader) && gone(child) })
	if d := time.Since(start); d > testtime.Bound(6*time.Second) {
		t.Errorf("the group took %s to end", d)
	}
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("records = %+v after the cancel, want none", recs)
	}
}

// TestToolEscalatesToKill pins the second half of the end: a tool that ignores SIGTERM (a
// hung JVM) has its group killed once the grace has passed.
func TestToolEscalatesToKill(t *testing.T) {
	f := newTool(t, func(o *ToolOptions) { o.TermGrace = 300 * time.Millisecond })
	sdktest.Write(t, f.dir, "hang", "")
	sdktest.Write(t, f.dir, "ignoreterm", "")
	sdktest.Write(t, f.dir, "child", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.tool.Run(ctx, ToolRun{Args: []string{"list", "device"}}); done <- err }()
	leader := readPid(t, filepath.Join(f.dir, "leader.pid"))
	child := readPid(t, filepath.Join(f.dir, "child.pid"))
	cancel()
	<-done
	testtime.Eventually(t, testtime.Bound(6*time.Second), "the group killed after the grace", func() bool { return gone(leader) && gone(child) })
}

// TestToolBounds pins the bounds: the call's time (a hung tool is SDK_TOOL_FAILED naming the
// bound, and its group is ended) and the output kept (the last MiB, so a tool that prints
// without end cannot grow the daemon).
func TestToolBounds(t *testing.T) {
	f := newTool(t)
	// The first run of a script just written can take seconds on a loaded macOS host (the
	// system scans a new executable before it runs it), which would use up the bound before
	// the fake started; one quick call first, under the long bound, takes that cost.
	if _, err := f.run("list", "device"); err != nil {
		t.Fatal(err)
	}
	bound := testtime.Bound(2 * time.Second)
	f.tool.timeout = bound
	sdktest.Write(t, f.dir, "hang", "")
	sdktest.Write(t, f.dir, "child", "")
	start := time.Now()
	_, err := f.run("list", "device")
	ce := wantErr(t, err, ErrToolFailed)
	if !strings.Contains(ce.Cause, "did not finish within "+bound.String()) {
		t.Errorf("cause = %q, want the bound named", ce.Cause)
	}
	if time.Since(start) > testtime.Bound(8*time.Second) {
		t.Errorf("the bound took %s", time.Since(start))
	}
	leader := readPid(t, filepath.Join(f.dir, "leader.pid"))
	child := readPid(t, filepath.Join(f.dir, "child.pid"))
	testtime.Eventually(t, testtime.Bound(6*time.Second), "the group ended at the bound", func() bool { return gone(leader) && gone(child) })

	big := newTool(t)
	filler := strings.Repeat("x", 1023) + "\n"
	sdktest.Write(t, big.dir, "devices.out", strings.Repeat(filler, 3*1024)+"THE LAST LINE\n")
	out, err := big.run("list", "device")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) != toolOutputTail || !strings.HasSuffix(string(out.Output), "THE LAST LINE\n") {
		t.Errorf("kept %d bytes ending %q, want the last %d", len(out.Output), tailOf(out.Output, 20), toolOutputTail)
	}
}

func tailOf(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// TestToolFailed pins the failure: a status other than 0 is SDK_TOOL_FAILED, the cause
// carries the status and the decisive line of the output (avdmanager's own "Error:" line,
// not the list that follows it), the output is not the cause's whole.
func TestToolFailed(t *testing.T) {
	f := newTool(t)
	sdktest.Write(t, f.dir, "create.fail", "Warning: a notice\nError: Package path is not valid. Valid system image paths are:\nnull\n")
	_, err := f.run("create", "avd", "--name", "x")
	ce := wantErr(t, err, ErrToolFailed)
	for _, want := range []string{"avdmanager create avd", "status 1", "Error: Package path is not valid."} {
		if !strings.Contains(ce.Cause, want) {
			t.Errorf("cause = %q, want it to contain %q", ce.Cause, want)
		}
	}
	if strings.Contains(ce.Cause, "null") || strings.Contains(ce.Cause, "Warning") {
		t.Errorf("cause = %q carries lines that are not the decisive one", ce.Cause)
	}
	if got := decisiveLine([]byte("a\nException in thread \"main\" java.lang.Oops: no\nlast line\n")); !strings.HasPrefix(got, "Exception in thread") {
		t.Errorf("without an Error line the exception is decisive; got %q", got)
	}
	if got := decisiveLine([]byte("one\n\ntwo\n\n")); got != "two" {
		t.Errorf("with neither, the last line; got %q", got)
	}
	nostart := newTool(t, func(o *ToolOptions) { o.Start = nil })
	_, err = nostart.run("list", "device")
	wantErr(t, err, ErrToolFailed)
}

// TestToolRecordFailureEndsTheGroup: when the pid record cannot be written the supervisor
// kills the run's whole group and the runner reports it as a failure to start.
func TestToolRecordFailureStartsNothing(t *testing.T) {
	f := newTool(t, func(o *ToolOptions) {
		o.Start = func(*exec.Cmd, string) (Recorded, error) { return nil, errors.New("processes.json cannot be written") }
	})
	_, err := f.run("list", "device")
	ce := wantErr(t, err, ErrToolFailed)
	if !strings.Contains(ce.Cause, "could not be started") {
		t.Errorf("cause = %q", ce.Cause)
	}
}

// TestTailKeepsTheEnd: the tail writer keeps the last bytes, accepts everything and is the
// same at any write size.
func TestTailKeepsTheEnd(t *testing.T) {
	w := &tail{max: 10}
	for _, chunk := range []string{"0123456789", "ab", "cdefghijklmnop", "XY"} {
		if n, err := w.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := string(w.bytes()); got != "ijklmnopXY" {
		t.Errorf("tail = %q, want the last 10 bytes", got)
	}
}

package sidecar

// The opt-in real-Gradle tests of step 1 (plan m1g §1 Check, D14): they spawn the
// embedded jars on a real JVM, run Gradle 8.13 on the sidecar/testdata/plain fixture, and
// assert the protocol of docs/subsystems/gradle.md §Protocol end to end. They are gated
// by DROIDYARD_GRADLE_TESTS=1 so `go test ./...` stays fast, and they fail rather than
// skip when it is set and a prerequisite is missing (S5: a skipped opt-in test proves
// nothing). The fixture is copied to a temporary directory for each test: Gradle writes
// `.gradle/` and `build/` into the project, and the checked-in fixture must not change.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func gradleTest(t *testing.T) {
	t.Helper()
	if os.Getenv("DROIDYARD_GRADLE_TESTS") != "1" {
		t.Skip("level 5 only: set DROIDYARD_GRADLE_TESTS=1 (spawns a JVM and Gradle)")
	}
	if findJDK() == "" {
		t.Fatal("DROIDYARD_GRADLE_TESTS=1 but no JDK 17 found (set JAVA_HOME)")
	}
}

// --- the process harness ---------------------------------------------------------------

type proc struct {
	t   *testing.T
	cmd *exec.Cmd
	in  io.WriteCloser

	mu       sync.Mutex
	msgs     []map[string]any
	badLines []string
	lineInfo []lineInfo
	errText  strings.Builder

	nextID     int
	exitCh     chan struct{}
	waitErr    error
	stdoutDone chan struct{}
	stderrDone chan struct{}
}

// lineInfo records one stdout line's byte length and whether it is a reply (mail with an
// id) rather than a notification, so stop can enforce the notification cap (D6).
type lineInfo struct {
	bytes   int
	isReply bool
}

// procOpts are the test-only launch properties startProcOpts adds (plan m1h step 2).
type procOpts struct {
	// testStdout adds -Ddroidyard.sidecar.testStdout (the 1g property).
	testStdout bool
	// probeArgs becomes -Ddroidyard.sidecar.testProbeArgs: extra Tooling API arguments for
	// the probed fetch, which is how the probe's droidyard.probe.fail hook is reached.
	probeArgs string
	// probeInit becomes -Ddroidyard.sidecar.testProbeInit: another init script than the
	// embedded one, so a broken one can be exercised.
	probeInit string
	// env is extra environment for the sidecar process (m3e step 1: GRADLE_USER_HOME, so a
	// test installs the distribution into a user home of its own).
	env []string
}

// startProc launches the embedded jars on the embedded classpath, in a process group of
// its own as the supervisor spawns the sidecar. With testStdout it passes
// -Ddroidyard.sidecar.testStdout, which makes the sidecar write to System.out at start
// and while serving a request.
func startProc(t *testing.T, testStdout bool) *proc {
	return startProcOpts(t, procOpts{testStdout: testStdout})
}

func startProcOpts(t *testing.T, o procOpts) *proc {
	t.Helper()
	java := filepath.Join(findJDK(), "bin", "java")
	var args []string
	if o.testStdout {
		args = append(args, "-Ddroidyard.sidecar.testStdout")
	}
	// The probe's init script path comes from the launch line, as the daemon passes it
	// (D7); the test override replaces it, so a broken script can be exercised.
	init := o.probeInit
	if init == "" {
		init = probeInit(t)
	}
	args = append(args, "-Ddroidyard.probe.init="+init)
	if o.probeArgs != "" {
		args = append(args, "-Ddroidyard.sidecar.testProbeArgs="+o.probeArgs)
	}
	args = append(args, "-cp", writeJars(t), Class)
	cmd := exec.Command(java, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if len(o.env) > 0 {
		cmd.Env = append(os.Environ(), o.env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the sidecar: %v", err)
	}
	p := &proc{
		t:          t,
		cmd:        cmd,
		in:         in,
		exitCh:     make(chan struct{}),
		stdoutDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
	go func() { p.waitErr = cmd.Wait(); close(p.exitCh) }()
	go p.readStdout(stdout)
	go p.readStderr(stderr)
	t.Cleanup(p.stop)
	return p
}

// writeJars writes the embedded jars out and returns their classpath, the probe jar
// included as the daemon's launch line carries it (D7). The test runs the bytes the daemon
// will run, not a build tree.
func writeJars(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	deps := filepath.Join(dir, "gradle-sidecar-deps.jar")
	classes := filepath.Join(dir, "gradle-sidecar.jar")
	probe := filepath.Join(dir, "droidyard-probe.jar")
	for path, data := range map[string][]byte{
		deps:    DepsJar(),
		classes: Jar(),
		probe:   ProbeJar(),
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return deps + string(os.PathListSeparator) + classes + string(os.PathListSeparator) + probe
}

// probeInit writes the embedded probe jar and init script beside each other, as the
// daemon's cache directory has them (D4: the init script finds the jar beside itself), and
// returns the script's path.
func probeInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "droidyard-probe.jar"), ProbeJar(), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "droidyard-probe.init.gradle")
	if err := os.WriteFile(script, InitScript(), 0o644); err != nil {
		t.Fatal(err)
	}
	return script
}

func (p *proc) readStdout(r io.ReadCloser) {
	defer close(p.stdoutDone)
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\n"); strings.TrimSpace(trimmed) != "" {
			var m map[string]any
			p.mu.Lock()
			if e := json.Unmarshal([]byte(trimmed), &m); e != nil {
				p.badLines = append(p.badLines, trimmed)
			} else {
				p.msgs = append(p.msgs, m)
				p.lineInfo = append(p.lineInfo, lineInfo{bytes: len(trimmed), isReply: m["id"] != nil})
			}
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *proc) readStderr(r io.ReadCloser) {
	defer close(p.stderrDone)
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			p.mu.Lock()
			p.errText.WriteString(line)
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// stop is the cleanup: kill the process group if it is still up, drain the readers, and
// hold the promise that stdout carries only the protocol (D6).
func (p *proc) stop() {
	select {
	case <-p.exitCh:
	default:
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		select {
		case <-p.exitCh:
		case <-time.After(5 * time.Second):
		}
	}
	for _, done := range []chan struct{}{p.stdoutDone, p.stderrDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	p.mu.Lock()
	bad := append([]string(nil), p.badLines...)
	infos := append([]lineInfo(nil), p.lineInfo...)
	p.mu.Unlock()
	if len(bad) > 0 {
		p.t.Errorf("the sidecar wrote %d non-JSON line(s) to stdout: %q", len(bad), bad)
	}
	// D6 as changed in the step 1 fix round: the sidecar bounds every notification to
	// 1 MiB by construction. Replies (project.model) may be larger and are capped at
	// 16 MiB by the client instead.
	for _, li := range infos {
		if !li.isReply && li.bytes > 1<<20 {
			p.t.Errorf("a notification line was %d bytes, over the 1 MiB notification cap", li.bytes)
		}
	}
}

func (p *proc) writeLine(line string) {
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		p.t.Fatalf("writing to the sidecar: %v", err)
	}
}

func (p *proc) send(method string, params any) int {
	p.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": p.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		p.t.Fatal(err)
	}
	p.writeLine(string(b))
	return p.nextID
}

// waitFor polls the parsed messages for the first that matches.
func (p *proc) waitFor(timeout time.Duration, match func(map[string]any) bool) map[string]any {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		for _, m := range p.msgs {
			if match(m) {
				p.mu.Unlock()
				return m
			}
		}
		p.mu.Unlock()
		if time.Now().After(deadline) {
			p.t.Fatalf("no matching protocol message within %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *proc) waitResponse(id int, timeout time.Duration) map[string]any {
	return p.waitFor(timeout, func(m map[string]any) bool { return idOf(m) == id })
}

// result waits for id's response and returns its result object, failing on an error
// response.
func (p *proc) result(id int, timeout time.Duration) map[string]any {
	p.t.Helper()
	resp := p.waitResponse(id, timeout)
	if e := objOf(resp, "error"); e != nil {
		p.t.Fatalf("request %d failed: %v", id, e)
	}
	r := objOf(resp, "result")
	if r == nil {
		p.t.Fatalf("request %d has no result: %v", id, resp)
	}
	return r
}

func (p *proc) notifications(method string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, m := range p.msgs {
		if m["method"] == method {
			out = append(out, m)
		}
	}
	return out
}

func (p *proc) outputLines(buildID string) []string {
	var out []string
	for _, n := range p.notifications("event.build.output") {
		params := objOf(n, "params")
		if strOf(params, "buildId") != buildID {
			continue
		}
		for _, l := range arrOf(params, "lines") {
			if s, ok := l.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func (p *proc) stderrText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.errText.String()
}

func (p *proc) closeStdin() { _ = p.in.Close() }

// waitExit reports whether the sidecar ended within timeout; it returns how long it took.
func (p *proc) waitExit(timeout time.Duration) (time.Duration, bool) {
	start := time.Now()
	select {
	case <-p.exitCh:
		return time.Since(start), true
	case <-time.After(timeout):
		return time.Since(start), false
	}
}

// --- fixture --------------------------------------------------------------------------

// plainFixture copies sidecar/testdata/plain into a temporary directory. Gradle writes
// its caches and outputs into the project, so the checked-in fixture is never touched.
func plainFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("testdata/plain")); err != nil {
		t.Fatalf("copying testdata/plain: %v", err)
	}
	return dst
}

// injectJavaError adds a source file that javac rejects, so :lib:compileJava fails with
// a real compiler error line on the console.
func injectJavaError(t *testing.T, root string) {
	t.Helper()
	src := "package lib;\n\npublic class Broken {\n    static int broken() {\n        return \"not an int\";\n    }\n}\n"
	path := filepath.Join(root, "lib", "src", "main", "java", "lib", "Broken.java")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// open connects the fixture root and returns the connection result.
func openRoot(t *testing.T, p *proc, root string) map[string]any {
	t.Helper()
	return p.result(p.send("connection.open", map[string]any{"root": root}), 180*time.Second)
}

// runBuild starts a build and waits for its terminal notification.
func runBuild(t *testing.T, p *proc, root, buildID string, tasks ...string) map[string]any {
	t.Helper()
	id := p.send("build.run", map[string]any{"buildId": buildID, "root": root, "tasks": tasks})
	if boolOf(p.result(id, 30*time.Second), "accepted") != true {
		t.Fatalf("build.run %s was not accepted", buildID)
	}
	return p.waitFor(240*time.Second, func(m map[string]any) bool {
		return m["method"] == "event.build.finished" && strOf(objOf(m, "params"), "buildId") == buildID
	})
}

// --- the tests -------------------------------------------------------------------------

// Decision (gradle.md §Protocol, D19; plan m1h D7): the handshake carries protocol 2 and
// the pinned Tooling API; connection.open reports Gradle 8.13 from the wrapper and the
// daemon's JVM home; project.model lists the root and its one subproject with compileJava
// and, unprobed, answers probe.status skipped. ping and shutdown answer too.
func TestGradleHandshakeAndProjectModel(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)

	hello := p.result(p.send("hello", nil), 60*time.Second)
	if got := intOf(hello, "protocolVersion"); got != 2 {
		t.Errorf("hello protocolVersion %d, want 2", got)
	}
	if got := strOf(hello, "tapiVersion"); got != ToolingAPI {
		t.Errorf("hello tapiVersion %q, want %q", got, ToolingAPI)
	}
	if got := strOf(hello, "sidecarVersion"); got == "" {
		t.Error("hello sidecarVersion is empty")
	}
	if jvm := objOf(hello, "java"); jvm == nil || !strings.HasPrefix(strOf(jvm, "version"), "17.") {
		t.Errorf("hello java %v, want the jars running on a JVM 17", jvm)
	}

	open := openRoot(t, p, root)
	if got := strOf(open, "gradleVersion"); got != "8.13" {
		t.Errorf("connection.open gradleVersion %q, want 8.13", got)
	}
	if got := strOf(open, "source"); got != "wrapper" {
		t.Errorf("connection.open source %q, want wrapper", got)
	}
	if h := strOf(open, "daemonJavaHome"); !isExecutable(filepath.Join(h, "bin", "java")) {
		t.Errorf("connection.open daemonJavaHome %q is not a JVM home", h)
	}
	if caps := objOf(open, "capabilities"); caps == nil || caps["problemEvents"] != true {
		t.Errorf("connection.open capabilities %v, want problemEvents true for 8.13", caps)
	}

	model := p.result(p.send("project.model", map[string]any{"root": root}), 240*time.Second)
	projects := arrOf(model, "projects")
	if len(projects) != 2 {
		t.Fatalf("project.model lists %d projects, want the root and :lib", len(projects))
	}
	if rootProj := projectAt(projects, ":"); rootProj == nil {
		t.Errorf("project.model has no root project: %v", projectPaths(projects))
	}
	lib := projectAt(projects, ":lib")
	if lib == nil {
		t.Fatalf("project.model has no :lib: %v", projectPaths(projects))
	}
	if !hasTask(lib, "compileJava") {
		t.Errorf(":lib lists no compileJava task")
	}

	if r := p.result(p.send("ping", nil), 30*time.Second); len(r) != 0 {
		t.Errorf("ping result %v, want an empty object", r)
	}
	if r := p.result(p.send("connection.close", map[string]any{"root": root}), 30*time.Second); len(r) != 0 {
		t.Errorf("connection.close result %v, want an empty object", r)
	}

	p.result(p.send("shutdown", nil), 30*time.Second)
	if took, ok := p.waitExit(5 * time.Second); !ok {
		t.Errorf("shutdown did not end the sidecar within 5s (it ran %s)", took)
	}
}

// Decision (gradle.md §Output: control vs. volume; D6): a build's console output arrives
// as event.build.output batches; a task's println is one line; a 2 MiB line is cut at
// 64 KiB with a marker counting what was dropped; and no console line carries an ANSI
// escape (colour is off).
func TestGradleConsoleOutputBatchesAndCutsLongLines(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)

	fin := runBuild(t, p, root, "b-console", ":echoLine", ":bigLine")
	if got := strOf(objOf(fin, "params"), "outcome"); got != "success" {
		t.Errorf("build outcome %q, want success", got)
	}

	lines := p.outputLines("b-console")
	var sawEcho, sawCut, sawMarker, sawEscape bool
	longest := 0
	for _, l := range lines {
		if l == "hello from the task" {
			sawEcho = true
		}
		if len(l) == 65536 {
			sawCut = true
		}
		if strings.Contains(l, "console line truncated at 65536 bytes") && strings.Contains(l, "bytes dropped") {
			sawMarker = true
		}
		if strings.Contains(l, "\x1b[") {
			sawEscape = true
		}
		if len(l) > longest {
			longest = len(l)
		}
	}
	if !sawEcho {
		t.Errorf("no `hello from the task` line among %d console lines", len(lines))
	}
	if !sawCut {
		t.Errorf("no 64 KiB line among %d console lines (longest %d)", len(lines), longest)
	}
	if !sawMarker {
		t.Errorf("no truncation marker among %d console lines", len(lines))
	}
	if sawEscape {
		t.Error("a console line carries an ANSI escape: setColorOutput(false) is not in effect")
	}
	for _, n := range p.notifications("event.build.output") {
		if got := len(arrOf(objOf(n, "params"), "lines")); got > 200 {
			t.Errorf("an event.build.output batch carried %d lines, over the 200-line cap", got)
		}
	}
	// Decision (N5): `taskSummary.executed` counts a plain task success; without the
	// summaryKey mapping the count never moves.
	executed := 0
	for _, n := range p.notifications("event.build.progress") {
		if c := objOf(objOf(n, "params"), "counts"); c != nil {
			if v := intOf(c, "executed"); v > executed {
				executed = v
			}
		}
	}
	if executed == 0 {
		t.Error("counts.executed stayed 0 after a successful build (N5: summaryKey(\"success\"))")
	}
}

// Decision (gradle.md §Protocol; D6): a failed build streams its compiler error as
// console output, reports the task as failed and ends `failed` with a non-empty failure
// tree.
func TestGradleBuildFailureStreamsTheConsole(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	injectJavaError(t, root)
	openRoot(t, p, root)

	fin := runBuild(t, p, root, "b-javac", ":lib:compileJava")
	params := objOf(fin, "params")
	if got := strOf(params, "outcome"); got != "failed" {
		t.Errorf("build outcome %q, want failed", got)
	}
	if failures := arrOf(params, "failures"); len(failures) == 0 {
		t.Error("failed build has an empty failure tree")
	} else if msg := strOf(failures[0].(map[string]any), "message"); msg == "" {
		t.Errorf("failure tree has no root message: %v", failures[0])
	}

	failed := false
	for _, n := range p.notifications("event.build.taskFinished") {
		tp := objOf(n, "params")
		if strOf(tp, "path") == ":lib:compileJava" && strOf(tp, "outcome") == "failed" {
			failed = true
		}
	}
	if !failed {
		t.Error("no failed :lib:compileJava in event.build.taskFinished")
	}

	var javac bool
	for _, l := range p.outputLines("b-javac") {
		if strings.Contains(l, "Broken.java") && strings.Contains(l, "error:") {
			javac = true
		}
	}
	if !javac {
		t.Errorf("no javac error line among %d console lines: %q", len(p.outputLines("b-javac")), p.outputLines("b-javac"))
	}
}

// Decision (gradle.md §Lifecycle; D6): build.cancel cancels the running build and the
// terminal notification is `cancelled` within five seconds.
func TestGradleBuildCancelledWithinFiveSeconds(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)
	// Warm the daemon on this project so the five seconds measure the cancellation, not
	// a cold Gradle start.
	if fin := runBuild(t, p, root, "b-warm", ":echoLine"); strOf(objOf(fin, "params"), "outcome") != "success" {
		t.Fatalf("warm-up build did not succeed: %v", fin)
	}

	id := p.send("build.run", map[string]any{"buildId": "b-slow", "root": root, "tasks": []string{":slow"}})
	if boolOf(p.result(id, 30*time.Second), "accepted") != true {
		t.Fatal("build.run :slow was not accepted")
	}
	time.Sleep(2 * time.Second) // let the task start executing
	start := time.Now()
	if r := p.result(p.send("build.cancel", map[string]any{"buildId": "b-slow"}), 30*time.Second); len(r) != 0 {
		t.Errorf("build.cancel result %v, want an empty object", r)
	}
	fin := p.waitFor(5*time.Second, func(m map[string]any) bool {
		return m["method"] == "event.build.finished" && strOf(objOf(m, "params"), "buildId") == "b-slow"
	})
	if got := strOf(objOf(fin, "params"), "outcome"); got != "cancelled" {
		t.Errorf("cancelled build outcome %q, want cancelled", got)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the build took %s to report cancelled after build.cancel, over five seconds", took)
	}
}

// Decision (D6 as changed in the step 1 fix round): the 64 KiB cut of a console line
// lands on a UTF-8 boundary, so a multi-byte character is never split into a U+FFFD.
func TestGradleConsoleCutKeepsUtf8Boundary(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)
	fin := runBuild(t, p, root, "b-wide", ":wideLine")
	if got := strOf(objOf(fin, "params"), "outcome"); got != "success" {
		t.Fatalf("wideLine outcome %q, want success", got)
	}
	var cut string
	for _, l := range p.outputLines("b-wide") {
		if strings.HasPrefix(l, "日") {
			cut = l
		}
	}
	if cut == "" {
		t.Fatalf("no wide-character console line among %v", p.outputLines("b-wide"))
	}
	if !utf8.ValidString(cut) {
		t.Error("the cut console line is not valid UTF-8")
	}
	if strings.ContainsRune(cut, utf8.RuneError) {
		t.Error("the 64 KiB cut split a UTF-8 sequence (a U+FFFD appeared)")
	}
	if len(cut)%3 != 0 {
		t.Errorf("the cut console line is %d bytes, not a whole number of three-byte characters", len(cut))
	}
}

// Decision (gradle.md §Daemon JVM; D6 as fixed in the step 1 fix round): connection.open's
// javaHome selects each operation's JVM (`setJavaHome`), never the Gradle installation, so
// the daemon runs on the named JVM and a build still succeeds.
func TestGradleJavaHomeSelectsTheDaemonJvm(t *testing.T) {
	gradleTest(t)
	jdk := findJDK()
	p := startProc(t, false)
	root := plainFixture(t)
	open := p.result(p.send("connection.open", map[string]any{"root": root, "javaHome": jdk}), 240*time.Second)
	if got := strOf(open, "daemonJavaHome"); !samePath(got, jdk) {
		t.Errorf("connection.open daemonJavaHome %q, want the requested javaHome %q", got, jdk)
	}
	if got := strOf(open, "gradleVersion"); got != "8.13" {
		t.Errorf("connection.open gradleVersion %q, want 8.13 (javaHome must not select the installation)", got)
	}
	fin := runBuild(t, p, root, "b-javahome", ":lib:compileJava")
	if got := strOf(objOf(fin, "params"), "outcome"); got != "success" {
		t.Errorf("build with javaHome %q had outcome %q, want success", jdk, got)
	}
}

// Decision (D6 as changed, N3): build.cancel is answered on the reader thread, so a cancel
// reaches a running build even while a project.model sits in the dispatcher behind it.
func TestGradleCancelAnsweredDuringAModelFetch(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)
	if fin := runBuild(t, p, root, "b-warm", ":echoLine"); strOf(objOf(fin, "params"), "outcome") != "success" {
		t.Fatalf("warm-up build did not succeed: %v", fin)
	}
	// A slow build holds the root's lock.
	id := p.send("build.run", map[string]any{"buildId": "b-busy", "root": root, "tasks": []string{":slow"}})
	if boolOf(p.result(id, 30*time.Second), "accepted") != true {
		t.Fatal("build.run :slow was not accepted")
	}
	time.Sleep(2 * time.Second)
	// project.model queues on the dispatcher and blocks on the root's lock...
	modelID := p.send("project.model", map[string]any{"root": root})
	time.Sleep(500 * time.Millisecond)
	// ...but build.cancel must still be answered, promptly.
	start := time.Now()
	cancelID := p.send("build.cancel", map[string]any{"buildId": "b-busy"})
	p.result(cancelID, 3*time.Second)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("build.cancel took %s while a project.model was in flight", took)
	}
	// The model fetch then answers once the lock is free.
	p.result(modelID, 60*time.Second)
	fin := p.waitFor(10*time.Second, func(m map[string]any) bool {
		return m["method"] == "event.build.finished" && strOf(objOf(m, "params"), "buildId") == "b-busy"
	})
	if got := strOf(objOf(fin, "params"), "outcome"); got != "cancelled" {
		t.Errorf("cancelled build outcome %q, want cancelled", got)
	}
}

// Decision (D6 as changed in the step 1 fix round): a failure tree wider than the 200-node
// cap is truncated, a parent carries truncatedCauses, and the finished notification stays
// a bounded line (the harness asserts every notification is under 1 MiB).
func TestGradleFailureTreeIsBounded(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)
	fin := runBuild(t, p, root, "b-deep", ":deepFailure")
	params := objOf(fin, "params")
	if got := strOf(params, "outcome"); got != "failed" {
		t.Fatalf("deepFailure outcome %q, want failed", got)
	}
	nodes, truncated := countTree(arrOf(params, "failures"))
	t.Logf("deepFailure tree: %d nodes, %d truncated parents", nodes, truncated)
	if nodes == 0 {
		t.Fatal("deepFailure produced no failure tree")
	}
	if nodes > maxFailureNodes {
		t.Errorf("failure tree has %d nodes, over the %d-node cap", nodes, maxFailureNodes)
	}
	if truncated == 0 {
		t.Errorf("failure tree has %d nodes but no truncatedCauses: the chain did not reach a limit", nodes)
	}
}

// Decision (process-lifecycle.md layer 1; D6): stdin EOF ends the sidecar within five
// seconds whatever it is doing — a running build, and a model fetch in flight.
func TestGradleExitsOnStdinEof(t *testing.T) {
	gradleTest(t)

	t.Run("during a build", func(t *testing.T) {
		p := startProc(t, false)
		root := plainFixture(t)
		openRoot(t, p, root)
		if fin := runBuild(t, p, root, "b-warm", ":echoLine"); strOf(objOf(fin, "params"), "outcome") != "success" {
			t.Fatalf("warm-up build did not succeed: %v", fin)
		}
		if boolOf(p.result(p.send("build.run", map[string]any{
			"buildId": "b-eof", "root": root, "tasks": []string{":slow"},
		}), 30*time.Second), "accepted") != true {
			t.Fatal("build.run :slow was not accepted")
		}
		time.Sleep(2 * time.Second) // the build is running
		p.closeStdin()
		if took, ok := p.waitExit(5 * time.Second); !ok {
			t.Fatalf("the sidecar did not exit within 5s of stdin EOF during a build (ran %s)", took)
		}
	})

	t.Run("during a model fetch", func(t *testing.T) {
		p := startProc(t, false)
		root := plainFixture(t)
		openRoot(t, p, root)
		// Send the model fetch and close stdin without waiting for it, so it is in
		// flight: a fresh project still has to be configured.
		p.send("project.model", map[string]any{"root": root})
		p.closeStdin()
		if took, ok := p.waitExit(5 * time.Second); !ok {
			t.Fatalf("the sidecar did not exit within 5s of stdin EOF during a model fetch (ran %s)", took)
		}
	})
}

// Decision (D6): System.out is pointed at stderr at start, so when the sidecar itself
// writes to System.out (the -Ddroidyard.sidecar.testStdout property) every stdout line
// stays valid JSON. The junk showing up on stderr is what proves System.out was writing.
func TestGradleStdoutCarriesOnlyTheProtocol(t *testing.T) {
	gradleTest(t)
	p := startProc(t, true)
	root := plainFixture(t)
	p.result(p.send("hello", nil), 60*time.Second)
	openRoot(t, p, root)
	p.result(p.send("ping", nil), 30*time.Second)

	if got := p.stderrText(); !strings.Contains(got, "sidecar: started") {
		t.Errorf("stderr has no test-marker line, so System.out was not redirected to stderr:\n%s", got)
	}
	// The cleanup asserts stdout held only JSON; the property is what would break it.
	if bad := p.badStdout(); len(bad) > 0 {
		t.Errorf("stdout carried non-JSON lines while the sidecar wrote to System.out: %q", bad)
	}
}

// Decision (D7): the hand-written codec round-trips the awkward strings — quotes,
// backslashes, control characters, non-ASCII, a valid surrogate pair, a lone high
// surrogate (U+FFFD), a high surrogate followed by a non-low escape (the rewind path),
// and a lone low surrogate — through the Go client, and an unknown method answers -32601
// with the method echoed. The request lines are built by hand: encoding/json cannot
// produce a lone surrogate.
func TestGradleCodecRoundTripAndUnknownMethod(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	cases := []struct {
		name string
		id   int
		line string
		want string
	}{
		{
			"escapes, non-ASCII and a lone high surrogate",
			7,
			`{"jsonrpc":"2.0","id":7,"method":"A\"B\\C\tD\u0001E\u001fFé日本語😀\ud800G"}`,
			"A\"B\\C\tD\x01E\x1fFé日本語😀�G",
		},
		{
			"an escaped surrogate pair",
			8,
			`{"jsonrpc":"2.0","id":8,"method":"pa\ud83d\ude00ir"}`,
			"pa😀ir",
		},
		{
			"a high surrogate then a non-low escape (rewind)",
			9,
			`{"jsonrpc":"2.0","id":9,"method":"hi\ud800\u0041lo"}`,
			"hi�Alo",
		},
		{
			"a lone low surrogate",
			10,
			`{"jsonrpc":"2.0","id":10,"method":"lone\udc00end"}`,
			"lone�end",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p.writeLine(c.line)
			resp := p.waitResponse(c.id, 30*time.Second)
			errObj := objOf(resp, "error")
			if errObj == nil {
				t.Fatalf("unknown method did not error: %v", resp)
			}
			if code := intOf(errObj, "code"); code != -32601 {
				t.Errorf("unknown method code %d, want -32601", code)
			}
			if msg := strOf(errObj, "message"); msg != "Method not found: "+c.want {
				t.Errorf("message round-tripped to\n %q\nwant\n %q", msg, "Method not found: "+c.want)
			}
		})
	}
}

// Decision (D6, N9): an unknown method sent as a notification (no id) gets no response,
// as JSON-RPC 2.0 requires; a later request still proves the sidecar is alive and did not
// answer the notification.
func TestGradleUnknownNotificationGetsNoResponse(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	p.writeLine(`{"jsonrpc":"2.0","method":"no.such.notification"}`)
	ping := p.send("ping", nil)
	p.result(ping, 30*time.Second)
	for _, n := range p.msgsSnapshot() {
		if e, isErr := n["error"]; isErr && n["id"] == nil {
			t.Errorf("the sidecar answered the notification with an error: %v", e)
		}
	}
}

// --- the probe's real cells and cases (plan m1h step 2) ---------------------------------

// cell is one Gradle/AGP/Kotlin row of the canary (plan m1h D3, step 1's table). The
// fixture is rewritten per cell in a temporary copy, never in the repository.
type cell struct {
	name string
	// gradle is the wrapper's distribution version; the AGP and Kotlin versions go into
	// settings.gradle.
	gradle string
	agp    string
	kgp    string
	// kotlin keeps the org.jetbrains.kotlin.android lines; C2 drops them (AGP 9's
	// built-in Kotlin).
	kotlin bool
	// libBuildTools is AGP's own default on :lib, where the fixture sets no
	// buildToolsVersion. :app always sets 35.0.0 explicitly (step 1 G4).
	libBuildTools string
}

var cells = []cell{
	{name: "C1", gradle: "8.13", agp: "8.13.2", kgp: "2.0.21", kotlin: true, libBuildTools: "35.0.0"},
	{name: "C2", gradle: "9.6.0", agp: "9.4.1", kgp: "2.2.10", kotlin: false, libBuildTools: "36.0.0"},
	{name: "C4", gradle: "8.0.2", agp: "8.0.2", kgp: "2.0.21", kotlin: true, libBuildTools: "30.0.3"},
}

// flavouredFixture copies sidecar/testdata/flavoured into a temporary directory, adds a
// local.properties naming the host SDK, and rewrites the wrapper's distribution and the
// plugin versions for the cell (plan m1h D3; S8: the fixture is never built in place).
func flavouredFixture(t *testing.T, c cell) string {
	t.Helper()
	sdk := hostSDK()
	if sdk == "" {
		t.Skip("level 5 only: no host Android SDK (set ANDROID_HOME)")
	}
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("testdata/flavoured")); err != nil {
		t.Fatalf("copying testdata/flavoured: %v", err)
	}
	writeFile(t, filepath.Join(dst, "local.properties"), "sdk.dir="+sdk+"\n")
	writeFile(t, filepath.Join(dst, "gradle", "wrapper", "gradle-wrapper.properties"),
		"distributionBase=GRADLE_USER_HOME\n"+
			"distributionPath=wrapper/dists\n"+
			"distributionUrl="+cellDistribution(t, c)+"\n"+
			"zipStoreBase=GRADLE_USER_HOME\n"+
			"zipStorePath=wrapper/dists\n")
	settings := readFile(t, filepath.Join(dst, "settings.gradle"))
	settings = strings.ReplaceAll(settings, "version '8.13.2'", "version '"+c.agp+"'")
	if !c.kotlin {
		settings = dropLines(settings, "org.jetbrains.kotlin.android")
	}
	writeFile(t, filepath.Join(dst, "settings.gradle"), settings)
	if !c.kotlin {
		for _, rel := range []string{"build.gradle", filepath.Join("lib", "build.gradle")} {
			p := filepath.Join(dst, rel)
			writeFile(t, p, dropLines(readFile(t, p), "org.jetbrains.kotlin.android"))
		}
	}
	return dst
}

// cellDistribution is the wrapper URL for a cell: the published one for C1 and C2, and a
// file:// URL to the zip build.sh fetched for C4 (m1h D3), so the floor runs whenever that
// build cache exists.
func cellDistribution(t *testing.T, c cell) string {
	if c.name != "C4" {
		return `https\://services.gradle.org/distributions/gradle-` + c.gradle + `-bin.zip`
	}
	zip := filepath.Join(buildCacheDir(), "gradle-"+c.gradle+"-bin.zip")
	if _, err := os.Stat(zip); err != nil {
		t.Skipf("level 5 only: the Gradle %s zip build.sh fetched is not in %s", c.gradle, buildCacheDir())
	}
	return `file\:` + zip
}

// buildCacheDir mirrors build.sh's cache: $BUILD_CACHE, else the XDG cache.
func buildCacheDir() string {
	if c := os.Getenv("BUILD_CACHE"); c != "" {
		return c
	}
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "droidyard-sidecar-build")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "droidyard-sidecar-build")
}

// hostSDK finds the host's Android SDK, the one local.properties must name (step 1 G4).
func hostSDK() string {
	for _, k := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(k); v != "" && isDir(v) {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, "Library/Android/sdk"),
		filepath.Join(home, "Android/Sdk"),
	} {
		if isDir(c) {
			return c
		}
	}
	return ""
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dropLines removes every line containing substr.
func dropLines(text, substr string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, substr) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// probedModel fetches the fixture's model over one connection and returns the reply.
func probedModel(t *testing.T, p *proc, root string, probe bool) map[string]any {
	t.Helper()
	return p.result(p.send("project.model", map[string]any{"root": root, "probe": probe}), 300*time.Second)
}

// projectTasks maps each project's path to its sorted task names, so a probed and an
// unprobed fetch can be compared for the same modules and tasks.
func projectTasks(model map[string]any) map[string]string {
	out := map[string]string{}
	for _, entry := range arrOf(model, "projects") {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		var names []string
		for _, task := range arrOf(m, "tasks") {
			if tm, ok := task.(map[string]any); ok {
				names = append(names, strOf(tm, "name"))
			}
		}
		sort.Strings(names)
		out[strOf(m, "path")] = strings.Join(names, ",")
	}
	return out
}

func variantNames(android map[string]any) []string {
	var out []string
	for _, v := range arrOf(android, "variants") {
		if vm, ok := v.(map[string]any); ok {
			out = append(out, strOf(vm, "name"))
		}
	}
	sort.Strings(out)
	return out
}

func variantByName(android map[string]any, name string) map[string]any {
	for _, v := range arrOf(android, "variants") {
		if vm, ok := v.(map[string]any); ok && strOf(vm, "name") == name {
			return vm
		}
	}
	return nil
}

func flavorLabels(variant map[string]any) string {
	var out []string
	for _, f := range arrOf(variant, "flavors") {
		if fm, ok := f.(map[string]any); ok {
			out = append(out, strOf(fm, "dimension")+":"+strOf(fm, "name"))
		}
	}
	return strings.Join(out, ",")
}

func notesOf(probe map[string]any) []string {
	var out []string
	for _, n := range arrOf(probe, "notes") {
		if s, ok := n.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func anyNoteContains(notes []string, parts ...string) bool {
	for _, n := range notes {
		all := true
		for _, p := range parts {
			if !strings.Contains(n, p) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return false
}

// Decision (plan m1h D5, D6, D7): a probed project.model is one BuildAction that reads the
// fixture's Android facts — AGP and KGP build-wide, :app's eight variants with build type
// and flavours in dimension order, :lib's debug and release, the compile target, the
// effective build-tools and AGP's SDK directory — with probe.status ok, on C1, C2 and C4,
// and the same modules and tasks as an unprobed fetch. Seen failing with the variants
// dropped, with plugins absent and with a per-cell build-tools constant.
func TestGradleProbeReadsTheFixturePerCell(t *testing.T) {
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			gradleTest(t)
			root := flavouredFixture(t, c)
			p := startProc(t, false)
			openRoot(t, p, root)

			model := probedModel(t, p, root, true)
			plugins := objOf(model, "plugins")
			if plugins == nil {
				t.Fatalf("a probed reply has no plugins block: %v", model)
			}
			if got := strOf(plugins, "agp"); got != c.agp {
				t.Errorf("plugins.agp = %q, want %q", got, c.agp)
			}
			if got := strOf(plugins, "kgp"); got != c.kgp {
				t.Errorf("plugins.kgp = %q, want %q", got, c.kgp)
			}
			probe := objOf(model, "probe")
			if got := strOf(probe, "status"); got != "ok" {
				t.Errorf("probe.status = %q, want ok (notes: %v)", got, notesOf(probe))
			}

			projects := arrOf(model, "projects")
			app := projectAt(projects, ":app")
			if app == nil {
				t.Fatalf("no :app: %v", projectPaths(projects))
			}
			appA := objOf(app, "android")
			if appA == nil {
				t.Fatal(":app has no android block")
			}
			if got := strOf(appA, "compileTarget"); got != "android-36" {
				t.Errorf(":app compileTarget = %q, want android-36", got)
			}
			if got := strOf(appA, "buildTools"); got != "35.0.0" {
				t.Errorf(":app buildTools = %q, want the explicit 35.0.0", got)
			}
			if got := strOf(appA, "sdkDir"); !samePath(got, hostSDK()) {
				t.Errorf(":app sdkDir = %q, want the host SDK %q", got, hostSDK())
			}
			appBuildTypes := map[string]string{
				"freeStagingDebug": "debug", "freeStagingRelease": "release",
				"freeProdDebug": "debug", "freeProdRelease": "release",
				"paidStagingDebug": "debug", "paidStagingRelease": "release",
				"paidProdDebug": "debug", "paidProdRelease": "release",
			}
			appFlavors := map[string]string{
				"freeStagingDebug": "tier:free,stage:staging", "freeStagingRelease": "tier:free,stage:staging",
				"freeProdDebug": "tier:free,stage:prod", "freeProdRelease": "tier:free,stage:prod",
				"paidStagingDebug": "tier:paid,stage:staging", "paidStagingRelease": "tier:paid,stage:staging",
				"paidProdDebug": "tier:paid,stage:prod", "paidProdRelease": "tier:paid,stage:prod",
			}
			seen := map[string]bool{}
			for _, v := range arrOf(appA, "variants") {
				vm, _ := v.(map[string]any)
				name := strOf(vm, "name")
				want, ok := appBuildTypes[name]
				if !ok {
					t.Errorf(":app has an unexpected variant %q", name)
					continue
				}
				seen[name] = true
				if bt := strOf(vm, "buildType"); bt != want {
					t.Errorf(":app %s buildType = %q, want %q", name, bt, want)
				}
				if fl := flavorLabels(vm); fl != appFlavors[name] {
					t.Errorf(":app %s flavors = %q, want %q", name, fl, appFlavors[name])
				}
			}
			for name := range appBuildTypes {
				if !seen[name] {
					t.Errorf(":app is missing variant %q (got %v)", name, variantNames(appA))
				}
			}

			lib := projectAt(projects, ":lib")
			if lib == nil {
				t.Fatalf("no :lib: %v", projectPaths(projects))
			}
			libA := objOf(lib, "android")
			if libA == nil {
				t.Fatal(":lib has no android block")
			}
			if got := strOf(libA, "buildTools"); got != c.libBuildTools {
				t.Errorf(":lib buildTools = %q, want AGP's default %q", got, c.libBuildTools)
			}
			if got := variantNames(libA); strings.Join(got, ",") != "debug,release" {
				t.Errorf(":lib variants = %v, want debug and release", got)
			}
			t.Logf("%s: agp %s kgp %s status %s; :app %s build-tools %s, %d variants; :lib build-tools %s",
				c.name, strOf(plugins, "agp"), strOf(plugins, "kgp"), strOf(probe, "status"),
				strOf(appA, "compileTarget"), strOf(appA, "buildTools"), len(arrOf(appA, "variants")),
				strOf(libA, "buildTools"))

			// Same modules and tasks as an unprobed fetch of the same root.
			unprobed := probedModel(t, p, root, false)
			got, want := projectTasks(model), projectTasks(unprobed)
			if len(got) != len(want) {
				t.Fatalf("the probed fetch has %d modules, the unprobed %d: %v against %v", len(got), len(want), got, want)
			}
			for path, tasks := range want {
				if got[path] != tasks {
					t.Errorf("module %s: probed tasks %q, unprobed %q", path, got[path], tasks)
				}
			}
		})
	}
}

// Decision (plan m1h D5, D7): probe: false answers the generic model and probe.status
// skipped, with no plugins and no per-project android. Seen failing when the probe runs
// regardless of the flag.
func TestGradleProbeSkippedWhenNotAsked(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	p := startProc(t, false)
	openRoot(t, p, root)

	model := probedModel(t, p, root, false)
	if got := objOf(model, "plugins"); got != nil {
		t.Errorf("an unprobed reply carries plugins: %v", got)
	}
	if probe := objOf(model, "probe"); strOf(probe, "status") != "skipped" {
		t.Errorf("probe.status = %q, want skipped", strOf(probe, "status"))
	}
	for _, entry := range arrOf(model, "projects") {
		if m, ok := entry.(map[string]any); ok && objOf(m, "android") != nil {
			t.Errorf("an unprobed reply carries android for %s", strOf(m, "path"))
		}
	}
}

// Decision (plan m1h D6): a field whose own read fails is omitted and noted, and
// probe.status is partial; the rest of the facts survive. Seen failing with one try
// around all fields, where the broken field takes the variants with it.
func TestGradleProbeFieldFailureIsPartial(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	p := startProcOpts(t, procOpts{probeArgs: "-Ddroidyard.probe.fail=compileTarget"})
	openRoot(t, p, root)

	model := probedModel(t, p, root, true)
	probe := objOf(model, "probe")
	if got := strOf(probe, "status"); got != "partial" {
		t.Errorf("probe.status = %q, want partial", got)
	}
	if !anyNoteContains(notesOf(probe), ":app", "compileTarget") {
		t.Errorf("no note names :app and compileTarget: %v", notesOf(probe))
	}
	app := projectAt(arrOf(model, "projects"), ":app")
	appA := objOf(app, "android")
	if appA == nil {
		t.Fatal(":app lost its whole android block with one field failing")
	}
	if _, present := appA["compileTarget"]; present {
		t.Errorf(":app compileTarget survived a failed read: %v", appA)
	}
	if got := strOf(appA, "buildTools"); got != "35.0.0" {
		t.Errorf(":app buildTools = %q after a compileTarget failure, want 35.0.0", got)
	}
	if got := strOf(appA, "sdkDir"); !samePath(got, hostSDK()) {
		t.Errorf(":app sdkDir = %q after a compileTarget failure, want the host SDK %q", got, hostSDK())
	}
	if got := variantNames(appA); len(got) != 8 {
		t.Errorf(":app variants = %v after a compileTarget failure, want the eight", got)
	}
}

// Decision (plan m1h D5, D17): a broken probe is retried once without it, one stderr line
// is logged, and the generic model is answered with probe.status failed. Seen failing with
// the retry removed (the broken script fails the call) and with the retry unconditional.
func TestGradleProbeBrokenInitScriptRetriesOnce(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.init.gradle")
	writeFile(t, bad, "this is not { valid gradle\n")
	p := startProcOpts(t, procOpts{probeInit: bad})
	openRoot(t, p, root)

	model := probedModel(t, p, root, true)
	probe := objOf(model, "probe")
	if got := strOf(probe, "status"); got != "failed" {
		t.Errorf("probe.status = %q, want failed (notes: %v)", got, notesOf(probe))
	}
	if objOf(model, "plugins") != nil {
		t.Error("a failed probe reply carries plugins")
	}
	// D5: the retry's note is the first failure's message, not a placeholder of ours.
	notes := notesOf(probe)
	if len(notes) != 1 || !strings.Contains(notes[0], "bad.init.gradle") {
		t.Errorf("the failed probe's note does not carry Gradle's message naming the script: %v", notes)
	}
	if got := len(arrOf(model, "projects")); got != 3 {
		t.Errorf("the retried fetch lists %d projects, want the root, :app and :lib", got)
	}
	if n := strings.Count(p.stderrText(), "retrying without it"); n != 1 {
		t.Errorf("the stderr has %d retry lines, want exactly 1:\n%s", n, p.stderrText())
	}
}

// Decision (plan m1h D6; step 2 review B2): a variant that cannot be read makes the list
// inexact, so the field is omitted (the core falls back to task names) instead of sending
// a short list as exact. Seen failing when the failed variant was only dropped.
func TestGradleProbeVariantFailureOmitsTheList(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	p := startProcOpts(t, procOpts{probeArgs: "-Ddroidyard.probe.fail=variants"})
	openRoot(t, p, root)
	model := probedModel(t, p, root, true)
	probe := objOf(model, "probe")
	if got := strOf(probe, "status"); got != "partial" {
		t.Errorf("probe.status = %q, want partial", got)
	}
	if !anyNoteContains(notesOf(probe), ":app", "variants") {
		t.Errorf("no note names :app and variants: %v", notesOf(probe))
	}
	appA := objOf(projectAt(arrOf(model, "projects"), ":app"), "android")
	if appA == nil {
		t.Fatal(":app lost its whole android block with the variants failing")
	}
	if _, has := appA["variants"]; has {
		t.Errorf(":app still carries variants after a failed read: %v", appA["variants"])
	}
	if got := strOf(appA, "buildTools"); got != "35.0.0" {
		t.Errorf(":app buildTools = %q, want the other fields kept", got)
	}
}

// Decision (plan m1h D5): a failure outside the fields' isolation (the probe's apply) is
// attributed to the probe by its frame, retried once, and answered failed.
func TestGradleProbeApplyFailureRetriesOnce(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	p := startProcOpts(t, procOpts{probeArgs: "-Ddroidyard.probe.fail=apply"})
	openRoot(t, p, root)

	model := probedModel(t, p, root, true)
	if got := strOf(objOf(model, "probe"), "status"); got != "failed" {
		t.Errorf("probe.status = %q, want failed", got)
	}
	if objOf(model, "plugins") != nil {
		t.Error("a failed probe reply carries plugins")
	}
	if n := strings.Count(p.stderrText(), "retrying without it"); n != 1 {
		t.Errorf("the stderr has %d retry lines, want exactly 1:\n%s", n, p.stderrText())
	}
	// B1 of the step 2 review: the note is the probe's own failure, not TAPI's wrapper.
	if notes := notesOf(objOf(model, "probe")); len(notes) != 1 || !strings.Contains(notes[0], "droidyard.probe.fail=apply") {
		t.Errorf("the failed probe's note is not the probe's own failure: %v", notes)
	}
}

// Decision (plan m1h D5): the project's own configuration failure is not the probe's, so
// the fetch fails PROJECT_MODEL_FAILED with the failure chain's messages and no retry.
// Seen failing with the retry unconditional (the broken build script logs a retry).
func TestGradleProbeDoesNotRetryTheProjectsOwnFailure(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	appBuild := filepath.Join(root, "app", "build.gradle")
	writeFile(t, appBuild, readFile(t, appBuild)+"\ndef broken = (\n")
	p := startProc(t, false)
	openRoot(t, p, root)

	resp := p.waitResponse(p.send("project.model", map[string]any{"root": root, "probe": true}), 300*time.Second)
	errObj := objOf(resp, "error")
	if errObj == nil {
		t.Fatalf("a broken app/build.gradle did not fail the fetch: %v", resp)
	}
	if code := intOf(errObj, "code"); code != -32000 {
		t.Errorf("error code %d, want -32000 (an application error)", code)
	}
	if got := strOf(errObj, "message"); got != "project.model failed" {
		t.Errorf("error message %q, want project.model failed", got)
	}
	cause := strOf(objOf(errObj, "data"), "message")
	if !strings.Contains(cause, "app/build.gradle") {
		t.Errorf("the failure cause does not name app/build.gradle:\n%s", cause)
	}
	if n := strings.Count(p.stderrText(), "retrying without it"); n != 0 {
		t.Errorf("the stderr has %d retry lines for the project's own failure, want none:\n%s", n, p.stderrText())
	}
}

// Decision (plan m1h D17; ADR-0018 rule 4): the probe runs only in a model fetch, never in
// a build. A probed fetch followed by a build of the same root leaves no probe marker in
// the build's console. Seen failing with the init script added to build.run.
func TestGradleProbeNeverReachesABuild(t *testing.T) {
	gradleTest(t)
	root := flavouredFixture(t, cells[0])
	p := startProc(t, false)
	openRoot(t, p, root)
	if got := strOf(objOf(probedModel(t, p, root, true), "probe"), "status"); got != "ok" {
		t.Fatalf("the probed fetch did not succeed: %q", got)
	}
	fin := runBuild(t, p, root, "b-probed", ":app:assembleFreeStagingDebug")
	if got := strOf(objOf(fin, "params"), "outcome"); got != "success" {
		t.Fatalf("the build after a probed fetch ended %q, want success: %v", got, fin)
	}
	for _, line := range p.outputLines("b-probed") {
		if strings.Contains(line, "droidyard-probe") {
			t.Errorf("the build's console carries the probe marker: %q", line)
		}
	}
	// Not vacuous: the build really streamed a console, so "no marker in it" means
	// something.
	if len(p.outputLines("b-probed")) == 0 {
		t.Error("the build streamed no console at all, so the absence of a marker proves nothing")
	}
}

func (p *proc) badStdout() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.badLines...)
}

func (p *proc) msgsSnapshot() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.msgs...)
}

// samePath reports whether two paths name the same directory, following symlinks.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra == nil && errb == nil {
		return ra == rb
	}
	return a == b
}

// maxFailureNodes mirrors the sidecar's D6 tree cap.
const maxFailureNodes = 200

// countTree returns the number of failure-tree nodes and how many carry truncatedCauses.
func countTree(failures []any) (nodes, truncated int) {
	var walk func(f any)
	walk = func(f any) {
		m, ok := f.(map[string]any)
		if !ok {
			return
		}
		nodes++
		if intOf(m, "truncatedCauses") > 0 {
			truncated++
		}
		for _, c := range arrOf(m, "causes") {
			walk(c)
		}
	}
	for _, f := range failures {
		walk(f)
	}
	return nodes, truncated
}

// --- decoded-message accessors ---------------------------------------------------------

func idOf(m map[string]any) int {
	f, _ := m["id"].(float64)
	return int(f)
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func boolOf(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func intOf(m map[string]any, k string) int {
	f, _ := m[k].(float64)
	return int(f)
}

func arrOf(m map[string]any, k string) []any {
	a, _ := m[k].([]any)
	return a
}

func objOf(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}

func projectAt(projects []any, path string) map[string]any {
	for _, p := range projects {
		if m, ok := p.(map[string]any); ok && strOf(m, "path") == path {
			return m
		}
	}
	return nil
}

func projectPaths(projects []any) []string {
	var out []string
	for _, p := range projects {
		if m, ok := p.(map[string]any); ok {
			out = append(out, strOf(m, "path"))
		}
	}
	return out
}

func hasTask(project map[string]any, name string) bool {
	for _, t := range arrOf(project, "tasks") {
		if m, ok := t.(map[string]any); ok && strOf(m, "name") == name {
			return true
		}
	}
	return false
}

// Decision (step 1 re-check, R1): a build.cancel that overtakes its own build.run (the
// run still queued behind a model fetch) is not lost: the build starts cancelled and
// ends `cancelled` with one finished. Seen failing before the tombstone: the cancel
// answered "no such build" and the build ran to success.
func TestGradleCancelBeforeItsRunIsDispatched(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)
	openRoot(t, p, root)
	if fin := runBuild(t, p, root, "b-warm", ":echoLine"); strOf(objOf(fin, "params"), "outcome") != "success" {
		t.Fatalf("warm-up build did not succeed: %v", fin)
	}
	// A slow build holds the root's lock; a model fetch then holds the dispatcher.
	id := p.send("build.run", map[string]any{"buildId": "b-hold", "root": root, "tasks": []string{":slow"}})
	if boolOf(p.result(id, 30*time.Second), "accepted") != true {
		t.Fatal("build.run :slow was not accepted")
	}
	time.Sleep(time.Second)
	modelID := p.send("project.model", map[string]any{"root": root})
	time.Sleep(300 * time.Millisecond)
	// This run queues behind the model fetch; its cancel is served at once.
	lateID := p.send("build.run", map[string]any{"buildId": "b-late", "root": root, "tasks": []string{":echoLine"}})
	p.result(p.send("build.cancel", map[string]any{"buildId": "b-late"}), 5*time.Second)
	p.result(p.send("build.cancel", map[string]any{"buildId": "b-hold"}), 5*time.Second)
	p.result(modelID, 60*time.Second)
	p.result(lateID, 60*time.Second)
	fin := p.waitFor(60*time.Second, func(m map[string]any) bool {
		return m["method"] == "event.build.finished" && strOf(objOf(m, "params"), "buildId") == "b-late"
	})
	if got := strOf(objOf(fin, "params"), "outcome"); got != "cancelled" {
		t.Errorf("a build cancelled before its run was dispatched ended %q, want cancelled", got)
	}
}

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

// startProc launches the embedded jars on the embedded classpath, in a process group of
// its own as the supervisor spawns the sidecar. With testStdout it passes
// -Ddroidyard.sidecar.testStdout, which makes the sidecar write to System.out at start
// and while serving a request.
func startProc(t *testing.T, testStdout bool) *proc {
	t.Helper()
	java := filepath.Join(findJDK(), "bin", "java")
	var args []string
	if testStdout {
		args = append(args, "-Ddroidyard.sidecar.testStdout")
	}
	args = append(args, "-cp", writeJars(t), Class)
	cmd := exec.Command(java, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

// writeJars writes the embedded jars out and returns their classpath. The test runs the
// bytes the daemon will run, not a build tree.
func writeJars(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	deps := filepath.Join(dir, "gradle-sidecar-deps.jar")
	classes := filepath.Join(dir, "gradle-sidecar.jar")
	if err := os.WriteFile(deps, DepsJar(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(classes, Jar(), 0o644); err != nil {
		t.Fatal(err)
	}
	return deps + string(os.PathListSeparator) + classes
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

// Decision (gradle.md §Protocol, D19): the handshake carries protocol 1 and the pinned
// Tooling API; connection.open reports Gradle 8.13 from the wrapper and the daemon's JVM
// home; project.model lists the root and its one subproject with compileJava. ping and
// shutdown answer too.
func TestGradleHandshakeAndProjectModel(t *testing.T) {
	gradleTest(t)
	p := startProc(t, false)
	root := plainFixture(t)

	hello := p.result(p.send("hello", nil), 60*time.Second)
	if got := intOf(hello, "protocolVersion"); got != 1 {
		t.Errorf("hello protocolVersion %d, want 1", got)
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

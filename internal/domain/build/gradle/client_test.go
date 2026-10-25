package gradle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// model is the reply every client test serves: two modules with the tasks the step 2
// run recorded.
func model(t *testing.T) gradle.Model {
	t.Helper()
	root := filepath.Join("..", "testdata", "s1run2")
	projects := readLines(t, filepath.Join(root, "projects.txt"))
	m := gradle.Model{Root: "/p"}
	for _, path := range projects {
		m.Projects = append(m.Projects, gradle.Module{Path: path, Name: name(path), Dir: dir(path), Tasks: tasksOf(t, root, path)})
	}
	return m
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// tasksOf reads a module's recorded task names, which the step 2 run saved as
// tasks-<module with : replaced by ->.txt; a module with no recording has no tasks.
func tasksOf(t *testing.T, root, path string) []gradle.Task {
	t.Helper()
	file := "tasks-" + strings.ReplaceAll(strings.TrimPrefix(path, ":"), ":", "-") + ".txt"
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil
	}
	var out []gradle.Task
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		out = append(out, gradle.Task{Name: name, Path: path + ":" + name, Group: "build", Public: true})
	}
	return out
}

func name(path string) string {
	if path == ":" {
		return "root"
	}
	if i := strings.LastIndex(path, ":"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func dir(path string) string {
	if path == ":" {
		return "."
	}
	return strings.ReplaceAll(strings.TrimPrefix(path, ":"), ":", "/")
}

// newClient starts a client over a fake sidecar scripted by cfg. It returns the client,
// the supervisor (to watch the process) and the events file.
func newClient(t *testing.T, cfg gradletest.Config, idle time.Duration) (*gradle.Client, *process.Supervisor, string) {
	t.Helper()
	return newClientFloor(t, cfg, idle, policy.BelowFloor{})
}

// newClientFloor is newClient with `gradle.allowBelowFloor` as read at daemon start, so a
// test can admit a Gradle below the floor (plan m1k step 7, D9).
func newClientFloor(t *testing.T, cfg gradletest.Config, idle time.Duration, floor policy.BelowFloor) (*gradle.Client, *process.Supervisor, string) {
	t.Helper()
	return newClientEnv(t, cfg, idle, floor, nil)
}

// newClientEnv is newClientFloor with the client's environment (nil: the daemon's).
func newClientEnv(t *testing.T, cfg gradletest.Config, idle time.Duration, floor policy.BelowFloor, env []string) (*gradle.Client, *process.Supervisor, string) {
	t.Helper()
	return newClientWith(t, cfg, idle, floor, env, nil)
}

// newClientWith is newClientEnv with a last say on the client's options (m3e step 5's
// InUse).
func newClientWith(t *testing.T, cfg gradletest.Config, idle time.Duration, floor policy.BelowFloor, env []string, tweak func(*gradle.Options)) (*gradle.Client, *process.Supervisor, string) {
	t.Helper()
	dir := t.TempDir()
	cfg.Events = filepath.Join(dir, "events")
	cfgPath := gradletest.WriteConfig(t, cfg)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: st, Log: testLog{t}})
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Shutdown)
	jvm := gradle.JVM{Found: true, Home: "/fake/jdk17", Version: "17.0.11", Source: gradle.SourceJavaHome}
	opts := gradle.Options{
		Supervisor: sup, Log: testLog{t}, JVM: &jvm, CacheDir: dir,
		Launch: gradletest.Launch(cfgPath), IdleTimeout: idle, AllowBelowFloor: floor, Env: env,
	}
	if tweak != nil {
		tweak(&opts)
	}
	c := gradle.New(opts)
	return c, sup, cfg.Events
}

func rootDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// events reads the fake's event log.
func events(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func started(t *testing.T, path string) int {
	n := 0
	for _, e := range events(t, path) {
		if strings.HasPrefix(e, "started ") {
			n++
		}
	}
	return n
}

// gone reports whether a process state means it is no longer running: Stopped for an
// orderly stop, Failed for one the supervisor killed on a protocol violation.
func gone(s process.State) bool { return s == process.Stopped || s == process.Failed }

// waitFor polls until cond holds, failing the test after 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func codeOf(t *testing.T, err error) capability.ErrorCode {
	t.Helper()
	e, ok := err.(*capability.Error)
	if !ok {
		t.Fatalf("error %v is not a capability error", err)
	}
	return e.Code
}

// Decision (gradle.md §Protocol; plan m1p step 2's level-4 run): a closing workspace
// closes its root's connection on the sidecar whenever the sidecar runs, idle included,
// so the root can be opened again. Before the fix Close spoke only to a "ready" sidecar;
// after any call it is "idle", so the close never reached it and every later open
// failed "root is already open" until the daemon restarted. Seen failing on that code.
func TestCloseReachesAnIdleSidecarSoTheRootReopens(t *testing.T) {
	c, sup, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	ctx := context.Background()
	root := rootDir(t)
	if _, _, err := c.Model(ctx, root); err != nil {
		t.Fatal(err)
	}
	if st := sup.State(gradle.ID, process.Global); st != process.Idle {
		t.Fatalf("after a model call the sidecar is %q; this test needs it idle", st)
	}
	c.Close(ctx, root)
	closed := false
	for _, e := range events(t, ev) {
		closed = closed || e == "served connection.close"
	}
	if !closed {
		t.Fatalf("Close did not reach the idle sidecar; events: %q", events(t, ev))
	}
	if _, _, err := c.Model(ctx, root); err != nil {
		t.Fatalf("the root does not reopen after a close: %v", err)
	}
}

// TestModelSpawnsOnceAndSharesOneProcessAcrossRoots pins ADR-0019: one sidecar per
// daemon serves every root, so the second root makes no second process.
func TestModelSpawnsOnceAndSharesOneProcessAcrossRoots(t *testing.T) {
	c, sup, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	ctx := context.Background()
	a, b := rootDir(t), rootDir(t)

	if _, stale, err := c.Model(ctx, a); err != nil || stale {
		t.Fatalf("first root: stale=%v err=%v", stale, err)
	}
	pid := sup.Pid(gradle.ID, process.Global)
	if pid == 0 {
		t.Fatal("the first model call did not start the sidecar")
	}
	if _, stale, err := c.Model(ctx, b); err != nil || stale {
		t.Fatalf("second root: stale=%v err=%v", stale, err)
	}
	if got := sup.Pid(gradle.ID, process.Global); got != pid {
		t.Errorf("the second root runs the sidecar in a second process (%d, was %d)", got, pid)
	}
	if n := started(t, ev); n != 1 {
		t.Errorf("the sidecar started %d times; want once for both roots", n)
	}
}

// TestProtocolVersionMismatchIsUnavailable pins the hello check: a sidecar speaking
// another protocol is refused BUILD_SIDECAR_UNAVAILABLE and stopped, never used. Seen
// failing with the version check removed.
func TestProtocolVersionMismatchIsUnavailable(t *testing.T) {
	c, sup, _ := newClient(t, gradletest.Config{ProtocolVersion: 1, Model: gradletest.MustModel(model(t))}, time.Minute)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildSidecarUnavailable {
		t.Fatalf("code = %s, want BUILD_SIDECAR_UNAVAILABLE: %v", got, err)
	}
	if !strings.Contains(err.Error(), "protocol version 1") {
		t.Errorf("the error does not name the sidecar's version: %v", err)
	}
	waitFor(t, "the sidecar to be stopped", func() bool { return sup.State(gradle.ID, process.Global) == process.Stopped })
}

// TestNonJSONStdoutLineIsACrash pins D6: stdout carries the protocol only, so a line
// that is not JSON-RPC means the channel is lost — BUILD_SIDECAR_CRASHED and the
// process killed, not a silent ignore.
func TestNonJSONStdoutLineIsACrash(t *testing.T) {
	c, sup, _ := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t)), StrayOn: "project.model"}, time.Minute)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildSidecarCrashed {
		t.Fatalf("code = %s, want BUILD_SIDECAR_CRASHED: %v", got, err)
	}
	waitFor(t, "the sidecar to be killed", func() bool { return sup.State(gradle.ID, process.Global) == process.Stopped })
}

// TestKilledMidCallDegradesBothRootsAndTheNextCallClears pins D20: the call fails
// BUILD_SIDECAR_CRASHED, every root with a connection is marked degraded, the crash drops
// the cached models, and the next call restarts the sidecar and clears it.
func TestKilledMidCallDegradesBothRootsAndTheNextCallClears(t *testing.T) {
	cfg := gradletest.Config{Model: gradletest.MustModel(model(t)), HoldOn: "project.model", HoldAfter: 2}
	c, sup, ev := newClient(t, cfg, time.Minute)
	ctx := context.Background()
	a, b := rootDir(t), rootDir(t)
	if _, _, err := c.Model(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Model(ctx, b); err != nil {
		t.Fatal(err)
	}

	// An unchanged root answers from the cache (D8), so the third call needs a build
	// file edit to reach the sidecar.
	if err := os.WriteFile(filepath.Join(a, "build.gradle.kts"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	go func() {
		_, _, err := c.Model(ctx, a)
		failed <- err
	}()
	waitFor(t, "the third call to reach the fake", func() bool { return count(events(t, ev), "served project.model") >= 3 })
	pid := sup.Pid(gradle.ID, process.Global)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err := <-failed
	if got := codeOf(t, err); got != capability.ErrBuildSidecarCrashed {
		t.Fatalf("code = %s, want BUILD_SIDECAR_CRASHED: %v", got, err)
	}
	for _, root := range []string{a, b} {
		if d := c.Degraded(root); d == nil || d.Code != capability.ErrBuildSidecarCrashed {
			t.Errorf("root %s is not degraded after the crash: %v", root, d)
		}
	}

	if _, _, err := c.Model(ctx, b); err != nil {
		t.Fatalf("the call after the crash did not restart the sidecar: %v", err)
	}
	if d := c.Degraded(b); d != nil {
		t.Errorf("the successful call did not clear the degradation: %v", d)
	}
	if n := started(t, ev); n != 2 {
		t.Errorf("the sidecar started %d times; want a restart after the crash", n)
	}
}

func count(lines []string, want string) int {
	n := 0
	for _, line := range lines {
		if line == want {
			n++
		}
	}
	return n
}

// TestALineOverTheBoundIsACrash pins the revised D6: the client accepts a line up to
// 16 MiB and treats one over it as a protocol violation, failing the call
// BUILD_SIDECAR_CRASHED and killing the sidecar. (The plan's step 5 text still says
// 2 MiB; §Changes, from the step 1 fix round, raised it to 16 MiB, so this uses 17.)
func TestALineOverTheBoundIsACrash(t *testing.T) {
	cfg := gradletest.Config{Model: gradletest.MustModel(model(t)), BigLineOn: "project.model", BigLineBytes: 17 << 20}
	c, sup, _ := newClient(t, cfg, time.Minute)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildSidecarCrashed {
		t.Fatalf("code = %s, want BUILD_SIDECAR_CRASHED: %v", got, err)
	}
	if !strings.Contains(err.Error(), "16 MiB") {
		t.Errorf("the cause does not name the bound: %v", err)
	}
	waitFor(t, "the sidecar to be killed", func() bool { return gone(sup.State(gradle.ID, process.Global)) })
}

// TestIdleStop pins gradle.md §Lifecycle: the sidecar is stopped once it has gone
// unused for the idle timeout and no lease is held — and not while a lease is, which is
// what a running build holds (client.Hold).
func TestIdleStop(t *testing.T) {
	c, sup, _ := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, 300*time.Millisecond)
	ctx := context.Background()
	if _, _, err := c.Model(ctx, rootDir(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the idle sidecar to be stopped", func() bool { return sup.State(gradle.ID, process.Global) == process.Stopped })

	lease, err := c.Hold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * 300 * time.Millisecond)
	if s := sup.State(gradle.ID, process.Global); s == process.Stopped {
		t.Fatalf("the sidecar was stopped while a build held a lease (%s)", s)
	}
	lease.Release()
	waitFor(t, "the sidecar to stop once the lease is released", func() bool { return sup.State(gradle.ID, process.Global) == process.Stopped })
}

// TestShutdownClosesStdin pins that the daemon's stop ends the sidecar through stdin
// EOF, the protocol's kill switch, rather than leaving it running.
func TestShutdownClosesStdin(t *testing.T) {
	c, sup, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	if _, _, err := c.Model(context.Background(), rootDir(t)); err != nil {
		t.Fatal(err)
	}
	sup.Shutdown()
	waitFor(t, "the fake to see stdin EOF", func() bool { return count(events(t, ev), "eof") == 1 })
}

// TestGradleBelowTheFloorIsRefused pins ADR-0018 rule 3 and D9: a project whose Gradle
// is below the floor fails BUILD_GRADLE_UNSUPPORTED naming floor and version, before
// any model work.
func TestGradleBelowTheFloorIsRefused(t *testing.T) {
	c, _, _ := newClient(t, gradletest.Config{GradleVersion: "7.6", Model: gradletest.MustModel(model(t))}, time.Minute)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildGradleUnsupported {
		t.Fatalf("code = %s, want BUILD_GRADLE_UNSUPPORTED: %v", got, err)
	}
	for _, want := range []string{"7.6", gradle.GradleFloor} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
}

// Decision (plan m1k step 7, D9; ADR-0018 rule 3): without `gradle.allowBelowFloor` a
// Tier C Gradle (7.3–7.x) is refused, and the refusal's nextStep names the hatch and the
// configuration file, so a human knows what to set and where.
func TestBelowFloorWithoutTheKeyNamesTheHatch(t *testing.T) {
	const cfgPath = "/etc/droidyard/config.json"
	floor := policy.BelowFloor{ConfigPath: cfgPath} // not allowed
	c, _, _ := newClientFloor(t, gradletest.Config{GradleVersion: "7.6.4", Model: gradletest.MustModel(model(t))}, time.Minute, floor)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildGradleUnsupported {
		t.Fatalf("code = %s, want BUILD_GRADLE_UNSUPPORTED: %v", got, err)
	}
	ce := err.(*capability.Error)
	if !strings.Contains(ce.NextStep, "gradle.allowBelowFloor") || !strings.Contains(ce.NextStep, cfgPath) {
		t.Errorf("the nextStep %q does not name the hatch and the file %q", ce.NextStep, cfgPath)
	}
}

// Decision (ADR-0032 §4; m1k close review N3): when the file was invalid at start, the
// refusal reports the file and its error instead of asking for a key it may already set.
func TestBelowFloorUnderAnInvalidFileReportsTheFile(t *testing.T) {
	const cfgPath = "/etc/droidyard/config.json"
	floor := policy.BelowFloor{ConfigPath: cfgPath, Invalid: "tools.adb: \"adb\" is not an absolute path"}
	c, _, _ := newClientFloor(t, gradletest.Config{GradleVersion: "7.6.4", Model: gradletest.MustModel(model(t))}, time.Minute, floor)
	_, _, err := c.Model(context.Background(), rootDir(t))
	ce, ok := err.(*capability.Error)
	if !ok || ce.Code != capability.ErrBuildGradleUnsupported {
		t.Fatalf("err = %v, want BUILD_GRADLE_UNSUPPORTED", err)
	}
	if !strings.Contains(ce.Cause, cfgPath) || !strings.Contains(ce.Cause, "not an absolute path") || !strings.Contains(ce.NextStep, "fix "+cfgPath) {
		t.Errorf("cause %q / nextStep %q do not report the invalid file", ce.Cause, ce.NextStep)
	}
}

// Decision (plan m1k step 7, D9; ADR-0018 rule 3): with `gradle.allowBelowFloor` set at
// daemon start, a Tier C Gradle opens its connection instead of being refused. Seen
// failing with the hatch admitting only down to 7.0.
func TestBelowFloorWithTheKeyOpensTheConnection(t *testing.T) {
	floor := policy.BelowFloor{Allowed: true, ConfigPath: "/etc/droidyard/config.json"}
	c, _, _ := newClientFloor(t, gradletest.Config{GradleVersion: "7.6.4", Model: gradletest.MustModel(model(t))}, time.Minute, floor)
	m, _, err := c.Model(context.Background(), rootDir(t))
	if err != nil {
		t.Fatalf("a Tier C Gradle under the hatch was refused: %v", err)
	}
	if len(m.Projects) == 0 {
		t.Error("the connection opened but the model is empty")
	}
}

// Decision (plan m1k step 7, D9; ADR-0018 rule 3): Tier D (below 7.3) is refused
// whatever the key says, and names no hatch, because nothing admits it. Seen failing
// with the hatch admitting 7.2.
func TestBelowTierCIsRefusedEvenWithTheKey(t *testing.T) {
	floor := policy.BelowFloor{Allowed: true, ConfigPath: "/etc/droidyard/config.json"}
	c, _, _ := newClientFloor(t, gradletest.Config{GradleVersion: "7.2", Model: gradletest.MustModel(model(t))}, time.Minute, floor)
	_, _, err := c.Model(context.Background(), rootDir(t))
	if got := codeOf(t, err); got != capability.ErrBuildGradleUnsupported {
		t.Fatalf("code = %s, want BUILD_GRADLE_UNSUPPORTED: %v", got, err)
	}
	if strings.Contains(err.Error(), "allowBelowFloor") {
		t.Errorf("the Tier D refusal names a hatch that cannot help: %v", err)
	}
}

// Decision (plan m1k step 7, D9): the floor itself is admitted without the hatch, so
// 8.0.2 opens as before (the build profile is the build-level degraded case, tested
// there).
func TestAtTheFloorOpensWithoutTheHatch(t *testing.T) {
	c, _, _ := newClient(t, gradletest.Config{GradleVersion: "8.0.2", Model: gradletest.MustModel(model(t))}, time.Minute)
	if _, _, err := c.Model(context.Background(), rootDir(t)); err != nil {
		t.Fatalf("8.0.2 is at the floor and must open without the hatch: %v", err)
	}
}

// TestCachedModelWhileABuildHoldsTheRoot pins R5 and D8: with the per-root lock held by
// a build, a model call answers from the cache at once rather than waiting for Gradle,
// with stale true once a build file has changed.
func TestCachedModelWhileABuildHoldsTheRoot(t *testing.T) {
	c, sup, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	ctx := context.Background()
	root := rootDir(t)
	if err := os.WriteFile(filepath.Join(root, "settings.gradle.kts"), []byte("rootProject.name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Model(ctx, root); err != nil {
		t.Fatal(err)
	}
	calls := count(events(t, ev), "served connection.open")

	release, err := c.LockRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale, err := c.Model(ctx, root); err != nil || stale {
		t.Fatalf("while the build holds the root and nothing changed: stale=%v err=%v", stale, err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.gradle.kts"), []byte("plugins {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		_, stale, err := c.Model(ctx, root)
		if err != nil {
			t.Errorf("cached model: %v", err)
		}
		done <- stale
	}()
	select {
	case stale := <-done:
		if !stale {
			t.Error("a build file changed while the root was busy, but the answer is not marked stale")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the model call blocked on the root a build holds; it must answer from the cache")
	}
	release()
	if got := count(events(t, ev), "served connection.open"); got != calls {
		t.Errorf("the cached answer opened the connection %d times, was %d: it must not touch Gradle", got, calls)
	}
	if sup.Pid(gradle.ID, process.Global) == 0 {
		t.Error("the cached answer stopped the sidecar")
	}
}

// TestVariantsFromRecordedTaskNames pins ADR-0023 §Decision 4 over step 2's real task
// lists: :app has debug and release, the library has none, and the AndroidTest and
// UnitTest variants are excluded. Seen failing with AndroidTest kept.
func TestVariantsFromRecordedTaskNames(t *testing.T) {
	m := model(t)
	app, ok := m.Module(":app")
	if !ok {
		t.Fatal("the recorded model has no :app")
	}
	got := gradle.VariantNames(app)
	if strings.Join(got, ",") != "debug,release" {
		t.Errorf(":app variants = %v, want debug and release", got)
	}
	for _, bad := range []string{"DebugAndroidTest", "DebugUnitTest"} {
		for _, v := range got {
			if strings.EqualFold(v, bad) {
				t.Errorf(":app variants include %s", v)
			}
		}
	}
	lib, ok := m.Module(":core:database")
	if !ok {
		t.Fatal("the recorded model has no :core:database")
	}
	if v := gradle.VariantNames(lib); len(v) != 0 {
		t.Errorf(":core:database variants = %v, want none (it packages no APK)", v)
	}
}

// TestFingerprintCoversIncludedBuildLogicAndIgnoresNoise pins D8: a build-logic edit
// changes the fingerprint, and build output and source that cannot change the model do
// not.
func TestFingerprintCoversIncludedBuildLogicAndIgnoresNoise(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.gradle.kts", "include(\":app\")\n")
	write("app/build.gradle.kts", "plugins {}\n")
	write("gradle/libs.versions.toml", "[versions]\n")
	write("build-logic/settings.gradle.kts", "rootProject.name = \"build-logic\"\n")
	write("build-logic/convention.gradle.kts", "// v1\n")
	write("app/build/generated.txt", "output\n")
	write("app/src/main/App.kt", "class App\n")

	base := gradle.Fingerprint(root, "")
	if base == "" {
		t.Fatal("the fingerprint is empty")
	}

	// Noise: neither build output nor source is part of the model's inputs.
	write("app/build/generated.txt", "other output, longer\n")
	write("app/src/main/App.kt", "class App2\n")
	if got := gradle.Fingerprint(root, ""); got != base {
		t.Errorf("a build-output or source edit changed the fingerprint: %s -> %s", base, got)
	}

	// An included build's own script is part of it.
	write("build-logic/convention.gradle.kts", "// v2, a longer body\n")
	if got := gradle.Fingerprint(root, ""); got == base {
		t.Errorf("a build-logic edit did not change the fingerprint (%s)", got)
	}
	next := gradle.Fingerprint(root, "")
	write("gradle/libs.versions.toml", "[versions]\napp = \"1\"\n")
	if gradle.Fingerprint(root, "") == next {
		t.Error("a version-catalog edit did not change the fingerprint")
	}
}

// TestClientAlwaysAsksForTheProbe pins D7 and D25: `project.model` is always asked with
// probe: true — the probe is the only source of the Android facts — and a reply carrying
// plugins, a per-module android block and the probe object still parses into the generic
// model the rest of the core uses. Seen failing when the request drops probe: true, and
// when a probe field the model does not know breaks the parse.
func TestClientAlwaysAsksForTheProbe(t *testing.T) {
	cfg := gradletest.Config{
		Model: gradletest.MustModel(model(t)),
		Probe: &gradletest.Probe{
			AGP: "8.13.2",
			KGP: "2.0.21",
			Android: map[string]gradletest.Android{
				":app": {
					CompileTarget: "android-36",
					BuildTools:    "35.0.0",
					SDKDir:        "/sdk",
					Variants: []gradletest.Variant{
						{Name: "debug", BuildType: "debug", Flavors: []gradletest.Flavor{{Dimension: "tier", Name: "free"}}},
					},
				},
			},
		},
	}
	c, _, ev := newClient(t, cfg, time.Minute)
	m, _, err := c.Model(context.Background(), rootDir(t))
	if err != nil {
		t.Fatalf("a probed fetch failed: %v", err)
	}
	if n := count(events(t, ev), "probe true"); n != 1 {
		t.Errorf("the client asked for %d probed fetches, want 1: %v", n, events(t, ev))
	}
	want := model(t)
	if len(m.Projects) != len(want.Projects) {
		t.Fatalf("the probed reply parsed to %d modules, want %d", len(m.Projects), len(want.Projects))
	}
	app, ok := m.Module(":app")
	if !ok || len(app.Tasks) == 0 {
		t.Errorf("the probed reply lost :app's tasks: %+v", app)
	}
}

// TestUnchangedRootAnswersFromTheCache pins D8 and A3: one configuration pass per
// (root, fingerprint), then the cache, whether or not a build holds the root; a build
// file edit fetches again. Seen failing when a free root always fetched.
func TestUnchangedRootAnswersFromTheCache(t *testing.T) {
	c, _, ev := newClient(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute)
	ctx := context.Background()
	a := rootDir(t)
	for range 3 {
		if _, stale, err := c.Model(ctx, a); err != nil || stale {
			t.Fatalf("model: stale=%v err=%v", stale, err)
		}
	}
	if n := count(events(t, ev), "served project.model"); n != 1 {
		t.Fatalf("three calls on an unchanged root reached the sidecar %d times, want 1", n)
	}
	if err := os.WriteFile(filepath.Join(a, "settings.gradle.kts"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Model(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := count(events(t, ev), "served project.model"); n != 2 {
		t.Fatalf("after a settings edit the sidecar served %d fetches, want 2", n)
	}
}

// TestFingerprintSeesTheUserHome pins the widened D8 (plan m3e step 2): the Gradle user
// home's gradle.properties, init.gradle(.kts) and init.d/ files are model inputs, so
// editing one changes the fingerprint; its caches and wrapper distributions are not,
// and an unset GRADLE_USER_HOME means $HOME/.gradle. Seen failing with the user home
// left out of the hash.
func TestFingerprintSeesTheUserHome(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := gradle.Fingerprint(root, home)
	step := func(what string, change func()) {
		t.Helper()
		change()
		got := gradle.Fingerprint(root, home)
		if got == base {
			t.Errorf("%s did not change the fingerprint", what)
		}
		base = got
	}
	step("adding init.d/a.gradle", func() { write("init.d/a.gradle", "// a\n") })
	step("editing init.d/a.gradle", func() { write("init.d/a.gradle", "// a, edited and longer\n") })
	step("adding gradle.properties", func() { write("gradle.properties", "org.gradle.jvmargs=-Xmx1g\n") })
	step("editing gradle.properties", func() { write("gradle.properties", "org.gradle.jvmargs=-Xmx22g\n") })
	step("adding init.gradle.kts", func() { write("init.gradle.kts", "// k\n") })
	step("adding init.gradle", func() { write("init.gradle", "// g\n") })

	write("caches/modules-2/x.bin", "cache")
	write("wrapper/dists/gradle-8.7/x.bin", "dist")
	write("daemon/8.7/daemon.log", "log")
	if got := gradle.Fingerprint(root, home); got != base {
		t.Errorf("a file under caches/, wrapper/dists/ or daemon/ changed the fingerprint")
	}

	// UserHome: GRADLE_USER_HOME wins, else $HOME/.gradle.
	if got := gradle.UserHome([]string{"GRADLE_USER_HOME=" + home, "HOME=/elsewhere"}); got != home {
		t.Errorf("GRADLE_USER_HOME set: user home %q, want %q", got, home)
	}
	fakeHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeHome, ".gradle", "init.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	uh := gradle.UserHome([]string{"HOME=" + fakeHome})
	if uh != filepath.Join(fakeHome, ".gradle") {
		t.Fatalf("GRADLE_USER_HOME unset: user home %q, want $HOME/.gradle", uh)
	}
	before := gradle.Fingerprint(root, uh)
	if err := os.WriteFile(filepath.Join(uh, "init.d", "b.gradle"), []byte("// b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gradle.Fingerprint(root, uh) == before {
		t.Error("an init.d file under $HOME/.gradle did not change the fingerprint")
	}
}

// TestUserHomeEditRefetchesTheModel pins the same decision at the client: a cached model
// is served again only while the user home is unchanged; an init.d edit makes the next
// Model reach the sidecar.
func TestUserHomeEditRefetchesTheModel(t *testing.T) {
	home := t.TempDir()
	c, _, ev := newClientEnv(t, gradletest.Config{Model: gradletest.MustModel(model(t))}, time.Minute, policy.BelowFloor{},
		append(os.Environ(), "GRADLE_USER_HOME="+home))
	ctx := context.Background()
	a := rootDir(t)
	for range 2 {
		if _, _, err := c.Model(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(events(t, ev), "served project.model"); n != 1 {
		t.Fatalf("two calls on an unchanged root reached the sidecar %d times, want 1", n)
	}
	if err := os.MkdirAll(filepath.Join(home, "init.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "init.d", "x.gradle"), []byte("// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Model(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := count(events(t, ev), "served project.model"); n != 2 {
		t.Fatalf("after an init.d edit the sidecar served %d fetches, want 2", n)
	}
}

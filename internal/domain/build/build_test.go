package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// TestMain runs the fake sidecar when this binary is re-executed in its mode, so these
// tests build over a real helper process without a JVM or Gradle (plan m1g step 7).
func TestMain(m *testing.M) {
	gradletest.Main()
	os.Exit(m.Run())
}

const testWS = "dc-0123456789ab"

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// recorder is the daemon's bus, as far as these tests need it: it keeps every
// operation.progress payload, so a test can assert what the build reported (D21).
type recorder struct {
	mu     sync.Mutex
	events []capability.Progress
}

func (r *recorder) Publish(topic, typ, workspace, operation string, payload any) {
	if topic != capability.ProgressTopic {
		return
	}
	if p, ok := payload.(capability.Progress); ok {
		r.mu.Lock()
		r.events = append(r.events, p)
		r.mu.Unlock()
	}
}

func (r *recorder) all() []capability.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capability.Progress(nil), r.events...)
}

// buildFixture is the build domain over the fake sidecar and a temp state root: the one
// place a build's capability registry, history, artifact store and client are wired as
// main.go wires them.
type buildFixture struct {
	t       *testing.T
	state   string
	root    string
	st      *store.Store
	arts    *artifact.Store
	history *Registry
	ops     *capability.Operations
	pub     *recorder
	client  *gradle.Client
}

// admitWS admits testWS and nothing else, the way the workspace registry admits an open
// one.
type admitWS struct{}

func (admitWS) Admit(target string, record func(id string)) bool {
	if target != testWS {
		return false
	}
	record(testWS)
	return true
}

func newFixture(t *testing.T, cfg gradletest.Config) *buildFixture {
	t.Helper()
	return newFixtureAt(t, t.TempDir(), cfg)
}

// newFixtureAt is newFixture over a state root the caller chose, so a test can open a
// second daemon over the same state (the restart in D13).
func newFixtureAt(t *testing.T, state string, cfg gradletest.Config) *buildFixture {
	t.Helper()
	root := t.TempDir()
	if len(cfg.Model) == 0 {
		cfg.Model = gradletest.MustModel(testModel(root))
	}
	st, err := store.New(filepath.Join(state, "state"))
	if err != nil {
		t.Fatal(err)
	}
	arts, err := artifact.New(artifact.Options{Epoch: "e1", StateRoot: st.Root()})
	if err != nil {
		t.Fatal(err)
	}
	history := NewRegistry(RegistryOptions{Store: st, Artifacts: arts, Log: testLog{t}})

	procStore, err := store.New(filepath.Join(state, "proc"))
	if err != nil {
		t.Fatal(err)
	}
	sup := process.New(process.Options{Store: procStore, Log: testLog{t}})
	if err := sup.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Shutdown)
	jvm := gradle.JVM{Found: true, Home: "/fake/jdk17", Version: "17.0.11", Source: gradle.SourceJavaHome}
	cfgPath := gradletest.WriteConfig(t, cfg)
	client := gradle.New(gradle.Options{
		Supervisor: sup, Log: testLog{t}, JVM: &jvm, CacheDir: state,
		Launch: gradletest.Launch(cfgPath), IdleTimeout: time.Minute,
	})

	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	pub := &recorder{}
	ops.UsePublisher(pub)
	ops.UseWorkspaces(admitWS{})
	Register(reg, Options{
		Client:  client,
		History: history,
		Authorize: func(context.Context, *capability.Call) (any, error) {
			return root, nil
		},
	})
	ops.RegisterCapabilities()
	return &buildFixture{t: t, state: state, root: root, st: st, arts: arts, history: history, ops: ops, pub: pub, client: client}
}

// testModel is the project model the fake serves: one :app module with the two tasks a
// variant's build needs (gradle.md §Variants).
func testModel(buildDir string) gradle.Model {
	return gradle.Model{Root: "/w", Projects: []gradle.Module{{
		Path: ":app", Name: "app", Dir: "app", BuildDir: buildDir,
		Tasks: []gradle.Task{
			{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
			{Name: "packageDebug", Path: ":app:packageDebug", Group: "build", Public: true},
			// Not a variant: AGP's resource packaging task (level 4, step 7).
			{Name: "packageDebugResources", Path: ":app:packageDebugResources"},
		},
	}}}
}

// modelWith serves testModel with a different build directory.
func modelWith(t *testing.T, cfg gradletest.Config, buildDir string) gradletest.Config {
	t.Helper()
	cfg.Model = gradletest.MustModel(testModel(buildDir))
	return cfg
}

// call runs one capability to its terminal state.
func (f *buildFixture) call(t *testing.T, name, params string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Invoke(context.Background(), capability.Request{
		Capability: name, Workspace: testWS, Params: []byte(params),
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// start starts a build and returns its operation id.
func (f *buildFixture) start(t *testing.T, name, params string) string {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: name, Workspace: testWS, Params: []byte(params),
	})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

func (f *buildFixture) wait(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustResult(t *testing.T, out capability.Outcome) Result {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation ended %s: %+v", out.State, out.Error)
	}
	r, ok := out.Result.(Result)
	if !ok {
		t.Fatalf("result is %T, want Result", out.Result)
	}
	return r
}

func codeOf(t *testing.T, err *capability.Error) capability.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	return err.Code
}

// writeAPK lays down a build directory's outputs/apk/<variant>/ as AGP leaves it: the
// output metadata and the APK files it names.
func writeAPK(t *testing.T, buildDir, variant, metadata string) {
	t.Helper()
	dir := filepath.Join(buildDir, "outputs", "apk", variant)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output-metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	var meta outputMetadata
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		t.Fatal(err)
	}
	for _, el := range meta.Elements {
		name := filepath.Base(filepath.FromSlash(el.OutputFile))
		if err := os.WriteFile(filepath.Join(dir, name), []byte("apk bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// outputMetadataJSON is the recorded S1 run-2 metadata, with applicationId and elements
// replaced when the caller names them: a test varies one field at a time, and the rest
// of the recording stays as AGP wrote it.
func outputMetadataJSON(t *testing.T, applicationID, elements string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "s1run2", "output-metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if applicationID != "" {
		doc["applicationId"] = applicationID
	}
	if elements != "" {
		var els []any
		if err := json.Unmarshal([]byte(elements), &els); err != nil {
			t.Fatal(err)
		}
		doc["elements"] = els
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// successBuild is the script of a build that ran :app:packageDebug and ended successfully.
func successBuild(counts gradle.TaskSummary) *gradletest.Build {
	return &gradletest.Build{
		Steps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:packageDebug", "BUILD SUCCESSFUL in 1s"}},
			{Kind: "progress", Counts: &counts, ActiveTask: ":app:packageDebug"},
			{Kind: "taskFinished", Path: ":app:packageDebug", Outcome: "success", DurationMs: 12},
		},
	}
}

// Decision (plan m1g step 7, D10, D13, D21): a successful build is a completed
// operation whose result is the record — the build id is the operation id, the merged
// errors are counted, the outputs come from the recorded output-metadata.json, the
// console is the durable artifact the result names, and build.lastResult reads the same
// record back. Progress reports the task running and then the tasks finished.
func TestAssembleRecordsTheBuildAndItsOutput(t *testing.T) {
	buildDir := t.TempDir()
	writeAPK(t, buildDir, "debug", outputMetadataJSON(t, "", ""))
	f := newFixture(t, modelWith(t, gradletest.Config{
		Build: successBuild(gradle.TaskSummary{Executed: 2}),
	}, buildDir))

	id := f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)
	out := f.wait(t, id)
	res := mustResult(t, out)

	if res.BuildID != id {
		t.Errorf("build id is %q, want the operation id %q (ADR-0020)", res.BuildID, id)
	}
	if res.Outcome != "success" {
		t.Errorf("outcome is %q, want success", res.Outcome)
	}
	if res.Request.Module != ":app" || res.Request.Variant != "debug" {
		t.Errorf("request is %+v, want :app/debug", res.Request)
	}
	if len(res.Outputs) != 1 {
		t.Fatalf("outputs are %+v, want one for :app debug", res.Outputs)
	}
	o := res.Outputs[0]
	if o.Module != ":app" || o.Variant != "debug" || o.ApplicationID != "com.example.dopaminecontrol" {
		t.Errorf("output is %+v, want :app/debug com.example.dopaminecontrol", o)
	}
	if len(o.APKs) != 1 || o.APKs[0].Name != "app-debug.apk" || !o.APKs[0].Supported || o.APKs[0].SizeBytes == 0 {
		t.Errorf("apks are %+v, want one supported app-debug.apk with a size", o.APKs)
	}
	if o.VersionCode == nil || *o.VersionCode != 1 || o.VersionName != "1.0" {
		t.Errorf("version is %v/%q, want 1/1.0", o.VersionCode, o.VersionName)
	}
	if res.Profile != "full" || len(res.MissingCapabilities) != 0 {
		t.Errorf("profile is %q with %v, want full with none at Gradle 8.13", res.Profile, res.MissingCapabilities)
	}

	// The console is the durable artifact the result names, and it holds the build's
	// output.
	entry, err := f.arts.Get(res.ConsoleRef)
	if err != nil {
		t.Fatalf("the console %s is not readable: %v", res.ConsoleRef, err)
	}
	body, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "BUILD SUCCESSFUL") {
		t.Errorf("console is %q, want the build's output", body)
	}

	// build.lastResult returns the same record.
	last := f.call(t, "build.lastResult", "")
	got, ok := last.Result.(Result)
	if !ok {
		t.Fatalf("lastResult is %T, want Result", last.Result)
	}
	if got.BuildID != res.BuildID || got.Outcome != res.Outcome || len(got.Outputs) != 1 || got.Outputs[0].APKs[0].Name != "app-debug.apk" {
		t.Errorf("lastResult is %+v, want the build just recorded", got)
	}

	// D21: progress named the task running, then counted the task finished.
	var sawActive, sawDone bool
	for _, p := range f.pub.all() {
		if p.Message == ":app:packageDebug" {
			sawActive = true
		}
		if p.Done == 1 && p.Message == "" {
			sawDone = true
		}
	}
	if !sawActive || !sawDone {
		t.Errorf("progress events are %+v, want the active task and then one finished task", f.pub.all())
	}
}

// Decision (plan m1g step 7, D19, gradle.md §Failure modes): a project with no wrapper
// builds on the Tooling API's own Gradle, the result says source connector, and the
// console names the missing wrapper rather than letting the substitution pass silently.
func TestNoWrapperIsReportedOnTheConsole(t *testing.T) {
	cfg := gradletest.Config{Source: "connector", Build: successBuild(gradle.TaskSummary{})}
	f := newFixture(t, cfg)
	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))
	if res.Gradle.Source != "connector" {
		t.Errorf("gradle.source is %q, want connector", res.Gradle.Source)
	}
	entry, err := f.arts.Get(res.ConsoleRef)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "no Gradle wrapper") {
		t.Errorf("console is %q, want a note naming the missing wrapper", body)
	}
}

// Decision (plan m1g step 7, gradle.md §Build outputs): an output is reported only for
// a variant whose package task ran in *this* build. Metadata left on disk by an earlier
// build is not this build's output, however present it is.
func TestOutputNeedsThePackageTaskInThisBuild(t *testing.T) {
	buildDir := t.TempDir()
	writeAPK(t, buildDir, "debug", outputMetadataJSON(t, "", ""))
	// The build ran assembleDebug but its packageDebug did not run: the metadata on
	// disk describes an earlier build (seen failing with the task condition dropped).
	f := newFixture(t, modelWith(t, gradletest.Config{Build: &gradletest.Build{
		Steps: []gradletest.Step{{Kind: "taskFinished", Path: ":app:assembleDebug", Outcome: "success", DurationMs: 1}},
	}}, buildDir))

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))
	if len(res.Outputs) != 0 {
		t.Errorf("outputs are %+v, want none: packageDebug did not run in this build", res.Outputs)
	}
}

// Decision (plan m1g step 7, gradle.md §Build outputs): packageDebug ending upToDate or
// fromCache is a build that produced the variant's output, so it is reported.
func TestUpToDatePackageTaskStillReportsTheOutput(t *testing.T) {
	for _, outcome := range []string{"upToDate", "fromCache"} {
		t.Run(outcome, func(t *testing.T) {
			buildDir := t.TempDir()
			writeAPK(t, buildDir, "debug", outputMetadataJSON(t, "", ""))
			f := newFixture(t, modelWith(t, gradletest.Config{Build: &gradletest.Build{
				Steps: []gradletest.Step{
					{Kind: "taskFinished", Path: ":app:packageDebugResources", Outcome: outcome, DurationMs: 0},
					{Kind: "taskFinished", Path: ":app:packageDebug", Outcome: outcome, DurationMs: 0},
				},
			}}, buildDir))

			res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))
			if len(res.Outputs) != 1 || len(res.Outputs[0].APKs) != 1 {
				t.Errorf("outputs are %+v, want the up-to-date variant's output and nothing for packageDebugResources", res.Outputs)
			}
		})
	}
}

// Decision (plan m1g step 7, gradle.md §Build outputs): a split APK is listed and marked
// unsupported, so device.install can refuse it without guessing.
func TestFilteredAPKsAreListedAndUnsupported(t *testing.T) {
	buildDir := t.TempDir()
	elements := `[
	  {"type":"SINGLE","filters":[],"versionCode":1,"versionName":"1.0","outputFile":"app-debug.apk"},
	  {"type":"SPLIT","filters":["en"],"versionCode":1,"versionName":"1.0","outputFile":"app-en-debug.apk"}
	]`
	writeAPK(t, buildDir, "debug", outputMetadataJSON(t, "", elements))
	f := newFixture(t, modelWith(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})}, buildDir))

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))
	if len(res.Outputs) != 1 || len(res.Outputs[0].APKs) != 2 {
		t.Fatalf("outputs are %+v, want two APKs", res.Outputs)
	}
	byName := map[string]OutputAPK{}
	for _, a := range res.Outputs[0].APKs {
		byName[a.Name] = a
	}
	if a := byName["app-debug.apk"]; !a.Supported || len(a.Filters) != 0 {
		t.Errorf("app-debug.apk is %+v, want supported with no filters", a)
	}
	if a := byName["app-en-debug.apk"]; a.Supported || len(a.Filters) != 1 || a.Filters[0] != "en" {
		t.Errorf("app-en-debug.apk is %+v, want listed, filtered and unsupported", a)
	}
}

// Decision (plan m1g step 7, gradle.md §Build outputs): metadata without an
// applicationId is reported as a reason, never guessed at.
func TestMissingApplicationIDIsAReasonNotAGuess(t *testing.T) {
	buildDir := t.TempDir()
	// The recorded metadata, with its applicationId taken out.
	withoutID := strings.Replace(outputMetadataJSON(t, "", ""), `"applicationId":"com.example.dopaminecontrol",`, "", 1)
	writeAPK(t, buildDir, "debug", withoutID)
	f := newFixture(t, modelWith(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})}, buildDir))

	res := mustResult(t, f.wait(t, f.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))
	if len(res.Outputs) != 1 {
		t.Fatalf("outputs are %+v, want one entry", res.Outputs)
	}
	o := res.Outputs[0]
	if o.ApplicationID != "" || len(o.APKs) != 0 {
		t.Errorf("output is %+v, want no applicationId and no APKs", o)
	}
	if !strings.Contains(o.Reason, "applicationId") {
		t.Errorf("reason is %q, want it to name the missing applicationId", o.Reason)
	}
}

// Decision (plan m1g step 7, ADR-0020, gradle.md §Failure modes): a failed build is a
// *completed* operation whose outcome is failed, never an operation error, and its
// merged errors are counted from the console and readable through build.errors.
func TestFailedBuildIsACompletedOperation(t *testing.T) {
	const kotlin = "e: file:///src/app/src/main/kotlin/Foo.kt:3:42 Unresolved reference: foo"
	f := newFixture(t, gradletest.Config{Build: &gradletest.Build{
		Steps: []gradletest.Step{
			{Kind: "output", Stream: "stderr", Lines: []string{kotlin}},
			{Kind: "taskFinished", Path: ":app:compileDebugKotlin", Outcome: "failed", Reason: "Compilation error."},
		},
		Outcome: "failed",
	}})
	res := mustResult(t, f.wait(t, f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)))
	if res.Outcome != "failed" {
		t.Errorf("outcome is %q, want failed", res.Outcome)
	}
	if res.ErrorCount != 1 || len(res.Errors) != 1 {
		t.Fatalf("errors are %+v (count %d), want one merged Kotlin error", res.Errors, res.ErrorCount)
	}
	if res.Errors[0].Tool != extract.ToolKotlin || !strings.Contains(res.Errors[0].Message, "Unresolved reference") {
		t.Errorf("error is %+v, want the Kotlin console form", res.Errors[0])
	}
	if len(res.FailedTasks) != 1 || res.FailedTasks[0].Reason != "Compilation error." {
		t.Errorf("failedTasks are %+v, want the task's own reason", res.FailedTasks)
	}

	// build.errors returns the same build's merged errors, by id.
	got := f.call(t, "build.errors", `{"buildId":"`+res.BuildID+`"}`)
	er, ok := got.Result.(ErrorsResult)
	if !ok {
		t.Fatalf("build.errors is %T, want ErrorsResult", got.Result)
	}
	if er.BuildID != res.BuildID || len(er.Errors) != 1 || er.Outcome != "failed" {
		t.Errorf("build.errors is %+v, want the failed build's one error", er)
	}
	if len(er.Warnings) != 0 {
		t.Errorf("warnings are %+v, want none without includeWarnings", er.Warnings)
	}
}

// Decision (plan m1g step 7, ADR-0020): operation.cancel reaches the build, which sends
// build.cancel to the sidecar, records the build cancelled and ends the operation
// cancelled. The record is what a later build.errors reads.
func TestCancellingABuildRecordsItCancelled(t *testing.T) {
	f := newFixture(t, gradletest.Config{Build: &gradletest.Build{
		WaitForCancel: true,
		Steps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:compileDebugKotlin"}},
			{Kind: "progress", Counts: &gradle.TaskSummary{}, ActiveTask: ":app:compileDebugKotlin"},
		},
	}})
	id := f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)
	waitForProgress(t, f)
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCancelled {
		t.Fatalf("operation ended %s (%+v), want cancelled", out.State, out.Error)
	}
	rec, err := f.history.newest(testWS)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "cancelled" || rec.BuildID != id {
		t.Errorf("record is %+v, want build %s cancelled", rec, id)
	}
}

func waitForProgress(t *testing.T, f *buildFixture) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.pub.all()) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the build reported no progress")
}

// Decision (plan m1g step 7, D13): the console and the record outlive the daemon. A
// second registry over the same state root reads the same record and the same console.
func TestTheRecordAndConsoleOutliveTheDaemon(t *testing.T) {
	state := t.TempDir()
	buildDir := t.TempDir()
	writeAPK(t, buildDir, "debug", outputMetadataJSON(t, "", ""))
	first := newFixtureAt(t, state, modelWith(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})}, buildDir))
	res := mustResult(t, first.wait(t, first.start(t, "build.assemble", `{"module":":app","variant":"debug"}`)))

	second := newFixtureAt(t, state, modelWith(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})}, buildDir))
	got := second.call(t, "build.lastResult", "")
	last, ok := got.Result.(Result)
	if !ok {
		t.Fatalf("lastResult is %T, want Result", got.Result)
	}
	if last.BuildID != res.BuildID || len(last.Outputs) != 1 || len(last.Outputs[0].APKs) != 1 || last.Outputs[0].APKs[0].Name != "app-debug.apk" {
		t.Errorf("lastResult is %+v, want the first daemon's record", last)
	}
	entry, err := second.arts.Get(last.ConsoleRef)
	if err != nil {
		t.Fatalf("the console is not readable in the second daemon: %v", err)
	}
	body, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "BUILD SUCCESSFUL") {
		t.Errorf("console is %q, want the first daemon's output", body)
	}
}

// Decision (plan m1g step 7, D13): the registry keeps the last 50 builds; the 51st
// drops the oldest record and the console it owned.
func TestTheFiftyFirstBuildDropsTheOldest(t *testing.T) {
	f := newFixture(t, gradletest.Config{})
	base := time.Now()
	for i := 1; i <= retainBuilds+1; i++ {
		rec := record{
			BuildID:    fmt.Sprintf("build-%03d", i),
			Outcome:    "success",
			FinishedAt: base.Add(time.Duration(i) * time.Second),
		}
		console, err := f.arts.CreateDurable(testWS, rec.BuildID, artifact.DurableOptions{MediaType: "text/plain", Cap: consoleCap})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := console.Write([]byte("output of " + rec.BuildID)); err != nil {
			t.Fatal(err)
		}
		console.Close()
		if err := f.history.persist(testWS, rec); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := f.history.entries(testWS)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != retainBuilds {
		t.Errorf("the index holds %d builds, want %d", len(entries), retainBuilds)
	}
	if entries[0].BuildID != fmt.Sprintf("build-%03d", retainBuilds+1) {
		t.Errorf("newest is %s, want the last build", entries[0].BuildID)
	}
	for _, e := range entries {
		if e.BuildID == "build-001" {
			t.Error("the 51st build kept the oldest record: retention dropped nothing")
		}
	}
	if _, err := os.Stat(filepath.Join(f.st.Root(), "ws", testWS, "builds", "build-001.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dropped record's file is still there (err %v)", err)
	}
	files, err := f.arts.ListDurable(testWS)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Owner == "build-001" {
			t.Error("the dropped build's console is still there")
		}
	}
	if last := f.mustRecord(t, fmt.Sprintf("build-%03d", retainBuilds+1)); last.Outcome != "success" {
		t.Errorf("the newest record is %+v, want it readable", last)
	}
}

func (f *buildFixture) mustRecord(t *testing.T, buildID string) record {
	t.Helper()
	rec, err := f.history.load(testWS, buildID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// Decision (plan m1g step 7, D13): a console no record owns — what a daemon that died
// mid-build leaves — is deleted when the workspace is next opened. A console a record
// owns is left alone.
func TestTheSweepDeletesAnOrphanConsole(t *testing.T) {
	f := newFixture(t, gradletest.Config{})
	for _, buildID := range []string{"build-owned", "build-inflight", "build-orphan"} {
		console, err := f.arts.CreateDurable(testWS, buildID, artifact.DurableOptions{MediaType: "text/plain", Cap: consoleCap})
		if err != nil {
			t.Fatal(err)
		}
		console.Close()
	}
	if err := f.history.persist(testWS, record{BuildID: "build-owned", Outcome: "success", FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// A record is written before the index entry that names it, so a build that
	// finished while this sweep ran has a console the index does not claim yet. The
	// record file is what the index is derived from, so it is what ownership means.
	if err := f.st.Write(recordFile(testWS, "build-inflight"), record{BuildID: "build-inflight", Outcome: "success", FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.history.Sweep(testWS)
	files, err := f.arts.ListDurable(testWS)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]bool{}
	for _, file := range files {
		owners[file.Owner] = true
	}
	if owners["build-orphan"] {
		t.Error("the sweep kept a console no record owns")
	}
	if !owners["build-owned"] {
		t.Error("the sweep deleted a console its record owns")
	}
	if !owners["build-inflight"] {
		t.Error("the sweep deleted a console whose record exists but is not in the index yet")
	}
}

// Decision (plan m1g step 7, gradle.md §Three sources): the Problems events are the
// only source of warnings, and build.errors keeps them out of the result unless the
// caller asks for them.
func TestWarningsAreProblemsEventsAndOptIn(t *testing.T) {
	warning := gradle.Problem{Severity: "warning", ProblemID: "deprecation", Details: "Using the legacy API."}
	f := newFixture(t, gradletest.Config{Build: &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "problem", Problem: &warning},
	}}})
	id := f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)
	f.wait(t, id)

	plain, ok := f.call(t, "build.errors", `{"buildId":"`+id+`"}`).Result.(ErrorsResult)
	if !ok {
		t.Fatal("build.errors did not return an ErrorsResult")
	}
	if len(plain.Warnings) != 0 {
		t.Errorf("warnings are %+v, want none without includeWarnings", plain.Warnings)
	}
	asked, ok := f.call(t, "build.errors", `{"buildId":"`+id+`","includeWarnings":true}`).Result.(ErrorsResult)
	if !ok {
		t.Fatal("build.errors did not return an ErrorsResult")
	}
	if len(asked.Warnings) != 1 || asked.Warnings[0].ProblemID != "deprecation" {
		t.Errorf("warnings are %+v, want the one the Problems event carried", asked.Warnings)
	}
}

// Decision (plan m1g step 7, gradle.md §Lifecycle, ADR-0020): a sidecar that dies while
// a build runs fails the operation with BUILD_SIDECAR_CRASHED and records nothing —
// the registry keeps history, and a build that never finished is not history.
func TestASidecarThatDiesMidBuildRecordsNothing(t *testing.T) {
	f := newFixture(t, gradletest.Config{Build: &gradletest.Build{
		Steps:         []gradletest.Step{{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:compileDebugKotlin"}}},
		DieAfterSteps: 1,
	}})
	out := f.wait(t, f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`))
	if out.State != capability.StateFailed {
		t.Fatalf("operation ended %s, want failed", out.State)
	}
	if code := codeOf(t, out.Error); code != capability.ErrBuildSidecarCrashed {
		t.Errorf("code is %s, want BUILD_SIDECAR_CRASHED", code)
	}
	if _, err := f.history.newest(testWS); err == nil {
		t.Error("a build that never finished was recorded")
	}
}

// Decision (plan m1g step 7, D13): retention is the last 50 builds *or* 7 days,
// whichever is stricter, so a record older than the age bound is dropped even when
// fewer than 50 builds exist.
func TestRetentionDropsBuildsOlderThanSevenDays(t *testing.T) {
	f := newFixture(t, gradletest.Config{})
	now := time.Now()
	f.history.now = func() time.Time { return now }
	if err := f.history.persist(testWS, record{BuildID: "build-old", Outcome: "success", FinishedAt: now.Add(-8 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := f.history.persist(testWS, record{BuildID: "build-new", Outcome: "success", FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	entries, err := f.history.entries(testWS)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].BuildID != "build-new" {
		t.Errorf("the index holds %+v, want only the build inside the age bound", entries)
	}
}

// Decision (plan m1g step 7, D9/R2): build.errors on a workspace that has finished no
// build is BUILD_NOT_FOUND, and so is an unknown buildId.
func TestErrorsWithNoBuildIsBuildNotFound(t *testing.T) {
	f := newFixture(t, gradletest.Config{})
	out := f.call(t, "build.errors", `{}`)
	if code := codeOf(t, out.Error); code != capability.ErrBuildNotFound {
		t.Errorf("code is %s, want BUILD_NOT_FOUND", code)
	}
	out = f.call(t, "build.errors", `{"buildId":"build-nope"}`)
	if code := codeOf(t, out.Error); code != capability.ErrBuildNotFound {
		t.Errorf("code is %s, want BUILD_NOT_FOUND for an unknown id", code)
	}
	out = f.call(t, "build.lastResult", `{}`)
	if code := codeOf(t, out.Error); code != capability.ErrBuildNotFound {
		t.Errorf("lastResult code is %s, want BUILD_NOT_FOUND", code)
	}
}

// Decision (plan m1g step 7, R6): a task that is not a task — a Gradle option such as
// -x — is REQUEST_INVALID_PARAMS before anything is started, so no build reaches the
// sidecar.
func TestAnOptionShapedTaskIsRejectedBeforeAnythingRuns(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events")
	cfg := gradletest.Config{Build: successBuild(gradle.TaskSummary{}), Events: events}
	f := newFixture(t, cfg)
	out := f.call(t, "build.run", `{"tasks":["-x","test"]}`)
	if code := codeOf(t, out.Error); code != capability.ErrInvalidParams {
		t.Fatalf("code is %s, want REQUEST_INVALID_PARAMS", code)
	}
	data, err := os.ReadFile(events)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "served build.run") {
		t.Error("the fake sidecar served build.run: the task was not rejected first")
	}
}

// Decision (plan m1g step 7, D17): the profile is degraded and missingCapabilities names
// what is missing when the connected Gradle refuses a gated call — reported by the
// connection and by the build's terminal event, deduplicated.
func TestDegradedProfileNamesTheMissingCapabilities(t *testing.T) {
	cfg := gradletest.Config{
		Capabilities: map[string]bool{"problemEvents": false},
		Build: &gradletest.Build{
			Outcome:             "failed",
			MissingCapabilities: []string{"detailedFailure"},
		},
	}
	f := newFixture(t, cfg)
	res := mustResult(t, f.wait(t, f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)))
	if res.Profile != "degraded" {
		t.Errorf("profile is %q, want degraded", res.Profile)
	}
	want := map[string]bool{"problemEvents": true, "detailedFailure": true}
	got := map[string]bool{}
	for _, name := range res.MissingCapabilities {
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missingCapabilities are %v, want them to name %s", res.MissingCapabilities, name)
		}
	}
	if len(res.MissingCapabilities) != len(want) {
		t.Errorf("missingCapabilities are %v, want %d names", res.MissingCapabilities, len(want))
	}
}

// Decision (plan m1g step 7, D9): build.assemble validates module and variant against
// the model before anything runs, and lists the candidates when it refuses.
func TestAssembleRejectsAnUnknownVariant(t *testing.T) {
	buildDir := t.TempDir()
	f := newFixture(t, modelWith(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})}, buildDir))
	out := f.call(t, "build.assemble", `{"module":":app","variant":"release"}`)
	if code := codeOf(t, out.Error); code != capability.ErrInvalidParams {
		t.Fatalf("code is %s, want REQUEST_INVALID_PARAMS", code)
	}
	variants, ok := out.Error.Details["variants"].([]string)
	if !ok || len(variants) != 1 || variants[0] != "debug" {
		t.Errorf("details are %+v, want the candidates [debug]", out.Error.Details)
	}
	out = f.call(t, "build.assemble", `{"module":":nope","variant":"debug"}`)
	if code := codeOf(t, out.Error); code != capability.ErrInvalidParams {
		t.Fatalf("code is %s, want REQUEST_INVALID_PARAMS for an unknown module", code)
	}
}

package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
)

// syncBuffer is a log the release goroutine writes while the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness is the shipped workspace registry and operation store, in process, with
// test-only workspace-scoped fixtures, which name their project as every project capability
// does (`project`, ADR-0137 §3). Every fixture returns nil, never an error, so an operation
// that ends cancelled was cancelled by the store, not by its handler:
//
//	fixture.honours  sends its operation id on started, then returns nil once its
//	                 context is cancelled
//	fixture.ignores  sends its operation id on started, then returns nil once release
//	                 is closed, whatever its context
//	fixture.scoped   returns nil at once, recording the Call.Workspace it saw
type harness struct {
	reg     *capability.Registry
	ws      *Registry
	ops     *capability.Operations
	st      *store.Store
	log     *syncBuffer
	started chan string
	release chan struct{}
	ran     atomic.Int32
	saw     atomic.Value // capability.Call: what the last fixture.scoped ran with
	// stopped is when the stop-sidecars hook last ran: the end of a release's work.
	stopped atomic.Value // time.Time
	events  *recorder
}

// recorder is a capability.Publisher that keeps what it is given.
type recorder struct {
	mu     sync.Mutex
	events []published
}

type published struct {
	topic, typ, workspace, operation string
	payload                          any
}

func (r *recorder) Publish(topic, typ, workspace, operation string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, published{topic, typ, workspace, operation, payload})
}

// take returns what was published since the last take.
func (r *recorder) take() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func newHarness(t *testing.T, drain time.Duration) *harness {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newHarnessOn(t, st, drain)
}

func newHarnessOn(t *testing.T, st *store.Store, drain time.Duration) *harness {
	t.Helper()
	h := &harness{st: st, log: &syncBuffer{}, started: make(chan string, 8), release: make(chan struct{}), events: &recorder{}}
	reg := capability.NewRegistry()
	h.reg = reg
	h.ops = capability.NewOperations(reg)
	h.ops.RegisterCapabilities()
	h.ws = New(Options{Store: st, Ops: h.ops, Drain: drain, Log: h.log, Publisher: h.events,
		StopSidecars: func(string) { h.stopped.Store(time.Now()) }})
	type none struct{}
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.honours", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs until cancelled.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.started <- call.OperationID
			<-ctx.Done()
			return none{}, nil
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.ignores", Kind: capability.Command, Scope: capability.Workspace,
		Description: "Runs until released.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.started <- call.OperationID
			<-h.release
			return none{}, nil
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.scoped", Kind: capability.Query, Scope: capability.Workspace,
		Description: "Reads a workspace.", Params: none{}, Result: none{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.ran.Add(1)
			h.saw.Store(*call)
			return none{}, nil
		},
	})
	t.Cleanup(func() {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.ops.Shutdown(ctx)
	})
	return h
}

// projectParams is the parameter object of a call that names project.
func projectParams(project string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{capability.ProjectParam: project})
	if err != nil {
		panic(err)
	}
	return raw
}

// sawWorkspace is the workspace id the last fixture.scoped ran with.
func (h *harness) sawWorkspace() string {
	c, _ := h.saw.Load().(capability.Call)
	return c.Workspace
}

func (h *harness) invoke(t *testing.T, capName, target string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Invoke(context.Background(), capability.Request{Capability: capName, Params: projectParams(target)})
	if err != nil {
		t.Fatalf("%s %s: rejected at Start: %v", capName, target, err)
	}
	return out
}

// open makes a call that names root, which opens it, and returns the workspace as the
// registry holds it.
func (h *harness) open(t *testing.T, root string) Info {
	t.Helper()
	out := h.invoke(t, "fixture.scoped", root)
	if out.State != capability.StateCompleted {
		t.Fatalf("a call on %s: %s %v", root, out.State, out.Error)
	}
	return h.infoOf(t, root)
}

// infoOf is the registry's workspace for the project root, as it stands.
func (h *harness) infoOf(t *testing.T, root string) Info {
	t.Helper()
	id, _ := platform.Current().WorkspaceID(root)
	w := h.ws.lookup(id)
	if w == nil {
		t.Fatalf("the registry holds no workspace for %s", root)
	}
	h.ws.mu.Lock()
	defer h.ws.mu.Unlock()
	return w.info
}

// closeWS releases root as the idle sweep does once it has been idle for longer than
// IdleAfter, by moving the registry's clock past it for that one round.
func (h *harness) closeWS(t *testing.T, root string) {
	t.Helper()
	id, _ := platform.Current().WorkspaceID(root)
	w := h.ws.lookup(id)
	if w == nil {
		t.Fatalf("the registry holds no workspace for %s", root)
	}
	h.ws.mu.Lock()
	real := h.ws.now
	h.ws.now = func() time.Time { return real().Add(2 * IdleAfter) }
	h.ws.mu.Unlock()
	h.ws.releaseOne(w)
	h.ws.mu.Lock()
	h.ws.now = real
	h.ws.mu.Unlock()
	if s := h.ws.State(root); s != Closed {
		t.Fatalf("releasing %s left it %s", root, s)
	}
}

// start starts a fixture in the workspace and returns its operation id once its
// handler runs.
func (h *harness) start(t *testing.T, fixture, target string) string {
	t.Helper()
	id, err := h.ops.Start(context.Background(), capability.Request{Capability: fixture, Params: projectParams(target)})
	if err != nil {
		t.Fatalf("%s in %s: %v", fixture, target, err)
	}
	select {
	case got := <-h.started:
		if got != id {
			t.Fatalf("started %s, want %s", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not start", fixture)
	}
	return id
}

func (h *harness) snapshot(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) waitEnd(t *testing.T, id string) capability.Outcome {
	t.Helper()
	out, err := h.ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil || !out.State.Terminal() {
		t.Fatalf("operation %s did not end within 5 s: %+v, %v", id, out, err)
	}
	return out
}

func codeOf(err error) capability.ErrorCode {
	if e, ok := err.(*capability.Error); ok {
		return e.Code
	}
	return ""
}

// project is a temporary project directory, by its canonical root.
func project(t *testing.T) string {
	t.Helper()
	return platform.Current().Canonical(t.TempDir())
}

// gradleProject is project with a settings.gradle: a call naming it is admitted (Enter
// resolves a project by its Gradle build).
func gradleProject(t *testing.T) string {
	t.Helper()
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func schemaVersionOf(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.SchemaVersion
}

// TestEnterRegistersARoot: the first call that names a project opens it: ready, named by
// the id rule, with workspace.json and the index written at schemaVersion 1; a second call
// changes nothing; a release (the idle one) closes it and the next call reopens it with the
// same createdAt (ADR-0021 §Layout; workspace-state.md §Identity and reuse). The handler runs
// with the workspace's id, its project root and its Gradle root.
func TestEnterRegistersARoot(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := gradleProject(t)
	info := h.open(t, root)
	wantID, _ := platform.Current().WorkspaceID(root)
	if info.ID != wantID || info.Root != root || info.Name != filepath.Base(root) || info.State != Ready || info.CreatedAt == nil || info.CreatedAt.IsZero() || info.DegradedBy != nil {
		t.Fatalf("the workspace after a call = %+v; want ready, id %s", info, wantID)
	}
	if saw, _ := h.saw.Load().(capability.Call); saw.Workspace != wantID || saw.Root != root || saw.GradleRoot != root {
		t.Errorf("the handler ran with id %q, root %q, Gradle root %q; want %s, %s, %s", saw.Workspace, saw.Root, saw.GradleRoot, wantID, root, root)
	}
	for _, f := range []string{filepath.Join(h.st.Root(), "ws", wantID, "workspace.json"), filepath.Join(h.st.Root(), "workspaces.json")} {
		if v := schemaVersionOf(t, f); v != 1 {
			t.Errorf("%s: schemaVersion %d", f, v)
		}
	}
	if again := h.open(t, root); !reflect.DeepEqual(again, info) {
		t.Errorf("a second call left %+v; want the same state %+v", again, info)
	}
	h.closeWS(t, root)
	if stopped, _ := h.stopped.Load().(time.Time); stopped.IsZero() {
		t.Error("a release did not stop the workspace's sidecars")
	}
	if reopened := h.open(t, root); reopened.CreatedAt == nil || !reopened.CreatedAt.Equal(*info.CreatedAt) {
		t.Errorf("reopening changed createdAt: %v → %v", info.CreatedAt, reopened.CreatedAt)
	}
}

func mustStore(t *testing.T, root string) *store.Store {
	t.Helper()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestEnterRunsNoProjectCode: a root whose gradlew would leave a marker is opened and
// the marker never appears (ADR-0015 §5; workspace-state.md §Lifecycle); run
// directly, the same script does leave it, so the check can see project code run.
func TestEnterRunsNoProjectCode(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := gradleProject(t)
	marker := filepath.Join(root, "ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	for _, f := range []string{"gradlew", "settings.gradle.kts"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.open(t, root)
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("opening ran project code: %v", err)
	}
	if out, err := exec.Command(filepath.Join(root, "gradlew")).CombinedOutput(); err != nil {
		t.Fatalf("running gradlew directly: %v: %s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gradlew run directly left no marker, so the check above proves nothing: %v", err)
	}
}

// TestNewerWorkspaceFileDegrades: a workspace.json of schemaVersion 2 is never
// overwritten; the workspace opens degraded, and carries WORKSPACE_STATE_NEWER in
// degradedBy; a call on a degraded workspace is still admitted (ADR-0026; ADR-0021 §Format).
func TestNewerWorkspaceFileDegrades(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	root := gradleProject(t)
	id, _ := platform.Current().WorkspaceID(root)
	file := filepath.Join(h.st.Root(), "ws", id, "workspace.json")
	os.MkdirAll(filepath.Dir(file), 0o700)
	newer := []byte(`{"schemaVersion":2,"root":"` + root + `","future":1}`)
	os.WriteFile(file, newer, 0o600)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(file, old, old)

	wantNext := "upgrade droidyard, or delete `" + filepath.Join(h.st.Root(), "ws", id) + "/` to reset this workspace"
	info := h.open(t, root)
	if info.State != Degraded || info.DegradedBy == nil || info.DegradedBy.Code != ErrStateNewer || info.DegradedBy.NextStep != wantNext {
		t.Errorf("the workspace of a newer file: %+v", info)
	}
	// The unread file's creation time is not known: no createdAt, rather than a zero time
	// that reads as a date.
	if raw, _ := json.Marshal(info); info.CreatedAt != nil || strings.Contains(string(raw), "createdAt") {
		t.Errorf("a degraded workspace carries createdAt: %s", raw)
	}
	raw, _ := os.ReadFile(file)
	fi, _ := os.Stat(file)
	if !bytes.Equal(raw, newer) || !fi.ModTime().Equal(old) {
		t.Errorf("the newer workspace.json changed: %q, mtime %v", raw, fi.ModTime())
	}
}

// TestProjectIsRequiredAndAbsolute holds ADR-0137 §3: a project capability's call names its
// project, by an absolute path to an existing folder, and one that does not is refused at
// Start, before anything is opened or recorded, in words that say what to pass. A call with
// no project, a relative path, `~/x`, a folder that does not exist and a file are each
// REQUEST_INVALID_PARAMS naming `project`; a folder with two builds below it and none above
// is PROJECT_NO_GRADLE_ROOT with the candidates and the nextStep that tells the caller to
// pass one of them; the home directory and `/` are a project only by a settings file of their
// own (ADR-0075 §2). No operation is recorded, no handler runs, nothing is registered.
func TestProjectIsRequiredAndAbsolute(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dir := project(t)
	file := filepath.Join(dir, "settings.gradle.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	two := filepath.Join(dir, "two")
	for _, b := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(two, b), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(two, b, "settings.gradle"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	home := project(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "only"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "only", "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	const pickStep = "pass the absolute path of one of the candidates (details.candidates, relative to the project) as the project"
	const folderStep = "pass the absolute path of the project's folder"
	type projectCase struct {
		name   string
		params json.RawMessage
		code   capability.ErrorCode
		step   string // the nextStep begins with this
		msg    string // the message contains this
	}
	const takes = "takes `project`"
	cases := []projectCase{
		{"no parameters at all", nil, capability.ErrInvalidParams, folderStep, takes},
		{"parameters with no project", json.RawMessage(`{}`), capability.ErrInvalidParams, folderStep, takes},
		{"an empty project", projectParams(""), capability.ErrInvalidParams, folderStep, takes},
		{"a project that is not a string", json.RawMessage(`{"project":["/a"]}`), capability.ErrInvalidParams, folderStep, takes},
		{"a relative path", projectParams("app/src"), capability.ErrInvalidParams, folderStep, ""},
		// Folders that exist from this test's own directory, so only the absolute-path rule
		// refuses them: resolved against the working directory they would be a project, or a
		// PROJECT_NO_GRADLE_ROOT, never this refusal.
		{"a relative path that exists", projectParams("."), capability.ErrInvalidParams, folderStep, ""},
		{"a relative path to a parent that exists", projectParams(".."), capability.ErrInvalidParams, folderStep, ""},
		{"a path from the home directory", projectParams("~/x"), capability.ErrInvalidParams, folderStep, ""},
		{"a folder that does not exist", projectParams(filepath.Join(dir, "missing")), capability.ErrInvalidParams, folderStep, ""},
		{"a file", projectParams(file), capability.ErrInvalidParams, folderStep, ""},
		{"a folder with two builds below it", projectParams(two), capability.ErrProjectNoGradleRoot, pickStep, ""},
		{"the home directory, which holds no settings file", projectParams(home), capability.ErrProjectNoGradleRoot, "", ""},
		{"the filesystem root, which holds none either", projectParams("/"), capability.ErrProjectNoGradleRoot, "", ""},
	}
	if os.Getuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
		cases = append(cases, projectCase{"a folder that cannot be listed", projectParams(locked), capability.ErrInvalidParams, folderStep, ""})
	}
	for _, c := range cases {
		id, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: c.params})
		e, _ := err.(*capability.Error)
		if id != "" || e == nil || e.Code != c.code || !strings.HasPrefix(e.NextStep, c.step) || e.NextStep == "" {
			t.Errorf("%s: Start = %q, %v; want %s with a nextStep beginning %q", c.name, id, err, c.code, c.step)
			continue
		}
		if c.code == capability.ErrInvalidParams && !strings.Contains(e.Message, "project") {
			t.Errorf("%s: the message %q does not name the parameter", c.name, e.Message)
		}
		if !strings.Contains(e.Message, c.msg) {
			t.Errorf("%s: the message %q does not say %q", c.name, e.Message, c.msg)
		}
	}
	// Two builds below and none above: the candidates are the two folders.
	_, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(two)})
	if cand, _ := err.(*capability.Error).Details["candidates"].([]string); !reflect.DeepEqual(cand, []string{"a", "b"}) {
		t.Errorf("candidates = %v; want [a b]", cand)
	}
	h.ws.allMu.RLock()
	entries := len(h.ws.all)
	h.ws.allMu.RUnlock()
	if h.ran.Load() != 0 || h.ops.Running() != 0 || len(h.ops.Active()) != 0 || entries != 0 {
		t.Fatalf("refused calls ran %d handlers, left %d operations and %d registered workspaces; want none", h.ran.Load(), h.ops.Running(), entries)
	}

	// The home directory with a settings file of its own is a project, and so is a folder
	// inside it that holds none of its own: the nearest ancestor decides.
	if err := os.WriteFile(filepath.Join(home, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := h.invoke(t, "fixture.scoped", home); out.State != capability.StateCompleted {
		t.Errorf("the home directory with a settings file: %+v", out)
	}
}

// TestStartOpensTheRootItNames: a workspace-scoped call on a root that is not open opens
// it at Start and runs with the canonical id, whatever spelling named it (ADR-0137 §3). A
// project that cannot be opened is refused at Start with the code that says why, no
// operation recorded and no handler run: a path that does not exist and a relative one are
// REQUEST_INVALID_PARAMS, a folder with no Gradle build PROJECT_NO_GRADLE_ROOT.
func TestStartOpensTheRootItNames(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dir, err := os.MkdirTemp("/tmp", "wsadmit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	id, _ := platform.Current().WorkspaceID(dir)
	for target, want := range map[string]capability.ErrorCode{
		filepath.Join(dir, "missing"): capability.ErrInvalidParams,
		"relative/path":               capability.ErrInvalidParams,
		id:                            capability.ErrInvalidParams, // an id is no project: ids name no call
		dir:                           capability.ErrProjectNoGradleRoot,
	} {
		opID, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(target)})
		e, _ := err.(*capability.Error)
		if opID != "" || e == nil || e.Code != want || e.NextStep == "" {
			t.Errorf("%s: Start = %q, %v; want %s with a nextStep", target, opID, err, want)
		}
	}
	if h.ran.Load() != 0 || h.ops.Running() != 0 || h.ws.State(dir) != "" {
		t.Fatalf("a refused call ran (%d), left an operation (%d) or opened its root (%q)", h.ran.Load(), h.ops.Running(), h.ws.State(dir))
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.gradle"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "app", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := h.invoke(t, "fixture.scoped", dir); out.State != capability.StateCompleted || h.ws.State(dir) != Ready {
		t.Fatalf("a call on a root never opened: %+v, state %q; want completed and ready", out, h.ws.State(dir))
	}
	canonical := platform.Current().Canonical(dir)
	for _, spelling := range []string{dir, dir + "/", canonical, filepath.Join(dir, "app", "src"), filepath.Join(dir, "app", "..")} {
		out := h.invoke(t, "fixture.scoped", spelling)
		saw, _ := h.saw.Load().(capability.Call)
		// Trust, the id and every published path are the resolved root's, never the path passed:
		// the handler's Root is the project root, whatever folder inside it named the call.
		if out.State != capability.StateCompleted || saw.Workspace != id || saw.Root != canonical || saw.GradleRoot != canonical {
			t.Errorf("%s: %+v, the handler saw workspace %q at %q (Gradle root %q); want %s at %s", spelling, out, saw.Workspace, saw.Root, saw.GradleRoot, id, canonical)
		}
	}
}

// TestNewerIndexIsNeitherReadNorWritten: with a workspaces.json of schemaVersion 2, a call
// by root works and skips the index, the file is untouched and recorded (plan m1c, R16;
// ADR-0021 §Format).
func TestNewerIndexIsNeitherReadNorWritten(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	file := filepath.Join(h.st.Root(), "workspaces.json")
	newer := []byte(`{"schemaVersion":2,"workspaces":{"x-0123456789ab":{"root":"/nowhere"}}}`)
	os.WriteFile(file, newer, 0o600)

	root := gradleProject(t)
	if info := h.open(t, root); info.State != Ready {
		t.Errorf("a call by root: %+v", info)
	}
	if raw, _ := os.ReadFile(file); !bytes.Equal(raw, newer) {
		t.Errorf("the newer index changed: %q", raw)
	}
	found := false
	for _, p := range h.st.Problems() {
		found = found || (p.File == "workspaces.json" && p.Problem == store.ProblemNewerVersion)
	}
	if !found {
		t.Errorf("problems: %+v", h.st.Problems())
	}
	if again := h.invoke(t, "fixture.scoped", root); again.State != capability.StateCompleted {
		t.Errorf("a second call: %+v", again.Error)
	}
}

// TestProductionDrainIs10s: the bound wired when none is given is ADR-0027's.
func TestProductionDrainIs10s(t *testing.T) {
	if d := New(Options{}).Drain(); d != 10*time.Second {
		t.Errorf("drain %s", d)
	}
}

// TestUnreadableStateFailsTheCall: when droidyard's own state cannot be read or written, for
// a reason other than a corrupt file or a newer version, the call that would open the
// workspace is refused at Start with WORKSPACE_STATE_UNREADABLE, details.target the root,
// ADR-0026's nextStep, and never REQUEST_INVALID_PARAMS (ADR-0026, second amendment); no
// operation is recorded and the workspace stays closed.
func TestUnreadableStateFailsTheCall(t *testing.T) {
	cases := map[string]func(t *testing.T, state, id string){
		"workspace.json is a directory": func(t *testing.T, state, id string) {
			os.MkdirAll(filepath.Join(state, "ws", id, "workspace.json", "x"), 0o700)
		},
		"workspaces.json is a directory": func(t *testing.T, state, id string) {
			os.MkdirAll(filepath.Join(state, "workspaces.json", "x"), 0o700)
		},
		"the state directory is read-only": func(t *testing.T, state, id string) {
			if os.Getuid() == 0 {
				t.Skip("root writes through permissions")
			}
			os.Chmod(state, 0o500)
			t.Cleanup(func() { os.Chmod(state, 0o700) })
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 200*time.Millisecond)
			root := gradleProject(t)
			id, _ := platform.Current().WorkspaceID(root)
			breakIt(t, h.st.Root(), id)
			opID, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)})
			e, _ := err.(*capability.Error)
			if opID != "" || e == nil || e.Code != ErrStateUnreadable || e.Details["target"] != root ||
				e.NextStep != "check the permissions and free space of the state directory" || e.Cause == "" || h.ops.Running() != 0 {
				t.Fatalf("a call on unreadable state: %q, %v; want WORKSPACE_STATE_UNREADABLE and no operation", opID, err)
			}
			if s := h.ws.State(root); s == Ready || s == Degraded || s == Opening {
				t.Errorf("state %s after a failed open", s)
			}
		})
	}
}

// TestRefusedCallsLeaveNoEntry: a project that cannot be opened is not registered, so a
// client trying many bad ones does not grow the registry: a missing path, a file, a
// relative path and a real folder with no Gradle build, each at Start.
func TestRefusedCallsLeaveNoEntry(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	dir := project(t)
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	size := func() int {
		h.ws.allMu.RLock()
		defer h.ws.allMu.RUnlock()
		return len(h.ws.all)
	}
	unopened := project(t)
	for i := 0; i < 100; i++ {
		for target, want := range map[string]capability.ErrorCode{
			filepath.Join(dir, "missing-"+strconv.Itoa(i)): capability.ErrInvalidParams,
			file + "/" + strconv.Itoa(i):                   capability.ErrInvalidParams,
			"nothing-" + strconv.Itoa(i):                   capability.ErrInvalidParams,
			unopened:                                       capability.ErrProjectNoGradleRoot,
		} {
			id, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(target)})
			if id != "" || codeOf(err) != want {
				t.Fatalf("Start on %s: %q, %v; want %s", target, id, err, want)
			}
		}
	}
	if n := size(); n != 0 {
		t.Errorf("400 calls rejected at Start left %d registry entries; want 0", n)
	}
	h.open(t, gradleProject(t))
	if n := size(); n != 1 {
		t.Errorf("one valid call left %d entries; want 1", n)
	}
}

// TestTransitionsArePublished: every workspace transition is published on
// workspace.lifecycle with the workspace id, the new state as its type and
// {state, root, degradedBy?} as its payload; root is the canonical project root the id
// stands for, the one way a client maps an event's workspace to a project (ADR-0137 §3); a
// second call publishes nothing (workspace-state.md §Observability; plan m1d, D4).
func TestTransitionsArePublished(t *testing.T) {
	expect := func(t *testing.T, h *harness, what, id, root string, states ...State) []published {
		t.Helper()
		got := h.events.take()
		var types []string
		for _, e := range got {
			types = append(types, e.typ)
			l, ok := e.payload.(Lifecycle)
			if e.topic != LifecycleTopic || e.workspace != id || e.operation != "" || !ok || string(l.State) != e.typ || l.Root != root {
				t.Errorf("%s: published %+v; want %s for %s, type the payload's state, root %s", what, e, LifecycleTopic, id, root)
			}
		}
		var want []string
		for _, s := range states {
			want = append(want, string(s))
		}
		if !reflect.DeepEqual(types, want) {
			t.Errorf("%s published %v; want %v", what, types, want)
		}
		return got
	}

	t.Run("a first call, a second call, a release", func(t *testing.T) {
		h := newHarness(t, 200*time.Millisecond)
		root := gradleProject(t)
		id, _ := platform.Current().WorkspaceID(root)
		h.open(t, root)
		expect(t, h, "the first call", id, root, Opening, Ready)
		h.open(t, root)
		expect(t, h, "a second call", id, root)
		h.closeWS(t, root)
		expect(t, h, "a release", id, root, Closing, Closed)
	})

	t.Run("the payload carries the root", func(t *testing.T) {
		h := newHarness(t, 200*time.Millisecond)
		root := gradleProject(t)
		h.open(t, root)
		got := h.events.take()
		raw, _ := json.Marshal(got[0].payload)
		if want := `{"state":"opening","root":` + strconv.Quote(root) + `}`; string(raw) != want {
			t.Errorf("opening's payload encodes as %s; want %s", raw, want)
		}
	})

	t.Run("a newer workspace.json", func(t *testing.T) {
		h := newHarness(t, 200*time.Millisecond)
		root := gradleProject(t)
		id, _ := platform.Current().WorkspaceID(root)
		file := filepath.Join(h.st.Root(), "ws", id, "workspace.json")
		os.MkdirAll(filepath.Dir(file), 0o700)
		os.WriteFile(file, []byte(`{"schemaVersion":2}`), 0o600)
		h.open(t, root)
		got := expect(t, h, "the first call", id, root, Opening, Degraded)
		if l, _ := got[len(got)-1].payload.(Lifecycle); l.DegradedBy == nil || l.DegradedBy.Code != ErrStateNewer {
			t.Errorf("degraded carries %+v; want degradedBy WORKSPACE_STATE_NEWER", l)
		}
	})

	t.Run("an unreadable state directory", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root writes through permissions")
		}
		h := newHarness(t, 200*time.Millisecond)
		root := gradleProject(t)
		id, _ := platform.Current().WorkspaceID(root)
		os.Chmod(h.st.Root(), 0o500)
		t.Cleanup(func() { os.Chmod(h.st.Root(), 0o700) })
		if _, err := h.ops.Start(context.Background(), capability.Request{Capability: "fixture.scoped", Params: projectParams(root)}); err == nil {
			t.Fatal("a call on an unwritable state directory was admitted")
		}
		expect(t, h, "a failed open", id, root, Opening, Closed)
	})
}

// recv bounds every wait on a channel, so a reversed decision fails the test rather
// than hanging it.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing within 5 s", what)
	}
	var zero T
	return zero
}

func waitState(t *testing.T, ws *Registry, target string, want State) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ws.State(target) != want; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", ws.State(target), want)
		}
	}
}

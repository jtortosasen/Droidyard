package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
)

// TestWatchOutputNeedsASession: build.watchOutput is streaming, so through plain HTTP, a
// CLI call and the core's MCP client kind it is SESSION_REQUIRED at Start, naming GET
// /v1/ws, with no operation (ADR-0044 §4; ADR-0010). The MCP adapter lists no
// build_watchOutput tool (TestMCPTranscript counts the tools).
func TestWatchOutputNeedsASession(t *testing.T) {
	t.Parallel()
	p, _ := startHTTP(t, "127.0.0.1:0")
	port := portOf(t, p.d.HTTPAddr())
	want := func(what string, env capability.ResponseEnvelope) {
		t.Helper()
		if env.Error == nil || env.Error.Code != capability.ErrSessionRequired || env.Operation != nil || env.Result != nil ||
			!strings.Contains(env.Error.NextStep, "GET /v1/ws") {
			t.Errorf("%s: %+v (error %+v); want SESSION_REQUIRED naming GET /v1/ws, no operation, no result", what, env, env.Error)
		}
	}

	resp := callRequest(port, p.token, `{"id":"w","capability":"build.watchOutput"}`).send(t, p.d.HTTPAddr())
	if resp.StatusCode != 200 {
		t.Errorf("POST /v1/call build.watchOutput: %d; want 200 with an envelope", resp.StatusCode)
	}
	want("POST /v1/call build.watchOutput", envelopeOf(t, resp))

	env, code := p.call(t, "build.watchOutput")
	if code == 0 {
		t.Error("droidyard call build.watchOutput exited 0")
	}
	want("droidyard call build.watchOutput", env)

	out, err := p.ops.Invoke(context.Background(), capability.Request{Capability: "build.watchOutput", Client: capability.ClientMCP})
	var e *capability.Error
	if !errors.As(err, &e) || e.Code != capability.ErrSessionRequired || out.OperationID != "" {
		t.Errorf("build.watchOutput as client mcp: (%+v, %v); want SESSION_REQUIRED", out, err)
	}
	if n := p.bus.Subscriptions(); n != 0 {
		t.Errorf("the rejected calls left %d subscriptions on the bus", n)
	}
}

// TestWSWatchOutputSeesAnMCPBuild is the promise of ADR-0044: a WebSocket session that
// watches a workspace is sent, line by line, the console of a build another client makes
// through the shared daemon. An MCP session calls build_assemble; the WebSocket receives
// lines events naming that build's id (the operation id of the MCP call) and, joined,
// they are the console artifact byte for byte. The daemon is the shipped wiring over a
// fake sidecar, so the publisher is the one main.go gave the build domain.
func TestWSWatchOutputSeesAnMCPBuild(t *testing.T) {
	t.Parallel()
	buildDir := t.TempDir()
	cfg := gradletest.WriteConfig(t, gradletest.Config{
		Model: gradletest.MustModel(gradle.Model{Projects: []gradle.Module{
			{Path: ":", Name: "root", Dir: "."},
			{Path: ":app", Name: "app", Dir: "app", BuildDir: buildDir, Tasks: []gradle.Task{
				{Name: "assembleDebug", Path: ":app:assembleDebug", Group: "build", Public: true},
				{Name: "packageDebug", Path: ":app:packageDebug", Group: "build", Public: true},
			}},
		}}),
		Build: &gradletest.Build{Steps: []gradletest.Step{
			{Kind: "output", Stream: "stdout", Lines: []string{"> Task :app:preBuild UP-TO-DATE", "> Task :app:compileDebugKotlin"}},
			{Kind: "output", Stream: "stderr", Lines: []string{"w: warning ✓ héllo"}},
			{Kind: "output", Stream: "stdout", Lines: []string{"BUILD SUCCESSFUL in 1s"}},
		}},
	})
	jdk := fakeJDK(t)
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{
		env: []string{"HOME=/nonexistent-droidyard-test", "PATH="},
		gradle: &gradleSeam{
			launch: gradletest.Launch(cfg),
			finder: func([]string) *gradle.Finder {
				return &gradle.Finder{GOOS: "linux", Env: []string{"JAVA_HOME=" + jdk}, StudioHomes: func() []string { return nil }}
			},
		},
	})

	root, _ := gradleProject(t)
	if code := droidyard(t, p.state, "trust", root).wait(t); code != 0 {
		t.Fatalf("droidyard trust exited %d", code)
	}
	w := wsConnect(t, p)
	w.send("open", "workspace.open", root, "{}", nil)
	if _, f := w.collect("open"); f.Error != nil {
		t.Fatalf("workspace.open: %+v", f.Error)
	}
	w.send("wo", "build.watchOutput", root, `{}`, nil)
	_, f := w.collect("wo")
	if f.Error != nil {
		t.Fatalf("build.watchOutput: %+v", f.Error)
	}
	sub := subscriptionOf(t, f)
	var watch build.WatchOutputResult
	if err := json.Unmarshal(f.Result, &watch); err != nil || len(watch.Builds) != 0 {
		t.Fatalf("the watch result %s: %v; want no build running", f.Result, err)
	}

	c := startShimIn(t, p.state, root, "mcp-client", []string{"mcp", "--no-autostart"})
	c.initialize("watch-output-test")
	c.callTool(1, "build_assemble", `{"module":":app","variant":"debug"}`)
	built := decodeTool(t, c.recv(), 1)
	if built.isError || built.view.State != capability.StateCompleted {
		t.Fatalf("build_assemble answered %v (%s); want a completed build", built.raw, built.text)
	}
	buildID := built.view.OperationID

	// The lines events were all published before the operation ended; read until the
	// socket is quiet.
	var lines []build.LinesEvent
	var ref string
	for {
		fr, ok := w.next(500 * time.Millisecond)
		if !ok {
			break
		}
		if fr.Event == nil || fr.SubscriptionID != sub {
			t.Fatalf("a frame that is not an event of %s: %+v", sub, fr)
		}
		if fr.Event.Topic != build.TopicOutput || fr.Event.Type != build.EventLines || fr.Event.Operation != buildID {
			t.Fatalf("the watch received %s/%s for %q; want build.output/lines for %s", fr.Event.Topic, fr.Event.Type, fr.Event.Operation, buildID)
		}
		var le build.LinesEvent
		if err := json.Unmarshal(fr.Event.Payload, &le); err != nil {
			t.Fatal(err)
		}
		if le.BuildID != buildID {
			t.Fatalf("a lines event for build %q; want the MCP call's %s", le.BuildID, buildID)
		}
		lines = append(lines, le)
		ref = le.ConsoleRef
	}
	if len(lines) != 3 {
		t.Fatalf("the watch received %d lines events, want the build's 3 batches: %+v", len(lines), lines)
	}
	entry, err := p.store.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, le := range lines {
		if int64(got.Len()) != le.Offset {
			t.Errorf("an event at offset %d where the console so far ends at %d", le.Offset, got.Len())
		}
		got.WriteString(strings.Join(le.Lines, "\n") + "\n")
	}
	if got.String() != string(file) {
		t.Errorf("the events joined are %q; the console is %q", got.String(), file)
	}
	if lines[1].Stream != "stderr" || lines[0].Stream != "stdout" {
		t.Errorf("the streams are %s, %s; want stdout, stderr", lines[0].Stream, lines[1].Stream)
	}
}

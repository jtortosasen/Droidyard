package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/workspace"
)

// frameOfType is the next event of sub on workspace.state, skipping other frames.
func frameOfType(t *testing.T, w *wsClient, sub, typ string) wsFrame {
	t.Helper()
	for {
		f := w.must("a workspace.state " + typ + " event")
		if f.Event == nil || f.SubscriptionID != sub {
			continue
		}
		if f.Event.Topic != workspace.StateTopic {
			t.Fatalf("an event on %s; want workspace.state", f.Event.Topic)
		}
		if f.Event.Type == typ {
			return f
		}
	}
}

// TestUIReusesTheOpenTab is ADR-0084 end to end, on a real `serve` and re-executed `ui`s
// whose browser launcher writes a file. With no WebSocket holding workspace.watch the
// launcher is called once, as before. With one (a tab), `ui` in a project, any project,
// opens nothing, says it switched the tab, and the tab's subscription receives a show
// event for that project's id with an empty payload: one tab in all, the owner's rule. Closing
// the WebSocket brings the browser back.
//
// Seen failing: with the workspace.show call removed from `ui`, the second launch called
// the launcher (and the tab got no show); with the count not returned by the bus, tabs
// stayed 0 and the same.
func TestUIReusesTheOpenTab(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	srv, _ := serveOn(t, state)
	rootA, subA := gradleProject(t)
	rootB, subB := gradleProject(t)
	idB := workspaceIDOf(t, rootB)

	// No tab: the browser opens.
	url, _ := launched(t, state, subA, "ui")
	if url == "" {
		t.Fatalf("with no tab the launcher got no URL")
	}

	// A tab: it is shown project B, opened by this very `ui`, and no browser opens.
	w := wsToDaemon(t, state)
	w.send("w", "workspace.watch", "", "", nil)
	_, f := w.collect("w")
	sub := subscriptionOf(t, f)
	file := filepath.Join(t.TempDir(), "url")
	ui := droidyardEnv(t, state, subB, []string{launcherEnv + "=" + file}, "ui")
	if code := ui.wait(t); code != 0 {
		t.Fatalf("ui exited %d; stderr: %s", code, ui.stderr.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("ui called the browser launcher with a tab open: %v", err)
	}
	if want := "switched the open Droidyard tab to " + filepath.Base(rootB); !strings.Contains(ui.stdout.String(), want) {
		t.Errorf("ui printed %q; want it to contain %q", ui.stdout.String(), want)
	}
	ev := frameOfType(t, w, sub, workspace.EventShow).Event
	if ev.Workspace == nil || *ev.Workspace != idB || string(ev.Payload) != "{}" {
		t.Errorf("the show event is workspace %v payload %s; want %s and {}", ev.Workspace, ev.Payload, idB)
	}

	// The tab is shown a second project the same way: one tab for every project.
	idA := workspaceIDOf(t, rootA)
	ui = droidyardEnv(t, state, subA, []string{launcherEnv + "=" + file}, "ui")
	if code := ui.wait(t); code != 0 {
		t.Fatalf("ui in the first project exited %d; stderr: %s", code, ui.stderr.String())
	}
	if ev := frameOfType(t, w, sub, workspace.EventShow).Event; ev.Workspace == nil || *ev.Workspace != idA {
		t.Errorf("the second show event is for %v; want %s", ev.Workspace, idA)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("ui called the browser launcher with a tab open: %v", err)
	}

	// The tab goes away: the next `ui` opens a browser again.
	w.c.CloseNow()
	<-w.done
	reopened := false
	for i := 0; i < 200 && !reopened; i++ {
		ui = droidyardEnv(t, state, subB, []string{launcherEnv + "=" + file}, "ui")
		ui.wait(t)
		_, err := os.Stat(file)
		reopened = err == nil
	}
	if !reopened {
		t.Errorf("after the tab closed, ui never opened a browser")
	}
	stopped(t, state)
	srv.wait(t)
}

// workspaceIDOf is the id the daemon gives root.
func workspaceIDOf(t *testing.T, root string) string {
	t.Helper()
	id, _ := platform.Current().WorkspaceID(root)
	return id
}

// TestWorkspaceForget is ADR-0086 end to end, on an in-process daemon with real CLI
// calls and a WebSocket. An open workspace is refused with WORKSPACE_OPEN and stays
// listed; closed, it leaves workspace.watch and workspaces.json, the watcher receives
// forgotten for its id, and ws/<wsid>/ stays; an id nothing knows answers as done;
// opening the root again lists it with the same createdAt, so its history is the one it
// had. Over MCP the capability is REQUEST_NOT_ALLOWED_HERE.
//
// Seen failing: with the open check removed the first forget succeeded; with the index
// not written the entry stayed in workspaces.json.
func TestWorkspaceForget(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root, _ := gradleProject(t)
	openEnv, code := callIn(t, p.state, "", "workspace.open", "--workspace", root)
	if code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, openEnv.Error)
	}
	opened := workspaceOf(t, openEnv)
	id := opened.ID
	marker := filepath.Join(p.state, "ws", id, "marker")
	if err := os.WriteFile(marker, []byte("history"), 0o600); err != nil {
		t.Fatal(err)
	}
	forget := func(target string) (capability.ResponseEnvelope, int) {
		return callIn(t, p.state, "", "workspace.forget", "--params", `{"workspace":"`+target+`"}`)
	}
	listed := func(id string) bool {
		for _, r := range watchRows(t, p.state) {
			if r.ID == id {
				return true
			}
		}
		return false
	}
	indexHolds := func(id string) bool {
		raw, err := os.ReadFile(filepath.Join(p.state, "workspaces.json"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(raw), `"`+id+`"`)
	}

	w := wsConnect(t, p)
	w.send("w", "workspace.watch", "", "", nil)
	_, f := w.collect("w")
	sub := subscriptionOf(t, f)

	// Open: refused, nothing removed.
	env, code := forget(id)
	if code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceOpen {
		t.Fatalf("forget of an open workspace = (%+v, exit %d); want WORKSPACE_OPEN", env.Error, code)
	}
	if !listed(id) || !indexHolds(id) {
		t.Errorf("a refused forget removed the entry (listed %v, in the index %v)", listed(id), indexHolds(id))
	}

	// Closed: removed, history kept, the watcher told.
	if _, code := callIn(t, p.state, "", "workspace.close", "--workspace", id); code != 0 {
		t.Fatalf("workspace.close exited %d", code)
	}
	if env, code := forget(id); code != 0 || env.Error != nil {
		t.Fatalf("forget of a closed workspace = (%+v, exit %d); want done", env.Error, code)
	}
	if listed(id) || indexHolds(id) {
		t.Errorf("after forget the entry is still there (listed %v, in the index %v)", listed(id), indexHolds(id))
	}
	if ev := frameOfType(t, w, sub, workspace.EventForgotten).Event; ev.Workspace == nil || *ev.Workspace != id || string(ev.Payload) != "{}" {
		t.Errorf("forgotten event is workspace %v payload %s; want %s and {}", ev.Workspace, ev.Payload, id)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("ws/<wsid>/ lost its content: %v", err)
	}

	// Idempotent: forgotten again, and an id nothing knows.
	for _, target := range []string{id, "nothing-knows-this"} {
		if env, code := forget(target); code != 0 || env.Error != nil {
			t.Errorf("forget of %q = (%+v, exit %d); want done", target, env.Error, code)
		}
	}

	// Reopening the root lists it again with its own history: same id, same createdAt.
	reopenEnv, code := callIn(t, p.state, "", "workspace.open", "--workspace", root)
	if code != 0 {
		t.Fatalf("reopening exited %d: %+v", code, reopenEnv.Error)
	}
	again := workspaceOf(t, reopenEnv)
	if again.ID != id || again.CreatedAt == nil || opened.CreatedAt == nil || !again.CreatedAt.Equal(*opened.CreatedAt) {
		t.Errorf("reopened %+v; want id %s and createdAt %v", again, id, opened.CreatedAt)
	}
	if !listed(id) {
		t.Errorf("the reopened workspace is not listed")
	}

	// A person's capability: an MCP client is refused.
	for _, name := range []string{workspace.CapForget, workspace.CapShow} {
		opID, err := p.ops.Start(context.Background(), capability.Request{Capability: name, Params: json.RawMessage(`{"workspace":"` + id + `"}`), Client: capability.ClientMCP})
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || opID != "" {
			t.Errorf("%s as client mcp: (%q, %v); want REQUEST_NOT_ALLOWED_HERE", name, opID, err)
		}
	}
}

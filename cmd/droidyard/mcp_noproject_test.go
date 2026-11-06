package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// nonProject is a folder with no Gradle build in it or above it.
func nonProject(t *testing.T) string {
	t.Helper()
	return platform.Current().Canonical(t.TempDir())
}

// listedTools starts the session and returns the sorted names tools/list gives.
func listedTools(t *testing.T, s *shim) []string {
	t.Helper()
	s.initialize("anywhere")
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var names []string
	for _, tool := range response(t, s.recv(), 1)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	return names
}

// TestMCPListsAllToolsAnywhere holds ADR-0137 §2 and the plan's Q6: the server is the
// machine's, so a shim lists the same tools wherever it starts and whatever it is given:
// in a folder with no project, with --from '${workspaceFolder}' (what an old Cursor entry
// carries), and with --root naming a folder that does not exist. --root and --from are
// accepted and ignored, with one line on stderr naming them; a shim given neither writes
// nothing there.
//
// Seen failing on step 6's tree (the shim served a session with no tools wherever it found
// no project): tools/list was empty for the empty folder, for --from ${workspaceFolder}
// and for --root /nonexistent.
func TestMCPListsAllToolsAnywhere(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	cases := []struct {
		name    string
		args    []string
		ignored string // the flag the stderr line names, "" when there is no line
	}{
		{"an empty folder", []string{"mcp", "--no-autostart"}, ""},
		{"--from ${workspaceFolder}", []string{"mcp", "--no-autostart", "--from", "${workspaceFolder}"}, "--from"},
		{"--from=<folder that is no project>", []string{"mcp", "--no-autostart", "--from=" + nonProject(t)}, "--from"},
		{"--root /nonexistent", []string{"mcp", "--no-autostart", "--root", "/nonexistent"}, "--root"},
		{"--root=/nonexistent", []string{"mcp", "--no-autostart", "--root=/nonexistent"}, "--root"},
	}
	var first []string
	for _, c := range cases {
		s := startShimIn(t, p.state, nonProject(t), "anywhere", c.args)
		got := listedTools(t, s)
		if len(got) == 0 {
			t.Errorf("%s: the session lists no tools; want the daemon's", c.name)
		}
		if first == nil {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Errorf("%s: lists %v; want the list every shim gets, %v", c.name, got, first)
		}
		if code := s.exit(); code != 0 {
			t.Errorf("%s: the shim exited %d at EOF; want 0", c.name, code)
		}
		lines := strings.Split(strings.TrimSpace(s.stderr.String()), "\n")
		switch {
		case c.ignored == "" && s.stderr.Len() != 0:
			t.Errorf("%s: stderr = %q; want nothing", c.name, s.stderr.String())
		case c.ignored != "" && (len(lines) != 1 || !strings.Contains(lines[0], c.ignored+" ignored") || !strings.Contains(lines[0], "refuse")):
			t.Errorf("%s: stderr = %q; want one line saying %s is ignored and a later version refuses it", c.name, s.stderr.String(), c.ignored)
		}
	}
}

// TestMCPOutsideAProjectDials holds that the shim no longer decides it has no project: it
// dials, wherever it started. A daemon that listens counts every connection.
//
// Seen failing on step 6's tree: the fake daemon counted no connection (the shim served
// the empty session itself).
func TestMCPOutsideAProjectDials(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	fake := startFakeDaemon(t, state, false)
	s := startShimIn(t, state, nonProject(t), "dials", []string{"mcp", "--no-autostart"})
	deadline := time.Now().Add(testtime.Bound(processGuard))
	for fake.dials.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := fake.dials.Load(); n != 1 {
		t.Errorf("the daemon was dialled %d time(s); want 1", n)
	}
	s.stdin.Close()
	s.cmd.Process.Kill()
}

// TestMCPServerDiscoverFallsBack holds the handshake Claude Code 2.1.308 opens with: it
// sends server/discover first and falls back to initialize when that is a JSON-RPC error
// (METHOD_NOT_FOUND, -32601). The daemon's session answers a server/discover that does not
// name the modern protocol with that error, and initialize works after it.
func TestMCPServerDiscoverFallsBack(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	s := startShimIn(t, p.state, nonProject(t), "discover", []string{"mcp", "--no-autostart"})
	s.send(`{"jsonrpc":"2.0","id":7,"method":"server/discover"}`)
	var msg struct {
		ID    *int `json:"id"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	line := s.recv()
	if err := json.Unmarshal(line, &msg); err != nil || msg.ID == nil || *msg.ID != 7 {
		t.Fatalf("server/discover answered %s; want an answer to id 7", line)
	}
	if msg.Error == nil || msg.Error.Code != -32601 {
		t.Errorf("server/discover answered %s; want a JSON-RPC error with METHOD_NOT_FOUND (-32601), the code Claude Code falls back to initialize on", line)
	}
	if len(listedTools(t, s)) == 0 {
		t.Error("the session lists no tools after the fallback")
	}
	s.exit()
}

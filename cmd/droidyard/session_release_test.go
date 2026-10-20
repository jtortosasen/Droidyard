package main

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// releaseSerial is the one emulator the fake adb lists.
const releaseSerial = "emulator-5554"

// uninstallArgs is the acting call the test uses to hold a device through the fake adb.
const uninstallArgs = `{"device":"` + releaseSerial + `","applicationId":"com.example.app"}`

// releaseWorld is an occupationWorld whose daemon runs the fake adb with one online
// emulator, so that an MCP session can hold a device without a real one.
func releaseWorld(t *testing.T) *occupationWorld {
	t.Helper()
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"+releaseSerial+" device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 device:emu64a16k transport_id:1\n"))
	devicetest.Write(t, dir, releaseSerial+".bootcompleted.out", []byte("1\n"))
	devicetest.Write(t, dir, releaseSerial+".uninstall.out", []byte("Success\n"))
	return newOccupationWorld(t, adbFakeEnv+"="+dir, adbPortEnv+"="+unusedPort(t), adbFloorVar())
}

// release calls session.release over the WebSocket, as the web UI does, and returns the
// error code ("" when it succeeded).
func release(t *testing.T, ws *wsClient, id, params string) string {
	t.Helper()
	ws.send(id, capability.SessionRelease, "", params, nil)
	_, f := ws.collect(id)
	if f.Error != nil {
		return string(f.Error.Code)
	}
	return ""
}

// TestSessionRelease is the owner's release of ADR-0094 §3, end to end: a real `serve`,
// real shims and the fake adb. A holds the worktree wt1 and the device emulator-5554;
// session.release over the WebSocket frees the worktree and then the device without moving A
// (A's row loses occupies, then devices, and A still describes wt1), B then enters wt1 and
// acts on the device; a release of what nobody holds answers as done; over MCP the tool is
// not there, and both fields or neither is REQUEST_INVALID_PARAMS.
//
// Seen failing: with ReleaseWorkspace also rebinding the holder to its project root, A's
// describe named the main tree and not wt1 ("A describes ... after the release; want wt1").
// The outright release (no handover to the chat's other session) is pinned in
// internal/capability, where it was seen failing with the session's own releaseLocked.
func TestSessionRelease(t *testing.T) {
	t.Parallel()
	w := releaseWorld(t)
	ui := wsToDaemon(t, w.state)

	a := w.agent(t, "agent-a", "chat-a", w.wt1)
	b := w.agent(t, "agent-b", "chat-b", w.main)
	w.occupies(t, "agent-a", w.wt1)
	if c := a.toolWith(2, "device_uninstall", uninstallArgs); c.isError {
		t.Fatalf("A's uninstall: %s", c.raw)
	}
	w.waitRow(t, "agent-a", "its device", func(r capability.SessionListed) bool {
		return len(r.Devices) == 1 && r.Devices[0] == releaseSerial
	})

	// Held, B is refused on both.
	if c := w.enter(t, b, 3, w.wt1); !c.isError || c.err.Code != capability.ErrWorkspaceOccupied {
		t.Fatalf("B entering the held wt1: %s; want %s", c.raw, capability.ErrWorkspaceOccupied)
	}
	if c := b.toolWith(4, "device_uninstall", uninstallArgs); !c.isError || c.err.Code != "DEVICE_OCCUPIED" {
		t.Fatalf("B's uninstall on the held device: %s; want DEVICE_OCCUPIED", c.raw)
	}

	// The wrong shapes first: nothing is released.
	for name, params := range map[string]string{"neither": `{}`, "both": `{"workspace":"` + w.idOf(w.wt1) + `","device":"` + releaseSerial + `"}`} {
		if code := release(t, ui, "bad-"+name, params); code != string(capability.ErrInvalidParams) {
			t.Errorf("session.release with %s = %q; want %s", name, code, capability.ErrInvalidParams)
		}
	}
	w.occupies(t, "agent-a", w.wt1)

	// The worktree: A loses occupies and keeps its binding.
	if code := release(t, ui, "rel-ws", `{"workspace":"`+w.idOf(w.wt1)+`"}`); code != "" {
		t.Fatalf("session.release {workspace} failed %s", code)
	}
	row := w.occupies(t, "agent-a", "")
	if row.Workspace != w.idOf(w.wt1) || len(row.Devices) != 1 {
		t.Errorf("A's row after the worktree's release is %+v; want it still bound to wt1 and still holding the device", row)
	}
	if root, _ := describedWith(t, a, 5); root != w.wt1 {
		t.Errorf("A describes %s after the release; want wt1: the owner's release never moves the session", root)
	}
	if c := w.enter(t, b, 6, w.wt1); c.isError {
		t.Fatalf("B entering the released wt1: %s", c.raw)
	}
	w.occupies(t, "agent-b", w.wt1)

	// The device: B can act on it.
	if code := release(t, ui, "rel-dev", `{"device":"`+releaseSerial+`"}`); code != "" {
		t.Fatalf("session.release {device} failed %s", code)
	}
	w.waitRow(t, "agent-a", "no device", func(r capability.SessionListed) bool { return len(r.Devices) == 0 })
	if c := b.toolWith(7, "device_uninstall", uninstallArgs); c.isError {
		t.Fatalf("B's uninstall after the release: %s", c.raw)
	}
	w.waitRow(t, "agent-b", "its device", func(r capability.SessionListed) bool { return len(r.Devices) == 1 })

	// Nobody holds an unheld one: done, twice.
	for _, params := range []string{`{"workspace":"` + w.idOf(w.wt2) + `"}`, `{"device":"emulator-9999"}`, `{"workspace":"` + w.idOf(w.wt2) + `"}`} {
		if code := release(t, ui, "free", params); code != "" {
			t.Errorf("session.release %s of what nobody holds failed %s; want done", params, code)
		}
	}

	// An agent may not release: the tool is not even listed to it (the core's refusal,
	// REQUEST_NOT_ALLOWED_HERE, is pinned in internal/capability).
	b.callTool(8, "session_release", `{"device":"`+releaseSerial+`"}`)
	if line := string(b.recv()); !strings.Contains(line, `"error"`) || !strings.Contains(line, "not found") {
		t.Fatalf("session_release over MCP answered %s; want the tool not found", line)
	}
	if row, _ := w.row(t, "agent-b"); len(row.Devices) != 1 {
		t.Errorf("B's row after its refused release is %+v; want its device still held", row)
	}
}

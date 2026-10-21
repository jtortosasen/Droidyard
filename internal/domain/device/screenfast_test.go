package device

import (
	"context"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// startedWith acquires the device's source for a start that is fast or not, then once more
// for the opposite, and returns the server's one logged start. It fails unless the second
// acquire joined the source the first started.
func startedWith(t *testing.T, firstFast bool) string {
	t.Helper()
	f := startedFixture(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), ssWait)
	defer cancel()
	s1, r1, err := f.adb.screens.acquireWith(ctx, f.adb, f.tgt, firstFast)
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	s2, r2, err := f.adb.screens.acquireWith(ctx, f.adb, f.tgt, !firstFast)
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	if s1 != s2 {
		t.Fatal("the second viewer did not join the running source")
	}
	f.waitServers(t, 1)
	starts := f.callsWith(t, "app_process")
	if len(starts) != 1 {
		t.Fatalf("the server was started %d times, want once: %q", len(starts), starts)
	}
	return starts[0]
}

// Decision (ADR-0118 §4): a source started for a fast peer uses the fast preset (1024 px,
// 2 Mbit/s, 30 fps), and every other start keeps ADR-0077 §1's options. Seen failing with the
// fast flag ignored in serverOptions: the fast start logged max_size=1280.
func TestPresetFollowsTheRemotesMark(t *testing.T) {
	fast := startedWith(t, true)
	for _, want := range []string{"max_size=1024", "video_bit_rate=2000000", "max_fps=30", "video_codec_options=i-frame-interval=2"} {
		if !strings.Contains(fast, want) {
			t.Errorf("a fast start lacks %q: %s", want, fast)
		}
	}
	plain := startedWith(t, false)
	for _, want := range []string{"max_size=1280", "video_bit_rate=4000000", "max_fps=60"} {
		if !strings.Contains(plain, want) {
			t.Errorf("a plain start lacks %q: %s", want, plain)
		}
	}
}

// Decision (ADR-0118 §4): a source keeps the options it started with, shared by every later
// viewer: the first starter decides. startedWith's second acquire is the opposite kind, and
// the one logged start is the first's. Seen failing with the second acquirer's flag applied
// to the source (s.fast overwritten on a join): the plain-first source logged fast options.
func TestASourceKeepsTheOptionsItStartedWith(t *testing.T) {
	if got := startedWith(t, true); strings.Contains(got, "max_size=1280") {
		t.Errorf("a plain viewer changed a fast source's options: %s", got)
	}
	if got := startedWith(t, false); strings.Contains(got, "max_size=1024") {
		t.Errorf("a fast viewer changed a plain source's options: %s", got)
	}
}

// Decision (ADR-0118 §4): only a paired peer's call whose hello said fast starts a fast
// source; the daemon's own clients never do, whatever the lookup says of a name they share.
// Seen failing with the client kind not checked: an mcp call named like a fast peer was fast.
func TestOnlyAFastPeersCallStartsAFastSource(t *testing.T) {
	a := &ADB{}
	call := func(kind capability.ClientKind, name string) *capability.Call {
		return &capability.Call{Origin: capability.Origin{Client: kind, Name: name}}
	}
	if a.callFast(call(capability.ClientPeer, "anna")) {
		t.Error("with no lookup a peer was fast")
	}
	a.SetPeerFast(func(p string) bool { return p == "anna" })
	if !a.callFast(call(capability.ClientPeer, "anna")) {
		t.Error("a fast peer's call was not fast")
	}
	if a.callFast(call(capability.ClientPeer, "bob")) {
		t.Error("a peer whose hello did not say fast was fast")
	}
	if a.callFast(call(capability.ClientMCP, "anna")) {
		t.Error("an MCP call named like a fast peer was fast")
	}
}

package capability

import (
	"context"
	"strings"
	"testing"
	"unicode"
)

// The session on the operation events (ADR-0048): who made a recorded call, as the
// events and operation.watch's rows name it.

// TestActiveOperationCarriesTheSession is the decision of ADR-0048 §1 for operation.watch's
// rows: an operation recorded with an origin is listed with session {id, name, workspace},
// the workspace being the bound root's canonical id (here the fake registry's, which admits
// any root); one recorded without an origin has no session; an origin without a root has no
// workspace.
func TestActiveOperationCarriesTheSession(t *testing.T) {
	ops, _, started := stateStore(t)
	ctx := context.Background()
	start := func(o Origin) string {
		t.Helper()
		id, err := ops.Start(ctx, Request{Capability: "trace.block", Client: ClientMCP, Origin: o})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		return id
	}
	named := start(Origin{ID: "s-a", Name: "agent-a", Root: "/projects/a"})
	unbound := start(Origin{ID: "s-b", Name: "agent-b"})
	anon := start(Origin{})
	t.Cleanup(func() {
		for _, id := range []string{named, unbound, anon} {
			ops.Cancel(id)
		}
	})

	rows := map[string]ActiveOperation{}
	for _, a := range ops.Active() {
		rows[a.OperationID] = a
	}
	if s := rows[named].Session; s == nil || *s != (SessionRef{ID: "s-a", Name: "agent-a", Workspace: "ws-canon"}) {
		t.Errorf("the bound session's row carries %+v; want {s-a agent-a ws-canon}", s)
	}
	if s := rows[unbound].Session; s == nil || *s != (SessionRef{ID: "s-b", Name: "agent-b"}) {
		t.Errorf("the unbound session's row carries %+v; want {s-b agent-b} and no workspace", s)
	}
	if s := rows[anon].Session; s != nil {
		t.Errorf("a call without an origin has the session %+v; want none", s)
	}
}

// TestStateEventsCarryTheSession is the same decision for operation.state: every event of
// an operation (its start and its terminal one) carries the session it was recorded with,
// and a call without an origin carries none.
func TestStateEventsCarryTheSession(t *testing.T) {
	ops, rec, _ := stateStore(t)
	ctx := context.Background()
	with, err := ops.Start(ctx, Request{Capability: "trace.ok", Client: ClientCLI, Origin: Origin{ID: "s-1", Name: "cli", Root: "/r"}})
	if err != nil {
		t.Fatal(err)
	}
	without, err := ops.Start(ctx, Request{Capability: "trace.ok", Client: ClientUI})
	if err != nil {
		t.Fatal(err)
	}
	rec.await(t, with)
	rec.await(t, without)

	seen := map[string]int{}
	for _, e := range rec.all() {
		p := e.payload.(OperationState)
		seen[e.operation]++
		switch e.operation {
		case with:
			if p.Session == nil || *p.Session != (SessionRef{ID: "s-1", Name: "cli", Workspace: "ws-canon"}) {
				t.Errorf("an event of the call with an origin carries %+v; want {s-1 cli ws-canon}", p.Session)
			}
		case without:
			if p.Session != nil {
				t.Errorf("an event of the call without an origin carries %+v; want none", p.Session)
			}
		}
	}
	if seen[with] < 2 || seen[without] < 2 {
		t.Errorf("events seen per operation: %v; want the start and the terminal event of each", seen)
	}
}

// TestSessionNameIsBounded is the decision of ADR-0048 §1 on the name: a client chooses
// it, so the store keeps at most 64 runes and removes control (Cc) and format (Cf)
// characters, which could break a line or reorder the text a panel shows. The controls are
// removed before the cut, so they do not eat into the 64, and the bound holds in runes, not
// bytes. Checked on the function and on the events and rows the store emits.
func TestSessionNameIsBounded(t *testing.T) {
	// 300 runes: a letter, then newline, escape and the right-to-left override, repeating,
	// each letter a multi-byte one.
	var b strings.Builder
	for b.Len() < 1000 && len([]rune(b.String())) < 300 {
		b.WriteString("é\n\x1b‮")
	}
	raw := string([]rune(b.String())[:300])

	got := SessionName(raw)
	if n := len([]rune(got)); n != 64 {
		t.Errorf("SessionName keeps %d runes of %d; want 64 (controls removed before the cut)", n, len([]rune(raw)))
	}
	for _, r := range got {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			t.Errorf("SessionName kept %q (%U)", r, r)
		}
	}
	if got := SessionName("  \x00 Cursor​ "); got != "Cursor" {
		t.Errorf("SessionName(%q) = %q; want Cursor", "  \x00 Cursor​ ", got)
	}
	if got := SessionName("\n‮"); got != "" {
		t.Errorf("a name of only control characters is %q; want empty", got)
	}
	if got := SessionName("a\xffb"); got != "ab" {
		t.Errorf("invalid UTF-8 gives %q; want ab", got)
	}

	// The store applies it to what an adapter hands over.
	ops, rec, started := stateStore(t)
	id, err := ops.Start(context.Background(), Request{Capability: "trace.block", Client: ClientMCP, Origin: Origin{ID: "s", Name: raw}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	t.Cleanup(func() { ops.Cancel(id) })
	for _, a := range ops.Active() {
		if a.OperationID == id && (a.Session == nil || a.Session.Name != SessionName(raw)) {
			t.Errorf("the watch row carries the name %+v; want it bounded", a.Session)
		}
	}
	for _, e := range rec.all() {
		if p := e.payload.(OperationState); e.operation == id && (p.Session == nil || p.Session.Name != SessionName(raw)) {
			t.Errorf("the event carries the name %+v; want it bounded", p.Session)
		}
	}
}

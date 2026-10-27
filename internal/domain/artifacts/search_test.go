package artifacts

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

func (f *fixture) search(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "artifact.search", Params: []byte(params)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) logsCapture(t *testing.T, content string, untrusted bool) artifact.Ref {
	t.Helper()
	ref, err := f.store.Put(artifact.Item{Data: []byte(content), MediaType: "text/plain", Untrusted: untrusted, Producer: "device.logs"})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func failed(t *testing.T, out capability.Outcome, code capability.ErrorCode) {
	t.Helper()
	if out.State != capability.StateFailed || out.Error == nil || out.Error.Code != code {
		t.Fatalf("want %s, got %s %+v", code, out.State, out.Error)
	}
}

// Decision (ADR-0136 §1, §2): the result is grep's shape over the artifact, rendered as
// N:text for a match and N-text for context, every text quoted so an artifact's line
// cannot forge a line of ours; Slim drops the lines.
func TestSearchResultAndRendering(t *testing.T) {
	f := newFixture(t)
	ref := f.logsCapture(t, "one\nERROR a\"b\ntwo\nthree\n", false)
	out := f.search(t, params(ref.Ref, `,"match":["ERROR"],"context":1`))
	if out.State != capability.StateCompleted {
		t.Fatalf("%s %+v", out.State, out.Error)
	}
	r := out.Result.(SearchResult)
	if r.Matched != 1 || r.Returned != 3 || r.Lines[1].Line != 2 || r.Lines[0].Context != true || r.Lines[1].Context {
		t.Fatalf("%+v", r)
	}
	d, _ := f.reg.Lookup("artifact.search")
	got := d.Text(r)
	if !strings.HasSuffix(got, "\n1-\"one\"\n2:\"ERROR a\\\"b\"\n3-\"two\"") {
		t.Fatalf("rendering:\n%s", got)
	}
	if s := d.Slim(r).(SearchResult); s.Lines != nil || s.Matched != 1 {
		t.Fatalf("slim: %+v", s)
	}
}

// Decision (ADR-0136 §1): patterns are validated before the reference is resolved, so an
// unknown reference with a bad pattern is REQUEST_INVALID_PARAMS, and an unknown one with
// good patterns is ARTIFACT_NOT_FOUND.
func TestSearchBadPatternBeforeLookup(t *testing.T) {
	f := newFixture(t)
	failed(t, f.search(t, params("droidyard://artifact/e.nope.1.aaaaaaaaaaaa", `,"match":["("]`)), capability.ErrInvalidParams)
	failed(t, f.search(t, params("droidyard://artifact/e.nope.1.aaaaaaaaaaaa", `,"match":["x"],"context":11`)), capability.ErrInvalidParams)
	failed(t, f.search(t, params("droidyard://artifact/e.nope.1.aaaaaaaaaaaa", `,"match":["x"],"maxLineBytes":15`)), capability.ErrInvalidParams)
	failed(t, f.search(t, params("droidyard://artifact/e.nope.1.aaaaaaaaaaaa", `,"match":["x"],"keep":"middle"`)), capability.ErrInvalidParams)
	failed(t, f.search(t, params("droidyard://artifact/e.nope.1.aaaaaaaaaaaa", `,"match":[]`)), capability.ErrInvalidParams)
	failed(t, f.search(t, params("droidyard://artifact/nonsense", `,"match":["x"]`)), capability.ErrArtifactNotFound)
}

// Decision (ADR-0136 §1): an image is not searched; the error says to read it with
// artifact.read.
func TestSearchRefusesAnImage(t *testing.T) {
	f := newFixture(t)
	ref := f.put(t, shot(t, images[0]), true)
	out := f.search(t, params(ref.Ref, `,"match":["x"]`))
	failed(t, out, capability.ErrInvalidParams)
	if !strings.Contains(out.Error.Message, "artifact.read") {
		t.Fatalf("message: %q", out.Error.Message)
	}
}

// Decision (ADR-0136 §1; ADR-0015 §1): the result carries the artifact's own untrusted
// mark, as artifact.read's does: a logs capture is marked, a build console is not.
func TestSearchMarkFollowsTheArtifact(t *testing.T) {
	f := newFixture(t)
	logs := f.logsCapture(t, "hit\n", true)
	console := f.console(t, "hit\n", true)
	for _, c := range []struct {
		ref  string
		want bool
	}{{logs.Ref, true}, {console.Ref, false}} {
		out := f.search(t, params(c.ref, `,"match":["hit"]`))
		if out.State != capability.StateCompleted || out.Untrusted != c.want {
			t.Errorf("%s: %s untrusted=%v, want %v", c.ref, out.State, out.Untrusted, c.want)
		}
	}
}

// Decision (ADR-0099 §2; ADR-0136 §2): artifact.search is not lendable. A peer's call is
// REQUEST_NOT_ALLOWED_HERE before anything runs.
func TestSearchIsNotLentToPeers(t *testing.T) {
	f := newFixture(t)
	ref := f.logsCapture(t, "hit\n", false)
	_, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "artifact.search", Client: capability.ClientPeer, Params: []byte(params(ref.Ref, `,"match":["hit"]`)),
	})
	if capability.AsError(err).Code != capability.ErrNotAllowedHere {
		t.Fatalf("a peer's search: %v", err)
	}
}

// Decision (ADR-0136 §1): nextOffset continues a call, a mid-line offset is
// REQUEST_INVALID_PARAMS, and a call that reached the end has no nextOffset.
func TestSearchContinuesByNextOffset(t *testing.T) {
	f := newFixture(t)
	ref := f.logsCapture(t, "hit 1\nhit 2\nhit 3\n", false)
	zero := time.Duration(0)
	searchBudget = &zero
	t.Cleanup(func() { searchBudget = nil })
	var seen []int
	offset := ""
	for i := 0; i < 5; i++ {
		out := f.search(t, params(ref.Ref, `,"match":["hit"]`+offset))
		r := out.Result.(SearchResult)
		for _, l := range r.Lines {
			seen = append(seen, l.Line)
		}
		if r.NextOffset == nil {
			break
		}
		offset = `,"offset":` + strconv.FormatInt(*r.NextOffset, 10)
	}
	if len(seen) != 3 || seen[0] != 1 || seen[2] != 3 {
		t.Fatalf("lines seen across calls: %v", seen)
	}
	searchBudget = nil
	failed(t, f.search(t, params(ref.Ref, `,"match":["hit"],"offset":2`)), capability.ErrInvalidParams)
}

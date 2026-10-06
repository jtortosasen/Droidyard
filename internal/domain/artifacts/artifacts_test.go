package artifacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

var images = []string{"pixel9a", "pixeltablet", "mediumphonea17"}

func shot(t *testing.T, img string) []byte {
	t.Helper()
	b, err := os.ReadFile("../device/testdata/" + img + "-screencap.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fixture struct {
	store *artifact.Store
	reg   *capability.Registry
	ops   *capability.Operations
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := artifact.New(artifact.Options{Epoch: "d-test", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	reg := capability.NewRegistry()
	Register(reg, store)
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	return &fixture{store: store, reg: reg, ops: ops}
}

// console creates a durable build console (D13) in workspace, writes content, and
// returns its reference. The producer keeps the handle open unless closed, so a read
// sees it growing.
func (f *fixture) console(t *testing.T, content string, close bool) artifact.Ref {
	t.Helper()
	d, err := f.store.CreateDurable("ws-1", "op-1", artifact.DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if close {
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return d.Ref()
}

func (f *fixture) put(t *testing.T, data []byte, untrusted bool) artifact.Ref {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := f.store.Put(artifact.Item{Data: data, MediaType: "image/png", Width: cfg.Width, Height: cfg.Height, Untrusted: untrusted, Producer: "device.screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func (f *fixture) read(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "artifact.read", Params: []byte(params)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.ops.Wait(context.Background(), id, time.Now().Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ok(t *testing.T, out capability.Outcome) (ReadResult, []byte) {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("%s: %+v", out.State, out.Error)
	}
	r := out.Result.(ReadResult)
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	return r, b
}

func params(ref string, extra string) string {
	b, _ := json.Marshal(ref)
	return `{"ref":` + string(b) + extra + `}`
}

// Decision (plan step 7, check): with neither parameter artifact.read returns the
// stored bytes unchanged (identical, not re-encoded), with the stored size as the
// delivered one. Run on the three recorded screens.
func TestReadWithoutParametersReturnsTheStoredBytes(t *testing.T) {
	for _, img := range images {
		f := newFixture(t)
		raw := shot(t, img)
		ref := f.put(t, raw, true)
		r, got := ok(t, f.read(t, params(ref.Ref, "")))
		if !bytes.Equal(got, raw) {
			t.Errorf("%s: the delivered bytes differ from the stored ones", img)
		}
		if stored(t, r) != (Size{ref.Width, ref.Height}) || delivered(t, r) != stored(t, r) || r.MediaType != "image/png" || r.Ref != ref.Ref {
			t.Errorf("%s: %+v", img, r)
		}
	}
}

// Decision: maxEdge limits the longest edge, keeping the aspect within a pixel, and
// never upscales: above the stored size the stored size and bytes come back.
func TestReadMaxEdge(t *testing.T) {
	for _, img := range images {
		f := newFixture(t)
		raw := shot(t, img)
		ref := f.put(t, raw, true)
		r, got := ok(t, f.read(t, params(ref.Ref, `,"maxEdge":1024`)))
		long := max(ref.Width, ref.Height)
		if long <= 1024 {
			t.Fatalf("%s: fixture is %dx%d, too small to test a downscale", img, ref.Width, ref.Height)
		}
		dv := delivered(t, r)
		if max(dv.Width, dv.Height) != 1024 {
			t.Errorf("%s: delivered %+v, want a longest edge of 1024", img, r.Delivered)
		}
		wantShort := float64(min(ref.Width, ref.Height)) * 1024 / float64(long)
		if d := float64(min(dv.Width, dv.Height)) - wantShort; d > 1 || d < -1 {
			t.Errorf("%s: short edge %d, aspect wants %.1f", img, min(dv.Width, dv.Height), wantShort)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(got))
		if err != nil || cfg.Width != dv.Width || cfg.Height != dv.Height {
			t.Errorf("%s: the bytes decode to %+v (%v), the result says %+v", img, cfg, err, r.Delivered)
		}
		if stored(t, r) != (Size{ref.Width, ref.Height}) {
			t.Errorf("%s: stored %+v", img, r.Stored)
		}
		r, got = ok(t, f.read(t, params(ref.Ref, `,"maxEdge":99999`)))
		if delivered(t, r) != stored(t, r) || !bytes.Equal(got, raw) {
			t.Errorf("%s: a maxEdge above the stored size changed the image: %+v", img, r)
		}
	}
}

// Decision: a region is normalised to the stored image; {0,0,0.5,0.5} halves both
// dimensions. A region outside 0..1 is REQUEST_INVALID_PARAMS, whatever the artifact.
func TestReadRegion(t *testing.T) {
	f := newFixture(t)
	ref := f.put(t, shot(t, "pixel9a"), true)
	r, _ := ok(t, f.read(t, params(ref.Ref, `,"region":{"x":0,"y":0,"w":0.5,"h":0.5}`)))
	dv := delivered(t, r)
	if d := dv.Width - ref.Width/2; d > 1 || d < -1 {
		t.Errorf("width %d, want about %d", dv.Width, ref.Width/2)
	}
	if d := dv.Height - ref.Height/2; d > 1 || d < -1 {
		t.Errorf("height %d, want about %d", dv.Height, ref.Height/2)
	}
	for _, region := range []string{`{"x":-0.1,"y":0,"w":0.5,"h":0.5}`, `{"x":0,"y":0,"w":1.5,"h":0.5}`, `{"x":0.6,"y":0,"w":0.5,"h":0.5}`, `{"x":0,"y":0,"w":0,"h":0.5}`} {
		out := f.read(t, params(ref.Ref, `,"region":`+region))
		if out.State != capability.StateFailed || out.Error.Code != capability.ErrInvalidParams {
			t.Errorf("region %s: %s, %+v", region, out.State, out.Error)
		}
	}
}

// Decision (ADR-0015 §1): the result carries the artifact's own mark, not a constant:
// an artifact stored untrusted yields meta.untrusted, one stored trusted yields none.
func TestReadCarriesTheArtifactsMark(t *testing.T) {
	f := newFixture(t)
	d, _ := f.reg.Lookup("artifact.read")
	if !d.Untrusted() {
		t.Error("the descriptor's derived flag must be true: data is tagged")
	}
	raw := shot(t, "pixel9a")
	for _, c := range []struct{ untrusted bool }{{true}, {false}} {
		ref := f.put(t, raw, c.untrusted)
		out := f.read(t, params(ref.Ref, ""))
		if out.State != capability.StateCompleted || out.Untrusted != c.untrusted {
			t.Errorf("artifact untrusted=%v: outcome %s, Untrusted=%v", c.untrusted, out.State, out.Untrusted)
		}
	}
}

// Decision (ADR-0012): a miss is told apart: a reference of another daemon run (and
// one this run's store evicted) is ARTIFACT_EXPIRED with a next step; a malformed or
// never-issued one is ARTIFACT_NOT_FOUND.
func TestReadMisses(t *testing.T) {
	f := newFixture(t)
	other, err := artifact.New(artifact.Options{Epoch: "d-other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := other.Put(artifact.Item{Data: shot(t, "pixel9a"), MediaType: "image/png"})
	for ref, code := range map[string]capability.ErrorCode{
		foreign.Ref:                                    capability.ErrArtifactExpired,
		"droidyard://artifact/nonsense":                capability.ErrArtifactNotFound,
		"droidyard://artifact/e.d-test.9.abcdefabcdef": capability.ErrArtifactNotFound,
	} {
		out := f.read(t, params(ref, ""))
		if out.State != capability.StateFailed || out.Error.Code != code {
			t.Errorf("%s: %s, %+v; want %s", ref, out.State, out.Error, code)
		}
	}
}

// Decision (B1): a retained artifact.read result keeps no bytes. The first answer
// carries data; a later operation.wait on the same operation returns the metadata
// without it, and the retained value is small, so the 200 retained results do not
// hold images past the store's own budget and lifetime (ADR-0020 §Retention).
func TestRetainedReadKeepsNoBytes(t *testing.T) {
	f := newFixture(t)
	raw := shot(t, "pixel9a")
	ref := f.put(t, raw, true)
	ctx := context.Background()
	id, err := f.ops.Start(ctx, capability.Request{Capability: "artifact.read", Params: []byte(params(ref.Ref, ""))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.ops.Wait(ctx, id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if r := first.Result.(ReadResult); !bytes.Equal(mustDecode(t, r.Data), raw) {
		t.Fatalf("the first answer must carry the bytes; got %d base64 chars", len(r.Data))
	}
	for i := 0; i < 2; i++ {
		again, err := f.ops.Invoke(ctx, capability.Request{Capability: "operation.wait", Params: []byte(`{"operationId":"` + id + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
		w, _ := again.Result.(ReadResult)
		if w.Data != "" || w.Ref != ref.Ref || w.MediaType != "image/png" || stored(t, w).Width != ref.Width || !again.Untrusted {
			t.Errorf("wait %d: %+v (untrusted %v); want the metadata and no data", i, w, again.Untrusted)
		}
		if b, _ := json.Marshal(again.Result); len(b) > 1024 {
			t.Errorf("the retained result is %d bytes", len(b))
		}
	}
}

// Decision (N4): the published minimum of maxEdge is enforced, an explicit 0 included.
func TestReadRejectsMaxEdgeBelowOne(t *testing.T) {
	f := newFixture(t)
	ref := f.put(t, shot(t, "pixel9a"), true)
	for _, v := range []string{"0", "-5"} {
		_, err := f.ops.Invoke(context.Background(), capability.Request{Capability: "artifact.read", Params: []byte(params(ref.Ref, `,"maxEdge":`+v))})
		out := f.read(t, params(ref.Ref, `,"maxEdge":`+v))
		if out.State != capability.StateFailed || out.Error.Code != capability.ErrInvalidParams {
			t.Errorf("maxEdge %s: %v, %+v; want %s", v, err, out, capability.ErrInvalidParams)
		}
	}
}

// Decision (ADR-0012 §Reading artifacts; D13): a durable console is read as text,
// paged from its file: offset, nextOffset, size, growing, text and truncated come
// back and the byte page is absent. It is unmarked, because a console is project
// content, not device-derived (ADR-0015 §1): reversing that mark is what this asserts.
func TestReadTextFromADurableConsole(t *testing.T) {
	f := newFixture(t)
	const content = "> Task :app:compileDebugKotlin\nBUILD SUCCESSFUL in 42s\n"
	ref := f.console(t, content, false)
	out := f.read(t, params(ref.Ref, ""))
	if out.State != capability.StateCompleted {
		t.Fatalf("%s: %+v", out.State, out.Error)
	}
	if out.Untrusted {
		t.Error("a build console is project content and must be unmarked")
	}
	r := out.Result.(ReadResult)
	if r.Ref != ref.Ref || r.MediaType != "text/plain" || r.Data != "" {
		t.Fatalf("result = %+v", r)
	}
	if r.Stored != nil || r.Delivered != nil {
		t.Errorf("a text read must carry no image sizes: %+v", r)
	}
	if r.Text == nil || *r.Text != content || r.Offset == nil || *r.Offset != 0 || r.NextOffset == nil || *r.NextOffset != int64(len(content)) {
		t.Fatalf("text page = %+v", r)
	}
	if r.Size == nil || *r.Size != int64(len(content)) || r.Growing == nil || !*r.Growing || r.Truncated == nil || *r.Truncated {
		t.Fatalf("metadata = %+v", r)
	}
}

// Decision (ADR-0012 §Reading artifacts): an offset at or past the end returns empty
// text, and empty is a fact: offset, nextOffset and size are still reported, and a
// console the producer has closed is no longer growing.
func TestReadTextOffsetAtOrPastTheEndIsEmpty(t *testing.T) {
	f := newFixture(t)
	ref := f.console(t, "a\nb\n", true)
	r, _ := ok(t, f.read(t, params(ref.Ref, `,"offset":100`)))
	if r.Text == nil || *r.Text != "" {
		t.Fatalf("text = %+v, want empty", r)
	}
	if r.Offset == nil || *r.Offset != 4 || r.NextOffset == nil || *r.NextOffset != 4 || r.Size == nil || *r.Size != 4 {
		t.Fatalf("metadata = %+v, want offset 4, nextOffset 4, size 4", r)
	}
	if r.Growing == nil || *r.Growing {
		t.Errorf("a closed console is not growing: %+v", r)
	}
}

// Decision (ADR-0012 §Reading artifacts): a negative offset counts back from the end
// and a read that lands mid-line starts at the next line; a limit shorter than the
// remaining lines ends at the last complete line within it.
func TestReadTextNegativeOffsetAndLimit(t *testing.T) {
	f := newFixture(t)
	ref := f.console(t, "aaa\nbbb\nccc\n", true)
	r, _ := ok(t, f.read(t, params(ref.Ref, `,"offset":-6`)))
	if r.Offset == nil || *r.Offset != 8 || r.Text == nil || *r.Text != "ccc\n" {
		t.Fatalf("negative offset: %+v, want offset 8 text ccc", r)
	}
	r, _ = ok(t, f.read(t, params(ref.Ref, `,"limit":6`)))
	if r.Text == nil || *r.Text != "aaa\n" || r.NextOffset == nil || *r.NextOffset != 4 {
		t.Fatalf("limit 6: %+v, want aaa and nextOffset 4", r)
	}
}

// Decision (ADR-0012 §Reading artifacts): a producer's half-written final line is not
// presented as a complete one; the read stops at the last newline and continues there.
func TestReadTextHoldsBackThePartialFinalLine(t *testing.T) {
	f := newFixture(t)
	ref := f.console(t, "aaa\nbbb\ncc", false)
	r, _ := ok(t, f.read(t, params(ref.Ref, "")))
	if r.Text == nil || *r.Text != "aaa\nbbb\n" || r.NextOffset == nil || *r.NextOffset != 8 || r.Size == nil || *r.Size != 10 {
		t.Fatalf("text = %+v, want aaa+bbb and nextOffset 8 of 10", r)
	}
}

// Decision (N4): the published minimum of limit is enforced at the capability, so an
// explicit 0 or a negative is REQUEST_INVALID_PARAMS, not a silent default.
func TestReadTextRejectsLimitBelowOne(t *testing.T) {
	f := newFixture(t)
	ref := f.console(t, "x\n", true)
	for _, v := range []string{"0", "-5"} {
		out := f.read(t, params(ref.Ref, `,"limit":`+v))
		if out.State != capability.StateFailed || out.Error.Code != capability.ErrInvalidParams {
			t.Errorf("limit %s: %s, %+v; want %s", v, out.State, out.Error, capability.ErrInvalidParams)
		}
	}
}

// Decision (ADR-0020 §Retention): a retained text read keeps no bytes either. The first
// answer carries the text; a later operation.wait on the same operation returns the
// page metadata without it, and the retained value is small, so 200 retained results
// cannot hold consoles past the store's own lifetime.
func TestRetainedTextReadKeepsNoText(t *testing.T) {
	f := newFixture(t)
	ref := f.console(t, "one\ntwo\n", true)
	ctx := context.Background()
	id, err := f.ops.Start(ctx, capability.Request{Capability: "artifact.read", Params: []byte(params(ref.Ref, ""))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.ops.Wait(ctx, id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if r := first.Result.(ReadResult); r.Text == nil || *r.Text != "one\ntwo\n" {
		t.Fatalf("the first answer must carry the text: %+v", r)
	}
	again, err := f.ops.Invoke(ctx, capability.Request{Capability: "operation.wait", Params: []byte(`{"operationId":"` + id + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := again.Result.(ReadResult)
	if w.Text != nil || w.Ref != ref.Ref || w.MediaType != "text/plain" || w.NextOffset == nil || *w.NextOffset != 8 {
		t.Errorf("wait: %+v; want the page metadata and no text", w)
	}
	if b, _ := json.Marshal(again.Result); len(b) > 512 {
		t.Errorf("the retained result is %d bytes", len(b))
	}
}

// stored and delivered are the image sizes an image read must carry.
func stored(t *testing.T, r ReadResult) Size {
	t.Helper()
	if r.Stored == nil {
		t.Fatalf("the result has no stored size: %+v", r)
	}
	return *r.Stored
}

func delivered(t *testing.T, r ReadResult) Size {
	t.Helper()
	if r.Delivered == nil {
		t.Fatalf("the result has no delivered size: %+v", r)
	}
	return *r.Delivered
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

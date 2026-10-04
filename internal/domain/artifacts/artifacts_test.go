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
	store, err := artifact.New(artifact.Options{Epoch: "d-test"})
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
		if r.Stored != (Size{ref.Width, ref.Height}) || r.Delivered != r.Stored || r.MediaType != "image/png" || r.Ref != ref.Ref {
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
		if max(r.Delivered.Width, r.Delivered.Height) != 1024 {
			t.Errorf("%s: delivered %+v, want a longest edge of 1024", img, r.Delivered)
		}
		wantShort := float64(min(ref.Width, ref.Height)) * 1024 / float64(long)
		if d := float64(min(r.Delivered.Width, r.Delivered.Height)) - wantShort; d > 1 || d < -1 {
			t.Errorf("%s: short edge %d, aspect wants %.1f", img, min(r.Delivered.Width, r.Delivered.Height), wantShort)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(got))
		if err != nil || cfg.Width != r.Delivered.Width || cfg.Height != r.Delivered.Height {
			t.Errorf("%s: the bytes decode to %+v (%v), the result says %+v", img, cfg, err, r.Delivered)
		}
		if r.Stored != (Size{ref.Width, ref.Height}) {
			t.Errorf("%s: stored %+v", img, r.Stored)
		}
		r, got = ok(t, f.read(t, params(ref.Ref, `,"maxEdge":99999`)))
		if r.Delivered != r.Stored || !bytes.Equal(got, raw) {
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
	if d := r.Delivered.Width - ref.Width/2; d > 1 || d < -1 {
		t.Errorf("width %d, want about %d", r.Delivered.Width, ref.Width/2)
	}
	if d := r.Delivered.Height - ref.Height/2; d > 1 || d < -1 {
		t.Errorf("height %d, want about %d", r.Delivered.Height, ref.Height/2)
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
		if w.Data != "" || w.Ref != ref.Ref || w.MediaType != "image/png" || w.Stored.Width != ref.Width || !again.Untrusted {
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

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

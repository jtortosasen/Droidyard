package device

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// fakeHostArtifacts is the host's artifact route: it answers each fetch with a, err and
// records the id and the bound it was asked with.
type fakeHostArtifacts struct {
	a      hosting.Artifact
	err    error
	ids    []string
	bounds []int64
}

func (f *fakeHostArtifacts) Artifact(_ context.Context, id string, max int64) (hosting.Artifact, error) {
	f.ids, f.bounds = append(f.ids, id), append(f.bounds, max)
	return f.a, f.err
}

const hostShotRef = artifact.RefPrefix + "e.d-0a1b2c3d.7.0123456789ab"

var qaPhone = &remoteTarget{remote: "qa", id: "qa/R58M001", serial: "R58M001"}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// hugePNG is a valid PNG header (and nothing else) for an image of w x h pixels: a few dozen
// bytes that declare an image no phone has.
func hugePNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 0 // 8 bits, greyscale
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func codeOf(t *testing.T, err error) (capability.ErrorCode, map[string]any) {
	t.Helper()
	var ce *capability.Error
	if !errors.As(err, &ce) {
		t.Fatalf("%v is not a capability error", err)
	}
	return ce.Code, ce.Details
}

// Decision (ADR-0100 §3, plan m2t step 5): a relayed screenshot is the bytes of the host's
// artifact stored in this daemon's own store under its own reference, untrusted, with the
// size this daemon decoded, and the result never carries the host's reference. The host's
// warnings name the id, not the bare serial.
func TestRelayedScreenshotIsStoredLocally(t *testing.T) {
	store := testStore(t)
	raw := pngBytes(t, 30, 50)
	f := &fakeHostArtifacts{a: hosting.Artifact{MediaType: "image/png", Untrusted: true, Data: raw}}
	host := ScreenshotResult{
		Ref:      artifact.Ref{Ref: hostShotRef, MediaType: "image/png", Width: 999, Height: 999},
		Warnings: []string{"R58M001 is secure"},
	}
	out, err := (&ADB{}).screenshotFrom(context.Background(), f, qaPhone, store, host)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(ScreenshotResult)
	if res.Ref.Ref == hostShotRef || !strings.HasPrefix(res.Ref.Ref, artifact.RefPrefix) {
		t.Errorf("the result carries %q; want this daemon's own reference, not the host's %q", res.Ref.Ref, hostShotRef)
	}
	if res.Width != 30 || res.Height != 50 || !res.Untrusted || res.Bytes != len(raw) {
		t.Errorf("the reference record %+v; want the decoded 30x50, untrusted, %d bytes (not the host's claims)", res.Ref, len(raw))
	}
	e, err := store.Get(res.Ref.Ref)
	if err != nil || !bytes.Equal(e.Data, raw) || e.Producer != "device.screenshot" || e.Inline == nil {
		t.Errorf("the stored artifact: %v, producer %q, inline %v; want the host's bytes", err, e.Producer, e.Inline != nil)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "qa/R58M001 is secure" {
		t.Errorf("warnings %q; want the id in place of the serial", res.Warnings)
	}
	if len(f.ids) != 1 || f.ids[0] != "e.d-0a1b2c3d.7.0123456789ab" || f.bounds[0] != screencapCap {
		t.Errorf("fetched %v with bounds %v; want the host's id once, bounded by the local screencap cap %d", f.ids, f.bounds, screencapCap)
	}
}

// Decision (plan m2t step 5; ADR-0015 §1): what the host serves as a screenshot is checked as
// the local capture is, and before it is decoded: a header that declares more pixels than a
// phone has is refused without a decode (it would take gigabytes), as are bytes that are no
// PNG, a PNG that is cut, and a reference that is not the shape of an artifact's. Nothing is
// stored by any of them.
func TestRelayedScreenshotRefusesWhatIsNotAScreenshot(t *testing.T) {
	cut := pngBytes(t, 30, 50)
	cut = cut[:len(cut)-20]
	for name, c := range map[string]struct {
		ref   string
		data  []byte
		code  capability.ErrorCode
		cause string
	}{
		"a header of 20000x20000":   {hostShotRef, hugePNG(20000, 20000), ErrScreenshotFailed, "a size this daemon decodes"},
		"a header of 4097x4097":     {hostShotRef, hugePNG(4097, 4097), ErrScreenshotFailed, "a size this daemon decodes"},
		"text":                      {hostShotRef, []byte("not an image"), ErrScreenshotFailed, "a size this daemon decodes"},
		"a cut PNG":                 {hostShotRef, cut, ErrScreenshotFailed, "whole PNG"},
		"a durable reference":       {artifact.RefPrefix + "d.0123456789abcdef.1", pngBytes(t, 3, 3), ErrNotReady, ""},
		"a path in the reference":   {artifact.RefPrefix + "e.d-0a1b2c3d.7.0123456789ab/../../ws", pngBytes(t, 3, 3), ErrNotReady, ""},
		"no reference":              {"", pngBytes(t, 3, 3), ErrNotReady, ""},
		"a reference of another id": {"https://example.com/e.d-0a1b2c3d.7.0123456789ab", pngBytes(t, 3, 3), ErrNotReady, ""},
	} {
		store := testStore(t)
		f := &fakeHostArtifacts{a: hosting.Artifact{MediaType: "image/png", Data: c.data}}
		_, err := (&ADB{}).screenshotFrom(context.Background(), f, qaPhone, store, ScreenshotResult{Ref: artifact.Ref{Ref: c.ref}})
		code, details := codeOf(t, err)
		var ce *capability.Error
		errors.As(err, &ce)
		if code != c.code || !strings.Contains(ce.Cause, c.cause) {
			t.Errorf("%s: %s (cause %q); want %s naming %q", name, code, ce.Cause, c.code, c.cause)
		}
		if c.code == ErrNotReady && (details["state"] != "incompatible" || len(f.ids) != 0) {
			t.Errorf("%s: state %v after %d fetches; want incompatible and nothing fetched", name, details["state"], len(f.ids))
		}
		if store.Len() != 0 {
			t.Errorf("%s: %d artifacts stored; want none", name, store.Len())
		}
	}
}

// Decision (ADR-0100 §4, plan m2t step 5): a fetch that does not complete is mapped into this
// daemon's contract in its own words, with no code of its own: the caller's cancel stays a
// cancel, a link down is DEVICE_NOT_READY (unreachable), an artifact the host no longer holds
// or one over the bound is the capability's own failure, a status the host chose or a failed
// connection is unreachable, and an answer this version cannot read, or a media type other
// than the result promises, is incompatible. What the host wrote is quoted in none.
func TestRelayedFetchErrorsAreMapped(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		err   error
		media string
		code  capability.ErrorCode
		state string
	}{
		"the link is down":      {err: hosting.ErrNotConnected, code: ErrNotReady, state: "unreachable"},
		"the host says 404":     {err: &hosting.ErrArtifactRefused{Status: 404}, code: ErrScreenshotFailed},
		"the host says 410":     {err: &hosting.ErrArtifactRefused{Status: 410}, code: ErrScreenshotFailed},
		"the host says 500":     {err: &hosting.ErrArtifactRefused{Status: 500}, code: ErrNotReady, state: "unreachable"},
		"over the bound":        {err: hosting.ErrArtifactTooLarge, code: ErrScreenshotFailed},
		"a reply of no kind":    {err: hosting.ErrArtifactReply, code: ErrNotReady, state: "incompatible"},
		"the connection failed": {err: errors.New("tls: handshake secret-from-the-host"), code: ErrNotReady, state: "unreachable"},
		"another media type":    {media: "text/plain", code: ErrNotReady, state: "incompatible"},
	} {
		f := &fakeHostArtifacts{err: c.err, a: hosting.Artifact{MediaType: cmpOr(c.media, "image/png"), Data: pngBytes(t, 3, 3)}}
		_, err := (&ADB{}).screenshotFrom(context.Background(), f, qaPhone, testStore(t), ScreenshotResult{Ref: artifact.Ref{Ref: hostShotRef}})
		code, details := codeOf(t, err)
		if code != c.code || (c.state != "" && details["state"] != c.state) {
			t.Errorf("%s: %s %v; want %s %s", name, code, details, c.code, c.state)
		}
		if strings.Contains(err.Error(), "secret-from-the-host") {
			t.Errorf("%s: the error quotes the transport's text: %v", name, err)
		}
	}
	f := &fakeHostArtifacts{err: context.Canceled}
	if _, err := (&ADB{}).screenshotFrom(cancelled, f, qaPhone, testStore(t), ScreenshotResult{Ref: artifact.Ref{Ref: hostShotRef}}); !errors.Is(err, context.Canceled) {
		t.Errorf("the caller's cancel: %v; want it to stay a cancel", err)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Decision (ADR-0100 §3, plan m2t step 5): a relayed device.logs capture is the host's text
// stored in this daemon's store under its own reference, untrusted, whatever bytes the host
// sent (invalid UTF-8 is replaced, as the text reader reads it); the lines and crashes are
// bounded as the local read bounds them; a result with no capture fetches nothing.
func TestRelayedLogsCaptureIsStoredLocally(t *testing.T) {
	store := testStore(t)
	f := &fakeHostArtifacts{a: hosting.Artifact{MediaType: "text/plain", Untrusted: true, Data: []byte("one\ntwo\xff\nthree\n")}}
	host := LogsResult{
		Device:   "R58M001",
		Lines:    make([]LogLine, logsMaxLinesMax+5),
		Crashes:  make([]Crash, logsCrashesMax+3),
		Capture:  &artifact.Ref{Ref: hostShotRef, MediaType: "text/plain"},
		Warnings: []string{"R58M001 logged little"},
	}
	out, err := (&ADB{}).logsFrom(context.Background(), f, qaPhone, store, host)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(LogsResult)
	if res.Capture == nil || res.Capture.Ref == hostShotRef || !res.Capture.Untrusted {
		t.Fatalf("capture %+v; want this daemon's own untrusted reference", res.Capture)
	}
	e, err := store.Get(res.Capture.Ref)
	if err != nil || string(e.Data) != "one\ntwo�\nthree\n" || e.Producer != "device.logs" || e.Ref.MediaType != "text/plain" {
		t.Errorf("the stored capture %q (%v), producer %q", e.Data, err, e.Producer)
	}
	if f.bounds[0] != logsCaptureCap {
		t.Errorf("fetched with bound %d; want the local capture cap %d", f.bounds[0], logsCaptureCap)
	}
	if len(res.Lines) != logsMaxLinesMax || !res.Truncated || len(res.Crashes) != logsCrashesMax {
		t.Errorf("%d lines (truncated %v), %d crashes; want them bounded to %d and %d", len(res.Lines), res.Truncated, len(res.Crashes), logsMaxLinesMax, logsCrashesMax)
	}
	if len(res.Warnings) != 1 || res.Warnings[0] != "qa/R58M001 logged little" {
		t.Errorf("warnings %q; want the id in place of the serial", res.Warnings)
	}

	// The rest of what the host sends is bounded as a local read bounds it: a line's text and a
	// crash's fields at 16 KiB, their lists, and a reason that is not a published one is dropped.
	long := strings.Repeat("x", logsLineCap+100)
	text := &fakeHostArtifacts{}
	odd, _ := (&ADB{}).logsFrom(context.Background(), text, qaPhone, store, LogsResult{
		Lines:       []LogLine{{Tag: long, Message: long}},
		Crashes:     []Crash{{Exception: long, Message: long, Causes: append([]string{long}, make([]string, 40)...), Frames: []string{long}}},
		EmptyReason: "<script>",
	})
	got := odd.(LogsResult)
	if len(got.Lines[0].Tag) != logsLineCap || len(got.Lines[0].Message) != logsLineCap ||
		len(got.Crashes[0].Exception) != logsLineCap || len(got.Crashes[0].Message) != logsLineCap ||
		len(got.Crashes[0].Causes) != logsCrashesMax || len(got.Crashes[0].Causes[0]) != logsLineCap || len(got.Crashes[0].Frames[0]) != logsLineCap || got.EmptyReason != "" {
		t.Errorf("a host's text is not bounded: %d/%d, %d/%d, %d causes, %d, reason %q", len(got.Lines[0].Tag), len(got.Lines[0].Message),
			len(got.Crashes[0].Exception), len(got.Crashes[0].Message), len(got.Crashes[0].Causes), len(got.Crashes[0].Frames[0]), got.EmptyReason)
	}
	// Replacing invalid UTF-8 can grow a capture: it is cut back to the local cap, and says so.
	invalid := &fakeHostArtifacts{a: hosting.Artifact{MediaType: "text/plain", Data: bytes.Repeat([]byte("\xffa"), logsCaptureCap/2)}}
	grown, gerr := (&ADB{}).logsFrom(context.Background(), invalid, qaPhone, store, LogsResult{Capture: &artifact.Ref{Ref: hostShotRef}})
	if gerr != nil {
		t.Fatal(gerr)
	}
	ge, serr := store.Get(grown.(LogsResult).Capture.Ref)
	if serr != nil || len(ge.Data) > logsCaptureCap || !utf8.Valid(ge.Data) || len(grown.(LogsResult).Warnings) != 1 {
		t.Errorf("a capture that grew: %d bytes (%v), cap %d, warnings %q", len(ge.Data), serr, logsCaptureCap, grown.(LogsResult).Warnings)
	}

	none := &fakeHostArtifacts{}
	if out, err := (&ADB{}).logsFrom(context.Background(), none, qaPhone, store, LogsResult{}); err != nil || out.(LogsResult).Capture != nil || len(none.ids) != 0 {
		t.Errorf("a result with no capture: %+v, %v, fetched %v; want none", out, err, none.ids)
	}
	bad := &fakeHostArtifacts{}
	_, err = (&ADB{}).logsFrom(context.Background(), bad, qaPhone, store, LogsResult{Capture: &artifact.Ref{Ref: "droidyard://artifact/../../x"}})
	if code, details := codeOf(t, err); code != ErrNotReady || details["state"] != "incompatible" || len(bad.ids) != 0 {
		t.Errorf("a capture reference of no shape: %v; want incompatible and nothing fetched", err)
	}
}

// relayedLogsWith is a stream whose subscription the test reads: what the host's events
// become.
func relayedLogsWith(t *testing.T) (*relayedLogs, *event.Subscription) {
	t.Helper()
	bus := event.New(event.Options{})
	sub := bus.Subscribe([]string{TopicLogcat}, nil)
	t.Cleanup(sub.Close)
	ready := make(chan struct{})
	close(ready)
	return &relayedLogs{adb: &ADB{}, rt: qaPhone, bus: bus, sub: sub, operation: "op-1", ready: ready, quit: make(chan struct{})}, sub
}

func hostEvent(typ string, payload any) json.RawMessage {
	p, _ := json.Marshal(payload)
	raw, _ := json.Marshal(map[string]any{"topic": TopicLogcat, "type": typ, "payload": json.RawMessage(p)})
	return raw
}

func nextEvent(t *testing.T, sub *event.Subscription) (event.Event, bool) {
	t.Helper()
	e, ok := sub.TryNext()
	return e, ok
}

// Decision (ADR-0100 §3, §4; ADR-0041 §4): the host's lines are republished to the caller's
// subscription with `device` the remote id, marked untrusted, bounded as a local stream's
// batch is (500 lines, a line's text at 16 KiB), and a line that is not in the stream's
// format is counted as dropped, as a local stream counts one it does not deliver.
func TestRelayedLogsRepublishesBoundedLines(t *testing.T) {
	s, sub := relayedLogsWith(t)
	lines := []StreamLine{{Time: "2026-10-19T10:00:00.000001Z", PID: 1, TID: 1, Priority: "info", Tag: "App", Message: "hello"}}
	lines = append(lines, StreamLine{Time: "t", Priority: "shouting", Tag: "App", Message: "not a priority"})
	lines = append(lines, StreamLine{Time: "2026-10-19T10:00:00.000002Z", Priority: "warn", Tag: "App", Message: strings.Repeat("é", logsLineCap)})
	s.onEvent(hostEvent(EventLines, LinesEvent{Device: "R58M001", Lines: lines, Dropped: 2, Untrusted: false}))

	e, ok := nextEvent(t, sub)
	if !ok || e.Topic != TopicLogcat || e.Type != EventLines || e.Operation != "op-1" {
		t.Fatalf("event %+v (%v); want a lines event of the caller's operation", e, ok)
	}
	var p LinesEvent
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Device != "qa/R58M001" || !p.Untrusted || len(p.Lines) != 2 || p.Dropped != 3 {
		t.Errorf("payload %+v; want device qa/R58M001, untrusted, 2 lines, 3 dropped (2 from the host and the bad priority)", p)
	}
	if len(p.Lines[1].Message) > logsLineCap {
		t.Errorf("a line's message is %d bytes; want at most %d", len(p.Lines[1].Message), logsLineCap)
	}

	many := make([]StreamLine, streamBatchLines+7)
	for i := range many {
		many[i] = StreamLine{Time: "t", Priority: "info", Tag: "T", Message: "m"}
	}
	s.onEvent(hostEvent(EventLines, LinesEvent{Lines: many}))
	e, _ = nextEvent(t, sub)
	json.Unmarshal(e.Payload, &p)
	if len(p.Lines) != streamBatchLines || p.Dropped != 7 {
		t.Errorf("%d lines, %d dropped; want %d and 7: a batch past the stream's own is bounded and counted", len(p.Lines), p.Dropped, streamBatchLines)
	}
}

// Decision (ADR-0041 §4, ADR-0042): the host's three stopped reasons arrive as the same
// reasons in this daemon's words (the host's cause is not repeated) and any other as
// exited; one stopped is published and no other follows it, a link lost after the host's
// stopped included; nothing is published once the caller's subscription has ended.
func TestRelayedLogsStoppedOnce(t *testing.T) {
	for host, want := range map[string]string{"deviceGone": "deviceGone", "exited": "exited", "replaced": "replaced", "detached": "exited", "": "exited", "<b>x</b>": "exited"} {
		s, sub := relayedLogsWith(t)
		s.onEvent(hostEvent(EventStopped, StoppedEvent{Device: "R58M001", Reason: host, Cause: "adb logcat exited with status 255 from R58M001"}))
		e, ok := nextEvent(t, sub)
		var p StoppedEvent
		if !ok || e.Type != EventStopped || json.Unmarshal(e.Payload, &p) != nil || p.Reason != want || p.Device != "qa/R58M001" {
			t.Errorf("host reason %q: %+v (%v); want stopped %s for qa/R58M001", host, p, ok, want)
		}
		if strings.Contains(p.Cause, "R58M001") || strings.Contains(p.Cause, "255") {
			t.Errorf("host reason %q: cause %q repeats the host's", host, p.Cause)
		}
		s.publishStopped(StopDeviceGone, "the link was lost")
		s.onEvent(hostEvent(EventLines, LinesEvent{Lines: []StreamLine{{Time: "t", Priority: "info"}}}))
		if e, ok := nextEvent(t, sub); ok {
			t.Errorf("host reason %q: %+v after stopped; want nothing", host, e)
		}
	}
	s, sub := relayedLogsWith(t)
	s.unsubscribed()
	s.onEvent(hostEvent(EventLines, LinesEvent{Lines: []StreamLine{{Time: "t", Priority: "info"}}}))
	s.publishStopped(StopDeviceGone, "gone")
	if e, ok := nextEvent(t, sub); ok {
		t.Errorf("%+v after the caller's subscription ended; want nothing", e)
	}
}

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The screenshots, logs and Logcat of a remote device end to end (plan m2t step 5; ADR-0099
// §2, ADR-0100 §3, ADR-0012 §Reading artifacts, ADR-0041, ADR-0042): two daemons paired over
// real TLS on loopback, the host's fake adb as real processes (a Logcat stream is a recorded
// child), and a second paired peer, bob, for the ownership rows.

// remoteLog is the host phone's log: both lines are due before its device clock.
const remoteLog = "--------- beginning of main\n" +
	"1696118000.000001 10146 6406 6406 I App: first\n" +
	"1696118000.000002 10146 6406 6406 I App: second\n"

// artifactsLab is a lab whose host phone has a screenshot and a log, and lends to bob as well.
type artifactsLab struct {
	*remoteLab
	raw      string // the host's screencap, byte for byte
	bobState string
	bobID    *hosting.Identity
}

func newArtifactsLab(t *testing.T) *artifactsLab { return newArtifactsLabWith(t, labOptions{}) }

func newArtifactsLabWith(t *testing.T, o labOptions) *artifactsLab {
	t.Helper()
	bobState := shortDir(t)
	bobID, _, err := hosting.LoadIdentity(bobState)
	if err != nil {
		t.Fatal(err)
	}
	raw := fixtureFile(t, "pixel9a-screencap.png")
	o.hostProcesses = true
	o.hostFiles = map[string]string{phone1 + ".screencap.out": raw, phone1 + ".log": remoteLog}
	o.extraPeers = []labPeer{{"bob", bobID.Fingerprint()}}
	lab := newRemoteLabWith(t, o)
	return &artifactsLab{remoteLab: lab, raw: raw, bobState: bobState, bobID: bobID}
}

// bobLink is a link to the host as bob, who is lent the phone too.
func (l *artifactsLab) bobLink(t *testing.T) *hosting.Link {
	t.Helper()
	addr := l.host.host.Health().Hosting.Listen
	configAt(t, l.bobState, `{"remotes":[{"name":"qa","address":"`+addr+`","fingerprint":"`+l.host.host.Identity().Fingerprint()+`"}]}`)
	bob := clientHost(t, l.bobState)
	bob.Start()
	t.Cleanup(bob.Stop)
	eventually(t, "bob's link to come up", func() bool { return bob.Remote("qa").Connected() })
	return bob.Remote("qa")
}

// shotRef is the host's reference of a screenshot a peer took over its link.
func shotRef(t *testing.T, link *hosting.Link) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := link.Call(ctx, capability.RequestEnvelope{Capability: "device.screenshot", Params: json.RawMessage(`{"device":"` + phone1 + `"}`)})
	if err != nil || resp.Error != nil {
		t.Fatalf("a peer's device.screenshot: %v %+v", err, resp.Error)
	}
	var r artifact.Ref
	if err := json.Unmarshal(resp.Result, &r); err != nil || r.Ref == "" {
		t.Fatalf("a peer's device.screenshot result %s: %v", resp.Result, err)
	}
	return r.Ref
}

func idOf(ref string) string { return strings.TrimPrefix(ref, artifact.RefPrefix) }

// fetchStatus is the status the host answers a fetch of id with, or 200 and the bytes.
func fetchStatus(t *testing.T, link *hosting.Link, id string) (status int, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := link.Artifact(ctx, id, 64<<20)
	if err == nil {
		return http.StatusOK, a.Data
	}
	if refused, ok := err.(*hosting.ErrArtifactRefused); ok {
		return refused.Status, nil
	}
	t.Fatalf("fetching %s: %v", id, err)
	return 0, nil
}

// TestRemoteArtifactsAndLogs: plan m2t step 5's check.
//
//   - A screenshot of a remote device is stored in the client's store under the client's own
//     reference: the bytes are the host's, the route serves them with X-Droidyard-Untrusted,
//     the client ran no adb for the device, and the host's reference is nowhere in the
//     result. Seen failing with the host's reference passed through.
//   - A device.logs capture likewise, readable with artifact.read, untrusted.
//   - A peer fetches the artifacts of its own calls and no other: another peer's id, a host
//     UI screenshot's id, a durable id, an id that never was, and (over the session) the same
//     through artifact.read, are all 404 or ARTIFACT_NOT_FOUND. Seen failing with the owner
//     check removed.
//   - MCP device_screenshot on the remote id returns its image.
//   - device.watchLogs on the remote id delivers the host's lines with the remote id; ending
//     the client's subscription, or its session, ends the host's logcat child; a subscribe
//     the client abandons leaves nothing on the host; the link cut ends the stream with
//     deviceGone.
func TestRemoteArtifactsAndLogs(t *testing.T) {
	t.Parallel()

	t.Run("a screenshot is stored locally", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		env := lab.on(t, "device.screenshot", `{`+dev(remotePhone)+`}`)
		if env.Error != nil || !untrusted(env) {
			t.Fatalf("device.screenshot %s: %+v, untrusted %v", remotePhone, env.Error, untrusted(env))
		}
		var res device.ScreenshotResult
		if err := json.Unmarshal(env.Result, &res); err != nil {
			t.Fatal(err)
		}
		e, err := lab.client.store.Get(res.Ref.Ref)
		if err != nil {
			t.Fatalf("the result's reference %s is not in the client's store: %v (the host's reference passed through?)", res.Ref.Ref, err)
		}
		if string(e.Data) != lab.raw || !e.Ref.Untrusted || res.Width == 0 {
			t.Errorf("the client's artifact: %d bytes (want the host's %d), untrusted %v", len(e.Data), len(lab.raw), e.Ref.Untrusted)
		}
		addr := lab.client.d.HTTPAddr()
		resp := artifactRequest(portOf(t, addr), lab.client.token, "/v1/artifacts/"+idOf(res.Ref.Ref)).send(t, addr)
		body := bodyOf(t, resp)
		if resp.StatusCode != 200 || resp.Header.Get("X-Droidyard-Untrusted") != "true" || resp.Header.Get("Content-Type") != "image/png" || string(body) != lab.raw {
			t.Errorf("the client's artifact route: %d, untrusted %q, %s, %d bytes; want 200, true, image/png, the host's %d", resp.StatusCode, resp.Header.Get("X-Droidyard-Untrusted"), resp.Header.Get("Content-Type"), len(body), len(lab.raw))
		}
		for _, c := range lab.clientCalls(t) {
			if strings.Contains(c, phone1) {
				t.Errorf("the client's adb was asked %q for a remote device", c)
			}
		}
		if hostCalls := lab.hostCalls(t); !hasScreencap(hostCalls) {
			t.Errorf("the host's adb ran no screencap: %v", hostCalls)
		}
	})

	t.Run("a log capture is stored locally", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		env := lab.on(t, "device.logs", `{`+dev(remotePhone)+`}`)
		if env.Error != nil || !untrusted(env) {
			t.Fatalf("device.logs %s: %+v, untrusted %v", remotePhone, env.Error, untrusted(env))
		}
		var res device.LogsResult
		if err := json.Unmarshal(env.Result, &res); err != nil {
			t.Fatal(err)
		}
		if res.Device != remotePhone || len(res.Lines) != 2 || res.Lines[0].Message != "first" || res.Capture == nil {
			t.Fatalf("device.logs result %+v; want %s, the two lines and a capture", res, remotePhone)
		}
		e, err := lab.client.store.Get(res.Capture.Ref)
		if err != nil {
			t.Fatalf("the capture's reference %s is not in the client's store: %v", res.Capture.Ref, err)
		}
		if !strings.Contains(string(e.Data), "first") || !strings.Contains(string(e.Data), "second") || !e.Ref.Untrusted || e.Ref.MediaType != "text/plain" {
			t.Errorf("the client's capture %q (untrusted %v, %s)", e.Data, e.Ref.Untrusted, e.Ref.MediaType)
		}
		read := lab.on(t, "artifact.read", `{"ref":"`+res.Capture.Ref+`"}`)
		var rr artifacts.ReadResult
		if read.Error != nil || json.Unmarshal(read.Result, &rr) != nil || rr.Text == nil || !strings.Contains(*rr.Text, "second") || !untrusted(read) {
			t.Errorf("artifact.read of the capture: %+v %s, untrusted %v", read.Error, read.Result, untrusted(read))
		}
		for _, c := range lab.clientCalls(t) {
			if strings.Contains(c, phone1) {
				t.Errorf("the client's adb was asked %q for a remote device", c)
			}
		}
	})

	t.Run("a peer reads its own artifacts only", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		anna, bob := lab.client.host.Remote("qa"), lab.bobLink(t)
		annaRef, bobRef := shotRef(t, anna), shotRef(t, bob)
		hostEnv, code := lab.host.call(t, "device.screenshot", "--params", `{"device":"`+phone1+`"}`)
		var hostShot artifact.Ref
		if code != 0 || hostEnv.Error != nil || json.Unmarshal(hostEnv.Result, &hostShot) != nil {
			t.Fatalf("the host's own screenshot: exit %d, %+v", code, hostEnv.Error)
		}

		for _, c := range []struct {
			who    string
			link   *hosting.Link
			id     string
			status int
		}{
			{"anna, her own", anna, idOf(annaRef), 200},
			{"bob, his own", bob, idOf(bobRef), 200},
			{"anna, bob's", anna, idOf(bobRef), 404},
			{"bob, anna's", bob, idOf(annaRef), 404},
			{"anna, a host UI screenshot", anna, idOf(hostShot.Ref), 404},
			{"bob, a host UI screenshot", bob, idOf(hostShot.Ref), 404},
			// A guess at a live artifact's counter is 404, as an id that never was: not a 410
			// that names it (and its producer) as once valid.
			{"anna, forged bits on a live host artifact", anna, forgedID(idOf(hostShot.Ref)), 404},
			{"anna, forged bits on bob's", anna, forgedID(idOf(bobRef)), 404},
			{"anna, an id that never was", anna, "e." + strings.Split(idOf(annaRef), ".")[1] + ".99999.0123456789ab", 404},
		} {
			status, body := fetchStatus(t, c.link, c.id)
			if status != c.status || (c.status == 200 && string(body) != lab.raw) {
				t.Errorf("%s: status %d (%d bytes); want %d", c.who, status, len(body), c.status)
			}
		}

		// What the client's link refuses to send is refused by the host as well: a durable id
		// and a path are 404 straight over TLS.
		addr := lab.host.host.Health().Hosting.Listen
		hostFP := lab.host.host.Identity().Fingerprint()
		for _, path := range []string{"d.0123456789abcdef.1", "d.0123456789abcdef.1/../x", idOf(annaRef) + "/x", ".."} {
			cfg := hosting.PinnedConfig(lab.client.host.Identity(), func(fp string) bool { return fp == hostFP })
			hc := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
			resp, err := hc.Get("https://" + addr + capability.PeerArtifactsPath + path)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 404 {
				t.Errorf("GET %s%s: %d; want 404", capability.PeerArtifactsPath, path, resp.StatusCode)
			}
		}

		// artifact.read over the session follows the same rule.
		read := func(link *hosting.Link, ref string) capability.ResponseEnvelope {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := link.Call(ctx, capability.RequestEnvelope{Capability: "artifact.read", Params: json.RawMessage(`{"ref":"` + ref + `"}`)})
			if err != nil {
				t.Fatalf("artifact.read over the link: %v", err)
			}
			return resp
		}
		if resp := read(anna, annaRef); resp.Error != nil {
			t.Errorf("anna's artifact.read of her own: %+v", resp.Error)
		}
		for name, c := range map[string]struct {
			link *hosting.Link
			ref  string
		}{
			"bob's by anna":        {anna, bobRef},
			"a host UI one":        {anna, hostShot.Ref},
			"a durable reference":  {anna, artifact.RefPrefix + "d.0123456789abcdef.1"},
			"anna's by bob":        {bob, annaRef},
			"a host UI one by bob": {bob, hostShot.Ref},
		} {
			if resp := read(c.link, c.ref); resp.Error == nil || resp.Error.Code != capability.ErrArtifactNotFound {
				t.Errorf("artifact.read of %s: %+v %s; want ARTIFACT_NOT_FOUND", name, resp.Error, resp.Result)
			}
		}
		// The host's own artifact.read still reads a screenshot of its own, and not a peer's
		// being unreadable to the peer takes nothing from the host.
		if env, code := lab.host.call(t, "artifact.read", "--params", `{"ref":"`+hostShot.Ref+`"}`); code != 0 || env.Error != nil {
			t.Errorf("the host's own artifact.read: exit %d, %+v", code, env.Error)
		}
	})

	t.Run("MCP device_screenshot on the remote id", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		c := startShim(t, lab.client.state, "client", "--wait", "5s")
		c.initialize("remote")
		c.callTool(1, "device_screenshot", `{"device":"`+remotePhone+`"}`)
		shot := decodeImageCall(t, c.recv(), 1)
		if shot.isError {
			t.Fatalf("device_screenshot on %s: %s", remotePhone, shot.raw["content"])
		}
		img, mime := shot.image(t)
		if mime != "image/png" || longEdge(img) != artifact.InlineMaxEdge {
			t.Errorf("the inline image %s %v; want image/png with a long edge of %d", mime, img.Bounds(), artifact.InlineMaxEdge)
		}
		if m := metaOf(t, shot.toolResult); m["droidyard/untrusted"] != true {
			t.Errorf("_meta = %v; want untrusted", m)
		}
		c.exit()
	})

	t.Run("watchLogs delivers the host's lines with the remote id", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		baseline := lab.host.bus.Subscriptions()
		w := wsConnect(t, lab.client)
		sub, early := lab.watchLogs(t, w, "l1")
		got := lab.lines(t, w, sub, early, 2)
		if strings.Join(got, ",") != "first,second" {
			t.Fatalf("the lines are %v; want first,second", got)
		}
		waitFor(t, "the host's logcat child", func() bool { return lab.host.adb.LiveStreams() == 1 && len(streamRecords(t, lab.host.state)) == 1 })

		// Ending the client's subscription ends the host's logcat child.
		w.send("u1", "events.unsubscribe", "", `{"subscriptionId":"`+sub+`"}`, nil)
		w.collect("u1")
		waitFor(t, "the host's logcat child to end after the client unsubscribed", func() bool {
			return lab.host.adb.LiveStreams() == 0 && len(streamRecords(t, lab.host.state)) == 0
		})
		waitFor(t, "the host's subscription to end", func() bool { return lab.host.bus.Subscriptions() == baseline })

		// Ending the client's session does too.
		w2 := wsConnect(t, lab.client)
		sub2, early2 := lab.watchLogs(t, w2, "l2")
		lab.lines(t, w2, sub2, early2, 2)
		waitFor(t, "a second host stream", func() bool { return lab.host.adb.LiveStreams() == 1 })
		w2.c.Close(1000, "")
		waitFor(t, "the host's logcat child to end after the client's session did", func() bool {
			return lab.host.adb.LiveStreams() == 0 && len(streamRecords(t, lab.host.state)) == 0
		})
		for _, c := range lab.clientCalls(t) {
			if strings.Contains(c, phone1) {
				t.Errorf("the client's adb was asked %q for a remote device", c)
			}
		}
	})

	t.Run("a subscribe the client abandons leaves nothing on the host", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		devicetest.Write(t, lab.hostDir, "devices.delay", []byte("1500"))
		baseline, clientBaseline := lab.host.bus.Subscriptions(), lab.client.bus.Subscriptions()
		w := wsConnect(t, lab.client)
		w.send("l1", "device.watchLogs", "", `{"device":"`+remotePhone+`","tail":10}`, nil)
		time.Sleep(300 * time.Millisecond) // the host is still resolving the device
		w.c.CloseNow()
		// The host finishes its start after the client left; whatever it opened is ended.
		time.Sleep(3 * time.Second)
		waitFor(t, "nothing of the abandoned stream on the host", func() bool {
			return lab.host.adb.LiveStreams() == 0 && len(streamRecords(t, lab.host.state)) == 0 && lab.host.bus.Subscriptions() == baseline
		})
		waitFor(t, "nothing of the abandoned stream on the client", func() bool { return lab.client.bus.Subscriptions() == clientBaseline })
	})

	t.Run("the link cut ends the stream with deviceGone", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		w := wsConnect(t, lab.client)
		sub, early := lab.watchLogs(t, w, "l1")
		lab.lines(t, w, sub, early, 2)

		lab.stopHost()
		for {
			f := w.must("the stopped event")
			if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.logcat" {
				t.Fatalf("a frame that is not a device.logcat event of %s: %+v", sub, f)
			}
			if f.Event.Type != "stopped" {
				continue
			}
			var p device.StoppedEvent
			if err := json.Unmarshal(f.Event.Payload, &p); err != nil || p.Reason != "deviceGone" || p.Device != remotePhone {
				t.Errorf("stopped %s (%v); want deviceGone for %s", f.Event.Payload, err, remotePhone)
			}
			break
		}
		if f, ok := w.next(500 * time.Millisecond); ok {
			t.Errorf("a frame after stopped: %+v", f)
		}
	})
}

func hasScreencap(calls []string) bool {
	for _, c := range calls {
		if strings.Contains(c, "screencap") {
			return true
		}
	}
	return false
}

// watchLogs sends device.watchLogs for the remote phone and returns the subscription and the
// event frames that arrived before the response.
func (l *artifactsLab) watchLogs(t *testing.T, w *wsClient, reqID string) (sub string, early []wsFrame) {
	t.Helper()
	w.send(reqID, "device.watchLogs", "", `{"device":"`+remotePhone+`","tail":10}`, nil)
	early, f := w.collect(reqID)
	if f.Error != nil {
		t.Fatalf("device.watchLogs %s: %+v", remotePhone, f.Error)
	}
	var r device.WatchLogsResult
	if err := json.Unmarshal(f.Result, &r); err != nil || r.Device != remotePhone || r.SubscriptionID == "" {
		t.Fatalf("device.watchLogs result %s: %v; want device %s", f.Result, err, remotePhone)
	}
	if !untrusted(f.ResponseEnvelope) {
		t.Errorf("a remote device's watchLogs is not marked untrusted: %+v", f.Meta)
	}
	return r.SubscriptionID, early
}

// lines reads lines events of sub, after the frames already seen, until n lines have
// arrived, and returns their messages; every event names the remote id and is untrusted.
func (l *artifactsLab) lines(t *testing.T, w *wsClient, sub string, seen []wsFrame, n int) []string {
	t.Helper()
	var msgs []string
	frame := func(f wsFrame) {
		if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.logcat" || f.Event.Type != "lines" {
			t.Fatalf("a frame that is not a device.logcat lines event of %s: %+v", sub, f)
		}
		var le device.LinesEvent
		if err := json.Unmarshal(f.Event.Payload, &le); err != nil || le.Device != remotePhone || !le.Untrusted {
			t.Fatalf("lines payload %s: %v; want device %s, untrusted", f.Event.Payload, err, remotePhone)
		}
		for _, line := range le.Lines {
			msgs = append(msgs, line.Message)
		}
	}
	for _, f := range seen {
		frame(f)
	}
	for len(msgs) < n {
		frame(w.must("a lines event"))
	}
	return msgs
}

// logRouter sorts the frames of a WebSocket session by the subscription they belong to: the
// messages of each lines event and the reason of each stopped event.
type logRouter struct {
	msgs    map[string][]string
	stopped map[string]string
}

func newLogRouter() *logRouter {
	return &logRouter{msgs: map[string][]string{}, stopped: map[string]string{}}
}

func (r *logRouter) add(t *testing.T, f wsFrame) {
	t.Helper()
	if f.Event == nil || f.Event.Topic != "device.logcat" {
		t.Fatalf("a frame that is not a device.logcat event: %+v", f)
	}
	switch f.Event.Type {
	case "lines":
		var le device.LinesEvent
		if err := json.Unmarshal(f.Event.Payload, &le); err != nil {
			t.Fatal(err)
		}
		for _, l := range le.Lines {
			r.msgs[f.SubscriptionID] = append(r.msgs[f.SubscriptionID], l.Message)
		}
	case "stopped":
		var se device.StoppedEvent
		if err := json.Unmarshal(f.Event.Payload, &se); err != nil {
			t.Fatal(err)
		}
		r.stopped[f.SubscriptionID] = se.Reason
	}
}

// pump reads frames of w into the router until done is true.
func (r *logRouter) pump(t *testing.T, w *wsClient, what string, done func() bool) {
	t.Helper()
	for !done() {
		r.add(t, w.must(what))
	}
}

func (r *logRouter) saw(sub, msg string) bool {
	for _, m := range r.msgs[sub] {
		if m == msg {
			return true
		}
	}
	return false
}

// hostLogRecords is how many logcat children the host's supervisor has a pid record for.
func (l *artifactsLab) hostLogRecords(t *testing.T) int { return len(streamRecords(t, l.host.state)) }

// appendHostLog adds a line to the host phone's log, due before its device clock and after
// the lines already there.
func (l *artifactsLab) appendHostLog(t *testing.T, msg string) {
	t.Helper()
	devicetest.Write(t, l.hostDir, phone1+".log", []byte(remoteLog+"1696118100.000000 10146 6406 6406 I App: "+msg+"\n"))
}

// TestRemoteLogsManyStreams: a client's Logcat tabs share one link session on the host, so
// the host bounds a peer's streams of a device by count instead of replacing them by session
// (plan m2t step 5, the owner's use case: two tabs, or a tab and a second client session, on
// one remote phone).
//
//   - Two client sessions watch the same remote device at once and both keep receiving
//     lines after the other started. Seen failing with the host replacing a peer's stream
//     by session and device, as it does a local one: the first session got stopped.
//   - A fifth stream of one peer on the device replaces the peer's oldest (stopped
//     replaced), the others keep streaming, and the host's logcat children never exceed 4.
//     Seen failing with the cap raised: five children, and the first stream not replaced.
//
// (A local session's second stream still replaces its first: TestWSWatchLogsStopped.)
func TestRemoteLogsManyStreams(t *testing.T) {
	t.Parallel()

	t.Run("two client sessions on one remote device", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		w1, w2 := wsConnect(t, lab.client), wsConnect(t, lab.client)
		r1, r2 := newLogRouter(), newLogRouter()
		sub1, early1 := lab.watchLogs(t, w1, "a")
		for _, f := range early1 {
			r1.add(t, f)
		}
		r1.pump(t, w1, "the first session's history", func() bool { return r1.saw(sub1, "second") })
		sub2, early2 := lab.watchLogs(t, w2, "b")
		for _, f := range early2 {
			r2.add(t, f)
		}
		r2.pump(t, w2, "the second session's history", func() bool { return r2.saw(sub2, "second") })
		waitFor(t, "two host streams", func() bool { return lab.host.adb.LiveStreams() == 2 && lab.hostLogRecords(t) == 2 })

		lab.appendHostLog(t, "after-both")
		r1.pump(t, w1, "the first session after the second started", func() bool { return r1.saw(sub1, "after-both") })
		r2.pump(t, w2, "the second session", func() bool { return r2.saw(sub2, "after-both") })
		if r1.stopped[sub1] != "" || r2.stopped[sub2] != "" {
			t.Errorf("stopped: %q and %q; want neither stream ended", r1.stopped[sub1], r2.stopped[sub2])
		}
	})

	t.Run("a fifth client session replaces the oldest", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)

		var peak atomic.Int64
		stopSampling := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for {
				select {
				case <-stopSampling:
					return
				case <-time.After(2 * time.Millisecond):
				}
				n := int64(len(streamRecords(t, lab.host.state)))
				if n > peak.Load() {
					peak.Store(n)
				}
			}
		}()
		var ws []*wsClient
		var rs []*logRouter
		var subs []string
		for i := range 5 {
			w, r := wsConnect(t, lab.client), newLogRouter()
			sub, early := lab.watchLogs(t, w, "s"+string(rune('1'+i)))
			for _, f := range early {
				r.add(t, f)
			}
			ws, rs, subs = append(ws, w), append(rs, r), append(subs, sub)
		}
		close(stopSampling)
		<-sampled
		if peak.Load() > 4 {
			t.Errorf("the host had %d logcat children at once; want at most 4", peak.Load())
		}

		rs[0].pump(t, ws[0], "the oldest stream's end", func() bool { return rs[0].stopped[subs[0]] != "" })
		if rs[0].stopped[subs[0]] != "replaced" {
			t.Errorf("the oldest stream stopped with %q; want replaced", rs[0].stopped[subs[0]])
		}
		waitFor(t, "four host streams", func() bool { return lab.host.adb.LiveStreams() == 4 && lab.hostLogRecords(t) == 4 })

		lab.appendHostLog(t, "after-five")
		for i := 1; i < 5; i++ {
			rs[i].pump(t, ws[i], "a stream that remains", func() bool { return rs[i].saw(subs[i], "after-five") })
			if rs[i].stopped[subs[i]] != "" {
				t.Errorf("stream %d stopped with %q; only the oldest was to be replaced", i+1, rs[i].stopped[subs[i]])
			}
		}
		if rs[0].saw(subs[0], "after-five") {
			t.Error("the replaced stream kept receiving lines")
		}
	})

	// The new child starts only after the evicted one is reaped: the evicted child's reaping
	// is held, and the fifth stream's call must stay pending, with no fifth child, until it is
	// released. Seen failing with the wait for the evicted stream removed: the call answered
	// at once and the host ran five children.
	t.Run("the new child waits for the evicted one to be reaped", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		var once sync.Once
		free := func() { once.Do(func() { close(release) }) }
		t.Cleanup(free)
		var started atomic.Int64
		lab := newArtifactsLabWith(t, labOptions{hostWrapStream: func(real device.StreamStarter) device.StreamStarter {
			return func(cmd *exec.Cmd, kind string) (device.Stream, error) {
				st, err := real(cmd, kind)
				if err != nil {
					return nil, err
				}
				if started.Add(1) == 1 { // the oldest stream's child: its reaping is held
					return heldWait{Stream: st, release: release}, nil
				}
				return st, nil
			}
		}})
		var ws []*wsClient
		for i := range 4 {
			w := wsConnect(t, lab.client)
			lab.watchLogs(t, w, "s"+string(rune('1'+i)))
			ws = append(ws, w)
		}
		waitFor(t, "four children", func() bool { return lab.hostLogRecords(t) == 4 })

		w5 := wsConnect(t, lab.client)
		w5.send("s5", "device.watchLogs", "", `{"device":"`+remotePhone+`","tail":10}`, nil)
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if f, ok := w5.next(100 * time.Millisecond); ok && f.ID == "s5" {
				t.Fatalf("the fifth stream was answered while the evicted child was not reaped: %+v", f.ResponseEnvelope)
			}
			if n := lab.hostLogRecords(t); n != 4 {
				t.Fatalf("the host has %d logcat children while the evicted one is not reaped; want 4", n)
			}
		}
		free()
		early, f := w5.collect("s5")
		if f.Error != nil {
			t.Fatalf("the fifth stream once the evicted child was reaped: %+v", f.Error)
		}
		_ = early
		waitFor(t, "four children again", func() bool { return lab.hostLogRecords(t) == 4 && lab.host.adb.LiveStreams() == 4 })
	})

	// A client session keeps ADR-0041's rule for itself: its second stream of a remote device
	// ends its first (stopped replaced), and the host's stream of the first is ended, so the
	// host's cap of 4 only ever separates distinct client sessions. Seen failing with the
	// client not replacing: the first stream kept streaming and the host held two.
	t.Run("a client session's second stream replaces its first", func(t *testing.T) {
		t.Parallel()
		lab := newArtifactsLab(t)
		w, r := wsConnect(t, lab.client), newLogRouter()
		sub1, early1 := lab.watchLogs(t, w, "a")
		for _, f := range early1 {
			r.add(t, f)
		}
		r.pump(t, w, "the first stream's history", func() bool { return r.saw(sub1, "second") })
		sub2, early2 := lab.watchLogs(t, w, "b")
		for _, f := range early2 {
			r.add(t, f)
		}
		r.pump(t, w, "the first stream's end", func() bool { return r.stopped[sub1] != "" && r.saw(sub2, "second") })
		if r.stopped[sub1] != "replaced" {
			t.Errorf("the first stream stopped with %q; want replaced", r.stopped[sub1])
		}
		waitFor(t, "the host to hold the second stream only", func() bool { return lab.host.adb.LiveStreams() == 1 && lab.hostLogRecords(t) == 1 })
		lab.appendHostLog(t, "after-replace")
		r.pump(t, w, "the second stream", func() bool { return r.saw(sub2, "after-replace") })
		if r.saw(sub1, "after-replace") || r.stopped[sub2] != "" {
			t.Errorf("the first stream kept receiving lines, or the second stopped (%q)", r.stopped[sub2])
		}
	})

	// A stream evicted while it still waits for its own evictee is a stream that stopped
	// replaced, not an adb failure, and the stream that evicts it waits for the oldest child
	// too, so the host never runs more than 4 children. The oldest child's reaping is held;
	// the fifth stream waits for it; four more arrive, the last one evicting the fifth. Seen
	// failing with the evicted stream's start failing on its cancelled context (DEVICE_ADB_FAILED
	// or the call hanging), and with the sixth waiting only for the fifth (five children).
	t.Run("a stream evicted while it waits for its own evictee", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		var once sync.Once
		free := func() { once.Do(func() { close(release) }) }
		t.Cleanup(free)
		var started atomic.Int64
		lab := newArtifactsLabWith(t, labOptions{hostWrapStream: func(real device.StreamStarter) device.StreamStarter {
			return func(cmd *exec.Cmd, kind string) (device.Stream, error) {
				st, err := real(cmd, kind)
				if err != nil {
					return nil, err
				}
				if started.Add(1) == 1 {
					return heldWait{Stream: st, release: release}, nil
				}
				return st, nil
			}
		}})
		var peak atomic.Int64
		stopSampling := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for {
				select {
				case <-stopSampling:
					return
				case <-time.After(2 * time.Millisecond):
				}
				if n := int64(len(streamRecords(t, lab.host.state))); n > peak.Load() {
					peak.Store(n)
				}
			}
		}()
		for i := range 4 {
			lab.watchLogs(t, wsConnect(t, lab.client), "s"+string(rune('1'+i)))
		}
		waitFor(t, "four children", func() bool { return lab.hostLogRecords(t) == 4 })

		send := func(id string) *wsClient {
			w := wsConnect(t, lab.client)
			w.send(id, "device.watchLogs", "", `{"device":"`+remotePhone+`","tail":10}`, nil)
			return w
		}
		fifth := send("b")
		waitFor(t, "the fifth stream to evict the oldest", func() bool { return lab.host.adb.LiveStreams() == 5 })
		for _, id := range []string{"c", "d", "e"} {
			w := send(id)
			_, f := w.collect(id)
			if f.Error != nil {
				t.Fatalf("stream %s: %+v", id, f.Error)
			}
		}
		sixth := send("f") // evicts the fifth, which is still waiting for the oldest child
		early, f := fifth.collect("b")
		if f.Error != nil {
			t.Fatalf("the evicted fifth stream: %+v; want a stream that stopped replaced", f.Error)
		}
		r := newLogRouter()
		for _, e := range early {
			r.add(t, e)
		}
		sub := subscriptionOf(t, f)
		r.pump(t, fifth, "the evicted fifth stream's stopped event", func() bool { return r.stopped[sub] != "" })
		if r.stopped[sub] != "replaced" {
			t.Errorf("the evicted fifth stream stopped with %q; want replaced", r.stopped[sub])
		}
		// The sixth waits for the oldest child as well.
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if g, ok := sixth.next(100 * time.Millisecond); ok && g.ID == "f" {
				t.Fatalf("the sixth stream was answered while the oldest child was not reaped: %+v", g.ResponseEnvelope)
			}
		}
		free()
		if _, g := sixth.collect("f"); g.Error != nil {
			t.Fatalf("the sixth stream once the oldest child was reaped: %+v", g.Error)
		}
		close(stopSampling)
		<-sampled
		if peak.Load() > 4 {
			t.Errorf("the host had %d logcat children at once; want at most 4", peak.Load())
		}
	})
}

// forgedID is id with its random bits replaced: a guess at a live artifact's counter.
func forgedID(id string) string { return id[:strings.LastIndex(id, ".")+1] + "000000000000" }

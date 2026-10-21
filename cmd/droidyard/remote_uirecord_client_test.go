package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/artifacts"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// A remote device's recording from the client (plan m2x step 10; ADR-0115 Decisions 2, 3 and 5,
// ADR-0100 §2 to §4 and §6): two daemons paired over real TLS on loopback, the client anna and
// the host with a fake UI reader behind the lent phone (the lease lab), a TCP proxy between them
// that a test can cut, hold and restore, and the host's recorder, its peer route and its
// recordings directory real. The client's calls are made on the remote id, the client's own
// reference is read, and the host's files are the oracle.

const remoteArgs = `{"device":"` + remotePhone + `"}`

// linkProxy is a TCP proxy between the client and the host. The link between them is TLS
// pinned end to end, so the proxy sees nothing and can only cut the connections, refuse new
// ones until it is restored, or hold what the host sends (to lose an answer deterministically).
type linkProxy struct {
	addr   string
	target string
	ln     net.Listener
	mu     sync.Mutex
	cond   *sync.Cond
	cut    bool
	hold   bool
	conns  map[net.Conn]bool
}

func newLinkProxy(t *testing.T, target string) *linkProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &linkProxy{addr: ln.Addr().String(), target: target, ln: ln, conns: map[net.Conn]bool{}}
	p.cond = sync.NewCond(&p.mu)
	go p.accept()
	t.Cleanup(func() {
		ln.Close()
		p.Cut()
	})
	return p
}

func (p *linkProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.serve(c)
	}
}

func (p *linkProxy) serve(client net.Conn) {
	p.mu.Lock()
	refuse := p.cut
	p.mu.Unlock()
	server, err := net.Dial("tcp", p.target)
	if refuse || err != nil {
		client.Close()
		if err == nil {
			server.Close()
		}
		return
	}
	p.mu.Lock()
	p.conns[client], p.conns[server] = true, true
	p.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.pump(client, server, false) }()
	go func() { defer wg.Done(); p.pump(server, client, true) }()
	wg.Wait()
	p.mu.Lock()
	delete(p.conns, client)
	delete(p.conns, server)
	p.mu.Unlock()
	client.Close()
	server.Close()
}

// pump copies src to dst; what comes from the host (fromHost) waits while the proxy holds and
// is dropped when it is cut.
func (p *linkProxy) pump(src, dst net.Conn, fromHost bool) {
	defer src.Close()
	defer dst.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.mu.Lock()
			for fromHost && p.hold && !p.cut {
				p.cond.Wait()
			}
			cut := p.cut
			p.mu.Unlock()
			if cut {
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Hold keeps what the host sends from reaching the client until Restore or Cut.
func (p *linkProxy) Hold() { p.mu.Lock(); p.hold = true; p.mu.Unlock() }

// Cut closes every connection, drops what was held and refuses new connections.
func (p *linkProxy) Cut() {
	p.mu.Lock()
	p.cut, p.hold = true, false
	for c := range p.conns {
		c.Close()
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Restore lets the client connect again.
func (p *linkProxy) Restore() {
	p.mu.Lock()
	p.cut, p.hold = false, false
	p.cond.Broadcast()
	p.mu.Unlock()
}

// invokeAs is invoke for a call whose origin is client kind and session.
func (p *inProcess) invokeAs(client capability.ClientKind, session, capName, params string) capability.ResponseEnvelope {
	req := capability.Request{Capability: capName, Client: client, Origin: capability.Origin{ID: session}}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	out, err := p.ops.Invoke(context.Background(), req)
	if err != nil {
		return capability.ResponseEnvelope{Error: capability.AsError(err)}
	}
	env := capability.ResponseEnvelope{Error: out.Error, Meta: out.Meta()}
	if out.Result != nil {
		env.Result, _ = json.Marshal(out.Result)
	}
	return env
}

// restartClient stops the client's daemon and starts another on the state it left, with its
// peers; the caller waits for the link if it wants it.
func (l *remoteLab) restartClient(t *testing.T) {
	t.Helper()
	l.stopClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	l.client.d.Shutdown(ctx)
	seam := &adbSeam{env: devicetest.Env(t, l.clientDir, unusedPort(t)), exec: devicetest.Exec(l.clientDir), state: l.client.state}
	l.client = startInProcessADB(t, "127.0.0.1:0", nil, seam)
	stop := startPeers(l.client.host, l.client.reg, l.client.ops, l.client.bus, l.client.store, l.client.adb, os.Stderr)
	var once sync.Once
	l.stopClient = func() { once.Do(stop) }
	t.Cleanup(l.stopClient)
}

// newRecordingLab is the UI lab with a proxy between the client and the host, and the
// supervisors' and the copy's timings short: ranges of rangeBytes bytes.
func newRecordingLab(t *testing.T, rangeBytes int64) (*leaseLab, *devicetest.FakeReader) {
	t.Helper()
	t.Cleanup(device.SetRemoteRecordingTimings(100*time.Millisecond, 50*time.Millisecond, rangeBytes))
	lab, r := newUILab(t, func(o *labOptions) { o.viaProxy = true })
	docs := make([]string, 8)
	for i := range docs {
		docs[i] = strings.Replace(remoteUIDump, "hello &amp; welcome", fmt.Sprintf("screen %d", i), 1)
	}
	r.SetFullDocs(docs...)
	return lab, r
}

// startRemote starts a recording of the remote phone as the MCP session agent-1 of the client.
func startRemote(t *testing.T, lab *leaseLab) device.UIRecording {
	t.Helper()
	env := lab.client.invokeAs(capability.ClientMCP, "agent-1", "device.recordUi", remoteArgs)
	rec := recordingOf(t, "the client's recordUi on "+remotePhone, env)
	if !untrusted(env) {
		t.Errorf("a remote recording's start is not marked untrusted: %+v", env.Meta)
	}
	return rec
}

// hostOnly is the one recording the host holds.
func hostOnly(t *testing.T, lab *leaseLab) device.UIRecording {
	t.Helper()
	got := listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", ""))
	if len(got) != 1 {
		t.Fatalf("the host holds %v; want one recording", ids(got))
	}
	return got[0]
}

// grow adds n clicks to the host's phone, each followed by a dump the host stores.
func grow(t *testing.T, lab *leaseLab, r *devicetest.FakeReader, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		r.AddEvents(clickAt(i))
		waitFor(t, fmt.Sprintf("the host's dump %d", i+1), func() bool { return hostOnly(t, lab).Dumps >= i+1 })
	}
}

// clientRec is the client's own listing of recording id.
func clientRec(t *testing.T, lab *leaseLab, id string) (device.UIRecording, bool) {
	t.Helper()
	got := listedOf(t, "the client's uiRecordings", lab.client.invoke("device.uiRecordings", `{"id":"`+id+`"}`))
	if len(got) == 0 {
		return device.UIRecording{}, false
	}
	return got[0], true
}

// waitClient waits until the client's listing of id satisfies ok and returns it.
func waitClient(t *testing.T, lab *leaseLab, id, what string, ok func(device.UIRecording) bool) device.UIRecording {
	t.Helper()
	var rec device.UIRecording
	testtime.Eventually(t, testtime.Bound(40*time.Second), "the client's recording to be "+what, func() bool {
		r, found := clientRec(t, lab, id)
		rec = r
		return found && ok(r)
	})
	return rec
}

func copiedRec(r device.UIRecording) bool { return r.Copied }

// clientFile is the client's file of recording id.
func clientFile(lab *leaseLab, id, name string) string {
	return filepath.Join(lab.client.state, "recordings", id, name)
}

func sumOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// readRef is artifact.read of ref on the client.
func readRef(t *testing.T, lab *leaseLab, ref string) artifacts.ReadResult {
	t.Helper()
	env := lab.client.invoke("artifact.read", `{"ref":"`+ref+`","limit":4096}`)
	var res artifacts.ReadResult
	if env.Error != nil || json.Unmarshal(env.Result, &res) != nil {
		t.Fatalf("artifact.read %s: %+v %s", ref, env.Error, env.Result)
	}
	return res
}

// notComplete requires the client's reference to read as still being written, with nothing of
// the timeline in the answer, on artifact.read and on the HTTP route (the route's 400).
func notComplete(t *testing.T, lab *leaseLab, rec device.UIRecording) {
	t.Helper()
	res := readRef(t, lab, rec.Timeline.Ref)
	if res.Growing == nil || !*res.Growing || (res.Text != nil && *res.Text != "") || (res.Size != nil && *res.Size != 0) {
		t.Errorf("artifact.read %s = growing %v text %v size %v; want growing with no bytes", rec.Timeline.Ref, res.Growing, res.Text, res.Size)
	}
	addr := lab.client.d.HTTPAddr()
	resp := artifactRequest(portOf(t, addr), lab.client.token, "/v1/artifacts/"+strings.TrimPrefix(rec.Timeline.Ref, "droidyard://artifact/")).send(t, addr)
	body := bodyOf(t, resp)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "still being written") || strings.Contains(string(body), `"kind"`) {
		t.Errorf("GET %s while it is copied: %d %q; want 400 'still being written' and no bytes", rec.Timeline.Ref, resp.StatusCode, body)
	}
}

// rangeLog records the ranges the copy asks the host for.
type rangeLog struct {
	mu   sync.Mutex
	list [][2]int64
}

func (l *rangeLog) add(off, n int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, [2]int64{off, n})
	return len(l.list)
}

func (l *rangeLog) all() [][2]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][2]int64(nil), l.list...)
}

// TestRemoteRecordingEndToEnd: plan m2x step 10's first check (ADR-0115 Decisions 2 and 5,
// ADR-0100 §6). The client's MCP session starts a recording on the remote id, a UI stop ends it,
// and the client's timeline equals the host's bytes, copied by ranges. Until then the record is
// the client's own (its id, `remote`, not `copied`, `device` the remote id, its starter the MCP
// session), the host's id appears nowhere in what the client published, and the host's
// directory is gone afterwards, as the client's adb log is empty. Seen failing with the host's
// id taken for the client's own (the record's id was the host's) and with the last range not
// bounded by the summary's size (a range of 700 bytes past the end).
func TestRemoteRecordingEndToEnd(t *testing.T) {
	lab, r := newRecordingLab(t, 700)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)

	if rec.ID == host.ID || !rec.Remote || rec.Copied || rec.Device != remotePhone || rec.State != device.RecordingRunning ||
		rec.StartedBy.Client != "mcp" || rec.StartedBy.Session != "agent-1" || rec.Timeline.Ref != "droidyard://artifact/r."+rec.ID {
		t.Fatalf("the client's recording = %+v (the host's %s); want its own running, remote, not copied, started by agent-1", rec, host.ID)
	}
	if raw, _ := json.Marshal(rec); strings.Contains(string(raw), host.ID) {
		t.Errorf("the client's summary names the host's recording: %s", raw)
	}
	if got := lab.client.adb.LiveRecorders(); got != 0 {
		t.Errorf("the client runs %d recorders; the host's is the only one", got)
	}
	if got := lab.host.adb.LiveRecorders(); got != 1 {
		t.Errorf("the host runs %d recorders; want the client's", got)
	}

	var rl rangeLog
	var once sync.Once
	var hostBytes []byte
	lab.client.adb.SetCopyHook(func(off, n int64) {
		once.Do(func() { hostBytes, _ = os.ReadFile(timelineOf(lab, host.ID)) })
		rl.add(off, n)
	})
	grow(t, lab, r, 4)
	// The client's record mirrors the host's counts while it runs.
	mirrored := waitClient(t, lab, rec.ID, "mirroring the host's dumps", func(x device.UIRecording) bool { return x.Dumps >= 3 && x.State == device.RecordingRunning })
	if mirrored.Copied || mirrored.Events == 0 {
		t.Errorf("the running client record = %+v; want the host's events and not copied", mirrored)
	}
	notComplete(t, lab, mirrored)

	stop := stoppedOf(t, "the UI's stop on "+remotePhone, lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	if !stop.Stopped || stop.Recording == nil || stop.Recording.ID != rec.ID || stop.Recording.State != device.RecordingEnded ||
		stop.Recording.EndReason != device.EndStopped || !stop.Recording.Remote || stop.Recording.Copied || stop.Recording.SHA256 == "" {
		t.Fatalf("the stop = %+v; want the client's recording %s ended stopped, remote, not copied yet", stop.Recording, rec.ID)
	}
	if raw, _ := json.Marshal(stop); strings.Contains(string(raw), host.ID) {
		t.Errorf("the stop names the host's recording: %s", raw)
	}
	done := waitClient(t, lab, rec.ID, "copied", copiedRec)

	want := hostBytes
	if len(want) == 0 || sumOf(want) != done.SHA256 || stop.Recording.SHA256 != done.SHA256 {
		t.Fatalf("the host's timeline read at the first range has %d bytes, SHA-256 %s; the summary says %s", len(want), sumOf(want), done.SHA256)
	}
	got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("the client's timeline has %d bytes (%v); want the host's %d, byte for byte", len(got), err, len(want))
	}
	if int64(len(want)) != done.Bytes {
		t.Errorf("the summary says %d bytes; the timeline has %d", done.Bytes, len(want))
	}
	if exists(clientFile(lab, rec.ID, "timeline.jsonl.part")) {
		t.Error("the part file is still there after the copy")
	}
	var next int64
	for i, c := range rl.all() {
		if c[0] != next || c[1] <= 0 || c[1] > 700 || c[0]+c[1] > int64(len(want)) {
			t.Errorf("range %d is %v after %d bytes; want contiguous ranges of at most 700 bytes inside the timeline of %d", i, c, next, len(want))
		}
		next = c[0] + c[1]
	}
	if next != int64(len(want)) || len(rl.all()) < 2 {
		t.Errorf("the copy asked for %d bytes in %d ranges; want the %d bytes in several", next, len(rl.all()), len(want))
	}
	waitFor(t, "the host's directory to be gone", func() bool { return !exists(filepath.Join(lab.host.state, "recordings", host.ID)) })
	if left := listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", "")); len(left) != 0 {
		t.Errorf("the host still lists %v after the copy", ids(left))
	}
	res := readRef(t, lab, rec.Timeline.Ref)
	if res.Growing == nil || *res.Growing || res.Size == nil || *res.Size != int64(len(want)) || res.Text == nil || !strings.Contains(*res.Text, `"kind"`) {
		t.Errorf("artifact.read of the copied timeline = growing %v size %v; want it complete, %d bytes", res.Growing, res.Size, len(want))
	}
	for _, c := range lab.clientCalls(t) {
		t.Errorf("the client's adb was asked %q for a remote recording", c)
	}
}

// TestRemoteRecordingResumesTheCopy: plan m2x step 10 (ADR-0115 Decision 5). The link is cut
// when the third range is about to be asked for: the part keeps the two ranges it has, the record
// is not copied and its reference still reads growing; once the link is restored the copy goes on
// from the part's size, never from zero, and the timeline has the host's SHA-256. Seen failing
// with the copy restarting from zero after the cut (a range at offset 0 after the cut).
func TestRemoteRecordingResumesTheCopy(t *testing.T) {
	lab, r := newRecordingLab(t, 600)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 4)

	var rl rangeLog
	var cutAt, partSize atomic.Int64
	cutAt.Store(-1)
	partSize.Store(-1)
	lab.client.adb.SetCopyHook(func(off, n int64) {
		if rl.add(off, n) == 3 {
			if fi, err := os.Stat(clientFile(lab, rec.ID, "timeline.jsonl.part")); err == nil {
				partSize.Store(fi.Size())
			}
			cutAt.Store(off)
			lab.proxy.Cut()
		}
	})
	stoppedOf(t, "the stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	waitFor(t, "the third range", func() bool { return cutAt.Load() >= 0 })
	cutOff := cutAt.Load()
	if cutOff <= 0 || partSize.Load() != cutOff {
		t.Fatalf("at the cut the third range starts at %d and the part holds %d bytes; want both the two ranges' bytes", cutOff, partSize.Load())
	}
	hostBytes, err := os.ReadFile(timelineOf(lab, host.ID))
	if err != nil || int64(len(hostBytes)) <= cutOff {
		t.Fatalf("the host's timeline: %d bytes, %v; want more than the %d copied", len(hostBytes), err, cutOff)
	}
	// While the link is down nothing completes, and the reference is still not a timeline.
	time.Sleep(400 * time.Millisecond)
	mid, _ := clientRec(t, lab, rec.ID)
	if mid.Copied || exists(clientFile(lab, rec.ID, "timeline.jsonl")) {
		t.Fatalf("the record reads copied (%v) or has a timeline with the link cut", mid.Copied)
	}
	notComplete(t, lab, mid)
	if fi, err := os.Stat(clientFile(lab, rec.ID, "timeline.jsonl.part")); err != nil || fi.Size() != cutOff {
		t.Errorf("the part is %v (%v) while the link is down; want the %d bytes it had", fi, err, cutOff)
	}

	lab.proxy.Restore()
	done := waitClient(t, lab, rec.ID, "copied after the link came back", copiedRec)
	got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
	if err != nil || !bytes.Equal(got, hostBytes) || sumOf(got) != done.SHA256 {
		t.Errorf("the resumed copy has %d bytes (%v), SHA-256 %s; want the host's %d bytes, %s", len(got), err, sumOf(got), len(hostBytes), done.SHA256)
	}
	all := rl.all()
	resumed := false
	for i, c := range all[3:] {
		if c[0] == 0 {
			t.Errorf("range %d after the cut starts at 0: the copy began again", i+3)
		}
		if c[0] == cutOff {
			resumed = true
		}
	}
	if !resumed {
		t.Errorf("no range after the cut starts at the part's size %d: %v", cutOff, all)
	}
}

// TestRemoteRefIsGrowingUntilCopied: plan m2x step 10 (ADR-0115 Decision 5, the promise that no
// consumer reads a remote timeline as complete before its digest matched). While the copy is
// half done, and after the client daemon restarted in the middle of it, the client's reference
// reads growing with no bytes on artifact.read and is the route's 400; the part survives the
// restart, and when the copy has matched the reference is the whole timeline and the route
// serves it. Seen failing with the record marked copied after the copy's first range (the
// record read copied with the copy half done).
func TestRemoteRefIsGrowingUntilCopied(t *testing.T) {
	lab, r := newRecordingLab(t, 600)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 4)

	reached, release := make(chan struct{}), make(chan struct{})
	var calls rangeLog
	lab.client.adb.SetCopyHook(func(off, n int64) {
		if calls.add(off, n) == 2 {
			close(reached)
			<-release
		}
	})
	stoppedOf(t, "the stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the copy never reached its second range")
	}
	mid, _ := clientRec(t, lab, rec.ID)
	if mid.Copied {
		t.Fatal("the record reads copied with the copy half done")
	}
	notComplete(t, lab, mid)
	if !exists(clientFile(lab, rec.ID, "timeline.jsonl.part")) || exists(clientFile(lab, rec.ID, "timeline.jsonl")) {
		t.Error("the half copy is not in the part file alone")
	}
	hostBytes, err := os.ReadFile(timelineOf(lab, host.ID))
	if err != nil {
		t.Fatal(err)
	}

	// The client restarts in the middle of it, with the link cut so the copy cannot go on.
	lab.proxy.Cut()
	close(release)
	lab.restartClient(t)
	again, found := clientRec(t, lab, rec.ID)
	if !found || again.Copied || again.Remote != true {
		t.Fatalf("after the restart the record is %+v (found %v); want it remote and not copied", again, found)
	}
	notComplete(t, lab, again)
	if !exists(clientFile(lab, rec.ID, "timeline.jsonl.part")) {
		t.Error("the restart lost the part file")
	}

	lab.proxy.Restore()
	done := waitClient(t, lab, rec.ID, "copied after the restart", copiedRec)
	got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
	if err != nil || !bytes.Equal(got, hostBytes) || sumOf(got) != done.SHA256 {
		t.Errorf("the copy after the restart has %d bytes (%v); want the host's %d", len(got), err, len(hostBytes))
	}
	res := readRef(t, lab, rec.Timeline.Ref)
	if res.Growing == nil || *res.Growing || res.Size == nil || *res.Size != int64(len(hostBytes)) {
		t.Errorf("artifact.read after the copy = growing %v size %v; want the whole timeline", res.Growing, res.Size)
	}
	addr := lab.client.d.HTTPAddr()
	resp := artifactRequest(portOf(t, addr), lab.client.token, "/v1/artifacts/r."+rec.ID).send(t, addr)
	if body := bodyOf(t, resp); resp.StatusCode != 200 || !bytes.Equal(body, hostBytes) {
		t.Errorf("GET after the copy: %d, %d bytes; want 200 and the whole timeline", resp.StatusCode, len(body))
	}
}

// TestRemoteRecordingAdoptedAtLinkUp: plan m2x step 10 (ADR-0115 Decision 5, ADR-0100 §4). The
// host starts the client's recording and the answer is lost with the link (the proxy holds what
// the host sends and is cut once the host's record exists): the start is DEVICE_DISCONNECTED and
// the client holds no record; the host's recording ends with the link and, at the next link up,
// the client adopts it, as the MCP session that started it, and copies it. Seen failing with no
// adoption (the client never held a record of it and the host's copy stayed), and with the
// search for it forgotten on the wall clock while the link was down (the link stayed cut for
// several times the search's time, and the recording was never adopted).
func TestRemoteRecordingAdoptedAtLinkUp(t *testing.T) {
	// The search for a lost start is bounded at 300 ms from the first listing of the host's, and
	// the link below stays down for several times that: it must not run out while nothing could
	// be listed.
	t.Cleanup(device.SetRemoteRecordingLimits(300*time.Millisecond, 30*time.Second))
	lab, _ := newRecordingLab(t, 700)
	lab.proxy.Hold()
	answered := make(chan capability.ResponseEnvelope, 1)
	go func() {
		answered <- lab.client.invokeAs(capability.ClientMCP, "agent-1", "device.recordUi", remoteArgs)
	}()
	waitFor(t, "the host's record of the start", func() bool {
		return len(listedOf(t, "the host's list", lab.host.invoke("device.uiRecordings", ""))) == 1
	})
	lab.proxy.Cut()
	env := <-answered
	if env.Error == nil || env.Error.Code != device.ErrDisconnected {
		t.Fatalf("the start whose answer was lost: %+v %s; want DEVICE_DISCONNECTED", env.Error, env.Result)
	}
	host := hostOnly(t, lab)
	waitFor(t, "the host's recording to end with the link", func() bool { return hostOnly(t, lab).State == device.RecordingEnded })
	if got := listedOf(t, "the client's list", lab.client.invoke("device.uiRecordings", "")); len(got) != 0 {
		t.Fatalf("the client holds %v for a start it never heard the answer of", ids(got))
	}
	time.Sleep(1500 * time.Millisecond) // five times the search's time, with the link down
	hostBytes, err := os.ReadFile(timelineOf(lab, host.ID))
	if err != nil || len(hostBytes) == 0 {
		t.Fatalf("the host's timeline: %d bytes, %v", len(hostBytes), err)
	}

	lab.proxy.Restore()
	testtime.Eventually(t, testtime.Bound(40*time.Second), "the client to adopt and copy the host's recording", func() bool {
		got := listedOf(t, "the client's list", lab.client.invoke("device.uiRecordings", ""))
		return len(got) == 1 && got[0].Copied
	})
	got := listedOf(t, "the client's list", lab.client.invoke("device.uiRecordings", ""))[0]
	if got.ID == host.ID || !got.Remote || got.Device != remotePhone || got.State != device.RecordingEnded || got.EndReason != device.EndDeviceGone ||
		got.StartedBy.Client != "mcp" || got.StartedBy.Session != "agent-1" || got.SHA256 != sumOf(hostBytes) {
		t.Errorf("the adopted recording = %+v; want the client's own, remote, ended deviceGone, started by agent-1, with the host's digest %s", got, sumOf(hostBytes))
	}
	file, err := os.ReadFile(clientFile(lab, got.ID, "timeline.jsonl"))
	if err != nil || !bytes.Equal(file, hostBytes) {
		t.Errorf("the adopted timeline has %d bytes (%v); want the host's %d", len(file), err, len(hostBytes))
	}
	waitFor(t, "the host's directory to be gone", func() bool { return !exists(filepath.Join(lab.host.state, "recordings", host.ID)) })
}

// TestRemoteRecordingDigestMismatchKeepsTheHosts: plan m2x step 10 (ADR-0115 Decision 5). The
// host's timeline is changed between its summary and its serving (one byte, same length): the
// copy ends in a SHA-256 that is not the summary's, the part is discarded, nothing is ever the
// client's timeline, the record is ended failed with the fixed cause and the host keeps its
// recording; the copy is not tried again, and deleting the client's record also deletes the
// host's. Seen failing with the digest comparison left out (the changed file was taken for the
// timeline, the record never failed) and with the host's delete left out of the client's (the
// host's directory stayed).
func TestRemoteRecordingDigestMismatchKeepsTheHosts(t *testing.T) {
	lab, r := newRecordingLab(t, 700)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 3)

	var calls rangeLog
	var once sync.Once
	lab.client.adb.SetCopyHook(func(off, n int64) {
		calls.add(off, n)
		once.Do(func() {
			path := timelineOf(lab, host.ID)
			b, err := os.ReadFile(path)
			if err == nil && len(b) > 100 {
				b[len(b)/2] ^= 0x01
				err = os.WriteFile(path, b, 0o600)
			}
			if err != nil {
				t.Errorf("changing the host's timeline: %v", err)
			}
		})
	})
	stoppedOf(t, "the stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	failed := waitClient(t, lab, rec.ID, "failed", func(x device.UIRecording) bool { return x.EndReason == device.EndFailed })
	if failed.Copied || !strings.Contains(failed.Cause, "did not match the host's SHA-256") || failed.State != device.RecordingEnded {
		t.Errorf("the recording whose copy did not match = %+v; want ended failed with the fixed cause, not copied", failed)
	}
	for _, name := range []string{"timeline.jsonl", "timeline.jsonl.part"} {
		if exists(clientFile(lab, rec.ID, name)) {
			t.Errorf("the client holds %s after a digest that did not match", name)
		}
	}
	if !exists(filepath.Join(lab.host.state, "recordings", host.ID, "timeline.jsonl")) {
		t.Error("the host's recording is gone after a copy that did not match")
	}
	notComplete(t, lab, failed)
	asked := len(calls.all())
	time.Sleep(500 * time.Millisecond)
	if got := len(calls.all()); got != asked {
		t.Errorf("the copy asked for %d more ranges after the mismatch; it must not be tried again", got-asked)
	}

	env := lab.client.invokeAs(capability.ClientUI, "", "device.deleteUiRecording", `{"id":"`+rec.ID+`"}`)
	var del device.DeleteUIRecordingResult
	if json.Unmarshal(env.Result, &del) != nil || env.Error != nil || !del.Deleted {
		t.Fatalf("deleteUiRecording on the failed copy: %+v %s", env.Error, env.Result)
	}
	if exists(filepath.Join(lab.client.state, "recordings", rec.ID)) {
		t.Error("the client's directory is still there after the delete")
	}
	waitFor(t, "the host's recording to be deleted with the client's", func() bool {
		return !exists(filepath.Join(lab.host.state, "recordings", host.ID))
	})
}

// TestRemoteRecordingLinkLostWhileRecording: plan m2x step 10 (ADR-0115 Decisions 4 and 5). A
// link cut while recording ends the client's record deviceGone with the link's cause at once,
// the copy waits, and at the next link up the client takes the host's final record and copies
// the timeline the host kept; and the host's own lease ending (the owner's release) is
// mirrored as the host's reason in the client's words, with the copy made over the same link.
// Seen failing with the record left recording after the cut (it never ended and could not be
// deleted).
func TestRemoteRecordingLinkLostWhileRecording(t *testing.T) {
	t.Run("link cut", func(t *testing.T) {
		lab, r := newRecordingLab(t, 700)
		rec := startRemote(t, lab)
		host := hostOnly(t, lab)
		grow(t, lab, r, 2)

		lab.proxy.Cut()
		gone := waitClient(t, lab, rec.ID, "ended with the link", func(x device.UIRecording) bool { return x.State == device.RecordingEnded })
		if gone.EndReason != device.EndDeviceGone || gone.Cause != "the link to qa was lost" || gone.Copied || !gone.Remote {
			t.Errorf("the client's record after the cut = %+v; want ended deviceGone 'the link to qa was lost', not copied", gone)
		}
		waitFor(t, "the host's recording to end with the link", func() bool { return hostOnly(t, lab).State == device.RecordingEnded })
		if h := hostOnly(t, lab); h.EndReason != device.EndDeviceGone {
			t.Errorf("the host ended it %q; want deviceGone", h.EndReason)
		}
		hostBytes, err := os.ReadFile(timelineOf(lab, host.ID))
		if err != nil || len(hostBytes) == 0 {
			t.Fatalf("the host's timeline: %d bytes, %v", len(hostBytes), err)
		}
		notComplete(t, lab, gone)
		if mid, _ := clientRec(t, lab, rec.ID); mid.Copied {
			t.Fatal("the record reads copied with the link down")
		}

		lab.proxy.Restore()
		done := waitClient(t, lab, rec.ID, "copied at the link up", copiedRec)
		got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
		if err != nil || !bytes.Equal(got, hostBytes) {
			t.Errorf("the copied timeline has %d bytes (%v); want the host's %d", len(got), err, len(hostBytes))
		}
		if done.EndReason != device.EndDeviceGone || done.SHA256 != sumOf(hostBytes) || done.Bytes != int64(len(hostBytes)) || done.Dumps < 3 {
			t.Errorf("the copied record = %+v; want deviceGone with the host's digest and counts", done)
		}
		waitFor(t, "the host's directory to be gone", func() bool { return !exists(filepath.Join(lab.host.state, "recordings", host.ID)) })
	})
	t.Run("lease end", func(t *testing.T) {
		lab, r := newRecordingLab(t, 700)
		rec := startRemote(t, lab)
		host := hostOnly(t, lab)
		grow(t, lab, r, 2)

		lab.release(t) // the host's owner ends anna's lease of the phone
		done := waitClient(t, lab, rec.ID, "copied after the lease ended", copiedRec)
		if done.EndReason != device.EndDeviceGone || !strings.Contains(done.Cause, "qa") || strings.Contains(done.Cause, "lease ended") {
			t.Errorf("the client's record = %+v; want deviceGone with the client's own words naming the remote", done)
		}
		got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
		if err != nil || sumOf(got) != done.SHA256 || len(got) == 0 {
			t.Errorf("the copied timeline: %d bytes (%v), SHA-256 %s; want the host's %s", len(got), err, sumOf(got), done.SHA256)
		}
		waitFor(t, "the host's directory to be gone", func() bool { return !exists(filepath.Join(lab.host.state, "recordings", host.ID)) })
	})
}

// TestRemoteRecordingAfterClientRestart: plan m2x step 10 (ADR-0113 §5 Recover, ADR-0115
// Decision 5). The client daemon restarts while its remote recording runs: its link ending
// ends the host's recording, and the restarted client, with no call made, finds its record
// (the same id), takes the host's final reason and copies the timeline, which is the host's byte
// for byte. A record the dead daemon left `recording` for a recording the host does not hold
// ends failed with the fixed cause. Seen failing with the supervisors not woken at start (the
// restarted client never copied).
func TestRemoteRecordingAfterClientRestart(t *testing.T) {
	lab, r := newRecordingLab(t, 700)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 2)

	lab.stopClient()
	waitFor(t, "the host's recording to end with the client's link", func() bool { return hostOnly(t, lab).State == device.RecordingEnded })
	hostBytes, err := os.ReadFile(timelineOf(lab, host.ID))
	if err != nil || len(hostBytes) == 0 {
		t.Fatalf("the host's timeline: %d bytes, %v", len(hostBytes), err)
	}
	// A record the dead daemon left running, of a recording the host never held.
	const ghostID, ghostHost = "20261021T100000Z-0a0a0a0a", "20261021T100000Z-0b0b0b0b"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	lab.client.d.Shutdown(ctx)
	cancel()
	st, err := store.New(lab.client.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := device.NewUIRecordings(st).Save(device.UIRecord{ID: ghostID, Device: remotePhone, Remote: true, HostID: ghostHost, MaxSeconds: 900,
		StartedBy: device.RecordingStarter{Client: "mcp", Session: "agent-1"}, State: device.RecordingRunning, StartedAt: time.Now().Add(-time.Minute).UTC()}); err != nil {
		t.Fatal(err)
	}
	// restartClient shuts the old daemon down again, which is harmless.
	lab.restartClient(t)

	done := waitClient(t, lab, rec.ID, "copied by the restarted client", copiedRec)
	if done.EndReason != device.EndDeviceGone || done.State != device.RecordingEnded || !done.Remote || done.Device != remotePhone {
		t.Errorf("the record after the restart = %+v; want the same id, ended deviceGone (the host's reason)", done)
	}
	got, err := os.ReadFile(clientFile(lab, rec.ID, "timeline.jsonl"))
	if err != nil || !bytes.Equal(got, hostBytes) {
		t.Errorf("the restarted client's timeline has %d bytes (%v); want the host's %d", len(got), err, len(hostBytes))
	}
	ghost := waitClient(t, lab, ghostID, "ended", func(x device.UIRecording) bool {
		return x.State == device.RecordingEnded && x.EndReason == device.EndFailed
	})
	if ghost.Copied || ghost.Cause != "the host no longer holds it" {
		t.Errorf("the record of a recording the host does not hold = %+v; want ended failed 'the host no longer holds it'", ghost)
	}
	waitFor(t, "the host's directory to be gone", func() bool { return !exists(filepath.Join(lab.host.state, "recordings", host.ID)) })
}

// TestRemoteRecordingReachAndPolicy: plan m2x step 10 (ADR-0100 §2, ADR-0101, ADR-0115 Decision
// 3). The client's own physical policy decides first: a start on a remote id its policy does not
// admit is DEVICE_NOT_ALLOWED and the host is asked nothing. The client's reach rule decides the
// stop: another MCP session's stop is refused naming the client's recording, not the host's, and
// the host is not asked; a second start on the device names the client's id too; the UI stops
// it. Seen failing with the client's reach rule left out (the other session's stop ended the
// recording).
func TestRemoteRecordingReachAndPolicy(t *testing.T) {
	lab, _ := newRecordingLab(t, 700)

	lab.rewriteClient(t, true) // the allowlist names nothing
	env := lab.client.invokeAs(capability.ClientMCP, "agent-1", "device.recordUi", remoteArgs)
	if env.Error == nil || env.Error.Code != device.ErrNotAllowed {
		t.Fatalf("a start the client's policy does not admit: %+v; want DEVICE_NOT_ALLOWED", env.Error)
	}
	if got := lab.host.adb.LiveRecorders(); got != 0 {
		t.Fatalf("the host runs %d recorders for a start the client's policy refused", got)
	}
	lab.rewriteClient(t, true, fullAllowlist()...)

	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	other := lab.client.invokeAs(capability.ClientMCP, "agent-2", "device.stopUiRecording", remoteArgs)
	if other.Error == nil || other.Error.Code != device.ErrUIRecordingActive || other.Error.Details["id"] != rec.ID {
		t.Fatalf("another session's stop: %+v; want DEVICE_UI_RECORDING_ACTIVE naming the client's recording %s", other.Error, rec.ID)
	}
	second := lab.client.invokeAs(capability.ClientMCP, "agent-2", "device.recordUi", remoteArgs)
	if second.Error == nil || second.Error.Code != device.ErrUIRecordingActive || second.Error.Details["id"] != rec.ID {
		t.Fatalf("a second start on the device: %+v; want DEVICE_UI_RECORDING_ACTIVE naming the client's recording", second.Error)
	}
	for _, e := range []*capability.Error{other.Error, second.Error} {
		if raw, _ := json.Marshal(e); strings.Contains(string(raw), host.ID) {
			t.Errorf("a refusal names the host's recording: %s", raw)
		}
	}
	if got := lab.host.adb.LiveRecorders(); got != 1 || hostOnly(t, lab).State != device.RecordingRunning {
		t.Errorf("the host runs %d recorders, its recording %q; the refused stop must leave it running", got, hostOnly(t, lab).State)
	}
	// A delete of a running remote record is refused, as a local one is.
	del := lab.client.invokeAs(capability.ClientUI, "", "device.deleteUiRecording", `{"id":"`+rec.ID+`"}`)
	if del.Error == nil || del.Error.Code != device.ErrUIRecordingActive {
		t.Errorf("a delete of the running remote record: %+v; want DEVICE_UI_RECORDING_ACTIVE", del.Error)
	}
	stop := stoppedOf(t, "the UI's stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	if !stop.Stopped || stop.Recording == nil || stop.Recording.ID != rec.ID {
		t.Errorf("the UI's stop = %+v; want the client's recording stopped", stop)
	}
	waitClient(t, lab, rec.ID, "copied", copiedRec)
}

// TestRemoteRecordingDeleteBeforeCopy: plan m2x step 10 (ADR-0115 Decision 5). A remote record
// deleted while its timeline is being copied is gone from the client at once, its copy is
// stopped and never resurrects it (no record, no directory, no timeline however long the
// blocked range takes), and the host's recording is deleted with it. Seen failing with the host's
// delete left out of the client's (the host's directory stayed).
func TestRemoteRecordingDeleteBeforeCopy(t *testing.T) {
	lab, r := newRecordingLab(t, 600)
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 3)

	reached, release := make(chan struct{}), make(chan struct{})
	var calls rangeLog
	lab.client.adb.SetCopyHook(func(off, n int64) {
		if calls.add(off, n) == 2 {
			close(reached)
			<-release
		}
	})
	stoppedOf(t, "the stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	<-reached
	env := lab.client.invokeAs(capability.ClientUI, "", "device.deleteUiRecording", `{"id":"`+rec.ID+`"}`)
	var del device.DeleteUIRecordingResult
	if json.Unmarshal(env.Result, &del) != nil || env.Error != nil || !del.Deleted {
		t.Fatalf("deleteUiRecording mid-copy: %+v %s", env.Error, env.Result)
	}
	close(release)
	waitFor(t, "the host's recording to be deleted with the client's", func() bool {
		return !exists(filepath.Join(lab.host.state, "recordings", host.ID))
	})
	time.Sleep(600 * time.Millisecond)
	if _, found := clientRec(t, lab, rec.ID); found || exists(filepath.Join(lab.client.state, "recordings", rec.ID)) {
		t.Errorf("the deleted record came back (listed %v, directory %v)", found, exists(filepath.Join(lab.client.state, "recordings", rec.ID)))
	}
}

// TestRemoteRecordingMirrorsToTheClientsWatch: plan m2x step 10 (ADR-0115 Decision 2). A
// client's device.watchUiRecordings session hears the remote recording under the client's id, as
// remote, from its start to the copy: running, ended not copied, then ended copied; it never
// hears the host's id. Seen failing with the proxy publishing nothing for the copy (the panel
// would keep Download held back for good).
func TestRemoteRecordingMirrorsToTheClientsWatch(t *testing.T) {
	lab, r := newRecordingLab(t, 700)
	w := wsConnect(t, lab.client)
	w.send("w1", "device.watchUiRecordings", "", `{}`, nil)
	if _, f := w.collect("w1"); f.Error != nil {
		t.Fatalf("device.watchUiRecordings: %+v", f.Error)
	}
	rec := startRemote(t, lab)
	host := hostOnly(t, lab)
	grow(t, lab, r, 2)
	stoppedOf(t, "the stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))

	var sawRunning, sawEndedNotCopied, sawCopied bool
	for !sawCopied {
		f := w.must("a device.uiRecording event")
		if f.Event == nil || f.Event.Topic != device.TopicUIRecording {
			continue
		}
		if strings.Contains(string(f.Event.Payload), host.ID) {
			t.Fatalf("the client's watch heard the host's recording id: %s", f.Event.Payload)
		}
		var p struct {
			Recording device.UIRecording `json:"recording"`
		}
		if f.Event.Type != "state" || json.Unmarshal(f.Event.Payload, &p) != nil || p.Recording.ID != rec.ID {
			continue
		}
		x := p.Recording
		if !x.Remote || x.Device != remotePhone {
			t.Fatalf("an event of the remote recording = %+v; want remote on %s", x, remotePhone)
		}
		switch {
		case x.State == device.RecordingRunning:
			sawRunning = true
		case x.State == device.RecordingEnded && !x.Copied:
			sawEndedNotCopied = true
		case x.State == device.RecordingEnded && x.Copied:
			sawCopied = true
		}
	}
	if !sawRunning || !sawEndedNotCopied {
		t.Errorf("the watch heard running %v and ended-not-copied %v before the copied event; want both", sawRunning, sawEndedNotCopied)
	}
}

// TestRemoteStopNamesOnlyWhatTheClientHolds: plan m2x step 10 (ADR-0115 Decision 2, the host's
// reference means nothing here). When the host stops a recording the client holds no record of,
// the answer says stopped and names no recording: not the host's, and not the client's newest
// older one, which would read as the one stopped. Seen failing with the newest of the client's
// own recordings returned under `stopped: true`.
func TestRemoteStopNamesOnlyWhatTheClientHolds(t *testing.T) {
	lab, _ := newRecordingLab(t, 700)
	first := startRemote(t, lab)
	stoppedOf(t, "the first stop", lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs))
	waitClient(t, lab, first.ID, "copied", copiedRec)

	second := startRemote(t, lab)
	// The client loses its record of the running one, as it would of a start it never heard of.
	if err := os.RemoveAll(filepath.Join(lab.client.state, "recordings", second.ID)); err != nil {
		t.Fatal(err)
	}
	env := lab.client.invokeAs(capability.ClientUI, "", "device.stopUiRecording", remoteArgs)
	res := stoppedOf(t, "the stop of a recording the client holds no record of", env)
	if !res.Stopped || res.Recording != nil {
		t.Errorf("the stop = %+v; want stopped with no recording named (the client's %s is older)", res, first.ID)
	}
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), hostOnlyID(t, lab)) {
		t.Errorf("the stop names the host's recording: %s", raw)
	}
}

// hostOnlyID is the id of the one recording the host holds.
func hostOnlyID(t *testing.T, lab *leaseLab) string {
	t.Helper()
	return hostOnly(t, lab).ID
}

// TestRemoteRemovedGivesTheCopyUp: plan m2x step 10. A record whose copy is pending, of a remote
// that is no longer in the client's configuration, ends failed with the fixed cause instead of
// being polled for ever; and nothing is asked of the host. Seen failing with the remote waited
// for without end (the record stayed pending).
func TestRemoteRemovedGivesTheCopyUp(t *testing.T) {
	t.Cleanup(device.SetRemoteRecordingLimits(15*time.Minute, 400*time.Millisecond))
	lab, _ := newRecordingLab(t, 700)
	lab.stopClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	lab.client.d.Shutdown(ctx)
	cancel()
	const id, hostID = "20261021T100000Z-0c0c0c0c", "20261021T100000Z-0d0d0d0d"
	st, err := store.New(lab.client.state)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute).UTC()
	end := start.Add(10 * time.Second)
	if err := device.NewUIRecordings(st).Save(device.UIRecord{ID: id, Device: remotePhone, Remote: true, HostID: hostID, MaxSeconds: 900,
		StartedBy: device.RecordingStarter{Client: "mcp", Session: "agent-1"}, State: device.RecordingEnded, StartedAt: start, EndedAt: &end,
		EndReason: device.EndStopped, Bytes: 100, SHA256: strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	// The configuration no longer names the remote.
	configAt(t, lab.client.state, jsonObject(t, map[string]any{"schemaVersion": 1,
		"devices": map[string]any{"allowPhysical": true, "allowlist": fullAllowlist()}}))
	lab.restartClient(t)

	testtime.Eventually(t, testtime.Bound(30*time.Second), "the copy of the removed remote's recording to be given up", func() bool {
		got := listedOf(t, "the client's list", lab.client.invoke("device.uiRecordings", `{"id":"`+id+`"}`))
		return len(got) == 1 && got[0].EndReason == device.EndFailed
	})
	got := listedOf(t, "the client's list", lab.client.invoke("device.uiRecordings", `{"id":"`+id+`"}`))[0]
	if got.Copied || !strings.Contains(got.Cause, "removed from this daemon's configuration") {
		t.Errorf("the record = %+v; want ended failed with the removed-remote cause", got)
	}
}

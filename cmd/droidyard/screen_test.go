package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// device.watchScreen and GET /v1/screen end to end (ADR-0078; plan m2p step 4): a WebSocket
// session, the shipped wiring, the fake adb as real processes the supervisor records, and a
// listener standing for the server behind the fake forward. It serves step 1's recording of
// the video socket and records what reaches the control socket.

const scSerial = "emulator-5554"

var scParams = `{"device":"` + scSerial + `"}`

// scListener is the server behind the forward: the first connection is the video socket,
// the second the control socket, the third a video socket again (a second start), and so on.
type scListener struct {
	ln   net.Listener
	Path string // the Unix socket the fake forward links to

	head, tail []byte
	gate       chan struct{}
	gateOnce   sync.Once

	mu      sync.Mutex
	accepts int
	conns   []net.Conn
	control []byte
}

func startSCListener(t *testing.T, head, tail []byte) *scListener {
	t.Helper()
	// Under /tmp: a Unix socket's path holds about 100 bytes, which a test's temporary
	// directory can exceed.
	root, err := os.MkdirTemp("/tmp", "dysc")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l := &scListener{ln: ln, Path: path, head: head, tail: tail, gate: make(chan struct{})}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.mu.Lock()
			n := l.accepts
			l.accepts++
			l.conns = append(l.conns, c)
			l.mu.Unlock()
			if n%2 == 0 {
				go l.video(c)
			} else {
				go l.controlSocket(c)
			}
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		os.RemoveAll(root)
		l.mu.Lock()
		for _, c := range l.conns {
			c.Close()
		}
		l.mu.Unlock()
	})
	return l
}

// Release lets the tail through.
func (l *scListener) Release() { l.gateOnce.Do(func() { close(l.gate) }) }

func (l *scListener) video(c net.Conn) {
	gone := make(chan struct{})
	go func() { _, _ = c.Read(make([]byte, 1)); close(gone) }()
	c.Write(l.head)
	select {
	case <-l.gate:
		c.Write(l.tail)
	case <-gone:
		return
	}
	<-gone
}

func (l *scListener) controlSocket(c net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		l.mu.Lock()
		l.control = append(l.control, buf[:n]...)
		l.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (l *scListener) controlBytes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.control)
}

// scRecording is the recorded video socket, its packets as Demux reads them, and the byte
// offset just after its key frame (the head: the source's start needs the config, and the
// viewer joins at the key frame).
func scRecording(t *testing.T) (data []byte, pkts []screen.Packet, keyEnd int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "domain", "device", "screen", "testdata", "scrcpy-api31-portrait.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := screen.Demux(bytes.NewReader(data), func(p screen.Packet) error {
		pkts = append(pkts, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cr := &countingReader{r: bytes.NewReader(data)}
	stop := errors.New("stop")
	n := 0
	if err := screen.Demux(cr, func(screen.Packet) error {
		if n++; n == 2 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatalf("splitting the recording: %v", err)
	}
	return data, pkts, cr.n
}

type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// scDaemon is the shipped wiring over the fake adb with the listener behind its forward.
type scDaemon struct {
	*inProcess
	dir string
	ln  *scListener
	ws  *wsClient
}

func startSCDaemon(t *testing.T, head, tail []byte) *scDaemon {
	t.Helper()
	ln := startSCListener(t, head, tail)
	dir := t.TempDir()
	devicetest.Write(t, dir, "devices.out", []byte("List of devices attached\n"+scSerial+" device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n"))
	devicetest.Write(t, dir, scSerial+".bootcompleted.out", []byte("1\n"))
	devicetest.Write(t, dir, scSerial+".forwardscrcpy.out", []byte(ln.Path+"\n"))
	p := startInProcessADB(t, "127.0.0.1:0", nil, &adbSeam{env: devicetest.Env(t, dir, unusedPort(t)), floor: adbFloorFor()})
	return &scDaemon{inProcess: p, dir: dir, ln: ln}
}

// watch calls device.watchScreen as id on w and returns its result.
func (w *wsClient) watchScreen(id string) (sub, stream string, width, height int) {
	w.t.Helper()
	w.send(id, "device.watchScreen", "", scParams, nil)
	_, f := w.collect(id)
	if f.Error != nil {
		w.t.Fatalf("device.watchScreen: %+v", f.Error)
	}
	var r struct {
		SubscriptionID string `json:"subscriptionId"`
		Device         string `json:"device"`
		StreamID       string `json:"streamId"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
	}
	if err := json.Unmarshal(f.Result, &r); err != nil || r.StreamID == "" || r.Device != scSerial {
		w.t.Fatalf("device.watchScreen result %s: %v", f.Result, err)
	}
	return r.SubscriptionID, r.StreamID, r.Width, r.Height
}

// stoppedFrame waits for the next event frame of sub and requires a device.screen stopped.
func (w *wsClient) stoppedFrame(sub string) (reason, cause string) {
	w.t.Helper()
	f, ok := w.next(25 * time.Second) // the unattached stream's is due after 10 s
	if !ok {
		w.t.Fatalf("a stopped event: no frame (the connection ended: %v)", w.endedErr())
	}
	if f.Event == nil || f.SubscriptionID != sub || f.Event.Topic != "device.screen" || f.Event.Type != "stopped" {
		w.t.Fatalf("a frame that is not a device.screen stopped event of %s: %+v", sub, f)
	}
	var se struct {
		Device string `json:"device"`
		Reason string `json:"reason"`
		Cause  string `json:"cause"`
	}
	if err := json.Unmarshal(f.Event.Payload, &se); err != nil || se.Device != scSerial {
		w.t.Fatalf("stopped payload %s: %v", f.Event.Payload, err)
	}
	return se.Reason, se.Cause
}

// screenSock is one /v1/screen connection, read in a goroutine of its own.
type screenSock struct {
	t    *testing.T
	c    *websocket.Conn
	msgs chan screenMsg
	done chan struct{}
	err  error // valid once done is closed
}

type screenMsg struct {
	typ  websocket.MessageType
	data []byte
}

func screenURL(p *inProcess) string { return "ws://" + p.d.HTTPAddr() + "/v1/screen" }

// dialScreen upgrades /v1/screen with an allowed Origin; not yet authenticated.
func dialScreen(t *testing.T, p *inProcess) *screenSock {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, screenURL(p), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {allowedOrigin(p)}}})
	if err != nil {
		t.Fatalf("the upgrade: %v (response %v)", err, resp)
	}
	c.SetReadLimit(32 << 20)
	s := &screenSock{t: t, c: c, msgs: make(chan screenMsg, 4096), done: make(chan struct{})}
	t.Cleanup(func() { c.CloseNow() })
	go func() {
		defer close(s.done)
		for {
			typ, data, err := c.Read(context.Background())
			if err != nil {
				s.err = err
				return
			}
			s.msgs <- screenMsg{typ, data}
		}
	}()
	return s
}

// attach dials and sends the auth frame for stream.
func attachScreen(t *testing.T, p *inProcess, token, stream string) *screenSock {
	t.Helper()
	s := dialScreen(t, p)
	s.write(websocket.MessageText, `{"auth":"`+token+`","stream":"`+stream+`"}`)
	return s
}

func (s *screenSock) write(typ websocket.MessageType, data string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.c.Write(ctx, typ, []byte(data)); err != nil {
		s.t.Fatalf("writing a frame: %v", err)
	}
}

// message is the next message, which must be binary.
func (s *screenSock) message(what string) (flags byte, pts uint64, payload []byte) {
	s.t.Helper()
	select {
	case m := <-s.msgs:
		if m.typ != websocket.MessageBinary {
			s.t.Fatalf("%s: a %v message", what, m.typ)
		}
		return decodeScreenMessage(s.t, m.data)
	case <-s.done:
		s.t.Fatalf("%s: the socket ended: %v", what, s.err)
	case <-time.After(20 * time.Second):
		s.t.Fatalf("%s: no message", what)
	}
	return
}

// closedWith waits for the socket to end and returns its close status.
func (s *screenSock) closedWith(within time.Duration) websocket.StatusCode {
	s.t.Helper()
	select {
	case <-s.done:
		return websocket.CloseStatus(s.err)
	case <-time.After(within):
		s.t.Fatalf("the socket stayed open for %v", within)
		return 0
	}
}

// decodeScreenMessage reads the header of ADR-0078 §4 on the test's side: its own decoder,
// so a change in the daemon's encoder meets an independent reading of the format.
func decodeScreenMessage(t *testing.T, b []byte) (flags byte, pts uint64, payload []byte) {
	t.Helper()
	if len(b) < 9 {
		t.Fatalf("a %d-byte message has no header", len(b))
	}
	return b[0], binary.BigEndian.Uint64(b[1:9]), b[9:]
}

// scWaitClean waits until nothing of the live view is left: no stream, no source, no
// forward, no pid record.
func scWaitClean(t *testing.T, d *scDaemon) {
	t.Helper()
	waitFor(t, "the live view to leave nothing behind", func() bool {
		return d.adb.LiveScreenStreams() == 0 && d.adb.LiveScreenSources() == 0 &&
			len(devicetest.Forwards(t, d.dir)) == 0 && len(streamRecords(t, d.state)) == 0
	})
}

// TestWatchScreenEndToEnd walks a stream through every way it ends (ADR-0078 §3, §6),
// one phase each on a daemon of its own wiring, and pins what travels: the bytes on the
// socket are the source's packets, header aside (A8). Seen failing with the adapter
// re-chunking each packet into two messages: the first message's payload was half of the
// merged key frame, and the second was not the device's next packet.
func TestWatchScreenEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("waits the 10 s of an unattached stream")
	}
	t.Parallel()
	data, pkts, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	w := wsConnect(t, d.inProcess)

	// The golden frame decodes with this test's header code.
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "domain", "device", "screen", "testdata", "frame-golden.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if flags, pts, payload := decodeScreenMessage(t, golden); flags != 3 || pts != 1234567 || len(payload) != 42 {
		t.Errorf("frame-golden.bin decodes to flags %d, pts %d, %d payload bytes; want 3, 1234567, 42", flags, pts, len(payload))
	}

	// 1. The bytes. The viewer joined at the key frame, the config in front; the tail follows.
	sub, stream, width, height := w.watchScreen("w1")
	if width != 590 || height != 1280 {
		t.Errorf("size %dx%d, want the recording's 590x1280", width, height)
	}
	sock := attachScreen(t, d.inProcess, d.token, stream)
	flags, pts, payload := sock.message("the first message")
	wantFirst := append(append([]byte(nil), pkts[0].Data...), pkts[1].Data...)
	if flags != 3 || pts != uint64(pkts[1].PTS) || !bytes.Equal(payload, wantFirst) {
		t.Fatalf("first message: flags %d, pts %d, %d bytes; want key+config, pts %d, the config and key frame in %d bytes",
			flags, pts, len(payload), pkts[1].PTS, len(wantFirst))
	}
	d.ln.Release()
	for i := 2; i < len(pkts); i++ {
		flags, pts, payload := sock.message("packet " + strconv.Itoa(i))
		var want byte
		if pkts[i].Key {
			want = 1
		}
		if flags != want || pts != uint64(pkts[i].PTS) || !bytes.Equal(payload, pkts[i].Data) {
			t.Fatalf("packet %d: flags %d pts %d, %d bytes; want flags %d pts %d, the device's %d bytes",
				i, flags, pts, len(payload), want, pkts[i].PTS, len(pkts[i].Data))
		}
	}

	// 2. The stream socket closes: stopped detached, and the id is unknown.
	sock.c.Close(websocket.StatusNormalClosure, "")
	if reason, _ := w.stoppedFrame(sub); reason != "detached" {
		t.Errorf("a closed socket gave reason %q, want detached", reason)
	}
	if code := attachScreen(t, d.inProcess, d.token, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4404 {
		t.Errorf("a reattach closed with %d, want 4404", code)
	}
	scWaitClean(t, d)

	// 3. An id never attached is released after 10 s (screenAttachBound, ADR-0078 §3; no
	// level-5 run timed it, so the claim keeps its value). The 10 s run from the stream's
	// creation, and the call that creates it takes as long as a loaded host's start of the
	// screen server does: the clock starts at the answer, not before the call.
	sub, stream, _, _ = w.watchScreen("w2")
	start := time.Now()
	if reason, cause := w.stoppedFrame(sub); reason != "detached" || !strings.Contains(cause, "no stream socket attached") {
		t.Errorf("an unattached stream gave %q (%q), want detached, no socket attached", reason, cause)
	}
	// The upper end is unscaled, like the claim: 14 s, below a bound doubled to 20 s.
	if el := time.Since(start); el < 9*time.Second || el > 14*time.Second {
		t.Errorf("an unattached stream was released %v after its start answered, want about 10 s", el)
	}
	if code := attachScreen(t, d.inProcess, d.token, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4404 {
		t.Errorf("an attach after the release closed with %d, want 4404", code)
	}
	scWaitClean(t, d)

	// 4. Closing the JSON session closes the socket and stops the server.
	w2 := wsConnect(t, d.inProcess)
	_, stream, _, _ = w2.watchScreen("w3")
	sock = attachScreen(t, d.inProcess, d.token, stream)
	sock.message("a message before the session closes")
	w2.c.Close(websocket.StatusNormalClosure, "")
	if code := sock.closedWith(testtime.Bound(30 * time.Second)); code != 4410 {
		t.Errorf("the socket of a closed session ended with %d, want 4410", code)
	}
	scWaitClean(t, d)

	// 5. The device disappears: deviceGone, then 4410. Last, since it unlists the device.
	sub, stream, _, _ = w.watchScreen("w4")
	sock = attachScreen(t, d.inProcess, d.token, stream)
	sock.message("a message before the device goes")
	devicetest.Write(t, d.dir, "devices.out", []byte("List of devices attached\n"))
	devicetest.Write(t, d.dir, scSerial+".scrcpy.stop", []byte("3"))
	if reason, _ := w.stoppedFrame(sub); reason != "deviceGone" {
		t.Errorf("a device that disappeared gave reason %q, want deviceGone", reason)
	}
	for {
		select {
		case <-sock.msgs:
			continue
		case <-sock.done:
		case <-time.After(testtime.Bound(30 * time.Second)):
			t.Fatal("the socket of a vanished device stayed open")
		}
		break
	}
	if code := websocket.CloseStatus(sock.err); code != 4410 {
		t.Errorf("the socket of a vanished device ended with %d, want 4410", code)
	}
	scWaitClean(t, d)
}

// TestScreenEndpointFence pins what GET /v1/screen refuses (ADR-0078 §4; http-websocket.md
// §Security baseline) and, above all, that nothing a client sends reaches the device: the
// handler holds a viewer that reads and closes, so a frame after the auth frame closes the
// socket with 1003 and the control socket of the source has received no byte. Seen failing
// with the client's binary frame handed to the source's Inject (a mutation: the viewer the
// adapter holds also offered Frame, which tapped): the control listener received the tap's
// bytes.
func TestScreenEndpointFence(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	port := portOf(t, d.d.HTTPAddr())
	ps := strconv.Itoa(port)

	// The upgrade runs the fence first, and Origin is mandatory.
	for _, c := range []struct {
		name, origin, host string
	}{
		{"no Origin", "", ""},
		{"Origin evil.example", "http://evil.example", ""},
		{"Host evil.example", "http://127.0.0.1:" + ps, "evil.example:" + ps},
	} {
		hdr := http.Header{}
		if c.origin != "" {
			hdr.Set("Origin", c.origin)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, resp, err := websocket.Dial(ctx, screenURL(d.inProcess), &websocket.DialOptions{HTTPHeader: hdr, Host: c.host})
		cancel()
		if err == nil {
			conn.CloseNow()
			t.Errorf("%s: upgraded; want 403", c.name)
		} else if resp == nil || resp.StatusCode != 403 {
			t.Errorf("%s: %v; want 403", c.name, resp)
		}
	}
	// No CORS, and a POST needs the token like every other API route.
	opts := httpRequest{method: "OPTIONS", path: "/v1/screen", proto: "HTTP/1.1", headers: []string{
		"Host: 127.0.0.1:" + ps, "Origin: http://127.0.0.1:" + ps}}
	if resp := opts.send(t, d.d.HTTPAddr()); resp.StatusCode != 403 {
		t.Errorf("OPTIONS /v1/screen: %d, want 403", resp.StatusCode)
	}
	post := httpRequest{method: "POST", path: "/v1/screen", proto: "HTTP/1.1", body: "{}", headers: []string{
		"Host: 127.0.0.1:" + ps, "Content-Type: application/json", "Origin: http://127.0.0.1:" + ps}}
	if resp := post.send(t, d.d.HTTPAddr()); resp.StatusCode != 401 {
		t.Errorf("POST /v1/screen without a token: %d, want 401", resp.StatusCode)
	}

	w := wsConnect(t, d.inProcess)
	_, stream, _, _ := w.watchScreen("f1")

	// A bad token is 4401, whatever the id; so is a first frame that is not the auth frame.
	for _, first := range []string{
		`{"auth":"` + strings.Repeat("0", 64) + `","stream":"` + stream + `"}`,
		`{"auth":"` + d.token[:63] + `","stream":"` + stream + `"}`,
		`{"auth":"","stream":"` + stream + `"}`,
		`{"stream":"` + stream + `"}`,
		`hello`,
	} {
		s := dialScreen(t, d.inProcess)
		s.write(websocket.MessageText, first)
		if code := s.closedWith(testtime.Bound(30 * time.Second)); code != 4401 {
			t.Errorf("first frame %.40q: closed with %d, want 4401", first, code)
		}
	}
	// ... and the stream was not claimed by any of them.
	s := dialScreen(t, d.inProcess)
	s.write(websocket.MessageBinary, `{"auth":"`+d.token+`","stream":"`+stream+`"}`)
	if code := s.closedWith(testtime.Bound(30 * time.Second)); code != 4401 {
		t.Errorf("a binary auth frame with the right token: closed with %d, want 4401", code)
	}
	// An unknown id is 4404, after the token was right.
	if code := attachScreen(t, d.inProcess, d.token, "nope").closedWith(testtime.Bound(30 * time.Second)); code != 4404 {
		t.Errorf("an unknown id: closed with %d, want 4404", code)
	}
	// A first frame over 4 KiB is 1009 (the library's limit).
	big := dialScreen(t, d.inProcess)
	big.write(websocket.MessageText, `{"auth":"`+strings.Repeat("x", 5000)+`"}`)
	if code := big.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusMessageTooBig {
		t.Errorf("a 5000-byte first frame: closed with %d, want 1009", code)
	}

	// The first attach holds; a second is 4409.
	good := attachScreen(t, d.inProcess, d.token, stream)
	good.message("the first message of the attached socket")
	if code := attachScreen(t, d.inProcess, d.token, stream).closedWith(testtime.Bound(30 * time.Second)); code != 4409 {
		t.Errorf("a second attach: closed with %d, want 4409", code)
	}

	// Nothing the client sends reaches the device: a binary frame, then a text frame on
	// another stream, each closes with 1003, and the control socket saw nothing.
	good.write(websocket.MessageBinary, "\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	if code := good.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusUnsupportedData {
		t.Errorf("a binary frame after auth: closed with %d, want 1003", code)
	}
	_, stream2, _, _ := w.watchScreen("f2")
	text := attachScreen(t, d.inProcess, d.token, stream2)
	text.message("the first message of the second socket")
	text.write(websocket.MessageText, `{"type":"tap","x":0.5,"y":0.5}`)
	if code := text.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusUnsupportedData {
		t.Errorf("a text frame after auth: closed with %d, want 1003", code)
	}
	// A frame over 4 KiB after auth is 1009: the read limit holds for the socket's life.
	_, stream3, _, _ := w.watchScreen("f3")
	oversize := attachScreen(t, d.inProcess, d.token, stream3)
	oversize.message("the first message of the third socket")
	oversize.write(websocket.MessageBinary, strings.Repeat("x", 5000))
	if code := oversize.closedWith(testtime.Bound(30 * time.Second)); code != websocket.StatusMessageTooBig {
		t.Errorf("a 5000-byte frame after auth: closed with %d, want 1009", code)
	}
	time.Sleep(300 * time.Millisecond)
	if n := d.ln.controlBytes(); n != 0 {
		t.Errorf("the control socket received %d bytes from a client that only sent frames", n)
	}

	// POST /v1/call has no session: SESSION_REQUIRED, and no stream is made.
	resp := callRequest(port, d.token, `{"id":"p","capability":"device.watchScreen","params":`+scParams+`}`).send(t, d.d.HTTPAddr())
	if resp.StatusCode != 200 {
		t.Errorf("POST /v1/call device.watchScreen: %d, want 200 with an envelope", resp.StatusCode)
	}
	if env := envelopeOf(t, resp); env.Error == nil || env.Error.Code != capability.ErrSessionRequired {
		t.Errorf("POST /v1/call device.watchScreen: %+v, want SESSION_REQUIRED", env.Error)
	}
}

// TestScreenAuthDeadline: a connection that sends no auth frame is closed with 4401 at the
// deadline (rule 4), the same one the session socket has.
func TestScreenAuthDeadline(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	d.http.SetAuthTimeout(200 * time.Millisecond)
	s := dialScreen(t, d.inProcess)
	if code := s.closedWith(testtime.Bound(30 * time.Second)); code != 4401 {
		t.Errorf("a silent connection closed with %d, want 4401", code)
	}
}

// TestWatchScreenStreamIDIsNotRetained: the stream id is a capability (ADR-0078 §3), and
// operation ids reach every operation.state watcher. The caller's own response carries the
// id, and still attaches; the retained operation, read back with operation.wait from another
// session, does not. Seen failing with the descriptor's Slim removed: operation.wait
// returned the stream id.
func TestWatchScreenStreamIDIsNotRetained(t *testing.T) {
	t.Parallel()
	data, _, keyEnd := scRecording(t)
	d := startSCDaemon(t, data[:keyEnd], data[keyEnd:])
	w := wsConnect(t, d.inProcess)
	w.send("w1", "device.watchScreen", "", scParams, nil)
	_, f := w.collect("w1")
	if f.Error != nil || f.Operation == nil {
		t.Fatalf("device.watchScreen: %+v", f)
	}
	var own struct {
		StreamID string `json:"streamId"`
	}
	if err := json.Unmarshal(f.Result, &own); err != nil || own.StreamID == "" {
		t.Fatalf("the caller's own response %s has no stream id (%v)", f.Result, err)
	}

	other := wsConnect(t, d.inProcess)
	other.send("o1", "operation.wait", "", `{"operationId":"`+f.Operation.ID+`"}`, nil)
	r := other.must("operation.wait")
	if r.Error != nil {
		t.Fatalf("operation.wait: %+v", r.Error)
	}
	var later struct {
		StreamID string `json:"streamId"`
		Device   string `json:"device"`
	}
	if err := json.Unmarshal(r.Result, &later); err != nil || later.Device != scSerial {
		t.Fatalf("operation.wait result %s: %v", r.Result, err)
	}
	if later.StreamID != "" {
		t.Errorf("operation.wait returned the stream id %q", later.StreamID)
	}

	// The id the caller got still works.
	sock := attachScreen(t, d.inProcess, d.token, own.StreamID)
	sock.message("the first message")
}

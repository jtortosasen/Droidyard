package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen/scrcpyserver"
)

// The live view's source (plan m2p step 3) over the re-executed fake adb: the server's
// `adb shell` child is a process the supervisor records, so only this fixture reaches the
// paths below. The fake's forward prints the port of a listener the test runs, which
// serves step 1's recording of the video socket and records the bytes of the control
// socket. Every spawn costs about a second under -race.

const ssWait = 60 * time.Second

// ssFixture is the stream of the recorded video socket (testdata of package screen),
// its packets as Demux reads them, and the byte offset just after its key frame.
func ssFixture(t *testing.T) (data []byte, pkts []screen.Packet, keyEnd int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("screen", "testdata", "scrcpy-api31-portrait.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := screen.Demux(bytes.NewReader(data), func(p screen.Packet) error {
		pkts = append(pkts, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The same stream, stopped after its second media packet (the config, then the key
	// frame): the reader counts exactly the bytes Demux asked for.
	cr := &countReader{r: bytes.NewReader(data)}
	stop := errors.New("stop")
	n := 0
	err = screen.Demux(cr, func(screen.Packet) error {
		if n++; n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("splitting the recording: %v", err)
	}
	return data, pkts, cr.n
}

type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// screenListenerOpts scripts the fake server's sockets.
type screenListenerOpts struct {
	head, tail   []byte // the video socket's bytes: head, then tail
	gateHead     bool   // the head waits for Release
	gateTail     bool   // the tail waits for Release
	refuse       int    // the first n connections are closed at once, as the forward does before the server listens
	refuseAll    bool
	silent       bool   // the video socket is accepted and nothing is written
	controlOut   []byte // written to the control socket as soon as it is connected
	gateControl  bool   // controlOut waits for Release
	cutAfterTail bool   // the video socket is closed right after the tail
}

// screenListener stands for the server behind the forward: the first connection that is
// kept is the video socket, the second the control socket, the third a video socket
// again (a second start), and so on.
type screenListener struct {
	opts screenListenerOpts
	ln   net.Listener
	Path string // the Unix socket the fake forward links to

	gate     chan struct{}
	gateOnce sync.Once

	mu            sync.Mutex
	accepted      int
	roles         int
	conns         []net.Conn
	control       []byte
	videoClosed   int
	controlClosed int
}

func startScreenListener(t *testing.T, o screenListenerOpts) *screenListener {
	t.Helper()
	// Under /tmp: a Unix socket's path holds about 100 bytes, which a test's temporary
	// directory can exceed.
	root, err := os.MkdirTemp("/tmp", "dyscr")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l := &screenListener{opts: o, ln: ln, Path: path, gate: make(chan struct{})}
	go l.accept()
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

// Release lets the gated bytes through.
func (l *screenListener) Release() { l.gateOnce.Do(func() { close(l.gate) }) }

func (l *screenListener) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		l.accepted++
		n := l.accepted
		l.conns = append(l.conns, c)
		l.mu.Unlock()
		go l.handle(c, n)
	}
}

func (l *screenListener) handle(c net.Conn, n int) {
	if l.opts.refuseAll || n <= l.opts.refuse {
		c.Close()
		return
	}
	l.mu.Lock()
	role := l.roles
	l.roles++
	l.mu.Unlock()
	if role%2 == 0 {
		l.video(c)
	} else {
		l.controlSocket(c)
	}
}

func (l *screenListener) video(c net.Conn) {
	gone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, c)
		close(gone)
	}()
	wait := func(gated bool) bool {
		if !gated {
			return true
		}
		select {
		case <-l.gate:
			return true
		case <-gone:
			return false
		}
	}
	if !l.opts.silent && wait(l.opts.gateHead) {
		c.Write(l.opts.head)
		if wait(l.opts.gateTail) {
			c.Write(l.opts.tail)
			if l.opts.cutAfterTail {
				c.Close()
			}
		}
	}
	<-gone
	l.mu.Lock()
	l.videoClosed++
	l.mu.Unlock()
}

func (l *screenListener) controlSocket(c net.Conn) {
	if len(l.opts.controlOut) > 0 {
		go func() {
			if l.opts.gateControl {
				<-l.gate
			}
			c.Write(l.opts.controlOut)
		}()
	}
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		l.mu.Lock()
		l.control = append(l.control, buf[:n]...)
		l.mu.Unlock()
		if err != nil {
			break
		}
	}
	l.mu.Lock()
	l.controlClosed++
	l.mu.Unlock()
}

func (l *screenListener) controlBytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.control...)
}

func (l *screenListener) closed() (video, control int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.videoClosed, l.controlClosed
}

// ssFix is the follow fixture with a listener behind the fake forward, and a target.
type ssFix struct {
	*followFixture
	ln  *screenListener
	tgt *target
}

func newSSFixture(t *testing.T, o screenListenerOpts, files map[string]string) *ssFix {
	t.Helper()
	l := startScreenListener(t, o)
	all := map[string]string{uiSerial + ".forwardscrcpy.out": l.Path + "\n"}
	for k, v := range files {
		all[k] = v
	}
	f := newFollowFixture(t, all)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ssWait)
		defer cancel()
		f.adb.screens.StopAll(ctx)
	})
	return &ssFix{followFixture: f, ln: l, tgt: &target{adb: f.adb.Resolve().Path, serial: uiSerial}}
}

// startedFixture is newSSFixture with the recording served at once.
func startedFixture(t *testing.T, files map[string]string) *ssFix {
	t.Helper()
	data, _, _ := ssFixture(t)
	return newSSFixture(t, screenListenerOpts{head: data}, files)
}

func (f *ssFix) acquire(ctx context.Context) (*screenSource, func(), error) {
	return f.adb.screens.acquire(ctx, f.adb, f.tgt)
}

func (f *ssFix) mustAcquire(t *testing.T) (*screenSource, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), ssWait)
	defer cancel()
	s, release, err := f.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return s, release
}

func (f *ssFix) calls(t *testing.T) []string { return devicetest.Calls(t, f.dir) }

// waitServers waits until the fake has logged n starts of the server. The fake is a
// process of its own and logs when it runs, which can be after the source is ready (the
// listener serves without it): a test that reads the log waits first.
func (f *ssFix) waitServers(t *testing.T, n int) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d logged starts of the server", n), func() bool {
		return len(f.callsWith(t, "app_process")) >= n
	})
}

// callsWith are the fake's invocations containing sub.
func (f *ssFix) callsWith(t *testing.T, sub string) []string {
	t.Helper()
	var out []string
	for _, c := range f.calls(t) {
		if strings.Contains(c, sub) {
			out = append(out, c)
		}
	}
	return out
}

// serverPids are the pids of every fake server that ran.
func (f *ssFix) serverPids(t *testing.T) []int {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.dir, uiSerial+".scrcpy.pids"))
	var pids []int
	for _, s := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, n)
	}
	return pids
}

// requireClean says nothing of the source is left: no source in the set, no pid record,
// no process, no forward.
func (f *ssFix) requireClean(t *testing.T) {
	t.Helper()
	if n := f.adb.screens.Live(); n != 0 {
		t.Errorf("%d sources are still in the set", n)
	}
	if recs := f.records(t); len(recs) != 0 {
		t.Errorf("processes.json holds %+v, want none", recs)
	}
	if fw := devicetest.Forwards(t, f.dir); len(fw) != 0 {
		t.Errorf("forwards left: %v", fw)
	}
	// adb leaves the socket file when it removes a forward (ADR-0102): the source unlinks it.
	if left, _ := os.ReadDir(forwardDirFor(t, f.dir)); len(left) != 0 {
		t.Errorf("socket files left in the forward directory: %v", left)
	}
	for _, pid := range f.serverPids(t) {
		if !gone(pid) {
			t.Errorf("the server's adb client, pid %d, is still running", pid)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(ssWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func ssCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), ssWait)
}

func streamErr(t *testing.T, err error) *capability.Error {
	t.Helper()
	ce := capability.AsError(err)
	if ce == nil || ce.Code != ErrStreamFailed {
		t.Fatalf("error %v, want DEVICE_STREAM_FAILED", err)
	}
	return ce
}

const ssJar = "/data/local/tmp/droidyard-scrcpy-"

var ssSCID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ssForward is the live view's forward: the socket file, then the abstract socket.
var ssForward = regexp.MustCompile(`^-s ` + uiSerial + ` forward localfilesystem:(.+/screen-[0-9a-f]{12}) localabstract:scrcpy_(\S+)$`)

// Decision (ADR-0077 §1, §2; plan m2p step 3): a start pushes the server to the
// digest-named path, forwards a socket of its own, runs the server there with the pinned
// version and options, and then the stream reaches a viewer as the device sent it, from
// the key frame with the config in front. The dummy byte the server sends first is read
// once, by Demux (peeked, not consumed, by the connect). Seen failing with the dummy byte
// consumed by the connect (Read for Peek): Demux read its preamble one byte off and failed
// the start.
func TestScreenSourceStreams(t *testing.T) {
	data, pkts, keyEnd := ssFixture(t)
	f := newSSFixture(t, screenListenerOpts{head: data[:keyEnd], tail: data[keyEnd:], gateTail: true}, nil)
	src, release := f.mustAcquire(t)
	f.waitServers(t, 1)

	if got := src.Size(); got != (screen.Size{Width: 590, Height: 1280}) {
		t.Errorf("size %v, want 590x1280, the recording's", got)
	}
	ctx, cancel := ssCtx()
	defer cancel()
	v := src.Join()
	first, err := v.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := append(append([]byte(nil), pkts[0].Data...), pkts[1].Data...)
	if !first.Key || !first.Config || !bytes.Equal(first.Data, wantFirst) {
		t.Errorf("first packet key=%v config=%v %d bytes, want the key frame with the config in front (%d bytes)",
			first.Key, first.Config, len(first.Data), len(wantFirst))
	}
	f.ln.Release()
	for i := 2; i < len(pkts); i++ {
		p, err := v.Next(ctx)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if p.Key != pkts[i].Key || p.PTS != pkts[i].PTS || !bytes.Equal(p.Data, pkts[i].Data) {
			t.Fatalf("packet %d differs from the device's", i)
		}
	}

	// The fake's log: push, then forward, then the server, with the digest-named path and
	// the pinned version. The options are spelled out here: they are the contract.
	// The source's FLAG_SECURE poll (ADR-0080 §3) may have run its `shell dumpsys window`
	// already: it is not part of the start.
	jar := ssJar + scrcpyserver.SHA256[:12] + ".jar"
	var calls []string
	for _, c := range f.calls(t) {
		if !strings.HasSuffix(c, " shell dumpsys window") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("calls %q, want push, forward, shell", calls)
	}
	if want := "-s " + uiSerial + " push LOCAL " + jar; calls[0] != want {
		t.Errorf("call 0 = %q, want %q", calls[0], want)
	}
	// The forward is to a socket file of the daemon's own, in its forward directory
	// (ADR-0102), named screen-<12 hex>.
	m := ssForward.FindStringSubmatch(calls[1])
	if m == nil {
		t.Fatalf("call 1 = %q, want a forward to localfilesystem:<dir>/screen-<12 hex> and scrcpy_<scid>", calls[1])
	}
	if dir := forwardDirFor(t, f.dir); filepath.Dir(m[1]) != dir {
		t.Errorf("the forward's socket %q is not in the forward directory %q", m[1], dir)
	}
	scid := m[2]
	if !ssSCID.MatchString(scid) {
		t.Errorf("scid %q is not 8 lowercase hex digits", scid)
	}
	if n, _ := strconv.ParseUint(scid, 16, 32); n > 0x7fffffff {
		t.Errorf("scid %s is past 31 bits: the server parses it as a non-negative int", scid)
	}
	wantShell := "-s " + uiSerial + " shell CLASSPATH=" + jar + " app_process / com.genymobile.scrcpy.Server " +
		scrcpyserver.Version + " scid=" + scid + " log_level=info tunnel_forward=true video=true audio=false " +
		"control=true video_codec=h264 max_size=1280 video_bit_rate=4000000 max_fps=60 " +
		"video_codec_options=i-frame-interval=2 clipboard_autosync=false cleanup=true"
	if calls[2] != wantShell {
		t.Errorf("call 2 = %q\nwant      %q", calls[2], wantShell)
	}
	if b, err := os.ReadFile(filepath.Join(f.dir, uiSerial+".scrcpypushed")); err != nil || !bytes.Equal(b, scrcpyserver.Server()) {
		t.Errorf("the pushed bytes are not the embedded server's (err %v)", err)
	}

	release()
	if len(f.callsWith(t, "forward --remove localfilesystem:")) != 1 {
		t.Errorf("calls %q, want one forward --remove", f.calls(t))
	}
	f.requireClean(t)
	eventually(t, "both sockets closed on the server's side", func() bool {
		v, c := f.ln.closed()
		return v == 1 && c == 1
	})
}

// Decision (ADR-0078 §5; plan m2p step 3): the connect treats a connection that closes
// without the dummy byte as the server not listening yet and tries again, so a start
// behind a forward that is up before the server is up still succeeds, with one server.
func TestScreenSourceConnectRetries(t *testing.T) {
	data, _, _ := ssFixture(t)
	f := newSSFixture(t, screenListenerOpts{head: data, refuse: 3}, nil)
	_, release := f.mustAcquire(t)
	f.waitServers(t, 1)
	if n := len(f.callsWith(t, "app_process")); n != 1 {
		t.Errorf("%d servers started, want 1", n)
	}
	release()
	f.requireClean(t)
}

// Decision (ADR-0078 §5; plan m2p step 3): the source is shared by every holder of the
// device. Two acquires start one server; releasing one keeps it; releasing the last
// closes the sockets, kills the child by its pid, removes the forward
// (`forward --remove localfilesystem:<path>`) and its record, and returns only when that is done.
// Seen failing with the refcount removed (the first release ends the source): the server
// was gone while a holder still used it.
func TestScreenSourceSharedPerDevice(t *testing.T) {
	f := startedFixture(t, nil)
	type got struct {
		s       *screenSource
		release func()
		err     error
	}
	res := make(chan got, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ctx, cancel := ssCtx()
			defer cancel()
			s, r, err := f.acquire(ctx)
			res <- got{s, r, err}
		}()
	}
	a, b := <-res, <-res
	if a.err != nil || b.err != nil {
		t.Fatalf("acquire: %v, %v", a.err, b.err)
	}
	if a.s != b.s {
		t.Fatal("two acquires of one device got two sources")
	}
	f.waitServers(t, 1)
	if n := len(f.callsWith(t, "app_process")); n != 1 {
		t.Fatalf("%d servers started, want 1", n)
	}
	rec := f.waitRecord(t)
	if rec.Kind != "device-stream" {
		t.Errorf("record kind %q, want device-stream", rec.Kind)
	}
	fwdCall := f.callsWith(t, "forward localfilesystem:")[0]
	sock := devicetest.ForwardSocket(t, f.dir, "scrcpy_"+fwdCall[strings.LastIndex(fwdCall, "scrcpy_")+len("scrcpy_"):])
	if target, err := os.Readlink(sock); err != nil || target != f.ln.Path {
		t.Errorf("forward socket %q leads to %q (%v), want the listener's %q", sock, target, err, f.ln.Path)
	}

	a.release()
	a.release() // idempotent: a second call must not drop the other holder's reference
	select {
	case <-a.s.Done():
		t.Fatal("the source ended with one holder left")
	case <-time.After(200 * time.Millisecond):
	}
	if recs := f.records(t); len(recs) != 1 || recs[0].PID != rec.PID {
		t.Errorf("records %+v, want the server's alone", recs)
	}
	if len(f.callsWith(t, "forward --remove")) != 0 {
		t.Error("the forward was removed with one holder left")
	}

	b.release()
	// release returned: the child is reaped, its record removed, its forward removed.
	if !gone(rec.PID) {
		t.Errorf("pid %d survived the last release", rec.PID)
	}
	if rm := f.callsWith(t, "forward --remove localfilesystem:"+sock); len(rm) != 1 {
		t.Errorf("calls %q, want one forward --remove localfilesystem:%s", f.calls(t), sock)
	}
	f.requireClean(t)
	select {
	case <-b.s.Done():
	default:
		t.Error("Done is not closed after the last release")
	}
	if err := b.s.Reason(); !errors.Is(err, screen.ErrClosed) {
		t.Errorf("reason %v, want screen.ErrClosed: nobody is left to read it", err)
	}

	// A new acquire after the end starts a new server.
	_, r3 := f.mustAcquire(t)
	f.waitServers(t, 2)
	if n := len(f.callsWith(t, "app_process")); n != 2 {
		t.Errorf("%d servers started in all, want 2", n)
	}
	r3()
	f.requireClean(t)
}

// Decision (plan m2p step 3): Inject writes EncodeControl's messages, against the stream's
// size, to the control socket; injections are serialised so two gestures never interleave
// their touches; a gesture cut short by its context still lifts the finger. Seen failing
// without the serialisation (the semaphore removed): the second tap's down landed between
// the first's down and up.
func TestScreenSourceInject(t *testing.T) {
	f := startedFixture(t, nil)
	src, release := f.mustAcquire(t)
	defer release()
	size := screen.Size{Width: 590, Height: 1280}
	enc := func(in screen.Input) []byte {
		cs, err := screen.EncodeControl(in, size)
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, c := range cs {
			b = append(b, c.Data...)
		}
		return b
	}
	ctx, cancel := ssCtx()
	defer cancel()
	waitControl := func(n int) []byte {
		t.Helper()
		eventually(t, fmt.Sprintf("%d control bytes", n), func() bool { return len(f.ln.controlBytes()) >= n })
		return f.ln.controlBytes()
	}

	tap := screen.Tap{X: 0.5, Y: 0.25}
	if err := src.Inject(ctx, tap); err != nil {
		t.Fatal(err)
	}
	want := enc(tap)
	if got := waitControl(len(want)); !bytes.Equal(got, want) {
		t.Fatalf("control bytes % x, want EncodeControl's % x", got, want)
	}

	// Serialised: two long presses at once are down, up, down, up in either order.
	a, b := screen.Tap{X: 0.1, Y: 0.1, Hold: 100 * time.Millisecond}, screen.Tap{X: 0.9, Y: 0.9, Hold: 100 * time.Millisecond}
	var wg sync.WaitGroup
	for _, in := range []screen.Input{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := src.Inject(ctx, in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	prev := len(want)
	both := waitControl(prev + len(enc(a)) + len(enc(b)))[prev:]
	if ab, ba := append(enc(a), enc(b)...), append(enc(b), enc(a)...); !bytes.Equal(both, ab) && !bytes.Equal(both, ba) {
		t.Errorf("two gestures interleaved: % x", both)
	}
	prev += len(both)

	// A swipe whose context ends after its first message still lifts the finger.
	swipe := screen.Swipe{Points: []screen.Point{{X: 0.5, Y: 0.8}, {X: 0.5, Y: 0.2, T: 5 * time.Second}}}
	cs, _ := screen.EncodeControl(swipe, size)
	cctx, ccancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer ccancel()
	if err := src.Inject(cctx, swipe); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a cut swipe returned %v, want the context's error", err)
	}
	wantCut := append(append([]byte(nil), cs[0].Data...), cs[len(cs)-1].Data...)
	if got := waitControl(prev + len(wantCut))[prev:]; !bytes.Equal(got, wantCut) {
		t.Errorf("a cut swipe wrote % x, want its down and its up % x", got, wantCut)
	}

	// An input the encoding refuses writes nothing.
	prev += len(wantCut)
	if err := src.Inject(ctx, screen.Tap{X: 2}); !errors.Is(err, screen.ErrBadInput) {
		t.Errorf("a tap outside the frame returned %v, want ErrBadInput", err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(f.ln.controlBytes()); n != prev {
		t.Errorf("%d control bytes after a refused input, want %d", n, prev)
	}
}

// Decision (ADR-0078 §6): the server's child exiting ends the source with deviceGone when
// device.list no longer holds the serial online (absent, or offline as an emulator shutting
// down is) and otherwise with exited, naming the exit status and never device text; the
// viewers see that reason, and the record, the process and the forward are gone.
func TestScreenSourceDeviceGone(t *testing.T) {
	offline := strings.Replace(phoneLine, " device ", " offline ", 1)
	for _, tc := range []struct {
		name, list, reason string
	}{
		{"absent", uiList(), StopDeviceGone},
		{"offline", uiList(offline), StopDeviceGone},
		{"online", uiList(phoneLine), StopExited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startedFixture(t, nil)
			src, release := f.mustAcquire(t)
			defer release()
			v := src.Join()
			devicetest.Write(t, f.dir, "devices.out", []byte(tc.list))
			devicetest.Write(t, f.dir, uiSerial+".scrcpy.stop", []byte("3"))
			select {
			case <-src.Done():
			case <-time.After(ssWait):
				t.Fatal("the source did not end with its child")
			}
			var end *ScreenEnd
			if !errors.As(src.Reason(), &end) || end.Reason != tc.reason {
				t.Fatalf("reason %v, want %s", src.Reason(), tc.reason)
			}
			if end.Cause != "adb shell exited with status 3" {
				t.Errorf("cause %q, want the exit status", end.Cause)
			}
			ctx, cancel := ssCtx()
			defer cancel()
			for { // the viewer drains what it was sent, then sees the reason
				if _, err := v.Next(ctx); err != nil {
					if !errors.As(err, &end) || end.Reason != tc.reason {
						t.Fatalf("viewer ended with %v, want %s", err, tc.reason)
					}
					break
				}
			}
			release()
			f.requireClean(t)
		})
	}
}

// Decision (ADR-0041 §6, ADR-0078 §6): StopAll ends every source and returns only when
// its child is reaped, its record removed and its forward removed, even while a holder
// still has the source; the viewers see exited, the holder's release then returns at
// once. The forward's removal is made slow here, so a StopAll that did not wait for the
// source to leave the set returns while the forward is still there. Seen failing with
// the wait removed from StopAll: the forward was still listed when StopAll returned.
func TestScreenSourceStopAllWaits(t *testing.T) {
	f := startedFixture(t, map[string]string{uiSerial + ".forwardremove.delay": "400"})
	src, release := f.mustAcquire(t)
	v := src.Join()
	rec := f.waitRecord(t)

	ctx, cancel := ssCtx()
	defer cancel()
	f.adb.screens.StopAll(ctx)
	if !gone(rec.PID) {
		t.Errorf("pid %d survived StopAll", rec.PID)
	}
	f.requireClean(t)
	var end *ScreenEnd
	if err := src.Reason(); !errors.As(err, &end) || end.Reason != StopExited {
		t.Errorf("reason %v, want exited", err)
	}
	for {
		if _, err := v.Next(ctx); err != nil {
			if !errors.As(err, &end) || end.Reason != StopExited {
				t.Errorf("viewer ended with %v, want exited", err)
			}
			break
		}
	}
	done := make(chan struct{})
	go func() { release(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("release after StopAll blocked")
	}
}

// Decision (ADR-0041 §6, as streamSet.add): once StopAll has begun, no source starts, and
// nothing is run on the device. Seen failing with the refusal removed: the acquire pushed
// and started a server the daemon's shutdown had already passed.
func TestScreenSourceRefusedAfterStopAll(t *testing.T) {
	f := startedFixture(t, nil)
	ctx, cancel := ssCtx()
	defer cancel()
	f.adb.screens.StopAll(ctx)
	_, _, err := f.acquire(ctx)
	ce := streamErr(t, err)
	if !strings.Contains(ce.Cause, "stopping") {
		t.Errorf("cause %q, want it to say the daemon is stopping", ce.Cause)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("the fake was run: %q", calls)
	}
	f.requireClean(t)
}

// Decision (plan m2p step 3): a cancel during a start leaves no record, no forward and no
// child when it was the last one waiting; a cancel of one of several waiters does not stop
// the start the others wait for; and a waiter behind a start that fails gets its own
// DEVICE_STREAM_FAILED, once the start has cleaned up after itself. Seen failing with an
// acquire that does not give up its reference when cancelled (a), with the cancel ending
// the source whatever the other waiters (b), and with the failure announced before the
// teardown (c): a server left running, the others' start killed, and a forward still
// listed when the error was returned.
func TestScreenSourceCancelledAcquire(t *testing.T) {
	t.Run("last waiter cancels", func(t *testing.T) {
		// The server is up but sends nothing: the start waits in connect.
		f := newSSFixture(t, screenListenerOpts{silent: true}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() {
			_, _, err := f.acquire(ctx)
			errc <- err
		}()
		f.waitRecord(t)
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire returned %v, want the context's error", err)
		}
		// acquire returned: the abandoned start is already cleaned up.
		if len(f.callsWith(t, "forward --remove localfilesystem:")) != 1 {
			t.Errorf("calls %q, want the forward removed", f.calls(t))
		}
		f.requireClean(t)
	})

	t.Run("a waiter remains", func(t *testing.T) {
		data, _, _ := ssFixture(t)
		f := newSSFixture(t, screenListenerOpts{head: data, gateHead: true}, nil)
		ctxA, cancelA := context.WithCancel(context.Background())
		errA := make(chan error, 1)
		go func() {
			_, _, err := f.acquire(ctxA)
			errA <- err
		}()
		rec := f.waitRecord(t)
		type res struct {
			s   *screenSource
			r   func()
			err error
		}
		resB := make(chan res, 1)
		go func() {
			ctx, cancel := ssCtx()
			defer cancel()
			s, r, err := f.acquire(ctx)
			resB <- res{s, r, err}
		}()
		eventually(t, "the second waiter", func() bool {
			f.adb.screens.mu.Lock()
			defer f.adb.screens.mu.Unlock()
			s := f.adb.screens.by[uiSerial]
			return s != nil && s.refs == 2
		})
		cancelA()
		if err := <-errA; !errors.Is(err, context.Canceled) {
			t.Fatalf("the first acquire returned %v, want the context's error", err)
		}
		if recs := f.records(t); len(recs) != 1 || recs[0].PID != rec.PID {
			t.Fatalf("records %+v: the cancel stopped a start another caller waits for", recs)
		}
		f.ln.Release()
		b := <-resB
		if b.err != nil {
			t.Fatalf("the waiter's acquire: %v", b.err)
		}
		f.waitServers(t, 1)
		if n := len(f.callsWith(t, "app_process")); n != 1 {
			t.Errorf("%d servers started, want 1", n)
		}
		b.r()
		f.requireClean(t)
	})

	t.Run("a waiter behind a failing start", func(t *testing.T) {
		old := screenConnectBound
		screenConnectBound = 400 * time.Millisecond
		defer func() { screenConnectBound = old }()
		f := newSSFixture(t, screenListenerOpts{silent: true},
			map[string]string{uiSerial + ".forwardremove.delay": "300"})
		const n = 3
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			go func() {
				ctx, cancel := ssCtx()
				defer cancel()
				_, _, err := f.acquire(ctx)
				errs <- err
			}()
		}
		for i := 0; i < n; i++ {
			ce := streamErr(t, <-errs)
			if !strings.HasPrefix(ce.Cause, "connect:") {
				t.Errorf("cause %q, want the connect phase", ce.Cause)
			}
			// Every waiter's error is returned after the teardown, not before it.
			if len(f.callsWith(t, "forward --remove localfilesystem:")) == 0 || len(devicetest.Forwards(t, f.dir)) != 0 {
				t.Errorf("a waiter was told of the failure while the forward was still listed: %v", devicetest.Forwards(t, f.dir))
			}
		}
		// At most one: under -race the fake may be killed before it logs its start.
		if got := len(f.callsWith(t, "app_process")); got > 1 {
			t.Errorf("%d servers started for %d waiters, want 1", got, n)
		}
		f.requireClean(t)
	})
}

// Decision (ADR-0078 §1): every failure at start is DEVICE_STREAM_FAILED whose cause names
// the phase and the exit status and carries no device text, and a failed start leaves no
// forward, record or process. Seen failing with the digest check removed (the corrupted
// server was pushed) and with a cause that quoted the client's stderr (the marker text
// reached the error).
func TestScreenSourceStartFailures(t *testing.T) {
	const marker = "DEVICE-TEXT-MARKER"
	data, _, _ := ssFixture(t)
	preamble := append([]byte{0}, make([]byte, 64)...)
	for _, tc := range []struct {
		name   string
		opts   screenListenerOpts
		files  map[string]string
		bound  bool // connect and first-packet bounds shortened
		phase  string
		cause  string
		pushed bool
	}{
		{name: "push", opts: screenListenerOpts{head: data},
			files: map[string]string{uiSerial + ".pushscrcpy.exit": "1", uiSerial + ".pushscrcpy.err": marker},
			phase: "push", cause: "adb push exited with status 1"},
		{name: "forward", opts: screenListenerOpts{head: data},
			files: map[string]string{uiSerial + ".forwardscrcpy.exit": "1", uiSerial + ".forwardscrcpy.err": marker},
			phase: "forward", cause: "adb forward exited with status 1", pushed: true},
		// The adb client was cut short after its server made the forward: the source removes
		// it by its own socket, without asking the listing (requireClean sees the table and
		// the directory empty). Seen failing with the removal skipped for a forward that did
		// not report success.
		{name: "forward cut short after it was made", opts: screenListenerOpts{head: data},
			files: map[string]string{uiSerial + ".forwardscrcpy.exit": "1", uiSerial + ".forwardscrcpy.made": "1",
				uiSerial + ".forwardlist.exit": "1"},
			phase: "forward", cause: "adb forward exited with status 1", pushed: true},
		{name: "server exits at start", opts: screenListenerOpts{refuseAll: true},
			files: map[string]string{uiSerial + ".scrcpyserver.exit": "1", uiSerial + ".scrcpyserver.err": marker},
			phase: "start", cause: "adb shell exited with status 1", pushed: true},
		{name: "no connect", opts: screenListenerOpts{refuseAll: true}, bound: true,
			phase: "connect", cause: "the server's sockets did not answer", pushed: true},
		{name: "not the protocol", opts: screenListenerOpts{head: append(append([]byte(nil), preamble...), []byte(marker)...)},
			phase: "protocol", cause: "the video stream was not in the expected format", pushed: true},
		{name: "no config", opts: screenListenerOpts{head: append(append([]byte(nil), preamble...), "h264"...)}, bound: true,
			phase: "protocol", cause: "no config packet arrived", pushed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.bound {
				oc, of := screenConnectBound, screenFirstBound
				screenConnectBound, screenFirstBound = 400*time.Millisecond, 400*time.Millisecond
				defer func() { screenConnectBound, screenFirstBound = oc, of }()
			}
			f := newSSFixture(t, tc.opts, tc.files)
			ctx, cancel := ssCtx()
			defer cancel()
			_, _, err := f.acquire(ctx)
			ce := streamErr(t, err)
			if !strings.HasPrefix(ce.Cause, tc.phase+": ") || !strings.Contains(ce.Cause, tc.cause) {
				t.Errorf("cause %q, want phase %q and %q", ce.Cause, tc.phase, tc.cause)
			}
			if all := ce.Message + ce.Cause + ce.NextStep; strings.Contains(all, marker) {
				t.Errorf("the error carries device text: %q", all)
			}
			if got := len(f.callsWith(t, " push ")) == 1; got != (tc.pushed || tc.phase == "push") {
				t.Errorf("push ran = %v, calls %q", got, f.calls(t))
			}
			f.requireClean(t)
		})
	}

	t.Run("digest", func(t *testing.T) {
		old := screenServerBytes
		screenServerBytes = func() []byte { return append(old(), 0) }
		defer func() { screenServerBytes = old }()
		f := startedFixture(t, nil)
		ctx, cancel := ssCtx()
		defer cancel()
		_, _, err := f.acquire(ctx)
		if ce := streamErr(t, err); !strings.HasPrefix(ce.Cause, "digest: ") {
			t.Errorf("cause %q, want the digest phase", ce.Cause)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Errorf("the fake was run: %q", calls)
		}
	})
}

// Decision (plan m2p §Changes, step 3): a video stream that fails or is cut ends the source
// with the reason the device's state gives: the device absent or offline is deviceGone
// (it cuts the socket and the child about together, in either order), online with the
// child still running is failed (a protocol error of a live server). Seen failing with the
// check removed (the reason straight to failed): the cut stream of an absent device ended
// as failed.
func TestScreenSourceCutStreamReason(t *testing.T) {
	data, _, keyEnd := ssFixture(t)
	for _, tc := range []struct {
		name   string
		tail   []byte
		list   string
		reason string
	}{
		// Five bytes of a twelve-byte packet header, then the socket closes.
		{"cut, device absent", []byte{0, 0, 0, 0, 0}, uiList(), StopDeviceGone},
		{"cut, device offline", []byte{0, 0, 0, 0, 0}, uiList(strings.Replace(phoneLine, " device ", " offline ", 1)), StopDeviceGone},
		// A header flagged both config and key frame is not the protocol.
		{"protocol error, device online", []byte{0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, uiList(phoneLine), StopFailed},
		{"cut, device online", []byte{0, 0, 0, 0, 0}, uiList(phoneLine), StopFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSFixture(t, screenListenerOpts{head: data[:keyEnd], tail: tc.tail, gateTail: true, cutAfterTail: true}, nil)
			src, release := f.mustAcquire(t)
			defer release()
			devicetest.Write(t, f.dir, "devices.out", []byte(tc.list))
			f.ln.Release()
			select {
			case <-src.Done():
			case <-time.After(ssWait):
				t.Fatal("the source survived a broken video stream")
			}
			var end *ScreenEnd
			if !errors.As(src.Reason(), &end) || end.Reason != tc.reason {
				t.Fatalf("reason %v, want %s", src.Reason(), tc.reason)
			}
			release()
			f.requireClean(t)
		})
	}
}

// Decision (plan m2p step 3): what the control socket sends is read and dropped; past the
// bound the server is not behaving as pinned and the source ends as failed, without
// device text. With clipboard sync off a real server sends nothing.
func TestScreenSourceControlFlood(t *testing.T) {
	data, _, _ := ssFixture(t)
	flood := bytes.Repeat([]byte("x"), 2<<20)
	t.Run("after the start", func(t *testing.T) { floodAfterStart(t, data, flood) })
	t.Run("during the start", func(t *testing.T) {
		// The server's preamble and then nothing, and the control socket floods: the start
		// ends with the protocol failure, not with a plain stop.
		old := screenFirstBound
		screenFirstBound = 20 * time.Second
		defer func() { screenFirstBound = old }()
		preamble := append(append([]byte{0}, make([]byte, 64)...), "h264"...)
		f := newSSFixture(t, screenListenerOpts{head: preamble, controlOut: flood}, nil)
		ctx, cancel := ssCtx()
		defer cancel()
		_, _, err := f.acquire(ctx)
		if ce := streamErr(t, err); ce.Cause != "protocol: the control socket sent more than expected" {
			t.Errorf("cause %q, want the protocol failure", ce.Cause)
		}
		f.requireClean(t)
	})
}

func floodAfterStart(t *testing.T, data, flood []byte) {
	f := newSSFixture(t, screenListenerOpts{head: data, controlOut: flood, gateControl: true}, nil)
	src, release := f.mustAcquire(t)
	defer release()
	f.ln.Release()
	select {
	case <-src.Done():
	case <-time.After(ssWait):
		t.Fatal("the source survived a flooded control socket")
	}
	var end *ScreenEnd
	if !errors.As(src.Reason(), &end) || end.Reason != StopFailed || end.Cause != "the control socket sent more than expected" {
		t.Errorf("reason %v, want failed because of the control socket", src.Reason())
	}
	release()
	f.requireClean(t)
}

// Decision (plan m2p step 3): "a -race storm of acquire and release". Many holders come and
// go, injecting, while sources start and end; whatever the interleaving, every server
// started was ended with its forward removed, nothing is left, and the counts of pushes,
// forwards, servers and removals agree.
func TestScreenSourceStorm(t *testing.T) {
	t.Run("holders", stormHolders)
	t.Run("abandoned starts", stormAbandoned)
}

// stormAbandoned: holders whose contexts end at every point of a start (and after it),
// so sources are abandoned during the push, the forward, the connect and the first
// packet, and acquires land on sources that are ending. Whatever the interleaving,
// nothing is left.
func stormAbandoned(t *testing.T) {
	f := startedFixture(t, nil)
	const holders, rounds = 4, 8
	var wg sync.WaitGroup
	for h := 0; h < holders; h++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration((h*7+i*13)%16)*100*time.Millisecond)
				_, release, err := f.acquire(ctx)
				cancel()
				if err == nil {
					release()
				} else if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
					t.Errorf("acquire: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	// Every forward is removed; a forward cut short before it ran is removed by name too, and
	// adb refuses that harmlessly, so removals may exceed forwards by those, never the starts.
	if fwd, rm, push := len(f.callsWith(t, "forward localfilesystem:")), len(f.callsWith(t, "forward --remove")), len(f.callsWith(t, " push ")); rm < fwd || rm > push {
		t.Errorf("pushes %d, forwards %d, removals %d: want every forward removed, and no more than one removal a start", push, fwd, rm)
	}
	f.requireClean(t)
	t.Logf("%d pushes, %d forwards, %d removals", len(f.callsWith(t, " push ")), len(f.callsWith(t, "forward localfilesystem:")), len(f.callsWith(t, "forward --remove")))
}

func stormHolders(t *testing.T) {
	f := startedFixture(t, nil)
	const holders, rounds = 4, 3
	var wg sync.WaitGroup
	var failed atomic.Bool
	for h := 0; h < holders; h++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				ctx, cancel := ssCtx()
				src, release, err := f.acquire(ctx)
				if err != nil {
					t.Errorf("acquire: %v", err)
					failed.Store(true)
					cancel()
					return
				}
				v := src.Join()
				src.Inject(ctx, screen.Tap{X: 0.5, Y: 0.5})
				if _, err := v.Next(ctx); err != nil {
					t.Errorf("Next: %v", err)
				}
				time.Sleep(time.Duration(10+h*15) * time.Millisecond)
				v.Close()
				release()
				cancel()
			}
		}()
	}
	wg.Wait()
	if failed.Load() {
		return
	}
	// Every start was pushed, forwarded and, once ended, unforwarded. The server's own log
	// line is not counted: under -race the fake can be killed before it writes it.
	push, fwd, rm := len(f.callsWith(t, " push ")), len(f.callsWith(t, "forward localfilesystem:")), len(f.callsWith(t, "forward --remove"))
	if push < 1 || push != fwd || fwd != rm {
		t.Errorf("pushes %d, forwards %d, removals %d: want equal and at least 1", push, fwd, rm)
	}
	if srv := len(f.callsWith(t, "app_process")); srv > push {
		t.Errorf("%d servers started for %d pushes", srv, push)
	}
	f.requireClean(t)
	t.Logf("%d holders x %d rounds took %d starts", holders, rounds, push)
}

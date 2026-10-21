package device

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen/scrcpyserver"
)

// The live view's source on the device (plan m2p step 3; ADR-0077, ADR-0078 §5, §6): scrcpy's
// server, pushed, run as a recorded `device-stream` child over a forward to a socket file (ADR-0102), its video
// socket demuxed into a screen.Hub and its control socket written through the control line
// (controlline.go): by Inject for a call's gesture, by the line's writer for a view's input.
//
// There is one source per device, shared and reference-counted. A source's life is one
// goroutine, run: it starts the server, waits to be ended, and tears everything down. Every
// way it ends (the last release, the server's child exiting, a demux error, StopAll, an
// acquire cancelled while the start is under way) goes through end, which decides the
// reason once and cancels the source's own context; teardown then closes the sockets, kills
// the child by its pid (never its group) and waits for it to be reaped, which removes its
// pid record, and removes the forward. Only then does the source leave the set and `done`
// close, which is what StopAll, the last release and a caller that wants a fresh source
// wait for.

// StopFailed joins StopDeviceGone and StopExited as a reason a screen source ended
// (ADR-0078 §2): a protocol error of the stream.
const StopFailed = "failed"

// ScreenEnd is why a source ended: the error its viewers' Next and Source.Reason return
// (ADR-0078 §2). Reason is one of StopDeviceGone, StopExited and StopFailed; Cause is ours,
// never device text.
type ScreenEnd struct{ Reason, Cause string }

func (e *ScreenEnd) Error() string { return "screen ended (" + e.Reason + "): " + e.Cause }

// The server's start (docs/research/spike-screen-stream-2026-10.md §(a)): the arguments
// after <version> scid=<8 hex>.
var screenServerOptions = []string{
	"log_level=info", "tunnel_forward=true", "video=true", "audio=false", "control=true",
	"video_codec=h264", "max_size=1280", "video_bit_rate=4000000", "max_fps=60",
	"video_codec_options=i-frame-interval=2", "clipboard_autosync=false", "cleanup=true",
}

const (
	// screenJarPrefix and the digest's first 12 hex name the jar on the device, so a jar
	// of another build is never mistaken for this one (as the reader's dex is named).
	screenJarDir    = "/data/local/tmp/"
	screenJarPrefix = "droidyard-scrcpy-"
	screenAbstract  = "scrcpy_"
	screenClass     = "com.genymobile.scrcpy.Server"

	// screenDiscardCap bounds what the control socket may send before the source ends as
	// failed: with clipboard sync off the server sends nothing.
	screenDiscardCap = 1 << 20

	screenRetryPause = 100 * time.Millisecond
)

// The start's working bounds; variables so a test is quick.
var (
	screenPushTimeout    = 30 * time.Second
	screenForwardTimeout = 5 * time.Second
	// screenConnectBound bounds both connects together (plan m2p step 3, Start 5);
	// screenFirstBound then bounds the wait for the first config packet, which proves the
	// bytes are the protocol and gives the frame size.
	screenConnectBound = 5 * time.Second
	screenFirstBound   = 5 * time.Second
	screenCleanupBound = 5 * time.Second
	screenWriteBound   = 2 * time.Second
	// screenChildExitBound is how long teardown lets the server exit on its own after the
	// control socket closed behind a cancel, before the child is killed.
	screenChildExitBound = time.Second
)

// screenServerBytes is the server the daemon pushes; a variable so a test can corrupt it
// and see the digest refuse it.
var screenServerBytes = scrcpyserver.Server

var errScreensStopping = errors.New("the daemon is stopping")

// startFailure is why a start failed, kept apart from the error each caller gets so that
// every waiter behind a failed start receives its own *capability.Error.
type startFailure struct {
	phase, detail string
	// adb says the adb client itself could not be run (not found, not executable, not
	// startable): DEVICE_ADB_FAILED, as in the preamble, not a stream failure.
	adb bool
}

func (f *startFailure) err() error {
	if f.adb {
		return capability.NewError(ErrADBFailed, "adb could not be run").
			WithCause(f.phase + ": " + f.detail).
			WithNextStep("Check that the adb binary is executable, then retry.")
	}
	return capability.NewError(ErrStreamFailed, "the device screen could not be streamed").
		WithCause(f.phase + ": " + f.detail).
		WithNextStep("Retry. If it keeps failing, check that the device is online and unlocked; device.list says whether it is.")
}

// screenSources is every live-view source of the daemon, at most one per device.
type screenSources struct {
	mu       sync.Mutex // guards stopping, by, all and each source's refs and ending
	stopping bool
	by       map[string]*screenSource
	all      map[*screenSource]struct{}
}

// screenSource is one device's live-view source. It implements screen.Source.
type screenSource struct {
	set    *screenSources
	adb    *ADB
	path   string // the adb binary
	serial string
	fast   bool // started for a peer marked fast: screenFastOptions (ADR-0118 §4)

	refs   int  // holders of the source, an acquire still starting included; set.mu
	ending bool // end was called; set.mu

	ctx    context.Context // the source's own: the callers' contexts never reach the child
	cancel context.CancelFunc
	hub    *screen.Hub

	// The child's own context. The source's ending does not kill the child by itself: a clean
	// end (the last viewer's) lets the server read the cancels written to its control socket
	// and exit on that socket's end (teardown); every other end kills it at once (close), and
	// teardown kills it whatever happened before.
	childCtx  context.Context
	killChild context.CancelFunc

	ready     chan struct{} // closed when the start finished, failed or not
	startFail *startFailure // set before ready closes
	done      chan struct{} // closed when the source left the set, child reaped and forward removed

	// Written by run's goroutine before the goroutines below start or ready closes.
	scid           string // the session id, which names the abstract socket
	forwarding     bool   // the forward command was run: a forward may exist whatever it answered
	forwarded      bool   // the forward command reported success
	sock           string // the forward's socket file, chosen before the forward runs
	cmd            *exec.Cmd
	stream         Stream
	video, control net.Conn
	br             *bufio.Reader

	childDone   chan struct{} // closed once the child was reaped
	childStatus int           // its exit status, -1 when it did not exit by itself
	demuxDone   chan struct{} // closed when Demux returned
	demuxErr    error
	first       chan struct{} // closed at the first config packet
	firstOnce   sync.Once
	wg          sync.WaitGroup // the demux, control and monitor goroutines

	line *controlLine // everything written to the control socket, and who holds the finger (controlline.go)

	secure secureState // the focused window's FLAG_SECURE, polled while the source runs (secure.go)
}

var _ screen.Source = (*screenSource)(nil)

// acquire returns the device's source, starting it when there is none, and the release
// the caller must call exactly when it is done (it is idempotent). Later callers for the
// serial wait on the start in progress; a start that fails fails every waiter. A caller
// whose ctx ends while waiting leaves, and the start is abandoned if it was the last one
// waiting. After StopAll has begun, acquire is refused.
func (ss *screenSources) acquireWith(ctx context.Context, a *ADB, t *target, fast bool) (*screenSource, func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		ss.mu.Lock()
		if ss.stopping {
			ss.mu.Unlock()
			return nil, nil, (&startFailure{phase: "start", detail: errScreensStopping.Error()}).err()
		}
		s := ss.by[t.serial]
		if s != nil && s.ending {
			// One source per device at a time: a new start waits for the old one's teardown.
			ss.mu.Unlock()
			select {
			case <-s.done:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		if s == nil {
			s = a.newScreenSource(ss, t)
			s.fast = fast // the first starter decides, for the source's life (ADR-0118 §4)
			if ss.by == nil {
				ss.by, ss.all = map[string]*screenSource{}, map[*screenSource]struct{}{}
			}
			ss.by[t.serial], ss.all[s] = s, struct{}{}
			go s.run()
		}
		s.refs++
		ss.mu.Unlock()

		select {
		case <-s.ready:
		case <-ctx.Done():
			s.unref()
			return nil, nil, ctx.Err()
		}
		if s.startFail != nil {
			return nil, nil, s.startFail.err()
		}
		var once sync.Once
		return s, func() { once.Do(s.unref) }, nil
	}
}

// StopAll ends every source and waits until each has left the set, which is when its
// child is reaped, its record removed and its forward removed. New acquires are refused
// from here on. It returns early only when ctx ends.
func (ss *screenSources) StopAll(ctx context.Context) {
	ss.mu.Lock()
	ss.stopping = true
	list := make([]*screenSource, 0, len(ss.all))
	for s := range ss.all {
		list = append(list, s)
	}
	ss.mu.Unlock()
	for _, s := range list {
		s.end(&ScreenEnd{Reason: StopExited, Cause: "the daemon is stopping"})
	}
	for _, s := range list {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

// Live is how many sources are in the set: zero once every child is reaped and every
// forward removed. Test-only in practice.
func (ss *screenSources) Live() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.all)
}

// running returns the device's source when it is started and not ending, and nil
// otherwise. It never starts one and holds no reference: the input capabilities use a live
// view that someone else opened (ADR-0079 §3), and the source may end the next instant, which
// Inject reports.
func (ss *screenSources) running(serial string) *screenSource {
	ss.mu.Lock()
	s := ss.by[serial]
	if s == nil || s.ending {
		ss.mu.Unlock()
		return nil
	}
	ss.mu.Unlock()
	select {
	case <-s.ready:
		if s.startFail != nil {
			return nil
		}
		return s
	default:
		return nil // still starting
	}
}

func (a *ADB) newScreenSource(ss *screenSources, t *target) *screenSource {
	ctx, cancel := context.WithCancel(context.Background())
	childCtx, killChild := context.WithCancel(context.Background())
	s := &screenSource{
		set: ss, adb: a, path: t.adb, serial: t.serial,
		ctx: ctx, cancel: cancel, hub: screen.NewHub(),
		childCtx: childCtx, killChild: killChild,
		ready: make(chan struct{}), done: make(chan struct{}),
		childDone: make(chan struct{}), demuxDone: make(chan struct{}), first: make(chan struct{}),
	}
	s.line = newControlLine(s.hub.Size, s.write, s.endOnWriteError, s.hub.Done(), s.hub.Reason)
	s.hub.OnLag(s.line.requestKeyFrame) // a lagging viewer asks the encoder for a key frame (ADR-0118 §2)
	return s
}

// unref drops one holder. The last one ends the source and waits until it is gone, so a
// caller that released the last reference knows the child is reaped and the forward
// removed when it returns.
//
// The last decrement claims the end in the same critical section (ending is set under the
// set's mutex with refs at zero), so an acquire that arrives next sees a source that is
// ending and waits for its teardown, never one that is about to close under it.
func (s *screenSource) unref() {
	s.set.mu.Lock()
	s.refs--
	last := s.refs == 0
	claimed := last && !s.ending
	if claimed {
		s.ending = true
	}
	s.set.mu.Unlock()
	if claimed {
		if screenAfterLastUnref != nil {
			screenAfterLastUnref()
		}
		s.close(nil)
	}
	if last {
		<-s.done
	}
}

// screenAfterLastUnref is a test hook, nil in production: it runs after the last
// reference was dropped and the end claimed, before the Hub closes, so a test can make an
// acquire arrive in that window.
var screenAfterLastUnref func()

// end decides why the source ended, once: the first reason wins. A nil reason is the end
// of the last viewer, which nobody is left to read. It closes the Hub, so every viewer's
// Next returns the reason, and cancels the source's context, which wakes run and kills
// the child. It never blocks, so any goroutine of the source may call it.
func (s *screenSource) end(reason error) {
	s.set.mu.Lock()
	if s.ending {
		s.set.mu.Unlock()
		return
	}
	s.ending = true
	s.set.mu.Unlock()
	s.close(reason)
}

// close carries out an end that was claimed by setting ending.
func (s *screenSource) close(reason error) {
	s.hub.Close(reason)
	s.cancel()
	if reason != nil && s.killChild != nil {
		s.killChild() // only the last viewer's end leaves the child to teardown
	}
}

// run is the source's life: start, wait to be ended, tear down, leave the set.
func (s *screenSource) run() {
	if f := s.start(); f != nil {
		s.end(nil)
		s.teardown()
		s.startFail = f
		s.finish()
		close(s.ready)
		return
	}
	close(s.ready)
	s.wg.Add(3)
	go s.monitor()
	go s.pollSecure()
	s.line.running.Store(true)
	go func() {
		defer s.wg.Done()
		s.line.run()
	}()
	<-s.ctx.Done()
	s.teardown()
	s.finish()
}

// finish takes the source out of the set and releases whoever waits for its end.
func (s *screenSource) finish() {
	ss := s.set
	ss.mu.Lock()
	if ss.by[s.serial] == s {
		delete(ss.by, s.serial)
	}
	delete(ss.all, s)
	ss.mu.Unlock()
	close(s.done)
}

// start runs the six steps of plan m2p step 3. It returns why it failed, or nil once the
// stream is running: the video and control sockets connected, the Demux reading, and the
// first config packet in the Hub.
func (s *screenSource) start() *startFailure {
	// 1. The digest of the bytes about to be pushed, not of the constant.
	jar := screenServerBytes()
	sum := sha256.Sum256(jar)
	digest := hex.EncodeToString(sum[:])
	if digest != scrcpyserver.SHA256 {
		return &startFailure{phase: "digest", detail: "the embedded server does not match its pinned digest"}
	}
	remote := screenJarDir + screenJarPrefix + digest[:12] + ".jar"

	// 2. Push.
	if f := s.push(jar, remote); f != nil {
		return f
	}

	// 3. Forward to a socket name of our own.
	scid, err := newSCID()
	if err != nil {
		return &startFailure{phase: "forward", detail: "no random number was available"}
	}
	s.scid = scid
	if f := s.forward(scid); f != nil {
		return f
	}

	// 4. The recorded child.
	if f := s.spawn(remote, scid); f != nil {
		return f
	}

	// 5. The sockets, the video one first.
	if f := s.connect(); f != nil {
		return f
	}

	// 6. Demux into the Hub; the first config proves the protocol and gives the size.
	s.wg.Add(2)
	go s.demux()
	go s.discardControl()
	return s.awaitFirst()
}

// newSCID is scrcpy's session id: 8 lowercase hex digits of a 31-bit number (the server
// parses it as a non-negative int).
func newSCID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x", binary.BigEndian.Uint32(b[:])&0x7fffffff), nil
}

// adbFailure says how an adb client failed, from its exit status or error, without any of
// its output (ADR-0078 §1).
func adbFailure(what string, out Output, err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return what + " timed out"
	case errors.Is(err, context.Canceled):
		return what + " was interrupted"
	case err != nil:
		return what + " could not be run"
	case out.ExitCode != 0:
		return what + " exited with status " + strconv.Itoa(out.ExitCode)
	}
	return ""
}

// adbFail is the start failure of an adb client that failed: DEVICE_ADB_FAILED when it
// could not be run at all (an error that is neither its timeout nor a cancel), a stream
// failure otherwise, and the plain stop when the source was ended meanwhile.
func (s *screenSource) adbFail(phase, detail string, err error) *startFailure {
	f := s.failure(phase, detail)
	if s.ctx.Err() == nil && err != nil && !errors.Is(err, ErrTimeout) && !errors.Is(err, context.Canceled) {
		f.adb = true
	}
	return f
}

func (s *screenSource) push(jar []byte, remote string) *startFailure {
	dir, err := os.MkdirTemp("", "droidyard-scrcpy-")
	if err != nil {
		return &startFailure{phase: "push", detail: "a temporary directory was unavailable"}
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "scrcpy-server.jar")
	if err := os.WriteFile(local, jar, 0o600); err != nil {
		return &startFailure{phase: "push", detail: "a temporary file was unavailable"}
	}
	out, err := s.adb.Run(s.ctx, s.path, screenPushTimeout, 1<<10, "-s", s.serial, "push", local, remote)
	if d := adbFailure("adb push", out, err); d != "" {
		return s.adbFail("push", d, err)
	}
	return nil
}

// failure is a start failure, unless the source was ended meanwhile: then the cause is
// that, whatever the cancelled command said.
func (s *screenSource) failure(phase, detail string) *startFailure {
	if s.ctx.Err() != nil {
		return s.stoppedFailure()
	}
	return &startFailure{phase: phase, detail: detail}
}

// stoppedFailure is the start failure of a source that was ended while it started: a
// protocol failure when that is what ended it (the control socket flooding before the
// first packet), else a plain stop.
func (s *screenSource) stoppedFailure() *startFailure {
	var end *ScreenEnd
	if errors.As(s.hub.Reason(), &end) && end.Reason == StopFailed {
		return &startFailure{phase: "protocol", detail: end.Cause}
	}
	return &startFailure{phase: "start", detail: "the stream was stopped while it started"}
}

func (s *screenSource) forward(scid string) *startFailure {
	sock, err := s.adb.forwardSocket(s.ctx, s.path, fwdKindScreen)
	if err != nil {
		return &startFailure{phase: "forward", detail: err.Error()}
	}
	s.sock, s.forwarding = sock, true
	out, err := s.adb.Run(s.ctx, s.path, screenForwardTimeout, 1<<10,
		"-s", s.serial, "forward", forwardLocalPrefix+sock, "localabstract:"+screenAbstract+scid)
	if d := adbFailure("adb forward", out, err); d != "" {
		return s.adbFail("forward", d, err)
	}
	s.forwarded = true
	return nil
}

func (s *screenSource) spawn(remote, scid string) *startFailure {
	if s.adb.stream == nil {
		return &startFailure{phase: "start", detail: "no recorded process starter is configured"}
	}
	args := append([]string{"-s", s.serial, "shell", "CLASSPATH=" + remote, "app_process", "/", screenClass,
		scrcpyserver.Version, "scid=" + scid}, s.serverOptions()...)
	cmd := exec.CommandContext(s.childCtx, s.path, args...)
	cmd.Env = s.adb.env
	cmd.Cancel = func() error { return cmd.Process.Kill() } // the pid, never the group
	cmd.WaitDelay = waitDelay
	// The server's output is device text and is never read: stdout and stderr go to the null device.
	st, err := s.adb.stream(cmd, deviceStreamKind)
	if err != nil {
		if errors.Is(err, ErrStreamUnrecorded) {
			return &startFailure{phase: "start", detail: "the pid record could not be written to the state store"}
		}
		return s.adbFail("start", "adb shell could not be started", err)
	}
	s.cmd, s.stream = cmd, st
	go func() {
		st.Wait() // reaps the child and removes its pid record
		s.childStatus = -1
		if cmd.ProcessState != nil {
			s.childStatus = cmd.ProcessState.ExitCode()
		}
		close(s.childDone)
	}()
	return nil
}

// childCause says how the child ended.
func (s *screenSource) childCause() string {
	if s.childStatus < 0 {
		return "adb shell was terminated"
	}
	return "adb shell exited with status " + strconv.Itoa(s.childStatus)
}

// connect opens the video socket and then the control socket over the forward, within
// screenConnectBound. The adb forward accepts a connection before the server listens and
// then drops it, so a connection counts only when the server's dummy byte arrives (it is
// peeked, not consumed: Demux reads it); anything else is retried.
func (s *screenSource) connect() *startFailure {
	deadline := time.Now().Add(screenConnectBound)
	for {
		if f := s.stopped(deadline); f != nil {
			return f
		}
		d := net.Dialer{Timeout: time.Until(deadline)}
		conn, err := d.DialContext(s.ctx, "unix", s.sock)
		if err == nil {
			br := bufio.NewReaderSize(conn, 64<<10)
			if s.dummyByte(conn, br, deadline) {
				s.video, s.br = conn, br
				break
			}
			conn.Close()
		}
		if f := s.pause(deadline); f != nil {
			return f
		}
	}
	for {
		if f := s.stopped(deadline); f != nil {
			return f
		}
		d := net.Dialer{Timeout: time.Until(deadline)}
		conn, err := d.DialContext(s.ctx, "unix", s.sock)
		if err == nil {
			s.control = conn
			return nil
		}
		if f := s.pause(deadline); f != nil {
			return f
		}
	}
}

// dummyByte waits for the first byte of a freshly connected video socket, in short
// slices so a child that exits or an ended source is noticed. False means the connection
// closed (or the wait ended) without one.
func (s *screenSource) dummyByte(conn net.Conn, br *bufio.Reader, deadline time.Time) bool {
	for {
		conn.SetReadDeadline(minTime(time.Now().Add(screenRetryPause), deadline))
		if _, err := br.Peek(1); err == nil {
			conn.SetReadDeadline(time.Time{})
			return true
		} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			return false // closed by the forward: the server is not listening yet
		}
		select {
		case <-s.childDone:
			return false
		case <-s.ctx.Done():
			return false
		default:
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// stopped says why connecting cannot go on: the source was ended, the child is gone (the
// server failed to start), or the bound passed.
func (s *screenSource) stopped(deadline time.Time) *startFailure {
	select {
	case <-s.ctx.Done():
		return s.stoppedFailure()
	case <-s.childDone:
		return &startFailure{phase: "start", detail: s.childCause()}
	default:
	}
	if !time.Now().Before(deadline) {
		return &startFailure{phase: "connect", detail: "the server's sockets did not answer within " + screenConnectBound.String()}
	}
	return nil
}

// pause waits before the next connect attempt, or says why there will be none.
func (s *screenSource) pause(deadline time.Time) *startFailure {
	t := time.NewTimer(screenRetryPause)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-s.ctx.Done():
	case <-s.childDone:
	}
	return s.stopped(deadline)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// demux reads the video socket into the Hub until it ends.
func (s *screenSource) demux() {
	defer s.wg.Done()
	err := screen.Demux(s.br, func(p screen.Packet) error {
		s.hub.Push(p)
		probePacket("scrcpy", p)
		if p.Config {
			s.firstOnce.Do(func() { close(s.first) })
			s.line.sizeChanged() // a finger held for the old size is lifted for this one
		}
		return nil
	})
	s.demuxErr = err
	close(s.demuxDone)
}

// discardControl reads and drops what the control socket sends, up to screenDiscardCap:
// past it the server is not behaving as pinned and the source ends as failed.
func (s *screenSource) discardControl() {
	defer s.wg.Done()
	buf := make([]byte, 4<<10)
	n := 0
	for {
		m, err := s.control.Read(buf)
		n += m
		if n > screenDiscardCap {
			s.end(&ScreenEnd{Reason: StopFailed, Cause: "the control socket sent more than expected"})
			return
		}
		if err != nil {
			return
		}
	}
}

// awaitFirst waits for the first config packet, which proves the stream is the protocol
// and gives the frame size, within screenFirstBound.
func (s *screenSource) awaitFirst() *startFailure {
	t := time.NewTimer(screenFirstBound)
	defer t.Stop()
	select {
	case <-s.first:
		if s.hub.Size() == (screen.Size{}) {
			return &startFailure{phase: "protocol", detail: "the first config packet carried no readable frame size"}
		}
		return nil
	case <-s.demuxDone:
		select {
		case <-s.childDone:
			return &startFailure{phase: "start", detail: s.childCause()}
		default:
		}
		if s.demuxErr == nil {
			return &startFailure{phase: "protocol", detail: "the video socket closed before the stream started"}
		}
		return &startFailure{phase: "protocol", detail: demuxCause(s.demuxErr)}
	case <-s.childDone:
		return &startFailure{phase: "start", detail: s.childCause()}
	case <-t.C:
		return &startFailure{phase: "protocol", detail: "no config packet arrived within " + screenFirstBound.String()}
	case <-s.ctx.Done():
		return s.stoppedFailure()
	}
}

// demuxCause names a demux failure without the bytes that caused it.
func demuxCause(err error) string {
	if errors.Is(err, screen.ErrBadStream) || errors.Is(err, screen.ErrPacketTooLarge) {
		return "the video stream was not in the expected format"
	}
	return "the video socket failed"
}

// monitor ends the source when its child exits or its video socket ends, until the source
// is ended by anything else.
func (s *screenSource) monitor() {
	defer s.wg.Done()
	select {
	case <-s.ctx.Done():
	case <-s.childDone:
		if s.ctx.Err() == nil { // not the kill of an end already decided
			s.endByExit(s.childCause())
		}
	case <-s.demuxDone:
		if s.ctx.Err() != nil { // the sockets were closed by an end already decided
			return
		}
		if s.demuxErr == nil {
			// The server closed the socket between two packets: its end, as the child's exit.
			s.endByExit("the video socket closed")
			return
		}
		s.endByDemuxError()
	}
}

// endByDemuxError ends the source after a video stream that failed or was cut. Decision
// (plan m2p §Changes, step 3; ADR-0078 §6 read with the device's death in mind): a device
// that dies cuts the video socket and its `adb shell` at about the same time, so which
// the source sees first is chance. The reason is therefore chosen as for the child's exit,
// by the bounded device.list check: the device absent or offline is deviceGone; online with
// the child already exited is exited; online with the child still running is failed, which
// is a protocol error of a live server. Without an answer from the check it is failed.
func (s *screenSource) endByDemuxError() {
	failed := &ScreenEnd{Reason: StopFailed, Cause: demuxCause(s.demuxErr)}
	switch online, known := s.deviceOnline(); {
	case !known:
	case !online:
		failed.Reason = StopDeviceGone
	default:
		select {
		case <-s.childDone:
			failed.Reason, failed.Cause = StopExited, s.childCause()
		default:
		}
	}
	s.end(failed)
}

// deviceOnline asks device.list, within streamListBound, whether the serial is listed and
// online (an emulator shutting down is listed offline). known is false when the listing
// failed.
func (s *screenSource) deviceOnline() (online, known bool) {
	lctx, cancel := context.WithTimeout(s.ctx, streamListBound)
	defer cancel()
	devices, err := s.adb.listAll(lctx, s.path)
	if err != nil {
		return false, false
	}
	for _, d := range devices {
		if d.Device == s.serial && d.State == StateDevice {
			return true, true
		}
	}
	return false, true
}

// endByExit ends the source with deviceGone when device.list no longer holds the serial
// online (an emulator shutting down is listed offline), else exited.
func (s *screenSource) endByExit(cause string) {
	reason := StopExited
	if online, known := s.deviceOnline(); known && !online {
		reason = StopDeviceGone
	}
	s.end(&ScreenEnd{Reason: reason, Cause: cause})
}

// teardown leaves nothing of the source on the host: the sockets closed, the child
// killed by its pid and reaped (its record removed), the goroutines joined, and the
// forward removed. It runs on run's goroutine, once.
//
// A source that ended with its last viewer first writes what its streams queued, the
// cancels of the fingers they held (ADR-0117 §5); one that failed has no socket to write. A
// cancel that was written still has to be read and injected by the server on the device,
// which reads the control socket in order and ends when the socket does (scrcpy's
// Controller), so the control socket is closed first and the child gets screenChildExitBound
// to exit on its own before the video socket is closed and the child killed. That is when
// something was written to the socket within that bound; a source that has not written lately
// (a view that held no finger) is not slowed.
func (s *screenSource) teardown() {
	s.cancel()
	wrote := false
	if s.control != nil && errors.Is(s.hub.Reason(), screen.ErrClosed) {
		s.line.flush(screenWriteBound)
		wrote = s.line.wroteWithin(screenChildExitBound)
	}
	s.line.shutdown()
	if wrote && s.cmd != nil {
		s.control.Close()
		t := time.NewTimer(screenChildExitBound)
		select {
		case <-s.childDone:
		case <-t.C:
		}
		t.Stop()
	}
	if s.video != nil {
		s.video.Close()
	}
	if s.control != nil {
		s.control.Close()
	}
	if s.killChild != nil {
		s.killChild()
	}
	if s.cmd != nil {
		if p := s.cmd.Process; p != nil {
			p.Kill() // the context's kill is the same; this one does not depend on it
		}
		<-s.childDone
	}
	s.wg.Wait()
	if !s.forwarding {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 2*screenCleanupBound)
	defer cancel()
	// adb leaves the socket file when it removes a forward, or when the command was cut
	// short: it is unlinked whatever became of the forward (ADR-0102).
	defer s.adb.unlinkForward(s.sock)
	// A forward command that did not report success may have been cut short with the forward
	// made. Its socket is this source's own, so it is removed by name without asking the
	// listing (which can fail too); adb's refusal for a forward that never existed is fine.
	out, err := s.adb.Run(ctx, s.path, screenCleanupBound, 1<<10, "-s", s.serial, "forward", "--remove", forwardLocalPrefix+s.sock)
	if d := adbFailure("adb forward --remove", out, err); d != "" && s.forwarded {
		s.adb.logf("screen: the forward %s of %s was not removed: %s", s.sock, s.serial, d)
	}
}

// Join implements screen.Source.
func (s *screenSource) Join() screen.Viewer { return s.hub.Join() }

// Size implements screen.Source.
func (s *screenSource) Size() screen.Size { return s.hub.Size() }

// Done implements screen.Source.
func (s *screenSource) Done() <-chan struct{} { return s.hub.Done() }

// Reason implements screen.Source: the *ScreenEnd that ended the stream, or
// screen.ErrClosed after the last release.
func (s *screenSource) Reason() error { return s.hub.Reason() }

// Inject implements screen.Source: EncodeControl's messages against the current frame
// size, written to the control socket on their schedule. A tap or a swipe holds the device's
// one finger as holder for its whole gesture: it waits for a holder it does not take the
// finger from, so two gestures never interleave their touches, and it ends, with a
// *screen.PartialError once it wrote, if a local view takes the finger from it (ADR-0117 §4).
// A key and a scroll have no finger. A gesture cut short by ctx still lifts the finger, with the last
// message of its encoding. A failure after the first byte was written is a
// *screen.PartialError (ADR-0079 §3); one before it is returned as it is.
func (s *screenSource) Inject(ctx context.Context, holder screen.Holder, in screen.Input) error {
	msgs, err := screen.EncodeControl(in, s.hub.Size())
	if err != nil {
		return err
	}
	fingerless := false
	switch in.(type) {
	case screen.Key, screen.Scroll:
		fingerless = true
	}
	var lost <-chan struct{}
	write := s.line.injectKeyWrite
	if fingerless {
		s.line.keyMu.Lock()
		defer s.line.keyMu.Unlock()
	} else {
		var size screen.Size
		if size, lost, err = s.line.acquire(ctx, holder); err != nil {
			return err
		}
		defer s.line.release(holder)
		// The frame may have changed while the gesture waited for the finger.
		if msgs, err = screen.EncodeControl(in, size); err != nil {
			return err
		}
		write = func(b []byte) (int, error) { return s.line.injectWrite(holder, b) }
	}
	// An ended source sends nothing, whichever of the waits above won.
	select {
	case <-s.hub.Done():
		return s.hub.Reason()
	default:
	}
	sent := false
	fail := func(err error) error {
		if sent {
			return &screen.PartialError{Err: err}
		}
		return err
	}
	began := time.Now()
	for i, m := range msgs {
		if wait := m.At - time.Since(began); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				if i > 0 {
					if _, err := write(msgs[len(msgs)-1].Data); err != nil && !errors.Is(err, errFingerLost) { // lift
						s.endOnWriteError()
					}
				}
				return fail(ctx.Err())
			case <-s.hub.Done():
				t.Stop()
				return fail(s.hub.Reason())
			case <-lost:
				t.Stop()
				return fail(errFingerLost)
			}
		}
		n, err := write(m.Data)
		if n > 0 {
			sent = true
		}
		if err != nil {
			if errors.Is(err, errFingerLost) {
				return fail(err)
			}
			select {
			case <-s.hub.Done():
				return fail(s.hub.Reason())
			default:
			}
			s.endOnWriteError()
			return fail(fmt.Errorf("screen: the control socket: %w", err))
		}
	}
	return nil
}

// Control implements screen.Source: a view's event, queued for the control line's writer.
func (s *screenSource) Control(holder screen.Holder, ev screen.Event) error {
	return s.line.Control(holder, ev)
}

// endOnWriteError ends the source after a control write that failed. A write that timed out
// midway leaves half a message on the socket, and every later input would be read out of
// frame, so the control socket is not used again: the viewers see failed and the next input
// finds no live view (ADR-0079 §3).
func (s *screenSource) endOnWriteError() {
	s.end(&ScreenEnd{Reason: StopFailed, Cause: "the control socket could not be written"})
}

func (s *screenSource) write(b []byte) (int, error) {
	s.control.SetWriteDeadline(time.Now().Add(screenWriteBound))
	n, err := s.control.Write(b)
	if err == nil {
		probeWrite(b)
	}
	return n, err
}

package screen

// TestSpikeScrcpy is the plan m2p step 1 spike tool. It is not a regression test: it
// skips unless DROIDYARD_SPIKE_SERIAL and ADB are set, and it shares no code with the
// package's production code (step 2 writes that from the note this tool feeds).
//
// What it does, in order, printing the elapsed time of each stage from its start:
//
//  1. writes the vendored scrcpy-server to a temp file and `adb push`es it to
//     /data/local/tmp/scrcpy-server.jar;
//  2. `adb forward tcp:<port> localabstract:scrcpy_<scid>`;
//  3. starts the server with `adb shell CLASSPATH=... app_process / com.genymobile.scrcpy.Server
//     <version> <options>` (the client process is kept, so it can be killed for -spike.lifetime);
//  4. connects the video socket (retrying until the server's dummy byte arrives), then the
//     control socket, as the server accepts them in that order;
//  5. for -spike.dur records every read of the video socket: the raw bytes to
//     <out>.bin and one line per read to <out>.times ("<ns since start> <bytes read>
//     <cumulative bytes>"), and parses the stream as it goes to print the first session
//     packet, the first config packet and the first keyframe, then counts at the end;
//  6. sends the -spike.ctl control messages at their offsets from the first session
//     packet, stamping each write, and prints the delay from each write to the next bytes
//     on the video socket; whatever the server sends on the control socket is counted and
//     printed;
//  7. closes the sockets and, with -spike.lifetime, kills the host `adb shell` client and
//     polls the device for the server process and the jar for 5 s.
//
// Invocation (from the repository root; the device is the owner's, the run is the main
// session's):
//
//	DROIDYARD_SPIKE_SERIAL=emulator-5554 ADB=$HOME/Library/Android/sdk/platform-tools/adb \
//	  go test ./internal/domain/device/screen -run TestSpikeScrcpy -count=1 -v -args \
//	  -spike.out=/tmp/spike/portrait -spike.dur=8s \
//	  -spike.ctl='1s:down:540:1200;1.1s:up:540:1200;2s:keydown:4;2.05s:keyup:4' \
//	  -spike.lifetime
//
// Flags (all after -args):
//
//	-spike.out       path prefix of <out>.bin and <out>.times (default: a temp dir)
//	-spike.dur       how long to record the video socket (default 6s)
//	-spike.port      local TCP port of the forward (default 27183)
//	-spike.scid      the server's 8-hex-digit session id (default 1a2b3c4d)
//	-spike.opts      the server options after the version, space separated; the default is
//	                 the plan's: tunnel_forward=true video=true audio=false control=true
//	                 video_codec=h264 max_size=1280 video_bit_rate=4000000 max_fps=60
//	                 video_codec_options=i-frame-interval=2 clipboard_autosync=false cleanup=true
//	                 (scid and log_level=info are always added in front)
//	-spike.ctl       control script, ';' separated "<offset>:<kind>:<args>"; offsets are
//	                 from the first session packet. Kinds: down|move|up:<x>:<y> (a touch of
//	                 the finger pointer, on the stream's own frame size), keydown|keyup:<keycode>
//	-spike.lifetime  after recording, kill the adb shell client and poll the device
//
// Sources followed, scrcpy v4.1 (read at that tag, not from memory):
//
//   - app/src/server.c: push to /data/local/tmp/scrcpy-server.jar; the command
//     `adb -s <serial> shell CLASSPATH=<jar> app_process / com.genymobile.scrcpy.Server
//     <version> scid=%08x log_level=... key=value...`; options are omitted at their default.
//   - server/.../device/DesktopConnection.java: with tunnel_forward the server listens on
//     the abstract socket "scrcpy_<scid as %08x>" and accepts video, then audio, then
//     control; the first accepted socket gets one dummy byte, and then the device name
//     (64 bytes, NUL padded) is written to the first socket.
//   - server/.../device/Streamer.java: on the video socket, 4 bytes of codec id, then a
//     12-byte "session" header per encoder (re)start (first bit set, flag bit 0 = client
//     resized, u32 width, u32 height), then packets with a 12-byte header: u64
//     (bit 62 config, bit 61 key frame, else the pts in microseconds) and u32 size.
//   - server/.../control/ControlMessageReader.java and app/src/control_msg.c (serialize):
//     the touch message is 32 bytes (type 2, action u8, pointer id i64, x i32, y i32,
//     screen w u16, screen h u16, pressure u16 fixed point, action button i32, buttons
//     i32), the keycode message 14 (type 0, action u8, keycode i32, repeat i32, meta i32),
//     all big endian. Binary.java: pressure 0xffff is 1.0.
//   - server/.../Options.java: the option keys; send_dummy_byte, send_device_meta,
//     send_frame_meta and send_stream_meta default to true.
//
// ASSUMED, not read at the tag: the finger's pointer id is -2 (scrcpy's
// SC_POINTER_ID_GENERIC_FINGER, named in Controller.java line 540; the value is from
// memory of app/src/input_manager.h). The note should confirm it by a tap that lands.

import (
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen/scrcpyserver"
)

var (
	spikeOut      = flag.String("spike.out", "", "path prefix of the .bin and .times files")
	spikeDur      = flag.Duration("spike.dur", 6*time.Second, "how long to record the video socket")
	spikePort     = flag.Int("spike.port", 27183, "local TCP port of the adb forward")
	spikeSCID     = flag.String("spike.scid", "1a2b3c4d", "the server's session id, 8 hex digits")
	spikeLifetime = flag.Bool("spike.lifetime", false, "kill the adb shell client afterwards and poll the device")
	spikeCtl      = flag.String("spike.ctl", "", "control script: <offset>:<kind>:<args>;...")
	spikeOpts     = flag.String("spike.opts",
		"tunnel_forward=true video=true audio=false control=true video_codec=h264 max_size=1280 "+
			"video_bit_rate=4000000 max_fps=60 video_codec_options=i-frame-interval=2 "+
			"clipboard_autosync=false cleanup=true",
		"server options after the version, space separated")
)

const spikeJar = "/data/local/tmp/scrcpy-server.jar"

type spikeRead struct {
	at time.Duration // since the start of the run
	n  int
}

type spikeWrite struct {
	desc          string
	before, after time.Duration
}

func TestSpikeScrcpy(t *testing.T) {
	serial := os.Getenv("DROIDYARD_SPIKE_SERIAL")
	adb := os.Getenv("ADB")
	if serial == "" || adb == "" {
		t.Skip("spike tool: set DROIDYARD_SPIKE_SERIAL and ADB to run it against a device")
	}
	steps, err := parseSpikeScript(*spikeCtl)
	if err != nil {
		t.Fatal(err)
	}
	out := *spikeOut
	if out == "" {
		out = filepath.Join(t.TempDir(), "spike")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	since := func() time.Duration { return time.Since(start) }
	say := func(format string, a ...any) {
		fmt.Printf("%9.3fs  %s\n", since().Seconds(), fmt.Sprintf(format, a...))
	}
	run := func(args ...string) string {
		b, err := exec.Command(adb, append([]string{"-s", serial}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("adb %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}

	// 1. push
	jar := filepath.Join(t.TempDir(), "scrcpy-server.jar")
	if err := os.WriteFile(jar, scrcpyserver.Server(), 0o644); err != nil {
		t.Fatal(err)
	}
	run("push", jar, spikeJar)
	say("pushed the server (%d bytes)", len(scrcpyserver.Server()))

	// 2. forward
	local := fmt.Sprintf("tcp:%d", *spikePort)
	run("forward", local, "localabstract:scrcpy_"+*spikeSCID)
	defer exec.Command(adb, "-s", serial, "forward", "--remove", local).Run()
	say("forwarded %s", local)

	// 3. start
	args := []string{"-s", serial, "shell", "CLASSPATH=" + spikeJar, "app_process", "/",
		"com.genymobile.scrcpy.Server", scrcpyserver.Version,
		"scid=" + *spikeSCID, "log_level=info"}
	args = append(args, strings.Fields(*spikeOpts)...)
	shell := exec.Command(adb, args...)
	shellOut := &spikeBuf{}
	shell.Stdout, shell.Stderr = shellOut, shellOut
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	shellDone := make(chan struct{})
	go func() { shell.Wait(); close(shellDone) }()
	say("started the server: adb %s", strings.Join(args, " "))

	// 4. connect: video first (wait for the dummy byte), then control.
	dial := func() net.Conn {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", *spikePort))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	var video net.Conn
	first := make([]byte, 1)
	for deadline := time.Now().Add(15 * time.Second); ; {
		if time.Now().After(deadline) {
			t.Fatalf("no dummy byte in 15 s; server output:\n%s", shellOut.String())
		}
		c := dial()
		c.SetReadDeadline(time.Now().Add(time.Second))
		if n, err := c.Read(first); err == nil && n == 1 {
			c.SetReadDeadline(time.Time{})
			video = c
			break
		}
		c.Close()
		time.Sleep(100 * time.Millisecond)
	}
	say("video socket connected, dummy byte %#x", first[0])
	control := dial()
	defer control.Close()
	defer video.Close()
	say("control socket connected")

	// 5. record + parse
	bin, err := os.Create(out + ".bin")
	if err != nil {
		t.Fatal(err)
	}
	defer bin.Close()
	times, err := os.Create(out + ".times")
	if err != nil {
		t.Fatal(err)
	}
	defer times.Close()

	var mu sync.Mutex
	var reads []spikeRead
	var recorded int
	var frameW, frameH uint32
	sessionSeen := make(chan struct{})
	record := func(b []byte) {
		at := since()
		mu.Lock()
		reads = append(reads, spikeRead{at, len(b)})
		recorded += len(b)
		total := recorded
		mu.Unlock()
		bin.Write(b)
		fmt.Fprintf(times, "%d %d %d\n", at.Nanoseconds(), len(b), total)
	}
	record(first)

	st := &spikeParser{}
	parserDone := make(chan struct{})
	go func() {
		defer close(parserDone)
		buf := make([]byte, 64*1024)
		sawSession := false
		for {
			n, err := video.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				record(chunk)
				for _, ev := range st.feed(chunk) {
					say("%s", ev)
				}
				if !sawSession && st.width != 0 {
					sawSession = true
					mu.Lock()
					frameW, frameH = st.width, st.height
					mu.Unlock()
					close(sessionSeen)
				}
			}
			if err != nil {
				say("video socket read ended: %v", err)
				return
			}
		}
	}()

	// control socket: count what the server sends.
	var ctlBytes int
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := control.Read(buf)
			if n > 0 {
				mu.Lock()
				ctlBytes += n
				mu.Unlock()
				say("control socket: %d bytes from the server: % x", n, buf[:min(n, 32)])
			}
			if err != nil {
				return
			}
		}
	}()

	// 6. control script
	var writes []spikeWrite
	scriptDone := make(chan struct{})
	go func() {
		defer close(scriptDone)
		select {
		case <-sessionSeen:
		case <-time.After(*spikeDur):
			return
		}
		mu.Lock()
		w, h := uint16(frameW), uint16(frameH)
		mu.Unlock()
		t0 := time.Now()
		for _, s := range steps {
			if d := time.Until(t0.Add(s.at)); d > 0 {
				time.Sleep(d)
			}
			msg := s.encode(w, h)
			before := since()
			_, err := control.Write(msg)
			after := since()
			writes = append(writes, spikeWrite{fmt.Sprintf("%s (% x)", s.desc, msg), before, after})
			say("control write %s err=%v", s.desc, err)
		}
	}()

	time.Sleep(*spikeDur)
	<-scriptDone
	video.Close()
	<-parserDone

	mu.Lock()
	say("recorded %d reads, %d bytes, to %s.bin / %s.times", len(reads), recorded, out, out)
	say("stream: %d session packets, %d config, %d frames, %d key frames, %d leftover bytes",
		st.sessions, st.configs, st.frames, st.keys, len(st.buf))
	say("control socket received %d bytes from the server", ctlBytes)
	for _, w := range writes {
		var next time.Duration = -1
		for _, r := range reads {
			if r.at > w.after {
				next = r.at - w.after
				break
			}
		}
		say("write %s: wrote in %v, next video bytes %v after the write", w.desc, w.after-w.before, next)
	}
	mu.Unlock()
	fmt.Printf("server client output:\n%s\n", shellOut.String())

	// 7. lifetime
	if *spikeLifetime {
		control.Close()
		shell.Process.Kill()
		<-shellDone
		killed := since()
		say("killed the adb shell client; polling the device")
		for {
			ps := run("shell", "ps -A -o PID,ARGS | grep '[c]om.genymobile'; ls "+spikeJar+" 2>&1")
			gone := !strings.Contains(ps, "com.genymobile")
			say("server running: %v; jar: %s", !gone, lastLine(ps))
			if since()-killed > 5*time.Second || (gone && strings.Contains(ps, "No such file")) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	} else {
		shell.Process.Kill()
		<-shellDone
	}
}

func lastLine(s string) string {
	l := strings.Split(s, "\n")
	return l[len(l)-1]
}

// spikeBuf is a goroutine-safe byte buffer for the adb shell client's output.
type spikeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *spikeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.b = append(s.b, p...)
	s.mu.Unlock()
	return len(p), nil
}

func (s *spikeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

// spikeParser follows the video socket as v4.1's Streamer writes it: after the dummy byte,
// 64 bytes of device name, 4 of codec id, then 12-byte headers, each a session packet
// (first bit set) or a frame/config packet followed by its payload.
type spikeParser struct {
	buf                             []byte
	gotPreamble                     bool
	need                            int // payload bytes still to skip
	width, height                   uint32
	sessions, configs, frames, keys int
}

func (p *spikeParser) feed(chunk []byte) []string {
	var ev []string
	p.buf = append(p.buf, chunk...)
	if !p.gotPreamble {
		// The caller recorded the dummy byte separately, so it is not in the chunks.
		if len(p.buf) < 64+4 {
			return ev
		}
		name := strings.TrimRight(string(p.buf[:64]), "\x00")
		codec := p.buf[64:68]
		ev = append(ev, fmt.Sprintf("device name %q, codec id % x (%q)", name, codec, codec))
		p.buf = p.buf[68:]
		p.gotPreamble = true
	}
	for {
		if p.need > 0 {
			k := min(p.need, len(p.buf))
			p.buf = p.buf[k:]
			p.need -= k
			if p.need > 0 {
				return ev
			}
		}
		if len(p.buf) < 12 {
			return ev
		}
		h := p.buf[:12]
		if h[0]&0x80 != 0 {
			p.sessions++
			p.width = binary.BigEndian.Uint32(h[4:8])
			p.height = binary.BigEndian.Uint32(h[8:12])
			ev = append(ev, fmt.Sprintf("session packet: %dx%d, client resized flag %d (header % x)",
				p.width, p.height, h[3]&1, h))
			p.buf = p.buf[12:]
			continue
		}
		pts := binary.BigEndian.Uint64(h[:8])
		size := int(binary.BigEndian.Uint32(h[8:12]))
		switch {
		case pts&(1<<62) != 0:
			p.configs++
			ev = append(ev, fmt.Sprintf("config packet, %d bytes (header % x)", size, h))
		case pts&(1<<61) != 0:
			p.frames++
			p.keys++
			ev = append(ev, fmt.Sprintf("key frame, %d bytes, pts %d us", size, pts&^(1<<61)))
		default:
			p.frames++
		}
		p.buf = p.buf[12:]
		p.need = size
	}
}

type spikeStep struct {
	at   time.Duration
	desc string
	enc  func(w, h uint16) []byte
}

func (s spikeStep) encode(w, h uint16) []byte { return s.enc(w, h) }

func parseSpikeScript(script string) ([]spikeStep, error) {
	var steps []spikeStep
	if strings.TrimSpace(script) == "" {
		return nil, nil
	}
	for _, item := range strings.Split(script, ";") {
		f := strings.Split(strings.TrimSpace(item), ":")
		if len(f) < 3 {
			return nil, fmt.Errorf("control step %q: want <offset>:<kind>:<args>", item)
		}
		at, err := time.ParseDuration(f[0])
		if err != nil {
			return nil, fmt.Errorf("control step %q: %v", item, err)
		}
		num := func(i int) (int32, error) {
			v, err := strconv.ParseInt(f[i], 10, 32)
			return int32(v), err
		}
		switch f[1] {
		case "down", "move", "up":
			if len(f) != 4 {
				return nil, fmt.Errorf("control step %q: want %s:<x>:<y>", item, f[1])
			}
			x, err1 := num(2)
			y, err2 := num(3)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("control step %q: bad coordinates", item)
			}
			action := map[string]byte{"down": 0, "up": 1, "move": 2}[f[1]] // MotionEvent.ACTION_*
			pressure := uint16(0xffff)
			if f[1] == "up" {
				pressure = 0
			}
			steps = append(steps, spikeStep{at, fmt.Sprintf("%s at %s", f[1], strings.Join(f[2:], ",")),
				func(w, h uint16) []byte { return spikeTouch(action, x, y, w, h, pressure) }})
		case "keydown", "keyup":
			k, err := num(2)
			if err != nil {
				return nil, fmt.Errorf("control step %q: bad keycode", item)
			}
			action := byte(0) // KeyEvent.ACTION_DOWN
			if f[1] == "keyup" {
				action = 1
			}
			steps = append(steps, spikeStep{at, fmt.Sprintf("%s %d", f[1], k),
				func(uint16, uint16) []byte { return spikeKey(action, k) }})
		default:
			return nil, fmt.Errorf("control step %q: unknown kind %q", item, f[1])
		}
	}
	return steps, nil
}

// spikeTouch is TYPE_INJECT_TOUCH_EVENT (2), 32 bytes, big endian (control_msg.c
// serialize; ControlMessageReader.parseInjectTouchEvent).
func spikeTouch(action byte, x, y int32, w, h, pressure uint16) []byte {
	b := make([]byte, 32)
	b[0] = 2
	b[1] = action
	binary.BigEndian.PutUint64(b[2:], ^uint64(1)) // pointer id -2: ASSUMED generic finger
	binary.BigEndian.PutUint32(b[10:], uint32(x))
	binary.BigEndian.PutUint32(b[14:], uint32(y))
	binary.BigEndian.PutUint16(b[18:], w)
	binary.BigEndian.PutUint16(b[20:], h)
	binary.BigEndian.PutUint16(b[22:], pressure)
	// action button and buttons stay 0
	return b
}

// spikeKey is TYPE_INJECT_KEYCODE (0), 14 bytes: action, keycode, repeat 0, meta 0.
func spikeKey(action byte, keycode int32) []byte {
	b := make([]byte, 14)
	b[1] = action
	binary.BigEndian.PutUint32(b[2:], uint32(keycode))
	return b
}

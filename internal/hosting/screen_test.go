package hosting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// frame lays out one message of the video socket (ADR-0078 §4) with its own arithmetic.
func frame(flags byte, pts uint64, data []byte) []byte {
	b := []byte{flags}
	b = binary.BigEndian.AppendUint64(b, pts)
	return append(b, data...)
}

var (
	testSPS = []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x29}
	testPPS = []byte{0, 0, 0, 1, 0x68, 0xce, 0x3c}
	testIDR = []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}
)

// Decision (ADR-0100 §5): a message is a packet of ADR-0078 §4, and the join form is split
// back into the config and the key frame, the bytes in order. Seen failing without the
// split: one packet of 22 bytes flagged key and config.
func TestParseScreenFrame(t *testing.T) {
	cfg := append(append([]byte(nil), testSPS...), testPPS...)

	pkts, err := ParseScreenFrame(frame(3, 1234, append(append([]byte(nil), cfg...), testIDR...)))
	if err != nil || len(pkts) != 2 {
		t.Fatalf("join form: %+v, %v; want the config and the key frame", pkts, err)
	}
	if !pkts[0].Config || pkts[0].Key || !bytes.Equal(pkts[0].Data, cfg) || !pkts[1].Key || pkts[1].Config || !bytes.Equal(pkts[1].Data, testIDR) || pkts[1].PTS != 1234 {
		t.Errorf("join form pieces: %+v", pkts)
	}

	for name, c := range map[string]struct {
		msg  []byte
		key  bool
		data []byte
	}{
		"key frame":   {frame(1, 7, testIDR), true, testIDR},
		"delta frame": {frame(0, 8, []byte{0, 0, 0, 1, 0x41, 1}), false, []byte{0, 0, 0, 1, 0x41, 1}},
		"empty delta": {frame(0, 9, nil), false, nil},
	} {
		got, err := ParseScreenFrame(c.msg)
		if err != nil || len(got) != 1 || got[0].Key != c.key || got[0].Config || !bytes.Equal(got[0].Data, c.data) {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// Decision: the bytes are another machine's, so each shape that is not a packet is an
// error and none is a panic or an allocation: a short header, flags the host never sends
// (a config alone, unknown bits), a presentation time that does not fit an int64, and a
// packet over the cap once split. Seen failing with the cap check removed: the oversized
// config of the last row was accepted.
func TestParseScreenFrameRefuses(t *testing.T) {
	big := bytes.Repeat([]byte{1}, screen.MaxPacket+1)
	for name, msg := range map[string][]byte{
		"empty":                 nil,
		"a short header":        {1, 0, 0, 0, 0, 0, 0, 0},
		"a config alone":        frame(2, 0, testSPS),
		"unknown flag bits":     frame(4, 0, testIDR),
		"flags 0x81":            frame(0x81, 0, testIDR),
		"a time over 63 bits":   frame(1, 1<<63, testIDR),
		"a delta over the cap":  frame(0, 0, big),
		"a key over the cap":    frame(1, 0, big),
		"a config over the cap": frame(3, 0, append(append(append([]byte(nil), testSPS...), bytes.Repeat([]byte{1}, screen.MaxPacket)...), testIDR...)),
	} {
		if got, err := ParseScreenFrame(msg); !errors.Is(err, ErrScreenFrame) || got != nil {
			t.Errorf("%s: %d packets, %v; want ErrScreenFrame", name, len(got), err)
		}
	}
	// The largest the cap allows is accepted: a config and a key frame of MaxPacket each.
	cfg := append(append([]byte(nil), testSPS...), bytes.Repeat([]byte{1}, screen.MaxPacket-len(testSPS))...)
	key := append(append([]byte(nil), testIDR...), bytes.Repeat([]byte{2}, screen.MaxPacket-len(testIDR))...)
	if got, err := ParseScreenFrame(frame(3, 0, append(append([]byte(nil), cfg...), key...))); err != nil || len(got) != 2 {
		t.Errorf("a join form of two packets at the cap: %d pieces, %v", len(got), err)
	}
	if MaxScreenFrame != 9+2*screen.MaxPacket {
		t.Errorf("MaxScreenFrame = %d", MaxScreenFrame)
	}
}

// FuzzPeerScreenFrame is the client's frame reader on any bytes: no panic, no packet over
// the cap, flags that are never both config and key, a time that is never negative, and the
// pieces of a message holding exactly its bytes after the header.
func FuzzPeerScreenFrame(f *testing.F) {
	cfg := append(append([]byte(nil), testSPS...), testPPS...)
	f.Add(frame(3, 1, append(append([]byte(nil), cfg...), testIDR...)))
	f.Add(frame(1, 2, testIDR))
	f.Add(frame(0, 3, []byte{0, 0, 1, 0x41}))
	f.Add(frame(2, 4, cfg))
	f.Add(frame(0xff, 1<<63, nil))
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, msg []byte) {
		pkts, err := ParseScreenFrame(msg)
		if err != nil {
			if pkts != nil || !errors.Is(err, ErrScreenFrame) {
				t.Fatalf("an error with packets %v or not ErrScreenFrame: %v", pkts, err)
			}
			return
		}
		if len(pkts) < 1 || len(pkts) > 2 {
			t.Fatalf("%d packets", len(pkts))
		}
		var all []byte
		for _, p := range pkts {
			if len(p.Data) > screen.MaxPacket {
				t.Fatalf("a packet of %d bytes passed the cap", len(p.Data))
			}
			if p.Config && p.Key {
				t.Fatal("a packet both config and key")
			}
			if p.PTS < 0 {
				t.Fatal("negative PTS")
			}
			all = append(all, p.Data...)
		}
		if !bytes.Equal(all, msg[frameHeader:]) {
			t.Fatalf("the packets hold %d bytes, the message %d after its header", len(all), len(msg)-frameHeader)
		}
		if len(msg) > MaxScreenFrame {
			t.Fatalf("a message of %d bytes passed", len(msg))
		}
	})
}

// screenHost is a host whose video socket plays script: it reads the stream id and sends
// each message, then holds the socket open until the test ends. It returns the id it was
// sent through got.
func screenHost(t *testing.T, script func(ctx context.Context, c *websocket.Conn)) (addr, fingerprint string, got chan string) {
	t.Helper()
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerScreenPath, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		typ, data, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageText {
			return
		}
		got <- string(data)
		script(ctx, c)
		<-ctx.Done()
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate()}, ClientAuth: tls.RequireAnyClientCert, NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), id.Fingerprint(), got
}

// Decision (ADR-0100 §3): the client attaches with {"stream": <id>} as the first frame and
// reads binary messages; a text message and a message past the cap are the host breaking
// the protocol (ErrScreenFrame), and the second is refused by the socket's read limit
// before its bytes are held. A host that closes the socket is its own error.
func TestDialScreenReadsAndRefuses(t *testing.T) {
	bigText := bytes.Repeat([]byte{'a'}, 16)
	oversized := bytes.Repeat([]byte{1}, MaxScreenFrame+1)
	for name, c := range map[string]struct {
		send  func(ctx context.Context, c *websocket.Conn)
		check func(t *testing.T, pkts []screen.Packet, err error)
	}{
		"a packet": {
			func(ctx context.Context, c *websocket.Conn) {
				c.Write(ctx, websocket.MessageBinary, frame(1, 5, testIDR))
			},
			func(t *testing.T, pkts []screen.Packet, err error) {
				if err != nil || len(pkts) != 1 || !pkts[0].Key || pkts[0].PTS != 5 {
					t.Errorf("%+v, %v", pkts, err)
				}
			},
		},
		// A key frame of megabytes is legal; the library's default read limit (32 KiB) would
		// refuse it, so this pins that the socket's limit is MaxScreenFrame. The next case
		// pins the other side of it.
		"a large legal packet": {
			func(ctx context.Context, c *websocket.Conn) {
				c.Write(ctx, websocket.MessageBinary, frame(0, 6, bytes.Repeat([]byte{1}, 3<<20)))
			},
			func(t *testing.T, pkts []screen.Packet, err error) {
				if err != nil || len(pkts) != 1 || len(pkts[0].Data) != 3<<20 {
					t.Errorf("%d packets, %v; want the 3 MiB packet", len(pkts), err)
				}
			},
		},
		"a text message": {
			func(ctx context.Context, c *websocket.Conn) { c.Write(ctx, websocket.MessageText, bigText) },
			func(t *testing.T, pkts []screen.Packet, err error) {
				if !errors.Is(err, ErrScreenFrame) {
					t.Errorf("%+v, %v; want ErrScreenFrame", pkts, err)
				}
			},
		},
		"a message over the cap": {
			func(ctx context.Context, c *websocket.Conn) { c.Write(ctx, websocket.MessageBinary, oversized) },
			func(t *testing.T, pkts []screen.Packet, err error) {
				if !errors.Is(err, ErrScreenFrame) {
					t.Errorf("%+v, %v; want ErrScreenFrame", pkts, err)
				}
			},
		},
		"a bad packet": {
			func(ctx context.Context, c *websocket.Conn) {
				c.Write(ctx, websocket.MessageBinary, frame(2, 0, testSPS))
			},
			func(t *testing.T, pkts []screen.Packet, err error) {
				if !errors.Is(err, ErrScreenFrame) {
					t.Errorf("%+v, %v; want ErrScreenFrame", pkts, err)
				}
			},
		},
		"a close": {
			func(ctx context.Context, c *websocket.Conn) { c.Close(4410, "the stream ended") },
			func(t *testing.T, pkts []screen.Packet, err error) {
				if websocket.CloseStatus(err) != 4410 {
					t.Errorf("%+v, %v; want the host's close 4410", pkts, err)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			addr, fp, got := screenHost(t, c.send)
			id, _, err := LoadIdentity(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			l := newLink(policy.Remote{Name: "x", Address: addr, Fingerprint: fp}, id, "0.0.0", &lockedLog{})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			sc, err := l.DialScreen(ctx, "the-stream-id")
			if err != nil {
				t.Fatal(err)
			}
			defer sc.Close()
			if first := <-got; first != `{"stream":"the-stream-id"}` {
				t.Errorf("the host was sent %s as the first frame", first)
			}
			pkts, err := sc.Next(ctx)
			c.check(t, pkts, err)
		})
	}
}

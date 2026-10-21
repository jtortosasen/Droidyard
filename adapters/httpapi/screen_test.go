package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Decision (ADR-0078 §4): a message of /v1/screen is [flags u8: 1 key, 2 config][pts u64
// big-endian][Annex B]. testdata/frame-golden.bin of the screen package is one such message;
// step 5's Vitest reads the same file, so the two encoders of the header meet one set of
// bytes. The Annex B part is the packet's, unchanged. Seen failing with the two flag bits
// swapped: the golden's flags byte 3 survives that, so the single-flag cases below are what
// caught it.
func TestScreenMessageGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "domain", "device", "screen", "testdata", "frame-golden.bin"))
	if err != nil {
		t.Fatal(err)
	}
	p := ScreenPacket{Key: true, Config: true, PTS: 1234567, Data: golden[9:]}
	if got := screenMessage(nil, p); !bytes.Equal(got, golden) {
		t.Errorf("the message is % x\nwant the golden % x", got, golden)
	}

	for _, tc := range []struct {
		name        string
		key, config bool
		flags       byte
	}{
		{"a delta frame", false, false, 0},
		{"a key frame", true, false, 1},
		{"a config", false, true, 2},
	} {
		m := screenMessage(nil, ScreenPacket{Key: tc.key, Config: tc.config, PTS: 1 << 40, Data: []byte{9, 8, 7}})
		want := []byte{tc.flags, 0, 0, 1, 0, 0, 0, 0, 0, 9, 8, 7}
		if !bytes.Equal(m, want) {
			t.Errorf("%s: % x, want % x", tc.name, m, want)
		}
	}

	// buf is reused: a second message does not carry the first's tail.
	buf := screenMessage(nil, ScreenPacket{Data: bytes.Repeat([]byte{1}, 100)})
	if m := screenMessage(buf, ScreenPacket{Data: []byte{2}}); len(m) != 10 {
		t.Errorf("a reused buffer gave %d bytes, want 10", len(m))
	}
}

// fakeViewer returns the same packet at once, forever, and records its Close.
type fakeViewer struct {
	data   []byte
	closed chan struct{}
	once   sync.Once

	inputs   chan []byte // each frame Input was given, when set
	inputErr error       // what Input answers
}

func (f *fakeViewer) Next(ctx context.Context) (ScreenPacket, error) {
	select {
	case <-ctx.Done():
		return ScreenPacket{}, ctx.Err()
	default:
		return ScreenPacket{Key: true, Data: f.data}, nil
	}
}

func (f *fakeViewer) Close() { f.once.Do(func() { close(f.closed) }) }

func (f *fakeViewer) Input(frame []byte) error {
	if f.inputs != nil {
		f.inputs <- append([]byte(nil), frame...)
	}
	return f.inputErr
}

// Decision (ADR-0078 §4): every message is written under a deadline, so a socket whose
// client stopped reading ends its own stream (its viewer is closed) instead of holding its
// writer for ever. The client here authenticates and never reads; the viewer offers 4 MiB
// packets without pause, so the socket's buffers fill and a write blocks. Seen failing
// with the deadline removed (the write on the connection's own context): the viewer was
// not closed within 10 s.
func TestScreenWriteDeadline(t *testing.T) {
	v := &fakeViewer{data: make([]byte, 4<<20), closed: make(chan struct{})}
	srv := New(nil, nil, nil, "tok", nil)
	srv.UseScreens(func(id string) (ScreenViewer, error) { return v, nil })
	srv.SetScreenWriteTimeout(200 * time.Millisecond)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v1/screen",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ts.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"auth":"tok","stream":"s"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("a client that stopped reading kept its stream open past the write deadline")
	}
}

// screenSocket dials /v1/screen on ts and sends the auth frame.
func screenSocket(t *testing.T, ts *httptest.Server) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v1/screen",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ts.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"auth":"tok","stream":"s"}`)); err != nil {
		t.Fatal(err)
	}
	return c, ctx
}

// closeCode reads the socket until it closes and returns the close code (-1 when it ended
// without one). The viewer's endless packets are read and dropped.
func closeCode(ctx context.Context, c *websocket.Conn) websocket.StatusCode {
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// Decision (ADR-0117 §3): after the auth frame the socket's binary frames are the view's
// input and the adapter hands each, whole and in order, to the viewer (which owns the wire);
// it does not look inside them. A text frame closes the socket 1003; so does a frame the
// viewer calls malformed (ErrInputMalformed); any other refusal (a full queue, too many
// frames, a stream that ended or takes no input) closes it 1008. Each case's code is the
// one a client tells apart. Seen failing with every refusal closing 1003: the refusal
// row got 1003.
func TestScreenSocketInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   websocket.MessageType
		frame string
		err   error
		code  websocket.StatusCode
	}{
		{"a text frame", websocket.MessageText, `{"type":"tap"}`, nil, websocket.StatusUnsupportedData},
		{"a malformed frame", websocket.MessageBinary, "\x09", ErrInputMalformed, websocket.StatusUnsupportedData},
		{"a refusal", websocket.MessageBinary, "\x01\x00\x00\x00\x00\x00", errors.New("queue full"), websocket.StatusPolicyViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &fakeViewer{data: []byte{1}, closed: make(chan struct{}), inputs: make(chan []byte, 8), inputErr: tc.err}
			srv := New(nil, nil, nil, "tok", nil)
			srv.UseScreens(func(id string) (ScreenViewer, error) { return v, nil })
			ts := httptest.NewServer(srv)
			defer ts.Close()
			c, ctx := screenSocket(t, ts)
			if err := c.Write(ctx, tc.typ, []byte(tc.frame)); err != nil {
				t.Fatal(err)
			}
			if got := closeCode(ctx, c); got != tc.code {
				t.Errorf("closed with %d, want %d", got, tc.code)
			}
			select {
			case <-v.closed:
			case <-time.After(10 * time.Second):
				t.Error("the viewer was not closed with the socket")
			}
			if tc.typ == websocket.MessageText && len(v.inputs) != 0 {
				t.Error("a text frame reached the viewer's Input")
			}
		})
	}

	t.Run("binary frames reach Input in order", func(t *testing.T) {
		v := &fakeViewer{data: []byte{1}, closed: make(chan struct{}), inputs: make(chan []byte, 8)}
		srv := New(nil, nil, nil, "tok", nil)
		srv.UseScreens(func(id string) (ScreenViewer, error) { return v, nil })
		ts := httptest.NewServer(srv)
		defer ts.Close()
		c, ctx := screenSocket(t, ts)
		go closeCode(ctx, c) // reads the packets, or the socket's buffers fill
		frames := []string{"\x01\x00\x00\x01\x00\x02", "\x02\x01", "\x03\x00\x00\x00\x00\x00\x01\x00\x00"}
		for _, f := range frames {
			if err := c.Write(ctx, websocket.MessageBinary, []byte(f)); err != nil {
				t.Fatal(err)
			}
		}
		for i, f := range frames {
			select {
			case got := <-v.inputs:
				if string(got) != f {
					t.Errorf("frame %d reached Input as % x, want % x", i, got, f)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("frame %d never reached Input", i)
			}
		}
	})
}

// Decision (ADR-0078 §4, ADR-0117 §3): a binary frame before the auth frame is the auth
// frame and is refused 4401 with the viewer never attached, so input cannot be sent by a
// client that has not authenticated.
func TestScreenInputBeforeAuthIsRefused(t *testing.T) {
	v := &fakeViewer{data: []byte{1}, closed: make(chan struct{}), inputs: make(chan []byte, 8)}
	srv := New(nil, nil, nil, "tok", nil)
	srv.UseScreens(func(id string) (ScreenViewer, error) { return v, nil })
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v1/screen",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ts.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("\x01\x00\x00\x00\x00\x00")); err != nil {
		t.Fatal(err)
	}
	if got := closeCode(ctx, c); got != 4401 {
		t.Errorf("a binary first frame closed with %d, want 4401", got)
	}
	if len(v.inputs) != 0 {
		t.Error("a frame sent before the auth frame reached Input")
	}
}

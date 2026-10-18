package httpapi

import (
	"bytes"
	"context"
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

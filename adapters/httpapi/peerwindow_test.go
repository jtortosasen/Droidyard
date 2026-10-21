package httpapi

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// countingViewer offers the same packet at once, forever, and counts how many it handed out.
type countingViewer struct {
	data   []byte
	nexts  atomic.Int64
	closed chan struct{}
	once   sync.Once
	inputs chan []byte
}

func (v *countingViewer) Next(ctx context.Context) (ScreenPacket, error) {
	if ctx.Err() != nil {
		return ScreenPacket{}, ctx.Err()
	}
	v.nexts.Add(1)
	return ScreenPacket{Key: true, Data: v.data}, nil
}
func (v *countingViewer) Close() { v.once.Do(func() { close(v.closed) }) }
func (v *countingViewer) Input(f []byte) error {
	if v.inputs != nil {
		v.inputs <- append([]byte(nil), f...)
	}
	return nil
}

// peerSocket serves ServePeerScreen on a test server and returns a client that has sent its
// stream frame and reads (and counts the bytes of) every message in the background.
func peerSocket(t *testing.T, v ScreenViewer) (c *websocket.Conn, ctx context.Context, got *atomic.Int64) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ServePeerScreen(r.Context(), ws, "anna", time.Second, func(peer, id string) (ScreenViewer, error) { return v, nil })
	}))
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"stream":"s"}`)); err != nil {
		t.Fatal(err)
	}
	got = new(atomic.Int64)
	go func() {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			got.Add(int64(len(b)))
		}
	}()
	return c, ctx, got
}

func ackFrame(n uint32) []byte { return binary.BigEndian.AppendUint32([]byte{4}, n) }

// Decision (ADR-0118 §3): once a peer's client has acknowledged, the host keeps at most
// peerWindow bytes sent and not acknowledged: it stops taking packets from the viewer (so the
// backlog forms in the Hub, not the socket) until an acknowledgement frees the window. The
// client reads everything at once, so only the window can stop the host. Seen failing with the
// window's wait removed: the viewer was asked for thousands of packets.
func TestPeerWindowHoldsTheSendsUntilAcknowledged(t *testing.T) {
	v := &countingViewer{data: make([]byte, 16<<10), closed: make(chan struct{})}
	c, ctx, got := peerSocket(t, v)
	if err := c.Write(ctx, websocket.MessageBinary, ackFrame(0)); err != nil {
		t.Fatal(err)
	}
	// The host sent freely until the first acknowledgement arrived; now it is far past the
	// window, so it must stop taking packets.
	time.Sleep(300 * time.Millisecond)
	n1 := v.nexts.Load()
	time.Sleep(300 * time.Millisecond)
	if n2 := v.nexts.Load(); n2 != n1 {
		t.Fatalf("the host kept taking packets (%d, then %d) with more than %d bytes unacknowledged", n1, n2, peerWindow)
	}
	// Acknowledging everything received frees exactly one window.
	if err := c.Write(ctx, websocket.MessageBinary, ackFrame(uint32(got.Load()))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	msg := int64(1 + 8 + 16<<10)
	grew := v.nexts.Load() - n1
	if max := int64(peerWindow)/msg + 1; grew < 2 || grew > max {
		t.Fatalf("an acknowledgement of everything freed %d packets; a window of %d bytes holds %d at most", grew, peerWindow, max)
	}
}

// Decision (ADR-0118 §3): a client that never acknowledges (an older one) is sent as before:
// the window applies only after the first acknowledgement. Seen failing with the window on
// from the start: the host stopped after the window with no ack ever sent.
func TestPeerWithoutAcksIsSentAsToday(t *testing.T) {
	v := &countingViewer{data: make([]byte, 16<<10), closed: make(chan struct{})}
	peerSocket(t, v)
	time.Sleep(500 * time.Millisecond)
	if n := v.nexts.Load(); n < 100 {
		t.Fatalf("a client that sends no acknowledgement was sent %d packets in 0.5 s; the window must not apply to it", n)
	}
}

// Decision (ADR-0118 §3): a client sending more than 100 acknowledgements a second is closed
// 1008, and an acknowledgement of more than was sent is refused the same way. Seen failing
// with no rate bound (the flood was served) and with the ahead check removed.
func TestPeerAckFloodAndBadAckCloseTheSocket(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(ctx context.Context, c *websocket.Conn)
	}{
		{"a flood", func(ctx context.Context, c *websocket.Conn) {
			for i := 0; i < 400; i++ {
				if c.Write(ctx, websocket.MessageBinary, ackFrame(0)) != nil {
					return
				}
			}
		}},
		{"an ack ahead of what was sent", func(ctx context.Context, c *websocket.Conn) {
			c.Write(ctx, websocket.MessageBinary, ackFrame(1<<30))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &countingViewer{data: make([]byte, 64), closed: make(chan struct{})}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				ServePeerScreen(r.Context(), ws, "anna", time.Second, func(peer, id string) (ScreenViewer, error) { return v, nil })
			}))
			defer ts.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			c.Write(ctx, websocket.MessageText, []byte(`{"stream":"s"}`))
			go tc.send(ctx, c)
			if got := closeCode(ctx, c); got != websocket.StatusPolicyViolation {
				t.Errorf("closed with %d, want %d", got, websocket.StatusPolicyViolation)
			}
		})
	}
}

// Decision (ADR-0118 §3): the local /v1/screen has no window: a type-4 frame there is not an
// acknowledgement the adapter swallows but input the viewer judges (the domain refuses type 4
// as malformed, so the socket closes 1003). Seen failing with the acknowledgement taken on
// the local socket too: the frame never reached the viewer.
func TestLocalSocketHandsTypeFourToTheViewer(t *testing.T) {
	v := &fakeViewer{data: []byte{1}, closed: make(chan struct{}), inputs: make(chan []byte, 8), inputErr: ErrInputMalformed}
	srv := New(nil, nil, nil, "tok", nil)
	srv.UseScreens(func(id string) (ScreenViewer, error) { return v, nil })
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c, ctx := screenSocket(t, ts)
	if err := c.Write(ctx, websocket.MessageBinary, ackFrame(0)); err != nil {
		t.Fatal(err)
	}
	if got := closeCode(ctx, c); got != websocket.StatusUnsupportedData {
		t.Errorf("closed with %d, want 1003", got)
	}
	select {
	case f := <-v.inputs:
		if len(f) != 5 || f[0] != 4 {
			t.Errorf("the viewer got % x", f)
		}
	default:
		t.Error("the type-4 frame never reached the viewer's Input")
	}
}

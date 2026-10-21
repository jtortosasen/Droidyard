package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// peerSocketAcking is peerSocket with a client that acknowledges the total it received after
// every message, at once.
func peerSocketAcking(t *testing.T, v ScreenViewer) {
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
	go func() {
		var total uint32
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			total += uint32(len(b))
			if c.Write(ctx, websocket.MessageBinary, ackFrame(total)) != nil {
				return
			}
		}
	}()
}

// Decision (ADR-0118 §3): the host counts a message's bytes before it writes it, because a
// client may read the frame and acknowledge it before Write returns; an acknowledgement of
// bytes the host had not yet counted is honest. The client here acknowledges exactly what it
// received, at once, and the hook holds each write's return for 50 ms. Seen failing with the
// count after the write: the socket closed 1008 on an honest client.
func TestPeerAckBeforeTheWriteReturnsIsNotRefused(t *testing.T) {
	sendHook = func() { time.Sleep(50 * time.Millisecond) }
	t.Cleanup(func() { sendHook = nil })
	v := &countingViewer{data: make([]byte, 100), closed: make(chan struct{})}
	peerSocketAcking(t, v)
	select {
	case <-v.closed:
		t.Fatal("the host closed an honest client that acknowledged what it received")
	case <-time.After(700 * time.Millisecond):
	}
}

// Decision (ADR-0118 §3): a client that acknowledges once and then goes silent does not hold
// the stream open: the wait on a closed window is bounded by the write timeout and ends the
// socket as a write timeout does. Seen failing without the bound on the wait: the viewer was
// not closed.
func TestPeerSilentClientAfterAnAckIsClosed(t *testing.T) {
	old := peerWriteTimeout
	peerWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { peerWriteTimeout = old })
	v := &countingViewer{data: make([]byte, 16<<10), closed: make(chan struct{})}
	c, ctx, _ := peerSocket(t, v)
	if err := c.Write(ctx, websocket.MessageBinary, ackFrame(0)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("a client that acknowledged once and went silent kept its stream open")
	}
}

// Decision (ADR-0118 §3): exactly 100 acknowledgements inside a second are allowed and the
// 101st closes the socket. Seen failing with the old off-by-one (the 100th closed it).
func TestPeerAckRateBoundIsOverOneHundred(t *testing.T) {
	w := newAckWindow()
	var n atomic.Int64
	base := time.Now()
	w.now = func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Millisecond) }
	w.sentBytes(10)
	for i := 1; i <= 100; i++ {
		if err := w.ack(ackFrame(0)); err != nil {
			t.Fatalf("ack %d refused", i)
		}
	}
	if w.ack(ackFrame(0)) == nil {
		t.Fatal("the 101st ack inside a second was taken")
	}
}

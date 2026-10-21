package hosting

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/policy"
)

// ackLink is a link to a host that plays script on its video socket, and the video socket the
// client dialled, for a host that named hostAPI in its hello.
func ackLink(t *testing.T, hostAPI string, script func(ctx context.Context, c *websocket.Conn)) (*ScreenConn, context.Context) {
	t.Helper()
	addr, fp, _ := screenHost(t, script)
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := newLink(policy.Remote{Name: "x", Address: addr, Fingerprint: fp}, id, "0.33.0", &lockedLog{})
	l.hostAPI = hostAPI
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	sc, err := l.DialScreen(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sc.Close)
	return sc, ctx
}

// Decision (ADR-0118 §3): a client acknowledges the video bytes it received on the peer screen
// socket, [4][u32 total mod 2^32 BE], the total being the sum of the messages' lengths, and the
// last acknowledgement reaches the total even when the stream pauses under 64 KiB (the 100 ms
// timer). Seen failing with received() not counting the bytes: no acknowledgement arrived.
func TestScreenConnAcknowledgesWhatItReceived(t *testing.T) {
	msg := frame(0, 1, bytes.Repeat([]byte{1}, 40000))
	acks := make(chan uint32, 16)
	sc, ctx := ackLink(t, "0.33.0", func(ctx context.Context, c *websocket.Conn) {
		for range 2 {
			c.Write(ctx, websocket.MessageBinary, msg)
		}
		c.Write(ctx, websocket.MessageBinary, frame(0, 2, []byte{1, 2, 3}))
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary && len(b) == 5 && b[0] == 4 {
				acks <- binary.BigEndian.Uint32(b[1:])
			}
		}
	})
	for range 3 {
		if _, err := sc.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	total := uint32(2*len(msg) + 9 + 3)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case n := <-acks:
			if n == total {
				return
			}
			if n > total {
				t.Fatalf("an acknowledgement of %d bytes; %d were received", n, total)
			}
		case <-deadline:
			t.Fatalf("no acknowledgement reached the total %d", total)
		}
	}
}

// Decision (ADR-0118 §3): the client sends acknowledgements only to a host whose hello names
// API 0.33.0 or later: an older host closes 1003 on a binary frame it does not know. Seen
// failing with the gate removed: an acknowledgement reached the 0.32.0 host.
func TestScreenConnSendsNoAcksToAnOlderHost(t *testing.T) {
	for _, api := range []string{"0.32.0", "", "0.9.9"} {
		got := make(chan struct{}, 1)
		sc, ctx := ackLink(t, api, func(ctx context.Context, c *websocket.Conn) {
			c.Write(ctx, websocket.MessageBinary, frame(0, 1, bytes.Repeat([]byte{1}, 100000)))
			if typ, _, err := c.Read(ctx); err == nil && typ == websocket.MessageBinary {
				got <- struct{}{}
			}
		})
		if _, err := sc.Next(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case <-got:
			t.Errorf("a host of API %q was sent an acknowledgement", api)
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// Decision (ADR-0118 §3): the version gate compares numbers, not text: 0.100.0 is after 0.33.0
// and 0.9.0 is before. Seen failing with a string comparison.
func TestAPIAtLeastComparesNumbers(t *testing.T) {
	for _, c := range []struct {
		have string
		want bool
	}{{"0.33.0", true}, {"0.33.1", true}, {"0.100.0", true}, {"1.0.0", true}, {"0.32.9", false}, {"0.9.0", false}, {"", false}, {"0.33", false}, {"x.y.z", false}} {
		if got := APIAtLeast(c.have, "0.33.0"); got != c.want {
			t.Errorf("APIAtLeast(%q, 0.33.0) = %v, want %v", c.have, got, c.want)
		}
	}
}

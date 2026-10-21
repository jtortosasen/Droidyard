package peer_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// endingViewer is a live view that, when its socket's handler closes it, publishes an event
// on the host's bus, as device.watchScreen's stream does with stopped{detached}: the viewer
// is closed because its socket went, and the stream tells its subscription.
type endingViewer struct {
	closed  chan struct{}
	onClose func()
}

func (v *endingViewer) Next(ctx context.Context) (httpapi.ScreenPacket, error) {
	select {
	case <-ctx.Done():
	case <-v.closed:
	}
	return httpapi.ScreenPacket{}, ctx.Err()
}

func (v *endingViewer) Close() {
	v.onClose()
	close(v.closed)
}

func (v *endingViewer) Input([]byte) error { return nil }

// Decision (a lost link ends a relayed screen deviceGone, internal/domain/device/remotescreen.go):
// a link the host cuts reaches the client as a lost link, not as whatever the host's own streams say while it goes down. When a listener shuts down, its
// session sockets are closed before its live views' sockets, so the event a view's close
// makes (stopped{detached}) finds the session already closed and is never sent: the client
// hears the link go, and its relayed screen ends deviceGone. Closed together (the shape
// before this test), the view's close round trip could finish first, the event went out on
// the still-open link, and the client's relayed screen ended failed.
//
// The two closes are ordered through the session-close delay, which holds the session's
// close back by 400 ms: the view's close and its event then have all of that time to
// reach the client. Seen failing without the ordering: the event arrived before the link
// was lost.
func TestShutdownClosesSessionsBeforeLiveViews(t *testing.T) {
	peer.SetSessionCloseDelay(400 * time.Millisecond)
	t.Cleanup(func() { peer.SetSessionCloseDelay(0) })
	host, client := newSide(t, "0.0.0"), newSide(t, "0.0.0")
	host.cfg.hosting.Peers = []policy.HostPeer{{Name: "anna", Fingerprint: client.host.Identity().Fingerprint()}}
	var attached atomic.Bool
	viewer := &endingViewer{closed: make(chan struct{}), onClose: func() {
		host.bus.Publish("peertest.tick", "tick", "", "", tick{N: 99})
	}}
	host.screens = func(string, string) (httpapi.ScreenViewer, error) {
		attached.Store(true)
		return viewer, nil
	}
	addr, stop := host.listen(t, "127.0.0.1:0")
	client.cfg.remotes = []policy.Remote{{Name: "qa-lab", Address: addr, Fingerprint: host.host.Identity().Fingerprint()}}
	client.host.Start()
	t.Cleanup(client.host.Stop)
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the link to come up", func() bool { return client.host.Remote("qa-lab").Connected() })
	link := client.host.Remote("qa-lab")

	// The client's subscription to the host's events, as a relayed screen's.
	events := make(chan int, 8)
	down := link.Down()
	env, _, err := link.CallRouted(context.Background(), capability.RequestEnvelope{ID: "w", Capability: "peertest.watch"}, func(e json.RawMessage) {
		var f struct {
			Payload tick `json:"payload"`
		}
		if json.Unmarshal(e, &f) == nil {
			events <- f.Payload.N
		}
	})
	if err != nil || env.Error != nil {
		t.Fatalf("peertest.watch: %v %+v", err, env.Error)
	}
	// And its video socket, attached.
	conn, err := link.DialScreen(context.Background(), "v")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	go func() {
		for {
			if _, err := conn.Next(context.Background()); err != nil {
				return
			}
		}
	}()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the video socket to be attached on the host", attached.Load)

	go stop()
	select {
	case <-down:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the link was never lost")
	}
	// The read loop delivers an event before it sees the close that follows it, so an event
	// sent while the link was up is in the channel by now.
	select {
	case n := <-events:
		t.Errorf("event %d, made by the live view's close, reached the client before the link was lost: it would end its relayed screen failed, not deviceGone", n)
	default:
	}
}

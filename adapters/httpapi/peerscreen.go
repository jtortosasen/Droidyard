package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
)

// PeerScreenAttach claims the viewer of a stream id for the named peer, once (ADR-0078 §4 on
// the peer port): an id that was not issued to that peer is ErrScreenUnknown, as one that
// was never issued, so a peer learns nothing of another's streams. Like ScreenAttach it is
// supplied by the daemon's wiring and hands back a viewer that reads packets and closes.
type PeerScreenAttach func(peer, id string) (ScreenViewer, error)

// ServePeerScreen serves one accepted /peer/v1/screen socket of the named peer: the first
// text frame, within firstFrame, is {"stream": <id>}; then the same binary messages and
// close codes as /v1/screen (ADR-0078 §4), with no token, since the pinned certificate is
// the peer's authentication. It returns when the socket or the stream ends, with the viewer
// closed. The socket reads at most 4 KiB per frame for its whole life.
func ServePeerScreen(parent context.Context, c *websocket.Conn, peer string, firstFrame time.Duration, attach PeerScreenAttach) {
	c.SetReadLimit(preAuthLimit)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	timer := time.AfterFunc(firstFrame, func() { c.Close(closeUnauthorized, "no stream frame in time") })
	typ, data, err := c.Read(ctx)
	if !timer.Stop() {
		return // the deadline passed: the timer is closing the connection
	}
	if err != nil {
		c.CloseNow()
		return
	}
	var f struct {
		Stream string `json:"stream"`
	}
	if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil {
		c.Close(websocket.StatusUnsupportedData, `a text frame {"stream": <id>} was expected`)
		return
	}
	v, err := attach(peer, f.Stream)
	switch {
	case errors.Is(err, ErrScreenUnknown):
		c.Close(closeStreamUnknown, "unknown stream")
		return
	case errors.Is(err, ErrScreenAttached):
		c.Close(closeStreamAttached, "the stream is already attached")
		return
	case err != nil || v == nil:
		c.Close(websocket.StatusInternalError, "the stream could not be attached")
		return
	}
	sendScreen(ctx, cancel, c, v, defaultScreenWriteTimeout, newAckWindow())
}

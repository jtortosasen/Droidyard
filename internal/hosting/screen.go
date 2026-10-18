package hosting

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// The video of a remote device's live view (ADR-0100 §3, ADR-0078 §4 on the peer port): a
// WebSocket of its own to the host, binary messages [flags u8][pts u64 BE][Annex B]. The
// bytes are another machine's, parsed here in the daemon, so every read is bounded.

const (
	frameHeader = 1 + 8

	frameKey    = 1
	frameConfig = 2

	// MaxScreenFrame is the largest message the socket reads: the header and a packet of
	// the host's Hub's join form, which is its config (at most screen.MaxPacket) in front
	// of a key frame (at most screen.MaxPacket). It is the socket's read limit: the library
	// buffers a message up to it and fails the read, closing the socket, past it, so no
	// message holds more than this.
	MaxScreenFrame = frameHeader + 2*screen.MaxPacket
)

// ErrScreenFrame is a message that is not a packet of ADR-0078 §4: bytes the host should
// never send.
var ErrScreenFrame = errors.New("the host sent a screen message that is not a packet")

// ParseScreenFrame reads one message of the video socket into the packets it stands for:
// one, or two when it is a key frame carrying its config (screen.Split). It checks every
// bound the daemon's Hub assumes, on any input and without panicking: a header of nine
// bytes; flags that are none, key, or key with config (a config packet alone is never
// sent, since the host's Hub merges it); a time that fits an int64; and no packet, once
// split, over screen.MaxPacket. The packets share msg's memory, which the caller must not
// reuse.
func ParseScreenFrame(msg []byte) ([]screen.Packet, error) {
	if len(msg) < frameHeader {
		return nil, fmt.Errorf("%w: %d bytes, a header is %d", ErrScreenFrame, len(msg), frameHeader)
	}
	flags := msg[0]
	if flags&^(frameKey|frameConfig) != 0 || flags&frameConfig != 0 && flags&frameKey == 0 {
		return nil, fmt.Errorf("%w: flags %#x", ErrScreenFrame, flags)
	}
	pts := binary.BigEndian.Uint64(msg[1:frameHeader])
	if pts > math.MaxInt64 {
		return nil, fmt.Errorf("%w: a presentation time over 63 bits", ErrScreenFrame)
	}
	pkts := screen.Split(screen.Packet{
		Key:    flags&frameKey != 0,
		Config: flags&frameConfig != 0,
		PTS:    int64(pts),
		Data:   msg[frameHeader:len(msg):len(msg)],
	})
	for _, p := range pkts {
		if len(p.Data) > screen.MaxPacket {
			return nil, fmt.Errorf("%w: a packet of %d bytes, the cap is %d", ErrScreenFrame, len(p.Data), screen.MaxPacket)
		}
	}
	return pkts, nil
}

// ScreenConn is the video socket of one stream.
type ScreenConn struct {
	c      *websocket.Conn
	client *http.Client
}

// DialScreen attaches the stream a device.watchScreen on the host issued: it opens the
// video socket, pinned like the session, and sends the stream id as its first frame. The
// host closes the socket with 4404 for an id it did not issue to this daemon.
func (l *Link) DialScreen(ctx context.Context, streamID string) (*ScreenConn, error) {
	client := l.pinnedClient()
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dctx, "wss://"+l.remote.Address+capability.PeerScreenPath, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		client.CloseIdleConnections()
		return nil, dialError(l.remote, err)
	}
	conn.SetReadLimit(MaxScreenFrame)
	first, _ := json.Marshal(struct {
		Stream string `json:"stream"`
	}{streamID})
	if err := conn.Write(dctx, websocket.MessageText, first); err != nil {
		conn.CloseNow()
		client.CloseIdleConnections()
		return nil, dialError(l.remote, err)
	}
	return &ScreenConn{c: conn, client: client}, nil
}

// Next reads the next message and returns its packets. An error is the socket's (the host
// closed it, with a code of ADR-0078 §4 or not; ctx ended) or ErrScreenFrame, after which
// the socket is closed.
func (s *ScreenConn) Next(ctx context.Context) ([]screen.Packet, error) {
	typ, data, err := s.c.Read(ctx)
	if err != nil {
		if errors.Is(err, websocket.ErrMessageTooBig) {
			return nil, fmt.Errorf("%w: a message over %d bytes", ErrScreenFrame, MaxScreenFrame)
		}
		return nil, err
	}
	if typ != websocket.MessageBinary {
		s.c.CloseNow() // no courtesy close: a host that sends this is not waited for
		return nil, fmt.Errorf("%w: a text message", ErrScreenFrame)
	}
	pkts, err := ParseScreenFrame(data)
	if err != nil {
		s.c.CloseNow()
		return nil, err
	}
	return pkts, nil
}

// Close drops the socket at once; the host sees it end and releases the stream.
func (s *ScreenConn) Close() {
	s.c.CloseNow()
	s.client.CloseIdleConnections()
}

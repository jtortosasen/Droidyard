package screen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
)

// The live view's own input (plan m2y step 2; ADR-0117): the binary frames a view sends on
// its stream socket, parsed into Events, and Events turned into scrcpy's control messages.
//
// The wire is big endian and every frame is a type byte followed by a fixed length. The
// coordinates are normalised to the frame, 0..65535, so a rotation or a relay changes
// nothing:
//
//	touch   [1][action: 0 down, 1 up, 2 move, 3 cancel][x u16][y u16]   6 bytes
//	key     [2][key: 1 back, 2 home, 3 appSwitch, 4 enter, 5 delete]    2 bytes (down and up)
//	scroll  [3][x u16][y u16][h i16][v i16]                             9 bytes (notches * 256)
//
// Nothing a client sends reaches the control socket as it is: ParseControl validates it and
// EncodeEvent builds scrcpy's messages from the Event, so scrcpy's other messages
// (clipboard, power, app start) are unreachable from a socket. Both are pure and never
// panic, whatever the bytes.

// ErrBadControl is a frame ParseControl refuses, or an Event EncodeEvent cannot encode.
var ErrBadControl = errors.New("screen: invalid control frame")

// ErrControlQueueFull is a Control call refused because the source's queue already holds
// its bound (ADR-0117 §3): the caller closes only its own socket.
var ErrControlQueueFull = errors.New("screen: the control queue is full")

// EventKind is what an Event is.
type EventKind uint8

// The kinds of Event. EventRelease is never on the wire: it is a stream telling its source
// that it is gone, so the source drops what is still queued for it and lifts its finger.
const (
	EventTouch EventKind = iota + 1
	EventKey
	EventScroll
	EventRelease
)

// TouchAction is a touch's action, with the wire's values (Android's MotionEvent actions).
type TouchAction uint8

// The touch actions of the wire.
const (
	TouchDown   TouchAction = 0
	TouchUp     TouchAction = 1
	TouchMove   TouchAction = 2
	TouchCancel TouchAction = 3
)

// The wire's frame types and lengths.
const (
	wireTouch  = 1
	wireKey    = 2
	wireScroll = 3

	wireTouchLen  = 6
	wireKeyLen    = 2
	wireScrollLen = 9
)

// MaxScrollNotches is the bound of a wheel event, in notches: scrcpy takes an axis as
// notches/16 clamped to [-1, 1], so more would be lost at the device. On the wire a notch is
// 256, so the bound is MaxScrollNotches*256 = 4096.
const MaxScrollNotches = 16

// ScrollUnit is the wire's notch (a scroll axis is notches * ScrollUnit).
const ScrollUnit = 256

// wireKeys are the names of the wire's key bytes 1..5.
var wireKeys = [...]string{"", "back", "home", "appSwitch", "enter", "delete"}

// Event is one thing a view's user did, parsed from a frame. X and Y are normalised to the
// frame, 0..65535. For an EventRelease X and Y are the last point of the finger, where its
// cancel is written.
type Event struct {
	Kind   EventKind
	Action TouchAction // EventTouch
	X, Y   uint16      // EventTouch, EventScroll and EventRelease
	Key    string      // EventKey: back, home, appSwitch, enter or delete
	H, V   int16       // EventScroll: notches * 256
}

// ParseControl reads one frame of the wire. A frame of an unknown type, of a length its type
// does not have, or with an unknown action or key is an error wrapping ErrBadControl.
func ParseControl(b []byte) (Event, error) {
	if len(b) == 0 {
		return Event{}, fmt.Errorf("%w: an empty frame", ErrBadControl)
	}
	switch b[0] {
	case wireTouch:
		if len(b) != wireTouchLen {
			return Event{}, badLength("touch", len(b), wireTouchLen)
		}
		if b[1] > byte(TouchCancel) {
			return Event{}, fmt.Errorf("%w: touch action %d", ErrBadControl, b[1])
		}
		return Event{Kind: EventTouch, Action: TouchAction(b[1]),
			X: binary.BigEndian.Uint16(b[2:]), Y: binary.BigEndian.Uint16(b[4:])}, nil
	case wireKey:
		if len(b) != wireKeyLen {
			return Event{}, badLength("key", len(b), wireKeyLen)
		}
		if b[1] == 0 || int(b[1]) >= len(wireKeys) {
			return Event{}, fmt.Errorf("%w: key %d", ErrBadControl, b[1])
		}
		return Event{Kind: EventKey, Key: wireKeys[b[1]]}, nil
	case wireScroll:
		if len(b) != wireScrollLen {
			return Event{}, badLength("scroll", len(b), wireScrollLen)
		}
		return Event{Kind: EventScroll,
			X: binary.BigEndian.Uint16(b[1:]), Y: binary.BigEndian.Uint16(b[3:]),
			H: int16(binary.BigEndian.Uint16(b[5:])), V: int16(binary.BigEndian.Uint16(b[7:]))}, nil
	}
	return Event{}, fmt.Errorf("%w: frame type %d", ErrBadControl, b[0])
}

// EncodeWire is the frame of the wire for ev, the inverse of ParseControl: what a daemon sends
// its host on the peer's screen socket for an event of its own view (ADR-0117 §3). An
// EventRelease is never on the wire, and an unknown kind, action or key is an error wrapping
// ErrBadControl.
func EncodeWire(ev Event) ([]byte, error) {
	switch ev.Kind {
	case EventTouch:
		if ev.Action > TouchCancel {
			return nil, fmt.Errorf("%w: touch action %d", ErrBadControl, ev.Action)
		}
		b := []byte{wireTouch, byte(ev.Action), 0, 0, 0, 0}
		binary.BigEndian.PutUint16(b[2:], ev.X)
		binary.BigEndian.PutUint16(b[4:], ev.Y)
		return b, nil
	case EventKey:
		for i := 1; i < len(wireKeys); i++ {
			if wireKeys[i] == ev.Key {
				return []byte{wireKey, byte(i)}, nil
			}
		}
		return nil, fmt.Errorf("%w: key %q", ErrBadControl, ev.Key)
	case EventScroll:
		b := make([]byte, wireScrollLen)
		b[0] = wireScroll
		binary.BigEndian.PutUint16(b[1:], ev.X)
		binary.BigEndian.PutUint16(b[3:], ev.Y)
		binary.BigEndian.PutUint16(b[5:], uint16(ev.H))
		binary.BigEndian.PutUint16(b[7:], uint16(ev.V))
		return b, nil
	}
	return nil, fmt.Errorf("%w: event kind %d", ErrBadControl, ev.Kind)
}

func badLength(what string, got, want int) error {
	return fmt.Errorf("%w: a %s frame of %d bytes, want %d", ErrBadControl, what, got, want)
}

// scrcpy's scroll message (TYPE_INJECT_SCROLL_EVENT, 21 bytes; read at tag v4.1 in the
// step 1 spike): the position as i32 x and y and the frame's u16 width and height, then
// each axis as an i16 fixed point (notches/16 clamped to [-1, 1] times 0x8000, +1.0 stored
// as 0x7fff), then the buttons (i32, zero).
const msgScroll = 3

// EncodeEvent returns scrcpy's control messages for ev against a frame of the given size,
// concatenated in the order to write them: a touch is one 32-byte message with the wire's
// action (pressure full for down and move, none for up and cancel), a key is its key-down
// message and then its key-up message, a scroll is one 21-byte message. A coordinate becomes
// the nearest pixel, clamped to the last pixel.
//
// An Event it cannot encode (a release, an unknown kind, action or key, a size outside
// 1..MaxDimension) is an error wrapping ErrBadControl.
func EncodeEvent(ev Event, size Size) ([]byte, error) {
	if size.Width < 1 || size.Height < 1 || size.Width > MaxDimension || size.Height > MaxDimension {
		return nil, fmt.Errorf("%w: frame size %dx%d", ErrBadControl, size.Width, size.Height)
	}
	switch ev.Kind {
	case EventTouch:
		pressure := uint16(pressureDown)
		switch ev.Action {
		case TouchDown, TouchMove:
		case TouchUp, TouchCancel:
			pressure = pressureUp
		default:
			return nil, fmt.Errorf("%w: touch action %d", ErrBadControl, ev.Action)
		}
		x, y := normPixel(ev.X, size.Width), normPixel(ev.Y, size.Height)
		return touch(byte(ev.Action), x, y, size, pressure), nil
	case EventKey:
		// The view's wire carries five keys (ADR-0117 §3); device.pressKey's further names
		// are keycodes too, but a view's event never names them.
		code, ok := keycodes[ev.Key]
		if !ok || !slices.Contains(wireKeys[1:], ev.Key) {
			return nil, fmt.Errorf("%w: key %q", ErrBadControl, ev.Key)
		}
		return append(keycode(actionDown, code), keycode(actionUp, code)...), nil
	case EventScroll:
		return scroll(normPixel(ev.X, size.Width), normPixel(ev.Y, size.Height), size, ev.H, ev.V), nil
	}
	return nil, fmt.Errorf("%w: event kind %d", ErrBadControl, ev.Kind)
}

// normPixel maps a coordinate normalised to 0..65535 to the nearest pixel of an axis of n
// pixels, the last pixel at most.
func normPixel(v uint16, n int) int32 {
	p := (int64(v)*int64(n) + 32767) / 65535
	return int32(min(p, int64(n-1)))
}

// scroll is TYPE_INJECT_SCROLL_EVENT. An axis of the wire is notches * 256; scrcpy's is
// notches/16 * 0x8000, which is the wire value times 8, saturated.
func scroll(x, y int32, size Size, h, v int16) []byte {
	b := make([]byte, 21)
	b[0] = msgScroll
	binary.BigEndian.PutUint32(b[1:], uint32(x))
	binary.BigEndian.PutUint32(b[5:], uint32(y))
	binary.BigEndian.PutUint16(b[9:], uint16(size.Width))
	binary.BigEndian.PutUint16(b[11:], uint16(size.Height))
	binary.BigEndian.PutUint16(b[13:], scrollAxis(h))
	binary.BigEndian.PutUint16(b[15:], scrollAxis(v))
	// buttons (b[17:21]) stay zero
	return b
}

func scrollAxis(wire int16) uint16 {
	return uint16(int16(max(min(int32(wire)*8, 0x7fff), -0x8000)))
}

// ClampScroll adds two wheel amounts of the wire, saturating at the bound of
// MaxScrollNotches, so a burst summed in a queue never carries more than the device would
// apply.
func ClampScroll(a, b int16) int16 {
	const bound = MaxScrollNotches * ScrollUnit
	return int16(max(min(int32(a)+int32(b), bound), -bound))
}

// CancelOf returns the ACTION_CANCEL message for the finger whose last touch message was
// msg, against a frame of the given size: the same point, clamped into the frame. It is how
// the daemon lifts a finger that is not the client's to lift (ADR-0117 §5). ok is false when
// msg is not a touch message or the size is not a frame's.
func CancelOf(msg []byte, size Size) (cancel []byte, ok bool) {
	if len(msg) != 32 || msg[0] != msgTouch ||
		size.Width < 1 || size.Height < 1 || size.Width > MaxDimension || size.Height > MaxDimension {
		return nil, false
	}
	x := min(max(int32(binary.BigEndian.Uint32(msg[10:])), 0), int32(size.Width-1))
	y := min(max(int32(binary.BigEndian.Uint32(msg[14:])), 0), int32(size.Height-1))
	return touch(actionCancel, x, y, size, pressureUp), true
}

// Holder names who holds a device's one finger: a view's stream, or an Inject for the length
// of its gesture (ADR-0117 §4). Peer says whether it acts for a paired peer, which a local
// holder takes the finger from and which never takes it from a local one. Holders are made
// with NewHolder; the zero Holder is a local one that is shared by everyone who uses it.
type Holder struct {
	id   uint64
	peer bool
}

var holderSeq atomic.Uint64

// NewHolder returns a Holder unlike every other.
func NewHolder(peer bool) Holder { return Holder{id: holderSeq.Add(1), peer: peer} }

// Peer says the holder acts for a paired peer.
func (h Holder) Peer() bool { return h.peer }

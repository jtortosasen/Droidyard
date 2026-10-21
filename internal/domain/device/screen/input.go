package screen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// Limits on one input. The capabilities that build an Input (plan m2p steps 6 and 7)
// bound theirs tighter; these keep EncodeControl's output finite for any Input.
const (
	MaxInputDuration = 60 * time.Second
	MaxSwipePoints   = 1024
)

// ErrBadInput: an Input EncodeControl cannot encode.
var ErrBadInput = errors.New("screen: invalid input")

// Input is one thing a person does to the screen: a Tap, a Swipe, a Key or a Scroll. Coordinates
// are normalised to the stream's frame, 0..1 from the left and from the top, so the
// same Input means the same place whatever the size. Times are offsets from the moment
// the input starts.
type Input interface{ isInput() }

// Point is a place on the screen at an offset into a Swipe.
type Point struct {
	X, Y float64
	T    time.Duration
}

// Tap touches the point and lifts after Hold; a zero Hold is a tap, a longer one a long
// press.
type Tap struct {
	X, Y float64
	Hold time.Duration
}

// Swipe puts a finger down at its first point, moves it through the others at their T
// and lifts it at the last. T never decreases.
type Swipe struct{ Points []Point }

// Key presses and releases a named key: back, home, appSwitch, enter or delete.
type Key struct{ Name string }

// Scroll turns the wheel at the point: DX and DY are in notches, each within
// ±MaxScrollNotches, with Android's sign (DY > 0 scrolls up, DX > 0 right). It takes no
// finger, like a Key (ADR-0117 §4).
type Scroll struct{ X, Y, DX, DY float64 }

func (Tap) isInput()    {}
func (Swipe) isInput()  {}
func (Key) isInput()    {}
func (Scroll) isInput() {}

// keycodes are Android's KeyEvent codes.
var keycodes = map[string]int32{
	"back":      4,   // KEYCODE_BACK
	"home":      3,   // KEYCODE_HOME
	"appSwitch": 187, // KEYCODE_APP_SWITCH
	"enter":     66,  // KEYCODE_ENTER
	"delete":    67,  // KEYCODE_DEL
}

// Control is one message for the control socket and when to write it, as an offset
// from the start of the input.
type Control struct {
	At   time.Duration
	Data []byte
}

// scrcpy's control message types and Android's MotionEvent and KeyEvent actions.
const (
	msgKeycode = 0
	msgTouch   = 2

	actionDown   = 0 // ACTION_DOWN, for a key and for a touch
	actionUp     = 1
	actionMove   = 2
	actionCancel = 3 // ACTION_CANCEL, for a view's events (control.go)

	pressureDown = 0xffff // 1.0 as a u16 fixed point
	pressureUp   = 0
)

// fingerID is scrcpy's generic finger pointer id, -2 (confirmed by taps that landed,
// docs/research/spike-screen-stream-2026-10.md §(d)).
const fingerID = ^uint64(1)

// EncodeControl returns scrcpy's control messages for in against a frame of the given
// size, in the order to write them, each with its offset:
//
//   - Tap: a touch down at 0 and a touch up at Hold;
//   - Swipe: a touch down at the first point, a move at each later point, then a touch up
//     at the last point, all at the points' T;
//   - Key: a key down and a key up, both at 0;
//   - Scroll: one scroll message at 0, its notches rounded to the wire's 1/256.
//
// A coordinate becomes the nearest pixel, math.Round(v*size), clamped to the last pixel
// so that 1.0 is the frame's edge, not past it. The messages carry the frame size they
// were computed against, as the server expects.
//
// An Input that is not encodable (a coordinate that is NaN or outside 0..1, a negative
// or over-long Hold or T, a Swipe with fewer than two or more than MaxSwipePoints points
// or with T going backwards, an unknown key, a Scroll with an amount that is NaN or over
// MaxScrollNotches, a size outside 1..MaxDimension) is an error wrapping ErrBadInput.
func EncodeControl(in Input, size Size) ([]Control, error) {
	if size.Width < 1 || size.Height < 1 || size.Width > MaxDimension || size.Height > MaxDimension {
		return nil, fmt.Errorf("%w: frame size %dx%d", ErrBadInput, size.Width, size.Height)
	}
	switch in := in.(type) {
	case Tap:
		if in.Hold < 0 || in.Hold > MaxInputDuration {
			return nil, fmt.Errorf("%w: hold %v", ErrBadInput, in.Hold)
		}
		x, y, err := pixel(in.X, in.Y, size)
		if err != nil {
			return nil, err
		}
		return []Control{
			{0, touch(actionDown, x, y, size, pressureDown)},
			{in.Hold, touch(actionUp, x, y, size, pressureUp)},
		}, nil
	case Swipe:
		n := len(in.Points)
		if n < 2 || n > MaxSwipePoints {
			return nil, fmt.Errorf("%w: a swipe of %d points", ErrBadInput, n)
		}
		out := make([]Control, 0, n+1)
		var last time.Duration
		var lx, ly int32
		for i, p := range in.Points {
			if p.T < last || p.T > MaxInputDuration { // the first point's T is at least 0
				return nil, fmt.Errorf("%w: point %d at %v", ErrBadInput, i, p.T)
			}
			last = p.T
			x, y, err := pixel(p.X, p.Y, size)
			if err != nil {
				return nil, err
			}
			action := byte(actionMove)
			if i == 0 {
				action = actionDown
			}
			out = append(out, Control{p.T, touch(action, x, y, size, pressureDown)})
			lx, ly = x, y
		}
		out = append(out, Control{last, touch(actionUp, lx, ly, size, pressureUp)})
		return out, nil
	case Key:
		code, ok := keycodes[in.Name]
		if !ok {
			return nil, fmt.Errorf("%w: key %q", ErrBadInput, in.Name)
		}
		return []Control{
			{0, keycode(actionDown, code)},
			{0, keycode(actionUp, code)},
		}, nil
	case Scroll:
		x, y, err := pixel(in.X, in.Y, size)
		if err != nil {
			return nil, err
		}
		h, err := scrollWire(in.DX)
		if err != nil {
			return nil, err
		}
		v, err := scrollWire(in.DY)
		if err != nil {
			return nil, err
		}
		return []Control{{0, scroll(x, y, size, h, v)}}, nil
	default:
		return nil, fmt.Errorf("%w: %T", ErrBadInput, in)
	}
}

// scrollWire is an amount in notches as the wire's notches*256.
func scrollWire(notches float64) (int16, error) {
	if math.IsNaN(notches) || math.Abs(notches) > MaxScrollNotches {
		return 0, fmt.Errorf("%w: scroll of %v notches", ErrBadInput, notches)
	}
	return int16(math.Round(notches * ScrollUnit)), nil
}

// pixel maps a normalised point to the nearest pixel of the frame.
func pixel(nx, ny float64, size Size) (x, y int32, err error) {
	x, err = axis(nx, size.Width)
	if err == nil {
		y, err = axis(ny, size.Height)
	}
	return x, y, err
}

func axis(v float64, n int) (int32, error) {
	if math.IsNaN(v) || v < 0 || v > 1 {
		return 0, fmt.Errorf("%w: coordinate %v", ErrBadInput, v)
	}
	return int32(min(math.Round(v*float64(n)), float64(n-1))), nil
}

// touch is TYPE_INJECT_TOUCH_EVENT: 32 bytes, big endian.
func touch(action byte, x, y int32, size Size, pressure uint16) []byte {
	b := make([]byte, 32)
	b[0] = msgTouch
	b[1] = action
	binary.BigEndian.PutUint64(b[2:], fingerID)
	binary.BigEndian.PutUint32(b[10:], uint32(x))
	binary.BigEndian.PutUint32(b[14:], uint32(y))
	binary.BigEndian.PutUint16(b[18:], uint16(size.Width))
	binary.BigEndian.PutUint16(b[20:], uint16(size.Height))
	binary.BigEndian.PutUint16(b[22:], pressure)
	// action button and buttons (b[24:32]) stay zero
	return b
}

// keycode is TYPE_INJECT_KEYCODE: 14 bytes, with no repeat and no meta state.
func keycode(action byte, code int32) []byte {
	b := make([]byte, 14)
	b[0] = msgKeycode
	b[1] = action
	binary.BigEndian.PutUint32(b[2:], uint32(code))
	return b
}

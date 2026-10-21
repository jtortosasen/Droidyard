package device

import (
	"context"
	"errors"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// device.tap, device.swipe and device.pressKey (plan m2p step 6; ADR-0079): input to the
// device from the live view, sent down the control socket of the device's running screen
// source. They are capabilities like any other, so each input passes the device policy
// (authorize), the device's write key and the operation trace; they are for humans (ADR-0060
// §2), so an MCP session is refused by the core, not by omission.

// ErrNoStream is an input with no live view open on the device: nothing was sent
// (ADR-0079 §3). The input capabilities never start a source.
const ErrNoStream capability.ErrorCode = "DEVICE_NO_STREAM"

// The bounds of one input (ADR-0079 §1); the screen package's own are looser.
const (
	maxTapHoldMs     = 2000
	minSwipePoints   = 2
	maxSwipePoints   = 64
	maxSwipeSpanMs   = 5000
	screenInputSince = "0.27.0"
)

// ScreenInputResult is the result of the three input capabilities.
type ScreenInputResult struct {
	Delivered bool `json:"delivered" jsonschema:"required" jsonschema_description:"True: the input reached the device, in full or, with a warning, in part. Not what the app did with it."`
	// Warnings are the daemon's own words, never device text.
	Warnings []string `json:"warnings,omitempty" jsonschema_description:"Set when the live view failed after the input began, so the device may have received part of it."`
}

// ScreenTapParams is device.tap's parameters.
type ScreenTapParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list. Its live view must be open."`
	// X and Y are pointers so that a null is refused rather than read as 0.
	X          *float64 `json:"x" jsonschema:"required,minimum=0,maximum=1" jsonschema_description:"Where to touch, from the left edge of the frame (0) to the right edge (1)."`
	Y          *float64 `json:"y" jsonschema:"required,minimum=0,maximum=1" jsonschema_description:"Where to touch, from the top edge of the frame (0) to the bottom edge (1)."`
	DurationMs *int     `json:"durationMs,omitempty" jsonschema:"minimum=0,maximum=2000" jsonschema_description:"How long to hold the touch, in milliseconds (default 0, a tap; a longer one is a long press). At most 2000."`
}

// SwipePoint is one place on a swipe's path. The fields are pointers so that a missing
// one is refused rather than read as 0.
type SwipePoint struct {
	X *float64 `json:"x" jsonschema:"required,minimum=0,maximum=1" jsonschema_description:"From the left edge of the frame (0) to the right edge (1)."`
	Y *float64 `json:"y" jsonschema:"required,minimum=0,maximum=1" jsonschema_description:"From the top edge of the frame (0) to the bottom edge (1)."`
	T *int     `json:"t" jsonschema:"required,minimum=0,maximum=5000" jsonschema_description:"Milliseconds from the first point (the first point's is 0); strictly greater than the point before, at most 5000."`
}

// ScreenSwipeParams is device.swipe's parameters.
type ScreenSwipeParams struct {
	Device string       `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list. Its live view must be open."`
	Path   []SwipePoint `json:"path" jsonschema:"required,minItems=2,maxItems=64" jsonschema_description:"The finger's path: down at the first point, through the others at their t, up at the last. 2 to 64 points."`
}

// ScreenKeyParams is device.pressKey's parameters.
type ScreenKeyParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list. Its live view must be open."`
	Key    string `json:"key" jsonschema:"required,enum=back,enum=home,enum=appSwitch,enum=enter,enum=delete" jsonschema_description:"The system key to press and release."`
}

func unitInterval(v float64) bool { return v >= 0 && v <= 1 } // false for NaN

func (p ScreenTapParams) input() (screen.Input, error) {
	if p.X == nil || p.Y == nil {
		return nil, capability.Errorf(capability.ErrInvalidParams, "x and y are required numbers")
	}
	if !unitInterval(*p.X) || !unitInterval(*p.Y) {
		return nil, capability.Errorf(capability.ErrInvalidParams, "x and y must be between 0 and 1, got %v and %v", *p.X, *p.Y)
	}
	hold := 0
	if p.DurationMs != nil {
		hold = *p.DurationMs
	}
	if hold < 0 || hold > maxTapHoldMs {
		return nil, capability.Errorf(capability.ErrInvalidParams, "durationMs must be between 0 and %d, got %d", maxTapHoldMs, hold)
	}
	return screen.Tap{X: *p.X, Y: *p.Y, Hold: time.Duration(hold) * time.Millisecond}, nil
}

func (p ScreenSwipeParams) input() (screen.Input, error) {
	n := len(p.Path)
	if n < minSwipePoints || n > maxSwipePoints {
		return nil, capability.Errorf(capability.ErrInvalidParams, "path carries %d points, between %d and %d are needed", n, minSwipePoints, maxSwipePoints)
	}
	pts := make([]screen.Point, n)
	prev := -1
	for i, pt := range p.Path {
		if pt.X == nil || pt.Y == nil || pt.T == nil {
			return nil, capability.Errorf(capability.ErrInvalidParams, "path point %d needs x, y and t", i)
		}
		if !unitInterval(*pt.X) || !unitInterval(*pt.Y) {
			return nil, capability.Errorf(capability.ErrInvalidParams, "path point %d: x and y must be between 0 and 1, got %v and %v", i, *pt.X, *pt.Y)
		}
		t := *pt.T
		switch {
		case i == 0 && t != 0:
			return nil, capability.Errorf(capability.ErrInvalidParams, "path point 0: t counts from the first point, so it must be 0, got %d", t)
		case t <= prev:
			return nil, capability.Errorf(capability.ErrInvalidParams, "path point %d: t must be greater than the point before (%d), got %d", i, prev, t)
		case t > maxSwipeSpanMs:
			return nil, capability.Errorf(capability.ErrInvalidParams, "path point %d: t must be at most %d, got %d", i, maxSwipeSpanMs, t)
		}
		prev = t
		pts[i] = screen.Point{X: *pt.X, Y: *pt.Y, T: time.Duration(t) * time.Millisecond}
	}
	return screen.Swipe{Points: pts}, nil
}

var pressKeys = []string{"back", "home", "appSwitch", "enter", "delete"}

func (p ScreenKeyParams) input() (screen.Input, error) {
	for _, k := range pressKeys {
		if p.Key == k {
			return screen.Key{Name: k}, nil
		}
	}
	return nil, capability.Errorf(capability.ErrInvalidParams, "key must be one of back, home, appSwitch, enter, delete, got %q", p.Key)
}

// registerScreenInput registers the three. build turns a call's parameters into the Input
// (validating them) and names the device.
func registerScreenInput(reg *capability.Registry, adb *ADB) {
	type spec struct {
		name, description string
		params            any
		device            func(*capability.Call) string
		build             func(*capability.Call) (screen.Input, error)
	}
	specs := []spec{
		{
			name:        "device.tap",
			description: "Touches the device's screen at a point of its open live view, normalised to the frame, and lifts after durationMs (a long press). With no live view open it fails DEVICE_NO_STREAM and sends nothing.",
			params:      ScreenTapParams{},
			device:      func(c *capability.Call) string { return c.Params.(ScreenTapParams).Device },
			build:       func(c *capability.Call) (screen.Input, error) { return c.Params.(ScreenTapParams).input() },
		},
		{
			name:        "device.swipe",
			description: "Drags a finger along a path of points of the open live view, normalised to the frame and replayed with the points' timing. With no live view open it fails DEVICE_NO_STREAM and sends nothing.",
			params:      ScreenSwipeParams{},
			device:      func(c *capability.Call) string { return c.Params.(ScreenSwipeParams).Device },
			build:       func(c *capability.Call) (screen.Input, error) { return c.Params.(ScreenSwipeParams).input() },
		},
		{
			name:        "device.pressKey",
			description: "Presses and releases a system key on the device. With no live view open it fails DEVICE_NO_STREAM and sends nothing.",
			params:      ScreenKeyParams{},
			device:      func(c *capability.Call) string { return c.Params.(ScreenKeyParams).Device },
			build:       func(c *capability.Call) (screen.Input, error) { return c.Params.(ScreenKeyParams).input() },
		},
	}
	for _, s := range specs {
		reg.MustRegister(adb.plain(capability.Descriptor{
			Name:         s.name,
			Kind:         capability.Command,
			Scope:        capability.Global,
			Audience:     capability.AudienceHuman,
			Since:        screenInputSince,
			Availability: []capability.Requirement{capability.RequiresADB},
			Description:  s.description,
			Params:       s.params,
			Target: func(p any) *capability.OpTarget {
				return deviceTarget(s.device(&capability.Call{Params: p}))
			},
			Result: ScreenInputResult{},
			Errors: append(append([]capability.ErrorCode{}, LentErrors...),
				ErrNoStream, ErrStreamFailed, capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
			Resource: func(call *capability.Call) string {
				if serial := s.device(call); serial != "" {
					return writeKey(serial)
				}
				return ""
			},
			Annotations: capability.Annotations{
				Destructive: boolPtr(false),
				Reason:      "its effect belongs to the app, and the policy keeps agents on emulators by default (mcp.md §Annotations)",
			},
			Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
				// Validate before the hook so a malformed call runs no adb command at all.
				if _, err := s.build(call); err != nil {
					return nil, err
				}
				return adb.authorize(ctx, call.Origin, s.device(call), s.name)
			},
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				in, err := s.build(call)
				if err != nil {
					return nil, err
				}
				return adb.injectScreen(ctx, call.Origin, call.Authorized.(*target).serial, in)
			},
		}))
	}
}

// runningScreen is the device's running live-view source, or nil. It never starts one.
func (a *ADB) runningScreen(serial string) screen.Source {
	if a.screenLookup != nil {
		return a.screenLookup(serial)
	}
	if s := a.screens.running(serial); s != nil {
		return s
	}
	return nil // not s, which would be a non-nil interface holding a nil pointer
}

// injectScreen sends in down the device's running source. Nothing is sent when there is no
// live view (ADR-0079 §3). A failure before the first byte is DEVICE_STREAM_FAILED; one
// after it completes the call with a warning, since part of the gesture is on the device
// and an error would invite a repeat (ui-automation.md §Design rules). A cancelled or
// timed-out call is that, not a stream failure.
func (a *ADB) injectScreen(ctx context.Context, who capability.Origin, serial string, in screen.Input) (any, error) {
	src := a.runningScreen(serial)
	if src == nil {
		return nil, capability.NewError(ErrNoStream, "the device has no live view open, so there is nothing to send the input through").
			WithNextStep("Open the device's live view, then retry.")
	}
	// The call's origin classes its gesture (ADR-0117 §4): a peer's lent call holds the finger
	// as a peer, which the host's own views take it from; any other call is a local holder.
	err := src.Inject(ctx, screen.NewHolder(who.Client == capability.ClientPeer), in)
	switch {
	case err == nil:
		return ScreenInputResult{Delivered: true}, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	}
	var partial *screen.PartialError
	if errors.As(err, &partial) {
		what := "the live view failed after the input began (" + injectCause(partial.Err) + ")"
		if errors.Is(partial.Err, errFingerLost) {
			what = "the live view took the finger after the input began"
		}
		return ScreenInputResult{Delivered: true, Warnings: []string{
			what + ": the device may have received only part of it; look at the screen before repeating it",
		}}, nil
	}
	if errors.Is(err, screen.ErrBadInput) {
		return nil, capability.Errorf(capability.ErrInvalidParams, "the input cannot be sent to the live view's frame").WithCause(err.Error())
	}
	if errors.Is(err, errFingerLost) {
		// Taken before the first message: the live view is fine and the call can be retried.
		// (No existing code says "someone else holds the finger": DEVICE_OCCUPIED is the
		// session registry's, ADR-0094, and is never returned for this.)
		return nil, capability.NewError(ErrStreamFailed, "the input was not sent: the live view's user took the finger").
			WithCause(injectCause(err)).
			WithNextStep("Nothing was sent and the live view is unharmed. Retry once the person using the live view has lifted the finger.")
	}
	return nil, capability.NewError(ErrStreamFailed, "the input could not be sent: the device's live view failed").
		WithCause(injectCause(err)).
		WithNextStep("Nothing was sent. Open the device's live view again, then retry.")
}

// injectCause says why an Inject failed in the daemon's own words, never the device's.
func injectCause(err error) string {
	var end *ScreenEnd
	switch {
	case errors.As(err, &end):
		return "the live view ended: " + end.Reason + ": " + end.Cause
	case errors.Is(err, screen.ErrClosed):
		return "the live view was closed"
	case errors.Is(err, errFingerLost):
		return "the host's own live view took the finger, or the frame changed"
	}
	return "the control socket could not be written"
}

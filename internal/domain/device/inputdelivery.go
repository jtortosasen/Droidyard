package device

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// How device.tap, device.swipe and device.pressKey reach the device (plan m3g step 4;
// ADR-0133 §1, §2). Three deliveries, tried in this order, and a request reaches the device
// at most once whichever answers:
//
//   - stream: the device's open live view takes the input down its control socket, as it
//     always did (ADR-0079);
//   - reader: the persistent UI reader injects it (`gesture`, `press`; ADR-0035 §1). A
//     `denied` or a reader that cannot run executed nothing, so the next delivery is allowed
//     (ADR-0035 §3); `busy` and a refusal executed nothing too, but they fail the call, since
//     the reader is there and the device is not ready; a request the reader took and did not
//     answer may have applied, so the call completes with a warning and nothing else is tried;
//   - input: `adb shell input` for a tap, a long press, a two-point swipe and a key. A path of
//     more than two points has no such command and is refused before anything is sent.
//
// The coordinates are fractions of the screen device.uiTree reports as `screen`: the reader
// scales them by the display's real size when the gesture starts, `input` by the on-device
// screencap header, which follows the rotation (spike agent-gestures (d); `wm size` does not).
const (
	// DeliveryStream, DeliveryReader and DeliveryInput are ScreenInputResult.Delivery.
	DeliveryStream = "stream"
	DeliveryReader = "reader"
	DeliveryInput  = "input"

	// tapMinHold is how long a tap holds the finger down through the reader: the reader needs
	// strictly increasing times, and a touch that lifts at once is not always a tap.
	tapMinHold = 50 * time.Millisecond

	// screenHeaderCommand reads the first eight bytes of a screencap, the width and height as
	// two 32-bit words, on the device.
	screenHeaderCommand = "screencap | head -c 8 | od -An -tu4"
	// maxScreenDimension bounds a size read from the device: the stream wire's own limit.
	maxScreenDimension = screen.MaxDimension
)

// inputSizeTimeout bounds the on-device screencap header read, about 0.3 s measured. A
// variable so a test injects a scale.
var inputSizeTimeout = 5 * time.Second

// inputWhat is the past participle the reader's refusals and busy answers use for an input:
// the call fails with "nothing was <what>".
func inputWhat(in screen.Input) string {
	switch in.(type) {
	case screen.Tap:
		return "tapped"
	case screen.Swipe:
		return "gestured"
	}
	return "pressed"
}

// inputNoun names an input in a warning.
func inputNoun(in screen.Input) string {
	switch in.(type) {
	case screen.Tap:
		return "tap"
	case screen.Swipe:
		return "swipe"
	}
	return "key press"
}

// deliverInput sends in to the device by the first delivery that can take it (see above).
func (a *ADB) deliverInput(ctx context.Context, call *capability.Call, t *target, in screen.Input) (any, error) {
	started := t.started
	if started.IsZero() {
		started = time.Now()
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(actionRunningBound))
	defer cancel()

	if src := a.runningScreen(t.serial); src != nil {
		return a.injectThrough(ctx, call.Origin, src, in)
	}
	if err := a.ready(ctx, t); err != nil {
		return nil, err
	}

	ac := a.newActor(ctx, t)
	if ac.ok {
		res, done, err := a.deliverByReader(ctx, ac, in)
		if done || err != nil {
			return res, err
		}
		// Not delivered and nothing executed: the reader denied the request or went away.
	}
	return a.deliverByInput(ctx, t, in)
}

// deliverByReader sends in through the reader. done says the call is settled (a result or an
// error); not done with a nil error means nothing was executed and the next delivery may
// answer.
func (a *ADB) deliverByReader(ctx context.Context, ac *actor, in screen.Input) (res any, done bool, err error) {
	var act readerAct
	var aerr error
	switch in := in.(type) {
	case screen.Tap:
		hold := max(in.Hold, tapMinHold)
		act, aerr = ac.gesture(ctx, []pathPoint{{X: in.X, Y: in.Y}, {X: in.X, Y: in.Y, At: hold}})
	case screen.Swipe:
		points := make([]pathPoint, len(in.Points))
		for i, p := range in.Points {
			points[i] = pathPoint{X: p.X, Y: p.Y, At: p.T}
		}
		act, aerr = ac.gesture(ctx, points)
	case screen.Key:
		code, ok := screen.KeyCode(in.Name)
		if !ok {
			return nil, true, capability.Errorf(capability.ErrInvalidParams, "key %q is not a key device.pressKey takes", in.Name)
		}
		act, aerr = ac.press(ctx, code)
	default:
		return nil, true, capability.Errorf(capability.ErrInvalidParams, "the input cannot be sent")
	}
	if aerr != nil {
		// Nothing was executed: the reader could not run or denied the request (ADR-0035 §3),
		// or the call ended first, which no other delivery answers.
		if cerr := ctx.Err(); cerr != nil {
			return nil, true, cerr
		}
		if errors.Is(aerr, context.Canceled) || errors.Is(aerr, context.DeadlineExceeded) {
			return nil, true, aerr
		}
		return nil, false, nil
	}
	what := inputWhat(in)
	switch act.kind {
	case readerActDone:
		return ScreenInputResult{Delivered: true, Delivery: DeliveryReader}, true, nil
	case readerActBusy:
		return nil, true, actingBusyError(what)
	case readerActRefused:
		return nil, true, actRefusedError(act, what)
	}
	// The reader took the request and never answered: it may be on the device, so the call
	// completes and nothing is sent again (ADR-0035 §3; ADR-0020's 2026-10-06 line).
	return ScreenInputResult{Delivered: true, Delivery: DeliveryReader, Warnings: []string{
		"The reader took the " + inputNoun(in) + " but never answered, so it may have reached the device; look at the screen before repeating it.",
	}}, true, nil
}

// deliverByInput sends in with `adb shell input`, once.
func (a *ADB) deliverByInput(ctx context.Context, t *target, in screen.Input) (any, error) {
	var args []string
	var span time.Duration
	switch in := in.(type) {
	case screen.Key:
		code, ok := screen.KeyCode(in.Name)
		if !ok {
			return nil, capability.Errorf(capability.ErrInvalidParams, "key %q is not a key device.pressKey takes", in.Name)
		}
		args = []string{"input", "keyevent", strconv.Itoa(code)}
	case screen.Tap:
		w, h, err := a.screenSize(ctx, t)
		if err != nil {
			return nil, err
		}
		x, y := strconv.Itoa(pixelOf(in.X, w)), strconv.Itoa(pixelOf(in.Y, h))
		if in.Hold > 0 {
			span = in.Hold
			args = []string{"input", "swipe", x, y, x, y, strconv.FormatInt(in.Hold.Milliseconds(), 10)}
		} else {
			args = []string{"input", "tap", x, y}
		}
	case screen.Swipe:
		if len(in.Points) != 2 {
			return nil, capability.NewError(ErrNotReady, "the UI reader cannot run on this device, and adb shell input drags along two points only").
				WithCause("the swipe has " + strconv.Itoa(len(in.Points)) + " points and the reader that would inject a longer path is not running").
				WithNextStep("Nothing was sent. Swipe from one point to another, or open the device's live view first.")
		}
		w, h, err := a.screenSize(ctx, t)
		if err != nil {
			return nil, err
		}
		from, to := in.Points[0], in.Points[1]
		span = to.T - from.T
		args = []string{"input", "swipe",
			strconv.Itoa(pixelOf(from.X, w)), strconv.Itoa(pixelOf(from.Y, h)),
			strconv.Itoa(pixelOf(to.X, w)), strconv.Itoa(pixelOf(to.Y, h)),
			strconv.FormatInt(span.Milliseconds(), 10)}
	default:
		return nil, capability.Errorf(capability.ErrInvalidParams, "the input cannot be sent")
	}
	warn, err := a.issueInput(ctx, t, inputNoun(in), actionTapTimeout+span, args...)
	if err != nil {
		return nil, err
	}
	res := ScreenInputResult{Delivered: true, Delivery: DeliveryInput}
	if warn != "" {
		res.Warnings = []string{warn}
	}
	return res, nil
}

// issueInput runs one `adb shell input` command and returns a warning when it did not
// provably fail before it ran: a timeout or a non-zero exit may still have reached the
// device, and an error would invite a second one (as inputTap, D18). It returns an error
// only when the command provably never ran: adb could not be started, or says the device is
// gone before the shell ran.
func (a *ADB) issueInput(ctx context.Context, t *target, noun string, timeout time.Duration, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"-s", t.serial, "shell"}, args...)
	out, err := a.Run(cctx, t.adb, timeout, 1<<10, full...)
	if cerr := ctx.Err(); cerr != nil {
		// The command was issued, so it may have reached the device whether the call was
		// cancelled or its running bound elapsed: a warning, never an error.
		if errors.Is(cerr, context.Canceled) {
			return inputWarning(noun, "the call was cancelled while the command was running"), nil
		}
		return inputWarning(noun, "the call's running bound elapsed while the command was running"), nil
	}
	if errors.Is(err, ErrNoStarter) || (err != nil && strings.HasPrefix(err.Error(), "starting adb")) {
		return "", capability.NewError(ErrADBFailed, "adb could not be run").
			WithCause("adb shell " + strings.Join(args[:2], " ") + ": " + err.Error()).
			WithNextStep("Check that the adb binary is executable, then retry: nothing was sent.")
	}
	if line := disconnectedLine(t.serial, out); line != "" {
		return "", disconnected(t.serial, line)
	}
	switch {
	case err == nil && out.ExitCode == 0:
		return "", nil
	case errors.Is(err, ErrTimeout):
		return inputWarning(noun, "it timed out"), nil
	case err != nil:
		return inputWarning(noun, err.Error()), nil
	default:
		return inputWarning(noun, fmt.Sprintf("it exited with status %d", out.ExitCode)), nil
	}
}

// inputWarning is the warning of an input command whose outcome is unknown.
func inputWarning(noun, why string) string {
	return "The " + noun + " command did not report success (" + why + "); it may have reached the device, and retrying the call could repeat it."
}

// screenSize reads the screen's real size, rotation applied, from the on-device screencap
// header (spike agent-gestures (d)). Failing to read it is DEVICE_NOT_READY with nothing sent.
func (a *ADB) screenSize(ctx context.Context, t *target) (width, height int, err error) {
	w, h, why := a.readScreenHeader(ctx, t)
	if why == "" {
		return w, h, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return 0, 0, cerr
	}
	return 0, 0, capability.NewError(ErrNotReady, "the screen's size could not be read, so nothing was sent").
		WithCause("adb shell screencap: " + why).
		WithNextStep("Nothing was sent. Retry once the screen is on, or open the device's live view first.")
}

// readScreenHeader runs the header command; why is "" on success and the reason otherwise.
func (a *ADB) readScreenHeader(ctx context.Context, t *target) (width, height int, why string) {
	out, err := a.runShell(ctx, t, inputSizeTimeout, 256, "shell", screenHeaderCommand)
	switch {
	case err != nil:
		return 0, 0, err.Error()
	case out.ExitCode != 0:
		return 0, 0, fmt.Sprintf("exited with status %d", out.ExitCode)
	}
	f := strings.Fields(string(out.Stdout))
	if len(f) != 2 {
		return 0, 0, "the header was not two numbers"
	}
	w, werr := strconv.Atoi(f[0])
	h, herr := strconv.Atoi(f[1])
	if werr != nil || herr != nil || w < 1 || h < 1 || w > maxScreenDimension || h > maxScreenDimension {
		return 0, 0, "the header was not a screen size"
	}
	return w, h, ""
}

// fillScreen gives a tree read by `uiautomator dump` (no reader could run) the screen's size
// from the same on-device header the `input` delivery reads. A header that cannot be read
// leaves the tree without a screen (ADR-0133 §2); the read itself is not failed for it.
func (a *ADB) fillScreen(ctx context.Context, t *target, s *Snapshot) {
	if s.Screen != nil || ctx.Err() != nil {
		return
	}
	if w, h, why := a.readScreenHeader(ctx, t); why == "" {
		s.Screen = &UIScreen{Width: w, Height: h}
	}
}

// pixelOf maps a fraction of an axis of n pixels to the nearest pixel, the last one at most
// (screen.EncodeControl's rule).
func pixelOf(v float64, n int) int {
	return int(min(math.Round(v*float64(n)), float64(n-1)))
}

package device

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The soft keyboard's touchable region covers the bottom of the display while an input field
// is focused, and a tap inside it lands on a key, not on the target under it (plan m1n step
// 5, fix round L1, seen at level 5 on API 31: after typing into one field the IME window's
// touchable region was y 1512–2208, and the next call's tap at y 1579 typed a character into
// the field it was meant to leave). ui-automation.md §Typing records the rule these helpers
// implement.

const (
	// inputMethodReadBound bounds the `dumpsys input_method` read the guard makes, kept short
	// so the check stays inside the before phase (plan m1n step 5, fix round L1).
	inputMethodReadBound = 2 * time.Second
	// inputMethodReadCap bounds that read's output, far above the `mInputShown` line.
	inputMethodReadCap = 1 << 20
	// imeWindowsReadCap bounds the `dumpsys window windows` read; that dump is large, and its
	// IME window block is far larger than the region line.
	imeWindowsReadCap = 4 << 20
)

// softKeyboardWarning is the warning a call carries when the soft keyboard covered the target
// and was hidden before the tap (plan m1n step 5, fix round L1).
const softKeyboardWarning = "The soft keyboard covered the target; it was hidden with BACK before the tap."

// hasFocusedEditable reports whether any node in s is both focused and editable: the shape of
// a soft keyboard being up for a field (plan m1n step 5, fix round L1). Only the reader
// writes the editable flag, so on the `uiautomator dump` path this is always false and the
// guard never runs.
func hasFocusedEditable(s Snapshot) bool {
	for _, n := range s.Nodes {
		if slices.Contains(n.Flags, "focused") && slices.Contains(n.Flags, "editable") {
			return true
		}
	}
	return false
}

// contains is the guard's "the point lies inside the keyboard" test; rect is the occlusion
// helper's, in the same package.
func (r rect) contains(p [2]int) bool {
	return p[0] >= r.l && p[0] < r.r && p[1] >= r.t && p[1] < r.b
}

// inputMethodShown reads whether the IME is shown, from `dumpsys input_method` bounded by
// inputMethodReadBound. ok is false when the read or the parse could not answer — the guard
// then behaves as it did before this fix (plan m1n step 5, fix round L1).
func (a *ADB) inputMethodShown(ctx context.Context, t *target) (shown, ok bool) {
	out, err := a.Run(ctx, t.adb, inputMethodReadBound, inputMethodReadCap, "-s", t.serial, "shell", "dumpsys", "input_method")
	if err != nil || out.ExitCode != 0 {
		return false, false
	}
	return parseInputMethodShown(string(out.Stdout))
}

// parseInputMethodShown reads the `mInputShown` line of `dumpsys input_method`: the value
// token after the first `mInputShown=`. ok is false when no such line is present, so an
// unparsed read cannot be mistaken for "not shown".
func parseInputMethodShown(out string) (shown, ok bool) {
	for _, ln := range strings.Split(out, "\n") {
		i := strings.Index(ln, "mInputShown=")
		if i < 0 {
			continue
		}
		v := ln[i+len("mInputShown="):]
		if j := strings.IndexAny(v, " \t\r"); j >= 0 {
			v = v[:j]
		}
		return v == "true", true
	}
	return false, false
}

// touchableRegionRE matches the IME window's touchable region line in `dumpsys window
// windows`, e.g. `touchable region=SkRegion((0,1512,1080,2208))`.
var touchableRegionRE = regexp.MustCompile(`touchable region=SkRegion\(\((-?\d+),(-?\d+),(-?\d+),(-?\d+)\)\)`)

// imeTouchableRegion reads the InputMethod window's touchable region from `dumpsys window
// windows`. ok is false when no InputMethod block or region line is found, and the guard then
// behaves as it did before this fix (plan m1n step 5, fix round L1).
func (a *ADB) imeTouchableRegion(ctx context.Context, t *target) (r rect, ok bool) {
	out, err := a.Run(ctx, t.adb, inputMethodReadBound, imeWindowsReadCap, "-s", t.serial, "shell", "dumpsys", "window", "windows")
	if err != nil || out.ExitCode != 0 {
		return rect{}, false
	}
	return parseIMETouchableRegion(string(out.Stdout))
}

// parseIMETouchableRegion walks the window blocks of `dumpsys window windows` and returns the
// first `touchable region=SkRegion(...)` line that falls inside a block whose header names
// the InputMethod window. A block begins at a `Window{...}` or `Window #N` header line; a
// region outside such a block is not the IME's.
func parseIMETouchableRegion(out string) (rect, bool) {
	inIME := false
	for _, ln := range strings.Split(out, "\n") {
		t := strings.TrimSpace(ln)
		if isWindowHeader(t) {
			inIME = isInputMethodWindow(t)
			continue
		}
		if !inIME {
			continue
		}
		if m := touchableRegionRE.FindStringSubmatch(t); m != nil {
			var b [4]int
			for i := 0; i < 4; i++ {
				b[i], _ = strconv.Atoi(m[i+1])
			}
			return rectOf(b), true
		}
	}
	return rect{}, false
}

func isWindowHeader(t string) bool {
	return strings.HasPrefix(t, "Window{") || strings.HasPrefix(t, "Window #")
}

func isInputMethodWindow(header string) bool {
	return strings.Contains(header, "InputMethod") || strings.Contains(header, "input_method")
}

// uncoverSoftKeyboard hides the soft keyboard when it covers point (plan m1n step 5, fix
// round L1; ui-automation.md §Typing). It does nothing unless the before tree shows a focused
// editable node and `dumpsys input_method` says the keyboard is shown and the IME window's
// touchable region contains the point; when the region cannot be read it behaves as it did
// before this fix. It returns hid true when the keyboard was hidden with one BACK, and the
// caller then re-reads the tree, re-resolves the target and re-chooses the point. An error is
// only the caller's cancellation; a BACK that did not report success is a warning, because
// whether the target is still covered is decided by the re-read, not by BACK's exit status.
func (a *ADB) uncoverSoftKeyboard(ctx context.Context, t *target, s Snapshot, point [2]int) (warning string, hid bool, err error) {
	if !hasFocusedEditable(s) {
		return "", false, nil
	}
	shown, ok := a.inputMethodShown(ctx, t)
	if !ok || !shown {
		return "", false, nil
	}
	region, ok := a.imeTouchableRegion(ctx, t)
	if !ok || !region.contains(point) {
		return "", false, nil
	}
	w, err := a.hideSoftKeyboard(ctx, t)
	if err != nil {
		return "", false, err
	}
	return w, true, nil
}

// softKeyboardStillCovered reports whether point is still inside the IME's touchable region
// after the keyboard was hidden (plan m1n step 5, fix round L1). A read that cannot answer, or
// a keyboard `dumpsys input_method` now reports as not shown, is treated as not covered, so a
// read failure never fails a call that the earlier steps allowed.
func (a *ADB) softKeyboardStillCovered(ctx context.Context, t *target, point [2]int) (bool, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return false, err
		}
		return false, nil
	}
	if shown, ok := a.inputMethodShown(ctx, t); ok && !shown {
		return false, nil
	}
	region, ok := a.imeTouchableRegion(ctx, t)
	if !ok {
		return false, nil
	}
	return region.contains(point), nil
}

// hideSoftKeyboard sends one BACK key event (plan m1n step 5, fix round L1). A failure after
// the command was issued is a warning, not an error: the re-read decides whether the target
// is still covered. Only a caller's cancellation is an error.
func (a *ADB) hideSoftKeyboard(ctx context.Context, t *target) (string, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "The soft keyboard could not be hidden: the before phase's budget had already elapsed before BACK was issued, so the target may still be covered.", nil
	}
	nonce := inputNonce()
	out, err := a.runShell(ctx, t, inputCommandTimeout, 1<<10, "shell", inputShellCommand(nonce, "input keyevent 4"))
	if err == nil && out.ExitCode == 0 {
		return "", nil
	}
	if err != nil && errors.Is(err, context.Canceled) {
		return "", err
	}
	return "The BACK key command that hides the soft keyboard did not report success, so the target may still be covered.", nil
}

// softKeyboardOffscreenError is the failure of a target the soft keyboard still covers after
// it was hidden (plan m1n step 5, fix round L1): the point cannot be tapped, so the call
// fails DEVICE_ELEMENT_OFFSCREEN rather than tapping a key. The next step names the keyboard,
// so the caller knows it is not a scroll or a covering view. what is "tapped" or "typed".
func softKeyboardOffscreenError(what string) *capability.Error {
	return capability.NewError(ErrElementOffscreen, "the soft keyboard covers the target, so it could not be "+what).
		WithCause("the target's tap point lies inside the soft keyboard's touchable region, and hiding the keyboard did not move it clear").
		WithNextStep("Dismiss the keyboard (a BACK key event, or send the field's text and press the keyboard's own done key), then retry: nothing was " + what + ".")
}

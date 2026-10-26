package device

import (
	"context"
	"errors"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// expect on device.tap, device.swipe and device.pressKey (plan m3g step 5; ADR-0133 §6): the
// act-then-verify engine of device.tapElement around a coordinate action, with its meaning
// and nothing of its resolution. There is no target to resolve, no soft-keyboard guard, no
// refusal behind a system dialog and no crash diagnosis: the caller aimed at a place, not an
// element, and says what it expects to see. Without expect there is no read at all.

// noInputObservationWarning says no read after the input observed the screen. It is the
// daemon's own words, so it may sit in ScreenInputResult.Warnings.
const noInputObservationWarning = "The screen could not be read after the input, so the effect was not observed and foreground.after is omitted; the input may still have landed, do not retry blindly."

// validateInputExpect rejects a malformed expect or timeoutMs with REQUEST_INVALID_PARAMS,
// before any command runs (tapElement's bounds).
func validateInputExpect(expect []Condition, timeoutMs *int) error {
	if len(expect) > maxConditions {
		return capability.Errorf(capability.ErrInvalidParams, "expect carries %d conditions, at most %d", len(expect), maxConditions)
	}
	for _, c := range expect {
		if err := c.validate(); err != nil {
			return err
		}
	}
	if timeoutMs != nil && (*timeoutMs < 1 || *timeoutMs > maxTapTimeoutMs) {
		return capability.Errorf(capability.ErrInvalidParams, "timeoutMs must be between 1 and %d, got %d", maxTapTimeoutMs, *timeoutMs)
	}
	return nil
}

// actOnInput delivers in and, when expect was given, verifies it. Without expect it is
// deliverInput: no read, no dump. With it, the sequence is
//
//	ready, a before read, pin the conditions' refs, flag those already true (vacuous),
//	deliverInput, verifyAfter from the moment the input was issued.
//
// Every read and every delivery takes the device's reader lock for itself and releases it
// before the next step starts: the before read has returned (its lock released) before
// deliverInput's actor takes it for a gesture, and verifyAfter reads after the gesture has
// released it. Nothing here holds the lock across two steps, so none waits on another.
func (a *ADB) actOnInput(ctx context.Context, call *capability.Call, t *target, in screen.Input, expect []Condition, timeoutMs *int) (any, error) {
	if len(expect) == 0 {
		return a.deliverInput(ctx, call, t, in)
	}
	started := t.started
	if started.IsZero() {
		started = time.Now()
	}
	timeout := time.Duration(defaultTapTimeoutMs) * time.Millisecond
	if timeoutMs != nil {
		timeout = time.Duration(*timeoutMs) * time.Millisecond
	}
	marks := func(err error) error {
		if marksCall(err) {
			yes := true
			call.Untrusted = &yes
		}
		return err
	}

	// The hard running bound from the hook's start, and the before phase's own 14 s inside it
	// (the same budget as tapElement's).
	actx, cancel := context.WithDeadline(ctx, started.Add(actionRunningBound))
	defer cancel()
	bctx, cancelBefore := context.WithDeadline(actx, started.Add(actionBeforePhase))
	defer cancelBefore()

	if err := a.ready(bctx, t); err != nil {
		return nil, phaseError(ctx, err, ErrNotReady, "the device was not ready inside the 14 s before-phase budget; nothing was sent")
	}
	before, _, err := a.readSnapshot(bctx, t, true)
	if err != nil {
		return nil, marks(phaseError(ctx, err, ErrUIDumpFailed, "the UI could not be read inside the 14 s before-phase budget; nothing was sent"))
	}
	store := a.snapshots(t.serial)
	pinned, err := store.pinConditions(expect)
	if err != nil {
		return nil, marks(err)
	}
	store.keep(before)
	vacuous := make([]Condition, 0, len(pinned))
	for _, c := range pinned {
		holds, herr := c.holds(store, before)
		if herr != nil {
			return nil, marks(herr)
		}
		if holds {
			vacuous = append(vacuous, c.cond)
		}
	}
	cancelBefore()

	actionStart := time.Now()
	out, err := a.deliverInput(actx, call, t, in)
	if err != nil {
		return nil, err
	}
	res, ok := out.(ScreenInputResult)
	if !ok {
		return out, nil
	}

	vr := a.verifyAfter(actx, t, actionStart, &before, pinned, vacuous, timeout, false)
	// A cancellation after the input has landed ends the call completed, saying the effect was
	// not observed, not an error that invites a repeat (ui-automation.md §Result; D21).
	if errors.Is(ctx.Err(), context.Canceled) && vr.Expect != nil && !vr.Expect.Matched {
		vr.Expect.Reason = ReasonCancelled
	}
	res.Expect = vr.Expect
	res.Foreground = vr.Foreground
	if vr.Tree != nil {
		res.Snapshot = vr.Tree.ID
	} else {
		res.Warnings = append(res.Warnings, noInputObservationWarning)
	}
	return res, nil
}

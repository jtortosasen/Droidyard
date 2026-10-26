package device

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// device.tapElement (ui-automation.md §Acting; plan m1f step 6): one tap, at the centre
// of the target's visible bounds, then the effect verified in the same call. The
// act-then-verify engine here is reused by device.inputText (step 9).

// The bounds of an act-then-verify call (plan m1f, D13; ui-automation.md §Sequence and
// timing). Variables so a test injects a scale. The clock is the authorization hook's
// start, recorded in target.started, so a hook that spent seconds is charged to the same
// budget as the read and the resolve.
var (
	// actionBeforePhase bounds the hook, ready, the before read and the resolve.
	actionBeforePhase = 14 * time.Second
	// actionTapTimeout bounds one `input tap` command.
	actionTapTimeout = 2 * time.Second
	// actionPollInterval is the least time between two poll reads, start to start.
	actionPollInterval = 250 * time.Millisecond
	// actionRunningBound bounds the whole call from the hook's start (D13, 37 s). The
	// phases are below it by construction; it is the hard cap for a hung device.
	actionRunningBound = 37 * time.Second
)

// pollIdle is the persistent reader's own idle wait for the polls after an action's first
// read (D2; ui-automation.md §Sequence and timing). There is no fixed settle any more: the
// first read follows the action at once and the reader's own idle wait — the default
// 100/500 of a plain `dump` — is the settle. Each later poll asks for a shorter one
// (`dump idle 100 200`), so a screen that never goes quiet does not delay every poll by the
// full default wait. A var, not a literal in the call, so its address can be taken.
var pollIdle = readerIdle{quiet: 100, max: 200}

const (
	// defaultTapTimeoutMs and maxTapTimeoutMs are the poll window (ui-automation.md
	// §Sequence and timing).
	defaultTapTimeoutMs = 5000
	maxTapTimeoutMs     = 20000
	// maxConditions is the most conditions `expect` may carry.
	maxConditions = 5
	// maxUnmetCandidates is the near candidates a failing condition lists (D11, 5).
	maxUnmetCandidates = 5
	// ReasonNoObservation is `expect.reason` when every read after the action failed:
	// `matched: false` is still a completed call (ui-automation.md §Result).
	ReasonNoObservation = "noObservation"
	// ReasonCancelled is `expect.reason` when the caller cancelled after the tap: the tap
	// happened, so the call is completed and the result says the effect was not
	// observed; an error would invite a retry that taps twice (ui-automation.md §Result).
	ReasonCancelled = "cancelled"
	// noObservationWarning says a read after the tap never observed the screen, so
	// foreground.after is omitted and the result alone would look like a success
	// (plan m1f step 6, fix round).
	noObservationWarning = "The screen could not be read after the tap, so the effect was not observed and foreground.after is omitted; the tap may still have landed, do not retry blindly."
)

// TapParams is device.tapElement's parameters.
type TapParams struct {
	Device     string      `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Target     Selector    `json:"target" jsonschema:"required" jsonschema_description:"The node to tap. Resolved before anything is tapped; a resolution failure taps nothing."`
	Expect     []Condition `json:"expect,omitempty" jsonschema:"maxItems=5" jsonschema_description:"Conditions that must all hold after the tap; at most 5."`
	TimeoutMs  *int        `json:"timeoutMs,omitempty" jsonschema:"minimum=1,maximum=20000" jsonschema_description:"How long to poll for the conditions, in milliseconds (default 5000, maximum 20000)."`
	ReturnTree bool        `json:"returnTree,omitempty" jsonschema_description:"Include the last snapshot in the result, saving a device.uiTree call."`
}

// Tapped describes the node that was tapped and where. Its fields carry device text, so
// the whole object is untrusted (R4).
type Tapped struct {
	Ref string `json:"ref" jsonschema:"required" jsonschema_description:"The node's ref in the before snapshot."`
	// Snapshot is the before snapshot Ref belongs to: a ref resolves only with its own
	// snapshot (plan m1f step 6, fix round).
	Snapshot  string `json:"snapshot" jsonschema:"required" jsonschema_description:"The before snapshot tapped.ref belongs to; pass it back beside the ref."`
	MatchedBy string `json:"matchedBy" jsonschema:"required,enum=exact,enum=contains"`
	Class     string `json:"class,omitempty"`
	ID        string `json:"id,omitempty"`
	Text      string `json:"text,omitempty"`
	Desc      string `json:"desc,omitempty"`
	// Point is where the tap landed: the display-pixel centre of the node's visible
	// bounds, or, when a node drawn above covers that centre, the centre of the largest
	// visible strip (ui-automation.md §Resolution; plan m1f D22).
	Point             [2]int     `json:"point" jsonschema:"required"`
	Clickable         bool       `json:"clickable" jsonschema:"required" jsonschema_description:"Whether the node itself is clickable; when false the tap may have no effect."`
	ClickableAncestor *Candidate `json:"clickableAncestor,omitempty" jsonschema_description:"The nearest clickable ancestor, when the node itself is not clickable."`
}

// Unmet is one condition that did not hold, with what the last tree showed.
type Unmet struct {
	Condition  Condition   `json:"condition" jsonschema:"required"`
	Candidates []Candidate `json:"candidates,omitempty" jsonschema_description:"The still-present match for gone, or near candidates for appears; at most 5."`
}

// ExpectResult is the verification of one act-then-verify call
// (ui-automation.md §Result). `unmet` carries device text, so it is untrusted (R4).
type ExpectResult struct {
	Matched bool `json:"matched" jsonschema:"required" jsonschema_description:"True when every condition held; false is a completed call, not an error."`
	// AfterMs is the time from the action to the read that decided, in milliseconds.
	AfterMs     int64       `json:"afterMs" jsonschema:"required"`
	Polls       int         `json:"polls" jsonschema:"required" jsonschema_description:"Reads attempted after the action, failed ones included."`
	FailedDumps int         `json:"failedDumps" jsonschema:"required"`
	Reason      string      `json:"reason,omitempty" jsonschema_description:"noObservation when every read after the action failed; cancelled when the caller cancelled after the tap, so the tap is reported and must not be retried."`
	Vacuous     []Condition `json:"vacuous,omitempty" jsonschema_description:"Conditions that already held before the action; they prove nothing."`
	Unmet       []Unmet     `json:"unmet,omitempty" untrusted:"true"`
}

// Foreground is the top-window package before and after the action.
type Foreground struct {
	Before string `json:"before" jsonschema:"required"`
	// After is omitted when no read after the action observed the screen (plan m1f
	// step 6, fix round), so a schema consumer must not require it.
	After string `json:"after,omitempty" jsonschema_description:"The top window after the action; omitted when no read after the action observed the screen."`
}

// TapResult is device.tapElement's result. `tapped`, `foreground` and `tree` carry
// device text and are untrusted (R4).
type TapResult struct {
	Tapped     Tapped        `json:"tapped" jsonschema:"required" untrusted:"true"`
	Expect     *ExpectResult `json:"expect,omitempty"`
	Foreground *Foreground   `json:"foreground,omitempty" untrusted:"true"`
	Tree       *Snapshot     `json:"tree,omitempty" untrusted:"true"`
	// Snapshot names the last read whose refs unmet candidates carry, and, with
	// returnTree, tree. A ref resolves only with the snapshot it came from; the id is
	// the daemon's, so it is not device text (plan m1f step 6, fix round).
	Snapshot string `json:"snapshot,omitempty" jsonschema_description:"The snapshot the last read returned; unmet candidates' refs, and tree, belong to it."`
	// Warnings are adb's and the reader's, each gathered once (D17), and a moved tap
	// point's, which names the covering node by class and id (D22): untrusted.
	Warnings []string `json:"warnings,omitempty" untrusted:"true" jsonschema_description:"adb's and the reader's warnings, and a moved tap point naming the covering node; they can quote app identifiers, so they are untrusted."`
	// AppCrash is the crash this action diagnosed, when the foreground left the app and
	// its process was gone (ADR-0034 §2); SystemDialog is the system error dialog the
	// action's reads saw (ADR-0034 §3).
	AppCrash     *AppCrash     `json:"appCrash,omitempty"`
	SystemDialog *SystemDialog `json:"systemDialog,omitempty"`
}

// validate rejects a malformed tapElement call before any command runs.
func (p TapParams) validate() error {
	if err := p.Target.validate(); err != nil {
		return err
	}
	if len(p.Expect) > maxConditions {
		return capability.Errorf(capability.ErrInvalidParams, "expect carries %d conditions, at most %d", len(p.Expect), maxConditions)
	}
	for _, c := range p.Expect {
		if err := c.validate(); err != nil {
			return err
		}
	}
	if p.TimeoutMs != nil && (*p.TimeoutMs < 1 || *p.TimeoutMs > maxTapTimeoutMs) {
		return capability.Errorf(capability.ErrInvalidParams, "timeoutMs must be between 1 and %d, got %d", maxTapTimeoutMs, *p.TimeoutMs)
	}
	return nil
}

// writeKey is the per-device write lock one action holds for its whole call
// (ui-automation.md §Concurrency; plan m1f, R2).
func writeKey(serial string) string { return "device.write:" + serial }

// deviceTarget is the operation target of a capability that acts on one device
// (ADR-0076): the serial the caller gave, nil when it gave none.
func deviceTarget(serial string) *capability.OpTarget {
	if serial == "" {
		return nil
	}
	return &capability.OpTarget{Device: serial}
}

// WriteKey is writeKey, exported so emulator.stop shares the exact key a device.launch
// or an install on the same serial holds, instead of a copy that could drift (plan m1i,
// D12: the key serialises a stop with the writes to that device and lets it end a stuck
// boot).
func WriteKey(serial string) string { return writeKey(serial) }

func registerTapElement(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.tapElement",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        "0.5.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Taps one UI element once and verifies the effect in the same call: it resolves target with the device.uiTree selector, taps it, then polls the accessibility tree until every expect condition holds or timeoutMs elapses. An unmet expectation is a completed call (matched false, with candidates), not an error, and a resolution failure taps nothing. Never repeat it: a repeat taps again; an expect already true before the tap is reported in vacuous, and gone needs two reads.",
		Params:       TapParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(TapParams).Device) },
		Result:       TapResult{},
		Occupies:     capability.OccupiesAct,
		Text:         capability.TextOf(tapText),
		Errors: append(append([]capability.ErrorCode{}, ActingErrors...),
			ErrUIDumpFailed, ErrElementNotFound, ErrElementAmbiguous, ErrElementOffscreen, ErrElementDisabled, ErrStaleRef,
			capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
		Resource: func(call *capability.Call) string {
			if s := call.Params.(TapParams).Device; s != "" {
				return writeKey(s)
			}
			return ""
		},
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "its effect belongs to the app, and the policy keeps agents on emulators by default (mcp.md §Annotations)",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			return adb.authorizeActing(ctx, call, call.Params.(TapParams).Device, "device.tapElement")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(TapParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			t := call.Authorized.(*target)
			res, err := adb.tapElement(ctx, t, p)
			if err != nil {
				if marksCall(err) {
					yes := true
					call.Untrusted = &yes
				}
				return nil, err
			}
			return *res, nil
		},
	}))
}

// tapElement runs the act-then-verify sequence of ui-automation.md §Sequence and
// timing. It returns an error only when nothing was tapped (or the caller cancelled);
// after the tap every failure is folded into the result.
func (a *ADB) tapElement(ctx context.Context, t *target, p TapParams) (*TapResult, error) {
	started := t.started
	if started.IsZero() {
		started = time.Now()
	}
	timeout := time.Duration(defaultTapTimeoutMs) * time.Millisecond
	if p.TimeoutMs != nil {
		timeout = time.Duration(*p.TimeoutMs) * time.Millisecond
	}

	// The hard running bound, counted from the hook's start (D13).
	actx, cancel := context.WithDeadline(ctx, started.Add(actionRunningBound))
	defer cancel()

	// Before phase: ready, the before read, the resolve. Its own sub-bound is D13's
	// 14 s from the hook's start, so a hook that spent seconds leaves the read less.
	bctx, cancelBefore := context.WithDeadline(actx, started.Add(actionBeforePhase))
	defer cancelBefore()

	if err := a.ready(bctx, t); err != nil {
		return nil, phaseError(ctx, err, ErrNotReady, "the device was not ready inside the 14 s before-phase budget; nothing was tapped")
	}
	before, beforeWarnings, err := a.readSnapshot(bctx, t, true)
	if err != nil {
		return nil, phaseError(ctx, err, ErrUIDumpFailed, "the UI could not be read inside the 14 s before-phase budget; nothing was tapped")
	}
	store := a.snapshots(t.serial)

	// The system error dialog, when the before tree shows one (ADR-0034 §3; plan m1n step
	// 6): a target inside it is acted on as usual, a target behind it is refused before the
	// tap. The detection runs on the before read only, so the tap it gates is what the
	// dialog can stop. A dialog seen in the before read appeared before the action began, so
	// the events read that names it looks the whole standalone window back, not from the
	// action's start.
	beforeDialog := a.systemDialogForRead(bctx, t, &before, time.Time{}, systemDialogWindow)

	// Resolve the target once, before anything is tapped, and choose the point around
	// occlusion: a centre covered by a node drawn above the target moves to the largest
	// visible strip, or refuses off-screen (ui-automation.md §Resolution; plan m1f D22).
	m, err := store.resolve(before, p.Target)
	if err != nil {
		if beforeDialog != nil && behindTheDialog(err) {
			// The dialog is the active window's root, so a target that is not found at all is
			// behind it: nothing is tapped (ADR-0034 §3). An ambiguous, disabled or off-screen
			// target describes the target itself and is passed through as itself.
			return nil, systemDialogRefusal(beforeDialog)
		}
		return nil, err
	}
	oc, err := chooseTapPoint(before, m.Node)
	if err != nil {
		return nil, err
	}
	warnings := append([]string{}, t.warnings...)

	// The soft keyboard can cover the target, so a tap would land on a key, not the field
	// (plan m1n step 5, fix round L1). When the before tree shows a focused editable node and
	// the IME's touchable region contains the point, hide the keyboard with BACK, re-read, and
	// re-resolve the same selector against the fresh tree, so the tap lands on the field.
	kbWarn, hid, err := a.uncoverSoftKeyboard(bctx, t, before, oc.Point)
	if err != nil {
		return nil, err
	}
	if hid {
		if kbWarn != "" {
			warnings = append(warnings, kbWarn)
		}
		after, aw, rerr := a.readSnapshot(bctx, t, true)
		if rerr != nil {
			return nil, afterKeyboardBack(phaseError(ctx, rerr, ErrUIDumpFailed, "the UI could not be re-read after the soft keyboard was hidden inside the 14 s before-phase budget"), "tapped")
		}
		m, err = store.resolve(after, p.Target)
		if err != nil {
			return nil, afterKeyboardBack(err, "tapped")
		}
		oc, err = chooseTapPoint(after, m.Node)
		if err != nil {
			return nil, afterKeyboardBack(err, "tapped")
		}
		if covered, cerr := a.softKeyboardStillCovered(bctx, t, oc.Point); cerr != nil {
			return nil, cerr
		} else if covered {
			return nil, softKeyboardOffscreenError("tapped")
		}
		before = after
		beforeWarnings = append(beforeWarnings, aw...)
		warnings = append(warnings, softKeyboardWarning)
	}

	// Pin every condition's ref to its node's identity before the before read is kept:
	// keeping it must not evict the snapshot a ref points to, so a ref from the oldest kept
	// snapshot still resolves (plan m1f step 6, fix round). Pinning once, before anything is
	// tapped, also means polling cannot evict the snapshot the ref came from and an unknown or
	// evicted snapshot fails DEVICE_STALE_REF here, with nothing tapped.
	pinned, err := store.pinConditions(p.Expect)
	if err != nil {
		return nil, err
	}
	store.keep(before)

	// A condition already true before the tap is flagged, not refused.
	vacuous := make([]Condition, 0, len(pinned))
	for _, c := range pinned {
		holds, herr := c.holds(store, before)
		if herr != nil {
			return nil, herr
		}
		if holds {
			vacuous = append(vacuous, c.cond)
		}
	}

	// Tap once, never retried (ui-automation.md §Result; D18).
	actionStart := time.Now()
	if oc.Moved && oc.Covering != nil {
		warnings = append(warnings, tapMovedWarning(*oc.Covering))
	}
	w, tapErr := a.tapAt(actx, t, oc.Point)
	if tapErr != nil {
		return nil, tapErr
	}
	if w != "" {
		warnings = append(warnings, w)
	}

	// No fixed settle (D2): the first read starts right after the tap and the reader's own
	// idle wait is the settle. A first read the deadline cuts still returns a result, so a
	// retry is never invited.
	vr := a.verifyAfter(actx, t, actionStart, &before, pinned, vacuous, timeout, false)
	// A cancellation that arrives after the tap has landed ends the call completed: the
	// tap happened, so the result carries it and, when expect was given, says the effect
	// was not observed rather than raising an error that invites a retry that taps twice
	// (ui-automation.md §Result; plan m1f step 7, D21).
	if errors.Is(ctx.Err(), context.Canceled) && vr.Expect != nil && !vr.Expect.Matched {
		vr.Expect.Reason = ReasonCancelled
	}
	warnings = append(warnings, beforeWarnings...)
	warnings = append(warnings, vr.Warnings...)

	// The later reads name a system dialog too (ADR-0034 §3): the last read's tree, the
	// one the result returns. The before read's dialog wins when both carry one, because it
	// is the state the call acted against — and when the before read already named one, the
	// later read is not looked at again: the dialog the call acted against is the one it
	// names and the extra events read is not paid.
	systemDialog := beforeDialog
	if systemDialog == nil {
		systemDialog = a.systemDialogForRead(actx, t, vr.Tree, started, systemDialogWindow)
	}

	// The crash, when the tap left the app and its process is gone (ADR-0034 §2; plan m1n
	// step 6): the crash buffer since the tap, else exit-info. Paid only on that path, and
	// never for the system ui's own window or when the call acted on a system dialog.
	var appCrash *AppCrash
	if crashApplies(before.Foreground, beforeDialog, vr.Foreground) && (crashDialogFor(systemDialog, before.Foreground) || vr.LastBusy || a.appPIDof(actx, t, before.Foreground) == 0) {
		appCrash = a.appCrashReason(actx, t, before.Foreground, actionStart)
	}

	res := &TapResult{
		Tapped:       tappedOf(before, m, oc.Point),
		Expect:       vr.Expect,
		Foreground:   vr.Foreground,
		AppCrash:     appCrash,
		SystemDialog: systemDialog,
		Warnings:     dedupWarnings(warnings),
	}
	if p.ReturnTree {
		res.Tree = vr.Tree
	}
	if vr.Tree != nil {
		// The refs in unmet candidates and in tree belong to the last read; name it
		// beside them so a caller can use those refs (plan m1f step 6, fix round).
		res.Snapshot = vr.Tree.ID
	}
	return res, nil
}

// readSnapshot reads one tree through the given chain, returning it unkept: the caller
// keeps it once every ref that resolves against it has been read, so keeping the before
// read cannot evict the snapshot a ref points to (plan m1f step 6, fix round).
// allowDump false is the D17 post-action read.
func (a *ADB) readSnapshot(ctx context.Context, t *target, allowDump bool) (Snapshot, []string, error) {
	var doc []byte
	var warnings []string
	var err error
	if allowDump {
		doc, warnings, err = a.read(ctx, t)
	} else {
		doc, warnings, err = a.readNoDump(ctx, t)
	}
	if err != nil {
		return Snapshot{}, nil, err
	}
	s, err := buildSnapshot(doc, NewSnapshotID(), DefaultUITreeBytes, nil)
	if err != nil {
		return Snapshot{}, nil, dumpFailed(err.Error())
	}
	return s, warnings, nil
}

// inputTap issues `adb shell input tap x y` once. It returns a warning when the command
// did not provably fail before it ran: a timeout or a non-zero exit still may have
// landed the tap, and an error would invite a second one (D18). It returns an error
// only when the tap provably never ran — adb could not be started, or adb says the
// device is gone before the shell ran.
func (a *ADB) inputTap(ctx context.Context, t *target, point [2]int) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, actionTapTimeout)
	defer cancel()
	out, err := a.Run(tctx, t.adb, actionTapTimeout, 1<<10, "-s", t.serial, "shell", "input", "tap", strconv.Itoa(point[0]), strconv.Itoa(point[1]))
	if cerr := ctx.Err(); cerr != nil {
		// The command was issued, so the tap may have landed whether the call was
		// cancelled or its running bound elapsed: a warning, never an error, because an
		// error would invite a second tap (D18, D21).
		if errors.Is(cerr, context.Canceled) {
			return tapWarning("the call was cancelled while the tap command was running"), nil
		}
		return tapWarning("the call's running bound elapsed while the tap command was running"), nil
	}
	if errors.Is(err, ErrNoStarter) || (err != nil && strings.HasPrefix(err.Error(), "starting adb")) {
		return "", capability.NewError(ErrADBFailed, "adb could not be run").
			WithCause("adb shell input tap: " + err.Error()).
			WithNextStep("Check that the adb binary is executable, then retry: nothing was tapped.")
	}
	if line := disconnectedLine(t.serial, out); line != "" {
		return "", disconnected(t.serial, line)
	}
	switch {
	case err == nil && out.ExitCode == 0:
		return "", nil
	case errors.Is(err, ErrTimeout):
		return tapWarning("it timed out"), nil
	case err != nil:
		return tapWarning(err.Error()), nil
	default:
		return tapWarning(fmt.Sprintf("it exited with status %d", out.ExitCode)), nil
	}
}

// tapWarning is the warning of a tap command whose outcome is unknown: it may have
// landed, so verification goes on and the caller must not retry blindly (D18).
func tapWarning(why string) string {
	return "The tap command did not report success (" + why + "); verification went on because the tap may have landed, and retrying the call could tap twice."
}

// actor acts on one device through its persistent reader when the reader can run (ADR-0035
// §3; plan m1n step 5). It is built by newActor, which pings the reader once: an answered
// ping means acting requests go through the reader, and one that is not answered means the
// non-reader path answers them. The ping's purpose is to keep an act from being sent to a
// reader that is not there, where the failure would have to be reported as a request that
// may have applied (plan m1n step 4, fix round N2).
type actor struct {
	a  *ADB
	t  *target
	c  *readerClient
	ok bool
}

// newActor decides whether this device's acts go through the persistent reader. It pings
// the reader once, bounded by the caller's running budget, and never fails: !ok is "the
// reader cannot run", which is an answer to act on, not an error (ADR-0035 §3).
func (a *ADB) newActor(parent context.Context, t *target) *actor {
	end := time.Now().Add(dumpDeadline)
	if d, ok := parent.Deadline(); ok && d.Before(end) {
		end = d
	}
	c := a.reader(t.serial)
	return &actor{a: a, t: t, c: c, ok: a.readerReady(parent, t, c, end)}
}

// locked runs one acting request with the device's read lock held, which actOnReader
// requires (ADR-0035 §1): the lock and the reader's own single acting thread are the two
// layers that keep one device's acts from overlapping.
func (ac *actor) locked(ctx context.Context, fn func(ctx context.Context, c *readerClient) (readerAct, error)) (readerAct, error) {
	if err := ac.c.acquire(ctx); err != nil {
		return readerAct{}, err
	}
	defer ac.c.release()
	return fn(ctx, ac.c)
}

// tap clicks at a display point through the reader (ADR-0035 §1: `tap <x> <y>`).
func (ac *actor) tap(ctx context.Context, point [2]int) (readerAct, error) {
	return ac.locked(ctx, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return ac.a.actTap(ctx, ac.t, c, point[0], point[1])
	})
}

// gesture injects a touch path through the reader (plan m3g step 2: `gesture <n> <x> <y>
// <t> ...`).
func (ac *actor) gesture(ctx context.Context, points []pathPoint) (readerAct, error) {
	return ac.locked(ctx, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return ac.a.actGesture(ctx, ac.t, c, points)
	})
}

// press injects a key down and up through the reader (plan m3g step 2: `press <keycode>`).
func (ac *actor) press(ctx context.Context, code int) (readerAct, error) {
	return ac.locked(ctx, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return ac.a.actPress(ctx, ac.t, c, code)
	})
}

// keys types text as the reader's virtual keyboard's key events (ADR-0035 §1: `keys
// <base64>`).
func (ac *actor) keys(ctx context.Context, text string) (readerAct, error) {
	return ac.locked(ctx, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return ac.a.actKeys(ctx, ac.t, c, text)
	})
}

// setText sets the focused field's text through the reader (ADR-0035 §1: `settext
// <replace|insert> <base64>`).
func (ac *actor) setText(ctx context.Context, replace bool, text string) (readerAct, error) {
	return ac.locked(ctx, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return ac.a.actSetText(ctx, ac.t, c, replace, text)
	})
}

// actingBusyError is the failure of an acting request the reader answered `busy` (ADR-0035
// §3): the device's single acting thread was still out on an earlier request, so this one
// executed nothing. The call fails before acting, so a pending effect is never followed by
// a second delivery across calls. what is "typed" or "tapped".
func actingBusyError(what string) *capability.Error {
	return capability.NewError(ErrNotReady, "the reader was still acting on an earlier request, so nothing was "+what).
		WithCause("the reader's single acting thread was busy").
		WithNextStep("Read the field or screen before acting again: an earlier effect may still be pending, so do not repeat this call until a read shows the device has settled.")
}

// actVerb is the infinitive of what ("typed"/"tapped") for a refusal's cause sentence:
// "the reader refused to type: no field had focus" reads, where "the reader refused the
// typed because no field had focus" did not (plan m1n step 8, main-session note). what is
// one of "typed", "tapped", "gestured" or "pressed" (the last two from plan m3g step 2).
func actVerb(what string) string {
	switch what {
	case "tapped":
		return "tap"
	case "gestured":
		return "gesture"
	case "pressed":
		return "press"
	}
	return "type"
}

// actRefusedError is the failure of an acting request the reader refused before executing
// anything, when no other method answers the refusal (ADR-0035 §3): only a `denied` and a
// refused ACTION_SET_TEXT lead to another method, so anything else here has nothing left to
// try. It is a distinctive answer, not a may-have-applied one: the reader said it executed
// nothing, so a retry is safe. what is "typed" or "tapped".
//
// `malformed` and `not properly terminated` are the reader failing to parse the daemon's own
// request line, and `too long` is the request line over the reader's cap: none of the three
// can succeed on a retry of the same request, so their next step does not invite one (plan m1n
// step 5, fix round N3). A typing `too long` is mapped to DEVICE_TEXT_NOT_DELIVERABLE by the
// caller, where the text is the caller's data; a tap's `too long` cannot happen (the request
// is `tap x y`), so this stays DEVICE_NOT_READY for it.
func actRefusedError(act readerAct, what string) *capability.Error {
	switch act.refusal {
	case refusalMalformed, refusalUnterminated:
		return capability.NewError(ErrNotReady, "the reader could not parse the request, so nothing was "+what).
			WithCause("the reader refused the request because " + refusalPhrase(act.refusal) + ".").
			WithNextStep("This is the daemon's request, not the call's; retrying the same call cannot help. Nothing was " + what + ".")
	case refusalTooLong:
		return capability.NewError(ErrNotReady, "the request was over the reader's length cap, so nothing was "+what).
			WithCause("the reader refused the request because " + refusalPhrase(act.refusal) + ".").
			WithNextStep("Send a shorter request; retrying the same call cannot help. Nothing was " + what + ".")
	}
	return capability.NewError(ErrNotReady, "the reader refused the request before executing it, so nothing was "+what).
		WithCause("the reader refused to " + actVerb(what) + ": " + refusalPhrase(act.refusal) + ".").
		WithNextStep("Read the field or screen, then retry: nothing was " + what + ", so this call is safe to repeat.")
}

// tapAt taps once at a display point (ADR-0035 §3; plan m1n step 5): through the persistent
// reader when it can run, and `input tap` when it cannot.
func (a *ADB) tapAt(ctx context.Context, t *target, point [2]int) (string, error) {
	w, _, _, err := a.tapAtWith(ctx, t, a.newActor(ctx, t), point)
	return w, err
}

// tapAtWith taps once through an actor the caller already built, so a call that acts more
// than once pings the reader once (device.inputText taps to focus and then types). It
// returns a warning when the tap did not definitively fail before it ran — it may have
// landed, so an error would invite a second tap (D18) — and an error only when the tap
// provably never ran. reader says whether the request went through the reader rather than
// `input tap`; unanswered says a request the reader took never came back, so the caller must
// treat an effect that may have applied as one that did (ADR-0020's 2026-10-06 line). A tap
// the reader took but never answered is never repeated through `input tap`, which could tap
// twice. A tap the reader refused before executing it, and an acting thread that was busy,
// are errors: nothing was tapped.
func (a *ADB) tapAtWith(ctx context.Context, t *target, ac *actor, point [2]int) (warn string, reader, unanswered bool, err error) {
	if !ac.ok {
		w, ierr := a.inputTap(ctx, t, point)
		return w, false, false, ierr
	}
	act, aerr := ac.tap(ctx, point)
	if aerr != nil {
		return a.tapFallback(ctx, t, point, aerr)
	}
	switch act.kind {
	case readerActDone:
		return "", true, false, nil
	case readerActBusy:
		return "", true, false, actingBusyError("tapped")
	case readerActRefused:
		return "", true, false, actRefusedError(act, "tapped")
	default:
		// The reader took the tap and never answered: it may have landed, so the call goes on
		// to verify it, and no second tap is issued (ADR-0035 §3; ADR-0020's 2026-10-06 line).
		return tapWarning("the reader took the tap but never answered"), true, true, nil
	}
}

// tapFallback decides what an acting request's error means for a tap: `input tap` answers a
// reader that could not run or denied the request (nothing was executed, ADR-0035 §3), while
// a caller's cancellation, or a budget that had already elapsed, cannot be answered by
// another tap at all. A tap is never repeated on a request the reader took.
func (a *ADB) tapFallback(ctx context.Context, t *target, point [2]int, err error) (string, bool, bool, error) {
	if cerr := ctx.Err(); cerr != nil {
		if errors.Is(cerr, context.Canceled) {
			return "", false, false, cerr
		}
		return tapWarning("the call's running bound elapsed before the tap command was sent"), false, false, nil
	}
	if errors.Is(err, context.Canceled) {
		return "", false, false, err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return tapWarning("the tap command's budget elapsed before it was sent"), false, false, nil
	}
	// The reader could not run (unreachable, no forward) or denied the request: nothing was
	// executed, so the non-reader path is allowed (ADR-0035 §3).
	w, ierr := a.inputTap(ctx, t, point)
	return w, false, false, ierr
}

// waitFocus polls reads after the focusing tap until a focused node at the tapped point
// appears, bounded by ctx (the before phase), and reports whether it saw one (plan m1n step
// 5). It replaces the fixed settle: a set-text acts on the focused input node, and a Compose
// field renders a frame later, so the field must be seen focused before the text is delivered
// (plan m1n step 2 (e)). A focused node counts when its bounds contain the tapped point, so
// the field is recognised wherever the tap landed on it: the earlier test of the exact class
// and bounds of the before read failed a Compose field whose bounds the tap's own focus moved
// (plan m1n step 5, fix round N5). A false answer is not an error: the caller delivers anyway
// (and warns when the tap it could not confirm left the field unfocused).
func (a *ADB) waitFocus(ctx context.Context, t *target, point [2]int) bool {
	for {
		s, _, err := a.readSnapshot(ctx, t, true)
		if err == nil {
			for _, n := range s.Nodes {
				if slices.Contains(n.Flags, "focused") && pointInBounds(n.Bounds, point) {
					return true
				}
			}
		}
		if !sleepCtx(ctx, actionPollInterval) {
			return false
		}
	}
}

// pointInBounds reports whether p lies inside b, half-open on the right and bottom edges, so
// the test matches the reader's convention for a point at an edge.
func pointInBounds(b [4]int, p [2]int) bool {
	return p[0] >= b[0] && p[0] < b[2] && p[1] >= b[1] && p[1] < b[3]
}

// phaseError turns a before-phase failure into the stage's code when the phase's own
// budget cut it; a caller's cancellation or a typed error passes through.
func phaseError(outer context.Context, err error, code capability.ErrorCode, message string) error {
	if outer.Err() != nil {
		return outer.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return capability.NewError(code, message).
			WithCause("the before phase's budget elapsed").
			WithNextStep("Retry device.tapElement: nothing on the device was changed.")
	}
	return err
}

// marksCall reports whether a failed action error carries device text in its details, so
// the handler marks the failed call (R5; plan m1f, D5). A busy-app read is one such
// error: its Details carry the app's package (ADR-0034 §1).
func marksCall(err error) bool {
	e := capability.AsError(err)
	if e == nil {
		return false
	}
	switch e.Code {
	case ErrElementNotFound, ErrElementAmbiguous, ErrElementOffscreen, ErrElementDisabled, ErrStaleRef:
		return true
	}
	return busyApp(err) || dialogRefusal(err)
}

// tapPoint is the display-pixel centre of a node's visible bounds: the bounds
// intersected with the display (ui-automation.md §Resolution).
func tapPoint(n UINode, d UIDisplay) [2]int {
	l, top, r, b := n.Bounds[0], n.Bounds[1], n.Bounds[2], n.Bounds[3]
	if d.Width > 0 && r > d.Width {
		r = d.Width
	}
	if d.Height > 0 && b > d.Height {
		b = d.Height
	}
	if l < 0 {
		l = 0
	}
	if top < 0 {
		top = 0
	}
	return [2]int{(l + r) / 2, (top + b) / 2}
}

// tappedOf renders the resolved node and the point the tap landed at.
func tappedOf(s Snapshot, m Matched, point [2]int) Tapped {
	t := Tapped{
		Ref: m.Node.Ref, Snapshot: s.ID, MatchedBy: m.MatchedBy, Class: m.Node.Class, ID: m.Node.ID,
		Text: m.Node.Text, Desc: m.Node.Desc, Point: point,
		Clickable: slices.Contains(m.Node.Flags, "clickable"),
	}
	if !t.Clickable {
		t.ClickableAncestor = clickableAncestor(s, m.Node)
	}
	return t
}

// clickableAncestor is the nearest ancestor flagged clickable, nil when the node itself
// is clickable or none is (ui-automation.md §Resolution).
func clickableAncestor(s Snapshot, n UINode) *Candidate {
	ti := indexTree(s)
	for p := ti.parent[n.Ref]; p != ""; p = ti.parent[p] {
		if a, ok := ti.node(s, p); ok && slices.Contains(a.Flags, "clickable") {
			c := candidateOf(a)
			return &c
		}
	}
	return nil
}

// verifyResult is what one verification pass found.
type verifyResult struct {
	Expect     *ExpectResult
	Tree       *Snapshot
	Foreground *Foreground
	Warnings   []string
	// LastBusy says the last read after the action was the busy answer (ADR-0034 §1): the
	// app did not answer, which is reason enough to look for its crash even while its
	// process lives — the "keeps stopping" dialog holds a crashed process alive, and a busy
	// read cannot show the dialog (level 5, plan m1n step 9).
	LastBusy bool
}

// verifyAfter polls reads after an action until every condition holds or the window
// ends (ui-automation.md §Sequence and timing). It never returns an error: a failed
// read counts in failedDumps, and every failed read leaves reason noObservation.
// allowDump is false for a post-tap read (D17). before may be nil when the caller took
// no before read. `pinned` carries each condition with its ref, if any, already
// resolved. The first read runs to the running bound, not to timeoutMs, so a small
// timeoutMs cannot make a one-shot fallback unobservable; timeoutMs bounds the later
// polls (plan m1f step 6, fix round).
func (a *ADB) verifyAfter(ctx context.Context, t *target, actionStart time.Time, before *Snapshot, pinned []pinnedCondition, vacuous []Condition, timeout time.Duration, allowDump bool) verifyResult {
	st := a.snapshots(t.serial)
	deadline := time.Now().Add(timeout)
	streaks := make([]int, len(pinned))
	var last *Snapshot
	var lastStart time.Time
	var warnings []string
	polls, failed := 0, 0
	matched := len(pinned) == 0
	lastBusy := false

	for polls == 0 || (!matched && time.Now().Before(deadline)) {
		if !lastStart.IsZero() {
			if gap := actionPollInterval - time.Since(lastStart); gap > 0 && !sleepCtx(ctx, gap) {
				break
			}
		}
		lastStart = time.Now()
		// The first read after the action runs to the running bound; timeoutMs bounds
		// the later polls (plan m1f step 6, fix round).
		rctx, readCancel := ctx, context.CancelFunc(func() {})
		if polls > 0 {
			rctx, readCancel = context.WithDeadline(ctx, deadline)
		}
		// The first read keeps the reader's default idle wait — the settle that replaces the
		// old fixed one (D2) — and each later poll asks for the shorter pollIdle, so a screen
		// that never goes quiet does not delay every poll by the full default.
		var idle *readerIdle
		if polls > 0 {
			idle = &pollIdle
		}
		doc, w, err := a.readChain(rctx, t, allowDump, idle)
		readCancel()
		polls++
		warnings = append(warnings, w...)
		lastBusy = err != nil && isAppBusy(err)
		if err != nil {
			failed++
			resetStreaks(streaks)
			if ctx.Err() != nil {
				break // the running bound or the caller ended: no observation will come
			}
			// A busy read whose app's process is gone is a crash, not a slow screen: polling
			// on would only meet more busy answers until timeoutMs (level 5, plan m1n step 9:
			// a crashing tap took 5.4 s). Stop, so the caller's crash path reports it now.
			if isAppBusy(err) && before != nil && before.Foreground != "" && before.Foreground != androidPackage &&
				a.appPIDof(ctx, t, before.Foreground) == 0 {
				break
			}
			continue
		}
		s, berr := buildSnapshot(doc, NewSnapshotID(), DefaultUITreeBytes, nil)
		if berr != nil {
			failed++
			resetStreaks(streaks)
			continue
		}
		snapshot := s
		last = &snapshot
		matched = st.evaluatePinned(snapshot, pinned, streaks)
	}

	// Keep only the snapshots whose refs the result returns: the before read (already
	// kept, for tapped.ref) and the last read here (for unmet candidates and tree),
	// never each poll. A caller's earlier snapshot is therefore not evicted by polling
	// (plan m1f step 6, fix round).
	if last != nil {
		st.keep(*last)
	}

	// With no expect there is nothing to verify: the result omits expect rather than
	// reporting a matched it did not check (plan m1f step 6, fix round).
	var verdict *ExpectResult
	if len(pinned) > 0 {
		verdict = &ExpectResult{
			Matched:     matched,
			AfterMs:     time.Since(actionStart).Milliseconds(),
			Polls:       polls,
			FailedDumps: failed,
			Vacuous:     vacuous,
		}
		if polls > 0 && failed == polls {
			verdict.Matched = false
			verdict.Reason = ReasonNoObservation
		}
		for i, p := range pinned {
			if matched {
				break
			}
			held := last != nil && st.conditionHeld(*last, p, streaks[i])
			if u := unmetFor(st, last, p, held); u != nil {
				verdict.Unmet = append(verdict.Unmet, *u)
			}
		}
	}

	var fg *Foreground
	if before != nil || last != nil {
		f := &Foreground{}
		if before != nil {
			f.Before = before.Foreground
		}
		if last != nil {
			f.After = last.Foreground
		}
		fg = f
	}
	if last == nil {
		// No read after the action observed the screen: foreground.after is omitted and
		// the warning says so, since a result without it would look like a success
		// (plan m1f step 6, fix round).
		warnings = append(warnings, noObservationWarning)
	}
	return verifyResult{Expect: verdict, Tree: last, Foreground: fg, Warnings: dedupWarnings(warnings), LastBusy: lastBusy}
}

func resetStreaks(streaks []int) {
	for i := range streaks {
		streaks[i] = 0
	}
}

// evaluatePinned reports whether every pinned condition holds in s, advancing the `gone`
// streaks; `gone` needs two consecutive successful reads (ui-automation.md §Assertion
// vocabulary). A ref is matched by the identity pinned before the action.
func (st *snapshotStore) evaluatePinned(s Snapshot, pinned []pinnedCondition, streaks []int) bool {
	all := true
	for i, p := range pinned {
		if p.cond.Gone != nil {
			matches, _, _ := st.matchSetPinned(s, *p.cond.Gone, p.named)
			if len(matches) == 0 {
				streaks[i]++
			} else {
				streaks[i] = 0
			}
			if streaks[i] < 2 {
				all = false
			}
			continue
		}
		holds, err := p.holds(st, s)
		if err != nil || !holds {
			all = false
		}
	}
	return all
}

// conditionHeld is evaluatePinned for one condition against the final tree, without
// advancing a streak: used to describe an unmet condition.
func (st *snapshotStore) conditionHeld(s Snapshot, p pinnedCondition, streak int) bool {
	if p.cond.Gone != nil {
		return streak >= 2
	}
	holds, err := p.holds(st, s)
	return err == nil && holds
}

// unmetFor describes one failing condition with what the last tree showed: the
// still-present match for gone, near candidates for appears (D11).
func unmetFor(st *snapshotStore, last *Snapshot, p pinnedCondition, held bool) *Unmet {
	if held {
		return nil
	}
	u := &Unmet{Condition: p.cond}
	if last == nil {
		return u
	}
	switch {
	case p.cond.Appears != nil:
		if cands := nearCandidates(*last, *p.cond.Appears, maxUnmetCandidates); len(cands) > 0 {
			u.Candidates = cands
		}
	case p.cond.Gone != nil:
		matches, _, _ := st.matchSetPinned(*last, *p.cond.Gone, p.named)
		if len(matches) > 0 {
			u.Candidates = candidatesOf(matches, maxUnmetCandidates)
		}
	}
	return u
}

// sleepCtx waits d or until ctx ends; it reports whether the whole wait passed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// dedupWarnings keeps one copy of each warning, in order.
func dedupWarnings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range in {
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// boolPtr is a helper for an annotation override.
func boolPtr(b bool) *bool { return &b }

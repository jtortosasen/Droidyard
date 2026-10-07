package device

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

const (
	tapPackage   = "dev.droidyard.formapp"
	inputTapHead = "-s " + uiSerial + " shell input tap "
)

// tappable is the form app's synthetic tree: a full-display root and the children the
// test gives. It mirrors what the reader sees on the step 2 app, without the app.
func tappable(children ...string) string {
	root := fmt.Sprintf(`<node class="android.widget.FrameLayout" package=%q resource-id="%s:id/root" bounds="[0,0][1080,2340]">%s</node>`,
		tapPackage, tapPackage, strings.Join(children, ""))
	return string(synthDump(0, root))
}

func tapNode(class, id, text, extra string) string {
	return fmt.Sprintf(`<node class="%s" package=%q resource-id="%s:id/%s" text=%q bounds="[0,0][200,200]" %s></node>`,
		class, tapPackage, tapPackage, id, text, extra)
}

func tapLike(text string) string {
	return tapNode("android.widget.Button", "like", text, `clickable="true"`)
}
func tapLikes(text string) string { return tapNode("android.widget.TextView", "likes", text, "") }

// resolutionTree is one tree holding every resolution-error case: two "Twin" nodes, an
// off-screen node and a disabled node.
func resolutionTree() string {
	twin := func() string {
		return fmt.Sprintf(`<node class="android.widget.Button" package=%q text="Twin" bounds="[300,300][400,400]"></node>`, tapPackage)
	}
	off := fmt.Sprintf(`<node class="android.widget.TextView" package=%q resource-id="%s:id/off" text="off" bounds="[2000,2000][2100,2100]"></node>`, tapPackage, tapPackage)
	disabled := fmt.Sprintf(`<node class="android.widget.Button" package=%q resource-id="%s:id/disabled" text="Disabled" bounds="[500,500][600,600]" enabled="false"></node>`, tapPackage, tapPackage)
	return tappable(tapLike("Like"), twin(), twin(), off, disabled)
}

// quickAct shrinks the act-then-verify bounds so a fake test runs in milliseconds. The
// 250 ms poll interval has its own test, which keeps the real value. cleanupReserve is
// scaled too: readChain clamps a read's end to the phase deadline and the one-shot
// reader's cleanup is reserved out of it (plan m1f step 6, fix round), so a 2 s phase
// must not reserve the real 2 s.
func quickAct(t *testing.T) {
	t.Helper()
	before, tap, settle, poll, bound, reserve := actionBeforePhase, actionTapTimeout, actionSettle, actionPollInterval, actionRunningBound, cleanupReserve
	actionBeforePhase, actionTapTimeout, actionSettle, actionPollInterval, actionRunningBound, cleanupReserve =
		2*time.Second, 200*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, 5*time.Second, 50*time.Millisecond
	// device.inputText shares the act-then-verify bounds and adds its own (plan m1f
	// step 9): the clear/typing/submit budget and the ADBKeyboard check.
	typeBound, methodBound, cmdBound := inputTypeBound, inputMethodTimeout, inputCommandTimeout
	probeInterval := inputProbeInterval
	inputTypeBound, inputMethodTimeout, inputCommandTimeout = 500*time.Millisecond, 200*time.Millisecond, 500*time.Millisecond
	inputProbeInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		actionBeforePhase, actionTapTimeout, actionSettle, actionPollInterval, actionRunningBound, cleanupReserve = before, tap, settle, poll, bound, reserve
		inputTypeBound, inputMethodTimeout, inputCommandTimeout = typeBound, methodBound, cmdBound
		inputProbeInterval = probeInterval
	})
}

// tapCall runs device.tapElement through the fixture's scheduler and waits.
func (f *uiFixture) tapCall(t *testing.T, params string) capability.Outcome {
	t.Helper()
	id, err := f.ops.Start(context.Background(), capability.Request{Capability: "device.tapElement", Params: []byte(params)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return f.wait(t, id)
}

func mustTap(t *testing.T, out capability.Outcome) TapResult {
	t.Helper()
	if out.State != capability.StateCompleted {
		t.Fatalf("operation %s with error %+v", out.State, out.Error)
	}
	r, ok := out.Result.(TapResult)
	if !ok {
		t.Fatalf("result is %T, want TapResult", out.Result)
	}
	return r
}

func countInputTaps(calls []string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, inputTapHead) {
			n++
		}
	}
	return n
}

// refOf is the ref of the node with the given id in a snapshot, for a test that feeds a
// ref back into a call.
func refOf(t *testing.T, s Snapshot, id string) string {
	t.Helper()
	for _, n := range s.Nodes {
		if n.ID == id {
			return n.Ref
		}
	}
	t.Fatalf("snapshot %s has no node with id %q", s.ID, id)
	return ""
}

// Decision (plan m1f step 6, check a; ui-automation.md §Sequence and timing): the poll
// that holds decides, and `appears` on the third read gives matched with polls 3. The
// tap is issued once and after the tap no uiautomator dump runs (D17).
func TestTapElementAppearsFromTheThirdRead(t *testing.T) {
	quickAct(t)
	before := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	mid := before
	match := tappable(tapLike("Like"), tapLikes("Likes: 1"))
	f := newUIFixture(t, withPersistentReader(t, nil, before, mid, mid, match))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}]}`)
	r := mustTap(t, out)
	if r.Expect == nil || !r.Expect.Matched || r.Expect.Polls != 3 {
		t.Fatalf("expect %+v, want matched with 3 polls", r.Expect)
	}
	if r.Tapped.Ref == "" || r.Tapped.ID != "like" || r.Tapped.Text != "Like" || r.Tapped.Point != [2]int{100, 100} || !r.Tapped.Clickable {
		t.Errorf("tapped = %+v", r.Tapped)
	}
	calls := f.calls(t)
	if n := countInputTaps(calls); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
	if n := count(calls, ttyCall); n != 0 {
		t.Errorf("uiautomator ran after the tap: %q", calls)
	}
}

// Decision (plan m1f step 6, check a): `gone` needs two consecutive successful reads. A
// read that shows the node present resets the streak, so one gone read is not enough;
// the call matches on the second consecutive gone read. Seen failing with a one-read
// `gone` (it would match at the first gone read, polls 1).
func TestTapElementGoneNeedsTwoConsecutiveReads(t *testing.T) {
	quickAct(t)
	withBanner := tappable(tapLike("Like"), `<node class="android.widget.TextView" package="`+tapPackage+`" resource-id="`+tapPackage+`:id/banner" text="Banner" bounds="[0,100][200,150]"></node>`)
	without := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, withBanner, withBanner, without, withBanner, without, without))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"gone":{"id":"banner"}}]}`)
	r := mustTap(t, out)
	if r.Expect == nil || !r.Expect.Matched || r.Expect.Polls != 5 {
		t.Fatalf("expect %+v, want matched with 5 polls (two consecutive gone reads at the end)", r.Expect)
	}
}

// Decision (plan m1f step 6, check a; ui-automation.md §Sequence and timing): a condition
// true before the tap is flagged vacuous, not refused, and the tap still happens.
func TestTapElementVacuousConditionIsFlagged(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 0"}}]}`)
	r := mustTap(t, out)
	if r.Expect == nil || !r.Expect.Matched || len(r.Expect.Vacuous) != 1 {
		t.Fatalf("expect %+v, want matched with one vacuous condition", r.Expect)
	}
	if r.Expect.Vacuous[0].Appears == nil {
		t.Errorf("vacuous = %+v, want the appears condition", r.Expect.Vacuous)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
}

// Decision (plan m1f step 6, check a): a condition that never holds is a completed call
// with matched false and unmet, carrying near candidates from the last tree. Seen
// failing with an error instead of a completed result.
func TestTapElementNeverTrueIsUnmatchedWithCandidates(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"id":"like","text":"Nope"}}],"timeoutMs":80}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched {
		t.Fatalf("expect %+v, want matched false", r.Expect)
	}
	if len(r.Expect.Unmet) != 1 || len(r.Expect.Unmet[0].Candidates) == 0 {
		t.Fatalf("unmet = %+v, want one condition with near candidates", r.Expect.Unmet)
	}
	if r.Expect.Unmet[0].Candidates[0].ID != "like" {
		t.Errorf("candidate = %+v, want the near like node", r.Expect.Unmet[0].Candidates[0])
	}
}

// Decision (plan m1f step 6, check a; ui-automation.md §Result): every read after the tap
// failing is a completed result with reason noObservation, not an error; the call tapped
// once and never fell back to uiautomator (D17). Seen failing with a read failure after
// the tap returned as an error.
func TestTapElementAllReadsAfterTheTapFailedIsNoObservation(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc, devicetest.ReaderGone))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":60}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched || r.Expect.Reason != ReasonNoObservation {
		t.Fatalf("expect %+v, want matched false with reason %s", r.Expect, ReasonNoObservation)
	}
	if r.Expect.FailedDumps != r.Expect.Polls || r.Expect.Polls == 0 {
		t.Errorf("failedDumps %d of %d polls, want every poll to have failed", r.Expect.FailedDumps, r.Expect.Polls)
	}
	calls := f.calls(t)
	if n := countInputTaps(calls); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
	if n := count(calls, ttyCall); n != 0 {
		t.Errorf("uiautomator ran after the tap: %q", calls)
	}
}

// Decision (plan m1f step 6, check a; R5, D5): every resolution error taps nothing and
// marks the failed call, because its details quote device text (or name a device node).
// The marking reaches the envelope's meta and the message note.
func TestTapElementResolutionErrorsTapNothing(t *testing.T) {
	cases := []struct {
		name string
		sel  string
		code capability.ErrorCode
	}{
		{"not found", `{"id":"nope"}`, ErrElementNotFound},
		{"ambiguous", `{"text":"Twin"}`, ErrElementAmbiguous},
		{"off-screen", `{"id":"off"}`, ErrElementOffscreen},
		{"disabled", `{"id":"disabled"}`, ErrElementDisabled},
		{"stale ref", `{"ref":"n0","snapshot":"s-nope"}`, ErrStaleRef},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickAct(t)
			doc := resolutionTree()
			f := newUIFixture(t, withPersistentReader(t, nil, doc))
			out := f.tapCall(t, `{"device":"emulator-5554","target":`+c.sel+`}`)
			ce := mustFail(t, out, c.code)
			if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
				t.Errorf("a resolution error is unmarked: %+v", out)
			}
			if !strings.HasSuffix(ce.Message, capability.UntrustedErrorNote) {
				t.Errorf("message %q does not end with the untrusted note", ce.Message)
			}
			if n := countInputTaps(f.calls(t)); n != 0 {
				t.Errorf("%d input taps for a resolution error, want none", n)
			}
		})
	}
}

// Decision (plan m1f step 6, check a; D13, BF1): the clock starts at the authorization
// hook, not at the handler's entry. A hanging `adb version` spends the hook's time, and
// the before read is then cut so the call still fails within the 14 s phase of the hook.
// Seen failing with the clock started at handler entry (the read would get the full
// phase after the hook, ~200 ms later).
func TestTapElementBeforePhaseIsClockedFromTheHook(t *testing.T) {
	oldPhase, oldBound := actionBeforePhase, actionRunningBound
	actionBeforePhase, actionRunningBound = 400*time.Millisecond, 3*time.Second
	defer func() { actionBeforePhase, actionRunningBound = oldPhase, oldBound }()
	oldTap, oldSettle := actionTapTimeout, actionSettle
	actionTapTimeout, actionSettle = 200*time.Millisecond, 5*time.Millisecond
	defer func() { actionTapTimeout, actionSettle = oldTap, oldSettle }()

	// The hook spends 200 ms in `devices -l`; the reader and the dump never answer, so
	// the read is the stage the phase cuts. A clock started at handler entry would give
	// the read a full 400 ms after the hook and the call ~600 ms.
	f := newUIFixture(t, map[string]string{
		"devices.delay":               "200",
		uiSerial + ".push.exit":       "0",
		uiSerial + ".start.out":       "",
		uiSerial + ".forward.exit":    "1",
		uiSerial + ".reader.hang":     "",
		uiSerial + ".dumptty.hang":    "",
		uiSerial + ".dumpsdcard.hang": "",
	})
	start := time.Now()
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	// A clock at handler entry gives the read a full phase after the 200 ms hook
	// (~600 ms); the hook's clock cuts it at ~400 ms. 520 ms separates the two.
	if d := time.Since(start); d > 520*time.Millisecond {
		t.Errorf("the call took %v; want within the 400 ms before phase measured from the hook", d)
	}
	mustFail(t, out, ErrUIDumpFailed)
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps before a failed read, want none", n)
	}
}

// Decision (plan m1f step 6, check a; D13, BF1): with `adb version` and `devices -l`
// hanging to their (injected) timeouts and the read hanging too, the call fails with a
// typed code inside the phase and taps nothing. The read is never reached.
func TestTapElementHangingPreambleFailsBeforeTheRead(t *testing.T) {
	oldVersion, oldDevices := versionTimeout, devicesTimeout
	versionTimeout, devicesTimeout = 60*time.Millisecond, 60*time.Millisecond
	defer func() { versionTimeout, devicesTimeout = oldVersion, oldDevices }()
	oldPhase := actionBeforePhase
	actionBeforePhase = 2 * time.Second
	defer func() { actionBeforePhase = oldPhase }()

	f := newUIFixture(t, map[string]string{
		"version.hang":             "",
		"devices.hang":             "",
		uiSerial + ".push.exit":    "0",
		uiSerial + ".start.out":    "",
		uiSerial + ".forward.exit": "1",
		uiSerial + ".reader.hang":  "",
		uiSerial + ".dumptty.hang": "",
	})
	start := time.Now()
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	if d := time.Since(start); d > actionBeforePhase {
		t.Errorf("the call took %v, want within the %v before phase", d, actionBeforePhase)
	}
	mustFail(t, out, ErrADBFailed)
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps, want none", n)
	}
}

// Decision (plan m1f step 6, check a; D18): a tap command that times out may have
// landed, so verification goes on and the result carries a warning; it is never an
// error, and the tap is issued once. Seen failing with the timeout returned as an error.
func TestTapElementTapTimeoutWarnsAndVerifies(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, map[string]string{uiSerial + ".inputtap.hang": ""}, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.Expect != nil {
		t.Fatalf("expect %+v, want it omitted: the call gave none", r.Expect)
	}
	if r.Foreground == nil || r.Foreground.After == "" {
		t.Errorf("foreground %+v, want verification to have observed the screen after the tap", r.Foreground)
	}
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "did not report success") {
		t.Errorf("warnings = %q, want the tap's own warning", r.Warnings)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
}

// Decision (plan m1f step 6, check a; fix round): with no expect, exactly one read runs
// after the settle, and the result omits expect rather than reporting a matched that was
// never checked.
func TestTapElementNoExpectDoesOneRead(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	res := mustTap(t, out)
	if res.Expect != nil {
		t.Fatalf("expect %+v, want it omitted when the call gave none", res.Expect)
	}
	if res.Foreground == nil || res.Foreground.After == "" {
		t.Errorf("foreground %+v, want the single read to have observed the screen", res.Foreground)
	}
	if got := r.Dumps(); got != 2 {
		t.Errorf("%d dumps, want the before read and exactly one after the settle", got)
	}
}

// Decision (plan m1f step 6, fix round): a condition's ref is resolved to its node's
// identity before the tap, so the polls cannot evict the snapshot it came from. A node
// present in every read therefore never satisfies got; seen failing reversed, where the
// poll snapshots push the referenced snapshot out of the 16-slot store and `gone`
// wrongly matches while the node is still on screen.
func TestTapElementGoneOverManyPollsUsesThePinnedRef(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	snap := mustComplete(t, f.tree(t, uiSerial))
	ref := refOf(t, snap, "like")
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"gone":{"ref":"`+ref+`","snapshot":"`+snap.ID+`"}}],"timeoutMs":1000}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched {
		t.Fatalf("expect %+v, want matched false: the node is present in every read", r.Expect)
	}
	if r.Expect.Polls <= keptSnapshots {
		t.Errorf("polls = %d, want more than %d so the polls would have evicted %s", r.Expect.Polls, keptSnapshots, snap.ID)
	}
	if len(r.Expect.Unmet) != 1 || len(r.Expect.Unmet[0].Candidates) == 0 {
		t.Errorf("unmet = %+v, want the still-present node as a candidate", r.Expect.Unmet)
	}
}

// Decision (plan m1f step 6, fix round): a condition's ref is pinned to its node's
// identity before the tap and matched by that identity afterwards, so evicting the
// snapshot the ref came from during the window does not turn a node still on screen into
// a `gone` match. The store is emptied of the referenced snapshot from inside the tap
// command, after the pin. Seen failing with the pinned branch disabled (named == nil),
// where each poll looks the evicted snapshot up again, finds nothing, and `gone` wrongly
// holds.
func TestTapElementGoneStaysFalseWhenItsSnapshotIsEvicted(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	snap := mustComplete(t, f.tree(t, uiSerial))
	ref := refOf(t, snap, "like")

	inner := f.adb.exec
	f.adb.exec = func(ctx context.Context, path string, args []string) ([]byte, []byte, int, error) {
		if len(args) == 7 && args[2] == "shell" && args[3] == "input" && args[4] == "tap" {
			st := f.adb.snapshots(uiSerial)
			for i := 0; i < keptSnapshots; i++ {
				st.keep(Snapshot{ID: fmt.Sprintf("s-evict%02d", i)})
			}
		}
		return inner(ctx, path, args)
	}

	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"gone":{"ref":"`+ref+`","snapshot":"`+snap.ID+`"}}],"timeoutMs":400}`)
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched {
		t.Fatalf("expect %+v, want matched false: the node is present in every read", r.Expect)
	}
	if len(r.Expect.Unmet) != 1 || len(r.Expect.Unmet[0].Candidates) == 0 {
		t.Errorf("unmet = %+v, want the still-present node as a candidate", r.Expect.Unmet)
	}
	if r.Expect.Polls < 2 {
		t.Errorf("polls = %d, want two or more so `gone` had two reads to (wrongly) hold", r.Expect.Polls)
	}
}

// Decision (plan m1f step 6, fix round): an unknown snapshot in a condition is
// DEVICE_STALE_REF before anything is tapped, not a condition that silently matches
// `gone`. Seen failing with the ref read as "no match" and the call completing with a
// tap.
func TestTapElementUnknownSnapshotInConditionIsStale(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"gone":{"ref":"n0","snapshot":"s-nope"}}]}`)
	ce := mustFail(t, out, ErrStaleRef)
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("a stale condition error is unmarked: %+v", out)
	}
	if !strings.HasSuffix(ce.Message, capability.UntrustedErrorNote) {
		t.Errorf("message %q does not end with the untrusted note", ce.Message)
	}
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps for a stale condition, want none", n)
	}
}

// Decision (plan m1f step 6, fix round): the store keeps only the snapshots the result
// returns refs for, never each poll, so a uiTree snapshot taken before a long tapElement
// is still resolvable after it. Seen failing reversed, where the polls evict it and the
// ref resolves as stale.
func TestTapElementKeepsAUIrefUsable(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	snap := mustComplete(t, f.tree(t, uiSerial))
	ref := refOf(t, snap, "like")
	long := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":1000}`)
	lr := mustTap(t, long)
	if lr.Expect == nil || lr.Expect.Polls <= keptSnapshots {
		t.Fatalf("the long call's expect is %+v, want more than %d polls to exercise eviction", lr.Expect, keptSnapshots)
	}
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"ref":"`+ref+`","snapshot":"`+snap.ID+`"}}`)
	res := mustTap(t, out)
	if res.Tapped.Ref == "" {
		t.Errorf("tapped = %+v, want the ref from before the long call resolved", res.Tapped)
	}
}

// Decision (plan m1f step 6, fix round): the target's ref is resolved before the before
// read is kept, so keeping the before read cannot evict the snapshot the ref points to: a
// ref from the oldest of the 16 kept snapshots still resolves. Seen failing reversed,
// where the before read is kept first, evicting the oldest snapshot before the ref is
// resolved, and the call is DEVICE_STALE_REF.
func TestTapElementResolvesARefBeforeKeepingTheBeforeRead(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	oldest := mustComplete(t, f.tree(t, uiSerial))
	for i := 1; i < keptSnapshots; i++ {
		mustComplete(t, f.tree(t, uiSerial))
	}
	if kept := f.adb.snapshots(uiSerial).kept(); kept != keptSnapshots {
		t.Fatalf("kept %d snapshots, want %d", kept, keptSnapshots)
	}
	ref := refOf(t, oldest, "like")
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"ref":"`+ref+`","snapshot":"`+oldest.ID+`"}}`)
	r := mustTap(t, out)
	if r.Tapped.Ref == "" || r.Tapped.ID != "like" {
		t.Errorf("tapped = %+v, want the ref from the oldest kept snapshot to have resolved", r.Tapped)
	}
}

// Decision (plan m1f step 6, fix round; ui-automation.md §Result): with no expect the
// result omits expect rather than reporting a matched it never checked; when no read
// after the tap observed the screen, foreground.after is omitted and a warning says so.
// Seen failing reversed, where expect is present with matched true and the omission is
// silent.
func TestTapElementNoObservationWarnsAndOmitsAfter(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc, devicetest.ReaderGone))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
	r := mustTap(t, out)
	if r.Expect != nil {
		t.Fatalf("expect %+v, want it omitted when the call gave none", r.Expect)
	}
	if r.Foreground == nil || r.Foreground.Before == "" || r.Foreground.After != "" {
		t.Errorf("foreground = %+v, want before only when nothing was observed", r.Foreground)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "was not observed") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want one saying nothing was observed", r.Warnings)
	}
}

// Decision (plan m1f step 6, fix round): the first read after the tap runs to the
// running bound, not to timeoutMs, so a one-shot fallback slower than a small timeoutMs
// is still observed, with a condition and without one. Seen failing reversed, where the
// first read is cut at timeoutMs and the call gives noObservation (or an unobserved
// single read).
func TestTapElementFirstReadRunsToTheRunningBound(t *testing.T) {
	newSlowReader := func(t *testing.T, doc string) (*devicetest.FakeReader, *uiFixture) {
		t.Helper()
		quickAct(t)
		r := devicetest.StartReader(t, doc)
		t.Cleanup(r.Close)
		r.SetDelay(40 * time.Millisecond)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".push.exit":   "0",
			uiSerial + ".start.out":   "",
			uiSerial + ".forward.out": r.Port,
		})
		return r, f
	}

	t.Run("with a condition", func(t *testing.T) {
		doc := tappable(tapLike("Like"), tapLikes("Likes: 1"))
		_, f := newSlowReader(t, doc)
		out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":5}`)
		res := mustTap(t, out)
		if res.Expect == nil || !res.Expect.Matched {
			t.Fatalf("expect %+v, want matched: the first read is not bounded by timeoutMs", res.Expect)
		}
	})

	t.Run("with no expect", func(t *testing.T) {
		doc := tappable(tapLike("Like"))
		_, f := newSlowReader(t, doc)
		out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"timeoutMs":5}`)
		res := mustTap(t, out)
		if res.Foreground == nil || res.Foreground.After == "" {
			t.Fatalf("foreground = %+v, want the single read to have observed the screen", res.Foreground)
		}
	})
}

// Decision (plan m1f step 6, fix round; D18): the result names the snapshot beside every
// ref it returns — tapped.snapshot for tapped.ref, a top-level snapshot for the unmet
// candidates and tree — and those refs resolve with those snapshots. Seen failing
// reversed, where the ids are absent (or the last read is not kept).
func TestTapElementResultSnapshotsAreUsable(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"), tapLikes("Likes: 1"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"id":"like","text":"Nope"}}],"timeoutMs":30,"returnTree":true}`)
	res := mustTap(t, out)
	if res.Tapped.Snapshot == "" {
		t.Fatalf("tapped = %+v, want the snapshot tapped.ref belongs to", res.Tapped)
	}
	if res.Expect == nil || len(res.Expect.Unmet) != 1 || len(res.Expect.Unmet[0].Candidates) == 0 {
		t.Fatalf("expect %+v, want one unmet condition with a candidate", res.Expect)
	}
	if res.Tree == nil || res.Snapshot == "" || res.Snapshot != res.Tree.ID {
		t.Fatalf("snapshot = %q, tree = %+v, want the top-level snapshot to name the last read", res.Snapshot, res.Tree)
	}
	cand := res.Expect.Unmet[0].Candidates[0]
	byTap := f.tapCall(t, `{"device":"emulator-5554","target":{"ref":"`+res.Tapped.Ref+`","snapshot":"`+res.Tapped.Snapshot+`"}}`)
	if tr := mustTap(t, byTap); tr.Tapped.Ref == "" {
		t.Errorf("tapped = %+v, want tapped.ref resolved through tapped.snapshot", tr.Tapped)
	}
	byUnmet := f.tapCall(t, `{"device":"emulator-5554","target":{"ref":"`+cand.Ref+`","snapshot":"`+res.Snapshot+`"}}`)
	if tr := mustTap(t, byUnmet); tr.Tapped.Ref == "" {
		t.Errorf("tapped = %+v, want an unmet candidate resolved through the top-level snapshot", tr.Tapped)
	}
}

// Decision (plan m1f step 6, check a; D18; fix round): an `input tap` that provably never
// ran is an error, and nothing is verified after it. adb saying the device is not found
// is DEVICE_DISCONNECTED; a client that cannot be started is DEVICE_ADB_FAILED. Seen
// failing reversed, where the error becomes a warning and the call reads on.
func TestTapElementInputTapNeverRan(t *testing.T) {
	newFixture := func(t *testing.T) (*devicetest.FakeReader, *uiFixture) {
		t.Helper()
		quickAct(t)
		r := devicetest.StartReader(t, tappable(tapLike("Like")))
		t.Cleanup(r.Close)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".push.exit":   "0",
			uiSerial + ".start.out":   "",
			uiSerial + ".forward.out": r.Port,
		})
		return r, f
	}

	t.Run("the device is not found", func(t *testing.T) {
		r, f := newFixture(t)
		devicetest.Write(t, f.dir, uiSerial+".inputtap.err", []byte("error: device '"+uiSerial+"' not found\n"))
		devicetest.Write(t, f.dir, uiSerial+".inputtap.exit", []byte("1"))
		out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}]}`)
		mustFail(t, out, ErrDisconnected)
		if n := r.Dumps(); n != 1 {
			t.Errorf("%d reader dumps, want only the before read: nothing is verified after a tap that never ran", n)
		}
		if n := countInputTaps(f.calls(t)); n != 1 {
			t.Errorf("%d input taps, want one", n)
		}
	})

	t.Run("adb cannot start", func(t *testing.T) {
		r, f := newFixture(t)
		inner := f.adb.exec
		f.adb.exec = func(ctx context.Context, path string, args []string) ([]byte, []byte, int, error) {
			if len(args) == 7 && args[0] == "-s" && args[2] == "shell" && args[3] == "input" && args[4] == "tap" {
				return nil, nil, 0, errors.New("exec: cannot start")
			}
			return inner(ctx, path, args)
		}
		out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}]}`)
		mustFail(t, out, ErrADBFailed)
		if n := r.Dumps(); n != 1 {
			t.Errorf("%d reader dumps, want only the before read: nothing is verified after a tap that never ran", n)
		}
		// The client never started, so the fake logged no input tap invocation.
		if n := countInputTaps(f.calls(t)); n != 0 {
			t.Errorf("%d input taps in calls.log, want none: the client did not start", n)
		}
	})
}

// Decision (plan m1f step 6, fix round; D18): a running bound that expires while `input
// tap` runs is a warning and verification goes on — the command was issued, so the tap
// may have landed and an error would invite a second. A caller's cancellation during the
// tap stays a cancellation. Seen failing reversed, where the bound becomes an error and
// the cancellation becomes a warning/completion.
func TestTapElementRunningBoundDuringTheTapWarns(t *testing.T) {
	t.Run("the running bound", func(t *testing.T) {
		oldBound, oldPhase, oldTap, oldReserve := actionRunningBound, actionBeforePhase, actionTapTimeout, cleanupReserve
		actionRunningBound, actionBeforePhase, actionTapTimeout, cleanupReserve = 400*time.Millisecond, 350*time.Millisecond, 2*time.Second, 100*time.Millisecond
		defer func() {
			actionRunningBound, actionBeforePhase, actionTapTimeout, cleanupReserve = oldBound, oldPhase, oldTap, oldReserve
		}()
		oldSettle := actionSettle
		actionSettle = 5 * time.Millisecond
		defer func() { actionSettle = oldSettle }()

		r := devicetest.StartReader(t, tappable(tapLike("Like")))
		t.Cleanup(r.Close)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".push.exit":     "0",
			uiSerial + ".start.out":     "",
			uiSerial + ".forward.out":   r.Port,
			uiSerial + ".inputtap.hang": "",
		})
		out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`)
		res := mustTap(t, out)
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "may have landed") {
			t.Errorf("warnings = %q, want the tap-may-have-landed warning", res.Warnings)
		}
		if n := countInputTaps(f.calls(t)); n != 1 {
			t.Errorf("%d input taps, want one", n)
		}
	})

	t.Run("a caller's cancellation", func(t *testing.T) {
		quickAct(t)
		oldTap := actionTapTimeout
		actionTapTimeout = 2 * time.Second
		defer func() { actionTapTimeout = oldTap }()
		r := devicetest.StartReader(t, tappable(tapLike("Like")))
		t.Cleanup(r.Close)
		f := newUIFixture(t, map[string]string{
			uiSerial + ".push.exit":     "0",
			uiSerial + ".start.out":     "",
			uiSerial + ".forward.out":   r.Port,
			uiSerial + ".inputtap.hang": "",
		})
		id, err := f.ops.Start(context.Background(), capability.Request{
			Capability: "device.tapElement",
			Params:     []byte(`{"device":"emulator-5554","target":{"id":"like"}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		waitLine(t, func() []string { return f.calls(t) }, inputTapHead+"100 100")
		if err := f.ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		out := f.wait(t, id)
		if out.State != capability.StateCompleted {
			t.Fatalf("state = %s with error %+v, want completed: the tap was issued and may have landed (D18, D21)", out.State, out.Error)
		}
		res := mustTap(t, out)
		if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "cancelled while the tap command was running") {
			t.Errorf("warnings %q, want one saying the call was cancelled while the tap ran", res.Warnings)
		}
	})
}

// Decision (plan m1f step 7, D21; ui-automation.md §Result): a cancellation that
// arrives after the tap does not end the call with an error: the tap happened, so the
// result is completed, carries tapped, and, when expect was given, says reason cancelled,
// because an error would invite a retry that taps twice. Seen failing reversed, where the
// cancellation surfaces as a cancellation (or noObservation) without the tap reported.
func TestTapCancelledAfterTheTapIsCompletedWithCancelled(t *testing.T) {
	quickAct(t)
	old := actionRunningBound
	actionRunningBound = 10 * time.Second
	defer func() { actionRunningBound = old }()
	doc := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.tapElement",
		Params:     []byte(`{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":20000}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the tap has run and at least one poll has been made (the before read is
	// the first dump), then cancel: the cancellation arrives after the tap.
	deadline := time.Now().Add(5 * time.Second)
	for len(r.DumpStarts()) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if err := f.ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	out := f.wait(t, id)
	if out.State != capability.StateCompleted {
		t.Fatalf("state = %s with error %+v, want completed: the tap happened", out.State, out.Error)
	}
	res, ok := out.Result.(TapResult)
	if !ok {
		t.Fatalf("result is %T, want TapResult", out.Result)
	}
	if res.Tapped.Ref == "" {
		t.Error("tapped is empty: the tap happened and must be reported")
	}
	if res.Expect == nil || res.Expect.Reason != ReasonCancelled {
		t.Errorf("expect %+v, want reason %s", res.Expect, ReasonCancelled)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
}

// Decision (plan m1f step 6, check a; D13; fix round): hanging reads after the tap,
// and the one-shot reader's removal hanging after the first one, still return within the
// running bound, as a completed noObservation, not an error. readChain clamps the read's
// end to the running bound, so the removal's deadline is that bound and not a fresh 22 s.
// The first one-shot read works; every later one and its removal hangs. The bound and the
// cleanup reserve are injected, and the reserve is below the bound so the removal can
// hang for it.
func TestTapElementHangingReadsStayWithinTheRunningBound(t *testing.T) {
	oldBound, oldPhase, oldReserve := actionRunningBound, actionBeforePhase, cleanupReserve
	actionRunningBound, actionBeforePhase, cleanupReserve = time.Second, 800*time.Millisecond, 500*time.Millisecond
	defer func() { actionRunningBound, actionBeforePhase, cleanupReserve = oldBound, oldPhase, oldReserve }()
	oldSettle := actionSettle
	actionSettle = 5 * time.Millisecond
	defer func() { actionSettle = oldSettle }()

	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":       "0",
		uiSerial + ".start.out":       "",
		uiSerial + ".forward.exit":    "1",
		uiSerial + ".reader#1.out":    doc, // the before read
		uiSerial + ".reader.hang":     "",  // every read after it
		uiSerial + ".rmreader#1.exit": "0", // the before read's removal
		uiSerial + ".rmreader.hang":   "",  // every later removal hangs
	})
	start := time.Now()
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Likes: 1"}}],"timeoutMs":5000}`)
	if d := time.Since(start); d > actionRunningBound+300*time.Millisecond {
		t.Errorf("the call took %v, want within the %v running bound", d, actionRunningBound)
	}
	r := mustTap(t, out)
	if r.Expect == nil || r.Expect.Matched || r.Expect.Reason != ReasonNoObservation {
		t.Fatalf("expect %+v, want matched false with reason %s", r.Expect, ReasonNoObservation)
	}
	if n := countInputTaps(f.calls(t)); n != 1 {
		t.Errorf("%d input taps, want one", n)
	}
}

// Decision (plan m1f step 6, check a; R2): poll reads start at least 250 ms apart (the
// real interval). Seen failing with the interval removed (back-to-back reads).
func TestTapElementPollReadsAreSpacedByTheInterval(t *testing.T) {
	oldPhase, oldBound, oldSettle, oldTap, oldReserve := actionBeforePhase, actionRunningBound, actionSettle, actionTapTimeout, cleanupReserve
	actionBeforePhase, actionRunningBound, actionSettle, actionTapTimeout, cleanupReserve =
		2*time.Second, 5*time.Second, 5*time.Millisecond, 200*time.Millisecond, 50*time.Millisecond
	defer func() {
		actionBeforePhase, actionRunningBound, actionSettle, actionTapTimeout, cleanupReserve = oldPhase, oldBound, oldSettle, oldTap, oldReserve
	}()

	doc := tappable(tapLike("Like"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Nope"}}],"timeoutMs":900}`)
	mustTap(t, out)
	starts := r.DumpStarts()
	if len(starts) < 3 {
		t.Fatalf("%d dumps, want a before read and at least two polls", len(starts))
	}
	for i := 2; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 240*time.Millisecond {
			t.Errorf("poll reads %d and %d started %v apart, want at least 250 ms", i-1, i, gap)
		}
	}
}

// Decision (plan m1f step 6, check a; R2, ADR-0020 §Core primitives): two actions on one
// device do not interleave: the second is queued and names the first in blockedBy. Seen
// failing with no write key (the second would run).
func TestTapElementSecondActionIsQueued(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	start := func() string {
		id, err := f.ops.Start(context.Background(), capability.Request{
			Capability: "device.tapElement",
			Params:     []byte(`{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Nope"}}],"timeoutMs":10000}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := start()
	second := start()
	snap, err := f.ops.Wait(context.Background(), second, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != capability.StateQueued || snap.BlockedBy == nil || snap.BlockedBy.OperationID != first {
		t.Fatalf("second action is %s blockedBy %+v, want queued behind %s", snap.State, snap.BlockedBy, first)
	}
	if err := f.ops.Cancel(first); err != nil {
		t.Fatal(err)
	}
	// The cancelled first releases the key; the second runs and completes (its own
	// never-matching expect still bounds it).
	if err := f.ops.Cancel(second); err != nil {
		t.Fatal(err)
	}
}

// Decision (plan m1f step 6, check a; R2): a uiTree reads between a tapElement's polls:
// the dump lock is per read, not per call, so a concurrent reader is not held for the
// whole action. Seen failing with the lock held for the whole call (the uiTree would
// finish only after the tapElement).
func TestUITreeReadsBetweenATapElementsPolls(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	r := devicetest.StartReader(t, doc)
	t.Cleanup(r.Close)
	r.SetDelay(30 * time.Millisecond)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	id, err := f.ops.Start(context.Background(), capability.Request{
		Capability: "device.tapElement",
		Params:     []byte(`{"device":"emulator-5554","target":{"id":"like"},"expect":[{"appears":{"text":"Nope"}}],"timeoutMs":2000}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the action start polling
	f.tree(t, uiSerial)                // waits for its own read
	uiDone := time.Now()
	f.wait(t, id)
	tapDone := time.Now()
	if !uiDone.Before(tapDone) {
		t.Errorf("the uiTree finished at %v, not before the tapElement at %v: the reader lock was held for the whole call", uiDone, tapDone)
	}
}

// Decision (plan m1f step 6, Schema/R3): device.tapElement's published contract: since
// 0.5.0, a command with the write lock and the preamble hook, not destructive (with its
// reason), open world, not idempotent; its result is untrusted, and its ambiguous error
// is marked.
func TestTapElementDescriptorAndMark(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, NewADB(nil, nil), testStore(t), nil)
	var d capability.Descriptor
	for _, x := range reg.All() {
		if x.Name == "device.tapElement" {
			d = x
		}
	}
	if d.Name == "" {
		t.Fatal("device.tapElement is not registered")
	}
	var codes []string
	for _, c := range d.Errors {
		codes = append(codes, string(c))
	}
	slices.Sort(codes)
	want := []string{"DEVICE_ADB_FAILED", "DEVICE_ADB_NOT_FOUND", "DEVICE_DISCONNECTED", "DEVICE_ELEMENT_AMBIGUOUS", "DEVICE_ELEMENT_DISABLED", "DEVICE_ELEMENT_NOT_FOUND", "DEVICE_ELEMENT_OFFSCREEN", "DEVICE_NOT_ALLOWED", "DEVICE_NOT_FOUND", "DEVICE_NOT_READY", "DEVICE_STALE_REF", "DEVICE_UI_DUMP_FAILED", "OPERATION_QUEUE_TIMEOUT", "REQUEST_INVALID_PARAMS"}
	if !slices.Equal(codes, want) {
		t.Errorf("errors = %v, want %v", codes, want)
	}
	if d.Since != "0.5.0" || d.Kind != capability.Command || len(d.Availability) != 1 || d.Availability[0] != capability.RequiresADB || d.Resource == nil || d.Authorize == nil || d.LongRunning {
		t.Errorf("descriptor = %+v", d)
	}
	if k := d.Resource(&capability.Call{Params: TapParams{Device: "emulator-5554"}}); k != "device.write:emulator-5554" {
		t.Errorf("write key = %q", k)
	}
	a := d.ResolveAnnotations()
	if a.ReadOnly || a.Idempotent || !a.OpenWorld || a.Destructive || a.Reason == "" {
		t.Errorf("annotations = %+v, want a non-destructive open-world command with a reason", a)
	}

	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	if out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"like"}}`); !out.Untrusted {
		t.Error("a tapElement result is not marked untrusted")
	}
}

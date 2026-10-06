package device

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// occlusionContent is a content subtree holding the greet button, drawn first.
func occlusionContent() string {
	button := fmt.Sprintf(`<node class="android.widget.Button" package=%q resource-id="%s:id/greet" text="Greet" bounds="[31,200][1049,326]" drawing-order="1"></node>`,
		tapPackage, tapPackage)
	return fmt.Sprintf(`<node class="android.widget.FrameLayout" package=%q resource-id="%s:id/content" bounds="[0,0][1080,2340]" drawing-order="1">%s</node>`,
		tapPackage, tapPackage, button)
}

// occlusionDoc is the observed level-5 case: a content subtree holds the greet button,
// and an action-bar container drawn above it covers the button's centre, leaving a strip
// of the button visible. barTop/barBottom set the bar's vertical span, barOrder its place
// among the root's children.
func occlusionDoc(barTop, barBottom, barOrder int) string {
	bar := fmt.Sprintf(`<node class="android.view.ViewGroup" package=%q resource-id="%s:id/action_bar" bounds="[0,%d][1080,%d]" drawing-order="%d"></node>`,
		tapPackage, tapPackage, barTop, barBottom, barOrder)
	return tappable(occlusionContent(), bar)
}

// Decision (plan m1f step 9 item 7, D22; ui-automation.md §Resolution, reversal): when a
// node drawn above the target covers its centre, the tap moves to the centre of the
// largest visible strip and the result warns, naming the covering node by class and id.
// Seen failing reversed, where the tap landed on the covered centre (540,263).
func TestTapElementMovesThePointOffACoveredCentre(t *testing.T) {
	quickAct(t)
	doc := occlusionDoc(0, 300, 2) // the bar covers the button's centre (540,263)
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"greet"}}`)
	r := mustTap(t, out)
	if r.Tapped.Point != [2]int{540, 313} {
		t.Errorf("point = %v, want (540,313), the centre of the button's visible strip", r.Tapped.Point)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell input tap 540 313"); n != 1 {
		t.Errorf("%d taps at the strip, want one; calls %q", n, f.calls(t))
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "ViewGroup") && strings.Contains(w, "action_bar") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want one naming the covering action bar by class and id", r.Warnings)
	}
}

// Decision (plan m1f step 9 item 7, D22): a target whose every visible point is covered
// fails DEVICE_ELEMENT_OFFSCREEN, marked untrusted, with nothing tapped.
func TestTapElementFullyCoveredIsOffscreen(t *testing.T) {
	quickAct(t)
	doc := occlusionDoc(150, 400, 2) // the bar covers [31,200]–[1049,326] entirely
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"greet"}}`)
	mustFail(t, out, ErrElementOffscreen)
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps for a fully covered node, want none", n)
	}
	if !out.Untrusted || out.Meta() == nil || !out.Meta().Untrusted {
		t.Errorf("a covered-target error is unmarked: %+v", out)
	}
}

// Decision (plan m1f step 9 item 7, D22): a visible strip thinner than the 8 px floor is
// not a tap point, so the call is OFFSCREEN rather than tapping at the very edge.
func TestTapElementThinVisibleStripIsOffscreen(t *testing.T) {
	quickAct(t)
	doc := occlusionDoc(0, 322, 2) // leaves only [31,322]–[1049,326], 4 px tall
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"greet"}}`)
	mustFail(t, out, ErrElementOffscreen)
	if n := countInputTaps(f.calls(t)); n != 0 {
		t.Errorf("%d input taps for a 4 px strip, want none", n)
	}
}

// Decision (plan m1f step 9 item 7, D22): a node whose bounds are the whole display is a
// transparent overlay, not a cover, so the centre is kept and no warning is produced.
func TestTapElementIgnoresAFullScreenOverlay(t *testing.T) {
	quickAct(t)
	overlay := fmt.Sprintf(`<node class="android.view.View" package=%q resource-id="%s:id/overlay" bounds="[0,0][1080,2340]" drawing-order="9"></node>`,
		tapPackage, tapPackage)
	doc := tappable(occlusionContent(), overlay)
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.tapCall(t, `{"device":"emulator-5554","target":{"id":"greet"}}`)
	r := mustTap(t, out)
	if r.Tapped.Point != [2]int{540, 263} {
		t.Errorf("point = %v, want the uncovered centre (540,263)", r.Tapped.Point)
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "overlay") {
			t.Errorf("warnings = %q, want no cover warning for a full-screen overlay", r.Warnings)
		}
	}
}

// Decision (plan m1f step 9 item 7, D22): device.inputText's focusing tap uses the same
// occlusion-aware point as device.tapElement, so the field is focused, not the bar.
func TestInputTextFocusingTapUsesTheVisibleStrip(t *testing.T) {
	quickAct(t)
	doc := occlusionDoc(0, 300, 2)
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	out := f.inputCall(t, `{"device":"emulator-5554","text":"x","target":{"id":"greet"}}`)
	r := mustInput(t, out)
	if r.Tapped == nil || r.Tapped.Point != [2]int{540, 313} {
		t.Errorf("tapped = %+v, want point (540,313)", r.Tapped)
	}
	if n := count(f.calls(t), "-s "+uiSerial+" shell input tap 540 313"); n != 1 {
		t.Errorf("%d focusing taps at the strip, want one; calls %q", n, f.calls(t))
	}
}

// Decision (D22, from the level-5 run on Pixel_9a, API 36.1): a tree the reader read from
// the form app drawn edge to edge (targetSdk 36): the content starts at y=0 under the
// action bar container [0,0][1080,289], drawn above it. The name field is covered
// entirely, by the action bar and not by the sibling that only touches its lower edge;
// greet keeps a strip [289,326) and is tapped there. Seen failing with every node drawn
// above counted as a cover whether or not it overlaps (the error named the sibling).
func TestTapPointOnARecordedEdgeToEdgeTree(t *testing.T) {
	dump, err := os.ReadFile("testdata/formapp-edge-to-edge-api36.xml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := buildSnapshot(dump, "s-00000001", 40<<10, nil)
	if err != nil {
		t.Fatal(err)
	}
	node := func(id string) UINode {
		for _, n := range s.Nodes {
			if n.ID == id {
				return n
			}
		}
		t.Fatalf("no node %q in the recorded tree", id)
		return UINode{}
	}
	_, err = chooseTapPoint(s, node("name"))
	e := capability.AsError(err)
	if e == nil || e.Code != ErrElementOffscreen {
		t.Fatalf("name: %v, want %s (covered by the action bar)", err, ErrElementOffscreen)
	}
	covering, _ := e.Details["covering"].([]Candidate)
	if len(covering) != 1 || covering[0].ID != "action_bar_container" {
		t.Errorf("name: covering %v, want only action_bar_container", e.Details["covering"])
	}
	o, err := chooseTapPoint(s, node("greet"))
	if err != nil || !o.Moved || o.Point[1] < 289 || o.Point[1] >= 326 {
		t.Errorf("greet: %+v, %v; want a moved point inside the visible strip y in [289,326)", o, err)
	}
}

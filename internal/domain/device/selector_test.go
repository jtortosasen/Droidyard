package device

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

func iptr(n int) *int { return &n }

// sn renders one synthetic <node> with explicit attributes; the caller always passes
// bounds, so the display (the root's right/bottom edge) is under the test's control.
func sn(class, attrs string, children ...string) string {
	return fmt.Sprintf(`<node class="%s" package="com.example" %s>%s</node>`, class, attrs, strings.Join(children, ""))
}

// sdump wraps body in one root whose extent is the display.
func sdump(display string, body ...string) []byte {
	return synthDump(0, sn("android.widget.FrameLayout", `resource-id="com.example:id/root" bounds="`+display+`"`, body...))
}

func mustSnapshot(t *testing.T, dump []byte, id string) Snapshot {
	t.Helper()
	s, err := BuildSnapshot(dump, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func recordedSettings(t *testing.T) Snapshot {
	t.Helper()
	return mustSnapshot(t, readFixture(t, "pixel9a-uidump-devtty.xml"), "s-rec")
}

// Decision: a malformed selector is REQUEST_INVALID_PARAMS (plan m1f step 4): no field,
// ref without snapshot or with any other field, a negative index, an empty within.
// Seen failing with the empty-selector row removed.
func TestSelectorValidation(t *testing.T) {
	bad := []struct {
		name string
		sel  Selector
	}{
		{"no field", Selector{}},
		{"index only", Selector{Index: iptr(0)}},
		{"ref without snapshot", Selector{Ref: "n1"}},
		{"ref with id", Selector{Ref: "n1", Snapshot: "s-1", ID: "x"}},
		{"ref with text", Selector{Ref: "n1", Snapshot: "s-1", Text: "x"}},
		{"ref with index", Selector{Ref: "n1", Snapshot: "s-1", Index: iptr(1)}},
		{"snapshot without ref", Selector{Snapshot: "s-1"}},
		{"negative index", Selector{ID: "x", Index: iptr(-1)}},
		{"empty within", Selector{ID: "x", Within: &AncestorSelector{}}},
	}
	for _, c := range bad {
		err := c.sel.validate()
		if e := capability.AsError(err); e == nil || e.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v, want %s", c.name, err, capability.ErrInvalidParams)
		}
	}
	good := []Selector{
		{ID: "x"},
		{Ref: "n1", Snapshot: "s-1"},
		{Within: &AncestorSelector{ID: "box"}},
		{ID: "x", Index: iptr(0)},
		{Text: "a", Within: &AncestorSelector{Text: "b"}},
		{ID: "x", Class: "Button", Text: "a", Desc: "b", Within: &AncestorSelector{Class: "FrameLayout"}, Index: iptr(2)},
	}
	for _, s := range good {
		if err := s.validate(); err != nil {
			t.Errorf("valid selector %+v rejected: %v", s, err)
		}
	}
}

// Decision: the resolution order of ui-automation.md §Resolution 1–2 — exact first,
// then case-insensitive contains on text and desc only, id and class stay exact.
// Seen failing with contains on id (that row would find n11).
func TestSelectorResolutionOverRecordedSettings(t *testing.T) {
	s := recordedSettings(t)
	st := newSnapshotStore()
	cases := []struct {
		name string
		sel  Selector
		ref  string
		by   string
		code capability.ErrorCode
	}{
		{"exact before contains", Selector{Text: "Brightness"}, "n11", "exact", ""},
		{"exact id and text", Selector{ID: "title", Text: "Brightness"}, "n11", "exact", ""},
		{"contains on text is case-insensitive", Selector{Text: "brightness level"}, "n13", "contains", ""},
		{"contains on desc", Selector{Desc: "dark theme"}, "n28", "contains", ""},
		{"id is never contains", Selector{ID: "titl"}, "", "", ErrElementNotFound},
		{"class is never contains", Selector{Class: "button"}, "", "", ErrElementNotFound},
		{"ambiguous id", Selector{ID: "title"}, "", "", ErrElementAmbiguous},
	}
	for _, c := range cases {
		m, err := st.resolve(s, c.sel)
		if c.code != "" {
			if e := capability.AsError(err); e == nil || e.Code != c.code {
				t.Errorf("%s: err %v, want %s", c.name, err, c.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if m.Node.Ref != c.ref || m.MatchedBy != c.by {
			t.Errorf("%s: got %s matchedBy %s, want %s %s", c.name, m.Node.Ref, m.MatchedBy, c.ref, c.by)
		}
	}
}

// Decision: nested duplicates keep the outermost match (ui-automation.md §Resolution 3;
// Compose merges semantics). Seen failing with the innermost duplicate kept.
func TestSelectorFoldNestedDuplicates(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.LinearLayout", `resource-id="com.example:id/outer" text="Sign in" bounds="[0,0][100,100]"`,
			sn("android.widget.LinearLayout", `resource-id="com.example:id/mid" text="Sign in" bounds="[0,0][100,100]"`,
				sn("android.widget.TextView", `resource-id="com.example:id/leaf" text="Sign in" bounds="[0,0][100,100]"`))))
	s := mustSnapshot(t, dump, "s-1")
	m, err := newSnapshotStore().resolve(s, Selector{Text: "Sign in"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Node.ID != "outer" {
		t.Errorf("kept %q, want the outermost match outer", m.Node.ID)
	}
}

// Decision: only off-screen matches are DEVICE_ELEMENT_OFFSCREEN, with their bounds and
// the display in details (ui-automation.md §Resolution 4); a zero-area node is
// off-screen too. Seen failing with the off-screen check removed.
func TestSelectorOffscreen(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.Button", `resource-id="com.example:id/on" text="On" bounds="[10,10][20,20]"`),
		sn("android.widget.Button", `resource-id="com.example:id/off" text="Off" bounds="[200,200][300,300]"`),
		sn("android.widget.Button", `resource-id="com.example:id/zero" text="Zero" bounds="[5,5][5,5]"`))
	s := mustSnapshot(t, dump, "s-1")
	st := newSnapshotStore()
	if m, err := st.resolve(s, Selector{ID: "on"}); err != nil || m.Node.ID != "on" {
		t.Errorf("on-screen target: %v, %+v", err, m)
	}
	for _, id := range []string{"off", "zero"} {
		e := capability.AsError(func() error { _, err := st.resolve(s, Selector{ID: id}); return err }())
		if e == nil || e.Code != ErrElementOffscreen {
			t.Fatalf("%s: want OFFSCREEN, got %v", id, e)
		}
		if _, ok := e.Details["candidates"]; !ok {
			t.Errorf("%s: off-screen error lacks candidates", id)
		}
		if _, ok := e.Details["display"]; !ok {
			t.Errorf("%s: off-screen error lacks the display", id)
		}
	}
	// An on-screen match beside an off-screen one is not off-screen: the on-screen one wins.
	dump = sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/dup" text="Hi" bounds="[200,200][300,300]"`),
		sn("android.widget.TextView", `resource-id="com.example:id/dup" text="Hi" bounds="[10,10][20,20]"`))
	s = mustSnapshot(t, dump, "s-1")
	if m, err := st.resolve(s, Selector{ID: "dup"}); err != nil || m.Node.Bounds != [4]int{10, 10, 20, 20} {
		t.Errorf("the one on-screen duplicate should win: %v, %+v", err, m)
	}
}

// Decision: more than one match without index is DEVICE_ELEMENT_AMBIGUOUS with at most
// 10 candidates; index picks in document order (ui-automation.md §Resolution 5; D11).
func TestSelectorAmbiguousAndIndex(t *testing.T) {
	var kids []string
	for i := 0; i < 12; i++ {
		kids = append(kids, sn("android.widget.Button",
			fmt.Sprintf(`resource-id="com.example:id/item" text="row %d" bounds="[0,%d][50,%d]"`, i, i*8, i*8+8), ""))
	}
	s := mustSnapshot(t, sdump("[0,0][100,100]", kids...), "s-1")
	st := newSnapshotStore()
	e := capability.AsError(func() error { _, err := st.resolve(s, Selector{ID: "item"}); return err }())
	if e == nil || e.Code != ErrElementAmbiguous {
		t.Fatalf("want AMBIGUOUS, got %v", e)
	}
	cands, _ := e.Details["candidates"].([]Candidate)
	if len(cands) != 10 {
		t.Errorf("ambiguous lists %d candidates, want the 10-cap", len(cands))
	}
	if !strings.Contains(e.Message, "12") {
		t.Errorf("message %q does not name the 12 matches", e.Message)
	}
	for _, want := range []int{0, 5, 11} {
		m, err := st.resolve(s, Selector{ID: "item", Index: iptr(want)})
		if err != nil {
			t.Fatalf("index %d: %v", want, err)
		}
		if m.Node.Ref != fmt.Sprintf("n%d", want+1) { // n0 is the kept root
			t.Errorf("index %d picked %s", want, m.Node.Ref)
		}
	}
	// An index past the matches is NOT_FOUND, not a panic and not a silent pick.
	if e := capability.AsError(func() error { _, err := st.resolve(s, Selector{ID: "item", Index: iptr(12)}); return err }()); e == nil || e.Code != ErrElementNotFound {
		t.Errorf("index past the matches: %v, want NOT_FOUND", e)
	}
}

// Decision: a disabled match is DEVICE_ELEMENT_DISABLED and is not dropped as
// off-screen (ui-automation.md §Resolution 7). Seen failing with disabled dropped as
// off-screen.
func TestSelectorDisabledNotDropped(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.Button", `resource-id="com.example:id/go" text="Go" enabled="false" clickable="true" bounds="[10,10][90,40]"`))
	s := mustSnapshot(t, dump, "s-1")
	e := capability.AsError(func() error { _, err := newSnapshotStore().resolve(s, Selector{ID: "go"}); return err }())
	if e == nil || e.Code != ErrElementDisabled {
		t.Fatalf("want DISABLED, got %v", e)
	}
	if _, ok := e.Details["node"]; !ok {
		t.Errorf("disabled error lacks the node")
	}
}

// Decision: ref resolves by identity (class, id, text, desc, bounds), never by
// position, so a node that moved in document order is still found
// (ui-automation.md §Selectors). Seen failing with a ref resolved by position.
func TestSelectorRefIdentityAfterMove(t *testing.T) {
	before := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/a" text="A" bounds="[0,0][10,10]"`),
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B" bounds="[0,10][10,20]"`),
		sn("android.widget.TextView", `resource-id="com.example:id/c" text="C" bounds="[0,20][10,30]"`))
	after := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B" bounds="[0,10][10,20]"`),
		sn("android.widget.TextView", `resource-id="com.example:id/a" text="A" bounds="[0,0][10,10]"`),
		sn("android.widget.TextView", `resource-id="com.example:id/c" text="C" bounds="[0,20][10,30]"`))
	old := mustSnapshot(t, before, "s-1")
	fresh := mustSnapshot(t, after, "s-2")
	st := newSnapshotStore()
	st.keep(old)
	m, err := st.resolve(fresh, Selector{Ref: "n2", Snapshot: "s-1"}) // n2 is b before the move
	if err != nil {
		t.Fatal(err)
	}
	if m.Node.ID != "b" || m.Node.Ref != "n1" {
		t.Errorf("ref resolved to %s (%s), want b at its new position n1", m.Node.ID, m.Node.Ref)
	}
}

// Decision: a ref resolves by identity, whose fields are class, id, text, desc AND
// bounds: a ref whose named node changed identity, moved its bounds, or whose snapshot
// is unknown or evicted (the 17th-oldest, D16), is DEVICE_STALE_REF. Seen failing with
// the identity check removed from the text comparison and with bounds dropped from it.
func TestSelectorStaleRef(t *testing.T) {
	before := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B" bounds="[0,10][10,20]"`))
	after := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B2" bounds="[0,10][10,20]"`))
	same := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B" bounds="[0,10][10,20]"`))
	moved := sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/b" text="B" bounds="[0,30][10,40]"`))
	old := mustSnapshot(t, before, "s-1")
	fresh := mustSnapshot(t, after, "s-2")
	equal := mustSnapshot(t, same, "s-3")
	movedSnap := mustSnapshot(t, moved, "s-4")
	st := newSnapshotStore()
	st.keep(old)
	if m, err := st.resolve(equal, Selector{Ref: "n1", Snapshot: "s-1"}); err != nil || m.Node.ID != "b" {
		t.Fatalf("unchanged ref: %v, %+v", err, m)
	}
	for _, c := range []struct {
		name string
		tree Snapshot
		sel  Selector
	}{
		{"text changed", fresh, Selector{Ref: "n1", Snapshot: "s-1"}},
		{"bounds moved", movedSnap, Selector{Ref: "n1", Snapshot: "s-1"}},
		{"snapshot unknown", equal, Selector{Ref: "n1", Snapshot: "s-nope"}},
		{"ref unknown", equal, Selector{Ref: "n99", Snapshot: "s-1"}},
	} {
		e := capability.AsError(func() error { _, err := st.resolve(c.tree, c.sel); return err }())
		if e == nil || e.Code != ErrStaleRef {
			t.Errorf("%s: want STALE_REF, got %v", c.name, e)
		}
	}

	// The 17th-oldest snapshot is evicted: its ref is stale, the 16 newest resolve.
	evict := newSnapshotStore()
	first := mustSnapshot(t, same, "s-first")
	evict.keep(first)
	for i := 0; i < 16; i++ {
		evict.keep(mustSnapshot(t, same, fmt.Sprintf("s-%d", i+1)))
	}
	if _, ok := evict.get("s-first"); ok {
		t.Fatalf("the 17th-oldest snapshot is still kept")
	}
	e := capability.AsError(func() error { _, err := evict.resolve(equal, Selector{Ref: "n1", Snapshot: "s-first"}); return err }())
	if e == nil || e.Code != ErrStaleRef {
		t.Errorf("evicted ref: want STALE_REF, got %v", e)
	}
	if evict.kept() != 16 {
		t.Errorf("kept %d snapshots, want 16", evict.kept())
	}
}

func nodeIDs(s Snapshot) []string {
	out := make([]string, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// Decision: `within` matches its fields exactly; the contains fallback of §Resolution 2
// applies to the target only (ui-automation.md §Selectors). A within whose text is only
// a substring of a node's text matches nothing — the subtree is empty with noMatch, and
// a target under it is NOT_FOUND. Seen failing with the contains fallback applied to
// within too.
func TestWithinMatchesFieldsExactly(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.LinearLayout", `resource-id="com.example:id/box" text="Sign in form" bounds="[0,0][100,50]"`,
			sn("android.widget.Button", `resource-id="com.example:id/go" text="Go" bounds="[0,0][50,50]"`)))
	s := mustSnapshot(t, dump, "s-1")

	tree, err := buildSnapshot(dump, "s-2", 0, &AncestorSelector{Text: "Sign in"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 0 || tree.EmptyReason != EmptyNoMatch {
		t.Errorf("within by substring: %+v, want empty with noMatch", tree)
	}
	tree, err = buildSnapshot(dump, "s-3", 0, &AncestorSelector{Text: "Sign in form"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"box", "go"}; !equalStrings(nodeIDs(tree), want) {
		t.Errorf("within by exact text kept %v, want %v", nodeIDs(tree), want)
	}

	e := capability.AsError(func() error {
		_, err := newSnapshotStore().resolve(s, Selector{ID: "go", Within: &AncestorSelector{Text: "form"}})
		return err
	}())
	if e == nil || e.Code != ErrElementNotFound {
		t.Errorf("target within a substring: %v, want NOT_FOUND", e)
	}
}

// Decision: DEVICE_ELEMENT_NOT_FOUND carries near candidates in document order and, for
// an empty tree, its emptyReason (ui-automation.md §Resolution 6; D11); DEVICE_STALE_REF
// carries candidates with the same text/desc (§Failure modes). Seen failing with the
// candidates dropped from each.
func TestNotFoundAndStaleRefCandidates(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.Button", `resource-id="com.example:id/one" text="Save draft" bounds="[0,0][50,20]"`),
		sn("android.widget.Button", `resource-id="com.example:id/two" text="Saved" bounds="[0,20][50,40]"`),
		sn("android.widget.Button", `resource-id="com.example:id/three" text="Discard" bounds="[0,40][50,60]"`))
	s := mustSnapshot(t, dump, "s-1")
	st := newSnapshotStore()

	// No node is a TextView, but "Save" is a near match of one and two.
	e := capability.AsError(func() error {
		_, err := st.resolve(s, Selector{Text: "Save", Class: "TextView"})
		return err
	}())
	if e == nil || e.Code != ErrElementNotFound {
		t.Fatalf("want NOT_FOUND, got %v", e)
	}
	cands, _ := e.Details["candidates"].([]Candidate)
	var refs []string
	for _, c := range cands {
		refs = append(refs, c.Ref)
	}
	if want := []string{"n1", "n2"}; !equalStrings(refs, want) {
		t.Errorf("near candidates %v, want the literal %v", refs, want)
	}

	// An empty tree names its emptyReason in details.
	empty := Snapshot{ID: "s-2", EmptyReason: EmptyNoAccessibilityInfo, Nodes: []UINode{}}
	e = capability.AsError(func() error {
		_, err := st.resolve(empty, Selector{ID: "x"})
		return err
	}())
	if e == nil || e.Code != ErrElementNotFound {
		t.Fatalf("empty tree: want NOT_FOUND, got %v", e)
	}
	if e.Details["emptyReason"] != EmptyNoAccessibilityInfo {
		t.Errorf("details emptyReason %v, want %s", e.Details["emptyReason"], EmptyNoAccessibilityInfo)
	}

	// A stale ref names the fresh nodes with the same text/desc.
	old := mustSnapshot(t, sdump("[0,0][100,100]",
		sn("android.widget.Button", `resource-id="com.example:id/b1" text="Save draft" bounds="[0,0][50,20]"`)), "s-old")
	fresh := mustSnapshot(t, sdump("[0,0][100,100]",
		sn("android.widget.Button", `resource-id="com.example:id/b2" text="Save draft now" bounds="[0,0][50,20]"`)), "s-fresh")
	stale := newSnapshotStore()
	stale.keep(old)
	e = capability.AsError(func() error {
		_, err := stale.resolve(fresh, Selector{Ref: "n1", Snapshot: "s-old"})
		return err
	}())
	if e == nil || e.Code != ErrStaleRef {
		t.Fatalf("want STALE_REF, got %v", e)
	}
	cands, _ = e.Details["candidates"].([]Candidate)
	if len(cands) != 1 || cands[0].Ref != "n1" {
		t.Errorf("stale candidates %+v, want the fresh node with the same text", cands)
	}
}

// Decision: snapshots are per device; a ref minted from a read on device A is
// DEVICE_STALE_REF resolved for device B, even against an identical tree, because B's
// store never held A's snapshot (D16; ui-automation.md §The tree). Seen failing with one
// store shared by every device.
func TestSnapshotsArePerDevice(t *testing.T) {
	f := newUIFixture(t, nil)
	s := mustComplete(t, f.tree(t, uiSerial))
	a := f.adb.snapshots(uiSerial)
	b := f.adb.snapshots("emulator-5556")
	if _, ok := a.get(s.ID); !ok {
		t.Fatalf("device A did not keep its own snapshot %s", s.ID)
	}
	if _, ok := b.get(s.ID); ok {
		t.Fatalf("device B kept device A's snapshot %s", s.ID)
	}
	var ref string
	for _, n := range s.Nodes {
		if n.ID == "switchWidget" {
			ref = n.Ref
		}
	}
	if ref == "" {
		t.Fatal("the recorded dump lost switchWidget")
	}
	e := capability.AsError(func() error {
		_, err := b.resolve(s, Selector{Ref: ref, Snapshot: s.ID})
		return err
	}())
	if e == nil || e.Code != ErrStaleRef {
		t.Errorf("a ref from device A on device B: %v, want DEVICE_STALE_REF", e)
	}
}

// Decision: NOT_FOUND on a tree with no accessibility information points at
// device.screenshot (ui-automation.md §Failure modes); the ordinary NOT_FOUND does not.
// Seen failing with the next step left generic.
func TestNotFoundPointsAtScreenshotWhenNoAccessibilityInfo(t *testing.T) {
	empty := Snapshot{ID: "s-1", EmptyReason: EmptyNoAccessibilityInfo, Nodes: []UINode{}}
	e := capability.AsError(func() error {
		_, err := newSnapshotStore().resolve(empty, Selector{ID: "x"})
		return err
	}())
	if e == nil || e.Code != ErrElementNotFound {
		t.Fatalf("want NOT_FOUND, got %v", e)
	}
	if !strings.Contains(e.NextStep, "device.screenshot") {
		t.Errorf("nextStep %q does not name device.screenshot", e.NextStep)
	}

	body := sdump("[0,0][100,100]", sn("android.widget.TextView", `text="x" bounds="[0,0][10,10]"`))
	e = capability.AsError(func() error {
		_, err := newSnapshotStore().resolve(mustSnapshot(t, body, "s-2"), Selector{ID: "y"})
		return err
	}())
	if e == nil || e.Code != ErrElementNotFound {
		t.Fatalf("plain NOT_FOUND: got %v", e)
	}
	if strings.Contains(e.NextStep, "device.screenshot") {
		t.Errorf("a plain NOT_FOUND points at device.screenshot: %q", e.NextStep)
	}
}

// Decision: ids are `s-` plus 8 hex digits and unique over 10,000 mints, and one device
// keeps only the last 16 (D16). Seen failing with the counter reset to random 4-digit
// ids (which collide).
func TestSnapshotIDsUniqueAndKept16(t *testing.T) {
	re := regexp.MustCompile(`^s-[0-9a-f]{8}$`)
	seen := map[string]bool{}
	ids := make([]string, 0, 10000)
	st := newSnapshotStore()
	first := NewSnapshotID()
	seen[first] = true
	ids = append(ids, first)
	for i := 0; i < 9999; i++ {
		id := NewSnapshotID()
		if !re.MatchString(id) {
			t.Fatalf("id %q is not s- plus 8 hex digits", id)
		}
		if seen[id] {
			t.Fatalf("id %q minted twice", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range ids {
		st.keep(Snapshot{ID: id})
	}
	last := ids[len(ids)-1]
	if st.kept() != 16 {
		t.Errorf("kept %d snapshots, want 16", st.kept())
	}
	if _, ok := st.get(last); !ok {
		t.Errorf("the newest snapshot %s is not kept", last)
	}
	if _, ok := st.get(first); ok {
		t.Errorf("the 10,000th-oldest snapshot %s is still kept", first)
	}
}

// Decision: `within` restricts to descendants of an ancestor matching every given
// field; the boundary node itself is not a descendant of itself
// (ui-automation.md §Selectors; D6).
func TestSelectorWithinKeepsDescendantsOnly(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.LinearLayout", `resource-id="com.example:id/box" bounds="[0,0][100,50]"`,
			sn("android.widget.TextView", `resource-id="com.example:id/inner" text="x" bounds="[0,0][50,50]"`,
				sn("android.widget.TextView", `resource-id="com.example:id/deep" text="y" bounds="[0,0][25,25]"`))),
		sn("android.widget.TextView", `resource-id="com.example:id/out" text="x" bounds="[50,50][100,100]"`))
	s := mustSnapshot(t, dump, "s-1")
	st := newSnapshotStore()
	m, err := st.resolve(s, Selector{ID: "inner", Within: &AncestorSelector{ID: "box"}})
	if err != nil || m.Node.ID != "inner" {
		t.Fatalf("inner within box: %v, %+v", err, m)
	}
	m, err = st.resolve(s, Selector{ID: "inner", Within: &AncestorSelector{ID: "root"}})
	if err != nil || m.Node.ID != "inner" {
		t.Fatalf("a match any ancestor holds: %v, %+v", err, m)
	}
	m, err = st.resolve(s, Selector{Text: "x", Within: &AncestorSelector{ID: "box"}})
	if err != nil || m.Node.ID != "inner" {
		t.Fatalf("within must exclude the sibling outside: %v, %+v", err, m)
	}
	for _, sel := range []Selector{
		{ID: "out", Within: &AncestorSelector{ID: "box"}},
		{ID: "box", Within: &AncestorSelector{ID: "box"}},
	} {
		if e := capability.AsError(func() error { _, err := st.resolve(s, sel); return err }()); e == nil || e.Code != ErrElementNotFound {
			t.Errorf("%+v: want NOT_FOUND, got %v", sel, e)
		}
	}
}

// Decision: `uiTree {within}` returns the subtrees of every match, before the cap, and
// says noMatch when nothing matches (D6; ui-automation.md §The tree).
func TestUITreeWithinBuildsSubtrees(t *testing.T) {
	dump := sdump("[0,0][100,100]",
		sn("android.widget.LinearLayout", `resource-id="com.example:id/box" bounds="[0,0][100,50]"`,
			sn("android.widget.TextView", `resource-id="com.example:id/inner" text="x" bounds="[0,0][50,50]"`,
				sn("android.widget.TextView", `resource-id="com.example:id/deep" text="y" bounds="[0,0][25,25]"`))),
		sn("android.widget.TextView", `resource-id="com.example:id/out" text="x" bounds="[50,50][100,100]"`))
	s, err := buildSnapshot(dump, "s-1", 0, &AncestorSelector{ID: "box"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range s.Nodes {
		ids = append(ids, n.ID)
	}
	if want := []string{"box", "inner", "deep"}; !equalStrings(ids, want) {
		t.Errorf("within kept %v, want %v", ids, want)
	}
	if s.EmptyReason != "" || s.Foreground != "com.example" || s.Display.Width != 100 {
		t.Errorf("within changed foreground/display/emptyReason: %+v", s)
	}
	s, err = buildSnapshot(dump, "s-2", 0, &AncestorSelector{ID: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 0 || s.EmptyReason != EmptyNoMatch {
		t.Errorf("within matching nothing: %+v, want empty with noMatch", s)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startParams starts an operation of cap with the given raw params.
func (f *uiFixture) startParams(t *testing.T, cap, params string) (string, error) {
	t.Helper()
	return f.ops.Start(context.Background(), capability.Request{Capability: cap, Params: []byte(params)})
}

// Decision: the whole uiTree path honours within: the recorded Settings subtree comes
// back, a within matching nothing is noMatch, a malformed within is
// REQUEST_INVALID_PARAMS with no read, and every snapshot is kept for refs (D6, D16).
func TestUITreeWithinThroughTheCapability(t *testing.T) {
	f := newUIFixture(t, nil)
	id, err := f.startParams(t, "device.uiTree", `{"device":"emulator-5554","within":{"id":"app_bar"}}`)
	if err != nil {
		t.Fatal(err)
	}
	s := mustComplete(t, f.wait(t, id))
	want := map[string]bool{"n2": true, "n3": true, "n4": true, "n5": true}
	for _, n := range s.Nodes {
		if !want[n.Ref] {
			t.Errorf("within app_bar leaked %s (%s)", n.Ref, n.ID)
		}
	}
	if len(s.Nodes) != len(want) {
		t.Errorf("within app_bar returned %d nodes, want %d", len(s.Nodes), len(want))
	}
	if s.Foreground != "com.android.settings" {
		t.Errorf("foreground %q", s.Foreground)
	}
	if kept := f.adb.snapshots(uiSerial).kept(); kept != 1 {
		t.Errorf("kept %d snapshots, want the returned one", kept)
	}

	id, err = f.startParams(t, "device.uiTree", `{"device":"emulator-5554","within":{"id":"nope"}}`)
	if err != nil {
		t.Fatal(err)
	}
	s = mustComplete(t, f.wait(t, id))
	if len(s.Nodes) != 0 || s.EmptyReason != EmptyNoMatch {
		t.Errorf("within nope: %+v, want empty with noMatch", s)
	}

	before := len(f.calls(t))
	id, err = f.startParams(t, "device.uiTree", `{"device":"emulator-5554","within":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	ce := mustFail(t, f.wait(t, id), capability.ErrInvalidParams)
	if !strings.Contains(ce.Message, "within") {
		t.Errorf("message %q does not name within", ce.Message)
	}
	for _, c := range f.calls(t)[before:] {
		if strings.Contains(c, "uiautomator") || strings.Contains(c, "push") {
			t.Errorf("a malformed within still read the device: %q", c)
		}
	}
}

// Decision: a ref returned by uiTree resolves against a later tree (D16), and the
// store's kept snapshots back it.
func TestUITreeRefResolvesInALaterTree(t *testing.T) {
	f := newUIFixture(t, nil)
	first := mustComplete(t, f.tree(t, uiSerial))
	second := mustComplete(t, f.tree(t, uiSerial))
	if first.ID == second.ID {
		t.Fatalf("two reads share the snapshot id %s", first.ID)
	}
	var ref string
	for _, n := range first.Nodes {
		if n.ID == "switchWidget" {
			ref = n.Ref
		}
	}
	if ref == "" {
		t.Fatal("the recorded dump lost switchWidget")
	}
	m, err := f.adb.snapshots(uiSerial).resolve(second, Selector{Ref: ref, Snapshot: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if m.Node.ID != "switchWidget" {
		t.Errorf("ref resolved to %s", m.Node.ID)
	}
}

// Decision: one-tree evaluation of the assertion vocabulary (ui-automation.md
// §Assertion vocabulary): appears needs an on-screen match, gone its absence,
// foreground the top window's package. The two-read rule for gone is the tap engine's.
func TestConditionOneTree(t *testing.T) {
	s := recordedSettings(t)
	st := newSnapshotStore()
	cases := []struct {
		name string
		cond Condition
		want bool
	}{
		{"appears exact", Condition{Appears: &Selector{ID: "title", Text: "Brightness"}}, true},
		{"appears absent", Condition{Appears: &Selector{ID: "nope"}}, false},
		{"gone absent", Condition{Gone: &Selector{ID: "nope"}}, true},
		{"gone present", Condition{Gone: &Selector{ID: "title", Text: "Brightness"}}, false},
		{"foreground matches", Condition{Foreground: "com.android.settings"}, true},
		{"foreground other", Condition{Foreground: "com.other"}, false},
	}
	for _, c := range cases {
		got, err := st.holds(s, c.cond)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	// appears requires on-screen: an off-screen match does not hold it.
	off := mustSnapshot(t, sdump("[0,0][100,100]",
		sn("android.widget.TextView", `resource-id="com.example:id/off" text="Off" bounds="[200,200][300,300]"`)), "s-1")
	if got, err := st.holds(off, Condition{Appears: &Selector{ID: "off"}}); err != nil || got {
		t.Errorf("off-screen appears: %v, %v; want false", got, err)
	}
	// A malformed condition is REQUEST_INVALID_PARAMS.
	if e := capability.AsError(func() error { _, err := st.holds(s, Condition{}); return err }()); e == nil || e.Code != capability.ErrInvalidParams {
		t.Errorf("empty condition: %v, want REQUEST_INVALID_PARAMS", e)
	}
}

package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var uiImages = []string{"pixel9a", "pixeltablet", "mediumphonea17"}

// synthNode renders one synthetic <node>; attrs are extra attributes.
func synthNode(class, attrs, children string) string {
	return fmt.Sprintf(`<node class="%s" package="com.example" %s bounds="[0,0][10,10]">%s</node>`, class, attrs, children)
}

func synthDump(rotation int, body string) []byte {
	return []byte(fmt.Sprintf(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="%d">%s</hierarchy>`, rotation, body))
}

// refsExist asserts every children ref points at a node in nodes (and refs are unique).
func refsExist(t *testing.T, s Snapshot) {
	t.Helper()
	have := map[string]bool{}
	for _, n := range s.Nodes {
		if have[n.Ref] {
			t.Errorf("duplicate ref %s", n.Ref)
		}
		have[n.Ref] = true
	}
	for _, n := range s.Nodes {
		for _, c := range n.Children {
			if !have[c] {
				t.Errorf("node %s lists child %s that is not in nodes", n.Ref, c)
			}
		}
	}
}

// Decision: the status line adb appends after </hierarchy> (recorded on all three
// images, no newline) is tolerated, because the parser stops at the root's end. A
// truncated document is still an error.
func TestUITreeTrailingStatusLineTolerated(t *testing.T) {
	for _, img := range uiImages {
		dump := readFixture(t, img+"-uidump-devtty.xml")
		if !strings.Contains(string(dump), "</hierarchy>UI hierchary dumped to: /dev/tty") {
			t.Fatalf("%s: fixture lost its status line", img)
		}
		if _, err := BuildSnapshot(dump, "s-1", 0); err != nil {
			t.Errorf("%s: %v", img, err)
		}
	}
	// Synthetic: arbitrary trailing garbage, including bytes that are not XML.
	if _, err := BuildSnapshot(append(synthDump(0, synthNode("a.B", `text="x"`, "")), []byte("<<< not xml \x00")...), "s", 0); err != nil {
		t.Errorf("trailing garbage: %v", err)
	}
	if _, err := BuildSnapshot([]byte(`<hierarchy rotation="0"><node class="a.B" text="x" bounds="[0,0][1,1]">`), "s", 0); err == nil {
		t.Error("a truncated document must fail")
	}
}

// Decision: the daemon only ever hands out refs that resolve inside the same snapshot,
// with or without pruning.
func TestUITreeRefsExist(t *testing.T) {
	for _, img := range uiImages {
		dump := readFixture(t, img+"-uidump-devtty.xml")
		for _, cap := range []int{0, 6000, 2500, 1200} {
			s, err := BuildSnapshot(dump, "s-1", cap)
			if err != nil {
				t.Fatal(err)
			}
			refsExist(t, s)
		}
	}
}

// Decision: layout-only nodes (no id, text, desc, flag) are folded into the parent and
// their children re-parented; meaningful ones stay. Synthetic.
func TestUITreeCompaction(t *testing.T) {
	leaf := synthNode("android.widget.Button", `text="OK" clickable="true"`, "")
	body := synthNode("android.widget.FrameLayout", "",
		synthNode("android.widget.LinearLayout", `resource-id="com.example:id/box"`,
			synthNode("android.view.ViewGroup", "", synthNode("android.view.View", "", leaf))))
	s, err := BuildSnapshot(synthDump(0, body), "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 2 {
		t.Fatalf("want box + button, got %+v", s.Nodes)
	}
	box, btn := s.Nodes[0], s.Nodes[1]
	if box.ID != "box" || btn.Text != "OK" {
		t.Fatalf("wrong nodes kept: %+v", s.Nodes)
	}
	if !reflect.DeepEqual(box.Children, []string{btn.Ref}) {
		t.Errorf("button should be re-parented to box across two folded levels: %v", box.Children)
	}
	// A node with only a flag is meaningful.
	s, _ = BuildSnapshot(synthDump(0, synthNode("a.B", `scrollable="true"`, "")), "s", 0)
	if len(s.Nodes) != 1 {
		t.Errorf("a flag-only node must be kept: %+v", s.Nodes)
	}
	// Recorded: compaction removes most of the raw nodes.
	for _, img := range uiImages {
		raw := strings.Count(string(readFixture(t, img+"-uidump-devtty.xml")), "<node ")
		s, _ := BuildSnapshot(readFixture(t, img+"-uidump-devtty.xml"), "s", 0)
		if len(s.Nodes) == 0 || len(s.Nodes) >= raw {
			t.Errorf("%s: %d raw nodes, %d kept", img, raw, len(s.Nodes))
		}
		for _, n := range s.Nodes {
			if n.ID == "" && n.Text == "" && n.Desc == "" && len(n.Flags) == 0 {
				t.Errorf("%s: layout-only node %s survived", img, n.Ref)
			}
		}
	}
}

// Decision: flags list non-default states only: enabled=true and every false boolean
// are absent; enabled=false is "disabled". Synthetic.
func TestUITreeFlagsNonDefaultOnly(t *testing.T) {
	attrs := `text="t" clickable="true" scrollable="false" checkable="true" checked="false" selected="true" focused="false" password="false" enabled="false" long-clickable="true" focusable="true"`
	s, _ := BuildSnapshot(synthDump(0, synthNode("a.B", attrs, "")), "s", 0)
	want := []string{"clickable", "checkable", "selected", "disabled"}
	if !reflect.DeepEqual(s.Nodes[0].Flags, want) {
		t.Errorf("flags = %v, want %v", s.Nodes[0].Flags, want)
	}
	s, _ = BuildSnapshot(synthDump(0, synthNode("a.B", `text="t" enabled="true" clickable="false"`, "")), "s", 0)
	if len(s.Nodes[0].Flags) != 0 {
		t.Errorf("defaults must not appear: %v", s.Nodes[0].Flags)
	}
	b, _ := json.Marshal(s.Nodes[0])
	if strings.Contains(string(b), "flags") {
		t.Errorf("empty flags must be omitted: %s", b)
	}
}

// Decision: id loses its "<package>:id/" prefix, class is the short name. Both on a
// recorded dump (no raw prefix or dotted class may remain) and a synthetic one.
func TestUITreeIDAndClassShortened(t *testing.T) {
	s, _ := BuildSnapshot(synthDump(0, synthNode("android.widget.Button", `resource-id="com.example:id/login_button"`, "")+
		synthNode("android.widget.TextView", `resource-id="android:id/title"`, "")+
		synthNode("NoDots", `resource-id="bare"`, "")), "s", 0)
	got := [][2]string{}
	for _, n := range s.Nodes {
		got = append(got, [2]string{n.Class, n.ID})
	}
	want := [][2]string{{"Button", "login_button"}, {"TextView", "title"}, {"NoDots", "bare"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	for _, img := range uiImages {
		s, _ := BuildSnapshot(readFixture(t, img+"-uidump-devtty.xml"), "s", 0)
		withID := 0
		for _, n := range s.Nodes {
			if strings.Contains(n.ID, ":id/") || strings.Contains(n.Class, ".") {
				t.Errorf("%s: unshortened node %+v", img, n)
			}
			if n.ID != "" {
				withID++
			}
		}
		if withID == 0 {
			t.Errorf("%s: no node kept an id; the check proves nothing", img)
		}
	}
}

// Decision: the 40 KB cap bounds the encoded result, the deepest levels go first, and
// the cut says so (truncated, a hint). Recorded dump with a small cap, plus synthetic.
func TestUITreeCapPrunesDeepestFirst(t *testing.T) {
	dump := readFixture(t, "pixel9a-uidump-devtty.xml")
	full, _ := BuildSnapshot(dump, "s", 1<<30)
	if full.Truncated {
		t.Fatal("an uncapped snapshot must not be truncated")
	}
	fullSize := encodedSize(full)
	if fullSize > DefaultUITreeBytes {
		t.Fatalf("recorded dump already exceeds the default cap: %d", fullSize)
	}
	capBytes := fullSize * 2 / 3
	s, _ := BuildSnapshot(dump, "s", capBytes)
	if !s.Truncated || s.Hint == "" || s.EmptyReason != "" {
		t.Fatalf("want truncated with a hint and no emptyReason: %+v", s)
	}
	if got := encodedSize(s); got > capBytes {
		t.Errorf("encoded %d bytes over the cap %d", got, capBytes)
	}
	if len(s.Nodes) == 0 || len(s.Nodes) >= len(full.Nodes) {
		t.Fatalf("pruning kept %d of %d", len(s.Nodes), len(full.Nodes))
	}
	// Deepest first: no kept node is deeper than a removed node. The kept nodes are a
	// document-order prefix by level; compute levels from the full tree.
	level := map[string]int{}
	var walk func(ref string, d int)
	byRef := map[string]UINode{}
	for _, n := range full.Nodes {
		byRef[n.Ref] = n
	}
	walk = func(ref string, d int) {
		level[ref] = d
		for _, c := range byRef[ref].Children {
			walk(c, d+1)
		}
	}
	child := map[string]bool{}
	for _, n := range full.Nodes {
		for _, c := range n.Children {
			child[c] = true
		}
	}
	for _, n := range full.Nodes {
		if !child[n.Ref] {
			walk(n.Ref, 0)
		}
	}
	kept := map[string]bool{}
	maxKept := 0
	for _, n := range s.Nodes {
		kept[n.Ref] = true
		maxKept = max(maxKept, level[n.Ref])
	}
	for _, n := range full.Nodes {
		if !kept[n.Ref] && level[n.Ref] < maxKept {
			t.Errorf("node %s at level %d was removed while level %d survives", n.Ref, level[n.Ref], maxKept)
		}
	}
	refsExist(t, s)
	// Refs are not renumbered by pruning.
	for _, n := range s.Nodes {
		if !reflect.DeepEqual(n.Ref, byRef[n.Ref].Ref) || n.Text != byRef[n.Ref].Text {
			t.Errorf("ref %s changed meaning after pruning", n.Ref)
		}
	}
}

// Decision: a cap too small for even the top level yields an empty tree that says
// "pruned", not "no accessibility info". Synthetic.
func TestUITreeEmptyReasons(t *testing.T) {
	body := synthNode("a.B", `text="top"`, synthNode("a.C", `text="child"`, ""))
	s, _ := BuildSnapshot(synthDump(0, body), "s", 40)
	if len(s.Nodes) != 0 || s.EmptyReason != EmptyPruned || !s.Truncated {
		t.Errorf("pruned: %+v", s)
	}
	if b, _ := json.Marshal(s); !strings.Contains(string(b), `"nodes":[]`) {
		t.Errorf("nodes must encode as [], not null: %s", b)
	}

	// No <node> at all, and a window of only layout nodes: nothing for an agent to see.
	for name, dump := range map[string][]byte{
		"empty hierarchy": []byte(`<?xml version='1.0' ?><hierarchy rotation="1"/>`),
		"layout only":     synthDump(0, synthNode("a.B", "", synthNode("a.C", "", ""))),
	} {
		s, err := BuildSnapshot(dump, "s", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Nodes) != 0 || s.EmptyReason != EmptyNoAccessibilityInfo || s.Truncated {
			t.Errorf("%s: %+v", name, s)
		}
	}
	// A normal tree has no emptyReason.
	s, _ = BuildSnapshot(synthDump(0, body), "s", 0)
	if s.EmptyReason != "" {
		t.Errorf("non-empty tree has emptyReason %q", s.EmptyReason)
	}
}

// Decision: foreground is the root node's package, even when the root itself is folded
// away by compaction (ui-automation.md; plan step 6). Synthetic plus recorded.
func TestUITreeForeground(t *testing.T) {
	dump := synthDump(0, `<node class="a.F" package="com.root.pkg" bounds="[0,0][10,10]">`+
		`<node class="a.B" package="com.other" text="x" bounds="[0,0][1,1]"/></node>`)
	s, _ := BuildSnapshot(dump, "s", 0)
	if s.Foreground != "com.root.pkg" {
		t.Errorf("foreground = %q", s.Foreground)
	}
	for _, img := range uiImages {
		s, _ := BuildSnapshot(readFixture(t, img+"-uidump-devtty.xml"), "s", 0)
		if s.Foreground != "com.android.settings" {
			t.Errorf("%s: foreground = %q", img, s.Foreground)
		}
	}
}

// Decision: display comes from the dump (root's right/bottom edge, hierarchy rotation)
// and agrees with `wm size`, swapped when the rotation is odd (the tablet fixture).
func TestUITreeDisplayMatchesWmSize(t *testing.T) {
	rotations := map[string]int{"pixel9a": 0, "mediumphonea17": 0, "pixeltablet": 1}
	for _, img := range uiImages {
		var w, h int
		if _, err := fmt.Sscanf(string(readFixture(t, img+"-wm-size.txt")), "Physical size: %dx%d", &w, &h); err != nil {
			t.Fatal(err)
		}
		s, _ := BuildSnapshot(readFixture(t, img+"-uidump-devtty.xml"), "s", 0)
		if s.Display.Rotation != rotations[img] {
			t.Errorf("%s: rotation %d", img, s.Display.Rotation)
		}
		if s.Display.Rotation%2 == 1 {
			w, h = h, w
		}
		if s.Display.Width != w || s.Display.Height != h {
			t.Errorf("%s: display %dx%d, wm size (rotation applied) %dx%d", img, s.Display.Width, s.Display.Height, w, h)
		}
	}
}

// Decision: the published shape. The JSON names are the ones ui-automation.md draws,
// and nodes, foreground and display carry the untrusted tag the schema step reads.
func TestUITreeShape(t *testing.T) {
	s, _ := BuildSnapshot(synthDump(2, synthNode("a.B", `text="x" clickable="true"`, "")), "s-81c2", 0)
	b, _ := json.Marshal(s)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"snapshot", "foreground", "display", "nodes"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q in %s", k, b)
		}
	}
	for _, k := range []string{"truncated", "hint", "emptyReason"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q must be omitted when unset: %s", k, b)
		}
	}
	n := m["nodes"].([]any)[0].(map[string]any)
	for _, k := range []string{"ref", "class", "text", "bounds", "flags"} {
		if _, ok := n[k]; !ok {
			t.Errorf("node missing %q: %s", k, b)
		}
	}
	rt := reflect.TypeOf(Snapshot{})
	for _, f := range []string{"Foreground", "Display", "Nodes"} {
		if sf, _ := rt.FieldByName(f); sf.Tag.Get("untrusted") != "true" {
			t.Errorf("%s lacks the untrusted tag", f)
		}
	}
	for _, f := range []string{"ID", "Truncated", "Hint", "EmptyReason"} {
		if sf, _ := rt.FieldByName(f); sf.Tag.Get("untrusted") != "" {
			t.Errorf("%s must not be untrusted", f)
		}
	}
}

// Logs the compact size of each recorded dump (go test -v) and holds the default cap.
func TestUITreeRecordedSizes(t *testing.T) {
	for _, img := range uiImages {
		s, _ := BuildSnapshot(readFixture(t, img+"-uidump-devtty.xml"), NewSnapshotID(), 0)
		n := encodedSize(s)
		t.Logf("%s: %d nodes, %d bytes compact JSON", img, len(s.Nodes), n)
		if n > DefaultUITreeBytes || s.Truncated {
			t.Errorf("%s: %d bytes, truncated=%v", img, n, s.Truncated)
		}
	}
}

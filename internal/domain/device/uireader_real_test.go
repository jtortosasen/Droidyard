package device

import (
	"slices"
	"testing"
)

// Decision (ADR-0029: the reader's output is what the parser already reads): the bytes
// the real reader.dex printed on the Pixel_9a showing Settings > Display
// (pixel9a-reader-display.xml, recorded by running the embedded dex as dump.go runs it)
// give, through BuildSnapshot, the same snapshot as the recorded `uiautomator dump` of
// that screen: same foreground, display, rotation, and every kept node's class, id,
// text, desc, bounds, flags and children. Only the snapshot id and the battery summary
// text (a percentage, dynamic) are left out of the comparison. Observed on recording:
// every node attribute was identical; the only byte difference was that the reader
// writes a leaf as <node ...></node> where uiautomator writes <node ... />.
func TestRealReaderOutputBuildsTheSameSnapshotAsTheRecordedDump(t *testing.T) {
	rd, err := BuildSnapshot([]byte(fixture(t, "pixel9a-reader-display.xml")), "s-reader", DefaultUITreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	ud, err := BuildSnapshot([]byte(pixelDoc(t)), "s-dump", DefaultUITreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	// Literals, so that two empty or identically wrong snapshots do not pass.
	if rd.Foreground != "com.android.settings" || rd.Display != (UIDisplay{Width: 1080, Height: 2424, Rotation: 0}) {
		t.Errorf("foreground %q, display %+v", rd.Foreground, rd.Display)
	}
	if len(rd.Nodes) != 41 || rd.Truncated || rd.EmptyReason != "" {
		t.Errorf("%d nodes, truncated %v, empty reason %q; want 41 kept nodes", len(rd.Nodes), rd.Truncated, rd.EmptyReason)
	}
	want := map[string]UINode{
		"n3":  {Ref: "n3", Class: "FrameLayout", ID: "collapsing_toolbar", Desc: "Display & touch", Bounds: [4]int{0, 142, 1080, 289}, Children: []string{"n4"}},
		"n5":  {Ref: "n5", Class: "ImageButton", Desc: "Navigate up", Bounds: [4]int{21, 142, 168, 289}, Flags: []string{"clickable"}},
		"n11": {Ref: "n11", Class: "TextView", ID: "title", Text: "Brightness", Bounds: [4]int{63, 352, 1038, 403}},
		"n28": {Ref: "n28", Class: "Switch", ID: "switchWidget", Desc: "Dark theme", Bounds: [4]int{859, 1333, 996, 1459}, Flags: []string{"clickable", "checkable"}},
		"n31": {Ref: "n31", Class: "TextView", ID: "summary", Text: "Font size, display size, bold text, outline text", Bounds: [4]int{84, 1596, 796, 1647}},
	}
	got := map[string]UINode{}
	for _, n := range rd.Nodes {
		got[n.Ref] = n
	}
	for ref, w := range want {
		if g := got[ref]; !equalNode(g, w) {
			t.Errorf("%s from the real reader: %+v, want %+v", ref, g, w)
		}
	}
	if rd.Foreground != ud.Foreground || rd.Display != ud.Display || len(rd.Nodes) != len(ud.Nodes) {
		t.Fatalf("reader and dump differ: %q %+v %d nodes against %q %+v %d", rd.Foreground, rd.Display, len(rd.Nodes), ud.Foreground, ud.Display, len(ud.Nodes))
	}
	for i := range rd.Nodes {
		r, u := rd.Nodes[i], ud.Nodes[i]
		if r.ID == "summary" && r.Text == "83%" { // the battery level: dynamic
			r.Text, u.Text = "", ""
		}
		if !equalNode(r, u) {
			t.Errorf("node %d: reader %+v, dump %+v", i, r, u)
		}
	}
}

func equalNode(a, b UINode) bool {
	return a.Ref == b.Ref && a.Class == b.Class && a.ID == b.ID && a.Text == b.Text && a.Desc == b.Desc &&
		a.Bounds == b.Bounds && slices.Equal(a.Flags, b.Flags) && slices.Equal(a.Children, b.Children)
}

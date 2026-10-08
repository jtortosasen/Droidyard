package device

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
)

// The pure half of device.uiTree: a `uiautomator dump` XML in, the Snapshot of
// ui-automation.md §The tree out, in the flat shape milestone 1e Q1 settles. No adb,
// no capability registration; the handler (step 6) supplies the bytes and the id.

// DefaultUITreeBytes is the byte cap on the encoded snapshot (plan m1e, step 6).
const DefaultUITreeBytes = 40 * 1024

// The values of Snapshot.EmptyReason this package produces. noMatch is `within`
// matching nothing (plan m1f step 4), pruned the byte cap, noAccessibilityInfo a
// window with no accessible nodes.
const (
	EmptyNoMatch             = "noMatch"
	EmptyPruned              = "pruned"
	EmptyNoAccessibilityInfo = "noAccessibilityInfo"
)

// Snapshot is the compact UI tree. Refs are valid with their snapshot id only.
// nodes, foreground and display carry device or app text, so they are untrusted whole
// fields (ADR-0015 §1); the id, truncated, hint and emptyReason are the daemon's.
type Snapshot struct {
	ID          string    `json:"snapshot" jsonschema:"required"`
	Foreground  string    `json:"foreground" jsonschema:"required" untrusted:"true"`
	Display     UIDisplay `json:"display" jsonschema:"required" untrusted:"true"`
	Nodes       []UINode  `json:"nodes" jsonschema:"required" untrusted:"true"`
	Truncated   bool      `json:"truncated,omitempty"`
	Hint        string    `json:"hint,omitempty"`
	EmptyReason string    `json:"emptyReason,omitempty"`
	// SystemDialog is the system error dialog the tree shows, when it holds the dialog's
	// buttons (ADR-0034 §3); its package is device-derived, its kind the daemon's.
	SystemDialog *SystemDialog `json:"systemDialog,omitempty"`
	// Warnings are adb's (client/server version mismatch), not the device's: untagged.
	Warnings []string `json:"warnings,omitempty"`
}

// UIDisplay is the root window's extent and the hierarchy's rotation.
type UIDisplay struct {
	Width    int `json:"width" jsonschema:"required"`
	Height   int `json:"height" jsonschema:"required"`
	Rotation int `json:"rotation" jsonschema:"required"`
}

// UINode is one kept node. Children are refs of nodes that exist in Snapshot.Nodes.
type UINode struct {
	Ref      string   `json:"ref" jsonschema:"required"`
	Class    string   `json:"class,omitempty"`
	ID       string   `json:"id,omitempty"`
	Text     string   `json:"text,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	Bounds   [4]int   `json:"bounds" jsonschema:"required"`
	Flags    []string `json:"flags,omitempty"`
	Children []string `json:"children,omitempty"`

	// drawingOrder is the node's `drawing-order` from the dump, kept for occlusion
	// (ui-automation.md §Resolution): the child drawn last covers its siblings. It is
	// unexported on purpose, so it never reaches the published snapshot (plan m1f D22).
	drawingOrder int

	// hint is the node's `hint` from the dump (the reader writes it, plan m1n step 5): a
	// field showing its hint reads back as the hint rather than as empty, so an acting
	// request's read-back comparison treats a value equal to it as empty. Unexported on
	// purpose, so it never reaches the published snapshot (plan m1f D22).
	hint string
}

// NewSnapshotID returns a fresh id like "s-81c2ab34". The plan's D16 fixes the shape:
// `s-` plus 8 hex digits, unique among the snapshots the daemon keeps. A counter seeded
// from crypto/rand makes 10,000 mints in one process distinct (a snapshot id is opaque
// to callers; the per-device store below is what bounds how long it stays valid).
var snapshotSeq atomic.Uint32

func init() {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	snapshotSeq.Store(binary.BigEndian.Uint32(b[:]))
}

func NewSnapshotID() string {
	return "s-" + fmt.Sprintf("%08x", snapshotSeq.Add(1))
}

// rawNode is a parsed <node>, before compaction.
type rawNode struct {
	class, pkg, id, text, desc, hint string
	bounds                           [4]int
	drawing                          int
	flags                            []string
	children                         []*rawNode
}

// meaningful reports whether a node survives compaction: an id, text, desc or flag.
func (n *rawNode) meaningful() bool {
	return n.id != "" || n.text != "" || n.desc != "" || len(n.flags) > 0
}

// parseDump reads the hierarchy and stops at its end tag: adb appends a status line
// ("UI hierchary dumped to: /dev/tty") after </hierarchy>, which is never read.
func parseDump(dump []byte) (rotation int, roots []*rawNode, err error) {
	dec := xml.NewDecoder(bytes.NewReader(dump))
	var stack []*rawNode
	seenRoot := false
	for {
		tok, terr := dec.Token()
		if terr != nil {
			if errors.Is(terr, io.EOF) {
				return 0, nil, errors.New("ui dump: no complete <hierarchy> element")
			}
			return 0, nil, fmt.Errorf("ui dump: %w", terr)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "hierarchy" && !seenRoot:
				seenRoot = true
				for _, a := range t.Attr {
					if a.Name.Local == "rotation" {
						rotation, _ = strconv.Atoi(a.Value)
					}
				}
			case t.Name.Local == "node" && seenRoot:
				n := nodeFromAttrs(t.Attr)
				if len(stack) == 0 {
					roots = append(roots, n)
				} else {
					p := stack[len(stack)-1]
					p.children = append(p.children, n)
				}
				stack = append(stack, n)
			case seenRoot:
				// An element of a kind this parser does not know: skip its subtree.
				if err := dec.Skip(); err != nil {
					return 0, nil, fmt.Errorf("ui dump: %w", err)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "node" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			} else if t.Name.Local == "hierarchy" && seenRoot {
				return rotation, roots, nil
			}
		}
	}
}

func nodeFromAttrs(attrs []xml.Attr) *rawNode {
	n := &rawNode{}
	enabled := true
	on := map[string]bool{}
	for _, a := range attrs {
		switch a.Name.Local {
		case "class":
			n.class = a.Value
		case "package":
			n.pkg = a.Value
		case "resource-id":
			n.id = a.Value
		case "text":
			n.text = a.Value
		case "content-desc":
			n.desc = a.Value
		case "hint":
			n.hint = a.Value
		case "bounds":
			n.bounds = parseBounds(a.Value)
		case "drawing-order":
			n.drawing, _ = strconv.Atoi(a.Value)
		case "enabled":
			enabled = a.Value != "false"
		case "clickable", "scrollable", "checkable", "checked", "selected", "focused", "password", "editable":
			on[a.Name.Local] = a.Value == "true"
		}
	}
	// editable is written by the reader (plan m1n step 5, fix round L1), not by
	// `uiautomator dump`, so a node read through the dump path never carries it.
	for _, f := range []string{"clickable", "scrollable", "checkable", "checked", "selected", "focused", "password", "editable"} {
		if on[f] {
			n.flags = append(n.flags, f)
		}
	}
	if !enabled {
		n.flags = append(n.flags, "disabled")
	}
	if i := strings.LastIndex(n.id, ":id/"); i >= 0 {
		n.id = n.id[i+len(":id/"):]
	}
	if i := strings.LastIndex(n.class, "."); i >= 0 {
		n.class = n.class[i+1:]
	}
	return n
}

// parseBounds reads "[l,t][r,b]"; a malformed value is zero bounds, not a failed dump.
func parseBounds(s string) (b [4]int) {
	var l, t, r, bt int
	if _, err := fmt.Sscanf(s, "[%d,%d][%d,%d]", &l, &t, &r, &bt); err == nil {
		b = [4]int{l, t, r, bt}
	}
	return b
}

// BuildSnapshot turns a `uiautomator dump` (trailing bytes after </hierarchy> allowed)
// into the compact snapshot with the given id, within maxBytes of encoded JSON
// (DefaultUITreeBytes when maxBytes <= 0).
//
// foreground is the first root node's package; display is that node's right and bottom
// edges (the window origin can be offset, as in the tablet dump) with the hierarchy's
// rotation. Layout-only nodes are folded into their parent and their children
// re-parented. Refs are assigned in document order after compaction and survive
// pruning unchanged; when the cap bites, the deepest levels go first.
func BuildSnapshot(dump []byte, id string, maxBytes int) (Snapshot, error) {
	return buildSnapshot(dump, id, maxBytes, nil)
}

// buildSnapshot is BuildSnapshot with `within`: only the subtrees of the nodes matching
// the ancestor selector are returned, before the cap, and emptyReason is noMatch when
// nothing matched (plan m1f D6; ui-automation.md §The tree).
func buildSnapshot(dump []byte, id string, maxBytes int, within *AncestorSelector) (Snapshot, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultUITreeBytes
	}
	rotation, roots, err := parseDump(dump)
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{ID: id, Nodes: []UINode{}, Display: UIDisplay{Rotation: rotation}}
	if len(roots) > 0 {
		s.Foreground = roots[0].pkg
		s.Display.Width, s.Display.Height = roots[0].bounds[2], roots[0].bounds[3]
	}

	var depths []int
	var flatten func(n *rawNode, parent, depth int)
	flatten = func(n *rawNode, parent, depth int) {
		if !n.meaningful() {
			for _, c := range n.children {
				flatten(c, parent, depth)
			}
			return
		}
		idx := len(s.Nodes)
		s.Nodes = append(s.Nodes, UINode{
			Ref: "n" + strconv.Itoa(idx), Class: n.class, ID: n.id, Text: n.text,
			Desc: n.desc, Bounds: n.bounds, Flags: n.flags, drawingOrder: n.drawing,
			hint: n.hint,
		})
		depths = append(depths, depth)
		if parent >= 0 {
			s.Nodes[parent].Children = append(s.Nodes[parent].Children, s.Nodes[idx].Ref)
		}
		for _, c := range n.children {
			flatten(c, idx, depth+1)
		}
	}
	for _, r := range roots {
		flatten(r, -1, 0)
	}
	if len(s.Nodes) == 0 {
		s.EmptyReason = EmptyNoAccessibilityInfo
		return s, nil
	}
	if within != nil {
		s, depths = withinSubtrees(s, depths, *within)
		if len(s.Nodes) == 0 {
			s.EmptyReason = EmptyNoMatch
			return s, nil
		}
	}

	removed := 0
	for encodedSize(s) > maxBytes && len(s.Nodes) > 0 {
		deepest := 0
		for _, d := range depths {
			deepest = max(deepest, d)
		}
		var kept []UINode
		var keptDepths []int
		gone := map[string]bool{}
		for i, n := range s.Nodes {
			if depths[i] == deepest {
				gone[n.Ref] = true
				continue
			}
			kept = append(kept, n)
			keptDepths = append(keptDepths, depths[i])
		}
		for i := range kept {
			var cs []string
			for _, c := range kept[i].Children {
				if !gone[c] {
					cs = append(cs, c)
				}
			}
			kept[i].Children = cs
		}
		if kept == nil {
			kept = []UINode{}
		}
		s.Nodes, depths = kept, keptDepths
		removed++
		s.Truncated = true
		s.Hint = fmt.Sprintf("output cut at %d bytes: the %d deepest level(s) were removed; narrow the tree with within", maxBytes, removed)
	}
	if len(s.Nodes) == 0 {
		s.EmptyReason = EmptyPruned
	}
	return s, nil
}

// withinSubtrees keeps the matching nodes and all of their descendants, in document
// order, dropping the rest; children lists are rewritten to the kept refs. depths stays
// parallel to the kept nodes, for the cap.
func withinSubtrees(s Snapshot, depths []int, within AncestorSelector) (Snapshot, []int) {
	at := make(map[string]int, len(s.Nodes))
	for i, n := range s.Nodes {
		at[n.Ref] = i
	}
	keep := make(map[string]bool)
	var mark func(ref string)
	mark = func(ref string) {
		if keep[ref] {
			return
		}
		keep[ref] = true
		for _, c := range s.Nodes[at[ref]].Children {
			mark(c)
		}
	}
	for _, n := range s.Nodes {
		if within.matches(n) {
			mark(n.Ref)
		}
	}
	out := s
	out.Nodes = make([]UINode, 0, len(s.Nodes))
	keptDepths := make([]int, 0, len(s.Nodes))
	for i, n := range s.Nodes {
		if !keep[n.Ref] {
			continue
		}
		var cs []string
		for _, c := range n.Children {
			if keep[c] {
				cs = append(cs, c)
			}
		}
		n.Children = cs
		out.Nodes = append(out.Nodes, n)
		keptDepths = append(keptDepths, depths[i])
	}
	return out, keptDepths
}

func encodedSize(s Snapshot) int {
	b, _ := json.Marshal(s)
	return len(b)
}

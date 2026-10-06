package device

import "github.com/jtortosasen/droidyard/internal/capability"

// Occlusion-aware tap points (ui-automation.md §Resolution; plan m1f, D22): the reader
// keeps every node's drawing-order, so a target whose centre another node covers is
// tapped at the largest visible strip instead, or refused DEVICE_ELEMENT_OFFSCREEN when
// nothing visible is left. Without this a tap at the centre of a node under an action
// bar hits the bar and silently does nothing.

// tapFloor is the margin a tap point keeps from the target's edge where the target is
// large enough for it, and the least dimension a visible strip must have.
const tapFloor = 8

// occlusion is the chosen tap point: the point, whether the target's centre was covered
// (so the result warns) and the covering node that forced the move.
type occlusion struct {
	Point    [2]int
	Moved    bool
	Covering *UINode
}

// chooseTapPoint picks where to tap the target: the centre of its visible bounds when no
// node drawn above it covers that centre, otherwise the centre of the largest strip left
// uncovered, at least tapFloor px inside the target (ui-automation.md §Resolution; D22).
func chooseTapPoint(s Snapshot, target UINode) (occlusion, error) {
	visible, ok := visibleRect(target.Bounds, s.Display)
	if !ok {
		return occlusion{Point: tapPoint(target, s.Display)}, nil
	}
	centre := visible.centre()
	covers := coveringNodes(s, target)
	cover := coveringAt(covers, centre)
	if cover == nil {
		return occlusion{Point: centre}, nil
	}
	free, ok := largestFreeStrip(visible, covers)
	if !ok {
		return occlusion{}, coveredTapError(target, covers)
	}
	return occlusion{Point: free.centre(), Moved: true, Covering: cover}, nil
}

// rect is a half-open rectangle [l,r) × [t,b), in display pixels.
type rect struct{ l, t, r, b int }

func (r rect) w() int         { return r.r - r.l }
func (r rect) h() int         { return r.b - r.t }
func (r rect) area() int      { return r.w() * r.h() }
func (r rect) centre() [2]int { return [2]int{(r.l + r.r) / 2, (r.t + r.b) / 2} }
func rectOf(b [4]int) rect    { return rect{b[0], b[1], b[2], b[3]} }
func intersect(a, b rect) (rect, bool) {
	l, t := max(a.l, b.l), max(a.t, b.t)
	r, bt := min(a.r, b.r), min(a.b, b.b)
	if l >= r || t >= bt {
		return rect{}, false
	}
	return rect{l, t, r, bt}, true
}

// visibleRect is the target's bounds clipped to the display, false when nothing is left.
func visibleRect(b [4]int, d UIDisplay) (rect, bool) {
	r := rectOf(b)
	if d.Width > 0 && r.r > d.Width {
		r.r = d.Width
	}
	if d.Height > 0 && r.b > d.Height {
		r.b = d.Height
	}
	if r.l < 0 {
		r.l = 0
	}
	if r.t < 0 {
		r.t = 0
	}
	if r.l >= r.r || r.t >= r.b {
		return rect{}, false
	}
	return r, true
}

// coveringNodes are the nodes that can hide part of target: neither its ancestor nor its
// descendant, not a full-display overlay (usually transparent), and drawn above it.
func coveringNodes(s Snapshot, target UINode) []UINode {
	ti := indexTree(s)
	tr := rectOf(target.Bounds)
	var out []UINode
	for _, o := range s.Nodes {
		if o.Ref == target.Ref || wholeDisplay(o.Bounds, s.Display) {
			continue
		}
		// Only a node that overlaps the target can cover it; one that merely touches an
		// edge (a sibling below in a vertical list) cannot.
		if _, ok := intersect(tr, rectOf(o.Bounds)); !ok {
			continue
		}
		if isAncestorRef(ti, o.Ref, target.Ref) || isAncestorRef(ti, target.Ref, o.Ref) {
			continue
		}
		if drawnAbove(s, ti, o, target) {
			out = append(out, o)
		}
	}
	return out
}

// isAncestorRef reports whether ancestor is on ref's parent chain.
func isAncestorRef(ti treeIndex, ancestor, ref string) bool {
	for p := ti.parent[ref]; p != ""; p = ti.parent[p] {
		if p == ancestor {
			return true
		}
	}
	return false
}

// wholeDisplay reports whether the bounds span the whole display: a full-screen overlay
// is usually transparent and must not be read as covering (ui-automation.md §Resolution).
func wholeDisplay(b [4]int, d UIDisplay) bool {
	if d.Width <= 0 || d.Height <= 0 {
		return false
	}
	return b[0] <= 0 && b[1] <= 0 && b[2] >= d.Width && b[3] >= d.Height
}

// drawnAbove reports whether o is drawn above target: at their lowest common ancestor,
// the child on o's side has a higher drawing-order than the child on target's side.
func drawnAbove(s Snapshot, ti treeIndex, o, target UINode) bool {
	lca, ok := lowestCommonAncestor(ti, o.Ref, target.Ref)
	if !ok {
		return false // different roots: no common drawing context
	}
	on, ok1 := ti.node(s, childTowards(ti, lca, o.Ref))
	tn, ok2 := ti.node(s, childTowards(ti, lca, target.Ref))
	if !ok1 || !ok2 {
		return false
	}
	return on.drawingOrder > tn.drawingOrder
}

// lowestCommonAncestor is the deepest node on both refs' parent chains, false when they
// share none (for instance two windows).
func lowestCommonAncestor(ti treeIndex, a, b string) (string, bool) {
	seen := make(map[string]bool, 8)
	for x := a; x != ""; x = ti.parent[x] {
		seen[x] = true
	}
	for y := b; y != ""; y = ti.parent[y] {
		if seen[y] {
			return y, true
		}
	}
	return "", false
}

// childTowards is the child of ancestor on ref's branch; ref itself when it is not below
// ancestor (the callers never ask that).
func childTowards(ti treeIndex, ancestor, ref string) string {
	for x := ref; x != ""; x = ti.parent[x] {
		if ti.parent[x] == ancestor {
			return x
		}
	}
	return ref
}

// coveringAt is the first cover whose bounds contain p, nil when none does.
func coveringAt(covers []UINode, p [2]int) *UINode {
	for i := range covers {
		b := covers[i].Bounds
		if p[0] >= b[0] && p[0] < b[2] && p[1] >= b[1] && p[1] < b[3] {
			return &covers[i]
		}
	}
	return nil
}

// largestFreeStrip is the largest rectangle of the target no cover overlaps whose centre
// is at least tapFloor px inside the target; false when none is left.
func largestFreeStrip(target rect, covers []UINode) (rect, bool) {
	free := []rect{target}
	for _, c := range covers {
		cr := rectOf(c.Bounds)
		var next []rect
		for _, r := range free {
			next = append(next, subtractRect(r, cr)...)
		}
		free = next
	}
	var best rect
	found := false
	for _, r := range free {
		if !insideByFloor(r.centre(), target) {
			continue
		}
		if !found || r.area() > best.area() {
			best, found = r, true
		}
	}
	return best, found
}

// subtractRect splits r into the pieces left after removing c: the strips above, below,
// left and right of their intersection, so a cover never splits a free strip.
func subtractRect(r, c rect) []rect {
	inter, ok := intersect(r, c)
	if !ok {
		return []rect{r}
	}
	var out []rect
	if r.t < inter.t {
		out = append(out, rect{r.l, r.t, r.r, inter.t})
	}
	if inter.b < r.b {
		out = append(out, rect{r.l, inter.b, r.r, r.b})
	}
	if r.l < inter.l {
		out = append(out, rect{r.l, inter.t, inter.l, inter.b})
	}
	if inter.r < r.r {
		out = append(out, rect{inter.r, inter.t, r.r, inter.b})
	}
	return out
}

// insideByFloor reports whether p is at least tapFloor px inside r on each axis where r
// is wide enough for that margin to exist.
func insideByFloor(p [2]int, r rect) bool {
	if p[0] < r.l || p[0] >= r.r || p[1] < r.t || p[1] >= r.b {
		return false
	}
	if r.w() >= 2*tapFloor && (p[0] < r.l+tapFloor || p[0] > r.r-tapFloor) {
		return false
	}
	if r.h() >= 2*tapFloor && (p[1] < r.t+tapFloor || p[1] > r.b-tapFloor) {
		return false
	}
	return true
}

// coveredTapError is DEVICE_ELEMENT_OFFSCREEN when every visible point of the target is
// covered by a node drawn above it, naming the covering node so the caller can explain.
func coveredTapError(target UINode, covers []UINode) *capability.Error {
	details := map[string]any{"node": candidateOf(target)}
	if len(covers) > 0 {
		details["covering"] = candidatesOf(covers, 1)
	}
	return capability.NewError(ErrElementOffscreen, "the matching node is covered by another node, so no visible point is left to tap").
		WithDetails(details).
		WithNextStep("Scroll the node clear of the covering UI, or take a fresh device.uiTree and retry; nothing was tapped.")
}

// tapMovedWarning says the tap point moved off the covered centre, naming the covering
// node by class and id only (plan m1f, D22).
func tapMovedWarning(o UINode) string {
	name := o.Class
	if name == "" {
		name = "a node"
	}
	if o.ID != "" {
		name += " (id " + o.ID + ")"
	}
	return "The node's centre was covered by " + name + ", so the tap was placed at the largest visible part of the node instead."
}

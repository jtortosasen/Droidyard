package device

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The selector codes (ui-automation.md §Failure modes; plan m1f, R7). They are the
// resolution errors of a tap target and of an expectation over the tree.
const (
	ErrElementNotFound  capability.ErrorCode = "DEVICE_ELEMENT_NOT_FOUND"
	ErrElementAmbiguous capability.ErrorCode = "DEVICE_ELEMENT_AMBIGUOUS"
	ErrElementOffscreen capability.ErrorCode = "DEVICE_ELEMENT_OFFSCREEN"
	ErrElementDisabled  capability.ErrorCode = "DEVICE_ELEMENT_DISABLED"
	ErrStaleRef         capability.ErrorCode = "DEVICE_STALE_REF"
)

// AncestorSelector names a node that must be an ancestor of a Selector's target
// (ui-automation.md §Selectors; plan m1f D6). It is deliberately not recursive: it has
// no within of its own, so the published schema stays inlined (milestone 1e, Q1).
type AncestorSelector struct {
	ID    string `json:"id,omitempty" jsonschema_description:"resource-id, without its package prefix; exact."`
	Text  string `json:"text,omitempty" jsonschema_description:"Text, exact."`
	Desc  string `json:"desc,omitempty" jsonschema_description:"Content description, exact."`
	Class string `json:"class,omitempty" jsonschema_description:"Short class name, e.g. Button; exact."`
}

// Selector is the one grammar for a tap target, for `within`, and inside expectations
// (ui-automation.md §Selectors). Fields id, text, desc, class combine with AND; within
// restricts the target to descendants of an ancestor; index (0-based, document order)
// breaks an ambiguity deliberately; ref names a node from a kept snapshot by identity.
type Selector struct {
	ID       string            `json:"id,omitempty" jsonschema_description:"resource-id, without its package prefix; exact."`
	Text     string            `json:"text,omitempty" jsonschema_description:"Text: exact first, then case-insensitive contains."`
	Desc     string            `json:"desc,omitempty" jsonschema_description:"Content description: exact first, then case-insensitive contains."`
	Class    string            `json:"class,omitempty" jsonschema_description:"Short class name, e.g. Button; exact."`
	Within   *AncestorSelector `json:"within,omitempty" jsonschema_description:"Only nodes inside a node matching these fields exactly; that node itself is excluded."`
	Index    *int              `json:"index,omitempty" jsonschema:"minimum=0" jsonschema_description:"0-based document-order position, to pick one of several matches."`
	Ref      string            `json:"ref,omitempty" jsonschema_description:"A node ref from an earlier snapshot; needs snapshot, and cannot combine with other fields."`
	Snapshot string            `json:"snapshot,omitempty" jsonschema_description:"The snapshot the ref came from."`
}

// Matched is one resolved target: the node and how the selector found it.
type Matched struct {
	Node      UINode
	MatchedBy string // "exact" or "contains"
}

// Candidate is one node named in a resolution error's details: ref, class, id, text and
// bounds (ui-automation.md §Resolution; plan m1f D11).
type Candidate struct {
	Ref    string `json:"ref"`
	Class  string `json:"class,omitempty"`
	ID     string `json:"id,omitempty"`
	Text   string `json:"text,omitempty"`
	Bounds [4]int `json:"bounds"`
}

// Condition is one expectation over a tree (ui-automation.md §Assertion vocabulary).
// Exactly one of its fields is set. `gone` is evaluated against one tree here; the
// two-consecutive-reads rule is the tap engine's (plan m1f step 6).
type Condition struct {
	Appears    *Selector `json:"appears,omitempty" jsonschema_description:"Holds when at least one on-screen node matches."`
	Gone       *Selector `json:"gone,omitempty" jsonschema_description:"Holds when no node matches."`
	Foreground string    `json:"foreground,omitempty" jsonschema_description:"Holds when the top window belongs to this applicationId."`
}

// validate rejects a malformed selector with REQUEST_INVALID_PARAMS (plan m1f step 4):
// no field, ref without snapshot or with other fields, a negative index, an empty
// within.
func (s Selector) validate() error {
	if s.Index != nil && *s.Index < 0 {
		return capability.Errorf(capability.ErrInvalidParams, "index must be 0 or more, got %d", *s.Index)
	}
	if s.Ref != "" {
		if s.Snapshot == "" {
			return capability.Errorf(capability.ErrInvalidParams, "ref %q needs the snapshot it was returned with; pass snapshot too", s.Ref)
		}
		if s.ID != "" || s.Text != "" || s.Desc != "" || s.Class != "" || s.Within != nil || s.Index != nil {
			return capability.Errorf(capability.ErrInvalidParams, "ref names one node by identity and cannot be combined with id, text, desc, class, within or index")
		}
		return nil
	}
	if s.Snapshot != "" {
		return capability.Errorf(capability.ErrInvalidParams, "snapshot %q was given without a ref: a snapshot is only meaningful with a ref", s.Snapshot)
	}
	if s.ID == "" && s.Text == "" && s.Desc == "" && s.Class == "" && s.Within == nil {
		return capability.Errorf(capability.ErrInvalidParams, "the selector has no field: give at least one of id, text, desc, class, within, or ref with snapshot")
	}
	return s.Within.validate("within")
}

func (a *AncestorSelector) validate(where string) error {
	if a == nil {
		return nil
	}
	if a.ID == "" && a.Text == "" && a.Desc == "" && a.Class == "" {
		return capability.Errorf(capability.ErrInvalidParams, "%s has no field: give at least one of id, text, desc, class", where)
	}
	return nil
}

// validate rejects a malformed condition: exactly one of appears, gone and foreground.
func (c Condition) validate() error {
	n := 0
	if c.Appears != nil {
		n++
	}
	if c.Gone != nil {
		n++
	}
	if c.Foreground != "" {
		n++
	}
	if n != 1 {
		return capability.Errorf(capability.ErrInvalidParams, "a condition names exactly one of appears, gone and foreground")
	}
	if c.Appears != nil {
		return c.Appears.validate()
	}
	if c.Gone != nil {
		return c.Gone.validate()
	}
	return nil
}

// matches reports whether n matches every given field exactly, for `within` ancestry.
func (a AncestorSelector) matches(n UINode) bool {
	if a.ID != "" && a.ID != n.ID {
		return false
	}
	if a.Text != "" && a.Text != n.Text {
		return false
	}
	if a.Desc != "" && a.Desc != n.Desc {
		return false
	}
	return a.Class == "" || a.Class == n.Class
}

// matchesExact is resolution step 1: every given field equals.
func (s Selector) matchesExact(n UINode) bool {
	if s.ID != "" && s.ID != n.ID {
		return false
	}
	if s.Text != "" && s.Text != n.Text {
		return false
	}
	if s.Desc != "" && s.Desc != n.Desc {
		return false
	}
	return s.Class == "" || s.Class == n.Class
}

// matchesContains is resolution step 2: case-insensitive contains on text and desc;
// id and class stay exact.
func (s Selector) matchesContains(n UINode) bool {
	if s.ID != "" && s.ID != n.ID {
		return false
	}
	if s.Class != "" && s.Class != n.Class {
		return false
	}
	if s.Text != "" && !containsFold(n.Text, s.Text) {
		return false
	}
	return s.Desc == "" || containsFold(n.Desc, s.Desc)
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// treeIndex is a snapshot's ref lookup and parent links, built once per operation.
type treeIndex struct {
	at     map[string]int
	parent map[string]string
}

func indexTree(s Snapshot) treeIndex {
	ti := treeIndex{at: make(map[string]int, len(s.Nodes)), parent: make(map[string]string, len(s.Nodes))}
	for i, n := range s.Nodes {
		ti.at[n.Ref] = i
	}
	for _, n := range s.Nodes {
		for _, c := range n.Children {
			ti.parent[c] = n.Ref
		}
	}
	return ti
}

func (ti treeIndex) node(s Snapshot, ref string) (UINode, bool) {
	i, ok := ti.at[ref]
	if !ok {
		return UINode{}, false
	}
	return s.Nodes[i], true
}

// withinOK reports whether n has an ancestor matching the within selector. A nil
// selector restricts nothing.
func withinOK(s Snapshot, ti treeIndex, n UINode, within *AncestorSelector) bool {
	if within == nil {
		return true
	}
	for p := ti.parent[n.Ref]; p != ""; p = ti.parent[p] {
		if a, ok := ti.node(s, p); ok && within.matches(a) {
			return true
		}
	}
	return false
}

// onScreen is resolution step 4: a node with zero-area bounds, or none intersecting
// the display, is off-screen.
func onScreen(n UINode, d UIDisplay) bool {
	l, t, r, b := n.Bounds[0], n.Bounds[1], n.Bounds[2], n.Bounds[3]
	if r <= l || b <= t {
		return false
	}
	if d.Width <= 0 || d.Height <= 0 {
		return true
	}
	return r > 0 && l < d.Width && b > 0 && t < d.Height
}

func isDisabled(n UINode) bool { return slices.Contains(n.Flags, "disabled") }

func candidateOf(n UINode) Candidate {
	return Candidate{Ref: n.Ref, Class: n.Class, ID: n.ID, Text: n.Text, Bounds: n.Bounds}
}

func candidatesOf(nodes []UINode, limit int) []Candidate {
	out := make([]Candidate, 0, min(limit, len(nodes)))
	for _, n := range nodes {
		if len(out) >= limit {
			break
		}
		out = append(out, candidateOf(n))
	}
	return out
}

// snapshotStore keeps the last keptSnapshots snapshots per device, so a ref returned
// with one can be resolved by identity against a later tree (plan m1f D16). An id that
// is not here (never kept, or evicted) is stale.
const keptSnapshots = 16

type snapshotStore struct {
	mu    sync.Mutex
	order []string
	byID  map[string]Snapshot
}

func newSnapshotStore() *snapshotStore {
	return &snapshotStore{byID: map[string]Snapshot{}}
}

func (st *snapshotStore) keep(s Snapshot) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.byID[s.ID]; ok {
		return
	}
	st.order = append(st.order, s.ID)
	st.byID[s.ID] = s
	for len(st.order) > keptSnapshots {
		old := st.order[0]
		st.order = st.order[1:]
		delete(st.byID, old)
	}
}

func (st *snapshotStore) get(id string) (Snapshot, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.byID[id]
	return s, ok
}

func (st *snapshotStore) kept() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.byID)
}

// matchSet is resolution steps 1–2: the nodes matching the selector, in document order,
// with how they matched. For a ref it is the identity match in the fresh tree. Off-screen
// matches are kept here; resolve drops them, conditions decide.
func (st *snapshotStore) matchSet(s Snapshot, sel Selector) ([]UINode, string, error) {
	return st.matchSetPinned(s, sel, nil)
}

// matchSetPinned is matchSet with a pinned identity: a ref selector is matched by named,
// the node the ref named at pin time, never by looking the snapshot up again. A selector
// without a ref ignores named.
func (st *snapshotStore) matchSetPinned(s Snapshot, sel Selector, named *UINode) ([]UINode, string, error) {
	if err := sel.validate(); err != nil {
		return nil, "", err
	}
	if sel.Ref != "" {
		if named != nil {
			n, ok := identityOf(s, *named)
			if !ok {
				return nil, "", nil
			}
			return []UINode{n}, "exact", nil
		}
		n, ok := st.identity(s, sel)
		if !ok {
			return nil, "", nil
		}
		return []UINode{n}, "exact", nil
	}
	ti := indexTree(s)
	var out []UINode
	for _, n := range s.Nodes {
		if withinOK(s, ti, n, sel.Within) && sel.matchesExact(n) {
			out = append(out, n)
		}
	}
	if len(out) > 0 {
		return out, "exact", nil
	}
	for _, n := range s.Nodes {
		if withinOK(s, ti, n, sel.Within) && sel.matchesContains(n) {
			out = append(out, n)
		}
	}
	return out, "contains", nil
}

// identity resolves a ref by the named node's identity in the fresh tree (class, id,
// text, desc, bounds), never by position (ui-automation.md §Selectors).
func (st *snapshotStore) identity(s Snapshot, sel Selector) (UINode, bool) {
	old, ok := st.get(sel.Snapshot)
	if !ok {
		return UINode{}, false
	}
	for i := range old.Nodes {
		if old.Nodes[i].Ref == sel.Ref {
			return identityOf(s, old.Nodes[i])
		}
	}
	return UINode{}, false
}

// identityOf finds in s the node with named's identity (class, id, text, desc, bounds),
// never by position (ui-automation.md §Selectors).
func identityOf(s Snapshot, named UINode) (UINode, bool) {
	for _, n := range s.Nodes {
		if n.Class == named.Class && n.ID == named.ID && n.Text == named.Text && n.Desc == named.Desc && n.Bounds == named.Bounds {
			return n, true
		}
	}
	return UINode{}, false
}

// pinnedCondition is a Condition whose ref, if any, was resolved to the named node's
// identity once before an action (plan m1f step 6, fix round). Later trees are compared
// against that identity, so polling cannot evict the snapshot the ref came from and the
// condition still means the same node. Pinning an unknown or evicted snapshot, or a ref
// it does not name, fails with DEVICE_STALE_REF before anything is tapped.
type pinnedCondition struct {
	cond Condition
	// named is the node the condition's ref named at pin time; nil when the condition's
	// selector has no ref.
	named *UINode
}

// pinConditions resolves every condition's ref to its node once. A condition without a
// ref pins nothing.
func (st *snapshotStore) pinConditions(conds []Condition) ([]pinnedCondition, error) {
	out := make([]pinnedCondition, 0, len(conds))
	for _, c := range conds {
		p, err := st.pinCondition(c)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// pinCondition resolves one condition's ref, validating the condition first.
func (st *snapshotStore) pinCondition(c Condition) (pinnedCondition, error) {
	p := pinnedCondition{cond: c}
	if err := c.validate(); err != nil {
		return p, err
	}
	switch {
	case c.Appears != nil:
		n, err := st.pinRef(*c.Appears)
		if err != nil {
			return p, err
		}
		p.named = n
	case c.Gone != nil:
		n, err := st.pinRef(*c.Gone)
		if err != nil {
			return p, err
		}
		p.named = n
	}
	return p, nil
}

// pinRef resolves a selector's ref against the snapshot it names, once, before an action.
// An unknown (never kept, or evicted) snapshot or a ref it does not name is
// DEVICE_STALE_REF; a selector without a ref returns nil.
func (st *snapshotStore) pinRef(sel Selector) (*UINode, error) {
	if sel.Ref == "" {
		return nil, nil
	}
	if old, ok := st.get(sel.Snapshot); ok {
		for i := range old.Nodes {
			if old.Nodes[i].Ref == sel.Ref {
				n := old.Nodes[i]
				return &n, nil
			}
		}
	}
	return nil, capability.NewError(ErrStaleRef, fmt.Sprintf("ref %q is not in snapshot %q", sel.Ref, sel.Snapshot)).
		WithDetails(map[string]any{"ref": sel.Ref, "snapshot": sel.Snapshot}).
		WithNextStep("Take a fresh device.uiTree and use a ref from it, or select by id, text, desc or class instead of a ref.")
}

// holds evaluates a pinned condition against one tree: `appears` needs at least one
// on-screen match, `gone` none, `foreground` the top window's package. A ref is matched
// by the identity pinned before the action, never by looking the snapshot up again.
func (p pinnedCondition) holds(st *snapshotStore, s Snapshot) (bool, error) {
	return st.holdsPinned(s, p.cond, p.named)
}

// foldMatches is resolution step 3: when a match and one of its ancestors both match,
// keep the outermost.
func foldMatches(s Snapshot, matches []UINode) []UINode {
	ti := indexTree(s)
	in := make(map[string]bool, len(matches))
	for _, n := range matches {
		in[n.Ref] = true
	}
	out := make([]UINode, 0, len(matches))
	for _, n := range matches {
		nested := false
		for p := ti.parent[n.Ref]; p != ""; p = ti.parent[p] {
			if in[p] {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, n)
		}
	}
	return out
}

// resolve runs ui-automation.md §Resolution 1–7 against one tree and returns the one
// match or the code that says why there is none.
func (st *snapshotStore) resolve(s Snapshot, sel Selector) (Matched, error) {
	if err := sel.validate(); err != nil {
		return Matched{}, err
	}
	matches, matchedBy, err := st.matchSet(s, sel)
	if err != nil {
		return Matched{}, err
	}
	if len(matches) == 0 {
		if sel.Ref != "" {
			return Matched{}, st.staleRefError(s, sel)
		}
		return Matched{}, notFoundError(s, sel)
	}
	matches = foldMatches(s, matches)
	var on []UINode
	for _, n := range matches {
		if onScreen(n, s.Display) {
			on = append(on, n)
		}
	}
	if len(on) == 0 {
		return Matched{}, offscreenError(matches, s.Display)
	}
	var chosen UINode
	switch {
	case sel.Index != nil:
		i := *sel.Index
		if i >= len(on) {
			return Matched{}, indexOutOfRangeError(on, i)
		}
		chosen = on[i]
	case len(on) > 1:
		return Matched{}, ambiguousError(on)
	default:
		chosen = on[0]
	}
	if isDisabled(chosen) {
		return Matched{}, disabledError(chosen)
	}
	return Matched{Node: chosen, MatchedBy: matchedBy}, nil
}

// holds evaluates one expectation against one tree: `appears` needs at least one
// on-screen match, `gone` none, `foreground` the top window's package. The two-read
// rule for `gone` belongs to the tap engine (plan m1f step 6).
func (st *snapshotStore) holds(s Snapshot, c Condition) (bool, error) {
	return st.holdsPinned(s, c, nil)
}

// holdsPinned is holds with a pinned ref identity, so an expectation over a ref does not
// look its snapshot up again (plan m1f step 6, fix round).
func (st *snapshotStore) holdsPinned(s Snapshot, c Condition, named *UINode) (bool, error) {
	if err := c.validate(); err != nil {
		return false, err
	}
	switch {
	case c.Appears != nil:
		matches, _, err := st.matchSetPinned(s, *c.Appears, named)
		if err != nil {
			return false, err
		}
		for _, n := range matches {
			if onScreen(n, s.Display) {
				return true, nil
			}
		}
		return false, nil
	case c.Gone != nil:
		matches, _, err := st.matchSetPinned(s, *c.Gone, named)
		if err != nil {
			return false, err
		}
		return len(matches) == 0, nil
	default:
		return s.Foreground == c.Foreground, nil
	}
}

func (st *snapshotStore) staleRefError(s Snapshot, sel Selector) *capability.Error {
	e := capability.NewError(ErrStaleRef, fmt.Sprintf("ref %q is not in snapshot %q, or the node it named changed", sel.Ref, sel.Snapshot)).
		WithNextStep("Take a fresh device.uiTree and use a ref from it, or select by id, text, desc or class instead of a ref.")
	details := map[string]any{"ref": sel.Ref, "snapshot": sel.Snapshot}
	if old, ok := st.get(sel.Snapshot); ok {
		for i := range old.Nodes {
			if old.Nodes[i].Ref != sel.Ref {
				continue
			}
			n := old.Nodes[i]
			var cands []Candidate
			for _, f := range s.Nodes {
				if (n.Text != "" && containsFold(f.Text, n.Text)) || (n.Desc != "" && containsFold(f.Desc, n.Desc)) {
					cands = append(cands, candidateOf(f))
					if len(cands) >= 10 {
						break
					}
				}
			}
			if len(cands) > 0 {
				details["candidates"] = cands
			}
			break
		}
	}
	return e.WithDetails(details)
}

func notFoundError(s Snapshot, sel Selector) *capability.Error {
	details := map[string]any{}
	if c := nearCandidates(s, sel, 10); len(c) > 0 {
		details["candidates"] = c
	}
	if s.EmptyReason != "" {
		details["emptyReason"] = s.EmptyReason
	}
	next := "Take a fresh device.uiTree, then narrow the selector or add a within boundary; nothing was tapped or typed, so this call is safe to repeat."
	if s.EmptyReason == EmptyNoAccessibilityInfo {
		// ui-automation.md §Failure modes: a window with no accessible nodes points at
		// device.screenshot; OCR is post-v1.
		next = "The window exposes no accessibility nodes, so no selector can match: take a device.screenshot to see it (OCR is post-v1); nothing was tapped or typed, so this call is safe to repeat."
	}
	e := capability.NewError(ErrElementNotFound, "no node matches the selector").
		WithNextStep(next)
	if len(details) > 0 {
		e = e.WithDetails(details)
	}
	return e
}

func offscreenError(nodes []UINode, d UIDisplay) *capability.Error {
	return capability.NewError(ErrElementOffscreen, "the matching node is off-screen").
		WithDetails(map[string]any{"candidates": candidatesOf(nodes, 10), "display": d}).
		WithNextStep("Scroll the node into view, or take a fresh device.uiTree and retry.")
}

func ambiguousError(nodes []UINode) *capability.Error {
	return capability.NewError(ErrElementAmbiguous, fmt.Sprintf("%d nodes match the selector", len(nodes))).
		WithDetails(map[string]any{"candidates": candidatesOf(nodes, 10)}).
		WithNextStep("Add index to pick one in document order, or narrow the selector with another field or a within boundary.")
}

func disabledError(n UINode) *capability.Error {
	return capability.NewError(ErrElementDisabled, "the matching node is disabled").
		WithDetails(map[string]any{"node": candidateOf(n)}).
		WithNextStep("Wait for the node to become enabled, or target another node.")
}

func indexOutOfRangeError(nodes []UINode, i int) *capability.Error {
	return capability.NewError(ErrElementNotFound, fmt.Sprintf("index %d is past the %d on-screen matches", i, len(nodes))).
		WithDetails(map[string]any{"candidates": candidatesOf(nodes, 10)}).
		WithNextStep("Lower index, or take a fresh device.uiTree to see the current tree; nothing was tapped or typed, so this call is safe to repeat.")
}

// nearCandidates names nodes matching any one of the selector's fields, up to limit,
// in document order (ui-automation.md §Resolution 6; D11).
func nearCandidates(s Snapshot, sel Selector, limit int) []Candidate {
	ti := indexTree(s)
	out := []Candidate{}
	for _, n := range s.Nodes {
		if !withinOK(s, ti, n, sel.Within) || !nearMatches(sel, n) {
			continue
		}
		out = append(out, candidateOf(n))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func nearMatches(sel Selector, n UINode) bool {
	any := false
	if sel.ID != "" {
		any = true
		if sel.ID == n.ID {
			return true
		}
	}
	if sel.Class != "" {
		any = true
		if sel.Class == n.Class {
			return true
		}
	}
	if sel.Text != "" {
		any = true
		if containsFold(n.Text, sel.Text) {
			return true
		}
	}
	if sel.Desc != "" {
		any = true
		if containsFold(n.Desc, sel.Desc) {
			return true
		}
	}
	return !any
}

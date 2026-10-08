package device

import (
	"regexp"
	"strconv"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The compact text renderings of the device capabilities (ADR-0036 Decision 2; mcp.md
// §Results). Each is a second view of a result, kept true by golden tests over recorded
// results; the structured content stays the full result.
//
// Every renderer writes through capability.Text, which quotes any string that is not a
// safe word, so a string from the device or the app cannot forge a line of ours
// (ADR-0015 §1). Strings that come from a tagged field are written with Q or Pre, always
// quoted; W is for the daemon's keywords, refs, snapshot ids, numbers and enum values.
//
// The formats, one line per fact; a line's first word says what it is:
//
//	device.uiTree (and a tree inside an action result)
//	  snapshot <id> "<foreground package>" <w>x<h> [rot <n>] [truncated]
//	      [empty=<reason>] [hint="<text>"]
//	   <ref> "<Class>" [#"<id>"] ["<text>"] [desc="<text>"] [<flag>,...] [l,t,r,b]
//	  one line per node, in document order, indented one space per ancestor shown. A
//	  status or navigation bar background view with nothing in it, and an unnamed
//	  wrapper around one child, are not shown (they still resolve by ref).
//	  [systemDialog <kind> [package="<id>"]]  [warning "<text>"]...
//
//	device.tapElement, device.inputText
//	  tapped <ref> "<Class>" ... exact|contains point <x>,<y> clickable|notClickable in <snapshot>
//	  [clickableAncestor <ref> "<Class>" ...]  (inputText: typed characters=<n> method=<m>)
//	  expect matched=<bool> afterMs=<n> polls=<n> [failedDumps=<n>] [reason=<r>]
//	  [vacuous <condition>]...  [unmet <condition>] with its [ candidate <ref> ...] lines
//	  foreground "<before>" same|-> "<after>"|-> none
//	  [snapshot <id>] [<the tree, as above>] [warning "<text>"]...
//	  [appCrash "<package>" source=<s> [pid=<n>] [time=<hh:mm:ss.mmm>] with its body]
//	  [systemDialog ...]
//
//	device.logs
//	  logs <device> [app="<id>"] by=<uid|pids|none> window <from> <to>
//	      scanned=<n> matched=<n> returned=<n> [truncated] [empty=<reason>]
//	  <hh:mm:ss.mmm> <P>/"<tag>" [p<pid>] "<message>"  (P is V D I W E F). The pid is written
//	  on the first line and whenever it changes from the last line written, and holds until
//	  the next one. Further messages of the same time, priority, tag and pid follow on
//	  indented lines, as "<message>".
//	  crash pid=<n> <hh:mm:ss.mmm>, with a body: exception "<class>" [message="<m>"],
//	  causedBy "<cause>"..., at "<frame>"...  (the entries replace every line flagged crash)
//	  [capture <ref> <mediaType> <n>B <durability>]  [warning "<text>"]...
//
// A selector in a condition is written as id="x" text="x" desc="x" class="x" within.id="x"
// index=<n> ref=<r> snapshot=<s>, only the fields it has.

// uiTreeText is the rendering of device.uiTree's result.
func uiTreeText(s Snapshot) string {
	var t capability.Text
	writeSnapshot(&t, s)
	return t.String()
}

// systemBarIDs are the ids of the views the system draws behind the status and navigation
// bars; an empty one carries nothing the agent can act on.
var systemBarIDs = map[string]bool{"statusBarBackground": true, "navigationBarBackground": true}

// hidden reports whether a node's line is left out of a rendering: an empty system-bar
// view, or an unnamed wrapper around exactly one child. The node stays in the snapshot,
// so a ref to it resolves as ever.
func hidden(n UINode) bool {
	if n.Text != "" || n.Desc != "" || len(n.Flags) > 0 {
		return false
	}
	if systemBarIDs[n.ID] && len(n.Children) == 0 {
		return true
	}
	return n.ID == "" && len(n.Children) == 1
}

func writeSnapshot(t *capability.Text, s Snapshot) {
	t.W("snapshot", s.ID).Q(s.Foreground).W(strconv.Itoa(s.Display.Width) + "x" + strconv.Itoa(s.Display.Height))
	if s.Display.Rotation != 0 {
		t.W("rot", strconv.Itoa(s.Display.Rotation))
	}
	if s.Truncated {
		t.W("truncated")
	}
	if s.EmptyReason != "" {
		t.W("empty=" + s.EmptyReason)
	}
	if s.Hint != "" {
		t.Pre("hint=", s.Hint)
	}
	t.Nl()

	parent := make(map[string]string, len(s.Nodes))
	byRef := make(map[string]UINode, len(s.Nodes))
	for _, n := range s.Nodes {
		byRef[n.Ref] = n
		for _, c := range n.Children {
			parent[c] = n.Ref
		}
	}
	// depth counts the shown ancestors; a visited set keeps a malformed tree (a cycle)
	// from looping.
	depths := make(map[string]int, len(s.Nodes))
	var depth func(ref string, seen map[string]bool) int
	depth = func(ref string, seen map[string]bool) int {
		if d, ok := depths[ref]; ok {
			return d
		}
		p, ok := parent[ref]
		if !ok || seen[ref] {
			return 0
		}
		seen[ref] = true
		d := depth(p, seen)
		if pn, ok := byRef[p]; ok && !hidden(pn) {
			d++
		}
		depths[ref] = d
		return d
	}
	for _, n := range s.Nodes {
		if hidden(n) {
			continue
		}
		t.Indent(depth(n.Ref, map[string]bool{}))
		writeNode(t, n.Ref, n.Class, n.ID, n.Text, n.Desc)
		t.List(n.Flags).Box(n.Bounds).Nl()
	}
	if s.SystemDialog != nil {
		writeSystemDialog(t, s.SystemDialog)
	}
	writeWarnings(t, s.Warnings)
}

// writeNode writes the part of a node line a candidate shares: ref, class, id, text, desc.
func writeNode(t *capability.Text, ref, class, id, text, desc string) {
	t.W(ref).Q(class)
	if id != "" {
		t.Pre("#", id)
	}
	if text != "" {
		t.Q(text)
	}
	if desc != "" {
		t.Pre("desc=", desc)
	}
}

func writeCandidate(t *capability.Text, word string, c Candidate) {
	t.W(word)
	writeNode(t, c.Ref, c.Class, c.ID, c.Text, "")
	t.Box(c.Bounds).Nl()
}

func writeWarnings(t *capability.Text, ws []string) {
	for _, w := range ws {
		t.W("warning").Q(w).Nl()
	}
}

func writeSystemDialog(t *capability.Text, d *SystemDialog) {
	t.W("systemDialog", d.Kind)
	if d.Package != "" {
		t.Pre("package=", d.Package)
	}
	t.Nl()
}

func kv(key string, n int64) string { return key + "=" + strconv.FormatInt(n, 10) }

// writeSelector writes the fields a selector has, as items on the current line.
func writeSelector(t *capability.Text, s Selector) {
	if s.ID != "" {
		t.Pre("id=", s.ID)
	}
	if s.Text != "" {
		t.Pre("text=", s.Text)
	}
	if s.Desc != "" {
		t.Pre("desc=", s.Desc)
	}
	if s.Class != "" {
		t.Pre("class=", s.Class)
	}
	if w := s.Within; w != nil {
		if w.ID != "" {
			t.Pre("within.id=", w.ID)
		}
		if w.Text != "" {
			t.Pre("within.text=", w.Text)
		}
		if w.Desc != "" {
			t.Pre("within.desc=", w.Desc)
		}
		if w.Class != "" {
			t.Pre("within.class=", w.Class)
		}
	}
	if s.Index != nil {
		t.W(kv("index", int64(*s.Index)))
	}
	if s.Ref != "" {
		t.Pre("ref=", s.Ref)
	}
	if s.Snapshot != "" {
		t.Pre("snapshot=", s.Snapshot)
	}
}

func writeCondition(t *capability.Text, c Condition) {
	switch {
	case c.Appears != nil:
		t.W("appears")
		writeSelector(t, *c.Appears)
	case c.Gone != nil:
		t.W("gone")
		writeSelector(t, *c.Gone)
	default:
		t.W("foreground").Q(c.Foreground)
	}
}

func writeTapped(t *capability.Text, tp Tapped) {
	t.W("tapped")
	writeNode(t, tp.Ref, tp.Class, tp.ID, tp.Text, tp.Desc)
	t.W(tp.MatchedBy, "point").Ints(tp.Point[0], tp.Point[1])
	if tp.Clickable {
		t.W("clickable")
	} else {
		t.W("notClickable")
	}
	t.W("in", tp.Snapshot).Nl()
	if c := tp.ClickableAncestor; c != nil {
		writeCandidate(t, "clickableAncestor", *c)
	}
}

func writeExpect(t *capability.Text, e *ExpectResult) {
	if e == nil {
		return
	}
	t.W("expect", "matched="+strconv.FormatBool(e.Matched), kv("afterMs", e.AfterMs), kv("polls", int64(e.Polls)))
	if e.FailedDumps > 0 {
		t.W(kv("failedDumps", int64(e.FailedDumps)))
	}
	if e.Reason != "" {
		t.W("reason=" + e.Reason)
	}
	t.Nl()
	for _, c := range e.Vacuous {
		t.W("vacuous")
		writeCondition(t, c)
		t.Nl()
	}
	for _, u := range e.Unmet {
		t.W("unmet")
		writeCondition(t, u.Condition)
		t.Nl()
		for _, c := range u.Candidates {
			t.Indent(1)
			writeCandidate(t, "candidate", c)
		}
	}
}

// writeCrashBody writes the lines under a crash header: the exception and message, the
// cause chain, the frames and, for an exit-info crash, the reason.
func writeCrashBody(t *capability.Text, exception, message, reason string, causes, frames []string) {
	if exception != "" || message != "" {
		t.Indent(1).W("exception").Q(exception)
		if message != "" {
			t.Pre("message=", message)
		}
		t.Nl()
	}
	if reason != "" {
		t.Indent(1).W("reason").Q(reason).Nl()
	}
	for _, c := range causes {
		t.Indent(1).W("causedBy").Q(c).Nl()
	}
	for _, f := range frames {
		t.Indent(1).W("at").Q(f).Nl()
	}
}

func writeAppCrash(t *capability.Text, c *AppCrash) {
	if c == nil {
		return
	}
	t.W("appCrash").Q(c.Package).W("source=" + c.Source)
	if c.PID != 0 {
		t.W(kv("pid", int64(c.PID)))
	}
	if c.Time != "" {
		t.W("time=" + clock(c.Time))
	}
	t.Nl()
	writeCrashBody(t, c.Exception, c.Message, c.Reason, nil, c.Frames)
}

func writeForeground(t *capability.Text, f *Foreground) {
	if f == nil {
		return
	}
	t.W("foreground").Q(f.Before)
	switch f.After {
	case "":
		t.W("->", "none")
	case f.Before:
		t.W("same")
	default:
		t.W("->").Q(f.After)
	}
	t.Nl()
}

// tapText is the rendering of device.tapElement's result.
func tapText(r TapResult) string {
	var t capability.Text
	writeTapped(&t, r.Tapped)
	writeExpect(&t, r.Expect)
	writeForeground(&t, r.Foreground)
	writeLastSnapshot(&t, r.Snapshot, r.Tree)
	writeWarnings(&t, r.Warnings)
	writeAppCrash(&t, r.AppCrash)
	if r.SystemDialog != nil {
		writeSystemDialog(&t, r.SystemDialog)
	}
	return t.String()
}

// writeLastSnapshot names the last read when the tree does not already say it, then
// writes the tree, when the call returned one.
func writeLastSnapshot(t *capability.Text, id string, tree *Snapshot) {
	if id != "" && (tree == nil || tree.ID != id) {
		t.W("snapshot", id).Nl()
	}
	if tree != nil {
		writeSnapshot(t, *tree)
	}
}

// typedText is the rendering of device.inputText's result.
func typedText(r InputTextResult) string {
	var t capability.Text
	if r.Typed {
		t.W("typed")
	} else {
		t.W("typed=false")
	}
	t.W(kv("characters", int64(r.Characters)), "method="+r.Method).Nl()
	if r.Tapped != nil {
		writeTapped(&t, *r.Tapped)
	}
	writeExpect(&t, r.Expect)
	writeWarnings(&t, r.Warnings)
	writeAppCrash(&t, r.AppCrash)
	if r.SystemDialog != nil {
		writeSystemDialog(&t, r.SystemDialog)
	}
	return t.String()
}

var (
	// clockRE reads the time of day of the daemon's RFC 3339 UTC timestamps.
	clockRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT(\d\d:\d\d:\d\d)(?:\.(\d+))?Z$`)
	// stampRE reads the date and the whole seconds of the same timestamps.
	stampRE = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?Z$`)
)

// clock is the time of day of an RFC 3339 UTC timestamp, to the millisecond: 14:25:06.443.
// A value that is not one is returned as it is (the writer quotes it if it is not a safe
// word).
func clock(ts string) string {
	m := clockRE.FindStringSubmatch(ts)
	if m == nil {
		return ts
	}
	return m[1] + "." + millis(m[2])
}

// stamp is an RFC 3339 UTC timestamp to the millisecond: 2026-10-07T14:25:06.443Z.
func stamp(ts string) string {
	m := stampRE.FindStringSubmatch(ts)
	if m == nil {
		return ts
	}
	return m[1] + "." + millis(m[2]) + "Z"
}

func millis(frac string) string {
	return (frac + "000")[:3]
}

// priorityLetter is logcat's one-letter priority.
func priorityLetter(p string) string {
	switch p {
	case "verbose":
		return "V"
	case "debug":
		return "D"
	case "info":
		return "I"
	case "warn":
		return "W"
	case "error":
		return "E"
	case "fatal":
		return "F"
	}
	return p
}

// logsText is the rendering of device.logs's result.
func logsText(r LogsResult) string {
	var t capability.Text
	t.W("logs", r.Device)
	if r.Filter.ApplicationID != "" {
		t.Pre("app=", r.Filter.ApplicationID)
	}
	t.W("by="+r.Filter.By, "window", stamp(r.Window.From), stamp(r.Window.To))
	t.W(kv("scanned", int64(r.Counts.Scanned)), kv("matched", int64(r.Counts.Matched)), kv("returned", int64(r.Counts.Returned)))
	if r.Truncated {
		t.W("truncated")
	}
	if r.EmptyReason != "" {
		t.W("empty=" + r.EmptyReason)
	}
	t.Nl()

	// A crash block's lines are replaced by the entries: once the read summarised a
	// crash, every line flagged crash is left out, whatever its pid (a native crash's
	// tombstone is logged by crash_dump, another pid than the crashed process). With no
	// entry, flagged lines stay: they are then all the read has of the crash. When the
	// entries reached logsCrashesMax the oldest blocks may have no entry, so a flagged line
	// older than the oldest entry stays too (the times are one fixed RFC 3339 form, so they
	// order as strings).
	fold := func(l LogLine) bool {
		return l.Crash && len(r.Crashes) > 0 && (len(r.Crashes) < logsCrashesMax || l.Time >= r.Crashes[0].Time)
	}
	var prev string // the header of the line before, "" at the start
	lastPID := 0    // the pid of the last header written
	for _, l := range r.Lines {
		if fold(l) {
			continue
		}
		head := clock(l.Time) + " " + priorityLetter(l.Priority) + "/" + l.Tag + " " + strconv.Itoa(l.PID)
		if head == prev {
			t.Indent(1).Q(l.Message).Nl()
			continue
		}
		prev = head
		t.W(clock(l.Time)).Pre(priorityLetter(l.Priority)+"/", l.Tag)
		if l.PID != lastPID {
			t.W("p" + strconv.Itoa(l.PID))
			lastPID = l.PID
		}
		t.Q(l.Message).Nl()
	}
	for _, c := range r.Crashes {
		t.W("crash", kv("pid", int64(c.PID)), clock(c.Time)).Nl()
		writeCrashBody(&t, c.Exception, c.Message, "", c.Causes, c.Frames)
	}
	if c := r.Capture; c != nil {
		t.W("capture", c.Ref, c.MediaType, strconv.Itoa(c.Bytes)+"B", string(c.Durability)).Nl()
	}
	writeWarnings(&t, r.Warnings)
	return t.String()
}

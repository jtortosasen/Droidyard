package device

import (
	"cmp"
	"slices"
	"time"
)

// The read shape of device.logs (ADR-0135 §5, plan m3i step 4): the stages after the line
// filter. In order over the parsed lines: the window (until), the scope and the filter
// pass (logfilter.go), collapse over the matched sequence, textfilter.Select for context,
// keep and maxLines, then maxMessageBytes on what is returned (logLineView).

// withinUntil keeps the lines at or before until, in order. The buffers are merged by
// time, but a line is judged on its own time rather than by where the first late one sits.
func withinUntil(lines []logLine, until time.Time) []logLine {
	out := make([]logLine, 0, len(lines))
	for _, l := range lines {
		if !l.at.After(until) {
			out = append(out, l)
		}
	}
	return out
}

// seqItem is one line of the sequence Select caps: a matched line, or, when context is
// asked for, an in-scope line the filter rejected (a candidate neighbour).
type seqItem struct {
	idx     int // into the parsed lines
	matched bool
	repeats int // with collapse: the lines folded into this one, 0 when none were
}

// readSequence builds the sequence Select works on. Without context it holds the matched
// lines alone; with context, every in-scope line, so a neighbour is always one of the
// call's own (another app's lines never become an app's context). With collapse, a matched
// line equal to the last matched one in pid, priority, tag, message and crash flag folds
// into it, whatever in-scope lines the filter rejected between them: the fold is over the
// matched sequence, so a run is one item to maxLines.
func readSequence(lines []logLine, crash []bool, inScope []int, matchedAt []bool, withContext, collapse bool) []seqItem {
	seq := make([]seqItem, 0, len(inScope))
	last := -1 // the position in seq of the last matched item
	for _, i := range inScope {
		if !matchedAt[i] {
			if withContext {
				seq = append(seq, seqItem{idx: i})
			}
			continue
		}
		if collapse && last >= 0 {
			p := seq[last].idx
			if crash[p] == crash[i] && lines[p].pid == lines[i].pid && lines[p].priority == lines[i].priority &&
				lines[p].tag == lines[i].tag && lines[p].message == lines[i].message {
				if seq[last].repeats == 0 {
					seq[last].repeats = 1
				}
				seq[last].repeats++
				continue
			}
		}
		seq = append(seq, seqItem{idx: i, matched: true})
		last = len(seq) - 1
	}
	return seq
}

// topTagsOf names the n tags with most lines in scope in the window, with how many of
// those the line filter kept: the noise to exclude next. Ties go to the tag that matched
// more, then by name, so the report is stable.
func topTagsOf(lines []logLine, inScope []int, matchedAt []bool, n int) []LogTag {
	byTag := map[string]*LogTag{}
	for _, i := range inScope {
		t := byTag[lines[i].tag]
		if t == nil {
			t = &LogTag{Tag: lines[i].tag}
			byTag[lines[i].tag] = t
		}
		t.Scanned++
		if matchedAt[i] {
			t.Matched++
		}
	}
	all := make([]LogTag, 0, len(byTag))
	for _, t := range byTag {
		all = append(all, *t)
	}
	slices.SortFunc(all, func(a, b LogTag) int {
		return cmp.Or(cmp.Compare(b.Scanned, a.Scanned), cmp.Compare(b.Matched, a.Matched), cmp.Compare(a.Tag, b.Tag))
	})
	if len(all) > n {
		all = all[:n]
	}
	for i := range all {
		all[i].Tag = truncateUTF8(all[i].Tag, logsTagBytesMax)
	}
	return all
}

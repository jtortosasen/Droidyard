// Package textfilter is the pattern engine every host-side filter of a capability call
// shares (ADR-0135 §1–§2, ADR-0136): caller patterns compiled once with their bounds,
// a time budget for the pass that runs them, and grep -C windows over a list of items.
// It knows nothing about logcat or search results; its callers decide what an item is,
// what a spent budget means for their result, and what a cut group is worth.
//
// Patterns are Go RE2: time linear in input times program, no backtracking. That bounds
// the shape of a hostile pattern, not its cost, so three bounds hold together: a count
// and a length per pattern, one instruction budget for everything a call compiles
// (MaxCallInsts), and a time budget for the pass (FilterBudget, Pass). Caller text never
// reaches an adb argument or a device shell; it is compiled and run on the host only.
package textfilter

import (
	"regexp"
	"regexp/syntax"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

const (
	// MaxPatterns is the most patterns one list (match or exclude) may hold.
	MaxPatterns = 20
	// MaxPatternBytes is the longest one pattern may be; the shortest is 1.
	MaxPatternBytes = 512

	// MaxCallInsts is the instruction budget of one call: the compiled programs of both
	// lists together (regexp/syntax), whatever their number. BenchmarkCost measures it:
	// the worst shape, a case-folded 12-way literal alternation, costs about 2.6 s per MiB
	// per 1,000 instructions on 200-byte lines (plan m3i step 2), so at the budget a 4 MiB
	// read of that shape outruns FilterBudget and is cut, with a warning. The time budget
	// is the bound; this one only keeps a query an agent plausibly writes (the owner's
	// eight patterns are 243, one 512-byte literal 514) from being refused outright.
	MaxCallInsts = 1000

	// FilterBudget is how long one call's filter pass may run before its caller stops it
	// and says so (a read is truncated with a warning; artifact.search returns
	// nextOffset). The pass overruns it by at most one item (Pass).
	FilterBudget = 5 * time.Second
)

// Spec is a call's two pattern lists. The field names of its JSON are the callers', so
// an error names `match[i]` or `exclude[i]` as the caller wrote them.
type Spec struct {
	Match   []string
	Exclude []string
}

// Filter is the compiled Spec. The zero value and a Filter compiled from an empty Spec
// keep every subject.
type Filter struct {
	match   []*regexp.Regexp
	exclude []*regexp.Regexp
}

// Compile validates and compiles spec. Every failure is REQUEST_INVALID_PARAMS naming
// the field and index (or the call's budget) with RE2's own message, which quotes only
// the caller's text. Nothing is compiled past the budget: a pattern whose program would
// be far beyond it is refused from the parse tree, before any program is built.
func Compile(spec Spec) (*Filter, error) {
	f := &Filter{}
	used := 0
	var err error
	if f.match, used, err = compileList("match", spec.Match, used); err != nil {
		return nil, err
	}
	if f.exclude, _, err = compileList("exclude", spec.Exclude, used); err != nil {
		return nil, err
	}
	return f, nil
}

func compileList(field string, patterns []string, used int) ([]*regexp.Regexp, int, error) {
	if len(patterns) > MaxPatterns {
		return nil, used, capability.Errorf(capability.ErrInvalidParams,
			"%s has %d patterns; at most %d", field, len(patterns), MaxPatterns)
	}
	var out []*regexp.Regexp
	for i, p := range patterns {
		if len(p) < 1 || len(p) > MaxPatternBytes {
			return nil, used, capability.Errorf(capability.ErrInvalidParams,
				"%s[%d] is %d bytes; a pattern is 1 to %d bytes", field, i, len(p), MaxPatternBytes)
		}
		tree, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			return nil, used, capability.Errorf(capability.ErrInvalidParams, "%s[%d]: %s", field, i, err)
		}
		// The parser already refuses nested repeats past 1,000 and a tree past its own
		// size limit, but a 512-byte body repeated 1,000 times is still hundreds of
		// thousands of instructions to build before the budget could say no. A cheap
		// upper bound on the tree refuses it first.
		if est := estimate(tree); used+est > estimateSlack*MaxCallInsts {
			return nil, used, budgetError(field, i, used, true)
		}
		prog, err := syntax.Compile(tree.Simplify())
		if err != nil {
			return nil, used, capability.Errorf(capability.ErrInvalidParams, "%s[%d]: %s", field, i, err)
		}
		if used += len(prog.Inst); used > MaxCallInsts {
			return nil, used, budgetError(field, i, used, false)
		}
		re, err := regexp.Compile(p)
		if err != nil { // Unreachable after syntax.Parse with the same flags; kept honest.
			return nil, used, capability.Errorf(capability.ErrInvalidParams, "%s[%d]: %s", field, i, err)
		}
		out = append(out, re)
	}
	return out, used, nil
}

// estimateSlack is how far past MaxCallInsts the tree estimate may reach before a
// pattern is refused unbuilt; the estimate is at most about twice the compiled length
// (TestEstimateBoundsTheProgram), so nothing the budget would accept is refused here.
const estimateSlack = 4

func budgetError(field string, i, used int, huge bool) error {
	if huge {
		return capability.Errorf(capability.ErrInvalidParams,
			"%s[%d] compiles to far more than the %d instructions that all of a call's patterns may use",
			field, i, MaxCallInsts)
	}
	return capability.Errorf(capability.ErrInvalidParams,
		"%s[%d] takes the call's patterns to %d compiled instructions; the budget for all of a call's patterns is %d",
		field, i, used, MaxCallInsts)
}

// Keep reports whether subject passes: any match pattern matches it (or there are none)
// and no exclude pattern does.
func (f *Filter) Keep(subject string) bool {
	if f == nil {
		return true
	}
	if len(f.match) > 0 && !anyMatch(f.match, subject) {
		return false
	}
	return !anyMatch(f.exclude, subject)
}

// Empty reports whether the filter has no pattern, so a caller may skip its pass.
func (f *Filter) Empty() bool { return f == nil || len(f.match)+len(f.exclude) == 0 }

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// estimate is an upper bound on the instructions of t's simplified program, saturating
// well below overflow. It is not the count the budget uses (that is the compiled
// program's length); it only keeps an enormous tree from being compiled.
func estimate(t *syntax.Regexp) int { return treeSize(t) + 4 } // 4: fail, match, the search loop

func treeSize(t *syntax.Regexp) int {
	const sat = 1 << 30
	clamp := func(n int) int { return min(n, sat) }
	sum := func() int {
		n := 0
		for _, s := range t.Sub {
			n = clamp(n + treeSize(s))
		}
		return n
	}
	switch t.Op {
	case syntax.OpLiteral:
		return len(t.Rune)
	case syntax.OpCapture:
		return clamp(treeSize(t.Sub[0]) + 2)
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		return clamp(treeSize(t.Sub[0]) + 1)
	case syntax.OpRepeat:
		n := treeSize(t.Sub[0]) + 1
		copies := t.Max
		if t.Max < 0 {
			copies = t.Min + 1
		}
		return clamp(max(copies, 1) * n)
	case syntax.OpConcat:
		return sum()
	case syntax.OpAlternate:
		return clamp(sum() + len(t.Sub))
	default:
		return 1
	}
}

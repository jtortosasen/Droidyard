package device

import (
	"context"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/textfilter"
)

// The line filter of device.logs (ADR-0135 §3, plan m3i step 3). Everything here runs on
// the host: caller text is compared in Go and never reaches an adb argument or a device
// shell (plan m1i R4). A stream takes the same filter (step 5), so the type is shared.

const (
	// logsExcludeTagsMax and logsTagPrioritiesMax bound the two tag lists like tags.
	logsExcludeTagsMax   = 20
	logsTagPrioritiesMax = 20
	// logsProcessesMax and logsProcessNameMax bound processes: names as ps prints them.
	logsProcessesMax   = 10
	logsProcessNameMax = 128
	// logsProcessPIDsMax bounds the pids a relayed result reports for one process name.
	logsProcessPIDsMax = 64
)

// LineFilter selects log lines by what they say: logcat's own tag and priority
// vocabulary, plus RE2 patterns over the line's text. It is embedded in LogsParams, so
// the schema flattens it; every field is omitempty, so a call that uses none of the new
// ones reaches an older host unchanged (ADR-0135 §6).
type LineFilter struct {
	Tags          []string `json:"tags,omitempty" jsonschema_description:"At most 20 logcat tags, matched exactly. Applied on the host: they never reach a device shell."`
	MinPriority   string   `json:"minPriority,omitempty" jsonschema:"enum=verbose,enum=debug,enum=info,enum=warn,enum=error,enum=fatal" jsonschema_description:"The lowest priority to keep: verbose, debug, info, warn, error or fatal."`
	Contains      string   `json:"contains,omitempty" jsonschema_description:"A literal substring of the message, at most 256 bytes; applied on the host."`
	ExcludeTags   []string `json:"excludeTags,omitempty" jsonschema_description:"At most 20 tags to drop, matched exactly: a line of a listed tag never passes."`
	TagPriorities []string `json:"tagPriorities,omitempty" jsonschema_description:"At most 20 as Tag:L, L one of V D I W E F S: that tag's minimum instead of minPriority; S drops the tag."`
	Match         []string `json:"match,omitempty" jsonschema_description:"At most 20 RE2 patterns over \"tag: message\"; a line passes if any matches. (?i) ignores case; ^Tag: selects a tag."`
	Exclude       []string `json:"exclude,omitempty" jsonschema_description:"At most 20 RE2 patterns over \"tag: message\"; a line any of them matches is dropped."`
	Processes     []string `json:"processes,omitempty" jsonschema_description:"At most 10 process names as ps prints them (system_server, com.android.systemui): their lines join the scope."`
}

// logsFilterTagMax bounds one excludeTags or tagPriorities tag, in bytes (plan m3i
// close review: the other filter strings have bounds; a tag past it names no line).
const logsFilterTagMax = 256

// letterRank is the rank of a priority letter in a tagPriorities entry: the published
// priorities' ranks, and S (silent) above all of them, so that nothing passes it.
var letterRank = map[string]int{
	"V": priorityRank["verbose"], "D": priorityRank["debug"], "I": priorityRank["info"],
	"W": priorityRank["warn"], "E": priorityRank["error"], "F": priorityRank["fatal"],
	"S": priorityRank["fatal"] + 1,
}

// validate rejects a filter that is not well formed, before the device is touched. It
// compiles the patterns, so a bad one is REQUEST_INVALID_PARAMS naming match[i] or
// exclude[i].
func (f LineFilter) validate() error {
	_, err := f.compile()
	return err
}

// processNameOK reports whether a process name is 1 to logsProcessNameMax bytes of
// [A-Za-z0-9._:@/+-]: what a ps NAME holds, and nothing a shell would read specially.
func processNameOK(s string) bool {
	if len(s) < 1 || len(s) > logsProcessNameMax {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._:@/+-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// compiledFilter is a LineFilter ready to judge lines: compiled once per call.
type compiledFilter struct {
	tags        []string
	excludeTags []string
	minRank     int            // -1: no minPriority
	tagRank     map[string]int // a listed tag's own minimum
	contains    string
	text        *textfilter.Filter
	hasText     bool
}

// compile validates f and prepares it. Every failure is REQUEST_INVALID_PARAMS.
func (f LineFilter) compile() (*compiledFilter, error) {
	if len(f.Tags) > logsTagsMax {
		return nil, capability.Errorf(capability.ErrInvalidParams, "tags lists %d entries; at most %d are allowed", len(f.Tags), logsTagsMax)
	}
	for _, t := range f.Tags {
		if strings.TrimSpace(t) == "" {
			return nil, capability.Errorf(capability.ErrInvalidParams, "tags contains an empty tag")
		}
	}
	c := &compiledFilter{minRank: -1, tags: f.Tags, excludeTags: f.ExcludeTags, contains: f.Contains}
	if f.MinPriority != "" {
		rank, ok := priorityRank[f.MinPriority]
		if !ok {
			return nil, capability.Errorf(capability.ErrInvalidParams, "minPriority %q is not one of verbose, debug, info, warn, error, fatal", f.MinPriority)
		}
		c.minRank = rank
	}
	if len(f.Contains) > logsContainsMax {
		return nil, capability.Errorf(capability.ErrInvalidParams, "contains is %d bytes; at most %d are allowed", len(f.Contains), logsContainsMax)
	}
	if len(f.ExcludeTags) > logsExcludeTagsMax {
		return nil, capability.Errorf(capability.ErrInvalidParams, "excludeTags lists %d entries; at most %d are allowed", len(f.ExcludeTags), logsExcludeTagsMax)
	}
	for i, t := range f.ExcludeTags {
		if strings.TrimSpace(t) == "" {
			return nil, capability.Errorf(capability.ErrInvalidParams, "excludeTags contains an empty tag")
		}
		if len(t) > logsFilterTagMax {
			return nil, capability.Errorf(capability.ErrInvalidParams, "excludeTags[%d] is %d bytes; at most %d are allowed", i, len(t), logsFilterTagMax)
		}
	}
	if len(f.TagPriorities) > logsTagPrioritiesMax {
		return nil, capability.Errorf(capability.ErrInvalidParams, "tagPriorities lists %d entries; at most %d are allowed", len(f.TagPriorities), logsTagPrioritiesMax)
	}
	for i, e := range f.TagPriorities {
		if len(e) > logsFilterTagMax+2 {
			return nil, capability.Errorf(capability.ErrInvalidParams, "tagPriorities[%d] is %d bytes; a tag is at most %d", i, len(e), logsFilterTagMax)
		}
		// The tag may hold a colon itself: the priority is what follows the last one.
		cut := strings.LastIndexByte(e, ':')
		tag, letter := "", ""
		if cut >= 0 {
			tag, letter = e[:cut], e[cut+1:]
		}
		rank, ok := letterRank[letter]
		if cut < 0 || strings.TrimSpace(tag) == "" || !ok {
			return nil, capability.Errorf(capability.ErrInvalidParams, "tagPriorities[%d] is not Tag:L with L one of V, D, I, W, E, F, S", i)
		}
		if tag == "*" {
			return nil, capability.Errorf(capability.ErrInvalidParams, "tagPriorities[%d] names the tag \"*\": tags are matched exactly, with no wildcard; minPriority is the default for every other tag", i)
		}
		if c.tagRank == nil {
			c.tagRank = map[string]int{}
		}
		if _, dup := c.tagRank[tag]; dup {
			return nil, capability.Errorf(capability.ErrInvalidParams, "tagPriorities[%d] names the tag %q a second time", i, tag)
		}
		c.tagRank[tag] = rank
	}
	text, err := textfilter.Compile(textfilter.Spec{Match: f.Match, Exclude: f.Exclude})
	if err != nil {
		return nil, err
	}
	c.text, c.hasText = text, !text.Empty()
	if len(f.Processes) > logsProcessesMax {
		return nil, capability.Errorf(capability.ErrInvalidParams, "processes lists %d names; at most %d are allowed", len(f.Processes), logsProcessesMax)
	}
	for i, n := range f.Processes {
		if !processNameOK(n) {
			return nil, capability.Errorf(capability.ErrInvalidParams, "processes[%d] is not a process name: use 1 to %d bytes of letters, digits and . _ : @ / + -", i, logsProcessNameMax)
		}
	}
	return c, nil
}

// passes applies the filter in ADR-0135 §3's order, scope aside: the priority (the tag's
// own minimum, else minPriority), tags, excludeTags, contains, then the patterns over
// "tag: message". The subject is built, and the patterns run, only when there are any.
func (c *compiledFilter) passes(l logLine) bool {
	rank, ranked := c.minRank, c.minRank >= 0
	if r, ok := c.tagRank[l.tag]; ok {
		rank, ranked = r, true
	}
	if ranked && priorityRank[l.priority] < rank {
		return false
	}
	if len(c.tags) > 0 && !containsString(c.tags, l.tag) {
		return false
	}
	if containsString(c.excludeTags, l.tag) {
		return false
	}
	if c.contains != "" && !strings.Contains(l.message, c.contains) {
		return false
	}
	if c.hasText && !c.text.Keep(l.tag+": "+l.message) {
		return false
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// costly reports whether passes may run a pattern, which is what the time budget bounds.
func (c *compiledFilter) costly() bool { return c.hasText }

// judgeOrder says which end of the log a filter pass starts from, and so which lines a
// spent budget leaves unjudged. device.logs keeps the newest lines, so its pass judges
// the newest first and a budget that runs out loses the oldest; keep: oldest (step 4)
// judges forward and loses the newest.
type judgeOrder int

const (
	judgeNewestFirst judgeOrder = iota
	judgeOldestFirst
)

// passResult is what one filter pass found. The kept lines are in log order whatever the
// order the pass judged them in.
type passResult struct {
	lines []logLine
	flags []bool // flags[i] is the crash flag of lines[i]
	// stopped is set when the time budget ran out; edge is then the device time of the
	// last line judged, and the lines beyond it in the pass's direction were not.
	stopped bool
	edge    time.Time
}

// runPass judges the lines in the given order. inScope says whether line i is the call's
// (the app's, the named processes', or everyone's); crash says whether it belongs to a
// crash block. Only a pass with patterns reads the clock: after every line it judged,
// since one line is the smallest unit a budget cannot interrupt. A cancelled ctx is
// returned as its error.
func runPass(ctx context.Context, lines []logLine, crash []bool, inScope func(int) bool, c *compiledFilter, order judgeOrder, budget time.Duration) (passResult, error) {
	var res passResult
	var clock *textfilter.Checker
	if c.costly() {
		clock = textfilter.Pass(ctx, budget)
	}
	keep := func(i int) {
		res.lines = append(res.lines, lines[i])
		res.flags = append(res.flags, crash[i])
	}
	n := len(lines)
judging:
	for k := 0; k < n; k++ {
		i := k
		if order == judgeNewestFirst {
			i = n - 1 - k
		}
		if !inScope(i) {
			continue
		}
		if c.passes(lines[i]) {
			keep(i)
		}
		if clock == nil {
			continue
		}
		switch clock.Check() {
		case textfilter.Cancelled:
			return passResult{}, ctx.Err()
		case textfilter.BudgetSpent:
			res.stopped, res.edge = true, lines[i].at
			break judging // the line just judged is the last
		}
	}
	if order == judgeNewestFirst {
		for a, b := 0, len(res.lines)-1; a < b; a, b = a+1, b-1 {
			res.lines[a], res.lines[b] = res.lines[b], res.lines[a]
			res.flags[a], res.flags[b] = res.flags[b], res.flags[a]
		}
	}
	return res, nil
}

// budgetWarning says that a spent budget left lines unfiltered, and where the judged
// lines end, in the device's own clock.
func budgetWarning(budget time.Duration, edge time.Time, order judgeOrder) string {
	judged, left := "from the newest back to", "older"
	if order == judgeOldestFirst {
		judged, left = "from the oldest up to", "newer"
	}
	return "the filter pass stopped at its " + budget.String() + " budget after judging the lines " + judged + " " +
		rfcMicro(edge) + " (device time): " + left + " lines were not filtered, so matches among them are missing"
}

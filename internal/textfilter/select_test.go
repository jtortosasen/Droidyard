package textfilter

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// render writes picks as "1c 2 3c": an index, with c when it is context only.
func render(picks []Pick) string {
	parts := make([]string, len(picks))
	for i, p := range picks {
		parts[i] = strconv.Itoa(p.Index)
		if p.Context {
			parts[i] += "c"
		}
	}
	return strings.Join(parts, " ")
}

func in(set ...int) func(int) bool {
	return func(i int) bool {
		for _, s := range set {
			if s == i {
				return true
			}
		}
		return false
	}
}

// Decision (plan m3i step 2): Select is grep -C over n items. Each expectation below was
// written by hand from `grep -n -C<context>` on a ten-line file (l0..l9) whose matches
// are the listed lines: context lines carry c; windows that overlap or touch print as
// one group (grep prints no `--` between them); a gap of one or more lines splits them
// (indices jump).
func TestSelectLikeGrepC(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		matched []int
		context int
		want    string
	}{
		{"no match", 10, nil, 2, ""},
		{"empty input", 0, nil, 2, ""},
		{"one match, no context", 10, []int{5}, 0, "5"},
		{"one match, C1", 10, []int{5}, 1, "4c 5 6c"},
		{"one match, C3", 10, []int{5}, 3, "2c 3c 4c 5 6c 7c 8c"},
		{"match at the first item", 10, []int{0}, 2, "0 1c 2c"},
		{"match at the last item", 10, []int{9}, 2, "7c 8c 9"},
		{"context larger than the input", 3, []int{1}, 10, "0c 1 2c"},
		{"overlapping windows merge", 10, []int{2, 4}, 1, "1c 2 3c 4 5c"},
		{"adjoining windows merge", 10, []int{2, 5}, 1, "1c 2 3c 4c 5 6c"},
		{"windows one apart stay two groups", 10, []int{2, 6}, 1, "1c 2 3c 5c 6 7c"},
		{"adjacent matches, no context, are one run", 10, []int{1, 2, 3}, 0, "1 2 3"},
		{"separated matches, no context", 10, []int{1, 3}, 0, "1 3"},
		{"a match inside another's context stays a match", 10, []int{4, 5}, 2, "2c 3c 4 5 6c 7c"},
		{"everything matches", 4, []int{0, 1, 2, 3}, 2, "0 1 2 3"},
		{"negative context is none", 10, []int{5}, -3, "5"},
		{"C2, two groups with edges", 12, []int{1, 10}, 2, "0c 1 2c 3c 8c 9c 10 11c"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			picks, count, truncated := Select(c.n, in(c.matched...), c.context, 0, Newest)
			if got := render(picks); got != c.want {
				t.Errorf("picks = %q; want %q", got, c.want)
			}
			if count != len(c.matched) {
				t.Errorf("matchedCount = %d; want %d", count, len(c.matched))
			}
			if truncated {
				t.Error("truncated with no cap")
			}
		})
	}

	// matched is asked once about each item, in order: callers pass a closure over a
	// slice and may count on it.
	var seen []int
	Select(5, func(i int) bool { seen = append(seen, i); return false }, 1, 0, Newest)
	if !reflect.DeepEqual(seen, []int{0, 1, 2, 3, 4}) {
		t.Errorf("matched was called for %v; want each of 0..4 once, in order", seen)
	}
}

// Decision (plan m3i Q1/step 2): max counts every pick, context included; Newest drops
// whole groups from the start and Oldest from the end; a group that does not fit is cut
// from its far side, so a cap smaller than one group still returns that group's nearest
// part rather than nothing.
func TestSelectKeepAndMax(t *testing.T) {
	// Groups with context 1: [1,3] (match 2), [8,11] (matches 9, 10), [16,18] (match 17):
	// 3 + 4 + 3 = 10 picks, 4 matches.
	const n = 20
	matched := in(2, 9, 10, 17)
	cases := []struct {
		name      string
		keep      Keep
		limit     int
		want      string
		truncated bool
	}{
		{"no cap", Newest, 0, "1c 2 3c 8c 9 10 11c 16c 17 18c", false},
		{"cap equals the total", Newest, 10, "1c 2 3c 8c 9 10 11c 16c 17 18c", false},
		{"cap over the total", Oldest, 11, "1c 2 3c 8c 9 10 11c 16c 17 18c", false},
		{"newest, cap on a group boundary", Newest, 7, "8c 9 10 11c 16c 17 18c", true},
		{"newest drops whole groups, then cuts the straddler at its start", Newest, 6, "9 10 11c 16c 17 18c", true},
		{"newest, cap of one group", Newest, 3, "16c 17 18c", true},
		{"newest, cap smaller than one group", Newest, 2, "17 18c", true},
		{"newest, cap of one keeps only context here", Newest, 1, "18c", true},
		{"oldest, cap on a group boundary", Oldest, 3, "1c 2 3c", true},
		{"oldest cuts the straddler at its end", Oldest, 5, "1c 2 3c 8c 9", true},
		{"oldest, cap smaller than one group", Oldest, 2, "1c 2", true},
		{"oldest, a cap that fits two groups exactly", Oldest, 7, "1c 2 3c 8c 9 10 11c", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			picks, count, truncated := Select(n, matched, 1, c.limit, c.keep)
			if got := render(picks); got != c.want {
				t.Errorf("picks = %q; want %q", got, c.want)
			}
			if truncated != c.truncated {
				t.Errorf("truncated = %v; want %v", truncated, c.truncated)
			}
			if count != 4 {
				t.Errorf("matchedCount = %d; want 4 (every match, picked or not)", count)
			}
			if c.limit > 0 && len(picks) > c.limit {
				t.Errorf("%d picks over a cap of %d", len(picks), c.limit)
			}
		})
	}
}

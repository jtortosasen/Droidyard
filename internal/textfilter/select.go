package textfilter

// Keep says which end of a result survives a cap.
type Keep int

const (
	// Newest drops whole groups from the start.
	Newest Keep = iota
	// Oldest drops whole groups from the end.
	Oldest
)

// Pick is one item chosen by Select. Context is true for an item chosen only because it
// neighbours a matched one.
type Pick struct {
	Index   int
	Context bool
}

// Select chooses items the way grep -C does. matched is called once for each of the n
// items, in order; around every matched item the context items before and after it join
// (a negative context is 0), and windows that overlap or touch merge into one group, as
// grep prints them without a separator. limit caps the picks, context included; limit <= 0
// means no cap. Over the cap, keep decides which groups survive: Newest drops the
// earliest whole groups, Oldest the latest, and the group that straddles the cap is cut
// from its far side (Newest loses its earliest items, Oldest its latest), so a cut group
// may keep only context. Picks come back in index order. matchedCount is every matched
// item, picked or not; truncated is true when the cap dropped any pick.
func Select(n int, matched func(int) bool, context, limit int, keep Keep) (picks []Pick, matchedCount int, truncated bool) {
	if context < 0 {
		context = 0
	}
	type group struct{ lo, hi int } // inclusive
	var (
		hits   []int
		groups []group
		total  int
	)
	for i := 0; i < n; i++ {
		if !matched(i) {
			continue
		}
		hits = append(hits, i)
		lo, hi := max(i-context, 0), min(i+context, n-1)
		if g := len(groups) - 1; g >= 0 && lo <= groups[g].hi+1 {
			total += hi - groups[g].hi
			groups[g].hi = hi
			continue
		}
		groups = append(groups, group{lo, hi})
		total += hi - lo + 1
	}
	matchedCount = len(hits)
	if len(groups) == 0 {
		return nil, 0, false
	}

	// The picks are the union of the groups, so the cap is one index: every pick at or
	// after lo (Newest) or at or before hi (Oldest) survives.
	lo, hi := groups[0].lo, groups[len(groups)-1].hi
	if limit > 0 && total > limit {
		truncated = true
		remaining := limit
		if keep == Oldest {
			for _, g := range groups {
				if size := g.hi - g.lo + 1; size < remaining {
					remaining -= size
				} else {
					hi = g.lo + remaining - 1
					break
				}
			}
		} else {
			for g := len(groups) - 1; g >= 0; g-- {
				if size := groups[g].hi - groups[g].lo + 1; size < remaining {
					remaining -= size
				} else {
					lo = groups[g].hi - remaining + 1
					break
				}
			}
		}
	}

	h := 0
	for _, g := range groups {
		for i := max(g.lo, lo); i <= min(g.hi, hi); i++ {
			for h < len(hits) && hits[h] < i {
				h++
			}
			picks = append(picks, Pick{Index: i, Context: h >= len(hits) || hits[h] != i})
		}
	}
	return picks, matchedCount, truncated
}

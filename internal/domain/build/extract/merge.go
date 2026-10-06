package extract

// merge implements gradle.md §Merge rules 1–2. Records are deduplicated on
// (severity, file, line, column?, message head): the column is a key component when
// two records both carry one, and a record without a column joins either, which is
// how a location gets filled from another source. When several sources produce one
// error the richest wins (failure > event > console) and its missing fields are
// filled from the others.
func merge(cands []Record) []Record {
	var out []Record
	for _, c := range cands {
		at := -1
		for i := range out {
			if sameKey(out[i], c) {
				at = i
				break
			}
		}
		if at < 0 {
			out = append(out, c)
			continue
		}
		out[at] = combine(out[at], c)
	}
	return out
}

func sameKey(a, b Record) bool {
	if a.Severity != b.Severity || a.File != b.File || a.Line != b.Line || a.head != b.head {
		return false
	}
	return a.Column == b.Column || a.Column == 0 || b.Column == 0
}

// sourceRank is the merge precedence (gradle.md §Merge rules 2).
func sourceRank(s Source) int {
	switch s {
	case SourceFailure:
		return 3
	case SourceEvent:
		return 2
	default:
		return 1
	}
}

// combine keeps the richer record and fills what it lacks from the poorer one.
func combine(a, b Record) Record {
	rich, poor := a, b
	if sourceRank(b.Source) > sourceRank(a.Source) {
		rich, poor = b, a
	}
	r := rich
	if r.File == "" {
		r.File = poor.File
	}
	if r.Line == 0 {
		r.Line = poor.Line
	}
	if r.Column == 0 {
		r.Column = poor.Column
	}
	if r.Message == "" {
		r.Message = poor.Message
	}
	if r.head == "" {
		r.head = poor.head
	}
	if r.ProblemID == "" {
		r.ProblemID = poor.ProblemID
	}
	if len(r.Solutions) == 0 {
		r.Solutions = poor.Solutions
	}
	if r.DocumentationLink == "" {
		r.DocumentationLink = poor.DocumentationLink
	}
	if r.Resolved == nil {
		r.Resolved = poor.Resolved
	}
	if r.Excerpt == "" {
		r.Excerpt = poor.Excerpt
	}
	if !r.External {
		r.External = poor.External
	}
	if !r.Merged {
		r.Merged = poor.Merged
	}
	return r
}

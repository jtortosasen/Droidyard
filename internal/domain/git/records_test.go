package git

// The pure half of the history reads: how git's output is parsed, ordered and capped, tested
// on synthetic output with no git and no repository (owner, 2026-10-15). Each cap has one
// test in real git too, which says where git's output meets the cap; what the Go code then
// does with the output is decided here.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// oidN is a distinct, well-formed object id for n.
func oidN(n int) string { return fmt.Sprintf("%040x", n+1) }

// refLine is one line of refsFormat's output, as for-each-ref writes it: the object's type
// and id, the type and id it peels to (empty unless it is a tag object), the creation date,
// the ref, its upstream.
func refLine(typ, oid, peeledTyp, peeled string, when int64, ref, upstream string) string {
	return strings.Join([]string{typ, oid, peeledTyp, peeled, strconv.FormatInt(when, 10), ref, upstream}, "\x00") + "\n"
}

func commitRef(oid, ref string) string { return refLine("commit", oid, "", "", 0, ref, "") }

// annotated is a tag object oid peeling to the commit target.
func annotated(tagOid, target string, when int64, ref string) string {
	return refLine("tag", tagOid, "commit", target, when, ref, "")
}

// Decision (Q1 §2): the tip set is HEAD, then the local branches, then the remote-tracking
// branches, then the tags by newest date and name, every tag peeled to its commit however
// many tags deep and a tag on a tree dropped, each commit once at its first place; at most
// maxTips, and truncated says one was dropped. The refs that share a commit count once: the
// cap is on distinct commits (TestTipsCapped holds the same against real git).
func TestTipSet(t *testing.T) {
	t.Parallel()
	head := oidN(0)
	out := commitRef(oidN(1), "refs/heads/a") + commitRef(head, "refs/heads/main") +
		commitRef(oidN(2), "refs/remotes/origin/main") + commitRef(oidN(1), "refs/remotes/origin/a") +
		refLine("commit", oidN(3), "", "", 100, "refs/tags/old", "") +
		annotated(oidN(90), oidN(4), 300, "refs/tags/new") +
		refLine("commit", oidN(5), "", "", 300, "refs/tags/aaa-same-date", "") +
		refLine("tag", oidN(91), "tree", oidN(92), 400, "refs/tags/on-a-tree", "") +
		refLine("tag", oidN(93), "commit", oidN(4), 250, "refs/tags/nested", "") // a duplicate of "new"'s commit
	got, truncated, err := tipSet(head, []byte(out), false)
	if err != nil || truncated {
		t.Fatalf("tipSet: %v truncated %v", err, truncated)
	}
	// head; heads in ref order (a, then main, which is HEAD again); remotes (main, then a, a
	// duplicate); tags newest first, a name within a date: aaa-same-date, new (its commit
	// first met again at "nested"); old. A tag on a tree has no commit.
	want := []string{head, oidN(1), oidN(2), oidN(5), oidN(4), oidN(3)}
	if !slices.Equal(got, want) {
		t.Errorf("tips\n got %v\nwant %v", got, want)
	}

	// No commit yet: no HEAD, no tips, and no error.
	if got, truncated, err := tipSet("", nil, false); err != nil || truncated || len(got) != 0 {
		t.Errorf("an empty repository: %v truncated %v err %v", got, truncated, err)
	}

	// A line of the wrong shape is GIT_FAILED, not a half-read set.
	if _, _, err := tipSet("", []byte("commit\x00x\n"), false); err == nil {
		t.Errorf("a ref line of two fields was accepted")
	}
}

// Decision (Q1 §2): the cap counts distinct commits after the refs that share one are folded:
// exactly maxTips distinct tips are all kept and not truncated, one more keeps maxTips and
// says so, and a few hundred more refs on a commit already in the set change nothing; HEAD
// and the branches stay first, the oldest tags are what drops. This is what
// TestTipsCapCountsDistinctCommits proved against a 6,000-commit repository.
func TestTipSetCapCountsDistinctCommits(t *testing.T) {
	t.Parallel()
	head, remote := oidN(0), oidN(1)
	build := func(tags, dups int) string {
		var b strings.Builder
		b.WriteString(commitRef(head, "refs/heads/main"))
		b.WriteString(commitRef(remote, "refs/remotes/origin/main"))
		for i := 0; i < tags; i++ {
			// Newest first by date, so the cut keeps the lowest i; every tenth annotated.
			when := int64(1_000_000 - i)
			if i%10 == 9 {
				b.WriteString(annotated(oidN(1_000_000+i), oidN(2+i), when, fmt.Sprintf("refs/tags/v%05d", i)))
			} else {
				b.WriteString(refLine("commit", oidN(2+i), "", "", when, fmt.Sprintf("refs/tags/v%05d", i), ""))
			}
		}
		for i := 0; i < dups; i++ { // more refs on a commit that is already a tip
			b.WriteString(refLine("commit", remote, "", "", 5, fmt.Sprintf("refs/tags/zdup%03d", i), ""))
		}
		return b.String()
	}
	for _, c := range []struct {
		name            string
		tags, dups      int
		wantLen         int
		wantTruncated   bool
		wantFirstMissed int // the index of the first tag commit that must be missing, or -1
	}{
		// HEAD and origin/main are two of the 5,000.
		{"4,998 tags make 5,000 distinct", maxTips - 2, 0, maxTips, false, -1},
		{"4,999 tags make 5,001 distinct", maxTips - 1, 0, maxTips, true, maxTips - 2},
		{"5,001 distinct and 300 duplicates", maxTips - 1, 300, maxTips, true, maxTips - 2},
		{"exactly 5,000 distinct and 300 duplicates (over 5,000 refs)", maxTips - 2, 300, maxTips, false, -1},
		{"far over", maxTips + 500, 0, maxTips, true, maxTips - 2},
	} {
		got, truncated, err := tipSet(head, []byte(build(c.tags, c.dups)), false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.wantLen || truncated != c.wantTruncated || len(dedupe(got)) != len(got) {
			t.Errorf("%s: %d tips (%d distinct), truncated %v; want %d, %v", c.name, len(got), len(dedupe(got)), truncated, c.wantLen, c.wantTruncated)
		}
		if got[0] != head || got[1] != remote {
			t.Errorf("%s: HEAD and the branches are not first: %v", c.name, got[:2])
		}
		if c.wantFirstMissed >= 0 && slices.Contains(got, oidN(2+c.wantFirstMissed)) {
			t.Errorf("%s: the oldest tag's commit is among the tips; the tail is what drops", c.name)
		}
	}

	// Output the runner cut at its own cap: the last (cut) line is never read, and the set
	// says it is not whole even though it is under the cap.
	out := commitRef(head, "refs/heads/main") + strings.TrimSuffix(commitRef(oidN(7), "refs/heads/zz-cu"), "\n")
	got, truncated, err := tipSet(head, []byte(out), true)
	if err != nil || !truncated || !slices.Equal(got, []string{head}) {
		t.Errorf("cut output: %v truncated %v err %v; want only HEAD (the cut last line dropped) and truncated", got, truncated, err)
	}
	if got, truncated, err := tipSet(head, []byte(commitRef(oidN(7), "refs/heads/a")), true); err != nil || !truncated || !slices.Equal(got, []string{head, oidN(7)}) {
		t.Errorf("cut output ending on a whole line: %v truncated %v err %v; want its whole lines kept and truncated", got, truncated, err)
	}
	if got, truncated, _ := tipSet(head, []byte("commit\x00half-a-li"), true); !truncated || !slices.Equal(got, []string{head}) {
		t.Errorf("output cut inside its first line: %v truncated %v", got, truncated)
	}
}

// Decision (Q1 §1): each of git.refs' lists holds at most maxRefList, with its own truncated
// flag, and exactly maxRefList is not truncated; a list is cut at the cap git's output hit
// when the output itself was cut, and every list after it too; the others are not marked.
func TestParseRefsCaps(t *testing.T) {
	t.Parallel()
	build := func(branches, remotes, tags int) string {
		var b strings.Builder
		for i := 0; i < branches; i++ {
			b.WriteString(commitRef(oidN(i), fmt.Sprintf("refs/heads/b%05d", i)))
		}
		for i := 0; i < remotes; i++ {
			b.WriteString(commitRef(oidN(i), fmt.Sprintf("refs/remotes/origin/r%05d", i)))
		}
		for i := 0; i < tags; i++ {
			b.WriteString(refLine("commit", oidN(i), "", "", int64(10_000-i), fmt.Sprintf("refs/tags/t%05d", i), ""))
		}
		return b.String()
	}
	type flags struct{ b, r, t bool }
	for _, c := range []struct {
		name                  string
		branches, remotes, tg int
		wantB, wantR, wantT   int
		trunc                 flags
	}{
		{"exactly the cap, nothing truncated", maxRefList, maxRefList, maxRefList, maxRefList, maxRefList, maxRefList, flags{}},
		{"one over in each", maxRefList + 1, maxRefList + 1, maxRefList + 1, maxRefList, maxRefList, maxRefList, flags{true, true, true}},
		{"one list over; the flags are independent", 3, maxRefList + 1, 3, 3, maxRefList, 3, flags{false, true, false}},
		{"tags over only", 1, 1, maxRefList + 1, 1, 1, maxRefList, flags{false, false, true}},
	} {
		lists, err := parseRefs([]byte(build(c.branches, c.remotes, c.tg)), false, []string{"origin"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		b, bt := capList(lists.branches, lists.cut[kindHead])
		r, rt := capList(lists.remotes, lists.cut[kindRemote])
		tg, tt := capList(lists.tags, lists.cut[kindTag])
		if len(b) != c.wantB || len(r) != c.wantR || len(tg) != c.wantT || (flags{bt, rt, tt}) != c.trunc {
			t.Errorf("%s: %d/%d/%d kept, truncated %v; want %d/%d/%d, %v", c.name, len(b), len(r), len(tg), flags{bt, rt, tt}, c.wantB, c.wantR, c.wantT, c.trunc)
		}
	}
	// What drops is the tail: the alphabetical end of the branches, the oldest tags.
	lists, _ := parseRefs([]byte(build(maxRefList+1, 0, maxRefList+1)), false, nil)
	b, _ := capList(lists.branches, false)
	tg, _ := capList(lists.tags, false)
	if b[len(b)-1].Name != fmt.Sprintf("b%05d", maxRefList-1) || tg[0].Name != "t00000" || tg[len(tg)-1].Name != fmt.Sprintf("t%05d", maxRefList-1) {
		t.Errorf("kept branches end at %s, tags run %s..%s; want the first 5,000 of each, newest tag first", b[len(b)-1].Name, tg[0].Name, tg[len(tg)-1].Name)
	}

	// Output git's runner cut: the cut line is dropped and its kind, with every later one, is
	// marked cut; the kinds before it are whole.
	for _, c := range []struct {
		name  string
		out   string
		upTo  int // the first kind marked cut
		count [3]int
	}{
		{"cut inside the tags", build(2, 2, 2) + "commit\x00half", kindTag, [3]int{2, 2, 2}},
		{"cut inside the remote-tracking branches", build(2, 2, 0) + "commit\x00half", kindRemote, [3]int{2, 2, 0}},
		{"cut inside the branches", build(2, 0, 0) + "commit\x00half", kindHead, [3]int{2, 0, 0}},
	} {
		lists, err := parseRefs([]byte(c.out), true, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		// The last complete line is the last "\n"; the half line has none after it, so it is
		// the one dropped, and the kind it would be in is the last whole line's.
		for k := kindHead; k <= kindTag; k++ {
			if lists.cut[k] != (k >= c.upTo) {
				t.Errorf("%s: kind %d cut = %v; want %v", c.name, k, lists.cut[k], k >= c.upTo)
			}
		}
		if len(lists.branches) != c.count[0] || len(lists.remotes) != c.count[1] || len(lists.tags) != c.count[2] {
			t.Errorf("%s: %d/%d/%d listed; want %v (whole lines only)", c.name, len(lists.branches), len(lists.remotes), len(lists.tags), c.count)
		}
	}
}

// Decision (review of step 5): a remote-tracking ref is split at the end of the longest
// configured remote name that prefixes it, not at the first slash, so a remote team/origin's
// branches are not credited to a remote team; a ref no configured remote claims falls back to
// the first slash. TestRefsRemoteNames holds the same against `git remote` and real refs.
func TestSplitRemote(t *testing.T) {
	t.Parallel()
	names := remoteNames([]byte("team\nteam/origin\norigin\n"), false)
	if !slices.Equal(names, []string{"team/origin", "origin", "team"}) {
		t.Fatalf("remoteNames = %v; want the longest first, ties in git's order", names)
	}
	for rest, want := range map[string][2]string{
		"team/main":            {"team", "main"},
		"team/dev/y":           {"team", "dev/y"},
		"team/origin/x":        {"team/origin", "x"},
		"team/origin/feat/z":   {"team/origin", "feat/z"},
		"origin/main":          {"origin", "main"},
		"stray/q/r":            {"stray", "q/r"},
		"team/originx/y":       {"team", "originx/y"},
		"ünï/zz-marker-remote": {"ünï", "zz-marker-remote"},
	} {
		if remote, name := splitRemote(rest, names); remote != want[0] || name != want[1] {
			t.Errorf("splitRemote(%q) = %q, %q; want %q, %q", rest, remote, name, want[0], want[1])
		}
	}
	// A name git printed cut at the cap is never read: the last line is dropped.
	if got := remoteNames([]byte("origin\nhalf-a-na"), true); !slices.Equal(got, []string{"origin"}) {
		t.Errorf("remoteNames over cut output = %v; want only origin", got)
	}
}

// Decision (ADR-0069 §6; Q1 §1): the records of git.log are logFields NUL-terminated fields
// each, read whole only: a trailing partial record (the output cut at the cap) is dropped, never
// half-read; text is valid UTF-8; a root commit has an empty parent list, not none; a record
// that is not what git writes is GIT_FAILED.
func TestParseLog(t *testing.T) {
	t.Parallel()
	rec := func(oid, parents, name, email, when, subject string) string {
		return strings.Join([]string{oid, parents, name, email, when, subject}, "\x00") + "\x00"
	}
	a, b, c := oidN(1), oidN(2), oidN(3)
	out := rec(a, b+" "+c, "Ann", "ann@x", "1700000000", "merge") + rec(b, "", "Zo\xe9", "bad\x80@x", "1700000001", "root \xff end")
	got, err := parseLog([]byte(out))
	if err != nil || len(got) != 2 {
		t.Fatalf("parseLog: %v, %d commits", err, len(got))
	}
	if !slices.Equal(got[0].Parents, []string{b, c}) || got[1].Parents == nil || len(got[1].Parents) != 0 {
		t.Errorf("parents %v and %#v; want the two in order and an empty non-nil list", got[0].Parents, got[1].Parents)
	}
	if got[1].AuthorName != "Zo�" || got[1].AuthorEmail != "bad�@x" || got[1].Subject != "root � end" || got[0].CommitTime != 1700000000 {
		t.Errorf("record = %+v; want invalid bytes made U+FFFD and the committer time", got[1])
	}

	// A cut record: whole fields of it, and then a half field, are both dropped.
	for name, cut := range map[string]string{
		"a half field":         rec(a, "", "n", "e", "1", "s") + b[:20],
		"whole fields, no end": rec(a, "", "n", "e", "1", "s") + strings.Join([]string{b, "", "n"}, "\x00") + "\x00",
		"nothing after":        rec(a, "", "n", "e", "1", "s"),
	} {
		got, err := parseLog([]byte(cut))
		if err != nil || len(got) != 1 || got[0].Oid != a {
			t.Errorf("%s: %d commits, %v; want only the whole record", name, len(got), err)
		}
	}
	if got, err := parseLog(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no output: %#v, %v; want an empty list", got, err)
	}
	for name, bad := range map[string]string{
		"a time that is not a number": rec(a, "", "n", "e", "yesterday", "s"),
		"an oid that is not one":      rec("not-an-oid", "", "n", "e", "1", "s"),
	} {
		if _, err := parseLog([]byte(bad)); err == nil || err.Code != CodeFailed {
			t.Errorf("%s: %v; want GIT_FAILED", name, err)
		}
	}
}

// Decision (plan m2n step 7, ADR-0069 §4): a commit's message is at most maxMessage bytes
// and messageTruncated says when it is longer, cut at a character and never in the middle of
// one; exactly maxMessage is whole; and a message the runner's cap cut is marked too. The
// files are at most maxShowFiles, in the order git wrote them, truncated says when there were
// more; exactly maxShowFiles are whole. TestShowCaps holds the message and the files against
// real git; here are the boundaries.
func TestShowCapsParsed(t *testing.T) {
	t.Parallel()
	oid := oidN(1)
	showOut := func(msg string) result {
		return result{Stdout: []byte(strings.Join([]string{oid, "", "A", "a@x", "2026-01-01T00:00:00Z", "C", "c@x", "2026-01-01T00:00:00Z", msg}, "\x00") + "\x00")}
	}
	for _, c := range []struct {
		name, msg string
		wantLen   int
		truncated bool
	}{
		{"exactly the cap", strings.Repeat("x", maxMessage), maxMessage, false},
		{"one byte more", strings.Repeat("x", maxMessage+1), maxMessage, true},
		{"a character on the limit", "a" + strings.Repeat("é", 40000), maxMessage - 1, true},
		{"three bytes of a four-byte character on the limit", strings.Repeat("x", maxMessage-3) + "😀", maxMessage - 3, true},
		{"short", "subject\n\nbody\n", len("subject\n\nbody\n"), false},
		{"empty", "", 0, false},
	} {
		res, err := parseShow(oid, showOut(c.msg))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(res.Message) != c.wantLen || res.MessageTruncated != c.truncated || !strings.HasPrefix(c.msg, res.Message) {
			t.Errorf("%s: %d bytes, truncated %v; want %d, %v, a prefix of the message", c.name, len(res.Message), res.MessageTruncated, c.wantLen, c.truncated)
		}
	}
	cutRes := showOut("cut by the runner")
	cutRes.Truncated = true
	if res, err := parseShow(oid, cutRes); err != nil || !res.MessageTruncated {
		t.Errorf("a message the runner cut: %+v, %v; want messageTruncated", res, err)
	}

	const modes = ":100644 100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb "
	files := func(n int) []byte {
		var raw, counts strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&raw, "%sM\x00f%05d\x00", modes, i)
			fmt.Fprintf(&counts, "1\t0\tf%05d\x00", i)
		}
		return []byte(raw.String() + counts.String())
	}
	for _, c := range []struct {
		n         int
		truncated bool
	}{{maxShowFiles, false}, {maxShowFiles + 1, true}, {3, false}} {
		got, truncated, err := parseShowFiles(files(c.n), false, "")
		if err != nil || truncated != c.truncated || len(got) != min(c.n, maxShowFiles) {
			t.Fatalf("%d files: %d listed, truncated %v, %v", c.n, len(got), truncated, err)
		}
		if got[0].Path != "f00000" || got[len(got)-1].Path != fmt.Sprintf("f%05d", len(got)-1) || countsText(got[0]) != "1 0" {
			t.Errorf("%d files: first %q last %q; want the first %d by path with their counts", c.n, got[0].Path, got[len(got)-1].Path, len(got))
		}
	}
	// The runner's cap fell in the counts: the files whose count was cut are listed without
	// one, the list says it is truncated, and the half record is not read.
	whole := files(10)
	cut := whole[:len(whole)-len("1\t0\tf00009\x00")-4]
	got, truncated, err := parseShowFiles(cut, true, "")
	if err != nil || !truncated || len(got) != 10 {
		t.Fatalf("cut in the counts: %d listed, truncated %v, %v", len(got), truncated, err)
	}
	if countsText(got[0]) != "1 0" || got[9].Added != nil {
		t.Errorf("cut in the counts: first %s, last %v; want the first counted and the last not", countsText(got[0]), got[9].Added)
	}
	// ...and in the raw records: no file has counts.
	rawOnly := []byte(modes + "A\x00f00000\x00" + modes + "A\x00f00001\x00" + modes[:20])
	got, truncated, err = parseShowFiles(rawOnly, true, "")
	if err != nil || !truncated || len(got) != 2 || got[0].Added != nil || got[1].Added != nil {
		t.Errorf("cut in the raw records: %+v truncated %v err %v; want the two whole files with no counts", got, truncated, err)
	}
}

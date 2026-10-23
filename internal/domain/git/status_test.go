package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "status", name+".z"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rec(fields ...string) []byte {
	return []byte(strings.Join(fields, "\x00") + "\x00")
}

func marks(st Status) map[string]Mark {
	m := map[string]Mark{}
	for _, e := range st.Entries {
		m[e.Path] = e.Mark
	}
	return m
}

// Decision (ADR-0066 §5): the marks against HEAD, one row at a time. Rows come from
// real git output (the fixtures) or, where git cannot be made to print one cheaply, a
// hand-written record in git's format. `AD` is nothing, not added; a rename in either
// column is renamed; `RD` is its origin deleted; a deleted path that is also untracked
// is modified.
func TestMarksFromPorcelain(t *testing.T) {
	t.Parallel()
	// The worktree fixture holds every `1` row.
	wt := marks(parseStatus(fixture(t, "worktree"), "", 5000, false))
	for path, want := range map[string]Mark{
		"a_staged.txt":   MarkAdded,    // A.
		"am.txt":         MarkAdded,    // AM
		"ita.txt":        MarkAdded,    // .A (intent to add)
		"d_staged.txt":   MarkDeleted,  // D.
		"d_unstaged.txt": MarkDeleted,  // .D
		"md.txt":         MarkDeleted,  // MD
		"m_staged.txt":   MarkModified, // M.
		"m_unstaged.txt": MarkModified, // .M
		"mm.txt":         MarkModified, // MM
		"rmcached.txt":   MarkModified, // D. with an untracked file at the same path
		"u.txt":          MarkUntracked,
		"dir2/u2.txt":    MarkUntracked,
		"dir/in dir.txt": MarkModified,
		"sp ace.txt":     MarkModified,
		"ünï.txt":        MarkModified,
	} {
		if got, ok := wt[path]; !ok || got != want {
			t.Errorf("%q: got %q (present %v), want %q", path, got, ok, want)
		}
	}
	if _, ok := wt["ad.txt"]; ok {
		t.Errorf("AD must be nothing, got %q", wt["ad.txt"])
	}
	n := 0
	for _, e := range parseStatus(fixture(t, "worktree"), "", 5000, false).Entries {
		if e.Path == "rmcached.txt" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rmcached.txt appears %d times, want once", n)
	}

	// Renames: R., RM, RD and a plain mv.
	rn := parseStatus(fixture(t, "rename"), "", 5000, false)
	want := []Entry{
		{Path: "moved1.txt", Mark: MarkRenamed, OrigPath: "r1.txt"},
		{Path: "moved2.txt", Mark: MarkRenamed, OrigPath: "r2.txt"},
		{Path: "plain-moved.txt", Mark: MarkUntracked},
		{Path: "r3.txt", Mark: MarkDeleted}, // RD: the origin deleted, nothing at moved3.txt
		{Path: "r4.txt", Mark: MarkDeleted},
	}
	if !reflect.DeepEqual(rn.Entries, want) {
		t.Errorf("rename fixture:\n got %+v\nwant %+v", rn.Entries, want)
	}

	// .R: an intent-to-add file equal to a deleted one (ADR-0066 §5).
	ir := parseStatus(fixture(t, "intent-rename"), "", 5000, false)
	if want := []Entry{{Path: "new.txt", Mark: MarkRenamed, OrigPath: "old.txt"}}; !reflect.DeepEqual(ir.Entries, want) {
		t.Errorf("intent-rename: got %+v, want %+v", ir.Entries, want)
	}

	// Conflicts, `u`.
	cf := marks(parseStatus(fixture(t, "conflict"), "", 5000, false))
	if cf["both.txt"] != MarkConflicted || cf["f.txt"] != MarkConflicted {
		t.Errorf("conflict: %v", cf)
	}

	// Rows git is awkward to coax into: a type change, a copy, CD.
	hand := parseStatus(rec(
		"1 T. N... 100644 120000 120000 aa bb typechange.txt",
		"1 .T N... 100644 100644 120000 aa bb typechange2.txt",
		"2 C. N... 100644 100644 100644 aa aa C100 copy.txt", "orig.txt",
		"2 CD N... 100644 100644 000000 aa aa C100 copyd.txt", "orig2.txt",
	), "", 5000, false)
	if want := []Entry{
		{Path: "copy.txt", Mark: MarkAdded},
		{Path: "typechange.txt", Mark: MarkModified},
		{Path: "typechange2.txt", Mark: MarkModified},
	}; !reflect.DeepEqual(hand.Entries, want) {
		t.Errorf("hand-written rows: got %+v, want %+v", hand.Entries, want)
	}

	// Gitlinks (ADR-0125 §3): a record whose <sub> begins `S` is marked by the same table
	// and carries git's S<c><m><u> bits. The rows are as git printed them for a submodule
	// `lib` of the fixture TestSubmoduleRecordsAgainstHEAD builds (with `--ignore-submodules`
	// none or dirty; only `C` can be set under dirty), the conflict and the removal included.
	// `git mv lib lib2` printed a `2` record, `2 RM S.MU ... R100 lib2` under none and
	// `2 R. S... ... R100 lib2` under dirty: it is read as any rename, renamed with origPath,
	// and carries no submodule object (recorded, not decided: ADR-0125 does not cover it).
	const z = "160000 160000 160000 aa bb "
	gl := parseStatus(rec(
		"1 .M SC.. "+z+"commits",
		"1 M. S... "+z+"staged",
		"1 .M S.M. "+z+"modified",
		"1 .M S..U "+z+"untracked",
		"1 .M S.MU "+z+"content",
		"1 A. S... 000000 160000 160000 0000 bb added",
		"1 D. S... 160000 000000 000000 aa 0000 deleted",
		"u UU S... 160000 160000 160000 160000 aa bb cc conflicted",
		"1 .M N... 100644 100644 100644 aa bb plain.txt",
		"2 RM S.MU "+z+"R100 lib2", "lib",
	), "", 5000, false)
	if want := []string{
		"added added S...",
		"commits modified SC..",
		"conflicted conflicted S...",
		"content modified S.MU",
		"deleted deleted S...",
		"lib2 renamed -",
		"modified modified S.M.",
		"plain.txt modified -",
		"staged modified SC..", // `M.`: a new commit that is staged still reads "new commits"
		"untracked modified S..U",
	}; !reflect.DeepEqual(describeEntries(gl), want) {
		t.Errorf("gitlink rows:\n got %v\nwant %v", describeEntries(gl), want)
	}
}

// Decision: the head. An unborn branch has no oid, a detached HEAD has no branch.
func TestHeadFromPorcelain(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]Head{
		"clean":    {Oid: "ac12d5e6d573589110ebc74c3a83694371f3ed71", Branch: "main"},
		"unborn":   {Branch: "main"},
		"detached": {Oid: "ac12d5e6d573589110ebc74c3a83694371f3ed71"},
	} {
		if got := parseStatus(fixture(t, name), "", 5000, false).Head; got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
	if st := parseStatus(fixture(t, "clean"), "", 5000, false); st.Entries == nil || len(st.Entries) != 0 {
		t.Errorf("a clean tree must carry an empty, non-nil list, got %#v", st.Entries)
	}
}

// Decision: a workspace in a subdirectory sees only its own paths, relative to it. A
// rename from outside is an added file here; one to outside leaves its origin deleted.
func TestPrefix(t *testing.T) {
	t.Parallel()
	st := parseStatus(fixture(t, "subdir"), "app/", 5000, false)
	if want := []Entry{{Path: "in.txt", Mark: MarkModified}, {Path: "sub/new.txt", Mark: MarkUntracked}}; !reflect.DeepEqual(st.Entries, want) {
		t.Errorf("subdir: got %+v", st.Entries)
	}
	// An entry outside the prefix, and one whose name only starts like it.
	st = parseStatus(rec(
		"1 .M N... 100644 100644 100644 aa aa top.txt",
		"1 .M N... 100644 100644 100644 aa aa app2/x.txt",
		"1 .M N... 100644 100644 100644 aa aa app/x.txt",
	), "app/", 5000, false)
	if want := []Entry{{Path: "x.txt", Mark: MarkModified}}; !reflect.DeepEqual(st.Entries, want) {
		t.Errorf("outside: got %+v", st.Entries)
	}
	st = parseStatus(rec(
		"2 R. N... 100644 100644 100644 aa aa R100 app/in.txt", "elsewhere/was.txt",
		"2 R. N... 100644 100644 100644 aa aa R100 elsewhere/now.txt", "app/gone.txt",
		"2 R. N... 100644 100644 100644 aa aa R100 other/a.txt", "other/b.txt",
		"2 RD N... 100644 100644 000000 aa aa R100 elsewhere/c.txt", "app/gone2.txt",
	), "app/", 5000, false)
	if want := []Entry{
		{Path: "gone.txt", Mark: MarkDeleted},
		{Path: "gone2.txt", Mark: MarkDeleted},
		{Path: "in.txt", Mark: MarkAdded},
	}; !reflect.DeepEqual(st.Entries, want) {
		t.Errorf("renames across the prefix: got %+v", st.Entries)
	}
}

// Decision: paths are git's bytes, not split on spaces; a newline and Unicode survive.
func TestAwkwardPaths(t *testing.T) {
	t.Parallel()
	st := parseStatus(rec(
		"1 .M N... 100644 100644 100644 aa aa with space.txt",
		"1 .M N... 100644 100644 100644 aa aa line\nbreak.txt",
		"? ünï☃.txt",
		"2 R. N... 100644 100644 100644 aa aa R100 new name.txt", "old\nname.txt",
	), "", 5000, false)
	got := marks(st)
	for p, m := range map[string]Mark{
		"with space.txt":  MarkModified,
		"line\nbreak.txt": MarkModified,
		"ünï☃.txt":        MarkUntracked,
		"new name.txt":    MarkRenamed,
	} {
		if got[p] != m {
			t.Errorf("%q: got %q, want %q", p, got[p], m)
		}
	}
	for _, e := range st.Entries {
		if e.Path == "new name.txt" && e.OrigPath != "old\nname.txt" {
			t.Errorf("origPath %q", e.OrigPath)
		}
	}
}

// Decision: at most the limit, sorted first so the cut is deterministic; and output cut
// at the byte cap is truncated without trusting its last record.
func TestStatusCaps(t *testing.T) {
	t.Parallel()
	var raw []byte
	for _, p := range []string{"c", "a", "b"} {
		raw = append(raw, rec("? "+p)...)
	}
	st := parseStatus(raw, "", 2, false)
	if !st.Truncated || len(st.Entries) != 2 || st.Entries[0].Path != "a" || st.Entries[1].Path != "b" {
		t.Errorf("limit: %+v", st)
	}
	st = parseStatus([]byte("? a\x00? b\x00? half-writ"), "", 10, true)
	if !st.Truncated || len(st.Entries) != 2 {
		t.Errorf("cut output: %+v", st)
	}
}

func (e *env) statusOf(dir string) (Status, error) {
	e.t.Helper()
	return e.r.status(context.Background(), dir, true)
}

// Decision: the whole path, through real git: the runner, rev-parse for the prefix and
// the parser.
func TestStatusOfARealRepository(t *testing.T) {
	t.Parallel()
	t.Run("unborn", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.write(d, "b.txt", "b\n")
		e.g(d, "add", "a.txt")
		st, err := e.statusOf(d)
		if err != nil {
			t.Fatal(err)
		}
		if st.Head.Oid != "" || st.Head.Branch != "main" {
			t.Errorf("head %+v", st.Head)
		}
		if want := (map[string]Mark{"a.txt": MarkAdded, "b.txt": MarkUntracked}); !reflect.DeepEqual(marks(st), want) {
			t.Errorf("got %+v", st.Entries)
		}
	})
	t.Run("rename and delete", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		e.write(d, "old.txt", "some content\nthat is long enough\nto be a rename\n")
		e.write(d, "gone.txt", "bye\n")
		e.commit(d)
		e.g(d, "mv", "old.txt", "new.txt")
		if err := os.Remove(filepath.Join(d, "gone.txt")); err != nil {
			t.Fatal(err)
		}
		st, err := e.statusOf(d)
		if err != nil {
			t.Fatal(err)
		}
		want := []Entry{
			{Path: "gone.txt", Mark: MarkDeleted},
			{Path: "new.txt", Mark: MarkRenamed, OrigPath: "old.txt"},
		}
		if !reflect.DeepEqual(st.Entries, want) || st.Head.Oid == "" {
			t.Errorf("got %+v", st)
		}
	})
	t.Run("linked worktree", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.commit(d)
		wt := filepath.Join(t.TempDir(), "wt")
		e.g(d, "worktree", "add", "-q", "-b", "side", wt)
		if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
			t.Fatalf(".git of a linked worktree must be a file: %v", err)
		}
		e.write(wt, "a.txt", "changed\n")
		st, err := e.statusOf(wt)
		if err != nil {
			t.Fatal(err)
		}
		if st.Head.Branch != "side" || !reflect.DeepEqual(st.Entries, []Entry{{Path: "a.txt", Mark: MarkModified}}) {
			t.Errorf("got %+v", st)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		_, err := e.statusOf(t.TempDir())
		if !errors.Is(err, ErrNotARepository) {
			t.Errorf("got %v, want ErrNotARepository", err)
		}
	})
	t.Run("nested repository", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		e.write(d, "a.txt", "a\n")
		e.commit(d)
		n := filepath.Join(d, "nested")
		if err := os.MkdirAll(n, 0o755); err != nil {
			t.Fatal(err)
		}
		e.g(n, "init", "-q", "-b", "main")
		e.write(n, "x.txt", "x\n")
		e.commit(n)
		st, err := e.statusOf(d)
		if err != nil {
			t.Fatal(err)
		}
		if want := []Entry{{Path: "nested", Mark: MarkUntracked}}; !reflect.DeepEqual(st.Entries, want) {
			t.Errorf("got %+v", st.Entries)
		}
	})
	t.Run("a workspace in a subdirectory", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		e.write(d, "top.txt", "t\n")
		e.write(d, "app/in.txt", "i\n")
		e.commit(d)
		e.write(d, "top.txt", "changed\n")
		e.write(d, "app/in.txt", "changed\n")
		st, err := e.statusOf(filepath.Join(d, "app"))
		if err != nil {
			t.Fatal(err)
		}
		if want := []Entry{{Path: "in.txt", Mark: MarkModified}}; !reflect.DeepEqual(st.Entries, want) {
			t.Errorf("got %+v", st.Entries)
		}
	})
	t.Run("5001 untracked files", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		d := e.repo()
		for i := 0; i < 5001; i++ {
			e.write(d, fmt.Sprintf("d%02d/f%04d.txt", i%50, i), "x\n")
		}
		st, err := e.statusOf(d)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Truncated || len(st.Entries) != 5000 {
			t.Errorf("truncated %v, %d entries", st.Truncated, len(st.Entries))
		}
	})
}

// A record whose XY is not two characters is skipped, never a panic.
func TestMalformedRecordIsSkipped(t *testing.T) {
	t.Parallel()
	raw := "1 M N... 100644 100644 100644 abc abc a.txt\x002 R N... 100644 100644 100644 abc abc R100 b.txt\x00a.txt\x00? c.txt\x00"
	st := parseStatus([]byte(raw), "", maxEntries, false)
	if len(st.Entries) != 1 || st.Entries[0].Path != "c.txt" {
		t.Fatalf("entries = %+v, want only c.txt", st.Entries)
	}
}

// Decision (ADR-0066 §4): the watch's status refuses a root inside the git directory
// as the read does, rather than reporting the whole repository from its top.
func TestStatusFromInsideTheGitDirectoryIsNotARepository(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "top.txt", "top\n")
	e.commit(dir)
	_, err := e.r.status(context.Background(), filepath.Join(dir, ".git"), true)
	if !errors.Is(err, ErrNotARepository) {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

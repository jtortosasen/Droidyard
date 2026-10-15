package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// showPage calls git.show as the UI would.
func (f *readFx) showPage(oid string) (ShowResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.show")
	if !ok {
		f.t.Fatal("git.show is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: ShowParams{Oid: oid}})
	if err != nil {
		return ShowResult{}, capability.AsError(err)
	}
	return res.(ShowResult), nil
}

// mustShow is showPage that fails the test on an error.
func (f *readFx) mustShow(oid string) ShowResult {
	f.t.Helper()
	res, err := f.showPage(oid)
	if err != nil {
		f.t.Fatalf("git.show %s: %s (%s; cause %q)", oid, err.Code, err.Message, err.Cause)
	}
	return res
}

// pathsOf is the paths of a commit's files, in order.
func showPathsOf(files []ShowFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// numstatOf is plain git's `diff --numstat` between two trees, by path: "added removed",
// or "- -" for a binary file. Renames are not detected, so a rename is a delete and an add.
func (e *env) numstatOf(dir, from, to string) map[string]string {
	e.t.Helper()
	out := map[string]string{}
	for _, l := range lines(e.g(dir, "-c", "core.quotepath=off", "diff", "--numstat", "--no-renames", from, to)) {
		f := strings.SplitN(l, "\t", 3)
		out[f[2]] = f[0] + " " + f[1]
	}
	return out
}

// countsText is a file's counts as numstatOf writes them.
func countsText(f ShowFile) string {
	if f.Binary {
		return "- -"
	}
	if f.Added == nil || f.Removed == nil {
		return "none"
	}
	return fmt.Sprintf("%d %d", *f.Added, *f.Removed)
}

// emptyTree is the oid of the empty tree, a root commit's side of the diff.
func (e *env) emptyTree(dir string) string {
	e.t.Helper()
	return strings.TrimSpace(e.g(dir, "hash-object", "-t", "tree", "/dev/null"))
}

// Decision (ADR-0069 §5): a merge commit's files are what it brought into its first
// parent's branch, `git diff <merge>^1 <merge>`. The file the second parent brought
// (src/login.txt, identical in the merged branch's tip) is listed; a combined diff, which
// lists only the files that differ from every parent, would leave it out, and src/app.txt,
// which differs from the second parent alone, is not there. Seen failing with showArgs'
// two-tree form replaced by `diff-tree -c <merge>` (the combined diff), see the plan.
func TestShowMergeAgainstFirstParent(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)

	res := f.mustShow(r.Oids["merge"])
	if len(res.Parents) != 2 || res.Message != "Merge feature/login" {
		t.Fatalf("parents %v, message %q; want a two-parent merge", res.Parents, res.Message)
	}
	want := f.numstatOf(r.Dir, r.Oids["merge"]+"^1", r.Oids["merge"])
	if len(want) != 2 || want["src/login.txt"] == "" || want["merged.txt"] == "" {
		t.Fatalf("fixture: git diffs the merge against its first parent as %v, want merged.txt and src/login.txt", want)
	}
	got := map[string]string{}
	for _, file := range res.Files {
		got[file.Path] = countsText(file)
		if file.Status != "added" {
			t.Errorf("%s is %s; the merge added it to the first parent's branch", file.Path, file.Status)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("files %v; want what git diff <merge>^1 <merge> shows, %v", got, want)
	}
	// The second parent's own diff is a different list: src/app.txt (control that the two
	// parents disagree, so the choice of parent shows).
	second := f.numstatOf(r.Dir, r.Oids["merge"]+"^2", r.Oids["merge"])
	if _, ok := second["src/app.txt"]; !ok {
		t.Fatalf("control: the diff against the second parent is %v, want src/app.txt in it", second)
	}
	if _, ok := got["src/app.txt"]; ok {
		t.Errorf("src/app.txt is listed: it differs from the second parent only")
	}

	// The combined diff, which git shows for a merge by default, lists only the files that
	// differ from every parent: the second parent's src/login.txt is not among them.
	combined := lines(f.g(r.Dir, "diff-tree", "-r", "--cc", "--no-commit-id", "--name-only", r.Oids["merge"]))
	if !equal(combined, []string{"merged.txt"}) {
		t.Errorf("control: the combined diff lists %v, want merged.txt alone", combined)
	}

	// An octopus (four parents) is also against its first.
	oct := f.mustShow(r.Oids["octopus"])
	if len(oct.Parents) != 4 {
		t.Fatalf("octopus parents %v", oct.Parents)
	}
	wantOct := f.numstatOf(r.Dir, r.Oids["octopus"]+"^1", r.Oids["octopus"])
	gotOct := map[string]string{}
	for _, file := range oct.Files {
		gotOct[file.Path] = countsText(file)
	}
	if fmt.Sprint(gotOct) != fmt.Sprint(wantOct) {
		t.Errorf("octopus files %v; want %v", gotOct, wantOct)
	}
}

// Decision (ADR-0069 §5): a root commit is shown against the empty tree: every file is
// added, with git's counts, and the parents are an empty list, not null.
func TestShowRootCommit(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)

	res := f.mustShow(r.Oids["root"])
	if res.Parents == nil || len(res.Parents) != 0 {
		t.Errorf("parents = %#v; want an empty, non-nil list", res.Parents)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"parents":[]`) {
		t.Errorf("a root commit as JSON has no empty parents list: %s", b)
	}
	if !equal(showPathsOf(res.Files), []string{"README.md", "data/blob.bin", "docs/old-name.txt", "src/app.txt"}) {
		t.Errorf("files %v; want the four files of the first commit, by path", showPathsOf(res.Files))
	}
	want := f.numstatOf(r.Dir, f.env.emptyTree(r.Dir), r.Oids["root"])
	for _, file := range res.Files {
		if file.Status != "added" || countsText(file) != want[file.Path] {
			t.Errorf("%s = %s %s; git counts %s against the empty tree", file.Path, file.Status, countsText(file), want[file.Path])
		}
	}
	if res.Message != "Initial import" || res.Author.Name != "Ann Author" || res.Author.Email != "ann@example.com" {
		t.Errorf("details %q by %+v", res.Message, res.Author)
	}
}

// Decision (ADR-0069 §1, plan m2n step 7): the counts are git's, for every commit of a
// real history; a binary file has none and says binary, a text file with no removed
// line has removed 0 and still carries it (a pointer to zero, not an absent field).
func TestShowNumstat(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)

	checked := 0
	for _, oid := range strings.Fields(f.g(r.Dir, "rev-list", "--all")) {
		if oid == r.Oids["rename"] {
			continue // a rename: its own test
		}
		parent := strings.TrimSpace(f.g(r.Dir, "rev-list", "--parents", "-n1", oid))
		from := f.env.emptyTree(r.Dir)
		if fields := strings.Fields(parent); len(fields) > 1 {
			from = fields[1]
		}
		want := f.numstatOf(r.Dir, from, oid)
		got := map[string]string{}
		for _, file := range f.mustShow(oid).Files {
			got[file.Path] = countsText(file)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: files %v; want git diff --numstat's %v", oid[:7], got, want)
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d commits checked", checked)
	}

	// The binary file of the root commit: no counts at all.
	for _, file := range f.mustShow(r.Oids["root"]).Files {
		b, _ := json.Marshal(file)
		switch file.Path {
		case "data/blob.bin":
			if !file.Binary || file.Added != nil || file.Removed != nil || strings.Contains(string(b), `"added":`) || strings.Contains(string(b), `"removed":`) {
				t.Errorf("binary file = %s; want binary true and no counts", b)
			}
		default:
			if file.Binary || file.Added == nil || file.Removed == nil {
				t.Errorf("%s = %s; want counts and not binary", file.Path, b)
			}
		}
	}
	// A pure addition has removed 0, present in the JSON.
	for _, file := range f.mustShow(r.Oids["second"]).Files {
		if b, _ := json.Marshal(file); file.Path != "src/app.txt" || !strings.Contains(string(b), `"added":1,"removed":0`) {
			t.Errorf("second commit's file = %s; want src/app.txt with added 1, removed 0", b)
		}
	}
}

// Decision (ADR-0069 §1): every kind of change has a status, and a rename carries its
// origin: added, modified, deleted, renamed (a change of kind, a file made a link, is
// modified; a copy cannot arise since copies are not detected). Counts of a renamed file
// are of the lines changed besides the move.
func TestShowRename(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	dir := f.root
	f.write(dir, "old.txt", "a\nb\nc\nd\ne\nf\n")
	f.write(dir, "gone.txt", "bye\n")
	f.write(dir, "keep.txt", "hi\n")
	f.write(dir, "kind.txt", "was a file\n")
	f.write(dir, "moved/exact.txt", "exactly the same\nin two lines\n")
	f.commit(dir)
	f.g(dir, "mv", "old.txt", "new.txt")
	f.write(dir, "new.txt", "a\nb\nc\nd\ne\nf\ng\n")
	f.g(dir, "mv", "moved/exact.txt", "exact.txt")
	f.g(dir, "rm", "-q", "gone.txt")
	f.write(dir, "keep.txt", "hi\nmore\n")
	f.write(dir, "added.txt", "new\n")
	if err := os.Remove(filepath.Join(dir, "kind.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keep.txt", filepath.Join(dir, "kind.txt")); err != nil {
		t.Fatal(err)
	}
	f.commit(dir)
	oid := f.oidsOf(dir, "HEAD")[0]

	type row struct{ status, orig, counts string }
	got := map[string]row{}
	for _, file := range f.mustShow(oid).Files {
		got[file.Path] = row{file.Status, file.OrigPath, countsText(file)}
	}
	want := map[string]row{
		"new.txt":   {"renamed", "old.txt", "1 0"},
		"exact.txt": {"renamed", "moved/exact.txt", "0 0"},
		"gone.txt":  {"deleted", "", "0 1"},
		"keep.txt":  {"modified", "", "1 0"},
		"added.txt": {"added", "", "1 0"},
		"kind.txt":  {"modified", "", ""},
	}
	for path, w := range want {
		g, ok := got[path]
		if !ok {
			t.Errorf("%s is not listed (got %v)", path, got)
			continue
		}
		if w.counts == "" { // the type change: only its status is the decision
			w.counts = g.counts
		}
		if g != w {
			t.Errorf("%s = %+v; want %+v", path, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("files %v; want exactly %d", got, len(want))
	}
	// A renamed file's origin is in the JSON only for a rename.
	for _, file := range f.mustShow(oid).Files {
		b, _ := json.Marshal(file)
		if strings.Contains(string(b), "origPath") != (file.Status == "renamed") {
			t.Errorf("%s as JSON = %s", file.Path, b)
		}
	}
}

// Decision (ADR-0069 §2, plan m2n step 7): the files are the workspace's, named relative to
// it, whether it is the repository's top or a folder of it; a path the workspace's own
// folder name could match as a glob (a `*`) is matched literally, so a sibling is not
// listed. Git reads the working directory's prefix literally by itself (the test passes
// without --literal-pathspecs); the `we*ird` case guards a future change that puts the
// prefix on argv as a pathspec. A file moved into the workspace from outside it is added,
// with no origin outside the root.
func TestShowPrefix(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	for path, text := range map[string]string{
		"top.txt": "t\n", "app/B.kt": "b\n", "app/src/A.kt": "a\n", "other/C.kt": "c\nc\nc\n",
		"we*ird/d.txt": "d\n", "we*ird/a*b.txt": "ab\n", "weXird/e.txt": "e\n",
	} {
		f.write(repo, path, text)
	}
	f.commit(repo)
	first := f.oidsOf(repo, "HEAD")[0]
	f.g(repo, "mv", "other/C.kt", "app/C.kt")
	f.write(repo, "app/B.kt", "b\nmore\n")
	f.write(repo, "weXird/e.txt", "e\nmore\n")
	f.commit(repo)
	second := f.oidsOf(repo, "HEAD")[0]

	for _, c := range []struct {
		workspace, oid string
		want           []string
	}{
		{"", first, []string{"app/B.kt", "app/src/A.kt", "other/C.kt", "top.txt", "we*ird/a*b.txt", "we*ird/d.txt", "weXird/e.txt"}},
		{"app", first, []string{"B.kt", "src/A.kt"}},
		{"app/src", first, []string{"A.kt"}},
		{"we*ird", first, []string{"a*b.txt", "d.txt"}}, // not weXird/e.txt
		{"we*ird", second, nil},
		{"app", second, []string{"B.kt", "C.kt"}}, // C.kt moved in from outside: added
		{"other", second, []string{"C.kt"}},       // ...and deleted from where it was
	} {
		f.at(filepath.Join(repo, c.workspace))
		res := f.mustShow(c.oid)
		if !equal(showPathsOf(res.Files), c.want) {
			t.Errorf("workspace %q, commit %s: files %v; want %v", c.workspace, c.oid[:7], showPathsOf(res.Files), c.want)
		}
	}
	f.at(filepath.Join(repo, "app"))
	for _, file := range f.mustShow(second).Files {
		if file.Path == "C.kt" && (file.Status != "added" || file.OrigPath != "") {
			t.Errorf("C.kt = %+v; want added, with no origin outside the root", file)
		}
	}
}

// Decision (plan m2n step 7): paths come out of git NUL-separated and are taken as they
// are: a space, a tab, a line break, a leading colon (the first byte of a raw record) or
// dash, a glob character and non-ASCII text are the file's name, and a text file's counts
// stay with it.
func TestShowAwkwardPaths(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	names := []string{"sp ace.txt", "tab\there.txt", "new\nline.txt", ":colon.txt", "-dash.txt", "a*b.txt", "é.txt", "q\"uote.txt"}
	for i, n := range names {
		f.write(f.root, n, strings.Repeat("l\n", i+1))
	}
	f.commit(f.root)
	res := f.mustShow(f.oidsOf(f.root, "HEAD")[0])
	got := map[string]string{}
	for _, file := range res.Files {
		got[file.Path] = countsText(file)
	}
	for i, n := range names {
		if got[n] != fmt.Sprintf("%d 0", i+1) {
			t.Errorf("%q = %q; want %d 0", n, got[n], i+1)
		}
	}
	if len(got) != len(names) {
		t.Errorf("files %q; want exactly the %d names", got, len(names))
	}
}

// commitMessage commits whatever is staged with exactly this message (no clean-up) and
// returns the new commit's oid; an empty commit is allowed.
func (e *env) commitMessage(dir, message string) string {
	e.t.Helper()
	file := filepath.Join(e.t.TempDir(), "message")
	if err := os.WriteFile(file, []byte(message), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.g(dir, "add", "-A")
	e.g(dir, "commit", "-q", "--allow-empty", "--cleanup=verbatim", "-F", file)
	return strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
}

// fastImportFiles writes a root commit of n files, each "x\n", at paths pad+number
// (pad long to make the commit's listing large), on a branch of its own, and returns the
// commit's oid. A tree built outside the file system takes any path length.
func (e *env) fastImportFiles(dir string, n int, pad string) string {
	e.t.Helper()
	var b strings.Builder
	b.WriteString("commit refs/heads/big\ncommitter T <t@example.com> 1700000000 +0000\ndata 4\nbig\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "M 100644 inline %s%05d\ndata 2\nx\n\n", pad, i)
	}
	e.gStdin(dir, b.String(), "fast-import", "--quiet")
	return strings.TrimSpace(e.g(dir, "rev-parse", "refs/heads/big"))
}

// Decision (plan m2n step 7, ADR-0069 §4): the message is at most 64 KiB and
// messageTruncated says when it is longer, cut at a character, never in the middle of one;
// exactly 64 KiB is whole. The files are at most 5,000, in path order, and truncated says
// when there were more; 5,000 exactly are whole. When git's output passes the runner's
// 4 MiB cap, the commit is truncated too: its files are listed as far as they were read
// (in the raw phase none has counts, in the counts phase the last ones have none).
func TestShowCaps(t *testing.T) {
	t.Parallel()
	t.Run("message", func(t *testing.T) {
		t.Parallel()
		f := newReadFx(t)
		for _, c := range []struct {
			name, message string
			wantLen       int
			truncated     bool
		}{
			// The other boundaries (one byte more, a short message, an empty one, a four-byte
			// character) are TestShowCapsParsed's, on synthetic output; git hands over the
			// whole message at the cap and one cut mid-character.
			{"exactly 64 KiB", strings.Repeat("x", maxMessage), maxMessage, false},
			{"a character on the limit", "a" + strings.Repeat("é", 40000), maxMessage - 1, true},
		} {
			oid := f.commitMessage(f.root, c.message)
			res := f.mustShow(oid)
			if len(res.Message) != c.wantLen || res.MessageTruncated != c.truncated || !utf8.ValidString(res.Message) || !strings.HasPrefix(c.message, res.Message) {
				t.Errorf("%s: %d bytes, truncated %v, valid %v; want %d bytes, truncated %v, a prefix of the message",
					c.name, len(res.Message), res.MessageTruncated, utf8.ValidString(res.Message), c.wantLen, c.truncated)
			}
		}
	})

	t.Run("files", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			n         int
			truncated bool
		}{{maxShowFiles, false}, {maxShowFiles + 1, true}} {
			f := newReadFx(t)
			oid := f.env.fastImportFiles(f.root, c.n, "f")
			res := f.mustShow(oid)
			if len(res.Files) != maxShowFiles || res.Truncated != c.truncated {
				t.Errorf("%d files: %d listed, truncated %v; want %d, %v", c.n, len(res.Files), res.Truncated, maxShowFiles, c.truncated)
			}
			if res.Files[0].Path != "f00000" || res.Files[maxShowFiles-1].Path != fmt.Sprintf("f%05d", maxShowFiles-1) {
				t.Errorf("first %q, last %q; want the first 5,000 by path", res.Files[0].Path, res.Files[maxShowFiles-1].Path)
			}
		}
	})

	// 4 MiB of git's output, with fewer than 5,000 files: paths of 1,000 bytes put the cap
	// in the counts, paths of 2,100 bytes in the raw records.
	for _, c := range []struct {
		name         string
		n, padLen    int
		wantCounts   bool
		wantUncounts bool
	}{
		{"the cap in the counts", 2400, 1000, true, true},
		{"the cap in the raw records", 2200, 2100, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newReadFx(t)
			oid := f.env.fastImportFiles(f.root, c.n, strings.Repeat("p", c.padLen))
			res := f.mustShow(oid)
			if !res.Truncated || len(res.Files) == 0 || len(res.Files) > c.n {
				t.Fatalf("%d files: %d listed, truncated %v; want some, and truncated", c.n, len(res.Files), res.Truncated)
			}
			counted := 0
			for i, file := range res.Files {
				if file.Path != strings.Repeat("p", c.padLen)+fmt.Sprintf("%05d", i) || file.Status != "added" {
					t.Fatalf("file %d = %+v; want the %dth path in order, added", i, file, i)
				}
				if file.Added != nil {
					counted++
					if countsText(file) != "1 0" || counted != i+1 {
						t.Fatalf("file %d counts %s after %d counted; want 1 0 and the counted files first", i, countsText(file), counted)
					}
				}
			}
			if (counted > 0) != c.wantCounts || (counted < len(res.Files)) != c.wantUncounts {
				t.Errorf("%d of %d listed files have counts; want some: %v, uncounted: %v", counted, len(res.Files), c.wantCounts, c.wantUncounts)
			}
		})
	}
}

// Decision (ADR-0069 §4, plan m2n step 7): an oid is a full lower-case hex id of a commit
// or REQUEST_INVALID_PARAMS: a ref name, HEAD, a short id, a revision, an option, a line
// break and an empty string never start a git; an id the repository does not hold, a
// tree, a blob and an annotated tag (not a commit) are the same code after the check.
func TestShowUnknownCommit(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)
	good := r.Oids["head"]
	for name, oid := range map[string]string{
		"a ref name": "main", "HEAD": "HEAD", "a short id": good[:7], "an id one short": good[:39],
		"a revision": good + "^", "an option": "--output=/tmp/x", "upper case": strings.ToUpper(good),
		"a line break": good + "\n" + good, "a trailing newline": good + "\n", "empty": "",
		"41 digits": good + "0", "a path": good + ":README.md",
	} {
		_, err := f.showPage(oid)
		if err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS with a next step", name, err)
		}
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("%d gits started for oids that were refused; want 0", n)
	}
	if _, err := f.showPage(good); err != nil || f.starts.Load() == 0 {
		t.Errorf("control: the head commit gave %v after %d starts; the zero above proves nothing", err, f.starts.Load())
	}

	tag := f.oidsOf(r.Dir, "refs/tags/v1.0")[0]
	if kind := strings.TrimSpace(f.g(r.Dir, "cat-file", "-t", tag)); kind != "tag" {
		t.Fatalf("fixture: v1.0 is a %s, want a tag object", kind)
	}
	blob := f.oidsOf(r.Dir, "HEAD:README.md")[0]
	for name, oid := range map[string]string{
		"an unknown id": strings.Repeat("1", 40), "a 64-digit id in a 40-digit repository": strings.Repeat("a", 64),
		"a tree": r.Oids["tree-tag"], "a blob": blob, "an annotated tag": tag,
	} {
		if _, err := f.showPage(oid); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS", name, err)
		}
	}

	// No commit at all: any full id is unknown.
	f.at(f.env.repo())
	_, err := f.showPage(good)
	wantCode(t, err, capability.ErrInvalidParams)
	// A folder in no repository.
	f.at(t.TempDir())
	_, err = f.showPage(good)
	wantCode(t, err, CodeNotARepository)
}

// Decision (plan m2n step 7, Q1 §1): times are RFC 3339 in the offset the commit
// recorded, for the author and the committer apart; names and e-mails are the raw ones;
// the message is the whole text, line breaks and non-ASCII included.
func TestShowDetails(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "one\n")
	f.g(f.root, "add", "-A")
	msg := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(msg, []byte("Subject line é\n\nBody line 1\r\nBody line 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "commit", "-q", "--cleanup=verbatim", "-F", msg)
	cmd.Dir = f.root
	cmd.Env = append(slices.Clone(f.env.env),
		"GIT_AUTHOR_NAME=Zoë Ünïcode", "GIT_AUTHOR_EMAIL=zoe@example.com", "GIT_AUTHOR_DATE=2026-03-04T05:06:07+0530",
		"GIT_COMMITTER_NAME=Cy Committer", "GIT_COMMITTER_EMAIL=cy@example.com", "GIT_COMMITTER_DATE=2026-03-04T05:06:07-0830")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	res := f.mustShow(f.oidsOf(f.root, "HEAD")[0])

	if res.Author != (ShowPerson{"Zoë Ünïcode", "zoe@example.com", "2026-03-04T05:06:07+05:30"}) {
		t.Errorf("author = %+v", res.Author)
	}
	if res.Committer != (ShowPerson{"Cy Committer", "cy@example.com", "2026-03-04T05:06:07-08:30"}) {
		t.Errorf("committer = %+v", res.Committer)
	}
	for _, p := range []ShowPerson{res.Author, res.Committer} {
		if _, err := time.Parse(time.RFC3339, p.Time); err != nil {
			t.Errorf("%q is not RFC 3339: %v", p.Time, err)
		}
	}
	if res.Message != "Subject line é\n\nBody line 1\r\nBody line 2" {
		t.Errorf("message %q; want the whole text, line breaks and non-ASCII included", res.Message)
	}
}

// Decision (ADR-0069 §3, ADR-0066 §3, §6 of the same): through the capability, on an
// untrusted root, what the repository configures to make git run something, or to show
// another history under an oid, does not happen: an external diff and a textconv driver
// are not run (controls: plain git runs both), a replace ref leaves the author, message,
// parents and files as the commit holds them, and a graft leaves its parents. Controls
// prove each forgery works against plain git.
func TestShowFence(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, attrs string) (*readFx, string) {
		f := newReadFx(t)
		dir, head := f.env.historyRepo(attrs)
		f.at(dir)
		f.trusted = func(string) (bool, *capability.Error) { return false, nil }
		return f, head
	}
	wantReal := func(t *testing.T, f *readFx, head string) {
		t.Helper()
		res := f.mustShow(head)
		if res.Author.Name != "T" || res.Message != "c\n" || len(res.Parents) != 1 || !equal(showPathsOf(res.Files), []string{"a.txt", "b.txt"}) {
			t.Errorf("show = author %q, message %q, parents %v, files %v; want the commit's own", res.Author.Name, res.Message, res.Parents, showPathsOf(res.Files))
		}
		if len(res.Files) == 0 || countsText(res.Files[0]) != "1 0" {
			t.Errorf("show's counts %v; want a.txt's real one added line", res.Files)
		}
	}

	t.Run("programs the repository configures", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t, "*.txt diff=custom\n")
		e := f.env
		ext, conv := filepath.Join(t.TempDir(), "ext"), filepath.Join(t.TempDir(), "conv")
		e.appendConfig(f.root, "[diff]\n\texternal = "+e.script("ext.sh", "touch '"+ext+"'")+"\n"+
			"[diff \"custom\"]\n\ttextconv = "+e.script("conv.sh", "touch '"+conv+"'\ncat \"$1\"")+"\n")
		e.g(f.root, "diff", "HEAD~1", "HEAD")
		e.g(f.root, "diff", "--no-ext-diff", "HEAD~1", "HEAD")
		for name, mark := range map[string]string{"diff.external": ext, "textconv": conv} {
			if !exists(mark) {
				t.Fatalf("control: plain git did not run %s; the check below proves nothing", name)
			}
			os.Remove(mark)
		}
		wantReal(t, f, head)
		for name, mark := range map[string]string{"diff.external": ext, "textconv": conv} {
			if exists(mark) {
				t.Errorf("git.show ran the repository's %s", name)
			}
		}
	})

	t.Run("a replace ref giving the commit another author, message and files", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t, "")
		e := f.env
		e.g(f.root, "update-ref", "refs/replace/"+head, e.forgedCommit(f.root, ""))
		if got := e.plainRun(f.root, nil, "log", "-1", "--format=%an %s", head); !strings.Contains(got, "FORGED") {
			t.Fatalf("control: plain git shows %q, want the forgery", got)
		}
		wantReal(t, f, head)
	})

	t.Run("a graft giving the commit no parent", func(t *testing.T) {
		t.Parallel()
		f, head := setup(t, "")
		f.write(f.root, ".git/info/grafts", head+"\n")
		if got := strings.TrimSpace(f.env.plainRun(f.root, nil, "-c", "advice.graftFileDeprecated=false", "log", "-1", "--format=%P", head)); got != "" {
			t.Fatalf("control: plain git shows parents %q, want none (the graft cuts the history)", got)
		}
		wantReal(t, f, head)
	})
}

// Decision (ADR-0069 §1, ADR-0020 §Retention): git.show is a stateless workspace query for
// the human UI (audience human: the core refuses it over MCP), that needs git, in the
// release git.log introduced, and whose codes are listed. Reversing the audience would list
// it as an agent tool.
func TestShowDescriptor(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, ok := f.reg.Lookup("git.show")
	if !ok {
		t.Fatal("git.show is not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Workspace || d.Statefulness != capability.Stateless || d.Audience != capability.AudienceHuman || d.Since != "0.24.0" {
		t.Errorf("kind %v scope %v statefulness %v audience %v since %s", d.Kind, d.Scope, d.Statefulness, d.Audience, d.Since)
	}
	if len(d.Availability) != 1 || d.Availability[0] != capability.RequiresGit {
		t.Errorf("availability %v; want git", d.Availability)
	}
	for _, code := range []capability.ErrorCode{CodeNotFound, CodeNotARepository, CodeFailed, capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams} {
		if !slices.Contains(d.Errors, code) {
			t.Errorf("the descriptor does not list %s, which the handler returns", code)
		}
	}
	if len(d.Errors) != 5 {
		t.Errorf("errors %v; want the five the handler returns", d.Errors)
	}
}

// Decision (ADR-0020 §Retention): a result returned again after delivery holds neither the
// message nor the files, so retention never keeps the repository's text; the commit's
// identity, its people and the flags stay, and the files as [] not null.
func TestShowSlim(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, _ := f.reg.Lookup("git.show")
	one := 1
	full := ShowResult{
		Oid: "a", Parents: []string{"b"},
		Author:           ShowPerson{"A", "a@x", "2026-01-01T00:00:00Z"},
		Committer:        ShowPerson{"C", "c@x", "2026-01-01T00:00:00Z"},
		Message:          "secret message",
		MessageTruncated: true,
		Files:            []ShowFile{{Path: "p", Status: "added", Added: &one, Removed: &one}},
		Truncated:        true,
	}
	got, ok := d.Slim(full).(ShowResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if got.Message != "" || len(got.Files) != 0 {
		t.Errorf("slim keeps the bulk: %+v", got)
	}
	if got.Oid != "a" || !equal(got.Parents, []string{"b"}) || got.Author != full.Author || got.Committer != full.Committer || !got.MessageTruncated || !got.Truncated {
		t.Errorf("slim = %+v; want the identity, the people and the flags kept", got)
	}
	if b, _ := json.Marshal(got); !strings.Contains(string(b), `"files":[]`) {
		t.Errorf("slim as JSON = %s; want files as [] not null", b)
	}
	if full.Message == "" || len(full.Files) != 1 {
		t.Errorf("Slim changed the result it was given")
	}
}

// Decision (plan m2n step 7): the parser reads what git writes and nothing else: a record
// of the wrong shape is GIT_FAILED, not a half-read list; a time git prints in no RFC 3339
// form becomes the epoch and does not refuse the commit. Over git's real output the other
// tests prove the parser; these are the shapes only a broken git would produce.
func TestParseShowFilesRefusesWhatGitDoesNotWrite(t *testing.T) {
	t.Parallel()
	const modes = ":100644 100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb "
	for name, out := range map[string]string{
		"a raw record of four fields":  ":100644 100644 aaaa M\x00a.txt\x00",
		"a raw record cut short":       modes + "R100\x00old.txt\x00",
		"a file with no count":         modes + "M\x00a.txt\x00",
		"a count that is not a number": modes + "M\x00a.txt\x00x\ty\ta.txt\x00",
		"a count record of two parts":  modes + "M\x00a.txt\x001\ta.txt\x00",
	} {
		if _, _, err := parseShowFiles([]byte(out), false, ""); err == nil || err.Code != CodeFailed {
			t.Errorf("%s: %v; want GIT_FAILED", name, err)
		}
	}
	files, truncated, err := parseShowFiles([]byte(modes+"M\x00a.txt\x001\t2\ta.txt\x00"), false, "")
	if err != nil || truncated || len(files) != 1 || countsText(files[0]) != "1 2" {
		t.Errorf("control: a whole record gave %+v, %v, %v", files, truncated, err)
	}
	res, perr := parseShow(strings.Repeat("a", 40), result{Stdout: []byte(strings.Repeat("a", 40) + "\x00\x00A\x00a@x\x00yesterday\x00C\x00c@x\x002026-01-01T00:00:00Z\x00msg\x00")})
	if perr != nil || res.Author.Time != epoch || res.Committer.Time != "2026-01-01T00:00:00Z" {
		t.Errorf("parseShow = %+v, %v; want the epoch for a time that is not RFC 3339", res, perr)
	}
}

// Decision (plan m2n step 7): every file the commit changed is listed once with the counts
// git gave it, for a deep tree too: the order is git's (by path), the workspace's prefix is
// removed from every path, and the number of numstat records equals the raw ones (a
// mismatch is GIT_FAILED, above), checked here against plain git's own --name-only.
func TestShowListsWhatGitLists(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)
	for _, oid := range strings.Fields(f.g(r.Dir, "rev-list", "--all")) {
		if oid == r.Oids["rename"] {
			continue
		}
		parent := f.env.emptyTree(r.Dir)
		if fields := strings.Fields(f.g(r.Dir, "rev-list", "--parents", "-n1", oid)); len(fields) > 1 {
			parent = fields[1]
		}
		want := lines(f.g(r.Dir, "diff", "--name-only", "--no-renames", parent, oid))
		if got := showPathsOf(f.mustShow(oid).Files); !equal(got, want) {
			t.Errorf("%s: files %v; want git's %v", oid[:7], got, want)
		}
	}
}

// showBase calls git.show with a base.
func (f *readFx) showBase(oid, base string) (ShowResult, *capability.Error) {
	f.t.Helper()
	d, _ := f.reg.Lookup("git.show")
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: ShowParams{Oid: oid, Base: base}})
	if err != nil {
		return ShowResult{}, capability.AsError(err)
	}
	return res.(ShowResult), nil
}

// Decision (ADR-0072 §2): with a base, the files are those between base and oid, as
// `diff --numstat base oid` lists them (not the first parent's), and the commit's details are
// unchanged. Absent, the first parent as before. A base that is no commit of the repository
// (a tree, a blob, an unknown id) or no full id is REQUEST_INVALID_PARAMS.
func TestShowBase(t *testing.T) {
	t.Parallel()
	f, r := newHistoryFx(t)
	a, b := r.Oids["rename"], r.Oids["merge"]
	want := f.numstatOf(r.Dir, a, b)
	against := f.numstatOf(r.Dir, b+"^1", b)
	if len(want) == 0 || fmt.Sprint(want) == fmt.Sprint(against) {
		t.Fatalf("fixture: base and first parent give the same files %v", want)
	}
	res, err := f.showBase(b, a)
	if err != nil {
		t.Fatalf("git.show with a base: %s %s", err.Code, err.Message)
	}
	got := map[string]string{}
	for _, file := range res.Files {
		got[file.Path] = countsText(file)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("files %v; want diff --numstat base oid, %v", got, want)
	}
	plain := f.mustShow(b)
	if res.Oid != plain.Oid || res.Message != plain.Message || fmt.Sprint(res.Parents) != fmt.Sprint(plain.Parents) {
		t.Errorf("details changed with a base: %+v vs %+v", res, plain)
	}
	gotPlain := map[string]string{}
	for _, file := range plain.Files {
		gotPlain[file.Path] = countsText(file)
	}
	if fmt.Sprint(gotPlain) != fmt.Sprint(against) {
		t.Errorf("without a base %v; want the first parent's %v", gotPlain, against)
	}

	n := f.starts.Load()
	for name, base := range map[string]string{"a ref name": "main", "a short id": a[:7], "a revision": a + "^", "an option": "--output=/tmp/x"} {
		if _, err := f.showBase(b, base); err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS", name, err)
		}
	}
	if f.starts.Load() != n {
		t.Errorf("%d gits started for a malformed base", f.starts.Load()-n)
	}
	blob := f.oidsOf(r.Dir, "HEAD:README.md")[0]
	for name, base := range map[string]string{"a tree": r.Oids["tree-tag"], "a blob": blob, "an unknown id": strings.Repeat("1", 40)} {
		if _, err := f.showBase(b, base); err == nil || err.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS (not a commit)", name, err)
		}
	}
}

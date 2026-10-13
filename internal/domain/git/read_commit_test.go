package git

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (ADR-0066 §1 as ADR-0069 §1 widens it): with a commit's full object id as the
// base, git.read returns the bytes that commit holds at the path, not HEAD's and not the
// working copy's; the result's base is that id; a path the commit does not hold is exists
// false (a file added later, a file already deleted), and a binary file and a directory
// are what they are at HEAD. A workspace below the repository's top adds its prefix to the
// object name as before.
func TestReadAtCommit(t *testing.T) {
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "a.txt", "v1\n")
	f.write(repo, "sub/b.txt", "b1\n")
	f.write(repo, "sub/keep.txt", "k\n")
	f.write(repo, "bin.dat", "x\x00y")
	f.commit(repo)
	older := f.oidsOf(repo, "HEAD")[0]
	f.write(repo, "a.txt", "v2\n")
	f.write(repo, "new.txt", "new\n")
	f.g(repo, "rm", "-q", "sub/b.txt")
	f.commit(repo)
	newer := f.oidsOf(repo, "HEAD")[0]
	f.write(repo, "a.txt", "v3, not committed\n")

	for _, c := range []struct {
		path, base string
		exists     bool
		text       string
	}{
		{"a.txt", older, true, "v1\n"},
		{"a.txt", newer, true, "v2\n"},
		{"a.txt", "HEAD", true, "v2\n"},
		{"a.txt", "", true, "v2\n"},
		{"new.txt", older, false, ""},
		{"new.txt", newer, true, "new\n"},
		{"sub/b.txt", older, true, "b1\n"},
		{"sub/b.txt", newer, false, ""},
	} {
		res, err := f.read(ReadParams{Path: c.path, Base: c.base})
		if err != nil {
			t.Errorf("%s at %q: %v", c.path, c.base, err)
			continue
		}
		wantBase := c.base
		if wantBase == "" {
			wantBase = "HEAD"
		}
		if res.Base != wantBase || res.Exists != c.exists || text(res) != map[bool]string{true: c.text, false: "<nil>"}[c.exists] {
			t.Errorf("%s at %q = base %q exists %v text %q; want base %q exists %v text %q", c.path, c.base, res.Base, res.Exists, text(res), wantBase, c.exists, c.text)
		}
	}

	if res, err := f.read(ReadParams{Path: "bin.dat", Base: older}); err != nil || !res.Exists || !res.Binary || res.Text != nil || res.Size != 3 {
		t.Errorf("a binary file at a commit = %+v, %v; want binary, size 3, no text", res, err)
	}
	_, err := f.read(ReadParams{Path: "sub", Base: older})
	wantCode(t, err, capability.ErrFileWrongType)
	if !strings.Contains(err.Message, "the commit") || strings.Contains(err.Message, "HEAD") {
		t.Errorf("wrong type at a commit says %q; want it to name the commit, not HEAD", err.Message)
	}

	// The prefix of a workspace below the top is added to the object name.
	f.at(repo + "/sub")
	if res, err := f.read(ReadParams{Path: "b.txt", Base: older}); err != nil || text(res) != "b1\n" || res.Path != "b.txt" {
		t.Errorf("from the subfolder workspace: %+v, %v; want the older commit's b1", res, err)
	}
	if res, err := f.read(ReadParams{Path: "b.txt", Base: newer}); err != nil || res.Exists {
		t.Errorf("from the subfolder workspace at the newer commit: %+v, %v; want exists false", res, err)
	}
}

// Decision (ADR-0069 §1, plan m2n step 7): an oid that is not a commit would, as an object
// name, read a path inside a tree, or nothing: a tree's id, a blob's, an annotated tag's
// and an id the repository does not hold are REQUEST_INVALID_PARAMS.
func TestReadBaseNotACommit(t *testing.T) {
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "a.txt", "v1\n")
	f.commit(repo)
	f.g(repo, "tag", "-a", "-m", "tag", "v1")
	for name, oid := range map[string]string{
		"a tree": f.oidsOf(repo, "HEAD^{tree}")[0], "a blob": f.oidsOf(repo, "HEAD:a.txt")[0],
		"an annotated tag": f.oidsOf(repo, "refs/tags/v1")[0], "an unknown id": strings.Repeat("1", 40),
		"a 64-digit id": strings.Repeat("a", 64),
	} {
		res, err := f.read(ReadParams{Path: "a.txt", Base: oid})
		if err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("%s: %+v, %v; want REQUEST_INVALID_PARAMS with a next step", name, res, err)
		}
	}
	// The tree's id as a base would, as an object name, read the file inside the tree.
	tree := f.oidsOf(repo, "HEAD^{tree}")[0]
	if got := strings.TrimSpace(f.g(repo, "cat-file", "-p", tree+":a.txt")); got != "v1" {
		t.Fatalf("control: git reads %q at %s:a.txt, want v1 (so the commit check is what refuses)", got, tree)
	}
}

// Decision (ADR-0069 §1; ADR-0066 §3 the base never reaches argv): base is HEAD or a full
// lower-case hex commit id, validated before any git starts: a branch name, a short id, a
// revision, an option, another case and a path-bearing name are REQUEST_INVALID_PARAMS with
// zero gits started; the control shows the counter counts and a full id starts one.
func TestReadBaseRefused(t *testing.T) {
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	head := f.oidsOf(f.root, "HEAD")[0]
	for _, base := range []string{
		"main", "HEAD~1", "HEAD^", "head", "abc1234", head[:39], head + "0", strings.ToUpper(head),
		head + "^", head + ":a.txt", "HEAD:a.txt", "-x", "--output=/tmp/x", head + "\n", " " + head, "refs/heads/main",
	} {
		_, err := f.read(ReadParams{Path: "a.txt", Base: base})
		if err == nil || err.Code != capability.ErrInvalidParams || err.NextStep == "" {
			t.Errorf("base %q: %v; want REQUEST_INVALID_PARAMS with a next step", base, err)
		}
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("%d gits started for bases that were refused; want 0", n)
	}
	if res, err := f.read(ReadParams{Path: "a.txt", Base: head}); err != nil || text(res) != "a\n" || f.starts.Load() == 0 {
		t.Errorf("control: the head commit's id gave %+v, %v after %d starts; the zero above proves nothing", res, err, f.starts.Load())
	}
}

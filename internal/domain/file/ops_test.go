package file

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The tree's operations (plan m2g step 2; ADR-0053): file.create and file.move, on the
// real volume the tests run on. Each test says which decision it holds.

func (f *fixture) move(items []MoveItem, onCollision string) (MoveResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("file.move", MoveParams{Items: items, OnCollision: onCollision})
	if err != nil {
		return MoveResult{}, err
	}
	return res.(MoveResult), nil
}

func (f *fixture) create(path, typ string) (CreateResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("file.create", CreateParams{Path: path, Type: typ})
	if err != nil {
		return CreateResult{}, err
	}
	return res.(CreateResult), nil
}

func mv(from, to string) MoveItem { return MoveItem{From: from, To: to} }

// tree is every entry under dir, by slash-separated name, with a file's bytes, a link's
// target text or "dir": enough to say whether anything changed.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.IsDir():
			out[rel] = "dir"
		case d.Type()&fs.ModeSymlink != 0:
			tgt, _ := os.Readlink(p)
			out[rel] = "link:" + tgt
		default:
			b, _ := os.ReadFile(p)
			out[rel] = "file:" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	var diff []string
	for k, v := range before {
		if w, ok := after[k]; !ok {
			diff = append(diff, "gone: "+k)
		} else if w != v {
			diff = append(diff, "changed: "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diff = append(diff, "new: "+k)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("%s changed the tree: %v", what, diff)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// names lists dir's entry names.
func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// withSeams restores the package's test seams when the test ends.
func withSeams(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { renameSeam, itemSeam = nil, nil })
}

// Decision (ADR-0053 §3): a rename never replaces. On the volume the tests run on, the
// no-replace rename answers EEXIST for a file onto a file, a folder onto an empty folder
// and onto a file, and a file onto an empty folder, and both are left as they were;
// os.Root's Rename would replace the file and the empty folder. A case-only rename of a
// file on a case-insensitive volume succeeds through the plain rename, naming one file;
// a hard-linked sibling under another name is never replaced; and a volume that answers
// ENOTSUP fails the item FILE_IO_FAILED with the target untouched, no fallback.
func TestRenameNoReplaceOnThisVolume(t *testing.T) {
	withSeams(t)
	t.Run("kernel refuses", func(t *testing.T) {
		dir := t.TempDir()
		mk := func(name, content string) {
			if content == "" {
				if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		mk("f1", "one")
		mk("f2", "two")
		mk("d1", "")
		mk("d2", "")
		mk("d3", "")
		d, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		before := tree(t, dir)
		for _, c := range []struct{ from, to string }{
			{"f1", "f2"}, // a file onto a file
			{"d1", "d2"}, // a folder onto an empty folder
			{"d1", "f2"}, // a folder onto a file
			{"f1", "d3"}, // a file onto an empty folder
		} {
			err := renameNoReplace(d, c.from, d, c.to)
			if !errors.Is(err, syscall.EEXIST) {
				t.Errorf("rename %s -> %s: %v; want EEXIST", c.from, c.to, err)
			}
			sameTree(t, c.from+" -> "+c.to, before, tree(t, dir))
		}
		// And it does rename to a free name.
		if err := renameNoReplace(d, "f1", d, "f3"); err != nil {
			t.Errorf("rename to a free name: %v", err)
		}
	})

	t.Run("case-only rename", func(t *testing.T) {
		f := newFixture(t)
		if !caseInsensitive(f.root) {
			t.Skip("case-sensitive volume: a case-only rename is an ordinary rename here (logged, not run)")
		}
		f.write("case/a.kt", "bytes of a")
		res, err := f.move([]MoveItem{mv("case/a.kt", "case/A.kt")}, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Done != 1 || res.Failed != 0 || res.Items[0].To != "case/A.kt" {
			t.Fatalf("a case-only rename: %+v", res)
		}
		if got := names(t, filepath.Join(f.root, "case")); len(got) != 1 || got[0] != "A.kt" {
			t.Errorf("the listing is %v; want A.kt alone", got)
		}
		if got := readFile(t, filepath.Join(f.root, "case", "A.kt")); got != "bytes of a" {
			t.Errorf("the bytes are %q after the rename", got)
		}
		// A folder takes the same path.
		f.write("case/sub/in.kt", "inside")
		res, err = f.move([]MoveItem{mv("case/sub", "case/Sub")}, "")
		if err != nil || res.Done != 1 {
			t.Fatalf("a case-only rename of a folder: %+v %v", res, err)
		}
		if got := names(t, filepath.Join(f.root, "case")); len(got) != 2 || got[0] != "A.kt" || got[1] != "Sub" {
			t.Errorf("the listing is %v; want A.kt and Sub", got)
		}
	})

	t.Run("hard-linked sibling", func(t *testing.T) {
		f := newFixture(t)
		f.write("hl/one.txt", "linked bytes")
		if err := os.Link(filepath.Join(f.root, "hl", "one.txt"), filepath.Join(f.root, "hl", "two.txt")); err != nil {
			t.Fatal(err)
		}
		before := tree(t, f.root)
		res, err := f.move([]MoveItem{mv("hl/one.txt", "hl/two.txt")}, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Done != 0 || res.Items[0].Code != "FILE_EXISTS" {
			t.Fatalf("a move onto a hard-linked sibling: %+v; want FILE_EXISTS", res)
		}
		sameTree(t, "the move onto a hard link", before, tree(t, f.root))
	})

	t.Run("volume without the flag", func(t *testing.T) {
		f := newFixture(t)
		f.write("nv/a.kt", "source")
		f.write("nv/b.kt", "target")
		noReplace = func(*os.File, string, *os.File, string) error { return syscall.ENOTSUP }
		t.Cleanup(func() { noReplace = renameNoReplace })
		before := tree(t, f.root)
		res, err := f.move([]MoveItem{mv("nv/a.kt", "nv/b.kt")}, "")
		if err != nil {
			t.Fatal(err)
		}
		it := res.Items[0]
		if res.Failed != 1 || it.Code != "FILE_IO_FAILED" {
			t.Fatalf("ENOTSUP: %+v; want the item FILE_IO_FAILED", res)
		}
		if !strings.Contains(it.Message, "not supported") && !strings.Contains(it.Message, "operation not supported") {
			t.Errorf("the message does not carry the OS error: %q", it.Message)
		}
		sameTree(t, "a volume that cannot refuse a replace", before, tree(t, f.root))
	})
}

// Decision (ADR-0053 §2; files.md §Paths and containment): a refusal decided from the
// text alone fails the whole call before anything changes: a path outside the root, the
// root itself, an empty path, a NUL, a source equal to its target, a target inside its
// source, a source named twice. A refusal only the filesystem can give (a parent symlink
// that leaves the root, however it is spelled) fails that item and the others go on. A
// symlink is moved as the link.
func TestMoveRefusesEscapes(t *testing.T) {
	f := newFixture(t)
	outsideAbs := filepath.Join(f.base, "outside")
	f.write("v.txt", "valid")
	f.write("a/x.txt", "x")
	f.write("a/x2.txt", "x2")
	f.write("a/x3.txt", "x3")
	before := tree(t, f.root)
	outsideBefore := tree(t, outsideAbs)

	lexical := []struct {
		name  string
		items []MoveItem
		want  capability.ErrorCode
	}{
		{"parent as source", []MoveItem{mv("..", "x")}, capability.ErrFileOutsideRoot},
		{"parent path as source", []MoveItem{mv("../x", "y")}, capability.ErrFileOutsideRoot},
		{"parent path as target", []MoveItem{mv("v.txt", "../y")}, capability.ErrFileOutsideRoot},
		{"absolute outside as source", []MoveItem{mv(filepath.Join(outsideAbs, "secret"), "y")}, capability.ErrFileOutsideRoot},
		{"absolute outside as target", []MoveItem{mv("v.txt", filepath.Join(outsideAbs, "y"))}, capability.ErrFileOutsideRoot},
		{"empty source", []MoveItem{mv("", "y")}, capability.ErrInvalidParams},
		{"empty target", []MoveItem{mv("v.txt", "")}, capability.ErrInvalidParams},
		{"dot as source", []MoveItem{mv(".", "y")}, capability.ErrFileOutsideRoot},
		{"dot as target", []MoveItem{mv("v.txt", ".")}, capability.ErrFileOutsideRoot},
		{"absolute root as source", []MoveItem{mv(f.root, "y")}, capability.ErrFileOutsideRoot},
		{"absolute root as target", []MoveItem{mv("v.txt", f.root)}, capability.ErrFileOutsideRoot},
		{"into itself", []MoveItem{mv("a", "a/b")}, capability.ErrInvalidParams},
		{"onto itself", []MoveItem{mv("a", "a")}, capability.ErrInvalidParams},
		{"onto itself spelled twice", []MoveItem{mv("a", "./a/")}, capability.ErrInvalidParams},
		{"NUL", []MoveItem{mv("v.txt", "y\x00z")}, capability.ErrInvalidParams},
		{"a source twice", []MoveItem{mv("v.txt", "y"), mv("v.txt", "z")}, capability.ErrInvalidParams},
		{"no items", nil, capability.ErrInvalidParams},
	}
	for _, c := range lexical {
		// A valid first item proves the whole call fails before anything changes.
		items := c.items
		if len(items) > 0 {
			items = append([]MoveItem{mv("v.txt", "moved.txt")}, items...)
		}
		_, err := f.move(items, "")
		if err == nil || err.Code != c.want {
			t.Errorf("%s: %v; want the call to fail %s", c.name, err, c.want)
		}
		sameTree(t, c.name, before, tree(t, f.root))
		sameTree(t, c.name+" (outside)", outsideBefore, tree(t, outsideAbs))
	}
	tooMany := make([]MoveItem, maxItems+1)
	for i := range tooMany {
		tooMany[i] = mv("v.txt", "t"+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	if _, err := f.move(tooMany, ""); err == nil || err.Code != capability.ErrInvalidParams {
		t.Errorf("%d items: %v; want REQUEST_INVALID_PARAMS", len(tooMany), err)
	}
	if _, err := f.move([]MoveItem{mv("v.txt", "y")}, "replace"); err == nil || err.Code != capability.ErrInvalidParams {
		t.Errorf("onCollision replace: %v; want REQUEST_INVALID_PARAMS", err)
	}
	sameTree(t, "the bounds", before, tree(t, f.root))

	// Through the filesystem: a parent symlink that leaves the root, relative and with an
	// absolute target. Each item fails FILE_OUTSIDE_ROOT; the valid ones are done.
	f.symlink(outsideAbs, "absout")
	res, err := f.move([]MoveItem{
		mv("v.txt", "moved.txt"),
		mv("a/x.txt", "dir/x.txt"),     // target parent: ../outside
		mv("dir/secret", "stolen"),     // source parent: ../outside
		mv("a/x2.txt", "absout/x.txt"), // target parent: an absolute link to the outside
		mv("absout/secret", "stolen2"), // source parent: the same
		mv("a/x3.txt", "a/y.txt"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	wantCodes := []string{"", "FILE_OUTSIDE_ROOT", "FILE_OUTSIDE_ROOT", "FILE_OUTSIDE_ROOT", "FILE_OUTSIDE_ROOT", ""}
	for i, w := range wantCodes {
		if res.Items[i].Code != w {
			t.Errorf("item %d: %+v; want code %q", i, res.Items[i], w)
		}
	}
	if res.Done != 2 || res.Failed != 4 {
		t.Errorf("done %d failed %d; want 2 and 4", res.Done, res.Failed)
	}
	sameTree(t, "the escapes through links", outsideBefore, tree(t, outsideAbs))
	if readFile(t, filepath.Join(f.root, "moved.txt")) != "valid" || readFile(t, filepath.Join(f.root, "a", "y.txt")) != "x3" {
		t.Errorf("the valid items of the batch were not done")
	}

	// A symlink is moved as the link, not what it points to.
	f.symlink("../outside/secret", "link1")
	res, err = f.move([]MoveItem{mv("link1", "link2"), mv("in", "in2")}, "")
	if err != nil || res.Done != 2 {
		t.Fatalf("moving links: %+v %v", res, err)
	}
	if tgt, lerr := os.Readlink(filepath.Join(f.root, "link2")); lerr != nil || tgt != "../outside/secret" {
		t.Errorf("link2 -> %q (%v); want the link itself moved", tgt, lerr)
	}
	if tgt, lerr := os.Readlink(filepath.Join(f.root, "in2")); lerr != nil || tgt != "src" {
		t.Errorf("in2 -> %q (%v); want the link itself moved", tgt, lerr)
	}
	sameTree(t, "moving links", outsideBefore, tree(t, outsideAbs))
	if readFile(t, filepath.Join(f.root, "src", "A.kt")) == "" {
		t.Error("a link's target folder was touched")
	}
}

// Decision (ADR-0053 §2, §3): a batch reports each item. Three items, the second's target
// exists: 1 and 3 are done, 2 is FILE_EXISTS and untouched. With keepBoth, the same
// collisions take D49's names, "b 2.kt" then "b 3.kt".
func TestMoveItemsReportEach(t *testing.T) {
	f := newFixture(t)
	f.write("m/a.kt", "A")
	f.write("m/b.kt", "taken")
	f.write("m/c.kt", "C")
	f.write("m/d.kt", "D")
	res, err := f.move([]MoveItem{mv("m/a.kt", "m/a2.kt"), mv("m/c.kt", "m/b.kt"), mv("m/d.kt", "m/d2.kt")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Failed != 1 {
		t.Fatalf("done %d failed %d; want 2 and 1: %+v", res.Done, res.Failed, res)
	}
	if res.Items[0].Code != "" || res.Items[1].Code != "FILE_EXISTS" || res.Items[2].Code != "" {
		t.Errorf("items: %+v; want 1 and 3 done, 2 FILE_EXISTS", res.Items)
	}
	if got := readFile(t, filepath.Join(f.root, "m", "b.kt")); got != "taken" {
		t.Errorf("the taken target now holds %q", got)
	}
	if got := readFile(t, filepath.Join(f.root, "m", "c.kt")); got != "C" {
		t.Errorf("the refused source holds %q", got)
	}

	f.write("k/p.kt", "P")
	f.write("k/q.kt", "Q")
	f.write("k/b.kt", "taken")
	res, err = f.move([]MoveItem{mv("k/p.kt", "k/b.kt"), mv("k/q.kt", "k/b.kt")}, "keepBoth")
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Items[0].To != "k/b 2.kt" || res.Items[1].To != "k/b 3.kt" {
		t.Fatalf("keepBoth: %+v; want k/b 2.kt then k/b 3.kt", res.Items)
	}
	if readFile(t, filepath.Join(f.root, "k", "b 2.kt")) != "P" || readFile(t, filepath.Join(f.root, "k", "b 3.kt")) != "Q" ||
		readFile(t, filepath.Join(f.root, "k", "b.kt")) != "taken" {
		t.Error("keepBoth put the bytes in the wrong place, or touched the taken target")
	}

	// D49's names: the extension is the last dot-suffix, a name that starts with its only
	// dot has none, a folder has none.
	for _, c := range []struct {
		name  string
		isDir bool
		want  string
	}{
		{"b.kt", false, "b 2.kt"}, {"a.tar.gz", false, "a.tar 2.gz"}, {".gitignore", false, ".gitignore 2"},
		{"README", false, "README 2"}, {"res", true, "res 2"}, {"v1.2", true, "v1.2 2"},
	} {
		if got := freeName(c.name, c.isDir, 2); got != c.want {
			t.Errorf("freeName(%q, dir=%v) = %q; want %q", c.name, c.isDir, got, c.want)
		}
	}
}

// Decision (ADR-0053 §2; the item code enum): what only the filesystem can say fails its
// item with the code that names it.
func TestMoveItemFailuresByCode(t *testing.T) {
	f := newFixture(t)
	f.write("e/file.txt", "x")
	f.write("e/other.txt", "y")
	f.write("e/file2.txt", "x2")
	f.write("e/file3.txt", "x3")
	if err := os.Mkdir(filepath.Join(f.root, "e", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := f.move([]MoveItem{
		mv("e/missing.txt", "e/z.txt"),         // the source is gone
		mv("e/file.txt", "nowhere/z.txt"),      // the target's parent is gone
		mv("e/file2.txt", "e/other.txt/z.txt"), // the target's parent is a file
		mv("e/file3.txt", "e/dir"),             // a file onto a folder
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"FILE_NOT_FOUND", "FILE_NOT_FOUND", "FILE_WRONG_TYPE", "FILE_EXISTS"}
	for i, w := range want {
		if res.Items[i].Code != w {
			t.Errorf("item %d: %+v; want %s", i, res.Items[i], w)
		}
		if res.Items[i].Message == "" {
			t.Errorf("item %d has no message", i)
		}
	}
	if readFile(t, filepath.Join(f.root, "e", "file.txt")) != "x" {
		t.Error("a refused source changed")
	}
}

// Decision (ADR-0053 §3): a writer that creates the target between the moment the two
// parents are open and the rename keeps its file: the item is FILE_EXISTS, the creator's
// bytes are intact and the source stays. A check followed by os.Root's Rename would
// overwrite it.
func TestMoveRaceKeepsTheOtherWritersFile(t *testing.T) {
	withSeams(t)
	f := newFixture(t)
	f.write("r/src.kt", "mine")
	renameSeam = func(from, to string) error {
		return os.WriteFile(filepath.Join(f.root, filepath.FromSlash(to)), []byte("theirs"), 0o644)
	}
	res, err := f.move([]MoveItem{mv("r/src.kt", "r/dst.kt")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Items[0].Code != "FILE_EXISTS" {
		t.Fatalf("a raced target: %+v; want FILE_EXISTS", res)
	}
	if got := readFile(t, filepath.Join(f.root, "r", "dst.kt")); got != "theirs" {
		t.Errorf("the other writer's file holds %q after the move", got)
	}
	if got := readFile(t, filepath.Join(f.root, "r", "src.kt")); got != "mine" {
		t.Errorf("the source holds %q after the refused move", got)
	}
}

// Decision (ADR-0053 §3): file.create makes a file by O_CREATE|O_EXCL and a folder by
// Mkdir, so neither can replace anything: an existing path of any kind is FILE_EXISTS and
// is left as it was, a file's bytes included. Missing parents are made; a parent that is
// a file is FILE_WRONG_TYPE; a path that leaves the root, or is the root, is refused.
func TestCreate(t *testing.T) {
	f := newFixture(t)
	res, err := f.create("n/deep/new.txt", "file")
	if err != nil || res.Path != "n/deep/new.txt" || res.Type != "file" {
		t.Fatalf("create a file: %+v %v", res, err)
	}
	if fi, serr := os.Stat(filepath.Join(f.root, "n", "deep", "new.txt")); serr != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Errorf("the new file: %v %v", fi, serr)
	}
	res, err = f.create("n/folder/inner", "dir")
	if err != nil || res.Type != "dir" {
		t.Fatalf("create a folder: %+v %v", res, err)
	}
	if fi, serr := os.Stat(filepath.Join(f.root, "n", "folder", "inner")); serr != nil || !fi.IsDir() {
		t.Errorf("the new folder: %v %v", fi, serr)
	}

	f.write("ex/keep.txt", "KEEP ME")
	f.symlink("../outside/missing", "ex/dangling")
	before := tree(t, f.root)
	for _, c := range []struct{ path, typ string }{
		{"ex/keep.txt", "file"}, // a file keeps its bytes (O_TRUNC would empty it)
		{"ex/keep.txt", "dir"},
		{"ex", "dir"},
		{"ex", "file"},
		{"ex/dangling", "file"}, // never created through a link
		{"ex/dangling", "dir"},
	} {
		_, e := f.create(c.path, c.typ)
		if e == nil || e.Code != capability.ErrFileExists {
			t.Errorf("create %s %s: %v; want FILE_EXISTS", c.path, c.typ, e)
		}
		sameTree(t, "create "+c.path+" "+c.typ, before, tree(t, f.root))
	}
	if readFile(t, filepath.Join(f.root, "ex", "keep.txt")) != "KEEP ME" {
		t.Error("an existing file lost its bytes")
	}

	if _, e := f.create("ex/keep.txt/x", "file"); e == nil || e.Code != capability.ErrFileWrongType {
		t.Errorf("a parent that is a file: %v; want FILE_WRONG_TYPE", e)
	}
	if _, e := f.create("ex/keep.txt/x/y", "dir"); e == nil || e.Code != capability.ErrFileWrongType {
		t.Errorf("a grandparent that is a file: %v; want FILE_WRONG_TYPE", e)
	}
	outsideAbs := filepath.Join(f.base, "outside")
	f.symlink(outsideAbs, "absout")
	outsideBefore := tree(t, outsideAbs)
	for _, p := range []string{"../x", "dir/x", "absout/x", filepath.Join(outsideAbs, "x"), ".", f.root} {
		_, e := f.create(p, "file")
		if e == nil || e.Code != capability.ErrFileOutsideRoot {
			t.Errorf("create %q: %v; want FILE_OUTSIDE_ROOT", p, e)
		}
	}
	sameTree(t, "the escapes", outsideBefore, tree(t, outsideAbs))
	for _, c := range []struct{ path, typ string }{{"", "file"}, {"a\x00b", "file"}, {"ok.txt", "link"}, {"ok.txt", ""}} {
		if _, e := f.create(c.path, c.typ); e == nil || e.Code != capability.ErrInvalidParams {
			t.Errorf("create %q %q: %v; want REQUEST_INVALID_PARAMS", c.path, c.typ, e)
		}
	}
}

// opsFixture runs file.move through a real Operations store, beside a test capability that
// takes the build lock's key.
func opsFixture(t *testing.T, f *fixture, withLock bool) (*capability.Operations, chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	reg := capability.NewRegistry()
	Register(reg, Options{Root: func(string) (string, bool) { return f.root, true }, Env: f.env})
	if withLock {
		reg.MustRegister(capability.Descriptor{
			Name: "fixture.build", Kind: capability.Command, Scope: capability.Workspace, LongRunning: true,
			Description: "Holds the build lock until its gate opens.",
			Params:      struct{}{}, Result: struct{}{},
			Resource: func(c *capability.Call) string { return "gradle.root:" + c.Workspace },
			Handler: func(ctx context.Context, call *capability.Call) (any, error) {
				select {
				case <-gate:
				case <-ctx.Done():
				}
				return struct{}{}, nil
			},
		})
	}
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admit{})
	ops.RegisterCapabilities()
	return ops, gate
}

func startMove(t *testing.T, ops *capability.Operations, items []MoveItem) string {
	t.Helper()
	params, _ := json.Marshal(MoveParams{Items: items})
	id, err := ops.Start(context.Background(), capability.Request{Capability: "file.move", Workspace: "ws", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Decision (ADR-0053 §1): the file operations take no build lock, as file.write takes
// none. With gradle.root:<wsid> held by another operation, file.move completes at once and
// is never queued behind it.
func TestFileOpsTakeNoBuildLock(t *testing.T) {
	f := newFixture(t)
	f.write("lk/a.kt", "a")
	ops, gate := opsFixture(t, f, true)
	defer close(gate)
	holder, err := ops.Start(context.Background(), capability.Request{Capability: "fixture.build", Workspace: "ws", Params: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := ops.Wait(context.Background(), holder, time.Now().Add(-time.Second)); out.State != capability.StateRunning {
		t.Fatalf("the lock holder is %s; want running", out.State)
	}
	id := startMove(t, ops, []MoveItem{mv("lk/a.kt", "lk/b.kt")})
	out, err := ops.Wait(context.Background(), id, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCompleted {
		t.Fatalf("file.move is %s while the build lock is held; want completed, never queued", out.State)
	}
	if res := out.Result.(MoveResult); res.Done != 1 {
		t.Errorf("result %+v", res)
	}
}

// cancelAt starts a file.move of three items whose seam stops at item stopAt, cancels the
// operation there, and returns the outcome. The seam blocks until the test has cancelled,
// so the cancel is always seen at that item.
func cancelAt(t *testing.T, f *fixture, stopAt int) capability.Outcome {
	t.Helper()
	withSeams(t)
	ops, _ := opsFixture(t, f, false)
	reached, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	itemSeam = func(i int) {
		if i == stopAt {
			once.Do(func() { close(reached); <-proceed })
		}
	}
	id := startMove(t, ops, []MoveItem{mv("c/a.kt", "c/a2.kt"), mv("c/b.kt", "c/b2.kt"), mv("c/c.kt", "c/c2.kt")})
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch never reached the item")
	}
	if err := ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	out, err := ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Decision (ADR-0053 §4): a cancel stops between items. After the first item it ends the
// operation completed, the later items OPERATION_CANCELLED and what was done kept, also
// when the first item failed (it was attempted); before any item it ends cancelled.
func TestCancelStopsBetweenItems(t *testing.T) {
	t.Run("after the first", func(t *testing.T) {
		f := newFixture(t)
		for _, n := range []string{"a", "b", "c"} {
			f.write("c/"+n+".kt", n)
		}
		out := cancelAt(t, f, 1)
		if out.State != capability.StateCompleted {
			t.Fatalf("state %s (%v); want completed", out.State, out.Error)
		}
		res := out.Result.(MoveResult)
		if res.Done != 1 || res.Failed != 2 || res.Items[0].Code != "" ||
			res.Items[1].Code != "OPERATION_CANCELLED" || res.Items[2].Code != "OPERATION_CANCELLED" {
			t.Errorf("items %+v", res)
		}
		if _, err := os.Stat(filepath.Join(f.root, "c", "a2.kt")); err != nil {
			t.Errorf("the first item's move was not kept: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.root, "c", "b.kt")); err != nil {
			t.Errorf("a cancelled item moved anyway: %v", err)
		}
	})
	t.Run("after a failed first", func(t *testing.T) {
		f := newFixture(t)
		for _, n := range []string{"b", "c"} {
			f.write("c/"+n+".kt", n)
		}
		out := cancelAt(t, f, 1) // c/a.kt does not exist: item 0 fails FILE_NOT_FOUND
		if out.State != capability.StateCompleted {
			t.Fatalf("state %s (%v); want completed: a failed item was attempted", out.State, out.Error)
		}
		res := out.Result.(MoveResult)
		if res.Items[0].Code != "FILE_NOT_FOUND" || res.Items[1].Code != "OPERATION_CANCELLED" || res.Items[2].Code != "OPERATION_CANCELLED" {
			t.Errorf("items %+v", res.Items)
		}
	})
	t.Run("before the first", func(t *testing.T) {
		f := newFixture(t)
		for _, n := range []string{"a", "b", "c"} {
			f.write("c/"+n+".kt", n)
		}
		out := cancelAt(t, f, 0)
		if out.State != capability.StateCancelled {
			t.Fatalf("state %s; want cancelled: no item was attempted", out.State)
		}
		if out.Error == nil || out.Error.Code != capability.ErrOperationCancelled {
			t.Errorf("error %+v", out.Error)
		}
		if _, err := os.Stat(filepath.Join(f.root, "c", "a.kt")); err != nil {
			t.Errorf("an item moved in a cancelled operation: %v", err)
		}
	})
}

// Decision (ADR-0036 Decision 2; ADR-0053 §2): a batch with failed items is a successful
// call, so its text rendering leads with the failures: the first line counts them and
// names their codes, then each failed item with its code and message, then the done ones.
func TestMoveTextLeadsWithTheFailures(t *testing.T) {
	f := newFixture(t)
	d, _ := f.reg.Lookup("file.move")
	one := MoveResult{
		Items:  []MovedItem{{From: "a.kt", To: "b.kt", Code: "FILE_EXISTS", Message: `"b.kt" already exists`}},
		Failed: 1,
	}
	if got, want := d.Text(one), "failed 1 of 1 FILE_EXISTS\nFILE_EXISTS a.kt -> b.kt \"\\\"b.kt\\\" already exists\""; got != want {
		t.Errorf("a one-item FILE_EXISTS result renders\n%s\nwant\n%s", got, want)
	}
	mixed := MoveResult{
		Items: []MovedItem{
			{From: "p.kt", To: "p2.kt"},
			{From: "q.kt", To: "x y.kt", Code: "FILE_NOT_FOUND", Message: "gone"},
			{From: "r.kt", To: "r2.kt", Code: "OPERATION_CANCELLED", Message: "cancelled"},
			{From: "s.kt", To: "s2.kt", Code: "FILE_NOT_FOUND", Message: "gone too"},
		},
		Done: 1, Failed: 3,
	}
	lines := strings.Split(d.Text(mixed), "\n")
	if lines[0] != "failed 3 of 4 FILE_NOT_FOUND,OPERATION_CANCELLED" {
		t.Errorf("first line %q", lines[0])
	}
	if len(lines) != 5 || !strings.HasPrefix(lines[1], "FILE_NOT_FOUND q.kt -> \"x y.kt\" ") || lines[4] != "moved p.kt -> p2.kt" {
		t.Errorf("rendering:\n%s", strings.Join(lines, "\n"))
	}
	if all := (MoveResult{Items: []MovedItem{{From: "a", To: "b"}}, Done: 1}); d.Text(all) != "failed 0 of 1\nmoved a -> b" {
		t.Errorf("an all-done result renders %q", d.Text(all))
	}
}

// Decision (ADR-0053 §1): the operations are for every audience and not destructive (they
// replace nothing), and their results carry no untrusted mark (ADR-0015 §1). file.create is
// idempotent; file.move is not: a swap batch run twice swaps back (m2g §Changes).
func TestOpsAnnotations(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"file.create", "file.move"} {
		d, ok := f.reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		a := d.ResolveAnnotations()
		if a.ReadOnly || a.Destructive || a.Idempotent != (name == "file.create") || a.OpenWorld {
			t.Errorf("%s annotations %+v; want a non-destructive command, idempotent only for file.create", name, a)
		}
		if d.Audience != capability.AudienceAll || d.Untrusted() || d.Resource != nil || d.Since != "0.19.0" {
			t.Errorf("%s: audience %v, untrusted %v, resource %v, since %s", name, d.Audience, d.Untrusted(), d.Resource != nil, d.Since)
		}
	}
}

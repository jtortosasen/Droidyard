package file

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// file.copy (plan m2g step 4; ADR-0053): bounded, complete-or-absent, never following a
// link, never replacing. Each test says which decision it holds.

func (f *fixture) copy(items []CopyItem, onCollision string) (CopyResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("file.copy", CopyParams{Items: items, OnCollision: onCollision})
	if err != nil {
		return CopyResult{}, err
	}
	return res.(CopyResult), nil
}

func cp(from, to string) CopyItem { return CopyItem{From: from, To: to} }

func withCopySeams(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { copySeam, noReplace, itemSeam = nil, renameNoReplace, nil })
}

// temporaries is every entry, file or folder, under root whose name is a droidyard
// temporary name.
func temporaries(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && tempWatchName(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustWrite(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// Decision (ADR-0053 §1, plan m2g step 4): a copy is complete or absent. A folder of four
// files whose third file fails to write leaves no destination and no temporary; the
// source is untouched. Without the cleanup the temporary folder stays.
func TestCopyIsCompleteOrAbsent(t *testing.T) {
	withCopySeams(t)
	f := newFixture(t)
	for _, n := range []string{"f1", "f2", "f3", "f4"} {
		f.write("src/"+n+".kt", "content "+n)
	}
	before := tree(t, f.root)
	files := 0
	copySeam = func(stage, rel string) error {
		if stage != stageFile {
			return nil
		}
		files++
		if files == 3 {
			return syscall.ENOSPC
		}
		return nil
	}
	res, err := f.copy([]CopyItem{cp("src", "dst")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Items[0].Code != "FILE_IO_FAILED" {
		t.Fatalf("a failing third file: %+v; want the item FILE_IO_FAILED", res)
	}
	if !strings.Contains(res.Items[0].Message, "no space left") {
		t.Errorf("the message does not carry the OS error: %q", res.Items[0].Message)
	}
	if files != 3 {
		t.Errorf("the seam ran for %d files; want the copy to stop at the third", files)
	}
	sameTree(t, "a copy that failed halfway", before, tree(t, f.root))
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}
}

// Decision (plan m2g D49): keepBoth gives a copy "name copy.ext", then "name copy 2.ext";
// a folder has no extension ("res copy"), and a name that starts with its only dot has none
// (".gitignore copy"). Duplicating is a copy onto the source's own path. Without keepBoth
// the same call is FILE_EXISTS. A copy to a free name is that name.
func TestCopyKeepsBothNames(t *testing.T) {
	f := newFixture(t)
	f.write("d/a.kt", "bytes of a")
	f.write("d/res/values/s.xml", "<r/>")
	f.write("d/.gitignore", "build/")
	f.write("d/v1.2/x", "x")

	res, err := f.copy([]CopyItem{cp("d/a.kt", "d/a.kt")}, "")
	if err != nil || res.Items[0].Code != "FILE_EXISTS" {
		t.Fatalf("a duplicate without keepBoth: %+v %v; want FILE_EXISTS", res, err)
	}
	for i, want := range []string{"d/a copy.kt", "d/a copy 2.kt", "d/a copy 3.kt"} {
		res, err = f.copy([]CopyItem{cp("d/a.kt", "d/a.kt")}, "keepBoth")
		if err != nil || res.Done != 1 || res.Items[0].To != want {
			t.Fatalf("duplicate %d: %+v %v; want %s", i+1, res, err, want)
		}
		if got := readFile(t, filepath.Join(f.root, filepath.FromSlash(want))); got != "bytes of a" {
			t.Errorf("%s holds %q", want, got)
		}
	}
	res, err = f.copy([]CopyItem{cp("d/res", "d/res"), cp("d/.gitignore", "d/.gitignore"), cp("d/v1.2", "d/v1.2")}, "keepBoth")
	if err != nil || res.Done != 3 {
		t.Fatalf("duplicating a folder, a dot file and a dotted folder: %+v %v", res, err)
	}
	for i, want := range []string{"d/res copy", "d/.gitignore copy", "d/v1.2 copy"} {
		if res.Items[i].To != want {
			t.Errorf("item %d went to %q; want %q", i, res.Items[i].To, want)
		}
	}
	if got := readFile(t, filepath.Join(f.root, "d", "res copy", "values", "s.xml")); got != "<r/>" {
		t.Errorf("the copied folder holds %q", got)
	}
	// A free target is taken as asked, keepBoth or not.
	res, err = f.copy([]CopyItem{cp("d/a.kt", "d/b.kt")}, "keepBoth")
	if err != nil || res.Items[0].To != "d/b.kt" {
		t.Errorf("a copy to a free name: %+v %v", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "d", "a.kt")); got != "bytes of a" {
		t.Errorf("the source holds %q after the copies", got)
	}
}

// Decision (files.md §Paths and containment): a copy never follows a symlink. A link to
// ../outside and one with an absolute target, inside a copied folder or named as the item,
// are re-created as links with their target text; the files they point at are not copied
// and not changed.
func TestCopyNeverFollowsLinks(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.base, "outside")
	f.write("L/plain.txt", "p")
	if err := os.Symlink("../../outside/secret", filepath.Join(f.root, "L", "rel")); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(outside, "secret")
	if err := os.Symlink(abs, filepath.Join(f.root, "L", "abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside/missing", filepath.Join(f.root, "L", "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(f.root, "L", "dirlink")); err != nil {
		t.Fatal(err)
	}
	outsideBefore := tree(t, outside)
	res, err := f.copy([]CopyItem{cp("L", "L2"), cp("in", "in2"), cp("rel", "rel2")}, "")
	if err != nil || res.Done != 3 {
		t.Fatalf("copying links: %+v %v", res, err)
	}
	for link, want := range map[string]string{
		"L2/rel": "../../outside/secret", "L2/abs": abs, "L2/dangling": "../outside/missing",
		"L2/dirlink": "../outside", "in2": "src", "rel2": "../outside/secret",
	} {
		fi, lerr := os.Lstat(filepath.Join(f.root, link))
		if lerr != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is not a link: %v %v", link, fi, lerr)
			continue
		}
		if tgt, _ := os.Readlink(filepath.Join(f.root, link)); tgt != want {
			t.Errorf("%s -> %q; want %q", link, tgt, want)
		}
	}
	sameTree(t, "copying links", outsideBefore, tree(t, outside))
	if readFile(t, filepath.Join(f.root, "src", "A.kt")) == "" {
		t.Error("a link's target folder was touched")
	}
	if _, err := os.Stat(filepath.Join(f.root, "in2", "A.kt")); err != nil {
		t.Errorf("in2 must be a link to src, which holds A.kt: %v", err)
	}
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}
}

// Decision (ADR-0053 §2; files.md §Paths and containment): a refusal from the text alone
// fails the whole call before anything is written; a refusal only the filesystem can give
// (a parent link that leaves the root) fails that item and the valid ones are done. A
// source cannot be copied inside itself, lexically or through a link that resolves under
// it: that copy is made once, from the snapshot, with no recursion.
func TestCopyRefusesEscapes(t *testing.T) {
	f := newFixture(t)
	outsideAbs := filepath.Join(f.base, "outside")
	f.write("v.txt", "valid")
	f.write("a/x.txt", "x")
	f.write("a/x2.txt", "x2")
	f.write("a/sub/y.txt", "y")
	before := tree(t, f.root)
	outsideBefore := tree(t, outsideAbs)

	lexical := []struct {
		name  string
		items []CopyItem
		want  capability.ErrorCode
	}{
		{"parent as source", []CopyItem{cp("..", "x")}, capability.ErrFileOutsideRoot},
		{"parent path as source", []CopyItem{cp("../x", "y")}, capability.ErrFileOutsideRoot},
		{"parent path as target", []CopyItem{cp("v.txt", "../y")}, capability.ErrFileOutsideRoot},
		{"absolute outside as source", []CopyItem{cp(filepath.Join(outsideAbs, "secret"), "y")}, capability.ErrFileOutsideRoot},
		{"absolute outside as target", []CopyItem{cp("v.txt", filepath.Join(outsideAbs, "y"))}, capability.ErrFileOutsideRoot},
		{"empty source", []CopyItem{cp("", "y")}, capability.ErrInvalidParams},
		{"empty target", []CopyItem{cp("v.txt", "")}, capability.ErrInvalidParams},
		{"dot as source", []CopyItem{cp(".", "y")}, capability.ErrFileOutsideRoot},
		{"dot as target", []CopyItem{cp("v.txt", ".")}, capability.ErrFileOutsideRoot},
		{"absolute root as source", []CopyItem{cp(f.root, "y")}, capability.ErrFileOutsideRoot},
		{"absolute root as target", []CopyItem{cp("v.txt", f.root)}, capability.ErrFileOutsideRoot},
		{"into itself", []CopyItem{cp("a", "a/b")}, capability.ErrInvalidParams},
		{"into itself, spelled otherwise", []CopyItem{cp("a", "./a/sub/b/")}, capability.ErrInvalidParams},
		{"NUL", []CopyItem{cp("v.txt", "y\x00z")}, capability.ErrInvalidParams},
		{"a source twice", []CopyItem{cp("v.txt", "y"), cp("v.txt", "z")}, capability.ErrInvalidParams},
		{"no items", nil, capability.ErrInvalidParams},
	}
	for _, c := range lexical {
		items := c.items
		if len(items) > 0 {
			items = append([]CopyItem{cp("v.txt", "copied.txt")}, items...)
		}
		_, err := f.copy(items, "")
		if err == nil || err.Code != c.want {
			t.Errorf("%s: %v; want the call to fail %s", c.name, err, c.want)
		}
		sameTree(t, c.name, before, tree(t, f.root))
		sameTree(t, c.name+" (outside)", outsideBefore, tree(t, outsideAbs))
	}
	if _, err := f.copy([]CopyItem{cp("v.txt", "y")}, "replace"); err == nil || err.Code != capability.ErrInvalidParams {
		t.Errorf("onCollision replace: %v; want REQUEST_INVALID_PARAMS", err)
	}

	// Through the filesystem: parent links that leave the root, relative and absolute.
	f.symlink(outsideAbs, "absout")
	res, err := f.copy([]CopyItem{
		cp("v.txt", "copied.txt"),
		cp("a/x.txt", "dir/x.txt"),     // target parent: ../outside
		cp("dir/secret", "stolen"),     // source parent: ../outside
		cp("a/x2.txt", "absout/x.txt"), // target parent: an absolute link to the outside
		cp("absout/secret", "stolen2"), // source parent: the same
		cp("a/sub/y.txt", "a/y.txt"),
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
	sameTree(t, "the escapes through links", outsideBefore, tree(t, outsideAbs))
	if readFile(t, filepath.Join(f.root, "copied.txt")) != "valid" || readFile(t, filepath.Join(f.root, "a", "y.txt")) != "y" {
		t.Errorf("the valid items of the batch were not done")
	}
	os.Remove(filepath.Join(f.root, "copied.txt"))
	os.Remove(filepath.Join(f.root, "a", "y.txt"))

	// Into a symlinked folder that resolves under the source: a copy of the snapshot, once.
	f.symlink("a/sub", "under")
	res, err = f.copy([]CopyItem{cp("a", "under/x")}, "")
	if err != nil || res.Done != 1 {
		t.Fatalf("a into a link that resolves under a: %+v %v", res, err)
	}
	for _, p := range []string{"a/sub/x/x.txt", "a/sub/x/x2.txt", "a/sub/x/sub/y.txt"} {
		if _, serr := os.Stat(filepath.Join(f.root, p)); serr != nil {
			t.Errorf("the snapshot's %s is missing: %v", p, serr)
		}
	}
	if _, serr := os.Lstat(filepath.Join(f.root, "a", "sub", "x", "sub", "x")); serr == nil {
		t.Error("the copy recursed into itself: a/sub/x/sub/x exists")
	}
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}
}

// Decision (plan m2g D48): a source over the limits fails its item FILE_TOO_LARGE with
// nothing written. The limits, scaled here through Options, admit a tree exactly at them
// and refuse one entry or one byte more; the real ones are 20,000 entries and 1 GiB.
func TestCopyBounds(t *testing.T) {
	if maxCopyEntries != 20000 || maxCopyBytes != 1<<30 {
		t.Fatalf("limits %d entries, %d bytes; D48 says 20,000 and 1 GiB", maxCopyEntries, maxCopyBytes)
	}
	f := newFixtureWith(t, func(o *Options) { o.CopyMaxEntries, o.CopyMaxBytes = 5, 100 })
	for _, n := range []string{"1", "2", "3", "4"} { // the folder and four files: 5 entries
		f.write("ok/"+n, "x")
	}
	for _, n := range []string{"1", "2", "3", "4", "5"} { // six entries
		f.write("over/"+n, "x")
	}
	f.write("deep/a/b/c/d/e", "x") // deep, with the folder: 6 entries
	f.write("fits.bin", strings.Repeat("x", 100))
	f.write("big.bin", strings.Repeat("x", 101))
	f.write("two/a", strings.Repeat("x", 60))
	f.write("two/b", strings.Repeat("x", 41))
	before := tree(t, f.root)

	res, err := f.copy([]CopyItem{cp("over", "o1"), cp("deep", "o2"), cp("big.bin", "o3"), cp("two", "o4")}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, it := range res.Items {
		if it.Code != "FILE_TOO_LARGE" {
			t.Errorf("item %d: %+v; want FILE_TOO_LARGE", i, it)
		}
	}
	sameTree(t, "copies over the limits", before, tree(t, f.root))
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}

	res, err = f.copy([]CopyItem{cp("ok", "ok2"), cp("fits.bin", "fits2.bin")}, "")
	if err != nil || res.Done != 2 {
		t.Fatalf("copies exactly at the limits: %+v %v; want both done", res, err)
	}
}

// Decision (plan m2g D48, ADR-0053 §2): the default limits are real. A tree of exactly
// 20,000 entries copies and one entry more is FILE_TOO_LARGE. Time of the 20,000-entry
// copy is logged.
func TestCopyDefaultLimitsAtTwentyThousandEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 20,000 entries")
	}
	f := newFixture(t)
	base := filepath.Join(f.root, "big")
	const dirs, perDir = 100, 199 // 1 + 100 + 100*199 = 20,001 entries
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(base, "d"+strconv.Itoa(d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < perDir; i++ {
			if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := f.copy([]CopyItem{cp("big", "big2")}, "")
	if err != nil || res.Items[0].Code != "FILE_TOO_LARGE" {
		t.Fatalf("20,001 entries: %+v %v; want FILE_TOO_LARGE", res, err)
	}
	if err := os.Remove(filepath.Join(base, "d0", "f0")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err = f.copy([]CopyItem{cp("big", "big2")}, "")
	took := time.Since(start)
	if err != nil || res.Done != 1 {
		t.Fatalf("20,000 entries: %+v %v; want done", res, err)
	}
	t.Logf("copied a 20,000-entry tree in %s", took)
	if _, serr := os.Stat(filepath.Join(f.root, "big2", "d99", "f198")); serr != nil {
		t.Errorf("the last file is missing: %v", serr)
	}
}

// Decision (plan m2g step 4, openRegular): a regular file in the snapshot swapped for a
// FIFO after the walk fails its item FILE_WRONG_TYPE at once, with nothing left behind: the
// copy opens sources by openRegular, not a plain open, which would block on a FIFO. A FIFO
// found by the walk itself, in a folder or as the item, is FILE_WRONG_TYPE with nothing
// written.
func TestCopyRefusesSpecialFiles(t *testing.T) {
	withCopySeams(t)
	f := newFixture(t)
	f.write("s/a.kt", "a")
	f.write("s/b.kt", "b")
	copySeam = func(stage, rel string) error {
		if stage != stageWalked {
			return nil
		}
		p := filepath.Join(f.root, "s", "b.kt")
		if err := os.Remove(p); err != nil {
			return err
		}
		return syscall.Mkfifo(p, 0o644)
	}
	done := make(chan CopyResult, 1)
	go func() {
		res, _ := f.copy([]CopyItem{cp("s", "t")}, "")
		done <- res
	}()
	select {
	case res := <-done:
		if res.Failed != 1 || res.Items[0].Code != "FILE_WRONG_TYPE" {
			t.Errorf("a source swapped for a FIFO: %+v; want FILE_WRONG_TYPE", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the copy blocked on a FIFO swapped in after the walk")
	}
	copySeam = nil
	if _, err := os.Lstat(filepath.Join(f.root, "t")); err == nil {
		t.Error("a destination exists after the refused copy")
	}
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}

	// The walk finds a FIFO: nothing is written for that item, however early its entry is.
	if err := os.Remove(filepath.Join(f.root, "s", "a.kt")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.root, "s", "a.kt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(f.root, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := tree(t, f.root)
	res, err := f.copy([]CopyItem{cp("s", "t"), cp("pipe", "pipe2")}, "")
	if err != nil || res.Items[0].Code != "FILE_WRONG_TYPE" || res.Items[1].Code != "FILE_WRONG_TYPE" {
		t.Fatalf("FIFOs found by the walk: %+v %v; want both items FILE_WRONG_TYPE", res, err)
	}
	sameTree(t, "copies of FIFOs", snapshot, tree(t, f.root))
}

// Decision (ADR-0053 §4): a cancel reaches inside a copy. Cancelled at the second file of a
// folder, the item is OPERATION_CANCELLED, the operation completes (an item was attempted)
// and no destination or temporary is left.
func TestCopyCancelMidCopy(t *testing.T) {
	withCopySeams(t)
	f := newFixture(t)
	for _, n := range []string{"f1", "f2", "f3", "f4"} {
		f.write("src/"+n, strings.Repeat(n, 1000))
	}
	f.write("other.kt", "o")
	before := tree(t, f.root)
	ops, _ := opsFixture(t, f, false)
	reached, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	files := 0
	copySeam = func(stage, rel string) error {
		if stage == stageFile {
			files++
			if files == 2 {
				once.Do(func() { close(reached); <-proceed })
			}
		}
		return nil
	}
	params, _ := json.Marshal(CopyParams{Items: []CopyItem{cp("src", "dst"), cp("other.kt", "other2.kt")}})
	id, err := ops.Start(context.Background(), capability.Request{Capability: "file.copy", Workspace: "ws", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the copy never reached its second file")
	}
	if err := ops.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	out, err := ops.Wait(context.Background(), id, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != capability.StateCompleted {
		t.Fatalf("state %s (%v); want completed", out.State, out.Error)
	}
	res := out.Result.(CopyResult)
	if res.Items[0].Code != "OPERATION_CANCELLED" || res.Items[1].Code != "OPERATION_CANCELLED" || res.Done != 0 {
		t.Errorf("items %+v; want both OPERATION_CANCELLED", res.Items)
	}
	sameTree(t, "a cancelled copy", before, tree(t, f.root))
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}
}

// Decision (ADR-0053 §3): a copy never replaces. A writer that creates the target after the
// copy is built and before the rename keeps its file (FILE_EXISTS, no temporary); and a
// volume that cannot refuse a replace fails the item FILE_IO_FAILED with the target
// untouched, never falling back to a replacing rename.
func TestCopyNeverReplaces(t *testing.T) {
	withCopySeams(t)
	f := newFixture(t)
	f.write("r/a.kt", "source")
	copySeam = func(stage, rel string) error {
		if stage == stageRename {
			return os.WriteFile(filepath.Join(f.root, "r", "b.kt"), []byte("the other writer's"), 0o644)
		}
		return nil
	}
	res, err := f.copy([]CopyItem{cp("r/a.kt", "r/b.kt")}, "")
	if err != nil || res.Items[0].Code != "FILE_EXISTS" {
		t.Fatalf("a target taken during the copy: %+v %v; want FILE_EXISTS", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "r", "b.kt")); got != "the other writer's" {
		t.Errorf("the other writer's file holds %q", got)
	}
	if tmp := temporaries(t, f.root); len(tmp) != 0 {
		t.Errorf("temporaries left behind: %v", tmp)
	}
	copySeam = nil

	f.write("r/c.kt", "target")
	f.write("r/d.kt", "d")
	noReplace = func(*os.File, string, *os.File, string) error { return syscall.ENOTSUP }
	before := tree(t, f.root)
	res, err = f.copy([]CopyItem{cp("r/a.kt", "r/c.kt"), cp("r/d.kt", "r/free.kt")}, "keepBoth") // keepBoth: the precheck of a taken name is not the call under test
	if err != nil || res.Items[0].Code != "FILE_IO_FAILED" || res.Items[1].Code != "FILE_IO_FAILED" {
		t.Fatalf("ENOTSUP: %+v %v; want both items FILE_IO_FAILED", res, err)
	}
	sameTree(t, "a volume that cannot refuse a replace", before, tree(t, f.root))
}

// Decision (plan m2g step 4): a copy keeps content and permission bits (set through the new
// file's descriptor), and takes no setuid bit; a folder tree copies whole, a content larger
// than a copy buffer included, and the source is untouched.
func TestCopyKeepsContentAndModes(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("0123456789abcdef", 40000) // 640,000 bytes
	mustWrite(t, filepath.Join(f.root, "t", "run.sh"), "#!/bin/sh\n", 0o755)
	mustWrite(t, filepath.Join(f.root, "t", "ro.txt"), "read only", 0o444)
	mustWrite(t, filepath.Join(f.root, "t", "sub", "big.bin"), big, 0o640)
	mustWrite(t, filepath.Join(f.root, "t", "sub", "empty"), "", 0o600)
	mustWrite(t, filepath.Join(f.root, "t", "su.sh"), "x", 0o4755)
	if err := os.Mkdir(filepath.Join(f.root, "t", "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := tree(t, f.root)
	res, err := f.copy([]CopyItem{cp("t", "t2")}, "")
	if err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	sameTree(t, "the copy", before, without(tree(t, f.root), "t2"))
	for name, want := range map[string]os.FileMode{"run.sh": 0o755, "ro.txt": 0o444, "sub/big.bin": 0o640, "sub/empty": 0o600, "su.sh": 0o755} {
		fi, serr := os.Stat(filepath.Join(f.root, "t2", filepath.FromSlash(name)))
		if serr != nil || fi.Mode() != want {
			t.Errorf("t2/%s: %v %v; want mode %v", name, fi, serr, want)
		}
	}
	got := tree(t, filepath.Join(f.root, "t2"))
	want := tree(t, filepath.Join(f.root, "t"))
	sameTree(t, "t2 against t", want, got)
	if _, err := os.Stat(filepath.Join(f.root, "t2", "emptydir")); err != nil {
		t.Errorf("the empty folder was not copied: %v", err)
	}
}

// without drops t's entries at and under prefix from a tree map.
func without(m map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k == prefix || strings.HasPrefix(k, prefix+"/") {
			continue
		}
		out[k] = v
	}
	return out
}

// Decision (plan m2g step 4): another operation's temporary names inside a source are not
// part of its tree and are not copied.
func TestCopySkipsTemporaryNames(t *testing.T) {
	f := newFixture(t)
	f.write("s/real.kt", "r")
	f.write("s/.real.kt.droidyard-0123456789abcdef.tmp", "half written")
	f.write("s/.droidyard-0123456789abcdef.tmp/in", "half copied")
	if res, err := f.copy([]CopyItem{cp("s", "s2")}, ""); err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := names(t, filepath.Join(f.root, "s2")); len(got) != 1 || got[0] != "real.kt" {
		t.Errorf("the copy holds %v; want real.kt alone", got)
	}
}

// Decision (ADR-0036 Decision 2; ADR-0053 §2): a copy batch renders as move's does, the
// failures first.
func TestCopyTextLeadsWithTheFailures(t *testing.T) {
	f := newFixture(t)
	d, _ := f.reg.Lookup("file.copy")
	r := CopyResult{
		Items: []CopiedItem{
			{From: "a.kt", To: "a copy.kt"},
			{From: "big", To: "big2", Code: "FILE_TOO_LARGE", Message: "too big"},
		},
		Done: 1, Failed: 1,
	}
	lines := strings.Split(d.Text(r), "\n")
	if lines[0] != "failed 1 of 2 FILE_TOO_LARGE" || !strings.HasPrefix(lines[1], "FILE_TOO_LARGE big -> big2 ") || lines[2] != `copied a.kt -> "a copy.kt"` {
		t.Errorf("rendering:\n%s", strings.Join(lines, "\n"))
	}
}

// Decision (plan m2g step 4): the temporary a watch must not report is a folder as well as
// a file. A watch of the target's folder, ticked while a copy of a folder is built and
// complete but not renamed, reports nothing; after the rename it reports the final name
// and what is under it, and never a temporary name. Seen failing with the file-only rule.
func TestWatchIgnoresTemporaryFolders(t *testing.T) {
	withCopySeams(t)
	f := newWatchFixture(t)
	f.write("w/src/a.kt", "a")
	f.write("w/src/sub/b.kt", "b")
	w, sub, _ := f.startManualWatch("w")

	ticked := false
	copySeam = func(stage, rel string) error {
		if stage == stageRename {
			if len(temporaries(t, filepath.Join(f.root, "w"))) == 0 {
				t.Error("no temporary folder exists at the rename")
			}
			w.tickOnce()
			ticked = true
		}
		return nil
	}
	res, err := f.copy([]CopyItem{cp("w/src", "w/dst")}, "")
	if err != nil || res.Done != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	copySeam = nil
	if !ticked {
		t.Fatal("the seam never ticked")
	}
	if got := drainEvents(sub); len(got) != 0 {
		t.Fatalf("a tick while the temporary folder was built published %d events; want none", len(got))
	}
	w.tickOnce()
	got := kinds(oneChanges(t, sub).Changes)
	want := map[string]string{"w/dst": "created", "w/dst/a.kt": "created", "w/dst/sub": "created", "w/dst/sub/b.kt": "created"}
	if len(got) != len(want) {
		t.Fatalf("after the rename the watch reported %+v; want %+v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s is %q; want %q", p, got[p], k)
		}
	}
	for p := range got {
		if tempWatchName(filepath.Base(p)) {
			t.Errorf("the watch reported the temporary name %s", p)
		}
	}
	if !strings.Contains(strings.Join(excludedText, "|"), "file.copy") {
		t.Errorf("excluded text %v does not name file.copy's temporaries", excludedText)
	}
}

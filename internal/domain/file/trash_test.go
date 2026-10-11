package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/store"
)

// The trash (plan m2g step 5; ADR-0054): file.delete and file.restore, on the real volume
// the tests run on, with a seam standing for the kernel's EXDEV where a second volume is
// needed. Each test says which decision it holds.

// trashFix is a fixture whose file capabilities have a trash under a state root of their
// own, beside the project root.
type trashFix struct {
	*fixture
	state string
	st    *store.Store
	tr    *Trash
}

func newTrashFixture(t *testing.T, tune func(*Options)) *trashFix {
	t.Helper()
	withTrashSeams(t)
	state := t.TempDir()
	st, err := store.New(state)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTrash(st)
	f := newFixtureWith(t, func(o *Options) {
		o.Trash = tr
		if tune != nil {
			tune(o)
		}
	})
	return &trashFix{fixture: f, state: state, st: st, tr: tr}
}

// withTrashSeams restores every seam the trash tests replace when the test ends.
func withTrashSeams(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		trashNow, fsync, removeAll, trashSeam = time.Now, func(f *os.File) error { return f.Sync() },
			func(root *os.Root, rel string) error { return root.RemoveAll(rel) }, nil
		copySeam, noReplace, itemSeam = nil, renameNoReplace, nil
	})
}

func (f *trashFix) callWS(ctx context.Context, ws, name string, params any) (any, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup(name)
	if !ok {
		f.t.Fatalf("%s is not registered", name)
	}
	res, err := d.Handler(ctx, &capability.Call{Workspace: ws, Params: params})
	return res, capability.AsError(err)
}

func (f *trashFix) del(paths ...string) (DeleteResult, *capability.Error) {
	f.t.Helper()
	return f.delCtx(context.Background(), "ws", paths...)
}

func (f *trashFix) delCtx(ctx context.Context, ws string, paths ...string) (DeleteResult, *capability.Error) {
	f.t.Helper()
	res, err := f.callWS(ctx, ws, "file.delete", DeleteParams{Paths: paths})
	if err != nil {
		return DeleteResult{}, err
	}
	return res.(DeleteResult), nil
}

func (f *trashFix) restore(ids ...string) (RestoreResult, *capability.Error) {
	f.t.Helper()
	return f.restoreIn("ws", ids...)
}

func (f *trashFix) restoreIn(ws string, ids ...string) (RestoreResult, *capability.Error) {
	f.t.Helper()
	res, err := f.callWS(context.Background(), ws, "file.restore", RestoreParams{TrashIDs: ids})
	if err != nil {
		return RestoreResult{}, err
	}
	return res.(RestoreResult), nil
}

// oneDeleted deletes path and returns its trash id, failing the test if it was not done.
func (f *trashFix) oneDeleted(path string) string {
	f.t.Helper()
	res, err := f.del(path)
	if err != nil || res.Done != 1 {
		f.t.Fatalf("deleting %s: %+v %v", path, res, err)
	}
	return res.Items[0].TrashID
}

func (f *trashFix) trashDir(ws string) string { return filepath.Join(f.state, "ws", ws, "trash") }
func (f *trashFix) itemPath(id string) string { return filepath.Join(f.trashDir("ws"), id, "item") }

// trashIDs lists the workspace's entry folders.
func (f *trashFix) trashIDs(ws string) []string {
	f.t.Helper()
	es, err := os.ReadDir(f.trashDir(ws))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// entry reads an entry.json, also returning its raw members.
func (f *trashFix) entry(id string) (trashEntry, map[string]any) {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.trashDir("ws"), id, "entry.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var e trashEntry
	var raw map[string]any
	if err := json.Unmarshal(b, &e); err != nil {
		f.t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		f.t.Fatal(err)
	}
	return e, raw
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// crossVolume makes the kernel answer EXDEV for the one rename that would put an item in
// the trash, or take it out: the call itself is the real one for every other rename.
func crossVolume() { crossVolumeHook(nil) }

// crossVolumeHook is crossVolume that also calls hook, if any, just before every rename it
// does not fail, so a test can act at a chosen rename.
func crossVolumeHook(hook func(from, to string)) {
	noReplace = func(fromDir *os.File, from string, toDir *os.File, to string) error {
		// Only a rename between two folders crosses a volume; item.partial to item, in one
		// folder, never does.
		if fromDir.Name() != toDir.Name() && (to == trashItem || from == trashItem) {
			return syscall.EXDEV
		}
		if hook != nil {
			hook(from, to)
		}
		return renameNoReplace(fromDir, from, toDir, to)
	}
}

// syncLog records the base names of the files and folders fsynced.
type syncLog struct {
	mu    sync.Mutex
	names []string
	full  []string
}

func recordSyncs() *syncLog {
	l := &syncLog{}
	fsync = func(f *os.File) error {
		l.mu.Lock()
		l.names = append(l.names, filepath.Base(f.Name()))
		l.full = append(l.full, f.Name())
		l.mu.Unlock()
		return f.Sync()
	}
	return l
}

func (l *syncLog) has(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range l.names {
		if n == name {
			return true
		}
	}
	return false
}

// hasSuffix reports whether a synced path ends with suffix (a slash-separated tail).
func (l *syncLog) hasSuffix(suffix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range l.full {
		if strings.HasSuffix(filepath.ToSlash(n), suffix) {
			return true
		}
	}
	return false
}

// asides is the droidyard temporary entries directly in dir.
func asides(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, n := range names(t, dir) {
		if tempWatchName(n) {
			out = append(out, n)
		}
	}
	return out
}

// Decision (ADR-0054 §1, §2): on one volume a delete is one rename, any size, the record
// written before it. The item's inode is the same under trash/<id>/item, the path is
// gone, entry.json has schemaVersion 1 and the fields of the design, and by the time of
// the rename entry.json already exists. Both folders are fsynced.
func TestDeleteMovesToTrash(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("d/a.kt", "package a")
	f.write("tree/x/one.kt", "1")
	f.write("tree/x/two.kt", "22")
	f.write("tree/three.kt", "333")
	ino := inode(t, filepath.Join(f.root, "d/a.kt"))
	treeIno := inode(t, filepath.Join(f.root, "tree"))
	syncs := recordSyncs()
	trashNow = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }

	// The record exists when the item moves.
	var recordFirst []bool
	noReplace = func(fromDir *os.File, from string, toDir *os.File, to string) error {
		if to == trashItem {
			_, err := os.Stat(filepath.Join(toDir.Name(), entryFile))
			recordFirst = append(recordFirst, err == nil)
		}
		return renameNoReplace(fromDir, from, toDir, to)
	}

	res, err := f.del("d/a.kt", "tree")
	if err != nil || res.Done != 2 || res.Failed != 0 {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if !reflect.DeepEqual(recordFirst, []bool{true, true}) {
		t.Errorf("entry.json existed at the rename: %v; want it written before the item moved", recordFirst)
	}
	id, treeID := res.Items[0].TrashID, res.Items[1].TrashID
	for _, i := range []string{id, treeID} {
		if !trashIDPattern.MatchString(i) {
			t.Errorf("trash id %q is not 16 lowercase hex", i)
		}
	}
	if got := inode(t, f.itemPath(id)); got != ino {
		t.Errorf("the trashed file has inode %d, was %d: it was copied, not renamed", got, ino)
	}
	if got := inode(t, f.itemPath(treeID)); got != treeIno {
		t.Errorf("the trashed folder has inode %d, was %d", got, treeIno)
	}
	if readFile(t, f.itemPath(id)) != "package a" {
		t.Error("the trashed file lost its bytes")
	}
	for _, p := range []string{"d/a.kt", "tree"} {
		if _, err := os.Lstat(filepath.Join(f.root, p)); !os.IsNotExist(err) {
			t.Errorf("%s is still at its path after the delete: %v", p, err)
		}
	}
	e, raw := f.entry(id)
	if raw["schemaVersion"] != float64(1) || e.Path != "d/a.kt" || e.Type != "file" || e.Bytes != 9 || e.Entries != 1 ||
		!e.DeletedAt.Equal(trashNow()) || e.Aside != "" || e.Truncated {
		t.Errorf("entry.json is %v", raw)
	}
	te, _ := f.entry(treeID)
	if te.Type != "dir" || te.Entries != 5 || te.Bytes != 6 {
		t.Errorf("the folder's entry is %+v; want type dir, 5 entries (tree, x, three files), 6 bytes", te)
	}
	// Both folders of each rename are synced: the trash entry's, and the item's old parent.
	if !syncs.hasSuffix("/ws/ws/trash") || !syncs.hasSuffix("/ws/ws") {
		t.Errorf("synced %v; want trash/ and ws/<wsid>/ synced after the record is written", syncs.full)
	}
	if !syncs.has(id) || !syncs.has("d") || !syncs.has(treeID) {
		t.Errorf("synced %v; want the entry folders %s and %s and the old parent d", syncs.names, id, treeID)
	}
	if got := asides(t, f.root); len(got) != 0 {
		t.Errorf("droidyard temporaries left in the project: %v", got)
	}
}

// Decision (ADR-0054 §2): across volumes the item is renamed aside in its own folder,
// copied whole with every file and folder synced, and the aside removed only once the
// trash holds it.
func TestDeleteAcrossVolumes(t *testing.T) {
	setup := func(t *testing.T) *trashFix {
		f := newTrashFixture(t, nil)
		f.write("v/tree/a.kt", "AAAA")
		f.write("v/tree/sub/b.kt", "BBBBBB")
		f.write("v/solo.kt", "solo")
		crossVolume()
		return f
	}

	t.Run("the aside is removed only after item exists, and every file and folder is synced", func(t *testing.T) {
		f := setup(t)
		syncs := recordSyncs()
		var itemThere []bool
		removeAll = func(root *os.Root, rel string) error {
			any := false
			for _, id := range f.trashIDs("ws") {
				if exists(f.itemPath(id)) {
					any = true
				}
			}
			itemThere = append(itemThere, any && tempWatchName(filepath.Base(rel)))
			return root.RemoveAll(rel)
		}
		res, err := f.del("v/tree", "v/solo.kt")
		if err != nil || res.Done != 2 {
			t.Fatalf("delete: %+v %v", res, err)
		}
		if !reflect.DeepEqual(itemThere, []bool{true, true}) {
			t.Errorf("at the removal of the asides, item existed and the name was an aside: %v; want both true", itemThere)
		}
		id := res.Items[0].TrashID
		if readFile(t, filepath.Join(f.itemPath(id), "sub/b.kt")) != "BBBBBB" || readFile(t, filepath.Join(f.itemPath(id), "a.kt")) != "AAAA" {
			t.Error("the trashed folder lost bytes")
		}
		if readFile(t, f.itemPath(res.Items[1].TrashID)) != "solo" {
			t.Error("the trashed file lost its bytes")
		}
		if _, err := os.Lstat(filepath.Join(f.root, "v/tree")); !os.IsNotExist(err) {
			t.Errorf("the folder is still at its path: %v", err)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left after a clean delete: %v", got)
		}
		e, _ := f.entry(id)
		if e.Aside != "" {
			t.Errorf("the entry still records aside %q after the aside was removed; want none, so no sweep or eviction looks for it", e.Aside)
		}
		for _, name := range []string{"a.kt", "b.kt", "sub", trashPartly} {
			if !syncs.has(name) {
				t.Errorf("%s was not fsynced; ADR-0021 §Format needs every file and folder synced", name)
			}
		}
		if !syncs.has(id) || !syncs.has("v") {
			t.Errorf("the entry folder %s and the old parent v were not synced: %v", id, syncs.names)
		}
		if _, err := os.Lstat(filepath.Join(f.trashDir("ws"), id, trashPartly)); !os.IsNotExist(err) {
			t.Errorf("item.partial is still there: %v", err)
		}
	})

	t.Run("a file written to the original path after the aside rename survives", func(t *testing.T) {
		f := setup(t)
		trashSeam = func(stage string) error {
			if stage == stageCopied {
				return os.WriteFile(filepath.Join(f.root, "v/solo.kt"), []byte("NEW"), 0o644)
			}
			return nil
		}
		res, err := f.del("v/solo.kt")
		if err != nil || res.Done != 1 {
			t.Fatalf("delete: %+v %v", res, err)
		}
		if got := readFile(t, filepath.Join(f.root, "v/solo.kt")); got != "NEW" {
			t.Errorf("the path holds %q; the writer's file must survive the delete", got)
		}
		if got := readFile(t, f.itemPath(res.Items[0].TrashID)); got != "solo" {
			t.Errorf("the trash holds %q; want the deleted bytes", got)
		}
	})

	t.Run("a crash before item leaves the aside, and the next sweep puts the original back", func(t *testing.T) {
		f := setup(t)
		trashSeam = func(stage string) error {
			if stage == stageCopied {
				return syscall.EIO
			}
			return nil
		}
		res, err := f.del("v/tree")
		if err != nil || res.Failed != 1 || res.Items[0].Code != "FILE_IO_FAILED" {
			t.Fatalf("the cut delete: %+v %v; want the item FILE_IO_FAILED", res, err)
		}
		// What the dead process left: no item at the path, the original aside, a record
		// without an item.
		if _, err := os.Lstat(filepath.Join(f.root, "v/tree")); !os.IsNotExist(err) {
			t.Fatalf("v/tree is at its path in the crashed state: %v", err)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 1 {
			t.Fatalf("asides after the crash: %v; want the original waiting", got)
		}
		ids := f.trashIDs("ws")
		if len(ids) != 1 || exists(f.itemPath(ids[0])) {
			t.Fatalf("trash entries after the crash: %v; want one without item", ids)
		}
		trashSeam = nil
		noReplace = renameNoReplace
		f.tr.Sweep("ws", f.root)
		if got := readFile(t, filepath.Join(f.root, "v/tree/sub/b.kt")); got != "BBBBBB" {
			t.Errorf("after the sweep v/tree/sub/b.kt holds %q; want the original", got)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides after the sweep: %v", got)
		}
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries after the sweep: %v; want the abandoned one dropped", got)
		}
	})

	t.Run("a removal that fails leaves the item done and the aside to a later sweep", func(t *testing.T) {
		f := setup(t)
		removeAll = func(*os.Root, string) error { return syscall.EACCES }
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 || res.Items[0].Code != "" {
			t.Fatalf("delete with a failing removal: %+v %v; want the item done", res, err)
		}
		id := res.Items[0].TrashID
		if !exists(f.itemPath(id)) {
			t.Fatal("the item is not in the trash")
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 1 {
			t.Fatalf("asides: %v; want the one the removal left", got)
		}
		removeAll = func(root *os.Root, rel string) error { return root.RemoveAll(rel) }
		f.tr.Sweep("ws", f.root)
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides after the sweep: %v", got)
		}
		if !exists(f.itemPath(id)) || readFile(t, filepath.Join(f.itemPath(id), "a.kt")) != "AAAA" {
			t.Error("the sweep took the finished item with the aside")
		}
	})

	t.Run("the copy carries everything, droidyard temporary names included", func(t *testing.T) {
		f := setup(t)
		f.write("v/tree/.droidyard-0123456789abcdef.tmp", "TEMP")
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 {
			t.Fatalf("delete: %+v %v", res, err)
		}
		if got := readFile(t, filepath.Join(f.itemPath(res.Items[0].TrashID), ".droidyard-0123456789abcdef.tmp")); got != "TEMP" {
			t.Errorf("the trashed folder holds %q for the temporary-named file; the aside's removal would lose it", got)
		}
	})

	t.Run("a copy that fails puts the original back and leaves no entry", func(t *testing.T) {
		f := setup(t)
		before := tree(t, f.root)
		files := 0
		copySeam = func(stage, rel string) error {
			if stage == stageFile {
				if files++; files == 2 {
					return syscall.ENOSPC
				}
			}
			return nil
		}
		res, err := f.del("v/tree")
		if err != nil || res.Failed != 1 || res.Items[0].Code != "FILE_IO_FAILED" {
			t.Fatalf("delete: %+v %v; want FILE_IO_FAILED", res, err)
		}
		sameTree(t, "a delete whose copy failed", before, tree(t, f.root))
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v; want none", got)
		}
	})

	t.Run("a cancel inside the copy puts the original back", func(t *testing.T) {
		f := setup(t)
		before := tree(t, f.root)
		ctx, cancel := context.WithCancel(context.Background())
		copySeam = func(stage, rel string) error {
			if stage == stageFile {
				cancel()
			}
			return nil
		}
		res, err := f.delCtx(ctx, "ws", "v/tree", "v/solo.kt")
		if err != nil || res.Done != 0 || res.Items[0].Code != "OPERATION_CANCELLED" || res.Items[1].Code != "OPERATION_CANCELLED" {
			t.Fatalf("delete: %+v %v; want both items OPERATION_CANCELLED", res, err)
		}
		sameTree(t, "a cancelled cross-volume delete", before, tree(t, f.root))
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v; want none", got)
		}
	})

	t.Run("a file created between the walk and the aside rename is not lost", func(t *testing.T) {
		f := setup(t)
		done := false
		crossVolumeHook(func(from, to string) {
			if from == "tree" && tempWatchName(to) && !done {
				done = true
				if err := os.WriteFile(filepath.Join(f.root, "v/tree/new.kt"), []byte("NEW"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 {
			t.Fatalf("delete: %+v %v", res, err)
		}
		if got := readFile(t, filepath.Join(f.itemPath(res.Items[0].TrashID), "new.kt")); got != "NEW" {
			t.Errorf("the trashed folder holds %q for new.kt; what the folder held at the rename must be what is copied", got)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left: %v", got)
		}
	})

	t.Run("a writer between the copy and the removal keeps its file, and nothing is deleted", func(t *testing.T) {
		f := setup(t)
		trashSeam = func(stage string) error {
			if stage == stageStored {
				as := asides(t, filepath.Join(f.root, "v"))
				if len(as) != 1 {
					t.Fatalf("asides at the seam: %v", as)
				}
				return os.WriteFile(filepath.Join(f.root, "v", as[0], "late.kt"), []byte("LATE"), 0o644)
			}
			return nil
		}
		res, err := f.del("v/tree")
		if err != nil || res.Failed != 1 || res.Items[0].Code != "FILE_IO_FAILED" || !strings.Contains(res.Items[0].Message, "changed while being deleted") {
			t.Fatalf("delete: %+v %v; want the item FILE_IO_FAILED, changed while being deleted", res, err)
		}
		if got := readFile(t, filepath.Join(f.root, "v/tree/late.kt")); got != "LATE" {
			t.Errorf("late.kt holds %q at the original path; the writer's file is lost", got)
		}
		if got := readFile(t, filepath.Join(f.root, "v/tree/a.kt")); got != "AAAA" {
			t.Errorf("a.kt holds %q", got)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left: %v", got)
		}
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v; want the copy dropped", got)
		}
	})

	t.Run("a changed folder whose path was taken stays aside, and no sweep removes it", func(t *testing.T) {
		f := setup(t)
		trashSeam = func(stage string) error {
			if stage == stageStored {
				as := asides(t, filepath.Join(f.root, "v"))
				if err := os.WriteFile(filepath.Join(f.root, "v", as[0], "late.kt"), []byte("LATE"), 0o644); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(f.root, "v/tree"), 0o755) // the path is taken again
			}
			return nil
		}
		res, err := f.del("v/tree")
		if err != nil || res.Items[0].Code != "FILE_IO_FAILED" {
			t.Fatalf("delete: %+v %v", res, err)
		}
		trashSeam = nil
		f.tr.Sweep("ws", f.root)
		as := asides(t, filepath.Join(f.root, "v"))
		if len(as) != 1 || readFile(t, filepath.Join(f.root, "v", as[0], "late.kt")) != "LATE" {
			t.Fatalf("asides after the sweep: %v; the changed original must stay, its late file in it", as)
		}
		if err := os.Remove(filepath.Join(f.root, "v/tree")); err != nil {
			t.Fatal(err)
		}
		f.tr.Sweep("ws", f.root)
		if got := readFile(t, filepath.Join(f.root, "v/tree/late.kt")); got != "LATE" {
			t.Errorf("once the path was free the sweep put back %q; want the original with late.kt", got)
		}
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v", got)
		}
	})

	t.Run("a read-only folder in the original does not keep the aside for good", func(t *testing.T) {
		f := setup(t)
		ro := filepath.Join(f.root, "v/tree/sub")
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = filepath.WalkDir(f.root, func(p string, d os.DirEntry, err error) error { _ = os.Chmod(p, 0o700); return nil })
		})
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 {
			t.Fatalf("delete: %+v %v", res, err)
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left behind a read-only folder: %v", got)
		}
	})

	t.Run("an item that is not known to be on disk keeps the original until a sync succeeds", func(t *testing.T) {
		f := setup(t)
		failing := true
		fsync = func(fl *os.File) error {
			if failing && trashIDPattern.MatchString(filepath.Base(fl.Name())) {
				return syscall.EIO
			}
			return fl.Sync()
		}
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 {
			t.Fatalf("delete: %+v %v; the item is in the trash, so it is done", res, err)
		}
		id := res.Items[0].TrashID
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 1 {
			t.Fatalf("asides: %v; the original must stay while the copy is not known to be durable", got)
		}
		f.tr.Sweep("ws", f.root) // the entry still cannot be synced
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 1 {
			t.Fatalf("asides after a sweep whose sync failed: %v", got)
		}
		failing = false
		f.tr.Sweep("ws", f.root)
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides after a sweep whose sync worked: %v", got)
		}
		if !exists(f.itemPath(id)) {
			t.Error("the item is gone")
		}
	})

	t.Run("a socket or device is refused in a delete's own words", func(t *testing.T) {
		f := setup(t)
		if err := syscall.Mkfifo(filepath.Join(f.root, "v/tree/pipe"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := tree(t, f.root)
		res, err := f.del("v/tree")
		if err != nil || res.Items[0].Code != "FILE_IO_FAILED" {
			t.Fatalf("delete: %+v %v", res, err)
		}
		m := res.Items[0].Message
		if !strings.Contains(m, "a delete across volumes cannot take") || strings.Contains(m, "copy") {
			t.Errorf("message %q; want the delete's wording", m)
		}
		sameTree(t, "a refused delete", before, tree(t, f.root))
	})

	t.Run("restoring a delete that was cut puts the original back", func(t *testing.T) {
		f := setup(t)
		trashSeam = func(stage string) error {
			if stage == stageCopied {
				return syscall.EIO
			}
			return nil
		}
		if res, err := f.del("v/tree"); err != nil || res.Items[0].Code != "FILE_IO_FAILED" {
			t.Fatalf("the cut delete: %+v %v", res, err)
		}
		trashSeam = nil
		id := f.trashIDs("ws")[0]
		// The path taken again: the item is FILE_EXISTS, and everything stays.
		if err := os.Mkdir(filepath.Join(f.root, "v/tree"), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := f.restore(id)
		if err != nil || res.Items[0].Code != "FILE_EXISTS" {
			t.Fatalf("restore with the path taken: %+v %v; want FILE_EXISTS", res, err)
		}
		if err := os.Remove(filepath.Join(f.root, "v/tree")); err != nil {
			t.Fatal(err)
		}
		res, err = f.restore(id)
		if err != nil || res.Done != 1 || res.Items[0].Path != "v/tree" {
			t.Fatalf("restore of a cut delete: %+v %v; want it finished", res, err)
		}
		if got := readFile(t, filepath.Join(f.root, "v/tree/sub/b.kt")); got != "BBBBBB" {
			t.Errorf("b.kt holds %q", got)
		}
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v", got)
		}
	})
}

// Decision (ADR-0054 §1, §2): the record is durable before an item moves, so the folders
// that hold it are synced too, not only <id>; and when that cannot be done the item does
// not move.
func TestDeleteSyncsTheFoldersThatHoldTheRecord(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("s/a.kt", "A")
	fsync = func(fl *os.File) error {
		if strings.HasSuffix(filepath.ToSlash(fl.Name()), "/ws/ws/trash") {
			return syscall.EIO
		}
		return fl.Sync()
	}
	res, err := f.del("s/a.kt")
	if err != nil || res.Items[0].Code != "FILE_IO_FAILED" {
		t.Fatalf("delete: %+v %v; want FILE_IO_FAILED", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "s/a.kt")); got != "A" {
		t.Errorf("the file holds %q; nothing may move before the record is durable", got)
	}
	if got := f.trashIDs("ws"); len(got) != 0 {
		t.Errorf("trash entries: %v", got)
	}
}

// Decision (ADR-0054 §4): the trash's record of an item is dropped only once the restored
// item is known to be on disk; when the sync fails the entry stays and the call still
// reports the item back.
func TestRestoreKeepsTheEntryUntilTheItemIsSynced(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("rs/a.kt", "A")
	id := f.oneDeleted("rs/a.kt")
	fsync = func(fl *os.File) error {
		if filepath.Base(fl.Name()) == "rs" {
			return syscall.EIO
		}
		return fl.Sync()
	}
	res, err := f.restore(id)
	if err != nil || res.Done != 1 || readFile(t, filepath.Join(f.root, "rs/a.kt")) != "A" {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if !exists(filepath.Join(f.trashDir("ws"), id, entryFile)) {
		t.Error("the entry was dropped although the restored item was not known to be durable")
	}
}

// Decision (ADR-0054 §2): a finished cross-volume delete whose aside could not be removed
// leaves the old bytes in the project; a restore of that item takes them with it, and
// the entry is never removed (by age or size) while its aside is still there.
func TestAsideNeverOutlivesItsEntry(t *testing.T) {
	removeFails := func() { removeAll = func(*os.Root, string) error { return syscall.EACCES } }
	removeWorks := func() { removeAll = func(root *os.Root, rel string) error { return root.RemoveAll(rel) } }

	t.Run("restore removes the aside", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		f.write("v/tree/a.kt", "AAAA")
		crossVolume()
		removeFails()
		res, err := f.del("v/tree")
		if err != nil || res.Done != 1 {
			t.Fatal(res, err)
		}
		removeWorks()
		noReplace = renameNoReplace
		rr, rerr := f.restore(res.Items[0].TrashID)
		if rerr != nil || rr.Done != 1 {
			t.Fatalf("restore: %+v %v", rr, rerr)
		}
		f.tr.Sweep("ws", f.root)
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("after restore and sweep, an aside with the old bytes stays in the project: %v", got)
		}
	})

	t.Run("an entry is not evicted by age or size while its aside stays, and goes with it", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		now := t0
		trashNow = func() time.Time { return now }
		f.write("v/old/a.kt", "AAAA")
		f.write("v/new/b.kt", "BBBBBBBB")
		crossVolume()
		removeFails()
		old := f.oneDeleted("v/old")
		now = t0.Add(time.Hour)
		fresh := f.oneDeleted("v/new")
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 2 {
			t.Fatalf("asides: %v; want two", got)
		}

		// Age: the old entry is due, but its aside cannot be removed, so it stays.
		now = t0.Add(8 * 24 * time.Hour)
		f.tr.Sweep("ws", f.root)
		if !exists(f.itemPath(old)) {
			t.Fatal("the entry was evicted while its aside stayed in the project")
		}
		removeWorks()
		f.tr.Sweep("ws", f.root)
		if exists(f.itemPath(old)) || exists(f.itemPath(fresh)) {
			t.Errorf("after a sweep that can remove asides: old kept %v, fresh kept %v; want both gone by age", exists(f.itemPath(old)), exists(f.itemPath(fresh)))
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left by evicted entries: %v", got)
		}
	})

	t.Run("size eviction removes the aside too", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		now := t0
		trashNow = func() time.Time { return now }
		f.write("v/old/a.kt", strings.Repeat("a", 60))
		f.write("v/new/b.kt", strings.Repeat("b", 60))
		crossVolume()
		removeFails()
		old := f.oneDeleted("v/old")
		now = t0.Add(time.Hour)
		fresh := f.oneDeleted("v/new")
		removeWorks()
		noReplace = renameNoReplace
		f.tr.MaxBytes = 100
		f.tr.Sweep("ws", f.root)
		if exists(f.itemPath(old)) || !exists(f.itemPath(fresh)) {
			t.Errorf("old kept %v, fresh kept %v; want the oldest evicted and the newest kept", exists(f.itemPath(old)), exists(f.itemPath(fresh)))
		}
		if got := asides(t, filepath.Join(f.root, "v")); len(got) != 0 {
			t.Errorf("asides left: %v", got)
		}
	})
}

// Decision (ADR-0054 §2): an item.partial may be the only copy of a deleted item, so the
// sweep never drops an entry that holds one unless the original is put back.
func TestSweepKeepsAPartialCopyWithoutItsOriginal(t *testing.T) {
	f := newTrashFixture(t, nil)
	for i, e := range []trashEntry{
		{Path: "lone.kt"}, // no aside recorded
		{Path: "lone.kt", Aside: ".droidyard-0123456789abcdef.tmp"}, // recorded, but gone
	} {
		e.Type, e.DeletedAt = "file", time.Now()
		id := strings.Repeat(string(rune('a'+i)), 16)
		if err := f.st.Write("ws/ws/trash/"+id+"/entry.json", e); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.trashDir("ws"), id, trashPartly), []byte("ONLY COPY"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.tr.Sweep("ws", f.root)
	for _, id := range f.trashIDs("ws") {
		if got := readFile(t, filepath.Join(f.trashDir("ws"), id, trashPartly)); got != "ONLY COPY" {
			t.Errorf("entry %s lost its partial copy: %q", id, got)
		}
	}
	if got := f.trashIDs("ws"); len(got) != 2 {
		t.Errorf("entries %v; want both kept", got)
	}
}

// Decision (ADR-0054 §2, §3; plan m2g D48): a delete across volumes is bounded and the
// item stays when it is over; one on the same volume has no limit; the trash keeps seven
// days and 2 GiB, oldest first, never the newest. The caps are scaled here.
func TestTrashBounds(t *testing.T) {
	t.Run("across volumes an item over a cap is FILE_TOO_LARGE and untouched", func(t *testing.T) {
		f := newTrashFixture(t, func(o *Options) { o.CopyMaxEntries, o.CopyMaxBytes = 5, 100 })
		for _, n := range []string{"1", "2", "3", "4", "5", "6"} {
			f.write("big/"+n, "x")
		}
		f.write("fat.bin", strings.Repeat("x", 101))
		before := tree(t, f.root)
		crossVolume()
		res, err := f.del("big", "fat.bin")
		if err != nil || res.Failed != 2 || res.Items[0].Code != "FILE_TOO_LARGE" || res.Items[1].Code != "FILE_TOO_LARGE" {
			t.Fatalf("delete: %+v %v; want both FILE_TOO_LARGE", res, err)
		}
		sameTree(t, "deletes over the caps", before, tree(t, f.root))
		if got := f.trashIDs("ws"); len(got) != 0 {
			t.Errorf("trash entries: %v; want none", got)
		}
	})

	t.Run("on one volume an item over the caps is trashed, its figures cut and said so", func(t *testing.T) {
		f := newTrashFixture(t, func(o *Options) { o.CopyMaxEntries, o.CopyMaxBytes = 5, 100 })
		for _, n := range []string{"1", "2", "3", "4", "5", "6"} {
			f.write("big/"+n, "x")
		}
		id := f.oneDeleted("big")
		e, _ := f.entry(id)
		if e.Entries != 5 || !e.Truncated {
			t.Errorf("entry %+v; want the walk cut at 5 entries and marked truncated", e)
		}
		if readFile(t, filepath.Join(f.itemPath(id), "6")) != "x" {
			t.Error("the whole folder is not in the trash")
		}
	})

	t.Run("seven days, then the oldest over the size cap, never the newest", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		now := t0
		trashNow = func() time.Time { return now }
		f.write("b/a", strings.Repeat("a", 60))
		f.write("b/b", strings.Repeat("b", 60))
		f.write("b/c", strings.Repeat("c", 60))
		idA := f.oneDeleted("b/a")
		now = t0.Add(24 * time.Hour)
		idB := f.oneDeleted("b/b")
		now = t0.Add(48 * time.Hour)
		idC := f.oneDeleted("b/c")
		f.tr.MaxBytes = 100
		f.tr.Sweep("ws", f.root)
		if exists(f.itemPath(idA)) || exists(f.itemPath(idB)) || !exists(f.itemPath(idC)) {
			t.Errorf("over the size cap: a %v, b %v, c %v; want the two oldest gone and the newest kept",
				exists(f.itemPath(idA)), exists(f.itemPath(idB)), exists(f.itemPath(idC)))
		}
		if got := f.trashIDs("ws"); !reflect.DeepEqual(got, []string{idC}) {
			t.Errorf("entry folders %v; want only %s (the removed entries leave no folder)", got, idC)
		}

		// The newest entry alone, over the cap, stays.
		f.tr.MaxBytes = 10
		f.tr.Sweep("ws", f.root)
		if !exists(f.itemPath(idC)) {
			t.Error("the newest entry was swept for being over the cap alone")
		}

		// The age rule: eight days old goes, six days stays.
		f.tr.MaxBytes = 0
		f.write("b/d", "d")
		f.write("b/e", "e")
		now = t0.Add(10 * 24 * time.Hour)
		idD := f.oneDeleted("b/d")
		now = t0.Add(13 * 24 * time.Hour)
		idE := f.oneDeleted("b/e")
		now = t0.Add(18*24*time.Hour + time.Hour) // D is 8 days and an hour old, E 5 days
		f.tr.Sweep("ws", f.root)
		if exists(f.itemPath(idD)) || !exists(f.itemPath(idE)) {
			t.Errorf("age rule: d kept %v, e kept %v; want the 8-day entry removed and the 5-day one kept", exists(f.itemPath(idD)), exists(f.itemPath(idE)))
		}
		if exists(f.itemPath(idC)) {
			t.Error("the 16-day-old entry c was not removed by the age rule")
		}
	})

	t.Run("the delete itself sweeps before it trashes", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
		now := t0
		trashNow = func() time.Time { return now }
		f.write("s/old", "o")
		f.write("s/new", "n")
		old := f.oneDeleted("s/old")
		now = t0.Add(8 * 24 * time.Hour)
		fresh := f.oneDeleted("s/new")
		if exists(f.itemPath(old)) || !exists(f.itemPath(fresh)) {
			t.Errorf("after a delete 8 days later: old kept %v, new kept %v; want the old swept and the new kept", exists(f.itemPath(old)), exists(f.itemPath(fresh)))
		}
	})
}

// Decision (ADR-0054 §4): a restore puts the item back at its recorded path with its
// bytes, makes the parents that are gone, never replaces (the item fails FILE_EXISTS and
// stays in the trash), and a name this workspace's trash lacks is FILE_NOT_IN_TRASH.
func TestRestore(t *testing.T) {
	f := newTrashFixture(t, nil)

	f.write("r/a/b.kt", "BYTES")
	f.write("r/tree/x/y.kt", "Y")
	f.write("r/tree/z.kt", "Z")
	before := tree(t, filepath.Join(f.root, "r/tree"))
	fileID := f.oneDeleted("r/a/b.kt")
	treeID := f.oneDeleted("r/tree")
	if err := os.RemoveAll(filepath.Join(f.root, "r/a")); err != nil {
		t.Fatal(err)
	}
	res, err := f.restore(fileID, treeID)
	if err != nil || res.Done != 2 || res.Items[0].Path != "r/a/b.kt" || res.Items[1].Path != "r/tree" {
		t.Fatalf("restore: %+v %v", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "r/a/b.kt")); got != "BYTES" {
		t.Errorf("the restored file holds %q (its parent r/a was made again)", got)
	}
	sameTree(t, "a restored folder", before, tree(t, filepath.Join(f.root, "r/tree")))
	if got := f.trashIDs("ws"); len(got) != 0 {
		t.Errorf("trash entries after restoring both: %v", got)
	}

	// A taken name is the item's FILE_EXISTS; the item stays in the trash, the taker's file
	// keeps its bytes, and once the name is free the same id restores.
	f.write("r/e.kt", "OLD")
	id := f.oneDeleted("r/e.kt")
	f.write("r/e.kt", "NEW")
	res, err = f.restore(id)
	if err != nil || res.Failed != 1 || res.Items[0].Code != "FILE_EXISTS" {
		t.Fatalf("restore onto a taken name: %+v %v; want the item FILE_EXISTS", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "r/e.kt")); got != "NEW" {
		t.Errorf("the taker's file holds %q", got)
	}
	if !exists(f.itemPath(id)) {
		t.Error("the item left the trash although it was not restored")
	}
	if err := os.Remove(filepath.Join(f.root, "r/e.kt")); err != nil {
		t.Fatal(err)
	}
	res, err = f.restore(id)
	if err != nil || res.Done != 1 || readFile(t, filepath.Join(f.root, "r/e.kt")) != "OLD" {
		t.Fatalf("restore after the name was freed: %+v %v", res, err)
	}

	// A valid id the trash never held, and one whose delete never reached item.
	res, err = f.restore("0123456789abcdef")
	if err != nil || res.Items[0].Code != "FILE_NOT_IN_TRASH" {
		t.Errorf("an id never trashed: %+v %v; want FILE_NOT_IN_TRASH", res, err)
	}
	if err := f.st.Write("ws/ws/trash/fedcba9876543210/entry.json", trashEntry{Path: "half.kt", Type: "file", DeletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	res, err = f.restore("fedcba9876543210")
	if err != nil || res.Items[0].Code != "FILE_NOT_IN_TRASH" {
		t.Errorf("an entry without item: %+v %v; want FILE_NOT_IN_TRASH", res, err)
	}
}

// Decision (plan m2g D50): a trash id is 16 lowercase hex characters, checked before any
// path is built. A malformed id fails the whole call REQUEST_INVALID_PARAMS with nothing
// read or moved, a valid item beside it included; another workspace's real id is the
// item's FILE_NOT_IN_TRASH and its trash is untouched.
func TestRestoreRefusesBadIDs(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("p/real.kt", "REAL")
	realID := f.oneDeleted("p/real.kt")
	// A planted entry that a path built from "../planted" would reach: ws/ws/planted holds
	// a record and an item, and is not inside the trash folder.
	planted := filepath.Join(f.state, "ws", "ws", "planted")
	if err := os.MkdirAll(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Write("ws/ws/planted/entry.json", trashEntry{Path: "pwn.txt", Type: "file", DeletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "item"), []byte("PWN"), 0o600); err != nil {
		t.Fatal(err)
	}

	bad := []string{"..", "../planted", "../../ws/planted", "a/b", planted, "/" + realID, realID[:15], realID + "0", strings.ToUpper(realID), realID + "\n", ""}
	stateBefore, rootBefore := tree(t, f.state), tree(t, f.root)
	for _, id := range bad {
		for _, ids := range [][]string{{id}, {realID, id}} {
			res, err := f.restore(ids...)
			if err == nil || err.Code != capability.ErrInvalidParams {
				t.Errorf("restore %q: %+v %v; want the call REQUEST_INVALID_PARAMS", ids, res, err)
			}
		}
	}
	sameTree(t, "restores with a malformed id (state)", stateBefore, tree(t, f.state))
	sameTree(t, "restores with a malformed id (project)", rootBefore, tree(t, f.root))
	if exists(filepath.Join(f.root, "pwn.txt")) || exists(filepath.Join(f.root, "p/real.kt")) {
		t.Error("a malformed id restored something")
	}

	// Another workspace's real id.
	f.write("p/other.kt", "OTHER")
	res, err := f.delCtx(context.Background(), "other", "p/other.kt")
	if err != nil || res.Done != 1 {
		t.Fatalf("delete in the other workspace: %+v %v", res, err)
	}
	otherID := res.Items[0].TrashID
	stateBefore = tree(t, f.state)
	got, err := f.restore(otherID)
	if err != nil || got.Items[0].Code != "FILE_NOT_IN_TRASH" {
		t.Fatalf("restore of another workspace's id: %+v %v; want the item FILE_NOT_IN_TRASH", got, err)
	}
	sameTree(t, "a restore of another workspace's id", stateBefore, tree(t, f.state))
	if exists(filepath.Join(f.root, "p/other.kt")) {
		t.Error("another workspace's item was restored")
	}
	got, err = f.restoreIn("other", otherID)
	if err != nil || got.Done != 1 || readFile(t, filepath.Join(f.root, "p/other.kt")) != "OTHER" {
		t.Errorf("restore in its own workspace: %+v %v", got, err)
	}
}

// Decision (ADR-0053 §2, ADR-0054 §4): the recorded path is resolved again through the
// os.Root. A parent that is now a symlink leaving the root fails that item
// FILE_OUTSIDE_ROOT, the batch's other restores are done, the item stays in the trash,
// and nothing is written outside.
func TestRestoreThroughASymlinkedParent(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("sub/deep/f.kt", "F")
	f.write("ok.kt", "OK")
	leaving := f.oneDeleted("sub/deep/f.kt")
	fine := f.oneDeleted("ok.kt")
	if err := os.RemoveAll(filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.base, "outside")
	if err := os.Symlink(outside, filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	outsideBefore := tree(t, outside)
	res, err := f.restore(leaving, fine)
	if err != nil || res.Failed != 1 || res.Done != 1 || res.Items[0].Code != "FILE_OUTSIDE_ROOT" || res.Items[1].Code != "" {
		t.Fatalf("restore: %+v %v; want item 1 FILE_OUTSIDE_ROOT and item 2 done", res, err)
	}
	sameTree(t, "a restore through a leaving symlink", outsideBefore, tree(t, outside))
	if !exists(f.itemPath(leaving)) || readFile(t, filepath.Join(f.root, "ok.kt")) != "OK" {
		t.Error("the refused item left the trash, or the other restore was not done")
	}

	// The same with a link whose target is absolute and inside the root.
	if err := os.Remove(filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "src"), filepath.Join(f.root, "sub")); err != nil {
		t.Fatal(err)
	}
	res, err = f.restore(leaving)
	if err != nil || res.Items[0].Code != "FILE_OUTSIDE_ROOT" {
		t.Errorf("restore through an absolute-target link: %+v %v; want FILE_OUTSIDE_ROOT", res, err)
	}
}

// Decision (ADR-0054 §2): the sweep uses an entry's aside only when it is one
// `.droidyard-<16 hex>.tmp` component, and its path only when it is a clean path inside
// the root. An entry.json that says otherwise (hand-edited, or from another program) is
// ignored: nothing moved, the entry left where it is.
func TestSweepIgnoresAnAsideThatIsNotOurs(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("keep/placeholder", "p")
	f.write("victim.txt", "VICTIM")
	before := tree(t, f.root)
	forged := []trashEntry{
		{Path: "keep/f.kt", Aside: "../victim.txt"},
		{Path: "keep/f.kt", Aside: "sub/.droidyard-0123456789abcdef.tmp"},
		{Path: "keep/f.kt", Aside: ".droidyard-0123456789abcdef.tmp/../../victim.txt"},
		{Path: "keep/f.kt", Aside: ".droidyard-0123.tmp"},
		{Path: "keep/f.kt", Aside: "victim.txt"},
		{Path: "../victim.txt", Aside: ".droidyard-0123456789abcdef.tmp"},
		{Path: "/etc/hosts", Aside: ".droidyard-0123456789abcdef.tmp"},
	}
	for i, e := range forged {
		e.Type, e.DeletedAt = "file", time.Now()
		id := strings.Repeat(string(rune('a'+i)), 16)
		if err := f.st.Write("ws/ws/trash/"+id+"/entry.json", e); err != nil {
			t.Fatal(err)
		}
	}
	// A genuine aside for the sixth path, which a naive sweep would rename into place.
	if err := os.WriteFile(filepath.Join(f.root, ".droidyard-0123456789abcdef.tmp"), []byte("ASIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.tr.Sweep("ws", f.root)
	if got := readFile(t, filepath.Join(f.root, "victim.txt")); got != "VICTIM" {
		t.Errorf("victim.txt holds %q", got)
	}
	os.Remove(filepath.Join(f.root, ".droidyard-0123456789abcdef.tmp"))
	sameTree(t, "a sweep of forged entries", before, tree(t, f.root))
	if got := f.trashIDs("ws"); len(got) != len(forged) {
		t.Errorf("entry folders %v; want all %d forged entries left alone", got, len(forged))
	}
}

// Decision (ADR-0054 §4): the trash is a folder on disk, so a second Trash on the same
// state root, as a restarted daemon has, restores what the first deleted.
func TestTrashSurvivesRestart(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("keep/me.kt", "ME")
	id := f.oneDeleted("keep/me.kt")

	st2, err := store.New(f.state)
	if err != nil {
		t.Fatal(err)
	}
	reg := capability.NewRegistry()
	Register(reg, Options{Root: func(string) (string, bool) { return f.root, true }, Env: f.env, Trash: NewTrash(st2)})
	d, _ := reg.Lookup("file.restore")
	res, cerr := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: RestoreParams{TrashIDs: []string{id}}})
	if cerr != nil || res.(RestoreResult).Done != 1 {
		t.Fatalf("restore through a second Trash: %+v %v", res, cerr)
	}
	if got := readFile(t, filepath.Join(f.root, "keep/me.kt")); got != "ME" {
		t.Errorf("the file holds %q", got)
	}
}

// Decision (ADR-0053 §2, §3; step 2's list): file.delete refuses what leaves the root:
// the text of the call decides (a path outside, the root itself) and fails the whole call
// with nothing changed; a symlink that leaves the root fails its item and the batch's
// valid item is done. A symlink is trashed as the link and restored as the link.
func TestDeleteRefusesEscapes(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("ok/a.kt", "A")
	f.write("ok/b.kt", "B")
	before := tree(t, f.root)
	outsideBefore := tree(t, filepath.Join(f.base, "outside"))

	for _, p := range []string{"..", "../x", filepath.Join(f.base, "outside", "secret"), "", ".", f.root, f.root + "/"} {
		res, err := f.del("ok/a.kt", p)
		if err == nil {
			t.Errorf("delete of %q: %+v; want the call to fail", p, res)
			continue
		}
		if err.Code != capability.ErrFileOutsideRoot && err.Code != capability.ErrInvalidParams {
			t.Errorf("delete of %q: %s; want FILE_OUTSIDE_ROOT or REQUEST_INVALID_PARAMS", p, err.Code)
		}
	}
	sameTree(t, "refused deletes", before, tree(t, f.root))
	if got := f.trashIDs("ws"); len(got) != 0 {
		t.Errorf("trash entries after refused calls: %v", got)
	}

	// Through a symlink that leaves the root (a relative or an absolute target) the item
	// fails FILE_OUTSIDE_ROOT and the valid item beside it is done.
	for i, p := range []string{"dir/secret", "esc/hosts"} {
		valid := []string{"ok/a.kt", "ok/b.kt"}[i]
		res, err := f.del(p, valid)
		if err != nil || res.Items[0].Code != "FILE_OUTSIDE_ROOT" || res.Done != 1 || res.Items[1].TrashID == "" {
			t.Errorf("delete of %q beside %s: %+v %v; want that item FILE_OUTSIDE_ROOT and the valid one done", p, valid, res, err)
		}
	}
	sameTree(t, "deletes through leaving symlinks (outside)", outsideBefore, tree(t, filepath.Join(f.base, "outside")))

	// A symlink goes as the link: its target is untouched, the trash holds a link, a
	// restore brings the link back.
	srcBefore := readFile(t, filepath.Join(f.root, "src/A.kt"))
	id := f.oneDeleted("in")
	if _, err := os.Lstat(filepath.Join(f.root, "in")); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	if got := readFile(t, filepath.Join(f.root, "src/A.kt")); got != srcBefore {
		t.Error("the link's target changed")
	}
	if target, err := os.Readlink(f.itemPath(id)); err != nil || target != "src" {
		t.Errorf("the trashed item is %q, %v; want the link to src", target, err)
	}
	if e, _ := f.entry(id); e.Type != "symlink" {
		t.Errorf("entry type %q; want symlink", e.Type)
	}
	if res, err := f.restore(id); err != nil || res.Done != 1 {
		t.Fatalf("restore of the link: %+v %v", res, err)
	}
	if target, err := os.Readlink(filepath.Join(f.root, "in")); err != nil || target != "src" {
		t.Errorf("the restored path is %q, %v; want the link to src", target, err)
	}
}

// Decision (ADR-0053 §2, §4): a delete is a batch whose items report each: a missing one
// is FILE_NOT_FOUND beside the done ones, the text leads with the failure, a cancel after
// the first item completes it with the rest OPERATION_CANCELLED, and a cancel before the
// first ends it cancelled. A daemon built without a trash deletes nothing.
func TestDeleteItemsReportEach(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("m/a.kt", "a")
	f.write("m/c.kt", "c")
	res, err := f.del("m/a.kt", "m/missing.kt", "m/c.kt")
	if err != nil || res.Done != 2 || res.Failed != 1 || res.Items[1].Code != "FILE_NOT_FOUND" ||
		res.Items[0].TrashID == "" || res.Items[2].TrashID == "" || res.Items[0].TrashID == res.Items[2].TrashID {
		t.Fatalf("delete: %+v %v; want 1 and 3 done with distinct ids and 2 FILE_NOT_FOUND", res, err)
	}
	d, _ := f.reg.Lookup("file.delete")
	text := d.Text(res)
	if !strings.HasPrefix(text, "failed 1 of 3 FILE_NOT_FOUND\nFILE_NOT_FOUND m/missing.kt ") || !strings.Contains(text, "trashed m/a.kt -> "+res.Items[0].TrashID) {
		t.Errorf("the rendering is %q; want the failure first, then the done items with their ids", text)
	}
	rd, _ := f.reg.Lookup("file.restore")
	rres, rerr := f.restore(res.Items[0].TrashID, "0123456789abcdef")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rt := rd.Text(rres); !strings.HasPrefix(rt, "failed 1 of 2 FILE_NOT_IN_TRASH\nFILE_NOT_IN_TRASH 0123456789abcdef ") || !strings.Contains(rt, "restored "+res.Items[0].TrashID+" -> m/a.kt") {
		t.Errorf("the restore rendering is %q", rt)
	}

	t.Run("cancel", func(t *testing.T) {
		f := newTrashFixture(t, nil)
		for _, n := range []string{"c1", "c2", "c3"} {
			f.write("k/"+n, n)
		}
		ctx, cancel := context.WithCancel(context.Background())
		itemSeam = func(i int) {
			if i == 1 {
				cancel()
			}
		}
		res, err := f.delCtx(ctx, "ws", "k/c1", "k/c2", "k/c3")
		if err != nil || res.Done != 1 || res.Items[0].TrashID == "" || res.Items[1].Code != "OPERATION_CANCELLED" || res.Items[2].Code != "OPERATION_CANCELLED" {
			t.Fatalf("cancel after the first: %+v %v; want it completed with items 2 and 3 OPERATION_CANCELLED", res, err)
		}
		if !exists(filepath.Join(f.root, "k/c2")) {
			t.Error("a cancelled item was deleted")
		}
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		itemSeam = nil
		if _, err := f.delCtx(ctx, "ws", "k/c2"); err == nil || err.Code != capability.ErrOperationCancelled {
			t.Errorf("cancel before the first: %v; want OPERATION_CANCELLED for the call", err)
		}
		if !exists(filepath.Join(f.root, "k/c2")) {
			t.Error("an item was deleted by a cancelled call")
		}
	})

	t.Run("no trash", func(t *testing.T) {
		g := newFixture(t) // Options.Trash is nil, as in the golden registry
		g.write("n/a.kt", "a")
		for _, call := range []struct {
			name   string
			params any
		}{
			{"file.delete", DeleteParams{Paths: []string{"n/a.kt"}}},
			{"file.restore", RestoreParams{TrashIDs: []string{"0123456789abcdef"}}},
		} {
			if _, err := g.call(call.name, call.params); err == nil || err.Code != capability.ErrFileIOFailed {
				t.Errorf("%s without a trash: %v; want FILE_IO_FAILED", call.name, err)
			}
		}
		if !exists(filepath.Join(g.root, "n/a.kt")) {
			t.Error("a delete without a trash removed the file")
		}
	})
}

// Decision (plan m2g D50): two restores of one id at once restore it once: the
// workspace's lock serialises them and the second finds nothing, FILE_NOT_IN_TRASH.
func TestRestoreTwiceAtOnce(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("race/a.kt", "A")
	id := f.oneDeleted("race/a.kt")
	var wg sync.WaitGroup
	results := make([]RestoreResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.callWS(context.Background(), "ws", "file.restore", RestoreParams{TrashIDs: []string{id}})
			if err == nil {
				results[i] = res.(RestoreResult)
			}
		}()
	}
	wg.Wait()
	var done, missing []int
	for i, r := range results {
		switch {
		case len(r.Items) == 1 && r.Items[0].Code == "":
			done = append(done, i)
		case len(r.Items) == 1 && r.Items[0].Code == "FILE_NOT_IN_TRASH":
			missing = append(missing, i)
		}
	}
	if len(done) != 1 || len(missing) != len(results)-1 {
		t.Errorf("done %v, FILE_NOT_IN_TRASH %v; want exactly one restore done", done, missing)
	}
	sort.Ints(done)
	if got := readFile(t, filepath.Join(f.root, "race/a.kt")); got != "A" {
		t.Errorf("the file holds %q", got)
	}
}

// Decision (ADR-0054 §2, §4): a restore across volumes copies the item with every file
// and folder synced and drops the trash entry only afterwards; a taken name is still the
// item's FILE_EXISTS, with nothing left behind and the item still in the trash.
func TestRestoreAcrossVolumes(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("x/tree/a.kt", "AAA")
	f.write("x/tree/sub/b.kt", "BB")
	before := tree(t, filepath.Join(f.root, "x/tree"))
	id := f.oneDeleted("x/tree")

	crossVolume()
	syncs := recordSyncs()
	res, err := f.restore(id)
	if err != nil || res.Done != 1 {
		t.Fatalf("restore across volumes: %+v %v", res, err)
	}
	sameTree(t, "a restore across volumes", before, tree(t, filepath.Join(f.root, "x/tree")))
	for _, name := range []string{"a.kt", "b.kt", "sub"} {
		if !syncs.has(name) {
			t.Errorf("%s was not fsynced on the way back", name)
		}
	}
	if got := f.trashIDs("ws"); len(got) != 0 {
		t.Errorf("trash entries after the restore: %v", got)
	}
	if got := asides(t, filepath.Join(f.root, "x")); len(got) != 0 {
		t.Errorf("temporaries left: %v", got)
	}

	// Onto a taken name.
	id = f.oneDeleted("x/tree")
	f.write("x/tree/mine.kt", "MINE")
	res, err = f.restore(id)
	if err != nil || res.Items[0].Code != "FILE_EXISTS" {
		t.Fatalf("restore across volumes onto a taken name: %+v %v; want FILE_EXISTS", res, err)
	}
	if readFile(t, filepath.Join(f.root, "x/tree/mine.kt")) != "MINE" || !exists(f.itemPath(id)) {
		t.Error("the taker's file changed, or the item left the trash")
	}
	if got := asides(t, filepath.Join(f.root, "x")); len(got) != 0 {
		t.Errorf("temporaries left after the refusal: %v", got)
	}
}

// Decision (plan m2g step 4, ADR-0021 §Format): only the trash's copy is synced.
// file.copy keeps its plain copy and calls no fsync, so a big copy is not slowed.
func TestCopyDoesNotFsync(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("cp/tree/a.kt", "A")
	syncs := recordSyncs()
	res, err := f.copy([]CopyItem{cp("cp/tree", "cp/tree2")}, "")
	if err != nil || res.Done != 1 {
		t.Fatalf("copy: %+v %v", res, err)
	}
	if len(syncs.names) != 0 {
		t.Errorf("file.copy fsynced %v; only the trash's copy is durable", syncs.names)
	}
}

// Decision (ADR-0053 §1, ADR-0054): file.delete is for every audience, the one
// destructive file command (the trash forgets), idempotent (a repeat is FILE_NOT_FOUND
// items) and long-running; file.restore never replaces. The trash id's schema pattern is
// the pattern the handler checks.
func TestTrashDescriptors(t *testing.T) {
	f := newFixture(t)
	del, _ := f.reg.Lookup("file.delete")
	rst, _ := f.reg.Lookup("file.restore")
	da, ra := del.ResolveAnnotations(), rst.ResolveAnnotations()
	if da.ReadOnly || !da.Destructive || !da.Idempotent || ra.ReadOnly || ra.Destructive || !ra.Idempotent {
		t.Errorf("annotations: delete %+v, restore %+v; want delete destructive and restore not, both idempotent", da, ra)
	}
	for _, d := range []capability.Descriptor{del, rst} {
		if d.Audience != capability.AudienceAll || d.Untrusted() || d.Resource != nil || d.Since != "0.19.0" || !d.LongRunning {
			t.Errorf("%s: audience %v, untrusted %v, resource %v, since %s, long-running %v", d.Name, d.Audience, d.Untrusted(), d.Resource != nil, d.Since, d.LongRunning)
		}
	}
	if strings.Contains(del.Description, "never for good") || !strings.Contains(del.Description, "7 days") || !strings.Contains(del.Description, "2 GiB per project") {
		t.Errorf("file.delete says %q; it must give the retention (7 days, 2 GiB per project), not claim the item is never gone", del.Description)
	}
	tag := reflect.TypeOf(RestoreParams{}).Field(0).Tag.Get("jsonschema")
	if !strings.Contains(tag, "pattern="+trashIDPattern.String()) {
		t.Errorf("RestoreParams.TrashIDs tag %q does not carry the pattern %s the handler checks", tag, trashIDPattern)
	}
}

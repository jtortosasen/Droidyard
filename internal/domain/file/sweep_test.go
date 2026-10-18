package file

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/event"
)

// The watch's sweep of leftover temporary files and the aside's own name (ADR-0091):
// real directories and links, the age read with os.Chtimes or the sweepNow seam. Each test
// says which decision it holds; the safety ones were each seen failing with their guard
// removed (plan m2r step 8).

const (
	sweepA = ".droidyard-0123456789abcdef.tmp"
	sweepB = ".droidyard-fedcba9876543210.tmp"
	sweepC = ".droidyard-00112233445566ff.tmp"
	sweepD = ".droidyard-aside-0123456789abcdef.tmp"
)

// newSweepFixture is the trash fixture with the daemon's bus and poller set, so a watch
// and the trash share one state root.
func newSweepFixture(t *testing.T) *trashFix {
	t.Helper()
	bus := event.New(event.Options{})
	ws := NewWatchers()
	f := newTrashFixture(t, func(o *Options) { o.Bus = bus; o.Watchers = ws })
	f.bus, f.watchers = bus, ws
	f.sess = event.NewSession(bus, "ses-sweep")
	return f
}

// sweepWatch starts a manual watch of rel (its baseline walk is made here) that knows the
// fixture's trash, as the daemon's watchers do.
func (f *trashFix) sweepWatch(rel string) (*watcher, *event.Subscription) {
	f.t.Helper()
	w, sub, _ := f.startManualWatch(rel)
	w.trash = f.tr
	return w, sub
}

// sweepClock makes the sweep read the time as now plus d, until the test ends.
func sweepClock(t *testing.T, d time.Duration) {
	t.Helper()
	sweepNow = func() time.Time { return time.Now().Add(d) }
	t.Cleanup(func() { sweepNow = time.Now })
}

// age sets the modification time of rel (relative to the root) to d ago.
func (f *trashFix) age(rel string, d time.Duration) {
	f.t.Helper()
	at := time.Now().Add(-d)
	if err := os.Chtimes(filepath.Join(f.root, filepath.FromSlash(rel)), at, at); err != nil {
		f.t.Fatal(err)
	}
}

func (f *trashFix) present(rel string) bool {
	return exists(filepath.Join(f.root, filepath.FromSlash(rel)))
}

// record writes a trash entry of workspace ws that records aside as its aside.
func (f *trashFix) record(ws, id, path, aside string) {
	f.t.Helper()
	e := trashEntry{Path: path, Type: "file", DeletedAt: time.Now(), Aside: aside}
	if err := f.st.Write("ws/"+ws+"/trash/"+id+"/entry.json", e); err != nil {
		f.t.Fatal(err)
	}
}

// Decision (ADR-0091 §2): a watch tick removes a file.write or file.copy temporary file or
// folder, with its contents, once it is more than a day old, and never on the baseline
// walk; a younger one, and a day-old file of any other name, stay. The sweep is silent.
func TestWatchSweepsOldTemporaryFiles(t *testing.T) {
	f := newSweepFixture(t)
	f.write("x/"+sweepA, "half written")
	f.write("x/"+sweepB+"/inner/f.kt", "half copied")
	f.write("x/.fresh.droidyard-01234567.tmp", "other shape")
	young := "x/" + sweepC
	f.write(young, "being written")
	f.write("x/old.txt", "an old file of mine")
	f.age("x/"+sweepA, 25*time.Hour)
	f.age("x/"+sweepB, 25*time.Hour)
	f.age(young, time.Hour)
	f.age("x/old.txt", 30*24*time.Hour)

	w, sub := f.sweepWatch("x") // the baseline walk happens here
	for _, rel := range []string{"x/" + sweepA, "x/" + sweepB + "/inner/f.kt"} {
		if !f.present(rel) {
			t.Fatalf("%s was removed by the baseline walk; only a tick sweeps", rel)
		}
	}

	w.tickOnce()
	for _, rel := range []string{"x/" + sweepA, "x/" + sweepB} {
		if f.present(rel) {
			t.Errorf("%s, 25 hours old, is still there after a tick", rel)
		}
	}
	for _, rel := range []string{young, "x/old.txt", "x/.fresh.droidyard-01234567.tmp"} {
		if !f.present(rel) {
			t.Errorf("%s was removed; only a name of the exact shape, older than a day, is", rel)
		}
	}
	if got := drainEvents(sub); len(got) != 0 {
		t.Errorf("the sweep published %d events; it is silent", len(got))
	}
}

// Decision (ADR-0091 §3): the watch never sweeps an aside, whatever its age: a new-style
// aside (.droidyard-aside-) does not match the sweep's exact pattern, and an old-named one
// that an entry of this state root records, in this workspace's trash or another's, is
// skipped; an old-named file no entry records is swept.
func TestWatchNeverSweepsAnAside(t *testing.T) {
	f := newSweepFixture(t)
	f.write("x/"+sweepD, "NEW-STYLE ASIDE")
	f.write("x/"+sweepA, "OLD-NAMED, RECORDED BY THIS WORKSPACE")
	f.write("x/"+sweepB, "OLD-NAMED, RECORDED BY ANOTHER WORKSPACE")
	f.write("x/"+sweepC, "OLD-NAMED, RECORDED BY NOBODY")
	f.record("ws", "1111111111111111", "x/a.kt", sweepA)
	f.record("dev", "2222222222222222", "x/b.kt", sweepB)
	sweepClock(t, 30*24*time.Hour)

	w, _ := f.sweepWatch("x")
	w.tickOnce()

	if !f.present("x/" + sweepD) {
		t.Error("a new-style aside was swept; the watch never removes one")
	}
	if !f.present("x/" + sweepA) {
		t.Error("an old-named aside recorded by this workspace's trash was swept")
	}
	if !f.present("x/" + sweepB) {
		t.Error("an old-named aside recorded by another workspace's trash was swept")
	}
	if f.present("x/" + sweepC) {
		t.Error("an old-named file no entry records stays; it is a leftover and is swept")
	}
}

// Decision (ADR-0091 §1, §3): a delete across volumes names its aside .droidyard-aside-…,
// the entry records that name, and a watch that finds it 30 days later leaves it for the
// trash's Sweep.
func TestCrossVolumeDeleteUsesTheAsideName(t *testing.T) {
	f := newSweepFixture(t)
	f.write("v/tree/a.kt", "AAAA")
	crossVolume()
	trashSeam = func(stage string) error {
		if stage == stageStored {
			return errors.New("the process died here")
		}
		return nil
	}
	if res, err := f.del("v/tree"); err == nil && res.Done == 1 {
		t.Fatal("the delete went through; the seam should have stopped it with the aside in place")
	}
	got := asides(t, filepath.Join(f.root, "v"))
	if len(got) != 1 || !strings.HasPrefix(got[0], ".droidyard-aside-") || !asidePattern.MatchString(got[0]) {
		t.Fatalf("the aside is %v; want one .droidyard-aside-<16 hex>.tmp", got)
	}
	ids := f.trashIDs("ws")
	if len(ids) != 1 {
		t.Fatalf("trash entries: %v", ids)
	}
	if e, _ := f.entry(ids[0]); e.Aside != got[0] {
		t.Errorf("the entry records aside %q; the project holds %q", e.Aside, got[0])
	}

	trashSeam = nil
	sweepClock(t, 30*24*time.Hour)
	w, _ := f.sweepWatch("v")
	w.tickOnce()
	if !f.present("v/" + got[0] + "/a.kt") {
		t.Error("the watch removed a 30-day-old aside")
	}
}

// Decision (ADR-0091 §1): restore and the trash's Sweep accept an aside named the old way,
// .droidyard-<16 hex>.tmp, so an entry an older binary wrote still resolves.
func TestRestoreResolvesAnOldNamedAside(t *testing.T) {
	f := newSweepFixture(t)
	// A delete cut before it was whole, written by an older binary: no item, the original
	// waits under the old aside name.
	f.write("o/"+sweepA, "ORIGINAL ONE")
	f.record("ws", "aaaaaaaaaaaaaaaa", "o/one.kt", sweepA)
	f.write("o/"+sweepB, "ORIGINAL TWO")
	f.record("ws", "bbbbbbbbbbbbbbbb", "o/two.kt", sweepB)

	res, err := f.restore("aaaaaaaaaaaaaaaa")
	if err != nil || res.Done != 1 {
		t.Fatalf("restoring an old-named aside: %+v %v", res, err)
	}
	if got := readFile(t, filepath.Join(f.root, "o/one.kt")); got != "ORIGINAL ONE" {
		t.Errorf("o/one.kt holds %q", got)
	}

	f.tr.Sweep("ws", f.root)
	if got := readFile(t, filepath.Join(f.root, "o/two.kt")); got != "ORIGINAL TWO" {
		t.Errorf("after the sweep o/two.kt holds %q; want the old-named aside put back", got)
	}
	if ids := f.trashIDs("ws"); len(ids) != 0 {
		t.Errorf("entries left: %v", ids)
	}
}

// Decision (ADR-0091 §2): the sweep goes through the watcher's os.Root and removes a link
// as a link. An old symlink named like a temporary file, pointing at a directory outside
// the project, is removed; the directory and its file are intact.
func TestWatchSweepRemovesALinkNotItsTarget(t *testing.T) {
	f := newSweepFixture(t)
	f.mkdir("x")
	f.symlink("../../outside", "x/"+sweepA)
	sweepClock(t, 30*24*time.Hour)

	w, _ := f.sweepWatch("x")
	w.tickOnce()

	if _, err := os.Lstat(filepath.Join(f.root, "x", sweepA)); err == nil {
		t.Error("the old link is still there")
	}
	if got := readFile(t, filepath.Join(f.base, "outside", "secret")); got != "SECRET-CONTENT" {
		t.Errorf("the link's target holds %q; want it untouched", got)
	}
}

// Decision (ADR-0091 §3): when any trash folder or entry cannot be read or parsed, the
// tick sweeps nothing, and a later tick, with the trash readable again, sweeps. A bad
// record is read, never moved aside.
func TestWatchSkipsTheSweepWhenTheTrashCannotBeRead(t *testing.T) {
	cases := []struct {
		name string
		// break makes the trash unreadable in one way; fix undoes it.
		brk func(f *trashFix) (fix func())
	}{
		{"an entry that does not parse", func(f *trashFix) func() {
			p := filepath.Join(f.state, "ws", "dev", "trash", "3333333333333333", "entry.json")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{ not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() { os.Remove(p) }
		}},
		{"an entry that cannot be read", func(f *trashFix) func() {
			f.record("dev", "3333333333333333", "x/z.kt", sweepB)
			p := filepath.Join(f.state, "ws", "dev", "trash", "3333333333333333", "entry.json")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			return func() { os.Chmod(p, 0o600) }
		}},
		{"a trash folder that cannot be read", func(f *trashFix) func() {
			f.record("dev", "3333333333333333", "x/z.kt", sweepB)
			p := filepath.Join(f.state, "ws", "dev", "trash")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			return func() { os.Chmod(p, 0o700) }
		}},
		{"an entry from a newer binary", func(f *trashFix) func() {
			p := filepath.Join(f.state, "ws", "dev", "trash", "3333333333333333", "entry.json")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(`{"schemaVersion":99,"aside":"`+sweepB+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() { os.Remove(p) }
		}},
		{"a folder with a quarantined record", func(f *trashFix) func() {
			p := filepath.Join(f.state, "ws", "dev", "trash", "3333333333333333", "entry.json.corrupt-20261018T000000.000000000Z")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{ not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() { os.Remove(p) }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSweepFixture(t)
			f.write("x/"+sweepA, "AN OLD LEFTOVER, OR THE ONLY COPY")
			sweepClock(t, 30*24*time.Hour)
			fix := c.brk(f)
			t.Cleanup(fix)
			w, sub := f.sweepWatch("x")

			w.tickOnce()
			if !f.present("x/" + sweepA) {
				t.Fatal("a tick swept while a trash record could not be read")
			}
			if got := drainEvents(sub); len(got) != 0 {
				t.Errorf("the skipped sweep published %d events", len(got))
			}
			if ents, _ := filepath.Glob(filepath.Join(f.state, "ws", "dev", "trash", "*", "entry.json.corrupt-*")); strings.Contains(c.name, "parse") && len(ents) != 0 {
				t.Errorf("reading the trash moved a record aside: %v", ents)
			}

			fix()
			w.tickOnce()
			if f.present("x/" + sweepA) {
				t.Error("the next tick, with the trash readable, did not sweep")
			}
		})
	}

	t.Run("a watch with no trash", func(t *testing.T) {
		f := newSweepFixture(t)
		f.write("x/"+sweepA, "AN OLD LEFTOVER")
		sweepClock(t, 30*24*time.Hour)
		w, _ := f.sweepWatch("x")
		w.trash = nil
		w.tickOnce()
		if !f.present("x/" + sweepA) {
			t.Error("a watch that cannot read the trash swept")
		}
	})
}

// Decision (ADR-0091 §2, step 8 fix round): the sweep claims a candidate by renaming it to
// a fresh temporary name before removing it, so a writer that renames its in-flight folder
// into place during the removal (file.copy, file.restore) finds the old name gone
// (ENOENT) instead of committing a folder that is being emptied. The seam stands for the
// writer acting between the claim and the removal.
func TestWatchSweepClaimsBeforeRemoving(t *testing.T) {
	f := newSweepFixture(t)
	f.write("x/"+sweepA+"/inner/f.kt", "THE COPY'S CONTENT")
	sweepClock(t, 30*24*time.Hour)
	w, _ := f.sweepWatch("x")

	var writerErr error
	removeAll = func(root *os.Root, rel string) error {
		parent, err := os.Open(filepath.Join(f.root, "x"))
		if err != nil {
			t.Fatal(err)
		}
		defer parent.Close()
		writerErr = noReplace(parent, sweepA, parent, "final")
		return root.RemoveAll(rel)
	}
	w.tickOnce()

	if writerErr == nil {
		t.Error("the writer's rename of the old name committed while the sweep was removing it; the claim must make it fail")
	} else if !errors.Is(writerErr, os.ErrNotExist) {
		t.Errorf("the writer's rename failed with %v; want ENOENT", writerErr)
	}
	if f.present("x/final") && !f.present("x/final/inner/f.kt") {
		t.Error("the committed folder was emptied")
	}
	if left := asides(t, filepath.Join(f.root, "x")); len(left) != 0 {
		t.Errorf("the claimed leftover was not removed: %v", left)
	}
}

// Decision (step 8 fix round): a claim that fails, for any reason, skips the entry for
// this tick; it is neither removed under its old name nor lost, and a later tick sweeps it.
func TestWatchSweepSkipsWhenTheClaimFails(t *testing.T) {
	f := newSweepFixture(t)
	f.write("x/"+sweepA+"/f.kt", "LEFTOVER")
	sweepClock(t, 30*24*time.Hour)
	w, _ := f.sweepWatch("x")

	noReplace = func(*os.File, string, *os.File, string) error { return syscall.EEXIST }
	w.tickOnce()
	if !f.present("x/" + sweepA + "/f.kt") {
		t.Fatal("the entry was removed although the claim failed")
	}

	noReplace = renameNoReplace
	w.tickOnce()
	if f.present("x/" + sweepA) {
		t.Error("the next tick, with the claim working, did not sweep")
	}
}

// Decision (step 8 fix round): the claim's rename does not change the entry's own
// modification time, so a leftover whose removal stops part-way keeps its age under the
// new name. (Removing children from a folder does set its mtime, which is not the rename.)
func TestWatchSweepClaimKeepsTheAge(t *testing.T) {
	f := newSweepFixture(t)
	f.write("x/"+sweepA+"/f.kt", "LEFTOVER")
	f.age("x/"+sweepA, 30*24*time.Hour)
	before, err := os.Lstat(filepath.Join(f.root, "x", sweepA))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(filepath.Join(f.root, "x"))
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := noReplace(parent, sweepA, parent, sweepB); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(filepath.Join(f.root, "x", sweepB))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the rename changed the folder's mtime from %v to %v", before.ModTime(), after.ModTime())
	}
}

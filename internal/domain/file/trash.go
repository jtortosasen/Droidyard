package file

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/store"
)

// The trash (ADR-0054): a deleted item waits at <state>/ws/<wsid>/trash/<id>/item with
// its record beside it, entry.json, written first. This file is the trash itself: its
// layout, its locks and its sweep. file.delete and file.restore are delete.go and
// restore.go.

// The trash's retention (ADR-0054 §3; plan m2g D48): entries are kept seven days and, per
// workspace, at most 2 GiB, the oldest removed first and the newest never.
const (
	defaultTrashMaxAge   = 7 * 24 * time.Hour
	defaultTrashMaxBytes = int64(2) << 30
)

// Names inside one trash entry's folder.
const (
	entryFile   = "entry.json"
	trashItem   = "item"
	trashPartly = "item.partial" // the copy of a delete across volumes, until it is whole
)

var (
	// trashIDPattern is a trash id: 16 lowercase hex characters. It is checked before any
	// path is built from one (plan m2g D50).
	trashIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// asidePattern is the one name an entry may record as its aside: a droidyard
	// temporary name, a single component. The sweep uses a recorded name only if it
	// matches, so a hand-edited entry.json cannot make it touch anything else. A new
	// aside is .droidyard-aside-<16 hex>.tmp; the older .droidyard-<16 hex>.tmp is still
	// accepted, for entries an older binary wrote (ADR-0091 §1).
	asidePattern = regexp.MustCompile(`^\.droidyard-(aside-)?[0-9a-f]{16}\.tmp$`)
)

// Test seams. Production leaves them as they are.
var (
	// trashNow is the clock of an entry's time and of the sweep's age rule.
	trashNow = time.Now
	// fsync is the kernel's fsync of a file or folder the trash or a copy for it writes. A
	// test replaces it to see which ones were synced.
	fsync = func(f *os.File) error { return f.Sync() }
	// removeAll removes an item (or an aside) through the os.Root: the call whose failure
	// a delete across volumes must survive.
	removeAll = func(root *os.Root, rel string) error { return root.RemoveAll(rel) }
	// trashSeam runs at the points of a delete across volumes named below. A non-nil
	// error stands for the process dying there: nothing after it runs, not even the
	// cleanup, so the next sweep finds exactly what a crash would have left.
	trashSeam func(stage string) error
)

// trashSeam's stages.
const (
	stageCopied = "copied" // the copy is whole in item.partial; the rename to item is next
	stageStored = "stored" // item exists and is synced; the aside is still to be checked and removed
)

// trashEntry is entry.json: what was deleted, from where, when, how big, and, while a
// delete across volumes runs, where the original waits (schemaVersion 1, written by the
// store). Truncated says Bytes and Entries stopped at the walk's cap.
type trashEntry struct {
	Path      string    `json:"path"`
	Type      string    `json:"type"`
	DeletedAt time.Time `json:"deletedAt"`
	Bytes     int64     `json:"bytes"`
	Entries   int       `json:"entries"`
	Truncated bool      `json:"truncated,omitempty"`
	Aside     string    `json:"aside,omitempty"`
	// By is who deleted (ADR-0096 §2); nil for an entry written before it existed.
	By *trashBy `json:"by,omitempty"`
}

// Trash is the state directory's trash for every workspace (ADR-0054). It is safe for
// concurrent use: one mutex per workspace serialises its deletes, restores and sweeps, so
// a sweep never takes a delete in flight for a crashed one.
type Trash struct {
	st *store.Store
	// MaxAge and MaxBytes are the retention limits; <= 0 keeps the defaults (7 days,
	// 2 GiB). A test scales them down.
	MaxAge   time.Duration
	MaxBytes int64

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewTrash returns the trash under st's state root.
func NewTrash(st *store.Store) *Trash {
	return &Trash{st: st, locks: map[string]*sync.Mutex{}}
}

func (t *Trash) maxAge() time.Duration {
	if t.MaxAge > 0 {
		return t.MaxAge
	}
	return defaultTrashMaxAge
}

func (t *Trash) maxBytes() int64 {
	if t.MaxBytes > 0 {
		return t.MaxBytes
	}
	return defaultTrashMaxBytes
}

// lock takes the workspace's mutex and returns its release.
func (t *Trash) lock(wsid string) func() {
	t.mu.Lock()
	m := t.locks[wsid]
	if m == nil {
		m = &sync.Mutex{}
		t.locks[wsid] = m
	}
	t.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// validWorkspaceID reports whether wsid can be one component of the trash's path.
func validWorkspaceID(wsid string) bool {
	return wsid != "" && wsid != "." && filepath.IsLocal(wsid) && !strings.ContainsAny(wsid, `/\`)
}

// dir is the workspace's trash folder; entryDir one entry's folder; entryName the name of
// its record as the store spells it (relative to the state root, slash-separated).
func (t *Trash) dir(wsid string) string { return filepath.Join(t.st.Root(), "ws", wsid, "trash") }
func (t *Trash) entryDir(wsid, id string) string {
	return filepath.Join(t.dir(wsid), id)
}
func entryName(wsid, id string) string { return path.Join("ws", wsid, "trash", id, entryFile) }

// newTrashID is a fresh id: 16 lowercase hex characters.
func newTrashID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// asideName is the name a delete across volumes gives the item it renames aside:
// .droidyard-aside-<16 hex>.tmp (ADR-0091 §1), so no temporary file of file.write or
// file.copy, or an aside of another state root's trash, can be taken for it or it for them.
func asideName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return ".droidyard-aside-" + hex.EncodeToString(b[:]) + ".tmp"
}

// RecordedAsides is the set of aside names the entries of every workspace's trash under
// this state root record (ADR-0091 §3), for the watch's sweep to leave them alone. Any
// trash folder or entry that cannot be read or parsed is an error, and the caller then
// sweeps nothing: an unreadable record might name the only copy of a deleted item.
//
// It reads the entry files directly, without the trash locks: a cross-volume delete
// holds its workspace's lock through the whole copy, and an entry is replaced atomically
// and names its aside before the item is renamed aside (delete.go). It does not use the
// store's Read either, which moves a file that does not parse out of the way.
func (t *Trash) RecordedAsides() (map[string]bool, error) {
	if t == nil || t.st == nil {
		return nil, errors.New("no trash")
	}
	out := map[string]bool{}
	wsDir := filepath.Join(t.st.Root(), "ws")
	workspaces, err := os.ReadDir(wsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		if !ws.IsDir() {
			continue
		}
		trashDir := filepath.Join(wsDir, ws.Name(), "trash")
		ids, err := os.ReadDir(trashDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !id.IsDir() {
				continue
			}
			if err := readRecordedAside(filepath.Join(trashDir, id.Name()), out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// readRecordedAside adds the aside one entry folder's record names, if any, to out. A
// folder with no entry.json is a half-made entry (nothing was moved: the record is
// written first) only when it holds nothing but the store's own temporary file; anything
// else there (a quarantined record, an item) leaves the question open, so it is an error.
func readRecordedAside(entryDir string, out map[string]bool) error {
	b, err := os.ReadFile(filepath.Join(entryDir, entryFile))
	if errors.Is(err, fs.ErrNotExist) {
		des, derr := os.ReadDir(entryDir)
		if derr != nil {
			return derr
		}
		for _, de := range des {
			if de.Name() != entryFile+".tmp" {
				return fmt.Errorf("%s holds %s but no %s", entryDir, de.Name(), entryFile)
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	var e struct {
		SchemaVersion int    `json:"schemaVersion"`
		Aside         string `json:"aside"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return fmt.Errorf("%s: %w", filepath.Join(entryDir, entryFile), err)
	}
	if e.SchemaVersion < 1 || e.SchemaVersion > store.Version {
		return fmt.Errorf("%s: schemaVersion %d is not one this binary reads", filepath.Join(entryDir, entryFile), e.SchemaVersion)
	}
	if e.Aside != "" {
		out[e.Aside] = true
	}
	return nil
}

// recordedPath reports whether p is a path the trash may have recorded: a clean,
// slash-separated name inside a root, never the root itself.
func recordedPath(p string) bool {
	return p != "" && p != "." && filepath.IsLocal(p) && path.Clean(p) == p && !strings.Contains(p, `\`)
}

// exists reports whether name exists (a symlink counts as itself).
func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

// isXdev reports whether err is the kernel's answer for a rename across volumes.
func isXdev(err error) bool { return errors.Is(err, syscall.EXDEV) }

// syncFile fsyncs a file or folder descriptor; the error is the caller's to read.
func syncFile(f *os.File) error { return fsync(f) }

// removeTree removes a folder and everything in it, also when it holds folders the owner
// cannot write (a read-only folder moved into the trash would otherwise stay for good).
func removeTree(p string) error {
	if err := os.RemoveAll(p); err == nil {
		return nil
	}
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(q, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}

// syncDirPath fsyncs the folder at p.
func syncDirPath(p string) error {
	d, err := os.Open(p)
	if err != nil {
		return err
	}
	err = fsync(d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncParents syncs the folders that hold an entry's folder: trash/ and ws/<wsid>/. The
// store syncs <id> itself, but a crash can still lose <id>'s own name in trash/.
func (t *Trash) syncParents(wsid string) error {
	for _, p := range []string{t.dir(wsid), filepath.Dir(t.dir(wsid))} {
		if err := syncDirPath(p); err != nil {
			return err
		}
	}
	return nil
}

// removeAside removes an aside (a tree in the project) through the os.Root. A folder the
// owner cannot write would make the removal fail for good, so on failure the folders get
// owner permissions, through the root too, and it is tried once more.
func removeAside(root *os.Root, rel string) error {
	err := removeAll(root, rel)
	if err == nil {
		return nil
	}
	chmodTree(root, rel)
	return removeAll(root, rel)
}

// chmodTree gives every folder under rel (and rel itself) owner read, write and search,
// without following a symlink or leaving the root.
func chmodTree(root *os.Root, rel string) {
	fi, err := root.Lstat(rel)
	if err != nil || !fi.IsDir() {
		return
	}
	_ = root.Chmod(rel, 0o700)
	d, err := root.Open(rel)
	if err != nil {
		return
	}
	des, _ := d.ReadDir(-1)
	d.Close()
	for _, de := range des {
		if de.IsDir() {
			chmodTree(root, path.Join(rel, de.Name()))
		}
	}
}

// Sweep is the trash's housekeeping for one workspace, run before each delete and when
// the workspace opens (ADR-0054 §3), given the workspace's canonical root. It
//   - finishes a delete that never reached item: the aside, if still there, is renamed
//     back to the recorded path and the entry dropped (if it cannot be, because the path is
//     taken again, the aside stays and so does the entry, for a later sweep); an item.partial
//     with no aside to put back may be the only copy, and its entry is left alone;
//   - removes the aside a finished delete could not remove, once the entry is synced;
//   - removes entries older than the age limit, then the oldest while the workspace holds
//     more than the size limit, never the newest; an entry whose aside is still there is
//     only removed once the aside is.
//
// An entry whose recorded path or aside is not the shape this package writes is left
// alone. Failures are skipped: the next sweep tries again.
func (t *Trash) Sweep(wsid, root string) {
	if t == nil || t.st == nil || !validWorkspaceID(wsid) {
		return
	}
	defer t.lock(wsid)()

	des, err := os.ReadDir(t.dir(wsid))
	if err != nil {
		return
	}
	var rt *os.Root
	defer func() {
		if rt != nil {
			rt.Close()
		}
	}()
	openRoot := func() *os.Root {
		if rt == nil {
			rt, _ = os.OpenRoot(root)
		}
		return rt
	}

	type kept struct {
		id string
		e  trashEntry
	}
	var entries []kept
	for _, de := range des {
		id := de.Name()
		if !de.IsDir() || !trashIDPattern.MatchString(id) {
			continue
		}
		var e trashEntry
		out, err := t.st.Read(entryName(wsid, id), store.Version, &e)
		if err != nil || out != store.Loaded {
			// No record and no item: nothing was ever moved (the record is written first),
			// so the folder is a half-made entry. Anything else is left alone.
			if err == nil && out == store.Absent && !exists(filepath.Join(t.entryDir(wsid, id), trashItem)) &&
				!exists(filepath.Join(t.entryDir(wsid, id), trashPartly)) {
				_ = removeTree(t.entryDir(wsid, id))
			}
			continue
		}
		if !recordedPath(e.Path) || (e.Aside != "" && !asidePattern.MatchString(e.Aside)) {
			continue
		}
		if !exists(filepath.Join(t.entryDir(wsid, id), trashItem)) {
			if t.finishAbandoned(openRoot(), wsid, id, e) {
				_ = removeTree(t.entryDir(wsid, id))
			}
			continue
		}
		if e.Aside != "" {
			if t.removeLeftover(openRoot(), wsid, id, e) {
				e.Aside = ""
			}
		}
		entries = append(entries, kept{id, e})
	}

	// evict removes an entry, after its aside if one is still there; false means it stays.
	evict := func(k kept) bool {
		if k.e.Aside != "" && !t.removeLeftover(openRoot(), wsid, k.id, k.e) {
			return false
		}
		return removeTree(t.entryDir(wsid, k.id)) == nil
	}

	// The age rule, then the size rule over what is left.
	now := trashNow()
	live := entries[:0]
	for _, k := range entries {
		if now.Sub(k.e.DeletedAt) > t.maxAge() && evict(k) {
			continue
		}
		live = append(live, k)
	}
	sort.Slice(live, func(i, j int) bool {
		if !live[i].e.DeletedAt.Equal(live[j].e.DeletedAt) {
			return live[i].e.DeletedAt.Before(live[j].e.DeletedAt)
		}
		return live[i].id < live[j].id
	})
	var total int64
	for _, k := range live {
		total += k.e.Bytes
	}
	// Oldest first; the last, the newest, is never removed.
	for i := 0; i < len(live)-1 && total > t.maxBytes(); i++ {
		if evict(live[i]) {
			total -= live[i].e.Bytes
		}
	}
}

// removeLeftover removes the aside of a finished delete (its item exists), if it is still
// there, and rewrites the record without it. The entry's folder is synced first: the
// aside is the original, and goes only when the copy is known to be on disk. It reports
// whether no aside remains.
func (t *Trash) removeLeftover(root *os.Root, wsid, id string, e trashEntry) bool {
	if root == nil {
		return false
	}
	dirRel, _ := splitDir(e.Path)
	asideRel := path.Join(dirRel, e.Aside)
	if _, err := root.Lstat(asideRel); err == nil {
		if syncDirPath(t.entryDir(wsid, id)) != nil || removeAside(root, asideRel) != nil {
			return false
		}
		_ = syncDir(root, dirRel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	e.Aside = ""
	_ = t.st.Write(entryName(wsid, id), e)
	return true
}

// putAsideBack renames an entry's aside back to its recorded path, never replacing: a nil
// error means the original is back; os.ErrExist that the path is taken again.
func (r *resolver) putAsideBack(root *os.Root, e trashEntry) error {
	dirRel, base := splitDir(e.Path)
	parent, perr := r.openParent(root, dirRel)
	if perr != nil {
		return errors.New(perr.Message)
	}
	defer parent.Close()
	return noReplace(parent, e.Aside, parent, base)
}

// finishAbandoned deals with an entry that has no item: a delete that crashed or was cut
// before it was whole. If its original still waits aside, it goes back to the recorded
// path. It reports whether the entry can be dropped: yes when the aside was put back, or
// nothing waits aside and no item.partial exists (a partial copy might be the only copy
// left, and is never dropped without the original back); no when the aside could not be
// put back (the path is taken, or the root will not open), so the entry keeps the record
// of where the data is.
func (t *Trash) finishAbandoned(root *os.Root, wsid, id string, e trashEntry) bool {
	partial := exists(filepath.Join(t.entryDir(wsid, id), trashPartly))
	if e.Aside == "" {
		return !partial // the item never left its path
	}
	if root == nil {
		return false
	}
	dirRel, _ := splitDir(e.Path)
	if _, err := root.Lstat(path.Join(dirRel, e.Aside)); err != nil {
		return errors.Is(err, fs.ErrNotExist) && !partial
	}
	r := &resolver{root: root.Name()}
	return r.putAsideBack(root, e) == nil
}

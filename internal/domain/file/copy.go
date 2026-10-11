package file

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Copy limits for one item (plan m2g D48). A tree over either is FILE_TOO_LARGE and
// nothing is written. Options overrides them for a test.
const (
	maxCopyEntries       = 20000
	maxCopyBytes   int64 = 1 << 30
)

// CopyItem is one copy of file.copy's parameters.
type CopyItem struct {
	From string `json:"from" jsonschema:"required" jsonschema_description:"The existing file or folder, relative to the project root."`
	To   string `json:"to" jsonschema:"required" jsonschema_description:"The copy's path, relative to the project root; its parent folder must exist. The source's own path duplicates it."`
}

// CopyParams is file.copy's parameters.
type CopyParams struct {
	Items       []CopyItem `json:"items" jsonschema:"required,minItems=1,maxItems=1000" jsonschema_description:"The copies, made in order; at most 1000."`
	OnCollision string     `json:"onCollision,omitempty" jsonschema:"enum=fail,enum=keepBoth" jsonschema_description:"When a target exists: fail the item (the default), or take a free name such as \"b copy.kt\"."`
}

// CopiedItem is one item of file.copy's result.
type CopiedItem struct {
	From string `json:"from" jsonschema:"required" jsonschema_description:"The source as asked, relative to the project root."`
	To   string `json:"to" jsonschema:"required" jsonschema_description:"Where the copy went (the free name under keepBoth); for a failed item, the target as asked."`
	Code string `json:"code,omitempty" jsonschema:"enum=FILE_EXISTS,enum=FILE_NOT_FOUND,enum=FILE_WRONG_TYPE,enum=FILE_OUTSIDE_ROOT,enum=FILE_IO_FAILED,enum=OPERATION_CANCELLED,enum=FILE_TOO_LARGE" jsonschema_description:"Present when the item failed: why. Absent for a done item."`
	// Message is the human-readable reason, with the OS error after a colon when there is one.
	Message string `json:"message,omitempty" jsonschema_description:"Present when the item failed: what happened, with the OS error when there is one."`
}

// CopyResult is file.copy's result: one entry per requested item, in order.
type CopyResult struct {
	Items  []CopiedItem `json:"items" jsonschema:"required" jsonschema_description:"Every requested item, in order, the failed ones beside the done ones."`
	Done   int          `json:"done" jsonschema:"required" jsonschema_description:"How many items were copied."`
	Failed int          `json:"failed" jsonschema:"required" jsonschema_description:"How many items failed or were cancelled."`
}

// copySeam, when non-nil, runs at three points of an item, with the stage and the path
// concerned, and a non-nil error stands for the filesystem's own answer at that point.
// Production leaves it nil. A test uses it to fail the nth file's write (stageFile), to
// swap a source for a FIFO after the walk (stageWalked), to let another writer take the
// target before the rename (stageRename), and to cancel the operation mid-copy (plan m2g
// step 4).
var copySeam func(stage, rel string) error

// copySeam's stages.
const (
	stageWalked = "walked" // the source is walked; nothing is written yet
	stageFile   = "file"   // a regular file's copy is open for writing, before its content
	stageRename = "rename" // the copy is complete, before the no-replace rename
)

// checkCopies runs the checks that need only the text of the call, on every item, before
// anything changes (ADR-0053 §2): move's, except that a source may equal its target: that
// is a duplicate (plan m2g D52; keepBoth gives it "name copy.ext"), and without keepBoth
// the item is FILE_EXISTS.
func (r *resolver) checkCopies(items []CopyItem) ([]resolvedMove, *capability.Error) {
	out := make([]resolvedMove, len(items))
	seen := make(map[string]int, len(items))
	for i, it := range items {
		from, err := r.opPath(it.From, itemWhere(i, "from"))
		if err != nil {
			return nil, err
		}
		to, err := r.opPath(it.To, itemWhere(i, "to"))
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(to, from+"/") {
			return nil, capability.Errorf(capability.ErrInvalidParams, "items[%d]: %q cannot be copied inside itself (%q)", i, from, to).
				WithNextStep("copy it beside, not inside, its own folder")
		}
		if j, dup := seen[from]; dup {
			return nil, capability.Errorf(capability.ErrInvalidParams, "items[%d]: %q is already the source of items[%d]", i, from, j).
				WithNextStep("name each source once")
		}
		seen[from] = i
		out[i] = resolvedMove{from: from, to: to}
	}
	return out, nil
}

// copy serves file.copy (ADR-0053): each item a bounded walk of its source, then a copy
// of that snapshot into a temporary beside the target, renamed into place without
// replacing anything.
func (r *resolver) copy(ctx context.Context, p CopyParams) (CopyResult, error) {
	if e := checkBatch(len(p.Items)); e != nil {
		return CopyResult{}, e
	}
	keepBoth, e := checkCollision(p.OnCollision)
	if e != nil {
		return CopyResult{}, e
	}
	copies, e := r.checkCopies(p.Items)
	if e != nil {
		return CopyResult{}, e
	}

	final := make([]string, len(copies))
	var errs []*capability.Error
	if root, cerr := r.openRoot(); cerr != nil {
		errs = failAll(len(copies), cerr)
	} else {
		defer root.Close()
		var err error
		errs, err = runBatch(ctx, len(copies), func(i int) *capability.Error {
			to, ferr := r.copyOne(ctx, root, copies[i], keepBoth)
			final[i] = to
			return ferr
		})
		if err != nil {
			return CopyResult{}, err
		}
	}

	res := CopyResult{Items: make([]CopiedItem, len(copies))}
	for i, c := range copies {
		it := CopiedItem{From: c.from, To: c.to}
		if errs[i] != nil {
			it.Code, it.Message = failure(errs[i])
			res.Failed++
		} else {
			it.To = final[i]
			res.Done++
		}
		res.Items[i] = it
	}
	return res, nil
}

// copyText is file.copy's rendering (batchText).
func copyText(r CopyResult) string {
	lines := make([]batchLine, len(r.Items))
	for i, it := range r.Items {
		lines[i] = batchLine{words: []string{it.From, "->", it.To}, code: it.Code, message: it.Message}
	}
	return batchText("copied", lines)
}

// freeCopyName is the nth candidate name of a copy that asked for keepBoth (plan m2g
// D49): "name copy.ext", then "name copy 2.ext", "name copy 3.ext"… The extension is
// freeName's.
func freeCopyName(name string, isDir bool, n int) string {
	stem, ext := name, ""
	if !isDir {
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			stem, ext = name[:i], name[i:]
		}
	}
	if n <= 1 {
		return stem + " copy" + ext
	}
	return stem + " copy " + strconv.Itoa(n) + ext
}

// snapEntry is one entry of a copy's snapshot: its name under the source item ("" for the
// item itself) and its kind, which is its Lstat's, so a symlink is a symlink.
type snapEntry struct {
	rel  string
	kind fs.FileMode // fs.ModeDir, fs.ModeSymlink, or 0 for a regular file
	perm fs.FileMode // a folder's permission bits at the walk
	size int64       // a regular file's size at the walk
}

// copyOne makes one copy and returns the path it ended at: the target, or, for keepBoth,
// the free name it was given. The copy is complete or absent: it is built under a
// temporary name in the target's folder and appears only through the final no-replace
// rename, and every failure or cancel removes the temporary. Nothing here is fsynced: a
// crash can leave the finished copy short, like cp, where file.write's state files cannot.
func (r *resolver) copyOne(ctx context.Context, root *os.Root, m resolvedMove, keepBoth bool) (string, *capability.Error) {
	toPath, toBase := splitDir(m.to)
	toDir, e := r.openParent(root, toPath)
	if e != nil {
		return "", e
	}
	defer toDir.Close()

	if !keepBoth {
		// Only to spare the walk and the copy when the answer is already known; the rename
		// below is the authority, and a name taken after this look is still refused there.
		if _, err := root.Lstat(m.to); err == nil {
			return "", existsErr(m.to)
		}
	}
	snap, e := r.walkSource(ctx, root, m.from)
	if e != nil {
		return "", e
	}
	if copySeam != nil {
		if err := copySeam(stageWalked, m.from); err != nil {
			return "", r.ioFailed(m.from, err)
		}
	}

	tmpBase := tempName()
	tmpRel := path.Join(toPath, tmpBase)
	committed := false
	defer func() {
		if !committed {
			root.RemoveAll(tmpRel)
		}
	}()
	if e := r.copyTree(ctx, root, snap, m.from, tmpRel); e != nil {
		return "", e
	}
	if ctx.Err() != nil {
		return "", copyCancelled()
	}

	var err error
	if copySeam != nil {
		err = copySeam(stageRename, m.to)
	}
	if err == nil {
		err = noReplace(toDir, tmpBase, toDir, toBase)
	}
	if err == nil {
		committed = true
		return m.to, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return "", r.copyError(m, err)
	}
	if !keepBoth {
		return "", existsErr(m.to)
	}
	// keepBoth: D49's names in turn. Each try is the same no-replace rename, so a free
	// name is still taken only if it is free at the moment of the rename.
	isDir := snap[0].kind == fs.ModeDir
	for n := 1; n <= maxFreeNames; n++ {
		name := freeCopyName(toBase, isDir, n)
		err := noReplace(toDir, tmpBase, toDir, name)
		if err == nil {
			committed = true
			return path.Join(toPath, name), nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", r.copyError(m, err)
		}
	}
	return "", existsErr(m.to)
}

// copyCancelled is the item error of a copy stopped by a cancel (ADR-0053 §4).
func copyCancelled() *capability.Error {
	return capability.NewError(capability.ErrOperationCancelled, "the operation was cancelled while this item was being copied")
}

// tooLarge is FILE_TOO_LARGE: the source has more than one item may carry (D48).
func (r *resolver) tooLarge(from, what string) *capability.Error {
	return capability.Errorf(capability.ErrFileTooLarge, "%q has more than %s, the most one item copies", from, what).
		WithDetails(map[string]any{"path": from, "maxEntries": r.copyEntries(), "maxBytes": r.copyBytes()}).
		WithNextStep("copy it in smaller parts")
}

func (r *resolver) copyEntries() int {
	if r.maxCopyEntries > 0 {
		return r.maxCopyEntries
	}
	return maxCopyEntries
}

func (r *resolver) copyBytes() int64 {
	if r.maxCopyBytes > 0 {
		return r.maxCopyBytes
	}
	return maxCopyBytes
}

// walkSource takes the snapshot of the source item through the os.Root, parents before
// children, names sorted, without following a symlink or entering one. It stops at the
// first entry past the limits (FILE_TOO_LARGE) and at an entry that is neither a regular
// file, a folder nor a symlink (FILE_WRONG_TYPE), so nothing has been written when either
// is found. A cancel inside it is OPERATION_CANCELLED. Another operation's temporary
// names are not part of a tree and are skipped.
func (r *resolver) walkSource(ctx context.Context, root *os.Root, from string) ([]snapEntry, *capability.Error) {
	fi, err := root.Lstat(from)
	if err != nil {
		return nil, r.classifyOp(from, err)
	}
	var snap []snapEntry
	var bytes int64
	add := func(rel string, fi fs.FileInfo) *capability.Error {
		p := path.Join(from, rel)
		var kind fs.FileMode
		switch {
		case fi.IsDir():
			kind = fs.ModeDir
		case fi.Mode()&fs.ModeSymlink != 0:
			kind = fs.ModeSymlink
		case fi.Mode().IsRegular():
			bytes += fi.Size()
		default:
			return capability.Errorf(capability.ErrFileWrongType, "%q is a %s, which a copy cannot take", p, typeName(fi.Mode())).
				WithDetails(map[string]any{"path": p, "type": typeName(fi.Mode())}).
				WithNextStep("leave it out of the copy")
		}
		if len(snap) >= r.copyEntries() {
			return r.tooLarge(from, strconv.Itoa(r.copyEntries())+" entries")
		}
		if bytes > r.copyBytes() {
			return r.tooLarge(from, strconv.FormatInt(r.copyBytes(), 10)+" bytes")
		}
		snap = append(snap, snapEntry{rel: rel, kind: kind, perm: fi.Mode().Perm(), size: fi.Size()})
		return nil
	}
	if e := add("", fi); e != nil {
		return nil, e
	}
	var descend func(rel string) *capability.Error
	descend = func(rel string) *capability.Error {
		if ctx.Err() != nil {
			return copyCancelled()
		}
		dir := path.Join(from, rel)
		d, err := root.Open(dir)
		if err != nil {
			return r.classifyOp(dir, err)
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return r.classifyOp(dir, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, de := range entries {
			if tempWatchName(de.Name()) {
				continue
			}
			info, err := de.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // gone since the listing: not part of the snapshot
			}
			if err != nil {
				return r.classifyOp(path.Join(dir, de.Name()), err)
			}
			child := path.Join(rel, de.Name())
			if e := add(child, info); e != nil {
				return e
			}
			if info.IsDir() {
				if e := descend(child); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if fi.IsDir() {
		if e := descend(""); e != nil {
			return nil, e
		}
	}
	return snap, nil
}

// copyTree writes the snapshot of from at dst (the temporary), all through the os.Root:
// a folder made owner-writable so it can be filled and removed, a symlink re-created with
// its target text and never followed, a regular file by copyFile. The files' bytes are
// held to the item's byte cap as they are read, not to the sizes the walk saw: a file that
// grew since the walk is copied whole while the item stays under the cap, and the item
// fails FILE_TOO_LARGE past it, so a copy is never cut short and reported done.
func (r *resolver) copyTree(ctx context.Context, root *os.Root, snap []snapEntry, from, dst string) *capability.Error {
	budget := r.copyBytes()
	for _, ent := range snap {
		if ctx.Err() != nil {
			return copyCancelled()
		}
		src := path.Join(from, ent.rel)
		out := path.Join(dst, ent.rel)
		switch ent.kind {
		case fs.ModeDir:
			if err := root.Mkdir(out, ent.perm|0o700); err != nil {
				return r.classifyWrite(out, err)
			}
		case fs.ModeSymlink:
			target, err := root.Readlink(src)
			if err != nil {
				return r.classifyOp(src, err)
			}
			if err := root.Symlink(target, out); err != nil {
				return r.classifyWrite(out, err)
			}
		default:
			n, cerr := r.copyFile(ctx, root, src, out, budget)
			if cerr != nil {
				return cerr
			}
			if n > budget {
				return r.tooLarge(from, strconv.FormatInt(r.copyBytes(), 10)+" bytes")
			}
			budget -= n
		}
	}
	return nil
}

// ctxReader fails a Read once ctx is done, so a cancel reaches inside a large file.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// copyFile copies one regular file. The source is opened by openRegular (a stat, then a
// non-blocking open and an fstat of the descriptor), so a FIFO swapped in after the walk
// is refused and cannot block. The copy is created exclusively with owner-only
// permissions and given the source's permission bits through its own descriptor, before
// any content is written, so nothing is readable under a wider mode than the source's;
// Root.Chmod, which has a check-then-act race, is not used. Setuid, setgid and sticky are
// not copied. It reads at most budget+1 bytes and returns how many it copied, so a caller
// sees a file over the budget (n > budget) without reading the rest of it.
func (r *resolver) copyFile(ctx context.Context, root *os.Root, src, dst string, budget int64) (int64, *capability.Error) {
	in, fi, cerr := r.openRegular(root, src)
	if cerr != nil {
		return 0, cerr
	}
	defer in.Close()
	out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, r.classifyWrite(dst, err)
	}
	defer out.Close()
	if err := out.Chmod(fi.Mode().Perm()); err != nil {
		return 0, r.classifyWrite(dst, err)
	}
	if copySeam != nil {
		if err := copySeam(stageFile, src); err != nil {
			return 0, r.classifyWrite(dst, err)
		}
	}
	n, err := io.Copy(out, ctxReader{ctx, io.LimitReader(in, budget+1)})
	if err != nil {
		if ctx.Err() != nil {
			return 0, copyCancelled()
		}
		return 0, r.classifyWrite(dst, err)
	}
	if err := out.Close(); err != nil {
		return 0, r.classifyWrite(dst, err)
	}
	return n, nil
}

// copyError is the item's error for a final rename that failed for a reason other than a
// taken target.
func (r *resolver) copyError(m resolvedMove, err error) *capability.Error {
	switch {
	case unsupported(err):
		return capability.Errorf(capability.ErrFileIOFailed, "%q was not copied: this volume cannot refuse a replacing rename, and nothing is replaced", m.from).
			WithCause(err.Error()).
			WithDetails(map[string]any{"path": m.from})
	case isEscape(err):
		return r.outside(m.to)
	default:
		return capability.Errorf(capability.ErrFileIOFailed, "copying %q to %q failed", m.from, m.to).
			WithCause(err.Error()).
			WithDetails(map[string]any{"path": m.from})
	}
}

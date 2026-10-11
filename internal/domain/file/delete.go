package file

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// DeleteParams is file.delete's parameters.
type DeleteParams struct {
	Paths []string `json:"paths" jsonschema:"required,minItems=1,maxItems=1000" jsonschema_description:"The files or folders to delete, relative to the project root, in order; at most 1000. A symlink goes as the link."`
}

// DeletedItem is one item of file.delete's result.
type DeletedItem struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"The path as asked."`
	TrashID string `json:"trashId,omitempty" jsonschema_description:"Present when the item was deleted: its id in the trash, for file.restore."`
	Code    string `json:"code,omitempty" jsonschema:"enum=FILE_NOT_FOUND,enum=FILE_OUTSIDE_ROOT,enum=FILE_TOO_LARGE,enum=FILE_IO_FAILED,enum=OPERATION_CANCELLED" jsonschema_description:"Present when the item failed: why. Absent for a done item."`
	// Message is the human-readable reason, with the OS error after a colon when there is one.
	Message string `json:"message,omitempty" jsonschema_description:"Present when the item failed: what happened, with the OS error when there is one."`
}

// DeleteResult is file.delete's result: one entry per requested item, in order.
type DeleteResult struct {
	Items  []DeletedItem `json:"items" jsonschema:"required" jsonschema_description:"Every requested item, in order, the failed ones beside the done ones."`
	Done   int           `json:"done" jsonschema:"required" jsonschema_description:"How many items were deleted."`
	Failed int           `json:"failed" jsonschema:"required" jsonschema_description:"How many items failed or were cancelled."`
}

// noTrash is the call-level answer of a daemon built without a trash (the golden
// registry): there is nowhere to put a deleted item, and a delete never destroys one.
func noTrash() *capability.Error {
	return capability.NewError(capability.ErrFileIOFailed, "this daemon has no trash, so nothing is deleted or restored")
}

// checkDeletes runs the checks that need only the text of the call, on every path, before
// anything changes (ADR-0053 §2): a NUL or an empty path, a path outside the root, the
// root itself.
func (r *resolver) checkDeletes(paths []string) ([]string, *capability.Error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		rel, err := r.opPath(p, "paths["+strconv.Itoa(i)+"]")
		if err != nil {
			return nil, err
		}
		out[i] = rel
	}
	return out, nil
}

// delete serves file.delete (ADR-0053, ADR-0054): each item moved into the workspace's
// trash, not removed. The trash is swept first, so what this call trashes is never swept
// by it.
func (r *resolver) delete(ctx context.Context, p DeleteParams) (DeleteResult, error) {
	if r.trash == nil {
		return DeleteResult{}, noTrash()
	}
	if e := checkBatch(len(p.Paths)); e != nil {
		return DeleteResult{}, e
	}
	paths, e := r.checkDeletes(p.Paths)
	if e != nil {
		return DeleteResult{}, e
	}

	ids := make([]string, len(paths))
	var errs []*capability.Error
	if root, cerr := r.openRoot(); cerr != nil {
		errs = failAll(len(paths), cerr)
	} else {
		defer root.Close()
		r.trash.Sweep(r.workspace, r.root)
		var err error
		errs, err = runBatch(ctx, len(paths), func(i int) *capability.Error {
			id, ferr := r.deleteOne(ctx, root, paths[i])
			ids[i] = id
			return ferr
		})
		if err != nil {
			return DeleteResult{}, err
		}
	}

	res := DeleteResult{Items: make([]DeletedItem, len(paths))}
	for i, rel := range paths {
		it := DeletedItem{Path: rel}
		if errs[i] != nil {
			it.Code, it.Message = failure(limitCodes(errs[i], deleteCodes...))
			res.Failed++
		} else {
			it.TrashID = ids[i]
			res.Done++
		}
		res.Items[i] = it
	}
	return res, nil
}

// deleteCodes are the codes an item of file.delete can carry; limitCodes folds any other
// into FILE_IO_FAILED, with its message kept.
var deleteCodes = []capability.ErrorCode{
	capability.ErrFileNotFound, capability.ErrFileOutsideRoot, capability.ErrFileTooLarge,
	capability.ErrFileIOFailed, capability.ErrOperationCancelled,
}

// limitCodes returns e if its code is one of allowed, else FILE_IO_FAILED carrying e's
// message, so an item never reports a code its enum does not publish.
func limitCodes(e *capability.Error, allowed ...capability.ErrorCode) *capability.Error {
	for _, c := range allowed {
		if e.Code == c {
			return e
		}
	}
	out := *e
	out.Code = capability.ErrFileIOFailed
	return &out
}

// deleteText is file.delete's rendering (batchText).
func deleteText(r DeleteResult) string {
	lines := make([]batchLine, len(r.Items))
	for i, it := range r.Items {
		words := []string{it.Path}
		if it.TrashID != "" {
			words = append(words, "->", it.TrashID)
		}
		lines[i] = batchLine{words: words, code: it.Code, message: it.Message}
	}
	return batchText("trashed", lines)
}

// measure counts what a delete takes into the trash for entry.json: the item's entries
// and the bytes of its regular files, by a walk through the os.Root that never follows a
// link and stops at the entry cap (D48), reporting that it was cut. A folder it cannot
// read is counted but not entered; a size is only a figure for the sweep, never a gate.
func (r *resolver) measure(ctx context.Context, root *os.Root, rel string, fi fs.FileInfo) (bytes int64, entries int, truncated bool) {
	limit := r.copyEntries()
	entries = 1
	if fi.Mode().IsRegular() {
		bytes = fi.Size()
	}
	if !fi.IsDir() {
		return bytes, entries, false
	}
	stack := []string{rel}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return bytes, entries, true
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		d, err := root.Open(dir)
		if err != nil {
			continue
		}
		des, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			continue
		}
		for _, de := range des {
			if entries >= limit {
				return bytes, entries, true
			}
			entries++
			info, err := de.Info()
			if err != nil {
				continue
			}
			switch {
			case info.IsDir():
				stack = append(stack, path.Join(dir, de.Name()))
			case info.Mode().IsRegular():
				bytes += info.Size()
			}
		}
	}
	return bytes, entries, false
}

// deleteOne moves one item into the trash and returns its id. The record is written
// first, then the item leaves its path by one rename, so a crash at any point leaves
// either the item where it was or a complete entry (ADR-0054 §1, §2). The workspace's
// trash lock is held throughout, so a sweep never takes this delete for a crashed one.
func (r *resolver) deleteOne(ctx context.Context, root *os.Root, rel string) (string, *capability.Error) {
	fi, err := root.Lstat(rel)
	if err != nil {
		return "", r.classifyOp(rel, err)
	}
	dirRel, base := splitDir(rel)
	parent, e := r.openParent(root, dirRel)
	if e != nil {
		return "", e
	}
	defer parent.Close()

	t := r.trash
	if !validWorkspaceID(r.workspace) {
		return "", capability.Errorf(capability.ErrFileIOFailed, "the workspace id %q cannot name a trash folder", r.workspace)
	}
	defer t.lock(r.workspace)()

	bytes, entries, truncated := r.measure(ctx, root, rel, fi)
	id := newTrashID()
	entry := trashEntry{
		Path: rel, Type: entryType(fi.Mode()), DeletedAt: trashNow().UTC(),
		Bytes: bytes, Entries: entries, Truncated: truncated,
	}
	idPath := t.entryDir(r.workspace, id)
	if err := t.st.Write(entryName(r.workspace, id), entry); err != nil {
		_ = removeTree(idPath)
		return "", r.trashFailed(rel, "the trash entry could not be written", err)
	}
	// The store syncs <id>; the folders that hold <id> must be synced too, or a crash can
	// lose the record the item's move depends on.
	if err := t.syncParents(r.workspace); err != nil {
		_ = removeTree(idPath)
		return "", r.trashFailed(rel, "the trash folders could not be synced", err)
	}
	idDir, err := os.Open(idPath)
	if err != nil {
		_ = removeTree(idPath)
		return "", r.trashFailed(rel, "the trash folder could not be opened", err)
	}
	defer idDir.Close()

	// One volume: one rename, any size.
	err = noReplace(parent, base, idDir, trashItem)
	if err == nil {
		_ = syncFile(idDir)
		_ = syncFile(parent)
		return id, nil
	}
	if !isXdev(err) {
		_ = removeTree(idPath)
		return "", r.deleteRenameError(rel, err)
	}
	return r.deleteAcross(ctx, root, parent, idDir, entry, id)
}

// deleteAcross is deleteOne when the trash is on another volume (the rename answered
// EXDEV). The item is renamed aside, in its own folder, so nothing written to its path
// afterwards is taken; the aside itself is walked, so what is copied is exactly what the
// aside holds, whatever was created in the folder before the rename; the aside is copied
// whole, every file and folder synced, into item.partial, which becomes item; the aside is
// walked again and, only if it still is what was copied, removed. Before item exists, any
// failure or cancel renames the aside back and drops the entry; a crash leaves the aside
// and the entry for the sweep to put back.
func (r *resolver) deleteAcross(ctx context.Context, root *os.Root, parent, idDir *os.File, entry trashEntry, id string) (string, *capability.Error) {
	t := r.trash
	rel := entry.Path
	dirRel, base := splitDir(rel)
	idPath := t.entryDir(r.workspace, id)

	// A quick bound check, with nothing touched: the walk that is copied is made below, on
	// the aside.
	if _, e := r.walkTree(ctx, root, rel, deleteWalk); e != nil {
		_ = removeTree(idPath)
		return "", e
	}
	aside := tempName()
	entry.Aside = aside
	if err := t.st.Write(entryName(r.workspace, id), entry); err != nil {
		_ = removeTree(idPath)
		return "", r.trashFailed(rel, "the trash entry could not be written", err)
	}
	if err := noReplace(parent, base, parent, aside); err != nil {
		_ = removeTree(idPath)
		return "", r.deleteRenameError(rel, err)
	}
	asideRel := path.Join(dirRel, aside)
	// back puts the original at its path again and drops the entry; if the path has been
	// taken in the meantime the aside and the entry stay, for the sweep to retry.
	back := func() {
		if noReplace(parent, aside, parent, base) == nil {
			_ = removeTree(idPath)
		}
	}

	snap, e := r.walkTree(ctx, root, asideRel, deleteWalk)
	if e != nil {
		back()
		return "", renamedFrom(e, asideRel, rel)
	}
	dst, err := os.OpenRoot(idPath)
	if err != nil {
		back()
		return "", r.trashFailed(rel, "the trash folder could not be opened", err)
	}
	defer dst.Close()
	if e := r.copyTree(ctx, root, dst, snap, asideRel, trashPartly, true); e != nil {
		back()
		return "", renamedFrom(e, asideRel, rel)
	}
	if ctx.Err() != nil {
		back()
		return "", copyCancelled()
	}
	if trashSeam != nil {
		if err := trashSeam(stageCopied); err != nil {
			return "", r.trashFailed(rel, "the process stopped before the copy was moved into place", err)
		}
	}
	if err := noReplace(idDir, trashPartly, idDir, trashItem); err != nil {
		back()
		return "", r.trashFailed(rel, "the copy could not be moved into place", err)
	}
	// If the item is not durable the aside stays: it is the only copy that is. The sweep
	// removes it once a sync of the entry succeeds.
	if err := syncFile(idDir); err != nil {
		return id, nil
	}
	if trashSeam != nil {
		if err := trashSeam(stageStored); err != nil {
			return "", r.trashFailed(rel, "the process stopped before the original was removed", err)
		}
	}

	// The trash holds the item whole. Before the original goes, it must still be what was
	// copied: a writer that got in since the walk (a process that held the folder open)
	// would otherwise lose its files with the aside. A cancel does not interrupt this.
	again, e := r.walkTree(context.WithoutCancel(ctx), root, asideRel, deleteWalk)
	if e != nil || !sameSnapshot(snap, again) {
		changed := capability.Errorf(capability.ErrFileIOFailed, "%q changed while being deleted; nothing was deleted", rel).
			WithDetails(map[string]any{"path": rel}).
			WithNextStep("delete it again once nothing is writing to it")
		// The entry must stop being complete before the aside is put back, or a sweep
		// would remove the aside, with whatever it holds, as a finished delete's leftover.
		// If that fails, the original still goes back when its path is free: back drops the
		// entry with it, so no sweep finds the aside.
		if err := noReplace(idDir, trashItem, idDir, trashPartly); err != nil {
			back()
			return "", changed.WithCause("the copy could not be withdrawn: " + err.Error())
		}
		back()
		return "", changed
	}
	// A removal that fails leaves the item deleted with its aside for the sweep.
	if err := removeAside(root, asideRel); err == nil {
		_ = syncFile(parent)
		// The record stops naming an aside that is gone, so a sweep does not look for it and
		// no eviction can find one it must clean.
		entry.Aside = ""
		_ = t.st.Write(entryName(r.workspace, id), entry)
	}
	return id, nil
}

// sameSnapshot reports whether two walks of one tree found the same entries: names and
// kinds, and the sizes of regular files.
func sameSnapshot(a, b []snapEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// A file's size and modification time: an edit in place that keeps the size, by a
		// process holding a descriptor inside the aside, still changes the time.
		if a[i].rel != b[i].rel || a[i].kind != b[i].kind || (a[i].kind == 0 && (a[i].size != b[i].size || !a[i].mod.Equal(b[i].mod))) {
			return false
		}
	}
	return true
}

// renamedFrom puts the item's own path in a copy error that names the place it was read
// from (the aside of a delete, the trash's item of a restore).
func renamedFrom(e *capability.Error, aside, rel string) *capability.Error {
	out := *e
	out.Message = strings.ReplaceAll(e.Message, `"`+aside, `"`+rel)
	if d, ok := e.Details["path"].(string); ok && d == aside {
		details := map[string]any{}
		for k, v := range e.Details {
			details[k] = v
		}
		details["path"] = rel
		out.Details = details
	}
	return &out
}

// trashFailed is the item's FILE_IO_FAILED for a failure of the trash itself.
func (r *resolver) trashFailed(rel, what string, err error) *capability.Error {
	return capability.Errorf(capability.ErrFileIOFailed, "%s (%q)", what, rel).
		WithCause(err.Error()).
		WithDetails(map[string]any{"path": rel})
}

// deleteRenameError is the item's error for the rename into the trash (or aside) that
// failed for a reason other than a volume boundary.
func (r *resolver) deleteRenameError(rel string, err error) *capability.Error {
	switch {
	case isEscape(err):
		return r.outside(rel)
	case errors.Is(err, fs.ErrNotExist):
		return r.notFound(rel)
	case unsupported(err):
		return r.trashFailed(rel, "this volume cannot refuse a replacing rename, and nothing is replaced", err)
	default:
		return r.trashFailed(rel, "it could not be moved into the trash", err)
	}
}

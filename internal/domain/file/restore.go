package file

import (
	"context"
	"errors"
	"os"
	"path"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/store"
)

// RestoreParams is file.restore's parameters.
type RestoreParams struct {
	TrashIDs []string `json:"trashIds" jsonschema:"required,minItems=1,maxItems=1000,pattern=^[0-9a-f]{16}$" jsonschema_description:"Trash ids from file.delete, restored in order; at most 1000."`
}

// RestoredItem is one item of file.restore's result.
type RestoredItem struct {
	TrashID string `json:"trashId" jsonschema:"required" jsonschema_description:"The trash id as asked."`
	Path    string `json:"path,omitempty" jsonschema_description:"Present when the item was restored: where it is, relative to the project root."`
	Code    string `json:"code,omitempty" jsonschema:"enum=FILE_NOT_IN_TRASH,enum=FILE_EXISTS,enum=FILE_OUTSIDE_ROOT,enum=FILE_WRONG_TYPE,enum=FILE_IO_FAILED,enum=OPERATION_CANCELLED" jsonschema_description:"Present when the item failed: why. Absent for a done item."`
	// Message is the human-readable reason, with the OS error after a colon when there is one.
	Message string `json:"message,omitempty" jsonschema_description:"Present when the item failed: what happened, with the OS error when there is one."`
}

// RestoreResult is file.restore's result: one entry per requested item, in order.
type RestoreResult struct {
	Items  []RestoredItem `json:"items" jsonschema:"required" jsonschema_description:"Every requested item, in order, the failed ones beside the done ones."`
	Done   int            `json:"done" jsonschema:"required" jsonschema_description:"How many items were restored."`
	Failed int            `json:"failed" jsonschema:"required" jsonschema_description:"How many items failed or were cancelled."`
}

// restoreCodes are the codes an item of file.restore can carry (see limitCodes).
var restoreCodes = []capability.ErrorCode{
	capability.ErrFileNotInTrash, capability.ErrFileExists, capability.ErrFileOutsideRoot,
	capability.ErrFileWrongType, capability.ErrFileIOFailed, capability.ErrOperationCancelled,
}

// checkTrashIDs refuses a call with an id that is not 16 lowercase hex characters, before
// any path is built from one (plan m2g D50): `..`, `a/b` and an absolute path never reach
// the filesystem.
func checkTrashIDs(ids []string) *capability.Error {
	for i, id := range ids {
		if !trashIDPattern.MatchString(id) {
			return capability.Errorf(capability.ErrInvalidParams, "trashIds[%d] is not a trash id", i).
				WithNextStep("pass a trashId from a delete's result: 16 lowercase hex characters")
		}
	}
	return nil
}

// restore serves file.restore (ADR-0054 §4): each item put back at its recorded path,
// re-resolved inside the root, never replacing.
func (r *resolver) restore(ctx context.Context, p RestoreParams) (RestoreResult, error) {
	if r.trash == nil {
		return RestoreResult{}, noTrash()
	}
	if e := checkBatch(len(p.TrashIDs)); e != nil {
		return RestoreResult{}, e
	}
	if e := checkTrashIDs(p.TrashIDs); e != nil {
		return RestoreResult{}, e
	}

	paths := make([]string, len(p.TrashIDs))
	var errs []*capability.Error
	if root, cerr := r.openRoot(); cerr != nil {
		errs = failAll(len(p.TrashIDs), cerr)
	} else {
		defer root.Close()
		var err error
		errs, err = runBatch(ctx, len(p.TrashIDs), func(i int) *capability.Error {
			to, ferr := r.restoreOne(ctx, root, p.TrashIDs[i])
			paths[i] = to
			return ferr
		})
		if err != nil {
			return RestoreResult{}, err
		}
	}

	res := RestoreResult{Items: make([]RestoredItem, len(p.TrashIDs))}
	for i, id := range p.TrashIDs {
		it := RestoredItem{TrashID: id}
		if errs[i] != nil {
			it.Code, it.Message = failure(limitCodes(errs[i], restoreCodes...))
			res.Failed++
		} else {
			it.Path = paths[i]
			res.Done++
		}
		res.Items[i] = it
	}
	return res, nil
}

// restoreText is file.restore's rendering (batchText).
func restoreText(r RestoreResult) string {
	lines := make([]batchLine, len(r.Items))
	for i, it := range r.Items {
		words := []string{it.TrashID}
		if it.Path != "" {
			words = append(words, "->", it.Path)
		}
		lines[i] = batchLine{words: words, code: it.Code, message: it.Message}
	}
	return batchText("restored", lines)
}

// notInTrash is the item error of an id this workspace's trash does not hold.
func notInTrash(id string) *capability.Error {
	return capability.Errorf(capability.ErrFileNotInTrash, "%q is not in this workspace's trash", id).
		WithDetails(map[string]any{"trashId": id}).
		WithNextStep("it was restored, swept after 7 days or past the size limit, or belongs to another workspace")
}

// restoreOne puts one trash entry back and returns the path it went to. The id was
// checked; the entry is read from this workspace's trash alone, so another workspace's id
// is simply not there. The recorded path is resolved again, its missing parents made
// through the os.Root, and the rename never replaces; across volumes the item is copied,
// synced, and the trash entry removed only afterwards.
func (r *resolver) restoreOne(ctx context.Context, root *os.Root, id string) (string, *capability.Error) {
	t := r.trash
	if !validWorkspaceID(r.workspace) {
		return "", capability.Errorf(capability.ErrFileIOFailed, "the workspace id %q cannot name a trash folder", r.workspace)
	}
	defer t.lock(r.workspace)()

	var entry trashEntry
	out, err := t.st.Read(entryName(r.workspace, id), store.Version, &entry)
	switch {
	case errors.Is(err, store.ErrNewer):
		return "", capability.Errorf(capability.ErrFileIOFailed, "%q was written by a newer droidyard", id)
	case err != nil:
		return "", r.trashFailed(id, "its trash entry could not be read", err)
	case out != store.Loaded:
		return "", notInTrash(id)
	}
	idPath := t.entryDir(r.workspace, id)
	rel, cerr := r.opPath(entry.Path, "the recorded path")
	if cerr != nil {
		return "", cerr
	}
	if !exists(path.Join(idPath, trashItem)) {
		return r.finishCutDelete(root, id, idPath, rel, entry)
	}
	dirRel, base := splitDir(rel)
	if dirRel != "." {
		if err := root.MkdirAll(dirRel, 0o755); err != nil {
			if p, _ := firstNonDirAncestor(root, rel); p != "" || errors.Is(err, syscall.ENOTDIR) {
				return "", r.parentFile(root, rel)
			}
			return "", r.classifyWrite(rel, err)
		}
	}
	parent, e := r.openParent(root, dirRel)
	if e != nil {
		return "", e
	}
	defer parent.Close()
	idDir, err := os.Open(idPath)
	if err != nil {
		return "", r.trashFailed(id, "its trash folder could not be opened", err)
	}
	defer idDir.Close()

	err = noReplace(idDir, trashItem, parent, base)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrExist):
		return "", existsErr(rel)
	case isXdev(err):
		if e := r.restoreAcross(ctx, root, parent, idPath, dirRel, base); e != nil {
			return "", renamedFrom(e, trashItem, rel)
		}
	case isEscape(err):
		return "", r.outside(rel)
	case unsupported(err):
		return "", r.trashFailed(rel, "this volume cannot refuse a replacing rename, and nothing is replaced", err)
	default:
		return "", r.trashFailed(rel, "it could not be moved back", err)
	}
	// The item is back; the entry has served. But the trash's copy is dropped only once
	// the item is known to be on disk: if the sync fails, the entry stays.
	if syncFile(parent) != nil {
		return rel, nil
	}
	// The aside a finished delete could not remove holds the old bytes of the item just
	// restored: it goes with the entry, or it would stay in the project for good.
	if entry.Aside != "" && asidePattern.MatchString(entry.Aside) {
		_ = removeAside(root, path.Join(dirRel, entry.Aside))
	}
	// A failure here leaves an entry without an item, which the next sweep drops and a
	// restore answers FILE_NOT_IN_TRASH.
	_ = removeTree(idPath)
	return rel, nil
}

// finishCutDelete is restoreOne for an entry with no item: a delete that was cut before
// it was whole. If its original still waits aside, restoring it is putting it back, and
// that is done; otherwise the id holds nothing.
func (r *resolver) finishCutDelete(root *os.Root, id, idPath, rel string, entry trashEntry) (string, *capability.Error) {
	if entry.Aside == "" || !asidePattern.MatchString(entry.Aside) {
		return "", notInTrash(id)
	}
	dirRel, _ := splitDir(rel)
	if _, err := root.Lstat(path.Join(dirRel, entry.Aside)); err != nil {
		return "", notInTrash(id)
	}
	switch err := r.putAsideBack(root, entry); {
	case err == nil:
		_ = removeTree(idPath)
		return rel, nil
	case errors.Is(err, os.ErrExist):
		return "", existsErr(rel)
	default:
		return "", r.trashFailed(rel, "the original could not be put back", err)
	}
}

// restoreAcross is restoreOne when the trash is on another volume: the item is copied
// from the trash into a temporary beside its path, every file and folder synced, and
// renamed into place without replacing. The trash keeps its copy until the caller drops
// the entry. Any failure or cancel removes the temporary and leaves the item in the trash.
func (r *resolver) restoreAcross(ctx context.Context, root *os.Root, parent *os.File, idPath, dirRel, base string) *capability.Error {
	src, err := os.OpenRoot(idPath)
	if err != nil {
		return r.trashFailed(path.Join(dirRel, base), "its trash folder could not be opened", err)
	}
	defer src.Close()
	snap, e := r.walkTree(ctx, src, trashItem, restoreWalk)
	if e != nil {
		return e
	}
	tmpBase := tempName()
	tmpRel := path.Join(dirRel, tmpBase)
	committed := false
	defer func() {
		if !committed {
			root.RemoveAll(tmpRel)
		}
	}()
	if e := r.copyTree(ctx, src, root, snap, trashItem, tmpRel, true); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return copyCancelled()
	}
	if err := noReplace(parent, tmpBase, parent, base); err != nil {
		if errors.Is(err, os.ErrExist) {
			return existsErr(path.Join(dirRel, base))
		}
		return r.trashFailed(path.Join(dirRel, base), "it could not be moved back", err)
	}
	committed = true
	return nil
}

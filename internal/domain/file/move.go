package file

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// MoveItem is one move of file.move's parameters.
type MoveItem struct {
	From string `json:"from" jsonschema:"required" jsonschema_description:"The existing file or folder, relative to the project root."`
	To   string `json:"to" jsonschema:"required" jsonschema_description:"Its new path, relative to the project root; the target's parent folder must exist."`
}

// MoveParams is file.move's parameters.
type MoveParams struct {
	Items       []MoveItem `json:"items" jsonschema:"required,minItems=1,maxItems=1000" jsonschema_description:"The moves, made in order; at most 1000."`
	OnCollision string     `json:"onCollision,omitempty" jsonschema:"enum=fail,enum=keepBoth" jsonschema_description:"When a target exists: fail the item (the default), or take a free name such as \"b 2.kt\"."`
}

// MovedItem is one item of file.move's result.
type MovedItem struct {
	From string `json:"from" jsonschema:"required" jsonschema_description:"The source as asked, relative to the project root."`
	To   string `json:"to" jsonschema:"required" jsonschema_description:"Where it went (the free name under keepBoth); for a failed item, the target as asked."`
	Code string `json:"code,omitempty" jsonschema:"enum=FILE_EXISTS,enum=FILE_NOT_FOUND,enum=FILE_WRONG_TYPE,enum=FILE_OUTSIDE_ROOT,enum=FILE_IO_FAILED,enum=OPERATION_CANCELLED" jsonschema_description:"Present when the item failed: why. Absent for a done item."`
	// Message is the human-readable reason, with the OS error after a colon when there is one.
	Message string `json:"message,omitempty" jsonschema_description:"Present when the item failed: what happened, with the OS error when there is one."`
}

// MoveResult is file.move's result: one entry per requested item, in order.
type MoveResult struct {
	Items  []MovedItem `json:"items" jsonschema:"required" jsonschema_description:"Every requested item, in order, the failed ones beside the done ones."`
	Done   int         `json:"done" jsonschema:"required" jsonschema_description:"How many items were moved."`
	Failed int         `json:"failed" jsonschema:"required" jsonschema_description:"How many items failed or were cancelled."`
}

// renameSeam, when non-nil, runs after an item's two parent directories are open and
// before its rename, with the paths of the item. Production leaves it nil. A test uses it
// to let another writer take the target at the worst moment, or, by returning an error,
// to stand for the rename's own answer, ENOTSUP for one (plan m2g step 2). A non-nil
// error is treated as the rename's.
var renameSeam func(from, to string) error

// noReplace is the kernel's no-replace rename. A test swaps it for one answering ENOTSUP,
// so the answer passes through rename exactly as a volume's would.
var noReplace = renameNoReplace

// resolvedMove is a move whose paths passed the lexical checks.
type resolvedMove struct{ from, to string }

// checkMoves runs the checks that need only the text of the call, on every item, before
// anything changes (ADR-0053 §2): a NUL or an empty path, a path outside the root, the
// root itself, a source equal to its target, a target inside its source, a source named
// twice. The first failure fails the whole call.
func (r *resolver) checkMoves(items []MoveItem) ([]resolvedMove, *capability.Error) {
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
		if from == to {
			return nil, capability.Errorf(capability.ErrInvalidParams, "items[%d]: %q is moved onto itself", i, from).
				WithNextStep("drop the item, or give it a different target")
		}
		if strings.HasPrefix(to, from+"/") {
			return nil, capability.Errorf(capability.ErrInvalidParams, "items[%d]: %q cannot move inside itself (%q)", i, from, to).
				WithNextStep("move it beside, not inside, its own folder")
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

// move serves file.move (ADR-0053): each item a rename that never replaces, between two
// parent directories opened through the os.Root and renamed on their descriptors.
func (r *resolver) move(ctx context.Context, p MoveParams) (MoveResult, error) {
	if e := checkBatch(len(p.Items)); e != nil {
		return MoveResult{}, e
	}
	keepBoth, e := checkCollision(p.OnCollision)
	if e != nil {
		return MoveResult{}, e
	}
	moves, e := r.checkMoves(p.Items)
	if e != nil {
		return MoveResult{}, e
	}

	final := make([]string, len(moves))
	var errs []*capability.Error
	if root, cerr := r.openRoot(); cerr != nil {
		errs = failAll(len(moves), cerr)
	} else {
		defer root.Close()
		var err error
		errs, err = runBatch(ctx, len(moves), func(i int) *capability.Error {
			to, ferr := r.moveOne(root, moves[i], keepBoth)
			final[i] = to
			return ferr
		})
		if err != nil {
			return MoveResult{}, err
		}
	}

	res := MoveResult{Items: make([]MovedItem, len(moves))}
	for i, m := range moves {
		it := MovedItem{From: m.from, To: m.to}
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

// moveText is file.move's rendering (batchText).
func moveText(r MoveResult) string {
	lines := make([]batchLine, len(r.Items))
	for i, it := range r.Items {
		lines[i] = batchLine{words: []string{it.From, "->", it.To}, code: it.Code, message: it.Message}
	}
	return batchText("moved", lines)
}

// openParent opens dir (a name relative to the root) as a directory descriptor through
// the os.Root, so a symlink in it cannot leave the root, and checks, on the descriptor,
// that it is a directory. A failure names dir.
func (r *resolver) openParent(root *os.Root, dir string) (*os.File, *capability.Error) {
	// Non-blocking, so a FIFO named as a parent opens at once and is refused below, as
	// file.list does.
	f, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, r.classifyOp(dir, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, r.classifyOp(dir, err)
	}
	if !fi.IsDir() {
		f.Close()
		return nil, notDir(dir, typeName(fi.Mode()))
	}
	return f, nil
}

// splitDir is a root-relative name's directory (opened with root.OpenFile; "." for the
// root) and final component.
func splitDir(rel string) (dir, base string) {
	dir, base = path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}
	return dir, base
}

// moveOne makes one move and returns the path the item ended at: the target, or, for
// keepBoth, the free name it was given. The final components are never followed: a
// symlink is moved as the link.
func (r *resolver) moveOne(root *os.Root, m resolvedMove, keepBoth bool) (string, *capability.Error) {
	fromPath, fromBase := splitDir(m.from)
	toPath, toBase := splitDir(m.to)
	fromDir, e := r.openParent(root, fromPath)
	if e != nil {
		return "", e
	}
	defer fromDir.Close()
	toDir, e := r.openParent(root, toPath)
	if e != nil {
		return "", e
	}
	defer toDir.Close()

	var err error
	if renameSeam != nil {
		err = renameSeam(m.from, m.to)
	}
	if err == nil {
		err = r.rename(fromDir, fromBase, toDir, toBase, m)
	}
	if err == nil {
		return m.to, nil
	}
	if !errors.Is(err, errExists) {
		return "", r.moveError(m, err)
	}
	if !keepBoth {
		return "", existsErr(m.to)
	}
	// keepBoth: D49's names in turn. Each try is the same no-replace rename, so a free
	// name is still taken only if it is free at the moment of the rename.
	isDir := false
	if fi, lerr := root.Lstat(m.from); lerr == nil {
		isDir = fi.IsDir()
	}
	for n := 2; n < 2+maxFreeNames; n++ {
		name := freeName(toBase, isDir, n)
		err := r.rename(fromDir, fromBase, toDir, name, resolvedMove{from: m.from, to: path.Join(toPath, name)})
		if err == nil {
			return path.Join(toPath, name), nil
		}
		if !errors.Is(err, errExists) {
			return "", r.moveError(m, err)
		}
	}
	return "", existsErr(m.to)
}

// errExists is rename's answer for a target that is taken: the kernel's EEXIST, kept as a
// sentinel so moveOne can tell it from every other failure.
var errExists = errors.New("target exists")

// rename renames from to to between two open directories, never replacing. A target that is taken is errExists, except when it
// is the source itself under another spelling that the volume folds (a case-only rename
// on a case-insensitive volume, or a normalisation change), which is a plain rename: the
// two names are one entry, so nothing can be replaced. That is decided on the two
// descriptors, from the entry itself (a name the directory lists exactly is always a
// separate entry, so a hard-linked sibling is never taken for the source).
func (r *resolver) rename(fromDir *os.File, from string, toDir *os.File, to string, m resolvedMove) error {
	err := noReplace(fromDir, from, toDir, to)
	if err == nil || !errors.Is(err, os.ErrExist) {
		return err
	}
	fromPath, _ := splitDir(m.from)
	toPath, _ := splitDir(m.to)
	if fromPath == toPath && from != to && foldEqual(from, to) {
		if same, serr := r.foldedSource(fromDir, from, toDir, to); serr == nil && same {
			return renameSameFile(fromDir, from, toDir, to)
		}
	}
	return errExists
}

// foldedSource reports whether the entry `to` of toDir is the source, from, under a
// spelling the volume folds: the directory does not list `to` exactly (so no separate
// entry has that name) and an lstat of `to` finds the source's device and inode.
func (r *resolver) foldedSource(fromDir *os.File, from string, toDir *os.File, to string) (bool, error) {
	if _, err := toDir.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	names, err := toDir.Readdirnames(-1)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == to {
			return false, nil
		}
	}
	return sameEntry(fromDir, from, toDir, to)
}

// moveError is the item's error for a rename that failed for a reason other than a taken
// target.
func (r *resolver) moveError(m resolvedMove, err error) *capability.Error {
	switch {
	case unsupported(err):
		return capability.Errorf(capability.ErrFileIOFailed, "%q was not moved: this volume cannot refuse a replacing rename, and nothing is replaced", m.from).
			WithCause(err.Error()).
			WithDetails(map[string]any{"path": m.from})
	case isEscape(err):
		return r.outside(m.from)
	case errors.Is(err, os.ErrNotExist):
		return r.notFound(m.from)
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EISDIR):
		return capability.Errorf(capability.ErrFileWrongType, "%q cannot be moved to %q: a file and a folder do not exchange places", m.from, m.to).
			WithCause(err.Error()).
			WithDetails(map[string]any{"path": m.from})
	default:
		return capability.Errorf(capability.ErrFileIOFailed, "moving %q to %q failed", m.from, m.to).
			WithCause(err.Error()).
			WithDetails(map[string]any{"path": m.from})
	}
}

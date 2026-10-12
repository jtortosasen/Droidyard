package file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The file operations that change the tree (ADR-0053, ADR-0054): file.create and file.move
// here, file.copy, file.delete and file.restore beside them. They are batches:
// a call carries up to maxItems items, a refusal decided from the text alone fails the
// whole call before anything changes, and anything only the filesystem can say fails its
// item. The call then completes with every item's outcome, the failed ones beside the
// done ones, so the caller can build an Undo from what was done (plan m2g Q1).

// maxItems is the most items one call takes (ADR-0053 §2; plan m2g D48).
const maxItems = 1000

// onCollision's values (ADR-0053 §3): fail the item, or ask the daemon for a free name.
const (
	collisionFail     = "fail"
	collisionKeepBoth = "keepBoth"
)

// itemSeam, when non-nil, runs before each item of a batch with its index, ahead of the
// cancel check. Production leaves it nil; a test uses it to cancel an operation at a
// chosen point between items (plan m2g step 2).
var itemSeam func(i int)

// failure is an item's code and message as a result carries them: the error's message,
// with the OS error beside it as the cause when there is one.
func failure(e *capability.Error) (code, message string) {
	message = e.Message
	if e.Cause != "" {
		message += ": " + e.Cause
	}
	return string(e.Code), message
}

// opPath resolves one path of an operation (resolve's rules, "" and a NUL
// REQUEST_INVALID_PARAMS) and refuses the project root itself: an operation on the root's
// own entry would change a directory outside the root (ADR-0053 §2). where names the
// parameter in the message.
func (r *resolver) opPath(p, where string) (string, *capability.Error) {
	rel, err := r.resolve(p, true)
	if err != nil {
		err.Message = where + ": " + err.Message
		return "", err
	}
	if rel == "." {
		return "", capability.Errorf(capability.ErrFileOutsideRoot, "%s: %q is the project root itself, which no file operation changes", where, p).
			WithDetails(map[string]any{"path": p, "root": r.root}).
			WithNextStep("name a file or folder inside the project root")
	}
	return rel, nil
}

// checkBatch bounds a batch's item count: at least one, at most maxItems.
func checkBatch(n int) *capability.Error {
	if n == 0 {
		return capability.NewError(capability.ErrInvalidParams, "items is empty").
			WithNextStep("pass at least one item")
	}
	if n > maxItems {
		return capability.Errorf(capability.ErrInvalidParams, "%d items; the most is %d", n, maxItems).
			WithNextStep("split the call into batches of at most " + strconv.Itoa(maxItems))
	}
	return nil
}

// checkCollision validates onCollision: "" is fail.
func checkCollision(v string) (keepBoth bool, e *capability.Error) {
	switch v {
	case "", collisionFail:
		return false, nil
	case collisionKeepBoth:
		return true, nil
	}
	return false, capability.Errorf(capability.ErrInvalidParams, "onCollision is %q; it is %q or %q", v, collisionFail, collisionKeepBoth)
}

// runBatch runs attempt for each of n items in order and returns each item's error (nil
// for a done item). A cancel stops between items (ADR-0053 §4): with no item attempted
// yet it fails the call OPERATION_CANCELLED, so the operation ends cancelled; otherwise
// it returns normally and every item not attempted is OPERATION_CANCELLED, so the
// operation completes and keeps what was done. An item that failed was attempted.
func runBatch(ctx context.Context, n int, attempt func(i int) *capability.Error) ([]*capability.Error, error) {
	errs := make([]*capability.Error, n)
	attempted := 0
	for i := 0; i < n; i++ {
		if itemSeam != nil {
			itemSeam(i)
		}
		if ctx.Err() != nil {
			if attempted == 0 {
				return nil, capability.NewError(capability.ErrOperationCancelled, "the operation was cancelled before any item was attempted")
			}
			for j := i; j < n; j++ {
				errs[j] = capability.NewError(capability.ErrOperationCancelled, "the operation was cancelled before this item")
			}
			break
		}
		errs[i] = attempt(i)
		attempted++
	}
	return errs, nil
}

// failAll is runBatch's answer when the whole batch cannot start for a reason only the
// filesystem can give (the root would not open): every item fails with it.
func failAll(n int, e *capability.Error) []*capability.Error {
	errs := make([]*capability.Error, n)
	for i := range errs {
		errs[i] = e
	}
	return errs
}

// existsErr is FILE_EXISTS: the target is taken and nothing was replaced.
func existsErr(path string) *capability.Error {
	return capability.Errorf(capability.ErrFileExists, "%q already exists", path).
		WithDetails(map[string]any{"path": path}).
		WithNextStep("pick another name, or pass onCollision keepBoth to take a free one")
}

// freeName is the nth candidate name of a move that asked for keepBoth (ADR-0053 §3;
// plan m2g D49): "name 2.ext", "name 3.ext"… The extension is a file name's last
// dot-suffix unless the name starts with its only dot (".gitignore" has none); a folder
// has none.
func freeName(name string, isDir bool, n int) string {
	stem, ext := name, ""
	if !isDir {
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			stem, ext = name[:i], name[i:]
		}
	}
	return stem + " " + strconv.Itoa(n) + ext
}

// maxFreeNames is how many free names a keepBoth tries before the item is FILE_EXISTS
// (plan m2g D49).
const maxFreeNames = 1000

// errnoOf is the errno inside err, or 0.
func errnoOf(err error) syscall.Errno {
	var en syscall.Errno
	if errors.As(err, &en) {
		return en
	}
	return 0
}

// unsupported reports whether err is a volume (or a platform) saying it has no rename
// that refuses a replacement: the item then fails FILE_IO_FAILED, never falling back to a
// replacing one (ADR-0053 §3). EINVAL, which some volumes answer for the flag, is also
// what the kernel says for a folder moved into itself, so it is reported as the plain I/O
// failure it is, with the OS error as its cause; it too fails the item.
func unsupported(err error) bool {
	// ENOTSUP and EOPNOTSUPP are one number on Linux, so they cannot be two switch cases.
	if en := errnoOf(err); en == syscall.ENOTSUP || en == syscall.EOPNOTSUPP {
		return true
	}
	return errors.Is(err, errors.ErrUnsupported)
}

// classifyOp turns an error from a descriptor-relative operation into a capability error
// about path: a taken name is FILE_EXISTS, a missing one FILE_NOT_FOUND, a parent that is
// a file FILE_WRONG_TYPE, a refusal to leave the root FILE_OUTSIDE_ROOT, the rest
// FILE_IO_FAILED with the OS error as its cause.
func (r *resolver) classifyOp(path string, err error) *capability.Error {
	switch {
	case err == nil:
		return nil
	case isEscape(err):
		return r.outside(path)
	case errors.Is(err, fs.ErrExist):
		return existsErr(path)
	case errors.Is(err, fs.ErrNotExist):
		return r.notFound(path)
	case errors.Is(err, syscall.ENOTDIR):
		return capability.Errorf(capability.ErrFileWrongType, "a parent of %q is not a directory", path).
			WithDetails(map[string]any{"path": path})
	default:
		return r.ioFailed(path, err)
	}
}

// itemWhere names an item's parameter in a message.
func itemWhere(i int, field string) string { return fmt.Sprintf("items[%d].%s", i, field) }

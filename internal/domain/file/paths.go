package file

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// escapeText is the standard library's sentinel for a path that leaves an os.Root:
// the unexported errPathEscapes, wrapped in a *PathError or a *LinkError (Go 1.27,
// os/file.go: `var errPathEscapes = errors.New("path escapes from parent")`). It is
// unexported, so it can only be recognised by its text; a test pins the text from a
// real os.Root refusal, so a Go release that changes it fails loudly rather than
// turning an escape into an I/O failure.
const escapeText = "path escapes from parent"

// resolver turns a caller-supplied path into a root-relative name and classifies the
// errors an os.Root reports. root is the workspace's canonical root (workspace.open
// canonicalises it), so an absolute path is stripped of it lexically first (D2).
type resolver struct {
	root string
	env  platform.Env
}

// resolve maps path to a slash-separated name relative to the root, per D2: "" and "."
// are the root, a relative path is cleaned and must stay local, and an absolute path is
// stripped of the canonical root or, failing that, of its own canonicalised directory
// with the final component left unresolved. needFile makes "" a REQUEST_INVALID_PARAMS
// (a file is required); a NUL byte is one too, always.
func (r *resolver) resolve(path string, needFile bool) (string, *capability.Error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", capability.NewError(capability.ErrInvalidParams, "the path contains a NUL byte").
			WithNextStep("pass a path with no NUL byte")
	}
	if path == "" {
		if needFile {
			return "", capability.NewError(capability.ErrInvalidParams, "a path is required").
				WithNextStep("pass the file's path relative to the project root")
		}
		return ".", nil
	}
	if filepath.IsAbs(path) {
		return r.resolveAbsolute(path)
	}
	cleaned := filepath.Clean(path)
	// A relative path that would leave the root lexically ("..", "../x") never reaches
	// the filesystem: filepath.IsLocal is the lexical rule, filepath.Clean has already
	// resolved ".", "//" and interior "..".
	if !filepath.IsLocal(cleaned) {
		return "", r.outside(path)
	}
	return filepath.ToSlash(cleaned), nil
}

// resolveAbsolute strips the canonical root from an absolute path at a component
// boundary; only when that fails does it canonicalise the path's directory (so the
// "/var/…" and "/private/var/…" aliases of the root agree) and append the final
// component unresolved, then strip again. The final component therefore reaches
// os.Root as written, so an absolute path to a link obeys the same link rules as a
// relative one; anything that still does not fall under the root is FILE_OUTSIDE_ROOT.
func (r *resolver) resolveAbsolute(path string) (string, *capability.Error) {
	abs := filepath.Clean(path)
	if rel, ok := stripRoot(r.root, abs); ok {
		return rel, nil
	}
	dir, base := filepath.Split(abs)
	canonDir := r.env.Canonical(filepath.Clean(dir))
	if rel, ok := stripRoot(r.root, filepath.Join(canonDir, base)); ok {
		return rel, nil
	}
	return "", r.outside(path)
}

// stripRoot reports whether p is the root or sits under it, and the slash-separated
// name relative to it ("." for the root itself). The boundary is a path separator, so
// /a/bc is not under /a/b.
func stripRoot(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	if p == root {
		return ".", true
	}
	prefix := root
	if root != string(filepath.Separator) {
		prefix = root + string(filepath.Separator)
	}
	if strings.HasPrefix(p, prefix) {
		return filepath.ToSlash(p[len(prefix):]), true
	}
	return "", false
}

// classify turns an error from an os.Root operation into a capability error: a missing
// path is FILE_NOT_FOUND, a refusal to leave the root is FILE_OUTSIDE_ROOT, and
// anything else is FILE_IO_FAILED with the OS error as its cause (D3).
func (r *resolver) classify(path string, err error) *capability.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return r.notFound(path)
	case isEscape(err):
		return r.outside(path)
	default:
		return r.ioFailed(path, err)
	}
}

// isEscape reports whether err is an os.Root's refusal to leave the root: the escape
// sentinel wrapped in a *PathError or a *LinkError, possibly inside another one (a
// Root.MkdirAll reports "mkdirat dir: statat dir: path escapes from parent").
func isEscape(err error) bool {
	for err != nil {
		var inner error
		var pe *fs.PathError
		var le *os.LinkError
		switch {
		case errors.As(err, &pe):
			inner = pe.Err
		case errors.As(err, &le):
			inner = le.Err
		default:
			return false
		}
		if inner != nil && inner.Error() == escapeText {
			return true
		}
		err = inner
	}
	return false
}

// outside is FILE_OUTSIDE_ROOT: the path names a location outside the project root, so
// nothing was read or written there (D3).
func (r *resolver) outside(path string) *capability.Error {
	return capability.Errorf(capability.ErrFileOutsideRoot, "%q is outside the project root", path).
		WithDetails(map[string]any{"path": path, "root": r.root}).
		WithNextStep("pass a path inside the project root; files outside it are not reachable through droidyard")
}

// notFound is FILE_NOT_FOUND for a path that does not exist (D3).
func (r *resolver) notFound(path string) *capability.Error {
	return capability.Errorf(capability.ErrFileNotFound, "%q does not exist", path).
		WithDetails(map[string]any{"path": path}).
		WithNextStep("call file.list on the parent directory to see what is there")
}

// wrongType is FILE_WRONG_TYPE: the path exists but is not what the call needs — a
// directory where a file is required, a FIFO, socket or device, or (a write) a symlink
// (D3).
func wrongType(path, typ string) *capability.Error {
	return capability.Errorf(capability.ErrFileWrongType, "%q is a %s, not a regular file", path, typ).
		WithDetails(map[string]any{"path": path, "type": typ}).
		WithNextStep("pass a path to a regular file")
}

// notDir is FILE_WRONG_TYPE for file.list: the path exists but is not a directory (D3).
func notDir(path, typ string) *capability.Error {
	return capability.Errorf(capability.ErrFileWrongType, "%q is a %s, not a directory", path, typ).
		WithDetails(map[string]any{"path": path, "type": typ}).
		WithNextStep("call file.read on a file, or file.list on its parent directory")
}

// ioFailed is FILE_IO_FAILED: everything else the filesystem did, with the OS error as
// its cause (D3).
func (r *resolver) ioFailed(path string, err error) *capability.Error {
	return capability.Errorf(capability.ErrFileIOFailed, "accessing %q failed", path).
		WithCause(err.Error()).
		WithDetails(map[string]any{"path": path})
}

// typeName is a FileMode's kind as the wrong-type error reports it (D3's
// details.type).
func typeName(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "directory"
	case m.IsRegular():
		return "regular file"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "special file"
	}
}

// entryType is a directory entry's type as file.list publishes it: file, dir, symlink
// or other (D4).
func entryType(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	default:
		return "other"
	}
}

// ResolveDir resolves path, relative to the canonical root, to the absolute canonical
// directory it names: the same rules as every file.* path (D2: lexical containment, then
// an os.Root open so no symlink leaves the root), and the target must be a directory.
// "" and "." are the root. It exists for shell.exec's cwd (plan m1p D1); the error is
// descriptive and carries no FILE_* code, since the caller maps it to its own.
func ResolveDir(env platform.Env, root, path string) (string, error) {
	r := &resolver{root: root, env: env}
	rel, cerr := r.resolve(path, false)
	if cerr != nil {
		return "", errors.New(cerr.Message)
	}
	rt, cerr := r.openRoot()
	if cerr != nil {
		return "", errors.New(cerr.Message)
	}
	defer rt.Close()
	d, err := rt.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%q does not exist", path)
		}
		if isEscape(err) {
			return "", fmt.Errorf("%q leaves the project root", path)
		}
		return "", fmt.Errorf("%q cannot be opened: %v", path, err)
	}
	defer d.Close()
	fi, err := d.Stat()
	if err != nil {
		return "", fmt.Errorf("%q cannot be examined: %v", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is a %s, not a directory", path, typeName(fi.Mode()))
	}
	abs, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("%q cannot be resolved: %v", path, err)
	}
	if _, ok := stripRoot(root, abs); !ok {
		return "", fmt.Errorf("%q leaves the project root", path)
	}
	return abs, nil
}

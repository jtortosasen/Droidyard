package file

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"golang.org/x/text/unicode/norm"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// maxWrite is the most content file.write accepts: 512 KiB of UTF-8, so an ordinary
// request stays under the 1 MiB HTTP and WebSocket bound (D6).
const maxWrite = 512 << 10

// writeSeam, when non-nil, runs between the temporary file and the rename. Production
// leaves it nil; a test replaces it to force the failure path — the original file left
// intact, no temporary file, FILE_IO_FAILED (plan m1j step 2).
var writeSeam func(temp string) error

// WriteParams is file.write's parameters.
type WriteParams struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"File to write, relative to the project root; missing parent directories are created."`
	Content string `json:"content" jsonschema:"required" jsonschema_description:"The file's new content, UTF-8, at most 524288 bytes. The whole file is replaced."`
}

// WriteResult is file.write's result (D6).
type WriteResult struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"The file written, relative to the project root, using the name it has on disk."`
	Size    int64  `json:"size" jsonschema:"required" jsonschema_description:"The file's size in bytes after the write."`
	Created bool   `json:"created" jsonschema_description:"True when the write created the file; false when it replaced an existing one."`
}

// write serves file.write (D6): a whole-file replacement, atomic by ADR-0021 §Format's
// recipe — a temporary file in the target's directory, fsynced, renamed over the target,
// the directory fsynced — so a reader sees the old file or the new one, never a partial
// write. An existing file keeps its on-disk name and exact mode; a new file is created
// 0644 before umask; missing parents are created inside the root only.
func (r *resolver) write(p WriteParams) (WriteResult, error) {
	if len(p.Content) > maxWrite {
		return WriteResult{}, capability.Errorf(capability.ErrInvalidParams, "content is %d bytes; the most is %d", len(p.Content), maxWrite)
	}
	rel, cerr := r.resolve(p.Path, true)
	if cerr != nil {
		return WriteResult{}, cerr
	}
	dir, base := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}

	root, cerr := r.openRoot()
	if cerr != nil {
		return WriteResult{}, cerr
	}
	defer root.Close()

	// Classify the existing target before touching anything (D6): a symlink, a
	// directory, a FIFO or any other special file is FILE_WRONG_TYPE — a rename would
	// replace the link, not its target — and an existing file without its owner-write
	// bit is FILE_IO_FAILED, because a rename would defeat the mark.
	mode := fs.FileMode(0o644)
	created := false
	fi, lerr := root.Lstat(rel)
	switch {
	case lerr == nil:
		if fi.Mode()&fs.ModeSymlink != 0 {
			return WriteResult{}, wrongType(rel, "symlink")
		}
		if !fi.Mode().IsRegular() {
			return WriteResult{}, wrongType(rel, typeName(fi.Mode()))
		}
		if fi.Mode().Perm()&0o200 == 0 {
			return WriteResult{}, capability.Errorf(capability.ErrFileIOFailed, "%q is not writable", rel).
				WithCause("read-only file").
				WithDetails(map[string]any{"path": rel})
		}
		mode = fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	case errors.Is(lerr, fs.ErrNotExist):
		created = true
	case errors.Is(lerr, syscall.ENOTDIR):
		// A parent component is a file where a directory is needed.
		return WriteResult{}, r.parentFile(root, rel)
	default:
		return WriteResult{}, r.classifyWrite(rel, lerr)
	}

	// Create missing parents inside the root only. A dangling in-root parent link makes
	// MkdirAll fail with EEXIST, which is an I/O failure: a write's list has no
	// FILE_NOT_FOUND, so no ENOENT here is classified as one (D6).
	if created && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			if errors.Is(err, syscall.ENOTDIR) {
				return WriteResult{}, r.parentFile(root, rel)
			}
			return WriteResult{}, r.classifyWrite(rel, err)
		}
	}

	// The directory descriptor: its entries name the target's on-disk spelling, and it
	// is fsynced after the rename so the new name survives a crash (D6, ADR-0021).
	dirf, err := root.Open(dir)
	if err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	defer dirf.Close()

	// The target keeps its on-disk name: the caller's final component when the
	// directory lists it exactly, else the case- or normalisation-equal entry that
	// os.SameFile matches with the target — never another name sharing its inode, so a
	// hard-linked sibling is not chosen (D6, verifier B2).
	dest := base
	if !created {
		dest = onDiskName(root, dir, dirf, base, fi)
	}

	tempRel := path.Join(dir, tempName(dest))
	f, err := root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	closed, committed := false, false
	// Every failure path closes the descriptor and removes the temporary file, so a
	// failure never leaves one behind (D6).
	defer func() {
		if !closed {
			f.Close()
		}
		if !committed {
			root.Remove(tempRel)
		}
	}()

	if !created {
		// Give the temporary file the existing target's mode through its own descriptor
		// (Root.Chmod has a check-then-act race), before any content is written, so a
		// 0600 file's content is never readable under the temporary file's 0644 (D6;
		// m1j §Changes, step 2).
		if err := f.Chmod(mode); err != nil {
			return WriteResult{}, r.classifyWrite(rel, err)
		}
	}
	if _, err := f.WriteString(p.Content); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	if !created && mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		// A write clears setuid and setgid, so a mode carrying them is set again once
		// the content is in.
		if err := f.Chmod(mode); err != nil {
			return WriteResult{}, r.classifyWrite(rel, err)
		}
	}
	if err := f.Sync(); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	if err := f.Close(); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	closed = true

	if writeSeam != nil {
		if err := writeSeam(tempRel); err != nil {
			return WriteResult{}, capability.Errorf(capability.ErrFileIOFailed, "writing %q failed", rel).
				WithCause(err.Error()).
				WithDetails(map[string]any{"path": rel})
		}
	}

	if err := root.Rename(tempRel, path.Join(dir, dest)); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}
	committed = true
	if err := dirf.Sync(); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}

	resultPath := dest
	if dir != "." {
		resultPath = dir + "/" + dest
	}
	return WriteResult{Path: resultPath, Size: int64(len(p.Content)), Created: created}, nil
}

// tempName is the temporary file's name (D6): a dot-prefixed, recognisable name in the
// target's directory, matching *.droidyard-*.tmp so a human and file.watch both know it.
func tempName(base string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "." + base + ".droidyard-" + hex.EncodeToString(b[:]) + ".tmp"
}

// onDiskName returns the target's on-disk name: base when the directory lists it
// exactly, else the case- or normalisation-equal entry that os.SameFile matches with the
// target. Only entries equal to base under case folding and Unicode normalisation are
// considered, so a hard-linked sibling that merely shares the inode is never chosen
// (D6, verifier B2).
func onDiskName(root *os.Root, dir string, d *os.File, base string, target fs.FileInfo) string {
	entries, err := d.ReadDir(-1)
	if err != nil {
		return base
	}
	for _, e := range entries {
		if e.Name() == base {
			return base
		}
	}
	for _, e := range entries {
		if !foldEqual(e.Name(), base) {
			continue
		}
		if fi, err := root.Lstat(path.Join(dir, e.Name())); err == nil && os.SameFile(target, fi) {
			return e.Name()
		}
	}
	return base
}

// foldEqual reports whether two names are the same to a volume that folds case and
// Unicode normalisation, the two ways such a volume names one file two ways (D6).
func foldEqual(a, b string) bool {
	if a == b {
		return true
	}
	return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}

// parentFile is FILE_WRONG_TYPE for a write whose parent component is a file: a file
// where a directory is needed (D3, D6).
func (r *resolver) parentFile(root *os.Root, rel string) *capability.Error {
	p, typ := firstNonDirAncestor(root, rel)
	if p == "" {
		p, typ = rel, "regular file"
	}
	return capability.Errorf(capability.ErrFileWrongType, "%q is a %s, not a directory", p, typ).
		WithDetails(map[string]any{"path": p, "type": typ}).
		WithNextStep("pass a path whose parent directory exists inside the project root")
}

// firstNonDirAncestor returns the deepest ancestor of a rooted path that exists and is
// not a directory, following symlinks as os.Root does, with its reported type.
func firstNonDirAncestor(root *os.Root, rel string) (string, string) {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		p := strings.Join(parts[:i], "/")
		fi, err := root.Stat(p)
		if err != nil {
			return "", ""
		}
		if !fi.IsDir() {
			return p, typeName(fi.Mode())
		}
	}
	return "", ""
}

// classifyWrite turns an error from a write's filesystem operations into a capability
// error. Unlike classify, a missing path is not FILE_NOT_FOUND: a write's list has no
// such code, so any ENOENT (a dangling in-root parent link, a temporary file removed
// before the rename) is FILE_IO_FAILED (D6).
func (r *resolver) classifyWrite(path string, err error) *capability.Error {
	switch {
	case err == nil:
		return nil
	case isEscape(err):
		return r.outside(path)
	default:
		return r.ioFailed(path, err)
	}
}

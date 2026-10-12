package file

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
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

// lockSeam, when non-nil, runs just before file.write takes its root's mutex, so a test
// knows a call is about to wait for it; revisionSeam runs inside the critical section,
// after the revision is compared and before the rename, so a test can hold a writer
// there deterministically (plan m2i step 2; as policy.update's seams). Production leaves
// both nil.
var lockSeam, revisionSeam atomic.Pointer[func()]

func runSeam(p *atomic.Pointer[func()]) {
	if f := p.Load(); f != nil {
		(*f)()
	}
}

// WriteLocks holds the mutex every file.write on one canonical root takes from the
// revision compare to the rename (ADR-0058 §4), so no two daemon writes interleave there.
// The daemon builds one; a Register without one gets its own.
type WriteLocks struct {
	mu     sync.Mutex
	byRoot map[string]*sync.Mutex
}

// NewWriteLocks returns an empty set.
func NewWriteLocks() *WriteLocks { return &WriteLocks{byRoot: map[string]*sync.Mutex{}} }

// of returns the root's mutex. Entries stay for the daemon's life: one per root opened.
func (l *WriteLocks) of(root string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.byRoot[root]
	if !ok {
		m = &sync.Mutex{}
		l.byRoot[root] = m
	}
	return m
}

// WriteParams is file.write's parameters.
type WriteParams struct {
	Path       string  `json:"path" jsonschema:"required" jsonschema_description:"File to write, relative to the project root; missing parent directories are created."`
	Content    string  `json:"content" jsonschema:"required" jsonschema_description:"The file's new content, UTF-8, at most 524288 bytes. The whole file is replaced."`
	IfRevision *string `json:"ifRevision,omitempty" jsonschema_description:"A revision file.read returned, or absent for a new file; if the file differs, FILE_REVISION_CONFLICT."`
}

// UnmarshalJSON refuses an explicit "ifRevision": null, which would silently turn the
// check off (as policy.update refuses it); the rest is the strict decode decodeParams
// applies.
func (p *WriteParams) UnmarshalJSON(data []byte) error {
	type wire WriteParams // no methods, so Decode does not recurse here
	var w wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		for k, v := range raw {
			if strings.EqualFold(k, "ifRevision") && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return errors.New("ifRevision must be a string: omit it to write without the check")
			}
		}
	}
	*p = WriteParams(w)
	return nil
}

// WriteResult is file.write's result (D6).
type WriteResult struct {
	Path     string `json:"path" jsonschema:"required" jsonschema_description:"The file written, relative to the project root, using the name it has on disk."`
	Size     int64  `json:"size" jsonschema:"required" jsonschema_description:"The file's size in bytes after the write."`
	Created  bool   `json:"created" jsonschema_description:"True when the write created the file; false when it replaced an existing one."`
	Revision string `json:"revision" jsonschema:"required" jsonschema_description:"The SHA-256 of the content written, the revision file.read reports for it."`
}

// absentRevision is ifRevision's value for a file that must not exist (policy's
// AbsentRevision, spelled the same).
const absentRevision = "absent"

// wellFormedRevision reports whether s has the shape of a revision: 64 lowercase hex
// digits, or "absent".
func wellFormedRevision(s string) bool {
	if s == absentRevision {
		return true
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

// revisionConflict is FILE_REVISION_CONFLICT (ADR-0058 §2): the current revision is in
// the cause only, never in details, so a blind retry with it cannot overwrite without a
// read.
func revisionConflict(rel, given, current string) *capability.Error {
	msg := fmt.Sprintf("%q is not the revision file.read returned, so %q was not written", given, rel)
	switch {
	case given == absentRevision:
		msg = fmt.Sprintf("%q exists and ifRevision is absent, so it was not written", rel)
	case wellFormedRevision(given):
		msg = fmt.Sprintf("%q changed since revision %s was read, so it was not written", rel, given)
	}
	return capability.Errorf(capability.ErrFileRevisionConflict, "%s", msg).
		WithCause("the file's current revision is " + current).
		WithDetails(map[string]any{"path": rel}).
		WithNextStep("call file.read and apply the change again")
}

// currentRevision opens the target as openRegular does (non-blocking, the descriptor's
// own fstat as the type check) and returns its revision, or absentRevision when it is
// missing. Its size is checked before a byte is read: a target over the cap is
// FILE_TOO_LARGE and is never hashed (ADR-0058 §2).
func (r *resolver) currentRevision(root *os.Root, rel string) (string, *capability.Error) {
	tooLarge := func() *capability.Error {
		limit := fmt.Sprintf("%d bytes", r.revisionMax())
		if r.revisionMax()%(1<<20) == 0 {
			limit = fmt.Sprintf("%d MiB", r.revisionMax()>>20)
		}
		return capability.Errorf(capability.ErrFileTooLarge, "%q is too large for ifRevision: file.write compares files up to %s", rel, limit).
			WithDetails(map[string]any{"path": rel})
	}
	fi, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return absentRevision, nil
	case err != nil:
		return "", r.classifyWrite(rel, err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", wrongType(rel, "symlink")
	case !fi.Mode().IsRegular():
		return "", wrongType(rel, typeName(fi.Mode()))
	case fi.Size() > r.revisionMax():
		return "", tooLarge() // before any open or read
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return absentRevision, nil
		}
		return "", r.classifyWrite(rel, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", r.classifyWrite(rel, err)
	}
	if !st.Mode().IsRegular() {
		return "", wrongType(rel, typeName(st.Mode()))
	}
	if st.Size() > r.revisionMax() {
		return "", tooLarge()
	}
	rev, ok, err := hashFile(f, r.revisionMax())
	if err != nil {
		return "", r.classifyWrite(rel, err)
	}
	if !ok {
		return "", tooLarge()
	}
	return rev, nil
}

// write serves file.write (D6): a whole-file replacement, atomic by ADR-0021 §Format's
// recipe — a temporary file in the target's directory, fsynced, renamed over the target,
// the directory fsynced — so a reader sees the old file or the new one, never a partial
// write. An existing file keeps its on-disk name and exact mode; a new file is created
// 0644 before umask; missing parents are created inside the root only. With ifRevision the
// replacement happens only if the target still has that revision (ADR-0058).
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
		// Nothing exists to match a revision: refuse before a parent directory is made.
		// The decision is still taken again under the lock.
		if p.IfRevision != nil && *p.IfRevision != absentRevision {
			return WriteResult{}, revisionConflict(rel, *p.IfRevision, absentRevision)
		}
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

	tempRel := path.Join(dir, tempName())
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

	// From the compare to the rename, one writer per root (ADR-0058 §4): the revision
	// checked is the revision replaced. A conditional write whose target already holds
	// exactly content succeeds without writing, before the revision is judged, so a
	// retried write whose first attempt landed does not conflict with itself (§2).
	contentRev := revisionOf([]byte(p.Content))
	target := path.Join(dir, dest)
	noop, cerr := func() (bool, *capability.Error) {
		mu := r.writeLocks.of(r.root)
		runSeam(&lockSeam)
		mu.Lock()
		defer mu.Unlock()
		if p.IfRevision != nil {
			// absent is settled by existence alone: a file over the compare limit is a
			// conflict, not FILE_TOO_LARGE, whose message would invite dropping the check.
			if *p.IfRevision == absentRevision {
				if fi, err := root.Lstat(target); err == nil && fi.Size() > r.revisionMax() {
					return false, revisionConflict(rel, absentRevision, "not computed: the file is over the compare limit")
				}
			}
			current, cerr := r.currentRevision(root, target)
			if cerr != nil {
				return false, cerr
			}
			if current == contentRev {
				return true, nil
			}
			if current != *p.IfRevision {
				return false, revisionConflict(rel, *p.IfRevision, current)
			}
			created = current == absentRevision
		}
		runSeam(&revisionSeam)
		if err := root.Rename(tempRel, target); err != nil {
			return false, r.classifyWrite(rel, err)
		}
		return false, nil
	}()
	if cerr != nil {
		return WriteResult{}, cerr
	}
	committed = !noop
	if noop {
		created = false
	} else if err := dirf.Sync(); err != nil {
		return WriteResult{}, r.classifyWrite(rel, err)
	}

	resultPath := dest
	if dir != "." {
		resultPath = dir + "/" + dest
	}
	return WriteResult{Path: resultPath, Size: int64(len(p.Content)), Created: created, Revision: contentRev}, nil
}

// tempName is the temporary file's name (D6): a dot-prefixed, recognisable name in the
// target's directory, matching *.droidyard-*.tmp so a human and file.watch both know it.
// It is a fixed 31 bytes whatever the target is called, so a final name of any length the
// file system allows (255 bytes) still writes; a name built from the target's would
// overflow it, and the random part keeps two writes in one directory apart.
func tempName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return ".droidyard-" + hex.EncodeToString(b[:]) + ".tmp"
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

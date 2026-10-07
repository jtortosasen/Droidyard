// Package file registers the capabilities that read and write files in a workspace's
// project root: file.list and file.read (this milestone), file.write and file.watch
// (the next ones). Every caller-provided path is resolved to a name relative to the
// root and every filesystem call goes through os.Root opened on that root, so no path
// escapes it however it is spelled — an absolute path, "..", or a symlink (D2). The
// package runs no trust check: file.* works on trusted and untrusted roots alike
// (ADR-0015 §1, §5), and its results are project content, never marked untrusted.
package file

import (
	"context"
	"os"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// Version is the API version that introduces file.list and file.read (R1).
const version = "0.9.0"

// Options configure the file capabilities.
type Options struct {
	// Root names the canonical root of an open workspace, and whether it is open. The
	// workspace registry's Describe implements it; file.* needs no trust, so it is the
	// only thing this package reads from the registry (D2).
	Root func(workspace string) (string, bool)
	// Env canonicalises the directory of an absolute path, so the "/var/…" and
	// "/private/var/…" spellings of the root agree (D2). Its Canonical is
	// platform.Env.Canonical.
	Env platform.Env
}

// Register adds file.list and file.read to the registry.
func Register(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:  "file.list",
		Kind:  capability.Query,
		Scope: capability.Workspace,
		Since: version,
		Description: "Lists one level of a directory in the project, never recursively. " + pathNote + " " +
			"Each entry carries name, type (file, dir, symlink or other), size and modification time; a symlink is reported, never followed. " +
			"Entries are sorted by name; up to limit (1 to 2000, default 500) are returned, and when more exist truncated is true and the hint names how to narrow (a directory holding more than 20000 entries yields a sorted subset). " +
			"A path outside the project root is FILE_OUTSIDE_ROOT and is never read; a missing directory is FILE_NOT_FOUND; a file where a directory is needed is FILE_WRONG_TYPE.",
		Params: ListParams{},
		Result: ListResult{},
		// The codes the handler returns (D3). WORKSPACE_NOT_OPEN comes from Start.
		Errors: []capability.ErrorCode{
			capability.ErrFileNotFound, capability.ErrFileOutsideRoot,
			capability.ErrFileWrongType, capability.ErrFileIOFailed,
			capability.ErrInvalidParams,
		},
		// A retained result drops the entries once delivered (ADR-0020 §Retention): a
		// later operation.wait returns the path and the truncation flag, and the caller
		// lists again.
		Slim: func(result any) any {
			if r, ok := result.(ListResult); ok {
				r.Entries = []Entry{}
				r.Hint = "entries dropped after delivery; call file.list again"
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.list(call.Params.(ListParams))
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:  "file.read",
		Kind:  capability.Query,
		Scope: capability.Workspace,
		Since: version,
		Description: "Reads a byte range of a text file in the project, paging exactly as artifact.read does. " + pathNote + " " +
			"Returns path, size, modTime, offset, nextOffset, text and truncated: text ends at the last complete line within limit (default 16384 bytes, maximum 262144), a negative offset counts back from the end and a negative offset that lands mid-line starts at the next line, and an offset at or past the end is empty text. " +
			"A file whose window holds a NUL byte or invalid UTF-8 (a rune cut by the window's end excepted) is binary: binary is true and text is omitted, size is kept. " +
			"A path outside the project root is FILE_OUTSIDE_ROOT and is never read; a missing file is FILE_NOT_FOUND; a directory, FIFO, socket or device is FILE_WRONG_TYPE.",
		Params: ReadParams{},
		Result: ReadResult{},
		Errors: []capability.ErrorCode{
			capability.ErrFileNotFound, capability.ErrFileOutsideRoot,
			capability.ErrFileWrongType, capability.ErrFileIOFailed,
			capability.ErrInvalidParams,
		},
		// A retained result drops the text once delivered (ADR-0020 §Retention): a later
		// operation.wait returns the metadata, and the caller reads the range again.
		Slim: func(result any) any {
			if r, ok := result.(ReadResult); ok {
				r.Text = nil
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.read(call.Params.(ReadParams))
		},
	})
}

// pathNote is the sentence both descriptions share: how the path is named and where
// containment is decided (D2, D10).
const pathNote = "The path is relative to the project root and slash-separated; \".\" or an empty path is the root; an absolute path inside the project root is accepted and treated the same. " +
	"Every path stays inside the project root: an absolute path outside it, \"..\", a symlink (intermediate or final) that would leave it, and any symlink with an absolute target, even one pointing inside the root, fail FILE_OUTSIDE_ROOT."

// resolver builds the per-call resolver from the call's workspace, or fails when the
// workspace is not open (which Start has already rejected; kept for a direct call).
func (o Options) resolver(call *capability.Call) (*resolver, *capability.Error) {
	root, ok := "", false
	if o.Root != nil {
		root, ok = o.Root(call.Workspace)
	}
	if !ok || root == "" {
		return nil, capability.Errorf(capability.ErrWorkspaceNotOpen, "workspace %q is not open", call.Workspace).
			WithNextStep("call workspace.open on it, then retry")
	}
	return &resolver{root: root, env: o.Env}, nil
}

// openRoot opens the canonical root as an os.Root. Containment is the root's, so every
// operation from here on — an open, a stat, a read — cannot leave it, with no
// check-then-act race (D2).
func (r *resolver) openRoot() (*os.Root, *capability.Error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, r.ioFailed(r.root, err)
	}
	return root, nil
}

// openRegular opens a regular file read-only through root. A directory, FIFO, socket or
// device is refused with FILE_WRONG_TYPE without blocking: the path is stat'ed first
// (a stat never blocks), and the open is non-blocking with the descriptor's own fstat
// as the authoritative type check, so a path swapped for a FIFO between the two cannot
// block either (D5).
func (r *resolver) openRegular(root *os.Root, rel string) (*os.File, os.FileInfo, *capability.Error) {
	if fi, err := root.Stat(rel); err != nil {
		return nil, nil, r.classify(rel, err)
	} else if !fi.Mode().IsRegular() {
		return nil, nil, wrongType(rel, typeName(fi.Mode()))
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, r.classify(rel, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, r.classify(rel, err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, wrongType(rel, typeName(fi.Mode()))
	}
	return f, fi, nil
}

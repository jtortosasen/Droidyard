// Package file registers the capabilities that read and write files in a workspace's
// project root: file.list, file.read, file.write and file.watch (plan m1j). Every
// caller-provided path is resolved to a name relative to the
// root and every filesystem call goes through os.Root opened on that root, so no path
// escapes it however it is spelled — an absolute path, "..", or a symlink (D2). The
// package runs no trust check: file.* works on trusted and untrusted roots alike
// (ADR-0015 §1, §5), and its results are project content, never marked untrusted.
package file

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// version is the API version that introduces file.list, file.read and file.write (R1).
// file.watch, in the same milestone, keeps it.
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
	// Bus is the daemon's event bus: file.watch publishes each tick's differences on it,
	// to its own subscription alone (D8). Nil (the golden registry's descriptors) makes a
	// watch answer SESSION_REQUIRED.
	Bus *event.Bus
	// Watchers is the daemon's live-poller set, so a workspace close can stop that
	// workspace's watches (D9). Nil makes a watch answer SESSION_REQUIRED, as Bus does.
	Watchers *Watchers
	// Tick is the poller's interval between walks; <= 0 is watchInterval's one second. A
	// test shortens it (plan m1j step 4).
	Tick time.Duration
}

// Register adds file.list, file.read and file.write to the registry.
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
			"A page holding a NUL byte or invalid UTF-8 is binary: binary is true and text is omitted, while size, offset and nextOffset are kept so a caller can page past it. " +
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

	reg.MustRegister(capability.Descriptor{
		Name:  "file.write",
		Kind:  capability.Command,
		Scope: capability.Workspace,
		Since: version,
		// Repeating the call with the same arguments has no additional effect beyond the
		// first write (mcp-tool-surface.md §Annotations); it is destructive, which is a
		// command's default, so no override and no reason are needed.
		Idempotent: true,
		Description: "Writes a whole text file into the project, replacing any existing file atomically: a reader sees either the old file or the new one, never a partial write. " + writePathNote + " " +
			"Missing parent directories are created. An existing file keeps its name on disk and its permission bits (setuid, setgid and sticky included; owner, ACLs and extended attributes are not kept); a new file is created 0644 before umask. Content is UTF-8, at most 512 KiB. " +
			"A hard link to a replaced file keeps the old content, and a write under .git/ is allowed. " +
			"An existing file without its owner-write bit is FILE_IO_FAILED; a directory, a FIFO, a socket, a device or a symlink where the file goes is FILE_WRONG_TYPE.",
		Params: WriteParams{},
		Result: WriteResult{},
		// The codes the handler returns (D3, D6). It never returns FILE_NOT_FOUND: a
		// missing target is created and any ENOENT during the write is FILE_IO_FAILED.
		// WORKSPACE_NOT_OPEN comes from Start.
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrFileWrongType,
			capability.ErrFileIOFailed, capability.ErrInvalidParams,
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.write(call.Params.(WriteParams))
		},
	})

	// A watch creates a subscription, so it is a command, not a query (mcp-tool-surface.md
	// §Annotations: "a query that writes is a Kind bug"); it is streaming, so it needs a
	// WebSocket session (ADR-0010). It is not idempotent (each call opens a new
	// subscription) and, being a command, is destructive by default: the override and its
	// reason say it changes no file (D7).
	notDestructiveWatch := false
	reg.MustRegister(capability.Descriptor{
		Name:         "file.watch",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		Statefulness: capability.Streaming,
		Since:        version,
		Description: "Watches a directory in the project and reports what changes under it, WebSocket-only. " + watchPathNote + " " +
			"The result carries subscriptionId and the events then arrive on the calling session, on topic file.changes, each naming this subscription and this call's operation: type changes with {path, changes [{path, kind: created | modified | deleted, type}], truncated} (at most 1000 changes per event) and, once and last, type stopped {path, reason}: workspaceClosed when the workspace closes, pathGone when the watched directory is gone (its entries reported deleted first), unreadable when it can no longer be read. " +
			"The watch polls: the directory is walked at the call, then once a second, and a change is an entry's type, size or modification time differing. " +
			"Each walk records at most 20000 entries (watched, truncated), and a truncated tick reports no deletions; a subdirectory that cannot be read keeps its last known entries; excluded are .git/, .gradle/, .idea/, .kotlin/, .cxx/, a build/ directory beside a build.gradle or build.gradle.kts, and file.write's temporary files. " +
			"A path outside the project root is FILE_OUTSIDE_ROOT and is never read; a missing directory is FILE_NOT_FOUND; a file where a directory is needed is FILE_WRONG_TYPE.",
		Params: WatchParams{},
		Result: WatchResult{},
		// The codes the handler returns (D3, D7). WORKSPACE_NOT_OPEN and SESSION_REQUIRED
		// come from Start.
		Errors: []capability.ErrorCode{
			capability.ErrFileNotFound, capability.ErrFileOutsideRoot,
			capability.ErrFileWrongType, capability.ErrFileIOFailed,
			capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructiveWatch,
			Reason:      "adds a subscription to the calling session; changes no file",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.watch(ctx, call)
		},
	})
}

// pathNote is the sentence both descriptions share: how the path is named and where
// containment is decided (D2, D10).
const pathNote = "The path is relative to the project root and slash-separated; \".\" or an empty path is the root; an absolute path inside the project root is accepted and resolved to the same name (a /var-style alias of the root resolves intermediate links on the way). " +
	"Every path stays inside the project root: an absolute path outside it, \"..\", a symlink (intermediate or final) that would leave it, and any symlink with an absolute target, even one pointing inside the root, fail FILE_OUTSIDE_ROOT."

// watchPathNote is file.watch's path rule: as pathNote, but the path must name a
// directory and the walk reports a symlink without descending it (D7).
const watchPathNote = "The path is relative to the project root and slash-separated; \".\" or an empty path is the root; an absolute path inside the project root is accepted and resolved to the same name (a /var-style alias of the root resolves intermediate links on the way). " +
	"Every path stays inside the project root: an absolute path outside it, \"..\", and any symlink (intermediate or final) that would leave it fail FILE_OUTSIDE_ROOT. The walk records a symlink as a symlink but never descends into it."

// writePathNote is file.write's path rule: as pathNote for the directories on the way,
// but a symlink as the final component is never followed, wherever it points, so it is
// FILE_WRONG_TYPE rather than FILE_OUTSIDE_ROOT (D6).
const writePathNote = "The path is relative to the project root and slash-separated; an absolute path inside the project root is accepted and resolved to the same name (a /var-style alias of the root resolves intermediate links on the way). " +
	"An absolute path outside the root, \"..\", and an intermediate symlink that would leave the root or has an absolute target fail FILE_OUTSIDE_ROOT; a symlink as the final component is FILE_WRONG_TYPE wherever it points, and is never followed."

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

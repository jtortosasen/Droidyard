// Package file registers the capabilities that read, write and change files in a
// workspace's project root: file.list, file.read, file.write and file.watch (plan m1j),
// and the tree's operations, file.create, file.move, file.copy, file.delete and
// file.restore (ADR-0053, ADR-0054). Every
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

// opsVersion is the API version that introduces the tree's operations (ADR-0053; plan
// m2g D47): the later ones of the milestone ride on it.
const opsVersion = "0.19.0"

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
	// CopyMaxEntries and CopyMaxBytes override file.copy's per-item limits (20,000
	// entries, 1 GiB; plan m2g D48); <= 0 keeps them. A test scales them down.
	CopyMaxEntries int
	CopyMaxBytes   int64
	// Trash holds what file.delete removes and file.restore returns (ADR-0054). Nil (the
	// golden registry's descriptors) makes both answer FILE_IO_FAILED.
	Trash *Trash
	// WriteLocks serialises file.write per project root (ADR-0058 §4). Nil (the golden
	// registry's descriptors, a test) makes a set private to that Register.
	WriteLocks *WriteLocks
	// RevisionMax overrides the largest file that has a revision (8 MiB); <= 0 keeps it.
	// A test scales it down.
	RevisionMax int64
}

// Register adds file.list, file.read and file.write to the registry.
func Register(reg *capability.Registry, opts Options) {
	if opts.WriteLocks == nil {
		opts.WriteLocks = NewWriteLocks()
	}
	reg.MustRegister(capability.Descriptor{
		Name:        "file.list",
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       version,
		Description: "Lists one level of a directory in the project, never recursively, with name, type, size and modification time per entry. The path is relative to the project root; a path that leaves it, or a symlink that would, is FILE_OUTSIDE_ROOT.",
		Params:      ListParams{},
		Result:      ListResult{},
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
		Name:        "file.read",
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       version,
		Description: "Reads a byte range of a text file in the project, paging as artifact.read does: text ends at a complete line, nextOffset continues, a negative offset reads from the end. A binary file reports binary and no text, and a path outside the project root is FILE_OUTSIDE_ROOT.",
		Params:      ReadParams{},
		Result:      ReadResult{},
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
		// first write (mcp.md §Annotations); it is destructive, which is a
		// command's default, so no override and no reason are needed.
		Idempotent:  true,
		Description: "Writes a whole UTF-8 text file (at most 512 KiB) into the project, atomically replacing any existing file and creating missing parent directories. The path is relative to the project root; a path that leaves it, or a symlink as the final component, is refused.",
		Params:      WriteParams{},
		Result:      WriteResult{},
		// The codes the handler returns (D3, D6). It never returns FILE_NOT_FOUND: a
		// missing target is created and any ENOENT during the write is FILE_IO_FAILED.
		// WORKSPACE_NOT_OPEN comes from Start.
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrFileWrongType,
			capability.ErrFileIOFailed, capability.ErrInvalidParams,
			capability.ErrFileRevisionConflict, capability.ErrFileTooLarge,
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.write(call.Params.(WriteParams))
		},
	})

	// The tree's operations (ADR-0053). Neither replaces anything, so neither is destructive
	// whatever else a command's default says; both are workspace-scoped commands for every
	// audience, run no trust check, hold no build lock (as file.write) and mark nothing
	// untrusted.
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:        "file.create",
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		Since:       opsVersion,
		Idempotent:  true,
		Description: "Creates an empty file or a folder in the project, making missing parent folders; it never replaces, and an existing path is FILE_EXISTS. The path is relative to the project root.",
		Params:      CreateParams{},
		Result:      CreateResult{},
		// The codes the handler returns. WORKSPACE_NOT_OPEN comes from Start.
		Errors: []capability.ErrorCode{
			capability.ErrFileExists, capability.ErrFileOutsideRoot,
			capability.ErrFileWrongType, capability.ErrFileIOFailed,
			capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "creates only; an existing entry is refused, never replaced",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.create(call.Params.(CreateParams))
		},
	})

	// A batch: Errors lists the codes that fail the whole call, decided from the text of
	// the request alone; an item's own code is the enum of MovedItem.Code (ADR-0053 §2).
	reg.MustRegister(capability.Descriptor{
		Name:  "file.move",
		Kind:  capability.Command,
		Scope: capability.Workspace,
		Since: opsVersion,
		// Not idempotent: a swap batch [a→t, b→a, t→b] run twice swaps back, so a client
		// must not retry it blindly.
		Description: "Moves or renames up to 1000 files or folders, a rename being a move inside one folder; nothing is replaced. The call completes with each item's outcome: a taken target is FILE_EXISTS, or a free name with onCollision keepBoth.",
		Params:      MoveParams{},
		Result:      MoveResult{},
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "never replaces an existing entry; a move back undoes it",
		},
		Text: capability.TextOf(moveText),
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.move(ctx, call.Params.(MoveParams))
		},
	})

	// A batch like file.move, and long-running: a copy takes as long as its bytes (ADR-0053
	// §1). Not idempotent: each call makes a new copy, and keepBoth another free name.
	reg.MustRegister(capability.Descriptor{
		Name:        "file.copy",
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		LongRunning: true,
		Since:       opsVersion,
		Description: "Copies up to 1000 files or folders, a folder whole and a symlink as a link; nothing is replaced, and a failed or cancelled copy leaves nothing. A taken target is FILE_EXISTS, or a free name such as \"a copy.kt\" with onCollision keepBoth; an item over 20000 entries or 1 GiB is FILE_TOO_LARGE.",
		Params:      CopyParams{},
		Result:      CopyResult{},
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "never replaces an existing entry; deleting the copy undoes it",
		},
		Text: capability.TextOf(copyText),
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.copy(ctx, call.Params.(CopyParams))
		},
	})

	// The trash (ADR-0054). A batch like file.copy, and long-running: a delete across
	// volumes takes as long as its bytes. file.delete is the one destructive file command
	// (the trash forgets), which is a command's default; it is idempotent, a repeat being
	// FILE_NOT_FOUND items. Errors lists the call-level codes; FILE_IO_FAILED is a daemon
	// built without a trash.
	reg.MustRegister(capability.Descriptor{
		Name:        "file.delete",
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		LongRunning: true,
		Since:       opsVersion,
		Idempotent:  true,
		Description: "Deletes up to 1000 files or folders into the workspace's trash, kept up to 7 days within 2 GiB per project; an item's trashId restores it with file.restore. A symlink goes as the link, and across volumes an item over 20000 entries or 1 GiB is FILE_TOO_LARGE.",
		Params:      DeleteParams{},
		Result:      DeleteResult{},
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrInvalidParams, capability.ErrFileIOFailed,
		},
		Text: capability.TextOf(deleteText),
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.delete(ctx, call.Params.(DeleteParams))
		},
	})

	reg.MustRegister(capability.Descriptor{
		Name:        "file.restore",
		Kind:        capability.Command,
		Scope:       capability.Workspace,
		LongRunning: true,
		Since:       opsVersion,
		Idempotent:  true,
		Description: "Puts trashed items back at their paths, making missing folders and replacing nothing: a taken path is FILE_EXISTS, and an id this workspace's trash lacks is FILE_NOT_IN_TRASH. It works after a daemon restart.",
		Params:      RestoreParams{},
		Result:      RestoreResult{},
		Errors: []capability.ErrorCode{
			capability.ErrFileOutsideRoot, capability.ErrInvalidParams, capability.ErrFileIOFailed,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "never replaces an existing entry; deleting the item again undoes it",
		},
		Text: capability.TextOf(restoreText),
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			r, err := opts.resolver(call)
			if err != nil {
				return nil, err
			}
			return r.restore(ctx, call.Params.(RestoreParams))
		},
	})

	// A watch creates a subscription, so it is a command, not a query (mcp.md
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
		Description:  "Watches a directory in the project (WebSocket sessions only) and reports created, modified and deleted entries as events on topic file.changes until the workspace closes or the directory is gone. It polls once a second and skips .git/, .gradle/, .idea/ and build outputs.",
		Params:       WatchParams{},
		Result:       WatchResult{},
		// The stream (ADR-0049 §2): the client is generated from it (ADR-0041 §4).
		Events: &capability.Events{
			Topic: TopicFileChanges,
			Types: map[string]any{TypeChanges: ChangesPayload{}, TypeStopped: StoppedPayload{}},
		},
		// A retained result drops the baseline once delivered (ADR-0020 §Retention, ADR-0049
		// §1): a later operation.wait returns the count and the flags, and the caller
		// watches again for a baseline.
		Slim: func(result any) any {
			if r, ok := result.(WatchResult); ok {
				r.Entries = []WatchedEntry{}
				return r
			}
			return result
		},
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

// The path rules, which the tool descriptions no longer repeat (ADR-0036 Decision 1):
// a path is relative to the project root and slash-separated, "." or an empty path is the
// root, and an absolute path inside the root is accepted and resolved to the same name
// (a /var-style alias of the root resolves intermediate links on the way). Every path
// stays inside the root: an absolute path outside it, "..", a symlink (intermediate or
// final) that would leave it, and any symlink with an absolute target, even one pointing
// inside the root, fail FILE_OUTSIDE_ROOT. file.watch records a symlink as a symlink and
// never descends into it (D7). file.write never follows a symlink that is the final
// component, wherever it points, so that is FILE_WRONG_TYPE rather than
// FILE_OUTSIDE_ROOT (D6).

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
	return &resolver{root: root, env: o.Env, maxCopyEntries: o.CopyMaxEntries, maxCopyBytes: o.CopyMaxBytes,
		workspace: call.Workspace, trash: o.Trash, writeLocks: o.WriteLocks, maxRevision: o.RevisionMax}, nil
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

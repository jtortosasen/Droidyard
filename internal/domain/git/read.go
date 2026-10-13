package git

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/file"
)

// ReadParams is git.read's parameters.
type ReadParams struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"The file to read, relative to the project root. An absolute path inside the root is accepted."`
	Base string `json:"base,omitempty" jsonschema:"pattern=^(HEAD|[0-9a-f]{40}|[0-9a-f]{64})$" jsonschema_description:"What to read the file at: HEAD, or a commit's full object id. Absent means HEAD."`
	Root string `json:"root,omitempty" jsonschema:"maxLength=4096" jsonschema_description:"A submodule's path as git.submodules lists it; absent or . is the project's repository. Path must lie in it."`
}

// ReadResult is git.read's result: a path's text at the base. Text is absent unless the
// path exists there and is neither binary nor too large, and, once the result has been
// delivered, for a retained one (Slim).
type ReadResult struct {
	Path     string  `json:"path" jsonschema:"required" jsonschema_description:"The file read, relative to the project root."`
	Base     string  `json:"base" jsonschema:"required,pattern=^(HEAD|[0-9a-f]{40}|[0-9a-f]{64})$" jsonschema_description:"What it was read at: HEAD, or the commit's full object id."`
	Exists   bool    `json:"exists" jsonschema:"required" jsonschema_description:"False when the base holds no such file: a new file, an untracked one, or a repository with no commit yet."`
	Size     int64   `json:"size" jsonschema:"required" jsonschema_description:"The file's size in bytes at the base; 0 when it does not exist."`
	Binary   bool    `json:"binary" jsonschema:"required" jsonschema_description:"True when the file holds a NUL byte or invalid UTF-8; text is then absent."`
	TooLarge bool    `json:"tooLarge" jsonschema:"required" jsonschema_description:"True when the file is over 512 KiB, the editor's save limit; text is then absent."`
	Text     *string `json:"text,omitempty" jsonschema_description:"The file's whole text at the base, exactly as committed."`
}

func registerRead(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "git.read",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Since:        version,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Reads a file's whole text as committed at HEAD, or at the commit base names by its full object id, for the diff: a file the base does not hold is exists false, and a binary file or one over 512 KiB is reported without its text. A path outside the project root is FILE_OUTSIDE_ROOT, and no git runs for it.",
		Params:       ReadParams{},
		Result:       ReadResult{},
		// The codes the handler returns (ADR-0066 §4). WORKSPACE_NOT_OPEN comes from Start.
		// WORKSPACE_UNTRUSTED is an untrusted root whose git filter drivers cannot all be
		// switched off (§3).
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrFileOutsideRoot,
			capability.ErrFileWrongType, capability.ErrInvalidParams,
		},
		// A retained result drops the text once delivered (ADR-0020 §Retention): a later
		// operation.wait returns the metadata, and the caller reads again.
		Slim: func(result any) any {
			if r, ok := result.(ReadResult); ok {
				r.Text = nil
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.read(ctx, call)
		},
	})
}

// read serves git.read. The path never reaches git's argv: it goes, as the object name
// <base>:<prefix><path>, on the stdin of cat-file, which runs no filter and so returns
// the committed bytes exactly. The base is HEAD or a full commit object id, checked here
// before any git starts, and an oid is then confirmed to be a commit (a tree's oid would
// name a path inside that tree). Containment is lexical and decided before any git starts.
// With a root (roots.go) the path is still the workspace's and must lie inside the root; git
// reads it in the submodule, from the submodule's own top.
func (o Options) read(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(ReadParams)
	base := BaseHEAD
	if p.Base != "" {
		base = p.Base
	}
	if base != BaseHEAD && !isOid(base) {
		return nil, capability.Errorf(capability.ErrInvalidParams, "base %q is neither %s nor a full commit object id", p.Base, BaseHEAD).
			WithNextStep("omit base, pass HEAD, or pass a commit's full hex object id (as git.log returned it), not a branch name or a short id")
	}
	sub, cerr := parseRoot(p.Root)
	if cerr != nil {
		return nil, cerr
	}
	ws, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	rel, cerr := file.ResolveLexical(ws, o.Env, p.Path)
	if cerr != nil {
		return nil, cerr
	}
	// The path is the project's, and must lie inside the root it is read in: decided
	// lexically, before any git starts. inRoot is the path from the root's own top.
	inRoot, inside := gitRoot{rel: sub}.inside(rel)
	if !inside {
		return nil, capability.Errorf(capability.ErrFileOutsideRoot, "%q is outside the root %q", p.Path, sub).
			WithDetails(map[string]any{"path": rel, "root": sub}).
			WithNextStep("pass a path inside the submodule the root names, or read it with its own root")
	}
	// cat-file reads one object name per line, so a line break would be a second name,
	// and a carriage return before one is stripped by git. The resolver has refused NUL.
	if strings.ContainsAny(rel, "\n\r\x00") {
		return nil, capability.NewError(capability.ErrInvalidParams, "the path contains a line break").
			WithNextStep("git.read cannot read a path with a line break in its name")
	}
	r, cerr := o.runner()
	if cerr != nil {
		return nil, cerr
	}
	trusted := o.trusted(ws)
	root, err := o.resolveRoot(ctx, r, ws, trusted, sub)
	if err != nil {
		return nil, err
	}
	// The workspace's folder below its repository's top. A submodule root is the top of its
	// own repository (resolveRoot proved it), so the path from its top is inRoot.
	prefix := ""
	if root.rel == "" {
		prefix, err = r.prefix(ctx, root.dir, trusted)
		if err != nil {
			return nil, root.mapError(err)
		}
	}
	if base != BaseHEAD {
		i, cerr := o.firstNotCommit(ctx, r, root, trusted, []string{base})
		if cerr != nil {
			return nil, cerr
		}
		if i >= 0 {
			return nil, capability.NewError(capability.ErrInvalidParams, "base is not a commit of this repository").
				WithNextStep("pass HEAD, or the full object id of a commit git.log returned")
		}
	}
	out := ReadResult{Path: rel, Base: base}
	name := base + ":" + prefix + inRoot
	if strings.ContainsAny(name, "\n\r") {
		return nil, capability.NewError(capability.ErrInvalidParams, "the project root's path inside the repository contains a line break").
			WithNextStep("git.read cannot name a path under a folder with a line break in its name")
	}

	// First the object's kind and size, so a blob of a gigabyte is never piped through the
	// daemon; then, for a small blob, its bytes.
	stdin := []byte(name + "\n")
	res, err := r.run(ctx, root.dir, trusted, stdin, "cat-file", "--batch-check")
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	kind, size, found, perr := parseHeader(res.Stdout)
	if perr != nil {
		return nil, perr
	}
	if !found {
		return out, nil
	}
	if kind != "blob" {
		return nil, wrongType(out.Path, kind, base)
	}
	out.Exists, out.Size = true, size
	if size > file.EditableLimit {
		out.TooLarge = true
		return out, nil
	}
	res, err = r.run(ctx, root.dir, trusted, stdin, "cat-file", "--batch")
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	kind, size, found, perr = parseHeader(res.Stdout)
	switch {
	case perr != nil:
		return nil, perr
	case !found:
		// The file is gone from the base between the two calls: HEAD moved (a commit base
		// is immutable, so only HEAD can).
		return ReadResult{Path: out.Path, Base: base}, nil
	case kind != "blob":
		return nil, wrongType(out.Path, kind, base)
	}
	out.Size = size
	if size > file.EditableLimit {
		out.TooLarge = true
		return out, nil
	}
	body := res.Stdout[bytes.IndexByte(res.Stdout, '\n')+1:]
	if int64(len(body)) < size {
		return nil, capability.NewError(CodeFailed, "git returned less of the file than it announced")
	}
	body = body[:size]
	// file.read's binary rule, applied to the whole blob; a blob that ends inside a rune
	// is not text the JSON could carry intact either.
	if file.LooksBinary(body) || !utf8.Valid(body) {
		out.Binary = true
		return out, nil
	}
	text := string(body)
	out.Text = &text
	return out, nil
}

// parseHeader reads the first line of cat-file's --batch-check or --batch output:
// "<oid> <type> <size>" for an object and "<name> missing" for none. The two are told
// apart by the oid, which a name beginning "<base>:" never is.
func parseHeader(out []byte) (kind string, size int64, found bool, err error) {
	line, _, _ := strings.Cut(string(out), "\n")
	f := strings.Fields(line)
	if len(f) == 3 && isOid(f[0]) {
		n, perr := strconv.ParseInt(f[2], 10, 64)
		if perr == nil && n >= 0 {
			return f[1], n, true, nil
		}
	}
	if strings.HasSuffix(line, " missing") {
		return "", 0, false, nil
	}
	return "", 0, false, capability.NewError(CodeFailed, "git answered something this version does not understand").
		WithCause(truncate(line, 200))
}

func isOid(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// wrongType is FILE_WRONG_TYPE: the base (HEAD, or a commit) holds a directory, or a
// submodule, at the path.
func wrongType(path, kind, base string) *capability.Error {
	at := "HEAD"
	if base != BaseHEAD {
		at = "the commit"
	}
	typ := kind
	switch kind {
	case "tree":
		typ = "directory"
	case "commit":
		typ = "submodule"
	}
	return capability.Errorf(capability.ErrFileWrongType, "%q is a %s at %s, not a regular file", path, typ, at).
		WithDetails(map[string]any{"path": path, "type": typ}).
		WithNextStep("pass a path to a regular file")
}

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
	Base string `json:"base,omitempty" jsonschema:"enum=HEAD" jsonschema_description:"What to read the file at. Only HEAD exists; absent means HEAD."`
}

// ReadResult is git.read's result: a path's text at the base. Text is absent unless the
// path exists there and is neither binary nor too large, and, once the result has been
// delivered, for a retained one (Slim).
type ReadResult struct {
	Path     string  `json:"path" jsonschema:"required" jsonschema_description:"The file read, relative to the project root."`
	Base     string  `json:"base" jsonschema:"required,enum=HEAD" jsonschema_description:"What it was read at."`
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
		Description:  "Reads a file's whole text as committed at HEAD, for the diff: a file HEAD does not hold is exists false, and a binary file or one over 512 KiB is reported without its text. A path outside the project root is FILE_OUTSIDE_ROOT, and no git runs for it.",
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
// HEAD:<prefix><path>, on the stdin of cat-file, which runs no filter and so returns
// the committed bytes exactly. Containment is lexical and decided before any git starts.
func (o Options) read(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(ReadParams)
	if p.Base != "" && p.Base != BaseHEAD {
		return nil, capability.Errorf(capability.ErrInvalidParams, "base %q is not supported; only %s is", p.Base, BaseHEAD).
			WithNextStep("omit base, or pass HEAD")
	}
	root, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	rel, cerr := file.ResolveLexical(root, o.Env, p.Path)
	if cerr != nil {
		return nil, cerr
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
	trusted := o.trusted(root)

	prefix, err := r.prefix(ctx, root, trusted)
	if err != nil {
		return nil, mapError(root, err)
	}
	out := ReadResult{Path: rel, Base: BaseHEAD}
	if rel == "." {
		rel = ""
	}
	name := BaseHEAD + ":" + prefix + rel
	if strings.ContainsAny(name, "\n\r") {
		return nil, capability.NewError(capability.ErrInvalidParams, "the project root's path inside the repository contains a line break").
			WithNextStep("git.read cannot name a path under a folder with a line break in its name")
	}

	// First the object's kind and size, so a blob of a gigabyte is never piped through the
	// daemon; then, for a small blob, its bytes.
	stdin := []byte(name + "\n")
	res, err := r.run(ctx, root, trusted, stdin, "cat-file", "--batch-check")
	if err != nil {
		return nil, mapError(root, err)
	}
	if res.ExitCode != 0 {
		return nil, mapError(root, gitError(res))
	}
	kind, size, found, perr := parseHeader(res.Stdout)
	if perr != nil {
		return nil, perr
	}
	if !found {
		return out, nil
	}
	if kind != "blob" {
		return nil, wrongType(out.Path, kind)
	}
	out.Exists, out.Size = true, size
	if size > file.EditableLimit {
		out.TooLarge = true
		return out, nil
	}
	res, err = r.run(ctx, root, trusted, stdin, "cat-file", "--batch")
	if err != nil {
		return nil, mapError(root, err)
	}
	if res.ExitCode != 0 {
		return nil, mapError(root, gitError(res))
	}
	kind, size, found, perr = parseHeader(res.Stdout)
	switch {
	case perr != nil:
		return nil, perr
	case !found:
		// HEAD moved between the two calls, and the file is gone from it.
		return ReadResult{Path: out.Path, Base: BaseHEAD}, nil
	case kind != "blob":
		return nil, wrongType(out.Path, kind)
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
// apart by the oid, which a name beginning "HEAD:" never is.
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

// wrongType is FILE_WRONG_TYPE: HEAD holds a directory, or a submodule, at the path.
func wrongType(path, kind string) *capability.Error {
	typ := kind
	switch kind {
	case "tree":
		typ = "directory"
	case "commit":
		typ = "submodule"
	}
	return capability.Errorf(capability.ErrFileWrongType, "%q is a %s at HEAD, not a regular file", path, typ).
		WithDetails(map[string]any{"path": path, "type": typ}).
		WithNextStep("pass a path to a regular file")
}

package git

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The limits of one commit's details (ADR-0069 §4; plan m2n step 7).
const (
	// maxMessage is the most bytes of a commit message git.show returns.
	maxMessage = 64 << 10
	// maxShowFiles is the most files git.show lists.
	maxShowFiles = 5000
	// epoch stands for a commit time git printed in no RFC 3339 form: a crafted date is
	// no reason to refuse the commit's other details.
	epoch = "1970-01-01T00:00:00Z"
)

// ShowParams is git.show's parameters.
type ShowParams struct {
	Oid  string `json:"oid" jsonschema:"required,pattern=^([0-9a-f]{40}|[0-9a-f]{64})$" jsonschema_description:"The commit's full object id, as git.log returned it."`
	Base string `json:"base,omitempty" jsonschema:"pattern=^([0-9a-f]{40}|[0-9a-f]{64})$" jsonschema_description:"A commit (full object id) to list the files against, in place of the first parent."`
	Root string `json:"root,omitempty" jsonschema:"maxLength=4096" jsonschema_description:"A submodule's path in the project (a gitlink of its parent, initialised); absent or . is the project's own repository."`
}

// ShowPerson is an author or a committer, as the commit records them.
type ShowPerson struct {
	Name  string `json:"name" jsonschema:"required" jsonschema_description:"The name as the commit records it."`
	Email string `json:"email" jsonschema:"required" jsonschema_description:"The e-mail as the commit records it."`
	Time  string `json:"time" jsonschema:"required" jsonschema_description:"The time in RFC 3339, in the UTC offset the commit recorded; 1970-01-01T00:00:00Z when git prints the commit's date in no RFC 3339 form."`
}

// ShowFile is one file a commit changed.
type ShowFile struct {
	Path     string `json:"path" jsonschema:"required" jsonschema_description:"The file's path after the commit, relative to the project root."`
	OrigPath string `json:"origPath,omitempty" jsonschema_description:"A renamed file's path before the commit, relative to the project root."`
	Status   string `json:"status" jsonschema:"required,enum=added,enum=modified,enum=deleted,enum=renamed" jsonschema_description:"What the commit did to the file. A change of kind (a file made a link) is modified."`
	Added    *int   `json:"added,omitempty" jsonschema:"minimum=0" jsonschema_description:"Lines added, as git counts them; absent for a binary file, and for the files listed after git's output was cut (truncated is then true)."`
	Removed  *int   `json:"removed,omitempty" jsonschema:"minimum=0" jsonschema_description:"Lines removed, as git counts them; absent for a binary file, and for the files listed after git's output was cut (truncated is then true)."`
	Binary   bool   `json:"binary" jsonschema:"required" jsonschema_description:"True when git counts no lines because the file is binary."`
}

// ShowResult is git.show's result. Message and Files drop out of a result returned again
// after delivery (Slim). Every text is the repository's own (ADR-0069 §6).
type ShowResult struct {
	Oid              string     `json:"oid" jsonschema:"required" jsonschema_description:"The commit's full object id."`
	Parents          []string   `json:"parents" jsonschema:"required" jsonschema_description:"The parents' object ids, first parent first; empty for a root commit."`
	Author           ShowPerson `json:"author" jsonschema:"required" jsonschema_description:"Who wrote the change, and when."`
	Committer        ShowPerson `json:"committer" jsonschema:"required" jsonschema_description:"Who recorded it, and when."`
	Message          string     `json:"message" jsonschema:"required" jsonschema_description:"The whole commit message, at most 64 KiB. Empty in a result returned again after delivery."`
	MessageTruncated bool       `json:"messageTruncated" jsonschema:"required" jsonschema_description:"True when the message is longer than the 64 KiB returned."`
	Files            []ShowFile `json:"files" jsonschema:"required" jsonschema_description:"The files the commit changed under the project root, by path, at most 5000: against the first parent, so a merge shows what it brought into its branch; a root commit against the empty tree. Empty in a result returned again after delivery."`
	Truncated        bool       `json:"truncated" jsonschema:"required" jsonschema_description:"True when the commit changed more files than listed: past 5000, or past the 4 MiB git's output is read to."`
}

func registerShow(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "git.show",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Since:        sinceLog,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Reads one commit for the UI's expanded row: its author, committer, times, parents and message, and the files it changed under the project root with the lines added and removed, against its first parent (a merge shows what it brought in; a root commit, everything). The commit is named by its full object id.",
		Params:       ShowParams{},
		Result:       ShowResult{},
		// The codes the handler returns (ADR-0069 §4). WORKSPACE_NOT_OPEN comes from Start.
		// WORKSPACE_UNTRUSTED is an untrusted root whose git filter drivers cannot all be
		// switched off (ADR-0066 §3). REQUEST_INVALID_PARAMS is an oid that is not a full
		// object id of a commit of this repository, or a root that is no initialised
		// submodule of the project (ADR-0071 §7).
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
		},
		// A retained result drops the message and the files once delivered (ADR-0020
		// §Retention): a later operation.wait returns the rest, and the caller reads again.
		Slim: func(result any) any {
			if r, ok := result.(ShowResult); ok {
				r.Message = ""
				r.Files = []ShowFile{}
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.show(ctx, call)
		},
	})
}

// show serves git.show. The one caller text on argv is the oid, a full hex object id
// checked here before any git starts and confirmed to be a commit before it is used; the
// first parent comes from the commit's own details, and the files are listed under the
// workspace's folder, which git is run in (ADR-0069 §2, §3, §5). With a root (roots.go) it
// is the submodule's commit, run in the submodule, and each file's path carries the root's.
func (o Options) show(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(ShowParams)
	if !isOid(p.Oid) {
		return nil, capability.NewError(capability.ErrInvalidParams, "oid is not a full object id").
			WithNextStep("pass a full hex object id (as git.log returned it), not a ref name or a short id")
	}
	if p.Base != "" && !isOid(p.Base) {
		return nil, capability.NewError(capability.ErrInvalidParams, "base is not a full object id").
			WithNextStep("pass a full hex object id of a commit, not a ref name or a short id")
	}
	sub, cerr := parseRoot(p.Root)
	if cerr != nil {
		return nil, cerr
	}
	ws, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
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

	i, cerr := o.firstNotCommit(ctx, r, root, trusted, []string{p.Oid})
	if cerr != nil {
		return nil, cerr
	}
	if i >= 0 {
		return nil, capability.NewError(capability.ErrInvalidParams, "oid is not a commit of this repository").
			WithNextStep("pass the id of a commit git.log returned; reload the history if the repository changed")
	}
	if p.Base != "" {
		i, cerr := o.firstNotCommit(ctx, r, root, trusted, []string{p.Base})
		if cerr != nil {
			return nil, cerr
		}
		if i >= 0 {
			return nil, capability.NewError(capability.ErrInvalidParams, "base is not a commit of this repository").
				WithNextStep("pass the full object id of a commit of this repository")
		}
	}
	// The workspace's folder below its repository's top, whose files are listed. A
	// submodule root is the top of its own repository (resolveRoot proved it), so its
	// diff-tree paths are the root's own and each gets the root's path in front below.
	prefix := ""
	if root.rel == "" {
		prefix, err = r.prefix(ctx, root.dir, trusted)
		if err != nil {
			return nil, root.mapError(err)
		}
	}

	detailsArgs, _ := showArgs(p.Oid, "")
	res, err := r.run(ctx, root.dir, trusted, nil, detailsArgs...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	out, perr := parseShow(p.Oid, res)
	if perr != nil {
		return nil, perr
	}

	first := ""
	if len(out.Parents) > 0 {
		first = out.Parents[0]
		if !isOid(first) { // from git's own output, but only an oid may reach argv
			return nil, capability.NewError(CodeFailed, "git answered something this version does not understand").
				WithCause("a parent that is not an object id: " + truncate(first, 100))
		}
	}
	if p.Base != "" { // ADR-0072 §2: the files between base and oid
		first = p.Base
	}
	_, filesArgs := showArgs(p.Oid, first)
	res, err = r.run(ctx, root.dir, trusted, nil, filesArgs...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	files, truncated, perr := parseShowFiles(res.Stdout, res.Truncated, prefix)
	if perr != nil {
		return nil, perr
	}
	if root.rel != "" { // paths in and out are the workspace's
		for i := range files {
			files[i].Path = root.rel + "/" + files[i].Path
			if files[i].OrigPath != "" {
				files[i].OrigPath = root.rel + "/" + files[i].OrigPath
			}
		}
	}
	out.Files, out.Truncated = files, truncated
	return out, nil
}

// parseShow reads showFormat's one record: showFields NUL-terminated fields, the message
// last. The oid must be the one asked for. Text is made valid UTF-8, so the JSON carries
// it whatever bytes the commit holds; a message past maxMessage is cut at a rune.
func parseShow(oid string, res result) (ShowResult, *capability.Error) {
	f := bytes.Split(res.Stdout, []byte{0})
	if len(f) < showFields || string(f[0]) != oid {
		return ShowResult{}, capability.NewError(CodeFailed, "git answered something this version does not understand").
			WithCause(truncate(string(res.Stdout), 200))
	}
	parents := strings.Fields(string(f[1]))
	if parents == nil {
		parents = []string{}
	}
	person := func(name, email, when []byte) ShowPerson {
		t := string(when)
		if _, err := time.Parse(time.RFC3339, t); err != nil {
			t = epoch
		}
		return ShowPerson{
			Name:  strings.ToValidUTF8(string(name), "�"),
			Email: strings.ToValidUTF8(string(email), "�"),
			Time:  t,
		}
	}
	msg := strings.ToValidUTF8(string(f[8]), "�")
	cut := res.Truncated
	if len(msg) > maxMessage {
		n := maxMessage
		for n > 0 && !utf8.RuneStart(msg[n]) {
			n--
		}
		msg, cut = msg[:n], true
	}
	return ShowResult{
		Oid:              oid,
		Parents:          parents,
		Author:           person(f[2], f[3], f[4]),
		Committer:        person(f[5], f[6], f[7]),
		Message:          msg,
		MessageTruncated: cut,
		Files:            []ShowFile{},
	}, nil
}

// parseShowFiles reads diff-tree's `--raw --numstat -z` output: every raw record
// (":<modes> <oids> <status>", then one path, or two for a rename), then every numstat
// record ("<added>\t<removed>\t<path>", or an empty path followed by the old and new
// paths for a rename; "-\t-" for a binary file), both in path order. The paths are the
// repository's; those under prefix (the workspace's folder, "" or "app/") are returned
// relative to it. cut says the runner passed its output cap: the last field is not
// trusted, a file whose count record was cut off is listed with no counts, and the list is
// truncated. At most maxShowFiles are returned.
func parseShowFiles(out []byte, cut bool, prefix string) ([]ShowFile, bool, *capability.Error) {
	bad := func(what string) *capability.Error {
		return capability.NewError(CodeFailed, "git answered something this version does not understand").WithCause(what)
	}
	tok := strings.Split(string(out), "\x00")
	tok = tok[:len(tok)-1] // what follows the last NUL: empty, or a cut field
	rel := func(p string) (string, bool) {
		p, ok := strings.CutPrefix(p, prefix)
		return strings.ToValidUTF8(p, "�"), ok
	}
	type counts struct {
		added, removed int
		binary         bool
	}
	var files []ShowFile
	i := 0
	for ; i < len(tok) && strings.HasPrefix(tok[i], ":"); i++ {
		fields := strings.Fields(tok[i])
		if len(fields) != 5 || fields[4] == "" {
			return nil, false, bad(truncate(tok[i], 200))
		}
		status := fields[4]
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		if i+paths >= len(tok) {
			if cut { // the cap fell inside this record
				i = len(tok)
				break
			}
			return nil, false, bad("a raw record ends short")
		}
		var orig string
		path := tok[i+paths]
		if paths == 2 {
			orig = tok[i+1]
		}
		i += paths
		p, ok := rel(path)
		if !ok {
			continue
		}
		f := ShowFile{Path: p}
		switch status[0] {
		case 'A':
			f.Status = "added"
		case 'D':
			f.Status = "deleted"
		case 'R':
			f.Status = "renamed"
			if o, ok := rel(orig); ok {
				f.OrigPath = o
			} else {
				f.Status = "added"
			}
		case 'C': // a copy is a new file, as git.status reads it
			f.Status = "added"
		default: // M, and T, a change of kind
			f.Status = "modified"
		}
		files = append(files, f)
	}
	byPath := make(map[string]counts, len(files))
	for ; i < len(tok); i++ {
		parts := strings.SplitN(tok[i], "\t", 3)
		if len(parts) != 3 {
			return nil, false, bad(truncate(tok[i], 200))
		}
		path := parts[2]
		if path == "" { // a rename: the old and the new path follow
			if i+2 >= len(tok) {
				if cut {
					break
				}
				return nil, false, bad("a count record ends short")
			}
			path = tok[i+2]
			i += 2
		}
		p, ok := rel(path)
		if !ok {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" {
			byPath[p] = counts{binary: true}
			continue
		}
		a, aerr := strconv.Atoi(parts[0])
		r, rerr := strconv.Atoi(parts[1])
		if aerr != nil || rerr != nil || a < 0 || r < 0 {
			return nil, false, bad(truncate(tok[i], 200))
		}
		byPath[p] = counts{added: a, removed: r}
	}

	kept := files[:0]
	for _, f := range files {
		c, ok := byPath[f.Path]
		if !ok {
			if cut { // the cap fell before its count: listed with none, and truncated says so
				kept = append(kept, f)
				continue
			}
			return nil, false, bad("no count record for " + truncate(f.Path, 100))
		}
		if c.binary {
			f.Binary = true
		} else {
			added, removed := c.added, c.removed
			f.Added, f.Removed = &added, &removed
		}
		kept = append(kept, f)
	}
	truncated := cut
	if len(kept) > maxShowFiles {
		kept, truncated = kept[:maxShowFiles], true
	}
	if kept == nil {
		kept = []ShowFile{}
	}
	return kept, truncated, nil
}

package git

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// sinceLog is the API version that introduces git.log (plan m2n step 2).
const sinceLog = "0.24.0"

// The page, and what a caller may pass (ADR-0069 §2).
const (
	// defaultLogLimit is a page's size when limit is absent.
	defaultLogLimit = 500
	// maxLogLimit is the most commits a page holds.
	maxLogLimit = 1000
	// maxFrom is the most oids `from` holds: the tip set's cap, so a first page's tips
	// are always acceptable as the next page's from.
	maxFrom = maxTips
	// maxSkip is the largest integer git accepts for --skip.
	maxSkip = 1<<31 - 1
)

// LogParams is git.log's parameters.
type LogParams struct {
	From  []string `json:"from,omitempty" jsonschema:"maxItems=5000,pattern=^([0-9a-f]{40}|[0-9a-f]{64})$" jsonschema_description:"The tips a first page returned (full object ids), passed back unchanged; at most 5000. Empty or absent: the tips now."`
	Skip  *int     `json:"skip,omitempty" jsonschema:"minimum=0,maximum=2147483647" jsonschema_description:"How many commits of the walk to skip: the number already received. Absent means 0."`
	Limit *int     `json:"limit,omitempty" jsonschema:"minimum=1,maximum=1000" jsonschema_description:"The most commits to return, 1 to 1000. Absent means 500."`
	Root  string   `json:"root,omitempty" jsonschema:"maxLength=4096" jsonschema_description:"A submodule's path as git.submodules lists it; absent or . is the project's own repository."`
}

// LogCommit is one commit of the history. Names, e-mails and subjects are the
// repository's own text (ADR-0069 §6): the raw author fields, never mailmapped.
type LogCommit struct {
	Oid         string   `json:"oid" jsonschema:"required" jsonschema_description:"The commit's full object id."`
	Parents     []string `json:"parents" jsonschema:"required" jsonschema_description:"The parents' object ids, first parent first; empty for a root commit."`
	AuthorName  string   `json:"authorName" jsonschema:"required" jsonschema_description:"The author's name as the commit records it."`
	AuthorEmail string   `json:"authorEmail" jsonschema:"required" jsonschema_description:"The author's e-mail as the commit records it."`
	CommitTime  int64    `json:"commitTime" jsonschema:"required" jsonschema_description:"When it was committed, in Unix seconds."`
	Subject     string   `json:"subject" jsonschema:"required" jsonschema_description:"The first paragraph of the message, on one line."`
}

// LogResult is git.log's result: one page of the walk. Commits and Tips drop out of a
// result returned again after delivery (Slim).
type LogResult struct {
	Commits       []LogCommit `json:"commits" jsonschema:"required" jsonschema_description:"The page: newest first by commit date, every commit before its parents. Empty in a result returned again after delivery, and for a repository with no commit."`
	Tips          []string    `json:"tips" jsonschema:"required" jsonschema_description:"The tip set the walk started from, as full object ids: HEAD, then branches, remote-tracking branches and tags, peeled to commits, at most 5000. Pass it back as from for the next page. Empty when from was given, and in a result returned again after delivery."`
	TipsTruncated bool        `json:"tipsTruncated" jsonschema:"required" jsonschema_description:"True when the repository has more tips than the 5000 returned; the oldest tags are left out. False when from was given."`
	More          bool        `json:"more" jsonschema:"required" jsonschema_description:"True when the walk goes on past this page. The next page's skip is the number of commits received so far; a page cut short by the 4 MiB output cap holds whole commits only and says more."`
}

func registerLog(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "git.log",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Since:        sinceLog,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Reads one page of the repository's commit history for the UI's graph, newest first with every commit before its parents, whole even when the project is a subfolder. The first call starts from the tips now and returns them; later pages pass them back as from, so refs moving does not shift the history.",
		Params:       LogParams{},
		Result:       LogResult{},
		// The codes the handler returns (ADR-0069 §4). WORKSPACE_NOT_OPEN comes from Start.
		// WORKSPACE_UNTRUSTED is an untrusted root whose git filter drivers cannot all be
		// switched off (ADR-0066 §3). REQUEST_INVALID_PARAMS is a from that is not a list
		// of full object ids of commits, a page out of bounds, or a root that is no
		// initialised submodule of the project (ADR-0071 §7).
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
		},
		// A retained result drops the page and the tips once delivered (ADR-0020
		// §Retention): a later operation.wait returns the flags, and the caller reads again.
		Slim: func(result any) any {
			if r, ok := result.(LogResult); ok {
				r.Commits = []LogCommit{}
				r.Tips = []string{}
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.log(ctx, call)
		},
	})
}

// log serves git.log. No parameter reaches argv but the two validated integers: the
// commits to walk from go to git on stdin, as full object ids checked here before any
// git starts (ADR-0069 §2). The optional root is a submodule (roots.go): its repository is
// the one walked, and `from` names its commits.
func (o Options) log(ctx context.Context, call *capability.Call) (any, error) {
	p := call.Params.(LogParams)
	skip, limit := 0, defaultLogLimit
	if p.Skip != nil {
		skip = *p.Skip
	}
	if p.Limit != nil {
		limit = *p.Limit
	}
	switch {
	case skip < 0:
		return nil, capability.Errorf(capability.ErrInvalidParams, "skip %d is negative", skip).
			WithNextStep("pass the number of commits already received, or omit skip")
	case skip > maxSkip:
		return nil, capability.Errorf(capability.ErrInvalidParams, "skip %d is above %d", skip, maxSkip).
			WithNextStep("pass the number of commits already received, or omit skip")
	case limit < 1 || limit > maxLogLimit:
		return nil, capability.Errorf(capability.ErrInvalidParams, "limit %d is outside 1 to %d", limit, maxLogLimit).
			WithNextStep("pass a limit from 1 to " + strconv.Itoa(maxLogLimit) + ", or omit it for " + strconv.Itoa(defaultLogLimit))
	case len(p.From) > maxFrom:
		return nil, capability.Errorf(capability.ErrInvalidParams, "from holds %d object ids; at most %d", len(p.From), maxFrom).
			WithNextStep("pass the tips a first page returned, unchanged")
	}
	for i, oid := range p.From {
		if !isOid(oid) {
			return nil, capability.Errorf(capability.ErrInvalidParams, "from[%d] is not a full object id", i).
				WithNextStep("pass full hex object ids (the tips a first page returned), not ref names or short ids")
		}
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

	out := LogResult{Commits: []LogCommit{}, Tips: []string{}}
	from := p.From
	if len(from) == 0 {
		tp, truncated, err := tips(ctx, r, root.dir, trusted)
		if err != nil {
			return nil, root.mapError(err)
		}
		if len(tp) == 0 { // no commit yet
			return out, nil
		}
		out.Tips, out.TipsTruncated = tp, truncated
		from = tp
	} else if cerr := o.commitsExist(ctx, r, root, trusted, from); cerr != nil {
		return nil, cerr
	}

	// One more than the page, so that "more" is known exactly.
	res, err := r.run(ctx, root.dir, trusted, []byte(strings.Join(from, "\n")+"\n"), logArgs(skip, limit+1)...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	commits, perr := parseLog(res.Stdout)
	if perr != nil {
		return nil, perr
	}
	more := false
	if res.Truncated {
		// The cut record is dropped (parseLog read whole ones only). With none whole, the
		// next page would start where this one did.
		if len(commits) == 0 {
			return nil, capability.NewError(CodeFailed, "a commit's record is larger than git.log's 4 MiB output cap")
		}
		more = true
	}
	if len(commits) > limit {
		commits, more = commits[:limit], true
	}
	out.Commits, out.More = commits, more
	return out, nil
}

// commitsExist refuses, as REQUEST_INVALID_PARAMS, a from that names an object the
// repository does not hold or one that is not a commit (ADR-0069 §4). The oids go on
// stdin, as everywhere.
func (o Options) commitsExist(ctx context.Context, r *runner, root gitRoot, trusted bool, oids []string) *capability.Error {
	i, cerr := o.firstNotCommit(ctx, r, root, trusted, oids)
	if cerr != nil {
		return cerr
	}
	if i >= 0 {
		return capability.Errorf(capability.ErrInvalidParams, "from[%d] is not a commit of this repository", i).
			WithNextStep("pass the tips a first page returned, unchanged; reload the history if the repository changed")
	}
	return nil
}

// firstNotCommit asks git for the kind of each of oids (full object ids, validated by the
// caller) and returns the index of the first that is no commit of this repository, or -1.
// The one place that decides "is this oid a commit": git.log's from, git.show's oid and
// git.read's base share it.
func (o Options) firstNotCommit(ctx context.Context, r *runner, root gitRoot, trusted bool, oids []string) (int, *capability.Error) {
	res, err := r.run(ctx, root.dir, trusted, []byte(strings.Join(oids, "\n")+"\n"), hist("cat-file", "--batch-check")...)
	if err != nil {
		return -1, capability.AsError(root.mapError(err))
	}
	if res.ExitCode != 0 {
		return -1, capability.AsError(root.mapError(gitError(res)))
	}
	lines := strings.Split(strings.TrimSuffix(string(res.Stdout), "\n"), "\n")
	if res.Truncated || len(lines) != len(oids) {
		return -1, capability.NewError(CodeFailed, "git answered something this version does not understand")
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == oids[i] && f[1] == "commit" {
			continue
		}
		return i, nil
	}
	return -1, nil
}

// parseLog reads logFormat's records: logFields NUL-terminated fields each. A trailing
// partial record (output cut at the cap) is dropped, never half-read. Text is made valid
// UTF-8, so the JSON carries it whatever bytes the commit holds.
func parseLog(out []byte) ([]LogCommit, *capability.Error) {
	fields := bytes.Split(out, []byte{0})
	fields = fields[:len(fields)-1] // what follows the last NUL is empty, or a cut field
	commits := make([]LogCommit, 0, len(fields)/logFields)
	for i := 0; i+logFields <= len(fields); i += logFields {
		f := fields[i : i+logFields]
		when, err := strconv.ParseInt(string(f[4]), 10, 64)
		if err != nil || !isOid(string(f[0])) {
			return nil, capability.NewError(CodeFailed, "git answered something this version does not understand").
				WithCause(truncate(string(bytes.Join(f, []byte{' '})), 200))
		}
		parents := strings.Fields(string(f[1]))
		if parents == nil {
			parents = []string{}
		}
		commits = append(commits, LogCommit{
			Oid:         string(f[0]),
			Parents:     parents,
			AuthorName:  strings.ToValidUTF8(string(f[2]), "�"),
			AuthorEmail: strings.ToValidUTF8(string(f[3]), "�"),
			CommitTime:  when,
			Subject:     strings.ToValidUTF8(string(f[5]), "�"),
		})
	}
	return commits, nil
}

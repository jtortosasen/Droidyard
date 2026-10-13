package git

import (
	"bytes"
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// maxRefList caps each list git.refs returns (ADR-0069 §2, plan m2n Q1 §1).
const maxRefList = 5000

// RefsParams is git.refs' parameters: the root, optionally.
type RefsParams struct {
	Root string `json:"root,omitempty" jsonschema:"maxLength=4096" jsonschema_description:"A submodule's path in the project (a gitlink of its parent, initialised); absent or . is the project's own repository."`
}

// RefsHead is what HEAD is, and how far the current branch is from its upstream.
type RefsHead struct {
	Oid      string `json:"oid,omitempty" jsonschema_description:"HEAD's commit id; absent in a repository with no commit yet."`
	Branch   string `json:"branch,omitempty" jsonschema_description:"The checked-out branch, also when it has no commit yet; absent when HEAD is detached."`
	Detached bool   `json:"detached" jsonschema:"required" jsonschema_description:"True when HEAD is detached: it points at a commit, not at a branch."`
	Upstream string `json:"upstream,omitempty" jsonschema_description:"The current branch's upstream as configured, for example origin/main; absent when the branch has none or HEAD is detached."`
	// UpstreamGone is required and always present, unlike the branches' (omitted when false).
	UpstreamGone bool `json:"upstreamGone" jsonschema:"required" jsonschema_description:"True when the current branch names an upstream that is not among the refs listed: deleted, or past the cut of git's output. Ahead and behind are zero then."`
	Ahead        int  `json:"ahead" jsonschema:"required,minimum=0" jsonschema_description:"Commits on the current branch that its upstream lacks. Zero when there is no upstream or it is no longer a branch of this repository. Counted for the current branch only."`
	Behind       int  `json:"behind" jsonschema:"required,minimum=0" jsonschema_description:"Commits on the upstream that the current branch lacks; zero as ahead is."`
}

// RefsBranch is a local branch.
type RefsBranch struct {
	Name     string `json:"name" jsonschema:"required" jsonschema_description:"The branch's name without refs/heads/, folders included (feature/login)."`
	Oid      string `json:"oid" jsonschema:"required" jsonschema_description:"The object the branch points at."`
	Upstream string `json:"upstream,omitempty" jsonschema_description:"The upstream as configured (origin/main); absent when the branch has none."`
	// UpstreamGone is omitted when false, unlike HEAD's (always present).
	UpstreamGone bool `json:"upstreamGone,omitempty" jsonschema_description:"True when the upstream is not among the refs listed: deleted, or past the cut of git's output. Omitted when false."`
}

// RefsRemote is a remote-tracking branch.
type RefsRemote struct {
	Remote string `json:"remote" jsonschema:"required" jsonschema_description:"The remote's name, as configured, slashes included (team/origin). A ref no configured remote claims is split at its first slash."`
	Name   string `json:"name" jsonschema:"required" jsonschema_description:"The branch's name on that remote, folders included. A remote's default-branch pointer is listed as HEAD."`
	Oid    string `json:"oid" jsonschema:"required" jsonschema_description:"The object the ref points at."`
}

// RefsTag is a tag.
type RefsTag struct {
	Name string `json:"name" jsonschema:"required" jsonschema_description:"The tag's name without refs/tags/."`
	Oid  string `json:"oid,omitempty" jsonschema_description:"The commit the tag finally points at, however many tags deep; absent for a tag on a tree or a blob."`
}

// RefsWorktree is a work tree of the repository.
type RefsWorktree struct {
	Path    string `json:"path" jsonschema:"required" jsonschema_description:"The work tree's absolute path, as git records it."`
	Branch  string `json:"branch,omitempty" jsonschema_description:"The branch checked out in it; absent when it is detached."`
	Oid     string `json:"oid,omitempty" jsonschema_description:"The commit it has checked out; absent when it has none yet."`
	Current bool   `json:"current" jsonschema:"required" jsonschema_description:"True for the work tree that holds this workspace."`
}

// RefsResult is git.refs' result. The lists and the tips drop out of a result returned
// again after delivery (Slim). Ref names are the repository's own text (ADR-0069 §6).
type RefsResult struct {
	Head               RefsHead       `json:"head" jsonschema:"required" jsonschema_description:"What HEAD is."`
	Branches           []RefsBranch   `json:"branches" jsonschema:"required" jsonschema_description:"Local branches by name; at most 5000. Empty in a result returned again after delivery."`
	BranchesTruncated  bool           `json:"branchesTruncated" jsonschema:"required" jsonschema_description:"True when there are more local branches than listed."`
	Remotes            []RefsRemote   `json:"remotes" jsonschema:"required" jsonschema_description:"Remote-tracking branches by name; at most 5000. Empty in a result returned again after delivery."`
	RemotesTruncated   bool           `json:"remotesTruncated" jsonschema:"required" jsonschema_description:"True when there are more remote-tracking branches than listed."`
	Tags               []RefsTag      `json:"tags" jsonschema:"required" jsonschema_description:"Tags, newest first; at most 5000: the oldest are left out at the cap, and when git's output is cut at 4 MiB the tags last in name order are missing too. Empty in a result returned again after delivery."`
	TagsTruncated      bool           `json:"tagsTruncated" jsonschema:"required" jsonschema_description:"True when there are more tags than listed."`
	Worktrees          []RefsWorktree `json:"worktrees" jsonschema:"required" jsonschema_description:"The repository's work trees, the main one first; at most 5000. Empty in a result returned again after delivery."`
	WorktreesTruncated bool           `json:"worktreesTruncated" jsonschema:"required" jsonschema_description:"True when there are more work trees than listed."`
	Tips               []string       `json:"tips" jsonschema:"required" jsonschema_description:"The tip set git.log starts from, as full object ids, the same list: HEAD, then branches, remote-tracking branches and tags, peeled to commits, at most 5000. Empty in a result returned again after delivery."`
	TipsTruncated      bool           `json:"tipsTruncated" jsonschema:"required" jsonschema_description:"True when the repository has more tips than the 5000 returned, or git's refs were cut at 4 MiB; the oldest tags are left out at the cap, the tags last in name order at the cut."`
}

func registerRefs(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "git.refs",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Since:        sinceLog,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Reads what the repository's history view lists beside the graph: HEAD, with how far the current branch is ahead of and behind its upstream, the local and remote-tracking branches, the tags and the work trees, and the tip set git.log starts from. The refs are the repository's, whole even when the project is a subfolder.",
		Params:       RefsParams{},
		Result:       RefsResult{},
		// The codes the handler returns (ADR-0069 §4). WORKSPACE_NOT_OPEN comes from Start.
		// WORKSPACE_UNTRUSTED is an untrusted root whose git filter drivers cannot all be
		// switched off (ADR-0066 §3).
		// REQUEST_INVALID_PARAMS is a root that is no initialised submodule of the project.
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed, capability.ErrWorkspaceUntrusted,
			capability.ErrInvalidParams,
		},
		// A retained result drops the lists and the tips once delivered (ADR-0020
		// §Retention): a later operation.wait returns HEAD and the flags, and the caller
		// reads again.
		Slim: func(result any) any {
			if r, ok := result.(RefsResult); ok {
				r.Branches = []RefsBranch{}
				r.Remotes = []RefsRemote{}
				r.Tags = []RefsTag{}
				r.Worktrees = []RefsWorktree{}
				r.Tips = []string{}
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.refs(ctx, call)
		},
	})
}

// refs serves git.refs. Nothing a caller or the repository wrote reaches argv: the only
// arguments are fixed, and the two object ids the ahead and behind count compares go to
// git on stdin after the check that they are object ids (ADR-0069 §2).
func (o Options) refs(ctx context.Context, call *capability.Call) (any, error) {
	sub, cerr := parseRoot(call.Params.(RefsParams).Root)
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

	// HEAD and the refs are read once: the lists below and the tip set are built from the
	// same two outputs, so they cannot disagree. The first git also meets a root that is no
	// repository.
	head, err := headOid(ctx, r, root.dir, trusted)
	if err != nil {
		return nil, root.mapError(err)
	}
	refsOut, refsCut, err := refsOutput(ctx, r, root.dir, trusted)
	if err != nil {
		return nil, root.mapError(err)
	}
	tp, tipsTruncated, err := tipSet(head, refsOut, refsCut)
	if err != nil {
		return nil, root.mapError(err)
	}
	out := RefsResult{
		Branches: []RefsBranch{}, Remotes: []RefsRemote{}, Tags: []RefsTag{}, Worktrees: []RefsWorktree{},
		Tips: tp, TipsTruncated: tipsTruncated,
	}
	if out.Tips == nil {
		out.Tips = []string{}
	}
	out.Head.Oid = head

	// The branch HEAD is on, an unborn one included. A detached HEAD exits 1.
	res, err := r.run(ctx, root.dir, trusted, nil, hist("symbolic-ref", "--quiet", "HEAD")...)
	if err != nil {
		return nil, root.mapError(err)
	}
	switch res.ExitCode {
	case 0:
		out.Head.Branch = strings.TrimPrefix(validUTF8(strings.TrimSpace(string(res.Stdout))), "refs/heads/")
	case 1:
		out.Head.Detached = true
	default:
		return nil, root.mapError(gitError(res))
	}

	// The remotes' names, to split remote-tracking refs where a remote's name ends.
	res, err = r.run(ctx, root.dir, trusted, nil, hist("remote")...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	lists, perr := parseRefs(refsOut, refsCut, remoteNames(res.Stdout, res.Truncated))
	if perr != nil {
		return nil, root.mapError(perr)
	}
	out.Branches, out.BranchesTruncated = capList(lists.branches, lists.cut[kindHead])
	out.Remotes, out.RemotesTruncated = capList(lists.remotes, lists.cut[kindRemote])
	out.Tags, out.TagsTruncated = capList(lists.tags, lists.cut[kindTag])

	if out.Head.Branch != "" {
		for _, b := range lists.branches {
			if b.Name == out.Head.Branch {
				out.Head.Upstream, out.Head.UpstreamGone = b.Upstream, b.UpstreamGone
				break
			}
		}
		if up := lists.upstreamOid[out.Head.Branch]; head != "" && up != "" {
			ahead, behind, cerr := o.aheadBehind(ctx, r, root, trusted, head, up)
			if cerr != nil {
				return nil, cerr
			}
			out.Head.Ahead, out.Head.Behind = ahead, behind
		}
	}

	// The work tree that holds the workspace is the one whose top git reports from the
	// root. A bare repository has none.
	top := ""
	res, err = r.run(ctx, root.dir, trusted, nil, hist("rev-parse", "--show-toplevel")...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode == 0 {
		top = o.Env.Canonical(strings.TrimSpace(string(res.Stdout)))
	}
	res, err = r.run(ctx, root.dir, trusted, nil, worktreeArgs()...)
	if err != nil {
		return nil, root.mapError(err)
	}
	if res.ExitCode != 0 {
		return nil, root.mapError(gitError(res))
	}
	wts := parseWorktrees(res.Stdout, func(path string) bool { return top != "" && o.Env.Canonical(path) == top })
	out.Worktrees, out.WorktreesTruncated = capList(wts, res.Truncated)
	return out, nil
}

// aheadBehind counts the commits of head that upstream lacks and the reverse, from the
// two object ids on stdin as one symmetric difference.
func (o Options) aheadBehind(ctx context.Context, r *runner, root gitRoot, trusted bool, head, upstream string) (ahead, behind int, cerr *capability.Error) {
	if !isOid(head) || !isOid(upstream) {
		return 0, 0, capability.NewError(CodeFailed, "git answered something this version does not understand")
	}
	res, err := r.run(ctx, root.dir, trusted, []byte(head+"..."+upstream+"\n"), hist("rev-list", "--stdin", "--left-right", "--count")...)
	if err != nil {
		return 0, 0, capability.AsError(root.mapError(err))
	}
	if res.ExitCode != 0 {
		return 0, 0, capability.AsError(root.mapError(gitError(res)))
	}
	f := strings.Fields(string(res.Stdout))
	if len(f) == 2 && !res.Truncated {
		a, aerr := strconv.Atoi(f[0])
		b, berr := strconv.Atoi(f[1])
		if aerr == nil && berr == nil && a >= 0 && b >= 0 {
			return a, b, nil
		}
	}
	return 0, 0, capability.NewError(CodeFailed, "git answered something this version does not understand").
		WithCause(truncate(string(res.Stdout), 200))
}

// capList keeps the first maxRefList of list; truncated says some were dropped, here or
// before (cut: git's output itself was cut at the cap).
func capList[T any](list []T, cut bool) ([]T, bool) {
	if len(list) > maxRefList {
		return list[:maxRefList], true
	}
	if list == nil {
		list = []T{}
	}
	return list, cut
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, "�") }

// The three kinds of ref git.refs lists, in the order for-each-ref sorts them.
const (
	kindHead = iota
	kindRemote
	kindTag
)

// refLists is what refsFormat's lines hold.
type refLists struct {
	branches []RefsBranch
	remotes  []RefsRemote
	tags     []RefsTag
	// upstreamOid is, by local branch name, the commit its configured upstream points at,
	// when that upstream is a branch of the listing (local or remote-tracking).
	upstreamOid map[string]string
	// upstreamGone is, by local branch name, true for a branch whose configured upstream is
	// no ref of the listing: deleted, or past the cut of git's output.
	upstreamGone map[string]bool
	// cut is, by kind, whether git's output was cut at the byte cap in or before that
	// kind: the kinds are sorted, so everything after the cut is missing.
	cut [3]bool
}

// parseRefs reads refsFormat's lines. A last line cut at the cap is dropped, and the
// kind it was in, and every later one, are marked cut. remoteNames are the configured
// remotes' names, longest first (remoteNames' order).
func parseRefs(out []byte, cut bool, remoteNames []string) (refLists, error) {
	lastKind := kindHead
	if cut {
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
	}
	type tag struct {
		RefsTag
		when int64
	}
	var lists refLists
	var tags []tag
	oidOf := map[string]string{}
	upstreams := map[string]string{} // local branch -> its upstream's full ref name
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != refsFields {
			return lists, errRefsLine(len(f))
		}
		typ, oid, peeledTyp, peeled, when, ref, upstream := f[0], f[1], f[2], f[3], f[4], f[5], f[6]
		peeledOid := ""
		if typ == "tag" { // the final target, whatever depth of tags
			typ, oid = peeledTyp, peeled
		}
		if typ == "commit" {
			peeledOid = oid
		}
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			lastKind = kindHead
			name := validUTF8(strings.TrimPrefix(ref, "refs/heads/"))
			oidOf[ref] = oid
			b := RefsBranch{Name: name, Oid: oid}
			if upstream != "" {
				b.Upstream = shortRef(validUTF8(upstream))
				upstreams[name] = upstream
			}
			lists.branches = append(lists.branches, b)
		case strings.HasPrefix(ref, "refs/remotes/"):
			lastKind = kindRemote
			rest := strings.TrimPrefix(ref, "refs/remotes/")
			remote, name := splitRemote(rest, remoteNames)
			oidOf[ref] = oid
			lists.remotes = append(lists.remotes, RefsRemote{Remote: validUTF8(remote), Name: validUTF8(name), Oid: oid})
		case strings.HasPrefix(ref, "refs/tags/"):
			lastKind = kindTag
			n, _ := strconv.ParseInt(when, 10, 64)
			tags = append(tags, tag{RefsTag{Name: validUTF8(strings.TrimPrefix(ref, "refs/tags/")), Oid: peeledOid}, n})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].when != tags[j].when {
			return tags[i].when > tags[j].when
		}
		return tags[i].Name < tags[j].Name
	})
	for _, t := range tags {
		lists.tags = append(lists.tags, t.RefsTag)
	}
	lists.upstreamOid = map[string]string{}
	lists.upstreamGone = map[string]bool{}
	for name, up := range upstreams {
		if oid := oidOf[up]; oid != "" {
			lists.upstreamOid[name] = oid
		} else {
			lists.upstreamGone[name] = true
		}
	}
	for i := range lists.branches {
		lists.branches[i].UpstreamGone = lists.upstreamGone[lists.branches[i].Name]
	}
	if cut {
		for k := lastKind; k <= kindTag; k++ {
			lists.cut[k] = true
		}
	}
	return lists, nil
}

// remoteNames reads `git remote`'s lines, longest name first (so the first that prefixes
// a ref is the longest). A last line cut at the cap is dropped.
func remoteNames(out []byte, cut bool) []string {
	if cut {
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
	}
	var names []string
	for _, n := range strings.Split(string(out), "\n") {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return names
}

// splitRemote splits what follows refs/remotes/ into the remote and the branch: the
// longest configured remote name followed by a slash, else the part before the first slash.
func splitRemote(rest string, remoteNames []string) (remote, name string) {
	for _, n := range remoteNames {
		if strings.HasPrefix(rest, n+"/") {
			return validUTF8(n), validUTF8(rest[len(n)+1:])
		}
	}
	remote, name, _ = strings.Cut(rest, "/")
	return validUTF8(remote), validUTF8(name)
}

func errRefsLine(n int) error {
	return capability.Errorf(CodeFailed, "git answered something this version does not understand").
		WithCause("a ref line of " + strconv.Itoa(n) + " fields, want " + strconv.Itoa(refsFields))
}

// shortRef is a ref without its refs/heads/ or refs/remotes/ prefix, as git shows an
// upstream.
func shortRef(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		return s
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

// parseWorktrees reads `worktree list --porcelain -z`: NUL-terminated attributes, an
// empty one ending each entry. An entry the output was cut in is never terminated and so
// is never read. A bare repository's entry is not a work tree and is left out.
func parseWorktrees(out []byte, isCurrent func(path string) bool) []RefsWorktree {
	var list []RefsWorktree
	var wt RefsWorktree
	bare, started := false, false
	for _, attr := range strings.Split(string(out), "\x00") {
		if attr == "" {
			if started && !bare && wt.Path != "" {
				wt.Current = isCurrent(wt.Path)
				list = append(list, wt)
			}
			wt, bare, started = RefsWorktree{}, false, false
			continue
		}
		started = true
		key, val, _ := strings.Cut(attr, " ")
		switch key {
		case "worktree":
			wt.Path = validUTF8(val)
		case "HEAD":
			if strings.Trim(val, "0") != "" { // all zeros: no commit yet
				wt.Oid = val
			}
		case "branch":
			wt.Branch = strings.TrimPrefix(validUTF8(val), "refs/heads/")
		case "bare":
			bare = true
		}
	}
	return list
}

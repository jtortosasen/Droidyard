package git

// git.submodules (plan m2n P4; ADR-0071 §1, §7).
//
// What a submodule is here. A path that the repository's `.gitmodules` names AND whose
// index entry is a gitlink (mode 160000): git's own `submodule` commands refuse a gitlink
// with no `.gitmodules` entry, and a `.gitmodules` entry with no gitlink is no submodule.
// The walk is bounded by the submodules, not by the index: the candidate paths come from
// `config --file .gitmodules` (a file read; it runs nothing), and `ls-files --stage` is
// asked about those paths only, matched literally (--literal-pathspecs), in chunks so no
// argv grows with the file.
//
// Where a git may run. Only in the workspace. A submodule's directory is used only when
// file.ResolveDir resolves it to exactly its lexical join with the workspace root, so a
// symlink on the way (out of the root, or out of a subdirectory workspace) leaves the row
// out and starts no git there. A directory that is no repository (no `.git`, or a `.git`
// that is a symlink) is read by no git at all. The containment is of the directory git runs
// in and of the work tree, not of the git directory: a `.git` file may point outside the
// workspace, as the submodules of a subdirectory workspace do (their git directories are in
// the superproject's .git above it).
//
// The fence. Each git of a submodule runs through the runner in that submodule's own
// directory with the workspace's trust, so on an untrusted root the filter drivers it
// configures are read there, in that repository's own configuration, and switched off
// there (ADR-0066 §3). Reading them in the superproject only would leave a submodule's
// own `filter.<x>.clean` to run on its `status`.
//
// Per submodule: `rev-parse --is-inside-work-tree --show-prefix` (a submodule is its own
// top; an uninitialised one is inside the superproject), `rev-parse HEAD`, and m2l's
// status; on an untrusted root each of them is preceded by the driver read.

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

const (
	// maxSubmodules caps the list, nested submodules included.
	maxSubmodules = 1000
	// maxSubmoduleDepth is how deep the walk goes: depth 1 is a submodule of the
	// workspace's repository.
	maxSubmoduleDepth = 8
	// pathspecChunk is the most bytes of paths one ls-files is given.
	pathspecChunk = 64 << 10
	// gitlinkMode is the index mode of a submodule.
	gitlinkMode = "160000"
)

// The states of a submodule.
const (
	StateUninitialised = "uninitialised"
	StateClean         = "clean"
	StateOutdated      = "outdated"
	StateDirty         = "dirty"
)

// SubmodulesParams is git.submodules' parameters: none.
type SubmodulesParams struct{}

// Submodule is one submodule of the workspace's repository, or a nested one.
type Submodule struct {
	Path       string `json:"path" jsonschema:"required" jsonschema_description:"The submodule's directory, relative to the project root; a nested submodule's path starts with its parent's."`
	Recorded   string `json:"recorded" jsonschema:"required" jsonschema_description:"The commit the repository records for it (its index entry)."`
	CheckedOut string `json:"checkedOut,omitempty" jsonschema_description:"The commit it has checked out (its HEAD); absent when it is uninitialised or has no commit."`
	State      string `json:"state" jsonschema:"required,enum=uninitialised,enum=clean,enum=outdated,enum=dirty" jsonschema_description:"uninitialised: no repository of its own there yet. dirty: its working tree differs from its HEAD (its own nested submodules ignored), whatever its commit. outdated: clean but checked out at another commit than recorded. clean: the recorded commit, nothing changed."`
}

// SubmodulesResult is git.submodules' result. The list drops out of a result returned
// again after delivery (Slim).
type SubmodulesResult struct {
	Submodules []Submodule `json:"submodules" jsonschema:"required" jsonschema_description:"The submodules named in .gitmodules that the repository records as gitlinks, below the project root, each followed by its nested ones, to eight levels; at most 1000. Empty in a result returned again after delivery. A submodule whose directory is, or sits behind, a symlink, or whose .git is a symlink, is left out."`
	Truncated  bool        `json:"truncated" jsonschema:"required" jsonschema_description:"True when there are more submodules than listed (1000, or git's output passed 4 MiB); the list ends at a whole entry."`
}

func registerSubmodules(reg *capability.Registry, opts Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "git.submodules",
		Kind:         capability.Query,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Since:        sinceWrites,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Lists the project's git submodules for the UI's History tab: for each one the commit the repository records, the commit it has checked out and its state (uninitialised, clean, outdated or dirty), nested ones included. Read-only; each submodule is read in its own directory, with its own repository's filter drivers switched off on an untrusted project.",
		Params:       SubmodulesParams{},
		Result:       SubmodulesResult{},
		// The codes the handler returns (ADR-0071 §1, ADR-0066 §4). WORKSPACE_NOT_OPEN
		// comes from Start. WORKSPACE_UNTRUSTED is an untrusted root, or a submodule on
		// one, whose git filter drivers cannot all be switched off (ADR-0066 §3).
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed, capability.ErrWorkspaceUntrusted,
		},
		// A retained result drops the list once delivered (ADR-0020 §Retention): a later
		// operation.wait returns the flag, and the caller reads again.
		Slim: func(result any) any {
			if r, ok := result.(SubmodulesResult); ok {
				r.Submodules = []Submodule{}
				return r
			}
			return result
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.submodules(ctx, call)
		},
	})
}

// submodules serves git.submodules. No parameter exists; every path that reaches a
// git's argv comes from the repository's own files and is checked first (walker.level).
func (o Options) submodules(ctx context.Context, call *capability.Call) (any, error) {
	root, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	r, cerr := o.runner()
	if cerr != nil {
		return nil, cerr
	}
	return o.listSubmodules(ctx, r, root)
}

// listSubmodules is the walk itself, for git.submodules and for git.updateSubmodules, which
// asks it afresh which paths are submodules before it names any to git.
func (o Options) listSubmodules(ctx context.Context, r *runner, root string) (SubmodulesResult, error) {
	trusted := o.trusted(root)
	// The first git also meets a root that is no repository.
	prefix, err := r.prefix(ctx, root, trusted)
	if err != nil {
		return SubmodulesResult{}, mapError(root, err)
	}
	top := root
	for range strings.Count(prefix, "/") {
		top = filepath.Dir(top)
	}
	w := &walker{ctx: ctx, r: r, env: o.Env, root: root, trusted: trusted}
	err = w.level(root, top, prefix, "", 1)
	if err != nil && !errors.Is(err, errFull) {
		var se *submoduleError
		if errors.As(err, &se) {
			return SubmodulesResult{}, mapSubmoduleError(root, se)
		}
		return SubmodulesResult{}, mapError(root, err)
	}
	if w.out == nil {
		w.out = []Submodule{}
	}
	return SubmodulesResult{Submodules: w.out, Truncated: w.truncated}, nil
}

// errFull stops the walk at the cap; the result is truncated.
var errFull = errors.New("the submodule list is full")

// submoduleError is an error of a git run for one submodule (its own directory, or its own
// .gitmodules), named by its workspace-relative path. The whole call still fails with it,
// but the cause says which submodule; the code is the one the inner error maps to.
type submoduleError struct {
	path string
	err  error
}

func (e *submoduleError) Error() string { return "submodule " + e.path + ": " + e.err.Error() }
func (e *submoduleError) Unwrap() error { return e.err }

// inSubmodule names sub on an error that is not named yet (a nested submodule's own name is
// the more exact one); nil and the full-list stop stay as they are.
func inSubmodule(sub string, err error) error {
	var se *submoduleError
	if err == nil || errors.Is(err, errFull) || errors.As(err, &se) {
		return err
	}
	return &submoduleError{path: sub, err: err}
}

// mapSubmoduleError maps the error of a submodule's git like mapError maps the root's, with
// the submodule named in the message or the cause. A filter driver of the submodule that
// cannot be switched off is the project's WORKSPACE_UNTRUSTED, but its message does not
// blame the root's own configuration.
func mapSubmoduleError(root string, se *submoduleError) error {
	switch {
	case errors.Is(se.err, ErrUnsafeDriver):
		return capability.Errorf(capability.ErrWorkspaceUntrusted, "the project root %q is not trusted, and a filter driver configured in its submodule %q cannot be switched off, so git was not run there", root, se.path).
			WithCause(se.Error()).
			WithNextStep(policy.UntrustedNextStep(root))
	case errors.Is(se.err, ErrGitFailed):
		return capability.Errorf(CodeFailed, "git failed in the submodule %q", se.path).
			WithCause("submodule " + se.path + ": " + strings.TrimPrefix(se.err.Error(), ErrGitFailed.Error()+": "))
	}
	err := mapError(root, se.err)
	var ce *capability.Error
	if errors.As(err, &ce) && ce.Code == CodeFailed {
		ce.WithCause("submodule " + se.path + ": " + ce.Cause)
	}
	return err
}

// walker is one git.submodules walk.
type walker struct {
	ctx       context.Context
	r         *runner
	env       platform.Env
	root      string // the workspace's canonical root: every git runs at or below it
	trusted   bool
	out       []Submodule
	truncated bool
}

// add appends one row, or stops the walk when the list is full: the row that would be
// the 1001st is the proof that there are more.
func (w *walker) add(s Submodule) error {
	if len(w.out) >= maxSubmodules {
		w.truncated = true
		return errFull
	}
	w.out = append(w.out, s)
	return nil
}

// level lists the submodules of the repository whose work tree is `top`, that is the
// directory cwd (inside the workspace) lies in, at below from top ("" or "app/"); only
// the paths under below are the workspace's. cwd's workspace-relative path is rel ("" for
// the workspace root). Every git runs in cwd or in a submodule directory that has passed
// the containment check.
func (w *walker) level(cwd, top, below, rel string, depth int) error {
	err := w.listLevel(cwd, top, below, rel, depth)
	if rel == "" {
		return err
	}
	return inSubmodule(rel, err) // its .gitmodules or ls-files failed: name it
}

func (w *walker) listLevel(cwd, top, below, rel string, depth int) error {
	links, cut, err := w.children(cwd, top, below)
	if err != nil {
		return err
	}
	if cut {
		w.truncated = true
	}
	for _, l := range links {
		sub := path.Join(rel, l.path)
		sm, ok, err := w.read(sub, l)
		if err != nil {
			return inSubmodule(sub, err)
		}
		if !ok {
			continue
		}
		if err := w.add(sm); err != nil {
			return err
		}
		if sm.State != StateUninitialised && depth < maxSubmoduleDepth {
			abs := filepath.Join(w.root, filepath.FromSlash(sub))
			if err := w.level(abs, abs, "", sub, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// children lists the gitlinks the repository whose work tree is top names in its
// `.gitmodules` under below, sorted by path, relative to below: the listing that
// git.submodules and the watch's submodule pass (submodulepass.go) both walk. No
// `.gitmodules` there, or one that is not a regular file, is no submodules and starts no git.
// cut says the list of paths passed the runner's cap, so it may be incomplete.
func (w *walker) children(cwd, top, below string) (links []gitlink, cut bool, err error) {
	gm := filepath.Join(top, ".gitmodules")
	if fi, err := os.Lstat(gm); err != nil || !fi.Mode().IsRegular() {
		return nil, false, nil // no submodules; a symlinked .gitmodules is not read
	}
	res, err := w.r.run(w.ctx, cwd, w.trusted, nil,
		"config", "--file", gm, "--no-includes", "-z", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		return nil, false, err
	}
	switch res.ExitCode {
	case 0:
	case 1: // no such key
		return nil, false, nil
	default:
		return nil, false, gitError(res)
	}
	cands, cut := candidates(res.Stdout, res.Truncated, below)
	links, err = w.gitlinks(cwd, cands)
	if err != nil {
		return nil, false, err
	}
	return links, cut, nil
}

// candidates are the sorted, distinct paths `.gitmodules` names under below, relative to
// below. A path that is not a plain relative one (absolute, with `..`, `.git` or empty
// components, not in its cleaned form) is dropped. cut says the output passed the cap, so
// its last record is not trusted.
func candidates(out []byte, cut bool, below string) ([]string, bool) {
	recs := strings.Split(string(out), "\x00")
	recs = recs[:len(recs)-1] // the terminator of the last record
	if cut {
		recs = recs[:max(len(recs)-1, 0)]
	}
	seen := map[string]bool{}
	var list []string
	for _, rec := range recs {
		_, p, ok := strings.Cut(rec, "\n") // key, newline, value
		if !ok || !plainRelative(p) {
			continue
		}
		p, ok = strings.CutPrefix(p, below)
		if !ok || p == "" || seen[p] {
			continue
		}
		seen[p] = true
		list = append(list, p)
	}
	sort.Strings(list)
	return list, cut
}

// plainRelative: a path git would take as one relative path inside the repository, in
// the form path.Clean gives it, with no `.git` component.
func plainRelative(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if strings.EqualFold(c, ".git") {
			return false
		}
	}
	return true
}

// gitlink is an index entry of mode 160000.
type gitlink struct {
	path, oid string
}

// gitlinks keeps, of the candidates, those the index records as gitlinks, sorted by
// path. The candidates go to ls-files as literal pathspecs after `--`, in chunks.
func (w *walker) gitlinks(cwd string, cands []string) ([]gitlink, error) {
	want := make(map[string]bool, len(cands))
	for _, c := range cands {
		want[c] = true
	}
	type entry struct {
		oid   string
		stage int
	}
	found := map[string]entry{}
	// ask lists the index entries of the given candidates and keeps the gitlinks; cut says
	// the output passed the runner's cap.
	ask := func(chunk []string) (cut bool, err error) {
		args := append(hist("ls-files", "--stage", "-z", "--"), chunk...)
		res, err := w.r.run(w.ctx, cwd, w.trusted, nil, args...)
		if err != nil {
			return false, err
		}
		if res.ExitCode != 0 {
			return false, gitError(res)
		}
		gitlinkRecords(res, func(p, oid string, st int) {
			if !want[p] {
				return
			}
			if old, dup := found[p]; dup && old.stage == 0 {
				return
			}
			found[p] = entry{oid, st}
		})
		return res.Truncated, nil
	}
	for len(cands) > 0 {
		n, size := 0, 0
		for n < len(cands) && (n == 0 || size+len(cands[n])+1 <= pathspecChunk) {
			size += len(cands[n]) + 1
			n++
		}
		chunk := cands[:n]
		cands = cands[n:]
		cut, err := ask(chunk)
		if err != nil {
			return nil, err
		}
		if !cut || len(chunk) == 1 {
			// A candidate asked alone whose output is cut can only lose entries below it:
			// a literal pathspec matches the path itself or what is under it, and a gitlink
			// has nothing under it. So that cut hides no submodule and is not "truncated".
			continue
		}
		// A stale .gitmodules entry naming a huge ordinary directory filled the cap and the
		// gitlinks after it were dropped: ask again one candidate at a time.
		for _, c := range chunk {
			if _, err := ask([]string{c}); err != nil {
				return nil, err
			}
		}
	}
	out := make([]gitlink, 0, len(found))
	for p, e := range found {
		out = append(out, gitlink{p, e.oid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// gitlinkRecords reads `ls-files --stage -z` output and calls yield for each gitlink record
// (mode 160000) of it: its path, its oid and its stage. A record cut at the output cap is
// not read. A conflicted gitlink has up to three entries: the merged stage 0, else "ours"
// (stage 2) are what HEAD has; the base and "theirs" (stages 1 and 3) are not yielded. The
// one place that reads a gitlink record: the walk and git's `root` share it.
func gitlinkRecords(res result, yield func(p, oid string, stage int)) {
	recs := strings.Split(string(res.Stdout), "\x00")
	recs = recs[:len(recs)-1]
	if res.Truncated {
		recs = recs[:max(len(recs)-1, 0)]
	}
	for _, rec := range recs {
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[0] != gitlinkMode {
			continue
		}
		st := int(f[2][0] - '0')
		if st == 1 || st == 3 {
			continue
		}
		yield(p, f[1], st)
	}
}

// placement is where a gitlink's directory stands as to running a git in it.
type placement int

const (
	// placedOut: the directory is, or sits behind, a symlink, cannot be resolved, or holds a
	// .git that is a symlink: no git may run there.
	placedOut placement = iota
	// placedUninitialised: no directory, no .git, or a .git that is no repository of its own.
	placedUninitialised
	// placedInitialised: a repository of its own.
	placedInitialised
	// placedWithGitFile is stat's answer only: a .git is there and no git has been asked.
	placedWithGitFile
)

// stat is the containment of a submodule's directory, checked without a git: the directory
// at the workspace-relative path sub exists, file.ResolveDir resolves it to exactly its
// lexical join with the workspace root (so no symlink on the way, out of the root or out of
// a subdirectory workspace), and it holds a .git that is not a symlink. A submodule that has
// been initialised has a .git there: a file, or a directory. Git never makes a symlink of
// it, and one would send git to a git directory the workspace does not hold (a .git file may
// too, which is the superproject's own business; see the package comment), so without a
// .git, or with a symlink for one, no git runs. It answers placedOut, placedUninitialised or
// placedWithGitFile.
func (w *walker) stat(sub string) (abs string, p placement) {
	abs = filepath.Join(w.root, filepath.FromSlash(sub))
	fi, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return abs, placedUninitialised // a gitlink whose directory was never made
	}
	if err != nil || !fi.IsDir() {
		return abs, placedOut
	}
	if got, rerr := file.ResolveDir(w.env, w.root, sub); rerr != nil || got != abs {
		return abs, placedOut
	}
	gi, err := os.Lstat(filepath.Join(abs, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return abs, placedUninitialised
	}
	if err != nil || gi.Mode()&os.ModeSymlink != 0 {
		return abs, placedOut
	}
	return abs, placedWithGitFile
}

// place is stat and then, for a directory with a .git, the one git that says whether it is a
// repository of its own: rev-parse in it (a submodule is its own top; an uninitialised one
// is inside the superproject). It answers placedOut, placedUninitialised or placedInitialised;
// the error is that git's.
func (w *walker) place(sub string) (abs string, p placement, err error) {
	abs, p = w.stat(sub)
	if p != placedWithGitFile {
		return abs, p, nil
	}
	prefix, err := w.r.prefix(w.ctx, abs, w.trusted)
	if errors.Is(err, ErrNotARepository) {
		return abs, placedUninitialised, nil
	}
	if err != nil {
		return abs, placedOut, err
	}
	if prefix != "" { // inside the superproject, not a repository of its own
		return abs, placedUninitialised, nil
	}
	return abs, placedInitialised, nil
}

// read describes one gitlink at the workspace-relative path sub. ok is false when the
// submodule is left out: its directory is, or sits behind, a symlink (or cannot be
// resolved), so no git may run there.
func (w *walker) read(sub string, l gitlink) (sm Submodule, ok bool, err error) {
	sm = Submodule{Path: sub, Recorded: l.oid, State: StateUninitialised}
	abs, p, err := w.place(sub)
	switch {
	case err != nil:
		return sm, false, err
	case p == placedOut:
		return sm, false, nil
	case p == placedUninitialised:
		return sm, true, nil
	}
	head, err := headOid(w.ctx, w.r, abs, w.trusted)
	if err != nil {
		return sm, false, err
	}
	sm.CheckedOut = head
	res, err := w.r.run(w.ctx, abs, w.trusted, nil, submoduleStateArgs...)
	if err != nil {
		return sm, false, err
	}
	if res.ExitCode != 0 {
		return sm, false, gitError(res)
	}
	st := parseStatus(res.Stdout, "", maxEntries, res.Truncated)
	switch {
	case len(st.Entries) > 0 || st.Truncated:
		sm.State = StateDirty
	case head != l.oid:
		sm.State = StateOutdated
	default:
		sm.State = StateClean
	}
	return sm, true, nil
}

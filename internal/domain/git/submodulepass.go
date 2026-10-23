package git

// The submodule pass (plan m3b step 2; ADR-0125 §2-§4): after the project's status, one
// status in each submodule that may hold changes, so that the Git tab and Review show what
// is uncommitted inside a submodule.
//
// Which submodules. The pass runs only when the repository's top has a `.gitmodules` (an
// Lstat), and walks the same listing git.submodules does (walker.children), depth-first, to
// eight levels. On a trusted root git itself looked inside each populated submodule (the
// status carries `--ignore-submodules=none`) and flagged what it found in the record's
// S<c><m><u> field, which the entry keeps as git gave it, so only a gitlink flagged M or U is
// read here. On an untrusted root git looked at no submodule (`dirty`), so every initialised
// submodule is read and the bits of its gitlink are computed from what its status holds; a
// trusted status that fell back to `dirty` (runner.status) is read the same way, which is
// how the submodule that made git fail comes out unread. The mode is per status: each
// submodule's own status decides how its nested submodules are read, except that the
// submodules of a computed repository are read with `dirty` (statusIn's dirtyOnly), so that a
// pass after a fallback has no `none` in it.
//
// What is not read says so. A gitlink entry the pass does not read, whose contents are in
// question (trusted: git flagged M or U; computed: any entry), is marked unread with a cause:
// not listed in .gitmodules (also when there is none), nested deeper than eight levels, not
// contained in the project, its .git is a symbolic link. One with no directory of its own is
// not in question. A nested gitlink that is unread is no change of its parent's content.
//
// How. Each read is one status with the watch's arguments (statusArgs, so its own nesting
// follows the same rule) through the runner in the submodule's own directory, with the
// workspace's trust, after the containment checks of git.submodules (walker.place), rerun
// every tick. On an untrusted root that puts the filter drivers read and switched off in
// that repository's own configuration, where the project's read does not see them.
//
// Bounds. At most maxPassSubmodules are read per tick and the whole pass has one deadline
// (passTimeout). What the cap or the deadline leaves unread is said by Status.SubmodulesUnread
// and, on the gitlinks the status has an entry for, by Unread. A git that fails in a
// submodule, or in the listing of its nested ones, marks that gitlink unread (its entry is
// created if the status had none), lists none of its files and reads none of its nested
// ones; the pass goes on. Only a cancel of the tick's context ends it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// maxPassSubmodules is the most submodules one pass reads, nested ones included.
	maxPassSubmodules = 32
	// passTimeout is the deadline of one pass.
	passTimeout = 5 * time.Second
	// maxCause is the longest cause an Unread carries.
	maxCause = 300
)

// subPass is one submodule pass.
type subPass struct {
	w *walker // its ctx is the pass's: the tick's with the pass's deadline
	// ctx is the pass's context; parent is the tick's. A parent that is done ends the tick;
	// a ctx that is done alone is the pass's deadline.
	ctx, parent context.Context
	trusted     bool
	limit, left int

	stopped   bool   // the cap or the deadline was reached: nothing more is started
	cause     string // what Unread says of a gitlink the stop left unread
	unread    bool   // Status.SubmodulesUnread
	truncated bool   // a submodule's git output passed its cap
}

// submodulePass adds the contents of the submodules to st, the project's status at prefix
// (the workspace's path from the repository's top). It returns st itself when there is
// nothing to read; an error only for a cancel of ctx.
func (o Options) submodulePass(ctx context.Context, r *runner, root string, trusted bool, prefix string, st Status) (Status, error) {
	computed := !trusted || st.fellBack
	if !computed && !anyFlagged(st.Entries) {
		return st, nil // git flagged no submodule: nothing to read, nothing to list
	}
	top := topOf(root, prefix)
	timeout, limit := o.passTimeout, o.passCap
	if timeout <= 0 {
		timeout = passTimeout
	}
	if limit <= 0 {
		limit = maxPassSubmodules
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p := &subPass{
		w:   &walker{ctx: pctx, r: r, env: o.Env, root: root, trusted: trusted},
		ctx: pctx, parent: ctx, trusted: trusted, limit: limit, left: limit,
	}
	ents := st.Entries
	below, err := p.level(root, top, prefix, "", 1, &ents, computed)
	if err != nil {
		if ctx.Err() != nil {
			return Status{}, ctx.Err()
		}
		// The project's own submodules could not be listed (or the deadline ran out in the
		// listing): no gitlink can say so.
		if p.ctx.Err() != nil {
			p.halt()
		}
		p.unread = true
		below = nil
	}
	st.Entries = append(ents, below...)
	sort.SliceStable(st.Entries, func(i, j int) bool { return st.Entries[i].Path < st.Entries[j].Path })
	if len(st.Entries) > maxEntries {
		st.Entries, st.Truncated = st.Entries[:maxEntries], true
	}
	st.Truncated = st.Truncated || p.truncated
	st.SubmodulesUnread = p.unread
	return st, nil
}

// topOf is the top of the repository holding root, whose workspace prefix ("" or "app/")
// status read.
func topOf(root, prefix string) string {
	top := root
	for range strings.Count(prefix, "/") {
		top = filepath.Dir(top)
	}
	return top
}

// hasGitmodules: the repository whose top is top has a `.gitmodules` that is a regular file.
func hasGitmodules(top string) bool {
	fi, err := os.Lstat(filepath.Join(top, ".gitmodules"))
	return err == nil && fi.Mode().IsRegular()
}

// level reads the submodules of the repository whose work tree is top (cwd lies in it, below
// is the workspace's path from it, rel the repository's own workspace-relative path: "" for
// the project). ents are the entries of that repository's own status, in workspace paths,
// which the pass updates in place: the bits, checkedOut and unread of its gitlinks. computed
// says git did not flag them (see the file comment). It returns the entries of the
// submodules below. A non-nil error is the failure of the listing, or the cancel of the tick.
func (p *subPass) level(cwd, top, below, rel string, depth int, ents *[]Entry, computed bool) ([]Entry, error) {
	if !computed && !anyFlagged(*ents) {
		return nil, nil // git flagged no submodule: nothing to read, nothing to list
	}
	// A gitlink the pass does not read says so when its contents are in question: on a trusted
	// root git flagged it, on a computed one any entry of it is. One with no directory of its
	// own is not in question.
	if depth > maxSubmoduleDepth {
		p.unreadRest(ents, nil, computed, "nested deeper than eight levels", false)
		return nil, nil
	}
	if !hasGitmodules(top) {
		p.unreadRest(ents, nil, computed, "not listed in .gitmodules", true)
		return nil, nil
	}
	if p.stopped || p.ctx.Err() != nil {
		if p.parent.Err() != nil {
			return nil, p.parent.Err()
		}
		// Not listing: the status's own gitlinks are what can still be marked.
		if !p.stopped {
			p.halt()
		}
		for _, e := range append([]Entry(nil), *ents...) {
			if e.Submodule != nil && (computed || flagged(e)) {
				p.skip(ents, e.Path)
			}
		}
		return nil, nil
	}
	links, cut, err := p.w.children(cwd, top, below)
	if err != nil {
		return nil, err
	}
	if cut {
		p.unread = true
	}
	var out []Entry
	listed := map[string]bool{}
	for _, l := range links {
		sub := path.Join(rel, l.path)
		listed[sub] = true
		if !computed {
			if i := find(*ents, sub); i < 0 || !flagged((*ents)[i]) {
				continue
			}
		}
		got, err := p.one(sub, depth, rel, ents, computed)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	p.unreadRest(ents, listed, computed, "not listed in .gitmodules", true)
	return out, nil
}

// unreadRest marks unread the gitlink entries of ents that the pass did not look at, because
// they are not among listed (the submodules .gitmodules names that the index records as
// gitlinks) or the level is too deep, when their contents are in question (trusted: git
// flagged M or U; computed: any entry). A gitlink with no directory of its own is not in
// question. A directory the containment checks refuse is named so instead of why when
// refusalFirst. The cause is a sentence.
func (p *subPass) unreadRest(ents *[]Entry, listed map[string]bool, computed bool, why string, refusalFirst bool) {
	for i := range *ents {
		e := &(*ents)[i]
		if listed[e.Path] || e.Submodule == nil || e.Submodule.Unread != "" || (!computed && !flagged(*e)) {
			continue
		}
		cause := why
		switch _, k := p.w.stat(e.Path); k {
		case placedUninitialised:
			continue
		case placedOut:
			if refusalFirst {
				cause = "not contained in the project"
			}
		case placedLinked:
			if refusalFirst {
				cause = "its .git is a symbolic link"
			}
		}
		e.Submodule.Unread = cause
	}
}

// one reads the submodule at sub, of the repository at rel whose entries are ents. Its files
// and its nested submodules' come back; the error is only the cancel of the tick.
func (p *subPass) one(sub string, depth int, rel string, ents *[]Entry, computed bool) ([]Entry, error) {
	if p.stopped || p.left <= 0 || p.ctx.Err() != nil {
		if p.parent.Err() != nil {
			return nil, p.parent.Err()
		}
		p.skip(ents, sub)
		return nil, nil
	}
	abs, placed, err := p.w.place(sub)
	if err != nil {
		return p.failed(ents, sub, rel, err, "")
	}
	switch {
	case placed.refused():
		// No git runs there; the gitlink says why, if the status has an entry for it.
		if i := find(*ents, sub); i >= 0 && gitlinkOf(&(*ents)[i]).Unread == "" {
			gitlinkOf(&(*ents)[i]).Unread = "not contained in the project"
			if placed == placedLinked {
				gitlinkOf(&(*ents)[i]).Unread = "its .git is a symbolic link"
			}
		}
		return nil, nil
	case placed != placedInitialised:
		return nil, nil
	}
	p.left--
	// The submodules of a repository git did not flag (computed) are read with `dirty`, as on
	// an untrusted root: a trusted `none` there would be the call that just failed or ran out
	// of time.
	st, err := p.w.r.statusIn(p.ctx, abs, p.trusted, "", computed)
	if err != nil {
		return p.failed(ents, sub, rel, err, "")
	}
	mine := make([]Entry, len(st.Entries))
	for i, e := range st.Entries {
		e.Path, e.Root = path.Join(sub, e.Path), sub
		if e.OrigPath != "" {
			e.OrigPath = path.Join(sub, e.OrigPath)
		}
		mine[i] = e
	}
	deeper, err := p.level(abs, abs, "", sub, depth+1, &mine, !p.trusted || st.fellBack)
	if err != nil {
		return p.failed(ents, sub, rel, err, "its submodules could not be listed: ")
	}
	p.truncated = p.truncated || st.Truncated

	// The gitlink's entry: where the submodule is checked out and, when git did not say, what
	// changed in it.
	i := find(*ents, sub)
	m, u := contentBits(mine)
	if computed && i < 0 && (m || u) {
		i = p.entry(ents, sub, rel)
	}
	if i >= 0 {
		g := gitlinkOf(&(*ents)[i])
		g.CheckedOut = st.Head.Oid
		if computed {
			g.Modified, g.Untracked = m, u
		}
	}
	return append(mine, deeper...), nil
}

// failed marks the gitlink at sub unread after err, which is not one of the tick's own cancel
// (that one ends it), and drops what was read of it. The pass's deadline passing is not the
// submodule's fault: it stops the pass. prefix leads the cause.
func (p *subPass) failed(ents *[]Entry, sub, rel string, err error, prefix string) ([]Entry, error) {
	if p.parent.Err() != nil {
		return nil, p.parent.Err()
	}
	cause := ""
	if p.ctx.Err() != nil {
		p.halt()
		cause = p.cause
	} else {
		cause = prefix + causeOf(err)
	}
	i := p.entry(ents, sub, rel)
	gitlinkOf(&(*ents)[i]).Unread = limitCause(cause)
	return nil, nil
}

// halt stops the pass: no git starts from here, and Status.SubmodulesUnread is set.
func (p *subPass) halt() {
	if p.stopped {
		return
	}
	p.stopped, p.unread = true, true
	if p.ctx.Err() != nil {
		p.cause = "the time allowed for reading submodules in one tick ran out before it"
	} else {
		p.cause = fmt.Sprintf("more than %d submodules are read in one tick, and this one is past the limit", p.limit)
	}
}

// skip is a candidate the pass does not read because it has stopped or must stop now. When
// its directory looks like a submodule of its own (no git is run to ask) the pass is
// incomplete, and the gitlink says so if the status has an entry for it.
func (p *subPass) skip(ents *[]Entry, sub string) {
	if _, k := p.w.stat(sub); k != placedWithGitFile {
		return
	}
	p.halt()
	if i := find(*ents, sub); i >= 0 {
		gitlinkOf(&(*ents)[i]).Unread = limitCause(p.cause)
	}
}

// entry is the index of the entry for the gitlink at sub in ents, which it appends (marked
// modified, with the repository at rel as its root) when the status had none.
func (p *subPass) entry(ents *[]Entry, sub, rel string) int {
	i := find(*ents, sub)
	if i < 0 {
		*ents = append(*ents, Entry{Path: sub, Mark: MarkModified, Root: rel, Submodule: &SubmoduleChange{}})
		i = len(*ents) - 1
	}
	return i
}

// gitlinkOf is the entry's submodule object, made when the status gave none (a path that is
// a gitlink here and was something else there).
func gitlinkOf(e *Entry) *SubmoduleChange {
	if e.Submodule == nil {
		e.Submodule = &SubmoduleChange{}
	}
	return e.Submodule
}

// find is the index of the entry with the path, or -1.
func find(ents []Entry, p string) int {
	for i := range ents {
		if ents[i].Path == p {
			return i
		}
	}
	return -1
}

// flagged: git said the submodule holds modified or untracked content.
func flagged(e Entry) bool {
	return e.Submodule != nil && (e.Submodule.Modified || e.Submodule.Untracked)
}

func anyFlagged(ents []Entry) bool {
	for _, e := range ents {
		if flagged(e) {
			return true
		}
	}
	return false
}

// contentBits computes, from the entries of a submodule's own status (its nested gitlinks'
// bits already final), what git writes in its gitlink's S<c><m><u> field: git's own rule
// (is_submodule_modified): untracked is an untracked file or a nested gitlink whose untracked
// is set; modified is any other change, a nested record other than exactly S..U included (a
// nested gitlink with new commits, modified content, or any change but untracked files).
func contentBits(own []Entry) (modified, untracked bool) {
	for _, e := range own {
		switch {
		case e.Submodule != nil && e.Submodule.Unread != "":
			// Not read: it says nothing of the parent's content.
		case e.Submodule != nil:
			s := e.Submodule
			untracked = untracked || s.Untracked
			modified = modified || s.Commits || s.Modified || !s.Untracked
		case e.Mark == MarkUntracked:
			untracked = true
		default:
			modified = true
		}
	}
	return modified, untracked
}

// causeOf is the sentence an Unread carries for a failed git.
func causeOf(err error) string {
	if errors.Is(err, ErrUnsafeDriver) {
		return "git was not run there: " + err.Error()
	}
	return err.Error()
}

// limitCause keeps a cause to its first line and maxCause bytes.
func limitCause(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > maxCause {
		s = strings.ToValidUTF8(s[:maxCause], "") + "..."
	}
	return s
}

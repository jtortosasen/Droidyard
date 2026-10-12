package git

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxEntries is the most entries a status carries; more set Status.Truncated.
const maxEntries = 5000

// Errors status returns besides the runner's.
var (
	// ErrNotARepository: the root is in no work tree (the registration maps it to
	// GIT_NOT_A_REPOSITORY).
	ErrNotARepository = errors.New("not a git repository")
	// ErrGitFailed: git exited non-zero for another reason; the text is git's first
	// stderr line (GIT_FAILED's cause).
	ErrGitFailed = errors.New("git failed")
)

// Mark is how a path differs from HEAD.
type Mark string

// The marks, from porcelain v2's XY by ADR-0066 §5.
const (
	MarkModified   Mark = "modified"
	MarkAdded      Mark = "added"
	MarkUntracked  Mark = "untracked"
	MarkDeleted    Mark = "deleted"
	MarkRenamed    Mark = "renamed"
	MarkConflicted Mark = "conflicted"
)

// Head is what HEAD points at.
type Head struct {
	Oid    string `json:"oid,omitempty" jsonschema_description:"HEAD's commit id; absent in a repository with no commit yet."`
	Branch string `json:"branch,omitempty" jsonschema_description:"The checked-out branch; absent when HEAD is detached."`
}

// Entry is one path that differs from HEAD.
type Entry struct {
	Path     string `json:"path" jsonschema:"required" jsonschema_description:"The path relative to the project root."`
	Mark     Mark   `json:"mark" jsonschema:"required,enum=modified,enum=added,enum=untracked,enum=deleted,enum=renamed,enum=conflicted" jsonschema_description:"How the path differs from HEAD in the working tree."`
	OrigPath string `json:"origPath,omitempty" jsonschema_description:"For a renamed path: where it was in HEAD, relative to the project root."`
}

// Status is the working tree against HEAD.
type Status struct {
	Head      Head    `json:"head" jsonschema:"required" jsonschema_description:"What HEAD points at."`
	Entries   []Entry `json:"entries" jsonschema:"required" jsonschema_description:"The paths that differ from HEAD, sorted by path; at most 5000."`
	Truncated bool    `json:"truncated" jsonschema:"required" jsonschema_description:"True when more than 5000 paths differ and the rest are left out."`
}

// status reads the status of the repository holding root: the workspace's own paths,
// relative to root. trusted is policy.Trusted's answer for root.
func (r *runner) status(ctx context.Context, root string, trusted bool) (Status, error) {
	prefix, err := r.prefix(ctx, root, trusted)
	if err != nil {
		return Status{}, err
	}
	res, err := r.run(ctx, root, trusted, nil, statusArgs...)
	if err != nil {
		return Status{}, err
	}
	if res.ExitCode != 0 {
		return Status{}, gitError(res)
	}
	return parseStatus(res.Stdout, prefix, maxEntries, res.Truncated), nil
}

// prefix is the workspace's path from the repository's top ("" or "app/"). A root
// that is not in a work tree (inside .git/, a bare repository, outside a core.worktree
// set elsewhere) is ErrNotARepository: there --show-prefix prints "", and a path read
// from the top would leave the root.
func (r *runner) prefix(ctx context.Context, root string, trusted bool) (string, error) {
	res, err := r.run(ctx, root, trusted, nil, "rev-parse", "--is-inside-work-tree", "--show-prefix")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", gitError(res)
	}
	inside, prefix, _ := strings.Cut(string(res.Stdout), "\n")
	if inside != "true" {
		return "", ErrNotARepository
	}
	return strings.TrimSuffix(prefix, "\n"), nil
}

// gitError maps a failed call: git's own "not a git repository" is ErrNotARepository.
func gitError(res result) error {
	line := firstLine(res.Stderr)
	if strings.Contains(line, "not a git repository") {
		return ErrNotARepository
	}
	return fmt.Errorf("%w: %s", ErrGitFailed, line)
}

// statusArgs is the one status command (plan m2l Q1 §2): the paths come from the
// repository's top and the workspace's prefix is stripped by parseStatus.
var statusArgs = []string{
	"status", "--porcelain=v2", "-z", "--branch", "--no-ahead-behind",
	"--untracked-files=all", "--ignore-submodules=all", "--find-renames", "--", ".",
}

// parseStatus turns porcelain v2 `-z` output with --branch into a Status. prefix is
// the workspace's path from the repository's top ("" or "app/"); entries outside it
// are dropped and the rest made relative to it. cut says the output itself was cut at
// the byte cap, so the last record is not trusted and the status is truncated.
//
// The decisions (ADR-0066 §5):
//   - `?` is untracked (a nested repository, listed as `dir/`, loses the slash); `u` is
//     conflicted.
//   - A rename in either column (`R.`, `RM`, `.R`) is renamed with origPath; `RD` is
//     its origin deleted and nothing at the new path; a copy (`C`) is added.
//   - `AD` is nothing; `D.`, `.D` and `MD` are deleted; `A.`, `AM`, `.A` are added;
//     any other `M` or `T` is modified.
//   - A deleted path that is also untracked is one entry, modified.
//   - A rename whose origin is outside the prefix is added; one whose new path is
//     outside reports the origin deleted.
func parseStatus(raw []byte, prefix string, limit int, cut bool) Status {
	recs := strings.Split(string(raw), "\x00")
	if cut {
		recs = recs[:len(recs)-1] // the last record may be half a path
	}
	st := Status{Entries: []Entry{}, Truncated: cut}
	in := func(p string) (string, bool) { return strings.CutPrefix(p, prefix) }
	byPath := map[string]int{}
	add := func(p string, m Mark, orig string) {
		if i, ok := byPath[p]; ok {
			a := st.Entries[i].Mark
			if (a == MarkDeleted && m == MarkUntracked) || (a == MarkUntracked && m == MarkDeleted) {
				st.Entries[i] = Entry{Path: p, Mark: MarkModified}
			}
			return
		}
		byPath[p] = len(st.Entries)
		st.Entries = append(st.Entries, Entry{Path: p, Mark: m, OrigPath: orig})
	}
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		switch {
		case strings.HasPrefix(rec, "# branch.oid "):
			if oid := rec[len("# branch.oid "):]; oid != "(initial)" {
				st.Head.Oid = oid
			}
		case strings.HasPrefix(rec, "# branch.head "):
			if b := rec[len("# branch.head "):]; b != "(detached)" {
				st.Head.Branch = b
			}
		case strings.HasPrefix(rec, "? "):
			if p, ok := in(strings.TrimSuffix(rec[2:], "/")); ok {
				add(p, MarkUntracked, "")
			}
		case strings.HasPrefix(rec, "u "):
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 {
				if p, ok := in(f[10]); ok {
					add(p, MarkConflicted, "")
				}
			}
		case strings.HasPrefix(rec, "1 "):
			f := strings.SplitN(rec, " ", 9)
			if len(f) < 9 {
				continue
			}
			if m, ok := markOf(f[1]); ok {
				if p, ok := in(f[8]); ok {
					add(p, m, "")
				}
			}
		case strings.HasPrefix(rec, "2 "):
			// The origin is the next record (-z).
			f := strings.SplitN(rec, " ", 10)
			if len(f) < 10 || i+1 >= len(recs) {
				continue
			}
			i++
			rename(f[1], f[9], recs[i], in, add)
		}
	}
	sort.Slice(st.Entries, func(i, j int) bool { return st.Entries[i].Path < st.Entries[j].Path })
	if len(st.Entries) > limit {
		st.Entries, st.Truncated = st.Entries[:limit], true
	}
	return st
}

// markOf maps the XY of a `1` record; false is "no entry".
func markOf(xy string) (Mark, bool) {
	if len(xy) != 2 {
		return "", false
	}
	x, y := xy[0], xy[1]
	switch {
	case xy == "AD":
		return "", false
	case x == 'D' || y == 'D':
		return MarkDeleted, true
	case x == 'A' || y == 'A':
		return MarkAdded, true
	}
	return MarkModified, true
}

// rename maps a `2` record: its xy, new path and origin.
func rename(xy, path, orig string, in func(string) (string, bool), add func(string, Mark, string)) {
	if len(xy) != 2 {
		return
	}
	p, pIn := in(path)
	o, oIn := in(orig)
	isRename := xy[0] == 'R' || xy[1] == 'R'
	switch {
	case xy[1] == 'D':
		// The new path is gone from the working tree; a rename leaves its origin deleted.
		if isRename && oIn {
			add(o, MarkDeleted, "")
		}
	case !isRename: // a copy: the new path is added, the origin stays
		if pIn {
			add(p, MarkAdded, "")
		}
	case pIn && oIn:
		add(p, MarkRenamed, o)
	case pIn:
		add(p, MarkAdded, "")
	case oIn:
		add(o, MarkDeleted, "")
	}
}

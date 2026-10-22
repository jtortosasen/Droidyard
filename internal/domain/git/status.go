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
	Path      string           `json:"path" jsonschema:"required" jsonschema_description:"The path relative to the project root."`
	Mark      Mark             `json:"mark" jsonschema:"required,enum=modified,enum=added,enum=untracked,enum=deleted,enum=renamed,enum=conflicted" jsonschema_description:"How the path differs from HEAD in the working tree."`
	OrigPath  string           `json:"origPath,omitempty" jsonschema_description:"For a renamed path: where it was in HEAD, relative to the project root."`
	Root      string           `json:"root,omitempty" jsonschema_description:"The submodule the path is in, relative to the project root, as git.read's root takes it; absent for the project's own repository."`
	Submodule *SubmoduleChange `json:"submodule,omitempty" jsonschema_description:"Present only on a submodule's own entry (the path of its gitlink): what changed in it."`
}

// SubmoduleChange is what differs in a submodule, on its gitlink's entry (ADR-0125 §3). The
// three bits are git's own S<c><m><u> field of a porcelain v2 record.
type SubmoduleChange struct {
	Commits    bool   `json:"commits" jsonschema:"required" jsonschema_description:"The submodule's checked-out commit differs from the one HEAD records, staged or not (git's C, or a new commit that is staged)."`
	Modified   bool   `json:"modified" jsonschema:"required" jsonschema_description:"A tracked file in the submodule differs from its HEAD, or a nested submodule of it changed (git's M)."`
	Untracked  bool   `json:"untracked" jsonschema:"required" jsonschema_description:"The submodule holds untracked files (git's U)."`
	CheckedOut string `json:"checkedOut,omitempty" jsonschema_description:"The submodule's HEAD commit, when its contents were read."`
	Unread     string `json:"unread,omitempty" jsonschema_description:"Why the submodule's contents could not be read; absent when they were. Its files are then not listed."`
}

// Status is the working tree against HEAD.
type Status struct {
	Head             Head    `json:"head" jsonschema:"required" jsonschema_description:"What HEAD points at."`
	Entries          []Entry `json:"entries" jsonschema:"required" jsonschema_description:"The paths that differ from HEAD, sorted by path; at most 5000."`
	Truncated        bool    `json:"truncated" jsonschema:"required" jsonschema_description:"True when more than 5000 paths differ, or a git's output (the project's or a submodule's) passed its 4 MiB cap, and the rest are left out."`
	SubmodulesUnread bool    `json:"submodulesUnread,omitempty" jsonschema_description:"True when some submodule could not be read and no entry says which: the project's submodules could not be listed, or the per-tick count or time bound was reached. Absent otherwise."`

	// fellBack: a trusted root's `none` status failed and this is its `dirty` retry, so
	// git flagged no submodule's contents (see runner.status). Not published.
	fellBack bool
}

// status reads the status of the repository holding root: the workspace's own paths,
// relative to root. trusted is policy.Trusted's answer for root.
func (r *runner) status(ctx context.Context, root string, trusted bool) (Status, error) {
	prefix, err := r.prefix(ctx, root, trusted)
	if err != nil {
		return Status{}, err
	}
	res, err := r.run(ctx, root, trusted, nil, statusArgs(trusted)...)
	fellBack := false
	// The trusted fallback (ADR-0125 §4). With `none` git runs a child status in each
	// populated submodule and, when one of them fails (a corrupt index, a blob it may not
	// fetch), fails the whole status: exit 128 and no output, the project's own entries
	// lost. Only the project's own failure may stop the watch, so a failed or timed-out
	// `none` status is asked once more with `dirty`, which starts nothing in a submodule.
	// A success is marked fellBack, so the submodule pass reads every initialised submodule
	// as on an untrusted root and the failing one comes out unread. The cancel of ctx is
	// not a git failure and is not retried; neither is a git that did not start. If the
	// `dirty` status fails too, its error is the project's own.
	if trusted && (errors.Is(err, ErrTimeout) || (err == nil && res.ExitCode != 0)) {
		res, err = r.run(ctx, root, trusted, nil, statusArgsIgnoring("dirty")...)
		fellBack = true
	}
	if err != nil {
		return Status{}, err
	}
	if res.ExitCode != 0 {
		return Status{}, gitError(res)
	}
	st := parseStatus(res.Stdout, prefix, maxEntries, res.Truncated)
	st.fellBack = fellBack
	return st, nil
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

// statusArgs is the watch's status command (plan m2l Q1 §2): the paths come from the
// repository's top and the workspace's prefix is stripped by parseStatus.
//
// The --ignore-submodules value is explicit, so it overrides diff.ignoreSubmodules and a
// submodule's `ignore` in the configuration and in .gitmodules (git-status(1)), and it is
// decided by trust (ADR-0125 §1):
//   - Trusted: `none`. git itself looks inside each populated submodule (it runs a child
//     status there, with the submodule's own configuration under this invocation's -c
//     overrides and protocol variables) and flags what it finds in the record's S<c><m><u>
//     field. The root's trust covers the submodules below it.
//   - Untrusted: `dirty`. git reports a gitlink only when its commit differs and starts no
//     process in a submodule, so a filter driver configured only in a submodule's own
//     configuration, which the driver read of this root (ADR-0066 §3) does not see, never
//     runs. The submodules' contents are the daemon's to read, each through the runner in
//     its own directory.
func statusArgs(trusted bool) []string {
	ignore := "dirty"
	if trusted {
		ignore = "none"
	}
	return statusArgsIgnoring(ignore)
}

// statusArgsIgnoring is the status command with the given --ignore-submodules value: the
// seam statusArgs chooses through, and that a test substitutes to show what the other value
// would run.
func statusArgsIgnoring(ignore string) []string {
	return []string{
		"status", "--porcelain=v2", "-z", "--branch", "--no-ahead-behind",
		"--untracked-files=all", "--ignore-submodules=" + ignore, "--find-renames", "--", ".",
	}
}

// submoduleStateArgs is git.submodules' own status of a submodule (ADR-0071 §1): `all`, so
// that its `dirty` state means the submodule's own working tree and not its nested
// submodules'. Not the watch's: that one is statusArgs.
var submoduleStateArgs = statusArgsIgnoring("all")

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
//   - A `1` or `u` record whose <sub> field begins `S` is a gitlink (ADR-0125 §3): its mark is
//     the table's, and it carries Submodule {commits, modified, untracked} from the field's
//     `C`, `M` and `U`, except that a `1` record whose X is `M` (a new commit that is staged)
//     also sets commits: it is against HEAD, as the marks are. Under `dirty` only `C` can be
//     set; under `none` all three, and a gitlink changed only in content is marked modified
//     by its `.M`. A `2` record is read as before and carries no Submodule.
func parseStatus(raw []byte, prefix string, limit int, cut bool) Status {
	recs := strings.Split(string(raw), "\x00")
	if cut {
		recs = recs[:len(recs)-1] // the last record may be half a path
	}
	st := Status{Entries: []Entry{}, Truncated: cut}
	in := func(p string) (string, bool) { return strings.CutPrefix(p, prefix) }
	byPath := map[string]int{}
	add := func(p string, m Mark, orig string, sub *SubmoduleChange) {
		if i, ok := byPath[p]; ok {
			a := st.Entries[i].Mark
			if (a == MarkDeleted && m == MarkUntracked) || (a == MarkUntracked && m == MarkDeleted) {
				st.Entries[i] = Entry{Path: p, Mark: MarkModified}
			}
			return
		}
		byPath[p] = len(st.Entries)
		st.Entries = append(st.Entries, Entry{Path: p, Mark: m, OrigPath: orig, Submodule: sub})
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
				add(p, MarkUntracked, "", nil)
			}
		case strings.HasPrefix(rec, "u "):
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 {
				if p, ok := in(f[10]); ok {
					add(p, MarkConflicted, "", submoduleOf(f[2]))
				}
			}
		case strings.HasPrefix(rec, "1 "):
			f := strings.SplitN(rec, " ", 9)
			if len(f) < 9 {
				continue
			}
			if m, ok := markOf(f[1]); ok {
				if p, ok := in(f[8]); ok {
					sub := submoduleOf(f[2])
					if sub != nil && strings.HasPrefix(f[1], "M") {
						// commits is against HEAD, as the marks are (ADR-0066 §5): a new
						// commit that is staged (`M.`) is in the index, so git's C is clear.
						sub.Commits = true
					}
					add(p, m, "", sub)
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

// submoduleOf reads a record's <sub> field: nil for `N...` (not a submodule), else the bits
// of `S<c><m><u>`, a letter (`C`, `M`, `U`) for each that is set and `.` for each that is
// not.
func submoduleOf(field string) *SubmoduleChange {
	if len(field) != 4 || field[0] != 'S' {
		return nil
	}
	return &SubmoduleChange{Commits: field[1] == 'C', Modified: field[2] == 'M', Untracked: field[3] == 'U'}
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
func rename(xy, path, orig string, in func(string) (string, bool), add func(string, Mark, string, *SubmoduleChange)) {
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
			add(o, MarkDeleted, "", nil)
		}
	case !isRename: // a copy: the new path is added, the origin stays
		if pIn {
			add(p, MarkAdded, "", nil)
		}
	case pIn && oIn:
		add(p, MarkRenamed, o, nil)
	case pIn:
		add(p, MarkAdded, "", nil)
	case oIn:
		add(o, MarkDeleted, "", nil)
	}
}

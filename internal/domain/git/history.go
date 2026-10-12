package git

// The history reads' commands and the tip set (plan m2n step 1; the capabilities that
// run them come in later steps).
//
// What git is asked, and what it is not. A history read names commits by full object id
// and never by a ref name; the oids reach git on stdin (log --stdin) or as one validated
// hex argument, so nothing a repository or a caller wrote becomes an option. Every
// command here is plumbing or `log` with an explicit format, and each is given the flags
// that stop the repository's own configuration from running something when the plumbing
// does not already ignore it: a signature verified by a gpg.program, an external diff or
// textconv driver, a pager, a mailmap, notes, colour. Which of them each command accepts
// differs (for-each-ref and worktree take none), and which of them can actually fire in
// each command is recorded, attack by attack, in history_harden_test.go: a flag that
// cannot make git run anything is kept as a belt, and the test says so.
//
// The invocation around these arguments is run.go's, unchanged (ADR-0066 §2, §3).

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// maxTips caps the tip set, and the `from` a later page passes back (Q1 §2).
const maxTips = 5000

// The fence (Q1 §3): what `log` and `diff-tree` are told besides their own arguments.
// diff-tree has no signature, notes or mailmap to show, and for-each-ref and worktree
// take none of these.
var (
	logFence  = []string{"--no-show-signature", "--no-notes", "--no-color", "--no-ext-diff", "--no-textconv", "--no-use-mailmap", "--encoding=UTF-8"}
	diffFence = []string{"--no-ext-diff", "--no-textconv", "--no-color"}
)

// literalPathspecs is git's global switch that reads every pathspec as a path, so a
// workspace folder named like a pathspec magic (":(top)") is only a name. It is a
// global option: it comes before the subcommand, where `log` would not recognise it.
const literalPathspecs = "--literal-pathspecs"

// noReplaceObjects is git's global switch that ignores refs/replace/: by git-replace(1) a
// replace ref makes log, diff-tree and cat-file show another object's author, message,
// parents and files under an oid, so a repository could forge any commit it holds. Also
// global, also before the subcommand.
const noReplaceObjects = "--no-replace-objects"

// hist is the command line of a history read: the global switches, then the subcommand
// and its arguments.
func hist(args ...string) []string {
	return append([]string{literalPathspecs, noReplaceObjects}, args...)
}

// logFormat is the one record `git.log` reads: oid, parents, raw author name and
// e-mail (%an, %ae: never the mailmapped %aN, %aE), committer time, subject; fields
// and records both end in NUL (-z). A field cannot hold a NUL, so the count of fields
// per record is fixed at six.
const logFormat = "%H%x00%P%x00%an%x00%ae%x00%ct%x00%s"

// logFields is the number of NUL-terminated fields in a logFormat record.
const logFields = 6

// logArgs is one page of the history: the commits reachable from the oids on stdin (one
// per line), newest first with children before their parents, skipping skip and
// keeping at most limit. The tips go on stdin, never on argv (ARG_MAX), and are the
// caller's: the page is deterministic while refs move.
func logArgs(skip, limit int) []string {
	args := hist("log", "--stdin", "--date-order",
		"--skip="+strconv.Itoa(skip), "--max-count="+strconv.Itoa(limit))
	args = append(args, logFence...)
	return append(args, "-z", "--format="+logFormat)
}

// refsFormat is one ref: object type and name, the same of what it peels to (every tag
// level, so a tag of a tag is a commit's), the creation date in Unix seconds, the ref's
// full name and its upstream's. The fields end in NUL and the line in a newline; a ref
// name holds neither.
const refsFormat = "%(objecttype)%00%(objectname)%00%(*objecttype)%00%(*objectname)%00%(creatordate:unix)%00%(refname)%00%(upstream)"

// refsFields is the number of NUL-separated fields in a refsFormat line.
const refsFields = 7

// refsArgs lists the branches, remote-tracking branches and tags. for-each-ref runs no
// diff, signature, notes or mailmap, so it takes none of the fence.
func refsArgs() []string {
	return hist("for-each-ref", "--format="+refsFormat, "refs/heads", "refs/remotes", "refs/tags")
}

// headArgs resolves HEAD to a commit, or exits 1 when there is none (an unborn branch).
var headArgs = hist("rev-parse", "--verify", "--quiet", "HEAD^{commit}")

// worktreeArgs lists the repository's work trees. worktree list takes none of the fence.
func worktreeArgs() []string {
	return hist("worktree", "list", "--porcelain", "-z")
}

// showFormat is the details of one commit: oid, parents, raw author name, e-mail and
// time, committer name, e-mail and time, then the whole message (%B), last because it
// can hold anything but a NUL. Fields end in NUL.
const showFormat = "%H%x00%P%x00%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct%x00%B"

// showFields is the number of NUL-terminated fields in a showFormat record.
const showFields = 9

// showArgs returns the two commands that describe the commit oid (a full hex object id,
// validated by the caller): its details, and its files against parent, the commit's first
// parent as its details report it, or against the empty tree when parent is empty (a root
// commit). A merge's files are what it brought into its first parent's branch (Q1 §5):
// the two-tree form is explicit, because diff-tree's -m --first-parent still lists the
// other parents' differences. Only the workspace's files are listed ("." from the root
// git runs in). Raw lines come first, then git's own counts.
func showArgs(oid, parent string) (details, files []string) {
	details = hist("log", "-1", "-z")
	details = append(details, logFence...)
	details = append(details, "--format="+showFormat, oid, "--")
	files = hist("diff-tree", "-r", "-z", "-M", "--raw", "--numstat")
	files = append(files, diffFence...)
	if parent == "" {
		files = append(files, "--root", "--no-commit-id", oid)
	} else {
		files = append(files, parent, oid)
	}
	files = append(files, "--", ".")
	return details, files
}

// tips resolves the tip set of root: HEAD and every branch, remote-tracking branch and
// tag peeled to its commit (a tag on a tree or a blob has none and is dropped),
// deduplicated, in this order: HEAD, local branches, remote-tracking branches, tags by
// newest creation date, each group by ref name. At most maxTips are returned, and
// truncated says there were more (or that git's output passed the runner's cap and the
// rest was not read). The one place the tip set is decided; git.log and git.refs share it
// (Q1 §2).
func tips(ctx context.Context, r *runner, root string, trusted bool) (oids []string, truncated bool, err error) {
	var ordered []string
	res, err := r.run(ctx, root, trusted, nil, headArgs...)
	if err != nil {
		return nil, false, err
	}
	switch res.ExitCode {
	case 0:
		if head := strings.TrimSpace(string(res.Stdout)); head != "" {
			ordered = append(ordered, head)
		}
	case 1: // no commit yet
	default:
		return nil, false, gitError(res)
	}

	res, err = r.run(ctx, root, trusted, nil, refsArgs()...)
	if err != nil {
		return nil, false, err
	}
	if res.ExitCode != 0 {
		return nil, false, gitError(res)
	}
	out := res.Stdout
	if res.Truncated {
		// The last line is cut at the cap: drop it, and never claim the list is whole.
		truncated = true
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
	}
	type tag struct {
		oid  string
		when int64
		name string
	}
	var heads, remotes []string
	var tags []tag
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != refsFields {
			return nil, false, fmt.Errorf("%w: a ref line of %d fields, want %d", ErrGitFailed, len(f), refsFields)
		}
		typ, oid, peeledTyp, peeled, when, name := f[0], f[1], f[2], f[3], f[4], f[5]
		if typ == "tag" { // the final target, whatever depth of tags
			typ, oid = peeledTyp, peeled
		}
		if typ != "commit" {
			continue
		}
		switch {
		case strings.HasPrefix(name, "refs/heads/"):
			heads = append(heads, oid)
		case strings.HasPrefix(name, "refs/remotes/"):
			remotes = append(remotes, oid)
		case strings.HasPrefix(name, "refs/tags/"):
			n, _ := strconv.ParseInt(when, 10, 64)
			tags = append(tags, tag{oid, n, name})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].when != tags[j].when {
			return tags[i].when > tags[j].when
		}
		return tags[i].name < tags[j].name
	})
	ordered = append(ordered, heads...)
	ordered = append(ordered, remotes...)
	for _, t := range tags {
		ordered = append(ordered, t.oid)
	}

	seen := make(map[string]bool, len(ordered))
	for _, oid := range ordered {
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if len(oids) == maxTips {
			return oids, true, nil
		}
		oids = append(oids, oid)
	}
	return oids, truncated, nil
}

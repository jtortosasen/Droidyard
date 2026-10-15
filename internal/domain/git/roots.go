package git

// Submodules as roots (plan m2n P6; ADR-0071 §7, ADR-0069 §2, §3).
//
// git.log, git.refs, git.show and git.read take an optional `root`: absent or "." is the
// workspace's own repository; anything else names an initialised submodule below the
// workspace, and the read is that submodule's, run in its directory.
//
// What a root may be. A path is accepted only after it has been resolved level by level,
// each level a gitlink (index mode 160000) of the repository above it, found by one
// bounded `--literal-pathspecs ls-files --stage -z -- <that level's path>` in the level
// above (never the whole index, and never from the top for a nested one: the level above
// is a repository of its own), and then:
//
//   - its directory is exactly the lexical join with the workspace root (file.ResolveDir:
//     a symlink on the way, in or out of the workspace, refuses it);
//   - it has a `.git` of its own that is no symlink (an uninitialised submodule is an empty
//     directory, and a plain directory has none);
//   - it is the top of its own work tree (P4's prefix check): not a directory inside the
//     superproject.
//
// A path that fails any of these is REQUEST_INVALID_PARAMS, and no git has run in the
// directory it names: the lexical checks run no git, a gitlink is asked of the level above,
// and the directory's own repository is asked only after every filesystem check passed.
// Only a prefix that is an initialised repository (its own `.git`) is asked about at all,
// so a plain directory is refused without listing what it holds.
//
// Where it stops. Containment is of the directory git runs in and of the work tree, not of
// the git directory (a submodule's `.git` file points into the superproject's, which a
// subdirectory workspace's submodules need too; submodules.go says so). A directory
// swapped for a symlink between this check and the read is the same race as P4's list.
//
// The fence. Every git of a root runs through the runner in that root's own directory with
// the workspace's trust, so on an untrusted workspace the filter drivers are read in the
// submodule's own repository and switched off there (ADR-0066 §3); reading them in the
// workspace root would leave a submodule's own `filter.<x>.clean` to run.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/file"
)

// maxRootLen is the longest `root` accepted: PATH_MAX, so no argv grows with the caller.
const maxRootLen = 4096

// gitRoot is the repository a read is made in.
type gitRoot struct {
	// ws is the workspace's canonical root: the trust, the messages and every path in and
	// out are the workspace's.
	ws string
	// dir is the directory git runs in: ws, or a submodule's directory below it.
	dir string
	// rel is dir relative to ws, slash-separated; "" for the workspace itself.
	rel string
}

// workspaceRoot is the workspace's own repository.
func workspaceRoot(ws string) gitRoot { return gitRoot{ws: ws, dir: ws} }

// mapError is mapError for a git run in this root: a failure in a submodule names it.
func (g gitRoot) mapError(err error) error {
	if g.rel == "" {
		return mapError(g.ws, err)
	}
	return mapSubmoduleError(g.ws, &submoduleError{path: g.rel, err: err})
}

// inside reports whether the workspace-relative path p is the root or lies below it, and
// returns it relative to the root's directory ("" for the root itself). For the workspace
// every path of the workspace is inside; "." is the workspace root.
func (g gitRoot) inside(p string) (string, bool) {
	if g.rel == "" {
		if p == "." {
			return "", true
		}
		return p, true
	}
	if p == g.rel {
		return "", true
	}
	rest, ok := strings.CutPrefix(p, g.rel+"/")
	return rest, ok
}

// invalidRoot is the one refusal of a `root` that names no initialised submodule.
func invalidRoot(root, why string) *capability.Error {
	return capability.Errorf(capability.ErrInvalidParams, "root %q is not an initialised submodule of the project", root).
		WithCause(why).
		WithNextStep("pass the path of a submodule that git.submodules lists as clean, outdated or dirty, or omit root for the project's own repository")
}

// parseRoot is the lexical part of a root, before any git runs: it returns the clean
// workspace-relative path ("" for the workspace). A path that is absolute, holds `..`, a
// `.git` component, a NUL, an empty component or any other spelling than its cleaned
// form, or that is longer than maxRootLen, is REQUEST_INVALID_PARAMS.
func parseRoot(root string) (string, *capability.Error) {
	switch {
	case root == "" || root == ".":
		return "", nil
	case len(root) > maxRootLen:
		return "", invalidRoot(truncate(root, 100), "the path is longer than 4096 bytes")
	case strings.ContainsRune(root, 0):
		return "", invalidRoot(truncate(strings.ReplaceAll(root, "\x00", "\\0"), 100), "the path holds a NUL byte")
	case strings.HasPrefix(root, "/"):
		return "", invalidRoot(root, "the path is absolute; pass it relative to the project root")
	case !plainRelative(root):
		return "", invalidRoot(root, "the path is not a clean relative path inside the project (no .., ., .git or empty component)")
	}
	return root, nil
}

// resolveRoot is the directory git runs in for rel (parseRoot's answer), and the proof that
// it is an initialised submodule of the workspace's gitlink tree; see the file comment.
// A refusal is REQUEST_INVALID_PARAMS; any other error is already the contract's (mapError),
// or a cancel. ws is the workspace's canonical root and trusted is policy.Trusted's answer for it.
func (o Options) resolveRoot(ctx context.Context, r *runner, ws string, trusted bool, rel string) (gitRoot, error) {
	at := workspaceRoot(ws)
	if rel == "" {
		return at, nil
	}
	not := func(why string) (gitRoot, error) { return gitRoot{}, invalidRoot(rel, why) }
	rest := rel
	for depth := 1; rest != ""; depth++ {
		if depth > maxSubmoduleDepth {
			return not("the path is deeper than the eight levels of submodules git.submodules lists")
		}
		comps := strings.Split(rest, "/")
		found := false
		// The submodule of this level is the one prefix of the rest that the repository above
		// records as a gitlink: no gitlink has an entry below it, so there is at most one.
		// Shortest first, and only a prefix that has a `.git` of its own is asked about.
		for n := 1; n <= len(comps) && !found; n++ {
			p := strings.Join(comps[:n], "/")
			if gi, err := os.Lstat(filepath.Join(at.dir, filepath.FromSlash(p), ".git")); err != nil || gi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			link, err := recordsGitlink(ctx, r, at, trusted, p)
			if err != nil {
				return gitRoot{}, at.mapError(err)
			}
			if !link {
				continue
			}
			sub := p
			if at.rel != "" {
				sub = at.rel + "/" + p
			}
			abs := filepath.Join(ws, filepath.FromSlash(sub))
			if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
				return not(sub + " is not a directory of the project")
			}
			if got, err := file.ResolveDir(o.Env, ws, sub); err != nil || got != abs {
				return not(sub + " is, or sits behind, a symlink")
			}
			next := gitRoot{ws: ws, dir: abs, rel: sub}
			prefix, err := r.prefix(ctx, abs, trusted)
			switch {
			case errors.Is(err, ErrNotARepository):
				return not(sub + " has no repository of its own: it is not initialised")
			case err != nil:
				return gitRoot{}, next.mapError(err)
			case prefix != "":
				return not(sub + " is a directory inside the superproject, not a repository of its own")
			}
			at, rest, found = next, strings.Join(comps[n:], "/"), true
		}
		if !found {
			return not(rest + " is not a gitlink of " + describe(at) + ", or not initialised")
		}
	}
	return at, nil
}

// describe names a root in a refusal's cause.
func describe(g gitRoot) string {
	if g.rel == "" {
		return "the project's repository"
	}
	return "the submodule " + g.rel
}

// recordsGitlink asks the repository in at.dir whether its index records a gitlink at
// exactly p (relative to at.dir): one bounded ls-files over one literal pathspec. A
// record for p of another mode, or for a path below p, is no gitlink.
func recordsGitlink(ctx context.Context, r *runner, at gitRoot, trusted bool, p string) (bool, error) {
	res, err := r.run(ctx, at.dir, trusted, nil, hist("ls-files", "--stage", "-z", "--", p)...)
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, gitError(res)
	}
	link := false
	gitlinkRecords(res, func(got, _ string, _ int) {
		if got == p {
			link = true
		}
	})
	return link, nil
}

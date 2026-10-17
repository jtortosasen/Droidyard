package platform

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxWorktreeFile bounds every file LinkedWorktree reads: a path and a prefix are far
// below it, and a larger file is not one git wrote.
const maxWorktreeFile = 4 << 10

// LinkedWorktree reports whether root is a linked worktree of a non-bare git
// repository, and that repository's main tree (ADR-0081 §2). It is how a worktree
// inherits the trust of the project it belongs to (policy.Trusted), so it answers
// "genuine" only when the repository's own administrative directory names root back.
//
// The threat is a directory planted with a `.git` file that points at a trusted
// repository's data: such a file is written by whoever controls the directory, so it
// proves nothing by itself. What an outsider cannot forge is the back-link,
// `<common>/worktrees/<name>/gitdir`, which lives inside the trusted repository's
// `.git` (ADR-0081 §3; a process that can write there already runs code in the trusted
// root, ADR-0015 §6). Five tests, all on canonical paths, must hold:
//
//   - R/.git is a regular file (Lstat: not a link), of at most 4 KiB, holding
//     `gitdir: <P>`;
//   - P/commondir names the common directory C, and P is C/worktrees/<name>;
//   - P/gitdir names R/.git, the back-link;
//   - C is named `.git`, so its parent M is a main tree rather than a bare repository
//     or a submodule's modules/<x>; and M/.git is C itself, a directory.
//
// A relative path is resolved against the directory that holds the file naming it, as
// git resolves `.git`, `commondir` and `gitdir` (git ≥ 2.48 can write them relative).
//
// Git itself is not run: `git rev-parse --git-common-dir` would read the candidate's
// configuration before its trust is known, and, as far as is known, follows a `.git`
// file without checking the back-link. Every file is opened with O_NONBLOCK and must
// be a regular file on the open descriptor, so a FIFO planted at one of the paths
// fails at once instead of hanging the check, which runs before every call.
//
// Accepted residual risk (ADR-0081 §3): a worktree directory deleted by hand, without
// `git worktree remove`, leaves its administrative entry naming that path until
// `git worktree prune`; a directory made later at exactly that path with a `.git` file
// naming the entry inherits trust. The path is one the user chose.
func LinkedWorktree(env Env, root string) (main string, ok bool) {
	r := env.Canonical(root)
	dotGit := filepath.Join(r, ".git")
	if fi, err := os.Lstat(dotGit); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	p, ok := readNamedPath(env, dotGit, "gitdir: ", r)
	if !ok {
		return "", false
	}
	common, ok := readNamedPath(env, filepath.Join(p, "commondir"), "", p)
	if !ok {
		return "", false
	}
	// P is C/worktrees/<name>: the entry must sit directly in the common directory's
	// worktrees directory, so an admin directory of another repository whose
	// commondir names this one does not qualify.
	if filepath.Dir(filepath.Dir(p)) != common || filepath.Base(filepath.Dir(p)) != "worktrees" {
		return "", false
	}
	if name := filepath.Base(p); name == "." || name == ".." || name == string(filepath.Separator) {
		return "", false
	}
	// dotGit itself, not its canonical form: Lstat proved it is no link, and resolving
	// it again would follow one swapped in since (a same-uid race, but a free one).
	back, ok := readNamedPath(env, filepath.Join(p, "gitdir"), "", p)
	if !ok || back != dotGit {
		return "", false
	}
	if filepath.Base(common) != ".git" {
		return "", false
	}
	main = filepath.Dir(common)
	if fi, err := os.Lstat(filepath.Join(main, ".git")); err != nil || !fi.IsDir() {
		return "", false
	}
	return main, true
}

// readNamedPath reads the one-path file at file, strips prefix (which must be there),
// resolves a relative path against base, the directory holding the file that names it,
// and returns it canonical.
func readNamedPath(env Env, file, prefix, base string) (string, bool) {
	data, ok := readSmallRegular(file)
	if !ok {
		return "", false
	}
	text := strings.TrimRight(string(data), "\r\n\t ")
	if !strings.HasPrefix(text, prefix) {
		return "", false
	}
	path := strings.TrimPrefix(text, prefix)
	if path == "" || strings.ContainsAny(path, "\r\n\x00") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return env.Canonical(path), true
}

// readSmallRegular reads path without blocking: it is opened O_NONBLOCK and
// O_NOFOLLOW (git writes none of these files as a link, so a link in the last
// component is refused before a device it names is opened), must be a regular file on
// the open descriptor, and must hold at most maxWorktreeFile bytes.
func readSmallRegular(path string) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWorktreeFile+1))
	if err != nil || len(data) > maxWorktreeFile || bytes.IndexByte(data, 0) >= 0 {
		return nil, false
	}
	return data, true
}

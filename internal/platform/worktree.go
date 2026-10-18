package platform

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
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

// MaxWorktrees bounds the list Worktrees returns (ADR-0089).
const MaxWorktrees = 50

// maxWorktreeEntries is how many administrative entries Worktrees reads: twice the list
// it returns, room for stale entries. A repository with more has none listed.
const maxWorktreeEntries = 2 * MaxWorktrees

// Worktree is one linked worktree of a repository: its canonical root and, when its
// HEAD is a branch, the branch's short name (empty when it is detached).
type Worktree struct {
	Root   string
	Branch string
}

// Worktrees lists the linked worktrees of the non-bare repository whose main tree is
// main, sorted by root, at most MaxWorktrees (ADR-0089 §1, §2). It reads the repository's
// own administrative entries, `<main>/.git/worktrees/<name>/`, as LinkedWorktree reads
// one, and runs no git: each entry's `gitdir` names its worktree's `.git` file, and the
// entry is kept only when that worktree passes LinkedWorktree with exactly this main
// tree and names this very entry back, so a removed directory, a moved or broken
// back-link, or a second entry claiming the same root is left out. Branch is the `HEAD`
// of that entry when it reads `ref: refs/heads/<name>` and is a valid, plain name
// (validBranch), else empty. A repository whose `.git` is not a directory, that has no
// `worktrees` directory, or that has more than 100 entries in it, has none; so does a
// worktree whose root holds a control or format character.
func Worktrees(env Env, main string) []Worktree {
	main = env.Canonical(main)
	common := filepath.Join(main, ".git")
	if fi, err := os.Lstat(common); err != nil || !fi.IsDir() {
		return nil
	}
	admins := filepath.Join(common, "worktrees")
	if fi, err := os.Lstat(admins); err != nil || !fi.IsDir() {
		return nil
	}
	// At most maxWorktreeEntries names are read, however many the directory holds: the
	// entries of an untrusted repository are planted, every one costs several path
	// canonicalisations, and each of those lists the directory again (onDiskName), so
	// the scan is quadratic in the entry count. More than the limit lists none.
	dir, err := os.Open(admins)
	if err != nil {
		return nil
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxWorktreeEntries + 1)
	if err != nil && err != io.EOF || len(entries) > maxWorktreeEntries {
		return nil
	}
	var out []Worktree
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		admin := filepath.Join(admins, e.Name())
		dotGit, ok := readNamedPath(env, filepath.Join(admin, "gitdir"), "", admin)
		if !ok || filepath.Base(dotGit) != ".git" {
			continue
		}
		root := filepath.Dir(dotGit)
		if !plainText(root) {
			continue
		}
		if got, ok := LinkedWorktree(env, root); !ok || got != main || seen[root] {
			continue
		}
		// The worktree's own `.git` file must name this entry: with two entries for one
		// root, the branch is the one of the entry the worktree uses.
		if named, ok := readNamedPath(env, filepath.Join(root, ".git"), "gitdir: ", root); !ok || named != env.Canonical(admin) {
			continue
		}
		seen[root] = true
		out = append(out, Worktree{Root: root, Branch: worktreeBranch(admin)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	if len(out) > MaxWorktrees {
		out = out[:MaxWorktrees]
	}
	return out
}

// worktreeBranch is the short branch name admin/HEAD holds when it is a symbolic ref
// under refs/heads, else "": a detached HEAD, a ref elsewhere, or a file that is not one
// git wrote.
func worktreeBranch(admin string) string {
	data, ok := readSmallRegular(filepath.Join(admin, "HEAD"))
	if !ok {
		return ""
	}
	name, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n\t "), "ref: refs/heads/")
	if !ok || !validBranch(name) {
		return ""
	}
	return name
}

// validBranch reports whether name is a branch name git could have written (the rules
// of git check-ref-format) and one that is safe to show: the daemon's reader is an agent
// that acts on it, and the file is written by whoever controls the repository. So it also
// refuses what git would not write anyway, control and format characters (bidi
// overrides) included, and anything over 255 bytes.
func validBranch(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || !plainText(name) {
		return false
	}
	if strings.ContainsAny(name, " ~^:?*[\\") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	return true
}

// plainText reports whether s holds no control character (a byte below 0x20, 0x7f) and
// no Unicode format character (the bidi overrides, zero-width marks): text an agent
// reads as it appears.
func plainText(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

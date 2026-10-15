// Package gittest builds real Git repositories for the history tests and benchmarks of
// plan m2n: a small one with every shape a history UI has to draw, and a large
// synthetic one. Both are written with `git fast-import`, so a hundred thousand
// commits take seconds, not the minutes a loop of `git commit` would.
//
// The repositories are made in a hermetic environment: no GIT_* variable of the
// caller, no system or global configuration, and a fresh empty HOME, so the host's
// signing, fsmonitor or LFS settings cannot change what a test sees. Nothing here
// touches the network or any repository but the ones it creates.
package gittest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Repo is a repository History built, and the names a test looks up in it.
type Repo struct {
	// Dir is the work tree; Origin the bare remote it was pushed to and fetched from;
	// Linked a second work tree of the same repository, on the branch chore/wt.
	Dir, Origin, Linked string
	// Oids names commits: "root", "second" (what tag v0.9 points at), "merge" (a two-parent merge), "octopus" (a merge of
	// four parents), "rename" (the commit that renames a file), "head" (the current
	// branch's tip), "origin-main" (origin/main, which main is not an ancestor of), and
	// "tree-tag" (the tree a tag points at, not a commit).
	Oids map[string]string
}

// Env returns the environment every git of this package runs in.
func Env(t testing.TB, home string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env,
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "xdg"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_AUTHOR_DATE=1700000000 +0000",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com", "GIT_COMMITTER_DATE=1700000000 +0000",
	)
}

// git is one hermetic git, started in a directory.
type git struct {
	t    testing.TB
	env  []string
	path string
}

func newGit(t testing.TB) *git {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	home, err := os.MkdirTemp("", "gittest-home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return &git{t: t, env: Env(t, home), path: path}
}

// run runs git in dir with stdin (may be nil) and returns its stdout; a failure fails
// the test.
func (g *git) run(dir string, stdin []byte, args ...string) string {
	g.t.Helper()
	cmd := exec.Command(g.path, args...)
	cmd.Dir = dir
	cmd.Env = g.env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

// fastImport feeds a stream to `git fast-import` in dir. exportMarks, when not empty,
// is a file git writes its mark-to-oid table to.
func (g *git) fastImport(dir string, exportMarks string, write func(w *bufio.Writer)) {
	g.t.Helper()
	args := []string{"fast-import", "--quiet"}
	if exportMarks != "" {
		args = append(args, "--export-marks="+exportMarks)
	}
	cmd := exec.Command(g.path, args...)
	cmd.Dir = dir
	cmd.Env = g.env
	var errb bytes.Buffer
	cmd.Stderr = &errb
	in, err := cmd.StdinPipe()
	if err != nil {
		g.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		g.t.Fatal(err)
	}
	w := bufio.NewWriterSize(in, 1<<20)
	write(w)
	if err := w.Flush(); err != nil {
		g.t.Fatalf("fast-import stream: %v\n%s", err, errb.String())
	}
	in.Close()
	if err := cmd.Wait(); err != nil {
		g.t.Fatalf("git fast-import: %v\n%s", err, errb.String())
	}
}

// file is one change in a commit: content nil deletes the path.
type file struct {
	path    string
	content []byte
	// from, when set, renames the file from this path (content is then its new content).
	from string
	// gitlink, when set, records the path as a submodule at this commit (mode 160000); the
	// commit need not exist in the repository.
	gitlink string
}

// stream writes fast-import commands.
type stream struct {
	w    *bufio.Writer
	mark int
	// clock is the next commit's time, one minute after the last.
	clock int64
}

func newStream(w *bufio.Writer) *stream { return &stream{w: w, clock: 1700000000} }

// commit writes one commit on ref and returns its mark. from is the first parent's mark
// (0 for a root), merges the other parents'.
func (s *stream) commit(ref string, from int, merges []int, who, subject string, files ...file) int {
	s.mark++
	name, email, _ := strings.Cut(who, "|")
	ident := fmt.Sprintf("%s <%s> %d +0000", name, email, s.clock)
	s.clock += 60
	fmt.Fprintf(s.w, "commit %s\nmark :%d\nauthor %s\ncommitter %s\ndata %d\n%s\n", ref, s.mark, ident, ident, len(subject), subject)
	if from > 0 {
		fmt.Fprintf(s.w, "from :%d\n", from)
	}
	for _, m := range merges {
		fmt.Fprintf(s.w, "merge :%d\n", m)
	}
	for _, f := range files {
		switch {
		case f.gitlink != "":
			fmt.Fprintf(s.w, "M 160000 %s %s\n", f.gitlink, quote(f.path))
		case f.from != "":
			fmt.Fprintf(s.w, "R %s %s\n", quote(f.from), quote(f.path))
		case f.content == nil:
			fmt.Fprintf(s.w, "D %s\n", quote(f.path))
		default:
			fmt.Fprintf(s.w, "M 100644 inline %s\ndata %d\n", quote(f.path), len(f.content))
			s.w.Write(f.content)
			s.w.WriteByte('\n')
		}
	}
	s.w.WriteByte('\n')
	return s.mark
}

// reset points ref at a mark: a branch or a lightweight tag.
func (s *stream) reset(ref string, mark int) {
	fmt.Fprintf(s.w, "reset %s\nfrom :%d\n\n", ref, mark)
}

// tag writes an annotated tag object and its ref.
func (s *stream) tag(name string, mark int, message string) {
	fmt.Fprintf(s.w, "tag %s\nfrom :%d\ntagger Tagger <tagger@example.com> %d +0000\ndata %d\n%s\n", name, mark, s.clock, len(message), message)
	s.clock += 60
}

// quote writes a path as fast-import wants it when it holds a space or a special
// character; the paths of these fixtures need only the C-style quoting of git.
func quote(p string) string {
	if !strings.ContainsAny(p, " \"\\\n\t") && isASCII(p) {
		return p
	}
	return strconv.Quote(p)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// readMarks returns the mark-to-oid table git wrote.
func readMarks(t testing.TB, path string) map[int]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, line := range strings.Split(string(b), "\n") {
		m, oid, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(m, ":"))
		if err != nil {
			t.Fatalf("marks file line %q: %v", line, err)
		}
		out[n] = oid
	}
	return out
}

// History builds the repository K1's $B/hist describes in dir, which it creates: branches
// in feature/, bugfix/ and chore/ folders (one three folders deep), a two-parent merge
// and an octopus of four parents, an annotated tag, a lightweight tag, a tag of a tag
// and a tag on a tree, a binary file and a rename, a bare origin with remote-tracking
// refs (one with no local branch) on which main is both ahead and behind, and a linked
// work tree. The author names include non-ASCII text.
func History(t testing.TB, dir string) *Repo {
	t.Helper()
	g := newGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Dir: dir, Origin: dir + "-origin.git", Linked: dir + "-linked", Oids: map[string]string{}}
	g.run(dir, nil, "init", "-q", "-b", "main")

	const (
		ann = "Ann Author|ann@example.com"
		zoe = "Zoë Ünïcode|zoe@example.com"
		bob = "Bob Builder|bob@example.com"
	)
	marks := filepath.Join(t.TempDir(), "marks")
	var root, second, rename, merge, octopus int
	g.fastImport(dir, marks, func(w *bufio.Writer) {
		s := newStream(w)
		root = s.commit("refs/heads/main", 0, nil, ann, "Initial import",
			file{path: "README.md", content: []byte("# fixture\n")},
			file{path: "src/app.txt", content: []byte("one\n")},
			file{path: "data/blob.bin", content: []byte{0, 1, 2, 0, 255, 254, 0}},
			file{path: "docs/old-name.txt", content: []byte("documentation that will move\n")})
		second = s.commit("refs/heads/main", root, nil, ann, "Second commit", file{path: "src/app.txt", content: []byte("one\ntwo\n")})
		rename = s.commit("refs/heads/main", second, nil, bob, "Rename the documentation", file{from: "docs/old-name.txt", path: "docs/new-name.txt"})
		c4 := s.commit("refs/heads/main", rename, nil, ann, "Fourth commit", file{path: "src/app.txt", content: []byte("one\ntwo\nthree\n")})

		f1 := s.commit("refs/heads/feature/login", second, nil, zoe, "Add a login form", file{path: "src/login.txt", content: []byte("form\n")})
		f2 := s.commit("refs/heads/feature/login", f1, nil, zoe, "Validate the login form", file{path: "src/login.txt", content: []byte("form\nvalidated\n")})
		ui := s.commit("refs/heads/feature/search/ui/results", rename, nil, bob, "Search results page", file{path: "src/search.txt", content: []byte("results\n")})
		b1 := s.commit("refs/heads/bugfix/crash", rename, nil, bob, "Reproduce the crash", file{path: "src/crash.txt", content: []byte("repro\n")})
		b2 := s.commit("refs/heads/bugfix/crash", b1, nil, bob, "Fix the crash", file{path: "src/crash.txt", content: []byte("fixed\n")})
		d1 := s.commit("refs/heads/chore/deps", c4, nil, ann, "Bump the dependencies", file{path: "deps.txt", content: []byte("v2\n")})

		// fast-import keeps the first parent's tree for a merge, so a merge carries the
		// files its branches added explicitly, as a real merge would.
		merge = s.commit("refs/heads/main", c4, []int{f2}, ann, "Merge feature/login",
			file{path: "merged.txt", content: []byte("login\n")},
			file{path: "src/login.txt", content: []byte("form\nvalidated\n")})
		octopus = s.commit("refs/heads/main", merge, []int{b2, d1, ui}, ann, "Merge three branches",
			file{path: "octopus.txt", content: []byte("three\n")},
			file{path: "src/crash.txt", content: []byte("fixed\n")},
			file{path: "deps.txt", content: []byte("v2\n")},
			file{path: "src/search.txt", content: []byte("results\n")})
		s.commit("refs/heads/main", octopus, nil, ann, "Eighth commit", file{path: "src/app.txt", content: []byte("one\ntwo\nthree\nfour\n")})

		s.reset("refs/tags/v0.9", second)
		s.tag("v1.0", merge, "Release 1.0")
		s.reset("refs/heads/chore/wt", c4)
	})
	m := readMarks(t, marks)
	r.Oids["root"], r.Oids["second"], r.Oids["rename"], r.Oids["merge"], r.Oids["octopus"] = m[root], m[second], m[rename], m[merge], m[octopus]
	g.run(dir, nil, "reset", "-q", "--hard", "main")

	// A tag of a tag, and a tag on a tree: neither peels to a commit the usual way.
	inner := strings.TrimSpace(g.run(dir, nil, "rev-parse", "refs/tags/v1.0"))
	nested := strings.TrimSpace(g.run(dir, []byte("object "+inner+"\ntype tag\ntag v1.0-final\ntagger Tagger <tagger@example.com> 1700000000 +0000\n\ntag of a tag\n"),
		"hash-object", "-t", "tag", "-w", "--stdin"))
	g.run(dir, nil, "update-ref", "refs/tags/v1.0-final", nested)
	r.Oids["tree-tag"] = strings.TrimSpace(g.run(dir, nil, "rev-parse", "main^{tree}"))
	g.run(dir, nil, "tag", "-a", "-m", "a tag on a tree", "tree-tag", r.Oids["tree-tag"])

	// The remote: everything pushed, then one commit on origin's main and one branch
	// only origin has, so that main is behind; then one commit of ours, so it is ahead.
	g.run(filepath.Dir(dir), nil, "init", "-q", "--bare", "-b", "main", r.Origin)
	g.run(dir, nil, "remote", "add", "origin", r.Origin)
	g.run(dir, nil, "push", "-q", "origin", "--all")
	g.run(dir, nil, "push", "-q", "origin", "--tags")
	g.fastImport(r.Origin, "", func(w *bufio.Writer) {
		// "from <branch>^0" continues a branch git already has.
		w.WriteString("commit refs/heads/release/1.0\ncommitter Remote <remote@example.com> 1700100060 +0000\ndata 12\nRelease line\nfrom refs/heads/main~1\n\n")
		w.WriteString("commit refs/heads/main\ncommitter Remote <remote@example.com> 1700100000 +0000\ndata 14\nPushed by them\nfrom refs/heads/main^0\n\n")
	})
	g.run(dir, nil, "fetch", "-q", "origin")
	g.run(dir, nil, "branch", "-q", "--set-upstream-to=origin/main", "main")
	g.run(dir, nil, "remote", "set-head", "origin", "main")
	g.run(dir, nil, "symbolic-ref", "refs/remotes/origin/HEAD") // fails the test if it was not made
	r.Oids["origin-main"] = strings.TrimSpace(g.run(dir, nil, "rev-parse", "origin/main"))
	g.fastImport(dir, "", func(w *bufio.Writer) {
		fmt.Fprint(w, "commit refs/heads/main\ncommitter Ann Author <ann@example.com> 1700200000 +0000\ndata 11\nOurs, ahead\nfrom refs/heads/main^0\n\n")
	})
	g.run(dir, nil, "reset", "-q", "--hard", "main")
	r.Oids["head"] = strings.TrimSpace(g.run(dir, nil, "rev-parse", "HEAD"))

	g.run(dir, nil, "worktree", "add", "-q", r.Linked, "chore/wt")
	return r
}

// Synthetic builds, in dir, a repository of exactly `commits` commits in which main is a
// chain and `branches` side branches each fork from it, grow a few commits and merge
// back (so the graph has real merges and `branches` live refs refs/heads/branch/NN),
// with `tags` lightweight tags spread along main (every tenth annotated), and
// refs/remotes/origin/main a tenth of the history behind main. No commit-graph is
// written: a benchmark adds it. commits must be at least 3*branches+1.
func Synthetic(t testing.TB, dir string, commits, branches, tags int) {
	t.Helper()
	if branches < 0 || commits < 3*branches+1 {
		t.Fatalf("gittest.Synthetic: %d commits cannot hold %d merged branches", commits, branches)
	}
	g := newGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	g.run(dir, nil, "init", "-q", "-b", "main")
	const who = "Synthetic Author|synthetic@example.com"
	g.fastImport(dir, "", func(w *bufio.Writer) {
		s := newStream(w)
		var mainMarks []int
		body := func(n int) file {
			return file{path: fmt.Sprintf("f%02d.txt", n%64), content: []byte(fmt.Sprintf("commit %d\n", n))}
		}
		tip := s.commit("refs/heads/main", 0, nil, who, "root", body(0))
		mainMarks = append(mainMarks, tip)
		rest := commits - 1
		for b := 0; b < branches; b++ {
			seg := rest / (branches - b)
			rest -= seg
			side := min(10, max(1, seg/4))
			for i := 0; i < seg-side-1; i++ {
				tip = s.commit("refs/heads/main", tip, nil, who, fmt.Sprintf("main %d", s.mark+1), body(s.mark+1))
				mainMarks = append(mainMarks, tip)
			}
			ref := fmt.Sprintf("refs/heads/branch/%02d", b)
			st := tip
			for i := 0; i < side; i++ {
				st = s.commit(ref, st, nil, who, fmt.Sprintf("branch %02d step %d", b, i), body(s.mark+1))
			}
			tip = s.commit("refs/heads/main", tip, []int{st}, who, fmt.Sprintf("Merge branch/%02d", b), body(s.mark+1))
			mainMarks = append(mainMarks, tip)
		}
		for i := 0; i < rest; i++ { // branches == 0
			tip = s.commit("refs/heads/main", tip, nil, who, fmt.Sprintf("main %d", s.mark+1), body(s.mark+1))
			mainMarks = append(mainMarks, tip)
		}
		for i := 0; i < tags; i++ {
			m := mainMarks[i*len(mainMarks)/tags]
			if i%10 == 9 {
				s.tag(fmt.Sprintf("v%d", i), m, fmt.Sprintf("release %d", i))
			} else {
				s.reset(fmt.Sprintf("refs/tags/v%d", i), m)
			}
		}
		s.reset("refs/remotes/origin/main", mainMarks[len(mainMarks)*9/10])
	})
	g.run(dir, nil, "reset", "-q", "--hard", "main")
}

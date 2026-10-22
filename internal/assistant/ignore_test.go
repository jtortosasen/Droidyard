package assistant

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initFiles are the paths a per-project run of init for claude leaves in a tree.
var initFiles = []string{
	".mcp.json",
	".claude/settings.json",
	".claude/skills/" + SkillName + "/SKILL.md",
	".claude/skills/" + SkillName + "/reference.md",
}

// gitEnv isolates real git from the host's configuration.
func gitEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "xdg"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
}

type gitT struct {
	t   *testing.T
	env []string
}

func needGit(t *testing.T) gitT {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	return gitT{t: t, env: gitEnv(t)}
}

func (g gitT) run(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = g.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// repo makes a real repository with one commit and returns its real path.
func (g gitT) repo() string {
	g.t.Helper()
	dir, err := filepath.EvalSymlinks(g.t.TempDir())
	if err != nil {
		g.t.Fatal(err)
	}
	g.run(dir, "init", "-q", "-b", "main")
	writeAt(g.t, dir, "README.md", "hello\n")
	g.run(dir, "add", "-A")
	g.run(dir, "commit", "-q", "-m", "c")
	return dir
}

func writeAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAt(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func created(rels ...string) []Change {
	var cs []Change
	for _, r := range rels {
		cs = append(cs, Change{Path: r, Status: Created})
	}
	return cs
}

// facts reads what internal/domain/git's Tracked reads, with plain git (this package
// never imports it), for a root and the run's changes.
func (g gitT) facts(root string, changes []Change, target IgnoreTarget) IgnoreOptions {
	g.t.Helper()
	lines := strings.Split(strings.TrimSpace(g.run(root, "rev-parse", "--show-toplevel", "--git-common-dir")), "\n")
	o := IgnoreOptions{Root: root, Target: target, Changes: changes, InRepo: true, Top: lines[0], CommonDir: lines[1], Tracked: map[string]bool{}}
	if !filepath.IsAbs(o.CommonDir) {
		o.CommonDir = filepath.Join(root, o.CommonDir)
	}
	args := append([]string{"--literal-pathspecs", "ls-files", "-z", "--full-name", "--"}, IgnoreCandidates(changes)...)
	for _, p := range strings.Split(g.run(root, args...), "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			o.Tracked[p] = true
		}
	}
	return o
}

func (g gitT) ignore(root string, changes []Change, target IgnoreTarget, mod func(*IgnoreOptions)) Change {
	g.t.Helper()
	o := g.facts(root, changes, target)
	if mod != nil {
		mod(&o)
	}
	c, err := Ignore(o)
	if err != nil {
		g.t.Fatal(err)
	}
	return c
}

// listed reports the init files `git status` lists in dir, one per file.
func (g gitT) listed(dir string, rels ...string) []string {
	g.t.Helper()
	out := g.run(dir, "status", "--porcelain", "-uall")
	var got []string
	for _, r := range rels {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasSuffix(l, " "+r) {
				got = append(got, r)
			}
		}
	}
	return got
}

func (g gitT) wantAllListed(dir string, rels ...string) {
	g.t.Helper()
	if got := g.listed(dir, rels...); len(got) != len(rels) {
		g.t.Fatalf("control: git status in %s lists %v before any block; the test cannot fail", dir, got)
	}
}

// Decision (ADR-0124 Q4): --exclude writes the block into the common directory's
// info/exclude, the one file the main tree and every linked worktree read; the
// worktree's own git directory has no info/exclude that git reads. Run from the
// worktree, the block still silences both trees.
func TestIgnoreExcludeReachesEveryWorktree(t *testing.T) {
	g := needGit(t)
	main := g.repo()
	wt := filepath.Join(t.TempDir(), "wt")
	g.run(main, "worktree", "add", "-q", "-b", "side", wt)
	wt, _ = filepath.EvalSymlinks(wt)
	for _, d := range []string{main, wt} {
		for _, f := range initFiles {
			writeAt(t, d, f, "x\n")
		}
		g.wantAllListed(d, initFiles...)
	}

	c := g.ignore(wt, created(initFiles...), ExcludeFile, nil)
	if c.Status != Created && c.Status != Updated {
		t.Fatalf("status %s, notes %v", c.Status, c.Notes)
	}
	for _, d := range []string{main, wt} {
		if got := g.listed(d, initFiles...); len(got) != 0 {
			t.Errorf("git status in %s still lists %v", d, got)
		}
	}
	if got := readAt(t, filepath.Join(main, ".git", "info", "exclude")); !strings.Contains(got, ignoreHeader) {
		t.Errorf("the block is not in the common directory's info/exclude:\n%s", got)
	}
}

// Decision (ADR-0124 Q4): a tracked path gets no line, a line cannot ignore it and would
// say something false; it is reported instead, and the untracked ones are listed.
func TestIgnoreLeavesTrackedPathsOut(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	writeAt(t, dir, ".mcp.json", "{}\n")
	g.run(dir, "add", ".mcp.json")
	g.run(dir, "commit", "-q", "-m", "mcp")
	for _, f := range initFiles[1:] {
		writeAt(t, dir, f, "x\n")
	}
	writeAt(t, dir, ".mcp.json", "{\"changed\":1}\n")

	for _, target := range []IgnoreTarget{ExcludeFile, GitignoreFile} {
		c := g.ignore(dir, created(initFiles...), target, nil)
		file := filepath.Join(dir, ".gitignore")
		if target == ExcludeFile {
			file = filepath.Join(dir, ".git", "info", "exclude")
		}
		got := readAt(t, file)
		if strings.Contains(got, ".mcp.json") {
			t.Errorf("%s: a line for a tracked path:\n%s", target, got)
		}
		if !strings.Contains(got, "/.claude/settings.json") || !strings.Contains(got, "/.claude/skills/"+SkillName+"/\n") {
			t.Errorf("%s: the untracked files are not listed (the skill as its directory):\n%s", target, got)
		}
		if n := strings.Join(c.Notes, "\n"); !strings.Contains(n, "tracked by git") || !strings.Contains(n, ".mcp.json") {
			t.Errorf("%s: the tracked path is not reported: %v", target, c.Notes)
		}
	}
}

// Decision (ADR-0124 Q4): a rerun replaces the block with the current set and touches
// no line outside it; an identical rerun changes nothing.
func TestIgnoreRerunAddsAndKeepsTheUsersLines(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	file := filepath.Join(dir, ".git", "info", "exclude")
	writeAt(t, dir, ".git/info/exclude", "# mine\n*.log\n")
	first := created(".mcp.json")
	g.ignore(dir, first, ExcludeFile, nil)
	// the user adds a line below the block
	writeAt(t, dir, ".git/info/exclude", readAt(t, file)+"scratch/\n")

	c := g.ignore(dir, created(".mcp.json", "opencode.json"), ExcludeFile, nil)
	if c.Status != Updated {
		t.Errorf("status %s, want updated", c.Status)
	}
	got := readAt(t, file)
	want := "# mine\n*.log\n" + ignoreHeader + "\n/.mcp.json\n/opencode.json\n" + ignoreEnd + "\nscratch/\n"
	if got != want {
		t.Errorf("exclude =\n%q\nwant\n%q", got, want)
	}
	if c := g.ignore(dir, created(".mcp.json", "opencode.json"), ExcludeFile, nil); c.Status != Unchanged || readAt(t, file) != want {
		t.Errorf("an identical rerun: status %s, file changed %v", c.Status, readAt(t, file) != want)
	}
	// ADR-0126: a smaller set adds to the block, it does not replace it, because the
	// block is shared by every client and root that wrote into the file.
	g.ignore(dir, created(".mcp.json"), ExcludeFile, nil)
	if got := readAt(t, file); got != want {
		t.Errorf("a smaller set changed the block:\n%s", got)
	}
}

// Decision (ADR-0124 Q4): --uninstall removes the block and nothing else, byte for byte,
// from both files, and deletes a file that held only the block.
func TestIgnoreUninstallRemovesOnlyTheBlock(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	excl := filepath.Join(dir, ".git", "info", "exclude")
	gi := filepath.Join(dir, ".gitignore")
	exclUser := "# git's own\n*.log\n\n"
	giUser := "build/\n*.iml\n"
	writeAt(t, dir, ".git/info/exclude", exclUser)
	writeAt(t, dir, ".gitignore", giUser)
	chs := created(initFiles...)

	g.ignore(dir, chs, ExcludeFile, nil)
	g.ignore(dir, chs, GitignoreFile, nil)
	if !strings.Contains(readAt(t, excl), ignoreHeader) || !strings.Contains(readAt(t, gi), ignoreHeader) {
		t.Fatal("the blocks were not written")
	}
	for _, target := range []IgnoreTarget{ExcludeFile, GitignoreFile} {
		c := g.ignore(dir, removed(initFiles...), target, uninstalling)
		if c.Status != Removed {
			t.Errorf("%s: status %s, want removed", target, c.Status)
		}
	}
	if got := readAt(t, excl); got != exclUser {
		t.Errorf("exclude = %q, want the user's bytes %q", got, exclUser)
	}
	if got := readAt(t, gi); got != giUser {
		t.Errorf(".gitignore = %q, want the user's bytes %q", got, giUser)
	}
	// a second uninstall has nothing to remove
	if c := g.ignore(dir, removed(initFiles...), GitignoreFile, uninstalling); c.Status != Unchanged {
		t.Errorf("second uninstall: %s", c.Status)
	}

	// a .gitignore that held only the block goes with it
	if err := os.Remove(gi); err != nil {
		t.Fatal(err)
	}
	g.ignore(dir, chs, GitignoreFile, nil)
	c := g.ignore(dir, removed(initFiles...), GitignoreFile, uninstalling)
	if _, err := os.Stat(gi); c.Status != Deleted || !os.IsNotExist(err) {
		t.Errorf("a .gitignore of only the block: status %s, stat err %v; want deleted and gone", c.Status, err)
	}
}

// Decision (ADR-0124 Q4): an exclude line is anchored at the top, so it carries the
// root's path below it; a .gitignore line is anchored at the root, so it does not. The
// root of a project can lie below the repository's top (ADR-0075's layout).
func TestIgnoreRootBelowTheTop(t *testing.T) {
	g := needGit(t)
	top := g.repo()
	root := filepath.Join(top, "apps", "android")
	for _, f := range initFiles {
		writeAt(t, root, f, "x\n")
	}
	var rels []string
	for _, f := range initFiles {
		rels = append(rels, "apps/android/"+f)
	}
	g.wantAllListed(top, rels...)

	for _, target := range []IgnoreTarget{ExcludeFile, GitignoreFile} {
		g.ignore(root, created(initFiles...), target, nil)
		if got := g.listed(top, rels...); len(got) != 0 {
			t.Errorf("%s: git status still lists %v", target, got)
		}
	}
	if got := readAt(t, filepath.Join(top, ".git", "info", "exclude")); !strings.Contains(got, "\n/apps/android/.mcp.json\n") {
		t.Errorf("exclude lines are not anchored at the top:\n%s", got)
	}
	if got := readAt(t, filepath.Join(root, ".gitignore")); !strings.Contains(got, "\n/.mcp.json\n") || strings.Contains(got, "apps/android") {
		t.Errorf(".gitignore lines are not anchored at the root:\n%s", got)
	}
}

// Decision (ADR-0124 Q4): the tracked set is keyed from the top, so a root below it
// finds its tracked files by their top-relative names.
func TestIgnoreTrackedBelowTheTop(t *testing.T) {
	g := needGit(t)
	top := g.repo()
	root := filepath.Join(top, "apps", "android")
	writeAt(t, root, ".mcp.json", "{}\n")
	g.run(top, "add", "-A")
	g.run(top, "commit", "-q", "-m", "m")
	c := g.ignore(root, created(".mcp.json", "opencode.json"), GitignoreFile, nil)
	if got := readAt(t, filepath.Join(root, ".gitignore")); strings.Contains(got, ".mcp.json") || !strings.Contains(got, "/opencode.json") {
		t.Errorf(".gitignore:\n%s\nnotes %v", got, c.Notes)
	}
}

// Decision (ADR-0124 Q4): a dry run writes nothing; an install with a flag outside a
// repository is refused; the uninstall's block part there is a no-op, never a refusal.
func TestIgnoreDryRunAndOutsideARepository(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	g.ignore(dir, created(".mcp.json"), ExcludeFile, func(o *IgnoreOptions) { o.DryRun = true })
	g.ignore(dir, created(".mcp.json"), GitignoreFile, func(o *IgnoreOptions) { o.DryRun = true })
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote .gitignore (%v)", err)
	}
	if strings.Contains(readAt(t, filepath.Join(dir, ".git", "info", "exclude")), ignoreHeader) {
		t.Error("a dry run wrote the exclude file")
	}

	plain, _ := filepath.EvalSymlinks(t.TempDir())
	for _, target := range []IgnoreTarget{ExcludeFile, GitignoreFile} {
		c, err := Ignore(IgnoreOptions{Root: plain, Target: target, Changes: created(".mcp.json")})
		if err != nil || c.Status != Refused || !strings.Contains(strings.Join(c.Notes, " "), NotARepository) {
			t.Errorf("%s install outside a repository: %+v, %v; want the refusal %q", target, c, err, NotARepository)
		}
		c, err = Ignore(IgnoreOptions{Root: plain, Target: target, Uninstall: true})
		if err != nil || c.Status != Unchanged {
			t.Errorf("%s uninstall outside a repository: %+v, %v; want a no-op", target, c, err)
		}
	}
	if _, err := os.Stat(filepath.Join(plain, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("an outside-a-repository run wrote .gitignore (%v)", err)
	}
}

// Decision (ADR-0124 Q4): a refused file of the run is not ignored (init did not write
// it), and names with pattern characters are escaped to match themselves.
func TestIgnoreSkipsRefusedAndEscapesNames(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	chs := append(created("a[1].json"), Change{Path: "opencode.jsonc", Status: Refused})
	g.ignore(dir, chs, GitignoreFile, nil)
	got := readAt(t, filepath.Join(dir, ".gitignore"))
	if strings.Contains(got, "jsonc") || !strings.Contains(got, `/a\[1].json`) {
		t.Errorf(".gitignore:\n%s", got)
	}
}

// A block whose end line was deleted by hand cannot be replaced safely: the file is
// refused and left as it is.
func TestIgnoreRefusesABlockWithNoEnd(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	broken := "mine\n" + ignoreHeader + "\n/x\nmore of mine\n"
	writeAt(t, dir, ".gitignore", broken)
	c := g.ignore(dir, created(".mcp.json"), GitignoreFile, nil)
	if c.Status != Refused || readAt(t, filepath.Join(dir, ".gitignore")) != broken {
		t.Errorf("status %s, file %q", c.Status, readAt(t, filepath.Join(dir, ".gitignore")))
	}
}

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func removed(rels ...string) []Change {
	var cs []Change
	for _, r := range rels {
		cs = append(cs, Change{Path: r, Status: Deleted})
	}
	return cs
}

func uninstalling(o *IgnoreOptions) { o.Uninstall = true }

func excludeOf(t *testing.T, dir string) string {
	t.Helper()
	return readAt(t, filepath.Join(dir, ".git", "info", "exclude"))
}

// rmInit removes one of init's files and the directories that leaves empty, as
// --uninstall does.
func rmInit(dir, rel string) error {
	if err := os.Remove(filepath.Join(dir, rel)); err != nil {
		return err
	}
	for d := filepath.Dir(filepath.Join(dir, rel)); d != dir; d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

func readOrEmpty(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// Decision (ADR-0126): the block is shared by every client that wrote into the file, so
// an install adds to it (scenario a: cursor, then claude) and an uninstall takes out only
// the lines of the paths it removed (scenario b: cursor leaves, claude stays), and the
// block goes only with its last line.
func TestIgnoreBlockIsSharedByClients(t *testing.T) {
	g := needGit(t)
	cursor := []string{".cursor/mcp.json", ".cursor/hooks.json"}
	claude := []string{".mcp.json", ".claude/settings.json"}
	all := append(append([]string{}, cursor...), claude...)
	for _, target := range []IgnoreTarget{ExcludeFile, GitignoreFile} {
		t.Run(string(target), func(t *testing.T) {
			dir := g.repo()
			for _, f := range all {
				writeAt(t, dir, f, "x\n")
			}
			file := filepath.Join(dir, ".gitignore")
			if target == ExcludeFile {
				file = filepath.Join(dir, ".git", "info", "exclude")
			}
			g.ignore(dir, created(cursor...), target, nil)
			g.ignore(dir, created(claude...), target, nil)
			for _, f := range all {
				if !strings.Contains(readAt(t, file), "\n/"+f+"\n") {
					t.Errorf("after cursor then claude the block lacks %s:\n%s", f, readAt(t, file))
				}
			}
			if got := g.listed(dir, all...); len(got) != 0 {
				t.Errorf("git status lists %v", got)
			}
			g.ignore(dir, removed(cursor...), target, uninstalling)
			if got := g.listed(dir, claude...); len(got) != 0 {
				t.Errorf("uninstalling cursor unhid claude's files %v", got)
			}
			if got := readAt(t, file); strings.Contains(got, ".cursor") || strings.Count(got, ignoreHeader) != 1 {
				t.Errorf("the block after cursor's uninstall:\n%s", got)
			}
			g.ignore(dir, removed(claude...), target, uninstalling)
			if strings.Contains(readOrEmpty(file), ignoreHeader) {
				t.Errorf("the block outlived its last line")
			}
		})
	}
}

// Decision (ADR-0126): the exclude file is shared by every work tree, and a line anchored
// at the top hides the same path in all of them; a worktree's uninstall must not unhide
// the main tree's copy (scenario c).
func TestIgnoreWorktreeUninstallKeepsOtherTreesHidden(t *testing.T) {
	g := needGit(t)
	main := g.repo()
	wt := filepath.Join(t.TempDir(), "wt")
	g.run(main, "worktree", "add", "-q", "-b", "side", wt)
	wt, _ = filepath.EvalSymlinks(wt)
	for _, d := range []string{main, wt} {
		for _, f := range initFiles {
			writeAt(t, d, f, "x\n")
		}
		g.ignore(d, created(initFiles...), ExcludeFile, nil)
	}
	for _, f := range initFiles {
		if err := rmInit(wt, f); err != nil {
			t.Fatal(err)
		}
	}
	g.ignore(wt, removed(initFiles...), ExcludeFile, uninstalling)
	if got := g.listed(main, initFiles...); len(got) != 0 {
		t.Errorf("after the worktree's uninstall git status in the main tree lists %v", got)
	}
	// once the main tree's files go too, the block goes
	for _, f := range initFiles {
		if err := rmInit(main, f); err != nil {
			t.Fatal(err)
		}
	}
	g.ignore(main, removed(initFiles...), ExcludeFile, uninstalling)
	if strings.Contains(excludeOf(t, main), ignoreHeader) {
		t.Errorf("the block outlived every tree's files:\n%s", excludeOf(t, main))
	}
	// and a worktree's files that stay are kept hidden by a main-tree uninstall
	for _, f := range initFiles {
		writeAt(t, wt, f, "x\n")
	}
	g.ignore(wt, created(initFiles...), ExcludeFile, nil)
	g.ignore(main, removed(initFiles...), ExcludeFile, uninstalling)
	if got := g.listed(wt, initFiles...); len(got) != 0 {
		t.Errorf("the main tree's uninstall unhid the worktree's files %v", got)
	}
}

// Decision (ADR-0126): two project roots in one repository share the exclude file; the
// second's install keeps the first's lines, and uninstalling one root keeps the other's
// (scenario d).
func TestIgnoreTwoRootsOneRepository(t *testing.T) {
	g := needGit(t)
	top := g.repo()
	a, b := filepath.Join(top, "a"), filepath.Join(top, "b")
	var rels, bRels []string
	for _, f := range initFiles {
		rels = append(rels, "a/"+f, "b/"+f)
		bRels = append(bRels, "b/"+f)
	}
	for _, r := range []string{a, b} {
		for _, f := range initFiles {
			writeAt(t, r, f, "x\n")
		}
	}
	g.wantAllListed(top, rels...)
	for _, r := range []string{a, b} {
		g.ignore(r, created(initFiles...), ExcludeFile, nil)
	}
	if got := g.listed(top, rels...); len(got) != 0 {
		t.Errorf("git status lists %v: the second root dropped the first's lines", got)
	}
	for _, f := range initFiles {
		if err := rmInit(a, f); err != nil {
			t.Fatal(err)
		}
	}
	g.ignore(a, removed(initFiles...), ExcludeFile, uninstalling)
	if got := g.listed(top, bRels...); len(got) != 0 {
		t.Errorf("uninstalling root a unhid b's files %v", got)
	}
	if strings.Contains(excludeOf(t, top), "/a/") {
		t.Errorf("root a's lines are still there:\n%s", excludeOf(t, top))
	}
}

// Safety (ADR-0126): init does not write through an info directory that leads out of the
// repository's common directory.
func TestIgnoreExcludeRefusesInfoLeavingTheCommonDir(t *testing.T) {
	g := needGit(t)
	dir := g.repo()
	elsewhere := t.TempDir()
	info := filepath.Join(dir, ".git", "info")
	if err := os.RemoveAll(info); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, info); err != nil {
		t.Fatal(err)
	}
	c := g.ignore(dir, created(".mcp.json"), ExcludeFile, nil)
	if c.Status != Refused || !strings.Contains(strings.Join(c.Notes, " "), "outside") {
		t.Errorf("status %s, notes %v; want refused", c.Status, c.Notes)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("init wrote through the link: %v", entries)
	}
}

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

// rootFx is the submodule fixture (gittest.Submodules: `lib`, its `lib/nested` and the
// uninitialised `extra`) opened as the workspace, with a history of its own in lib: a
// branch main tracking origin/main with three commits of its own, a branch `side`, a tag
// `v1`, and files under src/ that the last commit adds to, renames and deletes.
type rootFx struct {
	*readFx
	s           *gittest.Subs
	lib, nested string
	// libOids names lib's commits: origin (origin/main), c1 and c2 (main's two of its own,
	// c2 the last), side, and in the fence test signed.
	libOids map[string]string
}

func newRootFx(t *testing.T) *rootFx {
	t.Helper()
	f := newReadFx(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.at(s.Super)
	x := &rootFx{readFx: f, s: s, lib: filepath.Join(f.root, "lib"), libOids: map[string]string{}}
	x.nested = filepath.Join(x.lib, "nested")
	lib := x.lib
	rev := func(r string) string { return strings.TrimSpace(f.g(lib, "rev-parse", r)) }

	// The submodule update left lib detached at the recorded commit: give it the branch
	// main, tracking origin/main, as a developer's clone would have.
	f.g(lib, "checkout", "-q", "-B", "main", "--track", "origin/main")
	x.libOids["origin"] = rev("origin/main")
	f.write(lib, "src/old.txt", "one\ntwo\n")
	f.write(lib, "src/deep/x.txt", "x\n")
	f.write(lib, "gone.txt", "gone\n")
	f.commit(lib)
	x.libOids["c1"] = rev("HEAD")
	f.g(lib, "tag", "v1")
	f.g(lib, "branch", "side")
	f.g(lib, "switch", "-q", "side")
	f.write(lib, "side.txt", "side\n")
	f.commit(lib)
	x.libOids["side"] = rev("HEAD")
	f.g(lib, "switch", "-q", "main")
	f.g(lib, "mv", "src/old.txt", "src/new.txt")
	f.write(lib, "src/deep/x.txt", "x\ny\nz\n")
	f.g(lib, "rm", "-q", "gone.txt")
	f.commit(lib)
	x.libOids["c2"] = rev("HEAD")
	return x
}

// call runs a registered git read as the UI would and returns its result or error.
func (f *readFx) call(name string, params any) (any, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup(name)
	if !ok {
		f.t.Fatalf("%s is not registered", name)
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: params})
	if err != nil {
		return nil, capability.AsError(err)
	}
	return res, nil
}

func (f *readFx) logRoot(root string, p LogParams) LogResult {
	f.t.Helper()
	p.Root = root
	res, err := f.logPage(p)
	if err != nil {
		f.t.Fatalf("git.log root %q: %s (%s; cause %q)", root, err.Code, err.Message, err.Cause)
	}
	return res
}

func (f *readFx) refsRoot(root string) RefsResult {
	f.t.Helper()
	res, err := f.call("git.refs", RefsParams{Root: root})
	if err != nil {
		f.t.Fatalf("git.refs root %q: %s (%s; cause %q)", root, err.Code, err.Message, err.Cause)
	}
	return res.(RefsResult)
}

func (f *readFx) showRoot(root, oid string) ShowResult {
	f.t.Helper()
	res, err := f.call("git.show", ShowParams{Oid: oid, Root: root})
	if err != nil {
		f.t.Fatalf("git.show root %q: %s (%s; cause %q)", root, err.Code, err.Message, err.Cause)
	}
	return res.(ShowResult)
}

func (f *readFx) readRoot(root, path, base string) (ReadResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("git.read", ReadParams{Path: path, Base: base, Root: root})
	if err != nil {
		return ReadResult{}, err
	}
	return res.(ReadResult), nil
}

// Decision (ADR-0071 §7, ADR-0069 §2): with a root, git.log is the history of that
// submodule's own repository: the same walk git gives there, with that repository's tips,
// paged by `from` and `skip` as ever. "." and absent are the workspace's repository, an
// oid of the superproject is no commit of the submodule (an oid names a commit of one
// repository), and a nested submodule is a root by its path from the workspace.
func TestRootLog(t *testing.T) {
	x := newRootFx(t)
	ctx := context.Background()

	res := x.logRoot("lib", LogParams{})
	libTips, _, err := tips(ctx, x.r, x.lib, false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(res.Tips, libTips) || res.Tips[0] != x.libOids["c2"] {
		t.Errorf("tips = %v; want lib's %v (HEAD first)", res.Tips, libTips)
	}
	// git -C lib log --date-order --format=%H over lib's tips.
	oracle := lines(x.g(x.lib, append([]string{"log", "--date-order", "--format=%H"}, libTips...)...))
	if got := oidsIn(res.Commits); !equal(got, oracle) || len(got) < 5 {
		t.Errorf("lib's page:\n got %v\nwant git -C lib log --date-order: %v", got, oracle)
	}
	for _, own := range []string{"c1", "c2", "side"} {
		if !slices.Contains(oidsIn(res.Commits), x.libOids[own]) {
			t.Errorf("lib's own commit %s is not in lib's log", own)
		}
	}

	// The superproject's log is another history, and "." and absent are it.
	super := x.mustLog(LogParams{})
	if dot := x.logRoot(".", LogParams{}); !equal(oidsIn(dot.Commits), oidsIn(super.Commits)) || !equal(dot.Tips, super.Tips) {
		t.Errorf(`root "." differs from no root`)
	}
	if slices.Contains(oidsIn(super.Commits), x.libOids["c1"]) || equal(oidsIn(super.Commits), oidsIn(res.Commits)) {
		t.Errorf("the superproject's log holds lib's commits: root did not change the repository")
	}

	// Pages with from and root: the tips of lib pass back, and the pages join.
	first := x.logRoot("lib", LogParams{Limit: ip(3)})
	if !first.More || len(first.Commits) != 3 || !equal(first.Tips, libTips) {
		t.Fatalf("first page = %d commits more %v tips %v", len(first.Commits), first.More, first.Tips)
	}
	second := x.logRoot("lib", LogParams{From: first.Tips, Skip: ip(3), Limit: ip(1000)})
	if len(second.Tips) != 0 {
		t.Errorf("a page with from returned tips %v", second.Tips)
	}
	if got := append(oidsIn(first.Commits), oidsIn(second.Commits)...); !equal(got, oracle) {
		t.Errorf("pages joined:\n got %v\nwant %v", got, oracle)
	}
	// An oid of the superproject is not a commit of lib (and the reverse).
	_, rerr := x.logPage(LogParams{From: []string{super.Tips[0]}, Root: "lib"})
	wantCode(t, rerr, capability.ErrInvalidParams)
	_, rerr = x.logPage(LogParams{From: []string{res.Tips[0]}})
	wantCode(t, rerr, capability.ErrInvalidParams)

	// A nested submodule, by its path from the workspace.
	nres := x.logRoot("lib/nested", LogParams{})
	want := lines(x.g(x.nested, "log", "--date-order", "--format=%H", x.s.Oids["nested2"]))
	if got := oidsIn(nres.Commits); !equal(got, want) || nres.Tips[0] != x.s.Oids["nested2"] {
		t.Errorf("lib/nested: got %v tips %v; want %v", got, nres.Tips, want)
	}
}

// Decision (ADR-0071 §7, ADR-0069 §2): with a root, git.refs is that submodule's HEAD,
// branches, remote-tracking branches, tags, work trees and tip set, and the current branch's
// ahead and behind against its upstream there. A nested submodule left detached by
// `submodule update` is a detached HEAD at its recorded commit.
func TestRootRefs(t *testing.T) {
	x := newRootFx(t)
	ctx := context.Background()
	res := x.refsRoot("lib")

	if res.Head.Oid != x.libOids["c2"] || res.Head.Branch != "main" || res.Head.Detached || res.Head.Upstream != "origin/main" {
		t.Errorf("head = %+v; want main at c2 tracking origin/main", res.Head)
	}
	ahead := strings.TrimSpace(x.g(x.lib, "rev-list", "--count", "origin/main..main"))
	if n, _ := strconv.Atoi(ahead); res.Head.Ahead != n || n != 2 || res.Head.Behind != 0 {
		t.Errorf("ahead %d behind %d; want %s and 0 (git's own count)", res.Head.Ahead, res.Head.Behind, ahead)
	}
	var branches, remotes, tags []string
	for _, b := range res.Branches {
		branches = append(branches, b.Name)
	}
	for _, r := range res.Remotes {
		remotes = append(remotes, r.Remote+"/"+r.Name)
	}
	for _, tg := range res.Tags {
		tags = append(tags, tg.Name)
	}
	sort.Strings(remotes)
	if want := []string{"main", "side"}; !equal(branches, want) {
		t.Errorf("branches = %v; want lib's %v", branches, want)
	}
	if !slices.Contains(remotes, "origin/main") || !equal(tags, []string{"v1"}) {
		t.Errorf("remotes %v tags %v; want origin/main and v1", remotes, tags)
	}
	libTips, _, err := tips(ctx, x.r, x.lib, false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(res.Tips, libTips) {
		t.Errorf("tips = %v; want lib's %v", res.Tips, libTips)
	}
	// git itself lists a submodule's one work tree by its git directory, so the path is
	// git's own, and it is not marked current (the top git reports for the root is lib).
	first, _, _ := strings.Cut(x.g(x.lib, "worktree", "list", "--porcelain"), "\n")
	gitsPath := strings.TrimPrefix(first, "worktree ")
	if len(res.Worktrees) != 1 || res.Worktrees[0].Path != gitsPath || res.Worktrees[0].Branch != "main" || res.Worktrees[0].Oid != x.libOids["c2"] {
		t.Errorf("worktrees = %+v; want git's own entry for lib (%s) on main at c2", res.Worktrees, gitsPath)
	}

	// Not the superproject's refs.
	super := x.mustRefs()
	if super.Head.Oid == res.Head.Oid || len(super.Branches) != 1 || equal(super.Tips, res.Tips) {
		t.Errorf("the superproject's refs equal lib's: %+v", super.Head)
	}
	if dot := x.refsRoot("."); dot.Head != super.Head || !equal(dot.Tips, super.Tips) {
		t.Errorf(`root "." differs from no root`)
	}

	nres := x.refsRoot("lib/nested")
	if !nres.Head.Detached || nres.Head.Oid != x.s.Oids["nested2"] || nres.Head.Branch != "" {
		t.Errorf("nested head = %+v; want detached at nested2", nres.Head)
	}
}

// Decision (ADR-0071 §7, plan m2n P6): paths in and out stay workspace-relative, so with a
// root the files of git.show carry the root's path (and a rename's origin), whatever the
// submodule's own paths are; the details are that repository's commit, and a commit of the
// superproject is not one. The statuses and counts are git's, in lib.
func TestRootShowPaths(t *testing.T) {
	x := newRootFx(t)
	res := x.showRoot("lib", x.libOids["c2"])

	if res.Oid != x.libOids["c2"] || len(res.Parents) != 1 || res.Parents[0] != x.libOids["c1"] {
		t.Errorf("details = %s parents %v; want c2 on c1", res.Oid, res.Parents)
	}
	got := map[string]ShowFile{}
	for _, f := range res.Files {
		got[f.Path] = f
	}
	if len(res.Files) != 3 {
		t.Fatalf("files = %v; want gone.txt, src/deep/x.txt and the rename", showPathsOf(res.Files))
	}
	if f := got["lib/gone.txt"]; f.Status != "deleted" {
		t.Errorf("lib/gone.txt = %+v; want deleted", f)
	}
	if f := got["lib/src/new.txt"]; f.Status != "renamed" || f.OrigPath != "lib/src/old.txt" {
		t.Errorf("lib/src/new.txt = %+v; want renamed from lib/src/old.txt", f)
	}
	counts := x.numstatOf(x.lib, x.libOids["c1"], x.libOids["c2"])
	if f := got["lib/src/deep/x.txt"]; f.Status != "modified" || f.Added == nil || f.Removed == nil ||
		strconv.Itoa(*f.Added)+" "+strconv.Itoa(*f.Removed) != counts["src/deep/x.txt"] {
		t.Errorf("lib/src/deep/x.txt = %+v; want modified with git's counts %q", f, counts["src/deep/x.txt"])
	}
	// The order is git's path order, root prefix and all.
	if want := []string{"lib/gone.txt", "lib/src/deep/x.txt", "lib/src/new.txt"}; !equal(showPathsOf(res.Files), want) {
		t.Errorf("paths = %v; want %v", showPathsOf(res.Files), want)
	}

	// A commit of lib is no commit of the superproject, and the reverse.
	_, err := x.showPage(x.libOids["c2"])
	wantCode(t, err, capability.ErrInvalidParams)
	_, err = x.call("git.show", ShowParams{Oid: x.s.Oids["super"], Root: "lib"})
	wantCode(t, err, capability.ErrInvalidParams)
	// The superproject's own commit, with root ".", lists its gitlinks as files, unprefixed.
	if sup := x.showRoot(".", x.s.Oids["super"]); !equal(showPathsOf(sup.Files), []string{".gitmodules", "extra", "lib"}) {
		t.Errorf(`root ".": files %v; want the superproject's own, unprefixed`, showPathsOf(sup.Files))
	}

	// A nested submodule: its path from the workspace in front.
	n := x.showRoot("lib/nested", x.s.Oids["nested2"])
	if len(n.Files) != 1 || n.Files[0].Path != "lib/nested/n.txt" || n.Files[0].Status != "modified" {
		t.Errorf("nested files = %+v; want lib/nested/n.txt modified", n.Files)
	}
}

// Decision (ADR-0071 §7, plan m2n P6): git.read with a root reads the file as that
// submodule's repository has it at HEAD or at one of its commits; the path is the
// workspace's (it carries the root's path, and the result's path too); a path outside the
// root, the workspace's own files among them, is FILE_OUTSIDE_ROOT before any git starts.
func TestRootReadAtCommit(t *testing.T) {
	x := newRootFx(t)

	res, err := x.readRoot("lib", "lib/lib.txt", "")
	if err != nil || !res.Exists || text(res) != "2\n" || res.Path != "lib/lib.txt" || res.Base != "HEAD" {
		t.Errorf("lib/lib.txt at HEAD = %+v (text %q), %v; want lib's 2", res, text(res), err)
	}
	// At an older commit of lib: the recorded first one.
	res, err = x.readRoot("lib", "lib/lib.txt", x.s.Oids["lib1"])
	if err != nil || text(res) != "1\n" || res.Base != x.s.Oids["lib1"] {
		t.Errorf("lib/lib.txt at lib1 = %+v, %v; want 1", res, err)
	}
	// A file lib's own history added, at lib's own commit; and absent before it.
	if res, err = x.readRoot("lib", "lib/src/deep/x.txt", x.libOids["c2"]); err != nil || text(res) != "x\ny\nz\n" {
		t.Errorf("x.txt at c2 = %+v, %v", res, err)
	}
	if res, err = x.readRoot("lib", "lib/src/deep/x.txt", x.s.Oids["lib1"]); err != nil || res.Exists {
		t.Errorf("x.txt at lib1 = %+v, %v; want absent", res, err)
	}
	// Absolute spellings inside the root are accepted as ever.
	if res, err = x.readRoot("lib", filepath.Join(x.lib, "lib.txt"), ""); err != nil || res.Path != "lib/lib.txt" || text(res) != "2\n" {
		t.Errorf("absolute path = %+v, %v", res, err)
	}
	// Without the root the same path is below the superproject's gitlink: nothing there.
	if res, err = x.readRoot("", "lib/lib.txt", ""); err != nil || res.Exists {
		t.Errorf("lib/lib.txt without a root = %+v, %v; want absent in the superproject", res, err)
	}
	// A commit of the superproject is no base in lib.
	_, err = x.readRoot("lib", "lib/lib.txt", x.s.Oids["super"])
	wantCode(t, err, capability.ErrInvalidParams)
	// The root itself is a directory there, not a file.
	_, err = x.readRoot("lib", "lib", "")
	wantCode(t, err, capability.ErrFileWrongType)

	// The nested submodule.
	res, err = x.readRoot("lib/nested", "lib/nested/n.txt", "")
	if err != nil || text(res) != "2\n" || res.Path != "lib/nested/n.txt" {
		t.Errorf("nested n.txt = %+v, %v", res, err)
	}
	if res, err = x.readRoot("lib/nested", "lib/nested/n.txt", x.s.Oids["nested1"]); err != nil || text(res) != "1\n" {
		t.Errorf("nested n.txt at nested1 = %+v, %v", res, err)
	}

	// Outside the root: the workspace's own file, ".", a sibling submodule, the parent of
	// the root, a root's name that merely begins like it; no git runs for any of them.
	before := x.starts.Load()
	for _, p := range []string{"README.md", ".", "extra/extra.txt", "lib2/x", "libx", ".gitmodules", "lib/../README.md"} {
		_, err = x.readRoot("lib", p, "")
		wantCode(t, err, capability.ErrFileOutsideRoot)
	}
	_, err = x.readRoot("lib/nested", "lib/lib.txt", "")
	wantCode(t, err, capability.ErrFileOutsideRoot)
	if n := x.starts.Load() - before; n != 0 {
		t.Errorf("%d gits ran for paths outside the root; want none", n)
	}
}

// Decision (ADR-0071 §7, plan m2n P6): a root is a path the repository records as a
// gitlink, level by level, that is initialised and has no symlink on the way. Everything
// else is REQUEST_INVALID_PARAMS before any git runs in the place it names, through each
// of the four reads, trusted or not. Each shape here is one the plan names.
func TestRootRefused(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		name := "trusted"
		if untrusted {
			name = "untrusted"
		}
		t.Run(name, func(t *testing.T) {
			x := newRootFx(t)
			if untrusted {
				x.untrusted()
			}
			ws := x.root
			// Places of the workspace that are not submodules.
			x.write(ws, "docs/a.txt", "a\n")
			x.g(ws, "add", "docs/a.txt")
			x.g(ws, "commit", "-q", "-m", "docs")
			inner := filepath.Join(ws, "docs", "inner") // a repository the superproject does not record
			x.g(ws, "init", "-q", "-b", "main", inner)
			x.write(inner, "i.txt", "i\n")
			x.commit(inner)
			plainInLib := filepath.Join(x.lib, "plain", "x") // a repository below a plain directory of lib
			x.g(x.lib, "init", "-q", "-b", "main", plainInLib)
			x.write(plainInLib, "p.txt", "p\n")
			x.commit(plainInLib)
			x.g(ws, "update-index", "--add", "--cacheinfo", "160000,"+x.s.Oids["extra"]+",fake")
			if err := os.MkdirAll(filepath.Join(ws, "fake", ".git"), 0o755); err != nil { // no repository: git finds the superproject's
				t.Fatal(err)
			}
			if err := os.Symlink("lib", filepath.Join(ws, "link-lib")); err != nil {
				t.Fatal(err)
			}
			// Gitlinks the superproject records whose directory sits behind a symlinked
			// directory: `mods` leads to a directory of the workspace, `outer` out of it. Each
			// holds a real repository `m`, so only the symlink stands in the way.
			outside := t.TempDir()
			for _, l := range []struct{ link, target, at string }{
				{"mods", "mods-real", filepath.Join(ws, "mods-real", "m")},
				{"outer", outside, filepath.Join(outside, "m")},
			} {
				if err := os.MkdirAll(filepath.Dir(l.at), 0o755); err != nil {
					t.Fatal(err)
				}
				x.g(filepath.Dir(l.at), "init", "-q", "-b", "main", l.at)
				x.write(l.at, "m.txt", "m\n")
				x.commit(l.at)
				x.g(ws, "update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(x.g(l.at, "rev-parse", "HEAD"))+","+l.link+"/m")
				if err := os.Symlink(l.target, filepath.Join(ws, l.link)); err != nil {
					t.Fatal(err)
				}
			}

			// refused[root] = the directories no git may have been started in.
			type shape struct {
				root string
				// none: no git at all may start, not even in the workspace.
				none bool
				// off lists directories git must never be started in.
				off []string
			}
			shapes := []shape{
				{root: "..", none: true},
				{root: "../s", none: true},
				{root: "lib/../..", none: true},
				{root: "lib/..", none: true},
				{root: ws, none: true},
				{root: filepath.Join(ws, "lib"), none: true},
				{root: "/", none: true},
				{root: "./lib", none: true},
				{root: "lib/", none: true},
				{root: "lib//nested", none: true},
				{root: ".git", none: true},
				{root: "lib/.git", none: true},
				{root: ".git/modules/lib", none: true},
				{root: "lib\x00", none: true},
				{root: strings.Repeat("a/", 3000), none: true},
				// A plain directory, a file, an uninitialised submodule: nothing there to ask.
				{root: "docs", none: true, off: []string{filepath.Join(ws, "docs")}},
				{root: "README.md", none: true, off: []string{filepath.Join(ws, "README.md")}},
				{root: "extra", none: true, off: []string{filepath.Join(ws, "extra")}},
				{root: "nowhere", none: true},
				{root: "lib/src", off: []string{filepath.Join(ws, "lib", "src")}},
				{root: "lib/nested/n.txt", off: []string{x.nested + "/n.txt"}},
				// A symlink to the submodule: not a gitlink of the workspace, and never entered.
				{root: "link-lib", off: []string{filepath.Join(ws, "link-lib"), x.lib}},
				{root: "link-lib/nested", off: []string{filepath.Join(ws, "link-lib"), x.lib, x.nested}},
				// A gitlink the repository records, behind a symlinked directory.
				{root: "mods/m", off: []string{filepath.Join(ws, "mods"), filepath.Join(ws, "mods-real")}},
				{root: "outer/m", off: []string{filepath.Join(ws, "outer"), outside}},
				// A repository the superproject does not record, and one below a plain directory
				// of a submodule: the level above is asked once and says no.
				{root: "docs/inner", off: []string{inner, filepath.Join(ws, "docs")}},
				{root: "lib/plain/x", off: []string{plainInLib, filepath.Join(x.lib, "plain")}},
				// A middle that is no gitlink, and one that is a gitlink with nothing below it.
				{root: "docs/a.txt/x", none: true, off: []string{filepath.Join(ws, "docs")}},
				{root: "lib/nested/nope", off: []string{filepath.Join(x.nested, "nope")}},
				{root: "lib/lib.txt", off: []string{filepath.Join(x.lib, "lib.txt")}},
				// Case: the index is asked for exactly these bytes.
				{root: "LIB"},
			}
			for _, sh := range shapes {
				for _, read := range x.reads(sh.root) {
					dirs := x.gitDirs()
					before := x.starts.Load()
					_, err := read.run()
					label := read.name + " root " + strconv.Quote(truncate(sh.root, 40))
					if err == nil {
						t.Errorf("%s: accepted", label)
						continue
					}
					// git.read rejects a path outside the root before it reads the root; its path is
					// inside every root here.
					if err.Code != capability.ErrInvalidParams {
						t.Errorf("%s: %s (%s); want REQUEST_INVALID_PARAMS", label, err.Code, err.Message)
					}
					if sh.none {
						if n := x.starts.Load() - before; n != 0 {
							t.Errorf("%s: %d gits started, in %v; want none", label, n, *dirs)
						}
					}
					for _, d := range *dirs {
						for _, off := range sh.off {
							if d == off || strings.HasPrefix(d, off+string(filepath.Separator)) {
								t.Errorf("%s: a git was started in %s, the place it refuses", label, d)
							}
						}
					}
				}
			}

			// A symlink in place of the submodule's directory, and one out of the workspace:
			// the level above is asked (it records the gitlink), the symlink is never entered.
			real := filepath.Join(ws, "lib-real")
			if err := os.Rename(x.lib, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("lib-real", x.lib); err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{"lib", "lib/nested"} {
				for _, read := range x.reads(root) {
					dirs := x.gitDirs()
					_, err := read.run()
					if err == nil || err.Code != capability.ErrInvalidParams {
						t.Errorf("%s root %q behind a symlink: %v; want REQUEST_INVALID_PARAMS", read.name, root, err)
					}
					for _, d := range *dirs {
						if strings.Contains(d, "lib-real") || d == x.lib || strings.HasPrefix(d, x.lib+string(filepath.Separator)) {
							t.Errorf("%s root %q behind a symlink: a git was started in %s", read.name, root, d)
						}
					}
				}
			}
			elsewhere := t.TempDir()
			x.g(elsewhere, "init", "-q", "-b", "main", "repo")
			if err := os.Remove(x.lib); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(elsewhere, "repo"), x.lib); err != nil {
				t.Fatal(err)
			}
			for _, read := range x.reads("lib") {
				dirs := x.gitDirs()
				_, err := read.run()
				if err == nil || err.Code != capability.ErrInvalidParams {
					t.Errorf("%s root lib symlinked out of the workspace: %v; want REQUEST_INVALID_PARAMS", read.name, err)
				}
				for _, d := range *dirs {
					if strings.HasPrefix(d, elsewhere) || d == x.lib {
						t.Errorf("%s: a git was started in %s", read.name, d)
					}
				}
			}
			if err := os.Remove(x.lib); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(real, x.lib); err != nil {
				t.Fatal(err)
			}

			// A `.git` that is a symlink is no repository of ours: not asked about at all.
			if err := os.Rename(filepath.Join(x.lib, ".git"), filepath.Join(ws, "lib-dotgit")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(ws, "lib-dotgit"), filepath.Join(x.lib, ".git")); err != nil {
				t.Fatal(err)
			}
			for _, read := range x.reads("lib") {
				dirs := x.gitDirs()
				_, err := read.run()
				if err == nil || err.Code != capability.ErrInvalidParams {
					t.Errorf("%s root lib with a symlinked .git: %v; want REQUEST_INVALID_PARAMS", read.name, err)
				}
				for _, d := range *dirs {
					if d == x.lib {
						t.Errorf("%s: a git was started in lib, whose .git is a symlink", read.name)
					}
				}
			}
			os.Remove(filepath.Join(x.lib, ".git"))
			if err := os.Rename(filepath.Join(ws, "lib-dotgit"), filepath.Join(x.lib, ".git")); err != nil {
				t.Fatal(err)
			}

			// A gitlink whose directory has a `.git` that is no repository: git finds the
			// superproject above it, so it is a directory inside it, and is refused. (The one git
			// that runs there is the prefix check itself; nothing is read from it.)
			dirs := x.gitDirs()
			for _, read := range x.reads("fake") {
				_, err := read.run()
				if err == nil || err.Code != capability.ErrInvalidParams {
					t.Errorf("%s root fake (no repository of its own): %v; want REQUEST_INVALID_PARAMS", read.name, err)
				}
			}
			for _, d := range *dirs {
				if d != ws && d != filepath.Join(ws, "fake") {
					t.Errorf("fake: a git was started in %s", d)
				}
			}

			// Control: the same fixture, a root that is fine, is read.
			if res := x.logRoot("lib", LogParams{}); len(res.Commits) == 0 {
				t.Errorf("control: lib's log is empty after the refusals")
			}
		})
	}
}

// read is one of the four reads called with a root.
type rootRead struct {
	name string
	run  func() (any, *capability.Error)
}

// reads are the four reads, each with root, for a test that holds them to one rule. The
// show and the read name a path and an oid that exist in the superproject and in lib.
func (x *rootFx) reads(root string) []rootRead {
	return []rootRead{
		{"git.log", func() (any, *capability.Error) { return x.call("git.log", LogParams{Root: root}) }},
		{"git.refs", func() (any, *capability.Error) { return x.call("git.refs", RefsParams{Root: root}) }},
		{"git.show", func() (any, *capability.Error) {
			return x.call("git.show", ShowParams{Oid: x.libOids["c2"], Root: root})
		}},
		{"git.read", func() (any, *capability.Error) {
			// The path lies inside every root of the table that has a slash-free or nested
			// name only by luck: it is the root's own directory, so a refused root is refused
			// as a root, not as a path.
			p := root
			if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
				p = "README.md"
			}
			return x.call("git.read", ReadParams{Path: p + "/f", Root: root})
		}},
	}
}

// Decision (ADR-0066 §3, ADR-0071 §7): on an untrusted workspace a submodule's own
// repository configuration is as hostile as the workspace's, and every git run in it, the
// resolution of the root included, has that repository's filter drivers switched off,
// read in that repository's own directory. The configuration of both lib and lib/nested
// carries each attack step 1 tried (a signature program on a signed HEAD, an external
// diff, a diff command, a textconv, a pager, a filter, an fsmonitor) and a replace ref
// forging lib's HEAD; none fires through any of the four reads, and the control shows plain
// git firing them in the same repositories.
//
// What this can and cannot see: the history plumbing runs no filter (ADR-0069 §Context), so
// the markers can only prove that nothing fires. That the driver read happens in the
// submodule, not the workspace root, is proved by the argv: every git in a submodule
// carries the override of that submodule's own driver, which the workspace root does not
// define.
func TestRootFence(t *testing.T) {
	x := newRootFx(t)
	marks := t.TempDir()
	mark := func(name string) string { return filepath.Join(marks, name) }
	marked := func() []string {
		ms, _ := filepath.Glob(mark("*"))
		for i := range ms {
			ms[i] = filepath.Base(ms[i])
		}
		return ms
	}
	clear := func() {
		ms, _ := filepath.Glob(mark("*"))
		for _, m := range ms {
			os.Remove(m)
		}
	}

	// A signed HEAD in lib (before any hostile configuration, so setup runs none of it).
	x.signedHead(x.lib, "PGP SIGNATURE")
	x.libOids["signed"] = strings.TrimSpace(x.g(x.lib, "rev-parse", "HEAD"))
	parent := strings.TrimSpace(x.g(x.lib, "rev-parse", "HEAD^"))

	type sub struct{ dir, gitDir, name, driver, tracked string }
	subs := []sub{
		{x.lib, filepath.Join(x.s.Super, ".git", "modules", "lib"), "lib", "evil-lib", "lib.txt"},
		{x.nested, filepath.Join(x.s.Super, ".git", "modules", "lib", "modules", "nested"), "nested", "evil-nested", "n.txt"},
	}
	for _, s := range subs {
		sc := func(what string) string {
			return x.script(what+"-"+s.name+".sh", "touch '"+mark(what+"-"+s.name)+"'\ncat")
		}
		x.append(filepath.Join(s.gitDir, "config"),
			"[filter \""+s.driver+"\"]\n\tclean = "+sc("clean")+"\n\tsmudge = "+sc("smudge")+"\n\tprocess = "+sc("process")+"\n\trequired = true\n"+
				"[core]\n\tfsmonitor = "+sc("fsmonitor")+"\n\tpager = "+sc("pager")+"\n"+
				"[pager]\n\tlog = true\n\tdiff = true\n"+
				"[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+sc("gpg")+"\n"+
				"[diff]\n\texternal = "+sc("diffext")+"\n"+
				"[diff \"evil\"]\n\tcommand = "+sc("diffcmd")+"\n\ttextconv = "+sc("textconv")+"\n")
		x.write(s.gitDir, "info/attributes", "*.txt filter="+s.driver+" diff=evil\n")
	}
	// A replace ref in lib making a forged commit stand for HEAD.
	forged := x.forgedCommit(x.lib, parent)
	x.g(x.lib, "update-ref", "refs/replace/"+x.libOids["signed"], forged)

	// The control: plain git fires the attacks in both repositories.
	clear()
	for _, s := range subs {
		x.touch(s.dir, s.tracked) // stat-dirty: git hashes it again through the clean filter
		x.gErr(s.dir, "status", "--porcelain")
		x.gErr(s.dir, "diff", "HEAD~1", "HEAD")
	}
	x.gErr(x.lib, "--no-replace-objects", "log", "-1") // the replace ref would show the unsigned forgery
	got := marked()
	// The process filter runs in place of clean and smudge when a driver names all three.
	for _, want := range []string{"process-lib", "process-nested", "fsmonitor-lib", "fsmonitor-nested", "gpg-lib", "diffext-lib"} {
		if !slices.Contains(got, want) {
			t.Fatalf("control: plain git did not fire %s (fired %v): the test cannot fail", want, got)
		}
	}

	// Untrusted, through the four reads, from both roots.
	x.untrusted()
	var argvs [][]string
	var dirs []string
	x.onStart = func(cmd *exec.Cmd) {
		argvs = append(argvs, cmd.Args)
		dirs = append(dirs, cmd.Dir)
	}
	clear()
	for _, root := range []string{"lib", "lib/nested"} {
		commit := x.libOids["signed"]
		file := "lib/lib.txt"
		if root == "lib/nested" {
			commit, file = x.s.Oids["nested2"], "lib/nested/n.txt"
		}
		x.touch(x.lib, "lib.txt")
		x.touch(x.nested, "n.txt")
		lres := x.logRoot(root, LogParams{})
		rres := x.refsRoot(root)
		sres := x.showRoot(root, commit)
		pres, err := x.readRoot(root, file, "")
		if err != nil || len(lres.Commits) == 0 || len(rres.Tips) == 0 || len(sres.Files) == 0 || !pres.Exists {
			t.Fatalf("root %q: log %d, refs %d tips, show %d files, read %+v (%v); want all four answered", root, len(lres.Commits), len(rres.Tips), len(sres.Files), pres, err)
		}
		// The replace ref does not show: lib's HEAD is the real, signed commit.
		if root == "lib" && (lres.Commits[0].AuthorName != "T" || sres.Author.Name != "T" || strings.Contains(sres.Message, "FORGED")) {
			t.Errorf("root lib shows the forged commit: %+v %+v", lres.Commits[0], sres.Author)
		}
	}
	if got := marked(); len(got) != 0 {
		t.Errorf("a submodule's own configuration ran something through the reads: %v", got)
	}

	// Every git in a submodule is preceded by that submodule's driver read and carries the
	// override of its own driver; the workspace root defines neither.
	driverRead := func(args []string) bool { return slices.Contains(args, "--name-only") }
	for i, args := range argvs {
		var want string
		switch dirs[i] {
		case x.lib:
			want = "filter.evil-lib.clean="
		case x.nested:
			want = "filter.evil-nested.clean="
		default:
			if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "filter.evil") }) {
				t.Errorf("a git in %s carries a submodule's driver override: %v", dirs[i], args)
			}
			continue
		}
		if !driverRead(args) && !slices.Contains(args, want) {
			t.Errorf("a git in %s lacks %s (its own driver, read in its own directory): %v", dirs[i], want, args)
		}
	}
	if !slices.Contains(dirs, x.lib) || !slices.Contains(dirs, x.nested) {
		t.Errorf("no git ran in lib or nested: %v", dirs)
	}

	// Trusted: the user's own git, and no override anywhere.
	x.trusted = func(string) (bool, *capability.Error) { return true, nil }
	argvs, dirs = nil, nil
	x.logRoot("lib", LogParams{})
	for i, args := range argvs {
		if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "filter.") }) {
			t.Errorf("trusted: a git in %s carries a filter override: %v", dirs[i], args)
		}
	}
}

// Decision (ADR-0071 §7): the root is a path of the workspace, not of the repository, so a
// workspace that is a subfolder of its repository names its submodule by the path below
// that folder, and what lies in the repository above the folder is outside every root.
func TestRootSubdirectoryWorkspace(t *testing.T) {
	f := newReadFx(t)
	top := f.root
	f.write(top, "app/a.txt", "a\n")
	f.write(top, "other/b.txt", "b\n")
	f.commit(top)
	for _, d := range []string{"app/sub", "other/sub2"} {
		f.g(top, "init", "-q", "-b", "main", d)
		f.write(filepath.Join(top, d), "s.txt", d+"\n")
		f.commit(filepath.Join(top, d))
	}
	f.commit(top) // records both as gitlinks
	if out := f.g(top, "ls-files", "--stage", "app/sub", "other/sub2"); strings.Count(out, "160000") != 2 {
		t.Fatalf("the fixture's gitlinks: %q", out)
	}
	f.at(filepath.Join(top, "app"))
	sub := filepath.Join(f.root, "sub")

	res := f.logRoot("sub", LogParams{})
	if want := lines(f.g(sub, "log", "--date-order", "--format=%H", "HEAD")); !equal(oidsIn(res.Commits), want) {
		t.Errorf("sub's log = %v; want %v", oidsIn(res.Commits), want)
	}
	if got := f.showRoot("sub", res.Commits[0].Oid); len(got.Files) != 1 || got.Files[0].Path != "sub/s.txt" {
		t.Errorf("sub's show files = %+v; want sub/s.txt", got.Files)
	}
	if rd, err := f.readRoot("sub", "sub/s.txt", ""); err != nil || text(rd) != "app/sub\n" || rd.Path != "sub/s.txt" {
		t.Errorf("sub/s.txt = %+v, %v", rd, err)
	}
	before := f.starts.Load()
	for _, root := range []string{"../other/sub2", "other/sub2", "app/sub", ".."} {
		_, err := f.call("git.refs", RefsParams{Root: root})
		wantCode(t, err, capability.ErrInvalidParams)
	}
	if n := f.starts.Load() - before; n != 0 {
		t.Errorf("%d gits for four refused roots; want none", n)
	}
}

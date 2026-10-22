package git

// m3b step 1 (ADR-0125 §1, §3): the watch's status stops ignoring submodules. A trusted
// root asks git itself (`--ignore-submodules=none`: git runs a child status in each
// populated submodule and flags what it finds in the record's S<c><m><u> field); an
// untrusted root asks for `dirty` (a gitlink whose commit differs, and no process in a
// submodule). These tests run real git on synthetic repositories; the second status the
// daemon runs in a submodule is step 2's.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// bits renders a Submodule the way git writes its <sub> field: "S" and then C, M, U or a
// dot for each bit; "-" for no submodule object.
func bits(s *SubmoduleChange) string {
	if s == nil {
		return "-"
	}
	b := []byte("S...")
	if s.Commits {
		b[1] = 'C'
	}
	if s.Modified {
		b[2] = 'M'
	}
	if s.Untracked {
		b[3] = 'U'
	}
	return string(b)
}

// describeEntries lists the entries as "path mark bits" (and " root=<r>" for an entry of a
// submodule), in the status's order.
func describeEntries(st Status) []string {
	var out []string
	for _, e := range st.Entries {
		line := e.Path + " " + string(e.Mark) + " " + bits(e.Submodule)
		if e.Root != "" {
			line += " root=" + e.Root
		}
		out = append(out, line)
	}
	return out
}

// entryOf is the status's entry for path.
func entryOf(st Status, path string) (Entry, bool) {
	for _, e := range st.Entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

// subStatus is the watch's status of dir (the repository's top) with the given trust.
func (e *env) subStatus(dir string, trusted bool) Status {
	e.t.Helper()
	// A hang guard, as TestSubmodulesFence's: the claim is what the status says.
	e.r.timeout = testtime.Bound(60 * time.Second)
	st, err := e.r.status(context.Background(), dir, trusted)
	if err != nil {
		e.t.Fatalf("status (trusted %v): %v", trusted, err)
	}
	return st
}

// gitField is the <sub> field of plain git's own record for path: `git status` of dir as
// the user would run it, with the given --ignore-submodules.
func (e *env) gitField(dir, ignore, path string) string {
	e.t.Helper()
	out := e.g(dir, "status", "--porcelain=v2", "-z", "--ignore-submodules="+ignore, "--untracked-files=all")
	for _, rec := range strings.Split(out, "\x00") {
		f := strings.Fields(rec)
		if len(f) > 2 && (f[0] == "1" || f[0] == "u") && f[len(f)-1] == path {
			return f[2]
		}
	}
	return ""
}

// ignoreFor is the --ignore-submodules value the watch uses for a trust.
func ignoreFor(trusted bool) string {
	if trusted {
		return "none"
	}
	return "dirty"
}

// Decision (ADR-0125 §1): the project's status reports a gitlink whose commit differs,
// added, removed or conflicted, on a trusted root and on an untrusted one, as its own
// entry with a `submodule` object whose bits are git's own. The value is explicit, so
// the entry is there whatever `all` the configuration would have chosen. A lib with
// content changes only has no entry on an untrusted root yet (the daemon's own pass in
// the submodule is step 2): `dirty` looks at no content.
func TestSubmoduleRecordsAgainstHEAD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		path  string
		mark  Mark
		setup func(e *env, s *gittest.Subs)
		// commits: the submodule's commit differs from the one HEAD records, staged or not.
		// A staged one is in the index, so git's own C is clear there (`M. S...`).
		commits bool
	}{
		{"a new commit, not staged", "lib", MarkModified, func(e *env, s *gittest.Subs) {
			e.g(filepath.Join(s.Super, "lib"), "commit", "-q", "--allow-empty", "-m", "x")
		}, true},
		{"a new commit, staged", "lib", MarkModified, func(e *env, s *gittest.Subs) {
			e.g(filepath.Join(s.Super, "lib"), "commit", "-q", "--allow-empty", "-m", "x")
			e.g(s.Super, "add", "lib")
		}, true},
		{"a submodule added and not committed", "added", MarkAdded, func(e *env, s *gittest.Subs) {
			e.g(s.Super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", s.Extra, "added")
		}, false},
		{"a submodule removed with git rm", "extra", MarkDeleted, func(e *env, s *gittest.Subs) {
			e.g(s.Super, "rm", "-q", "extra")
		}, false},
		{"a merge moving lib two ways", "lib", MarkConflicted, func(e *env, s *gittest.Subs) {
			lib := filepath.Join(s.Super, "lib")
			base := strings.TrimSpace(e.g(lib, "rev-parse", "HEAD"))
			e.g(s.Super, "checkout", "-q", "-b", "side")
			e.g(lib, "commit", "-q", "--allow-empty", "-m", "A")
			e.g(s.Super, "add", "lib")
			e.g(s.Super, "commit", "-q", "-m", "side")
			e.g(s.Super, "checkout", "-q", "main")
			e.g(lib, "checkout", "-q", base)
			e.g(lib, "commit", "-q", "--allow-empty", "-m", "B")
			e.g(s.Super, "add", "lib")
			e.g(s.Super, "commit", "-q", "-m", "main")
			if out, err := e.gErr(s.Super, "merge", "side"); err == nil {
				e.t.Fatalf("the merge did not conflict:\n%s", out)
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := needGit(t)
			s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
			c.setup(e, s)
			for _, trusted := range []bool{true, false} {
				st := e.subStatus(s.Super, trusted)
				got, ok := entryOf(st, c.path)
				if !ok || got.Mark != c.mark || got.Submodule == nil {
					t.Errorf("trusted %v: entry %q = %+v (present %v); want mark %s with a submodule object\nall: %v", trusted, c.path, got, ok, c.mark, describeEntries(st))
					continue
				}
				// M and U are git's own; C is against HEAD (ADR-0066 §5), so it is git's C or
				// a new commit that is staged, which git's field leaves clear.
				if want := e.gitField(s.Super, ignoreFor(trusted), c.path); bits(got.Submodule)[2:] != want[2:] {
					t.Errorf("trusted %v: %s bits %s; git's own field is %s", trusted, c.path, bits(got.Submodule), want)
				}
				if got.Submodule.Commits != c.commits {
					t.Errorf("trusted %v: %s commits = %v; want %v", trusted, c.path, got.Submodule.Commits, c.commits)
				}
				for _, other := range st.Entries {
					if other.Root != "" {
						t.Errorf("trusted %v: %q carries root %q; the daemon reads no submodule yet", trusted, other.Path, other.Root)
					}
				}
			}
		})
	}

	t.Run("content changes only", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		lib := filepath.Join(s.Super, "lib")
		e.write(lib, "lib.txt", "changed\n")
		e.write(lib, "new.txt", "new\n")
		if st := e.subStatus(s.Super, false); len(st.Entries) != 0 {
			t.Errorf("untrusted: %v; want no entry (dirty reports no content, step 2 reads it)", describeEntries(st))
		}
	})
}

// Decision (ADR-0125 §1, §3): on a trusted root git looks inside the submodule itself, so a
// lib changed only in content is a `modified` entry whose three bits are git's own field,
// and the same tree on an untrusted root has no entry yet. The expectations are literal
// where git's rule is plain (a tracked file M, an untracked one U) and git's own field
// where it is not (a change in a nested submodule).
func TestSubmoduleRecordsTrusted(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	lib := filepath.Join(s.Super, "lib")
	steps := []struct {
		name string
		do   func()
		want string // "" = git's own field only
	}{
		{"a tracked file modified", func() { e.write(lib, "lib.txt", "changed\n") }, "S.M."},
		{"and an untracked file", func() { e.write(lib, "new.txt", "new\n") }, "S.MU"},
		{"only an untracked file", func() { e.g(lib, "checkout", "-q", "--", "lib.txt") }, "S..U"},
		{"a nested submodule's file modified", func() {
			e.g(lib, "clean", "-q", "-f")
			e.write(filepath.Join(lib, "nested"), "n.txt", "changed\n")
		}, ""},
	}
	for _, step := range steps {
		step.do()
		st := e.subStatus(s.Super, true)
		got, ok := entryOf(st, "lib")
		field := e.gitField(s.Super, "none", "lib")
		if !ok || got.Mark != MarkModified || got.Submodule == nil || got.Root != "" {
			t.Errorf("%s: trusted entry = %+v (present %v); want lib modified with a submodule object\nall: %v", step.name, got, ok, describeEntries(st))
			continue
		}
		if bits(got.Submodule) != field {
			t.Errorf("%s: bits %s; git's own field is %s", step.name, bits(got.Submodule), field)
		}
		if step.want != "" && bits(got.Submodule) != step.want {
			t.Errorf("%s: bits %s; want %s", step.name, bits(got.Submodule), step.want)
		}
		if got.Submodule.Commits {
			t.Errorf("%s: commits set; lib is at its recorded commit", step.name)
		}
		if un := e.subStatus(s.Super, false); len(un.Entries) != 0 {
			t.Errorf("%s: untrusted: %v; want no entry yet", step.name, describeEntries(un))
		}
	}
}

// Decision (ADR-0125 §1 "git's children inherit ADR-0066 §2's -c overrides and protocol
// variables", plan m3b Q1): on a trusted root, the child status git starts in a submodule
// runs under the parent's guards. A lib that configures core.fsmonitor as a script gets no
// script run and its index is not rewritten (--no-optional-locks reaches the child), the
// control being plain git, which runs the script and rewrites the index; and a lib that is a
// blobless partial clone with a local remote fetches nothing while plain git fetches the blob
// its rename detection needs (GIT_NO_LAZY_FETCH, GIT_ALLOW_PROTOCOL and protocol.allow reach
// the child).
func TestTrustedSubmoduleChildGuarded(t *testing.T) {
	t.Parallel()
	t.Run("fsmonitor and the index", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
		lib := filepath.Join(s.Super, "lib")
		modules := filepath.Join(s.Super, ".git", "modules", "lib")
		marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
		fsm := e.script("fsmonitor.sh", "touch '"+marker+"'")
		e.append(filepath.Join(modules, "config"), "[core]\n\tfsmonitor = "+fsm+"\n")
		index := func() []byte {
			b, err := os.ReadFile(filepath.Join(modules, "index"))
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		// One tracked file changed in content, and one only stat-dirty (same bytes, new
		// mtime): the status must hash the second, and git would write what it learned
		// into lib's index.
		e.write(lib, "lib.txt", "changed\n")
		e.touch(lib, ".gitmodules")

		before := index()
		st := e.subStatus(s.Super, true)
		if got, ok := entryOf(st, "lib"); !ok || got.Submodule == nil || !got.Submodule.Modified {
			t.Fatalf("the tick did not look inside lib: %v", describeEntries(st))
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("the tick ran lib's core.fsmonitor script")
		}
		if !bytes.Equal(before, index()) {
			t.Errorf("the tick rewrote lib's index")
		}

		// The control: plain git runs the script and writes the index, so the two checks
		// above can fail.
		e.g(s.Super, "status", "--porcelain=v2", "--ignore-submodules=none")
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("control: plain git did not run lib's core.fsmonitor script; the test cannot fail")
		}
		if bytes.Equal(before, index()) {
			t.Errorf("control: plain git did not rewrite lib's index; the test cannot fail")
		}
	})

	t.Run("a partial clone fetches nothing", func(t *testing.T) {
		t.Parallel()
		e := needGit(t)
		remote := e.repo()
		e.g(remote, "config", "uploadpack.allowFilter", "true")
		e.g(remote, "config", "uploadpack.allowAnySHA1InWant", "true")
		var old, renamed strings.Builder
		for i := 0; i < 20; i++ {
			line := "line " + string(rune('a'+i)) + "\n"
			old.WriteString(line)
			if i == 10 {
				line = "a changed line\n"
			}
			renamed.WriteString(line)
		}
		e.write(remote, "old.txt", old.String())
		e.commit(remote)

		sup := e.repo()
		e.write(sup, "a.txt", "a\n")
		lib := filepath.Join(sup, "lib")
		e.g(sup, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+remote, "lib")
		// old.txt is in HEAD and its blob is not here. lib stages it renamed, to a file of
		// nearly the same content: git must read the blob to pair them.
		e.g(lib, "read-tree", "HEAD")
		e.g(lib, "rm", "-q", "--cached", "old.txt")
		e.write(lib, "new.txt", renamed.String())
		e.g(lib, "add", "new.txt")
		e.gitmodules(sup, "lib")
		e.commit(sup)

		missing := func() int {
			n := 0
			for _, l := range strings.Split(e.g(lib, "rev-list", "--objects", "--missing=print", "HEAD"), "\n") {
				if strings.HasPrefix(l, "?") {
					n++
				}
			}
			return n
		}
		before := missing()
		if before == 0 {
			t.Fatal("the fixture's lib is not a partial clone: no object is missing")
		}
		// The tick is the watch's own call. git's child status in lib cannot fetch and
		// fails, which fails the whole `none` status (exit 128, no output); the trusted
		// fallback (runner.status) asks again with `dirty` and the status succeeds.
		if res, err := e.r.run(context.Background(), sup, true, nil, statusArgsIgnoring("none")...); err != nil || res.ExitCode == 0 {
			t.Fatalf("none: exit %d, err %v; want git's child in lib to fail, or the fallback below proves nothing", res.ExitCode, err)
		}
		st, err := e.r.status(context.Background(), sup, true)
		if err != nil {
			t.Fatalf("the tick: %v; want exit 0 through the fallback", err)
		}
		if !st.fellBack {
			t.Errorf("the tick did not fall back to dirty: %v", describeEntries(st))
		}
		if after := missing(); after != before {
			t.Errorf("the tick fetched: %d objects missing before, %d after", before, after)
		}

		// The control: plain git fetches the blob from the local remote.
		e.g(sup, "status", "--porcelain=v2", "--ignore-submodules=none")
		if after := missing(); after >= before {
			t.Errorf("control: plain git fetched nothing (%d missing, %d after); the test cannot fail", before, after)
		}
	})
}

// Decision (ADR-0125 §1): the explicit flag overrides what the configuration would have
// decided. diff.ignoreSubmodules=all in the repository's config, `ignore = all` in
// .gitmodules and submodule.<name>.ignore=all in .git/config each hide a submodule from a
// plain `git status`, and the new-commit entry is there all the same, on both trusts.
func TestSubmoduleIgnoreConfigOverridden(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	lib := filepath.Join(s.Super, "lib")
	e.g(lib, "commit", "-q", "--allow-empty", "-m", "x")

	e.g(s.Super, "config", "diff.ignoreSubmodules", "all")
	e.g(s.Super, "config", "--file", ".gitmodules", "submodule.lib.ignore", "all")
	e.g(s.Super, "config", "submodule.lib.ignore", "all")
	if out := e.g(s.Super, "status", "--porcelain=v2", "--untracked-files=all"); strings.Contains(out, " lib") {
		t.Fatalf("control: plain git still reports lib under the three ignore settings:\n%s", out)
	}
	for _, trusted := range []bool{true, false} {
		st := e.subStatus(s.Super, trusted)
		if got, ok := entryOf(st, "lib"); !ok || got.Mark != MarkModified || got.Submodule == nil || !got.Submodule.Commits {
			t.Errorf("trusted %v: lib = %+v (present %v); want modified with new commits\nall: %v", trusted, got, ok, describeEntries(st))
		}
	}
}

// Decision (ADR-0125 §1, plan m3b Q1): on an untrusted root the status starts no git in a
// submodule, so a filter driver that only the submodule's own configuration holds (which
// the driver read of the root does not see) never runs. The control is plain git; the
// substitution is the trusted root's `none` on the same untrusted call, which does run it:
// that is what the flag's value decides.
func TestStatusStartsNoGitInASubmodule(t *testing.T) {
	t.Parallel()
	e := needGit(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	lib := filepath.Join(s.Super, "lib")
	modules := filepath.Join(s.Super, ".git", "modules", "lib")
	marker := filepath.Join(t.TempDir(), "filter-ran")
	clean := e.script("clean.sh", "touch '"+marker+"'\ncat")
	e.append(filepath.Join(modules, "config"), "[filter \"evil\"]\n\tclean = "+clean+"\n\trequired = true\n")
	e.write(modules, "info/attributes", "lib.txt filter=evil\n")
	e.touch(lib, "lib.txt")
	e.r.timeout = testtime.Bound(60 * time.Second)
	ran := func() bool { _, err := os.Stat(marker); return err == nil }

	// The tick, untrusted.
	if _, err := e.r.status(context.Background(), s.Super, false); err != nil {
		t.Fatal(err)
	}
	if ran() {
		t.Errorf("the untrusted tick ran the filter configured only in lib")
	}

	// Q1's evidence: the same call with the trusted root's value runs it.
	res, err := e.r.run(context.Background(), s.Super, false, nil, statusArgsIgnoring("none")...)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("status with none: %v, exit %d, %s", err, res.ExitCode, res.Stderr)
	}
	if !ran() {
		t.Errorf("none on an untrusted root did not run lib's filter: the flag's value does not decide the fence (narrow Q1's reason)")
	}

	// The control: plain git runs it too.
	os.Remove(marker)
	e.touch(lib, "lib.txt")
	e.g(s.Super, "status", "--porcelain=v2", "--ignore-submodules=none")
	if !ran() {
		t.Errorf("control: plain git did not run the filter; the test cannot fail")
	}
}

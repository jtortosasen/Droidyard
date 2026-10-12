package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Decision (ADR-0015 §5, Q1 §2), fail closed: a filter git selects with `filter=` and
// runs, whose driver name the daemon cannot write as an override (here the empty one),
// refuses the call; it is never skipped.
func TestEmptyDriverNameIsRefusedNotSkipped(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "mark")
	clean := e.script("clean.sh", "touch '"+mark+"'\ncat")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=\n")
	e.write(dir, "c.txt", "one\n")
	e.commit(dir)
	e.appendConfig(dir, "[filter \"\"]\n\tclean = "+clean+"\n")
	e.write(dir, "c.txt", "two\n")
	_, err := e.r.run(context.Background(), dir, false, nil, statusArgs...)
	if !errors.Is(err, ErrUnsafeDriver) {
		t.Errorf("err = %v, want ErrUnsafeDriver", err)
	}
	if exists(mark) {
		t.Errorf("untrusted: the empty-named driver's clean filter ran")
	}
	e.gErr(dir, "status", "--porcelain") // control
	if !exists(mark) {
		t.Errorf("control: plain git did not run the empty-named driver; the test cannot fail")
	}
}

// Decision: a two-part key (filter.<key>, no subsection) is not a driver, because git
// ignores it; the daemon skips it. Proven against real git, not assumed.
func TestTwoPartFilterKeyIsNoDriver(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "mark")
	clean := e.script("clean.sh", "touch '"+mark+"'\ncat")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=clean\nd.txt filter=\n")
	e.write(dir, "c.txt", "one\n")
	e.write(dir, "d.txt", "one\n")
	e.commit(dir)
	e.appendConfig(dir, "[filter]\n\tclean = "+clean+"\n")
	e.write(dir, "c.txt", "two\n")
	e.write(dir, "d.txt", "two\n")
	e.gErr(dir, "status", "--porcelain")
	if exists(mark) {
		t.Fatalf("git runs a subsection-less filter key; the daemon would have to refuse it")
	}
	res, err := e.r.run(context.Background(), dir, false, nil, statusArgs...)
	if err != nil || res.ExitCode != 0 {
		t.Errorf("untrusted status with a two-part filter key: %v exit %d", err, res.ExitCode)
	}
}

// Decision: the list of configured filters is never trusted incomplete. A driver
// listed after the stdout cap would otherwise run.
func TestTruncatedDriverListIsRefused(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "mark")
	clean := e.script("clean.sh", "touch '"+mark+"'\ncat")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=evil\n")
	e.write(dir, "c.txt", "one\n")
	e.commit(dir)
	// Each key lists as "filter.j.xxxxxx\0", 16 bytes: the cap falls on a record
	// boundary, so a reader that ignores the truncation sees only whole records and
	// misses the driver after them.
	e.appendConfig(dir, "[filter \"j\"]\n"+strings.Repeat("\txxxxxx = 1\n", 300000)+"[filter \"evil\"]\n\tclean = "+clean+"\n")
	e.write(dir, "c.txt", "two\n")
	_, err := e.r.run(context.Background(), dir, false, nil, statusArgs...)
	if !errors.Is(err, ErrUnsafeDriver) {
		t.Errorf("err = %v, want ErrUnsafeDriver", err)
	}
	if exists(mark) {
		t.Errorf("untrusted: a driver past the stdout cap ran")
	}
}

// Decision: the daemon never fetches. In a partial clone a missing blob makes git fetch
// it, which runs the repository's core.sshCommand; GIT_NO_LAZY_FETCH and
// protocol.allow=never each stop it, on a trusted root as on an untrusted one.
func TestPartialCloneNeverFetches(t *testing.T) {
	// The repository allows ssh per protocol, which overrides protocol.allow=never. Each
	// environment guard is also run without the other: "older git" drops
	// GIT_NO_LAZY_FETCH, which a git before 2.45 ignores.
	for _, c := range []struct {
		name    string
		trusted bool
		drop    string
	}{
		{"untrusted", false, ""},
		{"trusted", true, ""},
		{"untrusted older git", false, "GIT_NO_LAZY_FETCH=1"},
		{"untrusted no allow-protocol", false, "GIT_ALLOW_PROTOCOL=none"},
	} {
		trusted := c.trusted
		t.Run(c.name, func(t *testing.T) {
			e := needGit(t)
			if c.drop != "" {
				var env []string
				for _, kv := range e.r.env {
					if kv != c.drop {
						env = append(env, kv)
					}
				}
				e.r.env = env
			}
			mark := filepath.Join(t.TempDir(), "mark")
			ssh := e.script("ssh.sh", "touch '"+mark+"'\nexit 1")
			dir := e.repo()
			e.write(dir, "c.txt", "line one\nline two\nline three\nline four\n")
			e.commit(dir)
			oid := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD:c.txt"))
			e.g(dir, "mv", "c.txt", "m.txt")
			e.write(dir, "m.txt", "line one\nline two\nline three\nline four\nline five\n")
			e.g(dir, "add", "m.txt")
			if err := os.Remove(filepath.Join(dir, ".git", "objects", oid[:2], oid[2:])); err != nil {
				t.Fatal(err)
			}
			e.appendConfig(dir, "[core]\n\trepositoryformatversion = 1\n\tsshCommand = "+ssh+"\n[extensions]\n\tpartialClone = origin\n[remote \"origin\"]\n\turl = ssh://example.invalid/x\n\tpromisor = true\n[protocol \"ssh\"]\n\tallow = always\n")

			if _, err := e.r.run(context.Background(), dir, trusted, nil, statusArgs...); err != nil {
				t.Fatalf("status: %v", err)
			}
			if exists(mark) {
				t.Errorf("status fetched: core.sshCommand ran")
			}
			if _, err := e.r.run(context.Background(), dir, trusted, []byte("HEAD:c.txt\n"), "cat-file", "--batch"); err != nil {
				t.Fatalf("cat-file: %v", err)
			}
			if exists(mark) {
				t.Errorf("cat-file --batch fetched: core.sshCommand ran")
			}
			// Control: git without the guards does fetch.
			e.gErr(dir, "status", "--porcelain=v2", "--find-renames")
			if !exists(mark) {
				t.Errorf("control: plain git did not fetch; the test cannot fail")
			}
		})
	}
}

// Decision (T1): status --find-renames runs no diff textconv or external diff on an
// untrusted root.
func TestUntrustedStatusRunsNoTextconv(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "mark")
	tc := e.script("tc.sh", "touch '"+mark+"'\ncat \"$1\"")
	dir := e.repo()
	e.write(dir, ".gitattributes", "*.txt diff=tc\n")
	e.write(dir, "a.txt", "line one\nline two\nline three\nline four\n")
	e.commit(dir)
	e.appendConfig(dir, "[diff \"tc\"]\n\ttextconv = "+tc+"\n\tcommand = "+tc+"\n")
	e.g(dir, "mv", "a.txt", "b.txt")
	e.write(dir, "b.txt", "line one\nline two\nline three\nline four\nline five\n")
	e.g(dir, "add", "b.txt")
	e.write(dir, "b.txt", "line one\nline two\nline three\nline four\nline five\nsix\n")
	res := e.status(dir, false)
	if res.ExitCode != 0 {
		t.Fatalf("status exited %d: %s", res.ExitCode, res.Stderr)
	}
	if exists(mark) {
		t.Errorf("status ran a diff textconv or external diff")
	}
	if got := entries(res.Stdout); len(got) != 2 || !strings.HasPrefix(got[0], "2 RM ") || got[1] != "a.txt" {
		t.Errorf("entries = %q, want one renamed-and-modified record (two fields)", got)
	}
	e.g(dir, "diff", "HEAD", "--", "b.txt")
	if !exists(mark) {
		t.Errorf("control: git diff did not run the configured diff driver; the test cannot fail")
	}
}

// Decision (T2): a filter defined only in the user's global config is overridden on an
// untrusted root, as a repository's is.
func TestUntrustedRootOverridesGlobalFilter(t *testing.T) {
	e := needGit(t)
	mark := filepath.Join(t.TempDir(), "mark")
	clean := e.script("clean.sh", "touch '"+mark+"'\ncat")
	dir := e.repo()
	e.write(dir, ".gitattributes", "c.txt filter=glob\n")
	e.write(dir, "c.txt", "one\n")
	e.commit(dir)
	cfg := "[filter \"glob\"]\n\tclean = " + clean + "\n\trequired = true\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	e.write(dir, "c.txt", "two\n")
	res := e.status(dir, false)
	if res.ExitCode != 0 || exists(mark) {
		t.Fatalf("untrusted: exit %d, global filter ran %v: %s", res.ExitCode, exists(mark), res.Stderr)
	}
	if got, want := pathsOf(res.Stdout), []string{".M c.txt"}; !equal(got, want) {
		t.Errorf("entries = %q, want %q", got, want)
	}
	e.status(dir, true) // control: a trusted root runs the user's own filter
	if !exists(mark) {
		t.Errorf("control: the trusted run did not use the global filter; the test cannot fail")
	}
}

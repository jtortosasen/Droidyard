package git

// Fixtures shared between the tests that only read them (owner, 2026-10-15: the package's
// tests build real repositories with real git, and on a host that scans every process each
// build is paid for in seconds). A shared fixture is built once per test binary, the first
// time a test asks for it, in a directory TestMain removes at exit.
//
// The rule that keeps this honest: a test that takes a shared fixture reads it and nothing
// else. No write capability, no git command that changes a ref, the index, the work tree or
// the configuration, no touch, no file written in it. A test that changes anything, even one
// configuration line, builds its own copy (gittest.History, gittest.Synthetic... in its own
// t.TempDir()). When in doubt, own copy.

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// sharedBase is the directory the shared fixtures live under, made on first use.
var sharedBase struct {
	once sync.Once
	dir  string
	err  error
}

// removeShared is TestMain's cleanup: it first checks that no test changed a shared fixture
// (see checkShared), and returns what it found, for TestMain to fail the run with.
func removeShared() error {
	err := checkShared()
	if sharedBase.dir != "" {
		os.RemoveAll(sharedBase.dir)
	}
	return err
}

// sharedSnaps are the shared fixtures built and what they held when they were: a digest of
// every file's path, mode and bytes.
var sharedSnaps struct {
	sync.Mutex
	list []sharedSnap
}

type sharedSnap struct{ name, dir, digest string }

// checkShared compares each shared fixture with its digest from the end of its build. A test
// that wrote in one (a ref, the index, a file, a configuration line) would otherwise change
// what the tests that read it afterwards see, in a way that depends on which ran first.
func checkShared() error {
	sharedSnaps.Lock()
	defer sharedSnaps.Unlock()
	for _, s := range sharedSnaps.list {
		if now := digestDir(s.dir); now != s.digest {
			return fmt.Errorf("the shared fixture %q (%s) was changed by a test that was to read it only; "+
				"a test that changes a fixture builds its own (shared_test.go)", s.name, s.dir)
		}
	}
	return nil
}

// digestDir is a digest of the tree below dir: paths, modes, file bytes, link targets. The
// modification times are left out on purpose: reading changes none, and a file written with
// the bytes it had is not a change a test could see.
func digestDir(dir string) string {
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(h, "%s: %v\n", p, err)
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		info, ierr := d.Info()
		if ierr != nil {
			fmt.Fprintf(h, "%s: %v\n", rel, ierr)
			return nil
		}
		fmt.Fprintf(h, "%s %o\n", rel, info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			fmt.Fprintf(h, "-> %s\n", target)
		case info.Mode().IsRegular():
			if f, oerr := os.Open(p); oerr == nil {
				io.Copy(h, f)
				f.Close()
			}
		}
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil))
}

// sharedDir is the directory of the shared fixture called name.
func sharedDir(t testing.TB, name string) string {
	t.Helper()
	sharedBase.once.Do(func() { sharedBase.dir, sharedBase.err = os.MkdirTemp("", "droidyard-git-shared") })
	if sharedBase.err != nil {
		t.Fatal(sharedBase.err)
	}
	return filepath.Join(sharedBase.dir, name)
}

// lazyFixture is a value built once, by the first test that asks. The builder runs with
// that test's t, so a failure fails that test; the others then fail saying why, and do not
// read a half-built repository. A test that needs git calls needGit first, so a host
// without git skips before anything is built.
type lazyFixture[T any] struct {
	name string
	once sync.Once
	v    T
	ok   bool
}

func (l *lazyFixture[T]) get(t testing.TB, build func(t testing.TB, dir string) T) T {
	t.Helper()
	l.once.Do(func() {
		dir := sharedDir(t, l.name)
		l.v = build(t, dir)
		sharedSnaps.Lock()
		sharedSnaps.list = append(sharedSnaps.list, sharedSnap{l.name, dir, digestDir(dir)})
		sharedSnaps.Unlock()
		l.ok = true
	})
	if !l.ok {
		t.Fatalf("the shared fixture %q failed to build in the test that built it", l.name)
	}
	return l.v
}

var (
	historyFx = lazyFixture[*gittest.Repo]{name: "hist"}
	syn5100Fx = lazyFixture[string]{name: "syn5100"}
	remoteFx  = lazyFixture[*gittest.Remotes]{name: "remote"}
)

// sharedHistory is gittest.History, read only: the one fixture of the history tests.
func sharedHistory(t testing.TB) *gittest.Repo {
	t.Helper()
	return historyFx.get(t, func(t testing.TB, dir string) *gittest.Repo { return gittest.History(t, filepath.Join(dir, "hist")) })
}

// sharedSynthetic is a gittest.Synthetic repository of 6000 commits and 5100 tags (every
// tenth annotated), read only: TestTipsCapped and TestRefsTipsEqualLogTips read it, and no
// test changes it.
func sharedSynthetic(t testing.TB) string {
	t.Helper()
	return syn5100Fx.get(t, func(t testing.TB, dir string) string {
		repo := filepath.Join(dir, "syn")
		gittest.Synthetic(t, repo, 6000, 0, 5100)
		return repo
	})
}

// sharedRemote is gittest.Remote, read only. Its hooks write markers into the fixture only
// when a write runs there, which a reader never starts.
func sharedRemote(t testing.TB) *gittest.Remotes {
	t.Helper()
	return remoteFx.get(t, func(t testing.TB, dir string) *gittest.Remotes { return gittest.Remote(t, filepath.Join(dir, "fx")) })
}

// newHistoryFx is a read fixture over the shared History repository, which its test only
// reads. The repository is not the test's own: it is never written, never touched.
func newHistoryFx(t *testing.T) (*readFx, *gittest.Repo) {
	t.Helper()
	e := needGit(t)
	r := sharedHistory(t)
	f := &readFx{env: e, platform: platform.Current()}
	f.at(r.Dir)
	return f, r
}

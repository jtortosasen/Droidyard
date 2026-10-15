package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/file"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// readFx is a registered git.read over a real repository, with a Starter that counts
// the gits it starts, so a test can say "none ran".
type readFx struct {
	*env
	root   string
	starts atomic.Int32
	// onStart, when set, sees every git before it starts (a test records its argv).
	onStart  func(*exec.Cmd)
	trusted  func(string) (bool, *capability.Error)
	look     func(string) (string, error)
	reg      *capability.Registry
	platform platform.Env
}

// newReadFx builds a repository (not yet holding a commit) and registers git.read on
// it; workspace "ws" is the repository's top unless the test calls at(dir).
func newReadFx(t *testing.T) *readFx {
	t.Helper()
	e := needGit(t)
	f := &readFx{env: e, platform: platform.Current()}
	f.at(e.repo())
	return f
}

// at points workspace "ws" at dir (canonicalised as workspace.open does) and registers.
func (f *readFx) at(dir string) {
	f.root = f.platform.Canonical(dir)
	f.trusted = func(string) (bool, *capability.Error) { return true, nil }
	f.register()
}

func (f *readFx) register() {
	f.reg = capability.NewRegistry()
	Register(f.reg, Options{
		Root:    func(string) (string, bool) { return f.root, true },
		Env:     f.platform,
		Environ: f.osEnviron,
		Trusted: func(root string) (bool, *capability.Error) { return f.trusted(root) },
		Starter: func(cmd *exec.Cmd) error {
			f.starts.Add(1)
			if f.onStart != nil {
				f.onStart(cmd)
			}
			return groupStarter(cmd)
		},
		LookPath: func(name string) (string, error) {
			if f.look != nil {
				return f.look(name)
			}
			return exec.LookPath(name)
		},
	})
}

func (f *readFx) read(p ReadParams) (ReadResult, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup("git.read")
	if !ok {
		f.t.Fatal("git.read is not registered")
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: p})
	if err != nil {
		return ReadResult{}, capability.AsError(err)
	}
	return res.(ReadResult), nil
}

func wantCode(t *testing.T, err *capability.Error, code capability.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got success", code)
	}
	if err.Code != code {
		t.Fatalf("want %s, got %s (%s; cause %q)", code, err.Code, err.Message, err.Cause)
	}
}

func text(r ReadResult) string {
	if r.Text == nil {
		return "<nil>"
	}
	return *r.Text
}

// Decision (ADR-0066 §1): git.read returns the bytes HEAD holds, whatever the working
// copy has become, byte for byte (a CRLF, a missing final newline, non-ASCII), from the
// repository's top and from a workspace that is a subfolder of it (the prefix is
// rev-parse's), and by every spelling the file domain accepts for a path inside the root.
func TestReadBaseText(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	want := "line one\r\nline two é\nlast without newline"
	f.write(repo, "a.txt", want)
	f.write(repo, "app/src/B.kt", "package b\n")
	f.write(repo, "empty.txt", "")
	f.commit(repo)
	f.write(repo, "a.txt", "the working copy changed\n")
	f.write(repo, "app/src/B.kt", "changed too\n")

	res, err := f.read(ReadParams{Path: "a.txt"})
	if err != nil {
		t.Fatalf("a.txt: %v", err)
	}
	if !res.Exists || res.Binary || res.TooLarge || res.Base != "HEAD" || res.Path != "a.txt" || res.Size != int64(len(want)) || text(res) != want {
		t.Errorf("a.txt = %+v (text %q), want HEAD's %q", res, text(res), want)
	}
	// An empty file is text "", present, not absent.
	if res, err := f.read(ReadParams{Path: "empty.txt", Base: "HEAD"}); err != nil || !res.Exists || res.Text == nil || *res.Text != "" {
		t.Errorf("empty.txt = %+v, %v; want exists with an empty text", res, err)
	}
	// Other spellings of an inside path.
	for _, path := range []string{"./a.txt", "app/../a.txt", filepath.Join(f.root, "a.txt")} {
		if res, err := f.read(ReadParams{Path: path}); err != nil || text(res) != want || res.Path != "a.txt" {
			t.Errorf("%q = %+v, %v; want HEAD's text at a.txt", path, res, err)
		}
	}

	// A workspace below the repository's top: the prefix is added, and the result's path
	// stays relative to the root.
	f.at(filepath.Join(repo, "app"))
	res, err = f.read(ReadParams{Path: "src/B.kt"})
	if err != nil || text(res) != "package b\n" || res.Path != "src/B.kt" {
		t.Errorf("from the subfolder workspace: %+v, %v; want HEAD's package b", res, err)
	}
	// ...and a file above the workspace is outside it, though inside the repository.
	_, err = f.read(ReadParams{Path: "../a.txt"})
	wantCode(t, err, capability.ErrFileOutsideRoot)
}

// Decision (ADR-0066 §1): a path HEAD does not hold is exists false, not an error: an
// untracked file, a path never committed, and a repository with no commit at all (an
// unborn HEAD), where the diff shows the whole file as added.
func TestReadAbsent(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "new.txt", "brand new\n")
	// Unborn HEAD: nothing committed yet.
	res, err := f.read(ReadParams{Path: "new.txt"})
	if err != nil || res.Exists || res.Text != nil || res.Size != 0 || res.Binary || res.TooLarge {
		t.Errorf("unborn HEAD: %+v, %v; want exists false", res, err)
	}
	f.write(repo, "old.txt", "old\n")
	f.g(repo, "add", "old.txt")
	f.g(repo, "commit", "-q", "-m", "c")
	for _, p := range []string{"new.txt", "nowhere/at/all.txt"} {
		res, err := f.read(ReadParams{Path: p})
		if err != nil || res.Exists || res.Text != nil {
			t.Errorf("%s: %+v, %v; want exists false", p, res, err)
		}
	}
	// A file staged but not committed is not in HEAD.
	f.g(repo, "add", "new.txt")
	if res, err := f.read(ReadParams{Path: "new.txt"}); err != nil || res.Exists {
		t.Errorf("staged only: %+v, %v; want exists false (the base is HEAD, not the index)", res, err)
	}
}

// Decision (ADR-0066 §1): a binary blob (a NUL byte, or invalid UTF-8) and a blob over
// 512 KiB are reported with their size and no text; 512 KiB itself is text. Binary is
// file.read's rule (the NUL byte and the invalid UTF-8 rows), shared and not copied.
func TestReadBinaryAndTooLarge(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "nul.bin", "ab\x00cd")
	f.write(repo, "latin1.txt", "caf\xe9\n")
	f.write(repo, "cut.txt", "ends in a rune \xc3") // a whole file that stops inside a rune
	f.write(repo, "limit.txt", strings.Repeat("x", file.EditableLimit))
	f.write(repo, "big.txt", strings.Repeat("x", file.EditableLimit+1))
	f.commit(repo)

	for _, c := range []struct {
		path   string
		size   int
		binary bool
		large  bool
		text   bool
	}{
		{"nul.bin", 5, true, false, false},
		{"latin1.txt", 5, true, false, false},
		{"cut.txt", 16, true, false, false},
		{"limit.txt", file.EditableLimit, false, false, true},
		{"big.txt", file.EditableLimit + 1, false, true, false},
	} {
		res, err := f.read(ReadParams{Path: c.path})
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if !res.Exists || res.Size != int64(c.size) || res.Binary != c.binary || res.TooLarge != c.large || (res.Text != nil) != c.text {
			t.Errorf("%s = exists %v size %d binary %v tooLarge %v text %v; want size %d binary %v tooLarge %v text %v",
				c.path, res.Exists, res.Size, res.Binary, res.TooLarge, res.Text != nil, c.size, c.binary, c.large, c.text)
		}
	}
	// The shared rule is file.read's: the same bytes, the same verdict.
	if !file.LooksBinary([]byte("ab\x00cd")) || file.LooksBinary([]byte("plain\n")) {
		t.Errorf("file.LooksBinary is not file.read's rule")
	}
}

// Decision (plan m2l Q1 §1; files.md §Paths and containment): containment is the file
// domain's lexical rule, applied before any git starts. Every spelling that leaves the
// root is FILE_OUTSIDE_ROOT with zero starts on the counting Starter, so no git ever saw
// the path; a NUL byte, an empty path and a line break (which cat-file would read as a
// second object name) are REQUEST_INVALID_PARAMS, also with zero starts.
func TestReadOutsideRoot(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		code capability.ErrorCode
	}{
		{"../x", capability.ErrFileOutsideRoot},
		{filepath.Join(outside, "x"), capability.ErrFileOutsideRoot},
		{"a/../../x", capability.ErrFileOutsideRoot},
		{"..", capability.ErrFileOutsideRoot},
		{"/etc/passwd", capability.ErrFileOutsideRoot},
		{"a\x00b", capability.ErrInvalidParams},
		{"", capability.ErrInvalidParams},
		{"a\nb", capability.ErrInvalidParams},
		{"a\r", capability.ErrInvalidParams},
	} {
		_, err := f.read(ReadParams{Path: c.path})
		if err == nil || err.Code != c.code {
			t.Errorf("%q: %v, want %s", c.path, err, c.code)
		}
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("%d git processes started for paths that were refused; want 0", n)
	}
	// Control: the counter counts, and a path inside the root does start git.
	if _, err := f.read(ReadParams{Path: "a.txt"}); err != nil || f.starts.Load() == 0 {
		t.Errorf("control: reading a.txt gave %v after %d starts; the zero above proves nothing", err, f.starts.Load())
	}
}

// Decision (ADR-0066 §4): HEAD holding a directory, or a submodule, where the path
// points is FILE_WRONG_TYPE, for a subfolder and for the root itself; the type is named.
func TestReadDirectoryIsWrongType(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "src/A.kt", "a\n")
	f.commit(repo)
	f.g(repo, "update-index", "--add", "--cacheinfo", "160000,"+strings.TrimSpace(f.g(repo, "rev-parse", "HEAD"))+",sub")
	f.g(repo, "commit", "-q", "-m", "gitlink")
	for path, typ := range map[string]string{"src": "directory", ".": "directory", "sub": "submodule"} {
		_, err := f.read(ReadParams{Path: path})
		wantCode(t, err, capability.ErrFileWrongType)
		if err.Details["type"] != typ {
			t.Errorf("%s: type %v, want %s", path, err.Details["type"], typ)
		}
	}
}

// Decision (ADR-0066 §1): base is an enum holding only HEAD. Another value, a branch
// name or a commit, is REQUEST_INVALID_PARAMS and starts nothing; absent means HEAD.
func TestReadOtherBaseRefused(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	for _, base := range []string{"main", "HEAD~1", "abc1234", "head"} {
		_, err := f.read(ReadParams{Path: "a.txt", Base: base})
		wantCode(t, err, capability.ErrInvalidParams)
	}
	if n := f.starts.Load(); n != 0 {
		t.Errorf("%d gits started for a refused base", n)
	}
}

// Decision (ADR-0066 §3 the path never reaches argv): a path that git would read as an
// option or a revision if it were an argument (a leading dash, a colon, a glob, a
// revision syntax) is read as the file it names, because it travels on stdin as part of
// the object name.
func TestReadPathNeverReachesArgv(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	names := []string{"--version", "-h", ":/x", "a*b", "HEAD~1", "@{u}", "sp ace.txt", "tab\there.txt", "é.txt"}
	for _, n := range names {
		f.write(repo, n, "content of "+n+"\n")
	}
	f.commit(repo)
	for _, n := range names {
		res, err := f.read(ReadParams{Path: n})
		if err != nil || !res.Exists || text(res) != "content of "+n+"\n" {
			t.Errorf("%q = %+v, %v; want its own text", n, res, err)
		}
	}
}

// Decision (ADR-0066 §4, capability-api.md §Error model): no git on the machine is
// GIT_NOT_FOUND, answered by looking afresh at every call (installing git needs no
// restart), before any process starts; a root in no repository is GIT_NOT_A_REPOSITORY.
func TestReadWithoutGitAndWithoutARepository(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	f.write(f.root, "a.txt", "a\n")
	f.commit(f.root)
	f.look = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err := f.read(ReadParams{Path: "a.txt"})
	wantCode(t, err, CodeNotFound)
	if err.NextStep == "" || f.starts.Load() != 0 {
		t.Errorf("GIT_NOT_FOUND: next step %q, %d starts", err.NextStep, f.starts.Load())
	}
	f.look = nil
	if res, err := f.read(ReadParams{Path: "a.txt"}); err != nil || text(res) != "a\n" {
		t.Errorf("git back on PATH: %+v, %v; want the text without a restart", res, err)
	}

	f.at(t.TempDir()) // no repository here
	_, err = f.read(ReadParams{Path: "a.txt"})
	wantCode(t, err, CodeNotARepository)
}

// Decision (ADR-0066 §4): a root inside the git directory is in no work tree. There
// `--show-prefix` prints "", so HEAD:<path> would name a file outside the root; it is
// GIT_NOT_A_REPOSITORY instead (the step 3 verifier's escape).
func TestReadFromInsideTheGitDirectoryIsNotARepository(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	f.write(repo, "top.txt", "outside the root\n")
	f.commit(repo)
	for _, dir := range []string{".git", ".git/refs"} {
		f.at(filepath.Join(repo, dir))
		res, err := f.read(ReadParams{Path: "top.txt"})
		if err == nil {
			t.Fatalf("root %s: read %+v, want GIT_NOT_A_REPOSITORY", dir, res)
		}
		wantCode(t, err, CodeNotARepository)
	}
}

// Decision (ADR-0066 §3, plan m2l §Changes step 1): on a root that is not trusted, a
// repository whose filter driver cannot be switched off (here the empty name, which git
// selects with `filter=`) refuses the read with WORKSPACE_UNTRUSTED, naming the human's
// action, and the driver does not run. A trusted root reads (cat-file runs no filter
// either way), and a trust check that fails closes to untrusted, not to trusted.
func TestReadUnsafeDriverOnAnUntrustedRoot(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	repo := f.root
	mark := filepath.Join(t.TempDir(), "mark")
	clean := f.script("clean.sh", "touch '"+mark+"'\ncat")
	f.write(repo, ".gitattributes", "c.txt filter=\n")
	f.write(repo, "c.txt", "one\n")
	f.commit(repo)
	f.appendConfig(repo, "[filter \"\"]\n\tclean = "+clean+"\n")

	f.trusted = func(string) (bool, *capability.Error) { return false, nil }
	_, err := f.read(ReadParams{Path: "c.txt"})
	wantCode(t, err, capability.ErrWorkspaceUntrusted)
	if !strings.Contains(err.NextStep, "droidyard trust") {
		t.Errorf("next step %q does not name the human action", err.NextStep)
	}
	if exists(mark) {
		t.Errorf("the untrusted root's driver ran")
	}

	f.trusted = func(string) (bool, *capability.Error) {
		return true, capability.NewError(capability.ErrWorkspaceUntrusted, "the configuration could not be read")
	}
	_, err = f.read(ReadParams{Path: "c.txt"})
	wantCode(t, err, capability.ErrWorkspaceUntrusted)

	f.trusted = func(string) (bool, *capability.Error) { return true, nil }
	if res, err := f.read(ReadParams{Path: "c.txt"}); err != nil || text(res) != "one\n" {
		t.Errorf("trusted: %+v, %v; want HEAD's text", res, err)
	}
	if exists(mark) {
		t.Errorf("cat-file ran a clean filter")
	}
}

// Decision (ADR-0066 §4): each failure of the runner has one code: no git, no
// repository, a git that failed (its first stderr line is the cause), a timeout and a git
// that could not be started (GIT_FAILED), an unsafe driver (WORKSPACE_UNTRUSTED); a
// cancelled call is not turned into a git error.
func TestMapError(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err   error
		code  capability.ErrorCode
		cause string
	}{
		{ErrNotFound, CodeNotFound, ""},
		{ErrNotARepository, CodeNotARepository, ""},
		{fmt.Errorf("%w: boom", ErrGitFailed), CodeFailed, "boom"},
		{ErrTimeout, CodeFailed, ErrTimeout.Error()},
		{ErrNoStarter, CodeFailed, ErrNoStarter.Error()},
		{ErrUnsafeDriver, capability.ErrWorkspaceUntrusted, ErrUnsafeDriver.Error()},
	} {
		got := capability.AsError(mapError("/p", c.err))
		if got.Code != c.code || !strings.Contains(got.Cause, c.cause) {
			t.Errorf("%v -> %s (cause %q); want %s containing %q", c.err, got.Code, got.Cause, c.code, c.cause)
		}
	}
	failed := capability.AsError(mapError("/p", gitError(result{Stderr: []byte("fatal: bad thing\nmore\n"), ExitCode: 128})))
	if failed.Code != CodeFailed || failed.Cause != "fatal: bad thing" {
		t.Errorf("a git failure -> %s, cause %q; want GIT_FAILED with git's first stderr line", failed.Code, failed.Cause)
	}
	if err := mapError("/p", context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled call became %v", err)
	}
}

// Decision (ADR-0066 §1, ADR-0060 §2, ADR-0020 §Retention): git.read is a stateless
// workspace query for the human UI (audience human: the core refuses it over MCP), that
// needs git, and a retained result drops the text. Reversing the audience would list it
// as an agent tool.
func TestReadDescriptor(t *testing.T) {
	t.Parallel()
	f := newReadFx(t)
	d, ok := f.reg.Lookup("git.read")
	if !ok {
		t.Fatal("git.read is not registered")
	}
	if d.Kind != capability.Query || d.Scope != capability.Workspace || d.Statefulness != capability.Stateless || d.Audience != capability.AudienceHuman || d.Since != "0.23.0" {
		t.Errorf("kind %v scope %v statefulness %v audience %v since %s", d.Kind, d.Scope, d.Statefulness, d.Audience, d.Since)
	}
	if len(d.Availability) != 1 || d.Availability[0] != capability.RequiresGit {
		t.Errorf("availability %v; want git", d.Availability)
	}
	s := "text"
	if got := d.Slim(ReadResult{Text: &s, Exists: true}).(ReadResult); got.Text != nil || !got.Exists {
		t.Errorf("slim = %+v; want the text dropped and the rest kept", got)
	}
}

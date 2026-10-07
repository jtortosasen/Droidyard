package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// fixture is a real project root with the planted escape symlinks of the plan's check,
// the registered descriptors and the resolver they run with. The root is canonicalised
// exactly as workspace.open canonicalises one, so the macOS /var → /private/var alias
// is exercised for real (plan m1j step 1).
type fixture struct {
	t    *testing.T
	base string // the raw temp dir, the /var/… spelling
	root string // the canonical root, the /private/var/… spelling
	env  platform.Env
	reg  *capability.Registry
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	rootDir := filepath.Join(base, "root")
	if err := os.Mkdir(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("SECRET-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := platform.Current()
	root := env.Canonical(rootDir)

	f := &fixture{t: t, base: base, root: root, env: env}
	f.write("src/A.kt", "package dev.droidyard\n\nclass A\n")
	// The escape rows of the check (S3: only in a scratch copy).
	f.symlink("/etc", "esc")
	f.symlink("../outside/secret", "rel")
	f.symlink("../outside", "dir")
	f.symlink("../outside/missing", "gone")
	f.symlink(filepath.Join(root, "src", "A.kt"), "abs")
	f.symlink("src", "in")

	f.reg = capability.NewRegistry()
	Register(f.reg, Options{
		Root: func(string) (string, bool) { return root, true },
		Env:  env,
	})
	return f
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) writeBytes(rel string, data []byte) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) symlink(target, rel string) {
	f.t.Helper()
	if err := os.Symlink(target, filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) call(name string, params any) (any, *capability.Error) {
	f.t.Helper()
	d, ok := f.reg.Lookup(name)
	if !ok {
		f.t.Fatalf("%s is not registered", name)
	}
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: params})
	return res, capability.AsError(err)
}

func (f *fixture) read(p ReadParams) (ReadResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("file.read", p)
	if err != nil {
		return ReadResult{}, err
	}
	return res.(ReadResult), nil
}

func (f *fixture) list(p ListParams) (ListResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("file.list", p)
	if err != nil {
		return ListResult{}, err
	}
	return res.(ListResult), nil
}

// textOrDash renders a read's text pointer for a failure message.
func textOrDash(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func wantCode(t *testing.T, err *capability.Error, code capability.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got success", code)
	}
	if err.Code != code {
		t.Fatalf("want %s, got %s (%s)", code, err.Code, err.Message)
	}
}

// Decision (D2, D5): a root-relative path and every inside spelling of it read the same
// text — the plain name, "./src/../src/A.kt", the absolute canonical path, and the
// /var/… alias of that absolute path (which resolves to the same root through
// Env.Canonical).
func TestReadResolvesInsideSpellings(t *testing.T) {
	f := newFixture(t)
	want := "package dev.droidyard\n\nclass A\n"
	canonicalAbs := filepath.Join(f.root, "src", "A.kt")
	aliasAbs := filepath.Join(f.base, "root", "src", "A.kt")
	if canonicalAbs == aliasAbs {
		t.Fatalf("the temp path is not under a symlinked /var; the alias row cannot run")
	}
	for _, path := range []string{"src/A.kt", "./src/../src/A.kt", canonicalAbs, aliasAbs} {
		res, err := f.read(ReadParams{Path: path})
		if err != nil {
			t.Fatalf("%q: %s", path, err.Message)
		}
		if res.Text == nil || *res.Text != want {
			t.Errorf("%q: text %q, want %q", path, textOrDash(res.Text), want)
		}
	}
}

// Decision (D2): a relative symlink inside the root is followed for a read — "in -> src"
// makes "in/A.kt" the same file as "src/A.kt".
func TestReadFollowsAnInRootRelativeSymlink(t *testing.T) {
	f := newFixture(t)
	res, err := f.read(ReadParams{Path: "in/A.kt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text == nil || !strings.Contains(*res.Text, "class A") {
		t.Fatalf("reading through an in-root symlink gave %q", textOrDash(res.Text))
	}
}

// Decision (D2): every spelling that would leave the root is FILE_OUTSIDE_ROOT and
// reads nothing outside it — "../x", an absolute path outside, an absolute symlink
// (esc), a relative symlink that climbs out (rel, dir), a dangling one (gone), and a
// symlink whose target is the inside file named by an absolute path (abs), named
// relatively and by its own absolute path. No result carries a byte of the outside
// secret.
func TestReadRefusesEveryEscape(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{
		"../x",
		"/etc/hosts",
		"esc/hosts",
		"rel",
		"dir/secret",
		"gone",
		"abs",
		filepath.Join(f.root, "abs"),
		// The /var/… alias reaches resolveAbsolute's second branch: only the directory
		// is canonicalised, so the final link still reaches os.Root and is refused.
		filepath.Join(f.base, "root", "abs"),
		filepath.Join(f.base, "root", "rel"),
	} {
		res, err := f.read(ReadParams{Path: path})
		wantCode(t, err, capability.ErrFileOutsideRoot)
		if res.Text != nil && strings.Contains(*res.Text, "SECRET") {
			t.Errorf("%q leaked the outside file: %q", path, *res.Text)
		}
	}
}

// Decision (D2): an os.Root refusal is recognised by the escape sentinel's text,
// because the standard library's error is unexported. This pins that text from a real
// refusal, so a Go release that rewords it fails here rather than turning an escape
// into FILE_IO_FAILED.
func TestEscapeErrorTextIsPinned(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Stat("esc/x")
	if err == nil {
		t.Fatal("want an escape refusal, got success")
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("the refusal is %T, not a *PathError", err)
	}
	if pe.Err.Error() != "path escapes from parent" {
		t.Fatalf("the escape text is %q; the pinned constant is %q", pe.Err.Error(), escapeText)
	}
	if !isEscape(err) {
		t.Fatal("isEscape does not recognise a real os.Root escape")
	}
}

// Decision (D2): on a case-insensitive volume a differently-cased inside path reads the
// file and a differently-cased absolute symlink still escapes. Skipped where the volume
// is case-sensitive.
func TestReadCaseInsensitiveVolume(t *testing.T) {
	f := newFixture(t)
	if !caseInsensitive(f.root) {
		t.Skip("the volume is case-sensitive")
	}
	res, err := f.read(ReadParams{Path: "SRC/A.kt"})
	if err != nil {
		t.Fatalf("SRC/A.kt: %s", err.Message)
	}
	if res.Text == nil || !strings.Contains(*res.Text, "class A") {
		t.Fatalf("SRC/A.kt gave %q", textOrDash(res.Text))
	}
	_, err = f.read(ReadParams{Path: "ESC/hosts"})
	wantCode(t, err, capability.ErrFileOutsideRoot)
}

// Decision (D5): a directory, a FIFO and a missing file are told apart from a regular
// file: FILE_WRONG_TYPE for the first two, FILE_NOT_FOUND for the last. The FIFO is
// refused at once, not blocked on, which this test's 1 s deadline enforces.
func TestReadRefusesNonFiles(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "x")

	_, err := f.read(ReadParams{Path: "src"})
	wantCode(t, err, capability.ErrFileWrongType)

	_, err = f.read(ReadParams{Path: "nope.txt"})
	wantCode(t, err, capability.ErrFileNotFound)

	if err := syscall.Mkfifo(filepath.Join(f.root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan *capability.Error, 1)
	go func() {
		_, err := f.read(ReadParams{Path: "pipe"})
		done <- err
	}()
	select {
	case err := <-done:
		wantCode(t, err, capability.ErrFileWrongType)
	case <-time.After(1 * time.Second):
		t.Fatal("reading a FIFO blocked; want FILE_WRONG_TYPE at once")
	}
}

// Decision (D3): a file the process may not read is FILE_IO_FAILED with the OS error as
// its cause. Skipped as root, where no permission denies the open.
func TestReadUnreadableFileIsIOFailed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions do not deny the open")
	}
	f := newFixture(t)
	f.write("locked.txt", "hidden")
	if err := os.Chmod(filepath.Join(f.root, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(f.root, "locked.txt"), 0o644) })
	_, err := f.read(ReadParams{Path: "locked.txt"})
	wantCode(t, err, capability.ErrFileIOFailed)
	if err.Cause == "" {
		t.Error("FILE_IO_FAILED must carry the OS error as its cause")
	}
}

// Decision (D2): a NUL byte, and "" where a file is required, are REQUEST_INVALID_PARAMS
// before any filesystem call.
func TestReadInvalidPaths(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"", "src/\x00A.kt"} {
		_, err := f.read(ReadParams{Path: path})
		wantCode(t, err, capability.ErrInvalidParams)
	}
}

// Decision (D5, ADR-0012 §Reading artifacts): file.read pages exactly as artifact.read:
// text ends at the last complete line, a negative offset lands on the next line start,
// a line longer than the limit is cut at a UTF-8 boundary with truncated, and an offset
// at or past the end is empty text.
func TestReadPagesLikeArtifactRead(t *testing.T) {
	f := newFixture(t)
	f.write("lines.txt", "aaa\nbbb\nccc\n")

	res, err := f.read(ReadParams{Path: "lines.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text == nil || *res.Text != "aaa\nbbb\nccc\n" || res.NextOffset != 12 || res.Truncated {
		t.Fatalf("whole file: %+v", res)
	}

	neg := int64(-6) // lands inside "ccc": moved forward to the line start at 8
	res, err = f.read(ReadParams{Path: "lines.txt", Offset: &neg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Offset != 8 || res.Text == nil || *res.Text != "ccc\n" {
		t.Fatalf("negative offset: %+v", res)
	}

	// A 300 KiB line is longer than the default limit: it is cut at a UTF-8 boundary
	// (the default limit here), marked truncated, and the next read continues it.
	f.write("long.txt", strings.Repeat("x", 300<<10))
	res, err = f.read(ReadParams{Path: "long.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.Text == nil || len(*res.Text) != artifact.DefaultTextLimit || res.NextOffset != int64(artifact.DefaultTextLimit) {
		t.Fatalf("long line: %+v", res)
	}

	past := int64(100)
	res, err = f.read(ReadParams{Path: "lines.txt", Offset: &past})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text == nil || *res.Text != "" || res.Offset != 12 || res.NextOffset != 12 {
		t.Fatalf("past the end: %+v", res)
	}

	// Non-ASCII text near the end: the negative offset lands inside "é" and moves to the
	// next line start; the page there is text, not binary (the binary check reads the
	// page returned, not the raw window).
	f.write("accents.txt", "x\nééé\nok\n")
	neg = -7
	res, err = f.read(ReadParams{Path: "accents.txt", Offset: &neg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Binary || res.Offset != 9 || res.Text == nil || *res.Text != "ok\n" {
		t.Fatalf("negative offset inside a rune: %+v text %q", res, textOrDash(res.Text))
	}

	bad := 3 // below the published minimum
	_, err = f.read(ReadParams{Path: "lines.txt", Limit: &bad})
	wantCode(t, err, capability.ErrInvalidParams)
}

// Decision (D5): a file whose window holds a NUL byte or invalid UTF-8 is binary: text
// is omitted and binary is true, while a window that merely ends mid-rune is not binary.
func TestReadBinaryDetection(t *testing.T) {
	f := newFixture(t)
	// A PNG header: the signature's 0x89 is invalid UTF-8 and the IHDR that follows
	// holds NUL bytes.
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x01\x00")
	f.writeBytes("image.png", png)
	res, err := f.read(ReadParams{Path: "image.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Binary || res.Text != nil {
		t.Fatalf("a PNG header must be binary with no text: %+v", res)
	}
	if res.Size != int64(len(png)) {
		t.Errorf("binary read kept size %d, want %d", res.Size, len(png))
	}

	// "ééé" is six bytes; a five-byte window ends inside the third rune.
	f.write("runes.txt", "ééé")
	limit := 5
	res, err = f.read(ReadParams{Path: "runes.txt", Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if res.Binary {
		t.Fatalf("a window ending mid-rune is not binary: %+v", res)
	}
	if res.Text == nil || *res.Text != "éé" {
		t.Fatalf("mid-rune window gave %q", textOrDash(res.Text))
	}

	// A NUL past the raw window but inside the page returned (the negative offset moved
	// forward to a line start) makes that page binary: no NUL ever reaches text.
	f.write("late-nul.txt", "zzzzzzzz\ncc\x00\n")
	neg, limit8 := int64(-11), 8
	res, err = f.read(ReadParams{Path: "late-nul.txt", Offset: &neg, Limit: &limit8})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Binary || res.Text != nil || res.Offset != 9 || res.NextOffset != 13 {
		t.Fatalf("a NUL in the returned page must make it binary: %+v text %q", res, textOrDash(res.Text))
	}
}

// Decision (D5, ADR-0020 §Retention): the first delivery carries the text; a later
// operation.wait returns the same result with text dropped (Slim).
func TestReadSlimsAfterDelivery(t *testing.T) {
	f := newFixture(t)
	reg := capability.NewRegistry()
	Register(reg, Options{Root: func(string) (string, bool) { return f.root, true }, Env: f.env})
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admit{})
	ops.RegisterCapabilities()

	params, _ := json.Marshal(ReadParams{Path: "src/A.kt"})
	id, err := ops.Start(context.Background(), capability.Request{Capability: "file.read", Workspace: "ws", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	first := out.Result.(ReadResult)
	if first.Text == nil || !strings.Contains(*first.Text, "class A") {
		t.Fatalf("first delivery dropped text: %+v", first)
	}
	out, err = ops.Wait(context.Background(), id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second := out.Result.(ReadResult)
	if second.Text != nil {
		t.Fatalf("a retained read still carries text: %q", *second.Text)
	}
	if second.Size != first.Size {
		t.Errorf("Slim dropped size too: %d, want %d", second.Size, first.Size)
	}
}

// admit is a workspace registry that admits every target; the file tests never open one.
type admit struct{}

func (admit) Admit(target string, record func(string)) bool { record(target); return true }

// Decision (D4): file.list returns one level, sorted by name, with each entry's type
// (a symlink is reported, never followed); a limit smaller than the directory marks the
// result truncated and gives the narrowing hint; an empty directory is entries: []; a
// file is FILE_WRONG_TYPE.
func TestListOneLevelSortedTyped(t *testing.T) {
	f := newFixture(t)
	f.write("ls/b.txt", "b")
	f.write("ls/a.txt", "a")
	if err := os.Mkdir(filepath.Join(f.root, "ls", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.symlink("a.txt", filepath.Join("ls", "link"))
	if err := os.Mkdir(filepath.Join(f.root, "ls", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := f.list(ListParams{Path: "ls"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	types := map[string]string{}
	for _, e := range res.Entries {
		names = append(names, e.Name)
		types[e.Name] = e.Type
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entries are not sorted: %v", names)
	}
	if types["a.txt"] != "file" || types["sub"] != "dir" || types["link"] != "symlink" {
		t.Errorf("entry types wrong: %v", types)
	}
	if res.Truncated {
		t.Error("four entries with the default limit must not be truncated")
	}

	limit := 2
	res, err = f.list(ListParams{Path: "ls", Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 || !res.Truncated || res.Hint == "" {
		t.Fatalf("limit 2: %+v", res)
	}

	res, err = f.list(ListParams{Path: "ls/empty"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries == nil || len(res.Entries) != 0 {
		t.Fatalf("an empty directory lists entries: [] not nil: %+v", res.Entries)
	}

	_, err = f.list(ListParams{Path: "ls/a.txt"})
	wantCode(t, err, capability.ErrFileWrongType)
	if err.Details["type"] != "regular file" || !strings.Contains(err.Message, "not a directory") {
		t.Errorf("listing a file: %q, type %v", err.Message, err.Details["type"])
	}

	_, err = f.list(ListParams{Path: "nope"})
	wantCode(t, err, capability.ErrFileNotFound)

	_, err = f.list(ListParams{Path: "../x"})
	wantCode(t, err, capability.ErrFileOutsideRoot)
}

// Decision (R3, ADR-0015 §1): file.* runs with no trust check and its results carry no
// untrusted mark — not in the descriptor's derived flag, and not in the published
// schema under file.*. Options has no trust hook, so a root's trust is invisible here:
// an untrusted workspace lists and reads exactly as a trusted one, and the outcome is
// never marked (which is why the level-4 fixture copy can be untrusted).
func TestFileResultsAreUnmarked(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"file.list", "file.read"} {
		d, ok := f.reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if d.Untrusted() {
			t.Errorf("%s is marked untrusted; project content is not (ADR-0015 §1)", name)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "capabilities.json"))
	if err != nil {
		t.Fatalf("reading the golden schema: %v", err)
	}
	var doc struct {
		Capabilities []map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, c := range doc.Capabilities {
		name, _ := c["name"].(string)
		if !strings.HasPrefix(name, "file.") {
			continue
		}
		found++
		b, _ := json.Marshal(c)
		if bytes.Contains(b, []byte("x-droidyard-untrusted")) {
			t.Errorf("%s carries an untrusted field in the published schema", name)
		}
	}
	if found == 0 {
		t.Fatal("the golden schema holds no file.* capability; regenerate it")
	}
}

// caseInsensitive reports whether dir ignores case, by creating a file and looking it
// up in another case.
func caseInsensitive(dir string) bool {
	probe := filepath.Join(dir, "caseProbe.tmp")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return false
	}
	defer os.Remove(probe)
	_, err := os.Stat(filepath.Join(dir, "CASEPROBE.TMP"))
	return err == nil
}

package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type doc struct {
	Root string `json:"root"`
	N    int    `json:"n"`
}

// recorder is the real filesystem, with each durability call recorded in order:
// "sync <path>" or "rename <old> <new>", paths relative to the root, and any error.
type recorder struct {
	root  string
	calls []string
	errs  []error
}

func (r *recorder) rel(p string) string {
	rel, err := filepath.Rel(r.root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func (r *recorder) Sync(f *os.File) error {
	err := f.Sync()
	r.calls = append(r.calls, "sync "+r.rel(f.Name()))
	r.errs = append(r.errs, err)
	return err
}

func (r *recorder) Rename(oldpath, newpath string) error {
	err := os.Rename(oldpath, newpath)
	r.calls = append(r.calls, "rename "+r.rel(oldpath)+" "+r.rel(newpath))
	r.errs = append(r.errs, err)
	return err
}

// newStore is a store on a real temporary directory (APFS on macOS), seen through a
// recorder.
func newStore(t *testing.T) (*Store, *recorder) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{root: root}
	s.fs = rec
	return s, rec
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tmpFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".tmp") {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// TestWriteIsAtomicAndVersioned: a write leaves no *.tmp behind, its file parses with
// schemaVersion 1, the file is 0600 and its new directories 0700 (ADR-0021 §Format).
func TestWriteIsAtomicAndVersioned(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Write("ws/a-123/workspace.json", doc{Root: "/p", N: 3}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "ws", "a-123", "workspace.json")
	var got struct {
		SchemaVersion int `json:"schemaVersion"`
		doc
	}
	if err := json.Unmarshal(readFile(t, path), &got); err != nil {
		t.Fatalf("written file does not parse: %v", err)
	}
	if got.SchemaVersion != 1 || got.Root != "/p" || got.N != 3 {
		t.Errorf("written file = %+v, want schemaVersion 1, root /p, n 3", got)
	}
	if tmp := tmpFiles(t, s.Root()); len(tmp) != 0 {
		t.Errorf("a write left %v behind", tmp)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	for _, d := range []string{"ws", "ws/a-123"} {
		if fi, _ := os.Stat(filepath.Join(s.Root(), d)); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode %v, want 0700", d, fi.Mode().Perm())
		}
	}
	var back doc
	if out, err := s.Read("ws/a-123/workspace.json", 1, &back); out != Loaded || err != nil || back != (doc{Root: "/p", N: 3}) {
		t.Errorf("Read = %v, %v, %+v; want loaded, the value written", out, err, back)
	}
}

// TestWriteSyncsFileThenRenamesThenSyncsDirectory: the order that makes the rename
// durable (ADR-0021 §Format: "write *.tmp, fsync, rename, fsync the directory"), and
// the directory fsync succeeds on this filesystem (APFS on macOS, where Go's Sync is
// F_FULLFSYNC).
func TestWriteSyncsFileThenRenamesThenSyncsDirectory(t *testing.T) {
	s, rec := newStore(t)
	if err := s.Write("ws/a/workspace.json", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sync ws/a/workspace.json.tmp",
		"rename ws/a/workspace.json.tmp ws/a/workspace.json",
		"sync ws/a",
	}
	if strings.Join(rec.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(rec.calls, "\n"), strings.Join(want, "\n"))
	}
	for i, err := range rec.errs {
		if err != nil {
			t.Errorf("%s: %v", rec.calls[i], err)
		}
	}
}

// TestFailedRenameKeepsPreviousContent: when the rename fails (the target is a
// non-empty directory) the write fails, what was there is intact, and no *.tmp is
// left.
func TestFailedRenameKeepsPreviousContent(t *testing.T) {
	s, rec := newStore(t)
	target := filepath.Join(s.Root(), "processes.json")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(target, "keep")
	if err := os.WriteFile(inner, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("processes.json", doc{N: 2}); err == nil {
		t.Fatal("Write over a non-empty directory succeeded")
	}
	if len(rec.calls) < 2 || !strings.HasPrefix(rec.calls[1], "rename ") || rec.errs[1] == nil {
		t.Fatalf("the failure was not the rename: %v %v", rec.calls, rec.errs)
	}
	if got := readFile(t, inner); string(got) != "previous" {
		t.Errorf("previous content = %q", got)
	}
	if tmp := tmpFiles(t, s.Root()); len(tmp) != 0 {
		t.Errorf("a failed write left %v behind", tmp)
	}
}

// TestCorruptFileIsMovedAsideAndReadAsAbsent: a file that does not parse is renamed
// *.corrupt-<timestamp> with its bytes intact, treated as absent, and listed as a
// problem; corrupt state never stops the daemon (ADR-0021 §Format).
func TestCorruptFileIsMovedAsideAndReadAsAbsent(t *testing.T) {
	for name, garbage := range map[string][]byte{
		"not json":          []byte("\x00\xffnot json{"),
		"no schemaVersion":  []byte(`{"root":"/p"}`),
		"schemaVersion 0":   []byte(`{"schemaVersion":0}`),
		"not an object":     []byte(`[1,2]`),
		"fields wrong type": []byte(`{"schemaVersion":1,"root":"/half","n":"three"}`),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t)
			path := filepath.Join(s.Root(), "workspaces.json")
			if err := os.WriteFile(path, garbage, 0o600); err != nil {
				t.Fatal(err)
			}
			v := doc{Root: "untouched"}
			out, err := s.Read("workspaces.json", 1, &v)
			if out != Corrupt || err != nil {
				t.Fatalf("Read = %v, %v; want corrupt, nil", out, err)
			}
			if v.Root != "untouched" {
				t.Errorf("a corrupt file changed v: %+v", v)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the corrupt file is still in place: %v", err)
			}
			probs := s.Problems()
			if len(probs) != 1 || probs[0].File != "workspaces.json" || probs[0].Problem != ProblemCorrupt ||
				!strings.HasPrefix(probs[0].MovedTo, "workspaces.json.corrupt-") {
				t.Fatalf("problems = %+v", probs)
			}
			if moved := readFile(t, filepath.Join(s.Root(), probs[0].MovedTo)); !bytes.Equal(moved, garbage) {
				t.Errorf("moved bytes = %q, want %q", moved, garbage)
			}
			if out, err := s.Read("workspaces.json", 1, &v); out != Absent || err != nil {
				t.Errorf("after the move, Read = %v, %v; want absent", out, err)
			}
		})
	}
}

// TestNewerFileIsNeverOverwritten: schemaVersion above current reads as newer, its
// bytes unchanged; a later Write is refused with ErrNewer and changes nothing; the
// problem is listed once (ADR-0021 §Format; ADR-0026).
func TestNewerFileIsNeverOverwritten(t *testing.T) {
	s, rec := newStore(t)
	path := filepath.Join(s.Root(), "ws", "a", "workspace.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	newer := []byte(`{"schemaVersion":2,"root":"/p","future":true}`)
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)

	var v doc
	if out, err := s.Read("ws/a/workspace.json", 1, &v); out != Newer || !errors.Is(err, ErrNewer) {
		t.Fatalf("Read = %v, %v; want newer, ErrNewer", out, err)
	}
	if v != (doc{}) {
		t.Errorf("a newer file was decoded into v: %+v", v)
	}
	if err := s.Write("ws/a/workspace.json", doc{N: 9}); !errors.Is(err, ErrNewer) {
		t.Fatalf("Write = %v; want ErrNewer", err)
	}
	s.Read("ws/a/workspace.json", 1, &v)

	after, _ := os.Stat(path)
	if got := readFile(t, path); !bytes.Equal(got, newer) || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the newer file changed: %q, mtime %v → %v", got, before.ModTime(), after.ModTime())
	}
	if len(rec.calls) != 0 || len(tmpFiles(t, s.Root())) != 0 {
		t.Errorf("a refused write touched the disk: %v", rec.calls)
	}
	want := []Problem{{File: "ws/a/workspace.json", Problem: ProblemNewerVersion}}
	if got := s.Problems(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("problems = %+v, want %+v", got, want)
	}
}

// TestWriteRefusesNewerFileItWasNeverAskedToRead: "never overwritten" does not depend
// on the caller having read the file first.
func TestWriteRefusesNewerFileItWasNeverAskedToRead(t *testing.T) {
	s, _ := newStore(t)
	path := filepath.Join(s.Root(), "processes.json")
	newer := []byte(`{"schemaVersion":3,"processes":[]}`)
	os.WriteFile(path, newer, 0o600)
	if err := s.Write("processes.json", doc{}); !errors.Is(err, ErrNewer) {
		t.Fatalf("Write = %v; want ErrNewer", err)
	}
	if got := readFile(t, path); !bytes.Equal(got, newer) {
		t.Errorf("the newer file changed: %q", got)
	}
	if p := s.Problems(); len(p) != 1 || p[0].Problem != ProblemNewerVersion {
		t.Errorf("problems = %+v", p)
	}
}

// TestStaleTmpIsIgnoredAndReplaced: a *.tmp left by an interrupted write is never
// read, and the next Write replaces it (ADR-0021 §Format: the rename is the commit).
func TestStaleTmpIsIgnoredAndReplaced(t *testing.T) {
	s, _ := newStore(t)
	path := filepath.Join(s.Root(), "workspaces.json")
	if err := os.WriteFile(path+".tmp", []byte(`{"schemaVersion":1,"n":99`), 0o644); err != nil {
		t.Fatal(err)
	}
	var v doc
	if out, err := s.Read("workspaces.json", 1, &v); out != Absent || err != nil {
		t.Fatalf("with only a stale .tmp, Read = %v, %v; want absent", out, err)
	}
	if err := s.Write("workspaces.json", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte(`{"schemaVersion":1,"n":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := s.Read("workspaces.json", 1, &v); out != Loaded || err != nil || v.N != 1 {
		t.Fatalf("beside a stale .tmp, Read = %v, %v, %+v; want the committed file", out, err, v)
	}
	if err := s.Write("workspaces.json", doc{N: 2}); err != nil {
		t.Fatalf("Write beside a stale .tmp: %v", err)
	}
	if tmp := tmpFiles(t, s.Root()); len(tmp) != 0 {
		t.Errorf("the stale .tmp survived: %v", tmp)
	}
	if s.Read("workspaces.json", 1, &v); v.N != 2 {
		t.Errorf("after the write, n = %d, want 2", v.N)
	}
	if len(s.Problems()) != 0 {
		t.Errorf("a stale .tmp was reported: %+v", s.Problems())
	}
}

// TestNothingOutsideTheRoot: the store writes only under the state root it is given,
// never inside a project tree (ADR-0021 §Location).
func TestNothingOutsideTheRoot(t *testing.T) {
	outside := t.TempDir()
	s, rec := newStore(t)
	escape, _ := filepath.Rel(s.Root(), filepath.Join(outside, "x.json"))
	for _, name := range []string{escape, filepath.Join(outside, "x.json"), "", ".", "ws/../../x.json"} {
		if err := s.Write(name, doc{}); err == nil {
			t.Errorf("Write(%q) was accepted", name)
		}
		var v doc
		if _, err := s.Read(name, 1, &v); err == nil {
			t.Errorf("Read(%q) was accepted", name)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("written outside the root: %v", entries)
	}
	if len(rec.calls) != 0 {
		t.Errorf("a refused name reached the disk: %v", rec.calls)
	}
	if _, err := New("relative/state"); err == nil {
		t.Error("a relative state root was accepted")
	}
}

// TestWriteOwnsSchemaVersion: the store stamps the version; a value must be an object
// and may not carry its own.
func TestWriteOwnsSchemaVersion(t *testing.T) {
	s, _ := newStore(t)
	for name, v := range map[string]any{
		"own schemaVersion": map[string]any{"schemaVersion": 7},
		"not an object":     []int{1},
		"null":              (*doc)(nil),
	} {
		if err := s.Write("x.json", v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := s.Write("x.json", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(s.Root(), "x.json")); string(got) != "{\n  \"schemaVersion\": 1\n}\n" {
		t.Errorf("empty object written as %q", got)
	}
}

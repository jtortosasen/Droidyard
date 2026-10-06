package artifact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// newDurableStore is a store over a fresh state root, for the durable class.
func newDurableStore(t *testing.T, root string) *Store {
	t.Helper()
	s, err := New(Options{Epoch: "d-0a1b2c3d", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// Decision (D13, ADR-0012 §Durability classes): a durable artifact is written
// incrementally and reads `growing` until the producer closes it; after that it is an
// ordinary file. The class encodes the workspace and the owner, never a path.
func TestDurableAppendsReadWhileGrowingThenClosed(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateDurable("ws-1", "op-1", DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if ref := d.Ref(); ref.Durability != DurabilityDurable || ref.Ref != RefPrefix+"d.ws-1.op-1" || !ref.ExpiresAt.IsZero() {
		t.Fatalf("ref = %+v, want a durable d.ws-1.op-1 with no expiry", ref)
	}
	for _, line := range []string{"first\n", "second\n", "third\n"} {
		if _, err := d.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	const content = "first\nsecond\nthird\n"
	e, err := s.Get(d.Ref().Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Growing || e.Size != int64(len(content)) || e.Data != nil {
		t.Fatalf("while growing: growing=%v size=%d data=%d bytes, want growing, %d bytes and no in-memory copy", e.Growing, e.Size, len(e.Data), len(content))
	}
	page, err := ReadText(e, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Text != "first\nsecond\nthird\n" || !page.Growing || page.NextOffset != 19 {
		t.Fatalf("page = %+v", page)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(d.Ref().Ref)
	if err != nil {
		t.Fatal(err)
	}
	if e.Growing || e.Size != 19 || e.Ref.MediaType != "text/plain" || e.Ref.Untrusted {
		t.Fatalf("after close: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(root, "ws", "ws-1", "artifacts", "op-1.log")); err != nil {
		t.Fatalf("the console is not where ADR-0021 puts it: %v", err)
	}
}

// Decision (D13): a well-formed durable reference resolves in a new store over the
// same state, without the workspace being open: the file is the state, not a map.
func TestDurableRefResolvesInANewStoreAfterARestart(t *testing.T) {
	root := t.TempDir()
	first := newDurableStore(t, root)
	d, err := first.CreateDurable("ws-1", "op-1", DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	content := "> Task :app:compileDebugKotlin\nBUILD SUCCESSFUL in 42s\n"
	if _, err := d.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	ref := d.Ref().Ref

	second := newDurableStore(t, root)
	e, err := second.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	if e.Growing || e.Size != int64(len(content)) || e.Ref.MediaType != "text/plain" || e.Ref.Durability != DurabilityDurable {
		t.Fatalf("after restart: %+v", e)
	}
	page, err := ReadText(e, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Text != content || page.Size != int64(len(content)) {
		t.Fatalf("page = %+v", page)
	}
}

// Decision (D13): a durable artifact is deleted with its owner; its reference then
// reads ARTIFACT_EXPIRED with a next step, never ARTIFACT_NOT_FOUND.
func TestDeletingTheOwnerExpiresTheRef(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateDurable("ws-1", "op-1", DurableOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Write([]byte("x\n"))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	ref := d.Ref().Ref
	if err := s.DeleteDurable("ws-1", "op-1"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ref)
	e, ok := err.(*capability.Error)
	if !ok || e.Code != capability.ErrArtifactExpired || e.NextStep == "" {
		t.Fatalf("err = %v, want ARTIFACT_EXPIRED with a next step", err)
	}
	// A well-formed reference whose file is gone is EXPIRED, not NOT_FOUND.
	if _, err := s.Get(RefPrefix + "d.ws-1.op-9"); !isExpired(err) {
		t.Fatalf("a well-formed ref with no file: %v, want ARTIFACT_EXPIRED", err)
	}
}

// Decision: a durable reference never resolves to a file outside
// <state>/ws/<wsid>/artifacts/. The workspace and owner parts must be single path
// elements; "..", a slash, a percent-encoded separator or an absolute path is a
// reference this store never issued, so it is ARTIFACT_NOT_FOUND and nothing outside
// the workspace's artifacts/ is opened. Removing the part check makes this read the
// planted file.
func TestDurableRefsOutsideTheWorkspaceAreNotFound(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	planted := filepath.Join(root, "planted.log")
	if err := os.WriteFile(planted, []byte("SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Reachable if the part check were gone: "d.../x.op-1" would join to
	// <root>/x/artifacts/op-1.log (step 4 review N1).
	if err := os.MkdirAll(filepath.Join(root, "x", "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x", "artifacts", "op-1.log"), []byte("SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{
		RefPrefix + "d...op-1",
		RefPrefix + "d.a/../b.op-1",
		RefPrefix + "d.a%2fb.op-1",
		RefPrefix + "d./etc/passwd.op-1",
		RefPrefix + "d.ws-1./etc/passwd",
		RefPrefix + "d.ws-1.op-1/../../evil",
		RefPrefix + "d.ws-1.",
		RefPrefix + "d.ws-1",
		RefPrefix + "d.../x.op-1",
		RefPrefix + "d../x.op-1",
	} {
		e, err := s.Get(ref)
		if !isNotFound(err) {
			t.Errorf("%s: %v, want ARTIFACT_NOT_FOUND", ref, err)
			continue
		}
		if e.Size != 0 || e.File != "" || len(e.Data) != 0 {
			t.Errorf("%s: opened %+v", ref, e)
		}
	}
}

// Decision (step 4 review N2): a symlink planted under artifacts/ is not followed.
func TestDurableSymlinkIsNotFollowed(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ws", "ws-1", "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "op-7.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(RefPrefix + "d.ws-1.op-7"); !isNotFound(err) {
		t.Fatalf("a symlinked console: %v, want ARTIFACT_NOT_FOUND", err)
	}
}

// Decision (D24): the producer's cap stops the artifact growing; bytes past it are
// dropped and counted, and are not an error, so a producer's write loop is never
// interrupted.
func TestDurableCapDropsTheOverflow(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateDurable("ws-1", "op-1", DurableOptions{Cap: 10})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if n, err := d.Write([]byte("abcd\n")); n != 5 || err != nil {
			t.Fatalf("write: n=%d err=%v", n, err)
		}
	}
	if d.Size() != 10 || d.Dropped() != 5 {
		t.Fatalf("size %d dropped %d, want 10 and 5", d.Size(), d.Dropped())
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get(d.Ref().Ref)
	if e.Size != 10 {
		t.Fatalf("size = %d, want the cap", e.Size)
	}
	page, _ := ReadText(e, 0, 0)
	if page.Text != "abcd\nabcd\n" {
		t.Fatalf("text = %q", page.Text)
	}
}

// Decision (D13): the sweep's listing sees a workspace's durable files, by owner.
func TestListDurable(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	for _, owner := range []string{"op-1", "op-2"} {
		d, err := s.CreateDurable("ws-1", owner, DurableOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d.Write([]byte("line\n"))
		d.Close()
	}
	files, err := s.ListDurable("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Owner] = true
		if f.Bytes != 5 || f.Ref != RefPrefix+"d.ws-1."+f.Owner {
			t.Errorf("file = %+v", f)
		}
	}
	if !seen["op-1"] || !seen["op-2"] {
		t.Fatalf("owners = %v", seen)
	}
	if err := s.DeleteDurable("ws-1", "op-1"); err != nil {
		t.Fatal(err)
	}
	files, _ = s.ListDurable("ws-1")
	if len(files) != 1 || files[0].Owner != "op-2" {
		t.Fatalf("after delete: %+v", files)
	}
}

// Decision: the workspace part may hold a dot (a project directory may); the id
// splits on the last dot, so the owner is always the last segment.
func TestDurableWorkspaceWithDotsResolves(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateDurable("my.app-1a2b3c4d5e6f", "op-1", DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	d.Write([]byte("hello\n"))
	d.Close()
	e, err := s.Get(RefPrefix + "d.my.app-1a2b3c4d5e6f.op-1")
	if err != nil || e.Ref.Ref != d.Ref().Ref {
		t.Fatalf("round trip: %+v, %v", e, err)
	}
}

func isNotFound(err error) bool {
	e, ok := err.(*capability.Error)
	return ok && e.Code == capability.ErrArtifactNotFound
}

func isExpired(err error) bool {
	e, ok := err.(*capability.Error)
	return ok && e.Code == capability.ErrArtifactExpired
}

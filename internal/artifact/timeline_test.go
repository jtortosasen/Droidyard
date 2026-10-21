package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

const tlID = "20261021T101500Z-0a1b2c3d"

func tlRef(id string) string { return RefPrefix + "r." + id }

func codeOf(t *testing.T, err error) capability.ErrorCode {
	t.Helper()
	var ce *capability.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a capability error", err)
	}
	return ce.Code
}

// Decision (ADR-0113 §5): a timeline is untrusted by class, so the mark holds when the
// reference is resolved from disk by a store that never held the handle, not only while
// the producer has it open. A mark read off the handle alone would fail the last check.
func TestTimelineRefIsUntrustedAfterReopen(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateTimeline(tlID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Ref().Untrusted {
		t.Fatal("the handle's reference is not untrusted")
	}
	if e, err := s.Get(tlRef(tlID)); err != nil || !e.Ref.Untrusted || !e.Growing {
		t.Fatalf("open: %+v, %v; want untrusted and growing", e.Ref, err)
	}
	if err := d.WriteLine([]byte(`{"kind":"end"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]*Store{"same store, closed": s, "after a restart": newDurableStore(t, root)} {
		e, err := st.Get(tlRef(tlID))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !e.Ref.Untrusted || e.Growing || e.Ref.MediaType != "application/x-ndjson" || e.Ref.Durability != DurabilityDurable {
			t.Fatalf("%s: %+v growing=%v, want untrusted, closed, ndjson, durable", name, e.Ref, e.Growing)
		}
	}
}

// Decision (ADR-0113 §5): no reference resolves outside <state>/recordings/<id>/timeline.jsonl.
// An id is matched against the issued shape before any path is built; a symlink planted
// where the file should be is not followed.
func TestTimelineRefRefusesPathsOutsideRecordings(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	outside := filepath.Join(root, "secret.jsonl")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"", "..", "../secret", "../../" + tlID, tlID + "/../..", tlID + "/timeline.jsonl",
		"20261021T101500Z-0A1B2C3D", "20261021T101500Z-0a1b2c3", "20261021T101500Z-0a1b2c3d0",
		"20261021T101500Z-0a1b2c3d\x00", "/etc/passwd", "20261021T101500-0a1b2c3d",
	} {
		_, err := s.Get(RefPrefix + "r." + id)
		if codeOf(t, err) != capability.ErrArtifactNotFound {
			t.Errorf("id %q: %v, want ARTIFACT_NOT_FOUND", id, err)
		}
	}
	// A symlink in the file's place is not a regular file the store wrote.
	dir := filepath.Join(root, "recordings", tlID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "timeline.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tlRef(tlID)); codeOf(t, err) != capability.ErrArtifactNotFound {
		t.Fatalf("symlinked timeline: %v, want ARTIFACT_NOT_FOUND", err)
	}
	// A directory in its place likewise.
	other := "20261021T101500Z-ffffffff"
	if err := os.MkdirAll(filepath.Join(root, "recordings", other, "timeline.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tlRef(other)); codeOf(t, err) != capability.ErrArtifactNotFound {
		t.Fatalf("directory timeline: %v, want ARTIFACT_NOT_FOUND", err)
	}
	// CreateTimeline refuses an id that is not the issued shape too.
	if _, err := s.CreateTimeline("../x", 0); err == nil {
		t.Fatal("CreateTimeline accepted a path as an id")
	}
}

// Decision (ADR-0012 §Durability classes): a deleted recording's reference reads
// ARTIFACT_EXPIRED, not NOT_FOUND, so a consumer can tell "gone" from "never was".
func TestTimelineDeletedIsExpired(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	if _, err := s.Get(tlRef(tlID)); codeOf(t, err) != capability.ErrArtifactExpired {
		t.Fatalf("never created, well-formed: %v, want ARTIFACT_EXPIRED", err)
	}
	d, err := s.CreateTimeline(tlID, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.WriteLine([]byte("{}"))
	_ = d.Close()
	if err := os.RemoveAll(filepath.Join(root, "recordings", tlID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tlRef(tlID)); codeOf(t, err) != capability.ErrArtifactExpired {
		t.Fatalf("deleted: %v, want ARTIFACT_EXPIRED", err)
	}
	// The directory present, the file gone: also expired.
	if err := os.MkdirAll(filepath.Join(root, "recordings", tlID), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tlRef(tlID)); codeOf(t, err) != capability.ErrArtifactExpired {
		t.Fatalf("file gone: %v, want ARTIFACT_EXPIRED", err)
	}
}

// Decision: a timeline reads `growing` while the recorder holds it and the size follows
// the writes; closing it ends the growth. A remote recording not yet copied also reads
// growing (its bytes are not served yet, step 10), and once copied reads as a file.
func TestTimelineGrowingUntilClosed(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateTimeline(tlID, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.WriteLine([]byte(`{"a":1}`))
	e, _ := s.Get(tlRef(tlID))
	if !e.Growing || e.Size != 8 {
		t.Fatalf("growing=%v size=%d, want growing, 8", e.Growing, e.Size)
	}
	_ = d.WriteLine([]byte(`{"b":2}`))
	e, _ = s.Get(tlRef(tlID))
	if e.Size != 16 {
		t.Fatalf("size = %d, want 16", e.Size)
	}
	_ = d.Close()
	e, _ = s.Get(tlRef(tlID))
	if e.Growing || e.Size != 16 {
		t.Fatalf("closed: growing=%v size=%d", e.Growing, e.Size)
	}

	remote := "20261021T101600Z-00000001"
	dir := filepath.Join(root, "recordings", remote)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(js string) {
		if err := os.WriteFile(filepath.Join(dir, "recording.json"), []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schemaVersion":1,"remote":true}`)
	e, err = s.Get(tlRef(remote))
	if err != nil || !e.Growing || !e.Ref.Untrusted {
		t.Fatalf("remote not copied: %+v %v, want growing and untrusted", e, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "timeline.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(`{"schemaVersion":1,"remote":true,"copied":true}`)
	e, err = s.Get(tlRef(remote))
	if err != nil || e.Growing || e.Size != 3 {
		t.Fatalf("remote copied: growing=%v size=%d %v, want a closed 3-byte file", e.Growing, e.Size, err)
	}
}

// Decision (ADR-0012 §Reading artifacts): paging a timeline concatenates, byte for byte,
// to the file, so a consumer that walks nextOffset loses nothing and a dump's XML (with
// multi-byte text) arrives intact.
func TestTimelinePagesConcatenateByteExact(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 0)
	var want strings.Builder
	for i := 0; i < 40; i++ {
		line := `{"kind":"dump","xml":"<node text=\"añadir ✓ ` + strings.Repeat("é", i*7) + `\"/>"}`
		_ = d.WriteLine([]byte(line))
		want.WriteString(line + "\n")
	}
	_ = d.Close()
	e, err := s.Get(tlRef(tlID))
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	var off int64
	pages := 0
	for {
		p, err := ReadText(e, off, 600)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(p.Text)
		pages++
		if p.NextOffset == off || p.NextOffset >= p.Size {
			break
		}
		off = p.NextOffset
	}
	if pages < 3 {
		t.Fatalf("only %d pages: the test does not page", pages)
	}
	if got.String() != want.String() {
		t.Fatalf("pages concatenate to %d bytes, want %d identical", got.Len(), want.Len())
	}
}

// Decision (ADR-0099 §2, ADR-0113 §5): a timeline is the host's device text; a peer's
// artifact.read (GetOwned) never reaches one, open or closed, and the refusal is the one
// a reference that never was gets, so it tells a peer nothing.
func TestPeerCannotReadATimeline(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 0)
	_ = d.WriteLine([]byte("{}"))
	peer := PeerOwner("laptop")
	_, never := s.GetOwned(tlRef("20261021T101500Z-deadbeef"), peer)
	for _, closed := range []bool{false, true} {
		if closed {
			_ = d.Close()
		}
		_, err := s.GetOwned(tlRef(tlID), peer)
		if codeOf(t, err) != capability.ErrArtifactNotFound {
			t.Fatalf("closed=%v: %v, want ARTIFACT_NOT_FOUND", closed, err)
		}
		if err.Error() != never.Error() {
			t.Fatalf("closed=%v: %q differs from a never-issued reference's %q", closed, err, never)
		}
	}
	if _, err := s.Get(tlRef(tlID)); err != nil {
		t.Fatalf("the host's own Get: %v", err)
	}
}

// Decision (ADR-0113 §5, the at-rest posture): device text on the host is owner-only.
func TestTimelineFilesAre0600(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 0)
	_ = d.WriteLine([]byte("{}"))
	_ = d.Close()
	dir := filepath.Join(root, "recordings", tlID)
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "timeline.jsonl"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s is %v, want %v", path, fi.Mode().Perm(), want)
		}
	}
}

// Decision (ADR-0113 §5): the sha256 a finished record keeps is the file's, so a copy
// made later (a peer's, step 10) can be checked against it.
func TestTimelineSHA256IsTheFiles(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 0)
	for _, l := range []string{`{"kind":"recording"}`, `{"kind":"dump","xml":"é"}`, `{"kind":"end"}`} {
		if err := d.WriteLine([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()
	data, err := os.ReadFile(filepath.Join(root, "recordings", tlID, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := d.SHA256(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256() = %s, want the file's %x", got, sum)
	}
}

// Decision (plan step 4 fix N2): a write that fails half way leaves no partial line: the
// file is cut back to the last whole line, the size and the hash stay as they were, and the
// next line lands cleanly after it. The hash of the file therefore still matches the file.
func TestTimelineShortWriteLeavesNoPartialLine(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 0)
	if err := d.WriteLine([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	d.writeHook = func(b []byte) (int, error) {
		n, _ := d.file.Write(b[:len(b)/2])
		return n, errors.New("disk full")
	}
	if err := d.WriteLine([]byte(`{"n":2,"pad":"xxxxxxxxxxxxxxxx"}`)); err == nil {
		t.Fatal("a failed write reported success")
	}
	d.writeHook = nil
	if d.Size() != 8 {
		t.Fatalf("size = %d after a failed write, want 8", d.Size())
	}
	if err := d.WriteLine([]byte(`{"n":3}`)); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	data, _ := os.ReadFile(filepath.Join(root, "recordings", tlID, "timeline.jsonl"))
	if string(data) != "{\"n\":1}\n{\"n\":3}\n" {
		t.Fatalf("file = %q, want the two whole lines", data)
	}
	sum := sha256.Sum256(data)
	if d.SHA256() != hex.EncodeToString(sum[:]) {
		t.Fatal("the hash no longer matches the file")
	}
}

// Decision (plan step 4 fix B1): a link planted where the timeline should be is not
// followed on create, and RecordingDir builds no path from an unchecked id.
func TestCreateTimelineDoesNotFollowALink(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "recordings", tlID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "timeline.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTimeline(tlID, 0); err == nil {
		t.Fatal("CreateTimeline followed a symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep\n" {
		t.Fatalf("outside file = %q, was touched", b)
	}
	if got := s.RecordingDir("../x"); got != "" {
		t.Fatalf("RecordingDir of a path = %q, want empty", got)
	}
}

// Decision (ADR-0113 §5, plan step 4): the writer refuses a line that would pass the cap
// and cuts nothing, so the file stays whole lines and the recorder can end it maxBytes.
func TestTimelineWriterRefusesALinePastTheCap(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, _ := s.CreateTimeline(tlID, 20)
	if err := d.WriteLine([]byte(strings.Repeat("a", 9))); err != nil { // 10 bytes
		t.Fatal(err)
	}
	if err := d.WriteLine([]byte(strings.Repeat("b", 10))); !errors.Is(err, ErrTimelineFull) { // 11 > 10 left
		t.Fatalf("err = %v, want ErrTimelineFull", err)
	}
	if err := d.WriteLine([]byte(strings.Repeat("c", 9))); err != nil { // a smaller line still fits
		t.Fatalf("a line that fits was refused: %v", err)
	}
	if d.Size() != 20 {
		t.Fatalf("size = %d, want 20", d.Size())
	}
	_ = d.Close()
	data, _ := os.ReadFile(filepath.Join(root, "recordings", tlID, "timeline.jsonl"))
	if string(data) != "aaaaaaaaa\nccccccccc\n" {
		t.Fatalf("file = %q: the refused line left bytes", data)
	}
}

package file

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// sum is the revision of a string, spelled out here so a test does not trust revisionOf.
func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func ptr(s string) *string { return &s }

// setSeam installs a revision or lock seam for one test.
func setSeam(t *testing.T, p *atomic.Pointer[func()], fn func()) {
	t.Helper()
	p.Store(&fn)
	t.Cleanup(func() { p.Store(nil) })
}

// Decision (ADR-0058 §1): the revision is the whole file's hash on every page, not the
// page's, so a client that read three pages holds one value to pass back. A file over
// the cap has none.
func TestReadRevisionIsWholeFileOnEveryPage(t *testing.T) {
	f := newFixtureWith(t, func(o *Options) { o.RevisionMax = 64 << 10 })
	body := strings.Repeat("line of text for paging\n", 1000) // 24,000 bytes
	f.write("big.txt", body)
	want := sum(body)

	limit := 10000
	offset := int64(0)
	pages := 0
	for {
		res, err := f.read(ReadParams{Path: "big.txt", Offset: &offset, Limit: &limit})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if res.Revision != want {
			t.Fatalf("page %d (offset %d) revision = %q; want the whole file's %q", pages, res.Offset, res.Revision, want)
		}
		if res.Text != nil && sum(*res.Text) == res.Revision {
			t.Fatalf("page %d: the revision is the page's hash, not the file's", pages)
		}
		if res.NextOffset >= res.Size {
			break
		}
		offset = res.NextOffset
	}
	if pages < 3 {
		t.Fatalf("read in %d pages; the test needs at least three", pages)
	}

	// A binary window still reports the whole file's revision.
	bin := append([]byte("\x00\x01\x02"), []byte(body)...)
	f.writeBytes("bin.dat", bin)
	if res, err := f.read(ReadParams{Path: "bin.dat"}); err != nil || !res.Binary || res.Revision != sum(string(bin)) {
		t.Errorf("binary read = %+v, %v; want binary with the file's revision", res, err)
	}

	// Over the cap: no revision, and the read is otherwise as before.
	f.write("huge.txt", strings.Repeat("x", 64<<10+1))
	res, err := f.read(ReadParams{Path: "huge.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != "" {
		t.Errorf("a file over the cap has revision %q; want none", res.Revision)
	}
	if res.Text == nil {
		t.Error("a file over the cap lost its text")
	}
	// At the cap exactly it has one.
	f.write("edge.txt", strings.Repeat("y", 64<<10))
	if res, err := f.read(ReadParams{Path: "edge.txt"}); err != nil || res.Revision != sum(strings.Repeat("y", 64<<10)) {
		t.Errorf("a file at the cap = %+v, %v; want its revision", res, err)
	}
}

// Decision (ADR-0020 §Retention, ADR-0058 §1): a retained read drops the text but keeps
// the revision, which is the one thing a later write needs.
func TestReadSlimKeepsRevision(t *testing.T) {
	f := newFixture(t)
	d, _ := f.reg.Lookup("file.read")
	txt := "t"
	got := d.Slim(ReadResult{Text: &txt, Revision: "r"}).(ReadResult)
	if got.Text != nil || got.Revision != "r" {
		t.Errorf("slimmed read = %+v; want no text and the revision kept", got)
	}
}

// Decision (ADR-0058 §2): a stale revision writes nothing — the bytes, the mode and the
// inode stay, no temporary file is left — and the conflict carries the path alone in
// details, the current revision in the cause only, and a next step naming file.read.
func TestWriteStaleRevisionConflictsAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.root, "conf.txt")
	if err := os.WriteFile(p, []byte("agent wrote this"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)

	_, err := f.writeCall(WriteParams{Path: "conf.txt", Content: "user's save", IfRevision: ptr(sum("what the user read"))})
	if err == nil || err.Code != capability.ErrFileRevisionConflict {
		t.Fatalf("stale write = %v; want FILE_REVISION_CONFLICT", err)
	}
	if len(err.Details) != 1 || err.Details["path"] != "conf.txt" {
		t.Errorf("details = %v; want only the path", err.Details)
	}
	cur := sum("agent wrote this")
	if strings.Contains(err.Message, cur) || !strings.Contains(err.Cause, cur) {
		t.Errorf("the current revision must be in the cause only: message %q, cause %q", err.Message, err.Cause)
	}
	if !strings.Contains(err.NextStep, "file.read") {
		t.Errorf("nextStep = %q; want it to name file.read", err.NextStep)
	}
	got, _ := os.ReadFile(p)
	after, _ := os.Stat(p)
	if string(got) != "agent wrote this" || !os.SameFile(before, after) || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the conflicting write changed the file: %q, same inode %v, mode %v -> %v", got, os.SameFile(before, after), before.Mode(), after.Mode())
	}
	if left := tempFilesUnder(t, f.root); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// The matching revision writes, keeps the mode, and replaces the file.
	res, err := f.writeCall(WriteParams{Path: "conf.txt", Content: "user's save", IfRevision: ptr(cur)})
	if err != nil {
		t.Fatalf("the matching revision = %v", err)
	}
	if res.Created || res.Revision != sum("user's save") {
		t.Errorf("result = %+v", res)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v; want 0600 kept", st.Mode())
	}
}

// Decision (ADR-0058 §2): "absent" means the file must not exist — it creates a missing
// file and conflicts with an existing one; a hash against a missing file conflicts and
// makes no parent directory.
func TestWriteRevisionAbsent(t *testing.T) {
	f := newFixture(t)
	res, err := f.writeCall(WriteParams{Path: "new/dir/a.txt", Content: "one", IfRevision: ptr("absent")})
	if err != nil || !res.Created {
		t.Fatalf("absent against a missing file = %+v, %v; want created", res, err)
	}
	if _, err := f.writeCall(WriteParams{Path: "new/dir/a.txt", Content: "two", IfRevision: ptr("absent")}); err == nil || err.Code != capability.ErrFileRevisionConflict {
		t.Fatalf("absent against an existing file = %v; want FILE_REVISION_CONFLICT", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, "new/dir/a.txt")); string(b) != "one" {
		t.Errorf("file = %q; want it untouched", b)
	}

	_, err = f.writeCall(WriteParams{Path: "nodir/deeper/b.txt", Content: "x", IfRevision: ptr(sum("old"))})
	if err == nil || err.Code != capability.ErrFileRevisionConflict {
		t.Fatalf("a hash against a missing file = %v; want FILE_REVISION_CONFLICT", err)
	}
	if !strings.Contains(err.Cause, "absent") {
		t.Errorf("cause = %q; want the current revision, absent", err.Cause)
	}
	if _, serr := os.Stat(filepath.Join(f.root, "nodir")); serr == nil {
		t.Error("the conflicting write made a parent directory")
	}
}

// Decision (ADR-0058 §2, Alternatives: policy.update's order): a conditional write whose
// target already holds content succeeds without writing even with a stale ifRevision, so
// a retried write whose first attempt landed does not conflict with itself; an
// unconditional one still writes.
func TestWriteSameContentSucceedsWithoutWriting(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.root, "same.txt")
	if err := os.WriteFile(p, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)

	for _, rev := range []string{sum("an older version"), "absent", "not a revision"} {
		res, err := f.writeCall(WriteParams{Path: "same.txt", Content: "content", IfRevision: ptr(rev)})
		if err != nil {
			t.Fatalf("a conditional write of the same content (ifRevision %q) = %v; want success", rev, err)
		}
		if res.Created || res.Size != 7 || res.Revision != sum("content") || res.Path != "same.txt" {
			t.Errorf("result = %+v", res)
		}
		after, _ := os.Stat(p)
		if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("ifRevision %q: the no-op rewrote the file (same inode %v, mtime %v -> %v)", rev, os.SameFile(before, after), before.ModTime(), after.ModTime())
		}
	}
	if left := tempFilesUnder(t, f.root); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}

	// Unconditional: nothing changes about it — the file is rewritten.
	if _, err := f.writeCall(WriteParams{Path: "same.txt", Content: "content"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if os.SameFile(before, after) {
		t.Error("an unconditional write of the same content did not write")
	}
}

// Decision (ADR-0058 §3): the result's revision is the hash of content, whatever the
// file held and with or without a precondition — not a re-read.
func TestWriteResultRevisionIsContentHash(t *testing.T) {
	f := newFixture(t)
	f.write("r.txt", "old")
	for _, c := range []struct {
		name string
		p    WriteParams
	}{
		{"unconditional", WriteParams{Path: "r.txt", Content: "héllo\n"}},
		{"conditional", WriteParams{Path: "r.txt", Content: "second", IfRevision: ptr(sum("héllo\n"))}},
		{"created", WriteParams{Path: "fresh.txt", Content: ""}},
	} {
		res, err := f.writeCall(c.p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.Revision != sum(c.p.Content) {
			t.Errorf("%s: revision = %q; want the content's %q", c.name, res.Revision, sum(c.p.Content))
		}
	}
	// And file.read reports the same value for what was written.
	if r, err := f.read(ReadParams{Path: "r.txt"}); err != nil || r.Revision != sum("second") {
		t.Errorf("read after write = %+v, %v", r, err)
	}
}

// Decision (ADR-0058 §4): every file.write on one root holds one mutex from the compare
// to the rename. Two calls with one base revision and different contents: the first is
// held inside the section after its compare passed; the second must wait, so exactly one
// succeeds and the other conflicts. Without the mutex both compare against the old file
// and both succeed — the second finishing while the first is still held.
func TestConditionalWritesOnOneRootSerialise(t *testing.T) {
	f := newFixture(t)
	f.write("race.txt", "base")
	base := ptr(sum("base"))

	held, release := make(chan struct{}), make(chan struct{})
	var inSection, lockCalls atomic.Int32
	setSeam(t, &revisionSeam, func() {
		if inSection.Add(1) == 1 {
			close(held)
			<-release
		}
	})
	waiting := make(chan struct{})
	setSeam(t, &lockSeam, func() {
		if lockCalls.Add(1) == 2 { // the second call is about to take the mutex
			close(waiting)
		}
	})

	type outcome struct {
		res WriteResult
		err *capability.Error
	}
	run := func(content string) chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			res, err := f.writeCall(WriteParams{Path: "race.txt", Content: content, IfRevision: base})
			ch <- outcome{res, err}
		}()
		return ch
	}
	a := run("A")
	<-held // A has passed its compare and holds the section
	b := run("B")
	<-waiting
	select {
	case o := <-b:
		t.Fatalf("B finished while A held the section: %+v, %v", o.res, o.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	oa, ob := <-a, <-b

	if oa.err != nil {
		t.Fatalf("A = %v; want success", oa.err)
	}
	if ob.err == nil || ob.err.Code != capability.ErrFileRevisionConflict {
		t.Fatalf("B = %+v, %v; want FILE_REVISION_CONFLICT", ob.res, ob.err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "race.txt")); string(got) != "A" {
		t.Errorf("file = %q; want A's content", got)
	}
	if left := tempFilesUnder(t, f.root); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// Decision (ADR-0058 §4): the mutex is per root, so a write on another root is not held
// up by it, and one root always gets the same mutex.
func TestWriteLocksArePerRoot(t *testing.T) {
	l := NewWriteLocks()
	if l.of("/a") != l.of("/a") {
		t.Error("one root got two mutexes")
	}
	if l.of("/a") == l.of("/b") {
		t.Error("two roots share a mutex")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	l.of("/a").Lock()
	go func() { defer wg.Done(); l.of("/b").Lock(); l.of("/b").Unlock() }()
	wg.Wait() // /b is free while /a is held
	l.of("/a").Unlock()
}

// Decision (ADR-0058 §2): a target over the cap with ifRevision is FILE_TOO_LARGE and is
// never hashed — here it is not even readable, so a hash would have been FILE_IO_FAILED;
// without ifRevision the same target is written as before.
func TestWriteRevisionOverCapIsTooLargeUnhashed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without its read bit")
	}
	f := newFixtureWith(t, func(o *Options) { o.RevisionMax = 1024 })
	p := filepath.Join(f.root, "big.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 2048)), 0o200); err != nil {
		t.Fatal(err)
	}
	_, err := f.writeCall(WriteParams{Path: "big.txt", Content: "small", IfRevision: ptr(sum("anything"))})
	if err == nil || err.Code != capability.ErrFileTooLarge {
		t.Fatalf("over the cap = %v; want FILE_TOO_LARGE", err)
	}
	if err.NextStep != "" && strings.Contains(err.NextStep, "file.read") {
		t.Errorf("nextStep %q points back to file.read, which gives such a file no revision", err.NextStep)
	}
	if len(err.Details) != 1 || err.Details["path"] != "big.txt" {
		t.Errorf("details = %v; want only the path", err.Details)
	}
	if st, _ := os.Stat(p); st.Size() != 2048 {
		t.Error("the refused write changed the file")
	}
	if left := tempFilesUnder(t, f.root); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
	// absent is settled by existence alone (m2i step 2 verifier, finding 3): a conflict,
	// whose next step is a read, not FILE_TOO_LARGE, which would invite dropping the check.
	_, err = f.writeCall(WriteParams{Path: "big.txt", Content: "small", IfRevision: ptr(absentRevision)})
	if err == nil || err.Code != capability.ErrFileRevisionConflict {
		t.Fatalf("absent over the cap = %v; want FILE_REVISION_CONFLICT", err)
	}
	if st, _ := os.Stat(p); st.Size() != 2048 {
		t.Error("the refused absent write changed the file")
	}
	if _, err := f.writeCall(WriteParams{Path: "big.txt", Content: "small"}); err != nil {
		t.Errorf("an unconditional write of the same target = %v; want it to write", err)
	}
}

// Decision (ADR-0058 §2, as policy.update): an explicit "ifRevision": null is refused, not
// read as an omitted check, which would turn the guard off silently. Seen failing without
// WriteParams.UnmarshalJSON.
func TestWriteNullRevisionIsRefused(t *testing.T) {
	var p WriteParams
	if err := json.Unmarshal([]byte(`{"path":"a.txt","content":"x","ifRevision":null}`), &p); err == nil {
		t.Fatal("ifRevision null decoded; want an error")
	}
	if err := json.Unmarshal([]byte(`{"path":"a.txt","content":"x","ifRevision":"absent"}`), &p); err != nil || p.IfRevision == nil || *p.IfRevision != "absent" {
		t.Fatalf("a string ifRevision = %v, %v", p.IfRevision, err)
	}
	if err := json.Unmarshal([]byte(`{"path":"a.txt","content":"x","bogus":1}`), &p); err == nil {
		t.Fatal("an unknown field decoded; want the strict decode kept")
	}
	if err := json.Unmarshal([]byte(`{"path":"a.txt","content":"x"}`), &p); err != nil || p.IfRevision != nil {
		t.Fatalf("omitted ifRevision = %v, %v", p.IfRevision, err)
	}
}

// Decision (ADR-0058 §2, D5): the target is opened without blocking, so a path swapped
// for a FIFO after the early classification and before the compare fails at once with
// FILE_WRONG_TYPE instead of hanging the call (and the root's mutex).
func TestWriteRevisionTargetSwappedForFIFODoesNotBlock(t *testing.T) {
	f := newFixture(t)
	f.write("swap.txt", "was a file")
	p := filepath.Join(f.root, "swap.txt")
	writeSeam = func(string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return syscall.Mkfifo(p, 0o644)
	}
	t.Cleanup(func() { writeSeam = nil })

	done := make(chan *capability.Error, 1)
	go func() {
		_, err := f.writeCall(WriteParams{Path: "swap.txt", Content: "new", IfRevision: ptr(sum("was a file"))})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Code != capability.ErrFileWrongType {
			t.Fatalf("a FIFO target = %v; want FILE_WRONG_TYPE", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write blocked on a FIFO")
	}
	if st, _ := os.Lstat(p); st == nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Error("the FIFO was replaced")
	}
	// The mutex was released: a write on this root still runs.
	writeSeam = nil
	if _, err := f.writeCall(WriteParams{Path: "other.txt", Content: "x", IfRevision: ptr("absent")}); err != nil {
		t.Errorf("a later write = %v; the failed one left the root locked", err)
	}
}

// Decision (ADR-0058 §2, as policy.update): a value that is not a revision conflicts, and
// the message says it is not one rather than claiming the file changed.
func TestWriteMalformedRevisionConflictsAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.write("m.txt", "body")
	for _, bad := range []string{"", "ABC", strings.ToUpper(sum("body")), sum("body")[:63], "Absent"} {
		_, err := f.writeCall(WriteParams{Path: "m.txt", Content: "other", IfRevision: ptr(bad)})
		if err == nil || err.Code != capability.ErrFileRevisionConflict {
			t.Fatalf("ifRevision %q = %v; want FILE_REVISION_CONFLICT", bad, err)
		}
		if !strings.Contains(err.Message, "not the revision file.read returned") || strings.Contains(err.Message, "changed since") {
			t.Errorf("ifRevision %q: message %q; want it to say the value is not a revision", bad, err.Message)
		}
	}
	_, err := f.writeCall(WriteParams{Path: "m.txt", Content: "other", IfRevision: ptr(sum("stale"))})
	if err == nil || !strings.Contains(err.Message, "changed since") {
		t.Errorf("a well-formed stale revision: %v; want a message saying the file changed", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, "m.txt")); string(b) != "body" {
		t.Errorf("file = %q; want it untouched", b)
	}
}

// Decision (ADR-0058 §5): without ifRevision nothing changes — the write replaces
// whatever the file holds, whatever changed since the caller read it.
func TestWriteWithoutRevisionIsUnchanged(t *testing.T) {
	f := newFixture(t)
	f.write("u.txt", "first")
	r, _ := f.read(ReadParams{Path: "u.txt"})
	f.write("u.txt", "changed by someone else")
	res, err := f.writeCall(WriteParams{Path: "u.txt", Content: "mine"})
	if err != nil || res.Created {
		t.Fatalf("unconditional write = %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, "u.txt")); string(b) != "mine" || r.Revision == res.Revision {
		t.Errorf("file = %q", b)
	}
}

// BenchmarkRevisionHash measures the cost ADR-0058 §Consequences names: reading and
// hashing a file at the size of the largest write and at the cap.
func BenchmarkRevisionHash(b *testing.B) {
	for _, c := range []struct {
		name string
		size int
	}{{"512KiB", 512 << 10}, {"8MiB", 8 << 20}} {
		b.Run(c.name, func(b *testing.B) {
			p := filepath.Join(b.TempDir(), "f")
			if err := os.WriteFile(p, make([]byte, c.size), 0o644); err != nil {
				b.Fatal(err)
			}
			f, err := os.Open(p)
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			b.SetBytes(int64(c.size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok, err := hashFile(f, maxRevision); err != nil || !ok {
					b.Fatal(ok, err)
				}
			}
		})
	}
}

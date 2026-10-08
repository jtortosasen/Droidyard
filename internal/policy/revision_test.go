package policy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// revisionPatch builds a patch carrying ifRevision.
func revisionPatch(t *testing.T, revision, sections string) policy.UpdateParams {
	t.Helper()
	return patch(t, fmt.Sprintf(`{"ifRevision": %q%s}`, revision, sections))
}

// Decision (ADR-0032 §5 as amended, Q2): the revision policy.get returns, passed back
// as ifRevision, lets the update through, and the result carries the new revision.
func TestUpdateWithCurrentRevisionWrites(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]}}`)
	p := policy.New(envAt(dir))
	rev := p.Get().Revision
	if len(rev) != 64 {
		t.Fatalf("revision = %q; want a hex SHA-256", rev)
	}
	res, err := p.Update(revisionPatch(t, rev, `, "shell": {"enabled": true}`))
	if err != nil {
		t.Fatalf("an update with the current revision = %v", err)
	}
	if doc := read(t, path); doc["shell"] == nil {
		t.Errorf("the update with the current revision wrote nothing: %v", doc)
	}
	if res.Revision == rev || res.Revision != policy.New(envAt(dir)).Get().Revision {
		t.Errorf("the result's revision %q is not the written file's (old %q)", res.Revision, rev)
	}
}

// Decision (Q2): a `droidyard trust` between get and update is the lost-root race; with
// ifRevision the update fails, writes nothing, and the root stays.
func TestStaleRevisionConflictsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	p := policy.New(envAt(dir))
	rev := p.Get().Revision

	root := platform.Current().Canonical(t.TempDir())
	if _, err := policy.New(envAt(dir)).Trust(root); err != nil {
		t.Fatal(err)
	}
	before, mtime := raw(t, path)

	_, err := p.Update(revisionPatch(t, rev, `, "trust": {"roots": ["/other"]}`))
	e := errCode(err)
	if e == nil || e.Code != capability.ErrPolicyRevisionConflict {
		t.Fatalf("a stale revision = %v; want POLICY_REVISION_CONFLICT", err)
	}
	if !strings.Contains(e.NextStep, "policy.get") {
		t.Errorf("nextStep = %q; want it to name policy.get", e.NextStep)
	}
	after, afterMtime := raw(t, path)
	if string(after) != string(before) || afterMtime != mtime {
		t.Errorf("a conflicting update touched the file: %s", after)
	}
	if ok, _ := policy.New(envAt(dir)).Trusted(root); !ok {
		t.Error("the trusted root was lost")
	}
}

// Decision (Q2): the comparison is made under the lock, with the bytes re-read there. A
// write that lands while the update waits for the lock must conflict; a comparison made
// before taking the lock would let it through.
func TestWriteWhileUpdateWaitsForTheLockConflicts(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	rev := policy.New(envAt(dir)).Get().Revision

	lockFile, err := os.OpenFile(filepath.Join(dir, policy.LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	upd := revisionPatch(t, rev, `, "shell": {"enabled": true}`)
	about := make(chan struct{})
	policy.SetLockSeam(func() { close(about) })
	t.Cleanup(func() { policy.SetLockSeam(nil) })
	done := make(chan *capability.Error, 1)
	go func() {
		_, err := policy.New(envAt(dir)).Update(upd)
		done <- err
	}()
	<-about // the update is about to take the lock, which the test holds
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"trust":{"roots":["/hand"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || err.Code != capability.ErrPolicyRevisionConflict {
			t.Fatalf("an update that waited for the lock = %v; want POLICY_REVISION_CONFLICT", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the update never returned")
	}
	if doc := read(t, path); doc["shell"] != nil {
		t.Errorf("the conflicting update wrote: %v", doc)
	}
}

// Decision (Q2): with no file the revision is "absent", and ifRevision "absent" writes
// only while there is still no file.
func TestAbsentFileRevision(t *testing.T) {
	dir := t.TempDir()
	p := policy.New(envAt(dir))
	if rev := p.Get().Revision; rev != "absent" {
		t.Fatalf("revision of no file = %q; want absent", rev)
	}
	if _, err := p.Update(revisionPatch(t, "absent", `, "shell": {"enabled": true}`)); err != nil {
		t.Fatalf("absent with no file = %v; want it written", err)
	}
	path := filepath.Join(dir, policy.FileName)
	before, _ := raw(t, path)
	_, err := p.Update(revisionPatch(t, "absent", `, "shell": {"enabled": false}`))
	if e := errCode(err); e == nil || e.Code != capability.ErrPolicyRevisionConflict {
		t.Fatalf("absent with a file = %v; want POLICY_REVISION_CONFLICT", err)
	}
	if after, _ := raw(t, path); string(after) != string(before) {
		t.Error("the conflicting update changed the file")
	}
}

// Decision (Q2): without ifRevision the behaviour is what it was: the patch applies to
// whatever the file holds, whatever changed since.
func TestUpdateWithoutRevisionIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	p := policy.New(envAt(dir))
	p.Get()
	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/hand"]}}`)
	if _, err := p.Update(patch(t, `{"shell": {"enabled": true}}`)); err != nil {
		t.Fatalf("an update without ifRevision = %v", err)
	}
	doc := read(t, path)
	if doc["shell"] == nil || doc["trust"] == nil {
		t.Errorf("file = %v; want the patch applied and the hand edit kept", doc)
	}
}

// Decision (Q2): the revision describes the bytes the returned policy was parsed from. A
// file rewritten between the parse and the hash must not give the new bytes' hash to the
// old policy, or an update with it would pass the check over a policy the client never saw.
func TestRevisionIsOfTheParsedBytes(t *testing.T) {
	dir := t.TempDir()
	oldBody := `{"schemaVersion":1,"trust":{"roots":["/old"]}}`
	path := write(t, dir, oldBody)
	sum := sha256.Sum256([]byte(oldBody))
	want := hex.EncodeToString(sum[:])

	policy.SetParseSeam(func() {
		policy.SetParseSeam(nil)
		if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"trust":{"roots":["/new-and-longer"]}}`), 0o600); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { policy.SetParseSeam(nil) })

	res := policy.New(envAt(dir)).Get()
	if got := res.Trust.Roots; len(got) != 1 || got[0] != "/old" {
		t.Fatalf("the seam did not interleave: roots = %v", got)
	}
	if res.Revision != want {
		t.Errorf("revision = %q; want the parsed bytes' %q", res.Revision, want)
	}
}

// Decision (Q2, N3): an explicit null ifRevision must not silently switch the check off.
func TestNullIfRevisionIsRefused(t *testing.T) {
	var p policy.UpdateParams
	err := json.Unmarshal([]byte(`{"ifRevision": null, "shell": {"enabled": true}}`), &p)
	if err == nil || !strings.Contains(err.Error(), "ifRevision") {
		t.Fatalf("a null ifRevision decoded as %v; want an error naming ifRevision", err)
	}
}

// Decision (Q2, N4): an empty token is a token, not "no check": on an existing file it
// conflicts and the file is byte-identical.
func TestEmptyIfRevisionConflicts(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	before, mtime := raw(t, path)
	_, err := policy.New(envAt(dir)).Update(revisionPatch(t, "", `, "shell": {"enabled": true}`))
	e := errCode(err)
	if e == nil || e.Code != capability.ErrPolicyRevisionConflict {
		t.Fatalf("an empty ifRevision = %v; want POLICY_REVISION_CONFLICT", err)
	}
	if strings.Contains(e.Message, "since revision") {
		t.Errorf("message %q names a revision that was never given", e.Message)
	}
	if after, m := raw(t, path); string(after) != string(before) || m != mtime {
		t.Error("the conflicting update touched the file")
	}
}

// Decision (Q2, N2): a conflict's nextStep is policy.get, which must then read the file as
// it is. A snapshot cached before a same-size, same-mtime rewrite would keep answering
// with the old revision.
func TestConflictDropsAStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/old"]}}`)
	p := policy.New(envAt(dir))
	stale := p.Get().Revision
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh := `{"schemaVersion":1,"trust":{"roots":["/new"]}}`
	if err := os.WriteFile(path, []byte(fresh), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Update(revisionPatch(t, stale, `, "shell": {"enabled": true}`)); err == nil || err.Code != capability.ErrPolicyRevisionConflict {
		t.Fatalf("update = %v; want POLICY_REVISION_CONFLICT", err)
	}
	sum := sha256.Sum256([]byte(fresh))
	if got := p.Get().Revision; got != hex.EncodeToString(sum[:]) {
		t.Errorf("policy.get after the conflict = %q; want the file's current revision", got)
	}
}

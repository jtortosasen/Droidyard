package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// errCode returns err as a *capability.Error, or nil when it is a plain error.
func errCode(err error) *capability.Error {
	var e *capability.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// patch decodes a patch object the way the core does, so a test exercises the same
// strict decode a call does (plan m1k D3).
func patch(t *testing.T, body string) policy.UpdateParams {
	t.Helper()
	var p policy.UpdateParams
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return p
}

// raw reads the file's bytes and its mtime, so a test can prove it was untouched.
func raw(t *testing.T, path string) ([]byte, int64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return b, fi.ModTime().UnixNano()
}

// TestUpdateAppliesARestrictedMergePatch pins D3's core promise: only the keys the
// patch names change, every other section and every unknown key, top-level and inside a
// known section, is kept (ADR-0032 §1, §5). A write that rebuilt the file from the
// addressed section would drop `gradle`, `tools` and the unknown keys.
func TestUpdateAppliesARestrictedMergePatch(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{
  "schemaVersion": 1,
  "future": {"x": 1},
  "trust":   {"roots": ["/kept"], "prefixes": ["/p"], "unknown": true},
  "devices": {"allowPhysical": false, "allowlist": ["SER"]},
  "shell":   {"enabled": false, "roots": ["/s"], "extra": 1},
  "gradle":  {"sidecarJavaHome": null, "allowBelowFloor": true},
  "tools":   {"adb": "/usr/bin/adb", "emulator": null}
}`)
	before := read(t, path)

	got, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, `{"shell": {"enabled": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	after := read(t, path)

	// Only shell.enabled differs, JSON-equal otherwise.
	for _, key := range []string{"future", "trust", "devices", "gradle", "tools"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Errorf("%s changed by an unrelated update:\nbefore %#v\nafter  %#v", key, before[key], after[key])
		}
	}
	shBefore := before["shell"].(map[string]any)
	shAfter := after["shell"].(map[string]any)
	if shAfter["enabled"] != true {
		t.Errorf("shell.enabled = %v; want true", shAfter["enabled"])
	}
	if shAfter["extra"] != shBefore["extra"] || !reflect.DeepEqual(shAfter["roots"], shBefore["roots"]) {
		t.Errorf("the shell section was replaced, not merged: before %#v after %#v", shBefore, shAfter)
	}
	if got.State != policy.StateOK || !got.Shell.Enabled {
		t.Errorf("Update result = %+v; want state ok and shell enabled (D2's shape after the write)", got)
	}

	// A null section is removed and its defaults apply.
	if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, `{"shell": null}`)); err != nil {
		t.Fatal(err)
	}
	removed := read(t, path)
	if _, ok := removed["shell"]; ok {
		t.Errorf("{\"shell\": null} left the section: %v", removed["shell"])
	}
	if s := policy.New(envAt(dir)).Get(); s.Shell.Enabled || len(s.Shell.Roots) != 0 {
		t.Errorf("after removing the section: shell = %+v; want defaults", s.Shell)
	}
}

// TestUpdateEmptyPatchWritesNothing: an empty patch, and a section object that merges
// nothing, leave the file's bytes and mtime untouched (plan m1k D3 check).
func TestUpdateEmptyPatchWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]}}`)
	before, mtime := raw(t, path)

	for _, body := range []string{`{}`, `{"trust": {}}`} {
		if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, body)); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		after, afterMtime := raw(t, path)
		if string(after) != string(before) || afterMtime != mtime {
			t.Errorf("%s rewrote the file:\nbefore %s\nafter  %s", body, before, after)
		}
	}
}

// TestUpdateRefusesUnknownOrWrongKeysAtStart: the schema closes every object, so an
// unknown section or key, or a wrong type, is REQUEST_INVALID_PARAMS at Start, before
// any operation exists, and the file is untouched (plan m1k D3). The strict decode is
// the decision; a decode that ignored unknown fields would silently no-op.
func TestUpdateRefusesUnknownOrWrongKeysAtStart(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]}}`)
	before, mtime := raw(t, path)

	reg := capability.NewRegistry()
	policy.Register(reg, policy.New(envAt(dir)))
	ops := capability.NewOperations(reg)

	for _, body := range []string{
		`{"gradle": {}}`,
		`{"shell": {"enable": true}}`,
		`{"schemaVersion": 2}`,
		`{"trust": {"roots": "x"}}`,
		`{"devices": {"allowlist": [1]}}`,
		// Case variants: encoding/json would match them, the merge would write them.
		`{"Shell": {"enabled": true}}`,
		`{"shell": {"Roots": ["rel"]}}`,
	} {
		_, err := ops.Start(context.Background(), capability.Request{
			Capability: "policy.update", Params: json.RawMessage(body), Client: capability.ClientCLI,
		})
		if e := errCode(err); e == nil || e.Code != capability.ErrInvalidParams {
			t.Errorf("%s at Start = %v; want REQUEST_INVALID_PARAMS", body, err)
		}
	}
	after, afterMtime := raw(t, path)
	if string(after) != string(before) || afterMtime != mtime {
		t.Errorf("a refused patch touched the file: %s", after)
	}
}

// TestUpdateCanonicalisesPathsAndRejectsRelative: a path entry the patch wrote is
// canonicalised as `droidyard trust` does; a relative one is REQUEST_INVALID_PARAMS
// naming the key, and the file is untouched (plan m1k D3 check).
func TestUpdateCanonicalisesPathsAndRejectsRelative(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)

	if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, `{"trust": {"roots": ["/x/../y"]}}`)); err != nil {
		t.Fatal(err)
	}
	if got := policy.New(envAt(dir)).Get().Trust.Roots; len(got) != 1 || got[0] != "/y" {
		t.Errorf("roots = %v; want [/y] (canonicalised)", got)
	}

	before, mtime := raw(t, path)
	for _, c := range []struct{ body, key string }{
		{`{"trust": {"roots": ["rel"]}}`, "trust.roots"},
		{`{"shell": {"roots": ["./here"]}}`, "shell.roots"},
	} {
		_, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, c.body))
		if e := errCode(err); e == nil || e.Code != capability.ErrInvalidParams || !strings.Contains(e.Cause, c.key) {
			t.Errorf("%s = %v; want REQUEST_INVALID_PARAMS naming %s", c.body, err, c.key)
		}
		after, afterMtime := raw(t, path)
		if string(after) != string(before) || afterMtime != mtime {
			t.Errorf("%s touched the file: %s", c.body, after)
		}
	}
}

// TestUpdateRefusesAnInvalidOrNewerFileUntouched: a file that does not parse or is of a
// newer version is POLICY_CONFIG_UNUSABLE, and the file keeps its bytes and mtime
// (ADR-0032 §4; plan m1k D3).
func TestUpdateRefusesAnInvalidOrNewerFileUntouched(t *testing.T) {
	for _, body := range []string{
		`{ this is not json`,
		`{"schemaVersion": 2}`,
	} {
		dir := t.TempDir()
		path := write(t, dir, body)
		before, mtime := raw(t, path)

		_, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, `{"shell": {"enabled": true}}`))
		if e := errCode(err); e == nil || e.Code != capability.ErrPolicyConfigUnusable {
			t.Errorf("%s = %v; want POLICY_CONFIG_UNUSABLE", body, err)
		}
		after, afterMtime := raw(t, path)
		if string(after) != string(before) || afterMtime != mtime {
			t.Errorf("%s was rewritten: %s", body, after)
		}
	}
}

// TestUpdateRefusesAReadOnlyDirectory: a write that cannot happen is
// POLICY_CONFIG_UNUSABLE with the OS error as its cause (plan m1k D3 check).
func TestUpdateRefusesAReadOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	_, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, `{"shell": {"enabled": true}}`))
	if e := errCode(err); e == nil || e.Code != capability.ErrPolicyConfigUnusable || e.Cause == "" {
		t.Fatalf("a read-only directory = %v; want POLICY_CONFIG_UNUSABLE with the OS error as cause", err)
	}
	if b, _ := raw(t, path); string(b) != `{"schemaVersion":1}` {
		t.Errorf("the file changed under a read-only directory: %s", b)
	}
}

// TestUpdateIsIdempotent: applying the same patch twice gives the same file, which is
// what makes the capability safe to retry (ADR-0032 §5; plan m1k D3, R2).
func TestUpdateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	body := `{"shell": {"enabled": true, "roots": ["/w"]}, "devices": {"allowPhysical": true, "allowlist": ["SER"]}}`

	if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, body)); err != nil {
		t.Fatal(err)
	}
	first, _ := raw(t, path)
	if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, body)); err != nil {
		t.Fatal(err)
	}
	second, _ := raw(t, path)
	if string(first) != string(second) {
		t.Errorf("a repeated update changed the file:\nfirst  %s\nsecond %s", first, second)
	}
}

// TestUpdateMakesTheShellPolicyLive: an update takes effect on the next check, without
// a restart (ADR-0032 §6; plan m1k A3). A cache that did not drop would keep answering
// from the state before the write.
func TestUpdateMakesTheShellPolicyLive(t *testing.T) {
	dir := t.TempDir()
	p := policy.New(envAt(dir))
	if ok, _ := p.ShellAllowed("/w/proj"); ok {
		t.Fatal("the shell is allowed before it is enabled")
	}
	if _, err := p.Update(context.Background(), patch(t, `{"shell": {"roots": ["/w/proj"]}}`)); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.ShellAllowed("/w/proj"); !ok || err != nil {
		t.Errorf("ShellAllowed after the update = %v, %v; want true without a restart", ok, err)
	}
}

// TestConcurrentTrustsAndUpdatesLoseNothing is the cross-writer promise (ADR-0031 §4;
// plan m1k D3): 8 `droidyard trust` calls and 8 updates toggling devices.allowPhysical,
// all racing, leave every root present and the file valid. Without the exclusive lock a
// read-modify-write drops another writer's key.
func TestConcurrentTrustsAndUpdatesLoseNothing(t *testing.T) {
	dir := t.TempDir()
	want := make([]string, 8)
	for i := range want {
		r := filepath.Join(t.TempDir(), "r")
		if err := os.MkdirAll(r, 0o700); err != nil {
			t.Fatal(err)
		}
		want[i] = platform.Current().Canonical(r)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range want {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			if _, err := policy.New(envAt(dir)).Trust(root); err != nil {
				errs <- err
			}
		}(want[i])
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"devices": {"allowPhysical": true}}`
			if i%2 == 0 {
				body = `{"devices": {"allowPhysical": false}}`
			}
			if _, err := policy.New(envAt(dir)).Update(context.Background(), patch(t, body)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent writer failed: %v", err)
	}

	path := filepath.Join(dir, policy.FileName)
	got := roots(read(t, path))
	for _, r := range want {
		if !contains(got, r) {
			t.Errorf("root %s was lost to a racing writer; roots = %v", r, got)
		}
	}
	// The file is valid: it parses and answers a live check.
	if s := policy.New(envAt(dir)).Get(); s.State != policy.StateOK {
		t.Errorf("the raced file = %+v; want state ok", s)
	}
}

// Decision (m1k §Changes, step 3 B3): a patch that would make the file larger than the
// reader accepts is refused and the file is left as it was, still trusting what it did.
func TestUpdateRefusesAFileOverTheReadersBound(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]}}`)
	before, mtime := raw(t, path)
	p := policy.New(envAt(dir))
	serials := make([]string, 0, 120000)
	for i := 0; i < 120000; i++ {
		serials = append(serials, fmt.Sprintf("SERIAL%06d", i))
	}
	body, _ := json.Marshal(map[string]any{"devices": map[string]any{"allowlist": serials}})
	_, err := p.Update(context.Background(), patch(t, string(body)))
	if err == nil || err.Code != capability.ErrInvalidParams {
		t.Fatalf("an oversized patch = %v; want REQUEST_INVALID_PARAMS", err)
	}
	after, afterMtime := raw(t, path)
	if string(after) != string(before) || afterMtime != mtime {
		t.Error("an oversized patch touched the file")
	}
	if ok, _ := p.Trusted("/a"); !ok {
		t.Error("after a refused oversized patch /a is no longer trusted")
	}
}

// Decision (D3): after an update the next check sees the new file, even when a later
// same-size rewrite is set back to the cached mtime. The explicit cache drop in Update is
// not what this row observes (Update's own Get re-reads first); see m1k §Changes, step 3.
func TestTheNextCheckSeesAnUpdate(t *testing.T) {
	dir := t.TempDir()
	p := policy.New(envAt(dir))
	if _, err := p.Update(context.Background(), patch(t, `{"shell": {"roots": ["/w/aa"]}}`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, policy.FileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.ShellAllowed("/w/bb"); ok {
		t.Fatal("/w/bb allowed before the update")
	}
	if _, err := p.Update(context.Background(), patch(t, `{"shell": {"roots": ["/w/bb"]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if fi2, _ := os.Stat(path); fi2.Size() != fi.Size() {
		t.Fatalf("the rewrite changed the size (%d → %d); the row needs equal sizes", fi.Size(), fi2.Size())
	}
	if ok, err := p.ShellAllowed("/w/bb"); !ok {
		t.Errorf("ShellAllowed(/w/bb) after the update = %v, %v; the cache served the old file", ok, err)
	}
}

// TestUpdateLockWaitHonoursCancel pins the decision that a call waiting for the
// configuration's lock stops when the call is cancelled: the wait is a poll under the
// call's context, not a blocking flock (which nothing can interrupt, so a cancelled
// policy.update would hold its place until the other writer let go). A cancelled wait
// is OPERATION_CANCELLED and leaves the file untouched.
func TestUpdateLockWaitHonoursCancel(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1}`)
	before, _ := raw(t, path)
	lockFile, err := os.OpenFile(filepath.Join(dir, policy.LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	about := make(chan struct{})
	policy.SetLockSeam(func() { close(about) })
	t.Cleanup(func() { policy.SetLockSeam(nil) })
	done := make(chan *capability.Error, 1)
	go func() {
		_, err := policy.New(envAt(dir)).Update(ctx, patch(t, `{"shell": {"enabled": true}}`))
		done <- err
	}()
	<-about
	time.Sleep(50 * time.Millisecond) // the call is polling a lock that stays held
	select {
	case err := <-done:
		t.Fatalf("the update returned %v while the lock was held and the call live", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Code != capability.ErrOperationCancelled {
			t.Fatalf("a cancelled update = %v; want OPERATION_CANCELLED", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("the cancelled update still waits for the lock after 100ms")
	}
	if after, _ := raw(t, path); string(after) != string(before) {
		t.Errorf("a cancelled update changed the file:\n%s", after)
	}
}

// A patch that repeats a key is refused, as a file that repeats one is unparseable: a
// last-wins patch would apply the second value unseen (step 7's verifier).
func TestPatchWithARepeatedKeyIsRefused(t *testing.T) {
	var p policy.UpdateParams
	if err := json.Unmarshal([]byte(`{"shell":{"enabled":false,"enabled":true}}`), &p); err == nil {
		t.Fatal("a patch repeating shell.enabled was accepted")
	}
}

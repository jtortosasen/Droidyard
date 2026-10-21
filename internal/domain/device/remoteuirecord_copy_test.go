package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/hosting"
)

// pendingFixture is an ADB with a recordings directory and a remote record whose copy is
// pending (ended, with the host's size and digest), for the tests of the copy's bookkeeping.
func pendingFixture(t *testing.T) (a *ADB, rec UIRecord, root string, log *strings.Builder) {
	t.Helper()
	r, _, root := newRecordings(t)
	a = &ADB{recordings: r}
	log = &strings.Builder{}
	a.SetLog(log)
	rec = remoteRec(RecordingEnded)
	end := rec.StartedAt.Add(12 * time.Second)
	rec.EndedAt, rec.EndReason, rec.Bytes, rec.SHA256 = &end, EndStopped, 5000, remoteDigest
	if err := r.Save(rec); err != nil {
		t.Fatal(err)
	}
	return a, rec, root, log
}

// Decision (a record that could not be written is not a copy that is done): when the record
// cannot be saved, finishCopy publishes nothing, asks the host to delete nothing and asks for
// another attempt, which finds the renamed timeline and marks the record; when it can, it
// deletes the host's copy. The positive control shows the log line the deletion leaves. Seen
// failing with mutate logging the save error and returning the record as changed (the host was
// asked to delete a recording the client had not recorded as copied).
func TestFinishCopyNeedsTheRecordSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a directory cannot be made unwritable for root")
	}
	t.Run("a record that can be saved is copied and the host asked", func(t *testing.T) {
		a, rec, _, log := pendingFixture(t)
		got := (&recSupervisor{a: a, remote: "qa"}).finishCopy(nil, rec, "")
		after, _, _ := a.recordings.Get(rec.ID)
		if got.retry || !after.Copied || !strings.Contains(log.String(), "was not deleted") {
			t.Errorf("finishCopy = %+v, record copied %v, log %q; want it done and the host asked to delete", got, after.Copied, log.String())
		}
	})
	t.Run("a record that cannot be saved is not", func(t *testing.T) {
		a, rec, root, log := pendingFixture(t)
		dir := filepath.Join(root, "recordings", rec.ID)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		got := (&recSupervisor{a: a, remote: "qa"}).finishCopy(nil, rec, "")
		os.Chmod(dir, 0o700)
		after, _, _ := a.recordings.Get(rec.ID)
		if !got.retry || after.Copied || strings.Contains(log.String(), "was not deleted") {
			t.Errorf("finishCopy = %+v, record copied %v, log %q; want a retry, not copied, and the host not asked", got, after.Copied, log.String())
		}
	})
}

// Decision (a refusal that will not pass is not retried for ever): the host answering 416, a
// file shorter than the part, gives the copy up as a host that does not serve the timeline as
// its summary described; 404 and 410 as a host that no longer holds it; 409 is a recording
// still running and is retried at the base delay; any other status is retried with the delay
// that grows. Seen failing with 416 retried like 409 (the copy asked again every second
// without end).
func TestCopyFailedClassifiesTheHostsRefusals(t *testing.T) {
	cases := []struct {
		describe string
		status   int
		gaveUp   bool
		cause    string
		retry    bool
		slow     bool
	}{
		{"not found", 404, true, causeHostGone, false, false},
		{"gone", 410, true, causeHostGone, false, false},
		{"range not satisfiable", 416, true, causeCopyRefused, false, false},
		{"still running", 409, false, "", true, false},
		{"a host error", 500, false, "", true, true},
		{"a host that is busy", 503, false, "", true, true},
	}
	for _, c := range cases {
		t.Run(c.describe, func(t *testing.T) {
			a, rec, _, _ := pendingFixture(t)
			got := (&recSupervisor{a: a, remote: "qa"}).copyFailed(t.Context(), rec, &hosting.ErrArtifactRefused{Status: c.status})
			after, _, _ := a.recordings.Get(rec.ID)
			if got.retry != c.retry || got.slow != c.slow || after.CopyGaveUp != c.gaveUp || after.Cause != c.cause {
				t.Errorf("status %d: result %+v, gave up %v, cause %q; want retry %v slow %v, gave up %v, cause %q",
					c.status, got, after.CopyGaveUp, after.Cause, c.retry, c.slow, c.gaveUp, c.cause)
			}
		})
	}
}

// Decision (no part in the directory of a record that gave its copy up): a copy that starts
// from a stale copy of the record, after the copy was given up or the record deleted, makes no
// part file and asks for nothing. Seen failing with the part opened without looking at the
// record (a part file in a failed record's directory).
func TestNoPartForARecordThatGaveItsCopyUp(t *testing.T) {
	a, rec, root, _ := pendingFixture(t)
	part := filepath.Join(root, "recordings", rec.ID, copyPart)
	a.giveUpCopy(rec.ID, causeHostGone, true)
	got := (&recSupervisor{a: a, remote: "qa"}).copyOne(t.Context(), nil, rec) // rec is the stale copy
	if got.retry || fileExists(part) {
		t.Errorf("copyOne of a stale record = %+v, part exists %v; want nothing done and no part", got, fileExists(part))
	}
	if err := a.recordings.Delete(rec.ID); err != nil {
		t.Fatal(err)
	}
	got = (&recSupervisor{a: a, remote: "qa"}).copyOne(t.Context(), nil, rec)
	if got.retry || fileExists(filepath.Join(root, "recordings", rec.ID)) {
		t.Errorf("copyOne of a deleted record = %+v; want nothing done and no directory made", got)
	}
}

// Decision (what the host says of time is bounded as the start's is): an end time in the future
// or older than a day is not kept, the record's own or now is; a start time likewise. Seen
// failing with the host's EndedAt taken as it came (a record that ended in the year 3000 sorted
// first for good).
func TestHostTimesAreBounded(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	for name, c := range map[string]struct {
		in *time.Time
		ok bool
	}{
		"now": {at(0), true}, "an hour ago": {at(-time.Hour), true}, "30 s ahead": {at(30 * time.Second), true},
		"an hour ahead": {at(time.Hour), false}, "two days ago": {at(-48 * time.Hour), false}, "unset": {nil, false}, "zero": {&time.Time{}, false},
	} {
		if _, ok := hostTime(c.in, now); ok != c.ok {
			t.Errorf("hostTime(%s) kept = %v; want %v", name, ok, c.ok)
		}
	}
	rec := remoteRec(RecordingRunning)
	hs := hostSummary(RecordingEnded, EndStopped)
	future := time.Now().Add(1000 * 24 * time.Hour)
	hs.EndedAt = &future
	applyHostSummary(&rec, hs, "qa")
	if rec.EndedAt == nil || rec.EndedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("the record ended at %v; want a time that is not in the future", rec.EndedAt)
	}
}

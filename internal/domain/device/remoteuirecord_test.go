package device

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
)

const remoteRecID = "20261021T100000Z-0a1b2c3d"

var remoteDigest = strings.Repeat("ab", 32)

// hostSummary is the host's summary of a recording that ended `reason`, as the client reads it.
func hostSummary(state, reason string) UIRecording {
	end := time.Date(2026, 10, 21, 10, 0, 12, 0, time.UTC)
	s := UIRecording{ID: "20261021T095959Z-deadbeef", Device: "R58M001", State: state, Dumps: 7, Unchanged: 2, Missed: 1,
		LockWaits: 3, Events: 40, Gaps: 1, Bytes: 5000}
	if state == RecordingEnded {
		s.EndedAt, s.EndReason, s.Cause, s.SHA256 = &end, reason, "the host's own words, which are never repeated", remoteDigest
	}
	return s
}

func remoteRec(state string) UIRecord {
	return UIRecord{ID: remoteRecID, Device: "qa/R58M001", Remote: true, HostID: "20261021T095959Z-deadbeef", MaxSeconds: 900,
		StartedBy: RecordingStarter{Client: "mcp", Session: "agent-1"}, State: state, StartedAt: time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)}
}

// Decision (ADR-0115 Decision 5, plan m2x step 10: the client's record mirrors the host's and
// takes its final reason): a running record takes the host's counts and ends when the host's
// recording has, with the host's reason and the daemon's own words for it, never the host's
// cause text (it is another machine's); a record that ended on the client's own account (the
// link lost) and is still to be copied takes the host's final reason, size and digest, and keeps
// its own cause when the reason is the same; a summary that cannot be copied (a digest that is
// not a SHA-256, a size over the host's own bound, none) gives the copy up with the fixed cause
// instead of trusting it; and a record already copied is never touched. Seen failing with the
// host's cause copied into the record (a sentence of the host's in a published field) and with
// the digest taken unchecked (the copy compared against a string that was not a digest).
func TestApplyHostSummary(t *testing.T) {
	t.Run("a running record mirrors the counts", func(t *testing.T) {
		rec := remoteRec(RecordingRunning)
		applyHostSummary(&rec, hostSummary(RecordingRunning, ""), "qa")
		if rec.State != RecordingRunning || rec.Dumps != 7 || rec.Events != 40 || rec.Bytes != 5000 || rec.LockWaits != 3 || rec.SHA256 != "" || rec.EndedAt != nil {
			t.Errorf("record = %+v; want running with the host's counts and no end", rec)
		}
	})
	t.Run("it ends with the host's reason in the daemon's words", func(t *testing.T) {
		rec := remoteRec(RecordingRunning)
		applyHostSummary(&rec, hostSummary(RecordingEnded, EndDeviceGone), "qa")
		if rec.State != RecordingEnded || rec.EndReason != EndDeviceGone || rec.SHA256 != remoteDigest || rec.CopyGaveUp || rec.EndedAt == nil {
			t.Fatalf("record = %+v; want ended deviceGone with the host's digest", rec)
		}
		if strings.Contains(rec.Cause, "host's own words") || !strings.Contains(rec.Cause, "qa") {
			t.Errorf("cause = %q; want the daemon's own words naming the remote, not the host's", rec.Cause)
		}
	})
	t.Run("a reason that is not published is failed", func(t *testing.T) {
		rec := remoteRec(RecordingRunning)
		applyHostSummary(&rec, hostSummary(RecordingEnded, "somethingNew"), "qa")
		if rec.EndReason != EndFailed || rec.Cause != "the recording failed on the host qa" {
			t.Errorf("reason %q cause %q; want failed in the daemon's words", rec.EndReason, rec.Cause)
		}
	})
	t.Run("a record ended by the lost link takes the host's final reason", func(t *testing.T) {
		rec := remoteRec(RecordingEnded)
		end := time.Date(2026, 10, 21, 10, 0, 5, 0, time.UTC)
		rec.EndedAt, rec.EndReason, rec.Cause = &end, EndDeviceGone, "the link to qa was lost"
		applyHostSummary(&rec, hostSummary(RecordingEnded, EndMaxDuration), "qa")
		if rec.EndReason != EndMaxDuration || rec.Cause != "" || rec.SHA256 != remoteDigest || rec.Bytes != 5000 || rec.Dumps != 7 {
			t.Errorf("record = %+v; want maxDuration, no cause, the host's digest, size and counts", rec)
		}
		// The same reason keeps the client's own cause, which says more.
		again := remoteRec(RecordingEnded)
		again.EndedAt, again.EndReason, again.Cause = &end, EndDeviceGone, "the link to qa was lost"
		applyHostSummary(&again, hostSummary(RecordingEnded, EndDeviceGone), "qa")
		if again.EndReason != EndDeviceGone || again.Cause != "the link to qa was lost" {
			t.Errorf("record = %+v; want deviceGone with the link's cause kept", again)
		}
	})
	t.Run("a host that still runs it changes only the counts", func(t *testing.T) {
		rec := remoteRec(RecordingEnded)
		end := time.Date(2026, 10, 21, 10, 0, 5, 0, time.UTC)
		rec.EndedAt, rec.EndReason, rec.Cause = &end, EndDeviceGone, "the link to qa was lost"
		applyHostSummary(&rec, hostSummary(RecordingRunning, ""), "qa")
		if rec.State != RecordingEnded || rec.EndReason != EndDeviceGone || rec.SHA256 != "" || rec.Dumps != 7 {
			t.Errorf("record = %+v; want ended deviceGone still, with no digest yet", rec)
		}
	})
	t.Run("a summary that cannot be copied gives the copy up", func(t *testing.T) {
		for name, edit := range map[string]func(*UIRecording){
			"a digest that is not one":   func(s *UIRecording) { s.SHA256 = "ABC" },
			"no digest":                  func(s *UIRecording) { s.SHA256 = "" },
			"an upper-case digest":       func(s *UIRecording) { s.SHA256 = strings.ToUpper(remoteDigest) },
			"no bytes":                   func(s *UIRecording) { s.Bytes = 0 },
			"a negative size":            func(s *UIRecording) { s.Bytes = -5 },
			"a size over the host's cap": func(s *UIRecording) { s.Bytes = recordMaxBytes + 1 },
		} {
			rec := remoteRec(RecordingRunning)
			hs := hostSummary(RecordingEnded, EndStopped)
			edit(&hs)
			applyHostSummary(&rec, hs, "qa")
			if !rec.CopyGaveUp || rec.EndReason != EndFailed || rec.Cause != causeCopyBad || rec.SHA256 != "" || pendingCopy(rec) {
				t.Errorf("%s: record = %+v; want the copy given up, failed with the fixed cause", name, rec)
			}
		}
	})
	t.Run("a copied record is left as it is", func(t *testing.T) {
		rec := remoteRec(RecordingEnded)
		rec.Copied, rec.EndReason, rec.Bytes, rec.SHA256 = true, EndStopped, 5000, remoteDigest
		before := rec
		applyHostSummary(&rec, hostSummary(RecordingEnded, EndDeviceGone), "qa")
		if rec != before {
			t.Errorf("record = %+v; want %+v untouched", rec, before)
		}
	})
	t.Run("counts from the host are bounded", func(t *testing.T) {
		rec := remoteRec(RecordingRunning)
		hs := hostSummary(RecordingRunning, "")
		hs.Dumps, hs.Events, hs.Bytes = -1, -9, 1<<62
		applyHostSummary(&rec, hs, "qa")
		if rec.Dumps != 0 || rec.Events != 0 || rec.Bytes != recordMaxBytes {
			t.Errorf("record = %+v; want negative counts 0 and the size at the host's own cap", rec)
		}
	})
}

// Decision (ADR-0115 Decision 5: until the copy matched, the reference reads growing and the
// stop and the list say to wait for `copied`): a remote recording's text has no `save` line
// before it is copied, but one that says what to wait for, or why it will not come (the fixed
// causes); once copied it is the local recording's own text. Seen failing with the save line on
// a remote recording whose timeline was not here.
func TestRemoteRecordingRenderingsAreTheGoldenText(t *testing.T) {
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	end := start.Add(12400 * time.Millisecond)
	ended := UIRecording{
		ID: remoteRecID, Device: "qa/R58M001", MaxSeconds: 900, StartedBy: PublishedStarter{Client: "mcp", Session: "s1"},
		State: RecordingEnded, StartedAt: start, EndedAt: &end, EndReason: EndStopped,
		Dumps: 14, Unchanged: 3, Missed: 1, LockWaits: 2, Events: 212, Gaps: 1, Bytes: 1843201, SHA256: "ab12",
		Timeline: artifact.TimelineRef(remoteRecID, 1843201), Remote: true,
	}
	golden(t, "testdata/uirecording-remote-pending.golden", stopUIRecordingText(StopUIRecordingResult{Stopped: true, Recording: &ended}))
	gaveUp := ended
	gaveUp.EndReason, gaveUp.Cause = EndFailed, causeCopyMismatch
	golden(t, "testdata/uirecording-remote-gaveup.golden", uiRecordingText(gaveUp))
	copied := ended
	copied.Copied = true
	golden(t, "testdata/uirecording-remote-copied.golden", uiRecordingText(copied))
	if got := uiRecordingText(copied); !strings.Contains(got, "save") || strings.Contains(got, "copy") {
		t.Errorf("a copied remote recording renders %q; want the save line", got)
	}
	// A running one has no timeline here yet either.
	running := ended
	running.State, running.EndedAt, running.EndReason = RecordingRunning, nil, ""
	if got := uiRecordingText(running); strings.Contains(got, "save") || !strings.Contains(got, "copied=true") {
		t.Errorf("a running remote recording renders %q; want the wait for copied, no save line", got)
	}
}

// Decision (ADR-0115 Decision 5, plan m2x step 10): retention does not sweep a remote record
// whose timeline is still to be copied, as it does not sweep a running one, and does sweep it
// once the copy is given up or done. Seen failing with the pending record swept like any ended
// one (the copy's directory was removed under it).
func TestSweepHoldsARemoteRecordWhoseCopyIsPending(t *testing.T) {
	r, _, root := newRecordings(t)
	r.MaxRecordings, r.MaxBytes = 1, 1<<40
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	seed := func(i int, edit func(*UIRecord)) {
		end := base.Add(time.Duration(i) * time.Hour)
		rec := UIRecord{ID: recID(i), Device: "qa/R58M001", Remote: true, HostID: recID(i + 20), MaxSeconds: 900,
			StartedBy: RecordingStarter{Client: "ui"}, State: RecordingEnded, StartedAt: end, EndedAt: &end, EndReason: EndStopped, Bytes: 10}
		edit(&rec)
		if err := r.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	seed(1, func(r *UIRecord) {})                      // pending
	seed(2, func(r *UIRecord) { r.Copied = true })     // done
	seed(3, func(r *UIRecord) { r.CopyGaveUp = true }) // given up
	seed(4, func(r *UIRecord) { r.Copied = true })     // done, the newest
	gone, err := r.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gone, ",") != recID(2)+","+recID(3) {
		t.Errorf("swept %v; want the copied and the given-up ones that are not the newest, and not the pending one", gone)
	}
	if !fileExists(root + "/recordings/" + recID(1)) {
		t.Error("the record whose copy is pending was swept")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

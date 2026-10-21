package device

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/store"
)

func newRecordings(t *testing.T) (*UIRecordings, *artifact.Store, string) {
	t.Helper()
	root := t.TempDir()
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	art, err := artifact.New(artifact.Options{Epoch: "d-0a1b2c3d", StateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(art.Close)
	return NewUIRecordings(st), art, root
}

func recID(i int) string { return fmt.Sprintf("202610%02dT101500Z-%08x", i, i) }

func startRecord(t *testing.T, r *UIRecordings, art *artifact.Store, id string, at time.Time) *TimelineWriter {
	t.Helper()
	rec := UIRecord{ID: id, Device: "emulator-5554", MaxSeconds: 900, State: RecordingRunning, StartedAt: at,
		StartedBy: RecordingStarter{Client: "ui", Session: "s1", Chat: "chat-secret"}}
	if err := r.Save(rec); err != nil {
		t.Fatal(err)
	}
	d, err := art.CreateTimeline(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return NewTimelineWriter(d)
}

// endRecord closes the writer and finalises the record, as the recorder does.
func endRecord(t *testing.T, r *UIRecordings, w *TimelineWriter, id string, at time.Time, bytes int64) {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rec, found, err := r.Get(id)
	if err != nil || !found {
		t.Fatalf("Get %s: %v %v", id, found, err)
	}
	rec.StartedAt, rec.EndReason, rec.Bytes = at, EndStopped, bytes
	if err := r.Finalise(rec); err != nil {
		t.Fatal(err)
	}
}

// Decision (ADR-0113 §5): after a crash the next start finalises a running record with
// whole lines and daemonExit. The cut is at the last newline: without the truncation the
// half-written line stays in the file and the "end" line is glued onto it, so a parser
// reading line by line fails on the one line the recovery made.
func TestRecoverFinalisesARunningRecord(t *testing.T) {
	r, art, root := newRecordings(t)
	id := recID(1)
	w := startRecord(t, r, art, id, time.Now())
	if err := w.Line(RecordingLine{Kind: "recording", ID: id, Device: "emulator-5554", Format: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Line(DumpLine{Kind: "dump", N: 1, Trigger: "start", XML: "<hierarchy/>"}); err != nil {
		t.Fatal(err)
	}
	// The crash: a third line half written, no newline; the handle is never closed.
	path := filepath.Join(root, "recordings", id, "timeline.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"dump","n":2,"xml":"<hier`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	mtime := time.Date(2026, 10, 21, 10, 20, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Finalised) != 1 || rep.Finalised[0] != id {
		t.Fatalf("report = %+v", rep)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var l struct {
			Kind, HostTime, Time, Reason string
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("line %q is not whole JSON: %v", sc.Text(), err)
		}
		kinds = append(kinds, l.Kind)
		if l.Kind == "end" {
			if l.Reason != "daemonExit" || l.Time != "" {
				t.Errorf("end = %+v, want daemonExit and no device time", l)
			}
			if got, _ := time.Parse(time.RFC3339Nano, l.HostTime); !got.Equal(mtime) {
				t.Errorf("hostTime = %q, want the file's mtime %v", l.HostTime, mtime)
			}
		}
	}
	if strings.Join(kinds, ",") != "recording,dump,end" || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("kinds = %v, trailing newline %v; want recording,dump,end", kinds, bytes.HasSuffix(data, []byte("\n")))
	}
	rec, _, _ := r.Get(id)
	sum := sha256.Sum256(data)
	if rec.State != RecordingEnded || rec.EndReason != EndDaemonExit || rec.EndedAt == nil ||
		rec.Bytes != int64(len(data)) || rec.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("record = %+v, want ended, daemonExit and the file's size and sha256", rec)
	}
	// It recovered once: a second start finds nothing running.
	if rep, _ := r.Recover(); len(rep.Finalised) != 0 {
		t.Fatalf("second recover finalised %v", rep.Finalised)
	}
}

// Decision (plan step 4 fix B1): recovery touches only <root>/recordings/<dir>/timeline.jsonl
// where <dir> is the directory's own name. A timeline that is a link to a file elsewhere
// leaves that file byte-identical; a record whose id names another directory, or none,
// creates nothing anywhere and is skipped.
func TestRecoverNeverLeavesItsDirectory(t *testing.T) {
	r, _, root := newRecordings(t)
	rd := filepath.Join(root, "recordings")
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mk := func(dirID, recID string) string {
		dir := filepath.Join(rd, dirID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		js := `{"schemaVersion":1,"id":"` + recID + `","state":"recording"}`
		if err := os.WriteFile(filepath.Join(dir, "recording.json"), []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	linked := mk(recID(1), recID(1))
	if err := os.Symlink(outside, filepath.Join(linked, "timeline.jsonl")); err != nil {
		t.Fatal(err)
	}
	liar := mk(recID(2), recID(5)) // claims another recording's id
	empty := mk(recID(3), "")      // claims none
	before, _ := os.ReadDir(rd)

	rep, err := r.Recover()
	if err == nil {
		t.Fatal("the linked timeline was not reported as an error")
	}
	if b, _ := os.ReadFile(outside); string(b) != "precious\n" {
		t.Fatalf("the outside file was written: %q", b)
	}
	if len(rep.Finalised) != 0 || len(rep.Skipped) != 2 {
		t.Fatalf("report = %+v, want nothing finalised and two skipped", rep)
	}
	for _, d := range []string{liar, empty} {
		if _, err := os.Stat(filepath.Join(d, "timeline.jsonl")); !os.IsNotExist(err) {
			t.Errorf("%s got a timeline: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(rd, recID(5))); !os.IsNotExist(err) {
		t.Errorf("a directory was created for the id a record claimed: %v", err)
	}
	after, _ := os.ReadDir(rd)
	if len(after) != len(before) {
		t.Fatalf("recordings/ went from %d to %d entries", len(before), len(after))
	}
}

// Decision (plan step 4 fix N1): one bad record does not leave the ones after it `recording`
// forever; and a timeline already ending in an `end` line gets no second one.
func TestRecoverContinuesPastABadRecord(t *testing.T) {
	r, art, root := newRecordings(t)
	bad := recID(1) // sorts first
	badDir := filepath.Join(root, "recordings", bad)
	if err := os.MkdirAll(filepath.Join(badDir, "timeline.jsonl"), 0o700); err != nil { // a directory
		t.Fatal(err)
	}
	js := `{"schemaVersion":1,"id":"` + bad + `","state":"recording"}`
	if err := os.WriteFile(filepath.Join(badDir, "recording.json"), []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	good := recID(2)
	w := startRecord(t, r, art, good, time.Now())
	_ = w.Line(RecordingLine{Kind: "recording"})
	_ = w.Line(EndLine{Kind: "end", HostTime: "2026-10-21T10:00:00Z", Reason: EndMaxBytes})

	rep, err := r.Recover()
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want the bad record named", err)
	}
	if len(rep.Finalised) != 1 || rep.Finalised[0] != good {
		t.Fatalf("report = %+v, want the good one finalised", rep)
	}
	data, _ := os.ReadFile(filepath.Join(root, "recordings", good, "timeline.jsonl"))
	if n := strings.Count(string(data), `"kind":"end"`); n != 1 {
		t.Fatalf("%d end lines, want the recorder's alone:\n%s", n, data)
	}
	rec, _, _ := r.Get(good)
	if rec.State != RecordingEnded || rec.EndReason != EndMaxBytes {
		t.Fatalf("record = %+v, want ended with the end line's own reason", rec)
	}
}

// Decision (ADR-0012, the owner rule): a directory with no readable record has no owner and
// is deleted; a directory not named like a recording is not ours and stays; a record from a
// newer schema is left untouched (ADR-0056); a remote one still receiving is its lease's.
func TestRecoverDeletesAnOwnerlessDirectory(t *testing.T) {
	r, art, root := newRecordings(t)
	rd := filepath.Join(root, "recordings")
	mk := func(id, record string) string {
		dir := filepath.Join(rd, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "timeline.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if record != "" {
			if err := os.WriteFile(filepath.Join(dir, "recording.json"), []byte(record), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	noRecord := mk(recID(1), "")
	corrupt := mk(recID(2), "{not json")
	newer := mk(recID(3), `{"schemaVersion":99,"id":"x","state":"recording"}`)
	remote := mk(recID(4), `{"schemaVersion":1,"id":"`+recID(4)+`","remote":true,"state":"recording"}`)
	foreign := filepath.Join(rd, "keep-me")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = art

	rep, err := r.Recover()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{noRecord, corrupt} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", d, err)
		}
	}
	for _, d := range []string{newer, remote, foreign} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed: %v", d, err)
		}
	}
	if len(rep.Deleted) != 2 || len(rep.Finalised) != 0 {
		t.Fatalf("report = %+v, want two deleted and none finalised", rep)
	}
	if b, _ := os.ReadFile(filepath.Join(newer, "recording.json")); !strings.Contains(string(b), `"schemaVersion":99`) {
		t.Fatalf("the newer record was rewritten: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(newer, "timeline.jsonl")); string(b) != "{}\n" {
		t.Fatalf("the newer record's timeline was touched: %q", b)
	}
}

// Decision (ADR-0113 §5): retention is 50 recordings and 2 GiB of ended ones, oldest
// first, and a running recording is never swept however old it is.
func TestSweepKeepsTheRunningAndTheNewest(t *testing.T) {
	r, art, root := newRecordings(t)
	r.MaxRecordings, r.MaxBytes = 3, 1000
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	// recID(1) is the oldest and still running; 2..6 ended, 100 bytes each.
	startRecord(t, r, art, recID(1), base).Line(RecordingLine{Kind: "recording"})
	for i := 2; i <= 6; i++ {
		w := startRecord(t, r, art, recID(i), base.Add(time.Duration(i)*time.Hour))
		endRecord(t, r, w, recID(i), base.Add(time.Duration(i)*time.Hour), 100)
	}
	gone, err := r.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	// Five ended against a limit of three: the two oldest ended go; the running one stays.
	if strings.Join(gone, ",") != recID(2)+","+recID(3) {
		t.Fatalf("swept %v, want the two oldest ended", gone)
	}
	for i, want := range map[int]bool{1: true, 2: false, 3: false, 4: true, 5: true, 6: true} {
		_, err := os.Stat(filepath.Join(root, "recordings", recID(i)))
		if (err == nil) != want {
			t.Errorf("recording %d present = %v, want %v", i, err == nil, want)
		}
	}
	// By size: 3 ended at 100 bytes with a 250-byte limit leaves two (newest).
	r.MaxRecordings, r.MaxBytes = 50, 250
	gone, _ = r.Sweep()
	if len(gone) != 1 || gone[0] != recID(4) {
		t.Fatalf("size sweep deleted %v, want only the oldest ended (%s)", gone, recID(4))
	}
	if _, err := os.Stat(filepath.Join(root, "recordings", recID(1))); err != nil {
		t.Fatalf("the running recording was swept: %v", err)
	}
}

// Decision (ADR-0113 §4, ADR-0088 §4): the chat id is on disk for the stop rule only;
// anything published names {client, session?}. Delete refuses a running recording.
func TestRecordKeepsTheChatOnDiskButNeverPublishesIt(t *testing.T) {
	r, art, root := newRecordings(t)
	id := recID(7)
	w := startRecord(t, r, art, id, time.Now())
	raw, _ := os.ReadFile(filepath.Join(root, "recordings", id, "recording.json"))
	if !strings.Contains(string(raw), "chat-secret") || !strings.Contains(string(raw), `"schemaVersion": 1`) {
		t.Fatalf("recording.json = %s, want the chat kept and schemaVersion 1", raw)
	}
	rec, _, _ := r.Get(id)
	pub, _ := json.Marshal(rec.StartedBy.Published())
	if string(pub) != `{"client":"ui","session":"s1"}` {
		t.Fatalf("published starter = %s", pub)
	}
	if err := r.Delete(id); !errors.Is(err, ErrRecordingRunning) {
		t.Fatalf("Delete of a running recording = %v, want ErrRecordingRunning", err)
	}
	endRecord(t, r, w, id, time.Now(), 0)
	if err := r.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := art.Get("droidyard://artifact/r." + id); err == nil {
		t.Fatal("a deleted recording's reference still resolves")
	}
	if _, found, _ := r.Get(id); found {
		t.Fatal("a deleted recording's record is still there")
	}
}

// Decision (ADR-0113 §5): the record and the timeline are owner-only (the at-rest posture
// for device text), and the record is written before the timeline exists.
func TestRecordFilesAre0600(t *testing.T) {
	r, art, root := newRecordings(t)
	id := recID(8)
	if err := r.Save(UIRecord{ID: id, State: RecordingRunning}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "recordings", id, "timeline.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the timeline exists before the recorder made it: %v", err)
	}
	d, _ := art.CreateTimeline(id, 0)
	d.Close()
	dir := filepath.Join(root, "recordings", id)
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "recording.json"): 0o600, filepath.Join(dir, "timeline.jsonl"): 0o600} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", p, fi, err, want)
		}
	}
}

// Decision (ADR-0113 §5, plan Q3): the writer refuses a line past the cap with nothing cut,
// and a dump's XML goes out as the reader sent it (no HTML escaping), one line per record.
func TestTimelineWriterRefusesALinePastTheCap(t *testing.T) {
	_, art, root := newRecordings(t)
	d, err := art.CreateTimeline(recID(9), 400)
	if err != nil {
		t.Fatal(err)
	}
	w := NewTimelineWriter(d)
	xml := "<node text=\"a & b\">\n\t<x/></node>"
	if err := w.Line(DumpLine{Kind: "dump", N: 1, XML: xml}); err != nil {
		t.Fatal(err)
	}
	before := w.Size()
	err = w.Line(DumpLine{Kind: "dump", N: 2, XML: strings.Repeat("x", 500)})
	if !errors.Is(err, artifact.ErrTimelineFull) {
		t.Fatalf("err = %v, want ErrTimelineFull", err)
	}
	if w.Size() != before {
		t.Fatalf("a refused line grew the file from %d to %d", before, w.Size())
	}
	w.Close()
	data, _ := os.ReadFile(filepath.Join(root, "recordings", recID(9), "timeline.jsonl"))
	if bytes.Count(data, []byte("\n")) != 1 || strings.Contains(string(data), "\\u0026") {
		t.Fatalf("file = %q: want one line, XML not HTML-escaped", data)
	}
	var back DumpLine
	if err := json.Unmarshal(bytes.TrimSpace(data), &back); err != nil || back.XML != xml {
		t.Fatalf("round trip = %q, %v; want the XML byte for byte", back.XML, err)
	}
}

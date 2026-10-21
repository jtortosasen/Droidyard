package device

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/store"
)

// The recordings store (ADR-0113 §5): <state>/recordings/<id>/recording.json, written
// through internal/store before the timeline exists, and timeline.jsonl beside it
// (internal/artifact). This file is the record, the line formats of the timeline, and the
// three things the daemon does to the directory as a whole: recover after a crash, sweep
// by retention, delete.

// Recording states.
const (
	RecordingRunning = "recording"
	RecordingEnded   = "ended"
)

// Ends of a recording (ADR-0113 §3).
const (
	EndStopped     = "stopped"
	EndMaxDuration = "maxDuration"
	EndMaxDumps    = "maxDumps"
	EndMaxBytes    = "maxBytes"
	EndDeviceGone  = "deviceGone"
	EndFailed      = "failed"
	EndDaemonExit  = "daemonExit"
)

// Retention (ADR-0113 §5): ended recordings only; a running one is never swept and may add
// up to 512 MiB beyond.
const (
	RecordingsKept     = 50
	RecordingsKeptSize = 2 << 30
)

// RecordingStarter is who started a recording, kept on disk for the stop rule only
// (ADR-0113 §4). Chat is ADR-0088's chat id: it never leaves the record; Published drops it.
type RecordingStarter struct {
	Client  string `json:"client"`
	Session string `json:"session,omitempty"`
	Chat    string `json:"chat,omitempty"`
	Peer    string `json:"peer,omitempty"`
}

// PublishedStarter is startedBy as anything published carries it (ADR-0048 §1): the client
// kind and the session, never the chat id (ADR-0088 §4) nor a peer's name.
type PublishedStarter struct {
	Client  string `json:"client"`
	Session string `json:"session,omitempty"`
}

// Published is the starter without the chat id.
func (s RecordingStarter) Published() PublishedStarter {
	return PublishedStarter{Client: s.Client, Session: s.Session}
}

// UIRecord is recording.json (schemaVersion 1).
type UIRecord struct {
	ID         string           `json:"id"`
	Device     string           `json:"device"`
	Remote     bool             `json:"remote,omitempty"`
	HostID     string           `json:"hostId,omitempty"`
	Invisible  bool             `json:"invisible"`
	MaxSeconds int              `json:"maxSeconds"`
	StartedBy  RecordingStarter `json:"startedBy"`
	State      string           `json:"state"`
	StartedAt  time.Time        `json:"startedAt"`
	EndedAt    *time.Time       `json:"endedAt,omitempty"`
	EndReason  string           `json:"endReason,omitempty"`
	Cause      string           `json:"cause,omitempty"`
	Dumps      int              `json:"dumps"`
	Unchanged  int              `json:"unchanged"`
	Missed     int              `json:"missed"`
	LockWaits  int              `json:"lockWaits"`
	Events     int              `json:"events"`
	Gaps       int              `json:"gaps"`
	Bytes      int64            `json:"bytes"`
	SHA256     string           `json:"sha256,omitempty"`
	Copied     bool             `json:"copied,omitempty"`
}

// The timeline's lines (plan m2x Q3), in write order. `time` is the device clock; a dump is
// the screen at an instant within [time, time+ms]; hostTime is the host's.

// RecordingLine is the header.
type RecordingLine struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	Device          string `json:"device"`
	Invisible       bool   `json:"invisible"`
	HostStartedAt   string `json:"hostStartedAt"`
	DeviceStartedAt string `json:"deviceStartedAt,omitempty"`
	Format          int    `json:"format"`
}

// EventLine is one event, with device.uiEvents's fields.
type EventLine struct {
	Kind string `json:"kind"`
	UIEvent
}

// GapLine says events were lost.
type GapLine struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Dropped int    `json:"dropped,omitempty"`
}

// DumpLine is one stored dump; XML is the reader's `dump full` document, byte for byte.
type DumpLine struct {
	Kind       string `json:"kind"`
	N          int    `json:"n"`
	Time       string `json:"time"`
	Ms         int    `json:"ms"`
	WaitedMs   int    `json:"waitedMs"`
	Trigger    string `json:"trigger"`
	Nodes      int    `json:"nodes"`
	Foreground string `json:"foreground,omitempty"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	Warning    string `json:"warning,omitempty"`
	XML        string `json:"xml"`
}

// MissedLine says a dump was lost: past the bound, over 8 MiB, unreadable, or the last one,
// at a stop, not taken (the lock was held, or the reader was in its pause).
type MissedLine struct {
	Kind   string `json:"kind"`
	Time   string `json:"time,omitempty"`
	Reason string `json:"reason"`
}

// EndLine closes the timeline. Time is the device clock and absent when the device is not
// there to ask (a crash, a gone device).
type EndLine struct {
	Kind     string `json:"kind"`
	Time     string `json:"time,omitempty"`
	HostTime string `json:"hostTime"`
	Reason   string `json:"reason"`
	Cause    string `json:"cause,omitempty"`
}

// TimelineWriter writes the timeline's lines. A line that would pass the cap is refused
// with artifact.ErrTimelineFull and nothing is written: the recorder then ends maxBytes.
type TimelineWriter struct {
	d *artifact.Durable
	// limit, when above zero, is the size no line but the last may take the file past
	// (Reserve): the room under the cap that End has to itself.
	limit int64
}

// NewTimelineWriter wraps the handle artifact.Store.CreateTimeline returned.
func NewTimelineWriter(d *artifact.Durable) *TimelineWriter { return &TimelineWriter{d: d} }

// Reserve keeps room bytes of the handle's cap (the one CreateTimeline got) for End: Line
// refuses, with artifact.ErrTimelineFull, any line that would leave less than room under it,
// so a timeline that reached its cap still ends with its `end` line (ADR-0113 §3, §5).
func (w *TimelineWriter) Reserve(cap, room int64) {
	if cap > 0 && room > 0 && room < cap {
		w.limit = cap - room
	}
}

// Line appends v as one JSON line. HTML characters are not escaped, so a dump's XML stays
// as readable as the reader sent it.
func (w *TimelineWriter) Line(v any) error { return w.write(v, w.limit) }

// End appends the closing line: it may use the room Reserve kept, and is refused only past
// the handle's own cap.
func (w *TimelineWriter) End(v any) error { return w.write(v, 0) }

func (w *TimelineWriter) write(v any, limit int64) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	line := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if limit > 0 && w.d.Size()+int64(len(line))+1 > limit {
		return artifact.ErrTimelineFull
	}
	return w.d.WriteLine(line)
}

// Size, SHA256 and Close pass through to the handle.
func (w *TimelineWriter) Size() int64    { return w.d.Size() }
func (w *TimelineWriter) SHA256() string { return w.d.SHA256() }
func (w *TimelineWriter) Close() error   { return w.d.Close() }

// UIRecordings is the recordings directory of one state root.
type UIRecordings struct {
	st *store.Store
	// MaxRecordings and MaxBytes are the retention Sweep applies (the defaults are
	// RecordingsKept and RecordingsKeptSize).
	MaxRecordings int
	MaxBytes      int64
	now           func() time.Time
}

// NewUIRecordings returns the recordings of the state root st holds.
func NewUIRecordings(st *store.Store) *UIRecordings {
	return &UIRecordings{st: st, MaxRecordings: RecordingsKept, MaxBytes: RecordingsKeptSize, now: time.Now}
}

func (r *UIRecordings) dir(id string) string {
	return filepath.Join(r.st.Root(), artifact.RecordingsDir, id)
}

func recordName(id string) string { return artifact.RecordingsDir + "/" + id + "/recording.json" }

func checkRecordingID(id string) error {
	if !artifact.ValidTimelineID(id) {
		return fmt.Errorf("%q is not a recording id", id)
	}
	return nil
}

// Save writes the record atomically. A recording's record is written before its timeline.
func (r *UIRecordings) Save(rec UIRecord) error {
	if err := checkRecordingID(rec.ID); err != nil {
		return err
	}
	return r.st.Write(recordName(rec.ID), rec)
}

// Get reads one record; found is false when there is none.
func (r *UIRecordings) Get(id string) (rec UIRecord, found bool, err error) {
	if err := checkRecordingID(id); err != nil {
		return UIRecord{}, false, err
	}
	out, err := r.st.Read(recordName(id), store.Version, &rec)
	if err != nil {
		return UIRecord{}, false, err
	}
	return rec, out == store.Loaded, nil
}

// List is every readable record, newest first.
func (r *UIRecordings) List() ([]UIRecord, error) {
	ids, err := r.ids()
	if err != nil {
		return nil, err
	}
	var out []UIRecord
	for _, id := range ids {
		rec, found, err := r.Get(id)
		if err != nil || !found {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// ids lists the directories under recordings/ named like a recording id, oldest id first;
// anything else there is not ours and is left alone.
func (r *UIRecordings) ids() ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(r.st.Root(), artifact.RecordingsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() && artifact.ValidTimelineID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// Finalise records that rec ended: state ended, EndedAt (now when unset), and the end
// reason, which must be set. The caller closed the timeline first and set Bytes and SHA256.
func (r *UIRecordings) Finalise(rec UIRecord) error {
	if rec.EndReason == "" {
		return errors.New("a finished recording needs an end reason")
	}
	rec.State = RecordingEnded
	if rec.EndedAt == nil {
		t := r.now().UTC()
		rec.EndedAt = &t
	}
	return r.Save(rec)
}

// Delete removes a recording: its record, its timeline and its directory; a missing one is
// not an error. Whether a live recorder still writes it is the caller's to check (ADB
// refuses one it holds): a record left `recording` with no recorder, as a failed Finalise
// leaves, must stay deletable.
func (r *UIRecordings) Delete(id string) error {
	if err := checkRecordingID(id); err != nil {
		return err
	}
	return os.RemoveAll(r.dir(id))
}

// RecoverReport is what Recover did.
type RecoverReport struct {
	Finalised []string
	Deleted   []string
	Skipped   []string // a running record whose id disagrees with its directory
}

// Recover runs at startup. A local record still `recording` was cut by a crash: its
// timeline is truncated to the last newline (a half-written line is dropped, so every line
// that remains is whole), an `end` line is appended with hostTime the file's last
// modification and no device time, and the record becomes ended, daemonExit. A directory
// without a readable record is deleted (ADR-0012's owner rule); a record with a newer
// schemaVersion is left untouched (ADR-0056); a remote one still receiving belongs to its
// lease (step 10) and is left.
//
// One bad record does not stop the others: each id is handled on its own and the errors are
// joined. The path is always built from the directory's name, never from the id the record
// claims; a record whose id disagrees with its directory (or has none) is skipped.
func (r *UIRecordings) Recover() (RecoverReport, error) {
	var rep RecoverReport
	ids, err := r.ids()
	if err != nil {
		return rep, err
	}
	var errs []error
	for _, id := range ids {
		var rec UIRecord
		out, err := r.st.Read(recordName(id), store.Version, &rec)
		switch {
		case errors.Is(err, store.ErrNewer) || out == store.Newer:
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("recording %s: %w", id, err))
			continue
		case out != store.Loaded: // absent, or corrupt and moved aside
			if err := os.RemoveAll(r.dir(id)); err != nil {
				errs = append(errs, fmt.Errorf("recording %s: %w", id, err))
				continue
			}
			rep.Deleted = append(rep.Deleted, id)
			continue
		}
		if rec.State != RecordingRunning || rec.Remote {
			continue
		}
		if rec.ID != id {
			rep.Skipped = append(rep.Skipped, id)
			continue
		}
		if err := r.finaliseCrashed(id, &rec); err != nil {
			errs = append(errs, fmt.Errorf("recording %s: %w", id, err))
			continue
		}
		rep.Finalised = append(rep.Finalised, id)
	}
	return rep, errors.Join(errs...)
}

func (r *UIRecordings) finaliseCrashed(id string, rec *UIRecord) error {
	path := filepath.Join(r.dir(id), artifact.TimelineFile)
	// Only a regular file this store wrote, or none: a link planted in its place is
	// never followed (O_NOFOLLOW closes the race between this check and the open).
	if li, err := os.Lstat(path); err == nil && !li.Mode().IsRegular() {
		return errors.New("the timeline is not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("the timeline is not a regular file")
	}
	hostTime := fi.ModTime().UTC()
	keep, err := lastNewline(f, fi.Size())
	if err != nil {
		return err
	}
	if err := f.Truncate(keep); err != nil {
		return err
	}
	// The counts the dead recorder held only in memory, recounted from the whole lines kept.
	// `unchanged` was never written to the file and stays 0.
	if err := recountLines(f, keep, rec); err != nil {
		return err
	}
	endReason := EndDaemonExit
	if reason, ok := lastLineEnd(f, keep); ok {
		// The recorder had already written its end line: no second one.
		endReason = reason
	} else {
		end, err := json.Marshal(EndLine{Kind: "end", HostTime: hostTime.Format(time.RFC3339Nano), Reason: EndDaemonExit})
		if err != nil {
			return err
		}
		if _, err := f.WriteAt(append(end, '\n'), keep); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(sum, f)
	if err != nil {
		return err
	}
	rec.Bytes, rec.SHA256 = n, hex.EncodeToString(sum.Sum(nil))
	rec.EndReason = endReason
	t := hostTime
	rec.EndedAt = &t
	return r.Finalise(*rec)
}

// recountLines sets rec's dumps, events, gaps and missed from the first size bytes of f, which
// end at a newline.
func recountLines(f *os.File, size int64, rec *UIRecord) error {
	rec.Dumps, rec.Events, rec.Gaps, rec.Missed = 0, 0, 0, 0
	br := bufio.NewReader(io.NewSectionReader(f, 0, size))
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var l struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(line, &l) == nil {
				switch l.Kind {
				case "dump":
					rec.Dumps++
				case "event":
					rec.Events++
				case "gap":
					rec.Gaps++
				case "missed":
					rec.Missed++
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// lastNewline is the length of the file's prefix that ends at its last newline (0 when it
// holds none).
func lastNewline(f *os.File, size int64) (int64, error) {
	const chunk = 64 << 10
	buf := make([]byte, chunk)
	for end := size; end > 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		b := buf[:end-start]
		if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

// lastLineEnd reports whether the last whole line of the first size bytes of f is an `end`
// line, and its reason.
func lastLineEnd(f *os.File, size int64) (string, bool) {
	if size == 0 {
		return "", false
	}
	before, err := lastNewline(f, size-1) // the newline before the final line
	if err != nil {
		return "", false
	}
	line := make([]byte, size-1-before)
	if _, err := f.ReadAt(line, before); err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	var l struct{ Kind, Reason string }
	if json.Unmarshal(line, &l) != nil || l.Kind != "end" {
		return "", false
	}
	return l.Reason, true
}

// Sweep deletes ended recordings, oldest first, until at most MaxRecordings remain and
// their bytes are within MaxBytes. A running recording is never deleted; the count and the
// size are of the ended ones (a running one may add up to 512 MiB beyond). It returns the
// ids it deleted.
func (r *UIRecordings) Sweep() ([]string, error) {
	recs, err := r.List()
	if err != nil {
		return nil, err
	}
	// Newest ended first: a long recording that started before others is as new as it ended.
	var ended []UIRecord
	var total int64
	for _, rec := range recs {
		if rec.State == RecordingEnded {
			ended = append(ended, rec)
			total += rec.Bytes
		}
	}
	endedAt := func(r UIRecord) time.Time {
		if r.EndedAt != nil {
			return *r.EndedAt
		}
		return r.StartedAt
	}
	sort.SliceStable(ended, func(i, j int) bool {
		if a, b := endedAt(ended[i]), endedAt(ended[j]); !a.Equal(b) {
			return a.After(b)
		}
		return ended[i].ID > ended[j].ID
	})
	var gone []string
	for i := len(ended) - 1; i >= 0 && (len(ended)-len(gone) > r.MaxRecordings || total > r.MaxBytes); i-- {
		if err := os.RemoveAll(r.dir(ended[i].ID)); err != nil {
			return gone, err
		}
		total -= ended[i].Bytes
		gone = append(gone, ended[i].ID)
	}
	return gone, nil
}

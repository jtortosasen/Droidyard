package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// This file is the recordings' class (ADR-0113 §5; amends ADR-0012 §Durability classes):
// the timeline of a UI recording, <state>/recordings/<id>/timeline.jsonl, behind
// "droidyard://artifact/r.<id>". Unlike a build console it is daemon-wide, not a
// workspace's, and it is untrusted by class: the mark holds on the open handle and after
// a restart alike, because every line carries device text.

const (
	// TimelineFile and RecordingsDir name the layout (ADR-0021 §Layout).
	TimelineFile  = "timeline.jsonl"
	RecordingsDir = "recordings"
	// TimelineMediaType is the timeline's content type.
	TimelineMediaType = "application/x-ndjson"
)

// timelineIDPattern is a recording id: the start time and 32 random bits. It is matched
// before any path is built, so no id reaches the file system unless it is one this store
// could have issued.
var timelineIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// ValidTimelineID reports whether id is a recording id.
func ValidTimelineID(id string) bool { return timelineIDPattern.MatchString(id) }

// ErrTimelineFull is WriteLine's refusal of a line that would pass the cap. Nothing was
// written: no line is ever cut.
var ErrTimelineFull = errors.New("artifact: the timeline would pass its byte cap")

// RecordingDir is <state>/recordings/<id>/, or "" when id is not a recording id, so no
// caller can build a path from an unchecked id.
func (s *Store) RecordingDir(id string) string {
	if !ValidTimelineID(id) {
		return ""
	}
	return filepath.Join(s.stateRoot, RecordingsDir, id)
}

// CreateTimeline starts the timeline of recording id (directory 0700, file 0600). cap is
// the most bytes it may hold (0 means no cap); WriteLine refuses a line past it. Until
// the handle is closed its reference reads `growing`.
func (s *Store) CreateTimeline(id string, cap int64) (*Durable, error) {
	if !ValidTimelineID(id) {
		return nil, fmt.Errorf("artifact: %q is not a recording id", id)
	}
	if s.stateRoot == "" {
		return nil, errors.New("artifact: timelines need a state root")
	}
	dir := s.RecordingDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, TimelineFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	ref := timelineRef(id)
	d := &Durable{
		s:     s,
		ref:   Ref{Ref: ref, MediaType: TimelineMediaType, Durability: DurabilityDurable, Untrusted: true},
		path:  path,
		owner: id,
		file:  f,
		cap:   cap,
		sum:   sha256.New(),
	}
	s.mu.Lock()
	if s.durable == nil {
		s.durable = map[string]*Durable{}
	}
	if old := s.durable[ref]; old != nil && old != d {
		old.mu.Lock()
		old.closed = true
		_ = old.file.Close()
		old.mu.Unlock()
	}
	s.durable[ref] = d
	s.mu.Unlock()
	return d, nil
}

// WriteLine appends one line (p without its newline, which WriteLine adds) or, when the
// line would pass the cap, writes nothing and returns ErrTimelineFull.
func (d *Durable) WriteLine(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fs.ErrClosed
	}
	n := int64(len(p)) + 1
	if d.cap > 0 && d.size+n > d.cap {
		return ErrTimelineFull
	}
	b := append(append(make([]byte, 0, n), p...), '\n')
	write := d.file.Write
	if d.writeHook != nil {
		write = d.writeHook
	}
	w, err := write(b)
	if err != nil || w != len(b) {
		// No partial line stays: cut the file back to the last whole line and leave the
		// size and the hash as they were.
		if err == nil {
			err = io.ErrShortWrite
		}
		if terr := d.file.Truncate(d.size); terr == nil {
			_, _ = d.file.Seek(d.size, io.SeekStart)
		}
		return err
	}
	d.size += int64(w)
	if d.sum != nil {
		d.sum.Write(b)
	}
	return nil
}

// SHA256 is the hex SHA-256 of the bytes written through this handle so far: the file's,
// for a timeline this handle created.
func (d *Durable) SHA256() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sum == nil {
		return ""
	}
	return hex.EncodeToString(d.sum.Sum(nil))
}

func timelineRef(id string) string { return RefPrefix + classRecording + "." + id }

// TimelineRef is the reference record of recording id's timeline with bytes as its size, for
// a summary that names the artifact without opening it. Like every timeline reference it is
// durable, untrusted and has no expiry.
func TimelineRef(id string, bytes int64) Ref {
	return Ref{Ref: timelineRef(id), MediaType: TimelineMediaType, Bytes: int(bytes), Durability: DurabilityDurable, Untrusted: true}
}

// recordMark is the part of recording.json the reference needs: a remote recording whose
// bytes are not yet copied from the lending host (step 10) reads `growing`.
type recordMark struct {
	Remote bool `json:"remote"`
	Copied bool `json:"copied"`
}

// getTimeline resolves "r.<id>". A malformed id, or a path that is not a regular file the
// store wrote, is ARTIFACT_NOT_FOUND with nothing opened; a well-formed id whose
// directory or file is gone is ARTIFACT_EXPIRED ("deleted with its recording").
func (s *Store) getTimeline(id string) (Entry, error) {
	if !ValidTimelineID(id) || s.stateRoot == "" {
		return Entry{}, notAnArtifact()
	}
	ref := timelineRef(id)
	if d := s.openDurable(ref); d != nil {
		d.mu.Lock()
		e := Entry{Ref: d.ref, File: d.path, Size: d.size, Growing: !d.closed}
		e.Ref.Bytes = int(d.size)
		d.mu.Unlock()
		return e, nil
	}
	dir := s.RecordingDir(id)
	di, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, timelineExpired()
		}
		return Entry{}, capability.AsError(err)
	}
	if !di.IsDir() {
		return Entry{}, notAnArtifact()
	}
	closed := Ref{Ref: ref, MediaType: TimelineMediaType, Durability: DurabilityDurable, Untrusted: true}
	if data, err := os.ReadFile(filepath.Join(dir, "recording.json")); err == nil {
		var m recordMark
		if json.Unmarshal(data, &m) == nil && m.Remote && !m.Copied {
			// The bytes are a .part file not yet served (step 10).
			return Entry{Ref: closed, Growing: true}, nil
		}
	}
	path := filepath.Join(dir, TimelineFile)
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, timelineExpired()
		}
		return Entry{}, capability.AsError(err)
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, notAnArtifact()
	}
	closed.Bytes = int(fi.Size())
	return Entry{Ref: closed, File: path, Size: fi.Size()}, nil
}

func timelineExpired() *capability.Error {
	return capability.NewError(capability.ErrArtifactExpired, "the artifact is gone: it was deleted with its recording").
		WithNextStep("record again")
}

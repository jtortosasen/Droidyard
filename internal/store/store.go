// Package store keeps the daemon's durable state as JSON files under the state root
// (ADR-0021 §Format, §Location). Every file carries schemaVersion; a write never
// leaves a half-written file; a file that does not parse is moved aside and treated
// as absent; a file of a newer version is never overwritten. Each of these is
// recorded as a problem for system.health instead of stopping the daemon.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

// Version is the schemaVersion Write stamps. Every file starts at version 1, so there
// are no migrations yet; the first file to reach version 2 brings its migration and a
// per-file version with it.
const Version = 1

// ErrNewer is returned when a file carries a schemaVersion above the one this binary
// reads. The file is left untouched and is never overwritten (ADR-0021 §Format).
var ErrNewer = errors.New("state file has a newer schemaVersion than this droidyard reads")

// Outcome is what Read found.
type Outcome int

const (
	// Absent: no file. The caller starts from its zero state.
	Absent Outcome = iota
	// Loaded: the file parsed and v holds it.
	Loaded
	// Corrupt: the file did not parse. It was moved to <name>.corrupt-<timestamp>,
	// recorded as a problem, and is treated as absent.
	Corrupt
	// Newer: the file's schemaVersion is above current. It was left untouched,
	// recorded as a problem, and every later Write to it is refused.
	Newer
)

func (o Outcome) String() string {
	switch o {
	case Absent:
		return "absent"
	case Loaded:
		return "loaded"
	case Corrupt:
		return "corrupt"
	case Newer:
		return "newer"
	}
	return fmt.Sprintf("Outcome(%d)", int(o))
}

// The problem kinds, spelled as system.health reports them (capability-api.md §Enum
// spelling).
const (
	ProblemCorrupt      = "corrupt"
	ProblemNewerVersion = "newerVersion"
)

// Problem is a state file the daemon could not use. File and MovedTo are relative to
// the state root, slash-separated. MovedTo is empty for a newer file, and for a
// corrupt one that could not be moved.
type Problem struct {
	File    string `json:"file"`
	Problem string `json:"problem"`
	MovedTo string `json:"movedTo,omitempty"`
}

// fileSystem is the part of the filesystem whose order makes a write durable. Tests
// replace it to see the calls.
type fileSystem interface {
	Sync(f *os.File) error
	Rename(oldpath, newpath string) error
}

type osFS struct{}

func (osFS) Sync(f *os.File) error                { return f.Sync() }
func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Store reads and writes the state files under one root. It is safe for concurrent
// use; reads and writes are serialised, which is cheap at the sizes ADR-0021 expects.
type Store struct {
	root string
	fs   fileSystem

	mu       sync.Mutex
	problems []Problem
	newer    map[string]bool
}

// New returns a store rooted at root, the state root. Nothing is written outside it.
func New(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("state root must be an absolute path, not %q", root)
	}
	return &Store{root: filepath.Clean(root), fs: osFS{}, newer: map[string]bool{}}, nil
}

// Root is the state root.
func (s *Store) Root() string { return s.root }

// Problems lists the files the store could not use, in the order it met them.
func (s *Store) Problems() []Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Problem(nil), s.problems...)
}

// path resolves name, a slash-separated path relative to the root, to its file and its
// clean name, the key problems and refusals are recorded under. A name that would
// leave the root is refused: the store never writes inside a project tree or anywhere
// else outside its state directory (ADR-0021 §Location).
func (s *Store) path(name string) (string, string, error) {
	local := filepath.Clean(filepath.FromSlash(name))
	// "." is local, but it names the root itself, whose .tmp would sit beside it.
	if !filepath.IsLocal(local) || local == "." {
		return "", "", fmt.Errorf("state file %q is not a path inside the state root", name)
	}
	return filepath.Join(s.root, local), filepath.ToSlash(local), nil
}

// Read loads name into v, which must be a non-nil pointer, if its schemaVersion is at
// most current. A stale <name>.tmp from an interrupted write is never read. An error
// other than ErrNewer means the file could not be read at all (permissions, a
// directory in its place); the file is left where it is.
func (s *Store) Read(name string, current int, v any) (Outcome, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return Absent, fmt.Errorf("store: Read of %q needs a non-nil pointer, got %T", name, v)
	}
	path, name, err := s.path(name)
	if err != nil {
		return Absent, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return Absent, err
	}
	version, ok := schemaVersion(data)
	if !ok {
		s.quarantine(name, path)
		return Corrupt, nil
	}
	if version > current {
		s.recordNewer(name)
		return Newer, ErrNewer
	}
	// Decode into a fresh value so a file that fails half way leaves v untouched.
	fresh := reflect.New(rv.Elem().Type())
	if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		s.quarantine(name, path)
		return Corrupt, nil
	}
	rv.Elem().Set(fresh.Elem())
	return Loaded, nil
}

// Write replaces name with v, a value that encodes as a JSON object, adding
// "schemaVersion": Version. It writes <name>.tmp (0600), syncs it, renames it over
// name and syncs the directory, creating directories 0700 as needed, so a reader sees
// the old file or the new one, never a mix (ADR-0021 §Format). A file of a newer
// version, whether Read met it or it is found now, is never overwritten: ErrNewer.
func (s *Store) Write(name string, v any) error {
	path, name, err := s.path(name)
	if err != nil {
		return err
	}
	data, err := encode(v)
	if err != nil {
		return fmt.Errorf("store: encoding %q: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.newer[name] {
		return ErrNewer
	}
	if existing, err := os.ReadFile(path); err == nil {
		if version, ok := schemaVersion(existing); ok && version > Version {
			s.recordNewer(name)
			return ErrNewer
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	// A stale .tmp is an interrupted write: removing it first gives O_EXCL a fresh
	// file with this write's mode.
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = s.fs.Sync(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = s.fs.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return s.syncDir(dir)
}

// syncDir makes a rename in dir durable.
func (s *Store) syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = s.fs.Sync(d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// quarantine moves a file that does not parse to <name>.corrupt-<UTC timestamp>, its
// bytes intact, and records it. Corrupt state never stops the daemon (ADR-0021
// §Format): if the move fails, the problem is recorded without MovedTo and the file
// is still treated as absent. Called with s.mu held.
func (s *Store) quarantine(name, path string) {
	suffix := ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	p := Problem{File: name, Problem: ProblemCorrupt}
	if err := s.fs.Rename(path, path+suffix); err == nil {
		p.MovedTo = name + suffix
		_ = s.syncDir(filepath.Dir(path))
	}
	s.problems = append(s.problems, p)
}

// recordNewer refuses later writes to name and records it once. Called with s.mu held.
func (s *Store) recordNewer(name string) {
	if s.newer[name] {
		return
	}
	s.newer[name] = true
	s.problems = append(s.problems, Problem{File: name, Problem: ProblemNewerVersion})
}

// schemaVersion reads a file's version. A file that is not a JSON object with an
// integer schemaVersion of at least 1 does not parse as a state file.
func schemaVersion(data []byte) (int, bool) {
	var head struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &head); err != nil || head.SchemaVersion == nil || *head.SchemaVersion < 1 {
		return 0, false
	}
	return *head.SchemaVersion, true
}

// encode renders v as an indented JSON object whose first member is schemaVersion,
// so a file reads the same with cat and with jq.
func encode(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return nil, fmt.Errorf("a state file must encode as a JSON object, got %s", truncate(body))
	}
	if _, ok := members["schemaVersion"]; ok {
		return nil, errors.New("schemaVersion is the store's to write")
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"schemaVersion":%d`, Version)
	if inner := bytes.TrimSpace(body[1 : len(body)-1]); len(inner) > 0 {
		buf.WriteByte(',')
		buf.Write(inner)
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func truncate(b []byte) string {
	if len(b) > 40 {
		return string(b[:40]) + "..."
	}
	return string(b)
}

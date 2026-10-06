// Package policy reads the user configuration, which is the file trust is recorded in
// (ADR-0015 §5–6; ADR-0021 §Location; plan m1g D1). It is not state: it lives outside
// the state root, is written only by human channels (`droidyard trust`) or by hand,
// and is never migrated or reset with the state.
//
// Only trust is defined here. Every other key of the configuration file waits for the
// full specification (plan m1g D1); unknown keys are kept across a rewrite and
// ignored.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// FileName is the configuration file, in the configuration directory
// (plan m1g D1).
const FileName = "config.json"

// LockName is the exclusive lock beside it that serialises `droidyard trust` so two
// concurrent runs lose nothing (plan m1g D2).
const LockName = FileName + ".lock"

// schemaVersion is the only configuration schema version this binary reads and writes.
// A file of a newer version is treated like an unparseable one: nothing is trusted and
// it is never rewritten.
const schemaVersion = 1

// Policy reads one configuration file with a size-and-mtime cache, so a hand edit or a
// `droidyard trust` is picked up without a restart (ADR-0015 §6) at the cost of a stat
// per trust check.
type Policy struct {
	env  platform.Env
	path string
	// dirErr is why the configuration directory could not be resolved. It is reported
	// per call, not at construction: a daemon that reads no configuration still starts.
	dirErr error

	mu     sync.Mutex
	cached *snapshot
}

// snapshot is the parsed file and the size and mtime it was parsed from. err is set
// when the file exists but could not be used; present is false then.
type snapshot struct {
	present  bool
	size     int64
	mtime    time.Time
	roots    []string
	prefixes []string
	err      *capability.Error
}

// New returns the policy that reads <env's configuration directory>/config.json.
func New(env platform.Env) *Policy {
	if env.Getenv == nil {
		env = platform.Current()
	}
	dir, err := env.ConfigDir()
	return &Policy{env: env, path: filepath.Join(dir, FileName), dirErr: err}
}

// Trusted reports whether root is trusted: equal to a `trust.roots` entry, or under a
// `trust.prefixes` entry at a path-component boundary, both canonicalised as a
// workspace root is (plan m1g D1). A missing file trusts nothing. A file that does not
// parse trusts nothing and returns the error, whose cause names the file; it is never
// renamed or rewritten (it is the user's configuration, not state).
func (p *Policy) Trusted(root string) (bool, *capability.Error) {
	if p.dirErr != nil {
		return false, configurationError("", p.dirErr)
	}
	snap := p.snapshot()
	if snap.err != nil {
		return false, snap.err
	}
	if !snap.present {
		return false, nil
	}
	return trustedBy(snap.roots, snap.prefixes, p.env.Canonical(root)), nil
}

// Authorize returns the Descriptor.Authorize hook the capabilities that run project
// code install: it learns the root the call would operate on from rootOf, checks it
// against the configuration, and fails WORKSPACE_UNTRUSTED before the handler runs
// (R4, D3). The value it returns, the canonical root, reaches the handler as
// Call.Authorized. Trust is decided here, not in the handler, because the check reads
// the file and Start does no I/O (ADR-0020, amended 2026-10-04).
func (p *Policy) Authorize(rootOf RootOf) func(context.Context, *capability.Call) (any, error) {
	return func(_ context.Context, call *capability.Call) (any, error) {
		root, ok := rootOf(call)
		if !ok || root == "" {
			return nil, capability.Errorf(capability.ErrWorkspaceNotOpen, "the call names no project root, so its trust cannot be decided").
				WithNextStep("open a workspace, then retry")
		}
		canonical := p.env.Canonical(root)
		trusted, err := p.Trusted(canonical)
		if err != nil {
			return nil, err
		}
		if !trusted {
			return nil, UntrustedError(canonical)
		}
		return canonical, nil
	}
}

// RootOf maps a call to the project root it would run code in; ok is false when the
// call names no root.
type RootOf func(call *capability.Call) (string, bool)

// UntrustedError is the fixed failure of a capability that would run code in an
// untrusted root: WORKSPACE_UNTRUSTED, nextStep naming the exact human action
// (ADR-0015 §5; plan m1g D3). Its text is a decision, asserted by the tests.
func UntrustedError(root string) *capability.Error {
	return capability.Errorf(capability.ErrWorkspaceUntrusted, "the project root %q is not trusted, so its code will not run", root).
		WithNextStep("ask the user to run `droidyard trust '" + strings.ReplaceAll(root, "'", `'\''`) + "'`")
}

// configurationError is the failure when the configuration file cannot be read or
// parsed. Its cause names the file (plan m1g D1).
func configurationError(path string, err error) *capability.Error {
	if path == "" {
		return capability.Errorf(capability.ErrWorkspaceUntrusted, "the droidyard configuration could not be read, so nothing is trusted").
			WithCause(err.Error())
	}
	return capability.Errorf(capability.ErrWorkspaceUntrusted, "the configuration file %s could not be read, so nothing is trusted", path).
		WithCause(path + ": " + err.Error()).
		WithNextStep("fix or remove " + path)
}

// trustedBy is the trust rule: an exact root, or a prefix at a path-component
// boundary, so prefix /a/b trusts /a/b/c and not /a/bc (plan m1g D1).
func trustedBy(roots, prefixes []string, root string) bool {
	for _, r := range roots {
		if r == root {
			return true
		}
	}
	for _, pre := range prefixes {
		if pre == "" {
			continue
		}
		if root == pre {
			return true
		}
		if pre == string(filepath.Separator) {
			// The filesystem root trusts everything; below it the boundary rule would
			// see no '/'-terminated second component.
			if strings.HasPrefix(root, pre) {
				return true
			}
			continue
		}
		if len(root) > len(pre) && strings.HasPrefix(root, pre) && root[len(pre)] == filepath.Separator {
			return true
		}
	}
	return false
}

// canonical maps each non-empty entry through the environment's canonicaliser, so a
// root reached through a symlink matches its canonical entry.
func (p *Policy) canonical(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e == "" {
			continue
		}
		out = append(out, p.env.Canonical(e))
	}
	return out
}

// snapshot returns the parsed file, re-reading it when its size or mtime changed.
func (p *Policy) snapshot() *snapshot {
	fi, err := os.Stat(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		p.mu.Lock()
		p.cached = nil
		p.mu.Unlock()
		return &snapshot{}
	}
	if err != nil {
		return &snapshot{err: configurationError(p.path, err)}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && p.cached.size == fi.Size() && p.cached.mtime.Equal(fi.ModTime()) {
		return p.cached
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		p.cached = nil
		return &snapshot{err: configurationError(p.path, err)}
	}
	snap := &snapshot{size: fi.Size(), mtime: fi.ModTime()}
	doc, err := decode(data)
	if err != nil {
		snap.err = configurationError(p.path, err)
	} else {
		snap.present = true
		snap.roots = p.canonical(doc.trust.roots)
		snap.prefixes = p.canonical(doc.trust.prefixes)
	}
	p.cached = snap
	return snap
}

// Trust adds root to the configuration's trusted roots, canonicalised as a workspace
// root is, and returns the canonical root (plan m1g D2). It takes an exclusive flock
// on <config dir>/config.json.lock, re-reads the file under it, and writes atomically,
// so two concurrent runs lose nothing. A file that does not parse is refused, never
// overwritten. No daemon is involved and none is started.
func (p *Policy) Trust(root string) (string, error) {
	if p.dirErr != nil {
		return "", p.dirErr
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%q is not an absolute path; `droidyard trust` takes the project root", root)
	}
	canonical := p.env.Canonical(root)

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock, err := lockExclusive(filepath.Join(dir, LockName))
	if err != nil {
		return "", err
	}
	defer lock.Close()

	doc, err := readDocument(p.path)
	if err != nil {
		return "", fmt.Errorf("%s could not be read, so it is left untouched: %w", p.path, err)
	}
	if !contains(doc.trust.roots, canonical) {
		doc.trust.roots = append(doc.trust.roots, canonical)
		if err := writeAtomic(p.path, doc); err != nil {
			return "", err
		}
	}
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
	return canonical, nil
}

// lockExclusive takes a blocking exclusive flock, the serialisation `droidyard trust`
// needs. The kernel releases it when the process dies, so a crashed run never blocks
// the next.
func lockExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// document is config.json as parsed: the keys this milestone knows, plus every key it
// does not, kept so a rewrite does not drop another tool's or a newer droidyard's.
type document struct {
	extra map[string]json.RawMessage // top-level keys other than schemaVersion and trust
	trust trustSection
}

type trustSection struct {
	extra    map[string]json.RawMessage // keys other than roots and prefixes
	roots    []string
	prefixes []string
}

// decode parses a configuration file. A file that is not a JSON object, or whose known
// keys have the wrong shape, is an error; so is a schemaVersion above this binary's.
func decode(data []byte) (*document, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	if top == nil {
		return nil, errors.New("the configuration is not a JSON object")
	}
	if v, ok := top["schemaVersion"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err != nil {
			return nil, fmt.Errorf("schemaVersion: %w", err)
		}
		if n > schemaVersion {
			return nil, fmt.Errorf("schemaVersion %d is newer than this droidyard reads (%d)", n, schemaVersion)
		}
	}
	doc := &document{extra: map[string]json.RawMessage{}, trust: trustSection{extra: map[string]json.RawMessage{}}}
	if v, ok := top["trust"]; ok {
		var t map[string]json.RawMessage
		if err := json.Unmarshal(v, &t); err != nil {
			return nil, fmt.Errorf("trust: %w", err)
		}
		for k, raw := range t {
			switch k {
			case "roots":
				if err := json.Unmarshal(raw, &doc.trust.roots); err != nil {
					return nil, fmt.Errorf("trust.roots: %w", err)
				}
				if err := absolute("trust.roots", doc.trust.roots); err != nil {
					return nil, err
				}
			case "prefixes":
				if err := json.Unmarshal(raw, &doc.trust.prefixes); err != nil {
					return nil, fmt.Errorf("trust.prefixes: %w", err)
				}
				if err := absolute("trust.prefixes", doc.trust.prefixes); err != nil {
					return nil, err
				}
			default:
				doc.trust.extra[k] = raw
			}
		}
	}
	for k, v := range top {
		if k != "schemaVersion" && k != "trust" {
			doc.extra[k] = v
		}
	}
	return doc, nil
}

// absolute refuses a relative entry: canonicalising "." or "~/src" would anchor it at
// "/", trusting every root or none without a word, so the whole file fails closed and
// the cause names the entry (ADR-0031 §1).
func absolute(key string, entries []string) error {
	for _, e := range entries {
		if e != "" && !filepath.IsAbs(e) {
			return fmt.Errorf("%s: %q is not an absolute path", key, e)
		}
	}
	return nil
}

// readDocument reads and parses path. A missing file is an empty configuration, so the
// first `droidyard trust` creates it.
func readDocument(path string) (*document, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &document{extra: map[string]json.RawMessage{}, trust: trustSection{extra: map[string]json.RawMessage{}}}, nil
	}
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// encode renders a document as indented JSON with schemaVersion and trust set, every
// other key kept as it was.
func encode(doc *document) ([]byte, error) {
	top := map[string]json.RawMessage{}
	for k, v := range doc.extra {
		top[k] = v
	}
	top["schemaVersion"] = json.RawMessage("1")

	section := map[string]json.RawMessage{}
	for k, v := range doc.trust.extra {
		section[k] = v
	}
	roots, err := json.Marshal(nonNil(doc.trust.roots))
	if err != nil {
		return nil, err
	}
	prefixes, err := json.Marshal(nonNil(doc.trust.prefixes))
	if err != nil {
		return nil, err
	}
	section["roots"] = roots
	section["prefixes"] = prefixes
	trust, err := json.Marshal(section)
	if err != nil {
		return nil, err
	}
	top["trust"] = trust

	body, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// writeAtomic replaces path with doc: write <path>.tmp (0600), sync, rename, sync the
// directory, so a reader sees the old file or the new one, never a mix.
func writeAtomic(path string, doc *document) error {
	data, err := encode(doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

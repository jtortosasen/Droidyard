// Package platform resolves where the daemon keeps its state and its socket
// (ADR-0021 §Location; ADR-0019 §6). Every client and the daemon derive both from
// the same environment, so the instance a client reaches is the one that environment
// names.
package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Name is the directory and socket basename (ADR-0024).
const Name = "droidyard"

// SocketName is the socket's file name (ADR-0024).
const SocketName = Name + ".sock"

// Env is what the paths depend on. Current reads it from the running process; tests
// build one to cover other systems.
type Env struct {
	GOOS   string
	UID    int
	Getenv func(string) string
	// Resolve canonicalises an existing path. Nil means the real filesystem's:
	// symlinks resolved, components in their on-disk case.
	Resolve func(string) (string, error)
}

// Current is the running process's environment.
func Current() Env {
	return Env{GOOS: runtime.GOOS, UID: os.Getuid(), Getenv: os.Getenv, Resolve: resolveOnDisk}
}

// StateDir is the state root: DROIDYARD_STATE_DIR if set; else on macOS
// ~/Library/Application Support/droidyard; else $XDG_STATE_HOME/droidyard, defaulting
// to ~/.local/state/droidyard (ADR-0021 §Location).
func (e Env) StateDir() (string, error) {
	if dir := e.Getenv("DROIDYARD_STATE_DIR"); dir != "" {
		// A relative root would resolve against each process's cwd, and a detached
		// daemon runs in / (ADR-0019 §4): client and daemon would disagree.
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("DROIDYARD_STATE_DIR must be an absolute path, not %q", dir)
		}
		return filepath.Clean(dir), nil
	}
	home := e.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME is not set, so the state directory cannot be located; set DROIDYARD_STATE_DIR")
	}
	if e.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", Name), nil
	}
	if dir := e.xdg("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, Name), nil
	}
	return filepath.Join(home, ".local", "state", Name), nil
}

// ConfigDir is the user configuration directory: the user's policy, trusted roots
// included, kept apart from the state so that deleting state cannot grant or revoke
// trust (ADR-0021 §Location; plan m1g D1).
//
// DROIDYARD_CONFIG_DIR if set; else, under an explicit DROIDYARD_STATE_DIR,
// <state>/config (one variable isolates a run); else on macOS
// ~/Library/Application Support/droidyard-config, a sibling of the state root and
// never inside it; else Linux $XDG_CONFIG_HOME/droidyard, defaulting to
// ~/.config/droidyard.
func (e Env) ConfigDir() (string, error) {
	if dir := e.Getenv("DROIDYARD_CONFIG_DIR"); dir != "" {
		// A relative path would resolve against each process's cwd, and `droidyard
		// trust` and a detached daemon do not share one (ADR-0019 §4): they would
		// disagree on the file.
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("DROIDYARD_CONFIG_DIR must be an absolute path, not %q", dir)
		}
		return filepath.Clean(dir), nil
	}
	if e.Getenv("DROIDYARD_STATE_DIR") != "" {
		state, err := e.StateDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(state, "config"), nil
	}
	home := e.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME is not set, so the configuration directory cannot be located; set DROIDYARD_CONFIG_DIR")
	}
	if e.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", Name+"-config"), nil
	}
	if dir := e.xdg("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, Name), nil
	}
	return filepath.Join(home, ".config", Name), nil
}

// CacheDir is the derived-cache directory, deletable at any time: on macOS
// ~/Library/Caches/droidyard; else $XDG_CACHE_HOME/droidyard, defaulting to
// ~/.cache/droidyard. Under an explicit DROIDYARD_STATE_DIR it is <state>/cache, so
// one variable isolates a run (ADR-0021 §Location; plan m1g D5).
func (e Env) CacheDir() (string, error) {
	if e.Getenv("DROIDYARD_STATE_DIR") != "" {
		state, err := e.StateDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(state, "cache"), nil
	}
	home := e.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME is not set, so the cache directory cannot be located; set DROIDYARD_STATE_DIR")
	}
	if e.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", Name), nil
	}
	if dir := e.xdg("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, Name), nil
	}
	return filepath.Join(home, ".cache", Name), nil
}

// SocketPath is the daemon's socket for the state root state: <state>/run/ when
// DROIDYARD_STATE_DIR is set or on macOS; else $XDG_RUNTIME_DIR/droidyard/, falling
// back to <state>/run/. A path too long for sun_path moves to
// /tmp/droidyard-<uid>/<discriminator>/, one directory per state root (ADR-0019 §6).
// Everything is derived from the canonical root, so every spelling of one root, and
// every client that uses one, reaches the same socket and makes the same fallback
// decision.
func (e Env) SocketPath(state string) string {
	state = e.Canonical(state)
	path := filepath.Join(e.runDir(state), SocketName)
	if len(path) > e.maxSocketPath() {
		path = filepath.Join("/tmp", Name+"-"+strconv.Itoa(e.UID), e.discriminator(state), SocketName)
	}
	return path
}

// runDir is where the socket of the canonical root state lives before any sun_path
// fallback.
func (e Env) runDir(state string) string {
	dir := filepath.Join(state, "run")
	if e.GOOS != "darwin" && e.Getenv("DROIDYARD_STATE_DIR") == "" {
		if rt := e.xdg("XDG_RUNTIME_DIR"); rt != "" {
			dir = filepath.Join(rt, Name)
		}
	}
	return dir
}

// ForwardNameMax is the longest file name a forward to the device gets in ForwardDir:
// "<kind>-<12 hex>", the kind at most 11 letters (ADR-0102).
const ForwardNameMax = 11 + 1 + 12

// ForwardDir is the directory of the sockets the device's readers and screen servers are
// forwarded to (ADR-0102): run/fwd/ beside the daemon's socket, placed by the same rules.
// Where a name of ForwardNameMax bytes would not fit sun_path, it moves to
// /tmp/droidyard-<uid>/fwd-<discriminator>/, one directory per state root and as deep as
// the socket's own fallback (ADR-0019 §6).
func (e Env) ForwardDir(state string) string {
	state = e.Canonical(state)
	dir := filepath.Join(e.runDir(state), "fwd")
	if !e.SocketFits(filepath.Join(dir, strings.Repeat("x", ForwardNameMax))) {
		dir = filepath.Join("/tmp", Name+"-"+strconv.Itoa(e.UID), "fwd-"+e.discriminator(state))
	}
	return dir
}

// SocketFits says whether path can be the address of a Unix socket: sun_path holds it.
func (e Env) SocketFits(path string) bool { return len(path) <= e.maxSocketPath() }

// OwnDir refuses a path that is not a directory of this user (a symlink included), or,
// for a parent, one that everyone may write to. It guards a directory that may be under
// the /tmp fallback, where another account could have made a path first.
func OwnDir(dir string, parent bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to uid %d, not to this user", dir, st.Uid)
	}
	if parent && fi.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("%s is writable by every user", dir)
	}
	return nil
}

// discriminator names a state root's fallback socket directory: the first 12 hex
// digits of the SHA-256 of the canonical state root, as ADR-0021 §Layout derives
// workspace ids. Deterministic, so every client finds its daemon; distinct per root,
// so two instances never share a socket (ADR-0019 §6).
func (e Env) discriminator(state string) string {
	return hash12(e.Canonical(state))
}

func hash12(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:12]
}

// WorkspaceID is the id of the workspace rooted at root, and that root canonicalised:
// <basename>-<12 hex of sha256(canonical root)> (ADR-0021 §Layout). Every spelling of
// one root (a symlink, another case on a volume that ignores it) gives one id, because
// the id and every lookup by root canonicalise with Canonical. The filesystem root,
// whose basename is "/", is named "root", so an id never holds a separator.
func (e Env) WorkspaceID(root string) (id, canonical string) {
	canonical = e.Canonical(root)
	base := filepath.Base(canonical)
	if base == string(filepath.Separator) {
		base = "root"
	}
	return base + "-" + hash12(canonical), canonical
}

// Canonical is path as ADR-0021 §Layout canonicalises a root: symlinks resolved, so
// /tmp/s and /private/tmp/s on macOS are one root, and each existing component in its
// on-disk case, so /Users/Me and /users/me are one root on a volume that ignores
// case. A path that does not exist yet is resolved as far as its nearest existing
// ancestor, and the rest kept as spelled.
func (e Env) Canonical(path string) string {
	resolve := e.Resolve
	if resolve == nil {
		resolve = resolveOnDisk
	}
	rest := ""
	for dir := filepath.Clean(path); ; {
		if r, err := resolve(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// resolveOnDisk resolves symlinks in an existing path, then spells each component as
// the directory that holds it lists it. A case-sensitive volume lists the spelling
// asked for, so only a volume that ignores case changes anything.
func resolveOnDisk(path string) (string, error) {
	r, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	out := string(filepath.Separator)
	for _, c := range strings.Split(strings.TrimPrefix(r, out), string(filepath.Separator)) {
		if c == "" {
			continue
		}
		out = filepath.Join(out, onDiskName(out, c))
	}
	return out, nil
}

// onDiskName is how dir lists the entry named name, ignoring case; name itself when
// dir lists it exactly or cannot be read.
func onDiskName(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	match := name
	for _, e := range entries {
		switch {
		case e.Name() == name:
			return name
		case match == name && strings.EqualFold(e.Name(), name):
			match = e.Name()
		}
	}
	return match
}

// maxSocketPath is the longest path a Unix socket address holds: sun_path, less the
// terminating NUL (104 bytes on macOS, 108 on Linux).
func (e Env) maxSocketPath() int {
	if e.GOOS == "darwin" {
		return 104 - 1
	}
	return 108 - 1
}

// xdg reads an XDG base directory. A relative value is invalid and ignored, as the
// XDG Base Directory specification requires.
func (e Env) xdg(name string) string {
	if dir := e.Getenv(name); filepath.IsAbs(dir) {
		return dir
	}
	return ""
}

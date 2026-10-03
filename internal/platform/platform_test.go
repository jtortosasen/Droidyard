package platform

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// env is an environment on another system: its paths resolve to themselves, since
// they do not exist here.
func env(goos string, vars map[string]string) Env {
	return Env{GOOS: goos, UID: 501, Getenv: func(k string) string { return vars[k] },
		Resolve: func(p string) (string, error) { return p, nil }}
}

// TestPaths holds the state-root and socket rules over the environment combinations
// that decide them (ADR-0021 §Location; ADR-0019 §6).
func TestPaths(t *testing.T) {
	long := "/Users/me/" + strings.Repeat("x", 80) // with /run/droidyard.sock: over 103
	cases := []struct {
		name        string
		goos        string
		vars        map[string]string
		state, sock string
	}{
		{
			// The rule that makes one variable enough to isolate an instance.
			name:  "linux: an explicit DROIDYARD_STATE_DIR beats XDG_RUNTIME_DIR",
			goos:  "linux",
			vars:  map[string]string{"HOME": "/home/me", "DROIDYARD_STATE_DIR": "/scratch/s", "XDG_STATE_HOME": "/home/me/xs", "XDG_RUNTIME_DIR": "/run/user/1000"},
			state: "/scratch/s",
			sock:  "/scratch/s/run/droidyard.sock",
		},
		{
			name:  "linux: XDG_STATE_HOME and XDG_RUNTIME_DIR",
			goos:  "linux",
			vars:  map[string]string{"HOME": "/home/me", "XDG_STATE_HOME": "/home/me/xs", "XDG_RUNTIME_DIR": "/run/user/1000"},
			state: "/home/me/xs/droidyard",
			sock:  "/run/user/1000/droidyard/droidyard.sock",
		},
		{
			name:  "linux: defaults, no XDG_RUNTIME_DIR",
			goos:  "linux",
			vars:  map[string]string{"HOME": "/home/me"},
			state: "/home/me/.local/state/droidyard",
			sock:  "/home/me/.local/state/droidyard/run/droidyard.sock",
		},
		{
			name:  "linux: relative XDG values are ignored",
			goos:  "linux",
			vars:  map[string]string{"HOME": "/home/me", "XDG_STATE_HOME": "rel/state", "XDG_RUNTIME_DIR": "rel/run"},
			state: "/home/me/.local/state/droidyard",
			sock:  "/home/me/.local/state/droidyard/run/droidyard.sock",
		},
		{
			name:  "macos: Application Support, XDG ignored",
			goos:  "darwin",
			vars:  map[string]string{"HOME": "/Users/me", "XDG_STATE_HOME": "/Users/me/xs", "XDG_RUNTIME_DIR": "/run/user/501"},
			state: "/Users/me/Library/Application Support/droidyard",
			sock:  "/Users/me/Library/Application Support/droidyard/run/droidyard.sock",
		},
		{
			name:  "macos: DROIDYARD_STATE_DIR",
			goos:  "darwin",
			vars:  map[string]string{"HOME": "/Users/me", "DROIDYARD_STATE_DIR": "/private/tmp/s/"},
			state: "/private/tmp/s",
			sock:  "/private/tmp/s/run/droidyard.sock",
		},
		{
			// The path would not fit in sun_path, so it moves to /tmp, into a
			// directory of its own: the first 12 hex digits of sha256(state root).
			// Pinned here so a change of the hash, which would strand running
			// daemons' clients, is a visible decision.
			name:  "macos: sun_path fallback",
			goos:  "darwin",
			vars:  map[string]string{"HOME": "/Users/me", "DROIDYARD_STATE_DIR": long},
			state: long,
			sock:  "/tmp/droidyard-501/94618fd3f32c/droidyard.sock",
		},
		{
			name:  "linux: sun_path fallback from XDG_RUNTIME_DIR",
			goos:  "linux",
			vars:  map[string]string{"HOME": "/home/me", "XDG_RUNTIME_DIR": "/run/" + strings.Repeat("r", 90)},
			state: "/home/me/.local/state/droidyard",
			sock:  "/tmp/droidyard-501/5deebf7caed0/droidyard.sock",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := env(c.goos, c.vars)
			state, err := e.StateDir()
			if err != nil {
				t.Fatal(err)
			}
			if state != c.state {
				t.Errorf("state root %q; want %q", state, c.state)
			}
			if sock := e.SocketPath(state); sock != c.sock {
				t.Errorf("socket %q; want %q", sock, c.sock)
			}
		})
	}
}

// TestSunPathBoundary: a socket path of exactly the longest length sun_path holds is
// kept; one byte more falls back.
func TestSunPathBoundary(t *testing.T) {
	for goos, c := range map[string]struct {
		max  int
		hash string
	}{"darwin": {103, "aa4a2abd02a4"}, "linux": {107, "f934821d8a40"}} {
		suffix := "/run/droidyard.sock"
		fits := "/" + strings.Repeat("s", c.max-len(suffix)-1)
		e := env(goos, map[string]string{"DROIDYARD_STATE_DIR": fits})
		if got := e.SocketPath(fits); got != fits+suffix || len(got) != c.max {
			t.Errorf("%s: a %d-byte socket path became %q; want it kept", goos, c.max, got)
		}
		over := fits + "s"
		if got := e.SocketPath(over); got != "/tmp/droidyard-501/"+c.hash+"/droidyard.sock" {
			t.Errorf("%s: a %d-byte socket path became %q; want the /tmp fallback of its root", goos, c.max+1, got)
		}
	}
}

// TestFallbackIsPerStateRoot: two state roots never share a fallback socket, so one
// daemon cannot remove another's as stale (ADR-0019 §6, amended 2026-09-30); one root
// always gets the same one, whichever spelling of it a client uses.
func TestFallbackIsPerStateRoot(t *testing.T) {
	long := "/Users/me/" + strings.Repeat("x", 90)
	e := env("darwin", map[string]string{"HOME": "/Users/me"})
	a, b := e.SocketPath(long+"/a"), e.SocketPath(long+"/b")
	if !strings.HasPrefix(a, "/tmp/droidyard-501/") || !strings.HasPrefix(b, "/tmp/droidyard-501/") {
		t.Fatalf("long roots did not fall back: %s, %s", a, b)
	}
	if a == b || filepath.Dir(a) == filepath.Dir(b) {
		t.Errorf("two state roots share a fallback directory: %s and %s", a, b)
	}
	if again := e.SocketPath(long + "/a/"); again != a {
		t.Errorf("one root gave two fallback paths: %s and %s", a, again)
	}

	// On the real filesystem: a symlinked spelling of a root is the same root.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cur := Current()
	for _, sub := range []string{"", "/not/yet/created/" + strings.Repeat("y", 90)} {
		if x, y := cur.discriminator(real+sub), cur.discriminator(link+sub); x != y {
			t.Errorf("%s and its symlink %s got different discriminators (%s, %s)", real+sub, link+sub, x, y)
		}
	}
}

// TestSunPathLimitIsThisSystems: the limit used for the running OS is the one a real
// bind enforces. Level 5 for this OS only.
func TestSunPathLimitIsThisSystems(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no sun_path limit recorded for", runtime.GOOS)
	}
	max := Current().maxSocketPath()
	dir, err := os.MkdirTemp("/tmp", "dy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	name := func(n int) string { return filepath.Join(dir, strings.Repeat("a", n-len(dir)-1)) }
	l, err := net.Listen("unix", name(max))
	if err != nil {
		t.Fatalf("binding a %d-byte socket path failed: %v", max, err)
	}
	l.Close()
	if l, err := net.Listen("unix", name(max+1)); err == nil {
		l.Close()
		t.Fatalf("binding a %d-byte socket path succeeded; the limit is not %d", max+1, max)
	}
}

// TestStateDirRefusesWhatCannotBeShared: a relative DROIDYARD_STATE_DIR would resolve
// against each process's cwd, and without HOME there is no default to fall back on.
func TestStateDirRefusesWhatCannotBeShared(t *testing.T) {
	if _, err := env("linux", map[string]string{"HOME": "/home/me", "DROIDYARD_STATE_DIR": "rel"}).StateDir(); err == nil {
		t.Error("a relative DROIDYARD_STATE_DIR was accepted")
	}
	if _, err := env("darwin", map[string]string{}).StateDir(); err == nil {
		t.Error("no HOME and no DROIDYARD_STATE_DIR produced a state root")
	}
}

// TestSpellingsOfOneRootShareOneSocket: the fallback is decided on the canonical
// root, so two spellings of one root reach one socket even where one spelling fits
// sun_path and the other does not (ADR-0019 §6; ADR-0021 §Layout). On macOS /tmp is
// a symlink to /private/tmp, 8 bytes longer.
func TestSpellingsOfOneRootShareOneSocket(t *testing.T) {
	if r, _ := filepath.EvalSymlinks("/tmp"); r != "/private/tmp" {
		t.Skip("/tmp is not a symlink to /private/tmp here")
	}
	// "/tmp/"+name+"/run/droidyard.sock" fits in 103 bytes; with "/private" it does not.
	name := "dys" + strings.Repeat("x", 75-3)
	if err := os.Mkdir("/tmp/"+name, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove("/tmp/" + name) })
	e := Current()
	short, long := e.SocketPath("/tmp/"+name), e.SocketPath("/private/tmp/"+name)
	if short != long {
		t.Errorf("two spellings of one root got two sockets: %s and %s", short, long)
	}
	if !strings.HasPrefix(short, "/tmp/droidyard-") {
		t.Errorf("the canonical root's socket path does not fit sun_path, yet no fallback: %s", short)
	}
}

// TestCaseOfARootIsTheOnDiskCase: on a volume that ignores case, a root spelled in
// another case is the same root, with the same fallback socket (ADR-0021 §Layout).
func TestCaseOfARootIsTheOnDiskCase(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "CaseRoot")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "caseroot")
	if _, err := os.Stat(other); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	e := Current()
	if a, b := e.discriminator(actual), e.discriminator(other); a != b {
		t.Errorf("%s and %s got different discriminators (%s, %s)", actual, other, a, b)
	}
	if got := e.Canonical(other + "/not-yet"); !strings.HasSuffix(got, "/CaseRoot/not-yet") {
		t.Errorf("canonical(%s) = %s; want the on-disk case, the missing tail as spelled", other+"/not-yet", got)
	}
}

// TestWorkspaceIDIsOneIDPerRoot: /tmp/x, /private/tmp/x and a case variant are one
// root, so one workspace id, <basename>-<12 hex of sha256(canonical root)>
// (ADR-0021 §Layout). The hash is pinned: a change would orphan every ws/<wsid>/.
func TestWorkspaceIDIsOneIDPerRoot(t *testing.T) {
	if r, _ := filepath.EvalSymlinks("/tmp"); r != "/private/tmp" {
		t.Skip("/tmp is not a symlink to /private/tmp here")
	}
	dir, err := os.MkdirTemp("/tmp", "WsCase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	base := filepath.Base(dir)
	canonical := "/private/tmp/" + base
	e := Current()
	spellings := []string{dir, canonical, "/tmp/" + strings.ToLower(base), canonical + "/"}
	if _, err := os.Stat(spellings[2]); err != nil {
		spellings = spellings[:2] // a case-sensitive volume
	}
	want := base + "-" + hash12(canonical)
	for _, s := range spellings {
		if id, c := e.WorkspaceID(s); id != want || c != canonical {
			t.Errorf("WorkspaceID(%s) = %s, %s; want %s, %s", s, id, c, want, canonical)
		}
	}
	if id, _ := env("darwin", nil).WorkspaceID("/Users/me/app"); id != "app-844551840eba" {
		t.Errorf("WorkspaceID(/Users/me/app) = %s; want app-844551840eba (shasum -a 256 of the path)", id)
	}
	if id, _ := env("darwin", nil).WorkspaceID("/"); !strings.HasPrefix(id, "root-") {
		t.Errorf("the filesystem root's id is %q; want root-<hash>", id)
	}
}

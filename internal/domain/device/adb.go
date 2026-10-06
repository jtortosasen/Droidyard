package device

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// versionTimeout bounds `adb version`; a variable so a test of a slow hook is quick.
var versionTimeout = 3 * time.Second

const (
	hostVersionTimeout = time.Second // host:version on the server's port
	versionOutputCap   = 4 << 10
)

// defaultServerPort is adb's own default; a variable so tests never dial the real one.
var defaultServerPort = "5037"

// ADB is the daemon's handle on adb: where it is, what version, and a way to run it.
type ADB struct {
	env   []string
	start Starter
	exec  Exec // nil in production; a test's in-process stand-in for the client

	mu    sync.Mutex
	cache *versions

	// snapMu guards snaps, the per-device snapshot stores that make a ref from an
	// earlier read resolvable (plan m1f D16).
	snapMu sync.Mutex
	snaps  map[string]*snapshotStore

	// rdMu guards readers, the per-device persistent UI reader clients ADB owns
	// (ADR-0030). Close stops them and removes their forwards.
	rdMu    sync.Mutex
	readers map[string]*readerClient
}

// NewADB returns an ADB over env, the environment every adb client is given whole
// (so ANDROID_ADB_SERVER_PORT reaches it, and a server it forks keeps the user's HOME
// and TMPDIR), and start, which starts the clients. The daemon passes os.Environ()
// and the supervisor's Spawn; a nil start can resolve and report but not run.
func NewADB(env []string, start Starter) *ADB {
	return &ADB{env: append([]string(nil), env...), start: start}
}

// Exec is an in-process stand-in for starting an adb client, for tests whose subject
// is not the process runner: it returns what the client would have written and its
// exit code, and honours ctx as a killed client would. Run still owns the timeout, the
// output cap and the mapping of a timeout or cancellation, so those behave as with a
// real client. Production never sets one.
type Exec func(ctx context.Context, path string, args []string) (stdout, stderr []byte, exitCode int, err error)

// NewADBWithExec is NewADB whose clients are run by exec, in this process, instead of
// started as processes. It exists for tests (the fake adb in devicetest); the daemon
// does not use it.
func NewADBWithExec(env []string, exec Exec) *ADB {
	return &ADB{env: append([]string(nil), env...), exec: exec}
}

// Resolve finds adb again: a few stats, run on every availability check and device
// call, so an adb installed or removed while the daemon runs is seen.
func (a *ADB) Resolve() Resolution { return Resolve(a.env) }

// snapshots returns the snapshot store for a device, creating it on first use. Kept
// snapshots are how a ref survives from the read that returned it to a later tree.
func (a *ADB) snapshots(serial string) *snapshotStore {
	a.snapMu.Lock()
	defer a.snapMu.Unlock()
	if a.snaps == nil {
		a.snaps = map[string]*snapshotStore{}
	}
	st := a.snaps[serial]
	if st == nil {
		st = newSnapshotStore()
		a.snaps[serial] = st
	}
	return st
}

// Status is adb as system.health reports it.
type Status struct {
	Found         bool       `json:"found" jsonschema:"required"`
	Path          string     `json:"path,omitempty"`
	Source        string     `json:"source,omitempty"`
	Version       string     `json:"version,omitempty"`
	ServerVersion string     `json:"serverVersion,omitempty"`
	Warnings      []string   `json:"warnings" jsonschema:"required"`
	Rejected      []Rejected `json:"rejected" jsonschema:"required"`
}

// versions is what one check learned, valid while the resolved binary is unchanged.
type versions struct {
	path   string
	mtime  time.Time
	client string // "1.0.41 (36.0.0-13206524)"
	build  string // "41": the part the server speaks
	server string
}

// Check resolves adb and compares its version with the running server's. A mismatch
// is a warning, never an error: device calls still run (ratifications §Operational
// defaults). The client version is cached until the resolved path or its mtime
// changes; so is the server's, except that no server listening is not cached, so a
// server that starts later is compared at the next check.
func (a *ADB) Check(ctx context.Context) Status {
	res := a.Resolve()
	st := Status{Found: res.Found, Path: res.Path, Source: res.Source, Warnings: append([]string{}, res.Warnings...), Rejected: res.Rejected}
	if !res.Found {
		return st
	}
	fi, err := os.Stat(res.Path)
	if err != nil {
		return st
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil || a.cache.path != res.Path || !a.cache.mtime.Equal(fi.ModTime()) {
		// The cache is assigned only after a successful run: a cancelled or timed-out
		// first run must not pin an empty client version for the daemon's lifetime.
		out, err := a.Run(ctx, res.Path, versionTimeout, versionOutputCap, "version")
		if err != nil || out.ExitCode != 0 {
			return st
		}
		v := &versions{path: res.Path, mtime: fi.ModTime()}
		v.client, v.build = parseClientVersion(string(out.Stdout))
		a.cache = v
	}
	v := a.cache
	if v.server == "" {
		v.server = serverVersion(ctx, a.env)
	}
	st.Version, st.ServerVersion = v.client, v.server
	if v.build != "" && v.server != "" && v.build != v.server {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"the adb client %s (%s, from %s) differs from the running adb server's version %s: running it restarts the shared server, which can interrupt Android Studio and other tools. Set ADB to the adb the server was started from.",
			v.client, res.Path, res.Source, v.server))
	}
	return st
}

var clientVersionRe = regexp.MustCompile(`(?m)^Android Debug Bridge version (\S+)\s*$`)
var buildVersionRe = regexp.MustCompile(`(?m)^Version (\S+)\s*$`)

// parseClientVersion reads `adb version`: "Android Debug Bridge version 1.0.41" and
// "Version 36.0.0-13206524". build is the last component of the first, the number
// host:version answers with.
func parseClientVersion(out string) (display, build string) {
	m := clientVersionRe.FindStringSubmatch(out)
	if m == nil {
		return "", ""
	}
	build = m[1][strings.LastIndex(m[1], ".")+1:]
	display = m[1]
	if b := buildVersionRe.FindStringSubmatch(out); b != nil {
		display += " (" + b[1] + ")"
	}
	return display, build
}

// serverVersion asks the adb server on 127.0.0.1:${ANDROID_ADB_SERVER_PORT:-5037} for
// its version with the smart-socket request "host:version": a socket read, never by
// running adb, which would start a server. "" if nothing listens or it does not answer
// as expected. A variable that is set but is not a port probes nothing: falling back
// to 5037 could reach the shared server the caller meant to avoid.
func serverVersion(ctx context.Context, env []string) string {
	port, set := envMap(env)["ANDROID_ADB_SERVER_PORT"]
	if !set || port == "" {
		port = defaultServerPort
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return ""
	}
	d := net.Dialer{Timeout: hostVersionTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(hostVersionTimeout))
	const req = "host:version"
	if _, err := fmt.Fprintf(conn, "%04x%s", len(req), req); err != nil {
		return ""
	}
	// OKAY, a four-hex-digit length, then that many bytes: the version in hex.
	head := make([]byte, 8)
	if _, err := readFull(conn, head); err != nil || string(head[:4]) != "OKAY" {
		return ""
	}
	n, err := strconv.ParseUint(string(head[4:]), 16, 16)
	if err != nil || n == 0 || n > 16 {
		return ""
	}
	body := make([]byte, n)
	if _, err := readFull(conn, body); err != nil {
		return ""
	}
	v, err := strconv.ParseUint(string(body), 16, 32)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(v, 10)
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

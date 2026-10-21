package device

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
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
	// stream starts a logcat follow client and records its pid for its life (plan m1i
	// D9), nil when the daemon passed none: a follow then reports DEVICE_LOGS_FAILED.
	stream StreamStarter
	exec   Exec // nil in production; a test's in-process stand-in for the client
	// policy is the physical-device policy seam (plan m1k D7), set once at wiring,
	// before any call, and read at each device check; nil is the emulators-only
	// default.
	policy Policy
	// lending says which of the host's devices a paired peer may use (ADR-0099 §1),
	// remotes are the hosts this daemon borrows devices from (ADR-0100) and uploads holds
	// the APKs its peers uploaded (ADR-0099 §3). All are set once at wiring; nil lends
	// nothing, has no remote and has no upload.
	lending Lending
	remotes Remotes
	uploads *hosting.Uploads
	// occupation holds who occupies which device (ADR-0094), set once at wiring; nil holds
	// nothing and refuses nothing.
	occupation Occupation
	// startHook is a test seam: see SetStartHook.
	startHook atomic.Pointer[func()]
	// floor is a test seam: see LengthenLimits. Zero in the product.
	floor time.Duration
	// toolKey is `tools.adb` as read at daemon start, handed to every resolution
	// (ADR-0032 §3, §6; plan m1k D8). Empty means the file set no key.
	toolKey policy.ToolKey

	// upMu guards upSlots: one slot per remote, held while a build is uploaded to it and
	// installed there (installRemote).
	upMu    sync.Mutex
	upSlots map[string]chan struct{}

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

	// fwd is the directory of the sockets the forwards to the device are made at
	// (forwardsock.go; ADR-0102).
	fwd forwardDir

	// appMu guards apps, the per-(serial, applicationId) launch and pid records
	// device.logs filters an app by when its logcat has no uid column (plan m1i D11).
	appMu sync.Mutex
	apps  *appRegistry

	// probeMu guards probes, the per-device cache of whether logcat prints the uid
	// column (plan m1i D5). Keyed by serial and transport id, since an emulator serial
	// is reused by other AVDs and images.
	probeMu sync.Mutex
	probes  map[string]columnProbe

	// devMu guards devs, the per-device cache of the invariants D3 keeps: that the device
	// finished booting and its SDK level. Keyed by serial and transport id, as probes is,
	// because an emulator serial is reused by other AVDs and images; a call that fails
	// DEVICE_NOT_FOUND drops the serial's entry.
	devMu sync.Mutex
	devs  map[string]*deviceCache

	// bus is the daemon's event bus, which device.watchLogs' streams publish to
	// (ADR-0041); nil in a registry that only needs the descriptors. streams is every
	// live stream: Close ends them all and waits for each child to be reaped.
	bus     *event.Bus
	streams streamSet

	// uiStreams is every device.watchUiEvents stream (ADR-0112); Close ends them first.
	uiStreams uiEventStreams

	// screens is every live-view source, one per device (plan m2p step 3); Close ends
	// them all beside the log streams.
	screens screenSources

	// relayedScreens are the live-view sources of remote devices, one per id (plan m2t
	// step 3; ADR-0100 §3): each shows the stream a host serves. Close ends them with the
	// others.
	relayedScreens relayedScreens
	// relayedLogs are the Logcat streams of remote devices, by client session and device
	// (remotelogs.go).
	relayedLogs relayedLogSet
	// relayedUIEvents are the UI events streams of remote devices, by client session and
	// device (remoteuievents.go).
	relayedUIEvents relayedUISet

	// screenViews is every device.watchScreen stream (plan m2p step 4): each holds one
	// reference on a source in screens or relayedScreens. Close ends them before the sources.
	screenViews screenStreams

	// screenLookup replaces the lookup of a device's running source in a test (a fake
	// screen.Source recording Inject); nil in production.
	screenLookup func(serial string) screen.Source

	// log is the daemon's log (daemon.log when the daemon is detached), where reader
	// restarts, fallbacks and slow warm reads are recorded (plan m1n step 2). Nil, the
	// zero value, logs nothing. logMu serialises writes so concurrent readers' lines
	// never interleave.
	log   io.Writer
	logMu sync.Mutex
}

// deviceCache is what a device call learned about invariants that hold while the
// device's transport id is unchanged (D3): it has finished booting (sys.boot_completed
// is 1), and its SDK level (ro.build.version.sdk). transport is the id it was learned
// on; a different one is a stale entry, and DEVICE_NOT_FOUND drops the serial's entry.
type deviceCache struct {
	transport     string
	bootCompleted bool
	sdk           int
	sdkKnown      bool
}

// columnProbe is what one check of logcat's uid column learned, valid while the
// device's transport id is unchanged.
type columnProbe struct {
	transport string
	supported bool
}

// cacheEntryLocked returns the serial's cache entry for transport, creating a fresh one
// when there is none or the stored transport changed — a serial reused by another AVD or
// image (D3, as m1i D5 keys the uid probe). Callers hold devMu.
func (a *ADB) cacheEntryLocked(serial, transport string) *deviceCache {
	if a.devs == nil {
		a.devs = map[string]*deviceCache{}
	}
	c := a.devs[serial]
	if c == nil || c.transport != transport {
		c = &deviceCache{transport: transport}
		a.devs[serial] = c
	}
	return c
}

// cachedBootCompleted reports whether the device is already known to have finished
// booting, for this serial and transport (D3). A true answer lets ready skip its
// sys.boot_completed getprop.
func (a *ADB) cachedBootCompleted(serial, transport string) bool {
	if transport == "" {
		return false // no transport id to key on: never cache (a reused serial would match)
	}
	a.devMu.Lock()
	defer a.devMu.Unlock()
	c := a.devs[serial]
	return c != nil && c.transport == transport && c.bootCompleted
}

// rememberBootCompleted records that the device finished booting, for this serial and
// transport (D3), so a later call on the same transport skips the getprop.
func (a *ADB) rememberBootCompleted(serial, transport string) {
	if transport == "" {
		return
	}
	a.devMu.Lock()
	a.cacheEntryLocked(serial, transport).bootCompleted = true
	a.devMu.Unlock()
}

// cachedSDK returns the device's SDK level when it is already known for this serial and
// transport (D3).
func (a *ADB) cachedSDK(serial, transport string) (int, bool) {
	if transport == "" {
		return 0, false
	}
	a.devMu.Lock()
	defer a.devMu.Unlock()
	c := a.devs[serial]
	if c != nil && c.transport == transport && c.sdkKnown {
		return c.sdk, true
	}
	return 0, false
}

// rememberSDK records the device's SDK level, for this serial and transport (D3).
func (a *ADB) rememberSDK(serial, transport string, sdk int) {
	if transport == "" {
		return
	}
	a.devMu.Lock()
	e := a.cacheEntryLocked(serial, transport)
	e.sdk = sdk
	e.sdkKnown = true
	a.devMu.Unlock()
}

// forgetDevice drops the serial's cache entry, so the next call probes again (D3: an
// entry is dropped when a call fails DEVICE_NOT_FOUND).
func (a *ADB) forgetDevice(serial string) {
	a.devMu.Lock()
	delete(a.devs, serial)
	a.devMu.Unlock()
}

// NewADB returns an ADB over env, the environment every adb client is given whole
// (so ANDROID_ADB_SERVER_PORT reaches it, and a server it forks keeps the user's HOME
// and TMPDIR), and start, which starts the clients. The daemon passes os.Environ()
// and the supervisor's Spawn; a nil start can resolve and report but not run.
func NewADB(env []string, start Starter) *ADB {
	return &ADB{env: append([]string(nil), env...), start: start}
}

// SetStreamStarter gives the ADB the recorded starter a bounded follow uses, the
// supervisor's SpawnRecorded through the daemon's adapter (plan m1i D9). Without one a
// follow cannot be recorded and fails rather than leave a client no daemon can find.
func (a *ADB) SetStreamStarter(s StreamStarter) { a.stream = s }

// SetBus gives the ADB the daemon's event bus, which device.watchLogs publishes its
// streams to (ADR-0041). It is called once, at wiring, before any call.
func (a *ADB) SetBus(b *event.Bus) { a.bus = b }

// SetPolicy gives the ADB the daemon's policy, so a physical device's admission is
// decided live at each check (ADR-0032 §2, §6; plan m1k D7). It is called once, at
// wiring, before any call. Without one the emulators-only default applies, which is what
// every registry that predates the configuration file gets.
func (a *ADB) SetPolicy(p Policy) { a.policy = p }

// SetLending gives the ADB the lending of hosting.lend, read live at each call by the
// calls of a paired peer (ADR-0099 §1). Without one nothing is lent.
func (a *ADB) SetLending(l Lending) { a.lending = l }

// SetRemotes gives the ADB the links to the hosts this daemon borrows devices from
// (ADR-0100). Without them every device is local.
func (a *ADB) SetRemotes(r Remotes) { a.remotes = r }

// SetUploads gives the ADB the APKs its peers uploaded, which device.installUpload installs
// (ADR-0099 §3). Without them every upload is unknown.
func (a *ADB) SetUploads(u *hosting.Uploads) { a.uploads = u }

// SetToolKey gives the ADB `tools.adb` as the daemon read it at start (ADR-0032 §3, §6;
// plan m1k D8): the first candidate of every resolution. The value is a snapshot, so an
// edit of the configuration file does not change it until a new daemon starts. Called
// once, at wiring; an empty key is the same as never calling it.
func (a *ADB) SetToolKey(key policy.ToolKey) { a.toolKey = key }

// SetLog gives the ADB the daemon's log (ADR-0021 §Layout; daemon.log when the daemon is
// detached), where it records every UI reader restart and fallback with its reason and
// every warm read that took a second or more (plan m1n step 2). It is called once, at
// wiring, before any call. Nil logs nothing, so a registry that passes none is unchanged.
func (a *ADB) SetLog(w io.Writer) { a.log = w }

// logf appends one line to the daemon's log. The write is one string under logMu, so
// concurrent readers' lines never interleave and a detached daemon's O_APPEND file keeps
// them whole.
func (a *ADB) logf(format string, args ...any) {
	if a.log == nil {
		return
	}
	a.logMu.Lock()
	defer a.logMu.Unlock()
	fmt.Fprintf(a.log, "device: "+format+"\n", args...)
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
// call, so an adb installed or removed while the daemon runs is seen. The configured
// `tools.adb` is fixed at start (ADR-0032 §6; plan m1k D8).
func (a *ADB) Resolve() Resolution { return ResolveWith(a.env, a.toolKey) }

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
			"the adb client %s (%s, from %s) differs from the running adb server's version %s: running it restarts the shared server, which can interrupt Android Studio and other tools. Set `tools.adb` in the droidyard configuration (or ADB in the environment) to the adb the server was started from.",
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

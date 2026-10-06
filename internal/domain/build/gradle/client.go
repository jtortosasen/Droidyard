package gradle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/sidecar"
)

// ID is the supervisor id of the one shared sidecar (ADR-0019: one sidecar per daemon,
// serving every root).
const ID = "gradle-sidecar"

// ProtocolVersion is the protocol this core speaks; a sidecar that answers otherwise is
// refused (gradle.md §Protocol).
const ProtocolVersion = 1

// IdleTimeout is how long the sidecar may go unused before the supervisor stops it
// (gradle.md §Lifecycle; plan m1g D7).
const IdleTimeout = 10 * time.Minute

// GradleFloor is the oldest Gradle the toolchain profile is valid for; below it the
// connection is refused naming floor and version (ADR-0018 rule 3; plan m1g D9).
const GradleFloor = "8.0"

// Hello is the sidecar's `hello` reply.
type Hello struct {
	ProtocolVersion int      `json:"protocolVersion"`
	SidecarVersion  string   `json:"sidecarVersion"`
	TAPIVersion     string   `json:"tapiVersion"`
	Java            JavaInfo `json:"java"`
}

// JavaInfo is the JVM the sidecar itself runs on.
type JavaInfo struct {
	Home    string `json:"home"`
	Version string `json:"version"`
}

// Connection is one root's `connection.open` reply.
type Connection struct {
	GradleVersion  string          `json:"gradleVersion"`
	Source         string          `json:"source"`
	DaemonJavaHome string          `json:"daemonJavaHome,omitempty"`
	Capabilities   map[string]bool `json:"capabilities,omitempty"`
}

// Launch builds the sidecar's command line. It is the seam that lets a test run a fake
// sidecar (a helper process speaking the protocol) instead of a real JVM, so no test
// needs Gradle; the daemon leaves it nil and runs the embedded jars on the discovered
// JVM (plan m1g step 5).
type Launch func(jvm JVM, jars Jars) (command string, args []string, env []string, err error)

// Options configure a Client.
type Options struct {
	// Supervisor starts and stops the sidecar. Nil is a client that can compute the JVM
	// and the fingerprint but cannot call the sidecar (the schema-only registry).
	Supervisor *process.Supervisor
	// Log receives a line per sidecar start, unexpected exit and protocol break.
	Log io.Writer
	// CacheDir is where the jars are extracted (D5). The daemon passes
	// platform.Env.CacheDir.
	CacheDir string
	// Env is the environment discovery reads and the sidecar inherits (D22: inherit).
	Env []string
	// GOOS overrides runtime.GOOS for discovery; "" means the real one.
	GOOS string
	// Finder overrides discovery's probes; nil uses the real environment.
	Finder *Finder
	// JVM, when set, is the discovery result: it skips discovery entirely.
	JVM *JVM
	// Launch, when set, replaces the real sidecar command (tests).
	Launch Launch
	// IdleTimeout overrides the 10 minutes the sidecar may go unused (tests).
	IdleTimeout time.Duration
}

// Client is the daemon's sidecar client: it starts the one shared sidecar on demand,
// speaks the line-JSON protocol, serialises TAPI work per root and caches the project
// model per (root, fingerprint).
type Client struct {
	opts Options
	log  io.Writer
	once sync.Once
	jvm  JVM

	mu     sync.Mutex
	sess   *session
	roots  map[string]*root
	builds map[string]BuildSink
}

// session is one run of the sidecar: the requests outstanding on it and the hello
// exchange that makes it ready.
type session struct {
	pid       int
	next      int
	helloID   int
	hello     *Hello
	helloErr  string
	helloDone bool
	pending   map[int]chan message
	// broken is set when the sidecar wrote a line that is not the protocol: the
	// in-flight calls fail with it (D6).
	broken *capability.Error
}

func (s *session) nextID() int {
	s.next++
	return s.next
}

// root is one project root's client-side state.
type root struct {
	canonical string
	// tapi is the per-root TAPI lock with capacity 1 (gradle.md §Concurrency; R5): a
	// build holds it for its duration, every model fetch takes it. A channel, not a
	// Mutex, so waiting for it is context-aware.
	tapi chan struct{}

	cmu    sync.Mutex
	conn   Connection
	opened bool
	// openPid is the sidecar process the connection was opened on: a restarted sidecar
	// (a crash or the idle stop) has no connection for the root, so the next call opens
	// it again (step 5 review B1).
	openPid     int
	model       *Model
	fingerprint string
	degraded    *capability.Error
}

// New returns a client over opts.
func New(opts Options) *Client {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = IdleTimeout
	}
	return &Client{opts: opts, log: opts.Log, roots: map[string]*root{}}
}

func (c *Client) logf(format string, args ...any) {
	if c.log != nil && c.log != io.Discard {
		_, _ = io.WriteString(c.log, fmt.Sprintf("gradle: "+format, args...)+"\n")
	}
}

// JVM is the sidecar's JVM, discovered once per daemon and cached: the environment is
// fixed at start (ADR-0019 §8; plan m1g D22).
func (c *Client) JVM() JVM {
	c.once.Do(func() {
		if c.opts.JVM != nil {
			c.jvm = *c.opts.JVM
			return
		}
		f := Finder{GOOS: c.opts.GOOS, Env: c.opts.Env}
		if c.opts.Finder != nil {
			f = *c.opts.Finder
		}
		c.jvm = Discover(f)
	})
	return c.jvm
}

// Missing implements the daemon's Requirements: the gradle requirement is available
// exactly when a JVM ≥ 17 was found (plan m1g D22). The string it returns is what
// system.capabilities reports.
func (c *Client) Missing(r capability.Requirement) (string, bool) {
	if r != capability.RequiresGradle {
		return "", false
	}
	if j := c.JVM(); !j.Found {
		return j.NotFound(), true
	}
	return "", false
}

// Degraded returns the error that marked root degraded, if any: the sidecar crashed
// while one of its calls ran, and the next successful handshake clears it (plan m1g
// D20).
func (c *Client) Degraded(canonical string) *capability.Error {
	c.mu.Lock()
	r := c.roots[canonical]
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return r.degraded
}

// Hold starts the sidecar if needed and takes a lease for the caller's whole duration
// — one running build (gradle.md §Lifecycle: a lease per call and per running build) —
// so the idle timer cannot stop it between an operation's calls. Release the lease when
// the work is done.
func (c *Client) Hold(ctx context.Context) (*process.Lease, error) {
	lease, _, err := c.use(ctx)
	return lease, err
}

// LockRoot takes root's per-root TAPI lock for the caller, waiting for it
// context-aware, and returns the release. A build holds it for its whole duration, so
// a model call while it is held answers from the cache (R5; step 7).
func (c *Client) LockRoot(ctx context.Context, canonical string) (func(), error) {
	r := c.root(canonical)
	select {
	case r.tapi <- struct{}{}:
		return r.unlock, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *root) unlock() { <-r.tapi }

// Model returns root's project model and whether it is stale.
//
// When no build holds the root the model is fetched (opening the connection if
// needed) and cached under the root's current fingerprint. When a build does hold the
// root, the cached model is returned at once with stale true when the fingerprint has
// changed since it was fetched (gradle.md §Concurrency; plan m1g R5, D8). With no
// cached model it waits for the build, context-aware.
func (c *Client) Model(ctx context.Context, canonical string) (Model, bool, error) {
	r := c.root(canonical)
	fp := Fingerprint(canonical)
	// One configuration pass per (root, fingerprint) (D8; A3): an unchanged build answers
	// from the cache whether or not a build holds the root.
	if m, cachedFP, ok := r.cached(); ok && cachedFP == fp {
		return m, false, nil
	}
	if r.tryLock() {
		defer r.unlock()
		m, err := c.fetch(ctx, r)
		if err != nil {
			return Model{}, false, err
		}
		r.setModel(m, fp)
		return m, false, nil
	}
	if m, cachedFP, ok := r.cached(); ok {
		return m, fp != cachedFP, nil
	}
	if err := r.lock(ctx); err != nil {
		return Model{}, false, err
	}
	defer r.unlock()
	m, err := c.fetch(ctx, r)
	if err != nil {
		return Model{}, false, err
	}
	r.setModel(m, fp)
	return m, false, nil
}

// Close closes root's Gradle connection, so a closing workspace releases Gradle's
// daemon connection instead of leaving it to the sidecar's idle timeout
// (gradle.md §Protocol). Nothing happens when the root was never opened or the sidecar
// is not running.
func (c *Client) Close(ctx context.Context, canonical string) {
	c.mu.Lock()
	r := c.roots[canonical]
	c.mu.Unlock()
	if r == nil {
		return
	}
	if err := r.lock(ctx); err != nil {
		return
	}
	defer r.unlock()
	// A sidecar that is not running has no connection to close: never start one for
	// this (step 5 review B1).
	if c.opts.Supervisor == nil || c.opts.Supervisor.State(ID, process.Global) != process.Ready ||
		!r.isOpen(c.opts.Supervisor.Pid(ID, process.Global)) {
		r.clearConn()
		return
	}
	lease, _, err := c.use(ctx)
	if err != nil {
		return
	}
	defer lease.Release()
	_, _ = c.call(ctx, lease, "connection.close", map[string]any{"root": canonical})
	r.clearConn()
}

func (c *Client) root(canonical string) *root {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.roots[canonical]
	if r == nil {
		r = &root{canonical: canonical, tapi: make(chan struct{}, 1)}
		c.roots[canonical] = r
	}
	return r
}

func (r *root) tryLock() bool {
	select {
	case r.tapi <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *root) lock(ctx context.Context) error {
	select {
	case r.tapi <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *root) connection() Connection {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return r.conn
}

// isOpen reports whether the root has a connection on the sidecar process pid.
func (r *root) isOpen(pid int) bool {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	return r.opened && r.openPid == pid
}

func (r *root) setConn(conn Connection, pid int) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	r.conn, r.opened, r.openPid = conn, true, pid
}

func (r *root) clearConn() {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	r.conn, r.opened, r.openPid, r.model, r.fingerprint = Connection{}, false, 0, nil, ""
}

func (r *root) setModel(m Model, fp string) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	r.model, r.fingerprint = &m, fp
}

func (r *root) cached() (Model, string, bool) {
	r.cmu.Lock()
	defer r.cmu.Unlock()
	if r.model == nil {
		return Model{}, "", false
	}
	return *r.model, r.fingerprint, true
}

// fetch takes a lease, opens the root's connection if it is not open, and asks for the
// model. The caller holds the TAPI lock.
func (c *Client) fetch(ctx context.Context, r *root) (Model, error) {
	lease, _, err := c.use(ctx)
	if err != nil {
		c.fail(r, err)
		return Model{}, err
	}
	defer lease.Release()
	if !r.isOpen(lease.Pid()) {
		conn, err := c.open(ctx, lease, r.canonical)
		if err != nil {
			c.fail(r, err)
			return Model{}, err
		}
		r.setConn(conn, lease.Pid())
	}
	raw, err := c.call(ctx, lease, "project.model", map[string]any{"root": r.canonical})
	if err != nil {
		c.fail(r, err)
		return Model{}, err
	}
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		e := capability.Errorf(capability.ErrProjectModelFailed, "the sidecar's project model could not be read").WithCause(err.Error())
		c.fail(r, e)
		return Model{}, e
	}
	return m, nil
}

// open opens root's connection and refuses a Gradle below the floor (ADR-0018 rule 3).
func (c *Client) open(ctx context.Context, lease *process.Lease, canonical string) (Connection, error) {
	raw, err := c.call(ctx, lease, "connection.open", map[string]any{"root": canonical})
	if err != nil {
		return Connection{}, err
	}
	var conn Connection
	if err := json.Unmarshal(raw, &conn); err != nil {
		return Connection{}, capability.Errorf(capability.ErrBuildSidecarUnavailable, "the sidecar's connection.open reply could not be read").WithCause(err.Error())
	}
	if !gradleAtLeast(conn.GradleVersion, 8, 0) {
		// The connection is open on the sidecar but unusable here: close it, then fail
		// naming floor, version and reason (ADR-0018 rule 3; plan m1g D9).
		_, _ = c.call(ctx, lease, "connection.close", map[string]any{"root": canonical})
		return Connection{}, capability.Errorf(capability.ErrBuildGradleUnsupported,
			"Gradle %s is older than the floor %s this droidyard supports, so its toolchain profile is not validated", conn.GradleVersion, GradleFloor).
			WithCause("Gradle " + conn.GradleVersion + " is below the floor " + GradleFloor).
			WithNextStep("upgrade the project's Gradle to " + GradleFloor + " or newer (its wrapper or the daemon's distribution), then retry")
	}
	return conn, nil
}

// AtLeast reports whether a Gradle version string is at least major.minor, for a
// caller outside this package that decides a toolchain profile from the version
// (plan m1g D17). A version it cannot read is not at least anything.
func AtLeast(v string, wantMajor, wantMinor int) bool {
	return gradleAtLeast(v, wantMajor, wantMinor)
}

// gradleAtLeast reports whether a Gradle version string is at least major.minor.
func gradleAtLeast(v string, wantMajor, wantMinor int) bool {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major > wantMajor || (major == wantMajor && minor >= wantMinor)
}

// use starts the sidecar if needed, waits for readiness, and completes the hello
// handshake, checking the protocol version (gradle.md §Protocol).
func (c *Client) use(ctx context.Context) (*process.Lease, *Hello, error) {
	if c.opts.Supervisor == nil {
		return nil, nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "no process supervisor is configured, so the Gradle sidecar cannot run")
	}
	d, err := c.descriptor()
	if err != nil {
		return nil, nil, err
	}
	lease, err := c.opts.Supervisor.Use(ctx, d)
	if err != nil {
		return nil, nil, c.useError(ctx, err)
	}
	h, herr := c.handshake()
	if herr != nil {
		lease.Release()
		c.opts.Supervisor.StopProcess(ID, process.Global)
		return nil, nil, herr
	}
	if h.ProtocolVersion != ProtocolVersion {
		lease.Release()
		c.opts.Supervisor.StopProcess(ID, process.Global)
		return nil, nil, capability.Errorf(capability.ErrBuildSidecarUnavailable,
			"the Gradle sidecar speaks protocol version %d; this droidyard speaks %d", h.ProtocolVersion, ProtocolVersion).
			WithNextStep("update droidyard or the sidecar jars so the two speak the same protocol")
	}
	c.clearDegraded()
	return lease, h, nil
}

// descriptor is the sidecar's process descriptor, launched on the discovered JVM.
func (c *Client) descriptor() (process.Descriptor, error) {
	j := c.JVM()
	if !j.Found {
		return process.Descriptor{}, capability.Errorf(capability.ErrBuildSidecarUnavailable,
			"the Gradle sidecar needs a JVM 17 or newer and none was found").
			WithCause(j.NotFound()).
			WithNextStep("install a JDK 17 or newer, set JAVA_HOME to it, and restart droidyard")
	}
	command, args, env, err := c.launch(j)
	if err != nil {
		return process.Descriptor{}, err
	}
	return process.Descriptor{
		ID:       ID,
		Kind:     process.SharedSidecar,
		Command:  command,
		Args:     args,
		Env:      env,
		Scope:    process.Global,
		Protocol: true,
		Lines:    c.line,
		Ready:    c.ready,
		OnSpawn:  c.spawn,
		// stdout carries the protocol; stderr goes to the log sink (D6).
		IdleTimeout: c.opts.IdleTimeout,
		Shutdown:    process.ShutdownPolicy{Signal: unix.SIGTERM, Timeout: 5 * time.Second},
	}, nil
}

// launch builds the sidecar command: the embedded jars extracted to the cache directory
// on the discovered JVM, or the Launch seam.
func (c *Client) launch(j JVM) (string, []string, []string, error) {
	var jars Jars
	if c.opts.Launch == nil {
		if c.opts.CacheDir == "" {
			return "", nil, nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "no cache directory is configured, so the sidecar jars cannot be extracted")
		}
		var err error
		jars, err = cacheJars(c.opts.CacheDir)
		if err != nil {
			return "", nil, nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "the sidecar jars could not be written to the cache directory").
				WithCause(err.Error()).WithDetails(map[string]any{"cacheDir": c.opts.CacheDir})
		}
	} else {
		return c.opts.Launch(j, jars)
	}
	classpath := jars.Deps + string(filepath.ListSeparator) + jars.Class
	return filepath.Join(j.Home, "bin", "java"),
		[]string{"-XX:+DisplayVMOutputToStderr", "-cp", classpath, sidecar.Class},
		c.opts.Env, nil
}

// spawn is the OnSpawn hook: it opens the session and sends `hello` before any line is
// read, so the handshake happens while the process becomes ready.
func (c *Client) spawn(pid int, send func(string) error) error {
	c.mu.Lock()
	if c.sess != nil && c.sess.pid != pid {
		c.failPending(c.sess, capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar restarted under a call"))
	}
	s := &session{pid: pid, pending: map[int]chan message{}}
	s.helloID = s.nextID()
	c.sess = s
	c.mu.Unlock()
	line, err := encodeRequest(s.helloID, "hello", nil)
	if err != nil {
		return err
	}
	return send(line)
}

// line is the Lines hook: every complete protocol line, in order.
func (c *Client) line(raw string) {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return
	}
	m, err := decodeMessage(raw)
	if err != nil {
		c.breakProtocol(s, err)
		return
	}
	if m.ID == nil {
		// A notification: a build event, routed to the sink of the build it names.
		c.notify(s, m)
		return
	}
	id := *m.ID
	c.mu.Lock()
	if id == s.helloID {
		s.helloDone = true
		switch {
		case m.Error != nil:
			s.helloErr = m.Error.dataText()
		case len(m.Result) == 0:
			s.helloErr = "hello returned no result"
		default:
			var h Hello
			if err := json.Unmarshal(m.Result, &h); err != nil {
				s.helloErr = err.Error()
			} else {
				s.hello = &h
			}
		}
	}
	ch := s.pending[id]
	delete(s.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// ready is the Ready hook: the process is ready once the hello reply has arrived.
func (c *Client) ready(string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil && c.sess.helloDone
}

// handshake returns the Hello the current session completed, if any.
func (c *Client) handshake() (*Hello, error) {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil || !s.helloDone {
		return nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "the Gradle sidecar did not complete the hello handshake before it exited")
	}
	if s.helloErr != "" {
		return nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "the Gradle sidecar refused the hello handshake").WithCause(s.helloErr)
	}
	if s.hello == nil {
		return nil, capability.Errorf(capability.ErrBuildSidecarUnavailable, "the Gradle sidecar's hello reply could not be read")
	}
	return s.hello, nil
}

// breakProtocol records a stdout line that is not JSON, fails the in-flight calls and
// kills the sidecar's group: the protocol channel is lost (gradle.md §Protocol; D6).
func (c *Client) breakProtocol(s *session, err error) {
	e := capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar wrote a line that is not JSON-RPC on its protocol channel").WithCause(err.Error())
	c.mu.Lock()
	s.broken = e
	c.mu.Unlock()
	c.logf("sidecar wrote a non-JSON line (%v); killing it", err)
	c.opts.Supervisor.StopProcess(ID, process.Global)
}

func (c *Client) failPending(s *session, e *capability.Error) {
	for id, ch := range s.pending {
		delete(s.pending, id)
		ch <- message{Error: &rpcError{Code: codeInternalError, Message: e.Message}}
	}
}

func (c *Client) forget(s *session, id int) {
	c.mu.Lock()
	delete(s.pending, id)
	c.mu.Unlock()
}

// call sends one request on lease and waits for its reply, the call's context or the
// sidecar's exit.
func (c *Client) call(ctx context.Context, lease *process.Lease, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	s := c.sess
	if s == nil || s.pid != lease.Pid() {
		c.mu.Unlock()
		return nil, capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar is no longer the one this call started on")
	}
	id := s.nextID()
	ch := make(chan message, 1)
	s.pending[id] = ch
	c.mu.Unlock()

	line, err := encodeRequest(id, method, params)
	if err == nil {
		err = lease.Send(line)
	}
	if err != nil {
		c.forget(s, id)
		return nil, capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar could not be written to").WithCause(err.Error())
	}
	select {
	case m := <-ch:
		return reply(method, m)
	case <-ctx.Done():
		c.forget(s, id)
		return nil, ctx.Err()
	case <-lease.Done():
		c.forget(s, id)
		// A reply that raced the exit is still a reply.
		select {
		case m := <-ch:
			return reply(method, m)
		default:
		}
		return nil, c.crash(s, lease)
	}
}

func reply(method string, m message) (json.RawMessage, error) {
	if m.Error != nil {
		return nil, rpcFailure(method, m.Error)
	}
	return m.Result, nil
}

// crash is the failure of a call whose sidecar is gone: a protocol break, a protocol
// violation (an over-long line) or an unexpected exit.
func (c *Client) crash(s *session, lease *process.Lease) *capability.Error {
	c.mu.Lock()
	broken := s.broken
	c.mu.Unlock()
	if broken != nil {
		return broken
	}
	err := lease.Err()
	if err != nil && errors.Is(err, process.ErrProtocol) {
		return capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar broke the protocol on a call").WithCause(err.Error())
	}
	e := capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar died while the call ran")
	if err != nil {
		e = e.WithCause(err.Error())
	}
	return e
}

// rpcFailure maps a JSON-RPC error object onto the contract's codes.
func rpcFailure(method string, e *rpcError) *capability.Error {
	cause := e.dataText()
	switch e.Code {
	case codeMethodNotFound:
		return capability.Errorf(capability.ErrBuildSidecarUnavailable, "the Gradle sidecar does not implement %s", method).
			WithCause(cause).WithNextStep("update the sidecar jars so core and sidecar speak the same protocol")
	case codeApplication:
		return capability.Errorf(capability.ErrProjectModelFailed, "%s failed in Gradle", method).WithCause(cause)
	default:
		return capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar answered %s with error %d", method, e.Code).WithCause(cause)
	}
}

// useError maps a supervisor failure onto the contract's codes.
func (c *Client) useError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case errors.Is(err, process.ErrProtocol):
		return capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar broke the protocol before it was ready").WithCause(err.Error())
	case errors.Is(err, process.ErrClosed):
		return capability.Errorf(capability.ErrBuildSidecarUnavailable, "the daemon is shutting down, so the Gradle sidecar cannot start").WithCause(err.Error())
	default:
		return capability.Errorf(capability.ErrBuildSidecarUnavailable, "the Gradle sidecar could not be started or did not become ready").
			WithCause(err.Error()).
			WithNextStep("check that a JDK 17 or newer is installed and JAVA_HOME points at it, then retry")
	}
}

// fail records a sidecar crash against every root that has a connection, so a
// subsequent call reports the root's degradation (plan m1g D20).
func (c *Client) fail(_ *root, err error) {
	if e, ok := err.(*capability.Error); !ok || e.Code != capability.ErrBuildSidecarCrashed {
		return
	}
	c.mu.Lock()
	roots := make([]*root, 0, len(c.roots))
	for _, r := range c.roots {
		roots = append(roots, r)
	}
	c.mu.Unlock()
	for _, r := range roots {
		r.cmu.Lock()
		if r.opened {
			r.degraded = capability.AsError(err)
		}
		// The cached models go with the sidecar that produced them, so the next call
		// reaches a new sidecar and its handshake clears the degradation (D20).
		r.model, r.fingerprint = nil, ""
		r.cmu.Unlock()
	}
}

// clearDegraded clears every root's degradation: a successful handshake means the
// sidecar is whole again (plan m1g D20).
func (c *Client) clearDegraded() {
	c.mu.Lock()
	roots := make([]*root, 0, len(c.roots))
	for _, r := range c.roots {
		roots = append(roots, r)
	}
	c.mu.Unlock()
	for _, r := range roots {
		r.cmu.Lock()
		r.degraded = nil
		r.cmu.Unlock()
	}
}

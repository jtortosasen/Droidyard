package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
)

type none struct{}

func (none) Missing(capability.Requirement) (string, bool) { return "", false }

// shortDir is a state root under /tmp, short enough that the socket stays inside it
// rather than moving to the sun_path fallback under /tmp/droidyard-<uid>/.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dyd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// blocker is a test-only capability, fixture.block: it hands the test its context and
// operation id, then blocks until released or cancelled.
type blocker struct {
	started chan blocked
	release chan struct{}
}

type blocked struct {
	id  string
	ctx context.Context
}

type harness struct {
	d      *Daemon
	ops    *capability.Operations
	block  blocker
	socket string
	state  string
}

func start(t *testing.T, uid int) *harness {
	t.Helper()
	state := shortDir(t)
	return startAt(t, uid, state, filepath.Join(state, "run", "droidyard.sock"))
}

func startAt(t *testing.T, uid int, state, socket string) *harness {
	t.Helper()
	h := &harness{block: blocker{started: make(chan blocked, 4), release: make(chan struct{})}}
	reg := capability.NewRegistry()
	h.ops = capability.NewOperations(reg)
	system.Register(reg, none{}, nil, h.ops, nil)
	h.ops.RegisterCapabilities()
	reg.MustRegister(capability.Descriptor{
		Name: "fixture.block", Kind: capability.Command, Scope: capability.Global,
		Description: "Blocks until released.", Params: struct{}{}, Result: struct {
			Released bool `json:"released"`
		}{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			h.block.started <- blocked{id: call.OperationID, ctx: ctx}
			select {
			case <-h.block.release:
				return map[string]bool{"released": true}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})

	h.state, h.socket = state, socket
	d, err := Start(Options{
		State: h.state, Socket: h.socket, Version: system.Version, UID: uid, Ops: h.ops,
		Handlers: map[capability.ClientKind]Handler{capability.ClientCLI: Native(reg, h.ops)},
		Log:      testLog{t},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.d = d
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	})
	return h
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func hello(version string) Hello {
	return Hello{Protocol: Protocol, Version: version, Client: capability.ClientCLI, Cwd: "/"}
}

// session is a raw client: one connection past its hello.
type session struct {
	conn net.Conn
	r    *bufio.Reader
}

func (h *harness) dial(t *testing.T) *session {
	t.Helper()
	conn, r, _, err := Dial(h.socket, hello(system.Version))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &session{conn: conn, r: r}
}

func (s *session) send(t *testing.T, id, capName, params string) {
	t.Helper()
	line := `{"id":"` + id + `","capability":"` + capName + `"`
	if params != "" {
		line += `,"params":` + params
	}
	if _, err := s.conn.Write([]byte(line + "}\n")); err != nil {
		t.Fatal(err)
	}
}

func (s *session) recv(t *testing.T) capability.ResponseEnvelope {
	t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := s.r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading a response: %v", err)
	}
	var env capability.ResponseEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func (h *harness) started(t *testing.T) blocked {
	t.Helper()
	select {
	case b := <-h.block.started:
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("fixture.block never started")
	}
	return blocked{}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// TestHelloFromAnotherVersionIsRefused: a shim of another version gets
// DAEMON_VERSION_MISMATCH, telling it to stop the stale daemon (ADR-0019 §7), and no
// session.
func TestHelloFromAnotherVersionIsRefused(t *testing.T) {
	h := start(t, os.Getuid())
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"hello":{"protocol":1,"version":"0.0.0","client":"cli","cwd":"/"}}` + "\n"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no reply to a mismatched hello: %v", err)
	}
	var reply HelloReply
	json.Unmarshal(line, &reply)
	if reply.Error == nil || reply.Error.Code != capability.ErrDaemonVersionMismatch || !strings.Contains(reply.Error.NextStep, "droidyard stop") || reply.Session != "" {
		t.Fatalf("a hello with version 0.0.0 got %s; want DAEMON_VERSION_MISMATCH, nextStep naming droidyard stop, no session", line)
	}
	if n := h.d.Sessions(); n != 0 {
		t.Errorf("a refused hello left %d session(s)", n)
	}
	// Dial reports the refusal as the typed error.
	if _, _, _, err := Dial(h.socket, hello("0.0.0")); !errors.As(err, new(*capability.Error)) {
		t.Errorf("Dial with another version returned %v; want the typed error", err)
	}
}

// TestPeerUidMismatchIsRefused: the daemon reads the peer's uid with the real
// getsockopt on a real connection and refuses, without answering, a peer whose uid is
// not the one it expects (ADR-0019 §6). No second account exists, so the expectation
// is moved instead: real uid + 1 refuses this process, the real uid admits it.
func TestPeerUidMismatchIsRefused(t *testing.T) {
	try := func(t *testing.T, expect int) ([]byte, error) {
		h := start(t, expect)
		conn, err := net.Dial("unix", h.socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte(`{"hello":{"protocol":1,"version":"` + system.Version + `","client":"cli","cwd":"/"}}` + "\n"))
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		return bufio.NewReader(conn).ReadBytes('\n')
	}

	t.Run("another uid is refused", func(t *testing.T) {
		got, err := try(t, os.Getuid()+1)
		if len(got) != 0 {
			t.Fatalf("a peer of an unexpected uid got an answer: %s", got)
		}
		if !errors.Is(err, io.EOF) {
			t.Fatalf("the connection was not closed by the daemon: %v", err)
		}
	})
	t.Run("the expected uid is answered", func(t *testing.T) {
		got, _ := try(t, os.Getuid())
		var reply HelloReply
		if err := json.Unmarshal(got, &reply); err != nil || reply.Session == "" {
			t.Fatalf("a peer of the expected uid got %q; want a session", got)
		}
	})
	t.Run("getsockopt reads this process's uid", func(t *testing.T) {
		sock := filepath.Join(shortDir(t), "p.sock")
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		client, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		server, err := ln.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		if uid, err := peerUID(server); err != nil || uid != os.Getuid() {
			t.Fatalf("peerUID read (%d, %v); want %d", uid, err, os.Getuid())
		}
	})
}

// TestSocketIsPrivate: the socket is 0600 in a 0700 directory (ADR-0019 §6), and the
// lock holder removes a stale socket but nothing else (ADR-0019 §5).
func TestSocketIsPrivate(t *testing.T) {
	h := start(t, os.Getuid())
	for path, want := range map[string]os.FileMode{filepath.Dir(h.socket): 0o700, h.socket: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s is %o; want %o", path, got, want)
		}
	}

	// A stale socket, left by a daemon that died without cleaning up.
	state := shortDir(t)
	sock := filepath.Join(state, "run", "droidyard.sock")
	os.MkdirAll(filepath.Dir(sock), 0o700)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	opts := Options{State: state, Socket: sock, Version: system.Version, UID: os.Getuid(), Ops: capability.NewOperations(capability.NewRegistry())}
	d, err := Start(opts)
	if err != nil {
		t.Fatalf("a stale socket stopped the lock holder: %v", err)
	}
	d.Shutdown(context.Background())

	// Anything that is not a socket stays, and the daemon does not start.
	os.WriteFile(sock, []byte("not a socket"), 0o600)
	if d, err := Start(opts); err == nil {
		d.Shutdown(context.Background())
		t.Fatal("the daemon removed a regular file at the socket path")
	}
	if data, _ := os.ReadFile(sock); string(data) != "not a socket" {
		t.Error("the regular file at the socket path was changed")
	}
	if _, held, _ := Holder(state); held {
		t.Error("a daemon that failed to bind kept the lock")
	}

	// The socket's directory must be a real directory of this user, and its parent
	// not writable by everyone: under the /tmp fallback another account could have
	// made either first.
	for name, prepare := range map[string]func(state string){
		"a world-writable parent": func(state string) { os.Chmod(state, 0o777) },
		"a symlinked directory": func(state string) {
			os.Symlink(shortDir(t), filepath.Join(state, "run"))
		},
	} {
		state := shortDir(t)
		prepare(state)
		opts := opts
		opts.State, opts.Socket = state, filepath.Join(state, "run", "droidyard.sock")
		if d, err := Start(opts); err == nil {
			d.Shutdown(context.Background())
			t.Errorf("the daemon bound its socket under %s", name)
		}
	}
}

// TestSessionsAreConnections: a session is one connection past its hello
// (ADR-0019 §Sessions), and the count drops when the connection ends.
func TestSessionsAreConnections(t *testing.T) {
	h := start(t, os.Getuid())
	if n := h.d.Sessions(); n != 0 {
		t.Fatalf("%d sessions before any connection", n)
	}
	a, b := h.dial(t), h.dial(t)
	if n := h.d.Sessions(); n != 2 {
		t.Fatalf("%d sessions after two hellos; want 2", n)
	}
	a.conn.Close()
	b.conn.Close()
	eventually(t, "the session count returns to 0", func() bool { return h.d.Sessions() == 0 })
}

// TestRequestsAreMatchedById: several requests share a connection, and each response
// carries its request's id, in the order they finish (docs/architecture/capability-api.md
// §Response envelope). system.health counts the running operation but not itself.
func TestRequestsAreMatchedById(t *testing.T) {
	h := start(t, os.Getuid())
	s := h.dial(t)

	s.send(t, "slow", "fixture.block", "")
	b := h.started(t)
	s.send(t, "fast", "system.health", "")
	fast := s.recv(t)
	if fast.ID != "fast" || fast.Operation == nil || fast.Operation.State != capability.StateCompleted {
		t.Fatalf("the first response is %+v; want the health call, completed, while the slow one runs", fast)
	}
	var health system.Health
	json.Unmarshal(fast.Result, &health)
	if health.Operations != 1 {
		t.Errorf("system.health reported %d operations while one other runs; want 1", health.Operations)
	}

	// operation.wait natively: the envelope carries the waited operation.
	s.send(t, "poll", "operation.wait", `{"operationId":"`+b.id+`","timeoutMs":0}`)
	poll := s.recv(t)
	if poll.ID != "poll" || poll.Operation == nil || poll.Operation.ID != b.id || poll.Operation.State != capability.StateRunning || poll.Result != nil || poll.Error != nil {
		t.Fatalf("operation.wait with timeoutMs 0 answered %+v; want operation %s running, no result, no error", poll, b.id)
	}

	close(h.block.release)
	slow := s.recv(t)
	if slow.ID != "slow" || slow.Operation == nil || slow.Operation.ID != b.id || string(slow.Result) != `{"released":true}` {
		t.Fatalf("the slow response is %+v (result %s); want its own id, operation and result", slow, slow.Result)
	}

	s.send(t, "nope", "nope.nope", "")
	if nope := s.recv(t); nope.Error == nil || nope.Error.Code != capability.ErrCapabilityNotFound || nope.Operation != nil {
		t.Fatalf("an unknown capability answered %+v; want REQUEST_CAPABILITY_NOT_FOUND and no operation", nope)
	}
}

// TestDisconnectDoesNotCancel: closing the connection that started an operation
// abandons the wait, never the operation (ADR-0020 §Adapter projections; ADR-0019
// §Sessions).
func TestDisconnectDoesNotCancel(t *testing.T) {
	h := start(t, os.Getuid())
	s := h.dial(t)
	s.send(t, "1", "fixture.block", "")
	b := h.started(t)
	s.conn.Close()
	eventually(t, "the session ends", func() bool { return h.d.Sessions() == 0 })

	if b.ctx.Err() != nil {
		t.Fatal("a disconnect cancelled the operation it started")
	}
	close(h.block.release)
	out, err := h.ops.Wait(context.Background(), b.id, time.Time{})
	if err != nil || out.State != capability.StateCompleted {
		t.Fatalf("the operation ended (%+v, %v); want completed", out, err)
	}
}

// TestShutdownCancelsRunningOperations: the shutdown path stops accepting, cancels
// running operations before it returns and tells their callers, closes the sessions,
// removes the socket and releases the lock (docs/architecture/process-lifecycle.md
// §Shutdown).
func TestShutdownCancelsRunningOperations(t *testing.T) {
	h := start(t, os.Getuid())
	s := h.dial(t)
	s.send(t, "1", "fixture.block", "")
	b := h.started(t)

	var wg sync.WaitGroup
	var env capability.ResponseEnvelope
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if line, err := s.r.ReadBytes('\n'); err == nil {
			json.Unmarshal(line, &env)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.d.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if b.ctx.Err() == nil {
		t.Fatal("Shutdown returned with the operation's handler context still live")
	}
	wg.Wait()
	if env.Error == nil || env.Error.Code != capability.ErrOperationCancelled || env.Operation == nil || env.Operation.State != capability.StateCancelled {
		t.Errorf("the caller of the running operation got %+v; want it cancelled with OPERATION_CANCELLED", env)
	}
	if h.d.Sessions() != 0 {
		t.Errorf("%d sessions after shutdown", h.d.Sessions())
	}
	if _, err := os.Lstat(h.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there after shutdown: %v", err)
	}
	if _, held, _ := Holder(h.state); held {
		t.Error("the lock is still held after shutdown")
	}
	if _, err := net.Dial("unix", h.socket); err == nil {
		t.Error("the daemon still accepts after shutdown")
	}
}

// TestLongStateRootsDoNotShareAFallbackSocket: two state roots whose socket paths
// exceed sun_path each get their own directory under /tmp/droidyard-<uid>/, so the
// second daemon does not remove the first one's live socket as stale (ADR-0019 §6,
// amended 2026-09-30). Real daemons, real binds in the real fallback.
func TestLongStateRootsDoNotShareAFallbackSocket(t *testing.T) {
	env := platform.Current()
	var started []*harness
	for _, name := range []string{"a", "b"} {
		state := filepath.Join(t.TempDir(), strings.Repeat("x", 100), name)
		sock := env.SocketPath(state)
		if !strings.HasPrefix(sock, "/tmp/droidyard-") {
			t.Fatalf("a %d-byte state root did not fall back: %s", len(state), sock)
		}
		// Removes the base as well if nothing else lives in it.
		t.Cleanup(func() { os.Remove(filepath.Dir(filepath.Dir(sock))) })
		t.Cleanup(func() { os.Remove(filepath.Dir(sock)) })
		started = append(started, startAt(t, os.Getuid(), state, sock))
	}
	for _, h := range started {
		// Each daemon answers at its own socket: the second start removed nothing of
		// the first. The session count says which daemon took the connection.
		conn, _, _, err := Dial(h.socket, hello(system.Version))
		if err != nil {
			t.Errorf("nothing answers at %s: %v", h.socket, err)
			continue
		}
		if n := h.d.Sessions(); n != 1 {
			t.Errorf("a connection to %s's socket is not a session of its daemon (%d sessions)", h.state, n)
		}
		conn.Close()
		fi, err := os.Stat(filepath.Dir(h.socket))
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("fallback directory %s: %v; want mode 0700", filepath.Dir(h.socket), err)
		}
	}
	if a, b := started[0], started[1]; filepath.Dir(a.socket) == filepath.Dir(b.socket) {
		t.Errorf("two state roots share the fallback directory %s", filepath.Dir(a.socket))
	}
}

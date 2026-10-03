package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/store"
)

// Session is one connection after a successful hello (ADR-0019 §Sessions; ADR-0010).
// Reader is the buffered reader that read the hello: it may already hold the bytes
// that followed it, so a handler reads from it, never from Conn directly.
type Session struct {
	ID     string
	Hello  Hello
	Conn   net.Conn
	Reader *bufio.Reader
}

// Handler serves one session until it ends. The daemon closes the connection when the
// handler returns. ctx ends when the daemon shuts down.
type Handler func(ctx context.Context, s *Session)

// Options configure a daemon.
type Options struct {
	// State is the state root; Socket the socket path derived from it.
	State  string
	Socket string
	// Version is the daemon's API version; a hello with another is refused.
	Version string
	// UID is the only peer uid accepted on the socket: the daemon's own in
	// production (ADR-0019 §6).
	UID int
	// Ops is the operation store; shutdown cancels its running operations.
	Ops *capability.Operations
	// Handlers serve a session by the client kind its hello names. A kind without a
	// handler is refused.
	Handlers map[capability.ClientKind]Handler
	// Store is the durable state under State; nil means a store of its own over State.
	Store *store.Store
	// Log receives one line per refused connection and per reconciled pid record.
	// Nil discards.
	Log io.Writer
}

// Daemon is a running daemon: it holds the lock and accepts on the socket.
type Daemon struct {
	opts Options
	lock *Lock
	ln   *net.UnixListener

	ctx    context.Context // ends when shutdown closes the sessions
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[*Session]struct{}
	wg       sync.WaitGroup // the accept loop and every connection
}

// Start takes the single-instance lock, reconciles the pid records a previous daemon
// left, prepares run/ and the socket, and starts accepting (ADR-0019 §5, §6). The
// records are reconciled by the lock holder, the single writer, before the socket is
// bound, so no client reaches a daemon while a previous one's orphans live (ADR-0021
// §Pid records).
func Start(opts Options) (*Daemon, error) {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Store == nil {
		st, err := store.New(opts.State)
		if err != nil {
			return nil, err
		}
		opts.Store = st
	}
	lock, err := Acquire(opts.State, LockInfo{
		PID:       os.Getpid(),
		StartTime: time.Now(),
		Version:   opts.Version,
		Socket:    opts.Socket,
	})
	if err != nil {
		return nil, err
	}
	// Unreadable records never stop the daemon: what could be reaped was.
	if err := process.Reconcile(opts.Store, opts.Log); err != nil {
		fmt.Fprintln(opts.Log, "droidyard: reconcile:", err)
	}
	ln, err := listen(opts.Socket)
	if err != nil {
		lock.Release()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{opts: opts, lock: lock, ln: ln, ctx: ctx, cancel: cancel, sessions: make(map[*Session]struct{})}
	d.wg.Add(1)
	go d.accept()
	return d, nil
}

// listen creates the socket's directory 0700, removes a stale socket, which only the
// lock holder may do, and binds the socket 0600.
func listen(path string) (*net.UnixListener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	// The directory may be under the /tmp fallback, /tmp/droidyard-<uid>/<root>/
	// (ADR-0019 §6), where another account could have created a path first. The
	// directory and its parent must be real directories of this user, the parent not
	// writable by everyone; the directory itself is made private.
	if err := ownDir(dir, false); err != nil {
		return nil, err
	}
	if err := ownDir(filepath.Dir(dir), true); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}

	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; refusing to remove it", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the stale socket: %w", err)
		}
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// ownDir refuses a path that is not a directory of this user (a symlink included),
// or, for a parent, one that everyone may write to.
func ownDir(dir string, parent bool) error {
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

func (d *Daemon) accept() {
	defer d.wg.Done()
	for {
		conn, err := d.ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			fmt.Fprintln(d.opts.Log, "droidyard: accept:", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer conn.Close()
			d.serve(conn)
		}()
	}
}

// serve checks the peer, reads the hello and hands the session to its handler.
func (d *Daemon) serve(conn *net.UnixConn) {
	uid, err := peerUID(conn)
	if err != nil || uid != d.opts.UID {
		// Refused without a word: an answer would tell another account that the
		// socket is live and what it speaks.
		fmt.Fprintf(d.opts.Log, "droidyard: refused a connection from uid %d (%v)\n", uid, err)
		return
	}

	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	r := bufio.NewReader(conn)
	raw, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var line helloLine
	if err := json.Unmarshal(raw, &line); err != nil || line.Hello == nil {
		fmt.Fprintln(d.opts.Log, "droidyard: closed a connection whose first line is not a hello")
		return
	}
	h := *line.Hello
	enc := json.NewEncoder(conn)
	if h.Protocol != Protocol || h.Version != d.opts.Version {
		enc.Encode(HelloReply{Version: d.opts.Version, Error: capability.Errorf(capability.ErrDaemonVersionMismatch,
			"this daemon is version %s (protocol %d); the client is version %s (protocol %d)", d.opts.Version, Protocol, h.Version, h.Protocol).
			WithNextStep("run `droidyard stop` to stop the running daemon, then retry")})
		return
	}
	handler, ok := d.opts.Handlers[h.Client]
	if !ok {
		fmt.Fprintf(d.opts.Log, "droidyard: closed a connection from unknown client kind %q\n", h.Client)
		return
	}
	conn.SetReadDeadline(time.Time{})

	s := &Session{ID: newSessionID(), Hello: h, Conn: conn, Reader: r}
	if !d.add(s) {
		return // shutting down
	}
	defer d.remove(s)
	if err := enc.Encode(HelloReply{Version: d.opts.Version, Session: s.ID}); err != nil {
		return
	}
	handler(d.ctx, s)
}

func (d *Daemon) add(s *Session) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sessions == nil {
		return false
	}
	d.sessions[s] = struct{}{}
	return true
}

func (d *Daemon) remove(s *Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, s)
}

// Sessions is the number of live sessions.
func (d *Daemon) Sessions() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

// Shutdown stops the daemon gracefully (docs/architecture/process-lifecycle.md
// §Shutdown): stop accepting connections and requests, cancel running operations,
// close the sessions, remove the socket, release the lock. A native session's callers
// hear that their operations ended cancelled; an MCP session is written nothing more
// once its input stops, since the adapter treats that as the end of the session.
// ctx bounds the wait for operations and sessions to end.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.ln.Close()

	d.mu.Lock()
	for s := range d.sessions {
		// Unblocks the readers; writes still go out.
		s.Conn.SetReadDeadline(time.Now())
	}
	d.mu.Unlock()

	opsErr := d.opts.Ops.Shutdown(ctx)

	// Give the sessions a moment to end by themselves; a native one first answers
	// what was in flight.
	for grace := time.Now().Add(2 * time.Second); d.Sessions() > 0 && time.Now().Before(grace) && ctx.Err() == nil; {
		time.Sleep(10 * time.Millisecond)
	}

	d.mu.Lock()
	for s := range d.sessions {
		s.Conn.Close()
	}
	d.sessions = nil
	d.mu.Unlock()
	d.cancel()

	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}

	if err := os.Remove(d.opts.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(d.opts.Log, "droidyard: removing the socket:", err)
	}
	lockErr := d.lock.Release()
	return errors.Join(opsErr, waitErr, lockErr)
}

func newSessionID() string {
	var b [8]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return "ses-" + hex.EncodeToString(b[:])
}

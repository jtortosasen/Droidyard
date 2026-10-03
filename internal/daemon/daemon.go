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
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	// Supervisor runs the daemon's child processes. Start loads it once the pid
	// records are reconciled; Shutdown stops what it runs. Nil: none.
	Supervisor *process.Supervisor
	// Log receives one line per refused connection and per reconciled pid record.
	// Nil discards.
	Log io.Writer
	// HTTP is the UI's listener; nil serves none. Built outside the daemon, so the
	// daemon imports no adapter (core-runtime.md §Adapter boundary).
	HTTP *HTTP
}

// HTTP is the UI's side of the daemon (docs/interfaces/http-websocket.md).
type HTTP struct {
	// Addr is the address asked for; Listen binds it, or another when it must.
	Addr   string
	Listen func(addr string) (net.Listener, error)
	// Handler serves every request, the security baseline included.
	Handler http.Handler
	// Token is the per-launch token, written to run/http-token for the browser
	// bootstrap and removed on exit.
	Token string
}

// TokenFile is the per-launch HTTP token's path under the state root (ADR-0021
// §Layout; http-websocket.md §Security baseline rule 4).
const TokenFile = "run/http-token"

// Daemon is a running daemon: it holds the lock and accepts on the socket.
type Daemon struct {
	opts Options
	lock *Lock
	ln   *net.UnixListener

	httpSrv  *http.Server
	httpLn   net.Listener
	httpDone chan struct{} // closed when the HTTP server has stopped serving

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
	// Only now may the supervisor spawn: before anything is spawned, the records are
	// reconciled (ADR-0021 §Pid records). One it cannot load spawns nothing.
	if opts.Supervisor != nil {
		if err := opts.Supervisor.Load(); err != nil {
			fmt.Fprintln(opts.Log, "droidyard: supervisor: no process can be started:", err)
		}
	}
	d := &Daemon{opts: opts, lock: lock, sessions: make(map[*Session]struct{})}
	// The HTTP listener is bound, and its token written, before the socket: whatever
	// reaches the socket finds them (plan m1d, R3).
	if opts.HTTP != nil {
		if err := d.bindHTTP(); err != nil {
			lock.Release()
			return nil, err
		}
		// The lock named no HTTP address until now (ADR-0019 §5, amended): it does
		// before the socket is bound, so a client that reaches the socket reads it.
		if err := lock.SetHTTP(d.HTTPAddr()); err != nil {
			d.closeHTTP()
			lock.Release()
			return nil, err
		}
	}
	ln, err := listen(opts.Socket)
	if err != nil {
		d.closeHTTP()
		lock.Release()
		return nil, err
	}
	d.ln = ln
	d.ctx, d.cancel = context.WithCancel(context.Background())
	if d.httpSrv != nil {
		go func() {
			defer close(d.httpDone)
			d.httpSrv.Serve(d.httpLn)
		}()
	}
	d.wg.Add(1)
	go d.accept()
	return d, nil
}

// bindHTTP binds the UI's listener and writes the token file.
func (d *Daemon) bindHTTP() error {
	h := d.opts.HTTP
	ln, err := h.Listen(h.Addr)
	if err != nil {
		return fmt.Errorf("binding the HTTP listener: %w", err)
	}
	if err := writeToken(d.opts.State, h.Token); err != nil {
		ln.Close()
		removeToken(d.opts.State) // closeHTTP is a no-op without d.httpLn
		return err
	}
	d.httpLn = ln
	d.httpSrv = &http.Server{Handler: h.Handler, ReadHeaderTimeout: 10 * time.Second}
	d.httpDone = make(chan struct{})
	return nil
}

// closeHTTP stops the UI's listener, if any, and removes the token file.
func (d *Daemon) closeHTTP() {
	if d.httpLn == nil {
		return
	}
	d.httpLn.Close()
	removeToken(d.opts.State)
}

// HTTPAddr is the address the UI's listener is bound to, "" without one.
func (d *Daemon) HTTPAddr() string {
	if d.httpLn == nil {
		return ""
	}
	return d.httpLn.Addr().String()
}

// ReadToken reads the per-launch HTTP token a running daemon wrote to run/http-token.
func ReadToken(state string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(state, TokenFile))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("%s is empty", TokenFile)
	}
	return token, nil
}

// writeToken writes the token to run/http-token, the file 0600 and its directory
// 0700, as 64 hex digits and a newline (plan m1d, D8). A file a crashed daemon left is
// replaced.
func writeToken(state, token string) error {
	path := filepath.Join(state, TokenFile)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing a stale %s: %w", TokenFile, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", TokenFile, err)
	}
	_, err = f.WriteString(token + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func removeToken(state string) error {
	err := os.Remove(filepath.Join(state, TokenFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
// §Shutdown): stop accepting connections and requests, cancel and drain running
// operations, stop the supervised processes, close the sessions, remove the socket,
// release the lock. The processes stop on their descriptors' own timeouts whatever is
// left of ctx, so operations that use up the deadline still leave none behind. A native session's callers
// hear that their operations ended cancelled; an MCP session is written nothing more
// once its input stops, since the adapter treats that as the end of the session.
// ctx bounds the wait for operations and sessions to end.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.ln.Close()
	// The HTTP listener stops accepting with the socket; requests in flight end with
	// their waits, which the operations' shutdown ends.
	httpStopped := make(chan struct{})
	if d.httpSrv != nil {
		var stopping sync.WaitGroup
		stopping.Add(1)
		go func() {
			defer stopping.Done()
			d.httpSrv.Shutdown(ctx)
		}()
		// A hijacked connection, a WebSocket, is not the HTTP server's to wait for: a
		// handler that holds some says so, and shutdown waits for them within ctx.
		if c, ok := d.opts.HTTP.Handler.(interface{ CloseSessions(context.Context) }); ok {
			stopping.Add(1)
			go func() {
				defer stopping.Done()
				c.CloseSessions(ctx)
			}()
		}
		go func() { stopping.Wait(); close(httpStopped) }()
	} else {
		close(httpStopped)
	}

	d.mu.Lock()
	for s := range d.sessions {
		// Unblocks the readers; writes still go out.
		s.Conn.SetReadDeadline(time.Now())
	}
	d.mu.Unlock()

	opsErr := d.opts.Ops.Shutdown(ctx)
	if d.opts.Supervisor != nil {
		d.opts.Supervisor.Shutdown()
	}

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

	if d.httpSrv != nil {
		select {
		case <-httpStopped:
		case <-ctx.Done():
		}
		d.httpSrv.Close()
		<-d.httpDone
		if err := removeToken(d.opts.State); err != nil {
			fmt.Fprintln(d.opts.Log, "droidyard: removing the HTTP token:", err)
		}
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

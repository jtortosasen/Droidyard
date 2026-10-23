package chats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// Cursor keeps its chats in SQLite databases, read through the `sqlite3` command as git and
// adb are run (ADR-0127 §3, ADR-0005), because the daemon builds with CGO_ENABLED=0
// (ADR-0068 §1). This file is the only place a sqlite3 starts. What makes it safe to point at
// a database a running client owns:
//
//   - `-init /dev/null` and `-safe`: no ~/.sqliterc runs, and the shell refuses the commands
//     that reach outside the database (.shell, .system, .load, .output, ...). The argument
//     after the file is a query built from constants and quoted literals, never user text.
//   - `-readonly` and a URI of `mode=ro`: the database is opened for reading and nothing is
//     written to it or its -wal. A reader of a WAL database still maps and updates its
//     `-shm` index (observed: plan m3c step 1 (e)); that file is not store data, and the
//     read never creates a file that was not there.
//   - A WAL database that is cleanly closed (no -wal, no -shm) cannot be opened `mode=ro`
//     (sqlite3 would have to create the index: observed, error 14), so it is opened
//     `immutable=1`, which is exact because nothing can be writing it: a database with a
//     side file is never opened that way, since immutable would miss the rows in the -wal.
//   - The database is opened only when it is a regular file by Lstat: a link is not
//     followed. (The check and the open are two steps; a link swapped between them is
//     opened read-only all the same.)
//   - The process is started through the supervisor's Starter, in a group of its own, and
//     the group is killed at sqliteTimeout or when the call ends; its output is capped.

const (
	// sqliteTimeout is how long one sqlite3 may run (working default, ADR-0127 §3).
	sqliteTimeout = 3 * time.Second
	// sqliteMaxOut is the most stdout one sqlite3 may write (ADR-0127 §3).
	sqliteMaxOut = 4 << 20
	// sqliteMaxErr bounds the stderr kept; only its first line is used.
	sqliteMaxErr = 4 << 10
	// sqliteWaitDelay bounds how long the call waits for the output pipes after the process
	// exited or was killed.
	sqliteWaitDelay = 250 * time.Millisecond
)

var (
	errNoSQLite  = errors.New("sqlite3 not found on PATH")
	errNoStarter = errors.New("no process starter is configured")
)

// sqliteRunner starts sqlite3.
type sqliteRunner struct {
	start   device.Starter
	timeout time.Duration
	maxOut  int
}

// sqliteArgs is the argument list of one run: the fixed options, the database URI and the
// query. The query is the last argument and starts with SELECT, so it can be neither an
// option nor a dot-command.
func sqliteArgs(uri, query string) []string {
	return []string{"-init", "/dev/null", "-safe", "-readonly", "-json", uri, query}
}

// query runs one query against the database at db and returns sqlite3's stdout: a JSON array
// of rows, or nothing when there are none. The error says why in a sentence fit for a
// source's cause.
func (r sqliteRunner) query(parent context.Context, db, query string) ([]byte, error) {
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, errNoSQLite
	}
	if r.start == nil {
		return nil, errNoStarter
	}
	fi, err := os.Lstat(db)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, sqliteArgs(dbURI(db), query)...)
	// Only what sqlite3 can need: a search path and a temporary directory. The user's
	// environment is not handed on.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.Getenv("TMPDIR")}
	// The group, not the pid, as git is killed: a process sqlite3 started is in its group.
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = sqliteWaitDelay
	out := &capWriter{max: r.maxOut, onCut: cancel}
	errb := &capWriter{max: sqliteMaxErr}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := r.start(cmd); err != nil {
		return nil, fmt.Errorf("cannot start sqlite3: %w", err)
	}
	werr := cmd.Wait()
	switch {
	case out.cut():
		return nil, fmt.Errorf("sqlite3 wrote more than %d MiB", r.maxOut>>20)
	case parent.Err() != nil:
		return nil, parent.Err()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("sqlite3 did not finish in %s", r.timeout)
	case werr == nil, errors.Is(werr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0:
		return out.bytes(), nil
	}
	if line := firstLine(errb.bytes()); line != "" {
		return nil, errors.New("sqlite3: " + line)
	}
	return nil, fmt.Errorf("sqlite3: %v", werr)
}

// firstLine is the first line of b, cleaned and cut: the cause a failed run gives.
func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	s = strings.TrimSpace(capability.StripInvisible(s))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// dbURI is the URI sqlite3 opens the database at: `file:` and the path percent-encoded (a
// `?`, `#` or `%` in a path must not be read as URI syntax), with mode=ro, or immutable=1 for
// a WAL database that is cleanly closed.
func dbURI(db string) string {
	mode := "mode=ro"
	if closedWAL(db) {
		mode = "immutable=1"
	}
	return "file:" + pctEncode(db) + "?" + mode
}

// pctEncode percent-encodes every byte of a path but the unreserved ones and '/'.
func pctEncode(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// closedWAL reports whether the database is in WAL mode (its header's file format versions,
// bytes 18 and 19, are both 2) and has neither a -wal nor a -shm beside it.
func closedWAL(db string) bool {
	f, err := os.Open(db)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [20]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return false
	}
	if string(h[:16]) != "SQLite format 3\x00" || h[18] != 2 || h[19] != 2 {
		return false
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(db + side); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// capWriter keeps the first max bytes written and drops the rest, still accepting them so
// the child never blocks on a full pipe. The first byte over the cap calls onCut, which ends
// the child.
type capWriter struct {
	mu    sync.Mutex
	buf   []byte
	max   int
	over  bool
	onCut func()
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n := len(p)
	room := max(w.max-len(w.buf), 0)
	cut := false
	if n > room {
		p = p[:room]
		if !w.over {
			w.over, cut = true, true
		}
	}
	w.buf = append(w.buf, p...)
	w.mu.Unlock()
	if cut && w.onCut != nil {
		w.onCut()
	}
	return n, nil
}

func (w *capWriter) cut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.over
}

func (w *capWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...)
}

// sqlLiteral is s as a SQL string literal: quoted, with each ' doubled. A candidate path is
// the only text that reaches a query, and it reaches it here.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

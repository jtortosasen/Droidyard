package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// OpenFile is one file a process holds open, as lsof names it: FD is its descriptor field
// (cwd, txt, a number) and Path its name.
type OpenFile struct {
	Pid  int
	FD   string
	Path string
}

// Lsof runs lsof by an absolute path, never through PATH (ADR-0129 §8). The zero value is
// not usable; DefaultLsof gives the shipped one, and tests replace its fields.
type Lsof struct {
	Path      string
	Timeout   time.Duration
	MaxOutput int
}

const (
	lsofTimeout   = 5 * time.Second
	lsofMaxOutput = 4 << 20
)

// DefaultLsof is the lsof of this system: /usr/sbin/lsof on macOS, /usr/bin/lsof else
// /usr/sbin/lsof on Linux, killed at 5 s with its output capped at 4 MiB.
func DefaultLsof() Lsof {
	p := "/usr/sbin/lsof"
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/usr/bin/lsof"); err == nil {
			p = "/usr/bin/lsof"
		}
	}
	return Lsof{Path: p, Timeout: lsofTimeout, MaxOutput: lsofMaxOutput}
}

// OpenUnder lists the files the processes pids hold open inside any of dirs.
func OpenUnder(pids []int, dirs []string) ([]OpenFile, error) {
	return DefaultLsof().OpenUnder(context.Background(), pids, dirs)
}

// OpenUnder runs lsof over pids and returns the files held open inside dirs. The error is
// a doubt, and a caller must read it as "could not check": lsof absent, killed by our
// timeout, our cap or a cancel, killed by a signal we did not send, an output with a line
// that is not a p, f or n field, or a pid still alive (and not a zombie) that lsof did not
// list, which it always does for a live process (it prints its cwd and text file, spike
// m3f step 1 (f)). The exit status is not read: lsof exits 1 for a pid that vanished, with
// the rest of its output intact.
func (l Lsof) OpenUnder(ctx context.Context, pids []int, dirs []string) ([]OpenFile, error) {
	if len(pids) == 0 {
		return nil, nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	cctx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	out := &capWriter{max: l.MaxOutput, stop: cancel}
	cmd := exec.CommandContext(cctx, l.Path, "-n", "-P", "-w", "-F", "pfn", "-p", strings.Join(ids, ","))
	cmd.Stdout = out
	var killed atomic.Bool
	cmd.Cancel = func() error {
		killed.Store(true)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsof could not run: %w", err)
	}
	werr := cmd.Wait()
	switch {
	case out.over:
		return nil, fmt.Errorf("lsof printed more than %d bytes", l.MaxOutput)
	case killed.Load() && errors.Is(cctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("lsof did not finish in %s", l.Timeout)
	case killed.Load():
		return nil, errors.New("lsof was cancelled")
	}
	var ee *exec.ExitError
	switch {
	case errors.As(werr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return nil, fmt.Errorf("lsof was killed by signal %s", ws.Signal())
		}
	case werr != nil:
		return nil, fmt.Errorf("lsof failed: %w", werr)
	}

	files, listed, err := parseLsof(out.buf.Bytes())
	if err != nil {
		return nil, err
	}
	for _, pid := range pids {
		if listed[pid] {
			continue
		}
		st, serr := processState(pid)
		if serr != nil {
			return nil, fmt.Errorf("lsof did not list pid %d and its state could not be read: %v", pid, serr)
		}
		if st.Present && !st.Zombie {
			return nil, fmt.Errorf("lsof did not list pid %d, which is alive", pid)
		}
	}
	var held []OpenFile
	for _, f := range files {
		if underAny(f.Path, dirs) {
			held = append(held, f)
		}
	}
	return held, nil
}

// parseLsof reads lsof -F pfn output: every line begins p, f or n, and an f or n line
// follows a p line. It returns every file and the pids that had a p line.
func parseLsof(b []byte) (files []OpenFile, listed map[int]bool, err error) {
	listed = map[int]bool{}
	pid := 0
	fd := ""
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			line, b = b, nil
		}
		if len(line) == 0 {
			return nil, nil, errors.New("lsof printed an empty line")
		}
		switch line[0] {
		case 'p':
			n, perr := strconv.Atoi(string(line[1:]))
			if perr != nil {
				return nil, nil, fmt.Errorf("lsof printed a process line %q", clip(line))
			}
			pid, fd = n, ""
			listed[n] = true
		case 'f':
			if pid == 0 {
				return nil, nil, errors.New("lsof printed a file before any process")
			}
			fd = string(line[1:])
		case 'n':
			if pid == 0 {
				return nil, nil, errors.New("lsof printed a name before any process")
			}
			files = append(files, OpenFile{Pid: pid, FD: fd, Path: string(line[1:])})
		default:
			return nil, nil, fmt.Errorf("lsof printed a line %q that is not a p, f or n field", clip(line))
		}
	}
	return files, listed, nil
}

func clip(b []byte) string {
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}

// underAny reports whether path is one of dirs or inside one.
func underAny(path string, dirs []string) bool {
	for _, d := range dirs {
		d = filepath.Clean(d)
		if path == d || strings.HasPrefix(path, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// capWriter keeps what lsof prints up to max bytes; past it, it stops lsof (our kill).
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
	stop context.CancelFunc
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.max {
		w.over = true
		w.stop()
		return len(p), nil
	}
	return w.buf.Write(p)
}

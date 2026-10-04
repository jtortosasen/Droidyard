package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// waitDelay bounds how long Run waits for the output pipe after the client exited or
// was killed: a server the client forked inherits the pipe and would otherwise hold
// the call for as long as it lives (plan m1e, R8).
const waitDelay = 250 * time.Millisecond

// Starter starts cmd in a process group of its own and returns once it runs; the
// caller waits for it. The daemon passes the supervisor's locked spawner
// (process.Supervisor.Spawn), through which every child it starts goes
// (process-lifecycle.md §Shutdown, layer 2).
type Starter func(cmd *exec.Cmd) error

// Errors Run returns besides a failure to start.
var (
	// ErrTimeout: the client ran past its timeout and was killed.
	ErrTimeout = errors.New("adb did not finish in time")
	// ErrNoStarter: the runner was built without a Starter.
	ErrNoStarter = errors.New("no process starter is configured")
)

// Output is what one adb client run produced.
type Output struct {
	Stdout, Stderr []byte
	// Truncated: either stream passed the cap, and the rest was dropped.
	Truncated bool
	// ExitCode is the client's; -1 if it did not exit by itself (timeout, cancel).
	ExitCode int
}

// Run starts the adb client at path with args and the daemon's whole environment, and
// waits for it. Only the client's own pid is ever killed, on timeout or when ctx ends,
// never its process group: a server the client forked is the shared adb server, which
// Droidyard never kills implicitly (adb-device.md §Lifecycle and concurrency). Each
// stream keeps at most limit bytes, enforced as it is read.
//
// A non-zero exit is not an error: it is in Output.ExitCode. A client that exited 0
// is a success even if a forked process still holds its output pipe.
func (a *ADB) Run(ctx context.Context, path string, timeout time.Duration, limit int, args ...string) (Output, error) {
	if a.exec != nil {
		return a.runExec(ctx, path, timeout, limit, args)
	}
	if a.start == nil {
		return Output{}, ErrNoStarter
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = a.env
	// The default Cancel kills the client's pid only; the group is never signalled.
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = waitDelay
	out, errb := &capped{max: limit}, &capped{max: limit}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := a.start(cmd); err != nil {
		return Output{}, fmt.Errorf("starting adb: %w", err)
	}
	err := cmd.Wait()
	res := Output{Stdout: out.bytes(), Stderr: errb.bytes(), Truncated: out.cut || errb.cut, ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		res.ExitCode = -1
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, ErrTimeout
		}
		return res, ctx.Err()
	case err == nil:
		return res, nil
	case errors.Is(err, exec.ErrWaitDelay) && res.ExitCode == 0:
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return res, nil
	}
	return res, err
}

// runExec is Run over an in-process Exec: the same timeout, cap and error mapping, no
// process.
func (a *ADB) runExec(ctx context.Context, path string, timeout time.Duration, limit int, args []string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A client is not started under a context that has already ended, and the error is
	// the one exec.CommandContext's start gives: a cleanup that inherits a cancelled
	// context leaves no trace, as with a process.
	if err := ctx.Err(); err != nil {
		return Output{}, fmt.Errorf("starting adb: %w", err)
	}
	so, se, code, err := a.exec(ctx, path, args)
	res := Output{ExitCode: code}
	var c1, c2 bool
	res.Stdout, c1 = capBytes(so, limit)
	res.Stderr, c2 = capBytes(se, limit)
	res.Truncated = c1 || c2
	switch {
	case ctx.Err() != nil:
		res.ExitCode = -1
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, ErrTimeout
		}
		return res, ctx.Err()
	case err != nil:
		res.ExitCode = -1
		return res, fmt.Errorf("starting adb: %w", err)
	}
	return res, nil
}

func capBytes(b []byte, limit int) ([]byte, bool) {
	if len(b) > limit {
		return append([]byte(nil), b[:limit]...), true
	}
	return append([]byte(nil), b...), false
}

// capped keeps the first max bytes written and discards the rest, still accepting
// them so the client never blocks on a full pipe.
type capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max - c.buf.Len()
	if room < 0 {
		room = 0
	}
	n := len(p)
	if n > room {
		c.cut = true
		p = p[:room]
	}
	c.buf.Write(p)
	return n, nil
}

func (c *capped) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

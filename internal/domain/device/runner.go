package device

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// Stream is a client started through a StreamStarter: its pid, and its end, which
// removes the pid record it was started with.
type Stream interface {
	Pid() int
	Wait() error
}

// StreamStarter starts cmd in a process group of its own, records its pid for its life
// and returns once it runs; the caller waits for it with Stream.Wait. The daemon passes
// the supervisor's SpawnRecorded through a thin adapter; a test its own. An error
// wrapping ErrStreamUnrecorded means the record could not be written and the client was
// killed by its pid before it was handed back; any other error is a failure to start.
type StreamStarter func(cmd *exec.Cmd, kind string) (Stream, error)

// deviceStreamKind is the kind of the pid record a follow's client gets (plan m1i D9).
const deviceStreamKind = "device-stream"

// Errors Run returns besides a failure to start.
var (
	// ErrTimeout: the client ran past its timeout and was killed.
	ErrTimeout = errors.New("adb did not finish in time")
	// ErrNoStarter: the runner was built without a Starter.
	ErrNoStarter = errors.New("no process starter is configured")
	// ErrStreamUnrecorded: a StreamStarter could not write the client's pid record, so
	// it killed the client and returns this; the follow reports DEVICE_LOGS_FAILED
	// (plan m1i D9).
	ErrStreamUnrecorded = errors.New("the stream client could not be recorded")
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
// ErrLineTooLong is a streamed line longer than the runner accepts.
var ErrLineTooLong = errors.New("a line passed the runner's length limit")

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

// maxLine is the largest stdout line RunLines hands to its callback: a logcat line is
// bounded at 16 KiB (plan m1i D7) and the scanner is given room well past it, so a
// valid line is never split and a runaway line is a scanner error, not a hang.
const maxLine = 1 << 20

// RunLines is Run for a stream consumed as it arrives: stdout is handed to onLine line
// by line, without its newline, as each arrives, under the same timeout, with only the
// client's pid killed on timeout or cancellation and the same WaitDelay. stderr is
// capped at stderrLimit. A non-zero exit is not an error, as in Run; onLine's own error
// ends the call, killing the client (plan m1i step 3 uses it for the bounded follow).
// line is only valid during the callback.
func (a *ADB) RunLines(ctx context.Context, path string, timeout time.Duration, stderrLimit int, onLine func(line []byte) error, args ...string) (Output, error) {
	if a.exec != nil {
		return a.runLinesExec(ctx, path, timeout, stderrLimit, onLine, args)
	}
	if a.start == nil {
		return Output{}, ErrNoStarter
	}
	return a.runLines(ctx, path, timeout, stderrLimit, onLine, plainStart(a.start), args)
}

// RunLinesRecorded is RunLines for a client that must be recorded while it runs: the
// client is started through the recorded starter, so its pid record lives as long as it
// does and a daemon that dies mid-stream leaves it findable (plan m1i D9). Everything
// else — the timeout, the pid-only kill, the stderr cap and the error mapping — is
// RunLines'. When the record cannot be written the starter leaves no client and returns
// an error wrapping ErrStreamUnrecorded.
func (a *ADB) RunLinesRecorded(ctx context.Context, path string, timeout time.Duration, stderrLimit int, onLine func(line []byte) error, args ...string) (Output, error) {
	if a.stream == nil {
		return Output{}, ErrNoStarter
	}
	return a.runLines(ctx, path, timeout, stderrLimit, onLine, func(cmd *exec.Cmd) (func() error, error) {
		st, err := a.stream(cmd, deviceStreamKind)
		if err != nil {
			return nil, err
		}
		return st.Wait, nil
	}, args)
}

// plainStart adapts a Starter to the shape runLines uses: the caller reaps the client
// with cmd.Wait.
func plainStart(start Starter) func(*exec.Cmd) (func() error, error) {
	return func(cmd *exec.Cmd) (func() error, error) {
		if err := start(cmd); err != nil {
			return nil, err
		}
		return cmd.Wait, nil
	}
}

// runLines is RunLines over a starter that also names the function that reaps the
// client, so a recorded client's record is removed by the same call that reaps it. A
// starter error is wrapped as a failure to start, so the caller's mapping is the same
// whichever starter it used.
func (a *ADB) runLines(ctx context.Context, path string, timeout time.Duration, stderrLimit int, onLine func(line []byte) error, start func(*exec.Cmd) (func() error, error), args []string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = a.env
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = waitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Output{}, fmt.Errorf("starting adb: %w", err)
	}
	errb := &capped{max: stderrLimit}
	cmd.Stderr = errb
	wait, err := start(cmd)
	if err != nil {
		return Output{}, fmt.Errorf("starting adb: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var lineErr error
	for sc.Scan() {
		if e := onLine(sc.Bytes()); e != nil {
			lineErr = e
			cancel()
			break
		}
	}
	// A line over maxLine stops the scanner (bufio.ErrTooLong): treated like a callback
	// error, so the client is cancelled and drained instead of the rest of a follow
	// being lost silently until its window (step 3 review 3).
	if lineErr == nil && errors.Is(sc.Err(), bufio.ErrTooLong) {
		lineErr = ErrLineTooLong
		cancel()
	}
	if lineErr != nil {
		io.Copy(io.Discard, stdout) // unblock a client still writing to a full pipe
	}
	err = wait()
	res := Output{Stderr: errb.bytes(), Truncated: errb.cut, ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case lineErr != nil:
		return res, lineErr
	case ctx.Err() != nil:
		res.ExitCode = -1
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, ErrTimeout
		}
		return res, ctx.Err()
	case err == nil:
		if serr := sc.Err(); serr != nil && !errors.Is(serr, bufio.ErrTooLong) {
			return res, serr
		}
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

// runLinesExec is RunLines over an in-process Exec, which returns its whole output at
// once: the lines are delivered in order and the timeout, cap and mapping are the
// same as with a process.
func (a *ADB) runLinesExec(ctx context.Context, path string, timeout time.Duration, stderrLimit int, onLine func([]byte) error, args []string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Output{}, fmt.Errorf("starting adb: %w", err)
	}
	so, se, code, err := a.exec(ctx, path, args)
	res := Output{ExitCode: code}
	res.Stderr, res.Truncated = capBytes(se, stderrLimit)
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
	for _, l := range rawLines(so) {
		if e := onLine([]byte(l)); e != nil {
			return res, e
		}
	}
	return res, nil
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

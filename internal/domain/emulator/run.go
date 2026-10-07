package emulator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// waitDelay bounds how long a launcher run waits for its output pipe after the client
// exited or was killed, as the device runner's does (device/runner.go): a launcher that
// forks and holds the pipe would otherwise hold the call.
const waitDelay = 250 * time.Millisecond

// launcherRun runs the launcher at path with args and the daemon's environment, through
// the same locked spawner adb clients use (device.Starter; process-lifecycle.md), and
// waits for it. It reuses the device runner's semantics — the client's pid only is killed
// on timeout, WaitDelay, bounded output — for a binary that is not adb, which is why it
// cannot call device.Run directly. Errors are device.ErrTimeout on timeout,
// device.ErrNoStarter when the daemon has no spawner, or the error starting the launcher.
func (e *Emulator) launcherRun(ctx context.Context, path string, timeout time.Duration, limit int, args ...string) (device.Output, error) {
	if e.start == nil {
		return device.Output{ExitCode: -1}, device.ErrNoStarter
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = e.env
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = waitDelay
	out, errb := &capped{max: limit}, &capped{max: limit}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := e.start(cmd); err != nil {
		return device.Output{ExitCode: -1}, fmt.Errorf("starting emulator: %w", err)
	}
	werr := cmd.Wait()
	res := device.Output{Stdout: out.bytes(), Stderr: errb.bytes(), Truncated: out.cut || errb.cut, ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		res.ExitCode = -1
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, device.ErrTimeout
		}
		return res, ctx.Err()
	case werr == nil:
		return res, nil
	case errors.Is(werr, exec.ErrWaitDelay) && res.ExitCode == 0:
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		return res, nil
	}
	return res, werr
}

// capped keeps the first max bytes written and discards the rest, still accepting them so
// the launcher never blocks on a full pipe (device/runner.go's capped).
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

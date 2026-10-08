package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"golang.org/x/sys/unix"
)

const (
	// shellCommandKind is the pid record kind a shell.exec command gets (plan m1k D5;
	// process.ShellCommand's value). It is spelled here so this package imports no
	// process; a test asserts the two agree.
	shellCommandKind = "shell-command"

	envMaxEntries = 64
	envMaxBytes   = 32 << 10

	commandMaxBytes  = 8192
	timeoutMinMs     = 1000
	timeoutMaxMs     = 600000
	timeoutDefaultMs = 60000

	// inlineBytes is how much of each stream is returned inline: the stream's last
	// 16 KiB.
	inlineBytes = 16 << 10
	// captureBytes is how much of each stream the artifact keeps: the stream's first
	// 4 MiB, capped while capturing as the build console is.
	captureBytes = 4 << 20

	// waitDelay bounds how long Wait waits for the output pipes after the shell exited
	// or was killed: a background child holding stdout cannot hold the call (D5).
	waitDelay = 1 * time.Second
	// termGrace is how long the group has to go after SIGTERM before it gets SIGKILL.
	termGrace = 2 * time.Second
	// killGrace is how long the group is polled after SIGKILL.
	killGrace = 1 * time.Second
	// pollEvery is the interval the group is polled at.
	pollEvery = 20 * time.Millisecond
)

// validate checks a Params against D5's bounds. A bad parameter is REQUEST_INVALID_PARAMS
// and nothing runs.
func validate(p Params) *capability.Error {
	switch {
	case p.Command == "":
		return capability.NewError(capability.ErrInvalidParams, "command is empty")
	case len(p.Command) > commandMaxBytes:
		return capability.Errorf(capability.ErrInvalidParams, "command is %d bytes; the most is %d", len(p.Command), commandMaxBytes)
	case strings.ContainsRune(p.Command, '\x00'):
		return capability.NewError(capability.ErrInvalidParams, "command contains a NUL byte")
	case p.TimeoutMs != 0 && (p.TimeoutMs < timeoutMinMs || p.TimeoutMs > timeoutMaxMs):
		return capability.Errorf(capability.ErrInvalidParams, "timeoutMs must be between %d and %d, got %d", timeoutMinMs, timeoutMaxMs, p.TimeoutMs)
	}
	return validateEnv(p.Env)
}

// validateEnv checks env: valid names, values without NUL, at most envMaxEntries entries
// and envMaxBytes of names plus values (plan m1p D1).
func validateEnv(env map[string]string) *capability.Error {
	if len(env) > envMaxEntries {
		return capability.Errorf(capability.ErrInvalidParams, "env has %d entries; the most is %d", len(env), envMaxEntries)
	}
	total := 0
	for k, v := range env {
		if !envName.MatchString(k) {
			return capability.Errorf(capability.ErrInvalidParams, "env name %q must match [A-Za-z_][A-Za-z0-9_]*", k)
		}
		if strings.ContainsRune(v, '\x00') {
			return capability.Errorf(capability.ErrInvalidParams, "env value of %q contains a NUL byte", k)
		}
		total += len(k) + len(v)
	}
	if total > envMaxBytes {
		return capability.Errorf(capability.ErrInvalidParams, "env is %d bytes (names and values); the most is %d", total, envMaxBytes)
	}
	return nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// mergeEnv applies env over base: an entry of base with a given name is replaced.
func mergeEnv(base []string, env map[string]string) []string {
	if len(env) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(env))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, over := env[name]; !over {
			out = append(out, kv)
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// timeout is the effective timeout: the parameter, or 60000 ms when it is absent.
func (p Params) timeout() time.Duration {
	ms := p.TimeoutMs
	if ms == 0 {
		ms = timeoutDefaultMs
	}
	return time.Duration(ms) * time.Millisecond
}

// run starts the command and waits for its end. The call ends when the shell exits, at
// the timeout, or when ctx ends (a cancel, a workspace close, a daemon shutdown); at
// every end the process group is killed, so nothing the command left behind outlives it
// (D5). A timeout is a completed result with timedOut true; a cancel is not a result.
func run(ctx context.Context, opts Options, root string, p Params) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	stdout, stderr := newCapture(), newCapture()
	cmd := exec.Command("/bin/sh", "-c", p.Command)
	cmd.Dir = root
	cmd.Env = mergeEnv(opts.Env, p.Env)
	// Stdin nil is the null device (exec's default): a command that reads stdin sees EOF.
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = waitDelay

	// A cancel or a workspace close that landed while the gate read the policy ends the
	// call here, before anything starts (m1k §Changes, step 2).
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	start := time.Now()
	rec, err := opts.Recorded(cmd, shellCommandKind)
	if err != nil {
		return Result{}, startError(cmd, err)
	}
	// A goroutine reaps the shell the moment it exits (or once WaitDelay has passed);
	// Recorded.Wait removes the pid record, so a record never outlives the command.
	exited := make(chan error, 1)
	go func() { exited <- rec.Wait() }()

	timedOut, cancelled, reaped := false, false, false
	select {
	case <-exited:
		reaped = true
	case <-ctx.Done():
		cancelled = !errors.Is(ctx.Err(), context.DeadlineExceeded)
		timedOut = !cancelled
	}
	// At every end the group is killed: a command's background processes end with the
	// call (process-lifecycle.md §Shutdown, cancellation and no-orphans).
	killGroup(rec)
	if cancelled || timedOut {
		// The reap is in flight; bound the wait so a stuck Wait cannot hang the call.
		select {
		case <-exited:
			reaped = true
		case <-time.After(termGrace + killGrace + waitDelay):
		}
	}
	if cancelled {
		return Result{}, ctx.Err()
	}
	// The status is read only once Wait has returned: before that the reaping goroutine
	// may still be writing cmd.ProcessState.
	var state *os.ProcessState
	if reaped {
		state = cmd.ProcessState
	}
	return opts.result(state, stdout, stderr, time.Since(start), timedOut)
}

// killGroup kills rec's process group: SIGTERM when it still has a member, SIGKILL after
// termGrace if it outlived it, then a bounded poll for it to vanish. A group with no
// member left is left alone.
func killGroup(rec Recorded) {
	pgid := rec.Pid()
	if !groupPresent(pgid) {
		return
	}
	rec.SignalGroup(syscall.SIGTERM)
	if !waitGroupGone(pgid, termGrace) {
		rec.SignalGroup(syscall.SIGKILL)
		waitGroupGone(pgid, killGrace)
	}
}

// groupPresent: kill(-pgid, 0) fails with ESRCH once no process is left in the group.
func groupPresent(pgid int) bool {
	return !errors.Is(unix.Kill(-pgid, 0), unix.ESRCH)
}

// waitGroupGone polls the group until it is gone or bound has passed; it reports whether
// the group is gone.
func waitGroupGone(pgid int, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for {
		if !groupPresent(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollEvery)
	}
}

// result renders the outcome of a command that ended: its status, the inline tails and
// the artifacts of any stream over the inline bound.
func (o Options) result(ps *os.ProcessState, stdout, stderr *capture, dur time.Duration, timedOut bool) (Result, error) {
	res := Result{
		TimedOut:    timedOut,
		DurationMs:  dur.Milliseconds(),
		StdoutBytes: stdout.total,
		StderrBytes: stderr.total,
	}
	if ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = signalName(ws.Signal())
		} else {
			code := ps.ExitCode()
			res.ExitCode = &code
		}
	}
	out, outRef, outCapped, err := o.stream("stdout", stdout)
	if err != nil {
		return Result{}, err
	}
	errb, errRef, errCapped, err := o.stream("stderr", stderr)
	if err != nil {
		return Result{}, err
	}
	res.Stdout, res.StdoutRef = out, outRef
	res.Stderr, res.StderrRef = errb, errRef
	res.Truncated = outCapped || errCapped
	return res, nil
}

// stream renders one captured stream: its inline tail (invalid UTF-8 replaced), and a
// reference to its first 4 MiB when it passed the inline bound. capped reports whether
// the stream passed the capture bound, so the artifact ends in a marker.
func (o Options) stream(name string, c *capture) (inline string, ref *artifact.Ref, capped bool, err error) {
	inline = strings.ToValidUTF8(string(c.tailBytes()), "�")
	if c.total <= inlineBytes {
		return inline, nil, false, nil
	}
	data := c.headBytes()
	if dropped := c.total - int64(len(data)); dropped > 0 {
		capped = true
		data = append(append([]byte(nil), data...), markerLine(name, captureBytes, dropped)...)
	}
	if o.Artifacts == nil {
		// No store (the golden registry never runs a handler): the inline tail stands.
		return inline, nil, capped, nil
	}
	r, perr := o.Artifacts.Put(artifact.Item{
		Data: data, MediaType: "text/plain", Untrusted: true, Producer: "shell.exec",
	})
	if perr != nil {
		// The command has run and had its effects: its result stands with the inline
		// tail and no reference, rather than a failure that loses the exit code.
		return inline, nil, capped, nil
	}
	return inline, &r, capped, nil
}

// markerLine is the closing line an artifact ended at the capture bound carries, in the
// build console's style: it names the cap and how many bytes were dropped (D6).
func markerLine(name string, at, dropped int64) []byte {
	return []byte(fmt.Sprintf("\n--- droidyard: shell %s truncated at %d bytes; %d bytes dropped ---\n", name, at, dropped))
}

// startError maps a starter failure to SHELL_EXEC_FAILED. When cmd.Process is set the
// command had started and may have had effects before its record could not be written
// (the starter kills its group); otherwise it never ran (D5).
func startError(cmd *exec.Cmd, err error) *capability.Error {
	if cmd.Process != nil {
		return capability.NewError(capability.ErrShellExecFailed, "the command was started but its pid record could not be written, so it was killed").
			WithCause("the command had started and may have had effects; its pid record could not be written: " + err.Error()).
			WithNextStep("check that droidyard's state directory is writable, then retry")
	}
	return capability.NewError(capability.ErrShellExecFailed, "the command could not be started").
		WithCause(err.Error()).
		WithNextStep("check that the project root exists and the command is valid, then retry")
}

// signalNames are the signal names a result reports, so a client reads SIGKILL rather
// than a number. An unmapped signal falls back to its number.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGSEGV: "SIGSEGV",
}

func signalName(s syscall.Signal) string {
	if n, ok := signalNames[s]; ok {
		return n
	}
	return "signal " + strconv.Itoa(int(s))
}

// capture collects one stream: the first captureBytes for the artifact, a rolling last
// inlineBytes for the inline tail, and the total byte count. Writes come from exec's
// copy goroutine, one per stream, and the reads happen after Wait has returned, so the
// lock is belt-and-braces against a WaitDelay copy that outlives the read.
type capture struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte
	total int64
}

func newCapture() *capture { return &capture{} }

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := captureBytes - len(c.head); room > 0 {
		if room > n {
			room = n
		}
		c.head = append(c.head, p[:room]...)
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > inlineBytes {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-inlineBytes:]...)
	}
	c.total += int64(n)
	return n, nil
}

func (c *capture) headBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.head...)
}

func (c *capture) tailBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.tail...)
}

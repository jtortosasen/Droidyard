// Package git is the daemon's read-only view of the project's Git repository, for
// the human UI (plan m2l; ADR-0015 §5). This file is the only place a git process is
// started.
//
// Why the daemon polls git. A commit, a checkout or a `git add` changes HEAD, the
// index, refs or packed refs, and in a linked worktree `.git` is a file; the file
// watch excludes `.git/` (ADR-0049). Asking git itself for the status once per tick
// covers every layout with one signal, working-tree edits included, and needs no OS
// watcher (plan m1j D7).
//
// The invocation. Every git the daemon runs is
//
//	git --no-optional-locks -c core.fsmonitor=false -c core.hooksPath=/dev/null \
//	    -c protocol.allow=never <overrides> <args>
//
// in the workspace root, with the daemon's environment minus every GIT_* variable and
// plus GIT_OPTIONAL_LOCKS=0, GIT_NO_LAZY_FETCH=1, GIT_ALLOW_PROTOCOL=none,
// GIT_TERMINAL_PROMPT=0 and LC_ALL=C:
//
//   - The optional-lock switch (flag and variable, so a git that ignores one still
//     honours the other) keeps `status` from rewriting a stale index and from taking
//     .git/index.lock, which would fail a commit another process is making. The price
//     is that a stat-dirty file is hashed again on every tick until some other git
//     refreshes the index.
//   - core.fsmonitor=false: `true` would start a git fsmonitor daemon Droidyard does not
//     own (process-lifecycle.md); a configured hook script is never run.
//   - GIT_NO_LAZY_FETCH=1, GIT_ALLOW_PROTOCOL=none and protocol.allow=never, on every
//     root: in a partial clone a missing object (status --find-renames, cat-file) makes
//     git fetch it, running the repository's core.sshCommand and writing objects into
//     .git. The daemon never fetches. GIT_NO_LAZY_FETCH needs git 2.45; an older git
//     relies on GIT_ALLOW_PROTOCOL, which a repository's protocol.<name>.allow cannot
//     override, as it does override protocol.allow.
//   - GIT_* from the daemon's environment (GIT_DIR, GIT_INDEX_FILE, GIT_WORK_TREE ...)
//     would point git at another repository.
//   - On a root that is not trusted (ADR-0015 §2) every configured filter driver, in
//     any scope, is overridden empty and not required, because a repository can define
//     a filter that is a command. The drivers are read again before every untrusted
//     call (a driver added between ticks is covered); `required` is overridden because
//     git dies on a required filter that produced nothing, and `git lfs install` sets
//     it globally.
//
// A git started here is in a process group of its own (the Starter) and the whole
// group is killed when the context ends or after the timeout, because a filter's child
// (git-lfs filter-process) must not outlive it. Stdout is capped as it is read.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device"
)

const (
	// runTimeout is the longest one git call may take.
	runTimeout = 10 * time.Second
	// stdoutLimit is the most stdout a call keeps; the rest is read and dropped.
	stdoutLimit = 4 << 20
	// stderrLimit is the most stderr a call keeps.
	stderrLimit = 64 << 10
	// waitDelay bounds how long Wait lingers for the output pipes after git exited or
	// was killed.
	waitDelay = 250 * time.Millisecond
)

// Errors run returns besides a failure to start.
var (
	// ErrNotFound: no git executable.
	ErrNotFound = errors.New("git is not installed")
	// ErrNoStarter: the runner was built without a Starter.
	ErrNoStarter = errors.New("no process starter is configured")
	// ErrTimeout: git ran past its timeout and its group was killed.
	ErrTimeout = errors.New("git did not finish in time")
	// ErrUnsafeDriver: the configured filter drivers cannot be listed completely (the
	// list passed the output cap), or a filter key cannot be mapped to a driver written
	// as a -c override (no filter. prefix, an empty name, '=' or a control character),
	// so a driver cannot be neutralised and the call is refused.
	ErrUnsafeDriver = errors.New("a configured filter driver cannot be overridden")
)

// result is what one git call produced.
type result struct {
	Stdout, Stderr []byte
	// Truncated: stdout passed the cap and the rest was dropped.
	Truncated bool
	// ExitCode is git's; -1 if it did not exit by itself (timeout, cancel).
	ExitCode int
}

// runner starts git.
type runner struct {
	start   device.Starter
	git     string
	env     []string
	timeout time.Duration
}

// newRunner builds a runner for the git executable at gitPath (empty means none), with
// the scrubbed form of environ. start is the supervisor's spawner, which puts the child
// in a process group of its own.
func newRunner(start device.Starter, gitPath string, environ []string) *runner {
	return &runner{start: start, git: gitPath, env: scrubEnv(environ), timeout: runTimeout}
}

// scrubEnv drops every GIT_* variable and LC_ALL from environ and appends the fixed
// ones.
func scrubEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+4)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

// run runs git with args in root and waits for it. stdin may be nil. trusted is
// policy.Trusted's answer for root, read by the caller for this call. A non-zero exit
// is not an error: it is in result.ExitCode. No caller parameter reaches argv except
// through args, which the callers fix.
func (r *runner) run(ctx context.Context, root string, trusted bool, stdin []byte, args ...string) (result, error) {
	if r.git == "" {
		return result{ExitCode: -1}, ErrNotFound
	}
	if r.start == nil {
		return result{ExitCode: -1}, ErrNoStarter
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var overrides []string
	if !trusted {
		drivers, err := r.drivers(ctx, root)
		if err != nil {
			return result{ExitCode: -1}, endOf(parent, ctx, err)
		}
		for _, d := range drivers {
			p := "filter." + d + "."
			overrides = append(overrides,
				"-c", p+"clean=", "-c", p+"smudge=", "-c", p+"process=", "-c", p+"required=false")
		}
	}
	res, err := r.exec(ctx, root, overrides, stdin, args)
	return res, endOf(parent, ctx, err)
}

// endOf maps an error to ErrTimeout when only the runner's own deadline ended ctx.
func endOf(parent, ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return err
}

// drivers returns every filter driver name configured in any scope, sorted.
func (r *runner) drivers(ctx context.Context, root string) ([]string, error) {
	res, err := r.exec(ctx, root, nil, nil, []string{"config", "-z", "--name-only", "--get-regexp", `^filter\.`})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 1: // no such key
		return nil, nil
	case 0:
	default:
		return nil, fmt.Errorf("reading the configured filters: git exited %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	if res.Truncated {
		// A driver listed past the cap would run; the list is not trusted incomplete.
		return nil, fmt.Errorf("%w: the list of configured filters passed %d bytes", ErrUnsafeDriver, stdoutLimit)
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range strings.Split(string(res.Stdout), "\x00") {
		if name == "" {
			continue // the trailing terminator
		}
		// filter.<driver>.<key>; the driver may hold dots. Git ignores a two-part name
		// (filter.<key>, no subsection): it has no driver. Any other key that cannot
		// become a -c override, an empty driver name included (git selects it with
		// `filter=` and runs it), refuses the call: never skipped silently.
		rest, ok := strings.CutPrefix(name, "filter.")
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnsafeDriver, name)
		}
		i := strings.LastIndex(rest, ".")
		if i < 0 {
			continue
		}
		d := rest[:i]
		if seen[d] {
			continue
		}
		if d == "" || strings.ContainsFunc(d, func(c rune) bool { return c == '=' || c < ' ' || c == 0x7f }) {
			return nil, fmt.Errorf("%w: %q", ErrUnsafeDriver, d)
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

// exec starts one git with the fixed prefix, overrides and args, and waits for it. ctx
// carries the timeout; when it ends the whole process group is killed.
func (r *runner) exec(ctx context.Context, root string, overrides []string, stdin []byte, args []string) (result, error) {
	argv := make([]string, 0, 8+len(overrides)+len(args))
	argv = append(argv, "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never")
	argv = append(argv, overrides...)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, r.git, argv...)
	cmd.Dir = root
	cmd.Env = r.env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// The group, not the pid: a filter git started is in git's group and holds git's
	// pipes. A starter that left no group of its own makes the group kill fail, and
	// then only the pid is killed.
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = waitDelay
	out, errb := &limited{max: stdoutLimit}, &limited{max: stderrLimit}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := r.start(cmd); err != nil {
		return result{ExitCode: -1}, fmt.Errorf("starting git: %w", err)
	}
	err := cmd.Wait()
	res := result{Stdout: out.bytes(), Stderr: errb.bytes(), Truncated: out.cut, ExitCode: -1}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		res.ExitCode = -1
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

// limited keeps the first max bytes written and discards the rest, still accepting
// them so git never blocks on a full pipe.
type limited struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
	cut bool
}

func (l *limited) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	room := max(l.max-l.buf.Len(), 0)
	if n > room {
		l.cut = true
		p = p[:room]
	}
	l.buf.Write(p)
	return n, nil
}

func (l *limited) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

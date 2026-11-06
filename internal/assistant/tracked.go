package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The one repository read of init's ignore step (ADR-0124 Q4), moved here from the Git
// domain when that left (ADR-0137 §1). It keeps that package's hardening: the repository
// is untrusted (ADR-0015 §5), so every git started here is
//
//	git --no-optional-locks -c core.fsmonitor=false -c core.hooksPath=/dev/null \
//	    -c protocol.allow=never -c advice.graftFileDeprecated=false <driver overrides> <args>
//
// in the project root, with the caller's environment minus every GIT_* variable and plus
// GIT_OPTIONAL_LOCKS=0, GIT_NO_LAZY_FETCH=1, GIT_ALLOW_PROTOCOL=none,
// GIT_NO_REPLACE_OBJECTS=1, GIT_GRAFT_FILE=/dev/null, GIT_TERMINAL_PROMPT=0 and LC_ALL=C.
//
//   - core.fsmonitor=false: a configured fsmonitor would start a hook script or a daemon.
//   - hooksPath=/dev/null, protocol.allow=never and the GIT_ variables: no hook runs, no
//     lazy fetch of a partial clone runs the repository's core.sshCommand, no replace ref
//     or graft file changes what the repository shows.
//   - Every configured filter driver, in any scope, is overridden empty and not required,
//     because a repository can define a filter that is a command. The drivers are read
//     before every call; a list that passed the output cap, or a key that cannot be written
//     as an override, refuses the call (ErrUnsafeDriver), never skips it.
//
// A git started here is in a process group of its own (the starter) and the whole group is
// killed when the context ends or after the timeout.

const (
	gitRunTimeout  = 10 * time.Second
	gitStdoutLimit = 4 << 20
	gitStderrLimit = 64 << 10
	gitWaitDelay   = 250 * time.Millisecond
)

// Errors Tracked returns besides a failure to start.
var (
	// ErrGitNotFound: no git executable.
	ErrGitNotFound = errors.New("git is not installed")
	// ErrNoStarter: the reader was built without a starter.
	ErrNoStarter = errors.New("no process starter is configured")
	// ErrGitTimeout: git ran past its timeout and its group was killed.
	ErrGitTimeout = errors.New("git did not finish in time")
	// ErrUnsafeDriver: the configured filter drivers cannot be listed completely, or a
	// filter key cannot be written as a -c override, so a driver cannot be neutralised and
	// the call is refused.
	ErrUnsafeDriver = errors.New("a configured filter driver cannot be overridden")
	// ErrNotRepository: root is not inside a git work tree (a bare repository included).
	ErrNotRepository = errors.New("not a git repository")
)

// Starter starts cmd in a process group of its own, as the supervisor's spawner does.
type Starter func(cmd *exec.Cmd) error

// Repo is what `droidyard init --exclude/--gitignore` needs to know about the repository a
// project root lies in (ADR-0124 Q4).
type Repo struct {
	// Top is the work tree's top, a real path.
	Top string
	// CommonDir is the repository's common directory, absolute: the main `.git` for the
	// main tree and for every linked worktree, so `info/exclude` under it is read by all
	// of them.
	CommonDir string
	// Tracked holds, among the asked paths, those the index tracks, as slash-separated
	// paths relative to Top.
	Tracked map[string]bool
}

// Tracked reads, untrusted, where root's repository keeps its common directory and which of
// paths (slash-separated, relative to root, matched literally) it tracks. gitPath is the git
// executable ("" means none: ErrGitNotFound).
func Tracked(ctx context.Context, start Starter, gitPath string, environ []string, root string, paths []string) (Repo, error) {
	if !filepath.IsAbs(root) {
		return Repo{}, fmt.Errorf("root %q is not an absolute path", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Repo{}, err
	}
	r := newGitRunner(start, gitPath, environ)
	res, err := r.run(ctx, real, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	if res.ExitCode != 0 {
		return Repo{}, fmt.Errorf("%w: %s", ErrNotRepository, gitFirstLine(res.Stderr))
	}
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return Repo{}, fmt.Errorf("unexpected rev-parse output %q", res.Stdout)
	}
	repo := Repo{Top: lines[0], CommonDir: lines[1], Tracked: map[string]bool{}}
	if !filepath.IsAbs(repo.CommonDir) {
		repo.CommonDir = filepath.Join(real, repo.CommonDir) // relative to the cwd
	}
	if top, err := filepath.EvalSymlinks(repo.Top); err == nil {
		repo.Top = top
	}
	if len(paths) == 0 {
		return repo, nil
	}
	args := append([]string{"--literal-pathspecs", "ls-files", "-z", "--full-name", "--"}, paths...)
	res, err = r.run(ctx, real, args...)
	if err != nil {
		return Repo{}, err
	}
	if res.ExitCode != 0 || res.Truncated {
		return Repo{}, fmt.Errorf("listing the tracked files: git exited %d: %s", res.ExitCode, gitFirstLine(res.Stderr))
	}
	for _, p := range strings.Split(string(res.Stdout), "\x00") {
		if p != "" {
			repo.Tracked[p] = true
		}
	}
	return repo, nil
}

type gitResult struct {
	Stdout, Stderr []byte
	// Truncated: stdout passed the cap and the rest was dropped.
	Truncated bool
	// ExitCode is git's; -1 if it did not exit by itself (timeout, cancel).
	ExitCode int
}

type gitRunner struct {
	start   Starter
	git     string
	env     []string
	timeout time.Duration
}

func newGitRunner(start Starter, gitPath string, environ []string) *gitRunner {
	return &gitRunner{start: start, git: gitPath, env: scrubGitEnv(environ), timeout: gitRunTimeout}
}

// scrubGitEnv drops every GIT_* variable and LC_ALL from environ and appends the fixed ones.
func scrubGitEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+4)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=none", "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

// run runs git with args in root, every filter driver overridden, and waits for it. A
// non-zero exit is not an error: it is in the result.
func (r *gitRunner) run(ctx context.Context, root string, args ...string) (gitResult, error) {
	if r.git == "" {
		return gitResult{ExitCode: -1}, ErrGitNotFound
	}
	if r.start == nil {
		return gitResult{ExitCode: -1}, ErrNoStarter
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	drivers, err := r.drivers(ctx, root)
	if err != nil {
		return gitResult{ExitCode: -1}, gitEndOf(parent, ctx, err)
	}
	var overrides []string
	for _, d := range drivers {
		p := "filter." + d + "."
		overrides = append(overrides,
			"-c", p+"clean=", "-c", p+"smudge=", "-c", p+"process=", "-c", p+"required=false")
	}
	res, err := r.exec(ctx, root, overrides, args)
	return res, gitEndOf(parent, ctx, err)
}

// gitEndOf maps an error to ErrGitTimeout when only the runner's own deadline ended ctx.
func gitEndOf(parent, ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrGitTimeout
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return err
}

// drivers returns every filter driver name configured in any scope, sorted.
func (r *gitRunner) drivers(ctx context.Context, root string) ([]string, error) {
	res, err := r.exec(ctx, root, nil, []string{"config", "-z", "--name-only", "--get-regexp", `^filter\.`})
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 1: // no such key
		return nil, nil
	case 0:
	default:
		return nil, fmt.Errorf("reading the configured filters: git exited %d: %s", res.ExitCode, gitFirstLine(res.Stderr))
	}
	if res.Truncated {
		// A driver listed past the cap would run; the list is not trusted incomplete.
		return nil, fmt.Errorf("%w: the list of configured filters passed %d bytes", ErrUnsafeDriver, gitStdoutLimit)
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

func gitFirstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

// exec starts one git with the fixed prefix, overrides and args, and waits for it. ctx carries
// the timeout; when it ends the whole process group is killed.
func (r *gitRunner) exec(ctx context.Context, root string, overrides []string, args []string) (gitResult, error) {
	argv := make([]string, 0, 8+len(overrides)+len(args))
	argv = append(argv, "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "advice.graftFileDeprecated=false")
	argv = append(argv, overrides...)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, r.git, argv...)
	cmd.Dir = root
	cmd.Env = r.env
	// The group, not the pid: a filter git started is in git's group and holds git's pipes.
	// A starter that left no group of its own makes the group kill fail, and then only the
	// pid is killed.
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = gitWaitDelay
	out, errb := &gitLimited{max: gitStdoutLimit}, &gitLimited{max: gitStderrLimit}
	cmd.Stdout, cmd.Stderr = out, errb
	if err := r.start(cmd); err != nil {
		return gitResult{ExitCode: -1}, fmt.Errorf("starting git: %w", err)
	}
	err := cmd.Wait()
	res := gitResult{Stdout: out.bytes(), Stderr: errb.bytes(), Truncated: out.truncated(), ExitCode: -1}
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

// gitLimited keeps the first max bytes written and discards the rest, still accepting them
// so git never blocks on a full pipe.
type gitLimited struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
	cut bool
}

func (l *gitLimited) Write(p []byte) (int, error) {
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

func (l *gitLimited) truncated() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cut
}

func (l *gitLimited) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

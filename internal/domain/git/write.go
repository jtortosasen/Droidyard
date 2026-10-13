package git

// The Git writes' commands (plan m2n part 2, steps P1 and P2; ADR-0071 §3, §5): the
// builders, the runner that starts a write git (its process group, its signals, its
// timeouts) and the parts every write capability shares (its descriptor, the repository's
// lock key). What each flag below is for, and what git does with each, is recorded fact by
// fact in write_spike_test.go.
//
// A write is never run on an untrusted root (ADR-0071 §2), so none of the read
// invocation's neutralising of the repository's configuration is repeated here beyond
// the fixed prefix: the repository's transport, credential, update and filter
// configuration is the user's own git. What stays off on every git the daemon starts:
// hooks (core.hooksPath), fsmonitor, a prompt, and, since a write leaves objects
// behind, a detached gc or maintenance that would outlive the call.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// writeConfig is what every write is given before its subcommand, in the order of git's
// own global options: the read builders' two global switches (hist), then
//
//   - gc.auto=0 and maintenance.auto=false: a fetch and a checkout end with
//     `maintenance run --auto`, which can detach a `gc` that outlives the call and the
//     process group the daemon kills. Which of the two stops it, in which git, is
//     recorded in the spike.
var writeConfig = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// writeCmd is the command line of a write: the global switches, the write config, then
// the subcommand and its arguments. A name never comes before --end-of-options and a path
// never before `--`, with the global --literal-pathspecs so a gitlink path holding `*`
// or `:(` is a path and not a pattern.
func writeCmd(args ...string) []string {
	return hist(slices.Concat(writeConfig, args)...)
}

// writeFixedPrefix is the fixed prefix run.go gives every git, except that a network
// command (fetch, a submodule update that clones) leaves out protocol.allow=never: git's
// own protocol policy then applies (ADR-0071 §3); and it ends with -c core.askPass=
// (empty), which git-lfs reads itself (it does not look at GIT_ASKPASS) and which, being
// set, also hides SSH_ASKPASS from git's own prompt (ADR-0073 §Consequences). The write
// runner takes this in place of the literal list in run.go's exec; a test
// (TestWritePrefixIsTheReadPrefix) holds the two equal, apart from that last pair, for a
// command that is not a network one.
func writeFixedPrefix(network bool) []string {
	p := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}
	if !network {
		p = append(p, "-c", "protocol.allow=never")
	}
	return append(p, "-c", "advice.graftFileDeprecated=false", "-c", "core.askPass=")
}

// writeEnv is the environment a write git runs in: scrubEnv's, and for a network command
// without GIT_ALLOW_PROTOCOL=none; never a prompt, whoever would ask (ADR-0071 §3).
//
//   - GIT_TERMINAL_PROMPT=0 stops git's own terminal prompt, but git asks an askpass
//     program first and waits for it: GIT_ASKPASS, then core.askPass, then SSH_ASKPASS.
//     So SSH_ASKPASS is dropped from the inherited environment and GIT_ASKPASS is set
//     empty, which an empty value makes git skip every askpass, core.askPass included
//     (TestWriteSpike5AskpassNeverRuns).
//   - ssh's own prompt (a passphrase, a host key) goes to SSH_ASKPASS when ssh has no
//     terminal; SSH_ASKPASS_REQUIRE=never makes it fail instead. GIT_SSH_COMMAND and
//     BatchMode are not set: they would override the user's core.sshCommand.
func writeEnv(environ []string, network bool) []string {
	environ = slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		return strings.HasPrefix(kv, "SSH_ASKPASS=") || strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=")
	})
	env := append(scrubEnv(environ), "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never")
	if !network {
		return env
	}
	return slices.DeleteFunc(env, func(kv string) bool { return kv == "GIT_ALLOW_PROTOCOL=none" })
}

// fetchArgs fetches one remote, or every remote when remote is empty. --quiet drops the
// per-ref progress lines (" * [new tag]"), which without a terminal fill stderr with one
// line per ref and would push git's own error out of what is kept; `error:` and `fatal:`
// lines stay. No prune of
// remote-tracking refs or tags, no recursion into submodules (their own fetch is
// updateArgs's business). The remote is a configured remote's name (validName).
func fetchArgs(remote string) []string {
	args := writeCmd("fetch", "--quiet", "--no-recurse-submodules", "--no-prune", "--no-prune-tags")
	if remote == "" {
		return append(args, "--all")
	}
	return append(args, "--end-of-options", remote)
}

// switchArgs switches to an existing local branch. --no-guess: a name that is only a
// remote-tracking branch's is not made into a local branch by git's own guess; that is
// trackArgs, asked for explicitly. --no-recurse-submodules: a submodule is a root of its
// own (ADR-0071 §6).
func switchArgs(branch string) []string {
	return writeCmd("switch", "--no-recurse-submodules", "--no-guess", "--end-of-options", branch)
}

// trackArgs creates the local branch name at the remote-tracking branch start (for
// example origin/feature/x) with start as its upstream, and switches to it.
func trackArgs(name, start string) []string {
	return writeCmd("switch", "--no-recurse-submodules", "--track", "--create", name, "--end-of-options", start)
}

// branchArgs creates the branch name at the commit from (a full object id the caller
// validated), with no upstream, and switches to it when checkout is set.
func branchArgs(name, from string, checkout bool) []string {
	if checkout {
		return writeCmd("switch", "--no-recurse-submodules", "--no-track", "--create", name, "--end-of-options", from)
	}
	return writeCmd("branch", "--no-track", "--end-of-options", name, from)
}

// updateArgs brings the submodules to the commits the superproject records, initialising
// them first: all of them, or the given paths (after `--`, literal).
func updateArgs(paths []string) []string {
	args := writeCmd("submodule", "update", "--init", "--recursive")
	if len(paths) == 0 {
		return args
	}
	return append(append(args, "--"), paths...)
}

// refFormatArgs asks git whether name is a branch name it would accept as it is.
func refFormatArgs(name string) []string {
	return hist("check-ref-format", "--branch", name)
}

// maxNameBytes is the longest name validName allows.
const maxNameBytes = 255

// validName is the check in Go that comes first (ADR-0071 §5): a name that no later step
// has to defend against. No empty name, no leading '-' (it would be read as an option
// wherever it lands), no control character, no "@{" (git expands "@{-1}" to the previous
// branch's name), not HEAD or "@" (its synonym), no prefix refs/ heads/ remotes/ tags/ and
// no pseudo-ref's name (only capitals and '_', ending in HEAD: FETCH_HEAD, ORIG_HEAD,
// MERGE_HEAD...): such a name is ambiguous with another ref, and git resolves it to that
// ref or warns (TestWriteSpike9EndOfOptionsAndNames records what each builder does), at
// most 255 bytes. It does not say that git accepts the name; refFormatArgs asks that. A
// name that starts with an existing remote's name and a '/' is ambiguous with that
// remote's branches; that needs the repository's remotes and is the caller's check.
func validName(name string) bool {
	if name == "" || name == "HEAD" || name == "@" || len(name) > maxNameBytes || name[0] == '-' || strings.Contains(name, "@{") {
		return false
	}
	for _, p := range []string{"refs/", "heads/", "remotes/", "tags/"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	if strings.HasSuffix(name, "HEAD") && !strings.ContainsFunc(name, func(c rune) bool { return c != '_' && (c < 'A' || c > 'Z') }) {
		return false
	}
	return !strings.ContainsFunc(name, func(c rune) bool { return c < ' ' || c == 0x7f })
}

// --- the write runner (plan m2n P2) ---------------------------------------------------

const (
	// networkWriteTimeout bounds a write that talks to a remote: a fetch, and a submodule
	// update that may clone (ADR-0071 §3).
	networkWriteTimeout = 5 * time.Minute
	// otherWriteTimeout bounds every other write.
	otherWriteTimeout = 60 * time.Second
	// termGrace is how long a write's group has to go after SIGTERM before it gets
	// SIGKILL: git removes its lock files only from a signal it can catch, so SIGKILL
	// alone would leave them (TestWriteCancelLeavesNoLock; write_spike_locks_test.go).
	termGrace = 2 * time.Second
	// killGrace is how long the group is polled after SIGKILL.
	killGrace = time.Second
	// groupPoll is the interval the group is polled at.
	groupPoll = 20 * time.Millisecond
	// writeKind is the pid record kind a write git gets (process.GitWrite's value). It is
	// spelled here so this package imports no process; a test asserts the two agree.
	writeKind = "git-write"
)

// Recorded is a process the recorded starter returned: its pid (also its group's and its
// session's), its end (which removes the pid record), and a way to signal its whole group.
// *process.Recorded implements it.
type Recorded interface {
	Pid() int
	Wait() error
	SignalGroup(sig syscall.Signal) error
}

// RecordedStarter starts a write git as its own session and group leader, records its pid
// for its life and returns once it runs; the runner reaps it with Recorded.Wait. The
// daemon passes the supervisor's SpawnRecorded through a thin adapter. When the record
// cannot be written the starter kills the git's whole group and returns an error.
type RecordedStarter func(cmd *exec.Cmd, kind string) (Recorded, error)

// writeRunner starts a write git. Unlike the reads' runner it is never given the
// repository's drivers to neutralise: a write runs only on a trusted root (ADR-0071 §2).
type writeRunner struct {
	start   RecordedStarter
	git     string
	environ []string
}

// writeRunner builds the runner for one call; git is looked for afresh, as runner does.
func (o Options) writeRunner() (*writeRunner, *capability.Error) {
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look("git")
	if err != nil || path == "" {
		return nil, notFound(err)
	}
	environ := os.Environ
	if o.Environ != nil {
		environ = o.Environ
	}
	return &writeRunner{start: o.Recorded, git: path, environ: environ()}, nil
}

// timeoutFor is the limit of a write: the test's override, else ADR-0071 §3's.
func (o Options) timeoutFor(network bool) time.Duration {
	switch {
	case o.writeTimeout > 0:
		return o.writeTimeout
	case network:
		return networkWriteTimeout
	}
	return otherWriteTimeout
}

// run runs a write git with args (a builder's command line) in root and waits for it.
// network says whether the command talks to a remote (writeFixedPrefix, writeEnv).
//
// The git is its own session and group leader, recorded for its life, and its whole
// group is ended at every end of the call: when ctx ends or the timeout passes, SIGTERM
// to the group and, if anything is left after termGrace, SIGKILL (git removes its lock
// files from its signal handlers, which SIGKILL skips; Go's WaitDelay would kill the pid
// alone); and after git has exited by itself, whatever it left in the group (an ssh, a
// transport), because the record goes with git's Wait and a stray would run unrecorded. A
// non-zero exit is not an error: it is in result.ExitCode.
func (w *writeRunner) run(ctx context.Context, root string, network bool, timeout time.Duration, args []string) (result, error) {
	if w.git == "" {
		return result{ExitCode: -1}, ErrNotFound
	}
	if w.start == nil {
		return result{ExitCode: -1}, ErrNoStarter
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A cancel that landed before anything started ends the call here.
	if err := ctx.Err(); err != nil {
		return result{ExitCode: -1}, endOf(parent, ctx, err)
	}
	cmd := exec.Command(w.git, append(writeFixedPrefix(network), args...)...)
	cmd.Dir = root
	cmd.Env = writeEnv(w.environ, network)
	out, errb := &limited{max: stdoutLimit}, &headTail{head: stderrHead, tail: stderrTail}
	cmd.Stdout, cmd.Stderr = out, errb
	// Bounds how long Wait lingers for the pipes after git exited or was killed, should a
	// stray in the group hold them.
	cmd.WaitDelay = waitDelay
	rec, err := w.start(cmd, writeKind)
	if err != nil {
		return result{ExitCode: -1}, fmt.Errorf("starting git: %w", err)
	}
	// A goroutine reaps git the moment it exits; Recorded.Wait removes the pid record, so
	// a record never outlives git.
	exited := make(chan error, 1)
	go func() { exited <- rec.Wait() }()

	var waitErr error
	reaped, ended := false, false
	select {
	case waitErr = <-exited:
		reaped = true
	case <-ctx.Done():
		ended = true
	}
	// At every end the group is ended: git's strays after it exited by itself, git and
	// its transport when the call is cancelled or timed out.
	stopGroup(rec)
	if ended {
		// The reap is in flight; bound the wait so a stuck Wait cannot hang the call.
		select {
		case waitErr = <-exited:
			reaped = true
		case <-time.After(waitDelay + killGrace):
		}
	}

	res := result{Stdout: out.bytes(), Stderr: errb.bytes(), Truncated: out.truncated(), ExitCode: -1}
	// ProcessState is read only once Wait has returned: before that the reaping goroutine
	// may still be writing it.
	if reaped && cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ended {
		res.ExitCode = -1
		return res, endOf(parent, ctx, ctx.Err())
	}
	var ee *exec.ExitError
	switch {
	case waitErr == nil, errors.Is(waitErr, exec.ErrWaitDelay) && res.ExitCode == 0, errors.As(waitErr, &ee):
		return res, nil
	}
	return res, waitErr
}

// headTail keeps the first head and the last tail bytes written and, between them when the
// output was longer, a marker saying so, still accepting everything so git never blocks on a
// full pipe. A write's stderr is kept this way: git's error comes last, after any progress
// lines (limited keeps only the first bytes, which suits the reads).
type headTail struct {
	mu         sync.Mutex
	head, tail int
	first      []byte
	last       []byte
	total      int
}

// stderrHead and stderrTail are the bytes of a write's stderr kept from its start and end;
// stderrCut stands between the two when bytes were dropped.
const (
	stderrHead = 32 << 10
	stderrTail = 32 << 10
	stderrCut  = "\n[... stderr cut ...]\n"
)

func (h *headTail) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(p)
	h.total += n
	if room := h.head - len(h.first); room > 0 {
		k := min(room, len(p))
		h.first = append(h.first, p[:k]...)
		p = p[k:]
	}
	h.last = append(h.last, p...)
	if len(h.last) > 2*h.tail {
		h.last = append([]byte(nil), h.last[len(h.last)-h.tail:]...)
	}
	return n, nil
}

func (h *headTail) bytes() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	last := h.last
	if len(last) > h.tail {
		last = last[len(last)-h.tail:]
	}
	out := append([]byte(nil), h.first...)
	if h.total > len(h.first)+len(last) {
		out = append(out, stderrCut...)
	}
	return append(out, last...)
}

// stopGroup ends rec's whole process group: SIGTERM when it still has a member, SIGKILL
// after termGrace if it outlived it, then a bounded poll for it to vanish. A group with
// no member left is left alone.
func stopGroup(rec Recorded) {
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
		time.Sleep(groupPoll)
	}
}

// --- what a failed write says (ADR-0071 §5, plan m2n Q6 (2)) ---------------------------

const (
	// maxCauseLines and maxCauseBytes bound a write's GIT_FAILED cause.
	maxCauseLines = 20
	maxCauseBytes = 4 << 10
)

// writeCause is a write's GIT_FAILED cause: git's stderr from its first `fatal:` or
// `error:` line, at most maxCauseLines lines and maxCauseBytes bytes, so a refused
// checkout names the files in the way (the reads keep the first line only, ADR-0066 §4).
// Stderr with neither line gives its first line. Text is made valid UTF-8.
func writeCause(stderr []byte) string {
	lines := strings.Split(strings.TrimSpace(strings.ToValidUTF8(string(stderr), "�")), "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "fatal:") || strings.HasPrefix(l, "error:") })
	if start < 0 {
		lines = lines[:1]
	} else {
		lines = lines[start:]
	}
	if len(lines) > maxCauseLines {
		lines = lines[:maxCauseLines]
	}
	return truncate(strings.TrimSpace(strings.Join(lines, "\n")), maxCauseBytes)
}

// writeFailure turns a write git's non-zero exit into the contract's error: a root that is
// no repository, or GIT_FAILED with writeCause. A line that says git needed a credential it
// could not ask for gets the nextStep of ADR-0071 §3.
func writeFailure(root string, res result) error {
	if strings.Contains(firstLine(res.Stderr), "not a git repository") {
		return mapError(root, ErrNotARepository)
	}
	cause := writeCause(res.Stderr)
	e := capability.NewError(CodeFailed, "git failed").WithCause(cause)
	if needsCredential(cause) {
		e.WithNextStep("git needs a credential it cannot ask for here: run `git fetch` once in a terminal in " + root + ", then retry")
	}
	return e
}

// needsCredential says whether git's error is the credential prompt it was forbidden:
// no terminal, so no username, no password.
func needsCredential(cause string) bool {
	for _, s := range []string{"terminal prompts disabled", "could not read Username", "could not read Password", "Authentication failed"} {
		if strings.Contains(cause, s) {
			return true
		}
	}
	return false
}

// mapWriteError maps what the write runner returns to the contract's codes; a timeout
// says how long it ran and what to do, and the rest is mapError's.
func mapWriteError(root string, err error, timeout time.Duration, command string) error {
	if errors.Is(err, ErrTimeout) {
		return capability.Errorf(CodeFailed, "git did not finish in %s and was stopped", timeout).
			WithCause(ErrTimeout.Error()).
			WithNextStep("run `git " + command + "` in a terminal in " + root + " to see why it is slow, then retry")
	}
	return mapError(root, err)
}

// --- what every write capability shares -------------------------------------------------

// write completes the descriptor of a write capability (ADR-0071 §1–§4): a command for
// the human UI, long-running because it can queue behind a five-minute fetch on its
// repository's key (past ADR-0020's queue bound for a call not so flagged; the registry
// enforces the pairing), needing git, gated on the root's trust before the handler runs
// and serialised per repository.
func (o Options) write(d capability.Descriptor) capability.Descriptor {
	d.Kind = capability.Command
	d.Scope = capability.Workspace
	d.Audience = capability.AudienceHuman
	d.LongRunning = true
	d.Availability = []capability.Requirement{capability.RequiresGit}
	d.Authorize = o.gate
	d.Resource = o.resource
	return d
}

// gate is the authorization re-check, run immediately before a write's handler: an
// untrusted root is WORKSPACE_UNTRUSTED with the trust nextStep and no git starts
// (ADR-0071 §2). Without the hook (the golden registry) every root is untrusted.
func (o Options) gate(ctx context.Context, call *capability.Call) (any, error) {
	if o.Authorize != nil {
		return o.Authorize(ctx, call)
	}
	root, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	return nil, policy.UntrustedError(root)
}

// resource is the lock key of a write: "git.write:" and the repository's common
// directory, so every work tree of one repository shares it and two repositories do not
// (ADR-0071 §4). A reader takes no key.
func (o Options) resource(call *capability.Call) string {
	root, ok := "", false
	if o.Root != nil {
		root, ok = o.Root(call.Workspace)
	}
	if !ok || root == "" {
		return "git.write:" + call.Workspace
	}
	return "git.write:" + commonDir(root)
}

// commonDir finds the common git directory of the repository that holds root, reading
// files and never running git: from root up to the first `.git` entry; a directory is the
// answer; a file holds `gitdir: <path>` (a linked work tree's or a submodule's), and that
// directory's `commondir` file, when it has one, names the shared directory. Symlinks of
// the result are resolved, so "/var/..." and "/private/var/..." agree. With no `.git`
// above root, root itself is the key (the call will answer GIT_NOT_A_REPOSITORY).
func commonDir(root string) string {
	for dir := filepath.Clean(root); ; {
		dotgit := filepath.Join(dir, ".git")
		if fi, err := os.Stat(dotgit); err == nil {
			if fi.IsDir() {
				return resolved(dotgit)
			}
			if gitdir, ok := gitdirOf(dotgit, dir); ok {
				return resolved(commonOf(gitdir))
			}
			return resolved(dotgit) // a .git file git could not follow: its own key
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return resolved(filepath.Clean(root))
		}
		dir = parent
	}
}

// gitdirOf reads the `gitdir: <path>` line of a `.git` file in dir; a relative path is
// relative to dir.
func gitdirOf(file, dir string) (string, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	p, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if p = strings.TrimSpace(p); !ok || p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p), true
}

// commonOf is the common directory of the git directory gitdir: the one its `commondir`
// file names (relative to gitdir), else gitdir itself.
func commonOf(gitdir string) string {
	b, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return gitdir
	}
	p := strings.TrimSpace(string(b))
	if p == "" {
		return gitdir
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(gitdir, p)
	}
	return filepath.Clean(p)
}

// resolved is p with its symlinks resolved, or p itself when that is not possible.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// The one place `avdmanager` starts (plan m3f step 6; ADR-0130 §5). Droidyard installs and
// removes SDK packages itself and spawns no tool for it; avdmanager (classic, from
// cmdline-tools) stays the writer of new AVDs and the remover of deleted ones, run under
// ADR-0129 §6's process rule: recorded as kind sdk-tool, its own session and group, the
// group ended at every end of the call, no death signal, a survivor's group ended by the
// next daemon's Reconcile.

// The codes of an avdmanager run.
const (
	// ErrToolsNotFound: there is no host SDK, or none of its cmdline-tools directories
	// holds bin/avdmanager. The next step installs cmdline-tools;latest.
	ErrToolsNotFound capability.ErrorCode = "SDK_TOOLS_NOT_FOUND"
	// ErrJavaNotFound: avdmanager is a Java tool and no JVM 17 or newer was found.
	ErrJavaNotFound capability.ErrorCode = "SDK_JAVA_NOT_FOUND"
	// ErrToolFailed: avdmanager ran and failed (a status other than 0, its bound, or an
	// outcome it reported as success and did not produce); the cause names the decisive
	// line of its output. A download, checksum or unpack failure is SDK_DOWNLOAD_FAILED.
	ErrToolFailed capability.ErrorCode = "SDK_TOOL_FAILED"
)

const (
	// ToolKind is the pid record kind of an avdmanager run (process.SDKTool's value). It
	// is spelled here so this package imports no process; a test asserts the two agree.
	ToolKind = "sdk-tool"
	// toolTimeout is ADR-0129 §6's bound for anything but an install (a working default).
	toolTimeout = 5 * time.Minute
	// toolOutputTail is how much of a run's output is kept: the last MiB.
	toolOutputTail = 1 << 20
	// toolTermGrace is how long a group has to go after SIGTERM before it gets SIGKILL;
	// toolKillGrace bounds the wait after SIGKILL.
	toolTermGrace = 5 * time.Second
	toolKillGrace = time.Second
	toolPoll      = 20 * time.Millisecond
	// toolWaitDelay bounds how long Wait lingers for the pipes after the tool exited or
	// was killed, should a stray in its group hold them.
	toolWaitDelay = 250 * time.Millisecond
)

// Recorded is a process the recorded starter returned: its pid (also its group's and its
// session's), its end (which removes the pid record) and a way to signal its whole group.
// *process.Recorded implements it.
type Recorded interface {
	Pid() int
	Wait() error
	SignalGroup(sig syscall.Signal) error
}

// RecordedStarter starts cmd as its own session and group leader, records its pid for its
// life and returns once it runs; the runner reaps it with Recorded.Wait. The daemon passes
// the supervisor's SpawnRecorded through a thin adapter. When the record cannot be written
// the starter kills the whole group and returns an error.
type RecordedStarter func(cmd *exec.Cmd, kind string) (Recorded, error)

// ToolOptions configure a Tool. Env is the daemon's environment; JVM answers where the
// JVM is (the sidecar's discovery, so the two cannot disagree); Start is the recorded
// spawner, without which no run starts.
type ToolOptions struct {
	Env   []string
	JVM   func() gradle.JVM
	Start RecordedStarter

	// For tests only: the bounds.
	Timeout, TermGrace time.Duration
}

// Tool runs avdmanager. It is safe for concurrent use.
type Tool struct {
	env       []string
	jvm       func() gradle.JVM
	start     RecordedStarter
	timeout   time.Duration
	termGrace time.Duration

	// The device profiles, kept for the avdmanager they were read from (profiles.go).
	profMu    sync.Mutex
	profiles  []Profile
	profStamp toolStamp
}

// NewTool returns the runner over o.
func NewTool(o ToolOptions) *Tool {
	t := &Tool{env: append([]string(nil), o.Env...), jvm: o.JVM, start: o.Start, timeout: toolTimeout, termGrace: toolTermGrace}
	if o.Timeout > 0 {
		t.timeout = o.Timeout
	}
	if o.TermGrace > 0 {
		t.termGrace = o.TermGrace
	}
	return t
}

// ToolPaths is where a run would start: the host SDK, the avdmanager under it and the JVM.
type ToolPaths struct {
	Root     string
	Path     string
	JavaHome string
}

// Resolve finds avdmanager and the JVM without starting anything: SDK_TOOLS_NOT_FOUND when
// the host has no SDK or none of its cmdline-tools holds bin/avdmanager, then
// SDK_JAVA_NOT_FOUND when no JVM 17 or newer is found.
func (t *Tool) Resolve() (ToolPaths, error) {
	h := Host(t.env)
	if !h.Found {
		return ToolPaths{}, toolsNotFound(h.NotFound())
	}
	path, ok := avdmanagerIn(h.Root)
	if !ok {
		return ToolPaths{}, toolsNotFound("the SDK has no cmdline-tools directory with bin/avdmanager")
	}
	if t.jvm == nil {
		return ToolPaths{}, javaNotFound("no JVM discovery is configured")
	}
	jvm := t.jvm()
	if !jvm.Found {
		return ToolPaths{}, javaNotFound(jvm.NotFound())
	}
	return ToolPaths{Root: h.Root, Path: path, JavaHome: jvm.Home}, nil
}

func toolsNotFound(cause string) *capability.Error {
	return capability.NewError(ErrToolsNotFound, "the Android command-line tools are not installed").
		WithCause(cause).
		WithNextStep(`call sdk.install {"packages": ["cmdline-tools;latest"]}; if it reports a licence not accepted, the user accepts it first`)
}

func javaNotFound(cause string) *capability.Error {
	return capability.NewError(ErrJavaNotFound, "avdmanager needs a JVM 17 or newer and none was found").
		WithCause(cause).
		WithNextStep("install a JDK 17 or newer, or set JAVA_HOME (or `gradle.sidecarJavaHome` in the droidyard configuration) to one")
}

// avdmanagerIn is bin/avdmanager of the cmdline-tools directory with the highest revision
// that has one (its package.xml, else source.properties; a tie goes to `latest`, then to
// the name, so the choice is stable). A directory whose revision cannot be read ranks
// below every one that can.
func avdmanagerIn(root string) (string, bool) {
	dirs, err := filepath.Glob(filepath.Join(root, "cmdline-tools", "*"))
	if err != nil {
		return "", false
	}
	type cand struct {
		path string
		rev  Revision
		ok   bool
		name string
	}
	var cands []cand
	for _, d := range dirs {
		bin := filepath.Join(d, "bin", "avdmanager")
		if fi, err := os.Stat(bin); err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		c := cand{path: bin, name: filepath.Base(d)}
		if p, found, _ := readPackage(d, ""); found {
			c.rev, c.ok = ParseRevision(p.Version)
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return "", false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.ok != b.ok {
			return a.ok
		}
		if a.ok {
			if c := a.rev.Compare(b.rev); c != 0 {
				return c > 0
			}
		}
		if (a.name == "latest") != (b.name == "latest") {
			return a.name == "latest"
		}
		return a.name > b.name
	})
	return cands[0].path, true
}

// ToolRun is one avdmanager invocation.
type ToolRun struct {
	// Args follow the program name: "create", "avd", "--name", ...
	Args []string
	// Stdin is what the tool reads; "" is /dev/null (the tool never gets consent, ADR-0129
	// §2), and `create avd` gets a fixed "no\n" for its hardware-profile prompt.
	Stdin string
	// Env are "KEY=value" entries set over the daemon's environment: ANDROID_AVD_HOME.
	Env []string
}

// ToolOutput is what a finished run printed: the last MiB of stdout and stderr together.
type ToolOutput struct {
	Output []byte
}

// Run runs avdmanager and waits for it. A status other than 0 is SDK_TOOL_FAILED with the
// decisive line; the bound passing is SDK_TOOL_FAILED too; the call's own cancel is the
// context's error. At every end of the call the tool's whole group is ended: SIGTERM, then
// SIGKILL once the grace has passed, and after the tool exited by itself whatever it left.
func (t *Tool) Run(ctx context.Context, r ToolRun) (ToolOutput, error) {
	paths, err := t.Resolve()
	if err != nil {
		return ToolOutput{}, err
	}
	if t.start == nil {
		return ToolOutput{}, toolFailed(r.Args, "was not started: the daemon has no process starter")
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	// A cancel that landed before anything started ends the call here.
	if ctx.Err() != nil {
		return ToolOutput{}, endOfRun(parent, r.Args, t.timeout)
	}
	cmd := exec.Command(paths.Path, r.Args...)
	cmd.Env = toolEnv(t.env, paths, r.Env)
	if r.Stdin != "" {
		cmd.Stdin = strings.NewReader(r.Stdin)
	} // nil is /dev/null: never the daemon's own stdin
	out := &tail{max: toolOutputTail}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = toolWaitDelay
	rec, err := t.start(cmd, ToolKind)
	if err != nil {
		return ToolOutput{}, toolFailed(r.Args, "could not be started: "+err.Error())
	}
	// A goroutine reaps the tool the moment it exits; Recorded.Wait removes the pid record,
	// so a record never outlives the tool.
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
	// At every end the group is ended: the tool's strays after it exited by itself, the
	// tool and its JVM when the call is cancelled or timed out.
	stopGroup(rec, t.termGrace)
	if ended {
		// The reap is in flight; bound the wait so a stuck Wait cannot hang the call.
		select {
		case waitErr = <-exited:
			reaped = true
		case <-time.After(toolWaitDelay + toolKillGrace):
		}
	}
	res := ToolOutput{Output: out.bytes()}
	if ended {
		return res, endOfRun(parent, r.Args, t.timeout)
	}
	// ProcessState is read only once Wait has returned: before that the reaping goroutine
	// may still be writing it.
	status := -1
	if reaped && cmd.ProcessState != nil {
		status = cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	switch {
	case waitErr == nil, errors.Is(waitErr, exec.ErrWaitDelay) && status == 0:
		return res, nil
	case errors.As(waitErr, &ee):
		return res, toolFailed(r.Args, fmt.Sprintf("exited with status %d", ee.ExitCode()), res.Output)
	}
	return res, toolFailed(r.Args, waitErr.Error(), res.Output)
}

// endOfRun is the error of a run that ended with its context: the caller's own cancel is
// its error; the tool's bound passing is SDK_TOOL_FAILED.
func endOfRun(parent context.Context, args []string, bound time.Duration) error {
	if perr := parent.Err(); perr != nil {
		return perr
	}
	return toolFailed(args, "did not finish within "+bound.String())
}

// toolFailed is SDK_TOOL_FAILED for the run of args: what happened and, when the tool
// printed anything, its decisive line.
func toolFailed(args []string, what string, output ...[]byte) *capability.Error {
	cause := "avdmanager " + subcommand(args) + " " + what
	if len(output) > 0 {
		if line := decisiveLine(output[0]); line != "" {
			cause += ": " + line
		}
	}
	return capability.NewError(ErrToolFailed, "avdmanager failed").
		WithCause(cause).
		WithNextStep("read the cause; the AVDs and the SDK were left as the tool left them")
}

// ToolFailed is SDK_TOOL_FAILED for an avdmanager run that exited 0 and did not produce
// what it reported (an AVD's .ini that is not there, or one that is still there).
func ToolFailed(args []string, what string) *capability.Error { return toolFailed(args, what) }

// subcommand is the words of args before its first flag: "create avd", "list device".
func subcommand(args []string) string {
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		words = append(words, a)
	}
	return strings.Join(words, " ")
}

// decisiveLine is the line of a failed run that says why: the first that begins "Error"
// (avdmanager's own), else the first Java exception line, else the last non-empty line.
func decisiveLine(out []byte) string {
	var first, exception, last string
	for _, l := range strings.Split(strings.ToValidUTF8(string(out), "�"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		last = l
		if first == "" && (strings.HasPrefix(l, "Error") || strings.HasPrefix(l, "error")) {
			first = l
		}
		if exception == "" && (strings.HasPrefix(l, "Exception in thread") || strings.Contains(l, "Exception:")) {
			exception = l
		}
	}
	for _, l := range []string{first, exception, last} {
		if l != "" {
			return cleanText(l, 200)
		}
	}
	return ""
}

// toolEnv is the daemon's environment without ANDROID_SDK_ROOT (deprecated, and a second
// answer to where the SDK is), with ANDROID_HOME the SDK root, JAVA_HOME the JVM and the
// call's own entries over the rest.
func toolEnv(base []string, p ToolPaths, extra []string) []string {
	drop := map[string]bool{"ANDROID_SDK_ROOT": true, "ANDROID_HOME": true, "JAVA_HOME": true}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	env := make([]string, 0, len(base)+len(extra)+2)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			env = append(env, kv)
		}
	}
	env = append(env, "ANDROID_HOME="+p.Root, "JAVA_HOME="+p.JavaHome)
	return append(env, extra...)
}

// tail keeps the last max bytes written, still accepting everything so the tool never
// blocks on a full pipe.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

var _ io.Writer = (*tail)(nil)

func (w *tail) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *tail) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buf
	if len(b) > w.max {
		b = b[len(b)-w.max:]
	}
	return bytes.Clone(b)
}

// stopGroup ends rec's whole process group: SIGTERM when it still has a member, SIGKILL
// after termGrace if it outlived it, then a bounded poll for it to vanish. A group with no
// member left is left alone.
func stopGroup(rec Recorded, termGrace time.Duration) {
	pgid := rec.Pid()
	if !groupPresent(pgid) {
		return
	}
	rec.SignalGroup(syscall.SIGTERM)
	if !waitGroupGone(pgid, termGrace) {
		rec.SignalGroup(syscall.SIGKILL)
		waitGroupGone(pgid, toolKillGrace)
	}
}

// groupPresent: kill(-pgid, 0) fails with ESRCH once no process is left in the group.
func groupPresent(pgid int) bool {
	return !errors.Is(unix.Kill(-pgid, 0), unix.ESRCH)
}

func waitGroupGone(pgid int, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for {
		if !groupPresent(pgid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(toolPoll)
	}
}

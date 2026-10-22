package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
	"github.com/jtortosasen/droidyard/internal/platform"
)

const hookUsage = "usage: droidyard hook --client claude|cursor [--root <absolute project root>]"

// hookDeadline is how long the hook may take to answer, from its start: a client waits
// on it before running the command, so past this it lets the command through
// (ADR-0061 §1).
const hookDeadline = 2 * time.Second

// hookMaxStdin bounds the event the hook reads; a larger one is not a client's event.
const hookMaxStdin = 1 << 20

// hookSeam is for this package's tests only: called first in the decision, so a test
// can force a panic there. The shipped binary never sets it.
var hookSeam func()

// hook runs `droidyard hook` (ADR-0061): it reads one assistant hook event on stdin and
// writes one answer on stdout: before a shell command, the client's pass-through or a
// deny that hands the agent `droidyard trash`, which moves the files to the trash from
// the agent's own shell (ADR-0062); after one, a hint naming the droidyard tool that
// does the same and a record of the Android command in Activity, or the pass-through
// (ADR-0063). It never starts a daemon, deletes nothing, denies nothing after a
// command, and never exits 2, which Claude Code reads as a deny: it exits 0 with an
// answer, or 1 with a note on stderr and nothing on stdout when it is misused. Anything
// that goes wrong after the arguments are read (no daemon, an error, the deadline,
// malformed input, a panic) is the client's pass-through, or after a command the hint
// once it is computed (fail open). Without --root (a user-level entry, ADR-0124 §Decision
// 3) the root is the project of the directory the client names as its own, never the
// event's cwd alone; with no project, or where that directory holds init's own hook for
// the client, every event passes with nothing dialled.
func hook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, root, err := parseHookArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "droidyard hook: %v\n%s\n", err, hookUsage)
		return 1
	}
	// One write, after the decision: stdout carries the answer and nothing else.
	stdout.Write(hookAnswer(client, root, stdin, stderr, time.Now().Add(hookDeadline)))
	return 0
}

// parseHookArgs reads the arguments; root is "" when --root is not given, which is the
// rootless hook of a user-level entry (ADR-0124).
func parseHookArgs(args []string) (client assistant.Client, root string, err error) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := fs.String("client", "", "")
	r := fs.String("root", "", "")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch client = assistant.Client(*c); client {
	case assistant.Claude, assistant.Cursor:
	default:
		return "", "", fmt.Errorf("--client needs claude or cursor, not %q", *c)
	}
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "root" })
	if !given {
		return client, "", nil
	}
	if !filepath.IsAbs(*r) {
		return "", "", fmt.Errorf("--root needs an absolute project root, not %q", *r)
	}
	return client, filepath.Clean(*r), nil
}

// hookAnswer decides in a goroutine of its own, so a stdin that never ends, a daemon
// that never answers or a panic cannot keep the answer past deadline. Past it, or after
// a panic, the answer is the fallback the decision had reached by then: after a shell
// command, the hint, which does not wait on the daemon (ADR-0063 §2, §3); else the
// client's pass-through.
func hookAnswer(client assistant.Client, root string, stdin io.Reader, stderr io.Writer, deadline time.Time) []byte {
	var fallback atomic.Pointer[[]byte]
	failOpen := func() []byte {
		if f := fallback.Load(); f != nil {
			return *f
		}
		return assistant.PassOutput(client)
	}
	answer := make(chan []byte, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "droidyard hook: recovered from a panic, the command passes: %v\n", r)
				answer <- failOpen()
			}
		}()
		answer <- hookDecide(client, root, stdin, deadline, &fallback)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case a := <-answer:
		return a
	case <-timer.C:
		return failOpen()
	}
}

// hookDecide is the answer for one event: the deny for a routed `rm`, handing the agent
// `droidyard trash`, when a daemon answers and opens the workspace, else the
// pass-through; after a shell command, hookObserve's answer. fallback is where the
// decision leaves the answer to give if the deadline comes first.
func hookDecide(client assistant.Client, root string, stdin io.Reader, deadline time.Time, fallback *atomic.Pointer[[]byte]) []byte {
	pass := assistant.PassOutput(client)
	if hookSeam != nil {
		hookSeam()
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, hookMaxStdin+1))
	if err != nil || len(raw) > hookMaxStdin {
		return pass
	}
	// Without --root the project is the client's (ADR-0124 §Decision 3). root stays ""
	// when there is none, and then nothing below dials or routes anything.
	rootless := root == ""
	if rootless {
		var yield bool
		if root, yield = clientRoot(client, raw); yield {
			return pass
		}
	}
	if edit, ok := assistant.DecodeEditEvent(client, raw); ok {
		if rootless && root == "" {
			return pass
		}
		if edit.After {
			return hookRecordEdit(client, root, edit, deadline)
		}
		return hookCheckEdit(client, edit, deadline)
	}
	if after, ok := assistant.DecodeAfterEvent(client, raw); ok {
		return hookObserve(client, root, after, deadline, fallback)
	}
	ev, ok := assistant.DecodeHookEvent(client, raw)
	if !ok {
		return pass
	}
	// The route resolves against the root the command's directory gives (ADR-0085 §3);
	// with none, --root's rules stand.
	if root == "" {
		return pass
	}
	if r, ok := hookRoot(root, ev.Cwd); ok {
		root = r
	}
	route, ok := assistant.RouteRm(ev, root)
	if !ok {
		return pass
	}
	// The replacement is `droidyard trash`, naming this binary, the project and, when the
	// hook's state root came from the environment, that state root, so it reaches the
	// daemon dialled below and never starts another (ADR-0062 §4).
	exe, err := os.Executable()
	if err != nil {
		return pass
	}
	env := platform.Current()
	redirect := assistant.Redirect{Binary: exe, Root: root}
	if env.Getenv("DROIDYARD_STATE_DIR") != "" {
		if redirect.StateDir, err = env.StateDir(); err != nil {
			return pass
		}
	}
	msg, ok := redirect.Message(route)
	if !ok {
		return pass
	}
	// Dial without auto-start: with no daemon the command passes and none is started.
	conn, r, _, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI, Chat: ev.Chat}, false, nil)
	if err != nil {
		return pass
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	// The workspace is opened so the replacement finds it open. That runs no project
	// code and grants no trust (ADR-0015 §5).
	if _, err := openProject(conn, r, root); err != nil {
		return pass
	}
	// A target in a worktree another chat holds is not this chat's to delete: the verdict
	// of a write there, not the trash, answers (ADR-0094 §5, ADR-0095 §3). It is asked after
	// the open, so a daemon that cannot open the workspace is one call, as before.
	var targets []string
	for _, p := range route.Paths {
		if abs := filepath.Join(root, filepath.FromSlash(p)); agent.PathOK(abs) && len(targets) < agent.MaxFiles {
			targets = append(targets, abs)
		}
	}
	if res, ok := checkWrite(client, ev.Chat, targets, deadline); ok {
		switch res.Verdict {
		case capability.VerdictRefuse, capability.VerdictAsk:
			// Never the client's ask: an approved ask would run the plain `rm`, and the
			// trash would be lost. The user who chose ask is consulted by the refusal's
			// words, and the way out is the same (ADR-0094 §6).
			return assistant.DenyOutput(client, res.Message)
		case capability.VerdictWarn:
			// The trash replacement still answers; the warning goes before it.
			if res.Message != "" {
				msg = res.Message + " " + msg
			}
		}
	}
	return assistant.DenyOutput(client, msg)
}

// hookObserve is the answer after a shell command (ADR-0063): the hint for the Android
// commands it ran, computed before anything is dialled so that the deadline still
// answers with it, and, when a daemon answers within the deadline, a record of them in
// Activity. It never denies. A command with no Android program dials nothing.
func hookObserve(client assistant.Client, root string, ev assistant.AfterEvent, deadline time.Time, fallback *atomic.Pointer[[]byte]) []byte {
	obs := assistant.ObserveCommands(ev.Command)
	if len(obs) == 0 {
		return assistant.PassOutput(client)
	}
	out := assistant.AfterOutput(client, ev.Name, assistant.Hint(obs))
	fallback.Store(&out)
	// A command run outside the project and its worktrees is not recorded (ADR-0085 §1),
	// and with no project to compare against (a rootless hook outside one) none is.
	if root == "" {
		return out
	}
	if r, ok := hookRoot(root, commandCwd(root, ev.Command, ev.Cwd)); ok {
		recordCommands(client, ev.Chat, r, obs, deadline)
	}
	return out
}

// clientRoot is the root of a hook run without --root (ADR-0124 §Decision 3): the project
// of the directory the client names as its own, reading the event and the environment
// (assistant.ClientProjectDir). yield is true where that directory holds init's own hook
// for the client, which runs as well and answers, so the user-level one passes. root is ""
// when the directory is no project (projectOf: file reads only), and the hook then dials
// nothing.
func clientRoot(client assistant.Client, event []byte) (root string, yield bool) {
	dir := assistant.ClientProjectDir(client, event, platform.Current().Getenv)
	if dir == "" {
		return "", false
	}
	if assistant.HasProjectHook(client, dir) {
		return "", true
	}
	root, _ = projectOf(dir)
	return root, false
}

// hookRoot is the root a hook event belongs to (ADR-0085 §1): file reads only, no git.
// cwd and flagRoot are canonicalised first, since `init` writes --root from a logical
// path and a client's cwd is usually physical. From cwd upwards the nearest directory
// holding a .git entry decides: a linked worktree of flagRoot's project is the root, and
// so is the project's main tree when flagRoot is one of its worktrees; a .git that is
// neither (a submodule, a nested clone) is passed and the walk goes on (ADR-0095 §5);
// else flagRoot when cwd is flagRoot or lies under it at a component boundary; else
// there is none. An empty cwd (a client that sends none) gives flagRoot (§2).
func hookRoot(flagRoot, cwd string) (root string, ok bool) {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return flagRoot, true
	}
	env := platform.Current()
	flag := env.Canonical(flagRoot)
	dir := env.Canonical(cwd)
	flagMain, isWorktree := platform.LinkedWorktree(env, flag)
	if !isWorktree {
		flagMain = flag
	}
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			if main, ok := platform.LinkedWorktree(env, d); ok && main == flagMain {
				return d, true
			}
			// With --root a linked worktree, its main tree is the project too; with --root
			// the main tree, this is flagRoot and the under-flag rule below says so.
			if d == flagMain && d != flag {
				return d, true
			}
			if d == flag {
				return flagRoot, true
			}
			// Any other .git is a repository nested in the way (a submodule, a nested
			// clone): it is not where the command belongs, so the walk goes on (ADR-0095 §5).
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if rel, err := filepath.Rel(flag, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return flagRoot, true
	}
	return "", false
}

// commandCwd is the directory a shell command ran in, from the event's cwd and a leading
// `cd <dir> &&` or `;` (ADR-0095 §5). What Claude Code 2.1.302 reports depends on where
// the `cd` took the shell (plan m2s step 1, (c1) and run c5): inside the project the
// directory persists, and PostToolUse's cwd is already the directory after the command
// (`cd app && pwd` from repo gave repo/app; `cd .. && pwd` from repo/app gave repo); out of
// the project the shell is reset, and cwd is the project's whatever the `cd` said
// (`cd ../wt && ...` reported the repository). So a relative target resolved against the
// event's cwd P is right only in the second case and wrong in the first (`cd ..` from
// repo/app would land outside the project). The rule therefore compares places: the
// target C is used only if it exists and its place (hookRoot) is a root of the project,
// its main tree or a linked worktree, other than P's place; else P. Persisted cds stay in
// P's place or leave the project, and a reset one is the case where C is a different root.
// An absolute target follows the same rule. A directory that does not exist leaves P: the
// `cd` failed, so the command did not run (`&&`) or ran in P (`;`). A relative target needs
// an absolute P to be placed.
func commandCwd(flagRoot, command, cwd string) string {
	dir, ok := assistant.LeadingCd(command)
	if !ok {
		return cwd
	}
	if !filepath.IsAbs(dir) {
		if !filepath.IsAbs(cwd) {
			return cwd
		}
		dir = filepath.Join(cwd, dir)
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return cwd
	}
	target, ok := hookRoot(flagRoot, dir)
	if !ok {
		return cwd
	}
	if here, ok := hookRoot(flagRoot, cwd); ok && here == target {
		return cwd
	}
	return dir
}

// commandRecord is the parameters of agent.recordCommand for obs: what the reader kept,
// and nothing else (ADR-0063 §4).
func commandRecord(client assistant.Client, obs []assistant.Observed) agent.Record {
	rec := agent.Record{Client: string(client)}
	for _, o := range obs {
		words := o.Words
		if words == nil {
			words = []string{} // the schema requires the list, never null
		}
		rec.Commands = append(rec.Commands, agent.Command{Program: o.Program, Words: words, Suggested: o.Suggested})
	}
	return rec
}

// recordCommands calls agent.recordCommand for obs, and gives up quietly: with no daemon
// (never started here), a refusal or the deadline, nothing is recorded and the caller
// answers all the same (ADR-0063 §3).
func recordCommands(client assistant.Client, chat, root string, obs []assistant.Observed, deadline time.Time) {
	params, err := json.Marshal(commandRecord(client, obs))
	if err != nil {
		return
	}
	recordIn("agent.recordCommand", chat, root, params, deadline)
}

// recordIn calls the workspace-scoped record capability name with params in the workspace
// of root, opened first, as the hook's: it never starts a daemon and gives up quietly.
// Opening the workspace and recording take a connection each, as in `droidyard trash`: a
// cli.Call reads its connection through a goroutine that outlives it. The open runs no
// project code and grants no trust (ADR-0015 §5).
func recordIn(name, chat, root string, params json.RawMessage, deadline time.Time) {
	hello := daemon.Hello{Client: capability.ClientCLI, Chat: chat}
	conn, r, _, err := connectOrStart(hello, false, nil)
	if err != nil {
		return
	}
	conn.SetDeadline(deadline)
	id, err := openProject(conn, r, root)
	conn.Close()
	if err != nil {
		return
	}
	conn, r, _, err = connectOrStart(hello, false, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	var out bytes.Buffer
	// The result is ignored: the answer does not depend on the record.
	_, _ = (cli.Call{Capability: name, Workspace: id, Params: params}).Run(r, conn, cli.Streams{Out: &out, Err: io.Discard}, nil)
}

// checkWrite asks agent.checkWrite for the verdict on paths, for the agent of chat (the
// connection's hello names it, so the daemon can tell it from a holder: ADR-0095 §1). ok
// is false when no daemon answers within deadline, or answers an error: the caller then
// lets the write through (fail open).
func checkWrite(client assistant.Client, chat string, paths []string, deadline time.Time) (res agent.CheckResult, ok bool) {
	if len(paths) == 0 {
		return res, false
	}
	params, err := json.Marshal(agent.CheckParams{Client: string(client), Paths: paths})
	if err != nil {
		return res, false
	}
	conn, r, _, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI, Chat: chat}, false, nil)
	if err != nil {
		return res, false
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	env, err := runCall(conn, r, cli.Call{Capability: "agent.checkWrite", Params: params})
	if err != nil || env.Error != nil || json.Unmarshal(env.Result, &res) != nil {
		return agent.CheckResult{}, false
	}
	return res, true
}

// hookCheckEdit is the answer before an agent's edit tool writes (ADR-0095 §3): the
// verdict of agent.checkWrite on the file, as the client's deny (refuse), ask, or the
// pass-through that carries the warning (warn) where the client's model reads it. With no
// daemon, or any failure, it is the pass-through.
func hookCheckEdit(client assistant.Client, ev assistant.EditEvent, deadline time.Time) []byte {
	res, ok := checkWrite(client, ev.Chat, []string{ev.Path}, deadline)
	if !ok || res.Message == "" {
		return assistant.PassOutput(client)
	}
	switch res.Verdict {
	case capability.VerdictRefuse:
		return assistant.DenyOutput(client, res.Message)
	case capability.VerdictAsk:
		return assistant.AskOutput(client, res.Message)
	case capability.VerdictWarn:
		return assistant.WarnOutput(client, res.Message)
	}
	return assistant.PassOutput(client)
}

// hookRecordEdit is the answer after an agent's edit tool wrote a file: the
// pass-through, and, when a daemon answers within deadline, a record of the file under
// its place (ADR-0095 §3, §5): the root hookRoot gives for the file's directory, the file
// relative to it; none, the hook's own --root, marked outside the project, with the
// absolute path.
func hookRecordEdit(client assistant.Client, flagRoot string, ev assistant.EditEvent, deadline time.Time) []byte {
	rec := agent.Edit{Client: string(client), Files: []agent.EditFile{{Path: ev.Path, Created: ev.Created}}, Outside: true}
	root := flagRoot
	if place, ok := hookRoot(flagRoot, filepath.Dir(ev.Path)); ok {
		env := platform.Current()
		if rel, err := filepath.Rel(env.Canonical(place), env.Canonical(ev.Path)); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root, rec.Outside, rec.Files[0].Path = place, false, filepath.ToSlash(rel)
		}
	}
	if params, err := json.Marshal(rec); err == nil {
		recordIn("agent.recordEdit", ev.Chat, root, params, deadline)
	}
	return assistant.PassOutput(client)
}

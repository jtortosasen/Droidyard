package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/mark3labs/mcp-go/server"
)

// stopTimeout bounds how long `stop` waits for the daemon to release its lock.
const stopTimeout = 30 * time.Second

// chatEnv is the variable Claude Code 2.1.302 sets in an MCP server's environment to the
// chat's own id, unchanged by --resume (ADR-0088; docs/research/spike-c2-sessions-2026-10.md).
// It is not documented as an interface for MCP servers: without it, or with a release that
// renames it, a resumed chat enters its worktree again.
const chatEnv = "CLAUDE_CODE_SESSION_ID"

// connect resolves the socket from the environment, as the daemon did, and says
// hello as the given client kind.
func connect(client capability.ClientKind) (net.Conn, *bufio.Reader, string, error) {
	return connectWith(daemon.Hello{Client: client})
}

// connectWith says hello with h's client kind and options; the rest of the hello is
// filled here.
func connectWith(h daemon.Hello) (net.Conn, *bufio.Reader, string, error) {
	env := platform.Current()
	state, err := env.StateDir()
	if err != nil {
		return nil, nil, "", err
	}
	socket := env.SocketPath(state)
	h.Protocol, h.Version = daemon.Protocol, system.Version
	h.Cwd, _ = os.Getwd()
	conn, r, _, err := daemon.Dial(socket, h)
	return conn, r, socket, err
}

// Auto-start (ADR-0019 §4): how long a shim keeps trying to reach the daemon it
// started, and the backoff between tries.
const (
	autostartTimeout = 10 * time.Second
	firstRetry       = 25 * time.Millisecond
	maxRetry         = 500 * time.Millisecond
)

// autostartWaiting is a test seam, nil in the shipped binary: connectOrStart calls it once
// the daemon it started has returned and before it waits for the socket, so a test knows
// the wait it interrupts is under way.
var autostartWaiting func()

// errStarted marks a failure after this shim started a daemon.
var errStarted = errors.New("a daemon was started, but none answered")

// errInterrupted is a Ctrl-C while waiting for a daemon to answer.
var errInterrupted = errors.New("interrupted while waiting for the daemon")

// connectOrStart connects and says hello. If nothing answers on the socket and
// autostart is set, it starts `droidyard serve --detach` and retries with backoff for
// up to autostartTimeout. Racing shims may each start one: every loser exits, and
// every shim reaches the winner. When interrupt closes, it stops waiting; nil never
// does.
func connectOrStart(h daemon.Hello, autostart bool, interrupt <-chan struct{}) (net.Conn, *bufio.Reader, string, error) {
	conn, r, socket, err := connectWith(h)
	if err == nil || !autostart || !nothingAnswers(err) {
		return conn, r, socket, err
	}
	if err := startDaemon(); err != nil {
		return nil, nil, socket, fmt.Errorf("%w: %v", errStarted, err)
	}
	if autostartWaiting != nil {
		autostartWaiting()
	}
	deadline := time.Now().Add(autostartTimeout)
	for delay := firstRetry; ; delay = min(2*delay, maxRetry) {
		select {
		case <-time.After(delay):
		case <-interrupt:
			return nil, nil, socket, errInterrupted
		}
		conn, r, socket, err = connectWith(h)
		if err == nil || !nothingAnswers(err) {
			return conn, r, socket, err
		}
		if time.Now().After(deadline) {
			return nil, nil, socket, fmt.Errorf("%w within %s: %v", errStarted, autostartTimeout, err)
		}
	}
}

// nothingAnswers is true when the socket is absent or refuses the connection: no
// daemon is listening. A daemon that answers and refuses the hello is not this case.
func nothingAnswers(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// startDaemon runs `droidyard serve --detach`, which starts the daemon in a session of
// its own and returns. It gets no stdio of ours: an MCP client's pipes must not
// outlive its shim in the daemon.
func startDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := &exec.Cmd{Path: exe, Args: []string{"droidyard", "serve", "--detach"}}
	return cmd.Run()
}

// reportDialError explains a failed connection on stderr. There is no daemon to
// return an envelope, so this is not one (ADR-0019 §Lifetime).
func reportDialError(cmd, socket string, err error) {
	var e *capability.Error
	switch {
	case errors.As(err, &e):
		fmt.Fprintf(os.Stderr, "droidyard %s: %s: %s\n", cmd, e.Code, e.Message)
		if e.NextStep != "" {
			fmt.Fprintf(os.Stderr, "Next: %s\n", e.NextStep)
		}
	case errors.Is(err, errStarted):
		fmt.Fprintf(os.Stderr, "droidyard %s: %v\n", cmd, err)
		fmt.Fprintf(os.Stderr, "See the daemon's log, %s, next to its lock.\n", logFile)
	default:
		fmt.Fprintf(os.Stderr, "droidyard %s: no daemon answers at %s: %v\n", cmd, socket, err)
		fmt.Fprintln(os.Stderr, "Start one with `droidyard serve`, or drop --no-autostart.")
	}
}

// call runs `droidyard call`, starting a daemon if none answers, unless
// --no-autostart. A capability that takes a project gets --project, or the current
// directory (ADR-0137 §5).
func call(ct *cmdTree, args []string) int {
	c, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droidyard call: %v\n%s\n", err, cli.Usage)
		return cli.ExitUsage
	}
	ct.defaultProject(&c)
	return runCapability("call", c)
}

// progressEvery is the least time between two progress lines on a terminal (plan m2z D9);
// a test lowers it through installTestSeams.
var progressEvery = time.Second

// stderrTerminal says whether standard error is a person's terminal, where a call that is
// still running shows its progress (plan m2z D9): a character device, the standard library
// alone, and not /dev/null, which a script that discards stderr names. A test replaces it
// through installTestSeams, since the pipe a test reads stderr from is never one.
var stderrTerminal = func() bool {
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// runCapability makes one capability call as `droidyard call` does, and a derived command
// (`droidyard device list`) the same way: it starts a daemon when none answers, unless the
// call says NoAutostart, and prints the response envelope. who names the command in its own
// messages.
func runCapability(who string, c cli.Call) int {
	// Installed before the request goes out, so a Ctrl-C is never lost to the default
	// action. The first one asks the adapter to cancel; a second one, if cancelling
	// hangs, kills the process as usual.
	interrupt := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		signal.Reset(os.Interrupt)
		close(interrupt)
	}()

	// The hello names no project: a call that takes one carries it as `project`, which the
	// command line defaults to the current directory (ADR-0137 §5).
	hello := daemon.Hello{Client: capability.ClientCLI}
	conn, r, socket, err := connectOrStart(hello, !c.NoAutostart, interrupt)
	if errors.Is(err, errInterrupted) {
		fmt.Fprintln(os.Stderr, "droidyard "+who+":", err)
		return cli.ExitInterrupted
	}
	if err != nil {
		reportDialError(who, socket, err)
		return 1
	}
	defer conn.Close()
	code, err := c.Run(r, conn, cli.Streams{Out: os.Stdout, Err: os.Stderr, Terminal: stderrTerminal(), ProgressEvery: progressEvery}, interrupt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard "+who+":", err)
		return 1
	}
	return code
}

// mcpShim runs `droidyard mcp`: it says hello as an MCP client, then relays stdin to
// the socket and the socket to stdout, byte for byte, with no MCP logic (ADR-0019 §2).
// When stdin ends it half-closes the socket and relays until the daemon closes the
// session, then exits 0. If the socket closes first, it exits 1 with a message on
// stderr (ADR-0019 §Lifetime). It starts a daemon if none answers, unless
// --no-autostart.
//
// The session's project is --root, else the project of --from or the cwd (projectOf,
// the rule `ui` and `init` use). With none, the shim dials and starts nothing: it
// serves a session with no tools itself (ADR-0124 §2).
func mcpShim(args []string) int {
	const usage = "usage: droidyard mcp [--root <path>] [--from <dir>] [--wait <duration>] [--tools core|all] [--no-autostart]"
	hello := daemon.Hello{Client: capability.ClientMCP}
	autostart := true
	explicitRoot := ""
	from, fromGiven := "", false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--no-autostart":
			autostart = false
		case a == "--root" || strings.HasPrefix(a, "--root="):
			v, ok := strings.CutPrefix(a, "--root=")
			if !ok && i+1 < len(args) {
				i++
				v = args[i]
			}
			if v == "" {
				fmt.Fprintf(os.Stderr, "droidyard mcp: --root needs a project directory\n%s\n", usage)
				return 2
			}
			explicitRoot = v
		case a == "--from" || strings.HasPrefix(a, "--from="):
			v, ok := strings.CutPrefix(a, "--from=")
			if !ok && i+1 < len(args) {
				i++
				v = args[i]
			}
			// An empty value is what a client expands a window with no folder to, and
			// one that still holds ${ is a client variable it did not expand: neither
			// names a folder, so both leave the cwd to name it (ADR-0124 §2).
			from, fromGiven = v, true
		case a == "--wait" || strings.HasPrefix(a, "--wait="):
			v, ok := strings.CutPrefix(a, "--wait=")
			if !ok && i+1 < len(args) {
				i++
				v = args[i]
			}
			// The budget a long-running tool waits before answering with the
			// operation view; it must stay below the client's tool timeout
			// (ADR-0020 §Adapter projections).
			// The hello carries whole milliseconds, and 0 means the default: a
			// shorter budget would silently become 20 s.
			d, err := time.ParseDuration(v)
			if err != nil || d < time.Millisecond {
				fmt.Fprintf(os.Stderr, "droidyard mcp: --wait needs a duration of at least 1ms, such as 45s, not %q\n%s\n", v, usage)
				return 2
			}
			hello.WaitMs = d.Milliseconds()
		case a == "--tools" || strings.HasPrefix(a, "--tools="):
			v, ok := strings.CutPrefix(a, "--tools=")
			if !ok && i+1 < len(args) {
				i++
				v = args[i]
			}
			// Refused here, before any connection: a typo must not start a session
			// that lists the wrong tools (ADR-0036 Decision 4).
			if !mcpadapter.ValidProfile(v) {
				fmt.Fprintf(os.Stderr, "droidyard mcp: --tools needs core or all, not %q\n%s\n", v, usage)
				return 2
			}
			if v == mcpadapter.ProfileCore {
				hello.Tools = v
			}
		default:
			fmt.Fprintf(os.Stderr, "droidyard mcp: unexpected argument %q\n%s\n", a, usage)
			return 2
		}
	}
	// The chat Claude Code names in the server's environment, if it does (ADR-0088 §1).
	if v := os.Getenv(chatEnv); daemon.ChatPattern.MatchString(v) {
		hello.Chat = v
	}
	if fromGiven && explicitRoot != "" {
		fmt.Fprintf(os.Stderr, "droidyard mcp: --from and --root exclude each other\n%s\n", usage)
		return 2
	}
	// The session's binding (ADR-0019 §Sessions, as ADR-0124 §2 changes it): --root,
	// made absolute against the cwd; else the project of --from or the cwd. The daemon
	// opens it and canonicalises it.
	cwd, _ := os.Getwd()
	if explicitRoot == "" {
		dir := cwd
		if from != "" && !strings.Contains(from, "${") {
			dir = from
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(cwd, dir)
			}
		}
		ok := false
		if dir != "" {
			_, ok = projectOf(filepath.Clean(dir))
		}
		if !ok {
			return serveNoTools(dir)
		}
	}
	conn, r, socket, err := connectOrStart(hello, autostart, nil)
	if err != nil {
		reportDialError("mcp", socket, err)
		return 1
	}
	defer conn.Close()

	fromDaemon := make(chan error, 1)
	go func() {
		// r, not conn: it may already hold what followed the hello reply.
		_, err := io.Copy(os.Stdout, r)
		fromDaemon <- err
	}()
	fromClient := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		fromClient <- err
	}()

	select {
	case err := <-fromClient:
		if err != nil {
			fmt.Fprintln(os.Stderr, "droidyard mcp: the daemon closed the session:", err)
			return 1
		}
		conn.(*net.UnixConn).CloseWrite()
		if err := <-fromDaemon; err != nil {
			fmt.Fprintln(os.Stderr, "droidyard mcp: reading from the daemon:", err)
			return 1
		}
		return 0
	case err := <-fromDaemon:
		if err == nil {
			err = io.EOF
		}
		fmt.Fprintf(os.Stderr, "droidyard mcp: the daemon closed the session (%v); it may have stopped or crashed\n", err)
		return 1
	}
}

// noToolsInstructions is the one line a session with no project gives its client.
func noToolsInstructions(dir string) string {
	where := "No Android project at " + dir
	if dir == "" {
		where = "No Android project here"
	}
	return where + ": droidyard has no tools in this session. " +
		"Start the assistant in the project's folder or one of its worktrees."
}

// serveNoTools is the session `mcp` serves when it finds no project (ADR-0124 §2): the
// tools capability declared and an empty list, answered by mcp-go itself, so the session
// is the same whether or not a daemon runs. It dials nothing, starts nothing and writes
// nothing, on stderr or on disk; it exits 0 when stdin ends.
func serveNoTools(dir string) int {
	srv := server.NewMCPServer("droidyard", system.Version,
		server.WithToolCapabilities(false),
		server.WithInstructions(noToolsInstructions(dir)),
	)
	stdio := server.NewStdioServer(srv)
	// mcp-go logs to stderr by default; this session writes nothing there.
	stdio.SetErrorLogger(log.New(io.Discard, "", 0))
	_ = stdio.Listen(context.Background(), os.Stdin, os.Stdout)
	return 0
}

// status names the lock holder and says whether the socket answers. It exits 0 iff a
// daemon answers.
func status() int {
	state, err := platform.Current().StateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard status:", err)
		return 1
	}
	info, held, err := daemon.Holder(state)
	switch {
	case err != nil && !held:
		fmt.Fprintln(os.Stderr, "droidyard status:", err)
		return 1
	case !held:
		fmt.Printf("no daemon is running (state %s)\n", state)
		return 1
	case err != nil:
		fmt.Printf("a daemon holds %s/%s, but the file does not name it: %v\n", state, daemon.LockFile, err)
	default:
		fmt.Printf("daemon pid %d, version %s, started %s\n", info.PID, info.Version, info.StartTime.Format(time.RFC3339))
	}

	conn, _, socket, err := connect(capability.ClientCLI)
	if err != nil {
		fmt.Printf("socket %s does not answer: %v\n", socket, err)
		return 1
	}
	conn.Close()
	fmt.Printf("socket %s answers\n", socket)
	// The API address comes from the lock, which the daemon writes before it binds the
	// socket (plan m1d, step 5). The lock is read again here, after the socket answered:
	// the record read at the top may predate the daemon's rewrite that added "http". The
	// token is not printed: there is no page to open with it (ADR-0137 §1).
	httpInfo, err := daemon.ReadLock(state)
	if err != nil {
		fmt.Printf("http: %v\n", err)
		return 0
	}
	switch addr, err := httpAddr(state, httpInfo); {
	case err != nil:
		fmt.Printf("http: %v\n", err)
	default:
		fmt.Printf("http %s\n", addr)
	}
	return 0
}

// httpAddr is the API address the lock names. A lock that names none is a daemon
// without an HTTP listener, and has to be restarted.
func httpAddr(state string, info daemon.LockInfo) (string, error) {
	if info.HTTP == nil || *info.HTTP == "" {
		return "", fmt.Errorf("%s names no HTTP address: this daemon serves no HTTP API; run `droidyard stop` and start it again", filepath.Join(state, daemon.LockFile))
	}
	return *info.HTTP, nil
}

// runCall runs call over the connection and returns the response envelope; an error the
// daemon answered with is the envelope's, not this function's.
func runCall(conn net.Conn, r *bufio.Reader, call cli.Call) (capability.ResponseEnvelope, error) {
	var out bytes.Buffer
	var env capability.ResponseEnvelope
	if _, err := call.Run(r, conn, cli.Streams{Out: &out, Err: io.Discard}, nil); err != nil {
		return env, err
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		return env, fmt.Errorf("the daemon's answer is not an envelope: %w", err)
	}
	return env, nil
}

// stop sends SIGTERM to the daemon that holds the lock and waits for the lock's
// release (ADR-0019 §3; ADR-0021 §Locking). With no daemon running it does nothing.
func stop() int {
	state, err := platform.Current().StateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard stop:", err)
		return 1
	}
	info, held, err := daemon.Holder(state)
	switch {
	case !held && err != nil:
		fmt.Fprintln(os.Stderr, "droidyard stop:", err)
		return 1
	case !held:
		fmt.Printf("no daemon is running (state %s)\n", state)
		return 0
	case err != nil || info.PID <= 0:
		fmt.Fprintf(os.Stderr, "droidyard stop: a daemon holds the lock, but %s/%s does not name it: %v\n", state, daemon.LockFile, err)
		return 1
	}
	if err := syscall.Kill(info.PID, syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "droidyard stop: signalling pid %d: %v\n", info.PID, err)
		return 1
	}
	for deadline := time.Now().Add(stopTimeout); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, held, _ := daemon.Holder(state); !held {
			fmt.Printf("stopped daemon pid %d\n", info.PID)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "droidyard stop: pid %d still holds the lock after %s\n", info.PID, stopTimeout)
	return 1
}

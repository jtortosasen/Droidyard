package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// stopTimeout bounds how long `stop` waits for the daemon to release its lock.
const stopTimeout = 30 * time.Second

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
// --no-autostart.
func call(args []string) int {
	c, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droidyard call: %v\n%s\n", err, cli.Usage)
		return cli.ExitUsage
	}
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

	// The cwd's root goes in the hello; the daemon uses it for a workspace-scoped call
	// without --workspace (plan m1c, R3).
	cwd, _ := os.Getwd()
	hello := daemon.Hello{Client: capability.ClientCLI}
	if cwd != "" {
		hello.Root = resolveRoot("", cwd)
	}
	conn, r, socket, err := connectOrStart(hello, !c.NoAutostart, interrupt)
	if errors.Is(err, errInterrupted) {
		fmt.Fprintln(os.Stderr, "droidyard call:", err)
		return cli.ExitInterrupted
	}
	if err != nil {
		reportDialError("call", socket, err)
		return 1
	}
	defer conn.Close()
	code, err := c.Run(r, conn, cli.Streams{Out: os.Stdout, Err: os.Stderr}, interrupt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard call:", err)
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
func mcpShim(args []string) int {
	const usage = "usage: droidyard mcp [--root <path>] [--wait <duration>] [--no-autostart]"
	hello := daemon.Hello{Client: capability.ClientMCP}
	autostart := true
	explicitRoot := ""
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
		default:
			fmt.Fprintf(os.Stderr, "droidyard mcp: unexpected argument %q\n%s\n", a, usage)
			return 2
		}
	}
	// The session's binding (ADR-0019 §Sessions): --root, made absolute against the
	// cwd, else the cwd's project root. The daemon opens it and canonicalises it.
	if cwd, err := os.Getwd(); err == nil {
		hello.Root = resolveRoot(explicitRoot, cwd)
	} else if filepath.IsAbs(explicitRoot) {
		hello.Root = filepath.Clean(explicitRoot)
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
	// The address and the URL come from the lock and run/http-token, which the daemon
	// writes before it binds the socket (plan m1d, step 5). The lock is read again here,
	// after the socket answered: the record read at the top may predate the daemon's
	// rewrite that added "http". The token is printed here, on a terminal the user
	// asked, and nowhere else (http-websocket.md §Bootstrapping).
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
		token, err := daemon.ReadToken(state)
		if err != nil {
			fmt.Printf("http token: %v\n", err)
			return 0
		}
		fmt.Println(uiURL(addr, token))
	}
	return 0
}

// httpAddr is the UI's address the lock names. A lock that names none is a daemon
// without an HTTP listener: one that cannot serve `ui`, and has to be restarted.
func httpAddr(state string, info daemon.LockInfo) (string, error) {
	if info.HTTP == nil || *info.HTTP == "" {
		return "", fmt.Errorf("%s names no HTTP address: this daemon serves no web UI; run `droidyard stop` and start it again", filepath.Join(state, daemon.LockFile))
	}
	return *info.HTTP, nil
}

// uiURL is the tokenised URL that bootstraps the web UI: the token is in the
// fragment, which a browser never sends to a server (http-websocket.md §Bootstrapping).
func uiURL(addr, token string) string {
	return "http://" + addr + "/#token=" + token
}

// openURL opens a URL in the default browser. A variable only so that this package's
// tests can replace it in a re-executed binary, as they do testCapabilities.
var openURL = openBrowser

// openBrowser runs the platform's launcher (plan m1d, R4). The URL is in its argument
// list while it runs (the plan's Unverified table flags this for the owner).
func openBrowser(url string) error {
	launcher := "xdg-open"
	if runtime.GOOS == "darwin" {
		launcher = "open"
	}
	// None of exec's errors quotes the argument list, so the token stays out of them.
	if err := exec.Command(launcher, url).Run(); err != nil {
		return fmt.Errorf("running %s: %w", launcher, err)
	}
	return nil
}

// ui runs `droidyard ui`: it connects as `call` does, starting a daemon when none
// answers unless --no-autostart, then opens the browser at the tokenised URL the lock
// and run/http-token give (ADR-0019 §3, §4). It does not print the token.
func ui(args []string) int {
	autostart := true
	for _, a := range args {
		if a != "--no-autostart" {
			fmt.Fprintf(os.Stderr, "droidyard ui: unexpected argument %q\nusage: droidyard ui [--no-autostart]\n", a)
			return 2
		}
		autostart = false
	}
	conn, _, socket, err := connectOrStart(daemon.Hello{Client: capability.ClientCLI}, autostart, nil)
	if err != nil {
		reportDialError("ui", socket, err)
		return 1
	}
	conn.Close()
	state, err := platform.Current().StateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard ui:", err)
		return 1
	}
	info, err := daemon.ReadLock(state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard ui:", err)
		return 1
	}
	addr, err := httpAddr(state, info)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard ui:", err)
		return 1
	}
	token, err := daemon.ReadToken(state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "droidyard ui: reading the token: %v\n", err)
		return 1
	}
	if err := openURL(uiURL(addr, token)); err != nil {
		fmt.Fprintf(os.Stderr, "droidyard ui: opening the browser: %v\nOpen the address `droidyard status` prints instead.\n", err)
		return 1
	}
	fmt.Printf("opened http://%s/ in the browser\n", addr)
	return 0
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

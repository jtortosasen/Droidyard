package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/adapters/httpapi"
	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	proc "github.com/jtortosasen/droidyard/internal/process"
	"github.com/jtortosasen/droidyard/internal/schema"
	"github.com/jtortosasen/droidyard/internal/store"
	"github.com/jtortosasen/droidyard/web"
	"golang.org/x/sys/unix"
)

// shutdownTimeout bounds a graceful stop: operations cancelled, sessions closed. The
// supervised processes stop on their own timeouts, outside it. A variable only so
// that this package's tests can shorten it in a re-executed binary.
var shutdownTimeout = 15 * time.Second

// httpPort is the UI's loopback port (plan m1d, D3). A variable only so that this
// package's tests can set it in a re-executed binary: 0 there, so test daemons never
// contend for the fixed port.
var httpPort = httpapi.DefaultPort

// newToken is a per-launch HTTP token: 256 random bits as 64 lowercase hex digits
// (http-websocket.md §Security baseline rule 4; plan m1d, D8).
func newToken() string {
	var b [32]byte
	rand.Read(b[:]) // never returns an error (crypto/rand, Go 1.24)
	return hex.EncodeToString(b[:])
}

// daemonOptions wires the daemon: the registry's store, a handler per client kind, and
// the UI's HTTP side on httpAddr with a token new at every start. The MCP adapter gets
// the published schemas from here, so it never imports internal/schema
// (docs/architecture/overview.md §Adapter boundary), and reads operation progress
// from bus and resolves an image reference against artifacts.
func daemonOptions(st *store.Store, sup *proc.Supervisor, socket, httpAddr string, reg *capability.Registry, ops *capability.Operations, bus *event.Bus, artifacts *artifact.Store, afterOps func(context.Context), log io.Writer) (daemon.Options, *mcpadapter.Server, error) {
	doc, err := schema.Generate(reg, system.Version)
	if err != nil {
		return daemon.Options{}, nil, err
	}
	schemas := make(map[string]mcpadapter.Schema, len(doc.Capabilities))
	for _, c := range doc.Capabilities {
		schemas[c.Name] = mcpadapter.Schema{Input: c.Input, Output: c.Output}
	}
	mcpServer, err := mcpadapter.New(reg, ops, schemas, system.Version, log)
	if err != nil {
		return daemon.Options{}, nil, err
	}
	mcpServer.UseBus(bus)
	mcpServer.UseArtifacts(artifacts)
	token := newToken()
	return daemon.Options{
		State:      st.Root(),
		Store:      st,
		Supervisor: sup,
		Socket:     socket,
		Version:    system.Version,
		UID:        os.Getuid(),
		Ops:        ops,
		AfterOps:   afterOps,
		Handlers: map[capability.ClientKind]daemon.Handler{
			capability.ClientCLI: daemon.Native(reg, ops),
			capability.ClientMCP: func(ctx context.Context, s *daemon.Session) {
				budget := time.Duration(s.Hello.WaitMs) * time.Millisecond
				if err := mcpServer.Serve(mcpadapter.WithSessionID(ctx, s.ID), s.Reader, s.Conn, budget, s.Hello.Root, s.Hello.Tools); err != nil {
					fmt.Fprintf(log, "droidyard: MCP session %s ended: %v\n", s.ID, err)
				}
			},
		},
		HTTP: &daemon.HTTP{
			Addr:    httpAddr,
			Listen:  httpapi.Listen,
			Handler: httpapi.New(reg, ops, bus, token, web.Assets()),
			Token:   token,
		},
		Log: log,
	}, mcpServer, nil
}

// serve runs the daemon until SIGTERM or SIGINT (ADR-0019 §5). It returns the exit
// status: 0 after a graceful stop, 1 when it could not start, including when another
// daemon holds the lock. With --detach it runs detached (ADR-0019 §4): if this process
// does not lead a session of its own, it starts one that does and returns at once.
func serve(args []string) int {
	detach := false
	for _, a := range args {
		if a != "--detach" {
			fmt.Fprintf(os.Stderr, "droidyard serve: unexpected argument %q\nusage: droidyard serve [--detach]\n", a)
			return 2
		}
		detach = true
	}
	env := platform.Current()
	state, err := env.StateDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard serve:", err)
		return 1
	}
	if detach {
		if sid, _ := unix.Getsid(0); sid != os.Getpid() {
			return startDetached(state)
		}
		fmt.Fprintf(os.Stderr, "droidyard serve (pid %d): starting\n", os.Getpid())
	}

	logw := daemonLog(state, detach)
	// The operation trace and the HTTP server's own lines use the standard logger.
	log.SetOutput(logw)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	st, err := store.New(state)
	if err != nil {
		fmt.Fprintln(logw, "droidyard serve:", err)
		return 1
	}
	// The daemon owns one supervisor; daemon.Start loads it once the pid records of a
	// previous daemon are reconciled.
	sup := proc.New(proc.Options{Store: st, Log: logw})
	reg, ops, bus, artifacts, adb, _ := registry(env, st, sup, logw)
	defer artifacts.Close()
	socket := env.SocketPath(state)
	opts, _, err := daemonOptions(st, sup, socket, net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort)), reg, ops, bus, artifacts, adb.Close, logw)
	if err != nil {
		fmt.Fprintf(logw, "droidyard serve (pid %d): %v\n", os.Getpid(), err)
		return 1
	}
	d, err := daemon.Start(opts)
	if errors.Is(err, daemon.ErrLocked) {
		// Racing auto-starts end here: the winner serves every shim.
		fmt.Fprintf(logw, "droidyard serve (pid %d): lost the lock: %v\n", os.Getpid(), err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(logw, "droidyard serve (pid %d): %v\n", os.Getpid(), err)
		return 1
	}
	fmt.Fprintf(logw, "droidyard serve (pid %d): holding %s/%s, listening on %s\n", os.Getpid(), state, daemon.LockFile, socket)

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := d.Shutdown(sctx); err != nil {
		fmt.Fprintln(logw, "droidyard serve: shutdown:", err)
		return 1
	}
	fmt.Fprintf(logw, "droidyard serve (pid %d): stopped\n", os.Getpid())
	return 0
}

// logFile is the detached daemon's stdout and stderr (ADR-0021 §Layout).
const logFile = "daemon.log"

// startDetached starts `droidyard serve --detach` in a new session, with its stdio
// appended to <state>/daemon.log and its cwd /, so neither a client that kills its
// shim's process group nor a terminal that closes can reach it (ADR-0019 §4). The log
// is opened O_APPEND: racing starts add their lines instead of overwriting each
// other's.
func startDetached(state string) int {
	if err := os.MkdirAll(state, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "droidyard serve:", err)
		return 1
	}
	path := filepath.Join(state, logFile)
	logf, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard serve:", err)
		return 1
	}
	defer logf.Close()
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard serve:", err)
		return 1
	}
	cmd := &exec.Cmd{
		Path:        exe,
		Args:        []string{"droidyard", "serve", "--detach"},
		Dir:         "/",
		Stdout:      logf,
		Stderr:      logf,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "droidyard serve: starting the daemon:", err)
		return 1
	}
	fmt.Printf("droidyard serve: started pid %d, logging to %s\n", cmd.Process.Pid, path)
	cmd.Process.Release()
	return 0
}

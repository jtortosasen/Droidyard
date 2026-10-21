package main

import (
	"bytes"
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
	"github.com/jtortosasen/droidyard/adapters/peer"
	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/hosting"
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
func daemonOptions(st *store.Store, sup *proc.Supervisor, socket, httpAddr string, reg *capability.Registry, ops *capability.Operations, bus *event.Bus, artifacts *artifact.Store, adb *device.ADB, log io.Writer) (daemon.Options, *mcpadapter.Server, error) {
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
	ui := httpapi.New(reg, ops, bus, token, web.Assets())
	ui.UseScreens(screenAttach(adb))
	ui.UseTextArtifacts(uiTextArtifacts(artifacts))
	return daemon.Options{
		State:      st.Root(),
		Store:      st,
		Supervisor: sup,
		Socket:     socket,
		Version:    system.Version,
		UID:        os.Getuid(),
		Ops:        ops,
		AfterOps:   adb.Close,
		// Recordings a dead daemon left running are finalised once this one holds the lock (a
		// daemon that lost it must not touch them) and before anything is served; a recorder's
		// start and the recordings' capabilities run it first too, and it runs once.
		AfterLock: func() { adb.RecoverRecordings() },
		Handlers: map[capability.ClientKind]daemon.Handler{
			capability.ClientCLI: daemon.Native(reg, ops),
			capability.ClientMCP: func(ctx context.Context, s *daemon.Session) {
				budget := time.Duration(s.Hello.WaitMs) * time.Millisecond
				if err := mcpServer.Serve(mcpadapter.WithSessionID(ctx, s.ID), s.Reader, s.Conn, budget, s.Hello.Root, s.Hello.Tools, s.Hello.Chat); err != nil {
					fmt.Fprintf(log, "droidyard: MCP session %s ended: %v\n", s.ID, err)
				}
			},
		},
		HTTP: &daemon.HTTP{
			Addr:    httpAddr,
			Listen:  httpapi.Listen,
			Handler: ui,
			Token:   token,
		},
		Log: log,
	}, mcpServer, nil
}

// screenAttach is the live view as the HTTP adapter may hold it (ADR-0078 §4): the
// device's AttachScreen, with the viewer's packets and the two refusals in the adapter's
// own terms. A viewer of the adapter's has Next and Close and nothing else, so nothing a
// socket sends can reach the source's Inject.
func screenAttach(adb *device.ADB) httpapi.ScreenAttach {
	return func(id string) (httpapi.ScreenViewer, error) {
		v, err := adb.AttachScreen(id)
		switch {
		case errors.Is(err, device.ErrScreenStreamUnknown):
			return nil, httpapi.ErrScreenUnknown
		case errors.Is(err, device.ErrScreenStreamAttached):
			return nil, httpapi.ErrScreenAttached
		case err != nil:
			return nil, err
		}
		return screenViewer{v}, nil
	}
}

// peerScreenAttach is the live view as the peer adapter may hold it (ADR-0100 §3): the
// device's AttachPeerScreen for the named peer, with the same refusals in the adapter's terms.
func peerScreenAttach(adb *device.ADB) httpapi.PeerScreenAttach {
	return func(peer, id string) (httpapi.ScreenViewer, error) {
		v, err := adb.AttachPeerScreen(peer, id)
		switch {
		case errors.Is(err, device.ErrScreenStreamUnknown):
			return nil, httpapi.ErrScreenUnknown
		case errors.Is(err, device.ErrScreenStreamAttached):
			return nil, httpapi.ErrScreenAttached
		case err != nil:
			return nil, err
		}
		return screenViewer{v}, nil
	}
}

// peerArtifacts is what the peer listener serves of the artifact store (ADR-0099 §2): the
// artifacts the named peer's own calls made, by the same ownership rule artifact.read applies
// to a peer, and no other.
func peerArtifacts(store *artifact.Store) peer.ArtifactReader {
	return func(name, id string) (peer.Artifact, error) {
		e, err := store.GetOwned(artifact.RefPrefix+id, artifact.PeerOwner(name))
		if err != nil {
			return peer.Artifact{}, err
		}
		return peer.Artifact{MediaType: e.Ref.MediaType, Untrusted: e.Ref.Untrusted, Data: e.Data}, nil
	}
}

// uiTextArtifacts is what the UI route serves of a finished text artifact (rule 8): the
// bytes of the store's entry, from memory or from the file in place.
func uiTextArtifacts(store *artifact.Store) httpapi.TextArtifacts {
	return func(id string) (httpapi.TextFile, error) {
		e, err := store.Get(artifact.RefPrefix + id)
		if err != nil {
			return httpapi.TextFile{}, err
		}
		if e.Growing {
			// The route refuses a growing artifact before it opens one; never serve one as whole.
			return httpapi.TextFile{}, capability.NewError(capability.ErrInvalidParams, "the artifact is still being written")
		}
		if e.File == "" {
			return httpapi.TextFile{Body: nopCloseSeeker{bytes.NewReader(e.Data)}}, nil
		}
		f, err := os.Open(e.File)
		if err != nil {
			return httpapi.TextFile{}, err
		}
		return httpapi.TextFile{Body: f}, nil
	}
}

type nopCloseSeeker struct{ *bytes.Reader }

func (nopCloseSeeker) Close() error { return nil }

// screenViewer adapts a screen.Viewer to the adapter's, packet for packet and byte for byte.
type screenViewer struct{ v screen.Viewer }

func (s screenViewer) Next(ctx context.Context) (httpapi.ScreenPacket, error) {
	p, err := s.v.Next(ctx)
	if err != nil {
		return httpapi.ScreenPacket{}, err
	}
	return httpapi.ScreenPacket{Key: p.Key, Config: p.Config, PTS: p.PTS, Data: p.Data}, nil
}

func (s screenViewer) Close() { s.v.Close() }

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
	reg, ops, bus, artifacts, adb, _, host := registry(env, st, sup, logw)
	defer artifacts.Close()
	// The idle sweep of the session registry runs for the daemon's life (ADR-0094 §4).
	sweep := capability.SweepInterval
	if sessionSweep > 0 {
		sweep = sessionSweep
	}
	go ops.Sessions().Run(ctx, sweep)
	socket := env.SocketPath(state)
	opts, _, err := daemonOptions(st, sup, socket, net.JoinHostPort("127.0.0.1", strconv.Itoa(httpPort)), reg, ops, bus, artifacts, adb, logw)
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
	// The peer link (ADR-0098), once the lock is held: the identity has one writer. A daemon
	// that cannot make one runs without peers.
	stopPeers := startPeers(host, reg, ops, bus, artifacts, adb, logw)
	fmt.Fprintf(logw, "droidyard serve (pid %d): holding %s/%s, listening on %s\n", os.Getpid(), state, daemon.LockFile, socket)

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// The peers first: no peer request reaches the operations the shutdown cancels.
	stopPeers()
	if err := d.Shutdown(sctx); err != nil {
		fmt.Fprintln(logw, "droidyard serve: shutdown:", err)
		return 1
	}
	fmt.Fprintf(logw, "droidyard serve (pid %d): stopped\n", os.Getpid())
	return 0
}

// reconcileEvery is how often the peer listeners and the links are matched to the
// configuration and to this machine's interfaces (ADR-0109 §3). A variable only so that a
// test can shorten it.
var reconcileEvery = 2 * time.Second

// peerDefaultPort is the port the private listeners try first (ADR-0109 §2). A variable only
// so that this package's tests can set it in a re-executed binary: 0 there, so test daemons
// never contend for the real default.
var peerDefaultPort = hosting.DefaultPort

// startPeers makes the daemon's identity, and keeps the peer listeners and the links
// matched to the configuration and to this machine's interfaces: a first pass now, then one
// every reconcileEvery and one whenever host.Reconcile is called (ADR-0109 §3). It returns
// the function that stops them all and waits. A configuration that cannot be used binds and
// dials nothing: system.health says so through its config (ADR-0032 §4).
func startPeers(host *hosting.Host, reg *capability.Registry, ops *capability.Operations, bus *event.Bus, store *artifact.Store, adb *device.ADB, logw io.Writer) (stop func()) {
	if err := host.Init(); err != nil {
		fmt.Fprintf(logw, "droidyard serve (pid %d): no peer identity: %v\n", os.Getpid(), err)
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := peer.New(peer.Options{Host: host, Sessions: httpapi.NewPeerSessions(reg, ops, bus), Screens: peerScreenAttach(adb), Uploads: host.Uploads(), Artifacts: peerArtifacts(store), Admit: host.Admit, API: system.Version, Log: logw})
	host.SetDefaultPort(peerDefaultPort)
	host.UseListeners(srv)
	leased := make(chan struct{})
	// The check that a lent device is still lent to the peer leasing it (ADR-0104 §4), at the
	// interval of the pin check.
	go func() { defer close(leased); adb.RunLeases(ctx) }()
	host.Start()
	host.Reconcile()
	reconciled := make(chan struct{})
	go func() {
		defer close(reconciled)
		t := time.NewTicker(reconcileEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				host.Reconcile()
			}
		}
	}()
	return func() {
		cancel()
		<-reconciled
		srv.Close()
		<-leased
		host.Stop()
		// After the listeners, so no upload is still arriving; what is left is removed.
		host.Uploads().Close()
	}
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

package hosting

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"sync"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/store"
)

// Config is the policy as the peer link reads it: *policy.Policy.
type Config interface {
	Hosting() (policy.HostingPolicy, *capability.Error)
	Remotes() ([]policy.Remote, *capability.Error)
}

// Host is a daemon's peer state: its identity, the links it dials, and what the peer
// listeners report. It holds no goroutine until Start, and no file is touched until Init,
// which the daemon calls once it holds the lock (the identity has one writer).
type Host struct {
	cfg   Config
	state string
	api   string
	log   io.Writer

	uploads *Uploads
	pending *Pending
	fg      foregrounds

	// recMu serialises Reconcile: the periodic pass and a share's must not interleave.
	recMu sync.Mutex
	// editMu serialises the edits of the policy this daemon's own commands make (Edit).
	editMu sync.Mutex

	mu         sync.Mutex
	identity   *Identity
	problems   []store.Problem
	listening  []string       // the addresses the listeners are bound to, in the order wanted
	listenErrs []ListenError  // why an address wanted is not, by address
	open       map[string]int // peer name → open sockets
	links      map[string]*Link
	// remotesRead is whether the last pass read the remotes' configuration: until one has, a
	// remote with no link may only be one not read yet.
	remotesRead bool
	ctx         context.Context // the links' parent; nil before Start
	cancel      context.CancelFunc
	stopped     bool
	wg          sync.WaitGroup

	binder      Binder
	sharesStore *store.Store // hosting/shares.json; nil before Init, and when there is none
	ifaces      Lister
	defaultPort int
	pendingPort int               // the port the listeners took, not yet written to hosting.port
	told        map[string]string // what a pass last logged under a key, so it logs a change and not a repeat
}

// New returns the peer state for the daemon whose state root is state and whose API
// version is api. log receives the link lines (daemon.log).
func New(cfg Config, state, api string, log io.Writer) *Host {
	if log == nil {
		log = io.Discard
	}
	return &Host{cfg: cfg, state: state, api: api, log: log, uploads: NewUploads(state, log), pending: NewPending(),
		open: map[string]int{}, links: map[string]*Link{}, ifaces: SystemInterfaces, defaultPort: DefaultPort, told: map[string]string{}}
}

// Uploads are the APKs paired peers uploaded to this daemon (ADR-0099 §3).
func (h *Host) Uploads() *Uploads { return h.uploads }

// Pending is the unused join codes of this daemon's shares (ADR-0107 §2).
func (h *Host) Pending() *Pending { return h.pending }

// CodePending reports whether any join code is unused: the peer listener lets a certificate
// that is not paired through a join handshake only while it is true.
func (h *Host) CodePending() bool { return h.pending.Any() }

// ConsumeCode uses up the secret of an unused code and names the device it was issued for.
// For a detached share's code the use is on disk before this returns (ADR-0108 §2): when it
// cannot be written the code is void all the same and ok is false, so the join is refused
// and a crash cannot make the code usable twice.
func (h *Host) ConsumeCode(secret [16]byte) (device string, ok bool) {
	device, ok, err := h.pending.Consume(secret)
	if err != nil {
		fmt.Fprintf(h.log, "droidyard: a join code was refused because its use could not be written to %s (%v); the code is void\n", SharesFile, err)
		return "", false
	}
	return device, ok
}

// Init sweeps the uploads an earlier daemon left and loads the identity, making it when
// absent (ADR-0098 §1). A daemon whose identity cannot be made runs without peers: the
// error is for its log.
func (h *Host) Init() error {
	// What an earlier daemon left of its peers' uploads is swept first (ADR-0091): whatever
	// the identity does, nothing refers to those files.
	if err := h.uploads.Sweep(); err != nil {
		fmt.Fprintf(h.log, "droidyard: the peers' upload directory could not be swept: %v\n", err)
	}
	// The unused codes of the detached shares an earlier daemon left (ADR-0108 §2, §7).
	h.initShares()
	id, problems, err := LoadIdentity(h.state)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.problems = problems
	if err != nil {
		return err
	}
	h.identity = id
	return nil
}

// Hosting is the hosting section of the policy as the file has it now: the pin of the peer
// listener and the lending (ADR-0098 §6).
func (h *Host) Hosting() (policy.HostingPolicy, *capability.Error) { return h.cfg.Hosting() }

// Certificate is the identity as the peer listener presents it; ok is false when the daemon
// has none.
func (h *Host) Certificate() (cert tls.Certificate, ok bool) {
	id := h.Identity()
	if id == nil {
		return tls.Certificate{}, false
	}
	return id.Certificate(), true
}

// Fingerprint is this daemon's fingerprint; empty when it has no identity.
func (h *Host) Fingerprint() string {
	if id := h.Identity(); id != nil {
		return id.Fingerprint()
	}
	return ""
}

// Pin is the check the peer listener makes of a certificate a peer presents: the
// certificate's fingerprint, and the name hosting.peers gives it now. ok is false for a
// fingerprint not paired, and for every one while the configuration cannot be used
// (ADR-0098 §3).
func (h *Host) Pin(cert *x509.Certificate) (name, fingerprint string, ok bool) {
	fingerprint = FingerprintOf(cert)
	cfg, err := h.cfg.Hosting()
	if err != nil {
		return "", fingerprint, false
	}
	for _, p := range cfg.Peers {
		if SamePin(p.Fingerprint, fingerprint) {
			return p.Name, fingerprint, true
		}
	}
	return "", fingerprint, false
}

// Identity is the daemon's identity; nil before Init, and when Init failed.
func (h *Host) Identity() *Identity {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.identity
}

// Problems are the state files Init found unusable and replaced or moved aside (the
// identity's, the join codes'), for system.health's stateProblems (ADR-0021 §Format).
func (h *Host) Problems() []store.Problem {
	h.mu.Lock()
	out := append([]store.Problem(nil), h.problems...)
	st := h.sharesStore
	h.mu.Unlock()
	if st != nil {
		out = append(out, st.Problems()...)
	}
	return out
}

// Start begins dialling the remotes of the configuration, one link each, and keeps the
// links as the configuration changes (Reconcile) until Stop. A configuration that cannot be
// used dials nothing, and system.health says why through its config. It does nothing without
// an identity.
func (h *Host) Start() {
	if h.Identity() == nil {
		return
	}
	h.mu.Lock()
	if h.cancel != nil {
		h.mu.Unlock()
		return
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.mu.Unlock()
	h.recMu.Lock()
	defer h.recMu.Unlock()
	h.reconcileLinks()
}

// Stop closes every link and waits for them. It is safe to call more than once.
func (h *Host) Stop() {
	// A share held for a connection does not outlive the daemon (ADR-0108 §3).
	h.EndForegroundAll(ShareStopping)
	h.mu.Lock()
	h.stopped = true
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
}

// Reconcile makes the peer machinery what the configuration and the interfaces say now
// (ADR-0109 §3): the listeners, when a Binder was given (UseListeners), and the links. The
// daemon calls it every 2 s and at once after a change it made itself. A configuration that
// cannot be used leaves everything as it is.
func (h *Host) Reconcile() {
	h.recMu.Lock()
	defer h.recMu.Unlock()
	h.reconcileListeners()
	h.reconcileLinks()
	// Codes of a device that is neither lent nor shared in the foreground are stale: `host
	// unlend` writes the configuration with no word to the daemon, and a later share of the
	// serial must not bring them back. The configuration is read under Pending's lock.
	// A write that fails here stays pending, and the retry below reports it.
	_ = h.pending.VoidWhere(h.unheld)
	// A write of the join codes' file that failed is tried again.
	if err := h.pending.Flush(); err != nil {
		h.once("shares", fmt.Sprintf("droidyard: %s could not be written: %v; it is tried again\n", SharesFile, err))
	} else {
		h.once("shares", "")
	}
}

// reconcileLinks starts the links of remotes that are new, closes those that are gone or
// whose fingerprint changed (a link is to one certificate), and updates the addresses of
// the rest in place, keeping their connection. It does nothing before Start and after Stop.
func (h *Host) reconcileLinks() {
	id := h.Identity()
	h.mu.Lock()
	running := h.cancel != nil && !h.stopped
	h.mu.Unlock()
	if id == nil || !running {
		return
	}
	remotes, err := h.cfg.Remotes()
	if err != nil {
		h.once("remotes", fmt.Sprintf("droidyard: the remotes are left as they are: %s\n", err.Cause))
		h.mu.Lock()
		h.remotesRead = false
		h.mu.Unlock()
		return
	}
	h.once("remotes", "")
	h.mu.Lock()
	h.remotesRead = true
	h.mu.Unlock()
	want := make(map[string]policy.Remote, len(remotes))
	for _, r := range remotes {
		want[r.Name] = r
	}
	var gone []*Link
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	for name, l := range h.links {
		if r, ok := want[name]; !ok || !SamePin(r.Fingerprint, l.remote.Fingerprint) {
			gone = append(gone, l)
			delete(h.links, name)
		}
	}
	for _, r := range remotes {
		if l := h.links[r.Name]; l != nil {
			if l.update(r) {
				fmt.Fprintf(h.log, "droidyard: remote %s: its addresses changed; the link is kept\n", r.Name)
			}
			continue
		}
		l := newLink(r, id, h.api, h.log)
		l.own = h.ownPrefixes
		lctx, stop := context.WithCancel(h.ctx)
		l.stop, l.done = stop, make(chan struct{})
		h.links[r.Name] = l
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer close(l.done)
			l.run(lctx)
		}()
	}
	h.mu.Unlock()
	for _, l := range gone {
		l.stop()
		<-l.done
		fmt.Fprintf(h.log, "droidyard: remote %s: no longer configured, or its fingerprint changed; the link is closed\n", l.remote.Name)
	}
}

// once logs line under key when it differs from the last one logged under it, so a pass
// that finds the same problem every 2 s says it once; an empty line forgets the key.
func (h *Host) once(key, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.told[key] == line {
		return
	}
	h.told[key] = line
	if line != "" {
		fmt.Fprint(h.log, line)
	}
}

// ownPrefixes are the subnets this machine is on now, for ordering a remote's addresses.
func (h *Host) ownPrefixes() []netip.Prefix {
	ifaces, err := h.Interfaces()
	if err != nil {
		return nil
	}
	return OwnPrefixes(ifaces)
}

// RemotesRead is whether the last pass read the remotes' configuration, so that Remote's nil
// means a remote that is not configured rather than one not read.
func (h *Host) RemotesRead() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.remotesRead
}

// Remote is the link to the named remote, nil when none is configured.
func (h *Host) Remote(name string) *Link {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.links[name]
}

// Links are the links to every remote of the configuration, in the order of their names.
func (h *Host) Links() []*Link {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Link, 0, len(h.links))
	for _, l := range h.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].remote.Name < out[j].remote.Name })
	return out
}

// SetListening is the peer listeners' report: the addresses they are bound to, in the order
// wanted, and why each address wanted that is not bound is not.
func (h *Host) SetListening(bound []string, errs map[string]error) {
	list := make([]ListenError, 0, len(errs))
	for addr, err := range errs {
		list = append(list, ListenError{Address: addr, Error: err.Error()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Address < list[j].Address })
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listening, h.listenErrs = append([]string(nil), bound...), list
}

// PeerOpened and PeerClosed count a paired peer's open sockets, so health can say which
// peers are connected.
func (h *Host) PeerOpened(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.open[name]++
}

func (h *Host) PeerClosed(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.open[name] > 0 {
		h.open[name]--
	}
}

// IdentityHealth is system.health's identity.
type IdentityHealth struct {
	Fingerprint string `json:"fingerprint" jsonschema:"required" jsonschema_description:"This daemon's certificate fingerprint, as droidyard id prints it: what a host pins to admit this daemon."`
}

// PeerHealth is one paired peer of this host.
type PeerHealth struct {
	Name      string `json:"name" jsonschema:"required"`
	Connected bool   `json:"connected" jsonschema:"required" jsonschema_description:"Whether the peer has a link open to this host now."`
}

// ListenError is why one address the peer listener was asked to bind is not bound.
type ListenError struct {
	Address string `json:"address" jsonschema:"required" jsonschema_description:"The address wanted, an IP literal and a port."`
	Error   string `json:"error" jsonschema:"required" jsonschema_description:"Why it is not bound; it is tried again at the next check, within seconds."`
}

// HostingHealth is system.health's hosting: the peer listeners and the peers paired.
type HostingHealth struct {
	Listen    string        `json:"listen,omitempty" jsonschema_description:"The first address a peer listener is bound to; absent when none is. listening has them all."`
	Listening []string      `json:"listening,omitempty" jsonschema_description:"Every address a peer listener is bound to, an IP literal and a port each."`
	Error     string        `json:"error,omitempty" jsonschema_description:"The first of errors: why an address wanted is not bound. It is tried again at the next check, within seconds."`
	Errors    []ListenError `json:"errors,omitempty" jsonschema_description:"Each address the peer listener was asked to bind and could not, with why."`
	Peers     []PeerHealth  `json:"peers" jsonschema:"required" jsonschema_description:"The peers in hosting.peers."`
}

// RemoteHealth is one remote this daemon is paired with.
type RemoteHealth struct {
	Name      string `json:"name" jsonschema:"required"`
	Connected bool   `json:"connected" jsonschema:"required" jsonschema_description:"Whether the link to the remote is up: its host accepted this daemon and speaks the peer protocol."`
	API       string `json:"api,omitempty" jsonschema_description:"The API version the remote's daemon reported; absent until the link has been up."`
	Error     string `json:"error,omitempty" jsonschema_description:"Why the link is down: the last failure to connect."`
}

// Health is what system.health reports of the peer link.
type Health struct {
	Identity *IdentityHealth
	Hosting  HostingHealth
	Remotes  []RemoteHealth
}

// Health reports the link as it stands. The peers are the configuration's now, each with
// whether it has a socket open; a configuration that cannot be used has none.
func (h *Host) Health() Health {
	cfg, _ := h.cfg.Hosting()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := Health{Hosting: HostingHealth{Peers: []PeerHealth{}}, Remotes: []RemoteHealth{}}
	if len(h.listening) > 0 {
		out.Hosting.Listen, out.Hosting.Listening = h.listening[0], append([]string(nil), h.listening...)
	}
	if len(h.listenErrs) > 0 {
		out.Hosting.Error, out.Hosting.Errors = h.listenErrs[0].Error, append([]ListenError(nil), h.listenErrs...)
	}
	if h.identity != nil {
		out.Identity = &IdentityHealth{Fingerprint: h.identity.Fingerprint()}
	}
	for _, p := range cfg.Peers {
		out.Hosting.Peers = append(out.Hosting.Peers, PeerHealth{Name: p.Name, Connected: h.open[p.Name] > 0})
	}
	for _, l := range h.links {
		out.Remotes = append(out.Remotes, l.health())
	}
	sort.Slice(out.Remotes, func(i, j int) bool { return out.Remotes[i].Name < out.Remotes[j].Name })
	return out
}

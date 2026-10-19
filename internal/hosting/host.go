package hosting

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
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
// listener reports. It holds no goroutine until Start, and no file is touched until Init,
// which the daemon calls once it holds the lock (the identity has one writer).
type Host struct {
	cfg   Config
	state string
	api   string
	log   io.Writer

	uploads *Uploads
	pending *Pending

	mu        sync.Mutex
	identity  *Identity
	problems  []store.Problem
	listen    string
	listenErr string
	open      map[string]int // peer name → open sockets
	links     map[string]*Link
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// New returns the peer state for the daemon whose state root is state and whose API
// version is api. log receives the link lines (daemon.log).
func New(cfg Config, state, api string, log io.Writer) *Host {
	if log == nil {
		log = io.Discard
	}
	return &Host{cfg: cfg, state: state, api: api, log: log, uploads: NewUploads(state, log), pending: NewPending(),
		open: map[string]int{}, links: map[string]*Link{}}
}

// Uploads are the APKs paired peers uploaded to this daemon (ADR-0099 §3).
func (h *Host) Uploads() *Uploads { return h.uploads }

// Pending is the unused join codes of this daemon's shares (ADR-0107 §2).
func (h *Host) Pending() *Pending { return h.pending }

// CodePending reports whether any join code is unused: the peer listener lets a certificate
// that is not paired through a join handshake only while it is true.
func (h *Host) CodePending() bool { return h.pending.Any() }

// ConsumeCode uses up the secret of an unused code and names the device it was issued for.
func (h *Host) ConsumeCode(secret [16]byte) (device string, ok bool) {
	return h.pending.Consume(secret)
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

// Problems are the identity files Init found unusable and replaced, for
// system.health's stateProblems (ADR-0021 §Format).
func (h *Host) Problems() []store.Problem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]store.Problem(nil), h.problems...)
}

// Start dials every remote of the configuration as it is now (ADR-0098 §4: remotes are
// read at start), one link each, keeping each connected until Stop. A configuration that
// cannot be used dials nothing, and system.health says why through its config. It does
// nothing without an identity.
func (h *Host) Start() {
	id := h.Identity()
	if id == nil {
		return
	}
	remotes, err := h.cfg.Remotes()
	if err != nil {
		fmt.Fprintf(h.log, "droidyard: no remote is dialled: %s\n", err.Cause)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	for _, r := range remotes {
		l := newLink(r, id, h.api, h.log)
		h.links[r.Name] = l
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			l.run(ctx)
		}()
	}
}

// Stop closes every link and waits for them. It is safe to call more than once.
func (h *Host) Stop() {
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
}

// Remote is the link to the named remote, nil when none is configured.
func (h *Host) Remote(name string) *Link {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.links[name]
}

// Links are the links to every remote of the configuration read at start, in the order of
// their names.
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

// SetListen is the peer listener's report: the address it is bound to, or why it is not.
// Either clears the other.
func (h *Host) SetListen(addr string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listen, h.listenErr = addr, ""
	if err != nil {
		h.listen, h.listenErr = "", err.Error()
	}
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

// HostingHealth is system.health's hosting: the peer listener and the peers paired.
type HostingHealth struct {
	Listen string       `json:"listen,omitempty" jsonschema_description:"The address the peer listener is bound to; absent when it is not."`
	Error  string       `json:"error,omitempty" jsonschema_description:"Why hosting.listen is set and the listener is not bound; it is tried again every 30 s."`
	Peers  []PeerHealth `json:"peers" jsonschema:"required" jsonschema_description:"The peers in hosting.peers."`
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
	out := Health{Hosting: HostingHealth{Listen: h.listen, Error: h.listenErr, Peers: []PeerHealth{}}, Remotes: []RemoteHealth{}}
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

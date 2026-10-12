// Package workspace is the workspace model: which project roots are open, their
// lifecycle, and their durable configuration (docs/architecture/workspace-state.md;
// ADR-0020 §Scope and workspace addressing; ADR-0021 §Layout; ADR-0026; ADR-0027).
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
)

// The workspace codes (ADR-0026).
const (
	ErrRootInvalid     capability.ErrorCode = "WORKSPACE_ROOT_INVALID"
	ErrStateNewer      capability.ErrorCode = "WORKSPACE_STATE_NEWER"
	ErrStateUnreadable                      = capability.ErrWorkspaceStateUnreadable
)

// State is where a workspace stands (workspace-state.md §Lifecycle). `created` is not
// held: a workspace is registered by the open that validates it.
type State string

const (
	Opening  State = "opening"
	Ready    State = "ready"
	Degraded State = "degraded"
	Closing  State = "closing"
	Closed   State = "closed"
)

// DefaultDrain is how long a close waits for the workspace's cancelled operations
// before it stops its sidecars and completes: the working default of ADR-0027, to move
// to the configuration file in Phase 1.
const DefaultDrain = 10 * time.Second

// Info is the workspace object that workspace.open and workspace.describe return
// (plan m1c, R6; ADR-0026 §Consequences).
type Info struct {
	ID      string `json:"id" jsonschema:"required"`
	Name    string `json:"name" jsonschema:"required"`
	Root    string `json:"root" jsonschema:"required"`
	State   State  `json:"state" jsonschema:"required,enum=ready,enum=degraded"`
	Trusted bool   `json:"trusted" jsonschema:"required"`
	// CreatedAt is when the workspace was first opened, from workspace.json. Absent
	// while that file is of a newer version and unread: the time is not known.
	CreatedAt  *time.Time        `json:"createdAt,omitempty"`
	DegradedBy *capability.Error `json:"degradedBy,omitempty"`
}

// config is ws/<wsid>/workspace.json (plan m1c, R8).
type config struct {
	Root      string    `json:"root"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// index is workspaces.json: the workspaces this state root has known, for id
// resolution (plan m1c, R8).
type index struct {
	Workspaces map[string]indexEntry `json:"workspaces"`
}

type indexEntry struct {
	Root         string    `json:"root"`
	Name         string    `json:"name"`
	LastOpenedAt time.Time `json:"lastOpenedAt"`
}

const indexFile = "workspaces.json"

func configFile(id string) string { return path.Join("ws", id, "workspace.json") }

// Options configure a Registry.
type Options struct {
	// Store is the state under the state root. Open fails without one.
	Store *store.Store
	// Ops is the operation store whose workspace-scoped calls the registry admits,
	// and whose operations a close cancels.
	Ops *capability.Operations
	// Policy decides which project roots are trusted (internal/policy; ADR-0015 §5).
	// Nil trusts nothing.
	Policy Policy
	// Env canonicalises roots; zero means platform.Current().
	Env platform.Env
	// Drain bounds a close's wait for its operations; zero means DefaultDrain.
	Drain time.Duration
	// StopSidecars stops the workspace's sidecars, after the drain and before the
	// workspace is closed. Nil until the supervisor exists (step 7).
	StopSidecars func(id string)
	// StopPollers asks the workspace's live file.watch and git.watch pollers to end,
	// each publishing its stopped event (plan m1j, D9). It runs after the drain, beside
	// StopSidecars, and must not block: it signals the pollers and returns. Nil until
	// file.* is registered.
	StopPollers func(id string)
	// CloseRoot releases the resources that belong to the workspace's project root,
	// after the drain: the Gradle sidecar closes its connection to that root so
	// Gradle's daemon connection is released rather than left to the sidecar's idle
	// timeout (gradle.md §Protocol; plan m1g step 5). It is called with the
	// workspace's canonical root and must not block for long.
	CloseRoot func(root string)
	// OnOpen runs once when a workspace is opened — not when an already-open one is
	// asked for again — with its id and canonical root. It is the build registry's
	// sweep of console files no record owns (plan m1g D13). It runs under the
	// workspace's transition lock, so it must not block for long.
	OnOpen func(id, root string)
	// Log receives a line per operation a close left running. Nil discards.
	Log io.Writer
	// Publisher receives every transition on workspace.lifecycle. Nil publishes
	// nothing.
	Publisher capability.Publisher
	// States receives the same transitions on workspace.state, ring-less, for the
	// workspace.watch subscriptions (ADR-0045 §3). Nil publishes nothing.
	States capability.StatePublisher
	// Dialog is the folder dialog workspace.chooseRoot raises; the zero value cannot
	// run one (ADR-0045 §4).
	Dialog Dialog
}

// Policy decides which project roots are trusted: trusted if equal to a configured
// root or under a configured prefix (ADR-0015 §5; internal/policy). It is read at
// every report, not once, so a `droidyard trust` takes effect without a restart.
type Policy interface {
	Trusted(root string) (bool, *capability.Error)
}

// LifecycleTopic is where workspace transitions are published (workspace-state.md
// §Observability): the event's type is the new state, its workspace the id, and its
// payload a Lifecycle (plan m1d, D4).
const LifecycleTopic = "workspace.lifecycle"

// Lifecycle is a workspace.lifecycle event's payload.
type Lifecycle struct {
	State      State             `json:"state"`
	DegradedBy *capability.Error `json:"degradedBy,omitempty"`
}

// StateTopic is where workspace.watch's events are published (ADR-0045 §3): every
// transition of LifecycleTopic again, with the workspace's name, root and trust, so a
// projects panel needs no read per event. It is the stream of a capability, not a subscribable
// topic, and it keeps off the replay ring.
const StateTopic = "workspace.state"

// EventState is the type of a workspace.state event.
const EventState = "state"

// StateEvent is a workspace.state event's payload.
type StateEvent struct {
	State      State             `json:"state" jsonschema:"required,enum=opening,enum=ready,enum=degraded,enum=closing,enum=closed"`
	DegradedBy *capability.Error `json:"degradedBy,omitempty"`
	Name       string            `json:"name" jsonschema:"required"`
	Root       string            `json:"root" jsonschema:"required"`
	Trusted    bool              `json:"trusted" jsonschema:"required" jsonschema_description:"Whether the root is trusted when the transition happened; a trust granted afterwards publishes no event."`
}

type workspace struct {
	// tx serialises open and close of this workspace across the whole transition
	// (ADR-0027).
	tx sync.Mutex

	// Written under Registry.mu; read without it.
	state atomic.Value // State
	// Guarded by Registry.mu.
	info Info
}

// Registry is every workspace this daemon has opened in its life, and the index of the
// ones the state root has known.
type Registry struct {
	opts Options
	now  func() time.Time

	// mu is the admission lock: an open check and the recording of the operation it
	// admits (Admit), and a close entering closing, all take it.
	mu sync.Mutex

	// pubMu makes a state change that Degrade or Recover make, and its publication, one
	// step, so two of them are published in the order they happened. They do not take
	// the workspace's tx: a close holds it while it waits for the very build that
	// reports the crash.
	pubMu sync.Mutex

	// allMu guards the map only, so a state can be read while mu is held. Entries are
	// never removed.
	allMu sync.RWMutex
	all   map[string]*workspace

	// watchHook is a test seam: see SetWatchHook.
	watchHook atomic.Pointer[func()]

	indexMu     sync.Mutex
	indexLoaded bool
	indexNewer  bool
	idx         index
}

// New returns an empty registry and makes it ops's admission control.
func New(opts Options) *Registry {
	if opts.Env.Getenv == nil {
		opts.Env = platform.Current()
	}
	if opts.Drain == 0 {
		opts.Drain = DefaultDrain
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	r := &Registry{opts: opts, now: time.Now, all: map[string]*workspace{}}
	if opts.Ops != nil {
		opts.Ops.UseWorkspaces(r)
	}
	return r
}

// Drain is the bound a close waits for its operations.
func (r *Registry) Drain() time.Duration { return r.opts.Drain }

// resolve turns a target into an id: one starting with "/" is a root, canonicalised;
// anything else is an id (plan m1c, R5).
func (r *Registry) resolve(target string) (id, root string) {
	if strings.HasPrefix(target, "/") {
		return r.opts.Env.WorkspaceID(target)
	}
	return target, ""
}

// Admit implements capability.Workspaces.
func (r *Registry) Admit(target string, record func(id string)) bool {
	id, _ := r.resolve(target)
	w := r.lookup(id)
	if w == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !w.info.State.open() {
		return false
	}
	record(id)
	return true
}

func (r *Registry) lookup(id string) *workspace {
	r.allMu.RLock()
	defer r.allMu.RUnlock()
	return r.all[id]
}

func (s State) open() bool { return s == Ready || s == Degraded }

// State is the workspace's state, read without the admission lock; "" for one this
// daemon has never opened.
func (r *Registry) State(target string) State {
	id, _ := r.resolve(target)
	if w := r.lookup(id); w != nil {
		return w.state.Load().(State)
	}
	return ""
}

// entry returns the workspace record for id, creating a closed one.
func (r *Registry) entry(id string) *workspace {
	r.allMu.Lock()
	defer r.allMu.Unlock()
	w := r.all[id]
	if w == nil {
		w = &workspace{info: Info{ID: id, State: Closed}}
		w.state.Store(Closed)
		r.all[id] = w
	}
	return w
}

// setState changes a workspace's state under the admission lock, and publishes it.
// name and root are the workspace's, which w.info does not hold until its first load.
func (r *Registry) setState(w *workspace, s State, name, root string) {
	r.mu.Lock()
	w.info.State = s
	w.state.Store(s)
	id := w.info.ID
	r.mu.Unlock()
	r.publish(id, s, nil, name, root)
}

// publish puts a transition on workspace.lifecycle and on workspace.state. Open and
// close transitions of one workspace are serialised by its tx, so they are published in
// the order they happen. Degrade and Recover bypass tx: pubMu orders their publishes
// among themselves and against any later publish (but not against Open's final one,
// which a build cannot overlap, since a build needs the workspace open).
func (r *Registry) publish(id string, s State, degradedBy *capability.Error, name, root string) {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.publishLocked(id, s, degradedBy, name, root)
}

func (r *Registry) publishLocked(id string, s State, degradedBy *capability.Error, name, root string) {
	if r.opts.Publisher != nil {
		r.opts.Publisher.Publish(LifecycleTopic, string(s), id, "", Lifecycle{State: s, DegradedBy: degradedBy})
	}
	if r.opts.States != nil {
		r.opts.States.Broadcast(StateTopic, EventState, id, "",
			StateEvent{State: s, DegradedBy: degradedBy, Name: name, Root: root, Trusted: r.trusted(root)})
	}
}

// Degrade marks the open workspace of root degraded by cause: the Gradle sidecar
// crashed while that workspace's build ran (workspace-state.md §Lifecycle; plan m2c,
// D21). Only a ready workspace degrades: one already degraded keeps the cause it has,
// and one that is not open is not touched. It publishes the transition.
func (r *Registry) Degrade(root string, cause *capability.Error) {
	id, _ := r.resolve(root)
	w := r.lookup(id)
	if w == nil || cause == nil {
		return
	}
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.mu.Lock()
	if w.info.State != Ready {
		r.mu.Unlock()
		return
	}
	w.info.State, w.info.DegradedBy = Degraded, cause
	w.state.Store(Degraded)
	name, wroot := w.info.Name, w.info.Root
	r.mu.Unlock()
	r.publishLocked(id, Degraded, cause, name, wroot)
}

// Recover returns every workspace degraded by code to ready and publishes each
// transition: the cause is gone. A workspace degraded by anything else stays degraded.
func (r *Registry) Recover(code capability.ErrorCode) {
	r.allMu.RLock()
	all := make([]*workspace, 0, len(r.all))
	for _, w := range r.all {
		all = append(all, w)
	}
	r.allMu.RUnlock()
	for _, w := range all {
		r.recoverOne(w, code)
	}
}

func (r *Registry) recoverOne(w *workspace, code capability.ErrorCode) {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.mu.Lock()
	if w.info.State != Degraded || w.info.DegradedBy == nil || w.info.DegradedBy.Code != code {
		r.mu.Unlock()
		return
	}
	w.info.State, w.info.DegradedBy = Ready, nil
	w.state.Store(Ready)
	id, name, root := w.info.ID, w.info.Name, w.info.Root
	r.mu.Unlock()
	r.publishLocked(id, Ready, nil, name, root)
}

func rootInvalid(target, cause string) *capability.Error {
	return capability.Errorf(ErrRootInvalid, "%q cannot be opened as a workspace", target).
		WithCause(cause).
		WithNextStep("call `workspace.open` with the absolute path of a readable project directory").
		WithDetails(map[string]any{"target": target})
}

// stateUnreadable is a failure to read or write droidyard's own state for the
// workspace: anything but a corrupt file or a newer version, which the store and load
// handle (ADR-0026, second amendment).
func stateUnreadable(target string, err error) *capability.Error {
	return capability.Errorf(ErrStateUnreadable, "the state of workspace %q could not be read or written", target).
		WithCause(err.Error()).
		WithNextStep("check the permissions and free space of the state directory").
		WithDetails(map[string]any{"target": target})
}

// Open opens the workspace target names, or returns its state if it is open
// (workspace-state.md §Lifecycle). It runs no project code and spawns nothing
// (ADR-0015 §5). A target that is not an existing, readable directory, or an id no
// index holds, fails with WORKSPACE_ROOT_INVALID, and state that cannot be read or
// written with WORKSPACE_STATE_UNREADABLE (ADR-0026). An open during a close
// waits for the workspace to be closed, then opens it (ADR-0027).
func (r *Registry) Open(target string) (Info, error) {
	if r.opts.Store == nil {
		return Info{}, errors.New("the workspace registry has no state store")
	}
	id, root := r.resolve(target)
	if root == "" {
		known, ok, err := r.knownRoot(id)
		if err != nil {
			return Info{}, stateUnreadable(target, err)
		}
		if !ok {
			return Info{}, rootInvalid(target, "no workspace index holds this id")
		}
		root = known
	}

	w := r.lookup(id)
	if w == nil {
		// Validated before it is registered, so a target that cannot be opened leaves
		// no entry behind. A known workspace is validated below, under its tx: an open
		// one is a no-op whatever its directory has become.
		if err := readableDir(root); err != nil {
			return Info{}, rootInvalid(target, err.Error())
		}
		w = r.entry(id)
	}
	w.tx.Lock()
	defer w.tx.Unlock()
	r.mu.Lock()
	if w.info.State.open() {
		info := w.info
		r.mu.Unlock()
		// Trust is read afresh: `droidyard trust` after the open must show here
		// (ADR-0015 §6), and reading the configuration does I/O we keep out of the
		// admission lock.
		info.Trusted = r.trusted(info.Root)
		// Opening again is still opening it: the projects panel lists the newest first
		// (ADR-0045 §3). A failed write does not fail an open that is otherwise a no-op.
		if err := r.recordOpen(id, info.Root, info.Name); err != nil {
			fmt.Fprintf(r.opts.Log, "droidyard: workspace %s: recording its reopen in %s: %v\n", id, indexFile, err)
		}
		return info, nil
	}
	r.mu.Unlock()

	if err := readableDir(root); err != nil {
		return Info{}, rootInvalid(target, err.Error())
	}
	name := filepath.Base(root)
	r.setState(w, Opening, name, root)
	info, err := r.load(id, root)
	if err != nil {
		r.setState(w, Closed, name, root)
		return Info{}, stateUnreadable(target, err)
	}
	r.mu.Lock()
	w.info = info
	w.state.Store(info.State)
	r.mu.Unlock()
	r.publish(id, info.State, info.DegradedBy, info.Name, info.Root)
	return info, nil
}

// readableDir checks that root is a directory this process can list.
func readableDir(root string) error {
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	d, err := os.Open(root)
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := d.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// load reads or creates ws/<id>/workspace.json and records the open in the index. A
// workspace.json of a newer version is left untouched and the workspace degrades
// (ADR-0026).
func (r *Registry) load(id, root string) (Info, error) {
	name := filepath.Base(root)
	info := Info{ID: id, Name: name, Root: root, State: Ready, Trusted: false}
	var cfg config
	out, err := r.opts.Store.Read(configFile(id), store.Version, &cfg)
	switch {
	case errors.Is(err, store.ErrNewer):
		info.State = Degraded
		info.DegradedBy = capability.Errorf(ErrStateNewer, "%s has a newer schemaVersion than this droidyard reads; it is left untouched", configFile(id)).
			WithNextStep(fmt.Sprintf("upgrade droidyard, or delete `%s/` to reset this workspace", filepath.Join(r.opts.Store.Root(), "ws", id)))
	case err != nil:
		return Info{}, err
	case out == store.Loaded:
		created := cfg.CreatedAt
		info.CreatedAt = &created
	default: // absent, or corrupt and moved aside
		cfg = config{Root: root, Name: name, CreatedAt: r.now().UTC()}
		if err := r.opts.Store.Write(configFile(id), cfg); err != nil {
			return Info{}, err
		}
		created := cfg.CreatedAt
		info.CreatedAt = &created
	}
	if err := r.recordOpen(id, root, name); err != nil {
		return Info{}, err
	}
	if r.opts.OnOpen != nil {
		// The workspace is now this daemon's: a hook that belongs to the open (the
		// build registry sweeps console files no record owns) runs here, once, and not
		// for an already-open workspace asked for again.
		r.opts.OnOpen(id, root)
	}
	info.Trusted = r.trusted(root)
	return info, nil
}

// trusted reports whether root is trusted now. An unreadable configuration trusts
// nothing: the error it carries is the [gradle] capabilities' to report, not
// workspace's, whose open and describe gain no error code for it (plan m1g D3).
func (r *Registry) trusted(root string) bool {
	if r.opts.Policy == nil {
		return false
	}
	ok, _ := r.opts.Policy.Trusted(root)
	return ok
}

// loadIndex reads workspaces.json once. One of a newer version is neither read nor
// written (plan m1c, R16). Called with indexMu held.
func (r *Registry) loadIndex() error {
	if r.indexLoaded {
		return nil
	}
	r.idx = index{Workspaces: map[string]indexEntry{}}
	var idx index
	out, err := r.opts.Store.Read(indexFile, store.Version, &idx)
	switch {
	case errors.Is(err, store.ErrNewer):
		r.indexNewer = true
	case err != nil:
		return err
	case out == store.Loaded && idx.Workspaces != nil:
		r.idx = idx
	}
	r.indexLoaded = true
	return nil
}

// knownRoot is the root of the workspace id: one this daemon has opened, else one the
// index holds.
func (r *Registry) knownRoot(id string) (string, bool, error) {
	if w := r.lookup(id); w != nil {
		r.mu.Lock()
		root := w.info.Root
		r.mu.Unlock()
		if root != "" {
			return root, true, nil
		}
	}
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if err := r.loadIndex(); err != nil {
		return "", false, err
	}
	e, ok := r.idx.Workspaces[id]
	return e.Root, ok && e.Root != "", nil
}

func (r *Registry) recordOpen(id, root, name string) error {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if err := r.loadIndex(); err != nil {
		return err
	}
	r.idx.Workspaces[id] = indexEntry{Root: root, Name: name, LastOpenedAt: r.now().UTC()}
	if r.indexNewer {
		// Kept for this daemon's life only, so workspace.watch lists what it opened:
		// a file of a newer version is neither read nor written.
		return nil
	}
	return r.opts.Store.Write(indexFile, r.idx)
}

// Describe is the state of an open workspace. Its trusted flag is read from the
// configuration each time, so a `droidyard trust` after the open shows without a
// restart (ADR-0015 §6; plan m1g D3).
func (r *Registry) Describe(id string) (Info, error) {
	w := r.lookup(id)
	if w == nil {
		return Info{}, notOpen(id)
	}
	r.mu.Lock()
	if !w.info.State.open() {
		r.mu.Unlock()
		return Info{}, notOpen(id)
	}
	info := w.info
	r.mu.Unlock()
	info.Trusted = r.trusted(info.Root)
	return info, nil
}

func notOpen(id string) *capability.Error {
	return capability.Errorf(capability.ErrWorkspaceNotOpen, "workspace %q is not open", id).
		WithNextStep("call workspace.open on it, then retry")
}

// Close closes the workspace target names for every session and returns once it is
// closed (ADR-0027): it enters closing, which rejects every other workspace-scoped
// call at Start; cancels the workspace's operations and waits for them up to the
// drain bound, logging any still running, which end cancelled when their handlers
// return; stops the workspace's sidecars; and marks it closed. It is not cancellable.
// A close of a workspace that is not open, or a second close, completes as a no-op.
func (r *Registry) Close(target string) error {
	id, _ := r.resolve(target)
	w := r.lookup(id)
	if w == nil {
		return nil
	}
	w.tx.Lock()
	defer w.tx.Unlock()

	r.mu.Lock()
	if !w.info.State.open() {
		r.mu.Unlock()
		return nil
	}
	root, name := w.info.Root, w.info.Name
	w.info.State = Closing
	w.state.Store(Closing)
	r.mu.Unlock()
	r.publish(id, Closing, nil, name, root)

	if r.opts.Ops != nil {
		ids := r.opts.Ops.CancelWorkspace(id)
		deadline := r.now().Add(r.opts.Drain)
		for _, op := range ids {
			out, err := r.opts.Ops.Wait(context.Background(), op, deadline)
			if err == nil && !out.State.Terminal() {
				fmt.Fprintf(r.opts.Log, "droidyard: workspace %s: operation %s (%s) still running %s after its close; left to end cancelled\n",
					id, op, out.Capability, r.opts.Drain)
			}
		}
	}
	if r.opts.CloseRoot != nil && root != "" {
		r.opts.CloseRoot(root)
	}
	if r.opts.StopSidecars != nil {
		r.opts.StopSidecars(id)
	}
	// The workspace's file.watch and git.watch pollers end here, each publishing its stopped event as
	// its own last act; nothing follows it (plan m1j, D9).
	if r.opts.StopPollers != nil {
		r.opts.StopPollers(id)
	}
	r.setState(w, Closed, name, root)
	return nil
}

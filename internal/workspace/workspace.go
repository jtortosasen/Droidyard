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
	"sort"
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
	// MainRoot is the main working tree of the repository when Root is one of its linked
	// worktrees (ADR-0082 §6; platform.LinkedWorktree), absolute and canonical; absent for
	// any other root. On the wire it is resolved at every open, describe and event;
	// Registry.Describe returns the open's (its callers use only Root).
	MainRoot string `json:"mainRoot,omitempty" jsonschema_description:"The project's main tree when root is a linked git worktree of it, absolute; absent otherwise. A worktree is a workspace of its project, trusted when that tree is."`
	// CreatedAt is when the workspace was first opened, from workspace.json. Absent
	// while that file is of a newer version and unread: the time is not known.
	CreatedAt  *time.Time        `json:"createdAt,omitempty"`
	DegradedBy *capability.Error `json:"degradedBy,omitempty"`
	// Gradle is where the project's Gradle build is (ADR-0075 §5). Filled by the open and
	// describe handlers, not by the registry: Registry.Describe, which every root lookup
	// calls, resolves nothing.
	Gradle GradleInfo `json:"gradle" jsonschema:"required"`
}

// Described is workspace.describe's result: the workspace object, and the project's
// linked worktrees (ADR-0089). Only describe carries them, so open, enter and the
// panel's streams, which share Info, advertise and read nothing more.
type Described struct {
	Info
	// Worktrees is filled by the describe handler only (Registry.worktreesOf).
	Worktrees []WorktreeInfo `json:"worktrees,omitempty" jsonschema_description:"The linked git worktrees of the project's repository, sorted by root, at most 50, the same whether root is the main tree or one of them. Each is read from the repository's own files and kept only while it names the repository back, so a removed or moved worktree is absent, as is one whose path holds a control or format character. Absent when the project has none, is not a git repository, or the repository has more than 100 worktree entries."`
}

// WorktreeInfo is one entry of Described.Worktrees.
type WorktreeInfo struct {
	Root   string `json:"root" jsonschema:"required" jsonschema_description:"The worktree's root, absolute and canonical; a path workspace.enter accepts."`
	Branch string `json:"branch,omitempty" jsonschema_description:"The branch checked out in it; absent when its HEAD is detached or names a branch with characters git does not write in one."`
}

// GradleInfo is the Gradle root as workspace.open and workspace.describe report it
// (ADR-0075 §5), resolved again at every call (ResolveGradleRoot).
type GradleInfo struct {
	// Root is the Gradle root relative to the project root, `/`-separated; "." is the
	// project root itself. Absent when there is none.
	Root string `json:"root,omitempty" jsonschema_description:"The Gradle root, relative to the project root (\".\" is the project root itself); absent when there is none."`
	// Candidates are the folders that hold a settings.gradle(.kts), relative to the
	// project root, sorted and at most 50: listed whether or not Root is set, so a choice
	// can be changed. Empty when none.
	Candidates []string `json:"candidates" jsonschema:"required" jsonschema_description:"The folders holding a settings.gradle(.kts), relative to the project root, sorted, at most 50; listed even when root is set."`
	// Error is PROJECT_NO_GRADLE_ROOT, present exactly when Root is absent: it says
	// whether there is none, several, or what is wrong with the project file's choice.
	Error *capability.Error `json:"error,omitempty" jsonschema_description:"PROJECT_NO_GRADLE_ROOT, present exactly when root is absent."`
}

// withGradle returns info with its Gradle root resolved against the project root. The
// open, describe and enter handlers call it; an info without a root (none is returned that way)
// gets an empty candidate list, never null.
func withGradle(info Info) Info {
	if info.Root == "" {
		info.Gradle = GradleInfo{Candidates: []string{}}
		return info
	}
	g := ResolveGradleRoot(info.Root, true)
	info.Gradle = GradleInfo{Root: g.Rel, Candidates: g.Candidates, Error: g.Err}
	return info
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
	Root string `json:"root"`
	Name string `json:"name"`
	// MainRoot is the main tree when Root was a linked worktree at its last open, written
	// at every open (ADR-0082 §6): an additive key, so the index keeps its version.
	MainRoot     string    `json:"mainRoot,omitempty"`
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
	// CloseRoot releases the resources that belong to a Gradle root, after the drain: the
	// Gradle sidecar closes its connection to that root so Gradle's daemon connection is
	// released rather than left to the sidecar's idle timeout (gradle.md §Protocol; plan
	// m1g step 5). It is called, outside mu and pubMu, once for each canonical Gradle root
	// the workspace's calls resolved (NoteGradleRoot) that no other open workspace
	// resolved (ADR-0075 §8), and must not block for long.
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
	MainRoot   string            `json:"mainRoot,omitempty" jsonschema_description:"The project's main tree when root is a linked git worktree of it, absolute; absent otherwise."`
}

type workspace struct {
	// tx serialises open and close of this workspace across the whole transition
	// (ADR-0027).
	tx sync.Mutex

	// Written under Registry.mu; read without it.
	state atomic.Value // State
	// Guarded by Registry.mu.
	info Info
	// gradleRoots are the canonical Gradle roots this workspace's calls resolved while it was
	// open (ADR-0075 §8), recorded by NoteGradleRoot. Guarded by Registry.mu.
	gradleRoots map[string]struct{}
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
			StateEvent{State: s, DegradedBy: degradedBy, Name: name, Root: root, Trusted: r.trusted(root), MainRoot: r.mainRoot(root)})
	}
}

// NoteGradleRoot records that a call of the workspace target names resolved the Gradle
// root abs, canonical (ADR-0075 §8), so a sidecar crash on that root degrades the
// workspace and a close releases the connection. It is a no-op unless the workspace is
// ready or degraded: an operation that outlives a close's drain cannot leave a root on a
// workspace that is closing or closed. The set is emptied when the workspace closes and
// when a closed one opens, never by an Open of an open one. It returns false when it
// recorded nothing because the workspace is not open, and the caller then refuses the
// call: a connection it opened would be recorded nowhere and outlive the close.
func (r *Registry) NoteGradleRoot(target, abs string) error {
	id, _ := r.resolve(target)
	w := r.lookup(id)
	if w == nil {
		return notOpen(id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !w.info.State.open() {
		return notOpen(id)
	}
	if abs == "" {
		return nil
	}
	if w.gradleRoots == nil {
		w.gradleRoots = map[string]struct{}{}
	}
	w.gradleRoots[abs] = struct{}{}
	return nil
}

// Degrade marks every open workspace whose calls resolved the Gradle root root degraded
// by cause: the Gradle sidecar crashed while a build of that root ran (workspace-state.md
// §Lifecycle; plan m2c, D21; ADR-0075 §8). An ordinary project records its own root, so
// it is one of these. Only a ready workspace degrades: one already degraded keeps the
// cause it has, and one that is not open is not touched. It publishes each transition.
func (r *Registry) Degrade(root string, cause *capability.Error) {
	if cause == nil {
		return
	}
	r.allMu.RLock()
	all := make([]*workspace, 0, len(r.all))
	for _, w := range r.all {
		all = append(all, w)
	}
	r.allMu.RUnlock()
	for _, w := range all {
		r.degradeOne(w, root, cause)
	}
}

func (r *Registry) degradeOne(w *workspace, root string, cause *capability.Error) {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.mu.Lock()
	if _, ok := w.gradleRoots[root]; !ok || w.info.State != Ready {
		r.mu.Unlock()
		return
	}
	w.info.State, w.info.DegradedBy = Degraded, cause
	w.state.Store(Degraded)
	id, name, wroot := w.info.ID, w.info.Name, w.info.Root
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
		info.MainRoot = r.mainRoot(info.Root)
		// Opening again is still opening it: the projects panel lists the newest first
		// (ADR-0045 §3). A failed write does not fail an open that is otherwise a no-op.
		if err := r.recordOpen(id, info.Root, info.Name, info.MainRoot); err != nil {
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
	// A closed workspace opens with no Gradle root: what a call noted while it was closing
	// or closed is gone, and NoteGradleRoot noted nothing in the states between.
	w.gradleRoots = nil
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
	info := Info{ID: id, Name: name, Root: root, State: Ready, Trusted: false, MainRoot: r.mainRoot(root)}
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
	if err := r.recordOpen(id, root, name, info.MainRoot); err != nil {
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

// mainRoot is the main tree of the repository when root is a linked worktree of it, else
// "" (platform.LinkedWorktree; ADR-0082 §6). It reads a few small files and runs no git.
func (r *Registry) mainRoot(root string) string {
	if root == "" {
		return ""
	}
	main, _ := platform.LinkedWorktree(r.opts.Env, root)
	return main
}

// worktreesOf lists the linked worktrees of the project info describes (ADR-0089): of
// its main tree when its root is a worktree, else of its root when that holds a `.git`
// directory. It reads a few small files and runs no git; a project that is no
// repository, or has no linked worktree, has none (nil).
func (r *Registry) worktreesOf(info Info) []WorktreeInfo {
	main := info.MainRoot
	if main == "" {
		main = info.Root
	}
	if main == "" {
		return nil
	}
	var out []WorktreeInfo
	for _, w := range platform.Worktrees(r.opts.Env, main) {
		out = append(out, WorktreeInfo{Root: w.Root, Branch: w.Branch})
	}
	return out
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

func (r *Registry) recordOpen(id, root, name, mainRoot string) error {
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if err := r.loadIndex(); err != nil {
		return err
	}
	r.idx.Workspaces[id] = indexEntry{Root: root, Name: name, MainRoot: mainRoot, LastOpenedAt: r.now().UTC()}
	if r.indexNewer {
		// Kept for this daemon's life only, so workspace.watch lists what it opened:
		// a file of a newer version is neither read nor written.
		return nil
	}
	return r.opts.Store.Write(indexFile, r.idx)
}

// Describe is the state of an open workspace. Its MainRoot is the one resolved at the
// open: Describe is called by every root lookup, so the workspace.describe handler
// resolves it again and this does not. Its trusted flag is read from the
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

// releasableRoots returns, sorted, the roots of mine (the Gradle roots a closing
// workspace w recorded, taken from it as it entered closing) that no open workspace
// records now (ADR-0075 §8). Called with r.mu held after the drain, so a workspace that
// resolved one of them during the drain keeps its connection; two workspaces closing
// together on a shared root may both release it, and a second close of a root is a no-op.
func (r *Registry) releasableRoots(w *workspace, mine map[string]struct{}) []string {
	if len(mine) == 0 {
		return nil
	}
	r.allMu.RLock()
	defer r.allMu.RUnlock()
	var out []string
	for g := range mine {
		shared := false
		for _, o := range r.all {
			if o == w || !o.info.State.open() {
				continue
			}
			if _, ok := o.gradleRoots[g]; ok {
				shared = true
				break
			}
		}
		if !shared {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
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
	mine := w.gradleRoots
	w.gradleRoots = nil
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
	// Which roots no open workspace uses is decided after the drain, so one that another
	// workspace resolved meanwhile is kept. CloseRoot runs outside mu and pubMu (w.tx is
	// held): the Gradle client waits up to 5 s for a root's lock, while a crashing build's
	// path takes the admission lock through Degrade.
	r.mu.Lock()
	release := r.releasableRoots(w, mine)
	r.mu.Unlock()
	if r.opts.CloseRoot != nil {
		for _, g := range release {
			r.opts.CloseRoot(g)
		}
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

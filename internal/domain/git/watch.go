package git

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// TopicGitChanges is the topic git.watch's stream lands on (ADR-0066 §1): type status
// with the whole status when it differs from the last one sent, type stopped once when
// the watch ends on its own. Like file.changes it is not in events.subscribe's enum and
// takes no `since`: its events take a seq but never enter the replay ring, so a watch
// that was not running produced nothing to replay and the client watches again.
const TopicGitChanges = "git.changes"

// The stream's type names.
const (
	TypeStatus  = "status"
	TypeStopped = "stopped"
)

// The stopped event's reasons.
const (
	// ReasonWorkspaceClosed: the watch's workspace closed.
	ReasonWorkspaceClosed = "workspaceClosed"
	// ReasonFailed: a tick's status failed (git gone, the root no longer a work tree, a
	// filter driver that cannot be switched off on a root no longer trusted, a timeout):
	// the watch stops rather than report nothing while it cannot see.
	ReasonFailed = "failed"
)

// watchInterval is how long the poller waits between statuses (ADR-0066 §1): one second.
const watchInterval = time.Second

// watchBaselineHook, when non-nil, runs after the baseline status succeeded and the
// poller started, just before the handler's last context check. A test uses it to land
// a cancel in that window. Nil in production.
var watchBaselineHook func()

// WatchParams is git.watch's parameters.
type WatchParams struct {
	Base string `json:"base,omitempty" jsonschema:"enum=HEAD" jsonschema_description:"What the working tree is compared with. Only HEAD exists; absent means HEAD."`
}

// WatchResult is git.watch's result: the subscription, and the baseline status the
// first event is compared with, so a client builds its view from one call and the events.
type WatchResult struct {
	SubscriptionID string  `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the status arrives on, over the session that made the call. The events carry the status, which is this result's fields besides subscriptionId and base, on topic git.changes, at most one per second and only when it differs from the last one sent."`
	Base           string  `json:"base" jsonschema:"required,enum=HEAD" jsonschema_description:"What the status is against."`
	Head           Head    `json:"head" jsonschema:"required" jsonschema_description:"What HEAD points at."`
	Entries        []Entry `json:"entries" jsonschema:"required" jsonschema_description:"The paths that differ from HEAD, sorted by path; at most 5000. Empty in a result returned again after delivery."`
	Truncated      bool    `json:"truncated" jsonschema:"required" jsonschema_description:"True when more than 5000 paths differ, or a git's output (the project's or a submodule's) passed its 4 MiB cap, and the rest are left out."`

	SubmodulesUnread bool `json:"submodulesUnread,omitempty" jsonschema_description:"True when some submodule could not be read and no entry says which: the project's submodules could not be listed, or the per-tick count or time bound was reached. Absent otherwise."`
}

// StoppedPayload is the git.changes event's payload for type stopped.
type StoppedPayload struct {
	Reason string `json:"reason" jsonschema:"required,enum=workspaceClosed,enum=failed" jsonschema_description:"workspaceClosed: the workspace was closed. failed: git could not report the status (it is gone, the project is no longer in a repository, or the root's filters can no longer be switched off); watch again, or trust the root."`
}

// watcher is one git.watch: the subscription its events are delivered to, the status
// its next one is compared with, and the goroutine that asks git every tick.
type watcher struct {
	opts      Options
	watchers  *Watchers
	bus       *event.Bus
	sub       *event.Subscription
	root      string
	workspace string
	operation string
	tick      time.Duration

	// ctx ends the status in flight when the watch is told to stop, so a stop never
	// waits for git (the whole git group is killed with it).
	ctx    context.Context
	cancel context.CancelFunc

	last Status

	// stop carries the reason a stopped event reports, "" for a subscription end that
	// publishes nothing. It is buffered and filled at most once, so requestStop never
	// blocks, however it races the poller's own end.
	stop     chan string
	stopOnce sync.Once
}

func newWatcher(o Options, ws *Watchers, bus *event.Bus, root string, call *capability.Call, tick time.Duration) *watcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &watcher{
		opts: o, watchers: ws, bus: bus, root: root,
		workspace: call.Workspace, operation: call.OperationID, tick: tick,
		ctx: ctx, cancel: cancel, stop: make(chan string, 1),
	}
}

// requestStop asks the poller to end, with the reason a stopped event carries ("" for a
// subscription end, which publishes nothing). The first request wins. It signals and
// cancels the status in flight and never waits, so it is safe under the bus's lock.
func (w *watcher) requestStop(reason string) {
	w.stopOnce.Do(func() {
		w.stop <- reason
		w.cancel()
	})
}

// run asks git every tick until stopped, then ends. A workspace close's stop publishes
// the stopped event as its last act, so nothing follows it; a subscription end stops
// silently. A tick that fails publishes stopped {failed} and ends.
func (w *watcher) run() {
	defer w.cancel()
	defer w.watchers.remove(w)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case reason := <-w.stop:
			w.finish(reason)
			return
		case <-t.C:
			// A stop that raced the tick wins: no status is started after it.
			select {
			case reason := <-w.stop:
				w.finish(reason)
				return
			default:
			}
			if !w.tickOnce() {
				return
			}
		}
	}
}

func (w *watcher) finish(reason string) {
	if reason != "" {
		w.bus.PublishTo(w.sub, TopicGitChanges, TypeStopped, w.workspace, w.operation, StoppedPayload{Reason: reason})
	}
}

// tickOnce reads the status and publishes it when it differs from the last one sent. The
// runner, the trust and the filter drivers are read afresh, so git installed or removed,
// trust revoked and a driver added mid-watch are all seen at the next tick (ADR-0015
// §2). A tick with no difference publishes nothing. It reports whether the watch goes on.
func (w *watcher) tickOnce() bool {
	st, err := w.read(w.ctx)
	if err != nil {
		if w.ctx.Err() != nil {
			// Told to stop while git ran; the stop request holds the reason.
			select {
			case reason := <-w.stop:
				w.finish(reason)
			default:
			}
			return false
		}
		w.bus.PublishTo(w.sub, TopicGitChanges, TypeStopped, w.workspace, w.operation, StoppedPayload{Reason: ReasonFailed})
		return false
	}
	if reflect.DeepEqual(st, w.last) {
		return true
	}
	w.last = st
	w.bus.PublishTo(w.sub, TopicGitChanges, TypeStatus, w.workspace, w.operation, st)
	return true
}

// read is one status of the watch's root with everything looked up afresh.
func (w *watcher) read(ctx context.Context) (Status, error) {
	return w.opts.statusOf(ctx, w.root)
}

// statusOf is one status of root: git looked for, trust read, filter drivers read (for
// an untrusted root), all at this call. Its errors are the runner's and status's, not yet
// mapped to the contract's codes.
func (o Options) statusOf(ctx context.Context, root string) (Status, error) {
	r, cerr := o.runner()
	if cerr != nil {
		return Status{}, ErrNotFound
	}
	return r.status(ctx, root, o.trusted(root))
}

// Watchers is every live git.watch poller, by workspace. Its Stop is joined into
// workspace.Options.StopPollers, so a workspace close ends that workspace's pollers;
// Live is the count a test asserts returns to zero.
type Watchers struct {
	mu   sync.Mutex
	byWS map[string]map[*watcher]struct{}
	live atomic.Int64
}

// NewWatchers returns an empty set.
func NewWatchers() *Watchers { return &Watchers{byWS: map[string]map[*watcher]struct{}{}} }

func (ws *Watchers) add(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	set := ws.byWS[w.workspace]
	if set == nil {
		set = map[*watcher]struct{}{}
		ws.byWS[w.workspace] = set
	}
	set[w] = struct{}{}
	ws.live.Add(1)
}

func (ws *Watchers) remove(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	set := ws.byWS[w.workspace]
	if _, ok := set[w]; !ok {
		return
	}
	delete(set, w)
	if len(set) == 0 {
		delete(ws.byWS, w.workspace)
	}
	ws.live.Add(-1)
}

// Stop asks every poller of the workspace to end, each publishing its stopped event, and
// returns without waiting: a status in progress must not block a close.
func (ws *Watchers) Stop(workspaceID string) {
	ws.mu.Lock()
	list := make([]*watcher, 0, len(ws.byWS[workspaceID]))
	for w := range ws.byWS[workspaceID] {
		list = append(list, w)
	}
	ws.mu.Unlock()
	for _, w := range list {
		w.requestStop(ReasonWorkspaceClosed)
	}
}

// Live is how many pollers are running: zero once every watch has ended.
func (ws *Watchers) Live() int { return int(ws.live.Load()) }

// registerWatch adds git.watch. A watch creates a subscription, so it is a command and
// not a query; it is streaming, so it needs a session that carries events (ADR-0010).
func registerWatch(reg *capability.Registry, opts Options) {
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "git.watch",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		Audience:     capability.AudienceHuman,
		Statefulness: capability.Streaming,
		Since:        version,
		Availability: []capability.Requirement{capability.RequiresGit},
		Description:  "Watches the project's working tree against HEAD for the calling session (WebSocket or CLI): the result is the status, and an event on topic git.changes carries the whole status whenever it differs from the last one sent, until the workspace closes or git can no longer report it. It asks git once a second and never takes the repository's index lock, so it cannot fail a commit made beside it.",
		Params:       WatchParams{},
		Result:       WatchResult{},
		// The stream: the client is generated from it (ADR-0041 §4).
		Events: &capability.Events{
			Topic: TopicGitChanges,
			Types: map[string]any{TypeStatus: Status{}, TypeStopped: StoppedPayload{}},
		},
		// A retained result drops the entries once delivered (ADR-0020 §Retention): a later
		// operation.wait returns the head and the flag, and the caller watches again for
		// a baseline.
		Slim: func(result any) any {
			if r, ok := result.(WatchResult); ok {
				r.Entries = []Entry{}
				return r
			}
			return result
		},
		// The codes the handler returns (ADR-0066 §4). WORKSPACE_NOT_OPEN and
		// SESSION_REQUIRED come from Start. WORKSPACE_UNTRUSTED is an untrusted root whose
		// git filter drivers cannot all be switched off (§3).
		Errors: []capability.ErrorCode{
			CodeNotFound, CodeNotARepository, CodeFailed,
			capability.ErrWorkspaceUntrusted, capability.ErrInvalidParams,
		},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session; changes no file and takes no repository lock",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return opts.watch(ctx, call)
		},
	})
}

// watch serves git.watch: it opens a subscription of the calling session, reads the
// status for its baseline, and starts a poller that publishes the status when it
// changes, on that subscription alone. It returns the id once the poller is running.
func (o Options) watch(ctx context.Context, call *capability.Call) (any, error) {
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	// The bus and the poller set are the daemon's; without them (the golden registry's
	// descriptors) a watch cannot run, and a call that reaches a handler without a
	// session still hears SESSION_REQUIRED rather than a nil dereference.
	if o.Bus == nil || o.Watchers == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "git.watch needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	p := call.Params.(WatchParams)
	if p.Base != "" && p.Base != BaseHEAD {
		return nil, capability.Errorf(capability.ErrInvalidParams, "base %q is not supported; only %s is", p.Base, BaseHEAD).
			WithNextStep("omit base, or pass HEAD")
	}
	root, cerr := o.root(call)
	if cerr != nil {
		return nil, cerr
	}
	if _, cerr := o.runner(); cerr != nil {
		return nil, cerr
	}
	tick := o.Tick
	if tick <= 0 {
		tick = watchInterval
	}
	w := newWatcher(o, o.Watchers, o.Bus, root, call, tick)
	// The subscription exists before the baseline reads, so a cancel during it has
	// something to clean up; its close hook ends the poller.
	id, sub, serr := sess.Open([]string{TopicGitChanges}, func() { w.requestStop("") })
	if serr != nil {
		w.cancel()
		return nil, serr
	}
	w.sub = sub

	st, err := w.read(ctx)
	if err != nil {
		sess.Unsubscribe(id)
		w.cancel()
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, mapError(root, err)
	}
	w.last = st
	o.Watchers.add(w)
	go w.run()
	if watchBaselineHook != nil {
		watchBaselineHook()
	}
	// The last act: a cancel that landed during the baseline ends the subscription and
	// the poller, so nothing outlives a cancelled operation.
	if ctx.Err() != nil {
		w.requestStop("")
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return WatchResult{
		SubscriptionID: id,
		Base:           BaseHEAD,
		Head:           st.Head,
		Entries:        st.Entries,
		Truncated:      st.Truncated,

		SubmodulesUnread: st.SubmodulesUnread,
	}, nil
}

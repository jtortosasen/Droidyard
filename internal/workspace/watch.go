package workspace

import (
	"context"
	"sort"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// CapWatch is workspace.watch's name.
const CapWatch = "workspace.watch"

// MaxListed is how many workspaces workspace.watch's snapshot lists at most, the newest
// first (plan m2c D18: a working default).
const MaxListed = 50

// Listed is one workspace in workspace.watch's snapshot.
type Listed struct {
	ID           string    `json:"id" jsonschema:"required"`
	Name         string    `json:"name" jsonschema:"required"`
	Root         string    `json:"root" jsonschema:"required"`
	LastOpenedAt time.Time `json:"lastOpenedAt" jsonschema:"required" jsonschema_description:"When it was last opened, an open workspace asked for again included."`
	State        State     `json:"state" jsonschema:"required,enum=opening,enum=ready,enum=degraded,enum=closing,enum=closed" jsonschema_description:"closed when this daemon holds no open workspace for it."`
	Trusted      bool      `json:"trusted" jsonschema:"required"`
}

// WatchResult is workspace.watch's result (ADR-0045 §3).
type WatchResult struct {
	SubscriptionID string   `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the workspace.state events arrive on, over the WebSocket session that made the call. It follows every workspace, opened by any client, until events.unsubscribe or the session's end."`
	Workspaces     []Listed `json:"workspaces" jsonschema:"required" jsonschema_description:"The workspaces this state root has known, newest first, with the live state of those open now, taken after the subscription opened: merge it with the events by id."`
	Truncated      bool     `json:"truncated" jsonschema:"required" jsonschema_description:"True when more than 50 are known and the oldest were left out."`
}

// SetWatchHook makes every workspace.watch call run f after it has opened its
// subscription and before it takes the snapshot. Test-only: it is where a workspace
// can be opened inside the window ADR-0045 §3 closes. Nil removes it.
func (r *Registry) SetWatchHook(f func()) {
	if f == nil {
		r.watchHook.Store(nil)
		return
	}
	r.watchHook.Store(&f)
}

// watch is workspace.watch: the subscription first, the snapshot second, so a workspace
// that opens or closes between the two reaches the watcher as an event, and one that
// changed before is in the snapshot (ADR-0045 §3).
func (r *Registry) watch(ctx context.Context, call *capability.Call) (any, error) {
	sess, ok := call.Session.(capability.Follower)
	if !ok {
		return nil, capability.NewError(capability.ErrSessionRequired, "events need a session that can carry them").
			WithNextStep("subscribe over the WebSocket session, GET /v1/ws")
	}
	id, err := sess.Follow([]string{StateTopic})
	if err != nil {
		return nil, err
	}
	if hook := r.watchHook.Load(); hook != nil {
		(*hook)()
	}
	list, truncated, err := r.listed()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		sess.Unsubscribe(id)
		return nil, err
	}
	return WatchResult{SubscriptionID: id, Workspaces: list, Truncated: truncated}, nil
}

// listed is the index merged with the live states, newest first and at most MaxListed.
// A state the index cannot be read for is reported by the error; a newer index lists
// what this daemon opened.
func (r *Registry) listed() ([]Listed, bool, error) {
	r.indexMu.Lock()
	err := r.loadIndex()
	entries := make(map[string]indexEntry, len(r.idx.Workspaces))
	for id, e := range r.idx.Workspaces {
		entries[id] = e
	}
	r.indexMu.Unlock()
	if err != nil {
		return nil, false, stateUnreadable("workspaces.json", err)
	}
	out := make([]Listed, 0, len(entries))
	for id, e := range entries {
		if e.Root == "" {
			continue
		}
		state := r.State(id)
		if state == "" {
			state = Closed
		}
		out = append(out, Listed{ID: id, Name: e.Name, Root: e.Root, LastOpenedAt: e.LastOpenedAt, State: state, Trusted: r.trusted(e.Root)})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastOpenedAt.Equal(out[j].LastOpenedAt) {
			return out[i].LastOpenedAt.After(out[j].LastOpenedAt)
		}
		return out[i].ID < out[j].ID
	})
	truncated := len(out) > MaxListed
	if truncated {
		out = out[:MaxListed]
	}
	return out, truncated, nil
}

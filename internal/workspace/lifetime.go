package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The per-root lifetime (ADR-0137 §3; plan m3j step 5): a root is opened by the first
// call that names it and released after IdleAfter with no call and no running operation.
// The lock order in the package doc is what makes both safe at once.

// Enter implements capability.Workspaces. It admits a call of scope workspace, whose
// target is a project path (absolute: resolved by ResolveProject and canonicalised) or the
// id of a workspace this state root has known (resolved through the index). The workspace
// is opened if it is not open, by Open's path under w.tx, so an enter during a close or a
// release waits for it (ADR-0027), and then the call is admitted under r.mu, as Admit does.
//
// An admission that finds the workspace no longer open, because a release or a close
// decided in between, opens it once more and admits. Once is enough: every Open stamps the
// idle clock under r.mu, so a root the second Open returns was used an instant ago and no
// release judges it idle before the admission. A workspace still not open after that
// refuses the call (WORKSPACE_NOT_OPEN).
//
// A target the call cannot start on is refused here and records no operation:
// REQUEST_INVALID_PARAMS (not absolute and no known id, missing, not a directory, not
// readable), PROJECT_NO_GRADLE_ROOT, WORKSPACE_STATE_UNREADABLE.
func (r *Registry) Enter(target string, record func(id string)) error {
	root, err := r.enterRoot(target)
	if err != nil {
		return err
	}
	if h := r.resolvedHook.Load(); h != nil {
		(*h)(root)
	}
	var id string
	for attempt := 0; attempt < 2; attempt++ {
		info, err := r.open(root, false)
		if err != nil {
			var e *capability.Error
			if errors.As(err, &e) && e.Code == ErrRootInvalid {
				// The folder went between the resolution and the open: the same refusal as
				// a folder that was never there, not a code outside the Start refusals.
				return invalidProject(root, e.Cause)
			}
			return capability.AsError(err)
		}
		id = info.ID
		if h := r.admitHook.Load(); h != nil {
			(*h)(id)
		}
		w := r.lookup(id)
		r.mu.Lock()
		if w != nil && w.info.State.open() {
			w.lastUsed = r.now()
			record(id)
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
	}
	return notOpen(id)
}

// enterRoot is the root Enter opens for target: the resolved project of a path, the
// indexed root of an id. A root that is no longer a readable directory is refused whether
// it was named by path or by id, so a deleted project opens nothing.
func (r *Registry) enterRoot(target string) (string, error) {
	if strings.HasPrefix(target, "/") {
		root, err := ResolveProject(r.opts.Env, target)
		if err != nil {
			return "", err
		}
		return root, nil
	}
	if r.opts.Store == nil {
		return "", capability.NewError(ErrStateUnreadable, "the workspace registry has no state store")
	}
	root, ok, err := r.knownRoot(target)
	if err != nil {
		return "", stateUnreadable(target, err)
	}
	if !ok {
		return "", invalidProject(target, "is neither an absolute path nor the id of a project this daemon has known")
	}
	if err := readableDir(root); err != nil {
		return "", invalidProject(root, err.Error())
	}
	return root, nil
}

// Ended implements capability.Workspaces: an operation of workspace id has reached its
// terminal state, and the idle clock starts from now. The operation store prunes terminal
// operations after 15 minutes, so the registry keeps its own.
func (r *Registry) Ended(id string) {
	if w := r.lookup(id); w != nil {
		r.mu.Lock()
		w.lastUsed = r.now()
		r.mu.Unlock()
	}
	if h := r.endedHook.Load(); h != nil {
		(*h)(id)
	}
}

// RunIdle releases the workspaces that have been idle, once a minute, until ctx ends. A
// release in progress when ctx ends completes: a close is not cancellable (ADR-0027).
func (r *Registry) RunIdle(ctx context.Context) {
	t := time.NewTicker(r.idleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.releaseIdle(ctx)
		}
	}
}

// releaseIdle is one round of RunIdle: every open workspace that is idle is released.
func (r *Registry) releaseIdle(ctx context.Context) {
	r.allMu.RLock()
	all := make([]*workspace, 0, len(r.all))
	for _, w := range r.all {
		all = append(all, w)
	}
	r.allMu.RUnlock()
	for _, w := range all {
		if ctx.Err() != nil {
			return
		}
		r.releaseOne(w)
	}
}

// releaseOne releases w if it is open and idle: no non-terminal operation of it and
// nothing used it for longer than IdleAfter. It takes w.tx with TryLock, so a workspace in
// the middle of an open, a close or another release is skipped until the next round, and
// holds it from the decision to closed. The decision enters closing under r.mu, the lock
// an admission records its operation under, so an operation is either counted by ActiveIn
// or admitted after closing, and then waits on w.tx (Enter, through Open) and runs on the
// reopened root. The release cancels nothing: it is released only with no operation to
// cancel.
func (r *Registry) releaseOne(w *workspace) {
	if !w.tx.TryLock() {
		return
	}
	defer w.tx.Unlock()

	r.mu.Lock()
	if !w.info.State.open() || r.now().Sub(w.lastUsed) <= IdleAfter ||
		(r.opts.Ops != nil && r.opts.Ops.ActiveIn(w.info.ID) > 0) {
		r.mu.Unlock()
		return
	}
	id, idle := w.info.ID, r.now().Sub(w.lastUsed)
	name, root, mine := r.enterClosingLocked(w)
	r.mu.Unlock()
	if h := r.releaseHook.Load(); h != nil {
		(*h)(id)
	}
	fmt.Fprintf(r.opts.Log, "droidyard: workspace %s: released after %s with no call\n", id, idle.Round(time.Second))
	r.finishClose(w, name, root, mine)
}

// The registry is the operation store's workspaces.
var _ capability.Workspaces = (*Registry)(nil)

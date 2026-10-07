package file

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"os"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// TopicFileChanges is the topic file.watch's stream lands on (plan m1j, D8): type
// changes with a tick's differences, type stopped once when the watch's workspace
// closes. It is not in events.subscribe's enum and takes no `since`: a watcher that was
// not running produced nothing to replay, so after a reconnect the client watches again
// and re-lists. Its events take a seq but never enter the replay ring, which other
// subscribers resume from.
const TopicFileChanges = "file.changes"

// The stream's type names (D8).
const (
	TypeChanges = "changes"
	TypeStopped = "stopped"
)

// ReasonWorkspaceClosed is the stopped event's reason when the watch's workspace closed
// (D8, D9).
const ReasonWorkspaceClosed = "workspaceClosed"

// ReasonPathGone is the stopped event's reason when the watched directory no longer
// exists (or is no longer a directory): its entries are reported deleted first, then the
// watch stops (m1j §Changes, step 4 B1).
const ReasonPathGone = "pathGone"

// ReasonUnreadable is the stopped event's reason when the watched directory itself can no
// longer be read: the watch stops rather than report nothing while it cannot see.
const ReasonUnreadable = "unreadable"

// watchInterval is how long the poller waits between walks (D7): one second. There is
// no OS notification API: FSEvents needs cgo on macOS or one kqueue descriptor per file,
// and inotify is Linux-only; a second's latency suits a file explorer.
const watchInterval = time.Second

// watchCap is the most entries one walk records (D7). A walk that reaches it keeps
// walking, so "there are more" is observable; a tick whose walk is truncated reports
// nothing deleted, so an entry pushed past the cap is never reported deleted.
const watchCap = 20000

// watchMaxChanges caps the changes one file.changes event carries (D8).
const watchMaxChanges = 1000

// excludedDirs are the directory names a walk never enters, anywhere (D7). A directory
// named build is excluded only beside a build.gradle or build.gradle.kts, so a source
// package named build is kept.
var excludedDirs = []string{".git", ".gradle", ".idea", ".kotlin", ".cxx"}

// excludedText is the fixed list the result publishes, so a caller learns what a watch
// skips without reading this file (D7). It is the exclusions of excludedDirs, the build
// rule and file.write's temporary files.
var excludedText = append(append([]string{}, excludedDirs...),
	"build (a directory named build beside build.gradle or build.gradle.kts)",
	"*.droidyard-*.tmp (file.write temporary files)")

// watchWalkHook, when non-nil, runs inside every walk. A test holds it to keep a watch
// mid-walk while it cancels the operation (plan m1j step 4). Nil in production.
var watchWalkHook func()

// WatchParams is file.watch's parameters (D7).
type WatchParams struct {
	Path string `json:"path,omitempty" jsonschema_description:"The directory to watch, relative to the project root; \".\" or absent is the root. An absolute path inside the root is accepted."`
}

// WatchResult is file.watch's result (D7).
type WatchResult struct {
	SubscriptionID string   `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the changes arrive on, over the WebSocket session that made the call. The events are {path, changes, truncated} on topic file.changes, at most one per second."`
	Watched        int      `json:"watched" jsonschema:"required" jsonschema_description:"How many entries the baseline walk recorded; at most 20000."`
	Truncated      bool     `json:"truncated" jsonschema_description:"True when the baseline walk stopped at 20000 entries; watch a subdirectory."`
	Excluded       []string `json:"excluded" jsonschema:"required" jsonschema_description:"Directories and names the watch never reports."`
}

// FileChange is one change in a file.changes event's payload (D8). Kind is created,
// modified or deleted; Type is the entry's kind, as file.list reports it.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Type string `json:"type,omitempty"`
}

// ChangesPayload is the file.changes event's payload for type changes (D8).
type ChangesPayload struct {
	Path      string       `json:"path"`
	Changes   []FileChange `json:"changes"`
	Truncated bool         `json:"truncated"`
}

// StoppedPayload is the file.changes event's payload for type stopped (D8).
type StoppedPayload struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// watchEntry is one entry of a walk: its kind, and for a file or symlink its size and
// modification time. A directory's own size and mtime change for reasons a watch does
// not report (a child added elsewhere already names itself), so only its kind is
// compared.
type watchEntry struct {
	typ   string
	size  int64
	mod   time.Time
	isDir bool
}

// changed reports whether an entry differs from an earlier walk's.
func changed(prev, next watchEntry) bool {
	if prev.typ != next.typ {
		return true
	}
	if next.isDir {
		return false
	}
	return prev.size != next.size || !prev.mod.Equal(next.mod)
}

// watcher is one file.watch: the subscription its events are delivered to, the baseline
// its next walk is compared with, and the goroutine that walks every tick (D7, D9).
type watcher struct {
	watchers  *Watchers
	bus       *event.Bus
	sub       *event.Subscription
	root      *os.Root
	rel       string
	workspace string
	operation string
	tick      time.Duration

	baseline map[string]watchEntry

	// stop carries the reason a stopped event should report, "" for a subscription end
	// that publishes nothing. It is buffered and filled at most once, so requestStop
	// never blocks, however it races the poller's own end (D9).
	stop     chan string
	stopOnce sync.Once
}

// requestStop asks the poller to end, with the reason a stopped event carries ("" for a
// subscription end, which publishes nothing). The first request wins; later ones, a
// subscription end racing a workspace close, are dropped.
func (w *watcher) requestStop(reason string) {
	w.stopOnce.Do(func() { w.stop <- reason })
}

// run walks every tick until stopped, then ends. A workspace close's stop publishes the
// stopped event as its last act, so nothing follows it (D9); a subscription end stops
// silently. The root is released on every path.
func (w *watcher) run() {
	defer w.root.Close()
	defer w.watchers.remove(w)
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case reason := <-w.stop:
			if reason != "" {
				w.bus.PublishTo(w.sub, TopicFileChanges, TypeStopped, w.workspace, w.operation,
					StoppedPayload{Path: w.rel, Reason: reason})
			}
			return
		case <-t.C:
			w.tickOnce()
		}
	}
}

// tickOnce walks, publishes the differences since the last walk, and makes the new walk
// the baseline. A tick with no change publishes nothing; a tick whose walk is truncated
// reports no deletion and marks its event truncated (D7, D8). A walk is never taken as
// "nothing changed" because it failed (capability-api.md §Empty is a fact): a
// subdirectory that cannot be read keeps its last known entries, unchanged; a watched
// directory that is gone has every entry reported deleted and the watch stops
// (pathGone); one that cannot be read stops the watch (unreadable).
func (w *watcher) tickOnce() {
	next, truncated, cerr := w.walkFrom(w.root.FS(), w.rel, w.baseline)
	if cerr != nil {
		if cerr.Code == capability.ErrFileNotFound || cerr.Code == capability.ErrFileWrongType {
			changes, cut := diff(w.baseline, map[string]watchEntry{}, false)
			w.baseline = map[string]watchEntry{}
			if len(changes) > 0 {
				w.bus.PublishTo(w.sub, TopicFileChanges, TypeChanges, w.workspace, w.operation,
					ChangesPayload{Path: w.rel, Changes: changes, Truncated: cut})
			}
			w.requestStop(ReasonPathGone)
			return
		}
		w.requestStop(ReasonUnreadable)
		return
	}
	prev := w.baseline
	w.baseline = next
	changes, cut := diff(prev, next, truncated)
	if len(changes) == 0 {
		return
	}
	w.bus.PublishTo(w.sub, TopicFileChanges, TypeChanges, w.workspace, w.operation,
		ChangesPayload{Path: w.rel, Changes: changes, Truncated: truncated || cut})
}

// diff compares two walks. An entry in next but not prev is created; one in both that
// changed is modified; one in prev but not next is deleted, unless next was truncated —
// then an entry the walk's cap pushed out is not a deletion (D7, review B4). The list is
// capped at watchMaxChanges, and the second return reports that the cap cut it.
func diff(prev, next map[string]watchEntry, truncatedNext bool) ([]FileChange, bool) {
	changes := make([]FileChange, 0, len(next))
	for p, n := range next {
		o, ok := prev[p]
		switch {
		case !ok:
			changes = append(changes, FileChange{Path: p, Kind: "created", Type: n.typ})
		case changed(o, n):
			changes = append(changes, FileChange{Path: p, Kind: "modified", Type: n.typ})
		}
	}
	if !truncatedNext {
		for p, o := range prev {
			if _, ok := next[p]; !ok {
				changes = append(changes, FileChange{Path: p, Kind: "deleted", Type: o.typ})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	if len(changes) > watchMaxChanges {
		return changes[:watchMaxChanges], true
	}
	return changes, false
}

// walk records every entry under rel, rel itself excluded, up to watchCap, skipping
// D7's exclusions and never descending a symlink. It reports whether more entries
// existed than it recorded. The walk starts with a stat of rel, so a missing path is
// FILE_NOT_FOUND, a file is FILE_WRONG_TYPE, and a path that leaves the root is
// FILE_OUTSIDE_ROOT; the handler then closes the subscription it opened.
func (w *watcher) walk(fsys fs.FS, rel string) (map[string]watchEntry, bool, *capability.Error) {
	return w.walkFrom(fsys, rel, nil)
}

// walkFrom is walk for a tick when prev is the last baseline: a subdirectory under rel
// that cannot be read keeps prev's entries beneath it instead of failing the walk, and
// an entry that vanished between the listing and its stat is skipped. With prev nil (the
// baseline walk) every error fails the call.
func (w *watcher) walkFrom(fsys fs.FS, rel string, prev map[string]watchEntry) (map[string]watchEntry, bool, *capability.Error) {
	r := &resolver{root: w.root.Name()}
	tick := prev != nil
	info, err := fs.Stat(fsys, rel)
	if err != nil {
		return nil, false, r.classify(rel, err)
	}
	if !info.IsDir() {
		return nil, false, notDir(rel, typeName(info.Mode()))
	}

	entries := make(map[string]watchEntry)
	truncated := false
	var descend func(dir string) *capability.Error
	descend = func(dir string) *capability.Error {
		if watchWalkHook != nil {
			watchWalkHook()
		}
		children, err := fs.ReadDir(fsys, dir)
		if err != nil {
			if tick && dir != rel {
				keepUnder(entries, prev, dir)
				return nil
			}
			return r.classify(dir, err)
		}
		for _, e := range children {
			name := e.Name()
			p := name
			if dir != "." {
				p = dir + "/" + name
			}
			info, err := e.Info()
			if err != nil {
				if tick && errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return r.classify(p, err)
			}
			isDir := info.IsDir()
			if isDir {
				if excludedDir(name) || (name == "build" && hasGradleMarker(fsys, dir)) {
					continue
				}
			} else if tempWatchName(name) {
				continue
			}
			if len(entries) >= watchCap {
				truncated = true
				continue
			}
			entries[p] = watchEntry{typ: entryType(info.Mode()), size: info.Size(), mod: info.ModTime(), isDir: isDir}
			if isDir {
				if cerr := descend(p); cerr != nil {
					return cerr
				}
			}
		}
		return nil
	}
	if cerr := descend(rel); cerr != nil {
		return nil, false, cerr
	}
	return entries, truncated, nil
}

// keepUnder copies prev's entries beneath dir into entries: a subdirectory a tick could
// not read is reported as unchanged, never as emptied.
func keepUnder(entries, prev map[string]watchEntry, dir string) {
	prefix := dir + "/"
	for p, e := range prev {
		if strings.HasPrefix(p, prefix) && len(entries) < watchCap {
			entries[p] = e
		}
	}
}

// excludedDir reports whether a directory name is one D7 skips anywhere.
func excludedDir(name string) bool {
	for _, d := range excludedDirs {
		if name == d {
			return true
		}
	}
	return false
}

// hasGradleMarker reports whether dir holds a build.gradle or build.gradle.kts, so a
// build directory beside one is Gradle output and is excluded (D7).
func hasGradleMarker(fsys fs.FS, dir string) bool {
	for _, m := range []string{"build.gradle", "build.gradle.kts"} {
		if _, err := fs.Stat(fsys, path.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// tempWatchName reports whether name is a file.write temporary file (D6): a
// dot-prefixed name carrying the ".droidyard-" marker and a ".tmp" suffix. A watch
// ignores them, so an in-progress write is not a stream of create-and-delete noise.
func tempWatchName(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, ".droidyard-") && strings.HasSuffix(name, ".tmp")
}

// Watchers is every live watch poller, by workspace (plan m1j, D9). Its Stop is wired
// into workspace.Options.StopPollers, so a workspace close ends that workspace's
// pollers; Live is the count a test asserts returns to zero.
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

// Stop asks every poller of the workspace to end, each publishing its stopped event,
// and returns without waiting: a walk in progress must not block a close (D9). It is
// what workspace.Close calls, beside StopSidecars and CloseRoot.
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

// watch serves file.watch (D7, D9): it walks the directory for its baseline, opens a
// subscription of the calling session, and starts a poller that publishes the
// differences on that subscription alone. It returns the id once the poller is running.
func (o Options) watch(ctx context.Context, call *capability.Call) (any, error) {
	// The bus and the poller set are the daemon's; without them (the golden registry's
	// descriptors) a watch cannot run, and a call reaching a handler without a session
	// still hears SESSION_REQUIRED rather than a nil dereference.
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	if o.Bus == nil || o.Watchers == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "file.watch needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	r, cerr := o.resolver(call)
	if cerr != nil {
		return nil, cerr
	}
	rel, cerr := r.resolve(call.Params.(WatchParams).Path, false)
	if cerr != nil {
		return nil, cerr
	}
	root, cerr := r.openRoot()
	if cerr != nil {
		return nil, cerr
	}

	tick := o.Tick
	if tick <= 0 {
		tick = watchInterval
	}
	w := &watcher{
		watchers:  o.Watchers,
		bus:       o.Bus,
		root:      root,
		rel:       rel,
		workspace: call.Workspace,
		operation: call.OperationID,
		tick:      tick,
		stop:      make(chan string, 1),
	}
	// The subscription exists before the baseline walks, so a cancel during the walk has
	// something to clean up; its close hook ends the poller (D9).
	id, sub, serr := sess.Open([]string{TopicFileChanges}, func() { w.requestStop("") })
	if serr != nil {
		root.Close()
		return nil, serr
	}
	w.sub = sub

	baseline, truncated, cerr := w.walk(root.FS(), rel)
	if cerr != nil {
		sess.Unsubscribe(id)
		root.Close()
		return nil, cerr
	}
	w.baseline = baseline

	o.Watchers.add(w)
	go w.run()
	// The last act (D9): a cancel that landed during the baseline walk ends the
	// subscription and the poller, so nothing outlives a cancelled operation.
	if ctx.Err() != nil {
		w.requestStop("")
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return WatchResult{
		SubscriptionID: id,
		Watched:        len(baseline),
		Truncated:      truncated,
		Excluded:       excludedText,
	}, nil
}

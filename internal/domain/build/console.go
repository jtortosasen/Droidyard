package build

import (
	"context"
	"sort"
	"sync"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// The live console (ADR-0044): every byte a build's console receives goes through one
// writer, under one lock, as whole lines. The lock covers the file write, the offset it
// took and the publication, so what a watcher is told is exactly what the file holds,
// in order: the console's prefix up to a snapshot's offset plus the events published
// after it equal the console, byte for byte.

// TopicOutput is the topic build.watchOutput's stream lands on. Like file.changes it is
// not in events.subscribe's enum: its events take a seq but never enter the replay ring,
// and only the subscriptions a build.watchOutput call opened receive them (ADR-0044 §3).
const TopicOutput = "build.output"

// EventLines is the stream's one event type (ADR-0044 §2).
const EventLines = "lines"

// streamDroidyard is the stream of droidyard's own text in the console: the connector
// notice and the cap marker (ADR-0044 §2). The build's own are the sidecar's stdout and
// stderr.
const streamDroidyard = "droidyard"

// WatchOutputResult is build.watchOutput's result (ADR-0044 §1).
type WatchOutputResult struct {
	SubscriptionID string         `json:"subscriptionId" jsonschema:"required" jsonschema_description:"The subscription the build.output events arrive on, over the WebSocket session that made the call. It follows every build of the workspace, started by any client, until events.unsubscribe or the session's end."`
	Builds         []WatchedBuild `json:"builds" jsonschema:"required" jsonschema_description:"The workspace's running builds, taken after the subscription opened. Read each console's bytes [0, offset) with artifact.read; the rest arrives as events. An event whose offset is past what the client holds for its build, a build not listed included, is preceded by bytes to read with artifact.read."`
}

// WatchedBuild is one running build in build.watchOutput's result.
type WatchedBuild struct {
	BuildID    string `json:"buildId" jsonschema:"required"`
	ConsoleRef string `json:"consoleRef" jsonschema:"required" jsonschema_description:"The build's console artifact, readable while it grows."`
	Offset     int64  `json:"offset" jsonschema:"required" jsonschema_description:"The console's size in bytes when the snapshot was taken, after the subscription opened. The events for this build with a smaller offset repeat bytes already below it: ignore them."`
}

// LinesEvent is the payload of a build.output lines event (ADR-0044 §2): whole console
// lines, each one followed by a newline in the artifact. Project output, not untrusted
// (ADR-0015 §1).
type LinesEvent struct {
	BuildID    string   `json:"buildId" jsonschema:"required"`
	ConsoleRef string   `json:"consoleRef" jsonschema:"required"`
	Offset     int64    `json:"offset" jsonschema:"required" jsonschema_description:"The byte position of the first line in the console artifact; the lines are contiguous from there, each followed by a newline."`
	Stream     string   `json:"stream" jsonschema:"required,enum=stdout,enum=stderr,enum=droidyard" jsonschema_description:"stdout or stderr from the build; droidyard for the connector notice and the cap marker."`
	Lines      []string `json:"lines" jsonschema:"required" jsonschema_description:"Whole lines without their newline, oldest first."`
}

// liveConsole is one running build's console: the durable artifact and the only writer
// of it. A line that does not fit under the cap is dropped whole and counted; the cap
// marker goes last, on its own line.
type liveConsole struct {
	reg       *Registry
	workspace string
	buildID   string
	out       *artifact.Durable
	ref       string
	cap       int64

	mu      sync.Mutex
	dropped int64
	ended   bool
}

// newLiveConsole makes out the build's console and lists it among the workspace's
// running ones, so a build.watchOutput snapshot finds it. The console is listed before
// its first byte is written.
func (r *Registry) newLiveConsole(workspace, buildID string, out *artifact.Durable) *liveConsole {
	c := &liveConsole{reg: r, workspace: workspace, buildID: buildID, out: out, ref: out.Ref().Ref, cap: r.consoleLimit()}
	r.runMu.Lock()
	if r.consoles[workspace] == nil {
		r.consoles[workspace] = map[string]*liveConsole{}
	}
	r.consoles[workspace][buildID] = c
	r.runMu.Unlock()
	return c
}

// consoleLimit is the cap a console is created with: consoleCap, or a test's lower one.
func (r *Registry) consoleLimit() int64 {
	if r.capOverride > 0 {
		return r.capOverride
	}
	return consoleCap
}

// append writes lines to the console as whole lines and publishes them, all under the
// console's lock, and returns the lines it kept. A line that does not fit under the cap
// is dropped whole and counted; once the console has ended nothing is written.
func (c *liveConsole) append(stream string, lines []string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return nil
	}
	offset := c.out.Size()
	room := c.cap - offset
	var b []byte
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		n := int64(len(line)) + 1
		if n > room {
			c.dropped += n
			continue
		}
		room -= n
		b = append(b, line...)
		b = append(b, '\n')
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return nil
	}
	if _, err := c.out.Write(b); err != nil {
		// The file may hold part of these lines: the console ends here, so a watcher sees
		// a missing tail, never a hole, and the extractor gets nothing the file lacks.
		c.ended = true
		return nil
	}
	c.publish(stream, offset, kept)
	return kept
}

// end writes the cap marker, when the cap dropped anything, as the console's last line,
// and closes the console to further writes. It is idempotent. The marker is written and
// published before the build's operation ends, so a watcher has it by then.
func (c *liveConsole) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.ended = true
	if c.dropped == 0 {
		return
	}
	line := markerLine(c.cap, c.dropped)
	offset := c.out.Size()
	if err := c.out.WriteMarker(line); err != nil {
		return
	}
	c.publish(streamDroidyard, offset, []string{line})
}

// release ends the console and takes it out of the running set.
func (c *liveConsole) release() {
	c.end()
	c.reg.runMu.Lock()
	defer c.reg.runMu.Unlock()
	if m := c.reg.consoles[c.workspace]; m != nil {
		if m[c.buildID] == c {
			delete(m, c.buildID)
		}
		if len(m) == 0 {
			delete(c.reg.consoles, c.workspace)
		}
	}
}

// droppedBytes is how many bytes the cap dropped, whole lines with their newlines.
func (c *liveConsole) droppedBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// publish offers one lines event to every watch of the workspace. Called with c.mu held,
// which is what keeps the events in the order of the file.
func (c *liveConsole) publish(stream string, offset int64, lines []string) {
	bus := c.reg.bus
	if bus == nil {
		return
	}
	subs := c.reg.watchersOf(c.workspace)
	if len(subs) == 0 {
		return
	}
	payload := LinesEvent{BuildID: c.buildID, ConsoleRef: c.ref, Offset: offset, Stream: stream, Lines: lines}
	for _, sub := range subs {
		bus.PublishTo(sub, TopicOutput, EventLines, c.workspace, c.buildID, payload)
	}
}

// watch is one build.watchOutput subscription, as the registry keeps it.
type watch struct {
	sub     *event.Subscription
	removed bool
}

// watchersOf is the subscriptions watching the workspace's builds.
func (r *Registry) watchersOf(workspace string) []*event.Subscription {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	set := r.watches[workspace]
	out := make([]*event.Subscription, 0, len(set))
	for w := range set {
		out = append(out, w.sub)
	}
	return out
}

func (r *Registry) addWatch(workspace string, w *watch, sub *event.Subscription) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if w.removed {
		return
	}
	w.sub = sub
	if r.watches[workspace] == nil {
		r.watches[workspace] = map[*watch]struct{}{}
	}
	r.watches[workspace][w] = struct{}{}
}

func (r *Registry) removeWatch(workspace string, w *watch) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	w.removed = true
	delete(r.watches[workspace], w)
	if len(r.watches[workspace]) == 0 {
		delete(r.watches, workspace)
	}
}

// snapshot lists the workspace's running builds with each console's size, each taken
// under that console's lock: an append is wholly before it (the offset counts it) or
// wholly after (the subscription, already registered, receives it).
func (r *Registry) snapshot(workspace string) []WatchedBuild {
	r.runMu.Lock()
	cs := make([]*liveConsole, 0, len(r.consoles[workspace]))
	for _, c := range r.consoles[workspace] {
		cs = append(cs, c)
	}
	r.runMu.Unlock()
	out := make([]WatchedBuild, 0, len(cs))
	for _, c := range cs {
		c.mu.Lock()
		out = append(out, WatchedBuild{BuildID: c.buildID, ConsoleRef: c.ref, Offset: c.out.Size()})
		c.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuildID < out[j].BuildID })
	return out
}

// watchOutput is build.watchOutput (ADR-0044 §1): the subscription opens first, the
// snapshot second, so a line written between the two reaches the watcher as an event
// and one written before is under the snapshot's offset.
func (r *Registry) watchOutput(ctx context.Context, call *capability.Call) (any, error) {
	sess, err := event.SessionOf(call)
	if err != nil {
		return nil, err
	}
	if r == nil || r.bus == nil {
		return nil, capability.NewError(capability.ErrSessionRequired, "build.watchOutput needs the daemon's event bus").
			WithNextStep("watch over the WebSocket session, GET /v1/ws")
	}
	w := &watch{}
	id, sub, err := sess.Open([]string{TopicOutput}, func() { r.removeWatch(call.Workspace, w) })
	if err != nil {
		return nil, err
	}
	r.addWatch(call.Workspace, w, sub)
	if hook := r.watchHook.Load(); hook != nil {
		(*hook)()
	}
	builds := r.snapshot(call.Workspace)
	if ctx.Err() != nil {
		sess.Unsubscribe(id)
		return nil, ctx.Err()
	}
	return WatchOutputResult{SubscriptionID: id, Builds: builds}, nil
}

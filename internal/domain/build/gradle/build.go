package gradle

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/process"
)

// The build half of the sidecar protocol (gradle.md §Protocol, §The build model):
// build.run is accepted at once, and every later fact arrives as a notification routed
// by buildId to the sink the caller registered before it sent the request, so no
// notification can race the reply. A build is cancelled with build.cancel, the only
// route to which is operation.cancel (ADR-0020).

// TaskSummary is the build-level view of the task outcomes (gradle.md §The build
// model). The sidecar sends it on every progress event; a record keeps the last one.
type TaskSummary struct {
	Executed  int `json:"executed"`
	UpToDate  int `json:"upToDate"`
	FromCache int `json:"fromCache"`
	NoSource  int `json:"noSource"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// Output is one coalesced console batch (event.build.output; gradle.md §Output: 200
// lines or 64 KiB, flushed every 100 ms, colour off).
type Output struct {
	Stream string   `json:"stream"`
	Lines  []string `json:"lines"`
}

// TaskFinished is event.build.taskFinished: one task's outcome, with Gradle's own
// reason verbatim ("Assembled" and "up-to-date" are different answers).
type TaskFinished struct {
	Path       string `json:"path"`
	Outcome    string `json:"outcome"`
	DurationMs int64  `json:"durationMs"`
	Reason     string `json:"reason,omitempty"`
}

// Progress is event.build.progress: the counts so far and the task running (D21).
type Progress struct {
	BuildID    string      `json:"buildId"`
	Counts     TaskSummary `json:"counts"`
	ActiveTask string      `json:"activeTask,omitempty"`
}

// Problem is one Problems API record as the sidecar forwards it (gradle.md §Error
// model). The same shape arrives failure-carried and as a progress event; the field
// names are the sidecar's, and the extractor's are the same on purpose, so the two
// cannot drift (internal/domain/build/extract).
type Problem struct {
	Severity           string   `json:"severity,omitempty"`
	ProblemID          string   `json:"problemId,omitempty"`
	ProblemDisplayName string   `json:"problemDisplayName,omitempty"`
	ContextualLabel    string   `json:"contextualLabel,omitempty"`
	Details            string   `json:"details,omitempty"`
	File               string   `json:"file,omitempty"`
	Line               int      `json:"line,omitempty"`
	Column             int      `json:"column,omitempty"`
	TaskPath           string   `json:"taskPath,omitempty"`
	Solutions          []string `json:"solutions,omitempty"`
	DocumentationLink  string   `json:"documentationLink,omitempty"`
}

// Failure is one node of the failure tree event.build.finished carries. The sidecar
// bounds it (D6): 8 KiB a string, 200 nodes, and a truncatedCauses count instead of
// more nodes. Description is the node's own text, a Java stack trace for a Gradle
// failure, kept in the record and never in a published result (plan m1g step 2 → 7).
type Failure struct {
	ID              string    `json:"id,omitempty"`
	Message         string    `json:"message"`
	Description     string    `json:"description,omitempty"`
	Causes          []Failure `json:"causes,omitempty"`
	TruncatedCauses int       `json:"truncatedCauses,omitempty"`
}

// Finished is the terminal event.build.finished notification: exactly one per accepted
// build, whatever its outcome. Outcome is success, failed or cancelled.
type Finished struct {
	BuildID                string    `json:"buildId"`
	Outcome                string    `json:"outcome"`
	Failures               []Failure `json:"failures"`
	FailureProblems        []Problem `json:"failureProblems"`
	FailureProblemsDropped int       `json:"failureProblemsDropped,omitempty"`
	MissingCapabilities    []string  `json:"missingCapabilities"`
}

// BuildSink receives one build's notifications, in the order the sidecar sent them.
// Every method runs on the client's protocol goroutine, which also carries the replies
// of every other call, so a sink must be quick and must not call back into the client.
type BuildSink interface {
	Output(Output)
	TaskFinished(TaskFinished)
	Progress(Progress)
	Problem(Problem)
	Finished(Finished)
}

// BuildRun is one build's place on the sidecar: the root's TAPI lock held for the
// build's whole duration (R5, so a model call meanwhile answers from the cache), a
// lease that keeps the sidecar from its idle stop, and the root's open connection.
// BeginBuild returns it; Close releases all three.
type BuildRun struct {
	c       *Client
	root    string
	lease   *process.Lease
	release func()

	mu      sync.Mutex
	conn    Connection
	watched []string
	closed  bool
}

var _ BuildSink = (*BuildRun)(nil)

// BeginBuild takes the root's TAPI lock, leases the sidecar and opens the root's
// connection if it is not open, so build.run can be sent. The caller must Close the
// run when the build ends, whatever its outcome.
func (c *Client) BeginBuild(ctx context.Context, canonical string) (*BuildRun, error) {
	release, err := c.LockRoot(ctx, canonical)
	if err != nil {
		return nil, err
	}
	lease, _, err := c.use(ctx)
	if err != nil {
		release()
		return nil, err
	}
	r := c.root(canonical)
	if !r.isOpen(lease.Pid()) {
		conn, err := c.open(ctx, lease, canonical)
		if err != nil {
			lease.Release()
			release()
			c.fail(r, err)
			return nil, err
		}
		r.setConn(conn, lease.Pid())
	}
	return &BuildRun{c: c, root: canonical, lease: lease, release: release, conn: r.connection()}, nil
}

// Conn is the root's connection: its Gradle version and source and the daemon JVM every
// result reports (gradle.md §Daemon JVM).
func (b *BuildRun) Conn() Connection {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn
}

// Done is closed when the sidecar process that serves this build is gone: the caller's
// signal that no terminal notification is coming (a crash mid-build).
func (b *BuildRun) Done() <-chan struct{} { return b.lease.Done() }

// Err is why the sidecar is gone, or nil while it runs.
func (b *BuildRun) Err() error { return b.lease.Err() }

// Crash is why a build ends when the sidecar serving it is gone: Done was closed
// before the terminal notification came, so no build finished and none will. It marks
// the root degraded for the next call (D20), degrades the workspace of the root whose
// build it was and no other (plan m2c, D21), and the caller records nothing
// (gradle.md §Lifecycle: the operation fails with a cause; the registry keeps only what
// finished).
func (b *BuildRun) Crash() *capability.Error {
	e := capability.Errorf(capability.ErrBuildSidecarCrashed, "the Gradle sidecar died while the build ran")
	if err := b.Err(); err != nil {
		e = e.WithCause(err.Error())
	}
	b.c.crashed(b.root, e)
	return e
}

// Run registers sink for the build's notifications and sends build.run. The sink is
// registered before the request, so a notification the sidecar writes while the reply
// is in flight is not lost. The sidecar answers {accepted} at once; the build's facts
// arrive at the sink.
func (b *BuildRun) Run(ctx context.Context, buildID string, tasks []string, sink BuildSink) error {
	b.c.watch(buildID, sink)
	b.mu.Lock()
	b.watched = append(b.watched, buildID)
	b.mu.Unlock()
	_, err := b.c.call(ctx, b.lease, "build.run", map[string]any{
		"buildId": buildID, "root": b.root, "tasks": tasks,
	})
	if err != nil {
		b.c.unwatch(buildID)
		// A sidecar that died under the build.run request ended this build as surely as
		// one that died after it accepted.
		if e, ok := err.(*capability.Error); ok && e.Code == capability.ErrBuildSidecarCrashed {
			b.c.crashed(b.root, e)
		}
		return err
	}
	return nil
}

// Cancel sends build.cancel for the build, so the sidecar cancels it at once and its
// terminal notification says cancelled.
func (b *BuildRun) Cancel(ctx context.Context, buildID string) error {
	_, err := b.c.call(ctx, b.lease, "build.cancel", map[string]any{"buildId": buildID})
	return err
}

// Close releases the root's lock, the lease and every sink it still holds.
func (b *BuildRun) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	watched := b.watched
	b.watched = nil
	b.mu.Unlock()
	for _, id := range watched {
		b.c.unwatch(id)
	}
	b.lease.Release()
	b.release()
}

// watch registers a build's sink. Guarded by the client's lock, like the pending calls,
// because the protocol goroutine reads the map while a caller writes it.
func (c *Client) watch(buildID string, sink BuildSink) {
	c.mu.Lock()
	if c.builds == nil {
		c.builds = map[string]BuildSink{}
	}
	c.builds[buildID] = sink
	c.mu.Unlock()
}

func (c *Client) unwatch(buildID string) {
	c.mu.Lock()
	delete(c.builds, buildID)
	c.mu.Unlock()
}

// The five build notifications (gradle.md §Protocol).
const (
	notifyOutput       = "event.build.output"
	notifyTaskFinished = "event.build.taskFinished"
	notifyProgress     = "event.build.progress"
	notifyProblem      = "event.build.problem"
	notifyFinished     = "event.build.finished"
)

// notify routes one notification to the sink of the build it names. A method this
// daemon does not know, and one naming a build nobody watches — a caller that gave up,
// or a build from before a restart — are both dropped: the sidecar's notification
// channel is not a request, and JSON-RPC gives an id-less message no reply. A known
// event of a watched build whose payload cannot be read is a protocol break, exactly as
// a non-JSON line is: the sidecar promised the shape.
func (c *Client) notify(s *session, m message) {
	switch m.Method {
	case notifyOutput, notifyTaskFinished, notifyProgress, notifyProblem, notifyFinished:
	default:
		return
	}
	var head struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal(m.Params, &head); err != nil {
		c.breakProtocol(s, err)
		return
	}
	c.mu.Lock()
	sink := c.builds[head.BuildID]
	if m.Method == notifyFinished {
		// The terminal event: the sink has nothing more to hear, and the map must not
		// hold a finished build's sink until its caller unwinds.
		delete(c.builds, head.BuildID)
	}
	c.mu.Unlock()
	if sink == nil {
		return
	}
	switch m.Method {
	case notifyOutput:
		var ev Output
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			c.breakProtocol(s, err)
			return
		}
		sink.Output(ev)
	case notifyTaskFinished:
		var ev TaskFinished
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			c.breakProtocol(s, err)
			return
		}
		sink.TaskFinished(ev)
	case notifyProgress:
		var ev Progress
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			c.breakProtocol(s, err)
			return
		}
		sink.Progress(ev)
	case notifyProblem:
		var ev struct {
			Problem Problem `json:"problem"`
		}
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			c.breakProtocol(s, err)
			return
		}
		sink.Problem(ev.Problem)
	case notifyFinished:
		var ev Finished
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			c.breakProtocol(s, err)
			return
		}
		sink.Finished(ev)
	}
}

// The sink methods below exist so a BuildRun is itself a sink, which is only useful in
// a test; nothing registers a BuildRun as one.
func (*BuildRun) Output(Output)             {}
func (*BuildRun) TaskFinished(TaskFinished) {}
func (*BuildRun) Progress(Progress)         {}
func (*BuildRun) Problem(Problem)           {}
func (*BuildRun) Finished(Finished)         {}

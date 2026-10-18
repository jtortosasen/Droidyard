package build

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// The build capabilities: build.run, build.assemble, build.errors and
// build.lastResult (gradle.md §The build model, §Persistence; plan m1g step 7). A
// build's id is its operation's id (ADR-0020 §Operation capabilities), the build runs
// with the workspace's scheduler key held for its whole duration (R5), and what it
// leaves behind is a record and a console, readable in the next daemon.

// cancelWait bounds how long the core waits for the sidecar to acknowledge a cancelled
// build before it stops waiting and records the build cancelled (ADR-0020: a cancelled
// operation ends cancelled whatever the handler returns).
const cancelWait = 10 * time.Second

// The phases a build cancel lands in, logged once per cancel so a level-5 run can tell
// which path it took (plan m1p D3): the build had not begun (no build record and nothing
// on the sidecar; a console created before BeginBuild failed is left for the open's sweep), the sidecar had not answered {accepted}, or the build was running.
const (
	phaseBeginBuild     = "begin-build"
	phaseBeforeAccepted = "before-accepted"
	phaseRunning        = "running"
)

// taskPattern is R6: what a build.run task may look like. It accepts a bare name and a
// full path, and no leading "-", so nothing an agent sends is read as a Gradle option.
var taskPattern = regexp.MustCompile(`^:?[A-Za-z0-9_][A-Za-z0-9_:.-]*$`)

// BuildClient is what the build capabilities need of the sidecar client: the cached
// project model (like build.tasks) and a build's place on the sidecar. *gradle.Client
// implements it.
type BuildClient interface {
	Client
	// BeginBuild takes the root's TAPI lock for the build's whole duration, leases the
	// sidecar and opens the root's connection. The caller closes the run when the build
	// ends, whatever its outcome.
	BeginBuild(ctx context.Context, canonical string) (*gradle.BuildRun, error)
}

// Options configure the build capabilities.
type Options struct {
	// Client is the sidecar client. Required.
	Client BuildClient
	// History is the build registry: where a finished build is written and where
	// build.lastResult and build.errors read it from. Required; an Options without one
	// registers capabilities whose handlers report that no state is configured.
	History *Registry
	// Authorize is the trust re-check the scheduler runs before each handler
	// (internal/policy; ADR-0015 §2): build.errors and build.lastResult read history, and
	// get the canonical project root as Call.Authorized.
	Authorize Authorize
	// AuthorizeGradle is Authorize followed by the Gradle root's resolution (ADR-0075
	// §3): build.tasks, build.run and build.assemble get a gradle.Roots as
	// Call.Authorized, or PROJECT_NO_GRADLE_ROOT.
	AuthorizeGradle Authorize
}

// Register adds build.tasks, build.run, build.assemble, build.errors and
// build.lastResult to the registry.
// buildIDPattern is an operation id (capability.newID), the only shape a build id has.
var buildIDPattern = regexp.MustCompile(`^op-[0-9a-f]{16}$`)

func Register(reg *capability.Registry, o Options) {
	registerTasks(reg, o.Client, o.AuthorizeGradle)
	registerBuilds(reg, o)
}

// buildErrors are the codes build.run and build.assemble can return (plan m1g D9, R2):
// everything a model fetch can raise, plus the params check and a record that could not
// be written.
var buildErrors = []capability.ErrorCode{
	capability.ErrWorkspaceUntrusted,
	capability.ErrProjectNoGradleRoot,
	capability.ErrBuildSidecarUnavailable,
	capability.ErrBuildSidecarCrashed,
	capability.ErrBuildGradleUnsupported,
	capability.ErrProjectModelFailed,
	capability.ErrInvalidParams,
	capability.ErrWorkspaceStateUnreadable,
}

// readErrors are the codes build.errors and build.lastResult can return: the trust
// hook's, the background scheduler's, and the history's own.
var readErrors = []capability.ErrorCode{
	capability.ErrWorkspaceUntrusted,
	capability.ErrBuildNotFound,
	capability.ErrWorkspaceStateUnreadable,
}

// resource is the scheduler key every build of a workspace holds (R5,
// overview.md §Concurrency): one build per workspace at a time, and a model fetch
// answered from the cache while one runs.
func resource(call *capability.Call) string { return "gradle.root:" + call.Workspace }

func registerBuilds(reg *capability.Registry, o Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "build.run",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    o.AuthorizeGradle,
		Resource:     resource,
		Params:       RunParams{},
		Result:       Result{},
		Text:         capability.TextOf(resultText),
		Errors:       buildErrors,
		Description:  "Runs 1 to 20 Gradle tasks and returns the finished build's compact record; a failed build is a completed result with outcome failed, never an error. Read a failure with build.errors, the console with artifact.read, and cancel with operation.cancel.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(RunParams)
			if err := checkTasks(p.Tasks); err != nil {
				return nil, err
			}
			return dispatch(ctx, call, o, Request{Tasks: p.Tasks})
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:         "build.assemble",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		LongRunning:  true,
		Idempotent:   true,
		Since:        "0.6.0",
		Availability: []capability.Requirement{capability.RequiresGradle},
		Authorize:    o.AuthorizeGradle,
		Resource:     resource,
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "it assembles a variant of a module, replacing only its own, rebuildable outputs",
		},
		Params: AssembleParams{},
		Target: func(p any) *capability.OpTarget {
			ap := p.(AssembleParams)
			return &capability.OpTarget{Module: ap.Module, Variant: ap.Variant}
		},
		Result:      Result{},
		Text:        capability.TextOf(resultText),
		Errors:      buildErrors,
		Description: "Assembles one module's variant with Gradle and returns the build record, as build.run does. module and variant must come from project.modules and project.variants.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(AssembleParams)
			model, err := projectModel(ctx, call, o)
			if err != nil {
				return nil, err
			}
			m, ok := model.Module(p.Module)
			if !ok {
				return nil, capability.Errorf(capability.ErrInvalidParams, "no project %q in this workspace", p.Module).
					WithDetails(map[string]any{"modules": modulePaths(model)}).
					WithNextStep("call project.modules for the project paths, then retry")
			}
			variants, _, _ := gradle.Variants(m, model.Probe)
			if !containsString(variants, p.Variant) {
				return nil, capability.Errorf(capability.ErrInvalidParams, "module %s has no variant %q", p.Module, p.Variant).
					WithDetails(map[string]any{"module": p.Module, "variants": variants}).
					WithNextStep("call project.variants {module: " + p.Module + "} for that module's variants, then retry")
			}
			return dispatchModel(ctx, call, o, Request{
				Tasks: []string{assembleTask(p.Module, p.Variant)}, Module: p.Module, Variant: p.Variant,
			}, model)
		},
	})
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "build.watchOutput",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		Statefulness: capability.Streaming,
		Since:        "0.16.0",
		Description:  "Streams every build console of the workspace to the calling WebSocket session while it runs: whole lines on topic build.output, with the byte offset of the first. The result lists the running builds with each console's size taken after the subscription opened; read [0, offset) with artifact.read, apply the events from there, and after an events.gap read on from the last offset.",
		Params:       struct{}{},
		Result:       WatchOutputResult{},
		Events: &capability.Events{
			Topic: TopicOutput,
			Types: map[string]any{EventLines: LinesEvent{}},
		},
		Errors: []capability.ErrorCode{capability.ErrSessionRequired},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "adds a subscription to the calling session; changes no build",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return o.History.watchOutput(ctx, call)
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "build.errors",
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       "0.6.0",
		Authorize:   o.Authorize,
		Params:      ErrorsParams{},
		Result:      ErrorsResult{},
		Errors:      readErrors,
		Description: "Returns a finished build's merged errors (severity, file, line, message, problem id), and its warnings on request; buildId absent means the newest build. Read it after a failed build instead of the console.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return errorsFor(o.History, call)
		},
	})
	reg.MustRegister(capability.Descriptor{
		Name:        "build.lastResult",
		Kind:        capability.Query,
		Scope:       capability.Workspace,
		Since:       "0.6.0",
		Authorize:   o.Authorize,
		Params:      struct{}{},
		Result:      Result{},
		Text:        capability.TextOf(resultText),
		Errors:      readErrors,
		Description: "Returns the newest finished build of the workspace in build.run's shape, without running anything. It survives a daemon restart.",
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			if !o.History.configured() {
				return nil, noState()
			}
			rec, err := o.History.newest(call.Workspace)
			if err != nil {
				return nil, err
			}
			return rec.result(), nil
		},
	})
}

// dispatch runs a build whose request is already validated: the project model is
// fetched first (the output search needs the modules' build directories, and a model
// fetch during the build would queue behind it), then the build runs.
func dispatch(ctx context.Context, call *capability.Call, o Options, req Request) (any, error) {
	model, err := projectModel(ctx, call, o)
	if err != nil {
		return nil, err
	}
	return dispatchModel(ctx, call, o, req, model)
}

// projectModel is a build's first step, the project model of the call's Gradle root; a cancel
// that ends it is logged as landing before the build began (plan m1p D3).
func projectModel(ctx context.Context, call *capability.Call, o Options) (gradle.Model, error) {
	model, _, err := o.Client.Model(ctx, call.Authorized.(gradle.Roots).Gradle)
	if err != nil && ctx.Err() != nil {
		o.History.logCancel(call.OperationID, phaseBeginBuild)
	}
	return model, err
}

func dispatchModel(ctx context.Context, call *capability.Call, o Options, req Request, model gradle.Model) (any, error) {
	if !o.History.configured() {
		return nil, noState()
	}
	return o.History.run(ctx, call, o.Client, req, model)
}

// run is one build, from the console's creation to the persisted record.
func (r *Registry) run(ctx context.Context, call *capability.Call, client BuildClient, req Request, model gradle.Model) (Result, error) {
	if !r.configured() {
		return Result{}, noState()
	}
	roots := call.Authorized.(gradle.Roots)
	buildID := call.OperationID

	// The console exists before the build does, so the whole of its output has
	// somewhere to go and a result's consoleRef is stable from the first byte
	// (gradle.md §Output; D13, D24).
	console, err := r.arts.CreateDurable(call.Workspace, buildID, artifact.DurableOptions{MediaType: "text/plain", Cap: r.consoleLimit()})
	if err != nil {
		return Result{}, capability.Errorf(capability.ErrWorkspaceStateUnreadable, "the build's console could not be created").
			WithCause(err.Error()).
			WithNextStep("check the permissions and free space of the state directory")
	}
	defer console.Close()
	// Every write to the console goes through lc, under its lock, as whole lines, and is
	// published to the workspace's build.watchOutput watches (ADR-0044). It is listed
	// among the running consoles before its first byte, and released (the cap marker
	// written, if due) before the file is closed.
	lc := r.newLiveConsole(call.Workspace, buildID, console)
	defer lc.release()
	r.markRunning(buildID)
	defer r.unmarkRunning(buildID)

	br, err := client.BeginBuild(ctx, roots.Gradle)
	if err != nil {
		if ctx.Err() != nil {
			r.logCancel(buildID, phaseBeginBuild)
		}
		return Result{}, err
	}
	defer br.Close()

	// D19: a project with no wrapper is built on the Tooling API's own Gradle
	// distribution (about 150 MB, downloaded on first use). The result's gradle.source
	// says "connector"; this line is what names it to a reader of the console, written
	// before the build's own output so the tail is never droidyard's text.
	conn := br.Conn()
	if line := connectorNotice(conn); line != "" {
		lc.append(streamDroidyard, []string{line})
	}

	sink := newBuildSink(lc, call.Progress, buildID)
	started := r.now()
	if err := br.Run(ctx, buildID, req.Tasks, sink); err != nil {
		if ctx.Err() != nil {
			r.logCancel(buildID, phaseBeforeAccepted)
			// Cancelled before the sidecar answered {accepted}: the build.run may still
			// be queued there, so send the cancel anyway; the sidecar keeps a cancel for
			// a build it has not started, and that build then starts cancelled (step 7
			// review B1).
			cctx, cancel := context.WithTimeout(context.Background(), cancelWait)
			_ = br.Cancel(cctx, buildID)
			cancel()
		}
		return Result{}, err
	}

	cancelled := false
	select {
	case <-sink.done:
	case <-ctx.Done():
		cancelled = true
	case <-br.Done():
		return Result{}, br.Crash()
	}
	if cancelled {
		r.logCancel(buildID, phaseRunning)
		// The operation was cancelled: ask the sidecar to cancel the build and give it
		// a bounded moment to end it, so the record can say what had finished. The
		// operation ends cancelled whatever the build reports, because its ctx is
		// cancelled and the handler returns that error.
		cctx, cancel := context.WithTimeout(context.Background(), cancelWait)
		_ = br.Cancel(cctx, buildID)
		cancel()
		select {
		case <-sink.done:
		case <-br.Done():
			return Result{}, br.Crash()
		case <-time.After(cancelWait):
		}
	}

	// The cap marker goes last, so it carries the final count of what the cap dropped
	// (D24), and before the record and the operation's end, so a watcher has it by then
	// (ADR-0044 §2).
	lc.end()
	fin := sink.finish()
	outcome := fin.Outcome
	if outcome == "" {
		// No terminal notification: the build never said how it ended, and the reason
		// is that it was cancelled.
		outcome = "cancelled"
	}
	finished := r.now()
	profile, missing := profileOf(conn, fin.MissingCapabilities)
	agp, kgp := pluginVersions(model.Plugins)
	expected := expectedSources(conn, model.Plugins)
	merged := extract.Extract(extract.Input{
		Root:            roots.Project,
		Outcome:         outcome,
		Console:         sink.console(),
		Failures:        sink.failureTree(),
		FailureProblems: sink.failureProblemRecords(),
		ProblemEvents:   sink.problemRecords(),
		Resolver:        newMergedResolver(model),
	})
	outputs, paths := resolveOutputs(model, sink.outcomes())
	tasks := sink.taskOutcomes()

	rec := record{
		BuildID:             buildID,
		Root:                roots.Gradle,
		Request:             req,
		Outcome:             outcome,
		StartedAt:           started,
		FinishedAt:          finished,
		DurationMs:          finished.Sub(started).Milliseconds(),
		Gradle:              gradleInfo(conn),
		Profile:             profile,
		MissingCapabilities: missing,
		Validation:          validationFor(profile),
		AGP:                 agp,
		KGP:                 kgp,
		ExpectedSources:     &expected,
		TaskSummary:         taskSummary(sink.counts()),
		Tasks:               tasks,
		Failures:            failureNodes(fin.Failures),
		FailureDropped:      fin.FailureProblemsDropped,
		Errors:              merged.Errors,
		Warnings:            merged.Warnings,
		Truncated:           merged.Truncated,
		Sources:             sourceNames(merged.Sources),
		Unrecognised:        merged.Unrecognised,
		RootFailures:        merged.RootFailures,
		Tail:                merged.Tail,
		Outputs:             outputs,
		OutputPaths:         paths,
		ConsoleRef:          console.Ref().Ref,
		ConsoleBytes:        console.Size(),
		ConsoleDropped:      lc.droppedBytes(),
	}
	if err := r.persist(call.Workspace, rec); err != nil {
		return Result{}, stateUnreadable(call.Workspace, err)
	}
	if cancelled {
		return Result{}, ctx.Err()
	}
	return rec.result(), nil
}

// errorsFor is build.errors: one build's merged problems, from the record (gradle.md
// §Merge rules).
func errorsFor(r *Registry, call *capability.Call) (ErrorsResult, error) {
	if !r.configured() {
		return ErrorsResult{}, noState()
	}
	p := call.Params.(ErrorsParams)
	var rec record
	var err error
	if p.BuildID != "" {
		// A build id is an operation id; anything else names no build of this
		// workspace, and must never reach a path (step 7 review B3).
		if !buildIDPattern.MatchString(p.BuildID) {
			return ErrorsResult{}, capability.Errorf(capability.ErrBuildNotFound, "no build %q in this workspace", p.BuildID).
				WithNextStep("call build.lastResult for the newest build's id")
		}
		rec, err = r.load(call.Workspace, p.BuildID)
	} else {
		rec, err = r.newest(call.Workspace)
	}
	if err != nil {
		return ErrorsResult{}, err
	}
	res := ErrorsResult{
		BuildID:      rec.BuildID,
		Outcome:      rec.Outcome,
		Errors:       rec.Errors,
		Truncated:    rec.Truncated,
		Unrecognised: rec.Unrecognised,
		RootFailures: rec.RootFailures,
		Tail:         rec.Tail,
		Sources:      rec.Sources,
	}
	if res.Errors == nil {
		res.Errors = []extract.Record{}
	}
	if p.IncludeWarnings {
		res.Warnings = rec.Warnings
	}
	return res, nil
}

// result is the compact record every build call returns (plan m1g D10): the first five
// of each merged list, and no path, no tail and no failure-tree description.
func (rec record) result() Result {
	res := Result{
		BuildID:             rec.BuildID,
		Request:             rec.Request,
		Outcome:             rec.Outcome,
		StartedAt:           rec.StartedAt,
		FinishedAt:          rec.FinishedAt,
		DurationMs:          rec.DurationMs,
		Gradle:              rec.Gradle,
		Profile:             rec.Profile,
		MissingCapabilities: rec.MissingCapabilities,
		Validation:          rec.Validation,
		AGP:                 rec.AGP,
		KGP:                 rec.KGP,
		ExpectedSources:     rec.ExpectedSources,
		TaskSummary:         rec.TaskSummary,
		ErrorCount:          len(rec.Errors),
		WarningCount:        len(rec.Warnings),
		Extraction:          Extraction{Sources: rec.Sources, Unrecognised: rec.Unrecognised},
		ConsoleRef:          rec.ConsoleRef,
		Outputs:             rec.Outputs,
	}
	for _, t := range rec.Tasks {
		if t.Outcome == "failed" {
			res.FailedTasks = append(res.FailedTasks, FailedTask{Path: t.Path, Reason: t.Reason})
		}
	}
	if n := min(len(rec.Errors), 5); n > 0 {
		res.Errors = rec.Errors[:n]
	}
	if n := min(len(rec.Warnings), 5); n > 0 {
		res.Warnings = rec.Warnings[:n]
	}
	return res
}

// buildSink receives one build's notifications and turns them into what the record
// needs: the console artifact, the lines the extractor reads, the task outcomes and the
// Problems records. Every method runs on the client's protocol goroutine, and progress
// is reported from there, before the build's next fact arrives.
type buildSink struct {
	out      *liveConsole
	progress func(done int, total int, message string)
	buildID  string

	mu       sync.Mutex
	lines    []extract.Line
	summary  gradle.TaskSummary
	tasks    []gradle.TaskFinished
	problems []extract.Problem
	failures []gradle.Failure
	fprobs   []extract.Problem
	active   string
	done     chan struct{}
	fin      gradle.Finished
}

var _ gradle.BuildSink = (*buildSink)(nil)

func newBuildSink(console *liveConsole, progress func(done int, total int, message string), buildID string) *buildSink {
	return &buildSink{out: console, progress: progress, buildID: buildID, done: make(chan struct{})}
}

// Output writes one console batch through the live console and keeps the lines it
// kept for the extractor: the same 32 MiB bound holds the artifact and the extractor's
// input, so a build that prints without end cannot grow the daemon's memory without
// bound (D24).
func (s *buildSink) Output(o gradle.Output) {
	kept := s.out.append(o.Stream, o.Lines)
	s.mu.Lock()
	for _, line := range kept {
		s.lines = append(s.lines, extract.Line{Stream: o.Stream, Text: line})
	}
	s.mu.Unlock()
}

// TaskFinished records one task's outcome, counts it and reports progress (D21).
func (s *buildSink) TaskFinished(t gradle.TaskFinished) {
	s.mu.Lock()
	s.tasks = append(s.tasks, t)
	s.summary = countTask(s.summary, t.Outcome)
	done := len(s.tasks)
	active := s.active
	s.mu.Unlock()
	if s.progress != nil {
		s.progress(done, 0, active)
	}
}

// Progress records the counts and the task running, and reports progress (D21).
func (s *buildSink) Progress(p gradle.Progress) {
	s.mu.Lock()
	s.summary = p.Counts
	s.active = p.ActiveTask
	done := len(s.tasks)
	s.mu.Unlock()
	if s.progress != nil {
		s.progress(done, 0, p.ActiveTask)
	}
}

// Problem records one Problems API event: the only source of warnings
// (gradle.md §Three sources, merged).
func (s *buildSink) Problem(p gradle.Problem) {
	s.mu.Lock()
	s.problems = append(s.problems, problemRecord(p))
	s.mu.Unlock()
}

// Finished is the terminal event: what ended the build, and what it failed with.
func (s *buildSink) Finished(f gradle.Finished) {
	s.mu.Lock()
	s.fin = f
	for _, p := range f.FailureProblems {
		s.fprobs = append(s.fprobs, problemRecord(p))
	}
	s.failures = f.Failures
	s.mu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	if s.progress != nil {
		s.progress(len(s.tasks), 0, "")
	}
}

// finish returns the terminal event's facts.
func (s *buildSink) finish() gradle.Finished {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fin
}

// counts is the build-level task summary: the last progress event's counts, with the
// task outcomes this build reported folded in when no progress event arrived
// (gradle.md §The build model).
func (s *buildSink) counts() gradle.TaskSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summary
}

func (s *buildSink) console() []extract.Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]extract.Line(nil), s.lines...)
}

func (s *buildSink) problemRecords() []extract.Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]extract.Problem(nil), s.problems...)
}

func (s *buildSink) failureProblemRecords() []extract.Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]extract.Problem(nil), s.fprobs...)
}

func (s *buildSink) failureTree() []extract.Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return extractFailures(s.failures)
}

func (s *buildSink) taskOutcomes() []TaskOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TaskOutcome, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, TaskOutcome{Path: t.Path, Outcome: t.Outcome, DurationMs: t.DurationMs, Reason: t.Reason})
	}
	return out
}

// outcomes is the task path → outcome map the output search needs.
func (s *buildSink) outcomes() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.tasks))
	for _, t := range s.tasks {
		out[t.Path] = t.Outcome
	}
	return out
}

// countTask adds one task's outcome to the summary, the way Gradle's own summary
// counts them: success is an executed task (gradle.md §The build model).
func countTask(sum gradle.TaskSummary, outcome string) gradle.TaskSummary {
	switch outcome {
	case "success":
		sum.Executed++
	case "upToDate":
		sum.UpToDate++
	case "fromCache":
		sum.FromCache++
	case "noSource":
		sum.NoSource++
	case "skipped":
		sum.Skipped++
	case "failed":
		sum.Failed++
	}
	return sum
}

// taskSummary converts the wire counts to the published result's.
func taskSummary(s gradle.TaskSummary) TaskSummary {
	return TaskSummary{
		Executed: s.Executed, UpToDate: s.UpToDate, FromCache: s.FromCache,
		NoSource: s.NoSource, Skipped: s.Skipped, Failed: s.Failed,
	}
}

func problemRecord(p gradle.Problem) extract.Problem {
	return extract.Problem{
		Severity:           p.Severity,
		ProblemID:          p.ProblemID,
		ProblemDisplayName: p.ProblemDisplayName,
		ContextualLabel:    p.ContextualLabel,
		Details:            p.Details,
		File:               p.File,
		Line:               p.Line,
		Column:             p.Column,
		Solutions:          p.Solutions,
		DocumentationLink:  p.DocumentationLink,
	}
}

func extractFailures(fs []gradle.Failure) []extract.Failure {
	if len(fs) == 0 {
		return nil
	}
	out := make([]extract.Failure, 0, len(fs))
	for _, f := range fs {
		out = append(out, extract.Failure{Message: f.Message, Causes: extractFailures(f.Causes)})
	}
	return out
}

func failureNodes(fs []gradle.Failure) []FailureNode {
	if len(fs) == 0 {
		return nil
	}
	out := make([]FailureNode, 0, len(fs))
	for _, f := range fs {
		out = append(out, FailureNode{
			ID: f.ID, Message: f.Message, Description: f.Description,
			Causes: failureNodes(f.Causes), TruncatedCauses: f.TruncatedCauses,
		})
	}
	return out
}

func gradleInfo(conn gradle.Connection) GradleInfo {
	return GradleInfo{Version: conn.GradleVersion, Source: conn.Source, DaemonJavaHome: conn.DaemonJavaHome}
}

// connectorNotice is D19's warning, said once at the head of a build's console: the
// project has no wrapper, so the Tooling API's own Gradle distribution ran the build.
// It is empty for a wrapper build, which is the ordinary case and needs no note.
func connectorNotice(conn gradle.Connection) string {
	if conn.Source != "connector" {
		return ""
	}
	version := conn.GradleVersion
	if version == "" {
		version = "its own"
	}
	return "droidyard: this project has no Gradle wrapper, so the build ran on the Tooling API's own Gradle " +
		version + ", downloaded on first use (about 150 MB). Add a wrapper to build with the project's own Gradle."
}

// gatedCapabilities are the TAPI capabilities ADR-0018's profile degrades on.
var gatedCapabilities = []string{"problemEvents", "problemLocations", "detailedFailure"}

// profileOf is D17 and m1k D9: conservative for a Gradle below the floor (Tier C,
// reachable only through `gradle.allowBelowFloor`: the stable skeleton only, no Problems
// and no detailed failure), full for Gradle 8.13 and newer, degraded below that or when
// a gated capability is missing, with missingCapabilities naming what is missing
// (ADR-0018 §Capability profile values).
func profileOf(conn gradle.Connection, fromSidecar []string) (string, []string) {
	if !gradle.AtLeast(conn.GradleVersion, 8, 0) {
		return "conservative", nil
	}
	missing := []string{}
	seen := map[string]bool{}
	for _, name := range gatedCapabilities {
		if ok, known := conn.Capabilities[name]; known && !ok {
			missing = append(missing, name)
			seen[name] = true
		}
	}
	for _, name := range fromSidecar {
		if name != "" && !seen[name] {
			missing = append(missing, name)
			seen[name] = true
		}
	}
	if !gradle.AtLeast(conn.GradleVersion, 8, 13) || len(missing) > 0 {
		return "degraded", missing
	}
	return "full", missing
}

// validationFor is rule 6: unsupported under the conservative profile (Tier C through
// the hatch), untested otherwise — no Gradle minor is a CI row yet, so tested is never
// produced (ADR-0018; plan m1k D9).
func validationFor(profile string) string {
	if profile == "conservative" {
		return "unsupported"
	}
	return "untested"
}

// pluginVersions is D10: the AGP and KGP versions the build ran with, from the model the
// build fetched, "unknown" when the probe did not read them.
func pluginVersions(p gradle.Plugins) (agp, kgp string) {
	agp, kgp = p.AGP, p.KGP
	if agp == "" {
		agp = "unknown"
	}
	if kgp == "" {
		kgp = "unknown"
	}
	return agp, kgp
}

// expectedSources is D10: the expected primary error source per tool, a pure function of
// the connection's Gradle capabilities and the probe's plugin facts (gradle.md §Version
// detection and toolchain profile, item 4). Kotlin is structured only when Gradle reports
// problem locations and KGP is ≥ 2.4 (an unknown KGP is below); javac follows problem
// locations on its own; the other four are not assumed, so they stay console.
func expectedSources(conn gradle.Connection, plugins gradle.Plugins) ExpectedSources {
	es := ExpectedSources{AAPT2: "console", ManifestMerger: "console", KSP: "console", Kapt: "console"}
	if conn.Capabilities["problemLocations"] {
		es.Javac = "problems"
	} else {
		es.Javac = "console"
	}
	if conn.Capabilities["problemLocations"] && gradle.KGPAtLeast(plugins.KGP, 2, 4) {
		es.Kotlin = "problems"
	} else {
		es.Kotlin = "console"
	}
	return es
}

// checkTasks is R6 for build.run: 1 to 20 tasks, each shaped like a Gradle task name or
// path. It runs before anything is started, so a task list that is not a list of tasks
// never reaches Gradle.
func checkTasks(tasks []string) error {
	if len(tasks) == 0 {
		return capability.NewError(capability.ErrInvalidParams, "build.run needs at least one task").
			WithNextStep("call build.tasks to see what exists, then pass its paths")
	}
	if len(tasks) > 20 {
		return capability.Errorf(capability.ErrInvalidParams, "build.run takes at most 20 tasks, not %d", len(tasks)).
			WithNextStep("split the work into successive calls")
	}
	for _, t := range tasks {
		if !taskPattern.MatchString(t) {
			return capability.Errorf(capability.ErrInvalidParams, "%q is not a Gradle task name or path", t).
				WithDetails(map[string]any{"task": t}).
				WithNextStep("pass a task path such as \":app:assembleDebug\"; build.tasks lists the ones that exist")
		}
	}
	return nil
}

func assembleTask(module, variant string) string {
	name := "assemble" + gradle.ExportVariant(variant)
	if module == "" || module == ":" {
		return ":" + name
	}
	return module + ":" + name
}

func modulePaths(m gradle.Model) []string {
	out := make([]string, 0, len(m.Projects))
	for _, p := range m.Projects {
		out = append(out, p.Path)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func sourceNames(sources []extract.Source) []string {
	if len(sources) == 0 {
		return nil
	}
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, string(s))
	}
	return out
}

func noState() *capability.Error {
	return capability.NewError(capability.ErrBuildNotFound, "this daemon has no build registry configured").
		WithCause("no state store was passed to the build package")
}

func boolPtr(b bool) *bool { return &b }

package build

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/store"
)

// The build registry (ADR-0021 §Layout; plan m1g D13): a finished build is a record at
// ws/<wsid>/builds/<buildId>.json, listed in that directory's index.json, and its
// console is the durable artifact ws/<wsid>/artifacts/<buildId>.log. A running build
// is its operation and no file. The console lives and dies with its record, and a
// console file no record owns is deleted when the workspace is next opened.

// consoleCap is the per-build console cap (gradle.md §Output; plan m1g D24): 32 MiB.
// Bytes past it are dropped, counted and marked in the file; the count is in the
// record. The same bound is the receiver's in-memory bound for the extractor's input.
const consoleCap = 32 << 20

// Retention of the records (gradle.md §Persistence; plan m1g D13): the last 50 builds,
// or 7 days, whichever is stricter, oldest first.
const (
	retainBuilds   = 50
	retainBuildFor = 7 * 24 * time.Hour
)

// MarkerLine is how a console that reached the cap says so (plan m1g D24). The count
// of dropped bytes is also in the record.
func markerLine(cap, dropped int64) string {
	return fmt.Sprintf("--- droidyard: console truncated at %d bytes; %d bytes dropped ---", cap, dropped)
}

// RegistryOptions configure a Registry. Both stores are required for the build
// capabilities to run; a schema-only registry may hold an empty RegistryOptions, whose
// handlers report that no state is configured rather than panicking.
type RegistryOptions struct {
	// Store is the daemon's state store: the records and their indexes live under its
	// root.
	Store *store.Store
	// Artifacts is the artifact store the console is created in and resolved through.
	Artifacts *artifact.Store
	// Log receives a line per console that could not be swept and per record that
	// could not be pruned. Nil discards.
	Log io.Writer
	// Now is the clock retention and timestamps read; nil means time.Now.
	Now func() time.Time
	// Bus is the daemon's event bus: build.output lines are published on it to the
	// build.watchOutput subscriptions (ADR-0044). Nil makes build.watchOutput answer
	// SESSION_REQUIRED and publishes nothing.
	Bus *event.Bus
}

// Registry is the daemon's build history, across every workspace it has built in: the
// records, their index and the console files they own. It is safe for concurrent use.
type Registry struct {
	store *store.Store
	arts  *artifact.Store
	log   io.Writer
	now   func() time.Time

	// mu serialises the index's read-modify-write, so two builds finishing at once
	// cannot lose one another's entry. The records themselves are written by the store,
	// which serialises its own writes.
	mu sync.Mutex
	// running holds the build ids this daemon is running, so the sweep at an open
	// never deletes a console an in-flight build is writing.
	runMu    sync.Mutex
	running  map[string]bool
	consoles map[string]map[string]*liveConsole // running builds' consoles, by workspace and build id

	// bus carries build.output (ADR-0044). Nil publishes nothing.
	bus *event.Bus
	// watchMu guards watches: the build.watchOutput subscriptions, by workspace.
	watchMu sync.Mutex
	watches map[string]map[*watch]struct{}
	// watchHook is a test seam: build.watchOutput runs it after its subscription is
	// registered and before it snapshots the running builds. Nil in production.
	watchHook atomic.Pointer[func()]
	// capOverride, when positive, replaces consoleCap for the consoles created from now
	// on. Test-only.
	capOverride int64
}

// NewRegistry returns an empty registry over opts.
func NewRegistry(o RegistryOptions) *Registry {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Registry{store: o.Store, arts: o.Artifacts, log: o.Log, now: o.Now, running: map[string]bool{}, consoles: map[string]map[string]*liveConsole{},
		bus: o.Bus, watches: map[string]map[*watch]struct{}{}}
}

// configured reports whether the registry has both stores a build capability needs. A
// registry without them serves descriptor-only registries (the schema and the CLI's
// capability listing): its handlers report that no state is configured rather than
// panicking on a nil store.
func (r *Registry) configured() bool {
	return r != nil && r.store != nil && r.arts != nil
}

// TaskOutcome is one task of the finished build: gradle.md §The build model's tasks[],
// with Gradle's own reason verbatim.
type TaskOutcome struct {
	Path       string `json:"path"`
	Outcome    string `json:"outcome"`
	DurationMs int64  `json:"durationMs"`
	Reason     string `json:"reason,omitempty"`
}

// FailureNode is one node of the persisted failure tree, with the description the
// sidecar capped (a Java stack trace for a Gradle failure). It stays in the record:
// results and build.errors show messages only (plan m1g step 2 → step 7).
type FailureNode struct {
	ID              string        `json:"id,omitempty"`
	Message         string        `json:"message"`
	Description     string        `json:"description,omitempty"`
	Causes          []FailureNode `json:"causes,omitempty"`
	TruncatedCauses int           `json:"truncatedCauses,omitempty"`
}

// OutputPath is one APK the record's outputs[] name, with the file's size and mtime as
// the build left them. It is the output-path index device.install resolves against
// after a restart, subject to its own BUILD_OUTPUT_STALE re-check (gradle.md §Build
// outputs, §Install reference). Never published: paths stay in the state root.
type OutputPath struct {
	Module    string    `json:"module"`
	Variant   string    `json:"variant"`
	APK       string    `json:"apk"`
	Path      string    `json:"path"`
	Filters   []string  `json:"filters,omitempty"`
	SizeBytes int64     `json:"sizeBytes"`
	ModTime   time.Time `json:"modTime"`
}

// record is one finished build as ws/<wsid>/builds/<buildId>.json holds it (plan m1g
// D10, D13). It keeps what a published result must not: the whole task list, the
// failure tree with its descriptions, the merged problems with gradle.md's caps, and
// the output-path index.
type record struct {
	BuildID             string     `json:"buildId"`
	Root                string     `json:"root"`
	Request             Request    `json:"request"`
	Outcome             string     `json:"outcome"`
	StartedAt           time.Time  `json:"startedAt"`
	FinishedAt          time.Time  `json:"finishedAt"`
	DurationMs          int64      `json:"durationMs"`
	Gradle              GradleInfo `json:"gradle"`
	Profile             string     `json:"profile"`
	MissingCapabilities []string   `json:"missingCapabilities,omitempty"`
	Validation          string     `json:"validation"`
	AGP                 string     `json:"agp"`
	KGP                 string     `json:"kgp"`
	// Absent in records written before 0.7.0, which then publish no expectedSources.
	ExpectedSources *ExpectedSources `json:"expectedSources,omitempty"`
	TaskSummary     TaskSummary      `json:"taskSummary"`
	Tasks           []TaskOutcome    `json:"tasks,omitempty"`
	Failures        []FailureNode    `json:"failures,omitempty"`
	FailureDropped  int              `json:"failureProblemsDropped,omitempty"`
	Errors          []extract.Record `json:"errors,omitempty"`
	Warnings        []extract.Record `json:"warnings,omitempty"`
	Truncated       int              `json:"truncated,omitempty"`
	Sources         []string         `json:"sources,omitempty"`
	Unrecognised    bool             `json:"unrecognised,omitempty"`
	RootFailures    []string         `json:"rootFailures,omitempty"`
	Tail            string           `json:"tail,omitempty"`
	Outputs         []Output         `json:"outputs,omitempty"`
	OutputPaths     []OutputPath     `json:"outputPaths,omitempty"`
	ConsoleRef      string           `json:"consoleRef"`
	ConsoleBytes    int64            `json:"consoleBytes"`
	ConsoleDropped  int64            `json:"consoleDropped,omitempty"`
}

// index is ws/<wsid>/builds/index.json: the records this state root holds, newest
// first, so lastResult and retention need not read them all.
type index struct {
	Builds []IndexEntry `json:"builds"`
}

// IndexEntry is one record in the index.
type IndexEntry struct {
	BuildID    string    `json:"buildId"`
	Outcome    string    `json:"outcome"`
	FinishedAt time.Time `json:"finishedAt"`
}

func recordFile(wsid, buildID string) string {
	return path.Join("ws", wsid, "builds", buildID+".json")
}

func indexFile(wsid string) string {
	return path.Join("ws", wsid, "builds", "index.json")
}

// markRunning records that this daemon is running build buildID, so the sweep leaves
// its console alone.
func (r *Registry) markRunning(buildID string) {
	r.runMu.Lock()
	r.running[buildID] = true
	r.runMu.Unlock()
}

func (r *Registry) unmarkRunning(buildID string) {
	r.runMu.Lock()
	delete(r.running, buildID)
	r.runMu.Unlock()
}

func (r *Registry) isRunning(buildID string) bool {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	return r.running[buildID]
}

// entries reads a workspace's index. A missing one is an empty list; one this binary
// cannot use — a newer version, a file that did not parse — is reported as an error,
// because guessing at the history would be worse than saying it cannot be read.
func (r *Registry) entries(wsid string) ([]IndexEntry, error) {
	var idx index
	out, err := r.store.Read(indexFile(wsid), store.Version, &idx)
	switch {
	case err != nil:
		return nil, err
	case out == store.Loaded:
		return idx.Builds, nil
	default:
		return nil, nil
	}
}

// persist writes a finished build's record and enters it in the index, then prunes
// what retention drops — the oldest records, by the count and the age bound — deleting
// each dropped record's console with it (plan m1g D13).
func (r *Registry) persist(wsid string, rec record) error {
	if err := r.store.Write(recordFile(wsid, rec.BuildID), rec); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	older, err := r.entries(wsid)
	if err != nil {
		return err
	}
	entries := append([]IndexEntry{{BuildID: rec.BuildID, Outcome: rec.Outcome, FinishedAt: rec.FinishedAt}}, older...)
	cut := r.now().Add(-retainBuildFor)
	keep := make([]IndexEntry, 0, len(entries))
	var drop []IndexEntry
	for i, e := range entries {
		if i >= retainBuilds || e.FinishedAt.Before(cut) {
			drop = append(drop, e)
			continue
		}
		keep = append(keep, e)
	}
	if err := r.store.Write(indexFile(wsid), index{Builds: keep}); err != nil {
		return err
	}
	for _, e := range drop {
		if p, ok := r.localFile(recordFile(wsid, e.BuildID)); ok {
			_ = os.Remove(p)
		}
		if r.arts != nil {
			_ = r.arts.DeleteDurable(wsid, e.BuildID)
		}
	}
	return nil
}

// localFile is the absolute path of a state file, refusing a name that would leave the
// state root. The parts come from the daemon (a workspace id, an operation id), so this
// is defence in depth, not the only guard.
func (r *Registry) localFile(name string) (string, bool) {
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", false
	}
	return filepath.Join(r.store.Root(), filepath.FromSlash(name)), true
}

// load reads one record by build id. A record the state root does not hold, or one
// that did not parse, is BUILD_NOT_FOUND; a file of a newer version is
// WORKSPACE_STATE_UNREADABLE, because the history exists and this binary cannot read
// it.
func (r *Registry) load(wsid, buildID string) (record, error) {
	// Every caller's buildId is checked here, so none can reach a path with it (step 7
	// review B3, step 8 review B1).
	if !buildIDPattern.MatchString(buildID) {
		return record{}, buildNotFound(wsid, buildID)
	}
	var rec record
	out, err := r.store.Read(recordFile(wsid, buildID), store.Version, &rec)
	switch {
	case err != nil:
		return record{}, stateUnreadable(wsid, err)
	case out != store.Loaded:
		return record{}, buildNotFound(wsid, buildID)
	}
	return rec, nil
}

// newest returns the newest finished build of a workspace. A workspace that has
// finished no build is BUILD_NOT_FOUND.
func (r *Registry) newest(wsid string) (record, error) {
	entries, err := r.entries(wsid)
	if err != nil {
		return record{}, stateUnreadable(wsid, err)
	}
	if len(entries) == 0 {
		return record{}, buildNotFound(wsid, "")
	}
	return r.load(wsid, entries[0].BuildID)
}

// Sweep deletes the console files of a workspace that no record owns. It runs when the
// workspace is opened, so an orphan from a daemon that died mid-build is gone before
// the next build of this run writes anything (plan m1g D13). A console a build of this
// run is writing, and one whose record exists on disk, is skipped: the record is
// written before the index names it, so the index alone would call a build that has
// just finished an orphan.
func (r *Registry) Sweep(wsid string) {
	if !r.configured() {
		return
	}
	files, err := r.arts.ListDurable(wsid)
	if err != nil {
		fmt.Fprintf(r.log, "droidyard: workspace %s: the consoles could not be listed (%v)\n", wsid, err)
		return
	}
	if len(files) == 0 {
		return
	}
	entries, err := r.entries(wsid)
	if err != nil {
		// Without the index, nothing can be called owned: leave the consoles alone
		// rather than delete a record's console on a guess.
		fmt.Fprintf(r.log, "droidyard: workspace %s: the build index could not be read (%v); consoles left alone\n", wsid, err)
		return
	}
	owned := make(map[string]bool, len(entries))
	for _, e := range entries {
		owned[e.BuildID] = true
	}
	for _, f := range files {
		if owned[f.Owner] || r.isRunning(f.Owner) || r.hasRecord(wsid, f.Owner) {
			continue
		}
		if err := r.arts.DeleteDurable(wsid, f.Owner); err != nil {
			fmt.Fprintf(r.log, "droidyard: workspace %s: the console %s could not be deleted (%v)\n", wsid, f.Ref, err)
		}
	}
}

// logCancel writes the one line that says which phase a build cancel landed in. Nil-safe:
// a schema-only registry logs nothing.
func (r *Registry) logCancel(buildID, phase string) {
	if r == nil || r.log == nil {
		return
	}
	fmt.Fprintf(r.log, "droidyard: build %s: cancel landed %s\n", buildID, phase)
}

// hasRecord reports whether the record file for a build exists. A record is written
// before its index entry, so a build that finished while this sweep ran has a record
// the index does not name yet — and its console, which no index entry claims, is still
// owned. The record file is the ground truth the index is derived from (D13).
func (r *Registry) hasRecord(wsid, buildID string) bool {
	p, ok := r.localFile(recordFile(wsid, buildID))
	if !ok {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func buildNotFound(wsid, buildID string) *capability.Error {
	if buildID == "" {
		return capability.NewError(capability.ErrBuildNotFound, fmt.Sprintf("workspace %q has no finished build", wsid)).
			WithNextStep("call build.assemble or build.run to build it")
	}
	return capability.Errorf(capability.ErrBuildNotFound, "this workspace has no build %q", buildID).
		WithNextStep("call build.lastResult for the newest build, or build.assemble to start one")
}

func stateUnreadable(wsid string, err error) *capability.Error {
	return capability.Errorf(capability.ErrWorkspaceStateUnreadable, "the build history of workspace %q could not be read", wsid).
		WithCause(err.Error()).
		WithNextStep("check the permissions and free space of the state directory")
}

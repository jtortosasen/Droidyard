package build

import (
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/build/extract"
)

// The published shapes of the build capabilities (gradle.md §The build model,
// §Build outputs; plan m1g D10). The persisted record keeps more than these — the
// whole task list, the failure tree with its descriptions and the output-path index —
// and never leaves the state root (principle 14: no paths in results, D26: no console
// tail in results).

// RunParams is build.run's parameters.
type RunParams struct {
	Tasks []string `json:"tasks" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"The Gradle tasks to run, 1 to 20, each a task path such as \":app:assembleDebug\" or a name such as \"assembleDebug\". Validated against the pattern Gradle accepts before anything is started; build.tasks lists what exists."`
}

// AssembleParams is build.assemble's parameters.
type AssembleParams struct {
	Module  string `json:"module" jsonschema:"required,pattern=^(:[A-Za-z0-9_.-]+)+$" jsonschema_description:"The Gradle project path of the module to assemble, e.g. \":app\". Must be one of project.modules' paths; an unknown one is REQUEST_INVALID_PARAMS listing them."`
	Variant string `json:"variant" jsonschema:"required,pattern=^[a-z][A-Za-z0-9]*$" jsonschema_description:"The variant to assemble, e.g. \"debug\". Must be one of that module's project.variants; an unknown one is REQUEST_INVALID_PARAMS listing them."`
}

// ErrorsParams is build.errors' parameters.
type ErrorsParams struct {
	BuildID         string `json:"buildId,omitempty" jsonschema:"pattern=^op-[0-9a-f]{16}$" jsonschema_description:"Which build's merged errors to return. Absent returns the newest finished build of the workspace (the one build.lastResult names)."`
	IncludeWarnings bool   `json:"includeWarnings,omitempty" jsonschema_description:"Also return the merged warnings. Off by default: up to 50 warning records an agent asking for its errors rarely needs."`
}

// Request is what was asked of Gradle: the tasks, and for build.assemble the module
// and variant they came from (gradle.md §The build model; no args in this milestone,
// plan m1g D11).
type Request struct {
	Tasks   []string `json:"tasks" jsonschema:"required"`
	Module  string   `json:"module,omitempty"`
	Variant string   `json:"variant,omitempty"`
}

// TaskSummary is the build-level view of the task outcomes, as the last progress
// event reported it (gradle.md §The build model).
type TaskSummary struct {
	Executed  int `json:"executed"`
	UpToDate  int `json:"upToDate"`
	FromCache int `json:"fromCache"`
	NoSource  int `json:"noSource"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// FailedTask is one task that ended failed, with Gradle's own reason verbatim
// ("taskSummary gives the build-level view of the same fact").
type FailedTask struct {
	Path   string `json:"path" jsonschema:"required"`
	Reason string `json:"reason,omitempty"`
}

// GradleInfo is the Gradle the build ran on (gradle.md §Daemon JVM). daemonJavaHome is
// absent when Gradle did not report one.
type GradleInfo struct {
	Version        string `json:"version" jsonschema:"required"`
	Source         string `json:"source" jsonschema:"required,enum=wrapper,enum=connector"`
	DaemonJavaHome string `json:"daemonJavaHome,omitempty"`
}

// OutputAPK is one APK of an output (gradle.md §Build outputs). Filters is always
// present: empty is the one unfiltered APK the MVP installs. Supported is false for an
// APK the MVP does not install — one with filters — which device.install then refuses
// BUILD_OUTPUT_UNSUPPORTED.
type OutputAPK struct {
	Name      string   `json:"name" jsonschema:"required"`
	Filters   []string `json:"filters" jsonschema:"required"`
	SizeBytes int64    `json:"sizeBytes" jsonschema:"required"`
	Supported bool     `json:"supported" jsonschema:"required"`
}

// Output is one module and variant's installable output, or the reason there is none
// (gradle.md §Build outputs: missing is reported, not guessed). A reason-only entry
// names no applicationId and has no apks: no metadata, unreadable metadata or no
// applicationId is never guessed at.
type Output struct {
	Module        string      `json:"module" jsonschema:"required"`
	Variant       string      `json:"variant" jsonschema:"required"`
	ApplicationID string      `json:"applicationId,omitempty"`
	VersionCode   *int        `json:"versionCode,omitempty"`
	VersionName   string      `json:"versionName,omitempty"`
	APKs          []OutputAPK `json:"apks,omitempty"`
	Reason        string      `json:"reason,omitempty"`
}

// ExpectedSources is the expected primary error source per tool, derived from the
// connection's Gradle capabilities and the probe's plugin facts (gradle.md §Version
// detection and toolchain profile, item 4; ADR-0018 rule 1; plan m1h D10). Each value is
// "problems" (structured events) or "console" (the extractor). It reports what to
// expect; it never switches the extractor off.
type ExpectedSources struct {
	Kotlin         string `json:"kotlin" jsonschema:"required,enum=problems,enum=console"`
	Javac          string `json:"javac" jsonschema:"required,enum=problems,enum=console"`
	AAPT2          string `json:"aapt2" jsonschema:"required,enum=problems,enum=console"`
	ManifestMerger string `json:"manifestMerger" jsonschema:"required,enum=problems,enum=console"`
	KSP            string `json:"ksp" jsonschema:"required,enum=problems,enum=console"`
	Kapt           string `json:"kapt" jsonschema:"required,enum=problems,enum=console"`
}

// Extraction is where the merged errors came from (gradle.md §The build model): the
// merge sources that produced a record, in precedence order, and whether a failed
// build recognised nothing.
type Extraction struct {
	Sources      []string `json:"sources,omitempty"`
	Unrecognised bool     `json:"unrecognised,omitempty"`
}

// Result is the compact build record build.run, build.assemble and build.lastResult
// return (plan m1g D10). Errors and Warnings are the first five of each; the whole
// list, with gradle.md's caps, is build.errors'.
type Result struct {
	BuildID    string     `json:"buildId" jsonschema:"required" jsonschema_description:"The build's id; it is its operation's id (ADR-0020)."`
	Request    Request    `json:"request" jsonschema:"required"`
	Outcome    string     `json:"outcome" jsonschema:"required,enum=success,enum=failed,enum=cancelled" jsonschema_description:"success, failed (a completed operation whose build failed) or cancelled; a failed build is a completed operation, never an operation error (ADR-0020)."`
	StartedAt  time.Time  `json:"startedAt" jsonschema:"required"`
	FinishedAt time.Time  `json:"finishedAt" jsonschema:"required"`
	DurationMs int64      `json:"durationMs"`
	Gradle     GradleInfo `json:"gradle" jsonschema:"required"`
	// Profile is the toolchain profile this build ran under: full for Gradle 8.13 and
	// newer, degraded below that or when a gated call was unsupported, conservative for a
	// Gradle below the floor admitted by `gradle.allowBelowFloor` (ADR-0018; plan m1g D17,
	// m1k D9).
	Profile             string   `json:"profile" jsonschema:"required,enum=full,enum=degraded,enum=conservative" jsonschema_description:"The toolchain profile this build ran under: full (Gradle 8.13 and newer), degraded (below that, or a gated call Gradle refused), or conservative (a Gradle below the floor, admitted by the configuration key gradle.allowBelowFloor: the stable skeleton only, with no Problems and no detailed failure)."`
	MissingCapabilities []string `json:"missingCapabilities,omitempty" jsonschema_description:"Tooling API capabilities Gradle refused, e.g. problemEvents; the console extractor covers what they would have carried."`
	// Validation is untested for a version at or above the floor, unsupported under the
	// conservative profile (`gradle.allowBelowFloor`); no minor is a CI row yet, so tested
	// is never produced (ADR-0018; plan m1g D17, m1k D9).
	Validation string `json:"validation" jsonschema:"required,enum=untested,enum=unsupported" jsonschema_description:"How much evidence stands behind the result: untested (at or above the floor) or unsupported (a Gradle below the floor, under the conservative profile the configuration key gradle.allowBelowFloor admits)."`
	AGP        string `json:"agp" jsonschema:"required" jsonschema_description:"The Android Gradle Plugin version the probe read, or \"unknown\" when the probe did not read it (plan m1h D10)."`
	KGP        string `json:"kgp" jsonschema:"required" jsonschema_description:"The Kotlin Gradle Plugin version the probe read, or \"unknown\" when the probe did not read it (plan m1h D10)."`
	// ExpectedSources is the expected primary error source per tool, derived from the
	// connection's Gradle capabilities and the plugin versions (plan m1h D10).
	ExpectedSources *ExpectedSources `json:"expectedSources,omitempty" jsonschema_description:"The expected primary error source per tool: problems (structured) or console (the extractor), from the connection's capabilities and the plugin versions."`
	TaskSummary     TaskSummary      `json:"taskSummary" jsonschema:"required"`
	FailedTasks     []FailedTask     `json:"failedTasks,omitempty"`

	ErrorCount   int              `json:"errorCount"`
	WarningCount int              `json:"warningCount"`
	Errors       []extract.Record `json:"errors,omitempty" jsonschema_description:"The first five merged errors; build.errors returns the whole list."`
	Warnings     []extract.Record `json:"warnings,omitempty"`
	Outputs      []Output         `json:"outputs,omitempty"`

	Extraction Extraction `json:"extraction" jsonschema:"required"`
	// ConsoleRef is the build's console, always present once the build has finished:
	// read it with artifact.read (plan m1g D27: never while the build runs).
	ConsoleRef string `json:"consoleRef" jsonschema:"required"`
}

// ErrorsResult is build.errors' result: one build's merged problems with gradle.md's
// caps, and, only when nothing was recognised, the labelled console tail
// (gradle.md §Merge rules 4; plan m1g D26).
type ErrorsResult struct {
	BuildID   string           `json:"buildId" jsonschema:"required"`
	Outcome   string           `json:"outcome" jsonschema:"required,enum=success,enum=failed,enum=cancelled"`
	Errors    []extract.Record `json:"errors" jsonschema:"required"`
	Warnings  []extract.Record `json:"warnings,omitempty"`
	Truncated int              `json:"truncated,omitempty" jsonschema_description:"How many records the caps (200 errors, 50 warnings) dropped."`
	// The unrecognised fallback: the build failed and no source recognised an error.
	Unrecognised bool     `json:"unrecognised,omitempty"`
	RootFailures []string `json:"rootFailures,omitempty"`
	Tail         string   `json:"tail,omitempty" jsonschema_description:"The last 4 KiB of the console, labelled, and present only when unrecognised is true."`
	Sources      []string `json:"sources,omitempty"`
}

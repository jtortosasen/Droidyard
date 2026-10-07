package gradle

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Task is one task of `project.model`'s reply (gradle.md §Project model): what
// build.tasks filters and what build.assemble names.
type Task struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Group       string `json:"group"`
	Description string `json:"description"`
	// Public is Gradle's own visibility: a hidden task has Public false and is listed
	// only when a caller opts in (gradle.md §Task listing).
	Public bool `json:"public"`
}

// Module is one Gradle project of `project.model`'s reply, the root `:` included
// (plan m1g §Changes, step 2 → step 5).
type Module struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Dir is the project directory relative to the root, "." for the root project.
	Dir string `json:"dir"`
	// BuildDir is core-internal (gradle.md §Project model) and never leaves the core.
	BuildDir string `json:"buildDir,omitempty"`
	Tasks    []Task `json:"tasks"`
	// Android is the probe's facts for this project, nil for a project that is not
	// Android or a reply without the probe (plan m1h D6, D7).
	Android *Android `json:"android,omitempty"`
}

// Android is one Android project's probe facts (plan m1h D6, D7). Each field is a
// pointer so an isolated read failure is distinguishable from a read value: the probe
// omits the field it could not read (D6). SDKDir is core-internal and never leaves the
// core; step 4 publishes it as sdk.requirements' project root (m1h D12).
type Android struct {
	CompileTarget *string   `json:"compileTarget,omitempty"`
	BuildTools    *string   `json:"buildTools,omitempty"`
	SDKDir        *string   `json:"sdkDir,omitempty"`
	Variants      []Variant `json:"variants,omitempty"`
}

// Variant is one AGP variant the probe read: its name, its build type and its
// product flavours in dimension order (plan m1h D6).
type Variant struct {
	Name      string   `json:"name"`
	BuildType string   `json:"buildType,omitempty"`
	Flavors   []Flavor `json:"flavors,omitempty"`
}

// Flavor is one product flavour: the dimension it belongs to and its name.
type Flavor struct {
	Dimension string `json:"dimension,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Plugins is the build-wide plugin facts the probe read: the first project in path order
// that reported one; "unknown" when none did (plan m1h D6, D10).
type Plugins struct {
	AGP string `json:"agp"`
	KGP string `json:"kgp"`
}

// Probe is the probe's own status and the notes of the fields it could not read (plan
// m1h D6, D7): status is ok, partial, failed or skipped, and notes are capped by the
// sidecar. An empty status is a reply without the probe.
type Probe struct {
	Status string   `json:"status"`
	Notes  []string `json:"notes"`
}

// Read reports whether the probe produced the model's Android facts: ok, or partial with
// some field isolated (plan m1h D6). failed, skipped and an absent probe status do not.
// It is the one place that decision lives, so project.variants, build.assemble's
// validation and sdk.requirements cannot drift (D8, D9, D12).
func (p Probe) Read() bool { return p.Status == "ok" || p.Status == "partial" }

// Model is the cached project model: one configuration pass's worth of modules and
// tasks, plus the probe's plugin facts and status when it ran (gradle.md §Project model;
// D25: the generic TAPI model plus the probe's Android facts).
type Model struct {
	Root     string   `json:"root"`
	Projects []Module `json:"projects"`
	Plugins  Plugins  `json:"plugins"`
	Probe    Probe    `json:"probe"`
}

// Module returns the module with the given path, e.g. ":app". The root project is ":".
func (m Model) Module(path string) (Module, bool) {
	for _, p := range m.Projects {
		if p.Path == path {
			return p, true
		}
	}
	return Module{}, false
}

// variantSuffixes are the task-name suffixes that mark a non-build variant, excluded
// from the task-name heuristic (ADR-0023 §Decision 4; gradle.md §Variants).
var variantSuffixes = []string{"AndroidTest", "UnitTest", "TestFixtures"}

// Variants is the one variant source every consumer shares (plan m1h D8, D9):
// project.variants, build.assemble's validation and the output search all call it, so a
// flavoured module's validation and outputs follow the exact variants when the probe
// read them.
//
// The probe read the model when its status is ok or partial, and it read a project's
// variants when that project is not Android (an empty list) or the project's Android
// facts carry a variants list. Everything else — no probe, a failed one, or a project
// whose variants field the probe could not read — falls back to the task-name
// heuristic, labelled task-names, exact false.
//
// names is never nil: a module with no variants reports an empty list. details is
// non-nil only for source probe, and mirrors the probe's dimension order.
func Variants(m Module, probe Probe) (names []string, details []Variant, source string) {
	if probe.Read() {
		if m.Android == nil && probe.Status == "ok" {
			// A non-Android project: the probe read every project and this one has no
			// Android facts (D8). Under partial, a missing block may be a project whose
			// read failed, so it falls back (step 3 review B1).
			return []string{}, nil, "probe"
		}
		if m.Android == nil {
			fallback := VariantNames(m)
			if fallback == nil {
				fallback = []string{}
			}
			return fallback, nil, "task-names"
		}
		if m.Android.Variants != nil {
			names = make([]string, 0, len(m.Android.Variants))
			for _, v := range m.Android.Variants {
				names = append(names, v.Name)
			}
			return names, m.Android.Variants, "probe"
		}
	}
	fallback := VariantNames(m)
	if fallback == nil {
		fallback = []string{}
	}
	return fallback, nil, "task-names"
}

// KGPAtLeast reports whether a Kotlin Gradle Plugin version is at least major.minor
// (plan m1h D10). An unreadable, absent or "unknown" version is not at least anything,
// so an unknown KGP is below the structured-error threshold, as D10 requires.
func KGPAtLeast(version string, major, minor int) bool {
	return gradleAtLeast(version, major, minor)
}

// VariantNames is the task-name fallback of gradle.md §Variants: the X with both
// `assemble<X>` and `package<X>` among the module's tasks, minus names ending in
// AndroidTest, UnitTest or TestFixtures (ADR-0023 §Decision 4). Names only, and
// `source: task-names, exact: false`. The order is the module's task order, so it is
// stable across runs.
//
// The variant name is X with its first letter lowercased: AGP capitalises the first
// letter when it builds the task name, so the variant `debug` has the task
// `assembleDebug` (the plan's step 5 check expects `debug`, `release`, and R6's variant
// pattern is lowercase-first).
func VariantNames(m Module) []string {
	packages := map[string]bool{}
	for _, t := range m.Tasks {
		if x, ok := strings.CutPrefix(t.Name, "package"); ok && x != "" {
			packages[x] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, t := range m.Tasks {
		x, ok := strings.CutPrefix(t.Name, "assemble")
		if !ok || x == "" || !packages[x] || seen[x] {
			continue
		}
		if excludedVariant(x) {
			continue
		}
		seen[x] = true
		out = append(out, unexportVariant(x))
	}
	return out
}

// ExportVariant is the inverse of unexportVariant: the task-name stem AGP builds for a
// variant name (`debug` → `Debug`), so build.assemble can name the task it runs
// `<module>:assemble<ExportVariant(variant)>` (plan m1g step 7).
func ExportVariant(v string) string {
	r, size := utf8.DecodeRuneInString(v)
	return string(unicode.ToUpper(r)) + v[size:]
}

// PackageVariant reports the variant a `package<Variant>` task builds, as the variant
// name (`packageFreeDebug` → `freeDebug`), and whether the task is one. It is the task
// side of VariantNames: build outputs are reported only for a variant whose package
// task ran in this build (gradle.md §Build outputs).
func PackageVariant(taskName string) (string, bool) {
	x, ok := strings.CutPrefix(taskName, "package")
	if !ok || x == "" {
		return "", false
	}
	return unexportVariant(x), true
}

// unexportVariant lowercases the first rune of a task-name stem, undoing AGP's
// capitalisation: `Debug` → `debug`, `FreeDebug` → `freeDebug`.
func unexportVariant(x string) string {
	r, size := utf8.DecodeRuneInString(x)
	return string(unicode.ToLower(r)) + x[size:]
}

func excludedVariant(x string) bool {
	for _, suffix := range variantSuffixes {
		if strings.HasSuffix(x, suffix) {
			return true
		}
	}
	return false
}

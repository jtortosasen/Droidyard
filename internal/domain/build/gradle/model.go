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
}

// Model is the cached project model: one configuration pass's worth of modules and
// tasks (gradle.md §Project model; D25: the generic TAPI model, not the probe).
type Model struct {
	Root     string   `json:"root"`
	Projects []Module `json:"projects"`
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

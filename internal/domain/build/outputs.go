package build

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
)

// Build outputs (gradle.md §Build outputs; plan m1g step 7). One source: AGP's
// output-metadata.json. For every module whose package<Variant> task ended success,
// upToDate or fromCache in this build, the core searches the module's
// <buildDir>/outputs/apk/, depth-bounded, for the metadata whose variantName matches,
// and reports the APKs it names — with the size and mtime it saw, for device.install
// to re-check (step 8). No metadata, an unreadable one or no applicationId is a reason
// in the result, never a guess.

// outputMetadata is AGP's output-metadata.json. Only the fields the contract names are
// read; the file's version and elementType are ignored.
type outputMetadata struct {
	ApplicationID string              `json:"applicationId"`
	VariantName   string              `json:"variantName"`
	Elements      []outputMetaElement `json:"elements"`
}

type outputMetaElement struct {
	OutputFile  string   `json:"outputFile"`
	Filters     []string `json:"filters"`
	VersionCode int      `json:"versionCode"`
	VersionName string   `json:"versionName"`
}

// metadataDepth bounds the search under a module's outputs/apk/: AGP writes
// outputs/apk/<variant>/output-metadata.json, and one directory more covers a layout
// that nests it (a split under the variant). Nothing deeper is walked, so a stray
// metadata in a nested tree cannot be reported as this build's.
const metadataDepth = 3

// outputTaskOutcomes are the task outcomes a variant's output may be reported from:
// the task ran, or Gradle confirmed its output is already current (gradle.md §Build
// outputs).
var outputTaskOutcomes = map[string]bool{"success": true, "upToDate": true, "fromCache": true}

// resolveOutputs builds the record's outputs[] and the output-path index behind them.
// outcomes maps a task path to its outcome; only the tasks this build reported are in
// it.
func resolveOutputs(model gradle.Model, outcomes map[string]string) ([]Output, []OutputPath) {
	var outputs []Output
	var paths []OutputPath
	seen := map[string]bool{}
	for _, m := range model.Projects {
		// Only a variant's own package task: AGP also has packageDebugResources,
		// packageDebugRenderscript and the like, which are not variants (the variants are
		// gradle.md §Variants' task-name ones).
		variants := map[string]bool{}
		for _, v := range gradle.VariantNames(m) {
			variants[v] = true
		}
		for _, t := range m.Tasks {
			variant, ok := gradle.PackageVariant(t.Name)
			if !ok || !variants[variant] || seen[m.Path+"\x00"+variant] {
				continue
			}
			if !outputTaskOutcomes[outcomes[t.Path]] {
				// The task did not run and was not up to date in this build, so any
				// metadata on disk describes an earlier build (or none).
				continue
			}
			seen[m.Path+"\x00"+variant] = true
			out, ps := resolveOutput(m, variant)
			outputs = append(outputs, out)
			paths = append(paths, ps...)
		}
	}
	return outputs, paths
}

// resolveOutput reports one module and variant's output, or the reason there is none.
func resolveOutput(m gradle.Module, variant string) (Output, []OutputPath) {
	out := Output{Module: m.Path, Variant: variant}
	if m.BuildDir == "" {
		out.Reason = "the project model does not report this module's build directory, so its outputs cannot be looked for"
		return out, nil
	}
	meta, file, reason := findMetadata(m.BuildDir, variant)
	if reason != "" {
		out.Reason = reason
		return out, nil
	}
	if meta.ApplicationID == "" {
		out.Reason = "the output metadata names no applicationId; it is reported, never guessed"
		return out, nil
	}
	out.ApplicationID = meta.ApplicationID
	if len(meta.Elements) == 0 {
		out.Reason = "the output metadata names no APK"
		return out, nil
	}
	dir := filepath.Dir(file)
	var paths []OutputPath
	for _, el := range meta.Elements {
		if out.VersionCode == nil && el.VersionCode != 0 {
			code := el.VersionCode
			out.VersionCode = &code
			out.VersionName = el.VersionName
		}
		name := filepath.Base(filepath.FromSlash(el.OutputFile))
		if name == "" || name == "." {
			out.Reason = "the output metadata names an APK without a file name"
			return Output{Module: m.Path, Variant: variant, ApplicationID: meta.ApplicationID, Reason: out.Reason}, nil
		}
		apk := filepath.Join(dir, name)
		fi, err := os.Stat(apk)
		if err != nil || !fi.Mode().IsRegular() {
			out.Reason = "the APK " + name + " the output metadata names is not there"
			return out, nil
		}
		out.APKs = append(out.APKs, OutputAPK{
			Name:      name,
			Filters:   append([]string{}, el.Filters...),
			SizeBytes: fi.Size(),
			Supported: len(el.Filters) == 0,
		})
		paths = append(paths, OutputPath{
			Module: m.Path, Variant: variant, APK: name, Path: apk,
			Filters: el.Filters, SizeBytes: fi.Size(), ModTime: fi.ModTime(),
		})
	}
	if len(out.APKs) == 0 {
		out.Reason = "the output metadata names no readable APK"
		return out, nil
	}
	return out, paths
}

// findMetadata searches buildDir/outputs/apk/, depth-bounded, for the
// output-metadata.json whose variantName is variant. It returns the metadata and the
// file it was read from; a reason is returned instead when there is none or it cannot
// be read.
func findMetadata(buildDir, variant string) (outputMetadata, string, string) {
	root := filepath.Join(buildDir, "outputs", "apk")
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if depth(rel) >= metadataDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "output-metadata.json" {
			found = append(found, p)
		}
		return nil
	})
	if len(found) == 0 {
		return outputMetadata{}, "", "no output-metadata.json under the module's build directory names variant " + strconv.Quote(variant)
	}
	sort.Strings(found)
	var read int
	for _, p := range found {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var meta outputMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.VariantName != variant {
			continue
		}
		read++
		if read == 1 {
			return meta, p, ""
		}
	}
	if read > 1 {
		return outputMetadata{}, "", "more than one output-metadata.json under the module's build directory names variant " + strconv.Quote(variant) + ": which one describes this build is ambiguous"
	}
	return outputMetadata{}, "", "no output-metadata.json under the module's build directory names variant " + strconv.Quote(variant)
}

// depth is how many directories deep rel is: "." is 0, "debug" is 1.
func depth(rel string) int {
	if rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// The AAPT2 merged-resource layout step 2 recorded (m1g D12; ADR-0022's amendment).
const (
	blameDirName  = "merged_res_blame_folder"
	mergedDirName = "merged.dir"
	// excerptLen caps the quoted merged line (D12 option 2: at most 300 characters).
	excerptLen = 300
)

// mergedResolver resolves the merged paths AAPT2 reports to what a developer can act
// on (extract.MergedResolver; m1g D12). It is built once per build from the project
// model's build directories, and every lookup is best-effort: a field it cannot
// recover is omitted, never guessed.
type mergedResolver struct {
	buildDirs []string
}

// newMergedResolver returns a resolver over the model's modules. Nil when no module
// reports a build directory: there is then nothing to look in.
func newMergedResolver(model gradle.Model) *mergedResolver {
	var dirs []string
	for _, m := range model.Projects {
		if m.BuildDir != "" {
			dirs = append(dirs, m.BuildDir)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	return &mergedResolver{buildDirs: dirs}
}

// blameLog is one entry of a multi-v2 blame file: the merged file it describes and the
// runs that produced it.
type blameLog struct {
	OutputFile string     `json:"outputFile"`
	Map        []blameMap `json:"map"`
}

type blameMap struct {
	Source string    `json:"source"`
	From   blameSpan `json:"from"`
	To     blameSpan `json:"to"`
}

// blameSpan is one side of a blame run. AGP writes each field as a comma-separated
// list of run starts; the entries of from and to are paired by index.
type blameSpan struct {
	StartLines   string `json:"startLines"`
	StartColumns string `json:"startColumns"`
}

type blameFile struct {
	Logs []blameLog `json:"logs"`
}

// ResolveBlame resolves a reported blame path to the developer's source file, and its
// 1-based line and column (m1g D12 option 1). AGP writes more than one log per merged
// file and, at level 4 (step 7), they disagreed about the same line from one build to
// the next, so a candidate counts only when its source line reads the same as the
// merged line AAPT2 reported; with no merged line to compare, or no candidate that
// matches, ok is false: the merged path stays verbatim and no source is invented.
func (r *mergedResolver) ResolveBlame(blamePath string, line int) (string, int, int, bool) {
	path, rel, _ := strings.Cut(blamePath, ":")
	if path == "" || rel == "" || line < 1 {
		return "", 0, 0, false
	}
	merged, ok := r.MergedLine(blamePath, line)
	if !ok || strings.TrimSpace(merged) == "" {
		return "", 0, 0, false
	}
	want := strings.TrimSpace(merged)
	for _, f := range r.blameFiles(fileNameOnly(strings.TrimPrefix(rel, "/"))) {
		for _, m := range blameMaps(f, blamePath) {
			i := indexOf(strconv.Itoa(line-1), m.To.StartLines)
			if i < 0 || m.Source == "" {
				continue
			}
			src, ok := nth(m.From.StartLines, i)
			if !ok {
				continue
			}
			srcLine, err := strconv.Atoi(src)
			if err != nil {
				continue
			}
			// AGP's blame lines and columns are 0-based; the record's are 1-based.
			text, ok := lineAt(m.Source, srcLine+1)
			if !ok || strings.TrimSpace(text) != want {
				continue
			}
			col := 0
			if c, ok := nth(m.From.StartColumns, i); ok {
				if n, err := strconv.Atoi(c); err == nil {
					col = n + 1
				}
			}
			return m.Source, srcLine + 1, col, true
		}
	}
	return "", 0, 0, false
}

// MergedLine returns the merged file's line for a reported blame path: the text of
// the line the developer cannot otherwise see (m1g D12 option 2). It is cut at 300
// characters. The merged file is located from the blame file that described the same
// merge — both live under the merge task's own directory tree — so a build whose merge
// wrote neither resolves neither.
func (r *mergedResolver) MergedLine(blamePath string, line int) (string, bool) {
	_, rel, found := strings.Cut(blamePath, ":")
	rel = strings.TrimPrefix(rel, "/")
	if !found || rel == "" || line < 1 || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", false
	}
	for _, f := range r.blameFiles(fileNameOnly(rel)) {
		if len(blameMaps(f, blamePath)) == 0 {
			continue
		}
		// f is <intermediates>/merged_res_blame_folder/<variant>/<merge task>/out/multi-v2/<dir>.json;
		// the merged file is <intermediates>/incremental/<variant>/<merge task>/merged.dir/<rel>
		// (spike S1 run 2, F3).
		task := filepath.Dir(filepath.Dir(filepath.Dir(f)))
		variant := filepath.Dir(task)
		intermediates := filepath.Dir(filepath.Dir(variant))
		merged := filepath.Join(intermediates, "incremental", filepath.Base(variant), filepath.Base(task), mergedDirName, filepath.FromSlash(rel))
		text, ok := lineAt(merged, line)
		if !ok {
			continue
		}
		if len(text) > excerptLen {
			text = text[:excerptLen]
		}
		return text, true
	}
	return "", false
}

// fileNameOnly is the first path element of a reported merged path; it is the name of
// the multi-v2 file AGP wrote for it (`values/values.xml` → `values`).
func fileNameOnly(rel string) string {
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

// blameFiles lists the multi-v2 blame files a reported path's directory can have:
// <buildDir>/intermediates/merged_res_blame_folder/<variant>/merge<Variant>Resources/out/multi-v2/<dir>.json,
// for every module of the build and every variant directory it holds.
func (r *mergedResolver) blameFiles(dir string) []string {
	var out []string
	for _, buildDir := range r.buildDirs {
		base := filepath.Join(buildDir, "intermediates", blameDirName)
		variants, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, v := range variants {
			if !v.IsDir() {
				continue
			}
			vdir := filepath.Join(base, v.Name())
			merges, err := os.ReadDir(vdir)
			if err != nil {
				continue
			}
			for _, m := range merges {
				if !m.IsDir() {
					continue
				}
				p := filepath.Join(vdir, m.Name(), "out", "multi-v2", dir+".json")
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					out = append(out, p)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// blameMaps reads a multi-v2 file and returns the map entries of every log describing
// the reported merged path: AGP 8.13 writes it bare and again prefixed with a directory
// of its own (the Gradle daemon's) (spike S1 run 2, F3).
func blameMaps(file, blamePath string) []blameMap {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var bf blameFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return nil
	}
	var out []blameMap
	for _, log := range bf.Logs {
		if log.OutputFile == blamePath || strings.HasSuffix(log.OutputFile, "/"+blamePath) {
			out = append(out, log.Map...)
		}
	}
	return out
}

// lineAt returns the text of a 1-based line of a file.
func lineAt(file string, line int) (string, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// A blame file is AGP's undocumented state: a line out of range (0, negative, past
	// the end) is a miss, never a panic.
	if line < 1 || line > len(lines) {
		return "", false
	}
	return strings.TrimRight(lines[line-1], "\r"), true
}

// indexOf finds want among a comma-separated AGP list.
func indexOf(want, list string) int {
	for i, s := range strings.Split(list, ",") {
		if s == want {
			return i
		}
	}
	return -1
}

// nth returns the i-th entry of a comma-separated AGP list.
func nth(list string, i int) (string, bool) {
	parts := strings.Split(list, ",")
	if i < 0 || i >= len(parts) {
		return "", false
	}
	return parts[i], true
}

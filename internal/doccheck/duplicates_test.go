package doccheck

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file checks the duplicates that stay after the plan m1q-one-source-of-truth
// (Delegated choices D1 and D2): facts that a document must repeat, which are
// therefore compared with their source, and the ADR policy that an accepted ADR is
// no longer amended.

// amendedFrozen is the number of "- Amended:" header lines each ADR had when D2 was
// adopted (2026-10-08). An accepted ADR is not edited again: a changed decision is a
// new ADR with "Supersedes:". Any count above this map, whatever the date on the
// line, fails. A new ADR is absent from the map, so it may carry none.
var amendedFrozen = map[string]int{
	"0001-core-runtime-go.md":                         3,
	"0003-unified-capability-api.md":                  1,
	"0005-reuse-platform-tools.md":                    1,
	"0006-web-ui-no-electron.md":                      1,
	"0010-capability-statefulness.md":                 2,
	"0012-visual-artifacts-as-refs.md":                4,
	"0015-untrusted-data-authorization.md":            4,
	"0017-gradle-execution-strategy.md":               4,
	"0018-gradle-version-support-policy.md":           4,
	"0019-process-topology-daemon-and-shims.md":       6,
	"0020-operation-contract.md":                      7,
	"0021-persistence-format-and-location.md":         8,
	"0022-build-error-extraction.md":                  2,
	"0023-android-project-facts-and-build-outputs.md": 2,
	"0024-project-name-and-identifiers.md":            2,
	"0025-mcp-adapter-implementation.md":              3,
	"0026-workspace-addressing-invalid-roots.md":      3,
	"0028-websocket-library.md":                       1,
	"0029-ui-reader-dex.md":                           2,
	"0030-persistent-ui-reader.md":                    2,
	"0031-configuration-file-trust-first.md":          1,
	"0032-configuration-file-v1.md":                   4,
	"0033-emulator-launch-owned-until-serial.md":      2,
	"0034-device-calls-report-busy-crash-dialog.md":   1,
}

var (
	supersedesRef = regexp.MustCompile(`ADR-(\d{4})( §)?`)
	adrRef        = regexp.MustCompile(`ADR-(\d{4})`)
)

// checkAmendedFrozen fails when an ADR header holds more "Amended:" lines than the
// frozen map allows (D2).
func checkAmendedFrozen(fsys fs.FS, frozen map[string]int) []string {
	byNum, problems, err := adrFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var names []string
	for _, files := range byNum {
		names = append(names, files...)
	}
	sort.Strings(names)
	for _, name := range names {
		p := adrDir + "/" + name
		d, err := readDoc(fsys, p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		if n := len(headerField(d, "Amended")); n > frozen[name] {
			problems = append(problems, fmt.Sprintf("%s: %d \"Amended:\" lines, %d allowed. An accepted ADR is not edited again (plan m1q D2): write a new ADR with \"Supersedes: ADR-NNNN\" (or \"Supersedes: ADR-NNNN §Section\") and give this one a \"Superseded by\" or \"Superseded in part by\" line", p, n, frozen[name]))
		}
	}
	return problems
}

// checkSupersession checks the pointer both ways (D2): an ADR whose Status is
// "Superseded by ADR-X", or whose header says "Superseded in part by: ADR-X", is
// named by a "Supersedes:" line in ADR-X, and every "Supersedes:" line names an ADR
// that carries the matching pointer back.
func checkSupersession(fsys fs.FS) []string {
	byNum, _, err := adrFiles(fsys)
	if err != nil {
		return nil
	}
	type header struct {
		file       string
		whole      int   // ADR number in "Superseded by ADR-X", or 0
		partial    []int // ADR numbers in "Superseded in part by:" lines
		supWhole   []int // "Supersedes: ADR-N"
		supPartial []int // "Supersedes: ADR-N §Section"
		doc        *doc
	}
	heads := map[int]*header{}
	for n, files := range byNum {
		if len(files) != 1 {
			continue
		}
		d, err := readDoc(fsys, adrDir+"/"+files[0])
		if err != nil {
			continue
		}
		h := &header{file: adrDir + "/" + files[0], doc: d}
		if st := adrStatus(d); len(st) == 1 {
			if m := supersededBy.FindStringSubmatch(st[0]); m != nil {
				h.whole, _ = strconv.Atoi(m[1])
			}
		}
		for _, v := range headerField(d, "Superseded in part by") {
			for _, m := range adrRef.FindAllStringSubmatch(v, -1) {
				k, _ := strconv.Atoi(m[1])
				h.partial = append(h.partial, k)
			}
		}
		for _, v := range headerField(d, "Supersedes") {
			for _, m := range supersedesRef.FindAllStringSubmatch(v, -1) {
				k, _ := strconv.Atoi(m[1])
				if m[2] != "" {
					h.supPartial = append(h.supPartial, k)
				} else {
					h.supWhole = append(h.supWhole, k)
				}
			}
		}
		heads[n] = h
	}
	has := func(list []int, n int) bool { return indexInts(list, n) >= 0 }
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	nums := make([]int, 0, len(heads))
	for n := range heads {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		h := heads[n]
		if k := h.whole; k != 0 {
			if o := heads[k]; o != nil && !has(o.supWhole, n) {
				add("%s: Status says \"Superseded by ADR-%04d\" but %s has no \"- Supersedes: ADR-%04d\" header line. The pointer must exist both ways (plan m1q D2)", h.file, k, o.file, n)
			}
		}
		for _, k := range h.partial {
			if o := heads[k]; o == nil {
				add("%s: \"Superseded in part by\" names ADR-%04d, which does not exist", h.file, k)
			} else if !has(o.supPartial, n) {
				add("%s: \"Superseded in part by\" names ADR-%04d but %s has no \"- Supersedes: ADR-%04d §<Section>\" header line. The pointer must exist both ways (plan m1q D2)", h.file, k, o.file, n)
			}
		}
		for _, k := range h.supWhole {
			if o := heads[k]; o == nil {
				add("%s: \"Supersedes\" names ADR-%04d, which does not exist", h.file, k)
			} else if o.whole != n {
				add("%s: \"Supersedes: ADR-%04d\" but %s does not have \"Status: Superseded by ADR-%04d\". Set it, and the index row with it (plan m1q D2)", h.file, k, o.file, n)
			}
		}
		for _, k := range h.supPartial {
			if o := heads[k]; o == nil {
				add("%s: \"Supersedes\" names ADR-%04d, which does not exist", h.file, k)
			} else if !has(o.partial, n) {
				add("%s: \"Supersedes: ADR-%04d §<Section>\" but %s has no \"- Superseded in part by: ADR-%04d\" header line (plan m1q D2)", h.file, k, o.file, n)
			}
		}
	}
	return problems
}

func indexInts(list []int, n int) int {
	for i, v := range list {
		if v == n {
			return i
		}
	}
	return -1
}

// plannedCodes are error codes that documents name but no Go constant defines yet.
// An entry needs a register row that says it is deferred.
var plannedCodes = map[string]bool{
	// docs/roadmap/known-limits.md: the row on the build queue's limit (deferred).
	"BUILD_QUEUE_FULL": true,
}

var errorCodeConst = regexp.MustCompile(`ErrorCode = "([A-Z][A-Z0-9_]*)"`)

// TestErrorCodesInDocsExist keeps the codes that documents mention honest: the
// meaning of each code lives on its constant in internal/capability/error.go and its
// descriptors, so a document that names a code (in the live documentation, the
// register, AGENTS.md or .claude/) may name only one of those, or a planned one.
func TestErrorCodesInDocsExist(t *testing.T) {
	report(t, checkErrorCodes(repo(), plannedCodes))
}

func errorCodes(fsys fs.FS) (map[string]bool, error) {
	files, err := goFiles(fsys)
	if err != nil {
		return nil, err
	}
	codes := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f, "internal/") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		for _, m := range errorCodeConst.FindAllStringSubmatch(string(raw), -1) {
			codes[m[1]] = true
		}
	}
	return codes, nil
}

// dupFile reports whether a markdown file is one whose repeated facts are checked
// against their source: the live documentation, the register, AGENTS.md, .claude/.
func dupFile(p string) bool {
	return liveCorpusFile(p) || p == registerPath || p == "AGENTS.md" || strings.HasPrefix(p, ".claude/")
}

func checkErrorCodes(fsys fs.FS, planned map[string]bool) []string {
	codes, err := errorCodes(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	if len(codes) == 0 {
		return []string{"no ErrorCode constants found under internal/; the error-code check has nothing to compare with"}
	}
	prefixes := map[string]bool{}
	for c := range codes {
		prefixes[c[:strings.Index(c+"_", "_")]] = true
	}
	var alts []string
	for p := range prefixes {
		alts = append(alts, p)
	}
	sort.Strings(alts)
	token := regexp.MustCompile(`\b(?:` + strings.Join(alts, "|") + `)_[A-Z0-9_]*[A-Z0-9]\b`)

	files, err := markdownFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, f := range files {
		if !dupFile(f) {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return []string{err.Error()}
		}
		for i, l := range strings.Split(string(raw), "\n") {
			for _, tok := range token.FindAllString(l, -1) {
				if !codes[tok] && !planned[tok] {
					problems = append(problems, fmt.Sprintf("%s:%d: %s is not an error code. The codes are the ErrorCode constants under internal/ (internal/capability/error.go); fix the spelling, or add it to plannedCodes in %s with the register row that defers it", f, i+1, tok, path.Base(selfFile)))
				}
			}
		}
	}
	return problems
}

const selfFile = "internal/doccheck/duplicates_test.go"

var (
	restatedCount   = regexp.MustCompile(`\b\d+ (ADRs|decisions|capabilities|MCP tools)\b`)
	restatedVersion = regexp.MustCompile(`\bAPI \d+\.\d+\.\d+\b`)
)

// TestNoRestatedCounts keeps the numbers that change with every milestone out of the
// prose: the number of capabilities and the API version are in schema/capabilities.json,
// the number of ADRs in docs/adr/README.md, and a copy only goes stale.
func TestNoRestatedCounts(t *testing.T) {
	report(t, checkRestatedCounts(repo()))
}

func checkRestatedCounts(fsys fs.FS) []string {
	files, err := markdownFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, f := range files {
		if !liveCorpusFile(f) && f != "AGENTS.md" {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return []string{err.Error()}
		}
		for i, l := range strings.Split(string(raw), "\n") {
			for _, re := range []*regexp.Regexp{restatedCount, restatedVersion} {
				for _, m := range re.FindAllString(l, -1) {
					problems = append(problems, fmt.Sprintf("%s:%d: %q restates a number that has one home (schema/capabilities.json for capabilities, tools and the API version; docs/adr/README.md for the ADRs). Say it without the number", f, i+1, m))
				}
			}
		}
	}
	return problems
}

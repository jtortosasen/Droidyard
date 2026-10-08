package doccheck

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The places a capability name is still written by hand. The registry is the source of
// truth for what exists (ADR-0016), and schema/capabilities.json is its published form:
// the golden-file test in internal/schema holds it to the registry that test builds, and
// cmd/droidyard's test holds it to the registry the binary ships. The roadmap table is
// the source of truth for what the MVP contains; the rest of mvp.md and the not-exposed
// list of the MCP interface document restate names, and are checked against them. The
// capability list, the classification table and the tool table are not documents any more:
// the descriptors and the schema say all of it.
const (
	mvpPath    = "docs/roadmap/mvp.md"
	mcpPath    = "docs/interfaces/mcp.md"
	schemaPath = "schema/capabilities.json"
)

// capabilityName is the registry's name rule (capability-api.md §Naming and
// namespaces).
var (
	capabilityName = regexp.MustCompile(`^[a-z]+\.[a-z][A-Za-z0-9]*$`)
	verbName       = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

func publishedNames(fsys fs.FS) ([]string, error) {
	raw, err := fs.ReadFile(fsys, schemaPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Capabilities []struct {
			Name string `json:"name"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %v", schemaPath, err)
	}
	var out []string
	for _, c := range doc.Capabilities {
		out = append(out, c.Name)
	}
	return out, nil
}

// TestCapabilityNamesAgree checks that every place a capability name is written by
// hand names the same set. It asserts the registry as single source of truth
// (ADR-0016) and closes the drift the pre-implementation review found as N2, "the
// capability names disagree across documents".
func TestCapabilityNamesAgree(t *testing.T) {
	published, err := publishedNames(repo())
	if err != nil {
		t.Fatal(err)
	}
	report(t, checkCapabilityNames(repo(), published))
}

func set(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func domainOf(name string) string { return name[:strings.IndexByte(name, '.')] }

func domains(names map[string]bool) map[string]bool {
	out := map[string]bool{}
	for n := range names {
		out[domainOf(n)] = true
	}
	return out
}

// only returns the members of a that are not in b, sorted.
func only(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// findSection locates a section, or records that it is missing: a renamed heading must
// fail here rather than make the check silently pass on an empty set.
func findSection(d *doc, title string, problems *[]string) (int, int, bool) {
	from, to, ok := d.section(title)
	if !ok {
		*problems = append(*problems, fmt.Sprintf(
			"%s: no heading titled %q. The capability-name check reads the names from it; if the heading was renamed, update internal/doccheck/capabilities_test.go to match",
			d.path, title))
	}
	return from, to, ok
}

// mvpSurface reads docs/roadmap/mvp.md §MVP capability surface: domain, verbs, phase.
func mvpSurface(d *doc, problems *[]string) (names map[string]bool, phase map[string]string) {
	names, phase = map[string]bool{}, map[string]string{}
	from, to, ok := findSection(d, "MVP capability surface", problems)
	if !ok {
		return
	}
	rows, lines := d.table(from, to)
	if len(rows) == 0 {
		*problems = append(*problems, fmt.Sprintf("%s: §MVP capability surface has no table", d.path))
	}
	for i, row := range rows {
		if len(row) < 3 {
			*problems = append(*problems, fmt.Sprintf("%s:%d: a row of §MVP capability surface has %d cells; expected | Domain | Capabilities | Phase |", d.path, lineNo(lines[i]), len(row)))
			continue
		}
		dom := codeSpans(row[0])
		if len(dom) != 1 || !verbName.MatchString(dom[0]) {
			*problems = append(*problems, fmt.Sprintf("%s:%d: the Domain cell %q must be one domain in backticks, such as `device`", d.path, lineNo(lines[i]), row[0]))
			continue
		}
		verbs := codeSpans(row[1])
		if len(verbs) == 0 {
			*problems = append(*problems, fmt.Sprintf("%s:%d: the Capabilities cell for `%s` lists no verb in backticks", d.path, lineNo(lines[i]), dom[0]))
		}
		for _, v := range verbs {
			n := dom[0] + "." + v
			if !capabilityName.MatchString(n) {
				*problems = append(*problems, fmt.Sprintf("%s:%d: `%s` does not match the name rule %s (capability-api.md §Naming and namespaces)", d.path, lineNo(lines[i]), n, capabilityName))
				continue
			}
			names[n] = true
			phase[n] = row[2]
		}
	}
	return
}

func checkCapabilityNames(fsys fs.FS, published []string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	pub := set(published)

	mvp, err := readDoc(fsys, mvpPath)
	if err != nil {
		return append(problems, err.Error())
	}
	surface, phase := mvpSurface(mvp, &problems)
	if len(surface) == 0 {
		return problems
	}

	// Everything the core publishes is part of the MVP surface.
	for _, n := range only(pub, surface) {
		add("%s §MVP capability surface: `%s` is registered and published in %s but is not in the table. Add it to the `%s` row; a capability outside the MVP needs an ADR first (AGENTS.md §Scope discipline)",
			mvpPath, n, schemaPath, domainOf(n))
	}

	// mvp.md restates the surface three more times.
	if from, to, ok := findSection(mvp, "In scope", &problems); ok {
		found := false
		for i := from; i < to; i++ {
			if !strings.Contains(mvp.lines[i], "**Capability domains**") {
				continue
			}
			found = true
			text := mvp.lines[i]
			for j := i + 1; j < to && strings.HasPrefix(mvp.lines[j], "  "); j++ {
				text += " " + mvp.lines[j]
			}
			listed := set(codeSpans(text))
			for _, dm := range only(domains(surface), listed) {
				add("%s:%d: §In scope **Capability domains** does not list `%s`, which has a row in §MVP capability surface. Add it, or remove the row", mvpPath, lineNo(i), dm)
			}
			for _, dm := range only(listed, domains(surface)) {
				add("%s:%d: §In scope **Capability domains** lists `%s`, which has no row in §MVP capability surface. Add the row, or remove the domain", mvpPath, lineNo(i), dm)
			}
		}
		if !found {
			add("%s §In scope: no **Capability domains** item. The capability-name check reads it; if it was reworded, update internal/doccheck/capabilities_test.go", mvpPath)
		}
	}
	if from, to, ok := findSection(mvp, "The golden path", &problems); ok {
		rows, lines := mvp.table(from, to)
		for i, row := range rows {
			for _, c := range codeSpans(row[len(row)-1]) {
				if capabilityName.MatchString(c) && !surface[c] {
					add("%s:%d: the golden path uses `%s`, which is not in §MVP capability surface. Correct the name, or add it to the table", mvpPath, lineNo(lines[i]), c)
				}
			}
		}
	}
	if h, ok := headingWithPrefix(mvp, "Phase 1 "); !ok {
		add("%s: no heading starting \"Phase 1 \". The capability-name check reads its deliverables; if it was renamed, update internal/doccheck/capabilities_test.go", mvpPath)
	} else {
		from, to, _ := mvp.section(h)
		for _, it := range bulletItems(mvp, from, to) {
			spans := codeSpans(it.text)
			if len(spans) == 0 || !strings.HasSuffix(spans[0], ".*") {
				continue
			}
			dom := strings.TrimSuffix(spans[0], ".*")
			for _, s := range spans[1:] {
				n := ""
				switch {
				case capabilityName.MatchString(s):
					n = s
				case verbName.MatchString(s):
					n = dom + "." + s
				default:
					continue
				}
				switch {
				case !surface[n]:
					add("%s:%d: Phase 1 lists `%s` as a deliverable, but it is not in §MVP capability surface. Correct the name, or add it to the table", mvpPath, lineNo(it.line), n)
				case !strings.Contains(phase[n], "1"):
					add("%s:%d: Phase 1 lists `%s` as a deliverable, but §MVP capability surface puts it in phase %q. Change one of them", mvpPath, lineNo(it.line), n, phase[n])
				}
			}
		}
	}

	// mcp.md: every capability it lists as not exposed is a published capability, so a
	// renamed or removed one cannot leave a stale exclusion behind.
	mcp, err := readDoc(fsys, mcpPath)
	if err != nil {
		return append(problems, err.Error())
	}
	if from, to, ok := findSection(mcp, "What is exposed, and what is not", &problems); ok {
		rows, lines := mcp.table(from, to)
		if len(rows) == 0 {
			add("%s §What is exposed, and what is not: no table of the capabilities that are not exposed. The capability-name check reads it", mcpPath)
		}
		for i, row := range rows {
			for _, c := range codeSpans(row[0]) {
				if capabilityName.MatchString(c) && !pub[c] {
					add("%s:%d: `%s` is listed as not exposed but is not published in %s. Correct the name, or remove the row", mcpPath, lineNo(lines[i]), c, schemaPath)
				}
			}
		}
	}
	return problems
}

func headingWithPrefix(d *doc, prefix string) (string, bool) {
	for _, h := range d.headings() {
		if strings.HasPrefix(h.text, prefix) {
			return h.text, true
		}
	}
	return "", false
}

type item struct {
	text string
	line int
}

// bulletItems returns each top-level "- " list item in [from, to), with its
// continuation lines joined.
func bulletItems(d *doc, from, to int) []item {
	var out []item
	for i := from; i < to; i++ {
		if d.inFence[i] || !strings.HasPrefix(d.lines[i], "- ") {
			continue
		}
		text := d.lines[i]
		for j := i + 1; j < to && strings.HasPrefix(d.lines[j], "  ") && !d.inFence[j]; j++ {
			text += " " + strings.TrimSpace(d.lines[j])
		}
		out = append(out, item{text: text, line: i})
	}
	return out
}

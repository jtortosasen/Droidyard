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

// The places a capability name is written by hand. The registry is the source of
// truth for what exists (ADR-0016), and schema/capabilities.json is its published
// form: the golden-file test in internal/schema holds it to the registry that test
// builds, and cmd/droidyard's test holds it to the registry the binary ships. The
// roadmap table is the source of truth for what the MVP contains, and the other
// documents restate the table.
const (
	mvpPath    = "docs/roadmap/mvp.md"
	apiPath    = "docs/architecture/capability-api.md"
	mcpPath    = "docs/interfaces/mcp-tool-surface.md"
	schemaPath = "schema/capabilities.json"
)

// capabilityName is the registry's name rule (capability-api.md §Naming and
// namespaces); toolName is its MCP spelling, one underscore in place of the dot.
var (
	capabilityName = regexp.MustCompile(`^[a-z]+\.[a-z][A-Za-z0-9]*$`)
	toolName       = regexp.MustCompile(`^[a-z]+_[a-z][A-Za-z0-9]*$`)
	verbName       = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	nameInText     = regexp.MustCompile(`\b[a-z]+\.[a-z][A-Za-z0-9]*\b`)
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

	// capability-api.md: the name list, the classification table and the domains.
	api, err := readDoc(fsys, apiPath)
	if err != nil {
		return append(problems, err.Error())
	}
	postMVP := map[string]bool{}
	if from, to, ok := findSection(api, "Naming and namespaces", &problems); ok {
		listed := map[string]bool{}
		fenced := false
		for i := from; i < to; i++ {
			if !api.inFence[i] {
				continue
			}
			fenced = true
			for _, n := range nameInText.FindAllString(api.lines[i], -1) {
				if strings.Contains(api.lines[i], "post-MVP") {
					postMVP[n] = true
				} else {
					listed[n] = true
				}
			}
		}
		if !fenced {
			add("%s §Naming and namespaces: no fenced name list. The capability-name check reads it; if it moved, update internal/doccheck/capabilities_test.go", apiPath)
		}
		for _, n := range only(surface, listed) {
			add("%s §Naming and namespaces: `%s` is in %s §MVP capability surface but not in the name list. Add it to the list, or remove it from the table", apiPath, n, mvpPath)
		}
		for _, n := range only(listed, surface) {
			add("%s §Naming and namespaces: `%s` is in the name list but not in %s §MVP capability surface. Add it to the table, or mark its line (post-MVP), or remove it", apiPath, n, mvpPath)
		}
		for n := range postMVP {
			if surface[n] {
				add("%s §Naming and namespaces: `%s` is on a (post-MVP) line but is in %s §MVP capability surface. Change one of them", apiPath, n, mvpPath)
			}
		}
	}
	if from, to, ok := findSection(api, "Classification of the MVP surface", &problems); ok {
		rows, lines := api.table(from, to)
		classified := map[string]bool{}
		for i, row := range rows {
			for _, c := range codeSpans(row[0]) {
				if !capabilityName.MatchString(c) {
					continue
				}
				if classified[c] {
					add("%s:%d: `%s` is classified twice in §Classification of the MVP surface. Keep one row", apiPath, lineNo(lines[i]), c)
				}
				classified[c] = true
			}
		}
		for _, n := range only(surface, classified) {
			add("%s §Classification of the MVP surface: `%s` is in %s §MVP capability surface but has no classification row. Add one", apiPath, n, mvpPath)
		}
		for _, n := range only(classified, surface) {
			add("%s §Classification of the MVP surface: `%s` is classified but is not in %s §MVP capability surface. Add it to the table, or remove the row", apiPath, n, mvpPath)
		}
	}
	if from, to, ok := findSection(api, "Initial domains", &problems); ok {
		rows, lines := api.table(from, to)
		listed := map[string]bool{}
		for i, row := range rows {
			spans := codeSpans(row[0])
			if len(spans) != 1 {
				add("%s:%d: the Domain cell %q of §Initial domains must be one domain in backticks", apiPath, lineNo(lines[i]), row[0])
				continue
			}
			listed[spans[0]] = true
		}
		want := domains(surface)
		for dm := range domains(postMVP) {
			want[dm] = true
		}
		for _, dm := range only(want, listed) {
			add("%s §Initial domains: domain `%s` has names in §Naming and namespaces or in %s §MVP capability surface, but no row here. Add the row", apiPath, dm, mvpPath)
		}
		for _, dm := range only(listed, want) {
			add("%s §Initial domains: domain `%s` has a row here but no names in §Naming and namespaces or in %s §MVP capability surface. Remove the row, or add its names", apiPath, dm, mvpPath)
		}
	}

	// mcp-tool-surface.md: every MVP capability is a tool, except the listed ones.
	mcp, err := readDoc(fsys, mcpPath)
	if err != nil {
		return append(problems, err.Error())
	}
	notExposed := map[string]bool{}
	if from, to, ok := findSection(mcp, "What is exposed, and what is not", &problems); ok {
		rows, lines := mcp.table(from, to)
		for i, row := range rows {
			for _, c := range codeSpans(row[0]) {
				if !capabilityName.MatchString(c) {
					continue
				}
				notExposed[c] = true
				if !surface[c] {
					add("%s:%d: `%s` is listed as not exposed but is not in %s §MVP capability surface. Correct the name, or add it to the table", mcpPath, lineNo(lines[i]), c, mvpPath)
				}
			}
		}
	}
	if from, to, ok := findSection(mcp, "The MVP tool surface", &problems); ok {
		rows, lines := mcp.table(from, to)
		tools := map[string]bool{}
		for i, row := range rows {
			spans := codeSpans(row[0])
			if len(spans) != 1 || !toolName.MatchString(spans[0]) {
				add("%s:%d: the Tool cell %q must be one MCP tool name in backticks, such as `device_list`", mcpPath, lineNo(lines[i]), row[0])
				continue
			}
			n := strings.Replace(spans[0], "_", ".", 1)
			if tools[n] {
				add("%s:%d: `%s` has two rows in §The MVP tool surface. Keep one", mcpPath, lineNo(lines[i]), spans[0])
			}
			tools[n] = true
			if notExposed[n] {
				add("%s:%d: `%s` has a row in §The MVP tool surface but §What is exposed, and what is not says `%s` is not exposed. Change one of them", mcpPath, lineNo(lines[i]), spans[0], n)
			}
		}
		for _, n := range only(surface, tools) {
			if notExposed[n] {
				continue
			}
			add("%s §The MVP tool surface: `%s` is in %s §MVP capability surface but has no tool row `%s`. Add the row, or list it under §What is exposed, and what is not with the reason",
				mcpPath, n, mvpPath, strings.Replace(n, ".", "_", 1))
		}
		for _, n := range only(tools, surface) {
			add("%s §The MVP tool surface: tool `%s` maps to `%s`, which is not in %s §MVP capability surface. Add it to the table, or remove the row",
				mcpPath, strings.Replace(n, ".", "_", 1), n, mvpPath)
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

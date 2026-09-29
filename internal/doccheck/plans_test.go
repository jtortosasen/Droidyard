package doccheck

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	plansDir   = "docs/plans"
	plansIndex = "docs/plans/README.md"
)

// planStatuses and stepFields come from .opencode/skills/milestone-plan/templates/plan.md.
var (
	planStatuses = map[string]bool{"Draft": true, "Blocked": true, "Reviewed": true, "Approved": true, "Done": true}
	stepFields   = []string{"Builds", "Implements", "Check", "Schema", "Needs", "Not here"}

	stepHeading = regexp.MustCompile(`^(\d+)\.\s+\S`)
	doneLine    = regexp.MustCompile(`^- \[( |x|X)\] Done\. Evidence:(.*)$`)
	acceptItem  = regexp.MustCompile(`^- \[( |x|X)\] `)
	provenBy    = regexp.MustCompile(`Proven by:(.*)$`)
	number      = regexp.MustCompile(`\d+`)
	placeholder = regexp.MustCompile(`^<[^>]*>$`)
)

// TestPlansFollowTheTemplate checks the rules of the milestone plan template that
// need no judgement: an allowed status, every step with its fields and its Done line,
// a ticked step with its evidence, and every acceptance item naming a step that
// exists. Whether each step is the right one is the plan reviewer's call.
func TestPlansFollowTheTemplate(t *testing.T) {
	report(t, checkPlans(repo()))
}

// TestPlansIndexMatchesFiles checks docs/plans/README.md §Index against the plans:
// one row per plan, every row pointing at a plan that exists, and the Status column
// saying what the plan says.
func TestPlansIndexMatchesFiles(t *testing.T) {
	report(t, checkPlansIndex(repo()))
}

func planFiles(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, plansDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "README.md" {
			out = append(out, plansDir+"/"+e.Name())
		}
	}
	return out, nil
}

// continued joins a list item's continuation lines, the indented lines after it.
func continued(d *doc, i, to int) string {
	var b strings.Builder
	for j := i + 1; j < to && strings.HasPrefix(d.lines[j], "  ") && !d.inFence[j]; j++ {
		b.WriteString(" " + strings.TrimSpace(d.lines[j]))
	}
	return b.String()
}

func checkPlans(fsys fs.FS) []string {
	files, err := planFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, p := range files {
		d, err := readDoc(fsys, p)
		if err != nil {
			add("%s: %v", p, err)
			continue
		}

		switch st := headerField(d, "Status"); {
		case len(st) != 1:
			add("%s: expected one \"- Status:\" line in the header, found %d. Use one of Draft | Blocked | Reviewed | Approved | Done", p, len(st))
		case !planStatuses[st[0]]:
			add("%s: \"Status: %s\" is not one of Draft | Blocked | Reviewed | Approved | Done (milestone-plan template)", p, st[0])
		}

		steps := map[int]bool{}
		from, to, ok := d.section("Steps")
		if !ok {
			add("%s: no \"## Steps\" section (milestone-plan template)", p)
		} else {
			var heads []heading
			for _, h := range d.headings() {
				if h.line >= from && h.line < to && h.level == 3 {
					heads = append(heads, h)
				}
			}
			for k, h := range heads {
				at := fmt.Sprintf("%s:%d", p, lineNo(h.line))
				m := stepHeading.FindStringSubmatch(h.text)
				if m == nil {
					add("%s: step heading %q must be \"### <n>. <title>\"", at, h.text)
					continue
				}
				n, _ := strconv.Atoi(m[1])
				if n != k+1 {
					add("%s: the step heading says %d but it is heading %d under ## Steps. Number steps 1, 2, 3… in order", at, n, k+1)
				}
				steps[n] = true

				end := to
				if k+1 < len(heads) {
					end = heads[k+1].line
				}
				have := map[string]bool{}
				done := false
				for i := h.line + 1; i < end; i++ {
					if d.inFence[i] {
						continue
					}
					l := d.lines[i]
					for _, f := range stepFields {
						if strings.HasPrefix(l, "- "+f+":") {
							have[f] = true
						}
					}
					dm := doneLine.FindStringSubmatch(l)
					if dm == nil {
						continue
					}
					done = true
					evidence := strings.TrimSpace(dm[2] + continued(d, i, end))
					if dm[1] != " " && (evidence == "" || placeholder.MatchString(evidence)) {
						add("%s:%d: step %d is ticked but its Evidence is empty. Write the command and its decisive output, or untick it", p, lineNo(i), n)
					}
				}
				for _, f := range stepFields {
					if !have[f] {
						add("%s: step %d has no \"- %s:\" line (milestone-plan template)", at, n, f)
					}
				}
				if !done {
					add("%s: step %d has no \"- [ ] Done. Evidence:\" line (milestone-plan template)", at, n)
				}
			}
		}

		if from, to, ok := d.section("Acceptance"); !ok {
			add("%s: no \"## Acceptance\" section (milestone-plan template)", p)
		} else {
			for i := from; i < to; i++ {
				if d.inFence[i] || !acceptItem.MatchString(d.lines[i]) {
					continue
				}
				text := d.lines[i] + continued(d, i, to)
				m := provenBy.FindStringSubmatch(text)
				if m == nil {
					add("%s:%d: the acceptance item does not say which step proves it. End it with \"Proven by: step <n>\"", p, lineNo(i))
					continue
				}
				nums := number.FindAllString(m[1], -1)
				if len(nums) == 0 {
					add("%s:%d: \"Proven by:\" names no step number", p, lineNo(i))
				}
				for _, s := range nums {
					if n, _ := strconv.Atoi(s); !steps[n] {
						add("%s:%d: the acceptance item is proven by step %d, which does not exist under ## Steps", p, lineNo(i), n)
					}
				}
			}
		}
	}
	return problems
}

func checkPlansIndex(fsys fs.FS) []string {
	files, err := planFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	index, err := readDoc(fsys, plansIndex)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	from, to, ok := index.section("Index")
	if !ok {
		return []string{plansIndex + ": no heading titled \"Index\"; the plan index check reads the table under it"}
	}
	rows, lines := index.table(from, to)
	listed := map[string]bool{}
	for i, row := range rows {
		m := linkRE.FindStringSubmatch(maskCode(row[0]))
		if m == nil {
			continue // the "none yet" row
		}
		at := fmt.Sprintf("%s:%d", plansIndex, lineNo(lines[i]))
		p, ok := resolve(plansIndex, m[1])
		if !ok || !exists(fsys, p) {
			add("%s: the row links to %s, which does not exist", at, m[1])
			continue
		}
		listed[p] = true
		d, err := readDoc(fsys, p)
		if err != nil {
			continue
		}
		if st := headerField(d, "Status"); len(st) == 1 && (len(row) < 3 || row[2] != st[0]) {
			got := ""
			if len(row) >= 3 {
				got = row[2]
			}
			add("%s: the index says %s is %q but the plan says \"- Status: %s\". The plan is authoritative; correct the row", at, p, got, st[0])
		}
	}
	for _, p := range files {
		if !listed[p] {
			add("%s §Index: %s has no row. Add | [%s](%s) | <milestone> | <status> |", plansIndex, p, strings.TrimPrefix(p, plansDir+"/"), strings.TrimPrefix(p, plansDir+"/"))
		}
	}
	return problems
}

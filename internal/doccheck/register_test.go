package doccheck

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

const registerPath = "docs/roadmap/known-limits.md"

var (
	registerColumns = []string{"Item", "Kind", "Area", "Source", "Recorded", "Closed by"}
	// registerKinds are in the order rows are sorted within an Area.
	registerKinds = []string{"limit", "unverified", "deferred", "open question"}
	// registerAreas are in case-insensitive alphabetical order, which is the order
	// rows are sorted in.
	registerAreas = []string{"build", "CLI", "code", "configuration", "device", "device/UI", "emulator", "files", "MCP/agents", "platform", "policy/security", "process", "SDK", "web UI"}

	milestoneRE = regexp.MustCompile(`^\d+[a-z]$`)
	closedByRE  = regexp.MustCompile(`^\d+[a-z]\b`)
	// registerLinkRE is a markdown link with a target, which Source must hold.
	registerLinkRE = regexp.MustCompile(`\[[^\]]+\]\([^)\s]+\)`)
)

// TestRegisterIsWellFormed checks docs/roadmap/known-limits.md, the one home of open
// items (AGENTS.md §Where each fact lives): six columns; Kind one of four; Area from
// a closed set; Source a link; Recorded a milestone or "design"; Closed by empty or
// starting with a milestone; rows ordered by Area, then Kind, as its intro says.
func TestRegisterIsWellFormed(t *testing.T) {
	report(t, checkRegister(repo()))
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func checkRegister(fsys fs.FS) []string {
	d, err := readDoc(fsys, registerPath)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	for i, l := range d.lines {
		if !d.inFence[i] && strings.HasPrefix(strings.TrimSpace(l), "|") {
			if h := splitRow(l); strings.Join(h, "|") != strings.Join(registerColumns, "|") {
				add("%s:%d: the table's columns are %q; they must be %q, in this order", registerPath, lineNo(i), h, registerColumns)
			}
			break
		}
	}
	rows, lines := d.table(0, len(d.lines))
	if len(rows) == 0 {
		add("%s: no table of rows found", registerPath)
	}
	prevArea, prevKind := -1, -1
	for r, row := range rows {
		at := fmt.Sprintf("%s:%d", registerPath, lineNo(lines[r]))
		if len(row) != len(registerColumns) {
			add("%s: the row has %d cells; it needs %d (%s). A literal | in the item goes inside a code span", at, len(row), len(registerColumns), strings.Join(registerColumns, ", "))
			continue
		}
		item, kind, area, source, recorded, closed := row[0], row[1], row[2], row[3], row[4], row[5]
		if item == "" {
			add("%s: the row has an empty Item", at)
		}
		k := indexOf(registerKinds, kind)
		if k < 0 {
			add("%s: Kind %q is not one of %s", at, kind, strings.Join(registerKinds, " | "))
		}
		a := indexOf(registerAreas, area)
		if a < 0 {
			add("%s: Area %q is not one of %s. A new area is a decision: add it to this check and to the register's intro", at, area, strings.Join(registerAreas, " | "))
		}
		if !registerLinkRE.MatchString(source) {
			add("%s: Source %q holds no link. Link the ADR, plan or research note that raised the item, or the area document", at, source)
		}
		if recorded != "design" && !milestoneRE.MatchString(recorded) {
			add("%s: Recorded %q must be a milestone such as 1n, or design", at, recorded)
		}
		if closed != "" && !closedByRE.MatchString(closed) {
			add("%s: Closed by %q must be empty or start with a milestone such as 1p", at, closed)
		}
		if k < 0 || a < 0 {
			continue
		}
		if a < prevArea || (a == prevArea && k < prevKind) {
			add("%s: the row (Area %s, Kind %s) comes after one that sorts later. Order rows by Area (%s), then Kind (%s)",
				at, area, kind, strings.Join(registerAreas, ", "), strings.Join(registerKinds, ", "))
		}
		prevArea, prevKind = a, k
	}
	return problems
}

// liveBudget is the most words the live documentation set may hold (wc -w, counted
// with strings.Fields). Milestone 1q fixed the measure at 42,775 words and aims for
// 0.6 of that; each of its merge steps lowers the constant to what the step reached. The set is
// every docs/**/*.md except docs/adr/, docs/plans/, docs/research/, docs/reviews/ and
// the register, which grows with what is open. Raised by 105 words (25,650 to 25,755) in
// milestone 1o step 3: the manual that left 31 tool descriptions (selectors, the golden
// order, never repeat an action, the shell and wrapper rules) is now the server
// instructions block in docs/interfaces/mcp.md, which `adapters/mcp` must equal verbatim
// and cannot be kept shorter than the points it carries (ADR-0036 Decision 1). Raised by 312
// (to 26,067) in milestone 2c step 1: the 2026-10-10 Phase 2 reorganisation (12 words net
// after cuts) and about 300 for the live documents 2c's steps 3, 4, 8, 9 and 11 change.
// Raised by 250 (to 26,317) in milestone 2g step 1: files.md's section on the five new
// operations, mcp.md's batch case and mvp.md's file row (ADR-0053, ADR-0054).
// Raised by 29 (to 26,346) in milestone 2k step 6: the `agent` domain's rows in mcp.md and
// mvp.md (ADR-0063). Raised by 31 (to 26,377) in milestone 2k step 7: mcp.md's hooks
// paragraph and table row name the observation (ADR-0063). Raised by 24 (to 26,401) in
// milestone 2l step 3: mvp.md's git row (`watch`, `read`, human-only) and mcp.md's
// not-exposed row for git.read (ADR-0066).
const liveBudget = 26401

// TestLiveCorpusWithinBudget keeps the live documents from growing back. A document
// that needs more words has to say what it cuts, or the budget is raised in a change
// that explains why.
func TestLiveCorpusWithinBudget(t *testing.T) {
	n, problems := checkCorpusBudget(repo(), liveBudget)
	t.Logf("live documentation: %d words, budget %d", n, liveBudget)
	report(t, problems)
}

func liveCorpusFile(p string) bool {
	if !strings.HasPrefix(p, "docs/") || p == registerPath {
		return false
	}
	for _, dir := range []string{adrDir + "/", plansDir + "/", "docs/research/", "docs/reviews/"} {
		if strings.HasPrefix(p, dir) {
			return false
		}
	}
	return path.Ext(p) == ".md"
}

func checkCorpusBudget(fsys fs.FS, budget int) (int, []string) {
	files, err := markdownFiles(fsys)
	if err != nil {
		return 0, []string{err.Error()}
	}
	total := 0
	for _, f := range files {
		if !liveCorpusFile(f) {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return 0, []string{err.Error()}
		}
		total += len(strings.Fields(string(raw)))
	}
	if total > budget {
		return total, []string{fmt.Sprintf("the live documentation holds %d words; the budget is %d. Cut %d words, or raise liveBudget in a change that says why", total, budget, total-budget)}
	}
	return total, nil
}

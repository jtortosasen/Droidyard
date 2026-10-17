package doccheck

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	adrDir   = "docs/adr"
	adrIndex = "docs/adr/README.md"
)

var (
	adrFile      = regexp.MustCompile(`^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
	adrTitle     = regexp.MustCompile(`^# ADR-(\d{4}): \S`)
	adrIndexLink = regexp.MustCompile(`^\[(\d{4})\]\(([^)]+)\)$`)
	supersededBy = regexp.MustCompile(`^Superseded by ADR-(\d{4})$`)
	datePrefix   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\b`)
)

// adrSections are the four sections every ADR has, in this order
// (docs/adr/README.md §Format). Other sections may sit between them.
var adrSections = []string{"Context", "Decision", "Consequences", "Alternatives considered"}

// placeholders are bodies that fill a section without saying anything.
var placeholders = map[string]bool{
	"": true, "tbd": true, "todo": true, "none": true, "n/a": true, "na": true,
	"-": true, "—": true, "…": true, "...": true, "none considered": true,
}

// TestADRIndexMatchesFiles checks docs/adr/README.md §Index against the files: every
// ADR has one row, every row points at a file that exists, no number is used twice,
// and the Status column says what the file says. Gaps are allowed: parallel lanes
// reserve numbers (work-board), and one a lane drops, or merges later, is a gap.
func TestADRIndexMatchesFiles(t *testing.T) {
	report(t, checkADRIndex(repo()))
}

// TestADRsAreWellFormed checks each ADR against docs/adr/README.md §Format: a title
// carrying its own number, a Status with an allowed value, a Date, dated Amended
// lines, and the four sections, with an Alternatives considered that says something.
// AGENTS.md: "A rejected option with a recorded reason is not an invitation to
// re-propose it" holds only if the reason is recorded.
func TestADRsAreWellFormed(t *testing.T) {
	report(t, checkADRFormat(repo()))
	report(t, checkAmendedFrozen(repo(), amendedFrozen))
}

// adrFiles returns the ADR files by number, and a problem for any markdown file in
// docs/adr/ that is neither the index nor a well-named ADR.
func adrFiles(fsys fs.FS) (map[int][]string, []string, error) {
	entries, err := fs.ReadDir(fsys, adrDir)
	if err != nil {
		return nil, nil, err
	}
	byNum := map[int][]string{}
	var problems []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		m := adrFile.FindStringSubmatch(name)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s/%s: an ADR file must be named NNNN-short-title.md (lower case, hyphens); rename it", adrDir, name))
			continue
		}
		n, _ := strconv.Atoi(m[1])
		byNum[n] = append(byNum[n], name)
	}
	return byNum, problems, nil
}

// adrStatus returns the value of the "- Status:" line in an ADR's header.
func adrStatus(d *doc) []string {
	return headerField(d, "Status")
}

// headerField returns every value of "- <field>:" in the lines before the first
// "## " heading.
func headerField(d *doc, field string) []string {
	var out []string
	prefix := "- " + field + ":"
	for i, l := range d.lines {
		if strings.HasPrefix(l, "## ") && !d.inFence[i] {
			break
		}
		if strings.HasPrefix(l, prefix) {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(l, prefix)))
		}
	}
	return out
}

func checkADRIndex(fsys fs.FS) []string {
	byNum, problems, err := adrFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	nums := make([]int, 0, len(byNum))
	for n, files := range byNum {
		nums = append(nums, n)
		if len(files) > 1 {
			add("%s: ADR number %04d is used by %d files (%s). Renumber all but one to the next free number and fix the index", adrDir, n, len(files), strings.Join(files, ", "))
		}
	}
	sort.Ints(nums)

	index, err := readDoc(fsys, adrIndex)
	if err != nil {
		return append(problems, err.Error())
	}
	from, to, ok := index.section("Index")
	if !ok {
		return append(problems, adrIndex+": no heading titled \"Index\"; the ADR index check reads the table under it")
	}
	rows, lines := index.table(from, to)
	listed := map[int]int{}
	prev := 0
	for i, row := range rows {
		at := fmt.Sprintf("%s:%d", adrIndex, lineNo(lines[i]))
		if len(row) < 3 {
			add("%s: an index row has %d cells; expected | ADR | Title | Status |", at, len(row))
			continue
		}
		m := adrIndexLink.FindStringSubmatch(row[0])
		if m == nil {
			add("%s: the ADR cell %q must be a link [NNNN](NNNN-short-title.md)", at, row[0])
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if listed[n] > 0 {
			add("%s: ADR %04d has a second row (the first is at line %d). Remove one", at, n, listed[n])
		}
		listed[n] = lineNo(lines[i])
		if n <= prev {
			add("%s: ADR %04d is listed after %04d. Keep the index in number order", at, n, prev)
		}
		prev = n
		target := m[2]
		if am := adrFile.FindStringSubmatch(target); am == nil || am[1] != m[1] {
			add("%s: the row for %04d links to %q, whose name does not start with %04d-. Link the ADR's own file", at, n, target, n)
		}
		if !exists(fsys, adrDir+"/"+target) {
			want := "no ADR file has that number"
			if files := byNum[n]; len(files) > 0 {
				want = "the file is " + files[0]
			}
			add("%s: the row for %04d links to %s, which does not exist; %s", at, n, target, want)
			continue
		}
		d, err := readDoc(fsys, adrDir+"/"+target)
		if err != nil {
			add("%s: %v", at, err)
			continue
		}
		if st := adrStatus(d); len(st) == 1 && st[0] != row[2] {
			add("%s: the index says ADR %04d is %q but %s/%s says \"- Status: %s\". The file is authoritative; correct the index row", at, n, row[2], adrDir, target, st[0])
		}
	}
	for _, n := range nums {
		if listed[n] == 0 {
			add("%s §Index: %s/%s has no row. Add | [%04d](%s) | <title> | <status> | in number order", adrIndex, adrDir, byNum[n][0], n, byNum[n][0])
		}
	}
	return problems
}

func checkADRFormat(fsys fs.FS) []string {
	byNum, problems, err := adrFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	var nums []int
	for n := range byNum {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	for _, n := range nums {
		for _, name := range byNum[n] {
			p := adrDir + "/" + name
			d, err := readDoc(fsys, p)
			if err != nil {
				add("%s: %v", p, err)
				continue
			}

			if m := adrTitle.FindStringSubmatch(d.lines[0]); m == nil {
				add("%s:1: the first line must be \"# ADR-%04d: <title>\"; it is %q", p, n, d.lines[0])
			} else if m[1] != fmt.Sprintf("%04d", n) {
				add("%s:1: the title says ADR-%s but the file is number %04d. Make them agree", p, m[1], n)
			}

			switch st := adrStatus(d); len(st) {
			case 0:
				add("%s: no \"- Status:\" line in the header. Add one: Proposed | Accepted | Superseded by ADR-XXXX | Deprecated", p)
			case 1:
				v := st[0]
				if m := supersededBy.FindStringSubmatch(v); m != nil {
					if k, _ := strconv.Atoi(m[1]); len(byNum[k]) == 0 {
						add("%s: \"Status: %s\" names an ADR that does not exist", p, v)
					}
				} else if v != "Proposed" && v != "Accepted" && v != "Deprecated" {
					add("%s: \"Status: %s\" is not an allowed value. Use Proposed | Accepted | Superseded by ADR-XXXX | Deprecated (docs/adr/README.md §Format)", p, v)
				}
			default:
				add("%s: %d \"- Status:\" lines in the header; keep one", p, len(st))
			}

			dates := headerField(d, "Date")
			if len(dates) != 1 {
				add("%s: expected one \"- Date: YYYY-MM-DD\" line in the header, found %d", p, len(dates))
			} else if !isDate(dates[0], true) {
				add("%s: \"Date: %s\" is not YYYY-MM-DD", p, dates[0])
			}
			for _, a := range headerField(d, "Amended") {
				if !isDate(a, false) {
					add("%s: \"Amended: %s\" must start with the date of the amendment, YYYY-MM-DD", p, clip(a, 40))
				}
			}

			last := -1
			for _, want := range adrSections {
				line, count := -1, 0
				for _, h := range d.headings() {
					if h.level == 2 && h.text == want {
						count++
						if line < 0 {
							line = h.line
						}
					}
				}
				switch {
				case count == 0:
					add("%s: no \"## %s\" section. Every ADR has ## Context, ## Decision, ## Consequences and ## Alternatives considered (docs/adr/README.md §Format)", p, want)
					continue
				case count > 1:
					add("%s: \"## %s\" appears %d times; keep one", p, want, count)
				}
				if line < last {
					add("%s:%d: \"## %s\" comes before a section it should follow. The order is Context, Decision, Consequences, Alternatives considered", p, lineNo(line), want)
				}
				last = line
				from, to, _ := d.section(want)
				body := strings.ToLower(strings.TrimSpace(strings.Join(d.lines[from:to], "\n")))
				body = strings.TrimRight(body, ".")
				if placeholders[body] {
					add("%s:%d: \"## %s\" is empty or a placeholder (%q). Record what it is meant to record", p, lineNo(line), want, body)
				}
			}
		}
	}
	return append(problems, checkSupersession(fsys)...)
}

func isDate(s string, whole bool) bool {
	m := datePrefix.FindStringSubmatch(s)
	if m == nil || (whole && m[1] != s) {
		return false
	}
	_, err := time.Parse("2006-01-02", m[1])
	return err == nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

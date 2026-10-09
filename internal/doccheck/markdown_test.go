package doccheck

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// repoRoot is the repository, seen from this package's directory.
const repoRoot = "../.."

func repo() fs.FS { return os.DirFS(repoRoot) }

// report turns a checker's findings into test failures, one per finding, so every
// drift is listed at once rather than the first one only.
func report(t *testing.T, problems []string) {
	t.Helper()
	for _, p := range problems {
		t.Error(p)
	}
}

// doc is one markdown file, split into lines, with fenced code blocks marked so that
// nothing inside a fence is read as a heading, a table or a link.
type doc struct {
	path    string
	lines   []string
	inFence []bool
}

func readDoc(fsys fs.FS, p string) (*doc, error) {
	raw, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil, err
	}
	return parseDoc(p, string(raw)), nil
}

func parseDoc(p, text string) *doc {
	d := &doc{path: p, lines: strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")}
	d.inFence = make([]bool, len(d.lines))
	fence := ""
	for i, l := range d.lines {
		t := strings.TrimSpace(l)
		if fence == "" {
			if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
				fence = t[:3]
				d.inFence[i] = true
			}
			continue
		}
		d.inFence[i] = true
		if strings.HasPrefix(t, fence) {
			fence = ""
		}
	}
	return d
}

type heading struct {
	level int
	text  string
	line  int // 0-based index into doc.lines
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

func (d *doc) headings() []heading {
	var out []heading
	for i, l := range d.lines {
		if d.inFence[i] {
			continue
		}
		if m := headingRE.FindStringSubmatch(l); m != nil {
			out = append(out, heading{level: len(m[1]), text: m[2], line: i})
		}
	}
	return out
}

// section returns the line range of the body under the heading whose text is exactly
// title, up to the next heading of the same or a higher level.
func (d *doc) section(title string) (from, to int, ok bool) {
	hs := d.headings()
	for i, h := range hs {
		if h.text != title {
			continue
		}
		end := len(d.lines)
		for _, next := range hs[i+1:] {
			if next.level <= h.level {
				end = next.line
				break
			}
		}
		return h.line + 1, end, true
	}
	return 0, 0, false
}

// table returns the rows of the first table in lines [from, to), header excluded,
// each row split into trimmed cells. The separator row is skipped.
func (d *doc) table(from, to int) (rows [][]string, lines []int) {
	started := false
	header := true
	for i := from; i < to; i++ {
		l := strings.TrimSpace(d.lines[i])
		if d.inFence[i] || !strings.HasPrefix(l, "|") {
			if started {
				break
			}
			continue
		}
		started = true
		cells := splitRow(l)
		if header {
			header = false
			continue
		}
		if isSeparator(cells) {
			continue
		}
		rows = append(rows, cells)
		lines = append(lines, i)
	}
	return rows, lines
}

// splitRow splits a table row on pipes that are not inside an inline code span.
func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	l = strings.TrimSuffix(l, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	for _, r := range l {
		switch {
		case r == '`':
			inCode = !inCode
			cur.WriteRune(r)
		case r == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if strings.Trim(c, ":- ") != "" {
			return false
		}
	}
	return true
}

var codeSpanRE = regexp.MustCompile("`([^`]+)`")

// codeSpans returns the contents of every inline code span in s.
func codeSpans(s string) []string {
	var out []string
	for _, m := range codeSpanRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// lineNo is the 1-based line number for a message.
func lineNo(i int) int { return i + 1 }

// thirdPartyDirs are vendored copies of other projects' text (plan m2a step 1: Svelte's
// two agent skills, MIT, kept verbatim at a pinned commit). They are not this
// repository's documentation and cite documents that are not here, so no check reads
// them.
var thirdPartyDirs = []string{
	".claude/skills/svelte-core-bestpractices",
	".claude/skills/svelte-code-writer",
}

// skipDir reports whether the walk leaves a directory unread: .git, the cloned research
// material under .refs/ (git-ignored), parallel workers' worktrees under
// .claude/worktrees/ (git-ignored), any node_modules (pi's extensions under .pi/npm/,
// git-ignored), and the vendored third-party directories.
func skipDir(p string) bool {
	if p == ".git" || p == ".refs" || path.Base(p) == "node_modules" || p == ".claude/worktrees" {
		return true
	}
	for _, d := range thirdPartyDirs {
		if p == d {
			return true
		}
	}
	return false
}

// markdownFiles lists the repository's own markdown (see skipDir for what it leaves out).
func markdownFiles(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && skipDir(p) {
			return fs.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(p, ".md") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// goFiles lists the repository's Go sources.
func goFiles(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && skipDir(p) {
			return fs.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func exists(fsys fs.FS, p string) bool {
	_, err := fs.Stat(fsys, p)
	return err == nil
}

// resolve joins a relative link target onto the directory of the file that holds it.
// It reports false when the target leaves the repository.
func resolve(from, target string) (string, bool) {
	p := path.Join(path.Dir(from), target)
	if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(target, "/") {
		return p, false
	}
	return p, true
}

// slug is GitHub's anchor for a heading: lower case, punctuation dropped, each space
// turned into a hyphen.
func slug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// anchors is every anchor GitHub generates for a file's headings, with the "-1", "-2"
// suffixes it gives to repeated headings.
func (d *doc) anchors() map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, h := range d.headings() {
		s := slug(h.text)
		if n := seen[s]; n > 0 {
			out[s+"-"+strconv.Itoa(n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

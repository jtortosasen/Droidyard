package doccheck

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var (
	// linkRE is an inline markdown link or image. Autolinks <https://…> are not links
	// to check.
	linkRE = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	// refDefRE is a reference-style link definition: [label]: target
	refDefRE = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s+(\S+)`)
	schemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

	// A section reference is "§Title" or "§N" right after something that names a
	// file: a link, "ADR-NNNN", a path in backticks, or a bare path with a directory,
	// as Go comments write it.
	refAnchorRE  = regexp.MustCompile(`(\[[^\]]*\]\(([^)\s]+)\)|ADR-(\d{4})|` + "`([^`\\s]+\\.md)`" + `|((?:[\w.-]+/)+[\w.-]+\.md))[ \t]*§`)
	adrMention   = regexp.MustCompile(`\bADR-(\d{4})\b`)
	numberedRef  = regexp.MustCompile(`^\d+(?:\.\d+)*`)
	boldLabel    = regexp.MustCompile(`^\s*(?:[-*+]\s+|\d+\.\s+)?\*\*(.+?)\*\*`)
	numberedItem = regexp.MustCompile(`^(\d+)\.\s`)
)

// TestRelativeLinksResolve checks that every relative link in the repository's
// markdown points at a file that exists, and that a #fragment names a heading of that
// file. The pre-implementation review found two such links by hand (N1, "Broken
// links").
func TestRelativeLinksResolve(t *testing.T) {
	report(t, checkLinks(repo()))
}

// TestSectionReferencesResolve checks that a "§Section" written right after a
// reference to a file names a heading, or a bold clause label, of that file. The
// documents cite sections this way throughout, and the verifier treats such a
// citation as a claim to check; this checks the part of the claim that is exact,
// that the section exists.
//
// Matching is by prefix at a word boundary, in either direction, ignoring case,
// backticks and emphasis, because the prose runs on after the name ("§Spikes before
// the sidecar is written and …") and short forms are used ("§Naming" for "Naming and
// namespaces"). "§N" matches a heading or a top-level list item numbered N. It
// cannot tell a right section from a wrong one that exists: that stays with the
// verifier.
//
// docs/reviews/ is skipped: those are closed records whose citations describe the
// documents as they were when the review ran.
func TestSectionReferencesResolve(t *testing.T) {
	report(t, checkSectionRefs(repo()))
}

// TestADRMentionsExist checks that every "ADR-NNNN" in the repository's markdown and
// Go sources names an ADR that exists.
func TestADRMentionsExist(t *testing.T) {
	report(t, checkADRMentions(repo()))
}

// maskCode blanks inline code spans, so a link written as code is not read as a link.
func maskCode(l string) string {
	return codeSpanRE.ReplaceAllStringFunc(l, func(s string) string { return strings.Repeat(" ", len(s)) })
}

func checkLinks(fsys fs.FS) []string {
	files, err := markdownFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	cache := map[string]*doc{}
	load := func(p string) *doc {
		if d, ok := cache[p]; ok {
			return d
		}
		d, err := readDoc(fsys, p)
		if err != nil {
			d = nil
		}
		cache[p] = d
		return d
	}
	for _, f := range files {
		d := load(f)
		if d == nil {
			continue
		}
		for i, l := range d.lines {
			if d.inFence[i] {
				continue
			}
			var targets []string
			for _, m := range linkRE.FindAllStringSubmatch(maskCode(l), -1) {
				targets = append(targets, m[1])
			}
			if m := refDefRE.FindStringSubmatch(l); m != nil {
				targets = append(targets, m[1])
			}
			for _, raw := range targets {
				target := strings.Trim(raw, "<>")
				if schemeRE.MatchString(target) {
					continue
				}
				at := fmt.Sprintf("%s:%d", f, lineNo(i))
				file, frag, _ := strings.Cut(target, "#")
				if dec, err := url.PathUnescape(file); err == nil {
					file = dec
				}
				dest := f
				if file != "" {
					p, inside := resolve(f, file)
					if !inside {
						problems = append(problems, fmt.Sprintf("%s: link %q leaves the repository", at, raw))
						continue
					}
					if !exists(fsys, p) {
						problems = append(problems, fmt.Sprintf("%s: link %q points at %s, which does not exist. Correct the path, relative to %s", at, raw, p, path.Dir(f)))
						continue
					}
					dest = p
				}
				if frag == "" || !strings.HasSuffix(dest, ".md") {
					continue
				}
				td := load(dest)
				if td == nil {
					continue
				}
				if !td.anchors()[frag] {
					problems = append(problems, fmt.Sprintf("%s: link %q names the anchor #%s, which no heading of %s produces. Headings there: %s", at, raw, frag, dest, headingList(td)))
				}
			}
		}
	}
	return problems
}

func headingList(d *doc) string {
	var out []string
	for _, h := range d.headings() {
		if h.level > 1 {
			out = append(out, h.text)
		}
	}
	if len(out) > 12 {
		out = append(out[:12], "…")
	}
	return strings.Join(out, " | ")
}

// target is a file a section reference can be resolved against: what it may name.
type target struct {
	names    []string // normalised headings and bold labels
	numbered map[string]bool
}

func normalise(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("`", "", "*", "", "_", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ".: ")
}

func targetOf(d *doc) *target {
	t := &target{numbered: map[string]bool{}}
	for _, h := range d.headings() {
		t.names = append(t.names, normalise(h.text))
		// A citation drops a trailing qualifier: "§Out of scope" for
		// "Out of scope (explicit)".
		if k := strings.LastIndex(h.text, " ("); k > 0 && strings.HasSuffix(h.text, ")") {
			t.names = append(t.names, normalise(h.text[:k]))
		}
		if n := numberedRef.FindString(h.text); n != "" {
			t.numbered[n] = true
			rest := strings.TrimLeft(strings.TrimPrefix(h.text, n), ". ")
			t.names = append(t.names, normalise(rest))
		}
	}
	for i, l := range d.lines {
		if d.inFence[i] {
			continue
		}
		if m := boldLabel.FindStringSubmatch(l); m != nil {
			t.names = append(t.names, normalise(m[1]))
		}
		if m := numberedItem.FindStringSubmatch(l); m != nil {
			t.numbered[m[1]] = true
		}
	}
	return t
}

// prefixAtWord reports whether short is a prefix of long that ends at a word boundary.
func prefixAtWord(short, long string) bool {
	if short == "" || !strings.HasPrefix(long, short) {
		return false
	}
	if len(long) == len(short) {
		return true
	}
	r := rune(long[len(short)])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

func (t *target) has(ref string) bool {
	if n := numberedRef.FindString(ref); n != "" {
		return t.numbered[n]
	}
	r := normalise(ref)
	for _, name := range t.names {
		if prefixAtWord(r, name) || prefixAtWord(name, r) {
			return true
		}
	}
	return false
}

// refText reads the section name that starts at text[i], after the "§". It runs to
// the first punctuation that ends a name, across a single soft line break.
func refText(text string, i int) (string, int) {
	var b strings.Builder
	for i < len(text) && b.Len() < 100 {
		for _, stop := range []string{"§", "·", "—", "–"} {
			if strings.HasPrefix(text[i:], stop) {
				return b.String(), i
			}
		}
		c := text[i]
		switch c {
		case ')', ',', ';', '|', ':', '"', '(', '*':
			return b.String(), i
		case '.':
			if i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\n' {
				return b.String(), i
			}
		case '\n':
			next := strings.TrimSpace(text[i+1:])
			if next == "" || strings.ContainsAny(next[:1], "-*#|>") || numberedItem.MatchString(next) || text[i+1] == '\n' {
				return b.String(), i
			}
			c = ' '
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), i
}

// chained reports whether the text between two references only joins them, as in
// "§Naming and §Classification" or "§5 to §7".
func chained(between string) bool {
	for _, w := range strings.Fields(strings.NewReplacer(",", " ", "·", " ").Replace(between)) {
		switch w {
		case "and", "or", "to", "vs", "vs.", "&":
		default:
			return false
		}
	}
	return true
}

// resolveFileRef finds the file a backticked path names: relative to the citing
// file, then to the repository root, then as the only file with that base name.
func resolveFileRef(fsys fs.FS, from, name string, byBase map[string][]string) (string, bool) {
	if p, ok := resolve(from, name); ok && exists(fsys, p) {
		return p, true
	}
	if exists(fsys, name) {
		return name, true
	}
	if !strings.Contains(name, "/") {
		if c := byBase[name]; len(c) == 1 {
			return c[0], true
		}
	}
	return "", false
}

func checkSectionRefs(fsys fs.FS) []string {
	mdFiles, err := markdownFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	srcs, err := goFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	byBase := map[string][]string{}
	for _, f := range mdFiles {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	adrs, _, err := adrFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	targets := map[string]*target{}
	targetFor := func(p string) *target {
		if t, ok := targets[p]; ok {
			return t
		}
		d, err := readDoc(fsys, p)
		var t *target
		if err == nil {
			t = targetOf(d)
		}
		targets[p] = t
		return t
	}

	var problems []string
	for _, f := range append(mdFiles, srcs...) {
		if strings.HasPrefix(f, "docs/reviews/") {
			continue
		}
		d, err := readDoc(fsys, f)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		text := strings.Join(proseLines(f, d), "\n")
		for _, m := range refAnchorRE.FindAllStringSubmatchIndex(text, -1) {
			var dest string
			switch {
			case m[4] >= 0: // a link
				link := text[m[4]:m[5]]
				if schemeRE.MatchString(link) {
					continue
				}
				file, _, _ := strings.Cut(link, "#")
				if file == "" {
					dest = f
				} else if p, ok := resolve(f, file); ok && strings.HasSuffix(p, ".md") && exists(fsys, p) {
					dest = p
				}
			case m[6] >= 0: // ADR-NNNN
				n, _ := strconv.Atoi(text[m[6]:m[7]])
				if files := adrs[n]; len(files) == 1 {
					dest = adrDir + "/" + files[0]
				}
			case m[8] >= 0: // `path.md`
				dest, _ = resolveFileRef(fsys, f, text[m[8]:m[9]], byBase)
			case m[10] >= 0: // path/to/file.md
				dest, _ = resolveFileRef(fsys, f, text[m[10]:m[11]], byBase)
			}
			if dest == "" {
				// A missing file is the link check's and the ADR-mention check's
				// finding; a path that does not resolve uniquely is not checked.
				continue
			}
			t := targetFor(dest)
			if t == nil {
				continue
			}
			cited := strings.TrimSpace(strings.TrimSuffix(text[m[0]:m[1]], "§"))
			pos := m[1]
			for {
				ref, end := refText(text, pos)
				ref = strings.TrimSpace(ref)
				if ref != "" && !strings.ContainsAny(ref, "<>…") && !t.has(ref) {
					line := strings.Count(text[:pos], "\n")
					problems = append(problems, fmt.Sprintf("%s:%d: %s §%s: %s has no heading or clause by that name. Cite a section that exists; its headings are: %s",
						f, lineNo(line), clip(cited, 60), ref, dest, headingList(mustDoc(fsys, dest))))
				}
				next := strings.IndexByte(text[end:], '\n')
				window := text[end:]
				if next >= 0 && next+1 < len(window) {
					// a chain may continue on the next line of the same paragraph
					if nl := strings.IndexByte(window[next+1:], '\n'); nl >= 0 {
						window = window[:next+1+nl]
					}
				}
				j := strings.Index(window, "§")
				if j < 0 || !chained(window[:j]) {
					break
				}
				pos = end + j + len("§")
			}
		}
	}
	return problems
}

// proseLines returns a file's lines with everything that is not prose blanked, line
// breaks kept so positions still map to lines: fenced blocks in markdown, and in Go
// everything but comments.
func proseLines(f string, d *doc) []string {
	out := make([]string, len(d.lines))
	if !strings.HasSuffix(f, ".go") {
		for i, l := range d.lines {
			if !d.inFence[i] {
				out[i] = l
			}
		}
		return out
	}
	src := []byte(strings.Join(d.lines, "\n"))
	fset := token.NewFileSet()
	file := fset.AddFile(f, -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		start := fset.Position(pos).Line - 1
		for k, l := range strings.Split(lit, "\n") {
			if start+k < len(out) {
				out[start+k] = l
			}
		}
	}
	return out
}

func mustDoc(fsys fs.FS, p string) *doc {
	d, err := readDoc(fsys, p)
	if err != nil {
		return parseDoc(p, "")
	}
	return d
}

func checkADRMentions(fsys fs.FS) []string {
	mdFiles, err := markdownFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	srcs, err := goFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	adrs, _, err := adrFiles(fsys)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, f := range append(mdFiles, srcs...) {
		d, err := readDoc(fsys, f)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for i, l := range proseLines(f, d) {
			for _, m := range adrMention.FindAllStringSubmatch(l, -1) {
				n, _ := strconv.Atoi(m[1])
				if n == 0 || len(adrs[n]) > 0 {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s:%d: %s does not exist; docs/adr/ holds ADR-0001 to ADR-%04d. Correct the number, or write the ADR first", f, lineNo(i), m[0], len(adrs)))
			}
		}
	}
	return problems
}

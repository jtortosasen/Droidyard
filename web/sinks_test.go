package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sinks are the ways markup or a URL reaches the page from a value (plan m2a D5;
// http-websocket.md §Security baseline rule 8). Device and app text is rendered as text
// only: never as HTML, never as a link. Banning the sinks makes every field inert by
// construction, so no field needs a mark saying it is.
var sinks = []struct {
	name string
	re   *regexp.Regexp
}{
	{"{@html}", regexp.MustCompile(`\{\s*@html\b`)},
	{"innerHTML", regexp.MustCompile(`innerHTML`)},
	{"outerHTML", regexp.MustCompile(`outerHTML`)},
	{"insertAdjacentHTML", regexp.MustCompile(`insertAdjacentHTML`)},
	{"document.write", regexp.MustCompile(`document\s*\.\s*write(ln)?\b`)},
	{"srcdoc", regexp.MustCompile(`srcdoc`)},
	// A URL attribute whose value holds an expression, bare or inside quotes: href={x},
	// href="{x}", href="https://{host}/p", xlink:href, src, srcset, action, formaction,
	// an object's data, or the {href} shorthand. A literal value (href="/x") is not a sink.
	{"URL attribute={expression}", regexp.MustCompile(`(?i)\b(xlink:)?(href|src|srcset|action|formaction|data)\s*=\s*["']?[^"'>\s]*\{`)},
	{"{href}/{src} shorthand", regexp.MustCompile(`(?i)[\s<]\{\s*(href|src|srcset|action|formaction)\s*\}`)},
	// An attribute spread on an element can carry any of them.
	{"attribute spread", regexp.MustCompile(`<[A-Za-z][\w:.-]*\b[^>]*\{\s*\.\.\.`)},
	// The same sinks set from script.
	{"URL property or setAttribute", regexp.MustCompile(`(?i)\.(href|src|srcdoc|action|formaction)\s*=[^=]|setAttribute\(\s*['"](href|src|srcset|srcdoc|action|formaction|xlink:href)`)},
}

// generated is the schema-generated client (git-ignored, present once gen has run): it
// holds types and descriptions, no markup, and a description may name a sink in prose.
const generated = "lib/api/capabilities.gen.ts"

// findSinks lists the sinks in one file's text, with their line numbers.
func findSinks(name, text string) []string {
	var found []string
	for i, line := range strings.Split(text, "\n") {
		for _, s := range sinks {
			if s.re.MatchString(line) {
				found = append(found, name+":"+strconv.Itoa(i+1)+": "+s.name)
			}
		}
	}
	return found
}

// Decision (plan m2a D5): every banned sink is found, a literal href or a plain
// expression is not, and the UI's source has none. A test of the walk alone would pass
// with a scanner that matches nothing, so the table is checked against known bad lines.
func TestNoHTMLSinks(t *testing.T) {
	bad := map[string]string{
		"{@html}":                "<p>{@html text}</p>",
		"{@html} spaced":         "<p>{ @html text }</p>",
		"innerHTML":              "el.innerHTML = x;",
		"outerHTML":              "el.outerHTML = x;",
		"insertAdjacentHTML":     "el.insertAdjacentHTML('beforeend', x);",
		"document.write":         "document.write(x);",
		"srcdoc":                 `<iframe srcdoc={x}></iframe>`,
		"href expression":        `<a href={x}>t</a>`,
		"href expression spaced": `<a href = {x}>t</a>`,
		"src expression":         `<img src={x} alt="" />`,
		"xlink:href":             `<use xlink:href={x} />`,
		"href shorthand":         `<a {href}>t</a>`,
		"href quoted":            `<a href="{x}">t</a>`,
		"href interpolated":      `<a href="https://{host}/p">t</a>`,
		"src quoted":             `<img src="{x}" alt="" />`,
		"spread":                 `<a {...props}>t</a>`,
		"svelte:element spread":  `<svelte:element this={tag} {...attrs} />`,
		"formaction":             `<button formaction={x}>t</button>`,
		"action":                 `<form action={x}></form>`,
		"object data":            `<object data={x}></object>`,
		"setAttribute":           `el.setAttribute('href', x);`,
		"href property":          `a.href = x;`,
	}
	for name, line := range bad {
		if len(findSinks("f", line)) == 0 {
			t.Errorf("%s: %q was not found", name, line)
		}
	}
	for _, line := range []string{
		`<a href="/static">t</a>`,
		`<p>{text}</p>`,
		`<img src="/logo.png" alt="" />`,
		`const html = 'plain';`,
		`const env = { ...params, id };`,
		`if (a.href === b) {}`,
		`<div data-row={i}>t</div>`,
	} {
		if got := findSinks("f", line); len(got) != 0 {
			t.Errorf("%q was flagged: %v", line, got)
		}
	}

	src := os.DirFS("src")
	var all []string
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if p == generated {
			return nil
		}
		switch filepath.Ext(p) {
		case ".svelte", ".ts", ".js", ".html":
		default:
			return nil
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		all = append(all, findSinks("web/src/"+p, string(b))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		t.Errorf("an HTML or URL sink in the UI source: %s", f)
	}
}

package sdk

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The package.xml Droidyard writes (plan m3f step 4; ADR-0130 §Consequences): other tools
// read it, so its form is held against the file the classic sdkmanager wrote (the spike's
// §(c)), with the licence text replaced by the fixtures' synthetic one: no Google licence
// text is committed.

// canonXML parses XML into a canonical, comparable text: elements with their resolved
// namespaces, attributes sorted (namespace declarations included), leaf text exactly, and
// the whitespace between elements ignored.
func canonXML(t *testing.T, data []byte) string {
	t.Helper()
	type node struct {
		name  xml.Name
		attrs []string
		text  strings.Builder
		kids  []*node
	}
	root := &node{}
	stack := []*node{root}
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("package.xml does not parse: %v", err)
		}
		switch x := tok.(type) {
		case xml.StartElement:
			n := &node{name: x.Name}
			for _, a := range x.Attr {
				n.attrs = append(n.attrs, fmt.Sprintf("{%s}%s=%q", a.Name.Space, a.Name.Local, a.Value))
			}
			sort.Strings(n.attrs)
			stack[len(stack)-1].kids = append(stack[len(stack)-1].kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].text.Write(x)
		}
	}
	var out strings.Builder
	var dump func(n *node, depth int)
	dump = func(n *node, depth int) {
		pad := strings.Repeat("  ", depth)
		fmt.Fprintf(&out, "%s<{%s}%s %s>", pad, n.name.Space, n.name.Local, strings.Join(n.attrs, " "))
		if len(n.kids) == 0 {
			fmt.Fprintf(&out, "%q\n", n.text.String())
			return
		}
		out.WriteString("\n")
		for _, k := range n.kids {
			dump(k, depth+1)
		}
	}
	for _, k := range root.kids {
		dump(k, 0)
	}
	return out.String()
}

// TestPackageXMLGolden: the package.xml written for the fixture platform-tools equals the
// classic tool's file for it, once both are parsed to a canonical tree (the 17 namespace
// declarations, the licence text, the type-details with its xsi:type mapped to the
// preamble's prefix, the revision, the display name, the licence reference). Seen failing
// with xsi:type left unmapped (the repository's own prefix written).
func TestPackageXMLGolden(t *testing.T) {
	f := newSimple(t)
	wantInstalled(t, f.installAs("platform-tools"))
	got, err := os.ReadFile(filepath.Join(f.root, "platform-tools", "package.xml"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "package-xml", "platform-tools.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := canonXML(t, got), canonXML(t, golden); a != b {
		t.Errorf("package.xml differs from the golden.\ngot:\n%s\nwant:\n%s\nraw:\n%s", a, b, got)
	}
	if !strings.Contains(string(got), `xsi:type="ns5:genericDetailsType"`) {
		t.Errorf("xsi:type is not the preamble's prefix: %s", got)
	}
	if strings.Contains(string(golden), "Google") {
		t.Error("the golden holds Google's text")
	}
}

// TestPackageXMLImage: a system image's package.xml keeps its type-details (api level, tag,
// ABI) under the preamble's prefix for the sys-img2 namespace, and its dependency on the
// emulator with its min-revision.
func TestPackageXMLImage(t *testing.T) {
	f := newSimple(t)
	wantInstalled(t, f.installAs(imageID))
	raw, err := os.ReadFile(filepath.Join(f.root, "system-images", "android-28", "default", "arm64-v8a", "package.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Local struct {
			Path    string `xml:"path,attr"`
			Details struct {
				Type  string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
				API   string `xml:"api-level"`
				TagID string `xml:"tag>id"`
				ABI   string `xml:"abi"`
			} `xml:"type-details"`
			Revision string `xml:"revision>major"`
			Deps     []struct {
				Path  string `xml:"path,attr"`
				Major string `xml:"min-revision>major"`
				Minor string `xml:"min-revision>minor"`
				Micro string `xml:"min-revision>micro"`
			} `xml:"dependencies>dependency"`
		} `xml:"localPackage"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	l := doc.Local
	if l.Path != imageID || l.Details.Type != "ns16:sysImgDetailsType" || l.Details.API != "28" || l.Details.TagID != "default" || l.Details.ABI != "arm64-v8a" || l.Revision != "2" {
		t.Errorf("localPackage = %+v", l)
	}
	if len(l.Deps) != 1 || l.Deps[0].Path != "emulator" || l.Deps[0].Major != "31" || l.Deps[0].Minor != "1" || l.Deps[0].Micro != "1" {
		t.Errorf("dependencies = %+v, want emulator 31.1.1", l.Deps)
	}
}

// TestPackageXMLEscapesUntrusted: text from the repository is written as text. A display
// name that closes its element and opens another, and a licence text with `<`, `&` and
// `]]>`, read back as exactly that text, create no element, and a licence text read back
// hashes to the hash it was accepted under.
func TestPackageXMLEscapesUntrusted(t *testing.T) {
	const name = "</display-name><x/>\"quoted\" & 'more'"
	const text = "\nTerms: a < b && c ]]> d <script>\n\n  indented\ttab\n"
	p := &RemotePackage{
		ID: "platform-tools", Revision: Revision{Major: 1, Parts: 1}, DisplayName: name,
		License:     LicenceRef{ID: "android-sdk-license", SHA1: licenceHash(text)},
		TypeDetails: TypeDetails{Namespace: "http://schemas.android.com/repository/android/generic/02", Local: "genericDetailsType"},
	}
	raw, err := packageXML(p, Licence{ID: "android-sdk-license", SHA1: p.License.SHA1, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		License string `xml:"license"`
		Local   struct {
			Name string `xml:"display-name"`
		} `xml:"localPackage"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, raw)
	}
	if doc.Local.Name != name || doc.License != text {
		t.Errorf("read back name=%q licence=%q", doc.Local.Name, doc.License)
	}
	if licenceHash(doc.License) != p.License.SHA1 {
		t.Error("the licence text read back hashes to something else")
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && (se.Name.Local == "x" || se.Name.Local == "script") {
			t.Errorf("untrusted text created an <%s> element", se.Name.Local)
		}
	}
}

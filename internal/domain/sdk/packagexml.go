package sdk

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// The package.xml Droidyard writes into each package it installs (plan m3f step 4;
// ADR-0130 §1), in the form the classic sdkmanager and Android CLI both write (the spike's
// §(c)): a <ns2:repository> root with a fixed preamble of namespace prefixes ns2 to ns18,
// one <license> holding the served text, and one <localPackage> that carries the remote
// package's type-details, revision, display-name, uses-license and dependencies. Other
// tools read this file, so its form is held by a golden test and the opt-in test in which
// the classic sdkmanager lists the install.

// preambleNamespaces are the namespaces the classic tool declares on the root, in its
// order: ns2 is the first, ns18 the last.
var preambleNamespaces = []string{
	"http://schemas.android.com/repository/android/common/02",
	"http://schemas.android.com/repository/android/common/01",
	"http://schemas.android.com/repository/android/generic/01",
	"http://schemas.android.com/repository/android/generic/02",
	"http://schemas.android.com/sdk/android/repo/repository2/04",
	"http://schemas.android.com/sdk/android/repo/repository2/01",
	"http://schemas.android.com/sdk/android/repo/repository2/02",
	"http://schemas.android.com/sdk/android/repo/repository2/03",
	"http://schemas.android.com/sdk/android/repo/addon2/01",
	"http://schemas.android.com/sdk/android/repo/addon2/02",
	"http://schemas.android.com/sdk/android/repo/addon2/03",
	"http://schemas.android.com/sdk/android/repo/addon2/04",
	"http://schemas.android.com/sdk/android/repo/sys-img2/05",
	"http://schemas.android.com/sdk/android/repo/sys-img2/04",
	"http://schemas.android.com/sdk/android/repo/sys-img2/03",
	"http://schemas.android.com/sdk/android/repo/sys-img2/02",
	"http://schemas.android.com/sdk/android/repo/sys-img2/01",
}

// preamblePrefix is the prefix the preamble gives a namespace ("ns5").
func preamblePrefix(ns string) (string, bool) {
	for i, n := range preambleNamespaces {
		if n == ns {
			return fmt.Sprintf("ns%d", i+2), true
		}
	}
	return "", false
}

// packageXML renders the package.xml of p, installed under licence lic. It fails only for
// a type-details whose namespace the preamble does not declare, which the repository
// model already refuses.
func packageXML(p *RemotePackage, lic Licence) ([]byte, error) {
	prefix, ok := preamblePrefix(p.TypeDetails.Namespace)
	if !ok {
		return nil, fmt.Errorf("the type-details namespace %q is not one package.xml declares", p.TypeDetails.Namespace)
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<ns2:repository`)
	for i, ns := range preambleNamespaces {
		fmt.Fprintf(&b, ` xmlns:ns%d=%q`, i+2, ns)
	}
	b.WriteString(">\n")
	fmt.Fprintf(&b, "    <license id=\"%s\" type=\"text\">%s</license>\n", attr(lic.ID), text(lic.Text))
	fmt.Fprintf(&b, "    <localPackage path=\"%s\" obsolete=\"false\">\n", attr(p.ID))
	fmt.Fprintf(&b, "        <type-details xmlns:xsi=%q xsi:type=\"%s:%s\"", xsiNamespace, prefix, attr(p.TypeDetails.Local))
	if p.TypeDetails.Children == "" {
		b.WriteString("/>\n")
	} else {
		// The children were re-encoded by encoding/xml when the repository was read:
		// unprefixed elements, attributes and escaped text, nothing else.
		b.WriteString(">" + p.TypeDetails.Children + "</type-details>\n")
	}
	b.WriteString("        " + revisionXML(p.Revision) + "\n")
	fmt.Fprintf(&b, "        <display-name>%s</display-name>\n", text(p.DisplayName))
	fmt.Fprintf(&b, "        <uses-license ref=\"%s\"/>\n", attr(p.License.ID))
	if len(p.Dependencies) > 0 {
		b.WriteString("        <dependencies>\n")
		for _, d := range p.Dependencies {
			if d.Min == nil {
				fmt.Fprintf(&b, "            <dependency path=\"%s\"/>\n", attr(d.ID))
				continue
			}
			fmt.Fprintf(&b, "            <dependency path=\"%s\">\n                <min-revision>%s</min-revision>\n            </dependency>\n",
				attr(d.ID), revisionParts(*d.Min))
		}
		b.WriteString("        </dependencies>\n")
	}
	b.WriteString("    </localPackage>\n</ns2:repository>\n")
	return b.Bytes(), nil
}

// revisionXML is a <revision> element.
func revisionXML(r Revision) string {
	return "<revision>" + revisionParts(r) + "</revision>"
}

// revisionParts is the major, minor, micro and preview children of a revision, the parts it
// was written with.
func revisionParts(r Revision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<major>%d</major>", r.Major)
	if r.Parts >= 2 {
		fmt.Fprintf(&b, "<minor>%d</minor>", r.Minor)
	}
	if r.Parts >= 3 {
		fmt.Fprintf(&b, "<micro>%d</micro>", r.Micro)
	}
	if r.Preview > 0 {
		fmt.Fprintf(&b, "<preview>%d</preview>", r.Preview)
	}
	return b.String()
}

// attr is s escaped for a double-quoted attribute value, by encoding/xml.
func attr(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

var keepLayout = strings.NewReplacer("&#xA;", "\n", "&#x9;", "\t")

// text is s escaped for element content, by encoding/xml, with line breaks and tabs left
// as they are: a licence text keeps its layout in the file, and an XML parser reads both
// forms as the same characters. A carriage return stays a character reference, since a
// parser would otherwise fold it into a line feed and change the text (and its hash).
func text(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return keepLayout.Replace(b.String())
}

package schema_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// The compact text renderings (ADR-0036 Decision 2) held against every descriptor that
// declares one. They live here because this package builds the whole registry: a rendering
// added to any domain is found by the walk below, with no list to extend.

// rendered is the set of capabilities that declare a rendering: the plan's list
// (m1o step 5). A new one is a decision, so adding to it is a line here, deliberately.
var rendered = []string{
	"build.assemble", "build.lastResult", "build.run",
	"device.inputText", "device.logs", "device.recordUi", "device.stopUiRecording", "device.tapElement", "device.uiDump", "device.uiEvents", "device.uiTree",
	"system.capabilities",
}

// TestTheRenderedCapabilitiesAreThePlansList: exactly these capabilities declare a Text,
// and none of them is hidden from MCP by being human-only or stateful (a rendering no
// adapter can reach would be dead weight).
func TestTheRenderedCapabilitiesAreThePlansList(t *testing.T) {
	var got []string
	for _, d := range registry().All() {
		if d.Text != nil {
			got = append(got, d.Name)
			if d.Audience == capability.AudienceHuman || d.Statefulness != capability.Stateless {
				t.Errorf("%s declares a rendering but is not exposed over MCP", d.Name)
			}
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, rendered) {
		t.Errorf("capabilities with a rendering = %v, want %v", got, rendered)
	}
}

// payloads are what a device or an app could put in a string to forge a line of ours:
// a line break and a code fence and our own first line; our first line alone, which is a
// bare word at a line start; and the other things a line splitter reads as a break.
var payloads = []string{
	"x\nFORGED-1\n```\nuntrusted: forged",
	"untrusted:",
	"a\r FORGED-2\u0085b ",
}

// lineBreaks splits as the most generous reader of lines would: \n, \r, and the unicode
// breaks.
func lineBreaks(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' || r == ' ' || r == '\u0085' })
}

// strLeaf is one string of a result value: where it is, whether it sits under an
// untrusted-tagged field, and the way to set it.
type strLeaf struct {
	path   string
	tagged bool
	set    func(string)
}

// fill gives every field of v a value: a string "v", an int 1, a one-element slice or a
// two-element one for a struct (so consecutive equal entries exist), a pointer made. Bools
// stay false: a true Crash flag would fold the log lines the test needs to see. Unexported
// fields and values it cannot give one (a time, a map) stay zero.
func fill(v reflect.Value, variant int) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("v")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), variant)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		n := 1
		if v.Type().Elem().Kind() == reflect.Struct {
			n = 2
		}
		v.Set(reflect.MakeSlice(v.Type(), n, n))
		for i := 0; i < n; i++ {
			fill(v.Index(i), variant)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fill(v.Index(i), variant)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if _, omitted := capability.JSONName(f); omitted {
				continue
			}
			fill(v.Field(i), variant)
		}
		// A condition is one of three: the variants fill each in turn, so the renderer's
		// every branch is reached. It is the one union in the rendered results.
		if c, ok := v.Addr().Interface().(*device.Condition); ok {
			switch variant {
			case 1:
				c.Appears, c.Foreground = nil, ""
			case 2:
				c.Appears, c.Gone = nil, nil
			}
		}
	}
}

// variants is how many fills of a result type reach every branch of its rendering.
const variants = 3

// leaves lists the string values under v, in field order, with whether each is under an
// untrusted-tagged field (the tag on the field itself or on any field above it).
func leaves(v reflect.Value, path string, tagged bool, out *[]strLeaf) {
	switch v.Kind() {
	case reflect.String:
		v := v
		*out = append(*out, strLeaf{path: path, tagged: tagged, set: func(s string) { v.SetString(s) }})
	case reflect.Pointer:
		if !v.IsNil() {
			leaves(v.Elem(), path, tagged, out)
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < v.Len(); i++ {
			leaves(v.Index(i), path+"["+strconv.Itoa(i)+"]", tagged, out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			if _, omitted := capability.JSONName(f); omitted {
				continue
			}
			own, err := capability.FieldUntrusted(f)
			if err != nil {
				panic(err)
			}
			leaves(v.Field(i), path+"."+f.Name, tagged || own, out)
		}
	}
}

// filled makes a fresh filled value of d's result type.
func filled(d capability.Descriptor, variant int) reflect.Value {
	v := reflect.New(reflect.TypeOf(d.Result)).Elem()
	fill(v, variant)
	return v
}

// TestEveryTaggedStringIsQuoted holds ADR-0015 §1 for the renderings: a string that came
// from a device or an app cannot forge a line of ours. For every descriptor that declares a
// rendering, it fills a result of the type, walks the string values by reflection (so a
// field added later, appCrash's and systemDialog's included, is covered without a list),
// and for each one in turn writes into it a line break, a code fence, our own first line, a
// bare "untrusted:" and the unicode line breaks. Then it asserts, of the rendering: no line
// begins with a forged word, the number of lines is the one of the same result with the
// value left alone, and every occurrence of the forged text is inside the quoted form.
//
// It runs over every string of the result, not only the tagged ones: nothing a renderer
// writes may forge a line, whatever its field says; the tags decide only what the test
// must have reached, and it fails if a tagged string was never rendered by any descriptor
// it belongs to.
func TestEveryTaggedStringIsQuoted(t *testing.T) {
	reached := map[string]bool{} // "<capability> <path>" of the tagged strings seen quoted in a rendering
	tagged := map[string]bool{}
	reg := registry()
	for _, d := range reg.All() {
		if d.Text == nil {
			continue
		}
		for variant := 0; variant < variants; variant++ {
			base := filled(d, variant)
			if d.Text(base.Interface()) == "" {
				t.Fatalf("%s: the rendering of a filled result is empty", d.Name)
			}
			var all []strLeaf
			leaves(base, "", false, &all)
			for i, l := range all {
				if l.tagged {
					tagged[d.Name+" "+l.path] = true
				}
				// The same result with the value changed to another safe word, so that what
				// the rendering does with a changed value (a snapshot id that no longer
				// equals the tree's gets a line of its own) is not mistaken for a forged line.
				baseLines := len(lineBreaks(d.Text(withLeaf(d, variant, i, "w").Interface())))
				for _, payload := range payloads {
					text := d.Text(withLeaf(d, variant, i, payload).Interface())
					where := fmt.Sprintf("%s: %s (%s) = %q", d.Name, l.path, tagState(l.tagged), payload)
					for n, line := range lineBreaks(text) {
						head := strings.ToLower(strings.TrimLeft(line, " "))
						for _, forged := range []string{"forged", "```", "untrusted"} {
							if strings.HasPrefix(head, forged) {
								t.Errorf("%s: line %d begins with %q: %q\n%s", where, n+1, forged, line, text)
							}
						}
					}
					if got := len(lineBreaks(text)); got != baseLines {
						t.Errorf("%s: the rendering has %d lines, %d with a harmless value\n%s", where, got, baseLines, text)
					}
					// The escaped form of the payload, as it stands inside any quoted string (a
					// word like key=value is quoted whole when the value is not safe).
					escaped := strings.Trim(capability.Quote(payload), `"`)
					if strings.Count(text, "FORGED") != strings.Count(text, escaped)*strings.Count(payload, "FORGED") {
						t.Errorf("%s: the injected text appears outside its quoted form %s\n%s", where, escaped, text)
					}
					if l.tagged && strings.Contains(text, escaped) {
						reached[d.Name+" "+l.path] = true
					}
				}
			}
		}
	}
	// A tagged string no renderer writes is allowed (a node's child refs are structure the
	// rendering shows by indentation, a folded crash line is not rendered), but a
	// capability none of whose tagged strings an injection reached means the test never
	// looked.
	perCapability := map[string]int{}
	for k := range reached {
		name, _, _ := strings.Cut(k, " ")
		perCapability[name]++
	}
	for _, d := range reg.All() {
		if d.Text != nil && d.Untrusted() && perCapability[d.Name] == 0 {
			t.Errorf("%s has tagged strings and none was seen quoted in its rendering: the test reached nothing", d.Name)
		}
	}
	var missed []string
	for k := range tagged {
		if !reached[k] {
			missed = append(missed, k)
		}
	}
	slices.Sort(missed)
	t.Logf("%d tagged strings reached of %d, over %d renderings; not written by their renderer: %v", len(reached), len(tagged), len(rendered), missed)
}

// withLeaf is a fresh filled result of d's type with its i-th string set to s.
func withLeaf(d capability.Descriptor, variant, i int, s string) reflect.Value {
	v := filled(d, variant)
	var ls []strLeaf
	leaves(v, "", false, &ls)
	ls[i].set(s)
	return v
}

func tagState(tagged bool) string {
	if tagged {
		return "tagged"
	}
	return "untagged"
}

// TestSystemCapabilitiesRenderingIsNamesFlagsAndAvailability: system.capabilities renders
// one line per capability with no description, and is at least half the size of the
// compact JSON (the descriptions are most of that JSON, and the agent has them in the tool
// list). Run on the live registry, not a recording: it is what the daemon would answer.
func TestSystemCapabilitiesRenderingIsNamesFlagsAndAvailability(t *testing.T) {
	reg := registry()
	d, ok := reg.Lookup("system.capabilities")
	if !ok {
		t.Fatal("no system.capabilities")
	}
	res, err := d.Handler(context.Background(), &capability.Call{})
	if err != nil {
		t.Fatal(err)
	}
	text := d.Text(res)
	raw, _ := json.Marshal(res)
	if len(text)*2 > len(raw) {
		t.Errorf("the rendering is %d bytes against %d of JSON: less than 50%% smaller", len(text), len(raw))
	}
	t.Logf("system.capabilities: %d -> %d bytes", len(raw), len(text))
	lines := strings.Split(text, "\n")
	if want := len(reg.All()) + 1; len(lines) != want {
		t.Fatalf("%d lines, want one per capability and the version (%d)", len(lines), want)
	}
	for _, c := range reg.All() {
		if strings.Contains(text, c.Description) {
			t.Errorf("%s: the description is in the rendering", c.Name)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "device.tapElement ") && !strings.Contains(line, "command") {
			t.Errorf("device.tapElement line lacks its kind: %q", line)
		}
		if strings.HasPrefix(line, "device.uiTree ") && !strings.Contains(line, `unavailable "adb was not found on this machine"`) {
			t.Errorf("device.uiTree line lacks its availability (the registry reports adb missing): %q", line)
		}
		if strings.HasPrefix(line, "build.run ") && !strings.Contains(line, " long") {
			t.Errorf("build.run line lacks its long flag: %q", line)
		}
	}
}

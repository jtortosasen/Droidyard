package schema_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// The description rule (docs/interfaces/mcp.md §Descriptions; ADR-0036 Decision 1): a
// tool description is at most two sentences and 400 characters, a property description
// at most 120, and the only longer descriptions are the named safety exceptions below,
// each one extra sentence. Every session pays for every description, so the rule is a
// test and not a convention.
const (
	maxSentences    = 2
	maxDescription  = 400
	maxPropertyDesc = 120
	// An exception carries one more sentence, and the characters it takes.
	maxSentencesException   = maxSentences + 1
	maxDescriptionException = 600
)

// safetyException is one description allowed its extra sentence, and the safety facts
// that sentence must still carry: a description that drops one of them while keeping
// the exception has lost the reason it was listed for.
type safetyException struct {
	why   string   // which safety rule the extra sentence is for
	facts []string // each must appear in the description (one assertion per fact)
}

// safetyExceptions is the named list (ADR-0036 Decision 1): a repeat repeats an effect;
// `expect` can be vacuous; `gone` needs two reads; emulator.start's cancel, which
// mcp.md §Long-running work (its last bullet) requires the description to say;
// shell.exec's reach.
var safetyExceptions = map[string]safetyException{
	"device.tapElement": {
		why:   "a repeat taps again; an expect already true before the tap is vacuous; gone needs two reads",
		facts: []string{"repeat", "vacuous", "two reads"},
	},
	"device.inputText": {
		why:   "a repeat types again; an expect already true before typing is vacuous; gone needs two reads",
		facts: []string{"repeat", "vacuous", "two reads"},
	},
	"emulator.start": {
		why:   "cancelling after the serial is resolved leaves the emulator running (mcp.md §Long-running work)",
		facts: []string{"Cancelling", "leaves the emulator running"},
	},
	"shell.exec": {
		why:   "it runs with the user's full rights, so it reaches droidyard's own configuration and trust",
		facts: []string{"full rights", "configuration", "trust"},
	},
}

// sentences counts sentences by one documented rule: a sentence ends at ". ", "! " or
// "? " followed by an uppercase letter or a backtick, or at the end of the text.
// Descriptions are written without abbreviations (no "e.g.") that this would miscount,
// and a name like "device.list" has no space after its dot.
func sentences(s string) int {
	n := 0
	rest := strings.TrimSpace(s)
	for len(rest) > 0 {
		end := -1
		for i := 0; i+2 < len(rest); i++ {
			if (rest[i] == '.' || rest[i] == '!' || rest[i] == '?') && rest[i+1] == ' ' {
				r, _ := utf8.DecodeRuneInString(rest[i+2:])
				if unicode.IsUpper(r) || r == '`' {
					end = i + 2
					break
				}
			}
		}
		n++
		if end < 0 {
			break
		}
		rest = strings.TrimSpace(rest[end:])
	}
	return n
}

// TestSentenceCountingRule pins the counting rule the description test relies on,
// including its known cost: "e.g. Button" is a boundary, which is why descriptions are
// written without abbreviations.
func TestSentenceCountingRule(t *testing.T) {
	for _, c := range []struct {
		text string
		want int
	}{
		{"One.", 1},
		{"One sentence without a stop", 1},
		{"Taps it. Then checks `expect`.", 2},
		{"Taps it! Is it there? `expect` says.", 3},
		{"Reads device.list and file.read, then stops. Done.", 2},
		{"Names such as e.g. Button count as a boundary.", 2},
		{"A lowercase continuation. is not a boundary.", 1},
	} {
		if got := sentences(c.text); got != c.want {
			t.Errorf("sentences(%q) = %d; want %d", c.text, got, c.want)
		}
	}
}

// propertyDescriptions returns every property description in an input schema, by path.
func propertyDescriptions(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		obj, ok := node.(map[string]any)
		if !ok {
			return
		}
		if props, ok := obj["properties"].(map[string]any); ok {
			for name, p := range props {
				child := path + "." + name
				if pm, ok := p.(map[string]any); ok {
					if d, ok := pm["description"].(string); ok {
						out[child] = d
					}
				}
				walk(p, child)
			}
		}
		if items, ok := obj["items"]; ok {
			walk(items, path+"[]")
		}
	}
	walk(root, "")
	return out
}

// TestDescriptionsFollowTheRule: every description keeps the rule or is a named safety
// exception that still carries its safety facts (ADR-0036 Decision 1). Reversing the
// decision (a long description, an unlisted extra sentence, a stale exception) fails it.
func TestDescriptionsFollowTheRule(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range doc.Capabilities {
		seen[c.Name] = true
		n, length := sentences(c.Description), utf8.RuneCountInString(c.Description)
		ex, isException := safetyExceptions[c.Name]
		overRule := n > maxSentences || length > maxDescription
		switch {
		case isException:
			if !overRule {
				t.Errorf("%s is listed as a safety exception (%s) but keeps the plain rule; remove it from safetyExceptions", c.Name, ex.why)
			}
			if n > maxSentencesException || length > maxDescriptionException {
				t.Errorf("%s: %d sentences, %d characters; an exception may carry %d sentences and %d characters", c.Name, n, length, maxSentencesException, maxDescriptionException)
			}
			for _, fact := range ex.facts {
				if !strings.Contains(c.Description, fact) {
					t.Errorf("%s: the description lost %q, the safety fact its exception exists for (%s)", c.Name, fact, ex.why)
				}
			}
		case overRule:
			t.Errorf("%s: %d sentences, %d characters; the rule is %d and %d unless it is a named safety exception", c.Name, n, length, maxSentences, maxDescription)
		}

		for path, d := range propertyDescriptions(t, c.Input) {
			if l := utf8.RuneCountInString(d); l > maxPropertyDesc {
				t.Errorf("%s%s: a property description of %d characters; the rule is %d", c.Name, path, l, maxPropertyDesc)
			}
		}
	}
	for name := range safetyExceptions {
		if !seen[name] {
			t.Errorf("safetyExceptions names %s, which is not a published capability", name)
		}
	}
}

// selectorTool is a tool whose input holds the selector more than once: the target,
// and each of an expectation's appears and gone.
var selectorTools = []string{"device.tapElement", "device.inputText"}

// TestNestedSelectorCopiesAreBare: a repeated object is described once per tool, on
// its first copy, and the nested copies carry no property descriptions (ADR-0036
// Decision 1). Both halves matter: a generator that stripped every copy would leave the
// model with an undescribed selector.
func TestNestedSelectorCopiesAreBare(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range selectorTools {
		var in json.RawMessage
		for _, c := range doc.Capabilities {
			if c.Name == name {
				in = c.Input
			}
		}
		if in == nil {
			t.Fatalf("%s is not published", name)
		}
		descs := propertyDescriptions(t, in)
		for _, p := range []string{".target.id", ".target.text", ".target.within", ".target.within.id", ".target.index", ".target.ref"} {
			if descs[p] == "" {
				t.Errorf("%s: the first selector copy lacks the description of %s", name, p)
			}
		}
		for path := range descs {
			for _, nested := range []string{".expect[].appears.", ".expect[].gone."} {
				if strings.HasPrefix(path, ".expect[]") && strings.Contains(path, nested) {
					t.Errorf("%s: %s is inside a nested selector copy and still has a description: %q", name, path, descs[path])
				}
			}
		}
		// The properties that hold a copy keep their own description: it says what the
		// condition means, which a bare selector does not.
		for _, p := range []string{".expect[].appears", ".expect[].gone"} {
			if descs[p] == "" {
				t.Errorf("%s: %s lost its own description", name, p)
			}
		}
	}
}

// TestSelectorWithinSaysItExcludesTheBoundary: agents twice passed `within` the text
// node they wanted and never matched (plan m1n step 9; m1m dy5), so the selector's
// within says the node it names is excluded.
func TestSelectorWithinSaysItExcludesTheBoundary(t *testing.T) {
	doc, err := schema.Generate(registry(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Capabilities {
		if c.Name != "device.tapElement" {
			continue
		}
		d := propertyDescriptions(t, c.Input)[".target.within"]
		if !strings.Contains(d, "itself") || !strings.Contains(d, "excluded") {
			t.Errorf("the selector's within says %q; want it to say the node itself is excluded", d)
		}
		return
	}
	t.Fatal("device.tapElement is not published")
}

type pair struct {
	ID   string `json:"id,omitempty" jsonschema_description:"The identifier."`
	Name string `json:"name,omitempty" jsonschema_description:"The name."`
}

type twice struct {
	First  pair `json:"first" jsonschema_description:"The first one."`
	Second pair `json:"second" jsonschema_description:"The second one."`
}

type twiceWrapper struct {
	Twice twice `json:"twice"`
	Third pair  `json:"third"`
}

// TestGeneratorDescribesARepeatedObjectOnce: the rule is the generator's, not the
// selector's: any object type that appears twice in one tool's input is described on
// its first copy only, in field order, and a second tool starts afresh.
func TestGeneratorDescribesARepeatedObjectOnce(t *testing.T) {
	reg := capability.NewRegistry()
	for _, name := range []string{"fake.one", "fake.two"} {
		reg.MustRegister(capability.Descriptor{
			Name: name, Kind: capability.Query, Scope: capability.Global, Since: "0.1.0",
			Description: "Takes pairs.", Params: twiceWrapper{}, Result: struct{}{},
			Handler: func(_ context.Context, _ *capability.Call) (any, error) { return nil, nil },
		})
	}
	doc, err := schema.Generate(reg, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Capabilities {
		got := propertyDescriptions(t, c.Input)
		want := map[string]string{
			".twice":             "",
			".twice.first":       "The first one.",
			".twice.first.id":    "The identifier.",
			".twice.first.name":  "The name.",
			".twice.second":      "The second one.",
			".twice.second.id":   "",
			".twice.second.name": "",
			".third":             "",
			".third.id":          "",
			".third.name":        "",
		}
		for path, w := range want {
			if got[path] != w {
				t.Errorf("%s%s: description %q; want %q", c.Name, path, got[path], w)
			}
		}
		if t.Failed() {
			return
		}
	}
}

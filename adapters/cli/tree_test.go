package cli

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// kitchen holds each shape of the flag derivation in one fake capability: a string, an
// integer, a number, an enum, a boolean, an array of scalars, an object of scalars with
// a nested object inside it, an array of objects, an open map and a union.
const kitchenInput = `{
 "type":"object","additionalProperties":false,"required":["name"],
 "properties":{
  "name":{"type":"string","description":"Who."},
  "count":{"type":"integer"},
  "ratio":{"type":"number"},
  "mode":{"type":"string","enum":["fast","slow"],"description":"How."},
  "verbose":{"type":"boolean"},
  "tags":{"type":"array","items":{"type":"string"}},
  "timeoutMs":{"type":"integer","default":60000},
  "build":{"type":"object","additionalProperties":false,"properties":{"module":{"type":"string"},"variant":{"type":"string"}}},
  "target":{"type":"object","additionalProperties":false,"properties":{"text":{"type":"string"},"index":{"type":"integer"},"within":{"type":"object","additionalProperties":false,"properties":{"id":{"type":"string"}}}}},
  "items":{"type":"array","items":{"type":"object","properties":{"from":{"type":"string"}}}},
  "env":{"type":"object","additionalProperties":{"type":"string"}},
  "policy":{"oneOf":[{"type":"object"},{"type":"string"}]}
 }}`

func spec(name, input string, mods ...func(*Spec)) Spec {
	s := Spec{Name: name, Description: "Does " + name + ". More text.", Input: json.RawMessage(input), Scope: capability.Global}
	for _, m := range mods {
		m(&s)
	}
	return s
}

func kitchen() Spec {
	return spec("demo.kitchenSink", kitchenInput, func(s *Spec) {
		s.LongRunning = true
		s.Scope = capability.Workspace
		s.Errors = []string{"ONE", "TWO"}
	})
}

func noEnv(string) string { return "" }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// sameJSON compares two JSON documents by value.
func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("params = %s, want %s", got, want)
	}
}

func parse(t *testing.T, s Spec, env func(string) string, args ...string) Derived {
	t.Helper()
	d, err := ParseDerived(s, args, env)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return d
}

func parseErr(t *testing.T, s Spec, env func(string) string, args ...string) string {
	t.Helper()
	_, err := ParseDerived(s, args, env)
	if err == nil {
		t.Fatalf("%v parsed; want a usage error", args)
	}
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("%v: %T, want *UsageError (exit %d)", args, err, ExitUsage)
	}
	return err.Error()
}

// TestFlagShapes: each row of the flag derivation (a parameter's schema decides its
// flag) parses to the JSON the daemon expects. The array rows are the decision that an
// array flag is repeated and never comma-split (paths hold commas); the object rows,
// that a dotted flag reaches one level and an array of objects is only --params.
func TestFlagShapes(t *testing.T) {
	k := kitchen()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"string, space form", []string{"--name", "Ada"}, `{"name":"Ada"}`},
		{"string, equals form", []string{"--name=Ada Lovelace"}, `{"name":"Ada Lovelace"}`},
		{"integer, negative", []string{"--name", "a", "--count", "-5"}, `{"name":"a","count":-5}`},
		{"number", []string{"--name", "a", "--ratio", "0.25"}, `{"name":"a","ratio":0.25}`},
		{"enum", []string{"--name", "a", "--mode", "slow"}, `{"name":"a","mode":"slow"}`},
		{"boolean bare", []string{"--name", "a", "--verbose"}, `{"name":"a","verbose":true}`},
		{"boolean false", []string{"--name", "a", "--verbose=false"}, `{"name":"a","verbose":false}`},
		{"kebab spelling of a camelCase parameter", []string{"--name", "a", "--timeout-ms", "2000"}, `{"name":"a","timeoutMs":2000}`},
		{"the camelCase spelling matches too", []string{"--name", "a", "--timeoutMs", "2000"}, `{"name":"a","timeoutMs":2000}`},
		{"array repeated in order, never comma-split", []string{"--name", "a", "--tags", "x,y", "--tags", "b", "--tags=a"}, `{"name":"a","tags":["x,y","b","a"]}`},
		{"dotted object flags", []string{"--name", "a", "--build.module", ":app", "--build.variant", "debug"}, `{"name":"a","build":{"module":":app","variant":"debug"}}`},
		{"dotted flag with an integer", []string{"--name", "a", "--target.text", "OK", "--target.index", "2"}, `{"name":"a","target":{"text":"OK","index":2}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sameJSON(t, parse(t, k, noEnv, c.args...).Call.Params, c.want)
		})
	}

	t.Run("an enum value outside the list names the allowed ones", func(t *testing.T) {
		msg := parseErr(t, k, noEnv, "--name", "a", "--mode", "medium")
		if !strings.Contains(msg, "fast, slow") || !strings.Contains(msg, "--mode") {
			t.Errorf("message %q must name the flag and the allowed values", msg)
		}
	})
	t.Run("an integer that does not parse", func(t *testing.T) {
		for _, v := range []string{"abc", "1.5", "1e3", ""} {
			parseErr(t, k, noEnv, "--name", "a", "--count", v)
		}
	})
	t.Run("a number that does not parse", func(t *testing.T) {
		parseErr(t, k, noEnv, "--name", "a", "--ratio", "NaN")
	})
	t.Run("a boolean value that is not one", func(t *testing.T) {
		parseErr(t, k, noEnv, "--name", "a", "--verbose=maybe")
	})
	t.Run("a flag twice", func(t *testing.T) {
		parseErr(t, k, noEnv, "--name", "a", "--name", "b")
	})
	t.Run("a value missing", func(t *testing.T) {
		parseErr(t, k, noEnv, "--name")
	})
	t.Run("an array of objects, an open map, a union and a nested object are not flags", func(t *testing.T) {
		for _, f := range []string{"--items", "--env", "--policy", "--target.within", "--target.within.id"} {
			msg := parseErr(t, k, noEnv, "--name", "a", f, "x")
			if !strings.Contains(msg, "unknown flag") {
				t.Errorf("%s: %q", f, msg)
			}
		}
		help, err := verbHelp(k)
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(help, "Only through --params")
		if !ok {
			t.Fatal("help has no --params-only section")
		}
		after, _, _ = strings.Cut(after, "Every command takes")
		for _, p := range []string{"items", "env", "policy", "target.within"} {
			if !strings.Contains(after, p) {
				t.Errorf("--params-only section must list %s:\n%s", p, after)
			}
		}
		// Hint: the flags are in Flags, not in the --params-only list.
		if strings.Contains(after, "--name") {
			t.Errorf("--name is a flag, listed as --params only:\n%s", after)
		}
	})
	t.Run("positional arguments are refused", func(t *testing.T) {
		parseErr(t, k, noEnv, "--name", "a", "stray")
	})
	t.Run("the CLI's own flags", func(t *testing.T) {
		d := parse(t, k, noEnv, "--name", "a", "--json", "--no-autostart", "--detach", "--workspace", "/p")
		if !d.JSON || !d.Call.NoAutostart || !d.Call.Detach || d.Call.Workspace != "/p" || d.Call.Capability != "demo.kitchenSink" {
			t.Errorf("%+v", d)
		}
	})
	t.Run("--detach and --workspace exist only where they mean something", func(t *testing.T) {
		plain := spec("demo.plain", `{"type":"object","additionalProperties":false,"properties":{}}`)
		parseErr(t, plain, noEnv, "--detach")
		parseErr(t, plain, noEnv, "--workspace", "/p")
	})
	t.Run("a workspace parameter of a global capability is a parameter", func(t *testing.T) {
		s := spec("session.release", `{"type":"object","additionalProperties":false,"properties":{"workspace":{"type":"string"}}}`)
		d := parse(t, s, noEnv, "--workspace", "w1")
		sameJSON(t, d.Call.Params, `{"workspace":"w1"}`)
		if d.Call.Workspace != "" {
			t.Errorf("the envelope workspace was set: %q", d.Call.Workspace)
		}
	})
	t.Run("required keys of an object parameter that is given", func(t *testing.T) {
		s := spec("artifact.read", `{"type":"object","additionalProperties":false,"properties":{"region":{"type":"object","additionalProperties":false,"required":["x","y"],"properties":{"x":{"type":"number"},"y":{"type":"number"}}}}}`)
		parseErr(t, s, noEnv, "--region.x", "0.1")
		parse(t, s, noEnv, "--region.x", "0.1", "--region.y", "0.2")
		parse(t, s, noEnv) // absent: nothing to complete
	})
	t.Run("nothing set is no params at all, as call sends", func(t *testing.T) {
		s := spec("demo.none", `{"type":"object","additionalProperties":false,"properties":{}}`)
		if d := parse(t, s, noEnv); d.Call.Params != nil {
			t.Errorf("params = %s", d.Call.Params)
		}
	})
	t.Run("--help skips the required check", func(t *testing.T) {
		if d := parse(t, k, noEnv, "--help"); !d.Help {
			t.Error("help not seen")
		}
		if d := parse(t, k, noEnv, "-h"); !d.Help {
			t.Error("-h not seen")
		}
	})
}

// TestParamsAndFlagsConflict: --params is the base and a flag adds to it; a key (or
// subkey) set by both is a usage error and never a silent precedence.
func TestParamsAndFlagsConflict(t *testing.T) {
	k := kitchen()

	t.Run("a key set twice is refused", func(t *testing.T) {
		msg := parseErr(t, k, noEnv, "--params", `{"name":"x"}`, "--name", "y")
		if !strings.Contains(msg, "name") || !strings.Contains(msg, "--params") {
			t.Errorf("message %q must name the key and --params", msg)
		}
	})
	t.Run("an array flag over a key --params holds", func(t *testing.T) {
		parseErr(t, k, noEnv, "--params", `{"name":"x","tags":["a"]}`, "--tags", "b")
	})
	t.Run("a subkey set twice is refused", func(t *testing.T) {
		parseErr(t, k, noEnv, "--params", `{"name":"x","target":{"text":"A"}}`, "--target.text", "B")
	})
	t.Run("distinct keys merge", func(t *testing.T) {
		d := parse(t, k, noEnv, "--params", `{"name":"x"}`, "--count", "3")
		sameJSON(t, d.Call.Params, `{"name":"x","count":3}`)
	})
	t.Run("a dotted flag merges into the object --params holds", func(t *testing.T) {
		d := parse(t, k, noEnv, "--params", `{"name":"x","target":{"within":{"id":"list"}}}`, "--target.text", "OK")
		sameJSON(t, d.Call.Params, `{"name":"x","target":{"within":{"id":"list"},"text":"OK"}}`)
	})
	t.Run("a dotted flag over a --params key that is not an object", func(t *testing.T) {
		parseErr(t, k, noEnv, "--params", `{"name":"x","target":"ok"}`, "--target.text", "B")
	})
	t.Run("--params must be a JSON object", func(t *testing.T) {
		for _, v := range []string{`[1]`, `"x"`, `null`, `{`} {
			parseErr(t, k, noEnv, "--params", v)
		}
	})
	t.Run("--params satisfies a required parameter", func(t *testing.T) {
		parse(t, k, noEnv, "--params", `{"name":"x"}`)
	})
}

// TestRequiredBeforeDial: a missing required parameter is a usage error (exit 2) found
// by Dispatch before it calls run, which is where a daemon is dialled or started.
func TestRequiredBeforeDial(t *testing.T) {
	tree, err := NewTree([]Spec{kitchen()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dialled := 0
	var errOut strings.Builder
	code := tree.Dispatch([]string{"demo", "kitchen-sink", "--count", "2"}, noEnv, io.Discard, &errOut, func(Derived) int { dialled++; return 0 })
	if code != ExitUsage || dialled != 0 {
		t.Fatalf("exit %d, dialled %d times; want exit %d and no dial", code, dialled, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "--name") {
		t.Errorf("the error must name the missing flag: %q", errOut.String())
	}
	var got Derived
	code = tree.Dispatch([]string{"demo", "kitchen-sink", "--name", "a"}, noEnv, io.Discard, io.Discard, func(d Derived) int { got = d; return 0 })
	if code != 0 || got.Call.Capability != "demo.kitchenSink" {
		t.Errorf("a complete command must reach run: exit %d, %+v", code, got)
	}
	t.Run("a parameter only --params can carry is named as such", func(t *testing.T) {
		s := spec("demo.copy", `{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":{"type":"object","properties":{"from":{"type":"string"}}}}}}`)
		msg := parseErr(t, s, noEnv)
		if !strings.Contains(msg, "items") || !strings.Contains(msg, "--params") {
			t.Errorf("message %q", msg)
		}
	})
}

// TestAndroidSerial: ANDROID_SERIAL fills a *required* device only, after the flag and
// --params; an optional device is never filled, because absent means something there.
func TestAndroidSerial(t *testing.T) {
	req := spec("device.screenshot", `{"type":"object","additionalProperties":false,"required":["device"],"properties":{"device":{"type":"string","description":"The serial."}}}`)
	opt := spec("device.uiRecordings", `{"type":"object","additionalProperties":false,"properties":{"device":{"type":"string"}}}`)
	env := envOf(map[string]string{"ANDROID_SERIAL": "emulator-5554"})

	sameJSON(t, parse(t, req, env).Call.Params, `{"device":"emulator-5554"}`)
	sameJSON(t, parse(t, req, env, "--device", "R5").Call.Params, `{"device":"R5"}`)
	sameJSON(t, parse(t, req, env, "--params", `{"device":"R6"}`).Call.Params, `{"device":"R6"}`)
	if d := parse(t, opt, env); d.Call.Params != nil {
		t.Errorf("an optional device was filled from ANDROID_SERIAL: %s", d.Call.Params)
	}
	sameJSON(t, parse(t, opt, env, "--device", "R5").Call.Params, `{"device":"R5"}`)
	msg := parseErr(t, req, noEnv)
	if !strings.Contains(msg, "--device") || !strings.Contains(msg, "ANDROID_SERIAL") {
		t.Errorf("message %q must name the flag and the fallback", msg)
	}
}

// TestSpellings: the kebab verb, the camelCase verb, any case and the canonical dotted
// name (which every nextStep prints) are the same command.
func TestSpellings(t *testing.T) {
	ui := spec("device.uiTree", `{"type":"object","additionalProperties":false,"properties":{}}`)
	other := spec("device.list", `{"type":"object","additionalProperties":false,"properties":{}}`)
	tree, err := NewTree([]Spec{ui, other}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"device", "ui-tree"}, {"device", "uiTree"}, {"device", "UITREE"}, {"Device", "ui-tree"},
		{"device.uiTree"}, {"device.ui-tree"}, {"device.uitree"},
	} {
		r, err := tree.Resolve(args)
		if err != nil || r.Spec == nil || r.Spec.Name != "device.uiTree" {
			t.Errorf("%v: %+v, %v", args, r, err)
		}
	}
	if r, err := tree.Resolve([]string{"device", "ui-tree", "--json"}); err != nil || !reflect.DeepEqual(r.Rest, []string{"--json"}) {
		t.Errorf("the arguments after the command must be left for its flags: %+v, %v", r, err)
	}

	t.Run("unknown names get the nearest ones", func(t *testing.T) {
		_, err := tree.Resolve([]string{"device", "ui-tre"})
		if err == nil || !strings.Contains(err.Error(), "ui-tree") {
			t.Errorf("%v", err)
		}
		_, err = tree.Resolve([]string{"devce", "list"})
		if _, ok := err.(*UsageError); !ok || !strings.Contains(err.Error(), "device") {
			t.Errorf("%v", err)
		}
		if _, err := tree.Resolve([]string{"zzzzzz"}); err == nil || !strings.Contains(err.Error(), "droidyard help") {
			t.Errorf("a name near nothing points at help: %v", err)
		}
	})
}

// TestNewTreeRefusesAmbiguity: two verbs that normalise equal, a hand verb a capability
// shadows and a hand command named like a domain are refused at construction, so neither
// can shadow the other silently.
func TestNewTreeRefusesAmbiguity(t *testing.T) {
	empty := `{"type":"object","additionalProperties":false,"properties":{}}`
	h := func(name string) Hand { return Hand{Name: name, Run: func([]string) int { return 0 }} }
	cases := []struct {
		name  string
		specs []Spec
		hands []Hand
	}{
		{"verbs that normalise equal", []Spec{spec("device.uiTree", empty), spec("device.uitree", empty)}, nil},
		{"a hand verb shadowed by a capability", []Spec{spec("device.allow", empty)}, []Hand{h("device allow")}},
		{"a hand command named like a domain", []Spec{spec("share.start", empty)}, []Hand{h("share")}},
		{"a hand command twice", nil, []Hand{h("init"), h("init")}},
		{"a name that is not domain.verb", []Spec{spec("nodot", empty)}, nil},
	}
	for _, c := range cases {
		if _, err := NewTree(c.specs, c.hands); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := NewTree([]Spec{spec("device.list", empty)}, []Hand{h("device allow"), h("init")}); err != nil {
		t.Errorf("hand verbs of a domain beside its capabilities are legitimate: %v", err)
	}
}

// TestCheckSpec: a parameter that normalises equal to another, or takes a flag the CLI
// reserves, would make a command ambiguous; CheckSpec is what the shipped-registry test
// calls on every capability.
func TestCheckSpec(t *testing.T) {
	if err := CheckSpec(kitchen()); err != nil {
		t.Errorf("the fake kitchen sink collides: %v", err)
	}
	dup := spec("demo.dup", `{"type":"object","additionalProperties":false,"properties":{"uiTree":{"type":"string"},"ui-tree":{"type":"string"}}}`)
	if err := CheckSpec(dup); err == nil {
		t.Error("two parameters of one flag accepted")
	}
	for _, p := range []string{"json", "params", "help"} {
		s := spec("demo.x", `{"type":"object","additionalProperties":false,"properties":{"`+p+`":{"type":"string"}}}`)
		if err := CheckSpec(s); err == nil {
			t.Errorf("a parameter named %s accepted", p)
		}
	}
	// detach is reserved only on a long-running capability (hosting.share's is not).
	notLong := spec("hosting.share", `{"type":"object","additionalProperties":false,"properties":{"detach":{"type":"boolean"}}}`)
	if err := CheckSpec(notLong); err != nil {
		t.Errorf("a plain detach parameter on a call that is not long-running: %v", err)
	}
	long := spec("demo.long", `{"type":"object","additionalProperties":false,"properties":{"detach":{"type":"boolean"}}}`, func(s *Spec) { s.LongRunning = true })
	if err := CheckSpec(long); err == nil {
		t.Error("detach parameter on a long-running capability accepted")
	}
	ws := spec("demo.ws", `{"type":"object","additionalProperties":false,"properties":{"workspace":{"type":"string"}}}`, func(s *Spec) { s.Scope = capability.Workspace })
	if err := CheckSpec(ws); err == nil {
		t.Error("workspace parameter on a workspace-scoped capability accepted")
	}
}

func noInputs() []Spec {
	empty := `{"type":"object","additionalProperties":false,"properties":{}}`
	return []Spec{
		spec("device.list", empty),
		spec("device.screenshot", `{"type":"object","additionalProperties":false,"required":["device"],"properties":{"device":{"type":"string","description":"The serial of the device, from device.list."}}}`),
		spec("build.run", `{"type":"object","additionalProperties":false,"required":["tasks"],"properties":{"tasks":{"type":"array","items":{"type":"string"},"description":"Gradle tasks."}}}`, func(s *Spec) { s.LongRunning = true; s.Scope = capability.Workspace }),
		// The ones the listings hide.
		spec("workspace.enter", empty, func(s *Spec) { s.Binding = true }),
		spec("events.unsubscribe", empty, func(s *Spec) { s.Statefulness = capability.Stateful }),
		spec("agent.record", empty),
		spec("device.watchScreen", empty, func(s *Spec) { s.Statefulness = capability.Streaming }),
		spec("device.watchLogs", empty, func(s *Spec) { s.Statefulness = capability.Streaming }),
		spec("share.human", empty, func(s *Spec) { s.Audience = capability.AudienceHuman }),
	}
}

func hands() []Hand {
	run := func([]string) int { return 0 }
	return []Hand{
		{Name: "init", Summary: "set up", Help: "init help text\n", Run: run},
		{Name: "device allow", Summary: "allow a device", Help: "allow help\n", Run: run},
		{Name: "trash", Help: "trash's own\n", OwnHelp: true, Run: run},
	}
}

// TestPartialRegistry: the tree holds exactly the domains it was given (a build that
// registers two gets a tree of two) and the index names nothing of the rest.
func TestPartialRegistry(t *testing.T) {
	empty := `{"type":"object","additionalProperties":false,"properties":{}}`
	tree, err := NewTree([]Spec{spec("alpha.one", empty), spec("alpha.two", empty), spec("beta.go", empty)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := tree.Help(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alpha", "one, two", "beta", "go"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index lacks %q:\n%s", want, idx)
		}
	}
	for _, rest := range []string{"device", "build", "workspace", "screenshot"} {
		if strings.Contains(idx, rest) {
			t.Errorf("index names %q, which this tree does not hold:\n%s", rest, idx)
		}
	}
	if _, err := tree.Resolve([]string{"device", "list"}); err == nil {
		t.Error("a domain this tree lacks resolved")
	}
	if n := strings.Count(idx, "\n  "); n != 2 {
		t.Errorf("want two domain lines, got %d:\n%s", n, idx)
	}
}

// TestHelpLevels: the index lists domains with their verbs and leaves out the commands
// the listings hide (they still run); a domain lists its verbs with hand verbs among
// them; a verb's help lists its required flags and the ANDROID_SERIAL fallback.
func TestHelpLevels(t *testing.T) {
	tree, err := NewTree(noInputs(), hands())
	if err != nil {
		t.Fatal(err)
	}
	tree.Before = []Section{{Title: "Start here", Body: "droidyard device list"}}
	tree.After = []Section{{Title: "The raw door", Body: "droidyard call <capability>"}}

	idx, _ := tree.Help(nil)
	if !strings.Contains(idx, "Start here:\ndroidyard device list") || !strings.Contains(idx, "The raw door:") {
		t.Errorf("the caller's sections are missing:\n%s", idx)
	}
	if strings.Index(idx, "Start here") > strings.Index(idx, "Capabilities") || strings.Index(idx, "Capabilities") > strings.Index(idx, "The raw door") {
		t.Errorf("sections must sit around the generated domains:\n%s", idx)
	}
	for _, want := range []string{"device", "allow", "list", "screenshot", "watch-logs", "build", "run"} {
		if !strings.Contains(idx, want) {
			t.Errorf("index lacks %q:\n%s", want, idx)
		}
	}
	for _, hide := range []string{"agent", "enter", "unsubscribe", "watch-screen", "record"} {
		if strings.Contains(idx, hide) {
			t.Errorf("index lists the hidden %q:\n%s", hide, idx)
		}
	}
	if !strings.Contains(idx, "human") && !strings.Contains(idx, "share") {
		t.Errorf("a human-only capability is shown (the CLI is a human channel):\n%s", idx)
	}

	dev, err := tree.Help([]string{"device"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"allow", "allow a device", "list", "screenshot", "Does device.screenshot."} {
		if !strings.Contains(dev, want) {
			t.Errorf("domain help lacks %q:\n%s", want, dev)
		}
	}
	if strings.Contains(dev, "watch-screen") || strings.Contains(dev, "More text") {
		t.Errorf("domain help must hide hidden verbs and show one sentence each:\n%s", dev)
	}

	shot, err := tree.Help([]string{"device", "screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--device <string> (required; else ANDROID_SERIAL)", "The serial of the device, from device.list.", "Kind: query", "--json", "--params"} {
		if !strings.Contains(shot, want) {
			t.Errorf("verb help lacks %q:\n%s", want, shot)
		}
	}
	run, _ := tree.Help([]string{"build", "run"})
	for _, want := range []string{"--tasks <string> (repeat for each) (required)", "--detach", "--workspace", "long-running"} {
		if !strings.Contains(run, want) {
			t.Errorf("verb help lacks %q:\n%s", want, run)
		}
	}
	if strings.Contains(run, "ANDROID_SERIAL") {
		t.Errorf("the fallback is only for a device:\n%s", run)
	}
	k, _ := verbHelp(kitchen())
	for _, want := range []string{"<fast|slow>", "How.", "(default 60000)", "--build.module", "--target.text", "Errors: ONE, TWO"} {
		if !strings.Contains(k, want) {
			t.Errorf("kitchen help lacks %q:\n%s", want, k)
		}
	}

	t.Run("hidden commands have help and run", func(t *testing.T) {
		if _, err := tree.Help([]string{"agent", "record"}); err != nil {
			t.Error(err)
		}
		r, err := tree.Resolve([]string{"device", "watch-screen"})
		if err != nil || r.Spec == nil {
			t.Errorf("%+v %v", r, err)
		}
	})
	t.Run("a hand command's help, and --help as its only argument", func(t *testing.T) {
		if h, _ := tree.Help([]string{"init"}); h != "init help text\n" {
			t.Errorf("%q", h)
		}
		var out strings.Builder
		ran := false
		tree2, _ := NewTree(nil, []Hand{{Name: "init", Help: "H\n", Run: func([]string) int { ran = true; return 0 }}, {Name: "trash", Help: "T\n", OwnHelp: true, Run: func(a []string) int { ran = len(a) == 1; return 0 }}})
		if code := tree2.Dispatch([]string{"init", "--help"}, noEnv, &out, io.Discard, nil); code != 0 || out.String() != "H\n" || ran {
			t.Errorf("init --help: exit %d, out %q, ran %v", code, out.String(), ran)
		}
		if code := tree2.Dispatch([]string{"trash", "--help"}, noEnv, &out, io.Discard, nil); code != 0 || !ran {
			t.Errorf("trash parses its own --help: exit %d, ran %v", code, ran)
		}
	})
	t.Run("a domain with --help", func(t *testing.T) {
		var out strings.Builder
		if code := tree.Dispatch([]string{"device", "--help"}, noEnv, &out, io.Discard, nil); code != 0 || !strings.Contains(out.String(), "allow") {
			t.Errorf("exit %d:\n%s", code, out.String())
		}
	})
}

package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

func descriptor(t *testing.T) capability.Descriptor {
	t.Helper()
	reg := capability.NewRegistry()
	Register(reg, Options{})
	d, ok := reg.Lookup("agent.recordCommand")
	if !ok {
		t.Fatal("agent.recordCommand is not registered")
	}
	return d
}

// Decision (ADR-0063 §5): agent.recordCommand is the hook's channel and never an MCP tool,
// so its audience is human (the core then refuses an MCP client); it is a command (its
// effect is the record) that is stateless and workspace-scoped; it changes nothing in
// the project or on a device, so it is explicitly not destructive, and it is neither
// idempotent nor open-world. A reversal to audience all would list it as a tool.
func TestRecordCommandDescriptor(t *testing.T) {
	d := descriptor(t)
	a := d.ResolveAnnotations()
	if d.Kind != capability.Command || d.Scope != capability.Workspace || d.Statefulness != capability.Stateless {
		t.Errorf("kind %v, scope %v, statefulness %v; want a stateless workspace command", d.Kind, d.Scope, d.Statefulness)
	}
	if d.Audience != capability.AudienceHuman {
		t.Errorf("audience %v; want human (ADR-0063 §5: never an MCP tool)", d.Audience)
	}
	if a.ReadOnly || a.Destructive || a.Idempotent || a.OpenWorld || a.Reason == "" {
		t.Errorf("annotations %+v; want a non-destructive, non-idempotent, closed-world command with its reason", a)
	}
	if d.Since != "0.22.0" || d.Untrusted() || d.Resource != nil || len(d.Availability) != 0 {
		t.Errorf("since %s, untrusted %v, resource %v, availability %v", d.Since, d.Untrusted(), d.Resource != nil, d.Availability)
	}
	if len(d.Errors) != 1 || d.Errors[0] != capability.ErrInvalidParams {
		t.Errorf("errors %v; want REQUEST_INVALID_PARAMS alone", d.Errors)
	}
}

func cmd(program string, suggested string, words ...string) Command {
	if words == nil {
		words = []string{} // words is required: no word is [], never null
	}
	return Command{Program: program, Words: words, Suggested: suggested}
}

// Decision (ADR-0063 §4, §5, ADR-0065): a valid record comes back as the result, with an
// empty words list normalised; every field outside the record's bounds (a program not
// among the six, a word failing the pattern, more than three words, none or more than four
// commands, another client, a suggestion that is not a tool name) is
// REQUEST_INVALID_PARAMS, so the hook can never leave a record the owner reads wrongly.
func TestRecordCommandHandler(t *testing.T) {
	d := descriptor(t)
	run := func(r Record) (any, error) {
		return d.Handler(context.Background(), &capability.Call{Params: r})
	}

	ok := Record{Client: "claude", Commands: []Command{
		cmd("gradlew", "build_run", ":app:installDebug"),
		cmd("adb", "device_install", "install", "app.apk"),
		cmd("emulator", ""),
	}}
	got, err := run(ok)
	if err != nil {
		t.Fatalf("a valid record: %v", err)
	}
	want := Record{Client: "claude", Commands: []Command{
		cmd("gradlew", "build_run", ":app:installDebug"),
		cmd("adb", "device_install", "install", "app.apk"),
		{Program: "emulator", Words: []string{}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("result %+v; want the record as accepted %+v", got, want)
	}

	five := make([]Command, 5)
	for i := range five {
		five[i] = cmd("adb", "", "devices")
	}
	for _, c := range []struct {
		name string
		rec  Record
	}{
		{"a program that is not observed", Record{"claude", []Command{cmd("rm", "", "x")}}},
		{"a program with a path", Record{"claude", []Command{cmd("./gradlew", "")}}},
		{"a word failing the pattern", Record{"claude", []Command{cmd("adb", "", "push", "a b")}}},
		{"a flag as a word", Record{"claude", []Command{cmd("adb", "", "-s")}}},
		{"a word over 64 characters", Record{"claude", []Command{cmd("adb", "", strings.Repeat("x", 65))}}},
		{"two leading colons", Record{"claude", []Command{cmd("gradlew", "", "::app")}}},
		{"four words", Record{"claude", []Command{cmd("adb", "", "a", "b", "c", "d")}}},
		{"five commands", Record{"claude", five}},
		{"no commands", Record{"claude", nil}},
		{"another client", Record{"vim", []Command{cmd("adb", "", "devices")}}},
		{"no client", Record{"", []Command{cmd("adb", "", "devices")}}},
		{"a suggestion that is not a tool name", Record{"cursor", []Command{cmd("adb", "rm -rf", "devices")}}},
		{"an over-long suggestion", Record{"cursor", []Command{cmd("adb", "device_"+strings.Repeat("x", 33), "devices")}}},
	} {
		_, err := run(c.rec)
		var e *capability.Error
		if !errors.As(err, &e) || e.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS", c.name, err)
		}
	}

	// The limits themselves are accepted: four commands, three words, 64 characters.
	edge := Record{Client: "cursor", Commands: []Command{
		cmd("adb", "", "a", "b", "c"), cmd("adb", ""), cmd("adb", ""),
		cmd("gradle", "", ":"+strings.Repeat("x", 64)),
	}}
	if _, err := run(edge); err != nil {
		t.Errorf("a record at every limit: %v", err)
	}
}

// Decision (ADR-0065): a word may start with one ':' (a Gradle task path from the root)
// and no more; the pattern is otherwise ADR-0063 §4's. A pattern that refused
// `:app:installDebug` would record `gradlew` with no task.
func TestWordPattern(t *testing.T) {
	for w, want := range map[string]bool{
		":app:installDebug": true, "assembleDebug": true, "app.apk": true, "emulator-5554": true, "a": true,
		"::app": false, ":": false, "": false, "-s": false, "a b": false, "a/b": false, "_a": false, "$X": false,
		strings.Repeat("x", 64): true, strings.Repeat("x", 65): false, ":" + strings.Repeat("x", 64): true,
	} {
		if got := Word.MatchString(w); got != want {
			t.Errorf("Word.MatchString(%q) = %v; want %v", w, got, want)
		}
	}
}

// Decision (one fact, one place): the facts about a record are this package's, and the
// schema tags repeat them because a tag must be a literal. The tags and the exported
// values must agree, or the published schema would promise a record the handler refuses
// (or the reverse).
func TestTagsMatchTheExportedFacts(t *testing.T) {
	tag := func(typ reflect.Type, field string) string {
		f, ok := typ.FieldByName(field)
		if !ok {
			t.Fatalf("%s has no field %s", typ, field)
		}
		return f.Tag.Get("jsonschema")
	}
	enums := func(tag string) []string {
		var out []string
		for _, p := range strings.Split(tag, ",") {
			if v, ok := strings.CutPrefix(p, "enum="); ok {
				out = append(out, v)
			}
		}
		return out
	}
	command := reflect.TypeOf(Command{})
	record := reflect.TypeOf(Record{})
	if got := enums(tag(command, "Program")); !reflect.DeepEqual(got, Programs) {
		t.Errorf("Program enum %v; Programs %v", got, Programs)
	}
	if got := enums(tag(record, "Client")); !reflect.DeepEqual(got, Clients) {
		t.Errorf("Client enum %v; Clients %v", got, Clients)
	}
	if w := tag(command, "Words"); !strings.Contains(w, "maxItems=3") || !strings.Contains(w, "pattern="+strings.ReplaceAll(WordPattern, ",", `\,`)) || MaxWords != 3 {
		t.Errorf("Words tag %q; want maxItems=3 and the word pattern", w)
	}
	if s := tag(command, "Suggested"); !strings.Contains(s, "pattern="+strings.ReplaceAll(ToolPattern, ",", `\,`)) {
		t.Errorf("Suggested tag %q; want the tool pattern", s)
	}
	if c := tag(record, "Commands"); !strings.Contains(c, "minItems=1") || !strings.Contains(c, "maxItems=4") || MaxCommands != 4 {
		t.Errorf("Commands tag %q; want 1 to 4 items", c)
	}
}

type admitAll struct{}

func (admitAll) Admit(target string, record func(id string)) bool { record(target); return true }

// Decision (ADR-0063 §4, row 211): the schema marks words required, but decoding gives
// nil for an absent or null field, so the handler refuses it by name; `[]` is the
// accepted way to say there is no word. Run through Operations so decodeParams comes
// first. Accepting nil again fails the first two cases.
func TestRecordCommandRequiresWords(t *testing.T) {
	reg := capability.NewRegistry()
	Register(reg, Options{})
	ops := capability.NewOperations(reg)
	ops.UseWorkspaces(admitAll{})
	call := func(params string) capability.Outcome {
		t.Helper()
		out, err := ops.Invoke(context.Background(), capability.Request{
			Capability: "agent.recordCommand", Workspace: "/p", Client: capability.ClientCLI,
			Params: []byte(params),
		})
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		return out
	}
	for _, c := range []string{
		`{"client":"claude","commands":[{"program":"adb"}]}`,
		`{"client":"claude","commands":[{"program":"adb","words":null}]}`,
	} {
		out := call(c)
		if out.State != capability.StateFailed || out.Error == nil ||
			out.Error.Code != capability.ErrInvalidParams || !strings.Contains(out.Error.Message, "commands[0].words") {
			t.Errorf("%s: state %v, error %+v; want failed REQUEST_INVALID_PARAMS naming commands[0].words", c, out.State, out.Error)
		}
	}
	out := call(`{"client":"claude","commands":[{"program":"adb","words":[]}]}`)
	want := Record{Client: "claude", Commands: []Command{{Program: "adb", Words: []string{}}}}
	if out.State != capability.StateCompleted || !reflect.DeepEqual(out.Result, want) {
		t.Errorf("words []: state %v, result %+v, error %+v; want completed with words []", out.State, out.Result, out.Error)
	}
}

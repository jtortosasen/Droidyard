package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/agent"
)

// TestObserveTable pins ADR-0063 §1, §4 and the hint table: which commands are read,
// what of each is kept (program, plain positional words, the tool), and that nothing a
// flag, a value or an assignment carried reaches the record or the hint.
func TestObserveTable(t *testing.T) {
	type rec struct {
		prog  string
		words []string
		tool  string
	}
	for _, c := range []struct {
		cmd  string
		want []rec
	}{
		{"./gradlew assembleDebug", []rec{{"gradlew", []string{"assembleDebug"}, "build_assemble"}}},
		{"cd app && ../gradlew -p app :app:installDebug 2>&1 | tail -5", []rec{{"gradlew", []string{":app:installDebug"}, "build_run"}}},
		{"./gradlew tasks", []rec{{"gradlew", []string{"tasks"}, "build_tasks"}}},
		{"adb -s emulator-5554 install -r out/app.apk", []rec{{"adb", []string{"install"}, "device_install"}}},
		{"$ANDROID_HOME/platform-tools/adb devices", []rec{{"adb", []string{"devices"}, "device_list"}}},
		{"adb exec-out screencap -p", []rec{{"adb", []string{"exec-out", "screencap"}, "device_screenshot"}}},
		{"adb exec-out run-as com.example cat files/token", []rec{{"adb", []string{"exec-out", "run-as"}, ""}}},
		{"adb shell echo hunter2", []rec{{"adb", []string{"shell", "echo"}, ""}}},
		{"adb shell input text hunter2", []rec{{"adb", []string{"shell", "input"}, "device_inputText"}}},
		{"emulator -avd Pixel -wipe-data", []rec{{"emulator", nil, "emulator_wipe"}}},
		{"emulator -avd Pixel", []rec{{"emulator", nil, "emulator_start"}}},
		{"ANDROID_SERIAL=x adb logcat -d", []rec{{"adb", []string{"logcat"}, "device_logs"}}},
		{"sdkmanager --list", []rec{{"sdkmanager", nil, "sdk_status"}}},
		{"env FOO=1 nohup time avdmanager list avd", []rec{{"avdmanager", []string{"list", "avd"}, "emulator_list"}}},
		{`"./gradlew" build`, []rec{{"gradlew", []string{"build"}, "build_run"}}},
		{"./gradlew assembleDebug && adb install x.apk", []rec{
			{"gradlew", []string{"assembleDebug"}, "build_assemble"},
			{"adb", []string{"install", "x.apk"}, "device_install"}}},
		// A word over the pattern's length or with other characters is not kept.
		{"adb push a/b.txt /sdcard/" + strings.Repeat("x", 80), []rec{{"adb", []string{"push"}, ""}}},
		{"TOKEN=s3cret ./gradlew publish -Ppassword=hunter2 -Dx=y", []rec{{"gradlew", []string{"publish"}, "build_run"}}},
		// Operands that can carry a secret are not kept (close verifier, m2k).
		{"./gradlew run --args secretToken42", []rec{{"gradlew", []string{"run"}, "build_run"}}},
		{"adb emu auth abcdef0123", []rec{{"adb", []string{"emu", "auth"}, ""}}},
		{"adb pair 192.168.1.5:37123 482915", []rec{{"adb", []string{"pair"}, ""}}},
		// Compound commands and prefixes.
		{"for d in a b; do adb devices; done", []rec{{"adb", []string{"devices"}, "device_list"}}},
		{"if adb devices; then echo ok; fi", []rec{{"adb", []string{"devices"}, "device_list"}}},
		{"{ adb devices; }", []rec{{"adb", []string{"devices"}, "device_list"}}},
		{"timeout 60 ./gradlew assembleDebug", []rec{{"gradlew", []string{"assembleDebug"}, "build_assemble"}}},
		{"sudo -n adb devices", []rec{{"adb", []string{"devices"}, "device_list"}}},
		// A here-document's body is text written, not run.
		{"cat > build.sh <<EOF\n./gradlew assembleRelease\nadb install app.apk\nEOF", nil},
		{"./gradlew assembleDebug && cat > notes.txt <<'EOF'\nadb devices\nEOF", []rec{{"gradlew", []string{"assembleDebug"}, "build_assemble"}}},
		// Not observed: the program word is not one of the six.
		{"echo adb", nil},
		{"grep gradle build.gradle", nil},
		{"cat gradlew", nil},
		{"droidyard call build.assemble", nil},
		{"sh -c 'adb devices'", nil},
		{"$(which adb) devices", nil},
		{"$ADB devices", nil},
		{"# adb devices", nil},
		{"adb 'unterminated", nil},
	} {
		got := ObserveCommands(c.cmd)
		var have []rec
		for _, o := range got {
			have = append(have, rec{o.Program, o.Words, o.Suggested})
		}
		if !reflect.DeepEqual(have, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.cmd, have, c.want)
		}
		raw, _ := json.Marshal(got)
		for _, secret := range []string{"hunter2", "s3cret", "x=y", "emulator-5554", "out/app.apk"} {
			if strings.Contains(string(raw)+Hint(got), secret) {
				t.Errorf("%q: %q leaked into the record or the hint", c.cmd, secret)
			}
		}
		for _, o := range got {
			if o.Suggested != "" && !contains(hintTools, o.Suggested) {
				t.Errorf("%q suggests %q, which is not in hintTools", c.cmd, o.Suggested)
			}
		}
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// TestObserveBounds pins ADR-0063 §1 (at most four commands a line) and §4 (at most
// three positional words).
func TestObserveBounds(t *testing.T) {
	if got := ObserveCommands(strings.Repeat("adb devices; ", 6)); len(got) != 4 {
		t.Errorf("%d commands read from six; want 4", len(got))
	}
	got := ObserveCommands("sdkmanager a b c d e")
	if len(got) != 1 || len(got[0].Words) != 3 {
		t.Errorf("words: %+v; want three", got)
	}
}

// TestHintShape pins the hint: one line per distinct tool, in order, within 300 bytes,
// empty when no command has a tool.
func TestHintShape(t *testing.T) {
	obs := ObserveCommands("./gradlew assembleDebug; ./gradlew assembleRelease; adb devices; adb install a; adb logcat; adb uninstall a")
	h := Hint(obs)
	lines := strings.Split(h, "\n")
	if len(lines) < 2 || len(h) > 300 {
		t.Fatalf("hint %d bytes, %d lines: %q", len(h), len(lines), h)
	}
	for _, tool := range []string{"build_assemble", "device_list"} {
		if strings.Count(h, "`"+tool+"`") != 1 {
			t.Errorf("hint names %s %d times; want once: %q", tool, strings.Count(h, "`"+tool+"`"), h)
		}
	}
	if Hint(ObserveCommands("adb shell ls; adb push a b")) != "" {
		t.Error("a hint for commands with no tool")
	}
}

// TestDecodeAfterEvents decodes Cursor's real after-events (plan m2k step 1) and Claude
// Code's, which follow the reference and are not recorded. A Cursor failure that was a
// hook's deny is not an after-command event.
func TestDecodeAfterEvents(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", "hooks", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range []struct {
		client Client
		file   string
		ok     bool
		name   string
		cmd    string
	}{
		{Cursor, "cursor-4-postToolUse.json", true, "postToolUse", "ls"},
		{Cursor, "cursor-5-postToolUse-refused.json", true, "postToolUse", ""},
		{Cursor, "cursor-6-postToolUseFailure.json", true, "postToolUseFailure", "ls /nonexistent-m2k"},
		{Cursor, "cursor-7-postToolUseFailure-denied.json", false, "", ""},
		{Claude, "claude-postToolUse.json", true, "PostToolUse", "./gradlew assembleDebug"},
		{Claude, "claude-postToolUseFailure.json", true, "PostToolUseFailure", "adb -s emulator-5554 install -r out/app.apk"},
		{Claude, "cursor-4-postToolUse.json", false, "", ""},
		{Cursor, "claude-postToolUse.json", false, "", ""},
		{OpenCode, "claude-postToolUse.json", false, "", ""},
		{Cursor, "cursor-3-beforeShellExecution.json", false, "", ""},
	} {
		ev, ok := DecodeAfterEvent(c.client, read(c.file))
		if ok != c.ok {
			t.Errorf("%s %s: ok %v", c.client, c.file, ok)
			continue
		}
		if ok && c.cmd != "" && (ev.Name != c.name || ev.Command != c.cmd) {
			t.Errorf("%s %s: %+v", c.client, c.file, ev)
		}
		if ok && ev.Name != c.name {
			t.Errorf("%s %s: event %q", c.client, c.file, ev.Name)
		}
	}
	// The before-events stay out of DecodeHookEvent's after-handling, and the after-events
	// out of the rm routing.
	if _, ok := DecodeHookEvent(Claude, read("claude-postToolUse.json")); ok {
		t.Error("DecodeHookEvent routed an after-event")
	}
	if _, ok := DecodeAfterEvent(Claude, []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"adb devices"}}`)); ok {
		t.Error("a PreToolUse decoded as an after-event")
	}
}

// TestAfterOutput pins where each client reads the hint: Claude Code's
// additionalContext under the event name it received, Cursor's additional_context; and
// the pass-through with no hint.
func TestAfterOutput(t *testing.T) {
	var m map[string]map[string]string
	if err := json.Unmarshal(AfterOutput(Claude, "PostToolUseFailure", "use `x` <now>"), &m); err != nil {
		t.Fatal(err)
	}
	h := m["hookSpecificOutput"]
	if h["hookEventName"] != "PostToolUseFailure" || h["additionalContext"] != "use `x` <now>" || len(h) != 2 {
		t.Errorf("Claude: %v", m)
	}
	var cu map[string]string
	if err := json.Unmarshal(AfterOutput(Cursor, "postToolUse", "hint"), &cu); err != nil {
		t.Fatal(err)
	}
	if cu["additional_context"] != "hint" || len(cu) != 1 {
		t.Errorf("Cursor: %v", cu)
	}
	if got := AfterOutput(Claude, "PostToolUse", ""); len(got) != 0 {
		t.Errorf("Claude without a hint: %q", got)
	}
	if got := strings.TrimSpace(string(AfterOutput(Cursor, "postToolUse", ""))); got != "{}" {
		t.Errorf("Cursor without a hint: %q", got)
	}
}

// TestObservationsAreWhatTheCapabilityAccepts pins the one-fact-one-place decision of
// plan m2k step 6: the reader keeps the programs, the word pattern and the bounds of
// internal/domain/agent, so every record it can make (a Gradle path from the root, an
// `adb shell` command word, a bare program, four commands on one line) is accepted
// by agent.recordCommand. A reader with its own looser pattern would fail on the
// 65-character row.
func TestObservationsAreWhatTheCapabilityAccepts(t *testing.T) {
	reg := capability.NewRegistry()
	agent.Register(reg)
	d, _ := reg.Lookup("agent.recordCommand")
	for _, line := range []string{
		"./gradlew :app:installDebug --info",
		"adb -s emulator-5554 shell am start -n com.example/.Main",
		"adb exec-out screencap -p",
		"emulator",
		"sdkmanager a b c d e",
		"adb push a b; adb devices; ./gradlew tasks; avdmanager list avd; adb logcat",
		"./gradlew " + strings.Repeat("x", 64) + " :" + strings.Repeat("y", 64),
		"./gradlew " + strings.Repeat("x", 65),
	} {
		obs := ObserveCommands(line)
		if len(obs) == 0 {
			t.Fatalf("%q: nothing observed", line)
		}
		rec := agent.Record{Client: "cursor"}
		for _, o := range obs {
			words := o.Words
			if words == nil {
				words = []string{} // as the hook sends it: words is required, never null
			}
			rec.Commands = append(rec.Commands, agent.Command{Program: o.Program, Words: words, Suggested: o.Suggested})
		}
		if _, err := d.Handler(context.Background(), &capability.Call{Params: rec}); err != nil {
			t.Errorf("%q: the capability refuses what the reader made: %v", line, err)
		}
	}
}

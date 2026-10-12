package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/assistant"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// afterEvent is a client's after-command event, built from the fixtures in
// internal/assistant/testdata/hooks (Claude Code's follow the reference's shape, Cursor's
// were observed, plan m2k step 1), with the command replaced.
func afterEvent(t *testing.T, client, name, command string) string {
	t.Helper()
	switch client {
	case "claude":
		fixture := "claude-postToolUse"
		if name == "PostToolUseFailure" {
			fixture = "claude-postToolUseFailure"
		}
		return cursorFixture(t, fixture, map[string]any{"tool_input": map[string]any{"command": command}})
	default:
		fixture := "cursor-4-postToolUse"
		if name == "postToolUseFailure" {
			fixture = "cursor-6-postToolUseFailure"
		}
		return cursorFixture(t, fixture, map[string]any{"tool_input": map[string]any{"command": command, "cwd": ""}})
	}
}

// hintOf is the text the model reads in an after-command answer: Claude Code's
// hookSpecificOutput.additionalContext, with the event name it received, or Cursor's
// additional_context. It fails the test when stdout is not that.
func hintOf(t *testing.T, client, event, stdout string) string {
	t.Helper()
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		AdditionalContext string `json:"additional_context"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("%s hook printed %q, not one JSON answer: %v", client, stdout, err)
	}
	if client == "claude" {
		if out.HookSpecificOutput.HookEventName != event || out.HookSpecificOutput.AdditionalContext == "" {
			t.Fatalf("claude hook printed %q; want %s with additionalContext", stdout, event)
		}
		return out.HookSpecificOutput.AdditionalContext
	}
	if out.AdditionalContext == "" {
		t.Fatalf("cursor hook printed %q; want additional_context", stdout)
	}
	return out.AdditionalContext
}

// recordsOf is the results of the agent.recordCommand operations that completed on the
// daemon since sub was opened, as the JSON Activity reads: it takes the operation.state
// events and asks operation.wait's source (the operations) for each result. It waits for
// want of them, then a moment more, so a surplus shows.
func recordsOf(t *testing.T, p *inProcess, sub *event.Subscription, want int) []string {
	t.Helper()
	var out []string
	deadline := time.Now().Add(3 * time.Second)
	var settle time.Time
	for {
		e, ok := sub.TryNext()
		if !ok {
			if len(out) >= want {
				if settle.IsZero() {
					settle = time.Now().Add(300 * time.Millisecond)
				}
				if time.Now().After(settle) {
					return out
				}
			}
			if time.Now().After(deadline) {
				return out
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		var st capability.OperationState
		if json.Unmarshal(e.Payload, &st) != nil || st.Capability != "agent.recordCommand" || st.State != capability.StateCompleted {
			continue
		}
		o, err := p.ops.Wait(context.Background(), e.Operation, time.Now().Add(5*time.Second))
		if err != nil || o.State != capability.StateCompleted {
			t.Fatalf("agent.recordCommand %s: %+v, %v", e.Operation, o, err)
		}
		b, _ := json.Marshal(o.Result)
		out = append(out, string(b))
	}
}

// TestHookObservesAnAgentsAndroidCommandsAndRecordsThem holds ADR-0063 §1–§3 end to end on
// a real daemon, for each client's success and failure event: the answer is the hint
// naming the droidyard tool that does the same, with the event name Claude Code sent
// back; the hook opened the workspace; and the daemon holds one completed
// agent.recordCommand with the client and, per command, the program, the positional
// words and the suggested tool. A flag, its value and an assignment, which carry
// secrets, appear nowhere: not in the answer, not in the record, not in the daemon's log
// or the hook's stderr (ADR-0063 §4). A compound command records each Android command.
func TestHookObservesAnAgentsAndroidCommandsAndRecordsThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		client, event, command, tool, record string
	}{
		{"claude", "PostToolUse", "./gradlew assembleDebug -Ppassword=hunter2", "build_assemble",
			`{"client":"claude","commands":[{"program":"gradlew","words":["assembleDebug"],"suggested":"build_assemble"}]}`},
		{"claude", "PostToolUseFailure", "TOKEN=hunter2 adb -s emulator-5554 install -r out/app.apk", "device_install",
			`{"client":"claude","commands":[{"program":"adb","words":["install"],"suggested":"device_install"}]}`},
		{"cursor", "postToolUse", "./gradlew assembleDebug --password=hunter2", "build_assemble",
			`{"client":"cursor","commands":[{"program":"gradlew","words":["assembleDebug"],"suggested":"build_assemble"}]}`},
		{"cursor", "postToolUseFailure", "gradle :app:installDebug && adb devices", "build_run",
			`{"client":"cursor","commands":[{"program":"gradle","words":[":app:installDebug"],"suggested":"build_run"},{"program":"adb","words":["devices"],"suggested":"device_list"}]}`},
	}
	for _, c := range cases {
		t.Run(c.client+" "+c.event, func(t *testing.T) {
			t.Parallel()
			p := startInProcess(t)
			root := hookProject(t)
			sub := p.bus.Subscribe([]string{capability.StateTopic}, nil)
			defer sub.Close()

			if env, code := p.call(t, "workspace.describe", "--workspace", root); code == 0 || env.Error == nil || env.Error.Code != capability.ErrWorkspaceNotOpen {
				t.Fatalf("before the hook, workspace.describe exited %d with %+v; want WORKSPACE_NOT_OPEN", code, env.Error)
			}
			res := runHook(t, p.state, afterEvent(t, c.client, c.event, c.command), nil, "--client", c.client, "--root", root)
			if res.code != 0 || res.stderr != "" {
				t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
			}
			hint := hintOf(t, c.client, c.event, res.stdout)
			if !strings.Contains(hint, "`"+c.tool+"`") {
				t.Errorf("the hint is %q; want it to name %s", hint, c.tool)
			}
			if env, code := p.call(t, "workspace.describe", "--workspace", root); code != 0 || env.Error != nil {
				t.Errorf("after the hook, workspace.describe exited %d with %+v; want the workspace open", code, env.Error)
			}

			got := recordsOf(t, p, sub, 1)
			if len(got) != 1 || got[0] != c.record {
				t.Fatalf("the daemon holds records %v; want exactly %s", got, c.record)
			}
			for what, text := range map[string]string{"the answer": res.stdout, "the record": got[0], "the daemon's log": p.log.String(), "stderr": res.stderr} {
				if strings.Contains(text, "hunter2") || strings.Contains(text, "emulator-5554") {
					t.Errorf("a flag value or an assignment is in %s: %s", what, text)
				}
			}
			// daemon.log names the capability, never the words (ADR-0063 §Consequences).
			if strings.Contains(p.log.String(), "assembleDebug") || strings.Contains(p.log.String(), `"words"`) {
				t.Errorf("the daemon's log holds the record's words: %s", p.log.String())
			}
		})
	}
}

// TestHookObservesWithoutADaemonAnswersTheHintAndStartsNone holds ADR-0063 §3: with
// nothing on the state root's socket the hint still goes, nothing is recorded (there is
// nowhere to), and no daemon is started: the lock and the run directory are absent
// afterwards. A hook that dialled with auto-start would leave both.
func TestHookObservesWithoutADaemonAnswersTheHintAndStartsNone(t *testing.T) {
	t.Parallel()
	for _, client := range []string{"claude", "cursor"} {
		t.Run(client, func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			root := hookProject(t)
			name := map[string]string{"claude": "PostToolUse", "cursor": "postToolUse"}[client]
			res := runHook(t, state, afterEvent(t, client, name, "./gradlew assembleDebug"), nil, "--client", client, "--root", root)
			if res.code != 0 || res.stderr != "" {
				t.Fatalf("hook exited %d with stderr %q; want 0 and none", res.code, res.stderr)
			}
			if hint := hintOf(t, client, name, res.stdout); !strings.Contains(hint, "`build_assemble`") {
				t.Errorf("the hint is %q; want it to name build_assemble", hint)
			}
			if locks := findLocks(t, state); len(locks) != 0 {
				t.Errorf("the hook started a daemon: %v", locks)
			}
			if exists(filepath.Join(state, "run")) {
				t.Error("the state root has a run directory: something started a daemon")
			}
		})
	}
}

// TestHookDoesNotDialForACommandItDoesNotObserve holds ADR-0063 §1: a command with no
// Android program, an after-event of another tool, malformed input, and a Cursor failure
// that was a hook's deny (the command did not run) all pass without the daemon being
// dialled. The control, an Android command, dials once, so a count that never moved would
// show; the daemon's refusal of workspace.open still leaves the hint.
func TestHookDoesNotDialForACommandItDoesNotObserve(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	fake := startFakeDaemon(t, state, true)
	root := hookProject(t)
	denied := cursorFixture(t, "cursor-7-postToolUseFailure-denied", map[string]any{"tool_input": map[string]any{"command": "./gradlew assembleDebug"}})
	rows := []struct{ name, client, event string }{
		{"claude: another command", "claude", afterEvent(t, "claude", "PostToolUse", "ls -la && echo adbx")},
		{"claude: a program named in an argument", "claude", afterEvent(t, "claude", "PostToolUse", "cat gradlew adb")},
		{"claude: a build output removal", "claude", afterEvent(t, "claude", "PostToolUseFailure", "rm -rf build")},
		{"claude: another tool", "claude", strings.Replace(afterEvent(t, "claude", "PostToolUse", "./gradlew assembleDebug"), `"Bash"`, `"Edit"`, 1)},
		{"claude: malformed", "claude", "this is not json"},
		{"claude: empty", "claude", ""},
		{"cursor: another command", "cursor", afterEvent(t, "cursor", "postToolUse", "ls")},
		{"cursor: a hook's deny, which did not run the command", "cursor", denied},
		{"cursor: malformed", "cursor", "{"},
	}
	for _, r := range rows {
		res := runHook(t, state, r.event, nil, "--client", r.client, "--root", root)
		if res.code != 0 || res.stdout != passOf(r.client) {
			t.Errorf("%s: the hook exited %d and printed %q; want 0 and %q", r.name, res.code, res.stdout, passOf(r.client))
		}
	}
	if n := fake.dials.Load(); n != 0 {
		t.Fatalf("the hook dialled the daemon %d times for commands it does not observe; want 0", n)
	}

	res := runHook(t, state, afterEvent(t, "claude", "PostToolUse", "adb devices"), nil, "--client", "claude", "--root", root)
	if res.code != 0 || !strings.Contains(hintOf(t, "claude", "PostToolUse", res.stdout), "`device_list`") {
		t.Errorf("a daemon that refuses workspace.open: the hook exited %d and printed %q; want the hint", res.code, res.stdout)
	}
	if n := fake.dials.Load(); n != 1 {
		t.Errorf("the observed control dialled %d times; want 1", n)
	}
	select {
	case req := <-fake.requests:
		if req.Capability != "workspace.open" || req.Workspace != root {
			t.Errorf("the hook sent %+v; want workspace.open on %s", req, root)
		}
	default:
		t.Error("the daemon saw no request from the control")
	}
}

// TestHookObservesAnswersTheHintWhenTheDaemonIsHeldPastTheDeadline holds ADR-0063 §2 and
// §3: a daemon that takes the connection and never answers workspace.open does not keep
// the agent waiting, and the answer past the 2 s is the hint, not the bare pass-through:
// the hint is computed before anything is dialled. A hook that made the record first and
// the hint after would answer nothing here.
func TestHookObservesAnswersTheHintWhenTheDaemonIsHeldPastTheDeadline(t *testing.T) {
	t.Parallel()
	for _, client := range []string{"claude", "cursor"} {
		t.Run(client, func(t *testing.T) {
			t.Parallel()
			state := shortDir(t)
			fake := startFakeDaemon(t, state, false)
			root := hookProject(t)
			name := map[string]string{"claude": "PostToolUseFailure", "cursor": "postToolUseFailure"}[client]
			res := runHook(t, state, afterEvent(t, client, name, "./gradlew assembleDebug"), nil, "--client", client, "--root", root)
			if res.code != 0 {
				t.Fatalf("hook exited %d; want 0", res.code)
			}
			if hint := hintOf(t, client, name, res.stdout); !strings.Contains(hint, "`build_assemble`") {
				t.Errorf("the hint is %q; want it to name build_assemble", hint)
			}
			if res.elapsed < hookDeadline-100*time.Millisecond || res.elapsed > hookDeadline+3*time.Second {
				t.Errorf("hook answered after %s; want about %s", res.elapsed, hookDeadline)
			}
			select {
			case req := <-fake.requests:
				if req.Capability != "workspace.open" {
					t.Errorf("the held request is %s; want workspace.open", req.Capability)
				}
			default:
				t.Error("the daemon never saw workspace.open: the hold was not what delayed the hook")
			}
			select {
			case req := <-fake.requests:
				t.Errorf("the daemon was sent %s after a held workspace.open; nothing may be recorded", req.Capability)
			default:
			}
		})
	}
}

// TestHookAfterAShellCommandNeverDeniesAndNeverExitsTwo holds ADR-0063 §2 and ADR-0061 §4:
// whatever the after-event is (an Android command that failed, a deny's failure event,
// garbage, a forced panic), the exit is 0 and the answer is a hint or the client's
// pass-through, never a deny: Claude Code reads exit 2 as one, and Cursor a permission of
// deny.
func TestHookAfterAShellCommandNeverDeniesAndNeverExitsTwo(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	root := hookProject(t)
	for _, client := range []string{"claude", "cursor"} {
		name := map[string]string{"claude": "PostToolUseFailure", "cursor": "postToolUseFailure"}[client]
		for _, ev := range []string{
			afterEvent(t, client, name, "adb install broken.apk"),
			afterEvent(t, client, name, "rm -rf a.kt"),
			"{",
		} {
			res := runHook(t, state, ev, nil, "--client", client, "--root", root)
			if res.code != 0 || strings.Contains(res.stdout, "deny") {
				t.Errorf("%s: exit %d, stdout %q; want exit 0 and no deny", client, res.code, res.stdout)
			}
		}
		res := runHook(t, state, afterEvent(t, client, name, "adb devices"), []string{panicEnv + "=1"}, "--client", client, "--root", root)
		if res.code != 0 || res.stdout != passOf(client) || !strings.Contains(res.stderr, "recovered from a panic") {
			t.Errorf("%s with a forced panic: exit %d, stdout %q, stderr %q; want exit 0, %q and a note", client, res.code, res.stdout, res.stderr, passOf(client))
		}
	}
}

// TestCommandRecordSendsAnEmptyWordsListNotNull holds the schema's promise (words is
// required, an array): `emulator -list-avds` has no positional word, and the reader
// returns nil for it, which would encode as null.
func TestCommandRecordSendsAnEmptyWordsListNotNull(t *testing.T) {
	obs := assistant.ObserveCommands("emulator -list-avds")
	if len(obs) != 1 || obs[0].Words != nil {
		t.Fatalf("ObserveCommands = %+v; want one command with nil words (the case this test is about)", obs)
	}
	b, err := json.Marshal(commandRecord(assistant.Cursor, obs))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"client":"cursor","commands":[{"program":"emulator","words":[],"suggested":"emulator_list"}]}`
	if string(b) != want {
		t.Errorf("the record is %s; want %s", b, want)
	}
}

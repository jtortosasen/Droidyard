package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editFixture returns the captured event (plan m2s step 1, docs/research/spike-c2-occupation-2026-10.md
// (a), (e)) with its file path replaced.
func editFixture(t *testing.T, name, path string, set map[string]any) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hooks", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	if in, ok := ev["tool_input"].(map[string]any); ok {
		in["file_path"] = path
	}
	for k, v := range set {
		ev[k] = v
	}
	b, _ := json.Marshal(ev)
	return b
}

// TestDecodeEditEvent holds the decisions of ADR-0095 §3 and plan m2s step 4 for the reader,
// on the events step 1 captured:
//   - Claude Code's Write and Edit fire before and after, and Write says create or update in
//     tool_response.type; an Edit is always an update (it has no type);
//   - cursor-agent's edit tool is Write, for creates and updates alike, and says neither;
//   - the Read cursor-agent makes first of the same path is not an edit, nor is its
//     afterFileEdit (the record comes from postToolUse, once), nor a shell command;
//   - a path that is not absolute, or that agent.recordEdit would refuse, is not decoded, so
//     the hook never sends what the capability would answer with an error.
func TestDecodeEditEvent(t *testing.T) {
	t.Parallel()
	const abs = "/p/wt1/app/Main.kt"
	const claudeChat, cursorChat = "c4f176e6-5e16-466a-9850-1d68bd64d9a2", "75d9a521-1f10-4a6f-bbee-18830fbfdc7c"
	cases := []struct {
		name    string
		client  Client
		fixture string
		path    string // the event's file path; abs when empty
		set     map[string]any
		ok      bool
		after   bool
		created bool
		chat    string
	}{
		{"Claude Write before", Claude, "claude-edit-1-pre-Write", "", nil, true, false, false, claudeChat},
		{"Claude Write after, created", Claude, "claude-edit-2-post-Write-create", "", nil, true, true, true, claudeChat},
		{"Claude Write after, updated", Claude, "claude-edit-3-post-Write-update", "", nil, true, true, false, claudeChat},
		{"Claude Edit before", Claude, "claude-edit-4-pre-Edit", "", nil, true, false, false, claudeChat},
		{"Claude Edit after is an update", Claude, "claude-edit-5-post-Edit", "", nil, true, true, false, claudeChat},
		{"Claude Edit whose response says create is still an update", Claude, "claude-edit-5-post-Edit", "", map[string]any{"tool_response": map[string]any{"type": "create"}}, true, true, false, claudeChat},
		{"Claude NotebookEdit names its notebook_path", Claude, "claude-edit-1-pre-Write", "", map[string]any{"tool_name": "NotebookEdit", "tool_input": map[string]any{"notebook_path": abs}}, true, false, false, claudeChat},
		{"Claude subagent keeps its parent's chat", Claude, "claude-edit-1-pre-Write", "", map[string]any{"agent_id": "a1", "agent_type": "general-purpose"}, true, false, false, claudeChat},
		{"Claude Read is not an edit", Claude, "claude-edit-1-pre-Write", "", map[string]any{"tool_name": "Read"}, false, false, false, ""},
		{"Claude PostToolUseFailure is not decoded", Claude, "claude-edit-2-post-Write-create", "", map[string]any{"hook_event_name": "PostToolUseFailure"}, false, false, false, ""},
		{"Cursor Write before", Cursor, "cursor-edit-2-pre-Write", "", nil, true, false, false, cursorChat},
		{"Cursor Write after says neither create nor update", Cursor, "cursor-edit-3-post-Write", "", nil, true, true, false, cursorChat},
		{"Cursor Read of the same path is not an edit", Cursor, "cursor-edit-1-pre-Read", "", nil, false, false, false, ""},
		{"Cursor afterFileEdit is not decoded", Cursor, "cursor-edit-4-afterFileEdit", "", nil, false, false, false, ""},
		{"a relative path", Claude, "claude-edit-1-pre-Write", "app/Main.kt", nil, false, false, false, ""},
		{"a path with a bidirectional override", Claude, "claude-edit-1-pre-Write", "/p/\u202eMain.kt", nil, false, false, false, ""},
		{"a path over the bound", Claude, "claude-edit-1-pre-Write", "/" + strings.Repeat("a", 4096), nil, false, false, false, ""},
	}
	for _, c := range cases {
		path := c.path
		if path == "" {
			path = abs
		}
		ev, ok := DecodeEditEvent(c.client, editFixture(t, c.fixture, path, c.set))
		if ok != c.ok {
			t.Errorf("%s: decoded %v; want %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if ev.After != c.after || ev.Created != c.created || ev.Chat != c.chat || ev.Path != abs {
			t.Errorf("%s: %+v; want after %v, created %v, chat %q, path %s", c.name, ev, c.after, c.created, c.chat, abs)
		}
	}
	// A shell command is not an edit.
	if _, ok := DecodeEditEvent(Claude, []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)); ok {
		t.Error("a Bash event was decoded as an edit")
	}
	if _, ok := DecodeEditEvent(Claude, []byte(`not json`)); ok {
		t.Error("malformed JSON was decoded")
	}
}

// TestEditAnswers holds the decisions of plan m2s step 4 on what each client is told:
//   - Claude Code's ask is its own permissionDecision "ask", with the reason; a Cursor ask is
//     the deny, since cursor-agent -p lets an ask through (ADR-0094 §6, step 1 (e));
//   - a Cursor refusal puts the way out in user_message, the only text its model reads (and
//     agent_message too, as DenyOutput has it);
//   - a warning reaches Claude Code's model as additionalContext and sets no
//     permissionDecision, so the user's own permission rules still decide; Cursor's pass is
//     untouched.
func TestEditAnswers(t *testing.T) {
	t.Parallel()
	const reason = "droidyard: another chat works in /p/wt1; make a worktree of your own"
	var ask struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(AskOutput(Claude, reason), &ask); err != nil {
		t.Fatal(err)
	}
	if h := ask.HookSpecificOutput; h["hookEventName"] != "PreToolUse" || h["permissionDecision"] != "ask" || h["permissionDecisionReason"] != reason {
		t.Errorf("Claude's ask is %v; want a PreToolUse ask carrying the reason", h)
	}
	var cursor struct {
		Permission   string `json:"permission"`
		UserMessage  string `json:"user_message"`
		AgentMessage string `json:"agent_message"`
	}
	if err := json.Unmarshal(AskOutput(Cursor, reason), &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor.Permission != "deny" || cursor.UserMessage != reason || cursor.AgentMessage != reason {
		t.Errorf("Cursor's ask is %+v; want the deny with the way out in user_message", cursor)
	}
	var warn struct {
		HookSpecificOutput map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(WarnOutput(Claude, reason), &warn); err != nil {
		t.Fatal(err)
	}
	if h := warn.HookSpecificOutput; h["hookEventName"] != "PreToolUse" || h["additionalContext"] != reason || h["permissionDecision"] != "" {
		t.Errorf("Claude's warning is %v; want additionalContext alone", h)
	}
	if got := string(WarnOutput(Cursor, reason)); got != "{}\n" {
		t.Errorf("Cursor's warning is %q; want its pass-through", got)
	}
	if got := WarnOutput(Claude, ""); got != nil {
		t.Errorf("an empty warning is %q; want Claude's pass-through", got)
	}
}

// TestInitWritesEditHooks holds ADR-0095 §3 and ADR-0090 for the edit entries, per client,
// with a user's own hooks in the file already:
//   - written: Claude Code's get the edit tools' matcher on PreToolUse and PostToolUse and none
//     on PostToolUseFailure; Cursor's get Write on preToolUse and postToolUse;
//   - merged and kept: the user's entries (an edit matcher of their own among them) survive
//     init, and a second init writes nothing;
//   - recognised (ADR-0090): an edit entry written for another binary is replaced by init,
//     not left beside the new one, and --uninstall removes it;
//   - removed: --uninstall puts the file back as it was.
//
// Seen failing: with the edit matcher left out of claudeStale (or cursorStale not told of
// Write), the init over an older binary's entries left two edit entries on an event; with
// the edit entries not written, the first row found none.
func TestInitWritesEditHooks(t *testing.T) {
	for _, c := range hookCases {
		t.Run(string(c.client), func(t *testing.T) {
			root := projectRoot(t)
			write(t, root, c.rel, c.mine)
			before := snapshot(t, root)
			o := opts(root, c.client)
			mustRun(t, o)

			editEntries := func(file string) map[string]int {
				var doc struct {
					Hooks map[string][]struct {
						Matcher string `json:"matcher"`
						Command string `json:"command"`
						Hooks   []struct {
							Command string `json:"command"`
						} `json:"hooks"`
					} `json:"hooks"`
				}
				if err := json.Unmarshal([]byte(file), &doc); err != nil {
					t.Fatal(err)
				}
				n := map[string]int{}
				for event, list := range doc.Hooks {
					for _, e := range list {
						cmd := e.Command
						if len(e.Hooks) > 0 {
							cmd = e.Hooks[0].Command
						}
						if strings.Contains(cmd, "droidyard hook") && (e.Matcher == claudeEditMatcher || e.Matcher == cursorEditMatcher) {
							n[event]++
						}
					}
				}
				return n
			}
			want := map[string]int{"PreToolUse": 1, "PostToolUse": 1}
			if c.client == Cursor {
				want = map[string]int{"preToolUse": 1, "postToolUse": 1}
			}
			got := editEntries(read(t, root, c.rel))
			if len(got) != len(want) {
				t.Errorf("%s: edit entries by event %v; want %v", c.rel, got, want)
			}
			for event := range want {
				if got[event] != 1 {
					t.Errorf("%s: %d edit entries on %s; want 1", c.rel, got[event], event)
				}
			}
			if file := read(t, root, c.rel); !strings.Contains(file, `"command": "mine"`) {
				t.Errorf("a user's entry is gone after init:\n%s", file)
			}
			if st := statusOf(t, mustRun(t, o), c.rel).Status; st != Unchanged {
				t.Errorf("a second init is %s; want unchanged", st)
			}

			// An entry written for another binary is init's (ADR-0090): replaced, not doubled.
			old := o
			old.Command = "/old/droidyard"
			stale := opts(root, c.client)
			write(t, root, c.rel, c.mine)
			mustRun(t, old)
			if st := statusOf(t, mustRun(t, stale), c.rel); st.Status != Updated || !strings.Contains(notesOf(st), "replaced") {
				t.Errorf("init over an older binary's entries: %s (%s); want them replaced", st.Status, notesOf(st))
			}
			got = editEntries(read(t, root, c.rel))
			for event := range want {
				if got[event] != 1 {
					t.Errorf("after replacing: %d edit entries on %s; want 1", got[event], event)
				}
			}
			if strings.Contains(read(t, root, c.rel), "/old/") {
				t.Errorf("an old binary's entry is left:\n%s", read(t, root, c.rel))
			}

			// --uninstall removes them, an older binary's included, and the user's stay.
			write(t, root, c.rel, c.mine)
			mustRun(t, old)
			mustRun(t, uninstall(stale))
			if file := read(t, root, c.rel); strings.Contains(file, "droidyard") || !strings.Contains(file, `"command": "mine"`) {
				t.Errorf("after --uninstall: %s; want init's entries gone and the user's kept", file)
			}
			write(t, root, c.rel, c.mine)
			mustRun(t, o)
			mustRun(t, uninstall(o))
			sameTree(t, before, snapshot(t, root))
		})
	}
}

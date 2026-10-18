package assistant

import "testing"

// TestEventsCarryTheirChat holds ADR-0095 §1: a hook event names its chat, Claude Code's
// session_id and Cursor's conversation_id, and only a value the daemon would accept as a
// chat id (daemon.ChatPattern) is kept, so a hello never carries one the daemon clears.
// Cursor's events carry a session_id too; it is conversation_id that names the chat, so
// a Cursor event is not read by the Claude field.
func TestEventsCarryTheirChat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, client, event, chat string
		want                      string
	}{
		{"Claude PreToolUse", "claude", `{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"abc-123","tool_input":{"command":"ls"}}`, "", "abc-123"},
		{"Claude PostToolUse", "claude", `{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"abc-123","tool_input":{"command":"ls"}}`, "after", "abc-123"},
		{"Claude, an id the daemon would clear", "claude", `{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"a b/c","tool_input":{"command":"ls"}}`, "after", ""},
		{"Claude, an id too long", "claude", `{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"0123456789012345678901234567890123456789012345678901234567890123456789","tool_input":{"command":"ls"}}`, "after", ""},
		{"Cursor beforeShellExecution", "cursor", `{"hook_event_name":"beforeShellExecution","command":"ls","conversation_id":"conv-1","session_id":"other"}`, "", "conv-1"},
		{"Cursor postToolUse", "cursor", `{"hook_event_name":"postToolUse","tool_name":"Shell","conversation_id":"conv-1","session_id":"other","tool_input":{"command":"ls"}}`, "after", "conv-1"},
		{"Cursor Delete", "cursor", `{"hook_event_name":"preToolUse","tool_name":"Delete","conversation_id":"conv-1","tool_input":{"file_path":"/p/a"}}`, "", "conv-1"},
		{"Cursor without a conversation_id", "cursor", `{"hook_event_name":"postToolUse","tool_name":"Shell","session_id":"only-session","tool_input":{"command":"ls"}}`, "after", ""},
	}
	for _, c := range cases {
		var got string
		var ok bool
		if c.chat == "after" {
			var ev AfterEvent
			ev, ok = DecodeAfterEvent(Client(c.client), []byte(c.event))
			got = ev.Chat
		} else {
			var ev HookEvent
			ev, ok = DecodeHookEvent(Client(c.client), []byte(c.event))
			got = ev.Chat
		}
		if !ok {
			t.Errorf("%s: the event was not decoded", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s: chat %q; want %q", c.name, got, c.want)
		}
	}
}

// TestLeadingCd holds ADR-0095 §5 for the reader: only a command whose first simple
// command is `cd <dir>` followed by `&&` or `;` names the directory it ran in, and only
// when the directory is one the reader can place without the shell: no expansion, no
// option, and the `cd` is not behind a pipe, an `||`, a background `&` or another command.
func TestLeadingCd(t *testing.T) {
	t.Parallel()
	yes := map[string]string{
		"cd ../wt && ./gradlew tasks":              "../wt",
		"cd ../wt; ./gradlew tasks":                "../wt",
		"cd /abs/wt && ./gradlew tasks":            "/abs/wt",
		"  cd 'my wt' && ./gradlew tasks":          "my wt",
		`cd "../wt dir" && ./gradlew tasks`:        "../wt dir",
		"cd ../wt 2>&1 && ./gradlew tasks":         "../wt",
		"cd ../wt && cd app && ./gradlew tasks":    "../wt",
		"(cd ../wt && ./gradlew tasks)":            "../wt",
		"cd ../wt ; cd - ; ./gradlew tasks":        "../wt",
		"cd ../wt &&\n./gradlew tasks":             "../wt",
		"cd ../wt && ./gradlew tasks | tail -5":    "../wt",
		"cd dir-with-dash && ./gradlew tasks":      "dir-with-dash",
		"cd ../wt;./gradlew tasks":                 "../wt",
		"cd ../wt&&./gradlew tasks":                "../wt",
		"cd ../wt && FOO=1 ./gradlew tasks":        "../wt",
		"cd ../wt && ./gradlew tasks && echo done": "../wt",
	}
	for cmd, want := range yes {
		if got, ok := LeadingCd(cmd); !ok || got != want {
			t.Errorf("LeadingCd(%q) = (%q, %v); want (%q, true)", cmd, got, ok, want)
		}
	}
	no := []string{
		"./gradlew tasks",
		"./gradlew tasks && cd ../wt",
		"echo hi && cd ../wt && ./gradlew tasks",
		"cd ../wt || ./gradlew tasks",
		"cd ../wt | ./gradlew tasks",
		"cd ../wt & ./gradlew tasks",
		"cd ../wt",
		"cd",
		"cd -",
		"cd -P ../wt && ./gradlew tasks",
		"cd ~/wt && ./gradlew tasks",
		"cd $WT && ./gradlew tasks",
		"cd \"$HOME/wt\" && ./gradlew tasks",
		"cd `pwd`/wt && ./gradlew tasks",
		"cd $(pwd)/wt && ./gradlew tasks",
		"cd wt* && ./gradlew tasks",
		"cd {a,b} && ./gradlew tasks",
		"cd a b && ./gradlew tasks",
		"pushd ../wt && ./gradlew tasks",
		"",
	}
	for _, cmd := range no {
		if got, ok := LeadingCd(cmd); ok {
			t.Errorf("LeadingCd(%q) = %q; want no directory", cmd, got)
		}
	}
}

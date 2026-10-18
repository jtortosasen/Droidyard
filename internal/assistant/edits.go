package assistant

import (
	"bytes"
	"encoding/json"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/domain/agent"
)

// The edit-tool events of the clients (ADR-0095 §3), as plan m2s step 1 observed them
// (docs/research/spike-c2-occupation-2026-10.md (a), (e)): Claude Code's Write, Edit and
// NotebookEdit fire PreToolUse and PostToolUse; cursor-agent's edit tool is Write, for
// creates and edits alike, and fires preToolUse and postToolUse. Both carry the file's
// absolute path.

// claudeEditMatcher is the matcher of Claude Code's edit entries. 2.1.302 offers no
// MultiEdit [obs, step 1 (a)].
const claudeEditMatcher = "Write|Edit|NotebookEdit"

// cursorEditMatcher is the tool name of cursor-agent's edit entries.
const cursorEditMatcher = "Write"

// EditEvent is the part of an edit-tool event the hook looks at.
type EditEvent struct {
	// After is false for the event before the write (PreToolUse, preToolUse), true for the
	// one after it.
	After bool
	// Path is the file, absolute and clean.
	Path string
	// Created is true when the event says the tool created the file: Claude Code's Write
	// response type "create". Claude Code's Edit is always an update, and cursor-agent's
	// Write says neither [obs], so it is false for both.
	Created bool
	// Chat is the event's chat id (Claude Code's session_id, Cursor's conversation_id), ""
	// when it carries none or one that is not a chat id (ADR-0095 §1). A subagent's event
	// carries its parent's.
	Chat string
}

// DecodeEditEvent reads one edit-tool event. ok is false for malformed JSON, another event
// or tool, a path that is not absolute or that agent.recordEdit would refuse. Cursor's
// verdict applies to its Write alone, never to the Read it makes first of the same path
// [obs, step 1 (e)].
func DecodeEditEvent(c Client, stdin []byte) (ev EditEvent, ok bool) {
	var raw struct {
		Event     string `json:"hook_event_name"`
		Tool      string `json:"tool_name"`
		SessionID string `json:"session_id"`
		ConvID    string `json:"conversation_id"`
		ToolInput struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		} `json:"tool_input"`
		ToolResponse struct {
			Type string `json:"type"`
		} `json:"tool_response"`
	}
	if json.Unmarshal(stdin, &raw) != nil {
		return EditEvent{}, false
	}
	path := raw.ToolInput.FilePath
	switch c {
	case Claude:
		switch raw.Tool {
		case "Write", "Edit":
		case "NotebookEdit":
			// The notebook tool names its file notebook_path (not observed in step 1).
			if path == "" {
				path = raw.ToolInput.NotebookPath
			}
		default:
			return EditEvent{}, false
		}
		switch raw.Event {
		case "PreToolUse":
		case "PostToolUse":
			ev.After = true
			ev.Created = raw.Tool == "Write" && raw.ToolResponse.Type == "create"
		default:
			return EditEvent{}, false
		}
		ev.Chat = chatOf(raw.SessionID)
	case Cursor:
		if raw.Tool != cursorEditMatcher {
			return EditEvent{}, false
		}
		switch raw.Event {
		case "preToolUse":
		case "postToolUse":
			ev.After = true
		default:
			return EditEvent{}, false
		}
		ev.Chat = chatOf(raw.ConvID)
	default:
		return EditEvent{}, false
	}
	if !filepath.IsAbs(path) || !agent.PathOK(path) {
		return EditEvent{}, false
	}
	ev.Path = filepath.Clean(path)
	return ev, true
}

// AskOutput is the client's ask with reason: Claude Code's permissionDecision "ask", which
// puts the question to the user (and a `-p` run treats as a refusal [obs, step 1 (b)]).
// cursor-agent's -p lets an ask through [obs, step 1 (e)], so its answer is the deny, as
// ADR-0094 §6 has it where Droidyard cannot ask.
func AskOutput(c Client, reason string) []byte {
	if c != Claude {
		return DenyOutput(c, reason)
	}
	return jsonLine(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "ask",
		"permissionDecisionReason": reason,
	}})
}

// WarnOutput is a pass that carries a warning where the model reads it: Claude Code's
// PreToolUse additionalContext, which reaches the model with the write allowed [obs, step 1
// (b3)]. The answer sets no permissionDecision, so the user's own permission rules still
// decide; that step 1 ran it with a decision of its own, and that the context reaches the
// model without one, is an assumption, not observed. cursor-agent is passed untouched: no
// answer that reaches its model on a pre-edit event was observed.
func WarnOutput(c Client, warning string) []byte {
	if c != Claude || warning == "" {
		return PassOutput(c)
	}
	return jsonLine(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName":     "PreToolUse",
		"additionalContext": warning,
	}})
}

func jsonLine(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

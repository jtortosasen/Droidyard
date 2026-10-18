package assistant

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/domain/agent"
)

// The reader behind the hook's after-command events (ADR-0063): the Android commands an
// agent ran in a shell, read lexically, so that the hook can name the droidyard tool
// that does the same and record that the command ran. Pure: no daemon, no dial, no I/O.
// It is not a boundary: `sh -c`, scripts and aliases are not seen. What it keeps of a
// command is bounded by ADR-0063 §4 (program, at most three plain positional words, the
// suggested tool); flags, flag values, assignments and the line itself are dropped.

// What a record may hold (the programs, the word pattern, the bounds) is
// internal/domain/agent's: agent.recordCommand validates the same values, so the hook
// can never produce a record the capability refuses.

// maxHintBytes bounds the hint (plan m2k step 5).
const maxHintBytes = 300

// Observed is one Android command found in a shell command line.
type Observed struct {
	// Program is the base name: gradlew, gradle, adb, emulator, sdkmanager or avdmanager.
	Program string
	// Words are the positional words kept: at most three matching agent.Word and, for
	// `adb shell`, only the shell's command word.
	Words []string
	// Suggested is the droidyard MCP tool that does the same ("" when there is none).
	Suggested string
}

// observedPrograms are the base names read (ADR-0063 §1): agent.Programs.
var observedPrograms = setOf(agent.Programs...)

// skippedPrefixes are the words that run the next command (ADR-0063 §1).
var skippedPrefixes = map[string]bool{"command": true, "exec": true, "env": true, "time": true, "nohup": true,
	// Words that open a compound command or run the next one (close verifier, m2k).
	"if": true, "then": true, "else": true, "elif": true, "while": true, "until": true, "do": true,
	"{": true, "!": true, "sudo": true, "timeout": true}

// valueFlags lists, per program, the flags that take the next word as their value, so
// that the value is not read as a positional word (`-p app`, `-s emulator-5554`). A flag
// written `--name=value` or with its value attached needs no entry.
var valueFlags = map[string]map[string]bool{
	"gradlew": gradleValueFlags,
	"gradle":  gradleValueFlags,
	"adb":     setOf("-s", "-t", "-H", "-P", "-L"),
	"emulator": setOf("-avd", "-port", "-ports", "-netspeed", "-netdelay", "-skin", "-memory", "-partition-size",
		"-sdcard", "-gpu", "-camera-back", "-camera-front", "-timezone", "-dns-server", "-http-proxy", "-cores",
		"-data", "-system", "-kernel", "-ramdisk", "-cache", "-accel", "-engine", "-snapshot", "-tcpdump",
		"-writable-system", "-prop", "-feature", "-lang", "-locale", "-logcat", "-qemu"),
	"sdkmanager": setOf("--channel", "--proxy", "--proxy_host", "--proxy_port", "--sdk_root"),
	"avdmanager": setOf("-n", "--name", "-k", "--package", "-d", "--device", "-p", "--path", "-c", "--sdcard",
		"-b", "--abi", "-t", "--tag", "-v"),
}

var gradleValueFlags = setOf("-p", "--project-dir", "-g", "--gradle-user-home", "-c", "--settings-file", "-b",
	"--build-file", "-I", "--init-script", "-x", "--exclude-task", "-M", "--write-verification-metadata", "-F",
	"--dependency-verification", "--include-build", "--max-workers", "--warning-mode", "--console", "--priority",
	"--project-cache-dir", "--project-prop", "--system-prop", "--tests", "--args")

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// hintTools is every tool the hint can name; cmd/droidyard tests each against tools/list.
var hintTools = []string{
	"build_assemble", "build_tasks", "build_run",
	"device_install", "device_logs", "device_list", "device_launch", "device_uninstall",
	"device_uiTree", "device_screenshot", "device_tapElement", "device_inputText",
	"emulator_list", "emulator_start", "emulator_wipe",
	"sdk_status",
}

// HintTools returns the MCP tool names the hint table can suggest.
func HintTools() []string { return append([]string(nil), hintTools...) }

// ObserveCommands reads the Android commands in a shell command line, at most four.
func ObserveCommands(command string) []Observed {
	var out []Observed
	for _, seg := range shellSegments(command) {
		if len(out) == agent.MaxCommands {
			break
		}
		if o, ok := observeSegment(seg); ok {
			out = append(out, o)
		}
	}
	return out
}

func observeSegment(words []string) (Observed, bool) {
	i := 0
	for i < len(words) {
		w := words[i]
		switch {
		case skippedPrefixes[w] || isAssignment(w):
			i++
			switch w {
			case "env", "sudo":
				for i < len(words) && (isAssignment(words[i]) || strings.HasPrefix(words[i], "-")) {
					i++
				}
			case "timeout":
				for i < len(words) && strings.HasPrefix(words[i], "-") {
					i++
				}
				if i < len(words) {
					i++ // the duration
				}
			}
			continue
		}
		break
	}
	if i >= len(words) {
		return Observed{}, false
	}
	prog := path.Base(words[i])
	if !observedPrograms[prog] {
		return Observed{}, false
	}
	var raw, matching []string
	flags := map[string]bool{}
	endOpts := false
	for args := words[i+1:]; len(args) > 0; args = args[1:] {
		a := args[0]
		if !endOpts && a == "--" {
			endOpts = true
			continue
		}
		if !endOpts && len(a) > 1 && a[0] == '-' {
			name, _, hasValue := strings.Cut(a, "=")
			flags[name] = true
			if !hasValue && valueFlags[prog][a] && len(args) > 1 {
				args = args[1:]
			}
			continue
		}
		raw = append(raw, a)
		if agent.Word.MatchString(a) {
			matching = append(matching, a)
		}
	}
	o := Observed{Program: prog, Suggested: suggest(prog, flags, raw, matching)}
	if prog == "adb" && len(raw) > 0 && raw[0] == "pair" {
		// Its operands are an address and a pairing code: neither is kept.
		o.Words = []string{"pair"}
	} else if prog == "adb" && len(raw) > 0 && (raw[0] == "shell" || raw[0] == "exec-out" || raw[0] == "emu") {
		// exec-out runs a device command as shell does, and emu a console command (whose
		// operands can be its auth token): only that command's first word is kept.
		o.Words = []string{raw[0]}
		if len(raw) > 1 && agent.Word.MatchString(raw[1]) {
			o.Words = append(o.Words, raw[1])
		}
	} else {
		o.Words = matching
		if len(o.Words) > agent.MaxWords {
			o.Words = o.Words[:agent.MaxWords]
		}
	}
	return o, true
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j, c := range w[:i] {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// at is raw[i] or "".
func at(raw []string, i int) string {
	if i < len(raw) {
		return raw[i]
	}
	return ""
}

// suggest is the hint table: program, flags and words to an MCP tool, "" for none.
func suggest(prog string, flags map[string]bool, raw, matching []string) string {
	switch prog {
	case "gradlew", "gradle":
		for _, w := range matching {
			task := w[strings.LastIndexByte(w, ':')+1:]
			switch {
			case strings.HasPrefix(task, "assemble"):
				return "build_assemble"
			case task == "tasks":
				return "build_tasks"
			case task != "":
				return "build_run"
			}
		}
	case "adb":
		switch sub := at(raw, 0); sub {
		case "devices":
			return "device_list"
		case "install", "install-multiple", "install-multi-package":
			return "device_install"
		case "uninstall":
			return "device_uninstall"
		case "logcat":
			return "device_logs"
		case "shell", "exec-out":
			switch at(raw, 1) {
			case "screencap":
				return "device_screenshot"
			case "uiautomator":
				if at(raw, 2) == "dump" {
					return "device_uiTree"
				}
			case "logcat":
				return "device_logs"
			case "input":
				switch at(raw, 2) {
				case "tap":
					return "device_tapElement"
				case "text":
					return "device_inputText"
				}
			case "am":
				if s := at(raw, 2); s == "start" || s == "start-activity" {
					return "device_launch"
				}
			case "monkey":
				return "device_launch"
			case "pm":
				switch at(raw, 2) {
				case "install":
					return "device_install"
				case "uninstall":
					return "device_uninstall"
				}
			}
		}
	case "emulator":
		switch {
		case flags["-wipe-data"]:
			return "emulator_wipe"
		case flags["-list-avds"]:
			return "emulator_list"
		case flags["-avd"] || strings.HasPrefix(at(raw, 0), "@"):
			return "emulator_start"
		}
	case "sdkmanager":
		if flags["--list"] || flags["--list_installed"] {
			return "sdk_status"
		}
	case "avdmanager":
		if at(raw, 0) == "list" {
			return "emulator_list"
		}
	}
	return ""
}

// Hint is the line or lines handed back to the agent beside the command's result: one
// per distinct suggested tool, at most 300 bytes. "" when no command has a tool.
func Hint(obs []Observed) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, o := range obs {
		if o.Suggested == "" || seen[o.Suggested] {
			continue
		}
		seen[o.Suggested] = true
		line := "droidyard: the MCP tool `" + o.Suggested + "` does this with structured results; prefer it next time."
		sep := 0
		if b.Len() > 0 {
			sep = 1
		}
		if b.Len()+sep+len(line) > maxHintBytes {
			break
		}
		if sep == 1 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// shellSegments splits a command line at unquoted `;`, `&&`, `||`, `|`, `&`, newlines
// and parentheses, into simple commands of unquoted words. Redirections and their
// targets are dropped, comments are ignored, and an unterminated quote ends the reading
// (the partial command is dropped). A word keeps its text, `$` and all: only a program
// whose base name is literal can match.
func shellSegments(s string) [][]string {
	segs, _ := shellScan(s)
	return segs
}

// shellScan is shellSegments with the separator that ended each segment: ";", "&&",
// "||", "|", "&", "\n", "(" or ")", and "" for the end of the line. seps is as long as
// segs.
func shellScan(s string) (segs [][]string, seps []string) {
	var words []string
	var cur []rune
	started, skipNext, digitsOnly, heredoc := false, false, true, false
	endWord := func() {
		if started {
			w := string(cur)
			if skipNext {
				skipNext = false
			} else {
				words = append(words, w)
			}
		}
		cur, started, digitsOnly = nil, false, true
	}
	endSeg := func(sep string) {
		endWord()
		skipNext = false
		if len(words) > 0 {
			segs = append(segs, words)
			seps = append(seps, sep)
		}
		words = nil
	}
	add := func(r rune, digit bool) {
		cur = append(cur, r)
		started = true
		if !digit {
			digitsOnly = false
		}
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch r {
		case ' ', '\t', '\r':
			endWord()
		case '\n':
			endSeg("\n")
			if heredoc {
				// A here-document's body is text, not commands: the reading stops.
				return segs, seps
			}
		case ';', '(', ')':
			endSeg(string(r))
		case '|':
			sep := "|"
			if i < len(s) && s[i] == '|' {
				sep = "||"
			}
			endSeg(sep)
			if i < len(s) && (s[i] == '|' || s[i] == '&') {
				i++
			}
		case '&':
			if i < len(s) && s[i] == '>' { // &>file
				endWord()
				i++
				if i < len(s) && s[i] == '>' {
					i++
				}
				skipNext = true
				continue
			}
			sep := "&"
			if i < len(s) && s[i] == '&' {
				sep = "&&"
			}
			endSeg(sep)
			if sep == "&&" {
				i++
			}
		case '<', '>':
			if started && !digitsOnly {
				endWord()
			} else {
				cur, started, digitsOnly = nil, false, true // an fd number: `2>&1`
			}
			if r == '<' && strings.HasPrefix(s[i:], "<") && !strings.HasPrefix(s[i:], "<<") {
				heredoc = true // `<<` or `<<-`, not the here-string `<<<`
			}
			for i < len(s) && (s[i] == '>' || s[i] == '<' || s[i] == '&' || s[i] == '|') {
				i++
			}
			skipNext = true
		case '#':
			if started {
				add(r, false)
				continue
			}
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '\'':
			end := strings.IndexByte(s[i:], '\'')
			if end < 0 {
				return segs, seps
			}
			for _, q := range s[i : i+end] {
				add(q, false)
			}
			started = true
			i += end + 1
		case '"':
			started = true
			closed := false
			for i < len(s) {
				q, n := utf8.DecodeRuneInString(s[i:])
				i += n
				if q == '"' {
					closed = true
					break
				}
				if q == '\\' && i < len(s) {
					nq, nn := utf8.DecodeRuneInString(s[i:])
					if nq == '$' || nq == '`' || nq == '"' || nq == '\\' {
						q = nq
						i += nn
					} else if nq == '\n' {
						i += nn
						continue
					}
				}
				add(q, false)
			}
			if !closed {
				return segs, seps
			}
		case '\\':
			if i >= len(s) {
				return segs, seps
			}
			q, n := utf8.DecodeRuneInString(s[i:])
			i += n
			if q != '\n' {
				add(q, false)
			}
		default:
			add(r, r >= '0' && r <= '9')
		}
	}
	endSeg("")
	return segs, seps
}

// LeadingCd is the directory a command changes to before anything else runs: its first
// simple command is `cd <dir>`, followed by `&&` or `;` (ADR-0095 §5). What the event's
// directory then is depends on where the shell ended up (plan m2s step 1 (c)), which is
// for the caller to weigh: this only reads the `cd`. dir is as typed, relative to the
// command's directory; ok is
// false for any other first command, a `cd` with an option or no operand, `cd -`, and a
// directory the shell would expand (`~`, `$`, a command substitution, a glob or brace),
// which the reader cannot place. Lexical only: nothing is read from the file system.
func LeadingCd(command string) (dir string, ok bool) {
	segs, seps := shellScan(command)
	if len(segs) == 0 || len(segs[0]) != 2 || segs[0][0] != "cd" {
		return "", false
	}
	if seps[0] != "&&" && seps[0] != ";" {
		return "", false
	}
	dir = segs[0][1]
	if dir == "" || strings.HasPrefix(dir, "-") || strings.HasPrefix(dir, "~") || strings.ContainsAny(dir, "$`*?[{") {
		return "", false
	}
	return dir, true
}

// AfterEvent is what the reader takes from a Claude Code PostToolUse or
// PostToolUseFailure event, or a Cursor postToolUse or postToolUseFailure.
type AfterEvent struct {
	// Name is the client's event name, as received.
	Name string
	// Command is the shell command that ran.
	Command string
	// Cwd is the directory the command ran in, as the client reported it; "" when the
	// client sends none (Cursor, as observed in plan m2q step 1) (ADR-0085).
	Cwd string
	// Chat is the event's chat id (Claude Code's session_id, Cursor's conversation_id),
	// "" when it carries none or one that is not a chat id (ADR-0095 §1).
	Chat string
}

// DecodeAfterEvent reads a shell command's after-event. ok is false for malformed JSON,
// another event or tool, an empty command, and a Cursor failure that was a hook's deny
// (`failure_type: "permission_denied"`: the command did not run). It reads the command
// the directory and the names only: never the output, the error text or the user's email
// (ADR-0063 §4).
func DecodeAfterEvent(c Client, stdin []byte) (ev AfterEvent, ok bool) {
	var raw struct {
		Event       string `json:"hook_event_name"`
		Tool        string `json:"tool_name"`
		Cwd         string `json:"cwd"`
		FailureType string `json:"failure_type"`
		SessionID   string `json:"session_id"`
		ConvID      string `json:"conversation_id"`
		ToolInput   struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(stdin, &raw) != nil || raw.ToolInput.Command == "" {
		return AfterEvent{}, false
	}
	switch c {
	case Claude:
		if (raw.Event == "PostToolUse" || raw.Event == "PostToolUseFailure") && raw.Tool == "Bash" {
			return AfterEvent{Name: raw.Event, Command: raw.ToolInput.Command, Cwd: raw.Cwd, Chat: chatOf(raw.SessionID)}, true
		}
	case Cursor:
		if (raw.Event == "postToolUse" || raw.Event == "postToolUseFailure") && raw.Tool == "Shell" && raw.FailureType != "permission_denied" {
			// Cursor's cwd was "" in every observed event (plan m2q step 1): the field is
			// read all the same, and "" keeps the hook's --root.
			return AfterEvent{Name: raw.Event, Command: raw.ToolInput.Command, Cwd: raw.Cwd, Chat: chatOf(raw.ConvID)}, true
		}
	}
	return AfterEvent{}, false
}

// AfterOutput is what the hook prints after a shell command: the hint where the model
// reads it (Claude Code's hookSpecificOutput.additionalContext, with the event name it
// received; Cursor's additional_context), or the client's pass-through with no hint.
func AfterOutput(c Client, event, hint string) []byte {
	if hint == "" {
		return PassOutput(c)
	}
	var v any
	switch c {
	case Claude:
		v = map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": hint,
		}}
	case Cursor:
		v = map[string]any{"additional_context": hint}
	default:
		return nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

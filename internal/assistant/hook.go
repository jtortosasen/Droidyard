package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/daemon"
)

// The reader behind `droidyard hook` (ADR-0061): one hook event in, a decision out,
// with no process, daemon call or output of its own. It recognises a simple `rm` whose
// every target `rm` itself would delete and lies inside the project root; anything else
// passes untouched (fail open). It is not a security boundary: `/bin/rm`, `sh -c` and
// `find -delete` pass.
//
// The only I/O is reading the file system under the root: ReadDir to expand a glob,
// Lstat and EvalSymlinks to place a target. Nothing is written. What the reader hands
// back is the operand words as typed, for `droidyard trash` to resolve in the agent's
// own shell (ADR-0062); the placing here only decides whether the `rm` is routed.

// maxRoutedPaths is file.delete's batch bound; a command with more targets passes.
const maxRoutedPaths = 1000

// regenerable names a target that is or lies under: build outputs, which trashing
// would use up the trash's cap on (ADR-0061 §2). Compared in lower case: on a
// case-insensitive volume (macOS's default) `Build` is the same directory (step 7–8
// verifier N1); on a case-sensitive one a user's `Build` then passes, the fail-open side.
var regenerable = map[string]bool{"build": true, ".gradle": true, ".kotlin": true, ".cxx": true}

// HookEvent is the part of one client event the reader looks at.
type HookEvent struct {
	// Command is the shell command, for a Claude Code `PreToolUse` Bash event or a
	// Cursor `beforeShellExecution`.
	Command string
	// Cwd is the shell's directory as the client reports it; "" when it does not (Cursor
	// carries "" [obs, m2j step 6]), and a relative operand then routes on lexical checks
	// only (ADR-0062 §5).
	Cwd string
	// DeletePath is the absolute path of a Cursor `Delete` tool call; Command is "" then.
	DeletePath string
	// Chat is the event's chat id (Claude Code's session_id, Cursor's conversation_id),
	// "" when it carries none or one that is not a chat id (ADR-0095 §1).
	Chat string
}

// DecodeHookEvent reads one event from the client's stdin. ok is false for malformed
// JSON and for any event this client does not route: another event name, another tool.
func DecodeHookEvent(c Client, stdin []byte) (ev HookEvent, ok bool) {
	var raw struct {
		Event     string `json:"hook_event_name"`
		Tool      string `json:"tool_name"`
		Command   string `json:"command"`
		Cwd       string `json:"cwd"`
		SessionID string `json:"session_id"`
		ConvID    string `json:"conversation_id"`
		ToolInput struct {
			Command  string `json:"command"`
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(stdin, &raw) != nil {
		return HookEvent{}, false
	}
	switch c {
	case Claude:
		if raw.Event != "PreToolUse" || raw.Tool != "Bash" || raw.ToolInput.Command == "" {
			return HookEvent{}, false
		}
		return HookEvent{Command: raw.ToolInput.Command, Cwd: raw.Cwd, Chat: chatOf(raw.SessionID)}, true
	case Cursor:
		chat := chatOf(raw.ConvID)
		switch {
		case raw.Event == "beforeShellExecution" && raw.Command != "":
			return HookEvent{Command: raw.Command, Cwd: raw.Cwd, Chat: chat}, true
		case raw.Event == "preToolUse" && raw.Tool == "Delete" && raw.ToolInput.FilePath != "":
			return HookEvent{DeletePath: raw.ToolInput.FilePath, Chat: chat}, true
		}
	}
	return HookEvent{}, false
}

// chatOf is an event's chat id when it is one the daemon accepts (daemon.ChatPattern), else
// "": a hello never carries a value the daemon would clear (ADR-0095 §1).
func chatOf(id string) string {
	if daemon.ChatPattern.MatchString(id) {
		return id
	}
	return ""
}

// Route is a delete the hook redirects to `droidyard trash`: the operands as typed and
// the flags `rm` was given.
type Route struct {
	// Paths are the targets, root-relative and slash-separated, in the order `rm` would
	// receive them, without duplicates. An operand the hook could not place (a relative
	// one with no cwd, ADR-0062 §5) is listed as its lexical path relative to the
	// shell's directory, a glob unexpanded.
	Paths []string
	// Recursive and Force are whether -r/-R and -f were typed.
	Recursive, Force bool

	operands []word
	// endOptions is whether `--` was typed. The replacement carries `--` only then, so
	// a glob that expands to a `-name` is read by `trash` as `rm` reads it: a flag, or a
	// usage error that moves nothing (step 2–3 verifier N1).
	endOptions bool
}

// rmOperand is one typed operand, with where the hook placed it: the absolute paths it
// expands to, or, when there is no cwd to place a relative operand, its lexical path.
type rmOperand struct {
	w       word
	targets []string
	lexical string
	// afterDashDash is whether `--` came before it.
	afterDashDash bool
}

// RouteRm decides whether the event is a delete the hook redirects to the trash. ok is
// false for a pass. root is an absolute path. Where the hook can place a target (an
// absolute cwd or operand) every target must be one `rm` itself would delete inside the
// root; a relative operand of an event with no cwd routes when it has no `..` or build
// output component and is not `.`, and `trash` checks the rest (ADR-0062 §5).
func RouteRm(ev HookEvent, root string) (rt Route, ok bool) {
	if !filepath.IsAbs(root) {
		return Route{}, false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Route{}, false
	}
	var ops []rmOperand
	if ev.DeletePath != "" {
		// The Delete tool is not a shell: the path is literal, with no glob. Without
		// -r semantics, a directory passes.
		if !filepath.IsAbs(ev.DeletePath) {
			return Route{}, false
		}
		w := make(word, 0, len(ev.DeletePath))
		for _, r := range ev.DeletePath {
			w = append(w, wordRune{r, true})
		}
		ops = []rmOperand{{w: w, targets: []string{ev.DeletePath}}}
	} else {
		ops, rt.Recursive, rt.Force, ok = parseRm(ev.Command, ev.Cwd)
		if !ok {
			return Route{}, false
		}
		// `--` after an operand is refused by parseRm, so the first operand says it.
		rt.endOptions = ops[0].afterDashDash
	}
	n := 0
	for _, op := range ops {
		n += len(op.targets)
		if op.lexical != "" {
			n++
		}
	}
	if n == 0 || n > maxRoutedPaths {
		return Route{}, false
	}
	seen := map[string]bool{}
	add := func(rel string) {
		if !seen[rel] {
			seen[rel] = true
			rt.Paths = append(rt.Paths, rel)
		}
	}
	for _, op := range ops {
		rt.operands = append(rt.operands, op.w)
		if op.lexical != "" {
			add(op.lexical)
			continue
		}
		for _, t := range op.targets {
			rel, isDir, ok := placeTarget(t, realRoot)
			if !ok || (isDir && !rt.Recursive) {
				return Route{}, false
			}
			add(rel)
		}
	}
	return rt, true
}

// parseRm reads a command as a simple `rm`: the word `rm`, flags among -r -R -f and
// `--`, and operands. Each operand's targets are absolute and uncleaned (a `..` or a
// link in them is still the shell's to resolve, which placeTarget does); with cwd ""
// a relative operand has its lexical path instead. ok is false for anything else.
func parseRm(command, cwd string) (ops []rmOperand, recursive, force, ok bool) {
	words, ok := splitWords(command)
	if !ok || len(words) < 2 || words[0].String() != "rm" || words[0].quoted() {
		return nil, false, false, false
	}
	// Flags end at `--` or at the first operand: BSD rm's getopt does not permute, so a
	// later `-r` is a file name to it, and a word that expands to a name starting with
	// `-` before `--` is a flag to it. Either passes (step 7–8 verifier N2).
	endOpts, seenOperand := false, false
	total := 0
	for _, w := range words[1:] {
		s := w.String()
		if !endOpts && seenOperand && strings.HasPrefix(s, "-") {
			return nil, false, false, false
		}
		if !endOpts && strings.HasPrefix(s, "-") {
			if s == "--" {
				endOpts = true
				continue
			}
			if len(s) < 2 {
				return nil, false, false, false
			}
			for _, r := range s[1:] {
				switch r {
				case 'r', 'R':
					recursive = true
				case 'f':
					force = true
				default:
					return nil, false, false, false
				}
			}
			continue
		}
		if len(w) == 0 {
			return nil, false, false, false // rm '' : an error of rm's, not a delete
		}
		seenOperand = true
		op := rmOperand{w: w, afterDashDash: endOpts}
		if cwd == "" && !strings.HasPrefix(s, "/") {
			// Cursor carries no cwd: nothing to place a relative operand against, and no
			// file system access for it (ADR-0062 §5).
			if op.lexical, ok = lexicalOperand(s); !ok {
				return nil, false, false, false
			}
			total++
		} else {
			if op.targets, ok = expandWord(w, cwd); !ok {
				return nil, false, false, false
			}
			if !endOpts && !strings.HasPrefix(s, "/") {
				for _, g := range op.targets {
					if r, err := filepath.Rel(cwd, g); err != nil || strings.HasPrefix(r, "-") {
						return nil, false, false, false
					}
				}
			}
			total += len(op.targets)
		}
		ops = append(ops, op)
		if total > maxRoutedPaths {
			return nil, false, false, false
		}
	}
	return ops, recursive, force, len(ops) > 0
}

// lexicalOperand checks a relative operand by its text alone: no `..` component, no
// build-output component (case-folded, as in PlaceTarget), not `.`. rel is its cleaned
// path.
func lexicalOperand(s string) (rel string, ok bool) {
	last := ""
	for _, c := range strings.Split(s, "/") {
		if c == ".." || regenerable[strings.ToLower(c)] {
			return "", false
		}
		if c != "" {
			last = c
		}
	}
	if last == "" || last == "." {
		return "", false
	}
	return path.Clean(s), true
}

// ErrTargetMissing is what PlaceTarget wraps when the operand names nothing that exists
// (or its parent directory does not): `rm -f` skips such an operand.
var ErrTargetMissing = errors.New("no such file or directory")

// placeTarget is PlaceTarget for the hook, which only needs to know whether the target
// is one `rm` itself would delete inside the root.
func placeTarget(p, realRoot string) (rel string, isDir, ok bool) {
	rel, isDir, err := PlaceTarget(p, realRoot)
	return rel, isDir, err == nil
}

// PlaceTarget locates one absolute operand the way the shell would: `..` and links
// resolved on the real tree. rel is the target's path under realRoot, the real root.
// The error is nil for an existing thing `rm` itself would delete inside the root, and
// otherwise says why not, in words an agent can read: outside the root, the root, `.` or
// `..`, a regenerable output, a link that leaves the root, a trailing slash on a
// non-directory, or (ErrTargetMissing) nothing there. `trash` (ADR-0062 §2) and the
// hook (ADR-0061 §2) share these rules.
func PlaceTarget(p, realRoot string) (rel string, isDir bool, err error) {
	slash := strings.HasSuffix(p, "/")
	p = strings.TrimRight(p, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", false, errors.New("is not an absolute path")
	}
	parent, name := p[:i], p[i+1:]
	if parent == "" {
		parent = "/"
	}
	if name == "" || name == "." || name == ".." {
		return "", false, errors.New("is the project root, `.` or `..`, which is never deleted")
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, ErrTargetMissing
		}
		return "", false, fmt.Errorf("cannot be resolved: %w", err)
	}
	full := filepath.Join(realParent, name)
	rel, ok := under(realRoot, full)
	if !ok {
		if full == realRoot {
			return "", false, errors.New("is the project root, which is never deleted")
		}
		return "", false, errors.New("is outside the project root")
	}
	fi, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, ErrTargetMissing
		}
		return "", false, fmt.Errorf("cannot be read: %w", err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		if slash {
			return "", false, errors.New("is a link written with a trailing slash, which names its target")
		}
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", false, errors.New("is a link whose target cannot be resolved")
		}
		if _, in := under(realRoot, target); !in {
			return "", false, errors.New("is a link that leaves the project root")
		}
	case fi.IsDir():
		isDir = true
	case slash:
		return "", false, errors.New("has a trailing slash but is not a directory")
	}
	for _, c := range strings.Split(rel, "/") {
		if regenerable[strings.ToLower(c)] {
			return "", false, fmt.Errorf("is or lies under %s, a build output that droidyard does not trash", c)
		}
	}
	return rel, isDir, nil
}

// under returns p relative to root, slash-separated, when p lies strictly inside it.
func under(root, p string) (string, bool) {
	r, err := filepath.Rel(root, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// wordRune is one character of a shell word; quoted means quoted or escaped, so it is
// never a glob character.
type wordRune struct {
	r      rune
	quoted bool
}

type word []wordRune

func (w word) String() string {
	var b strings.Builder
	for _, c := range w {
		b.WriteRune(c.r)
	}
	return b.String()
}

func (w word) quoted() bool {
	for _, c := range w {
		if c.quoted {
			return true
		}
	}
	return false
}

// splitWords splits a command into words as `sh` would, for single and double quotes
// and backslashes. It refuses (ok false) whatever it does not model: an operator, a
// redirection, a substitution, an expansion, a comment, a glob bracket, a control
// character, an unterminated quote.
func splitWords(s string) (words []word, ok bool) {
	var cur word
	started := false
	flush := func() {
		if started {
			words = append(words, cur)
		}
		cur, started = nil, false
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			return nil, false
		}
		i += n
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return nil, false
		}
		switch r {
		case ' ', '\t':
			flush()
		case ';', '&', '|', '<', '>', '(', ')', '`', '$', '{', '}', '[':
			return nil, false
		case '~', '#':
			if !started {
				return nil, false
			}
			cur = append(cur, wordRune{r: r})
		case '\'':
			end := strings.IndexByte(s[i:], '\'')
			if end < 0 {
				return nil, false
			}
			for _, q := range s[i : i+end] {
				if (q < 0x20 && q != '\t') || q == 0x7f {
					return nil, false
				}
				cur = append(cur, wordRune{q, true})
			}
			i += end + 1
			started = true
		case '"':
			started = true
			closed := false
			for i < len(s) {
				q, n := utf8.DecodeRuneInString(s[i:])
				if q == utf8.RuneError && n <= 1 {
					return nil, false
				}
				i += n
				if (q < 0x20 && q != '\t') || q == 0x7f {
					return nil, false
				}
				if q == '"' {
					closed = true
					break
				}
				if q == '$' || q == '`' {
					return nil, false
				}
				if q == '\\' && i < len(s) {
					nq, nn := utf8.DecodeRuneInString(s[i:])
					if nq == '$' || nq == '`' || nq == '"' || nq == '\\' {
						q = nq
						i += nn
					}
				}
				cur = append(cur, wordRune{q, true})
			}
			if !closed {
				return nil, false
			}
		case '\\':
			if i >= len(s) {
				return nil, false
			}
			q, n := utf8.DecodeRuneInString(s[i:])
			i += n
			if (q < 0x20 && q != '\t') || q == 0x7f {
				return nil, false
			}
			cur = append(cur, wordRune{q, true})
			started = true
		default:
			cur = append(cur, wordRune{r: r})
			started = true
		}
	}
	flush()
	return words, true
}

// expandWord turns an operand into absolute, uncleaned paths: relative ones are joined
// to cwd (which must be absolute, else ok is false), `*` and `?` expand as `sh` does.
// A word with a glob that matches nothing is not expanded by sh and is a pass here.
func expandWord(w word, cwd string) (out []string, ok bool) {
	var comps []word
	start := 0
	for i := range w {
		if w[i].r == '/' {
			comps = append(comps, w[start:i])
			start = i + 1
		}
	}
	comps = append(comps, w[start:])
	abs := len(w) > 0 && w[0].r == '/'
	base := "/"
	if !abs {
		if !filepath.IsAbs(cwd) {
			return nil, false
		}
		base = strings.TrimRight(cwd, "/")
		if base == "" {
			base = "/"
		}
	}
	trailing := len(comps) > 1 && len(comps[len(comps)-1]) == 0
	var parts []word
	for _, c := range comps {
		if len(c) > 0 {
			parts = append(parts, c)
		}
	}
	cands := []string{base}
	globbed := false
	for pi, c := range parts {
		last := pi == len(parts)-1
		if !hasGlob(c) {
			for i := range cands {
				cands[i] = joinPath(cands[i], c.String())
			}
			continue
		}
		globbed = true
		var next []string
		for _, d := range cands {
			entries, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !matchGlob(c, e.Name()) {
					continue
				}
				p := joinPath(d, e.Name())
				if !last {
					if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
						continue
					}
				}
				next = append(next, p)
				if len(next) > maxRoutedPaths {
					return nil, false
				}
			}
		}
		cands = next
	}
	if len(cands) == 0 {
		return nil, false
	}
	sort.Strings(cands)
	for _, p := range cands {
		if globbed {
			if _, err := os.Lstat(p); err != nil {
				return nil, false
			}
		}
		if trailing {
			p += "/"
		}
		out = append(out, p)
	}
	return out, true
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

func hasGlob(c word) bool {
	for _, r := range c {
		if !r.quoted && (r.r == '*' || r.r == '?') {
			return true
		}
	}
	return false
}

// matchGlob matches one path component as `sh` does: `*` any run, `?` one character,
// and a name starting with a dot only by a pattern that starts with a dot.
func matchGlob(pat word, name string) bool {
	if strings.HasPrefix(name, ".") && (len(pat) == 0 || pat[0].r != '.') {
		return false
	}
	return globAt(pat, []rune(name))
}

func globAt(pat word, name []rune) bool {
	for len(pat) > 0 {
		p := pat[0]
		switch {
		case !p.quoted && p.r == '*':
			for len(pat) > 0 && !pat[0].quoted && pat[0].r == '*' {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if globAt(pat, name[i:]) {
					return true
				}
			}
			return false
		case !p.quoted && p.r == '?':
			if len(name) == 0 {
				return false
			}
		default:
			if len(name) == 0 || name[0] != p.r {
				return false
			}
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// PassOutput is what the client's hook prints to let the command through untouched:
// nothing for Claude Code (exit 0), `{}` for Cursor [obs, m2j step 6]. The exit status
// is 0 either way; the hook never exits 2, which Claude Code reads as a deny.
func PassOutput(c Client) []byte {
	if c == Cursor {
		return []byte("{}\n")
	}
	return nil
}

// DenyOutput is the client's deny with reason as the text the model reads: Claude
// Code's `hookSpecificOutput.permissionDecisionReason`, Cursor's `user_message` (m1o,
// re-checked in plan m2k step 1: that is the field that reaches the model) and, as
// ADR-0062 §6 has it, `agent_message` too.
func DenyOutput(c Client, reason string) []byte {
	var v any
	switch c {
	case Claude:
		v = map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}}
	case Cursor:
		v = map[string]any{"permission": "deny", "user_message": reason, "agent_message": reason}
	default:
		return nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

// Redirect names what a replacement command must reach (ADR-0062 §4).
type Redirect struct {
	// Binary is the absolute path the hook itself was started as.
	Binary string
	// Root is the project root, the --root of the command.
	Root string
	// StateDir is the state root, written as --state-dir when the hook's own came from
	// DROIDYARD_STATE_DIR; "" otherwise.
	StateDir string
}

// Message is the deny reason for a routed `rm`: the one command, to run in the shell
// the `rm` was typed in, that moves the operands to droidyard's trash. ok is false when
// any part holds a single quote or a control character, which this quoting does not
// carry; the command then passes.
func (r Redirect) Message(rt Route) (msg string, ok bool) {
	if !filepath.IsAbs(r.Binary) || !filepath.IsAbs(r.Root) || len(rt.operands) == 0 {
		return "", false
	}
	if !plainText(r.Binary) || !plainText(r.Root) || !plainText(r.StateDir) {
		return "", false
	}
	parts := []string{shQuote(r.Binary), "trash"}
	if r.StateDir != "" {
		parts = append(parts, "--state-dir", shQuote(r.StateDir))
	}
	parts = append(parts, "--root", shQuote(r.Root))
	if rt.Recursive {
		parts = append(parts, "-r")
	}
	if rt.Force {
		parts = append(parts, "-f")
	}
	if rt.endOptions {
		parts = append(parts, "--")
	}
	for _, w := range rt.operands {
		q, ok := w.shell()
		if !ok {
			return "", false
		}
		parts = append(parts, q)
	}
	return "droidyard keeps deletes in this project restorable: run this instead, in the same directory: " + strings.Join(parts, " "), true
}

// plainText is whether s has no single quote and no control character.
func plainText(s string) bool {
	for _, c := range s {
		if c == '\'' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// shell renders the word for a POSIX shell so that it expands as it was typed
// (ADR-0062 §4): a `*` or `?` that was unquoted stays bare, so the shell globs it
// again; every other character is single-quoted. ok is false for a single quote or a
// control character.
func (w word) shell() (string, bool) {
	var b strings.Builder
	open := false
	for _, c := range w {
		if c.r == '\'' || c.r < 0x20 || c.r == 0x7f {
			return "", false
		}
		if !c.quoted && (c.r == '*' || c.r == '?') {
			if open {
				b.WriteByte('\'')
				open = false
			}
			b.WriteRune(c.r)
			continue
		}
		if !open {
			b.WriteByte('\'')
			open = true
		}
		b.WriteRune(c.r)
	}
	if open {
		b.WriteByte('\'')
	}
	return b.String(), true
}

// shQuote quotes a string with no single quote or control character for a POSIX shell,
// leaving a plain path as it is.
func shQuote(s string) string {
	plain := s != ""
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_@%+=:,./-", c)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + s + "'"
}

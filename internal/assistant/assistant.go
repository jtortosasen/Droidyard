// Package assistant writes droidyard's integration with the coding assistants into a
// user's project (ADR-0037, ADR-0038): the droidyard-android skill, and per client the
// MCP server entry and an allowlist of the read-only tools. It writes no AGENTS.md or
// CLAUDE.md. `droidyard init` is its only caller.
//
// It edits files the user owns, so it follows one rule throughout: add what is absent,
// keep what is there, and change nothing it cannot put back. JSON is merged by key, an
// existing entry under droidyard's key is kept and reported, never replaced, and a file
// that does not parse is refused and left as it is. --uninstall removes only what is
// equal, after parsing, to what the same run would write now. Every write goes through
// a temporary file and a rename, and no path may leave the project root. The skill's
// files are init's own and refreshed when they differ: a user's edits there are
// replaced (reported "updated"), while --uninstall keeps a differing one.
//
// The package knows the clients' file formats and nothing about capabilities: the tool
// names arrive in Options, already chosen by the caller from the registry.
package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// Client is one assistant init knows how to configure.
type Client string

const (
	Cursor   Client = "cursor"
	Claude   Client = "claude"
	OpenCode Client = "opencode"
)

// Clients lists every client, in the order init handles them.
func Clients() []Client { return []Client{Cursor, Claude, OpenCode} }

// ParseClients reads a --client value: client names separated by commas, or "all".
func ParseClients(v string) ([]Client, error) {
	var out []Client
	seen := map[Client]bool{}
	for _, name := range strings.Split(v, ",") {
		name = strings.TrimSpace(name)
		var add []Client
		switch Client(name) {
		case "all":
			add = Clients()
		case Cursor, Claude, OpenCode:
			add = []Client{Client(name)}
		default:
			return nil, fmt.Errorf("unknown client %q: use cursor, claude, opencode or all", name)
		}
		for _, c := range add {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// ServerName is the key the MCP server is registered under in every client. It is also
// the prefix of the tool names the allowlists carry.
const ServerName = "droidyard"

// SkillName is the skill's directory name (ADR-0011).
const SkillName = "droidyard-android"

// Options is one run of init.
type Options struct {
	// Root is the project root, an absolute path. The MCP entries name it with --root,
	// since Cursor's spawn directory is not known (plan m1l C4).
	Root string
	// Command is the absolute path of the droidyard binary every entry runs: a client
	// launched from a GUI has no shell PATH (ADR-0019 §Processes 8).
	Command string
	Clients []Client
	// Tools are the MCP names of the read-only tools to allowlist, chosen by the caller
	// from the registry's readOnlyHint.
	Tools []string
	// Skill is the skill's tree, SKILL.md at its root.
	Skill fs.FS
	// DryRun computes every change and writes none.
	DryRun bool
	// Uninstall removes what a run with the same options would write.
	Uninstall bool
}

// Change is what a run did, or in a dry run would do, to one file.
type Change struct {
	Path   string // slash-separated, relative to the root
	Status Status
	Notes  []string
	// Entry is, for a refused file, what the user adds by hand.
	Entry string
}

// Result is a whole run.
type Result struct {
	Changes []Change
	// Allowed are the permission strings written for each client that has an allowlist.
	Allowed map[Client][]string
}

// Refused reports whether any file was refused: the run did everything else it could.
func (r Result) Refused() bool {
	for _, c := range r.Changes {
		if c.Status == Refused {
			return true
		}
	}
	return false
}

// Note is the one line init prints after a successful run for a client whose own
// settings need a step init cannot take for the user; empty for a client with none.
func Note(c Client) string {
	switch c {
	case Cursor:
		return "cursor: the project's server may need enabling once: run `cursor-agent mcp enable droidyard`, or switch it on in the IDE's MCP settings."
	case Claude:
		return "claude: Claude Code asks you to approve the project's .mcp.json servers the first time it uses them."
	}
	return ""
}

// AllowRule is the permission string that allows one tool for a client: Cursor CLI's
// Mcp(server:tool) and Claude Code's mcp__server__tool. OpenCode has none.
func AllowRule(c Client, tool string) string {
	switch c {
	case Cursor:
		return "Mcp(" + ServerName + ":" + tool + ")"
	case Claude:
		return "mcp__" + ServerName + "__" + tool
	}
	return ""
}

// job is one file init owns part of.
type job struct {
	rel       string
	install   func(old []byte, exists bool) edit
	uninstall func(old []byte, exists bool) edit
	// fixed, when set, is the outcome without reading the file.
	fixed *edit
}

// Run installs, or with Options.Uninstall removes, the integration. An error means the
// options were unusable; a file init will not touch is a Refused change instead.
func Run(o Options) (Result, error) {
	root, err := newFSRoot(o.Root)
	if err != nil {
		return Result{}, err
	}
	if !strings.HasPrefix(o.Command, "/") {
		return Result{}, fmt.Errorf("command %q is not an absolute path", o.Command)
	}
	if len(o.Clients) == 0 {
		return Result{}, fmt.Errorf("no client chosen")
	}
	if o.Skill == nil {
		return Result{}, fmt.Errorf("no skill to write")
	}
	jobs, allowed, err := plan(o, root)
	if err != nil {
		return Result{}, err
	}
	res := Result{Allowed: allowed}
	for _, j := range jobs {
		res.Changes = append(res.Changes, run(o, root, j))
	}
	return res, nil
}

func has(cs []Client, c Client) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

func plan(o Options, root *fsRoot) ([]job, map[Client][]string, error) {
	var jobs []job
	allowed := map[Client][]string{}
	tools := append([]string(nil), o.Tools...)
	sort.Strings(tools)

	if has(o.Clients, Cursor) || has(o.Clients, OpenCode) {
		sj, err := skillJobs(o.Skill, ".agents/skills/"+SkillName)
		if err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, keepIfShared(sj, root, o)...)
	}
	if has(o.Clients, Claude) {
		sj, err := skillJobs(o.Skill, ".claude/skills/"+SkillName)
		if err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, sj...)
	}

	server := rawJSON(stdioServer{Command: o.Command, Args: []string{"mcp", "--root", o.Root}})
	serverSpec := jsonSpec{keys: []keyEntry{{path: cursorServers, key: ServerName, value: server}}}
	allowSpec := func(c Client) (jsonSpec, error) {
		if len(tools) == 0 {
			return jsonSpec{}, fmt.Errorf("no read-only tool to allowlist for %s", c)
		}
		var elems []json.RawMessage
		for _, t := range tools {
			rule := AllowRule(c, t)
			allowed[c] = append(allowed[c], rule)
			elems = append(elems, rawJSON(rule))
		}
		return jsonSpec{arrays: []arrayEntry{{path: []string{"permissions"}, key: "allow", elems: elems}}}, nil
	}

	if has(o.Clients, Cursor) {
		jobs = append(jobs, jsonJob(".cursor/mcp.json", serverSpec))
		spec, err := allowSpec(Cursor)
		if err != nil {
			return nil, nil, err
		}
		// cursor-agent 2026.10.01 refuses a project cli.json whose permissions lack a deny
		// array ("permissions.deny: Required"), which would switch the whole file off; an
		// existing deny list is the user's and is kept, and uninstall removes the empty one only
		// when it is all that is left of permissions (plan m1o step 9, close review B1).
		spec.keys = append(spec.keys, keyEntry{path: []string{"permissions"}, key: "deny", value: rawJSON([]string{}), sole: true})
		jobs = append(jobs, jsonJob(".cursor/cli.json", spec))
	}
	if has(o.Clients, Claude) {
		jobs = append(jobs, jsonJob(".mcp.json", serverSpec))
		spec, err := allowSpec(Claude)
		if err != nil {
			return nil, nil, err
		}
		jobs = append(jobs, jsonJob(".claude/settings.json", spec))
	}
	if has(o.Clients, OpenCode) {
		entry := rawJSON(openCodeServer{
			Type: "local", Command: []string{o.Command, "mcp", "--root", o.Root},
			// The measured configuration: with Code Mode on, OpenCode reaches MCP tools
			// through an execute/search wrapper (daily-use-harness-2026-10.md §2).
			Codemode: false,
		})
		spec := jsonSpec{keys: []keyEntry{{path: openCodeServers, key: ServerName, value: entry}}}
		if !o.Uninstall {
			// Comments would be lost by a rewrite, and OpenCode reads both files: if
			// opencode.jsonc is there (or cannot be looked at), refuse the entry and
			// print it, and leave opencode.json alone (ADR-0037 Decision 2).
			real, err := root.resolve("opencode.jsonc")
			if err != nil {
				jobs = append(jobs, job{rel: "opencode.jsonc", fixed: &edit{status: Refused,
					notes: []string{err.Error() + "; opencode.json is left alone"}, entry: spec.handEntry()}})
				return jobs, allowed, nil
			}
			if _, err := os.Stat(real); err == nil {
				jobs = append(jobs, job{rel: "opencode.jsonc", fixed: &edit{
					status: Refused,
					notes:  []string{"opencode.jsonc may hold comments that a rewrite would lose, and OpenCode reads it too: add this to it yourself (opencode.json is left alone)"},
					entry:  spec.handEntry(),
				}})
				return jobs, allowed, nil
			}
		}
		jobs = append(jobs, jsonJob("opencode.json", spec))
	}
	return jobs, allowed, nil
}

// stdioServer is Cursor's and Claude Code's server entry; the fields are in the order
// the files are written in.
type stdioServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// openCodeServer is OpenCode's local server (Mcp.LocalConfig in 2.0.18, whose schema has
// no `enabled`): started by a command line. Servers sit under mcp.servers.
type openCodeServer struct {
	Type     string   `json:"type"`
	Command  []string `json:"command"`
	Codemode bool     `json:"codemode"`
}

// openCodeServers is where opencode.json lists MCP servers.
var openCodeServers = []string{"mcp", "servers"}

// cursorServers is where .cursor/mcp.json lists them.
var cursorServers = []string{"mcpServers"}

// keepIfShared turns the jobs Cursor and OpenCode share (the .agents skill) into "kept"
// on --uninstall while a client not in this run still has its droidyard entry, so
// uninstalling one client does not take what the other reads. An entry file that cannot
// be read counts as an entry: init will not guess.
func keepIfShared(jobs []job, root *fsRoot, o Options) []job {
	if !o.Uninstall {
		return jobs
	}
	var users []string
	for _, c := range []Client{Cursor, OpenCode} {
		if !has(o.Clients, c) && root.hasEntry(c) {
			users = append(users, string(c))
		}
	}
	if len(users) == 0 {
		return jobs
	}
	kept := make([]job, len(jobs))
	for i, j := range jobs {
		kept[i] = job{rel: j.rel, fixed: &edit{status: Unchanged, notes: []string{"kept: still used by " + strings.Join(users, ", ")}}}
	}
	return kept
}

// hasEntry reports whether the client's file holds a droidyard server entry, or cannot
// be told (unreadable, not an object).
func (r *fsRoot) hasEntry(c Client) bool {
	rel, path := ".cursor/mcp.json", cursorServers
	if c == OpenCode {
		// An opencode.jsonc is refused by init and edited by hand, so its presence (or a
		// path that cannot be resolved) means OpenCode may be configured: init does not guess.
		jsonc, err := r.resolve("opencode.jsonc")
		if err != nil {
			return true
		}
		if _, err := os.Stat(jsonc); err == nil {
			return true
		}
		rel, path = "opencode.json", openCodeServers
	}
	real, err := r.resolve(rel)
	if err != nil {
		return true
	}
	data, exists, err := readFile(real)
	if err != nil {
		return true
	}
	if !exists {
		return false
	}
	root, err := parseObject(data)
	if err != nil {
		return true
	}
	present := false
	if _, err := root.editPath(path, false, false, func(leaf *object) (bool, error) {
		_, present = leaf.vals[ServerName]
		return false, nil
	}); err != nil {
		return true
	}
	return present
}

func rawJSON(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

func jsonJob(rel string, s jsonSpec) job {
	return job{
		rel:       rel,
		install:   func(old []byte, exists bool) edit { return installJSON(old, exists, s) },
		uninstall: func(old []byte, exists bool) edit { return uninstallJSON(old, exists, s) },
	}
}

// skillJobs is one job per file of the skill tree, under dir.
func skillJobs(skill fs.FS, dir string) ([]job, error) {
	var jobs []job
	err := fs.WalkDir(skill, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := fs.ReadFile(skill, p)
		if err != nil {
			return err
		}
		jobs = append(jobs, job{
			rel: path.Join(dir, p),
			install: func(old []byte, exists bool) edit {
				switch {
				case !exists:
					return edit{status: Created, content: want}
				case bytes.Equal(old, want):
					return edit{status: Unchanged, notes: []string{"already there"}}
				}
				return edit{status: Updated, content: want, notes: []string{"differed from this build's skill: refreshed"}}
			},
			uninstall: func(old []byte, exists bool) edit {
				switch {
				case !exists:
					return edit{status: Unchanged, notes: []string{"not there"}}
				case bytes.Equal(old, want):
					return edit{status: Deleted}
				}
				return edit{status: Unchanged, notes: []string{"differs from this build's skill: kept, not removed"}}
			},
		})
		return nil
	})
	return jobs, err
}

// run computes one job's outcome and, unless it is a dry run, applies it.
func run(o Options, root *fsRoot, j job) Change {
	c := Change{Path: j.rel}
	fail := func(format string, a ...any) Change {
		c.Status, c.Notes = Refused, []string{fmt.Sprintf(format, a...)}
		return c
	}
	if j.fixed != nil {
		c.Status, c.Notes, c.Entry = j.fixed.status, j.fixed.notes, j.fixed.entry
		return c
	}
	real, err := root.resolve(j.rel)
	if err != nil {
		return fail("%v", err)
	}
	old, exists, err := readFile(real)
	if err != nil {
		return fail("%v", err)
	}
	fn := j.install
	if o.Uninstall {
		fn = j.uninstall
	}
	e := fn(old, exists)
	c.Status, c.Notes, c.Entry = e.status, e.notes, e.entry
	if o.DryRun {
		return c
	}
	var werr error
	switch e.status {
	case Created, Updated, Removed:
		werr = writeFile(real, e.content)
	case Deleted:
		werr = root.removeFile(real)
	}
	if werr != nil {
		return fail("cannot write: %v", werr)
	}
	return c
}

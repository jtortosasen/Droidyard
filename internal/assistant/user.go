package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The run at user level (ADR-0124): the same integration, written once into the user's
// own configuration instead of into a project, so every project and every worktree has it
// and no entry names a root. The rules are ADR-0037's and ADR-0090's (add what is absent,
// keep what is there, remove only what init would write now) with these differences:
//
//   - Each directory the run writes in is its own fsRoot. A directory that is itself a link
//     (a ~/.claude kept in a dotfiles repository) is followed; a file in it that links out of
//     it is refused, as in a project. A directory that does not exist yet is created by the
//     first write.
//   - Claude's MCP entry lives in .claude.json, which Claude rewrites while it runs and
//     which is often over 1 MiB, so it is never written here: it is read, and Claude's own
//     CLI changes it through Options.ClaudeMCP.
//   - A hook entry of the rootless shape is init's, replaced when stale and removed on
//     uninstall. One that names a root (`hook --client claude --root /p`) is the user's.
//   - Change.Path is the absolute path, slash-separated.

// UserDirs are the directories and the file a user-level run reads and writes. The caller
// resolves them from the environment; each must be an absolute path.
type UserDirs struct {
	// Claude is Claude Code's configuration directory (CLAUDE_CONFIG_DIR, else ~/.claude):
	// settings.json and skills/.
	Claude string
	// ClaudeJSON is Claude Code's .claude.json, the file `claude mcp add-json --scope user`
	// writes. It is only read.
	ClaudeJSON string
	// Cursor is ~/.cursor: mcp.json, hooks.json and cli-config.json.
	Cursor string
	// OpenCode is OpenCode's configuration directory (<xdg>/opencode): opencode.json.
	OpenCode string
	// Agents is ~/.agents, where the skill Cursor and OpenCode share goes.
	Agents string
}

// ResolveUserDirs is UserDirs for a home directory and an environment: the places `init
// --user` writes in (ADR-0124) and the places the daemon reads the clients' chat stores
// from (ADR-0127 §2), so the two can never disagree. CLAUDE_CONFIG_DIR, when set, replaces
// ~/.claude, and Claude keeps .claude.json in it; XDG_CONFIG_HOME, when it is an absolute
// path, holds OpenCode's directory.
func ResolveUserDirs(home string, getenv func(string) string) UserDirs {
	dirs := UserDirs{
		Claude:     filepath.Join(home, ".claude"),
		ClaudeJSON: filepath.Join(home, ".claude.json"),
		Cursor:     filepath.Join(home, ".cursor"),
		OpenCode:   filepath.Join(home, ".config", "opencode"),
		Agents:     filepath.Join(home, ".agents"),
	}
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		// Claude keeps .claude.json in its configuration directory when one is named.
		dirs.Claude, dirs.ClaudeJSON = d, filepath.Join(d, ".claude.json")
	}
	if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		dirs.OpenCode = filepath.Join(x, "opencode")
	}
	return dirs
}

// ClaudeMCP changes Claude Code's user-scope MCP entry by running Claude's own CLI.
type ClaudeMCP interface {
	// Add runs `claude mcp add-json --scope user droidyard <entry>`.
	Add(entry json.RawMessage) error
	// Remove runs `claude mcp remove --scope user droidyard`.
	Remove() error
}

// maxClaudeStateSize bounds the read of .claude.json: Claude's live state, whose history
// makes more than maxFileSize ordinary, though not unbounded.
const maxClaudeStateSize = 64 << 20

// cursorConfigNote is said when the run writes Cursor's cli-config.json: cursor-agent
// keeps its own state there [inferred, plan m3a step 1: an unauthenticated run left the
// file alone, an authenticated one was not tried].
const cursorConfigNote = "close cursor-agent first: it may rewrite this file while it runs"

// userRoot is one directory a user-level run writes in.
type userRoot struct {
	fs  *fsRoot
	dir string // as given, for the paths reported
}

// path is the absolute, slash-separated path of rel below the directory.
func (u *userRoot) path(rel string) string {
	return filepath.ToSlash(filepath.Join(u.dir, filepath.FromSlash(rel)))
}

func openUserRoot(label, dir string) (*userRoot, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("the %s directory %q is not an absolute path", label, dir)
	}
	r, err := newUserFSRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("the %s directory %q: %w", label, dir, err)
	}
	return &userRoot{fs: r, dir: filepath.Clean(dir)}, nil
}

// newUserFSRoot is newFSRoot for a directory that may not exist yet: its nearest existing
// ancestor is resolved, and the missing names are put back below it.
func newUserFSRoot(dir string) (*fsRoot, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err == nil {
		fi, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, errors.New("not a directory")
		}
		return &fsRoot{real: real}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return nil, err
	}
	up, perr := newUserFSRoot(parent)
	if perr != nil {
		return nil, perr
	}
	return &fsRoot{real: filepath.Join(up.real, filepath.Base(dir))}, nil
}

func runUser(o Options) (Result, error) {
	if o.Root != "" {
		return Result{}, fmt.Errorf("a user-level run has no root (got %q)", o.Root)
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
	if o.StateDir != "" && !strings.HasPrefix(o.StateDir, "/") {
		return Result{}, fmt.Errorf("state dir %q is not an absolute path", o.StateDir)
	}
	tools := append([]string(nil), o.Tools...)
	sort.Strings(tools)

	var claudeRoot, cursorRoot, openCodeRoot, agentsRoot *userRoot
	open := func(dst **userRoot, label, dir string) error {
		r, err := openUserRoot(label, dir)
		if err != nil {
			return err
		}
		*dst = r
		return nil
	}
	if has(o.Clients, Claude) {
		if err := open(&claudeRoot, "Claude", o.Dirs.Claude); err != nil {
			return Result{}, err
		}
		if !filepath.IsAbs(o.Dirs.ClaudeJSON) {
			return Result{}, fmt.Errorf("the .claude.json path %q is not an absolute path", o.Dirs.ClaudeJSON)
		}
	}
	if has(o.Clients, Cursor) {
		if err := open(&cursorRoot, "Cursor", o.Dirs.Cursor); err != nil {
			return Result{}, err
		}
	}
	if has(o.Clients, OpenCode) {
		if err := open(&openCodeRoot, "OpenCode", o.Dirs.OpenCode); err != nil {
			return Result{}, err
		}
	}
	if has(o.Clients, Cursor) || has(o.Clients, OpenCode) {
		if err := open(&agentsRoot, "agents", o.Dirs.Agents); err != nil {
			return Result{}, err
		}
	}

	var steps []func() Change
	file := func(u *userRoot, j job, onChange string) {
		steps = append(steps, func() Change {
			c := run(o, u.fs, j)
			c.Path = u.path(j.rel)
			if onChange != "" && (c.Status == Created || c.Status == Updated) {
				c.Notes = append(c.Notes, onChange)
			}
			return c
		})
	}
	allowed := map[Client][]string{}

	skillDir := "skills/" + SkillName
	if agentsRoot != nil {
		sj, err := skillJobs(o.Skill, skillDir)
		if err != nil {
			return Result{}, err
		}
		for _, j := range userKeepIfShared(sj, o) {
			file(agentsRoot, j, "")
		}
	}
	if claudeRoot != nil {
		sj, err := skillJobs(o.Skill, skillDir)
		if err != nil {
			return Result{}, err
		}
		for _, j := range sj {
			file(claudeRoot, j, "")
		}
	}

	if cursorRoot != nil {
		server := stdioServer{Command: o.Command, Args: []string{"mcp", "--from", "${workspaceFolder}"}}
		if o.StateDir != "" {
			// Cursor rebuilds the server's environment (row 199), as in a project.
			server.Env = map[string]string{"DROIDYARD_STATE_DIR": o.StateDir}
		}
		file(cursorRoot, jsonJob("mcp.json", jsonSpec{keys: []keyEntry{{path: cursorServers, key: ServerName, value: rawJSON(server)}}}), "")
		spec, err := allowlistSpec(Cursor, tools, allowed)
		if err != nil {
			return Result{}, err
		}
		// No deny array, unlike a project's cli.json: step 1 saw no evidence that
		// cursor-agent needs one in cli-config.json (plan m3a P3).
		file(cursorRoot, jsonJob("cli-config.json", spec), cursorConfigNote)
		file(cursorRoot, jsonJob("hooks.json", cursorHookSpec(o)), "")
	}

	if claudeRoot != nil {
		spec, err := allowlistSpec(Claude, tools, allowed)
		if err != nil {
			return Result{}, err
		}
		spec.arrays = append(spec.arrays, claudeHookArrays(o)...)
		file(claudeRoot, jsonJob("settings.json", spec), "")
		steps = append(steps, func() Change { return claudeMCPChange(o) })
	}

	if openCodeRoot != nil {
		entry := rawJSON(openCodeServer{Type: "local", Command: []string{o.Command, "mcp"}, Codemode: false})
		spec := jsonSpec{keys: []keyEntry{{path: openCodeServers, key: ServerName, value: entry}}}
		for _, j := range openCodeJobs(openCodeRoot.fs, spec, o.Uninstall) {
			file(openCodeRoot, j, "")
		}
	}

	res := Result{Allowed: allowed, Hooks: map[Client]string{}}
	for _, c := range o.Clients {
		if hasHook(c) {
			res.Hooks[c] = HookCommand(o, c)
		}
	}
	for _, step := range steps {
		res.Changes = append(res.Changes, step())
	}
	return res, nil
}

// userKeepIfShared is keepIfShared for the .agents skill at user level: it is kept on
// --uninstall while the client not in this run still has its droidyard entry in its own
// directory. A client whose directory is not given, or whose entry file cannot be read,
// counts as having one: init will not guess.
func userKeepIfShared(jobs []job, o Options) []job {
	if !o.Uninstall {
		return jobs
	}
	var users []string
	for _, c := range []Client{Cursor, OpenCode} {
		if !has(o.Clients, c) && userHasEntry(c, o.Dirs) {
			users = append(users, string(c))
		}
	}
	return keepFor(jobs, users)
}

func userHasEntry(c Client, d UserDirs) bool {
	dir := d.Cursor
	if c == OpenCode {
		dir = d.OpenCode
	}
	if !filepath.IsAbs(dir) {
		return true
	}
	r, err := newUserFSRoot(dir)
	if err != nil {
		return true
	}
	if c == OpenCode {
		return r.hasServerEntry("opencode.json", "opencode.jsonc", openCodeServers)
	}
	return r.hasServerEntry("mcp.json", "", cursorServers)
}

// claudeUserServer is the entry `claude mcp add-json --scope user` is given and stores
// as given (plan m3a P1): no root, since the shim finds the project from the folder the
// chat starts in.
type claudeUserServer struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// claudeEntry is what .claude.json holds under mcpServers.droidyard.
type claudeEntry struct {
	fileExists bool
	present    bool
	raw        json.RawMessage
}

// readClaudeEntry reads .claude.json, never writes it. A missing file holds no entry; a
// file that is not a regular file, is over maxClaudeStateSize, or does not parse as an
// object with an object under mcpServers cannot be told, and is an error.
func readClaudeEntry(file string) (claudeEntry, error) {
	real, err := filepath.EvalSymlinks(file)
	if errors.Is(err, fs.ErrNotExist) {
		return claudeEntry{}, nil
	}
	if err != nil {
		return claudeEntry{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return claudeEntry{}, err
	}
	if !fi.Mode().IsRegular() {
		return claudeEntry{}, errors.New("not a regular file")
	}
	if fi.Size() > maxClaudeStateSize {
		return claudeEntry{}, fmt.Errorf("larger than %d MiB", maxClaudeStateSize>>20)
	}
	f, err := os.Open(real)
	if err != nil {
		return claudeEntry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxClaudeStateSize+1))
	if err != nil {
		return claudeEntry{}, err
	}
	if len(data) > maxClaudeStateSize {
		return claudeEntry{}, fmt.Errorf("larger than %d MiB", maxClaudeStateSize>>20)
	}
	var state struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return claudeEntry{}, fmt.Errorf("does not parse (%v)", err)
	}
	e := claudeEntry{fileExists: true}
	if t := bytes.TrimSpace(state.MCPServers); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return e, nil
	}
	servers, err := parseObject(state.MCPServers)
	if err != nil {
		return claudeEntry{}, fmt.Errorf("mcpServers does not parse as an object (%v)", err)
	}
	e.raw, e.present = servers.vals[ServerName]
	return e, nil
}

// claudeMCPChange is Claude's MCP entry at user level. Absent, it is added; equal, after
// parsing, to the entry this run would add, it is unchanged on install and removed on
// --uninstall; different, it is the user's, kept and reported. Claude's CLI does the
// change, and with none the change is refused, with the command to run as its entry.
func claudeMCPChange(o Options) Change {
	want := rawJSON(claudeUserServer{Type: "stdio", Command: o.Command, Args: []string{"mcp"}})
	const key = "mcpServers." + ServerName
	addCmd := "claude mcp add-json --scope user " + ServerName + " " + shellArg(string(want))
	rmCmd := "claude mcp remove --scope user " + ServerName
	c := Change{Path: filepath.ToSlash(o.Dirs.ClaudeJSON)}
	hand := addCmd
	if o.Uninstall {
		hand = rmCmd
	}
	refuse := func(reason string) Change {
		c.Status, c.Notes, c.Entry = Refused, []string{reason}, hand
		return c
	}

	have, err := readClaudeEntry(o.Dirs.ClaudeJSON)
	if err != nil {
		return refuse(fmt.Sprintf("cannot read it (%v); init does not edit Claude's state, run this yourself", err))
	}
	switch {
	case !have.present && o.Uninstall:
		c.Status, c.Notes = Unchanged, []string{key + ": not there"}
		return c
	case have.present && !equalJSON(have.raw, want):
		verb := "kept, yours differs from what init would write (not replaced)"
		if o.Uninstall {
			verb = "kept, differs from what init writes now"
		}
		c.Status, c.Notes = Unchanged, []string{key + ": " + verb}
		return c
	case have.present && !o.Uninstall:
		c.Status, c.Notes = Unchanged, []string{key + ": already there"}
		return c
	}

	// Add (absent, install) or Remove (equal, uninstall): Claude's CLI does it.
	status, note := Updated, key+": added by `claude mcp add-json --scope user`"
	if !have.fileExists {
		status = Created
	}
	if o.Uninstall {
		status, note = Removed, key+": removed by `claude mcp remove --scope user`"
	}
	if o.ClaudeMCP == nil {
		return refuse("no `claude` on PATH to run it: run this yourself")
	}
	if !o.DryRun {
		var err error
		if o.Uninstall {
			err = o.ClaudeMCP.Remove()
		} else {
			err = o.ClaudeMCP.Add(want)
		}
		if err != nil {
			return refuse(fmt.Sprintf("claude failed (%v); run this yourself", err))
		}
	}
	c.Status, c.Notes = status, []string{note}
	return c
}

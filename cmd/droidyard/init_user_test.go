package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The tests of `init --user`, `--exclude` and `--gitignore` (ADR-0124 §1, §4; plan m3a
// step 6). Every one runs droidyard with a HOME, CLAUDE_CONFIG_DIR and XDG_CONFIG_HOME of
// its own and, for Claude, a fake `claude` on PATH that appends its argv to a file: the
// real claude and the real configuration are never reached.

// userSetup is one isolated user: where its configuration lives and what the fake claude
// recorded.
type userSetup struct {
	home, claude, xdg, state string
	log                      string // the fake claude's argv, one argument per line, calls ended by "--"
	fakeBin                  string // a directory holding the fake claude
}

// fakeClaude is a `claude` that records its argv and does what the real one does to
// .claude.json for the two commands init runs: add-json stores the entry as given, remove
// deletes the file.
const fakeClaude = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done >> "$FAKE_CLAUDE_LOG"
printf -- '--\n' >> "$FAKE_CLAUDE_LOG"
file="${CLAUDE_CONFIG_DIR:-$HOME}/.claude.json"
case "$2" in
add-json) printf '{"mcpServers":{"droidyard":%s}}' "$6" > "$file" ;;
remove) rm -f "$file" ;;
esac
`

func newUserSetup(t *testing.T) userSetup {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u := userSetup{
		home: home, claude: filepath.Join(home, "claude-config"), xdg: filepath.Join(home, "xdg"),
		state: shortDir(t), log: filepath.Join(t.TempDir(), "claude.log"), fakeBin: t.TempDir(),
	}
	if err := os.WriteFile(filepath.Join(u.fakeBin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	return u
}

// env is the child's whole environment: the host's, minus what the user setup decides.
// path is the PATH the child gets.
func (u userSetup) env(path string) []string {
	own := map[string]bool{"HOME": true, "CLAUDE_CONFIG_DIR": true, "XDG_CONFIG_HOME": true, "PATH": true, "CLAUDECODE": true,
		"DROIDYARD_STATE_DIR": true, "FAKE_CLAUDE_LOG": true, reexec: true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !own[k] {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+u.home, "CLAUDE_CONFIG_DIR="+u.claude, "XDG_CONFIG_HOME="+u.xdg, "PATH="+path,
		"DROIDYARD_STATE_DIR="+u.state, "FAKE_CLAUDE_LOG="+u.log, reexec+"=1")
}

// init runs `droidyard init args` in dir with the fake claude first on PATH.
func (u userSetup) init(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return u.initWithPath(t, dir, u.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), args...)
}

func (u userSetup) initWithPath(t *testing.T, dir, path string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	p := droidyardProc(t, u.env(path), dir, append([]string{"init"}, args...)...)
	code = p.wait(t)
	return p.stdout.String(), p.stderr.String(), code
}

// calls are the fake claude's recorded calls, each as its arguments; nil when it never ran.
func (u userSetup) calls(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(u.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if l == "--" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, l)
	}
	return calls
}

// treeHash is a hash of every regular file below dir, by path and content. Empty
// directories are not part of it: an uninstall leaves the user's own directories in place.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", rel, len(data), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// files lists the regular files below dir, relative and slash-separated.
func filesBelow(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// gradleProject is a directory `projectOf` finds as a project.
func gradleProj(t *testing.T) string {
	t.Helper()
	dir := projectDir(t)
	if err := os.WriteFile(filepath.Join(dir, "settings.gradle.kts"), []byte("rootProject.name = \"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// Decision (ADR-0124 §1): `init --user` writes each client's files in the user's own
// directories, Claude's MCP entry through `claude mcp add-json --scope user` with no root,
// and nothing in the project it runs in; it starts no daemon.
func TestInitUserWritesTheUserFiles(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := gradleProj(t)
	before := treeHash(t, proj)

	out, errOut, code := u.init(t, proj, "--user", "--client", "all")
	if code != 0 {
		t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
	}

	// Claude's entry goes through Claude's CLI: exactly this argv, the entry naming no root.
	calls := u.calls(t)
	if len(calls) != 1 || len(calls[0]) != 6 {
		t.Fatalf("the fake claude was run as %q; want one `mcp add-json --scope user droidyard <entry>`", calls)
	}
	if got := strings.Join(calls[0][:5], " "); got != "mcp add-json --scope user droidyard" {
		t.Errorf("claude was run with %q", got)
	}
	var entry struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(calls[0][5]), &entry); err != nil {
		t.Fatalf("the entry given to claude, %q: %v", calls[0][5], err)
	}
	exe, _ := filepath.Abs(os.Args[0])
	if entry.Type != "stdio" || !filepath.IsAbs(entry.Command) || !sameFile(entry.Command, exe) || strings.Join(entry.Args, " ") != "mcp" {
		t.Errorf("claude's entry = %+v; want stdio, this binary by an absolute path, args [mcp] and no --root", entry)
	}

	// The files, where ADR-0124 §1's table puts them.
	want := map[string][]string{
		u.claude:                         {"settings.json", "skills/droidyard-android/SKILL.md"},
		filepath.Join(u.home, ".cursor"): {"mcp.json", "hooks.json", "cli-config.json"},
		filepath.Join(u.xdg, "opencode"): {"opencode.json"},
		filepath.Join(u.home, ".agents"): {"skills/droidyard-android/SKILL.md"},
	}
	for dir, rels := range want {
		have := filesBelow(t, dir)
		for _, rel := range rels {
			if !contains(have, rel) {
				t.Errorf("%s was not written (it holds %v)", filepath.Join(dir, rel), have)
			}
		}
	}
	var cursor struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	readJSON(t, filepath.Join(u.home, ".cursor", "mcp.json"), &cursor)
	if s := cursor.Servers["droidyard"]; strings.Join(s.Args, " ") != "mcp --from ${workspaceFolder}" {
		t.Errorf("Cursor's user entry runs %q; want `mcp --from ${workspaceFolder}`", s.Args)
	}
	if allow := allowOf(t, filepath.Join(u.claude, "settings.json")); len(allow) < 10 {
		t.Errorf("Claude's user settings.json allows %v", allow)
	}
	// No file of the configuration names a root, or the project.
	for _, f := range []string{
		filepath.Join(u.claude, "settings.json"), filepath.Join(u.home, ".cursor", "mcp.json"),
		filepath.Join(u.home, ".cursor", "hooks.json"), filepath.Join(u.xdg, "opencode", "opencode.json"),
	} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "--root") || strings.Contains(string(raw), proj) {
			t.Errorf("%s names a root:\n%s", f, raw)
		}
	}
	if !strings.Contains(out, "close cursor-agent first") {
		t.Errorf("the output does not say to close cursor-agent first:\n%s", out)
	}

	// Nothing in the project, no daemon.
	if after := treeHash(t, proj); after != before {
		t.Errorf("init --user wrote in the project it ran in: %v", filesBelow(t, proj))
	}
	if left, _ := os.ReadDir(u.state); len(left) != 0 {
		t.Errorf("init --user left %v under the state root; want no daemon", left)
	}
}

// Decision (ADR-0124 §1): the uninstall takes out what the install wrote, so the user's
// configuration is as it was, Claude's entry through `claude mcp remove --scope user`; a
// dry run writes nothing and runs no claude.
func TestInitUserUninstallLeavesHomeAsItWas(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := gradleProj(t)
	// The user's own configuration, which init merges into and must leave as it found it.
	seed := map[string]string{
		filepath.Join(u.claude, "settings.json"):                       "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(ls)\"\n    ]\n  }\n}\n",
		filepath.Join(u.home, ".cursor", "mcp.json"):                   "{\n  \"mcpServers\": {\n    \"other\": {\n      \"command\": \"x\"\n    }\n  }\n}\n",
		filepath.Join(u.home, ".agents", "skills", "mine", "SKILL.md"): "mine\n",
	}
	for p, c := range seed {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := treeHash(t, u.home)

	if _, errOut, code := u.init(t, proj, "--user", "--client", "all", "--dry-run"); code != 0 {
		t.Fatalf("dry run exited %d: %s", code, errOut)
	}
	if treeHash(t, u.home) != before || u.calls(t) != nil {
		t.Fatalf("a dry run changed the user's files (%v) or ran claude (%v)", filesBelow(t, u.home), u.calls(t))
	}

	if _, errOut, code := u.init(t, proj, "--user", "--client", "all"); code != 0 {
		t.Fatalf("install exited %d: %s", code, errOut)
	}
	if treeHash(t, u.home) == before {
		t.Fatal("the install changed nothing; this test would prove nothing")
	}
	out, errOut, code := u.init(t, proj, "--user", "--client", "all", "--uninstall")
	if code != 0 {
		t.Fatalf("uninstall exited %d:\n%s\n%s", code, out, errOut)
	}
	if after := treeHash(t, u.home); after != before {
		t.Errorf("the user's files differ after install and uninstall: now %v", filesBelow(t, u.home))
		for p := range seed {
			raw, _ := os.ReadFile(p)
			t.Logf("%s:\n%s", p, raw)
		}
	}
	calls := u.calls(t)
	if len(calls) != 2 || strings.Join(calls[1], " ") != "mcp remove --scope user droidyard" {
		t.Errorf("claude was run as %q; want the add, then `mcp remove --scope user droidyard`", calls)
	}
	if _, err := os.Stat(filepath.Join(u.claude, ".claude.json")); err == nil {
		t.Error("Claude's entry is still in .claude.json after the uninstall")
	}
}

// Decision (ADR-0124 §1): a project's own entries take precedence over the user's, and
// `init --user` run in such a project says so for each client chosen that has one, with the
// command that removes them.
func TestInitUserReportsAProjectThatTakesPrecedence(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := gradleProj(t)
	if out, _, code := u.init(t, proj, "--client", "claude,cursor", "--root", proj); code != 0 {
		t.Fatalf("project init exited %d:\n%s", code, out)
	}

	out, errOut, code := u.init(t, proj, "--user", "--client", "all")
	if code != 0 {
		t.Fatalf("init --user exited %d:\n%s\n%s", code, out, errOut)
	}
	for _, c := range []struct{ client, file string }{{"claude", ".mcp.json"}, {"cursor", ".cursor/mcp.json"}} {
		line := lineWith(out, filepath.Join(proj, c.file))
		if !strings.Contains(line, "precedence") || !strings.Contains(line, "droidyard init --uninstall --client "+c.client) {
			t.Errorf("no report for %s: %q\n%s", c.client, line, out)
		}
	}
	if strings.Contains(out, "opencode.json holds") || strings.Contains(out, "--client opencode") {
		t.Errorf("OpenCode has no entry in the project, yet:\n%s", out)
	}

	// Only the clients chosen are reported, and only for the project the run is in.
	out, _, _ = u.init(t, proj, "--user", "--client", "claude", "--dry-run")
	if !strings.Contains(out, "--client claude") || strings.Contains(out, "--client cursor") {
		t.Errorf("--client claude reported:\n%s", out)
	}
	elsewhere := projectDir(t)
	out, _, _ = u.init(t, elsewhere, "--user", "--client", "all", "--dry-run")
	if strings.Contains(out, "precedence") {
		t.Errorf("a folder that is no project was reported as one:\n%s", out)
	}
}

func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// Decision (ADR-0124 §1): Claude's entry is never written by init itself, so with no
// `claude` on PATH that file is refused with the command to run, and the other files are
// still written.
func TestInitUserWithoutClaudePrintsTheCommand(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := gradleProj(t)
	out, _, code := u.initWithPath(t, proj, t.TempDir(), "--user", "--client", "claude")
	if code != 1 {
		t.Errorf("init --user without claude exited %d; want 1 (a refused file)", code)
	}
	if !strings.Contains(out, "claude mcp add-json --scope user droidyard '{") || !strings.Contains(out, `"args":["mcp"]`) {
		t.Errorf("the output does not give the command to run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(u.claude, ".claude.json")); err == nil {
		t.Error("init wrote Claude's .claude.json itself")
	}
	if _, err := os.Stat(filepath.Join(u.claude, "settings.json")); err != nil {
		t.Errorf("the rest of the run was not done: %v", err)
	}
}

// Decision (ADR-0124 §1, §4): the flags that cannot be combined exit 2 and write nothing:
// --user names no root and no project file, and the block goes in one place.
func TestInitFlagCombinations(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := gradleProj(t)
	projBefore, homeBefore := treeHash(t, proj), treeHash(t, u.home)
	for _, args := range [][]string{
		{"--user", "--client", "all", "--root", proj},
		{"--user", "--client", "all", "--exclude"},
		{"--user", "--client", "all", "--gitignore"},
		{"--client", "all", "--root", proj, "--exclude", "--gitignore"},
		{"--client", "all", "--user", "--uninstall", "--root", proj},
	} {
		out, errOut, code := u.init(t, proj, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "exclude each other") {
			t.Errorf("init %v exited %d with stdout %q, stderr %q; want 2, nothing on stdout and the clash named", args, code, out, errOut)
		}
	}
	if treeHash(t, proj) != projBefore || treeHash(t, u.home) != homeBefore || u.calls(t) != nil {
		t.Error("a refused combination wrote files or ran claude")
	}
	// The combinations that are fine.
	for _, args := range [][]string{
		{"--client", "claude", "--root", proj, "--exclude", "--dry-run"},
		{"--client", "claude", "--root", proj, "--gitignore", "--dry-run"},
		{"--user", "--client", "claude", "--uninstall", "--dry-run"},
	} {
		if out, errOut, code := u.init(t, proj, args...); code == 2 {
			t.Errorf("init %v was refused as a usage error:\n%s%s", args, out, errOut)
		}
	}
}

// gitIn runs real git with the host's configuration out of the way.
func userGit(t *testing.T, u userSetup, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "HOME="+u.home, "XDG_CONFIG_HOME="+u.xdg, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// repoWithProject is a repository with one commit holding a Gradle project at its top.
func repoWithProject(t *testing.T, u userSetup) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := gradleProj(t)
	userGit(t, u, dir, "init", "-q", "-b", "main")
	userGit(t, u, dir, "add", "-A")
	userGit(t, u, dir, "commit", "-q", "-m", "c")
	return dir
}

// Decision (ADR-0124 §4): --exclude puts init's untracked files in the common directory's
// info/exclude, which the main tree and every linked worktree read, so `git status` lists
// none of them; a tracked file gets no line and is reported; --uninstall removes the block.
func TestInitExcludeHidesInitsFilesFromGit(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	repo := repoWithProject(t, u)
	wt := filepath.Join(t.TempDir(), "wt")
	userGit(t, u, repo, "worktree", "add", "-q", "-b", "side", wt)

	// Without the flag the files show; the control that makes the rest prove something.
	if out, _, code := u.init(t, repo, "--client", "all", "--root", repo); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	if st := userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(st, ".mcp.json") {
		t.Fatalf("init's files do not show in git status without --exclude:\n%s", st)
	}
	if out, _, code := u.init(t, repo, "--client", "all", "--root", repo, "--uninstall"); code != 0 {
		t.Fatalf("uninstall exited %d:\n%s", code, out)
	}

	for _, root := range []string{repo, wt} {
		out, errOut, code := u.init(t, root, "--client", "all", "--root", root, "--exclude")
		if code != 0 {
			t.Fatalf("init --exclude in %s exited %d:\n%s\n%s", root, code, out, errOut)
		}
		if !strings.Contains(out, "ignores /.mcp.json") || !strings.Contains(out, "/.cursor/") && !strings.Contains(out, "/.cursor/mcp.json") {
			t.Errorf("the output does not give the block's lines:\n%s", out)
		}
	}
	for _, root := range []string{repo, wt} {
		if st := userGit(t, u, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
			t.Errorf("git status in %s lists init's files:\n%s", root, st)
		}
	}
	// The block is in the main repository's info/exclude and nowhere in the worktree's own.
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), "# droidyard init:") {
		t.Errorf("info/exclude holds no block:\n%s", exclude)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error("--exclude wrote a .gitignore")
	}

	// Uninstall removes the block (and the files), from the worktree as from the main tree.
	if out, _, code := u.init(t, wt, "--client", "all", "--root", wt, "--uninstall"); code != 0 {
		t.Fatalf("uninstall exited %d:\n%s", code, out)
	}
	// ADR-0126: the main tree still has its files, so the shared block keeps hiding them
	// (git status in BOTH trees is clean); only the last uninstall removes the block.
	for _, root := range []string{repo, wt} {
		if st := userGit(t, u, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
			t.Errorf("after the worktree's uninstall git status in %s lists:\n%s", root, st)
		}
	}
	if out, _, code := u.init(t, repo, "--client", "all", "--root", repo, "--uninstall"); code != 0 {
		t.Fatalf("uninstall in the main tree exited %d:\n%s", code, out)
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(exclude), "droidyard init") {
		t.Errorf("the last uninstall left the block:\n%s", exclude)
	}
}

func TestInitExcludeReportsATrackedFile(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	repo := repoWithProject(t, u)
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte("{\"mcpServers\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	userGit(t, u, repo, "add", "-A")
	userGit(t, u, repo, "commit", "-q", "-m", "mcp")

	out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo, "--exclude")
	if code != 0 {
		t.Fatalf("init --exclude exited %d:\n%s\n%s", code, out, errOut)
	}
	if l := lineWith(out, "not ignored"); !strings.Contains(l, ".mcp.json") {
		t.Errorf("the tracked .mcp.json is not reported:\n%s", out)
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), "/.mcp.json") {
		t.Errorf("a tracked file got a line:\n%s", exclude)
	}
	// What git shows is the tracked file init changed, and nothing else of init's.
	if st := strings.TrimSpace(userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all")); st != "M .mcp.json" {
		t.Errorf("git status = %q; want only the modified tracked .mcp.json", st)
	}
}

// Decision (ADR-0124 §4): --gitignore puts the block in the root's .gitignore; a rerun
// changes nothing; --uninstall removes the block and deletes a .gitignore it leaves empty.
func TestInitGitignore(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	repo := repoWithProject(t, u)

	out, errOut, code := u.init(t, repo, "--client", "claude", "--root", repo, "--gitignore")
	if code != 0 {
		t.Fatalf("init --gitignore exited %d:\n%s\n%s", code, out, errOut)
	}
	ignore, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"/.mcp.json", "/.claude/settings.json", "/.claude/skills/droidyard-android/"} {
		if !strings.Contains(string(ignore), "\n"+line+"\n") {
			t.Errorf(".gitignore lacks %q:\n%s", line, ignore)
		}
		if !strings.Contains(out, "ignores "+line) {
			t.Errorf("the output does not print the line %q:\n%s", line, out)
		}
	}
	if st := strings.TrimSpace(userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all")); st != "?? .gitignore" {
		t.Errorf("git status = %q; want only the new .gitignore", st)
	}
	if exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(exclude), "droidyard init") {
		t.Errorf("--gitignore wrote the exclude file:\n%s", exclude)
	}

	if out, _, code := u.init(t, repo, "--client", "claude", "--root", repo, "--gitignore"); code != 0 || !strings.Contains(out, "unchanged") {
		t.Errorf("a rerun exited %d:\n%s", code, out)
	}
	if again, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); string(again) != string(ignore) {
		t.Errorf("a rerun changed .gitignore:\n%s", again)
	}

	if out, _, code := u.init(t, repo, "--client", "claude", "--root", repo, "--uninstall"); code != 0 {
		t.Fatalf("uninstall exited %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error("the uninstall left a .gitignore that held only init's block")
	}
	if st := userGit(t, u, repo, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("git status after the uninstall:\n%s", st)
	}
}

// Decision (ADR-0124 §4): an uninstall always removes the block, and outside a repository
// has none to remove; that is never a failure. An install with a flag there is refused.
func TestInitUninstallOutsideARepository(t *testing.T) {
	t.Parallel()
	u := newUserSetup(t)
	proj := projectDir(t)
	if out, _, code := u.init(t, proj, "--client", "all", "--root", proj); code != 0 {
		t.Fatalf("init exited %d:\n%s", code, out)
	}
	for _, flags := range [][]string{{}, {"--exclude"}, {"--gitignore"}} {
		args := append([]string{"--client", "all", "--root", proj, "--uninstall"}, flags...)
		if out, errOut, code := u.init(t, proj, args...); code != 0 {
			t.Errorf("init %v exited %d:\n%s\n%s", args, code, out, errOut)
		}
	}
	if left := filesBelow(t, proj); len(left) != 0 {
		t.Errorf("the uninstall left %v", left)
	}

	out, _, code := u.init(t, proj, "--client", "claude", "--root", proj, "--exclude")
	if code != 1 || !strings.Contains(out, "not a git repository: nothing to ignore") {
		t.Errorf("--exclude outside a repository exited %d:\n%s; want 1 and the reason", code, out)
	}
}

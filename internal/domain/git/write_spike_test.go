package git

// The spike of the Git writes (plan m2n part 2, step P1; ADR-0071 §3, §5). Eleven facts
// about real git, each a test with a control that shows plain git doing the opposite,
// or a note in the test (a comment and a t.Log) that no control could fire, as
// history_harden_test.go does. Nothing here touches the network or a repository but the
// fixtures: every remote is a local path or a loopback httptest server.
//
// How a write git is started here is how part 2's runner will start it (write.go): the
// fixed prefix of writeFixedPrefix, the builder's arguments, writeEnv's environment, a
// process group of its own (Setpgid, as groupStarter has it; the exception is item 6,
// which asks for Setsid). Only the runner itself (run.go) is not used, since it still
// builds the read prefix; TestWritePrefixIsTheReadPrefix holds the two together.
//
// Item 6 needs a controlling terminal for the test process. Run it as
//
//	script -q /dev/null go test ./internal/domain/git/ -run 'TestWriteSpike6' -v
//
// Without a terminal its terminal part skips and says so; the rest of the suite does not
// need one. Run the package on both host gits by putting the git to test first on PATH
// (/opt/homebrew/bin for 2.51, /usr/bin for Apple's 2.50).

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

// --- how a test starts a write git --------------------------------------------------

// wInv is one invocation style of a write git: the fixed prefix and the environment.
type wInv struct {
	prefix, env []string
}

// inv is the daemon's invocation of a write: writeFixedPrefix and writeEnv for network.
// GIT_CONFIG_NOSYSTEM is added as needGit adds it for the runner (the host's system
// configuration, osxkeychain included, stays out); no_proxy keeps a loopback server
// reachable whatever the host's proxy settings.
func (e *env) inv(network bool) wInv { return e.invWith(network) }

// invWith is inv for a daemon whose own environment also holds extra (writeEnv scrubs it).
func (e *env) invWith(network bool, extra ...string) wInv {
	return wInv{
		prefix: writeFixedPrefix(network),
		env:    append(writeEnv(append(e.osEnviron(), extra...), network), "GIT_CONFIG_NOSYSTEM=1", "no_proxy=127.0.0.1", "NO_PROXY=127.0.0.1"),
	}
}

// without returns the invocation with the prefix pair "-c key=value" removed and the
// environment variable kv removed (either may be empty).
func (i wInv) without(prefixPair, envKV string) wInv {
	out := wInv{env: slices.Clone(i.env)}
	for j := 0; j < len(i.prefix); j++ {
		if prefixPair != "" && i.prefix[j] == "-c" && j+1 < len(i.prefix) && i.prefix[j+1] == prefixPair {
			j++
			continue
		}
		out.prefix = append(out.prefix, i.prefix[j])
	}
	if envKV != "" {
		out.env = slices.DeleteFunc(out.env, func(kv string) bool { return kv == envKV })
	}
	return out
}

// wres is what one write git produced.
type wres struct {
	Stdout, Stderr string
	Code           int // -1: did not exit by itself
	Took           time.Duration
}

// firstStderr is the first line of stderr.
func (r wres) firstStderr() string { return firstLine([]byte(r.Stderr)) }

func gitPath(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	return p
}

// gitVersion is `git --version`, logged so the output says which git it is about.
func gitVersion(t testing.TB) string {
	t.Helper()
	out, err := exec.Command(gitPath(t), "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (i wInv) cmd(t testing.TB, ctx context.Context, dir string, args []string, attr *syscall.SysProcAttr) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, gitPath(t), append(slices.Clone(i.prefix), args...)...)
	cmd.Dir = dir
	cmd.Env = i.env
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd
}

// run runs git in dir with args in a group of its own and waits for it (90 s at most).
func (i wInv) run(t testing.TB, dir string, args []string) wres {
	t.Helper()
	return i.runAs(t, dir, args, &syscall.SysProcAttr{Setpgid: true})
}

func (i wInv) runAs(t testing.TB, dir string, args []string, attr *syscall.SysProcAttr) wres {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := i.cmd(t, ctx, dir, args, attr)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	res := wres{Stdout: out.String(), Stderr: errb.String(), Code: -1, Took: time.Since(start)}
	if cmd.ProcessState != nil {
		res.Code = cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("git %v: %v", args, err)
	}
	return res
}

// hung is a git started in the background, ended by the test.
type hung struct {
	cmd       *exec.Cmd
	done      chan struct{}
	out, errb *bytes.Buffer
	pid       int
}

// start starts git without waiting. Whatever happens to the test, its group is killed
// by its pid when the test ends: nothing the test started outlives it.
func (i wInv) start(t testing.TB, dir string, args []string, attr *syscall.SysProcAttr) *hung {
	t.Helper()
	cmd := i.cmd(t, context.Background(), dir, args, attr)
	h := &hung{cmd: cmd, done: make(chan struct{}), out: &bytes.Buffer{}, errb: &bytes.Buffer{}}
	cmd.Stdout, cmd.Stderr = h.out, h.errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting git %v: %v", args, err)
	}
	h.pid = cmd.Process.Pid
	go func() { cmd.Wait(); close(h.done) }()
	t.Cleanup(func() {
		syscall.Kill(-h.pid, syscall.SIGKILL)
		<-h.done
	})
	return h
}

// exitState is how git ended, read only once the helper's Wait has returned (it writes
// ProcessState); "still running" if it has not within 5 s.
func (h *hung) exitState() string {
	select {
	case <-h.done:
		return h.cmd.ProcessState.String()
	case <-time.After(5 * time.Second):
		return "still running"
	}
}

func groupAlive(pgid int) bool { return syscall.Kill(-pgid, 0) != syscall.ESRCH }

// waitGone waits until no process is left in the group pgid, up to d.
func waitGone(pgid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !groupAlive(pgid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// endGroup ends the group the way ADR-0071 §3 and process-lifecycle.md §Shutdown,
// cancellation and no-orphans say: SIGTERM to the group, SIGKILL to it after grace if
// anything is still there (term false: SIGKILL at once, the control). It returns how long
// it took until kill(-pgid, 0) said ESRCH and whether the SIGKILL was needed.
func (h *hung) endGroup(term bool, grace time.Duration) (took time.Duration, needKill bool, gone bool) {
	start := time.Now()
	if term {
		syscall.Kill(-h.pid, syscall.SIGTERM)
		if waitGone(h.pid, grace) {
			return time.Since(start), false, true
		}
	}
	syscall.Kill(-h.pid, syscall.SIGKILL)
	gone = waitGone(h.pid, 5*time.Second)
	return time.Since(start), true, gone
}

// waitFile waits for a non-empty file and returns its content.
func waitFile(t testing.TB, path string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear in %v", path, d)
	return ""
}

// newRemote is a fresh Remote fixture and a plain-git environment (HOME temporary).
func newRemote(t testing.TB) (*env, *gittest.Remotes) {
	t.Helper()
	e := needGit(t)
	return e, gittest.Remote(t, filepath.Join(t.TempDir(), "fx"))
}

func newSubs(t testing.TB) (*env, *gittest.Subs) {
	t.Helper()
	e := needGit(t)
	return e, gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
}

func (e *env) rev(dir, rev string) string {
	e.t.Helper()
	return strings.TrimSpace(e.g(dir, "rev-parse", rev))
}

func (e *env) revOr(dir, rev string) string {
	out, err := e.gErr(dir, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// hookMarks reads and clears the hook marker file.
func hookMarks(t testing.TB, path string, clear bool) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	if clear {
		os.Remove(path)
	}
	return string(b)
}

// treeState is every file of a work tree outside .git (path, mode, content), and the
// refs and HEAD: what "nothing changed" is compared on.
func treeState(t testing.TB, e *env, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, _ := d.Info()
		fmt.Fprintf(&b, "%s %v %x\n", rel, fi.Mode(), sha1.Sum(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "HEAD %s\n%s", e.g(dir, "symbolic-ref", "-q", "HEAD"), e.g(dir, "for-each-ref", "--format=%(refname) %(objectname)"))
	out, _ := e.gErr(dir, "status", "--porcelain=v2", "--untracked-files=all")
	return b.String() + out
}

// locks lists every *.lock file under dir's .git.
func locks(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(filepath.Join(dir, ".git"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".lock") {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// --- builders and environment: decisions of write.go --------------------------------

// Decision (ADR-0071 §3): the write prefix is the read prefix, and a network command
// leaves out exactly protocol.allow=never. Proven against the read runner's own argv,
// not against a copy of the list.
func TestWritePrefixIsTheReadPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '"+argv+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newRunner(groupStarter, fake, os.Environ())
	if _, err := r.run(context.Background(), dir, true, nil, "SENTINEL"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	// The write prefix is the read prefix and one pair more: -c core.askPass= (empty), which
	// git-lfs reads itself, as it does not look at GIT_ASKPASS (ADR-0073 §Consequences). The
	// pair is the prefix's last, and pinned here: dropping it fails this test.
	wp := writeFixedPrefix(false)
	if n := len(wp); n < 2 || wp[n-2] != "-c" || wp[n-1] != "core.askPass=" {
		t.Fatalf("the write prefix does not end in -c core.askPass=: %q", wp)
	}
	want := append(slices.Clone(wp[:len(wp)-2]), "SENTINEL")
	if !slices.Equal(got, want) {
		t.Errorf("the read runner's argv is\n  %q\nthe write prefix says\n  %q", got, want)
	}
	if n := writeFixedPrefix(true); n[len(n)-1] != "core.askPass=" {
		t.Errorf("the network prefix does not carry core.askPass= either: %q", n)
	}
	net := writeFixedPrefix(true)
	if slices.Contains(net, "protocol.allow=never") {
		t.Errorf("the network prefix still has protocol.allow=never: %q", net)
	}
	if rest := writeFixedPrefix(false); len(rest)-len(net) != 2 {
		t.Errorf("the network prefix differs by more than -c protocol.allow=never: %q vs %q", net, rest)
	}
	for _, p := range [][]string{net, writeFixedPrefix(false)} {
		for _, must := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false", "--no-optional-locks"} {
			if !slices.Contains(p, must) {
				t.Errorf("prefix %q lacks %s: a write never runs a hook, even on the network prefix", p, must)
			}
		}
	}
}

// Decision (ADR-0071 §3): writeEnv is scrubEnv, without GIT_ALLOW_PROTOCOL=none for a
// network command and nothing else different; a caller's GIT_* never reaches git and
// GIT_TERMINAL_PROMPT=0 stays.
func TestWriteEnv(t *testing.T) {
	t.Parallel()
	in := []string{"PATH=/bin", "HOME=/h", "GIT_DIR=/elsewhere", "GIT_ALLOW_PROTOCOL=ssh", "GIT_TERMINAL_PROMPT=1", "LC_ALL=de_DE",
		"SSH_ASKPASS=/their/askpass", "SSH_ASKPASS_REQUIRE=force", "GIT_ASKPASS=/their/askpass"}
	off, on := writeEnv(in, false), writeEnv(in, true)
	plain := []string{"PATH=/bin", "HOME=/h"}
	if want := append(scrubEnv(plain), "GIT_ASKPASS=", "SSH_ASKPASS_REQUIRE=never"); !slices.Equal(off, want) {
		t.Errorf("writeEnv(.., false) = %q, want scrubEnv's plus the two askpass variables %q", off, want)
	}
	// The askpass variables (ADR-0071 §3, never a prompt): git asks an askpass program
	// before it looks at GIT_TERMINAL_PROMPT, so the caller's is dropped and git's is
	// emptied; ssh is told never to use one.
	for name, env := range map[string][]string{"network": on, "other": off} {
		count := func(kv string) (n int) {
			for _, e := range env {
				if e == kv {
					n++
				}
			}
			return
		}
		if count("GIT_ASKPASS=") != 1 || count("SSH_ASKPASS_REQUIRE=never") != 1 {
			t.Errorf("%s: GIT_ASKPASS= x%d, SSH_ASKPASS_REQUIRE=never x%d, want one each", name, count("GIT_ASKPASS="), count("SSH_ASKPASS_REQUIRE=never"))
		}
		for _, kv := range env {
			if strings.HasPrefix(kv, "SSH_ASKPASS=") || kv == "GIT_ASKPASS=/their/askpass" || kv == "SSH_ASKPASS_REQUIRE=force" {
				t.Errorf("%s: the caller's %s reached the environment", name, kv)
			}
		}
	}
	if !slices.Contains(off, "GIT_ALLOW_PROTOCOL=none") || slices.Contains(on, "GIT_ALLOW_PROTOCOL=none") {
		t.Errorf("GIT_ALLOW_PROTOCOL=none: off=%v on=%v, want present then absent", slices.Contains(off, "GIT_ALLOW_PROTOCOL=none"), slices.Contains(on, "GIT_ALLOW_PROTOCOL=none"))
	}
	for _, kv := range on {
		if strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") || kv == "GIT_DIR=/elsewhere" || kv == "GIT_TERMINAL_PROMPT=1" {
			t.Errorf("the caller's %s reached a network write's environment", kv)
		}
	}
	for _, must := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"} {
		if !slices.Contains(on, must) {
			t.Errorf("the network environment lacks %s", must)
		}
	}
	if got := len(off) - len(on); got != 1 {
		t.Errorf("the network environment differs from the other by %d variables, want exactly one", got)
	}
}

// Decision (ADR-0071 §5): a name is validated in Go before git sees it. Each row is a
// name git would take as an option, expand, or treat as something else.
func TestValidName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"main": true, "feature/x": true, "a-b_c.d": true, "ünï": true, "x": true,
		"": false, "-x": false, "--end-of-options": false, "@{-1}": false, "a@{u}": false, "HEAD": false,
		"@": false, "refs/heads/x": false, "refs/": false, "heads/x": false, "remotes/origin/x": false, "tags/v1": false,
		"a@b": true, "@x": true, "my-refs/x": true, "feature/heads/x": true, "refs": true, "tags": true,
		"FETCH_HEAD": false, "ORIG_HEAD": false, "MERGE_HEAD": false, "CHERRY_PICK_HEAD": false, "_HEAD": false,
		"HEADLINE": true, "Head": true, "fetch_HEAD": true, "FETCH_HEAD2": true, "feature/FETCH_HEAD": true, "AHEAD": false,
		"a\x00b": false, "a\nb": false, "a\tb": false, "a\x7fb": false,
		strings.Repeat("a", 255): true, strings.Repeat("a", 256): false,
	} {
		if got := validName(name); got != want {
			t.Errorf("validName(%q) = %v, want %v", name, got, want)
		}
	}
	// The split of checkout_test.go's two lists: every name TestBranchNamesRefusedBeforeAnyGit
	// relies on validName to refuse is refused, and every name TestBranchNamesRefused leaves
	// to git (or to the remotes) is accepted by validName, so that only git can be refusing it.
	for _, name := range namesRefusedInGo {
		if validName(name) {
			t.Errorf("validName(%q) = true; it is one of the names refused before any git", name)
		}
	}
	for _, name := range remotesRefusedInGo {
		if validName(name) {
			t.Errorf("validName(%q) = true; it is one of the remote names refused before any git", truncate(name, 40))
		}
	}
	for _, name := range namesRefusedByGit {
		if !validName(name) {
			t.Errorf("validName(%q) = false; it is one of the names left to git, which TestBranchNamesRefused asks", name)
		}
	}
}

// Decision (ADR-0071 §3, §5): every builder carries the write config; the flags that
// stop a submodule recursion and a prune are in the commands that need them; a name is
// after --end-of-options and a path after `--`, never the other way round.
func TestWriteBuilders(t *testing.T) {
	t.Parallel()
	has := func(args []string, seq ...string) bool {
		for i := 0; i+len(seq) <= len(args); i++ {
			if slices.Equal(args[i:i+len(seq)], seq) {
				return true
			}
		}
		return false
	}
	oid := strings.Repeat("a", 40)
	all := map[string][]string{
		"fetch": fetchArgs("origin"), "fetch --all": fetchArgs(""), "switch": switchArgs("b"), "track": trackArgs("b", "origin/b"),
		"branch": branchArgs("b", oid, false), "branch+switch": branchArgs("b", oid, true),
		"update": updateArgs(nil), "update paths": updateArgs([]string{"lib"}),
	}
	for name, args := range all {
		if !has(args, "-c", "gc.auto=0", "-c", "maintenance.auto=false") {
			t.Errorf("%s: no write config in %q", name, args)
		}
		if args[0] != literalPathspecs || args[1] != noReplaceObjects {
			t.Errorf("%s: %q does not start with the global switches", name, args)
		}
	}
	for _, name := range []string{"fetch", "fetch --all"} {
		if !has(all[name], "--no-recurse-submodules", "--no-prune", "--no-prune-tags") {
			t.Errorf("%s: %q lacks --no-recurse-submodules --no-prune --no-prune-tags", name, all[name])
		}
	}
	for _, name := range []string{"switch", "track", "branch+switch"} {
		if !slices.Contains(all[name], "--no-recurse-submodules") {
			t.Errorf("%s: %q lacks --no-recurse-submodules", name, all[name])
		}
	}
	if !slices.Contains(all["fetch --all"], "--all") || slices.Contains(all["fetch --all"], "--end-of-options") {
		t.Errorf("fetch of every remote: %q", all["fetch --all"])
	}
	if !has(all["fetch"], "--end-of-options", "origin") || !has(all["switch"], "--end-of-options", "b") ||
		!has(all["track"], "--create", "b", "--end-of-options", "origin/b") || !has(all["branch"], "--end-of-options", "b", oid) ||
		!has(all["branch+switch"], "--create", "b", "--end-of-options", oid) {
		t.Errorf("a name is not after --end-of-options: %v", all)
	}
	if !has(all["update paths"], "--", "lib") || slices.Contains(all["update"], "--") {
		t.Errorf("update's paths: %q and %q", all["update paths"], all["update"])
	}
	if !has(all["branch"], "branch") || !has(all["branch+switch"], "switch") || !slices.Contains(all["branch"], "--no-track") || !slices.Contains(all["branch+switch"], "--no-track") {
		t.Errorf("a new branch from an oid must not track: %q %q", all["branch"], all["branch+switch"])
	}
}

// Decision (ADR-0071 §3): the global --literal-pathspecs makes a submodule path holding
// `*` a path. Control: without it the same argument is a pattern and reaches `lib`.
func TestWriteSpikeLiteralSubmodulePaths(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, s := newSubs(t)
	moveLibBack := func() {
		e.g(filepath.Join(s.Super, "lib"), "-c", "core.hooksPath=/dev/null", "checkout", "-q", s.Oids["lib1"])
		if e.rev(filepath.Join(s.Super, "lib"), "HEAD") != s.Oids["lib1"] {
			t.Fatal("lib is not at lib1")
		}
	}
	for _, path := range []string{"l*", ":(top)lib"} {
		moveLibBack()
		res := e.inv(true).run(t, s.Super, updateArgs([]string{path}))
		t.Logf("literal %q: exit %d, stderr %q", path, res.Code, res.firstStderr())
		if got := e.rev(filepath.Join(s.Super, "lib"), "HEAD"); got != s.Oids["lib1"] {
			t.Errorf("%q: a pathspec pattern reached lib: HEAD %s, want it untouched at lib1", path, got)
		}
		if res.Code == 0 {
			t.Errorf("%q: a pattern that names no path was accepted: exit 0", path)
		}
		// Control: the same path without the global switch is a pattern and updates lib.
		args := slices.DeleteFunc(updateArgs([]string{path}), func(a string) bool { return a == literalPathspecs })
		res = e.inv(true).run(t, s.Super, args)
		if got := e.rev(filepath.Join(s.Super, "lib"), "HEAD"); got != s.Oids["lib2"] {
			t.Errorf("control: without --literal-pathspecs %q did not reach lib (HEAD %s, exit %d, %q); the test cannot fail", path, got, res.Code, res.firstStderr())
		}
	}
}

// --- the eleven facts ---------------------------------------------------------------

// Fact 1 (ADR-0071 §3): a fetch needs the network invocation. The read invocation (the
// runner's own prefix and environment) refuses git's local-path transport, each half on
// its own (the prefix's protocol.allow=never and the environment's
// GIT_ALLOW_PROTOCOL=none); writeEnv(network) with the network prefix brings the pushed
// commit. Control: plain git brings it too, so the refusal is the invocation's.
func TestWriteSpike1FetchNeedsTheNetworkInvocation(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	pushed := r.PushOther(t, 1)
	tip := func() string { return e.rev(r.W, "origin/main") }
	if tip() == pushed {
		t.Fatal("the fixture already holds the pushed commit")
	}
	args := fetchArgs("origin")

	// The read runner: exit non-zero, nothing fetched.
	res, err := e.r.run(context.Background(), r.W, true, nil, args...)
	if err != nil || res.ExitCode == 0 || tip() == pushed {
		t.Errorf("the read invocation fetched: err %v exit %d tip moved %v", err, res.ExitCode, tip() == pushed)
	}
	t.Logf("read invocation: exit %d, %q", res.ExitCode, firstLine(res.Stderr))

	net := e.inv(true)
	// Each half alone still refuses.
	prefixOnly := wInv{prefix: writeFixedPrefix(false), env: net.env}
	if got := prefixOnly.run(t, r.W, args); got.Code == 0 || tip() == pushed {
		t.Errorf("network environment with protocol.allow=never fetched (exit %d)", got.Code)
	} else {
		t.Logf("only protocol.allow=never: exit %d, %q", got.Code, got.firstStderr())
	}
	envOnly := wInv{prefix: writeFixedPrefix(true), env: e.inv(false).env}
	if got := envOnly.run(t, r.W, args); got.Code == 0 || tip() == pushed {
		t.Errorf("network prefix with GIT_ALLOW_PROTOCOL=none fetched (exit %d)", got.Code)
	} else {
		t.Logf("only GIT_ALLOW_PROTOCOL=none: exit %d, %q", got.Code, got.firstStderr())
	}

	// The network invocation brings it.
	got := net.run(t, r.W, args)
	if got.Code != 0 || tip() != pushed {
		t.Fatalf("the network invocation: exit %d, %q, tip %s want %s", got.Code, got.Stderr, tip(), pushed)
	}

	// Control: plain git fetches the next push, so nothing but the invocation refused.
	second := r.PushOther(t, 2)
	e.g(r.W, "fetch", "-q", "origin")
	if tip() != second {
		t.Errorf("control: plain git did not fetch; the test cannot fail")
	}
}

// Fact 2 (ADR-0071 §3): no hook runs, in the repository or in a submodule's git
// directory, whatever the write does: the fetch's ref update, a new branch, the switch;
// and `submodule update --recursive` reaches the submodules' own gits with the -c.
// Control: plain git runs both hooks for the same writes, and the invocation without
// core.hooksPath lets the children's hooks run (so it is the -c that reached them).
func TestWriteSpike2NoHookRuns(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	net := e.inv(true)
	hookMarks(t, r.Markers, true)
	r.PushOther(t, 1)
	steps := [][]string{
		fetchArgs("origin"),
		trackArgs("feature/x", "origin/feature/x"),
		switchArgs("main"),
		branchArgs("n1", r.Oids["y"], true),
		switchArgs("main"),
		branchArgs("n2", r.Oids["y"], false),
	}
	for _, args := range steps {
		if res := net.run(t, r.W, args); res.Code != 0 {
			t.Fatalf("git %v: exit %d: %s", args, res.Code, res.Stderr)
		}
	}
	if e.rev(r.W, "feature/x") != r.Oids["feature/x"] || e.revOr(r.W, "n2") != r.Oids["y"] || !strings.Contains(e.g(r.W, "symbolic-ref", "HEAD"), "main") {
		t.Fatal("the writes did not do what they were asked: no checkout, no ref update, so the absence of a marker proves nothing")
	}
	if got := hookMarks(t, r.Markers, true); got != "" {
		t.Errorf("a hook ran in the repository:\n%s", got)
	}
	// Control: plain git, the same kinds of writes.
	r.PushOther(t, 2)
	e.g(r.W, "fetch", "-q", "origin")
	e.g(r.W, "switch", "-q", "feature/x")
	e.g(r.W, "branch", "-q", "c1", r.Oids["y"])
	got := hookMarks(t, r.Markers, true)
	for _, hook := range []string{"reference-transaction", "post-checkout"} {
		if !strings.Contains(got, hook) {
			t.Errorf("control: plain git ran no %s hook; the test cannot fail", hook)
		}
	}

	// The submodules: lib's and lib/nested's own git directories hold the hooks.
	_, s := newSubs(t)
	lib, nested := filepath.Join(s.Super, "lib"), filepath.Join(s.Super, "lib", "nested")
	moveBack := func() {
		e.g(lib, "-c", "core.hooksPath=/dev/null", "checkout", "-q", s.Oids["lib1"])
		e.g(nested, "-c", "core.hooksPath=/dev/null", "checkout", "-q", s.Oids["nested1"])
		hookMarks(t, s.Markers, true)
		if e.rev(lib, "HEAD") != s.Oids["lib1"] || e.rev(nested, "HEAD") != s.Oids["nested1"] {
			t.Fatal("the submodules are not moved back")
		}
	}
	atRecorded := func() bool { return e.rev(lib, "HEAD") == s.Oids["lib2"] && e.rev(nested, "HEAD") == s.Oids["nested2"] }

	moveBack()
	if res := net.run(t, s.Super, updateArgs([]string{"lib"})); res.Code != 0 || !atRecorded() {
		t.Fatalf("update: exit %d, %q, at recorded %v", res.Code, res.Stderr, atRecorded())
	}
	if got := hookMarks(t, s.Markers, true); got != "" {
		t.Errorf("a hook ran in a submodule under the write invocation:\n%s", got)
	}

	// Control 1: without core.hooksPath in the prefix the children's hooks run, so the
	// -c at the top reached the gits `submodule update` started.
	moveBack()
	noHooksPath := net.without("core.hooksPath=/dev/null", "")
	if res := noHooksPath.run(t, s.Super, updateArgs([]string{"lib"})); res.Code != 0 || !atRecorded() {
		t.Fatalf("update without hooksPath: exit %d, %q", res.Code, res.Stderr)
	}
	got = hookMarks(t, s.Markers, true)
	for _, where := range []string{"/s/lib", "/s/lib/nested"} {
		if !hookRan(got, "post-checkout", where) {
			t.Errorf("control: without core.hooksPath no post-checkout ran in %q; the -c reaching the children is not shown:\n%s", where, got)
		}
	}
	// Control 2: plain git, the same update.
	moveBack()
	e.g(s.Super, "submodule", "update", "-q", "--recursive", "--", "lib")
	if got := hookMarks(t, s.Markers, true); !strings.Contains(got, "reference-transaction") {
		t.Errorf("control: plain git ran no hook in the submodules:\n%s", got)
	}
}

// hookRan reports whether the marker text has a line of the hook run in a directory
// ending in where.
func hookRan(marker, hook, where string) bool {
	for _, l := range strings.Split(marker, "\n") {
		if name, dir, ok := strings.Cut(l, " "); ok && name == hook && strings.HasSuffix(dir, where) {
			return true
		}
	}
	return false
}

// Fact 3 (ADR-0071 §3): no detached gc or maintenance outlives a fetch. A repository
// with gc.auto=1 and loose objects reachable from main gets its gc started by a plain
// fetch (the control: a pack appears and the loose objects go, gc detached from the
// process group by its own setsid); with the write config nothing runs. Which of the two
// -c settings does it in this git is recorded.
func TestWriteSpike3NoGcOutlivesAFetch(t *testing.T) {
	// Not parallel: it counts the gc and maintenance processes of the whole machine (ps), so no other git may run one meanwhile.
	t.Log(gitVersion(t))
	type variant struct {
		name         string
		dropGC, drop bool // drop gc.auto=0 / maintenance.auto=false from the builder's args
	}
	variants := []variant{
		{"both (the builder's)", false, false},
		{"gc.auto=0 only", false, true},
		{"maintenance.auto=false only", true, false},
		{"neither (control)", true, true},
	}
	ran := map[string]bool{}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			e, r := newRemote(t)
			// Three loose blobs whose ids start with 17: gc.auto=1 counts the objects of
			// objects/17 against ceil(1/256).
			for i, c := range blobsWithPrefix17(3) {
				e.write(r.W, fmt.Sprintf("loose-%d.txt", i), c)
			}
			// Committed with automatic maintenance off: from git 2.5x a commit starts a detached
			// `maintenance run --auto` whose loose-objects task packs these blobs while the
			// fetch below is watched, which reads as the fetch's gc (seen on git 2.55).
			e.g(r.W, "add", "-A")
			e.g(r.W, "-c", "maintenance.auto=false", "-c", "gc.auto=0", "commit", "-q", "-m", "c")
			dir17 := filepath.Join(r.W, ".git", "objects", "17")
			loose := func() int { es, _ := os.ReadDir(dir17); return len(es) }
			packs := func() int {
				es, _ := filepath.Glob(filepath.Join(r.W, ".git", "objects", "pack", "*.pack"))
				return len(es)
			}
			e.g(r.W, "config", "gc.auto", "1")
			if loose() < 2 {
				t.Fatalf("%d loose objects in objects/17, need 2 or more", loose())
			}
			before, packsBefore := gcProcesses(t), packs()
			r.PushOther(t, 1)

			args := fetchArgs("origin")
			if v.dropGC {
				args = dropPair(args, "gc.auto=0")
			}
			if v.drop {
				args = dropPair(args, "maintenance.auto=false")
			}
			if res := e.inv(true).run(t, r.W, args); res.Code != 0 {
				t.Fatalf("fetch: exit %d: %s", res.Code, res.Stderr)
			}
			// A detached gc is not waited for by fetch: look for its effect.
			wait := 3 * time.Second
			if v.dropGC && v.drop {
				wait = 15 * time.Second // the control: gc has to be seen
			}
			deadline := time.Now().Add(wait)
			fired := false
			for time.Now().Before(deadline) && !fired {
				fired = packs() > packsBefore || loose() == 0
				time.Sleep(50 * time.Millisecond)
			}
			var pidFile bool
			if _, err := os.Stat(filepath.Join(r.W, ".git", "gc.pid")); err == nil {
				pidFile = true
			}
			left := newProcesses(before, gcProcesses(t))
			t.Logf("fired %v, loose left %d, packs %d -> %d, gc.pid %v, new gc/maintenance processes %v", fired, loose(), packsBefore, packs(), pidFile, left)
			ran[v.name] = fired
			if v.name == "neither (control)" {
				return
			}
			if fired || pidFile || len(left) > 0 {
				t.Errorf("a gc or maintenance ran or is still running after the fetch (fired %v, gc.pid %v, processes %v)", fired, pidFile, left)
			}
		})
	}
	if !ran["neither (control)"] {
		t.Logf("BELT: no control fired in this git: gc.auto=1 with loose objects and a plain fetch started no gc, so the write config is a belt here and the test cannot discriminate")
		return
	}
	if ran["both (the builder's)"] {
		t.Errorf("the builder's fetch still ran a gc")
	}
}

// blobsWithPrefix17 finds n file contents whose git blob ids start with "17".
func blobsWithPrefix17(n int) []string {
	var out []string
	for i := 0; len(out) < n; i++ {
		c := fmt.Sprintf("loose %d\n", i)
		if sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(c), c))); sum[0] == 0x17 {
			out = append(out, c)
		}
	}
	return out
}

// dropPair removes "-c", kv from args.
func dropPair(args []string, kv string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == kv {
			return slices.Delete(slices.Clone(args), i, i+2)
		}
	}
	return args
}

// gcProcesses lists the pids of git processes running a gc or maintenance (read only).
func gcProcesses(t testing.TB) map[int]string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	m := map[int]string{}
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		pid, cmd, ok := strings.Cut(l, " ")
		if !ok || !strings.Contains(cmd, "git") {
			continue
		}
		if strings.Contains(cmd, " gc ") || strings.HasSuffix(cmd, " gc") || strings.Contains(cmd, "maintenance run") || strings.Contains(cmd, "git-gc") {
			if n, err := strconv.Atoi(pid); err == nil {
				m[n] = cmd
			}
		}
	}
	return m
}

func newProcesses(before, after map[int]string) []string {
	var out []string
	for pid, cmd := range after {
		if _, ok := before[pid]; !ok {
			out = append(out, fmt.Sprintf("%d %s", pid, cmd))
		}
	}
	return out
}

// uploadpackScript makes remote.origin.uploadpack a script that records its pid and
// hangs, so a fetch of a local path hangs in the transport. stubborn: it, and with it
// everything it starts, ignores SIGTERM (ignored signals are inherited), so only the
// SIGKILL after the grace ends it.
func (e *env) hangingUploadpack(dir string, stubborn bool) (pidFile string) {
	pidFile = filepath.Join(e.t.TempDir(), "up.pid")
	body := "echo $$ > '" + pidFile + "'\nexec sleep 60"
	if stubborn {
		body = "trap '' TERM\necho $$ > '" + pidFile + "'\nwhile :; do sleep 1; done"
	}
	e.g(dir, "config", "remote.origin.uploadpack", e.script("uploadpack.sh", body))
	return pidFile
}

// Fact 4 (ADR-0071 §3, process-lifecycle.md §Shutdown): a fetch hung in uploadpack is
// ended by SIGTERM to its group and SIGKILL after a 2 s grace, and kill(-pgid, 0) says
// ESRCH within 3 s of the SIGTERM. Two hangs: one that dies to the SIGTERM (git and the
// uploadpack alike: the grace costs nothing), and one that ignores it. The control is
// SIGTERM alone against the second, which leaves it alive.
func TestWriteSpike4HungFetchIsEnded(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	for _, c := range []struct {
		name     string
		stubborn bool
	}{{"cooperative uploadpack", false}, {"uploadpack ignoring SIGTERM", true}} {
		t.Run(c.name, func(t *testing.T) {
			e, r := newRemote(t)
			pidFile := e.hangingUploadpack(r.W, c.stubborn)
			h := e.inv(true).start(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setpgid: true})
			child, _ := strconv.Atoi(strings.TrimSpace(waitFile(t, pidFile, 20*time.Second)))
			if !groupAlive(h.pid) || mustPgid(child) != h.pid {
				t.Fatalf("the uploadpack (pid %d, group %d) is not in git's group %d", child, mustPgid(child), h.pid)
			}
			took, needKill, gone := h.endGroup(true, 2*time.Second)
			t.Logf("SIGTERM then SIGKILL after 2s: group gone %v in %v, SIGKILL needed %v; git: %v", gone, took.Round(time.Millisecond), needKill, h.exitState())
			if !gone || took > 3*time.Second {
				t.Errorf("kill(-pgid, 0) was not ESRCH within 3 s of the SIGTERM (gone %v after %v)", gone, took)
			}
			if c.stubborn != needKill {
				t.Errorf("SIGKILL needed = %v, want %v: the two hangs must differ in whether the grace is used", needKill, c.stubborn)
			}
			if syscall.Kill(child, 0) != syscall.ESRCH {
				t.Errorf("the uploadpack %d is still alive", child)
			}
		})
	}
	// Control: SIGTERM alone does not end the process that ignores it.
	e, r := newRemote(t)
	pidFile := e.hangingUploadpack(r.W, true)
	h := e.inv(true).start(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setpgid: true})
	waitFile(t, pidFile, 20*time.Second)
	syscall.Kill(-h.pid, syscall.SIGTERM)
	if waitGone(h.pid, 3*time.Second) {
		t.Errorf("control: SIGTERM alone ended a process that ignores it; the grace-then-SIGKILL is not shown to be needed")
	}
	syscall.Kill(-h.pid, syscall.SIGKILL)
	if !waitGone(h.pid, 5*time.Second) {
		t.Errorf("SIGKILL left the group alive")
	}
}

func mustPgid(pid int) int { g, _ := syscall.Getpgid(pid); return g }

// authServer answers every request that carries no Authorization header with 401, and
// counts the ones that do (answering them 404, which is not a git repository).
type authServer struct {
	*httptest.Server
	unauth, authed atomic.Int32
}

func newAuthServer(t testing.TB) *authServer {
	t.Helper()
	s := &authServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			s.authed.Add(1)
			http.NotFound(w, r)
			return
		}
		s.unauth.Add(1)
		w.Header().Set("WWW-Authenticate", `Basic realm="droidyard-spike"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(s.Close)
	return s
}

// Fact 5 (ADR-0071 §3): a remote that needs credentials the helper does not have fails at
// once and does not wait: GIT_TERMINAL_PROMPT=0, no prompt. Loopback httptest server
// answering 401, the fixture's credential.helper empty. Control: with credentials in the
// URL git sends them, so the failure is the missing credential, not the server.
// The prompt that would wait is the terminal part of item 6.
func TestWriteSpike5CredentialNeverPrompts(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	srv := newAuthServer(t)
	e.g(r.W, "config", "remote.origin.url", srv.URL+"/r.git")
	res := e.inv(true).run(t, r.W, fetchArgs("origin"))
	t.Logf("exit %d in %v; stderr first line: %q", res.Code, res.Took.Round(time.Millisecond), res.firstStderr())
	if res.Code == 0 || res.Took > 5*time.Second {
		t.Errorf("a fetch needing a credential: exit %d after %v, want a failure within 5 s", res.Code, res.Took)
	}
	if srv.unauth.Load() == 0 {
		t.Errorf("the server was never asked: the failure is not the 401's")
	}
	if !strings.Contains(res.Stderr, "terminal prompts disabled") && !strings.Contains(res.Stderr, "Authentication failed") && !strings.Contains(res.Stderr, "could not read") {
		t.Errorf("stderr does not name the credential: %q", res.Stderr)
	}
	if srv.authed.Load() != 0 {
		t.Errorf("git sent credentials it was never given")
	}

	// Control: credentials in the URL are used.
	u := strings.Replace(srv.URL, "http://", "http://user:pass@", 1) + "/r.git"
	e.g(r.W, "config", "remote.origin.url", u)
	res = e.inv(true).run(t, r.W, fetchArgs("origin"))
	t.Logf("control with credentials in the URL: exit %d, %q, authenticated requests %d", res.Code, res.firstStderr(), srv.authed.Load())
	if srv.authed.Load() == 0 {
		t.Errorf("control: git sent no credentials when it had them; the test cannot fail")
	}
}

// hasTerminal reports whether this process has a controlling terminal.
func hasTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Fact 6 (ADR-0071 §3): no terminal. The write git is started with exactly
// SysProcAttr{Setsid: true}: its process group and session are its own pid, so
// getpgid == getsid == pid, and there is no controlling terminal to open. Setpgid cannot
// be added (setpgid fails on a session leader). The control is Setpgid only, which keeps
// the session and so the terminal. The ids need no terminal; opening /dev/tty and the
// prompt need this test process to have one (see the top of this file).
func TestWriteSpike6Setsid(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	pidFile := e.hangingUploadpack(r.W, false)
	inv := e.inv(true)

	idsOf := func(attr *syscall.SysProcAttr) (pid, pgid, sid int) {
		h := inv.start(t, r.W, fetchArgs("origin"), attr)
		waitFile(t, pidFile, 20*time.Second)
		pgid, _ = syscall.Getpgid(h.pid)
		sid, _ = unix.Getsid(h.pid)
		syscall.Kill(-h.pid, syscall.SIGKILL)
		<-h.done
		os.Remove(pidFile)
		return h.pid, pgid, sid
	}
	pid, pgid, sid := idsOf(&syscall.SysProcAttr{Setsid: true})
	t.Logf("Setsid: pid %d, pgid %d, sid %d", pid, pgid, sid)
	if pgid != pid || sid != pid {
		t.Errorf("with Setsid getpgid = %d, getsid = %d, want both the pid %d", pgid, sid, pid)
	}
	pid, pgid, sid = idsOf(&syscall.SysProcAttr{Setpgid: true})
	own, _ := unix.Getsid(0)
	t.Logf("control, Setpgid only: pid %d, pgid %d, sid %d (this process's sid %d)", pid, pgid, sid, own)
	if pgid != pid || sid == pid || sid != own {
		t.Errorf("control: Setpgid only gave pgid %d, sid %d (pid %d, test's sid %d); the test cannot tell the two apart", pgid, sid, pid, own)
	}

	// Setpgid beside Setsid fails: the child is a session leader when it calls setpgid.
	cmd := exec.Command(gitPath(t), "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setpgid: true}
	err := cmd.Run()
	t.Logf("Setsid and Setpgid together: %v", err)
	if err == nil {
		t.Errorf("Setsid with Setpgid started git; the brief's claim that setpgid fails on a session leader does not hold here")
	}

	if !hasTerminal() {
		t.Skip("no controlling terminal: the /dev/tty and prompt parts need `script -q /dev/null go test ...` (see the top of write_spike_test.go)")
	}
	// A core.sshCommand that tries to open /dev/tty and records whether it could.
	mark := filepath.Join(t.TempDir(), "tty")
	ssh := e.script("ssh.sh", "if (: < /dev/tty) 2>/dev/null; then echo open > '"+mark+"'; else echo closed > '"+mark+"'; fi\nexit 1")
	e.g(r.W, "config", "core.sshCommand", ssh)
	e.g(r.W, "config", "remote.origin.url", "ssh://nobody@example.invalid/r.git")
	for _, c := range []struct {
		name string
		attr *syscall.SysProcAttr
		want string
	}{
		{"Setsid", &syscall.SysProcAttr{Setsid: true}, "closed"},
		{"control, Setpgid only", &syscall.SysProcAttr{Setpgid: true}, "open"},
	} {
		os.Remove(mark)
		res := inv.runAs(t, r.W, fetchArgs("origin"), c.attr)
		b, _ := os.ReadFile(mark)
		t.Logf("%s: /dev/tty %s (exit %d, %q)", c.name, strings.TrimSpace(string(b)), res.Code, res.firstStderr())
		if strings.TrimSpace(string(b)) != c.want {
			t.Errorf("%s: /dev/tty was %q, want %q", c.name, strings.TrimSpace(string(b)), c.want)
		}
	}

	// The prompt: GIT_TERMINAL_PROMPT=0 alone fails at once (item 5). Without it, a git
	// that shares the terminal waits for an answer; a git in a session of its own fails.
	srv := newAuthServer(t)
	e.g(r.W, "config", "remote.origin.url", srv.URL+"/r.git")
	promptable := inv.without("", "GIT_TERMINAL_PROMPT=0")
	h := promptable.start(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setpgid: true})
	select {
	case <-h.done:
		t.Errorf("control: git sharing the terminal and allowed to prompt did not wait (stderr %q); the test cannot fail", h.errb.String())
	case <-time.After(4 * time.Second):
		t.Logf("control: with the terminal and no GIT_TERMINAL_PROMPT=0 git waits (still running after 4 s, state: %s)", psState(h.pid))
	}
	took, _, gone := h.endGroup(true, 2*time.Second)
	t.Logf("ended in %v, gone %v", took.Round(time.Millisecond), gone)
	res := promptable.runAs(t, r.W, fetchArgs("origin"), &syscall.SysProcAttr{Setsid: true})
	t.Logf("Setsid, prompting allowed: exit %d in %v, stderr %q", res.Code, res.Took.Round(time.Millisecond), res.Stderr)
	if res.Code == 0 || res.Took > 5*time.Second {
		t.Errorf("Setsid without GIT_TERMINAL_PROMPT=0: exit %d after %v, want a failure at once (no terminal to ask)", res.Code, res.Took)
	}
}

func psState(pid int) string {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

// Fact 7 (ADR-0071 §5): a switch git refuses over local changes changes nothing, exits
// non-zero and names the cause and the files in the lines ADR-0071 §5 keeps. Control: on a
// clean tree the same switch succeeds. Whether a refused `switch --create` leaves the
// branch behind is recorded.
func TestWriteSpike7RefusedSwitchChangesNothing(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	for _, c := range []struct {
		name  string
		dirty func(e *env, dir string)
		clean func(e *env, dir string)
		cause string
	}{
		{"a modified tracked file", func(e *env, d string) { e.write(d, "shared.txt", "my edit\n") }, func(e *env, d string) { e.g(d, "checkout", "-q", "--", "shared.txt") }, "shared.txt"},
		{"an untracked file in the way", func(e *env, d string) { e.write(d, "x.txt", "mine\n") }, func(e *env, d string) { os.Remove(filepath.Join(d, "x.txt")) }, "x.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r := newRemote(t)
			e.g(r.W, "branch", "-q", "feature/x", "origin/feature/x")
			c.dirty(e, r.W)
			before := treeState(t, e, r.W)
			inv := e.inv(false)

			res := inv.run(t, r.W, switchArgs("feature/x"))
			cause := writeCause([]byte(res.Stderr))
			t.Logf("switch: exit %d\nstderr:\n%s", res.Code, res.Stderr)
			if res.Code == 0 {
				t.Fatalf("git switched over %s", c.name)
			}
			if !strings.Contains(cause, c.cause) || !strings.Contains(cause, "would be overwritten") {
				t.Errorf("the cause ADR-0071 §5 keeps does not name the cause and the file %s:\n%s", c.cause, cause)
			}
			if first := res.firstStderr(); strings.Contains(first, c.cause) {
				t.Logf("the first line alone already names the file: %q", first)
			} else {
				t.Logf("the first line alone does not name the file (%q): the 20-line cause is needed", first)
			}
			if after := treeState(t, e, r.W); after != before {
				t.Errorf("a refused switch changed the work tree:\nbefore:\n%s\nafter:\n%s", before, after)
			}

			// A refused `switch --create` at the same commit.
			res = inv.run(t, r.W, branchArgs("fresh", r.Oids["feature/x"], true))
			left := e.revOr(r.W, "refs/heads/fresh")
			t.Logf("switch --create fresh <feature/x>: exit %d, branch left behind: %v, stderr %q", res.Code, left != "", res.firstStderr())
			if res.Code == 0 {
				t.Errorf("git created and switched over %s", c.name)
			}
			if left != "" {
				t.Errorf("a refused switch --create left the branch behind (%s)", left)
			}
			if after := treeState(t, e, r.W); after != before {
				t.Errorf("a refused switch --create changed the work tree or the refs:\nbefore:\n%s\nafter:\n%s", before, after)
			}

			// Control: the same switch on a clean tree works, so the refusal was the change.
			c.clean(e, r.W)
			if res = inv.run(t, r.W, switchArgs("feature/x")); res.Code != 0 || e.rev(r.W, "HEAD") != r.Oids["feature/x"] {
				t.Errorf("control: the same switch on a clean tree: exit %d, %q; the test cannot fail", res.Code, res.firstStderr())
			}
		})
	}
}

// Fact 8 (ADR-0071 §3): in a partial clone a switch whose blobs are missing does not
// fetch under the non-network invocation: it is refused and nothing changes. The network
// invocation keeps GIT_NO_LAZY_FETCH=1 and refuses as well (recorded). Control: the
// network invocation with the variable removed switches and the blob arrives, so the
// refusal is the invocation's.
func TestWriteSpike8PartialCloneSwitch(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	e.g(r.Origin, "config", "uploadpack.allowFilter", "true")
	e.g(r.Origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	pc := filepath.Join(t.TempDir(), "pc")
	e.g(filepath.Dir(pc), "clone", "-q", "--filter=blob:none", "file://"+r.Origin, pc)
	e.g(pc, "branch", "-q", "feature/x", "origin/feature/x")
	// The tree object names the blob without needing it; rev-list --missing=print lists
	// it as missing without fetching it.
	missing := strings.Fields(e.g(pc, "ls-tree", "origin/feature/x", "x.txt"))[2]
	isMissing := func() bool {
		out, _ := e.gErr(pc, "rev-list", "--objects", "--missing=print", "origin/feature/x")
		return strings.Contains(out, "?"+missing)
	}
	if !isMissing() {
		t.Fatalf("the blob %s of x.txt is not missing in the partial clone", missing)
	}
	before := treeState(t, e, pc)

	res := e.inv(false).run(t, pc, switchArgs("feature/x"))
	t.Logf("non-network switch: exit %d in %v\nstderr:\n%s", res.Code, res.Took.Round(time.Millisecond), res.Stderr)
	if res.Code == 0 {
		t.Errorf("the switch succeeded: git fetched under the non-network invocation")
	}
	if after := treeState(t, e, pc); after != before {
		t.Errorf("the refused switch changed the clone:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if l := locks(t, pc); len(l) > 0 {
		t.Errorf("the refused switch left locks: %v", l)
	}
	if !isMissing() {
		t.Errorf("the refused switch fetched the blob")
	}

	// The network invocation of ADR-0071 §3 keeps GIT_NO_LAZY_FETCH=1 (only GIT_ALLOW_PROTOCOL
	// and protocol.allow are lifted), so it refuses too: recorded, not a defect. The user
	// fetches the blobs once with their own git.
	res = e.inv(true).run(t, pc, switchArgs("feature/x"))
	t.Logf("network invocation, GIT_NO_LAZY_FETCH=1 kept: exit %d, %q", res.Code, res.Stderr)
	if res.Code == 0 || !isMissing() {
		t.Errorf("the network invocation lazily fetched (exit %d): GIT_NO_LAZY_FETCH=1 is not holding", res.Code)
	}

	// Control: with lazy fetching on, the same invocation switches and the blob arrives, so
	// the refusals above were this variable and the protocol policy, not the clone.
	lazy := e.inv(true).without("", "GIT_NO_LAZY_FETCH=1")
	res = lazy.run(t, pc, switchArgs("feature/x"))
	t.Logf("control, lazy fetch allowed: exit %d, %q", res.Code, res.firstStderr())
	if res.Code != 0 || e.rev(pc, "HEAD") != e.rev(pc, "origin/feature/x") || isMissing() {
		t.Errorf("control: with lazy fetching the switch did not happen (exit %d, %q); the test cannot fail", res.Code, res.Stderr)
	}
}

// Fact 9 (ADR-0071 §5): `--end-of-options` is accepted by the commands that take a name
// and keeps a name that looks like an option a name; `check-ref-format --branch`
// outcomes are recorded for each name the plan lists, and validName refuses every name
// git would expand into another.
func TestWriteSpike9EndOfOptionsAndNames(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e, r := newRemote(t)
	pushed := r.PushOther(t, 1)
	net := e.inv(true)

	// Accepted, and the argument after it is a name.
	if res := net.run(t, r.W, fetchArgs("origin")); res.Code != 0 || e.rev(r.W, "origin/main") != pushed {
		t.Errorf("fetch --end-of-options origin: exit %d, %q", res.Code, res.firstStderr())
	}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"fetch a remote named like an option", fetchArgs("--tags")},
		{"switch to a branch named like an option", switchArgs("--detach")},
		{"switch --create from a start named like an option", trackArgs("t", "--detach")},
		{"branch named like an option", branchArgs("--set-upstream-to=x", r.Oids["y"], false)},
		{"branch from a start named like an option", branchArgs("t2", "--list", false)},
	} {
		res := net.run(t, r.W, c.args)
		t.Logf("%s: exit %d, %q", c.name, res.Code, res.firstStderr())
		if strings.Contains(res.Stderr, "unknown option") || strings.Contains(res.Stderr, "usage:") || strings.Contains(res.Stderr, "unknown switch") {
			t.Errorf("%s: git read the name as an option: %q", c.name, res.Stderr)
		}
	}
	// Control: without --end-of-options the same arguments are options (git's own
	// complaint or action, not a name).
	noEOO := slices.DeleteFunc(switchArgs("--detach"), func(a string) bool { return a == "--end-of-options" })
	res := net.run(t, r.W, noEOO)
	t.Logf("control, switch --detach without --end-of-options: exit %d, %q", res.Code, res.firstStderr())
	if strings.Contains(res.Stderr, "invalid reference") || strings.Contains(res.Stderr, "is not a commit") {
		t.Errorf("control: without --end-of-options git still read --detach as a name; the test cannot fail: %q", res.Stderr)
	}

	// submodule update: whether --end-of-options is accepted there (the builder passes
	// only `--` and paths; this records the alternative's availability).
	_, s := newSubs(t)
	with := append(updateArgs(nil), "--end-of-options")
	res = net.run(t, s.Super, with)
	t.Logf("submodule update --end-of-options: exit %d, %q", res.Code, res.firstStderr())
	with = append(updateArgs(nil), "--end-of-options", "--", "lib")
	res = net.run(t, s.Super, with)
	t.Logf("submodule update --end-of-options -- lib: exit %d, %q", res.Code, res.firstStderr())

	// check-ref-format --branch. A previous branch gives @{-1} something to expand to.
	e.g(r.W, "switch", "-q", "-c", "previous")
	e.g(r.W, "switch", "-q", "main")
	long := strings.Repeat("a", 300)
	for _, n := range []string{"-x", "@{-1}", "HEAD", "a..b", "a b", "x.lock", "refs/heads/x", long, "feature/x", "main", "@", "a/", "a//b", "a~1", "a^", "a:b", "a*b", "a?b", "a[b", "a\\b"} {
		cmd := exec.Command(gitPath(t), append(slices.Clone(writeFixedPrefix(false)), refFormatArgs(n)...)...)
		cmd.Dir = r.W
		cmd.Env = e.inv(false).env
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		shown := n
		if len(shown) > 20 {
			shown = fmt.Sprintf("%d bytes", len(n))
		}
		got := strings.TrimSpace(out.String())
		t.Logf("check-ref-format --branch %q: exit %d, stdout %q, stderr %q; validName %v", shown, code, trunc(got, 24), firstLine(errb.Bytes()), validName(n))
		// The invariant: a name both validName and git accept comes out of git as it went
		// in, so git expands nothing that was checked.
		if validName(n) && code == 0 && got != n {
			t.Errorf("validName accepts %q and git accepts it as %q: git would expand what Go let through", n, got)
		}
	}
	// Names git accepts that Go must still refuse because git would expand or reinterpret
	// them (the rows the plan names).
	for _, n := range []string{"@{-1}", "HEAD", "-x", long, "@", "refs/heads/x", "heads/x", "remotes/origin/x", "tags/v1"} {
		if validName(n) {
			t.Errorf("validName(%q) = true", n)
		}
	}
	// Names that are ambiguous with another ref or with HEAD: git accepts them
	// (check-ref-format exits 0 and prints the name as it is, so the invariant above cannot
	// catch them). validName must refuse them; the probe below records what git does
	// with them when the builders run on them with validName out of the way.
	for _, n := range []string{"@", "heads/x", "remotes/origin/x", "tags/v1", "refs/heads/x"} {
		cmd := exec.Command(gitPath(t), append(slices.Clone(writeFixedPrefix(false)), refFormatArgs(n)...)...)
		cmd.Dir, cmd.Env = r.W, e.inv(false).env
		if err := cmd.Run(); err != nil {
			t.Logf("check-ref-format refuses %q: %v", n, err)
			continue // git itself refuses it: validName's refusal is then a belt
		}
		if validName(n) {
			t.Errorf("git accepts %q as a branch name, reads it as another ref, and validName lets it through", n)
		}
	}
	// Why validName refuses them: names git accepts and reads as another ref. The branch is
	// created first (the builders do not call validName), then the switch to it is tried;
	// the outcome is recorded, and every one of these must be refused by validName.
	for _, n := range []string{"refs/heads/x", "heads/x", "@", "FETCH_HEAD", "ORIG_HEAD", "CHERRY_PICK_HEAD", "remotes/origin/x", "tags/v1"} {
		if validName(n) {
			t.Errorf("validName(%q) = true", n)
		}
		made := net.run(t, r.W, branchArgs(n, r.Oids["y"], false))
		sw := net.run(t, r.W, switchArgs(n))
		t.Logf("%q: branch exit %d %q; then switch exit %d %q; HEAD %q", n, made.Code, made.firstStderr(), sw.Code, sw.firstStderr(), strings.TrimSpace(e.g(r.W, "rev-parse", "--abbrev-ref", "HEAD")))
		e.g(r.W, "switch", "-q", "main")
		e.gErr(r.W, "update-ref", "-d", "refs/heads/"+n)
	}
	// Other names a branch can carry that validName must let through.
	for _, n := range []string{"x@y", "my-refs/x", "feature/heads/x", "HEADLINE", "Head"} {
		if !validName(n) {
			t.Errorf("validName(%q) = false", n)
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Fact 10 (ADR-0071 §3): `submodule update --init` clones the submodules from local-path
// URLs, which git's default protocol.file.allow=user refuses for a submodule's clone
// (git sets GIT_PROTOCOL_FROM_USER=0). Recorded: which configuration admits it, the
// superproject's own or only a global one, and whether the non-network invocation is
// still refused when it is admitted. Control: the admitted run clones, so the refusal was
// the policy.
func TestWriteSpike10SubmoduleFileTransport(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	e := needGit(t)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	clone := func(name string) string {
		d := filepath.Join(t.TempDir(), name)
		e.g(filepath.Dir(d), "clone", "-q", s.Origin, d)
		return d
	}
	initialised := func(d string) bool {
		_, err := os.Stat(filepath.Join(d, "lib", "lib.txt"))
		return err == nil
	}
	net := e.inv(true)

	d := clone("default")
	res := net.run(t, d, updateArgs(nil))
	t.Logf("default policy: exit %d, initialised %v, cause %q", res.Code, initialised(d), firstLine([]byte(writeCause([]byte(res.Stderr)))))
	defaultAdmits := res.Code == 0 && initialised(d)

	d = clone("repo-config")
	e.g(d, "config", "protocol.file.allow", "always")
	res = net.run(t, d, updateArgs(nil))
	t.Logf("protocol.file.allow=always in the superproject's config: exit %d, initialised %v, %q", res.Code, initialised(d), res.firstStderr())
	repoAdmits := res.Code == 0 && initialised(d)

	d = clone("global-config")
	if err := os.WriteFile(filepath.Join(e.home, ".gitconfig"), []byte("[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = net.run(t, d, updateArgs(nil))
	t.Logf("protocol.file.allow=always in the global config: exit %d, initialised %v, %q", res.Code, initialised(d), res.firstStderr())
	globalAdmits := res.Code == 0 && initialised(d)

	d = clone("global-config-no-network")
	res = e.inv(false).run(t, d, updateArgs(nil))
	t.Logf("global config admits, non-network invocation: exit %d, initialised %v, %q", res.Code, initialised(d), res.firstStderr())
	nonNetworkAdmits := res.Code == 0 && initialised(d)

	// Control: plain git is refused under the default policy too, and admitted with -c.
	os.Remove(filepath.Join(e.home, ".gitconfig"))
	d = clone("plain")
	_, plainErr := e.gErr(d, "submodule", "update", "--init", "--recursive")
	t.Logf("plain git, default policy: refused %v", plainErr != nil)
	d = clone("plain-c")
	_, adminErr := e.gErr(d, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	t.Logf("plain git with -c protocol.file.allow=always: refused %v", adminErr != nil)

	t.Logf("OUTCOME (%s): default admits %v; superproject config admits %v; global admits %v; non-network invocation admits (global set) %v",
		gitVersion(t), defaultAdmits, repoAdmits, globalAdmits, nonNetworkAdmits)
	if adminErr != nil {
		t.Errorf("control: plain git was refused even with protocol.file.allow=always; the test cannot fail")
	}
	if plainErr == nil && defaultAdmits {
		t.Logf("this git admits a local-path submodule under its default policy; the refusal is not the case here")
	}
	if nonNetworkAdmits {
		t.Errorf("the non-network invocation cloned a submodule: protocol.allow=never / GIT_ALLOW_PROTOCOL=none did not refuse it")
	}
}

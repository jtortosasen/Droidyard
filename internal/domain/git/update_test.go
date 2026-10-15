package git

// git.updateSubmodules (plan m2n part 2, step P5; ADR-0071 §1–§3, ADR-0073). Real git over
// the gittest.Submodules fixture, through the real supervisor's recorded spawner as
// write_test.go does. A submodule's local-path URL needs protocol.file.allow=always in the
// GLOBAL configuration (plan m2n P1 item 10), which each test writes into its temporary
// HOME: the daemon's git reads that HOME, and the repository's own configuration stays
// without it.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// subsFx is a wfx whose workspace is a superproject with submodules.
type subsFx struct {
	*wfx
	s *gittest.Subs
}

// newSubsFx opens the fixture `s` (lib and lib/nested initialised and at the recorded
// commits, extra uninitialised, hooks in s and in both initialised submodules).
func newSubsFx(t *testing.T) *subsFx {
	t.Helper()
	f := newWfxBare(t) // the Remote of newWfx would go unused: the workspace is `s`
	allowFileProtocol(t, f.e.home)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "subs"))
	f.roots["ws"] = platform.Current().Canonical(s.Super)
	f.register()
	return &subsFx{f, s}
}

// allowFileProtocol writes the global setting a submodule's clone from a local path needs.
func allowFileProtocol(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[protocol \"file\"]\n\tallow = always\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// clone makes a fresh clone of the superproject's remote: nothing initialised, no hooks.
func (f *subsFx) clone() string {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), "c")
	f.e.g(filepath.Dir(dir), "clone", "-q", f.s.Origin, dir)
	return dir
}

// open makes dir the workspace.
func (f *subsFx) open(dir string) {
	f.roots["ws"] = platform.Current().Canonical(dir)
	f.register()
}

// states is git.submodules' answer as path -> state, and the oid each is checked out at.
func (f *subsFx) states() (map[string]string, map[string]string) {
	f.t.Helper()
	res, err := f.call("git.submodules", "ws", SubmodulesParams{})
	if err != nil {
		f.t.Fatalf("git.submodules: %s (%s; %s)", err.Code, err.Message, err.Cause)
	}
	st, oid := map[string]string{}, map[string]string{}
	for _, s := range res.(SubmodulesResult).Submodules {
		st[s.Path], oid[s.Path] = s.State, s.CheckedOut
	}
	return st, oid
}

// update calls git.updateSubmodules directly.
func (f *subsFx) update(paths []string) (UpdateSubmodulesResult, *capability.Error) {
	f.t.Helper()
	res, err := f.call("git.updateSubmodules", "ws", UpdateSubmodulesParams{Paths: paths})
	if err != nil {
		return UpdateSubmodulesResult{}, err
	}
	return res.(UpdateSubmodulesResult), nil
}

func (f *subsFx) mustUpdate(paths []string) UpdateSubmodulesResult {
	f.t.Helper()
	res, err := f.update(paths)
	if err != nil {
		f.t.Fatalf("git.updateSubmodules %v: %s (%s; cause %q; nextStep %q)", paths, err.Code, err.Message, err.Cause, err.NextStep)
	}
	return res
}

func wantStates(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("states %v, want %v", got, want)
		return
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("states %v, want %v", got, want)
			return
		}
	}
}

// Decision (ADR-0071 §1): an update initialises what is not yet, as `--init --recursive`
// says, and brings it to the commit the repository records: after it the submodules that
// were uninitialised are clean at `recorded`, a nested one that did not exist before
// included. With paths, only those (and what is inside them) are touched. The control is
// the state before: a fresh clone lists extra and lib uninitialised.
func TestUpdateInitialises(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	f.open(f.clone())
	st, _ := f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "uninitialised"})

	res := f.mustUpdate([]string{"lib"})
	if !slices.Equal(res.Paths, []string{"lib"}) {
		t.Errorf("result paths %v, want [lib]", res.Paths)
	}
	st, oid := f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "clean", "lib/nested": "clean"})
	if oid["lib"] != f.s.Oids["lib2"] || oid["lib/nested"] != f.s.Oids["nested2"] {
		t.Errorf("lib at %s and nested at %s, want the recorded %s and %s", oid["lib"], oid["lib/nested"], f.s.Oids["lib2"], f.s.Oids["nested2"])
	}

	// No paths: every submodule listed when the call started.
	res = f.mustUpdate(nil)
	if !slices.Equal(res.Paths, []string{"extra", "lib", "lib/nested"}) {
		t.Errorf("result paths %v, want [extra lib lib/nested]", res.Paths)
	}
	st, oid = f.states()
	wantStates(t, st, map[string]string{"extra": "clean", "lib": "clean", "lib/nested": "clean"})
	if oid["extra"] != f.s.Oids["extra"] {
		t.Errorf("extra at %s, want the recorded %s", oid["extra"], f.s.Oids["extra"])
	}
}

// Decision (ADR-0071 §1): an update brings an outdated submodule to the recorded commit,
// at any depth. A nested submodule named alone is no path of the superproject's index (git
// answers "pathspec did not match" to it from there), so the call runs in its parent's
// directory; and an update of a submodule takes its nested ones with it (--recursive).
// Seen failing with a nested path passed from the workspace root: GIT_FAILED, "pathspec
// 'lib/nested' did not match any file(s) known to git".
func TestUpdateOutdated(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	lib, nested := filepath.Join(f.s.Super, "lib"), filepath.Join(f.s.Super, "lib", "nested")

	f.e.g(nested, "checkout", "-q", f.s.Oids["nested1"])
	st, _ := f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "clean", "lib/nested": "outdated"})
	res := f.mustUpdate([]string{"lib/nested"})
	if !slices.Equal(res.Paths, []string{"lib/nested"}) {
		t.Errorf("result paths %v, want [lib/nested]", res.Paths)
	}
	st, oid := f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "clean", "lib/nested": "clean"})
	if oid["lib/nested"] != f.s.Oids["nested2"] {
		t.Errorf("nested at %s, want the recorded %s", oid["lib/nested"], f.s.Oids["nested2"])
	}

	// lib outdated as well: asking for lib brings its nested one along, and asking for both
	// is the same one update.
	f.e.g(lib, "checkout", "-q", f.s.Oids["lib1"])
	f.e.g(nested, "checkout", "-q", f.s.Oids["nested1"])
	// lib's own index records nested1 at lib1, so from lib's side the nested one is clean.
	st, _ = f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "outdated", "lib/nested": "clean"})
	f.mustUpdate([]string{"lib", "lib/nested"})
	st, oid = f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "clean", "lib/nested": "clean"})
	if oid["lib"] != f.s.Oids["lib2"] || oid["lib/nested"] != f.s.Oids["nested2"] {
		t.Errorf("lib at %s and nested at %s, want the recorded %s and %s", oid["lib"], oid["lib/nested"], f.s.Oids["lib2"], f.s.Oids["nested2"])
	}
}

// Decision (ADR-0071 §5, §3): git's refusal is the call's GIT_FAILED with git's words, and
// nothing is forced: lib has a local change to a file that its recorded commit changes, so
// the checkout git would make there is refused and the change stays. The control is the
// state before (outdated, with the change in the way).
func TestUpdateGitRefuses(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	lib := filepath.Join(f.s.Super, "lib")
	f.e.g(lib, "checkout", "-q", f.s.Oids["lib1"])
	if err := os.WriteFile(filepath.Join(lib, "lib.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := f.update([]string{"lib"})
	wantErrCode(t, err, CodeFailed)
	if !strings.Contains(err.Cause, "lib.txt") {
		t.Errorf("cause %q does not name the file in the way", err.Cause)
	}
	if got, _ := os.ReadFile(filepath.Join(lib, "lib.txt")); string(got) != "mine\n" {
		t.Errorf("lib.txt is %q after a refused update, want the local change kept", got)
	}
	if got := f.e.rev(lib, "HEAD"); got != f.s.Oids["lib1"] {
		t.Errorf("lib moved to %s, want it left at %s", got, f.s.Oids["lib1"])
	}
}

// Decision (ADR-0071 §3): no hook runs in an update, in the superproject or in any
// submodule's own git directory, nested ones included: the `-c core.hooksPath=/dev/null` of
// the write prefix reaches the gits `submodule update` starts. Two levels move here (lib and
// lib/nested), each with hooks in its git directory. The control is plain git doing the
// same update in the same state, which writes the markers. Seen failing with
// core.hooksPath removed from writeFixedPrefix: the markers were written.
func TestUpdateRunsNoHook(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	lib, nested := filepath.Join(f.s.Super, "lib"), filepath.Join(f.s.Super, "lib", "nested")
	outdate := func() {
		f.e.g(lib, "checkout", "-q", f.s.Oids["lib1"])
		f.e.g(nested, "checkout", "-q", f.s.Oids["nested1"])
		hookMarks(t, f.s.Markers, true) // the setup's own checkouts ran the hooks
	}

	outdate()
	f.e.g(f.s.Super, "submodule", "update", "-q", "--recursive")
	if hookMarks(t, f.s.Markers, true) == "" {
		t.Fatal("control: plain git ran no hook in the submodules, so the test cannot fail")
	}

	outdate()
	f.mustUpdate([]string{"lib"})
	if marks := hookMarks(t, f.s.Markers, false); marks != "" {
		t.Errorf("a hook ran during the update: %q", marks)
	}
	st, _ := f.states()
	wantStates(t, st, map[string]string{"extra": "uninitialised", "lib": "clean", "lib/nested": "clean"})
}

// Decision (ADR-0071 §3, §5): a path reaches git only if the walk of git.submodules lists
// it, so each of these is REQUEST_INVALID_PARAMS, whole call, and no write git is started:
// a parent directory, a plain directory of the project, an option, a path in another
// spelling than the listing's, an absolute path, an empty one, a repeated one, an empty
// list, and a valid path beside an invalid one (nothing is updated). The control is the
// valid path, which does start one. Seen failing with the listing check removed: `docs`
// and `-x` reached git.
func TestUpdatePathsRefused(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	if err := os.MkdirAll(filepath.Join(f.s.Super, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{
		{".."}, {"../lib"}, {"lib/../extra"}, {"docs"}, {"-x"}, {"--recurse-submodules"}, {"."}, {""},
		{"lib/"}, {"./lib"}, {"/etc"}, {f.s.Super + "/lib"}, {"lib/nested/"}, {"lib/nested/x"}, {"LIB"},
		{"lib", "lib"}, {"lib", "docs"}, {"extra", "-x"}, {},
		{"*"}, {":(top)lib"}, {"lib\n"},
	} {
		f.writeStarts.Store(0)
		_, err := f.update(paths)
		wantErrCode(t, err, capability.ErrInvalidParams)
		if err.NextStep == "" {
			t.Errorf("paths %q: no nextStep", paths)
		}
		if n := f.writeStarts.Load(); n != 0 {
			t.Errorf("paths %q: %d write gits started, want none", paths, n)
		}
	}
	// More paths than the listing can hold are refused before the listing is read.
	over := make([]string, maxSubmodules+1)
	for i := range over {
		over[i] = "m" + strconv.Itoa(i)
	}
	f.writeStarts.Store(0)
	reads := f.readStarts.Load()
	_, err := f.update(over)
	wantErrCode(t, err, capability.ErrInvalidParams)
	if n, r := f.writeStarts.Load(), f.readStarts.Load()-reads; n != 0 || r != 0 {
		t.Errorf("%d write gits and %d reads started for %d paths, want none", n, r, len(over))
	}
	if st, _ := f.states(); st["extra"] != "uninitialised" {
		t.Errorf("extra is %s after the refused calls, want it left uninitialised", st["extra"])
	}

	f.mustUpdate([]string{"extra"})
	if n := f.writeStarts.Load(); n != 1 {
		t.Errorf("control: a valid path started %d write gits, want 1", n)
	}
}

// hangingRemotes is the global configuration that makes a submodule's clone talk to its
// remote's upload-pack, where a hook can hang it: git copies a local path's objects with
// no transport at all, so the absolute-path URLs of the fixture are read as file:// ones
// (insteadOf), and the pack-objects hook, which git reads only from the global
// configuration, is the script that decides which remote hangs.
func hangingRemotes(hook string) string {
	return "[protocol \"file\"]\n\tallow = always\n[url \"file:///\"]\n\tinsteadOf = /\n[uploadpack]\n\tpackObjectsHook = " + hook + "\n"
}

// Decision (ADR-0071 §3; process-lifecycle.md §Shutdown, cancellation and no-orphans): a
// cancelled update leaves nothing of its group, whatever the transports do with SIGTERM,
// though the git that hangs is not git's own child but a grandchild of the clone of a
// NESTED submodule: lib clones, then lib/nested's remote hangs in the pack-objects hook
// (set in the global configuration, the only place git reads that hook from), the whole
// chain git submodule -> clone -> upload-pack -> pack-objects -> hook being one group. The
// hook's pgid must be the recorded git's, and the group must be gone when the operation
// ends cancelled, with no record left. Two hangs: one that dies to SIGTERM and one that
// ignores it. Seen failing with the runner signalling git's pid only: the stubborn hook
// outlived the call.
func TestUpdateCancel(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	for _, stubborn := range []bool{false, true} {
		name := "dies to SIGTERM"
		if stubborn {
			name = "ignores SIGTERM"
		}
		t.Run(name, func(t *testing.T) {
			f := newSubsFx(t)
			f.open(f.clone())
			pidFile := filepath.Join(t.TempDir(), "hook.pid")
			body := "case \"$(pwd)\" in\n*s-nested.git)\n"
			if stubborn {
				body += "trap '' TERM\necho $$ > '" + pidFile + "'\nwhile :; do sleep 1; done\n"
			} else {
				body += "echo $$ > '" + pidFile + "'\nexec sleep 60\n"
			}
			body += ";;\nesac\nexec \"$@\"\n"
			hook := f.e.script("hook.sh", body)
			cfg := hangingRemotes(hook)
			if err := os.WriteFile(filepath.Join(f.e.home, ".gitconfig"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}

			id := f.start("git.updateSubmodules", "ws", UpdateSubmodulesParams{Paths: []string{"lib"}})
			child := pidIn(t, pidFile)
			pgid := mustPgid(child)
			if g := f.groupsOnce(); len(g) != 1 || pgid != g[0] {
				t.Fatalf("the hook %d is in group %d, the recorded git's is %v", child, pgid, f.groups())
			}
			if !groupAlive(pgid) {
				t.Fatal("the group is not alive before the cancel")
			}
			start := time.Now()
			if err := f.ops.Cancel(id); err != nil {
				t.Fatal(err)
			}
			out := f.until(id, 10*time.Second)
			took := time.Since(start)
			if out.State != capability.StateCancelled {
				t.Errorf("outcome %s, error %+v; want cancelled", out.State, out.Error)
			}
			if groupLive(pgid) {
				t.Errorf("group %d still has a member when the operation ended cancelled", pgid)
			}
			if syscall.Kill(child, 0) != syscall.ESRCH {
				t.Errorf("the hook %d is still alive", child)
			}
			if took > 4*time.Second {
				t.Errorf("the cancel took %v; the group should be gone within 3 s", took.Round(time.Millisecond))
			}
			if recs := f.records(); len(recs) != 0 {
				t.Errorf("processes.json holds %+v after the cancel, want none", recs)
			}
		})
	}
}

// Decision (ADR-0071 §3): an update is a network command and runs for at most the limit,
// then its whole group is stopped and the call answers GIT_FAILED saying how long it ran,
// with a nextStep that names the exact submodule to update in a terminal (not every
// submodule of the repository). The limit is shortened for the test, and the hang must be
// reached: extra's clone hangs in a pack-objects hook that writes its pid, and that process
// must be gone with its group. The limit runs from git's start, so on a loaded machine it can
// pass before the hook starts; that attempt proves nothing about the grandchild and is
// tried again, and the test fails if no attempt in five reached the hook.
func TestUpdateTimeout(t *testing.T) {
	// Not parallel: its limit is 3 s and the hung remote has to start inside it (it retries five times).
	t.Log(gitVersion(t))
	type answer struct {
		res UpdateSubmodulesResult
		err *capability.Error
	}
	for attempt := 1; attempt <= 5; attempt++ {
		f := newSubsFx(t)
		f.open(f.clone())
		pidFile := filepath.Join(t.TempDir(), "hook.pid")
		hook := f.e.script("hook.sh", "case \"$(pwd)\" in\n*s-extra.git) echo $$ > '"+pidFile+"'; exec sleep 60;;\nesac\nexec \"$@\"\n")
		if err := os.WriteFile(filepath.Join(f.e.home, ".gitconfig"), []byte(hangingRemotes(hook)), 0o644); err != nil {
			t.Fatal(err)
		}
		f.timeout = 3 * time.Second
		f.register()

		got := make(chan answer, 1)
		go func() {
			res, err := f.update([]string{"extra"})
			got <- answer{res, err}
		}()
		child, ended := 0, false
		var a answer
		for !ended && child == 0 {
			select {
			case a = <-got:
				ended = true
			case <-time.After(10 * time.Millisecond):
				if b, err := os.ReadFile(pidFile); err == nil {
					child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				}
			}
		}
		pgid := 0
		if child != 0 {
			pgid = mustPgid(child)
			select {
			case a = <-got:
			case <-time.After(30 * time.Second):
				t.Fatal("git.updateSubmodules did not return past its limit")
			}
		}
		wantErrCode(t, a.err, CodeFailed)
		if !strings.Contains(a.err.Message, "did not finish") || !strings.Contains(a.err.NextStep, "submodule update --init --recursive -- 'extra'") {
			t.Errorf("error %+v; want the time limit named and a nextStep naming the submodule", a.err)
		}
		for _, g := range f.groups() {
			if groupLive(g) {
				t.Errorf("group %d outlived the timed-out call", g)
			}
		}
		if child == 0 {
			t.Logf("attempt %d: the limit passed before the hook started; trying again", attempt)
			continue
		}
		if groupLive(pgid) || pidLive(child) {
			t.Errorf("the group %d (hook %d) outlived the timed-out call", pgid, child)
		}
		return
	}
	t.Fatal("the hook never started within the limit in five attempts: the machine is too loaded to prove the group is ended")
}

// Decision (ADR-0071 §3): a timed-out nested run's nextStep names the path relative to the
// directory it runs in (the terminal command is run there), quoted, so that command updates
// that submodule and not all of its parent's; and when a later run fails the note that the
// earlier runs stay applied is added to the error's own nextStep, not put in its place.
func TestUpdateNextSteps(t *testing.T) {
	t.Parallel()
	cmd := updateRun{dir: "/r/lib", paths: []string{"nested", "it's"}}.command()
	if cmd != `submodule update --init --recursive -- 'nested' 'it'\''s'` {
		t.Errorf("command = %s", cmd)
	}
	many := updateRun{dir: "/r", paths: []string{"a", "b", "c", "d", "e", "f", "g"}}.command()
	if !strings.HasSuffix(many, "'e' (and 2 more)") {
		t.Errorf("command = %s", many)
	}
	if got := partial(0, capability.NewError(CodeFailed, "x").WithNextStep("mine")); got.(*capability.Error).NextStep != "mine" {
		t.Errorf("the first run's failure was changed: %+v", got)
	}
	got := partial(1, capability.NewError(CodeFailed, "x").WithNextStep("run it in a terminal")).(*capability.Error)
	if !strings.Contains(got.NextStep, "run it in a terminal") || !strings.Contains(got.NextStep, "stay updated") {
		t.Errorf("nextStep %q lost one of the two notes", got.NextStep)
	}
	if got := partial(1, capability.NewError(CodeFailed, "x")).(*capability.Error); !strings.Contains(got.NextStep, "stay updated") {
		t.Errorf("nextStep %q", got.NextStep)
	}
	if err := partial(1, context.Canceled); err != context.Canceled {
		t.Errorf("a cancel was changed into %v", err)
	}
}

// Decision (ADR-0020 §Retention): a result returned again after delivery holds no paths, as
// [] not null, and the original is not changed.
func TestUpdateSlim(t *testing.T) {
	t.Parallel()
	reg := capability.NewRegistry()
	Register(reg, Options{})
	d, _ := reg.Lookup("git.updateSubmodules")
	full := UpdateSubmodulesResult{Paths: []string{"lib", "extra"}}
	got, ok := d.Slim(full).(UpdateSubmodulesResult)
	if !ok {
		t.Fatalf("Slim returned %T", d.Slim(full))
	}
	if b, _ := json.Marshal(got); string(b) != `{"paths":[]}` {
		t.Errorf("slim as JSON = %s", b)
	}
	if len(full.Paths) != 2 {
		t.Errorf("Slim changed the result it was given")
	}
}

// Decision (ADR-0071 §1, §3): which gits an update runs, and where. "All" names the
// submodules that no listed one holds, since git with no path updates every submodule of
// the repository, those outside a subdirectory workspace included; a nested one is asked in
// its parent's directory with the path relative to it, because git does not know it from
// above; and a path whose ancestor is asked is left to that ancestor's --recursive. Seen
// failing with every path passed from the root: the nested one is a run at the root.
func TestUpdateRuns(t *testing.T) {
	t.Parallel()
	known := []string{"extra", "lib", "lib/nested", "mods/m", "mods/m/deep/d"}
	for _, c := range []struct {
		name  string
		asked []string
		want  []updateRun
	}{
		{"all", known, []updateRun{{"/r", []string{"extra", "lib", "mods/m"}}}},
		{"the repository's own, then the nested ones", []string{"lib/nested", "extra"}, []updateRun{{"/r", []string{"extra"}}, {"/r/lib", []string{"nested"}}}},
		{"an ancestor takes its nested ones", []string{"lib", "lib/nested"}, []updateRun{{"/r", []string{"lib"}}}},
		{"a nested one under a nested one", []string{"mods/m/deep/d", "mods/m"}, []updateRun{{"/r", []string{"mods/m"}}}},
		{"the nested one alone, two levels", []string{"mods/m/deep/d"}, []updateRun{{"/r/mods/m", []string{"deep/d"}}}},
		{"none", nil, nil},
	} {
		got := updateRuns("/r", known, c.asked)
		if len(got) != len(c.want) {
			t.Errorf("%s: runs %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i].dir != c.want[i].dir || !slices.Equal(got[i].paths, c.want[i].paths) {
				t.Errorf("%s: runs %+v, want %+v", c.name, got, c.want)
				break
			}
		}
	}
	// An argv never grows with the list: paths are cut into runs of at most pathspecChunk bytes.
	var long, asked []string
	for i := 0; i < 200; i++ {
		p := strings.Repeat("x", 500) + string(rune('a'+i%26)) + strings.Repeat("y", i)
		long = append(long, p)
		asked = append(asked, p)
	}
	runs := updateRuns("/r", long, asked)
	total := 0
	for _, r := range runs {
		size := 0
		for _, p := range r.paths {
			size += len(p) + 1
		}
		if size > pathspecChunk {
			t.Errorf("a run holds %d bytes of paths, over %d", size, pathspecChunk)
		}
		total += len(r.paths)
	}
	if len(runs) < 2 || total != len(long) {
		t.Errorf("%d runs holding %d paths, want several holding %d", len(runs), total, len(long))
	}
}

// Decision (ADR-0071 §3, plan m2n P4's walk): an update of a workspace that is a
// subdirectory of the repository touches only the submodules under it. `all` there is
// the workspace's own, named, because plain `git submodule update --init` run from the
// subdirectory initialises `extra` at the repository's top too (the control).
func TestUpdateSubdirectoryWorkspace(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newSubsFx(t)
	super := f.clone()
	f.e.g(super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", f.s.Extra, "app/sub")
	f.e.g(super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", f.s.Nested, "app/sub2")
	f.e.g(super, "commit", "-q", "-m", "app submodules")
	f.e.g(super, "submodule", "deinit", "-q", "-f", "app/sub", "app/sub2")
	app := filepath.Join(super, "app")

	// The control: from the subdirectory, git's own update with no path reaches `extra`.
	ctl := f.clone()
	f.e.g(ctl, "-c", "protocol.file.allow=always", "submodule", "add", "-q", f.s.Extra, "app/sub")
	f.e.g(ctl, "commit", "-q", "-m", "app submodule")
	f.e.g(filepath.Join(ctl, "app"), "submodule", "update", "-q", "--init")
	if _, err := os.Stat(filepath.Join(ctl, "extra", "extra.txt")); err != nil {
		t.Fatalf("control: plain `git submodule update --init` in the subdirectory left extra alone: %v", err)
	}

	f.open(app)
	st, _ := f.states()
	wantStates(t, st, map[string]string{"sub": "uninitialised", "sub2": "uninitialised"})
	res := f.mustUpdate(nil)
	if !slices.Equal(res.Paths, []string{"sub", "sub2"}) {
		t.Errorf("result paths %v, want [sub sub2]", res.Paths)
	}
	st, _ = f.states()
	wantStates(t, st, map[string]string{"sub": "clean", "sub2": "clean"})
	if _, err := os.Stat(filepath.Join(super, "extra", "extra.txt")); err == nil {
		t.Errorf("extra, outside the workspace, was initialised")
	}
	if _, err := os.Stat(filepath.Join(super, "lib", "lib.txt")); err == nil {
		t.Errorf("lib, outside the workspace, was initialised")
	}
	// One path, spelled as the listing does, from the subdirectory.
	f.e.g(filepath.Join(app, "sub2"), "checkout", "-q", f.s.Oids["nested1"])
	if st, _ = f.states(); st["sub2"] != "outdated" {
		t.Fatalf("control: sub2 is %s, want outdated", st["sub2"])
	}
	f.mustUpdate([]string{"sub2"})
	if st, _ = f.states(); st["sub2"] != "clean" {
		t.Errorf("sub2 is %s after its update, want clean", st["sub2"])
	}
}

// Decision (ADR-0071 §3): an update with nothing to update starts no git. A project with
// no submodule answers an empty list, and a path is refused.
func TestUpdateNoSubmodules(t *testing.T) {
	t.Parallel()
	f := newWfx(t) // the plain Remote fixture has no submodule
	res, err := f.call("git.updateSubmodules", "ws", UpdateSubmodulesParams{})
	if err != nil {
		t.Fatalf("git.updateSubmodules: %s (%s)", err.Code, err.Message)
	}
	if p := res.(UpdateSubmodulesResult).Paths; p == nil || len(p) != 0 {
		t.Errorf("paths %#v, want an empty list", p)
	}
	_, err = f.call("git.updateSubmodules", "ws", UpdateSubmodulesParams{Paths: []string{"lib"}})
	wantErrCode(t, err, capability.ErrInvalidParams)
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started, want none", n)
	}
}

// Decision (ADR-0071 §3): a git that cannot be found is GIT_NOT_FOUND with no write started.
func TestUpdateNoGit(t *testing.T) {
	t.Parallel()
	f := newSubsFx(t)
	f.look = func(string) (string, error) { return "", os.ErrNotExist }
	f.register()
	_, err := f.update(nil)
	wantErrCode(t, err, CodeNotFound)
	if n := f.writeStarts.Load(); n != 0 {
		t.Errorf("%d write gits started, want none", n)
	}
}

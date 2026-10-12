package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The fence of the history reads (plan m2n step 1c; Q1 §3). ADR-0066 §Consequences: the
// guard is a list of what git executes on a read. Each attack below is a thing a
// repository's own configuration or data can make git run, or show in place of the
// truth, on a command the history reads use. The test asks, for every command:
//
//   - guarded: the daemon's invocation with the builder's argv (runner, untrusted root)
//     runs nothing and shows nothing the attacker wrote;
//   - control: plain git, asked the way that does run it, runs it. A guard shown against
//     an attack that nothing could run proves nothing, so this is what makes "no marker"
//     mean something;
//   - unfenced: the same invocation with every flag of the fence removed. The commands
//     in which it fires are the ones the fence is for; the others are commands git's
//     plumbing never runs the attack in, and their flags are a belt (named in the
//     attack's want lists, so a git that grows one fails here and the fence is revisited);
//   - bare: plain git with the unfenced argv, to tell what the runner's own flags
//     (core.fsmonitor=false, the filter overrides) are for.
//
// Every attack is a marker: a script that touches a file, or a text that must not appear.

// histCmd is one command the history reads run, as the builders produce it.
type histCmd struct {
	name  string
	args  []string
	stdin []byte
	fence []string // the flags of the fence in args
}

func histCmds(oid, parent string) []histCmd {
	details, files := showArgs(oid, parent)
	line := []byte(oid + "\n")
	// Every command carries the global --no-replace-objects; the fence of each adds to it.
	with := func(flags []string) []string { return append(slices.Clone(flags), noReplaceObjects) }
	return []histCmd{
		{"log", logArgs(0, 10), line, with(logFence)},
		{"refs", refsArgs(), nil, with(nil)},
		{"head", headArgs, nil, with(nil)},
		{"show-details", details, nil, with(logFence)},
		{"show-files", files, nil, with(diffFence)},
		{"worktree", worktreeArgs(), nil, with(nil)},
		// Not a builder: the command a caller reading an object (git.read does) would run,
		// built the same way, to show that cat-file too is forged by a replace ref.
		{"cat-file", hist("cat-file", "--batch"), line, with(nil)},
	}
}

// without returns args minus every element of drop.
func without(args []string, drop ...string) []string {
	var out []string
	for _, a := range args {
		if !slices.Contains(drop, a) {
			out = append(out, a)
		}
	}
	return out
}

// attack is one thing a repository can make git do.
type attack struct {
	id   string
	name string
	// attrs is the .gitattributes the repository starts with; setup then writes the
	// attack into it (a repository of two commits on main, a.txt and b.txt, a branch, a
	// lightweight and an annotated tag), with script the program that touches the marker.
	attrs string
	setup func(e *env, dir, script string)
	// outMarker, when not empty, is a text whose appearance in the output is the attack
	// (notes, mailmap and replace change what is shown; they run nothing). broken, when
	// set, says that an output of the command cmd is the attack (an encoding that breaks
	// the record format). raw, when not empty, is a text the guarded log and details
	// output must hold: what the repository really says, not what the attack says.
	outMarker string
	broken    func(cmd, out string) bool
	raw       string
	// control is plain git asked the way that does run the attack; for an attack seen in
	// the output, controlArgs is the plain command whose output shows it (default log -1).
	control     func(e *env, dir string)
	controlArgs []string
	// envVars are the variables the runner sets that guard against this attack besides
	// the builders' flags. With them removed from the runner's environment the unfenced
	// and one-flag variants show what the flags do; noEnv is what fires with the builder's
	// full argv and the variables removed (what the variables do); with the variables set
	// and the fence removed nothing may fire (the variables alone suffice). skipDrops: no
	// flag affects the attack, so the one-flag variants are not run.
	envVars   []string
	noEnv     []string
	skipDrops bool
	// The expected observations, exact: the commands in which the invocation with the
	// whole fence removed fires (unfenced), the same in plain git with none of the
	// runner's flags either (bare), and the (command, flag) pairs where removing that one
	// flag alone makes the attack fire (drops). Everything not listed is a command git
	// never runs the attack in; its flags are a belt, and this test names that.
	unfenced, bare []string
	drops          [][2]string
}

// historyRepo makes the repository every attack starts from.
func (e *env) historyRepo(attrs string) (dir, head string) {
	e.t.Helper()
	dir = e.repo()
	e.write(dir, ".gitattributes", attrs)
	e.write(dir, "a.txt", "line one\nline two\n")
	e.write(dir, "b.txt", "other\n")
	e.commit(dir)
	e.write(dir, "a.txt", "line one\nline two\nline three\n")
	e.write(dir, "b.txt", "other changed\n")
	e.commit(dir)
	e.g(dir, "branch", "side")
	e.g(dir, "tag", "light")
	e.g(dir, "tag", "-a", "-m", "annotated", "ann")
	return dir, strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
}

// signedHead replaces HEAD with a copy that carries a signature header of the given
// kind (the armour decides which gpg program git runs to verify it).
func (e *env) signedHead(dir, armour string) string {
	e.t.Helper()
	tree := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD^{tree}"))
	parent := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD^"))
	raw := "tree " + tree + "\nparent " + parent + "\n" +
		"author T <t@example.com> 1767225600 +0000\ncommitter T <t@example.com> 1767225600 +0000\n" +
		"gpgsig -----BEGIN " + armour + "-----\n \n bm90IGEgc2lnbmF0dXJl\n -----END " + armour + "-----\n\nsigned\n"
	oid := strings.TrimSpace(e.gStdin(dir, raw, "hash-object", "-t", "commit", "-w", "--stdin"))
	e.g(dir, "update-ref", "refs/heads/main", oid)
	e.g(dir, "reset", "-q", "--hard", "main")
	return oid
}

// gOut is the trimmed stdout of plain git (not its stderr, which may carry a hint).
func (e *env) gOut(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// As the real caller would see it: no grafts and no replacements in what it asks.
	cmd.Env = append(slices.Clone(e.env), "GIT_GRAFT_FILE=/dev/null", "GIT_NO_REPLACE_OBJECTS=1")
	out, err := cmd.Output()
	if err != nil {
		e.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func (e *env) gStdin(dir, stdin string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = e.env
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func attacks() []attack {
	cfg := func(text string) func(*env, string, string) {
		return func(e *env, dir, script string) {
			e.appendConfig(dir, strings.ReplaceAll(text, "SCRIPT", script))
		}
	}
	// A signature is verified by `log` and by the details of one commit, and --no-show-signature
	// is what stops it in both, the one flag of the fence that is load-bearing.
	sigCmds := []string{"log", "show-details"}
	sigDrops := [][2]string{{"log", "--no-show-signature"}, {"show-details", "--no-show-signature"}}
	// A replace ref forges everything that reads an object: the log, the details, the files
	// and a cat-file; --no-replace-objects alone stops it.
	replaceCmds := []string{"log", "show-details", "show-files", "cat-file"}
	replaceDrops := [][2]string{{"log", noReplaceObjects}, {"show-details", noReplaceObjects}, {"show-files", noReplaceObjects}, {"cat-file", noReplaceObjects}}
	encDrops := [][2]string{{"log", "--encoding=UTF-8"}, {"show-details", "--encoding=UTF-8"}}
	return []attack{
		{
			id: "gpg-openpgp", name: "gpg.program verifying a signature (log.showSignature)",
			unfenced: sigCmds, bare: sigCmds, drops: sigDrops,
			setup: func(e *env, dir, script string) {
				e.signedHead(dir, "PGP SIGNATURE")
				e.appendConfig(dir, "[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+script+"\n")
			},
			control: func(e *env, dir string) { e.g(dir, "log", "-1") },
		},
		{
			id: "gpg-x509", name: "gpg.x509.program verifying a signature (log.showSignature)",
			unfenced: sigCmds, bare: sigCmds, drops: sigDrops,
			setup: func(e *env, dir, script string) {
				e.signedHead(dir, "SIGNED MESSAGE")
				e.appendConfig(dir, "[log]\n\tshowSignature = true\n[gpg \"x509\"]\n\tprogram = "+script+"\n")
			},
			control: func(e *env, dir string) { e.g(dir, "log", "-1") },
		},
		{
			id: "gpg-ssh", name: "gpg.ssh.program verifying a signature (log.showSignature)",
			unfenced: sigCmds, bare: sigCmds, drops: sigDrops,
			setup: func(e *env, dir, script string) {
				e.signedHead(dir, "SSH SIGNATURE")
				e.write(dir, "../signers", "t@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPlaceholderPlaceholderPlaceholderPlaceho\n")
				e.appendConfig(dir, "[log]\n\tshowSignature = true\n[gpg \"ssh\"]\n\tprogram = "+script+"\n\tallowedSignersFile = "+filepath.Join(dir, "..", "signers")+"\n")
			},
			control: func(e *env, dir string) { e.g(dir, "log", "-1") },
		},
		{
			id: "diff-external", name: "diff.external",
			setup:   cfg("[diff]\n\texternal = SCRIPT\n"),
			control: func(e *env, dir string) { e.g(dir, "diff", "HEAD~1", "HEAD") },
		},
		{
			id: "diff-command", name: "diff.<driver>.command selected by .gitattributes",
			attrs:   "*.txt diff=evil\n",
			setup:   cfg("[diff \"evil\"]\n\tcommand = SCRIPT\n"),
			control: func(e *env, dir string) { e.g(dir, "diff", "HEAD~1", "HEAD") },
		},
		{
			id: "diff-textconv", name: "diff.<driver>.textconv selected by .gitattributes",
			attrs:   "*.txt diff=evil\n",
			setup:   cfg("[diff \"evil\"]\n\ttextconv = SCRIPT\n"),
			control: func(e *env, dir string) { e.g(dir, "log", "-p", "-1") },
		},
		{
			id: "pager", name: "core.pager and pager.log",
			setup:   cfg("[core]\n\tpager = SCRIPT\n[pager]\n\tlog = true\n\tdiff = true\n"),
			control: func(e *env, dir string) { e.ttyG(dir, "log", "-1") },
		},
		{
			id: "filter", name: "a filter driver (clean, smudge, process)",
			attrs:   "*.txt filter=evil\n",
			setup:   cfg("[filter \"evil\"]\n\tclean = SCRIPT\n\tsmudge = SCRIPT\n\tprocess = SCRIPT\n\trequired = true\n"),
			control: func(e *env, dir string) { e.gErr(dir, "cat-file", "--filters", "HEAD:a.txt") }, // dies on the bogus process filter, after running it
		},
		{
			id: "fsmonitor", name: "core.fsmonitor",
			bare:    []string{"show-files"}, // the pathspec makes diff-tree read the index; the runner's core.fsmonitor=false stops it
			setup:   cfg("[core]\n\tfsmonitor = SCRIPT\n"),
			control: func(e *env, dir string) { e.g(dir, "status", "--porcelain") },
		},
		{
			id: "notes", name: "notes.displayRef",
			outMarker: "NOTE-FROM-THE-REPOSITORY",
			setup: func(e *env, dir, script string) {
				e.g(dir, "notes", "add", "-m", "NOTE-FROM-THE-REPOSITORY", "HEAD")
				e.appendConfig(dir, "[notes]\n\tdisplayRef = refs/notes/commits\n")
			},
			control: func(e *env, dir string) { e.g(dir, "log", "-1") },
		},
		{
			id: "mailmap", name: "log.mailmap with a .mailmap",
			outMarker: "Forged Name",
			setup: func(e *env, dir, script string) {
				e.write(dir, ".mailmap", "Forged Name <forged@example.com> <t@example.com>\n")
				e.commit(dir)
				e.appendConfig(dir, "[log]\n\tmailmap = true\n")
			},
			control: func(e *env, dir string) { e.g(dir, "log", "-1") },
		},
		{
			id: "replace", name: "refs/replace/<oid> making a forged commit stand for HEAD",
			// Either guard alone suffices: the flag in the argv, or GIT_NO_REPLACE_OBJECTS in
			// the runner's environment. Both removed, the attack works.
			envVars:  []string{"GIT_NO_REPLACE_OBJECTS"},
			unfenced: replaceCmds, bare: replaceCmds, drops: replaceDrops,
			outMarker: "FORGED", raw: "t@example.com",
			setup: func(e *env, dir, script string) {
				head := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
				forged := e.forgedCommit(dir, strings.TrimSpace(e.g(dir, "rev-parse", "HEAD^")))
				e.g(dir, "update-ref", "refs/replace/"+head, forged)
			},
		},
		{
			id: "grafts", name: ".git/info/grafts giving HEAD a forged parent",
			bare:      sigCmds,
			outMarker: "FORGED", raw: "t@example.com",
			setup: func(e *env, dir, script string) {
				head := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
				forged := e.forgedCommit(dir, "")
				graftOid = forged
				e.write(dir, ".git/info/grafts", head+" "+forged+"\n")
			},
			broken:      func(cmd, out string) bool { return graftOid != "" && strings.Contains(out, graftOid) },
			controlArgs: []string{"log", "--format=%an %H", "HEAD"},
			// --no-replace-objects does not switch grafts off; only GIT_GRAFT_FILE does, which
			// the runner sets: no flag matters, the variable does.
			envVars: []string{"GIT_GRAFT_FILE"}, noEnv: sigCmds, skipDrops: true,
			unfenced: sigCmds,
		},
		{
			id: "encoding", name: "i18n.logOutputEncoding = UTF-16",
			unfenced: sigCmds, bare: sigCmds, drops: encDrops,
			setup: cfg("[i18n]\n\tlogOutputEncoding = UTF-16\n"),
			broken: func(cmd, out string) bool {
				fields := map[string]int{"log": logFields, "show-details": showFields}[cmd]
				if fields == 0 {
					return false
				}
				parts := strings.Split(out, "\x00")
				n := len(parts) - 1
				if !utf8.ValidString(out) || parts[n] != "" || n == 0 || n%fields != 0 {
					return true
				}
				for i := 0; i < n; i += fields {
					if len(parts[i]) != 40 {
						return true
					}
				}
				return false
			},
			controlArgs: []string{"log", "-1", "-z", "--format=" + logFormat},
		},
	}
}

// graftOid is the forged commit of the grafts attack, for its check of the output.
var graftOid string

// forgedCommit writes a commit by a forged author, with a message and a file that carry
// the marker FORGED, on top of parent (none when empty), and returns its oid.
func (e *env) forgedCommit(dir, parent string) string {
	e.t.Helper()
	blob := strings.TrimSpace(e.gStdin(dir, "forged\n", "hash-object", "-w", "--stdin"))
	tree := strings.TrimSpace(e.gStdin(dir, "100644 blob "+blob+"\tFORGED-FILE.txt\n", "mktree"))
	raw := "tree " + tree + "\n"
	if parent != "" {
		raw += "parent " + parent + "\n"
	}
	raw += "author FORGED Author <forged@example.com> 1767225600 +0000\ncommitter FORGED Author <forged@example.com> 1767225600 +0000\n\nFORGED-MESSAGE\n"
	return strings.TrimSpace(e.gStdin(dir, raw, "hash-object", "-t", "commit", "-w", "--stdin"))
}

// ttyG runs plain git with a terminal on its stdout, through script(1), as a pager needs.
func (e *env) ttyG(dir string, args ...string) {
	e.t.Helper()
	sc, err := exec.LookPath("script")
	if err != nil {
		e.t.Skip("no script(1) to give git a terminal")
	}
	cmd := exec.Command(sc, append([]string{"-q", "/dev/null", "git"}, args...)...)
	cmd.Dir = dir
	cmd.Env = e.env
	cmd.CombinedOutput()
}

// plainRun is git, bare, with args and stdin: no runner, no -c guards.
func (e *env) plainRun(dir string, stdin []byte, args ...string) string {
	cmd := exec.Command(e.r.git, args...)
	cmd.Dir = dir
	cmd.Env = e.env
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestHistoryFence(t *testing.T) {
	t.Logf("%s at %s", strings.TrimSpace(needGit(t).plainRun(t.TempDir(), nil, "--version")), needGit(t).r.git)
	for _, a := range attacks() {
		t.Run(a.id, func(t *testing.T) {
			e := needGit(t)
			mark := filepath.Join(t.TempDir(), "mark")
			script := e.script("attack.sh", "touch '"+mark+"'\ncat")
			dir, _ := e.historyRepo(a.attrs)
			a.setup(e, dir, script)
			// A dirty work tree, so that a command which looked at it would have a reason
			// to run a clean filter or ask the file system monitor.
			e.write(dir, "a.txt", "an uncommitted edit\n")
			head := e.gOut(dir, "rev-parse", "HEAD")
			parent := e.gOut(dir, "rev-parse", "HEAD^")
			fired := func(cmd, out string) bool {
				_, err := os.Stat(mark)
				os.Remove(mark)
				return err == nil || (a.outMarker != "" && strings.Contains(out, a.outMarker)) || (a.broken != nil && a.broken(cmd, out))
			}

			// Control: the attack is real. Plain git, asked the way that triggers it, runs it.
			var controlOut string
			switch {
			case a.controlArgs != nil:
				controlOut = e.plainRun(dir, nil, a.controlArgs...)
			case a.outMarker != "":
				controlOut = e.plainRun(dir, nil, "log", "-1")
			default:
				a.control(e, dir)
			}
			if !fired("log", controlOut) {
				t.Fatalf("control: plain git did not run the attack; the test cannot fail")
			}

			ctx := context.Background()
			// base is the runner the unfenced and one-flag variants use: the runner as it is,
			// or, for an attack the runner's environment also guards, without those variables.
			base, noEnv := e.r, e.r
			if len(a.envVars) > 0 {
				rn := *e.r
				rn.env = nil
				for _, kv := range e.r.env {
					if !slices.Contains(a.envVars, strings.SplitN(kv, "=", 2)[0]) {
						rn.env = append(rn.env, kv)
					}
				}
				if len(rn.env) != len(e.r.env)-len(a.envVars) {
					t.Fatalf("the runner does not set %v", a.envVars)
				}
				base, noEnv = &rn, &rn
			}
			var unfenced, bare, noEnvFires []string
			var drops [][2]string
			for _, c := range histCmds(head, parent) {
				// Guarded: the daemon's invocation, the builder's argv.
				res, err := e.r.run(ctx, dir, false, c.stdin, c.args...)
				if err != nil || res.ExitCode != 0 {
					t.Errorf("%s: err %v exit %d: %s", c.name, err, res.ExitCode, res.Stderr)
				}
				if fired(c.name, string(res.Stdout)) {
					t.Errorf("%s: the attack ran or showed through the guarded invocation", c.name)
				}
				if a.raw != "" && (c.name == "log" || c.name == "show-details") && !strings.Contains(string(res.Stdout), a.raw) {
					t.Errorf("%s: the guarded output lacks what the repository really says (%q): %q", c.name, a.raw, res.Stdout)
				}
				// Unfenced: the same with the whole fence removed.
				un := without(c.args, c.fence...)
				res, err = base.run(ctx, dir, false, c.stdin, un...)
				if err != nil {
					t.Fatalf("%s unfenced: %v", c.name, err)
				}
				fires := fired(c.name, string(res.Stdout))
				if len(a.envVars) > 0 {
					// The variables alone: fence removed, environment as it is.
					res, err := e.r.run(ctx, dir, false, c.stdin, un...)
					if err != nil || fired(c.name, string(res.Stdout)) {
						t.Errorf("%s: with the fence removed the runner's environment alone did not stop the attack (err %v)", c.name, err)
					}
					// The flags alone: full argv, the variables removed.
					res, err = noEnv.run(ctx, dir, false, c.stdin, c.args...)
					if err != nil {
						t.Fatalf("%s without the environment: %v", c.name, err)
					}
					if fired(c.name, string(res.Stdout)) {
						noEnvFires = append(noEnvFires, c.name)
					}
				}
				if fires {
					unfenced = append(unfenced, c.name)
				}
				// Bare: plain git with the unfenced argv: what the runner's own flags are for.
				if fired(c.name, e.plainRun(dir, c.stdin, un...)) {
					bare = append(bare, c.name)
				}
				// One flag at a time, where the whole fence matters. A flag only ever removes
				// behaviour, so where nothing fires without all of them, nothing fires
				// without one.
				for _, d := range c.fence {
					if !fires || a.skipDrops {
						break
					}
					res, err := base.run(ctx, dir, false, c.stdin, without(c.args, d)...)
					if err != nil {
						t.Fatalf("%s without %s: %v", c.name, d, err)
					}
					if fired(c.name, string(res.Stdout)) {
						drops = append(drops, [2]string{c.name, d})
					}
				}
			}
			// The tip set runs three of those commands itself.
			tp, trunc, err := tips(ctx, e.r, dir, false)
			if err != nil || trunc || len(tp) == 0 || fired("tips", "") {
				t.Errorf("tips: %v truncated %v tips %v", err, trunc, tp)
			}

			// What the fence is for, exactly: the commands the unfenced invocation fires in
			// are the ones whose flags are load-bearing, and the flag is the one named. Any
			// other command is one git's plumbing never runs this attack in; a git that
			// grows it fails here.
			if !equal(unfenced, a.unfenced) {
				t.Errorf("fires without the fence in %q, want %q", unfenced, a.unfenced)
			}
			if !equal(bare, a.bare) {
				t.Errorf("fires in bare git with the unfenced argv in %q, want %q", bare, a.bare)
			}
			if !equal(noEnvFires, a.noEnv) {
				t.Errorf("fires with the full argv and the runner's variables %v removed in %q, want %q", a.envVars, noEnvFires, a.noEnv)
			}
			var gotDrops, wantDrops []string
			for _, d := range drops {
				gotDrops = append(gotDrops, d[0]+" without "+d[1])
			}
			for _, d := range a.drops {
				wantDrops = append(wantDrops, d[0]+" without "+d[1])
			}
			if !equal(gotDrops, wantDrops) {
				t.Errorf("fires with one flag removed: %q, want %q", gotDrops, wantDrops)
			}
		})
	}
}

// Decision (Q1 §3): the builders carry the whole fence and the global pathspec switch,
// before the subcommand where git reads it; a commit is named by its oid after the
// options and before "--"; and the oid is the only caller text in any argv.
func TestHistoryArgvShape(t *testing.T) {
	const oid, parent = "0123456789abcdef0123456789abcdef01234567", "89abcdef0123456789abcdef0123456789abcdef"
	details, files := showArgs(oid, parent)
	// Written out, not read from logFence and diffFence: a flag lost from them fails here.
	logFlags := []string{"--no-show-signature", "--no-notes", "--no-color", "--no-ext-diff", "--no-textconv", "--no-use-mailmap", "--encoding=UTF-8"}
	diffFlags := []string{"--no-ext-diff", "--no-textconv", "--no-color"}
	for name, c := range map[string]struct {
		args  []string
		fence []string
	}{
		"log": {logArgs(7, 11), logFlags}, "show details": {details, logFlags}, "show files": {files, diffFlags},
		"refs": {refsArgs(), nil}, "head": {headArgs, nil}, "worktree": {worktreeArgs(), nil},
	} {
		if c.args[0] != "--literal-pathspecs" || c.args[1] != "--no-replace-objects" {
			t.Errorf("%s: %q does not start with the two global options (a subcommand does not take them)", name, c.args)
		}
		for _, f := range c.fence {
			if !slices.Contains(c.args, f) {
				t.Errorf("%s: %q lacks %s", name, c.args, f)
			}
		}
	}
	if got := logArgs(7, 11); !slices.Contains(got, "--skip=7") || !slices.Contains(got, "--max-count=11") || !slices.Contains(got, "--stdin") {
		t.Errorf("log: %q lacks the page and the stdin switch", got)
	}
	if n := len(details); details[n-2] != oid || details[n-1] != "--" {
		t.Errorf("show details: %q does not end with the oid and --", details)
	}
	if n := len(files); files[n-4] != parent || files[n-3] != oid || files[n-2] != "--" || files[n-1] != "." {
		t.Errorf("show files: %q does not end with the parent, the oid, -- and the workspace", files)
	}
	_, rootFiles := showArgs(oid, "")
	if n := len(rootFiles); !slices.Contains(rootFiles, "--root") || rootFiles[n-3] != oid || slices.Contains(rootFiles, parent) {
		t.Errorf("show files of a root commit: %q is not --root and the oid alone", rootFiles)
	}
	if got := strings.Join(logArgs(0, 1), " "); strings.Contains(got, oid) || strings.Contains(got, "HEAD") {
		t.Errorf("log names a commit on argv (the tips go on stdin): %q", got)
	}
}

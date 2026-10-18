package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// trashRun is one run of a re-executed `droidyard trash`.
type trashRun struct {
	stdout, stderr string
	code           int
	home           string
}

// runTrash runs `droidyard trash args…` in dir as an agent's shell would: a PATH that
// lacks droidyard, a scratch HOME (so a daemon the command started would leave its lock
// there), and DROIDYARD_STATE_DIR only when state is not "".
func runTrash(t *testing.T, dir, state string, args ...string) trashRun {
	t.Helper()
	return runTrashEnv(t, dir, state, nil, args...)
}

// runTrashEnv is runTrash with more environment variables for the re-executed binary.
func runTrashEnv(t *testing.T, dir, state string, extra []string, args ...string) trashRun {
	t.Helper()
	home := shortDir(t)
	env := append([]string{reexec + "=1", testParentEnv + "=" + strconv.Itoa(os.Getpid()), "HOME=" + home, "PATH=/nonexistent"}, extra...)
	if state != "" {
		env = append(env, "DROIDYARD_STATE_DIR="+state)
	}
	var stdout, stderr bytes.Buffer
	cmd := &exec.Cmd{Path: os.Args[0], Args: append([]string{"droidyard", "trash"}, args...), Env: env, Dir: dir, Stdout: &stdout, Stderr: &stderr}
	res := trashRun{home: home}
	if err := cmd.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running trash: %v", err)
		}
		res.code = exit.ExitCode()
	}
	res.stdout, res.stderr = stdout.String(), stderr.String()
	return res
}

// trashProject is a project root and the directory above it, so that a path outside the
// root can exist (base/outside.kt). Files are created with the content of their name.
type trashProject struct {
	base, root string
}

func newTrashProject(t *testing.T, files ...string) trashProject {
	t.Helper()
	root, _ := gradleProject(t) // root is a directory of its own under the test's temp dir
	base := filepath.Dir(root)
	p := trashProject{base: base, root: root}
	for _, f := range append(files, filepath.Join("..", "outside.kt")) {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (p trashProject) path(rel string) string { return filepath.Join(p.root, rel) }

// trashed counts the items in the daemon's trash.
func trashed(t *testing.T, state string) int {
	t.Helper()
	items, _ := filepath.Glob(filepath.Join(state, "ws", "*", "trash", "*", "item"))
	return len(items)
}

// TestTrashResolvesOperandsInTheShellsDirectory holds ADR-0062 §1: operands are resolved
// against the process's working directory, not the project root, so `trash -- A.kt` run
// from a subdirectory trashes that directory's A.kt and leaves the root's. A file named
// with a space is one operand, and each trashed operand is reported by name.
func TestTrashResolvesOperandsInTheShellsDirectory(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	proj := newTrashProject(t, "A.kt", "b c.kt", "app/src/A.kt", "app/src/b c.kt")
	sub := proj.path("app/src")

	res := runTrash(t, sub, p.state, "--", "A.kt", "b c.kt")
	if res.code != 0 || res.stdout != "trashed A.kt\ntrashed b c.kt\n" {
		t.Fatalf("trash exited %d with stdout %q, stderr %q; want 0 and one trashed line per operand", res.code, res.stdout, res.stderr)
	}
	for _, f := range []string{"app/src/A.kt", "app/src/b c.kt"} {
		if exists(proj.path(f)) {
			t.Errorf("%s is still there; the operand named it from its directory", f)
		}
	}
	for _, f := range []string{"A.kt", "b c.kt"} {
		if !exists(proj.path(f)) {
			t.Errorf("%s at the root was trashed; operands resolve against the working directory, not the root", f)
		}
	}
	if n := trashed(t, p.state); n != 2 {
		t.Errorf("the trash holds %d items; want the 2 named", n)
	}
}

// TestTrashReadsRmFlagsAndAGlobExpandedByTheShell holds ADR-0062 §1: `-rf` (combined, as
// rm takes it) is accepted and takes a directory, and a glob that /bin/sh expanded in the
// working directory arrives as the names it expanded to.
func TestTrashReadsRmFlagsAndAGlobExpandedByTheShell(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	proj := newTrashProject(t, "dir/x.kt", "app/src/g1.kt", "app/src/g2.kt", "app/src/keep.txt")

	if res := runTrash(t, proj.root, p.state, "-rf", "dir"); res.code != 0 || res.stdout != "trashed dir\n" || exists(proj.path("dir")) {
		t.Fatalf("trash -rf dir exited %d with stdout %q, stderr %q; want dir trashed", res.code, res.stdout, res.stderr)
	}

	sh := exec.Command("/bin/sh", "-c", quoteArg(os.Args[0])+" trash -- *.kt")
	sh.Dir = proj.path("app/src")
	sh.Env = []string{reexec + "=1", testParentEnv + "=" + strconv.Itoa(os.Getpid()), "HOME=" + shortDir(t), "PATH=/nonexistent", "DROIDYARD_STATE_DIR=" + p.state}
	var out, errb bytes.Buffer
	sh.Stdout, sh.Stderr = &out, &errb
	if err := sh.Run(); err != nil || out.String() != "trashed g1.kt\ntrashed g2.kt\n" {
		t.Fatalf("sh -c 'trash -- *.kt' printed %q (%v), stderr %q; want both expanded names trashed", out.String(), err, errb.String())
	}
	if exists(proj.path("app/src/g1.kt")) || exists(proj.path("app/src/g2.kt")) || !exists(proj.path("app/src/keep.txt")) {
		t.Error("the glob did not trash exactly what the shell expanded")
	}
}

// TestTrashRefusesAllOrNothing holds ADR-0062 §2: one operand that rm itself would not
// delete inside the root (build output, outside the root, the root, `.`, a missing file
// without -f, a directory without -r) refuses the whole command: the good operand beside
// it stays where it is, the exit status is 1 and stderr hands back the `command rm` line.
func TestTrashRefusesAllOrNothing(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	proj := newTrashProject(t, "ok.kt", "build/x", "dir/y.kt", "app/src/z.kt")

	for _, tc := range []struct {
		name, dir string
		bad       string
	}{
		{"build output", proj.root, "build/x"},
		{"outside the root", proj.path("app"), "../../outside.kt"},
		{"the root, absolute", proj.root, proj.root},
		{"dot", proj.root, "."},
		{"missing without -f", proj.root, "missing.kt"},
		{"directory without -r", proj.root, "dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// ok.kt is at the root; from a subdirectory it is reached by ../ok.kt.
			good := "ok.kt"
			if tc.dir != proj.root {
				good = "../ok.kt"
			}
			res := runTrash(t, tc.dir, p.state, "--", good, tc.bad)
			if res.code != 1 || res.stdout != "" {
				t.Errorf("trash exited %d with stdout %q, stderr %q; want 1 and no stdout", res.code, res.stdout, res.stderr)
			}
			if !strings.Contains(res.stderr, "To delete without droidyard's trash: command rm -- ") || !strings.Contains(res.stderr, tc.bad) {
				t.Errorf("stderr %q lacks the command rm line naming %q", res.stderr, tc.bad)
			}
			if !exists(proj.path("ok.kt")) || !exists(proj.path("build/x")) || !exists(proj.path("dir/y.kt")) || !exists(proj.path("../outside.kt")) {
				t.Error("a file was moved by a refused command; it checks every operand before moving any")
			}
			if n := trashed(t, p.state); n != 0 {
				t.Errorf("the trash holds %d items after a refusal", n)
			}
		})
	}

	// A missing operand with -f is skipped, as rm -f does, and the rest goes.
	if res := runTrash(t, proj.root, p.state, "-f", "--", "missing.kt", "ok.kt"); res.code != 0 || res.stdout != "trashed ok.kt\n" || exists(proj.path("ok.kt")) {
		t.Errorf("trash -f exited %d with stdout %q, stderr %q; want ok.kt trashed and missing.kt skipped", res.code, res.stdout, res.stderr)
	}
}

// TestTrashWithoutADaemonMovesNothingAndStartsNone holds ADR-0062 §1: trash never
// auto-starts a daemon. With nothing on the state root's socket it exits 1, hands back
// the command rm line, leaves the file, and no daemon.lock appears anywhere.
func TestTrashWithoutADaemonMovesNothingAndStartsNone(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	proj := newTrashProject(t, "a.kt")

	res := runTrash(t, proj.root, state, "--", "a.kt")
	if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "To delete without droidyard's trash: command rm -- a.kt") {
		t.Errorf("trash exited %d with stdout %q, stderr %q; want 1 and the command rm line", res.code, res.stdout, res.stderr)
	}
	if !exists(proj.path("a.kt")) {
		t.Error("the file moved with no daemon")
	}
	if locks := append(findLocks(t, state), findLocks(t, res.home)...); len(locks) != 0 {
		t.Errorf("trash started a daemon: %v", locks)
	}
}

// TestTrashStateDirFlagReachesTheDaemon holds ADR-0062 §1: --state-dir names the state
// root with DROIDYARD_STATE_DIR unset and no droidyard on PATH (an agent's shell), so a
// replacement that carries the flag reaches the daemon the hook dialled.
func TestTrashStateDirFlagReachesTheDaemon(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	proj := newTrashProject(t, "a.kt")

	res := runTrash(t, proj.root, "", "--state-dir", p.state, "--root", proj.root, "--", "a.kt")
	if res.code != 0 || res.stdout != "trashed a.kt\n" || exists(proj.path("a.kt")) {
		t.Fatalf("trash --state-dir exited %d with stdout %q, stderr %q; want a.kt trashed by that daemon", res.code, res.stdout, res.stderr)
	}
	if n := trashed(t, p.state); n != 1 {
		t.Errorf("the daemon's trash holds %d items; want 1", n)
	}
	if locks := findLocks(t, res.home); len(locks) != 0 {
		t.Errorf("trash started a daemon of its own: %v", locks)
	}
}

// TestTrashReadsFlagsAsRmDoes holds ADR-0062 §1: options end at `--` or at the first
// operand, combined flags are taken, and a usage error is reported as one.
func TestTrashReadsFlagsAsRmDoes(t *testing.T) {
	t.Parallel()
	good := []struct {
		args     []string
		r, f     bool
		operands []string
	}{
		{[]string{"-rf", "a"}, true, true, []string{"a"}},
		{[]string{"-fR", "--", "-x", "b"}, true, true, []string{"-x", "b"}},
		// A flag after the first operand is a file name, as to BSD rm.
		{[]string{"a", "-r"}, false, false, []string{"a", "-r"}},
		{[]string{"-f", "-r", "a"}, true, true, []string{"a"}},
		{[]string{"--state-dir", "/s", "--root", "/p", "-r", "--", "a"}, true, false, []string{"a"}},
	}
	for _, tc := range good {
		a, err := parseTrashArgs(tc.args)
		if err != nil || a.recursive != tc.r || a.force != tc.f || !reflect.DeepEqual(a.operands, tc.operands) {
			t.Errorf("parseTrashArgs(%q) = %+v, %v; want recursive %v force %v operands %q", tc.args, a, err, tc.r, tc.f, tc.operands)
		}
	}
	for _, bad := range [][]string{{}, {"-r"}, {"--"}, {"-x", "a"}, {"--force", "a"}, {"--root", "rel", "a"}, {"--state-dir"}} {
		if _, err := parseTrashArgs(bad); err == nil {
			t.Errorf("parseTrashArgs(%q) accepted a usage error", bad)
		}
	}
	if res := runTrash(t, t.TempDir(), shortDir(t), "-x", "a"); res.code != 2 || !strings.Contains(res.stderr, "usage: droidyard trash") {
		t.Errorf("a bad flag exited %d with stderr %q; want 2 and the usage", res.code, res.stderr)
	}
}

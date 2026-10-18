package policy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// project is a repository with a commit, and two linked worktrees of it: one beside it
// and one more, so a test can tell a worktree from its sibling.
type project struct {
	base, main, wt, sibling string
	configDir               string
}

func newProject(t *testing.T) project {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	base := platform.Current().Canonical(t.TempDir())
	p := project{base: base, main: filepath.Join(base, "pre", "main"), wt: filepath.Join(base, "out", "wt"), sibling: filepath.Join(base, "out", "sibling"), configDir: filepath.Join(base, "cfg")}
	for _, d := range []string{p.main, filepath.Dir(p.wt)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, p.main, "init", "-q")
	runGit(t, p.main, "commit", "-q", "--allow-empty", "-m", "one")
	runGit(t, p.main, "worktree", "add", "-q", "-b", "wt", p.wt)
	runGit(t, p.main, "worktree", "add", "-q", "-b", "sibling", p.sibling)
	return p
}

// config writes the user configuration trust.roots / trust.prefixes / shell.roots.
func (p project) config(t *testing.T, trustRoots, trustPrefixes, shellRoots []string) *policy.Policy {
	t.Helper()
	doc := map[string]any{"schemaVersion": 1,
		"trust": map[string]any{"roots": trustRoots, "prefixes": trustPrefixes},
		"shell": map[string]any{"roots": shellRoots}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	write(t, p.configDir, string(body))
	return policy.New(envAt(p.configDir))
}

func trusted(t *testing.T, pol *policy.Policy, root string) bool {
	t.Helper()
	got, err := pol.Trusted(root)
	if err != nil {
		t.Fatalf("Trusted(%s): %v", root, err)
	}
	return got
}

// TestWorktreeTrust pins ADR-0081 §1, §4 and §5 in the policy: a linked worktree of a
// trusted main tree is trusted, trust never flows the other way, and the answer is
// re-decided on every call. The proof of "is a worktree" is the platform's
// (TestLinkedWorktree); this asserts what the policy does with it.
func TestWorktreeTrust(t *testing.T) {
	t.Run("trust.roots names the main tree", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, []string{p.main}, nil, nil)
		// The decision: the project is trusted, so its worktrees are. Without the
		// worktree clause in Trusted both worktrees come back untrusted.
		for _, wt := range []string{p.wt, p.sibling} {
			if !trusted(t, pol, wt) {
				t.Errorf("worktree %s of a trusted main tree is not trusted", wt)
			}
		}
		// Re-decided on each call (ADR-0015 §2): once git removes the worktree it is
		// no longer one, and nothing remembers it was.
		runGit(t, p.main, "worktree", "remove", "--force", p.wt)
		if trusted(t, pol, p.wt) {
			t.Error("a removed worktree is still trusted: the answer was cached")
		}
		if !trusted(t, pol, p.sibling) {
			t.Error("removing one worktree untrusted its sibling")
		}
	})

	t.Run("trust.prefixes covers the main tree only", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, nil, []string{filepath.Join(p.base, "pre")}, nil)
		if !trusted(t, pol, p.main) {
			t.Fatal("the prefix does not cover the main tree; the fixture is wrong")
		}
		if !trusted(t, pol, p.wt) {
			t.Error("a worktree outside the prefix is not trusted, though its main tree is under it")
		}
	})

	t.Run("a worktree trusted by path trusts neither its main tree nor its siblings", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, []string{p.wt}, nil, nil)
		if !trusted(t, pol, p.wt) {
			t.Fatal("a root trusted by path is not trusted")
		}
		// The decision: trust flows from the main tree to its worktrees only
		// (ADR-0081 §1). A flow the other way makes one worktree's trust the project's.
		if trusted(t, pol, p.main) {
			t.Error("trusting a worktree trusted its main tree")
		}
		if trusted(t, pol, p.sibling) {
			t.Error("trusting a worktree trusted its sibling")
		}
	})

	t.Run("a planted directory naming a genuine worktree's data", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, []string{p.main}, nil, []string{p.main})
		data, err := os.ReadFile(filepath.Join(p.wt, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		planted := filepath.Join(p.base, "planted")
		if err := os.MkdirAll(planted, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(planted, ".git"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if trusted(t, pol, planted) {
			t.Error("a directory whose .git names a genuine worktree's data is trusted")
		}
		if ok, _ := pol.ShellAllowed(planted); ok {
			t.Error("the shell is allowed in the planted directory")
		}
	})

	t.Run("shell.roots names the main tree", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, nil, nil, []string{p.main})
		// ADR-0081 §4: the same rule decides shell.exec.
		for _, wt := range []string{p.wt, p.sibling} {
			if ok, err := pol.ShellAllowed(wt); !ok {
				t.Errorf("ShellAllowed(%s) = false (%v); want the worktree of a shell.roots project allowed", wt, err)
			}
		}
		if ok, _ := pol.Trusted(p.wt); ok {
			t.Error("shell.roots trusted the worktree: the two lists are separate")
		}

		pol = p.config(t, nil, nil, []string{p.wt})
		if ok, _ := pol.ShellAllowed(p.main); ok {
			t.Error("shell.roots naming a worktree allowed the shell in its main tree")
		}
		if ok, _ := pol.ShellAllowed(p.sibling); ok {
			t.Error("shell.roots naming a worktree allowed the shell in its sibling")
		}
		if ok, err := pol.ShellAllowed(p.wt); !ok {
			t.Errorf("ShellAllowed(the named worktree) = false (%v)", err)
		}
	})

	t.Run("the failure names the main tree", func(t *testing.T) {
		p := newProject(t)
		pol := p.config(t, nil, nil, nil)
		authorize := pol.Authorize(func(*capability.Call) (string, bool) { return p.wt, true })
		_, err := authorize(t.Context(), &capability.Call{})
		ce, ok := err.(*capability.Error)
		if !ok || ce.Code != capability.ErrWorkspaceUntrusted {
			t.Fatalf("err = %v; want WORKSPACE_UNTRUSTED", err)
		}
		// ADR-0081 §5: the project is what gets trusted, so the human is asked to
		// trust the main tree, not the worktree.
		if !strings.Contains(ce.Message, p.main) {
			t.Errorf("message %q does not name the main tree %s", ce.Message, p.main)
		}
		if want := policy.UntrustedError(p.main).NextStep; ce.NextStep != want {
			t.Errorf("nextStep = %q; want the trust command for the main tree: %q", ce.NextStep, want)
		}
		if strings.Contains(ce.NextStep, p.wt) {
			t.Errorf("nextStep %q names the worktree", ce.NextStep)
		}

		// The git domain's refusals take the same nextStep (UntrustedNextStep).
		if got, want := policy.UntrustedNextStep(p.wt), policy.UntrustedError(p.main).NextStep; got != want {
			t.Errorf("UntrustedNextStep(worktree) = %q; want %q", got, want)
		}
		if got, want := policy.UntrustedNextStep(p.base), policy.UntrustedError(p.base).NextStep; got != want {
			t.Errorf("UntrustedNextStep(plain root) = %q; want %q", got, want)
		}

		// A plain root keeps the plain failure.
		plain := pol.Authorize(func(*capability.Call) (string, bool) { return p.base, true })
		_, err = plain(t.Context(), &capability.Call{})
		if ce, ok := err.(*capability.Error); !ok || ce.NextStep != policy.UntrustedError(p.base).NextStep || strings.Contains(ce.Message, "worktree") {
			t.Errorf("a plain untrusted root: %v", err)
		}

		// Once the main tree is trusted the worktree is authorized, canonical.
		pol = p.config(t, []string{p.main}, nil, nil)
		got, err := pol.Authorize(func(*capability.Call) (string, bool) { return p.wt, true })(t.Context(), &capability.Call{})
		if err != nil || got != p.wt {
			t.Errorf("Authorize(worktree of a trusted main) = (%v, %v); want (%s, nil)", got, err, p.wt)
		}
	})
}

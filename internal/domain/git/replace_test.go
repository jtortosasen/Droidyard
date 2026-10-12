package git

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// The runner's environment carries GIT_NO_REPLACE_OBJECTS=1 and GIT_GRAFT_FILE=/dev/null
// (run.go), so a refs/replace/ ref or a .git/info/grafts file cannot make any git the
// daemon runs show other content than the repository holds. These tests are for the two
// m2l readers; the history reads are in history_harden_test.go.

// forgedTreeCommit writes a commit whose tree holds a.txt with the content FORGED, on
// top of parent (none when empty), and returns its oid.
func (e *env) forgedTreeCommit(dir, parent string) string {
	e.t.Helper()
	blob := strings.TrimSpace(e.gStdin(dir, "FORGED\n", "hash-object", "-w", "--stdin"))
	tree := strings.TrimSpace(e.gStdin(dir, "100644 blob "+blob+"\ta.txt\n", "mktree"))
	raw := "tree " + tree + "\n"
	if parent != "" {
		raw += "parent " + parent + "\n"
	}
	raw += "author FORGED <forged@example.com> 1767225600 +0000\ncommitter FORGED <forged@example.com> 1767225600 +0000\n\nFORGED\n"
	return strings.TrimSpace(e.gStdin(dir, raw, "hash-object", "-t", "commit", "-w", "--stdin"))
}

// noEnv returns the runner without the variables names.
func (r *runner) without(names ...string) *runner {
	rn := *r
	rn.env = nil
	for _, kv := range r.env {
		if !slices.Contains(names, strings.SplitN(kv, "=", 2)[0]) {
			rn.env = append(rn.env, kv)
		}
	}
	return &rn
}

// Decision (runner environment): git.read returns the bytes HEAD really holds, though a
// replace ref stands another commit, or another blob, for them. A graft changes a
// commit's parents and not its tree, so it cannot change this read: its case only shows
// the read works under one. Control: plain git is forged by each replace ref.
func TestReadIgnoresReplaceRefsAndGrafts(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *readFx, head string)
		// forged is whether plain git, asked for HEAD:a.txt, shows the forgery.
		forged bool
	}{
		{"a replace ref on HEAD", func(f *readFx, head string) {
			f.g(f.root, "update-ref", "refs/replace/"+head, f.forgedTreeCommit(f.root, ""))
		}, true},
		{"a replace ref on the blob", func(f *readFx, head string) {
			blob := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD:a.txt"))
			forged := strings.TrimSpace(f.gStdin(f.root, "FORGED\n", "hash-object", "-w", "--stdin"))
			f.g(f.root, "update-ref", "refs/replace/"+blob, forged)
		}, true},
		{"a graft", func(f *readFx, head string) {
			f.write(f.root, ".git/info/grafts", head+" "+f.forgedTreeCommit(f.root, "")+"\n")
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newReadFx(t)
			f.write(f.root, "a.txt", "the real content\n")
			f.commit(f.root)
			head := strings.TrimSpace(f.g(f.root, "rev-parse", "HEAD"))
			c.setup(f, head)

			// Control: without the variables git is forged (or, for a graft, still reads
			// the same blob).
			plain := f.plainRun(f.root, nil, "cat-file", "-p", "HEAD:a.txt")
			if got := strings.Contains(plain, "FORGED"); got != c.forged {
				t.Fatalf("control: plain git shows the forgery = %v, want %v (%q)", got, c.forged, plain)
			}
			res, err := f.read(ReadParams{Path: "a.txt"})
			if err != nil {
				t.Fatal(err)
			}
			if text(res) != "the real content\n" || res.Size != int64(len("the real content\n")) {
				t.Errorf("git.read = %q (size %d), want HEAD's real content", text(res), res.Size)
			}
		})
	}
}

// Decision (runner environment): git.watch's status is the repository's own. A replace ref
// on HEAD would otherwise make an unmodified file look changed (the replacement's tree
// stands for HEAD's); head.oid, which is the ref's value, is unaffected either way. Seen
// failing with GIT_NO_REPLACE_OBJECTS removed from the runner.
func TestStatusIgnoresReplaceRefs(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.txt", "the real content\n")
	e.commit(dir)
	head := strings.TrimSpace(e.g(dir, "rev-parse", "HEAD"))
	forged := e.forgedTreeCommit(dir, "")
	e.g(dir, "update-ref", "refs/replace/"+head, forged)

	// Control: plain git sees the unmodified file as changed.
	if out := e.plainRun(dir, nil, "status", "--porcelain"); !strings.Contains(out, "a.txt") {
		t.Fatalf("control: plain git status = %q, want a.txt changed by the replacement", out)
	}
	st, err := e.r.status(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 0 || st.Head.Oid != head {
		t.Errorf("status = %+v, want a clean tree at HEAD %s", st, head)
	}
	// The variable is what does it: without it the runner reports the forgery.
	st, err = e.r.without("GIT_NO_REPLACE_OBJECTS").status(context.Background(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) == 0 && st.Head.Oid == head {
		t.Errorf("without GIT_NO_REPLACE_OBJECTS the status is still clean at HEAD; the test cannot fail")
	}
}

// Decision (runner invocation): naming a graft file (GIT_GRAFT_FILE) makes git print a
// deprecation hint on stderr whenever it parses a commit; -c advice.graftFileDeprecated=false
// keeps stderr git's own, so a status leaves it empty and a failure's first line, which
// gitError reports, is git's "fatal:" and not the hint.
func TestGraftHintDoesNotReachStderr(t *testing.T) {
	e := needGit(t)
	dir := e.repo()
	e.write(dir, "a.txt", "one\n")
	e.commit(dir)
	if res := e.status(dir, true); len(res.Stderr) != 0 {
		t.Errorf("status stderr = %q, want empty", res.Stderr)
	}
	_, files := showArgs(strings.Repeat("1", 40), "")
	res, err := e.r.run(context.Background(), dir, false, nil, files...)
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("diff-tree of an unknown oid: err %v exit %d, want a git failure", err, res.ExitCode)
	}
	gerr := gitError(res)
	if msg := gerr.Error(); !strings.Contains(msg, ": fatal:") || strings.Contains(msg, "hint:") {
		t.Errorf("gitError = %q, want git's own fatal line", msg)
	}
}

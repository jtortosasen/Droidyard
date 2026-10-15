package git

// A write's stderr and its fetch's result (plan m2n P2, fix round): --quiet, the head and the
// tail of stderr, and the remotes `fetch --all` skips.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision: fetch --quiet leaves the per-ref lines out of stderr and keeps `fatal:`, and
// `--all --quiet` still fetches every remote that can be fetched: with 2,000 tags arriving
// from one remote and a third remote that cannot be reached, the cause of the failure is
// git's fatal line, both good remotes were fetched, and the verbose control (no --quiet)
// shows what the flag drops: a line per tag, far more than the 64 KiB kept.
func TestFetchQuietKeepsTheFatalLine(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	tip := f.e.rev(f.r.Other, "HEAD")
	var refs strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&refs, "create refs/tags/release-tag-number-%04d %s\n", i, tip)
	}
	f.e.gStdin(f.r.Other, refs.String(), "update-ref", "--stdin")
	f.e.g(f.r.Other, "push", "-q", "origin", "--tags")
	f.e.g(f.r.W, "remote", "add", "second", f.r.Origin)
	f.e.g(f.r.W, "remote", "add", "zz-bad", "/nonexistent/droidyard/x.git")

	// What the flag is for, against the verbose control, in a copy of the repository.
	quiet := f.e.inv(true).run(t, f.r.W, fetchArgs(""))
	if quiet.Code == 0 || strings.Contains(quiet.Stderr, "[new tag]") || !strings.Contains(quiet.Stderr, "fatal:") {
		t.Errorf("quiet: exit %d, per-tag lines %v, fatal %v; want a failure with the fatal line and no per-tag line",
			quiet.Code, strings.Contains(quiet.Stderr, "[new tag]"), strings.Contains(quiet.Stderr, "fatal:"))
	}
	f2 := newWfx(t)
	f2.e.gStdin(f2.r.Other, refs.String(), "update-ref", "--stdin")
	f2.e.g(f2.r.Other, "push", "-q", "origin", "--tags")
	verbose := f2.e.inv(true).run(t, f2.r.W, slices.DeleteFunc(fetchArgs(""), func(a string) bool { return a == "--quiet" }))
	if n := strings.Count(verbose.Stderr, "[new tag]"); n < 2000 || len(verbose.Stderr) < 2*stderrLimit {
		t.Errorf("control: %d per-tag lines in %d bytes of stderr; the test cannot show what --quiet saves", n, len(verbose.Stderr))
	}

	// Through the capability: git's fatal line is the cause; origin and second were fetched.
	_, err := f.call("git.fetch", "ws", FetchParams{})
	wantErrCode(t, err, CodeFailed)
	if !strings.Contains(err.Cause, "fatal:") {
		t.Errorf("cause %q lacks git's fatal line", err.Cause)
	}
	if n := strings.Count(f.e.g(f.r.W, "tag", "-l"), "release-tag-number-"); n != 2000 {
		t.Errorf("%d of 2000 tags fetched", n)
	}
	if f.e.revOr(f.r.W, "refs/remotes/second/main") == "" || f.e.revOr(f.r.W, "refs/remotes/origin/main") == "" {
		t.Errorf("--all --quiet did not fetch every remote that can be")
	}
}

// Decision: a write keeps the head and the tail of stderr, so git's error, which comes last
// after any amount of output, is the cause; and a credential failure at the end still gets
// the terminal nextStep. A cut stderr says it was cut. Seen failing with stderr kept by
// limited (head only): the fatal line was gone.
func TestWriteStderrKeepsItsEnd(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	script := f.e.script("uploadpack.sh", `i=0
while [ $i -lt 3000 ]; do echo "progress line number $i with some padding to make it long enough" >&2; i=$((i+1)); done
echo "fatal: could not read Username for 'http://x': terminal prompts disabled" >&2
exit 1`)
	f.e.g(f.r.W, "config", "remote.origin.uploadpack", script)
	_, err := f.call("git.fetch", "ws", FetchParams{Remote: "origin"})
	wantErrCode(t, err, CodeFailed)
	if !strings.Contains(err.Cause, "terminal prompts disabled") {
		t.Errorf("cause %q lost the line at the end of a long stderr", truncate(err.Cause, 200))
	}
	if !strings.Contains(err.NextStep, "terminal") {
		t.Errorf("nextStep %q is not the terminal one", err.NextStep)
	}
}

// Decision: headTail keeps the first and last bytes, a marker between them when it cut, the
// whole when it did not; whatever the size of the writes. writeCause on the cut text finds a
// late fatal line and needsCredential still matches it.
func TestHeadTail(t *testing.T) {
	t.Parallel()
	for _, chunk := range []int{1, 7, 1000, 100 << 10} {
		h := &headTail{head: 32 << 10, tail: 32 << 10}
		var all []byte
		line := []byte("0123456789 progress line\n")
		for len(all) < 200<<10 {
			all = append(all, line...)
		}
		all = append(all, "fatal: could not read Password for 'h': terminal prompts disabled\n"...)
		for i := 0; i < len(all); i += chunk {
			h.Write(all[i:min(i+chunk, len(all))])
		}
		got := h.bytes()
		if !strings.HasPrefix(string(got), string(all[:32<<10])) || !strings.HasSuffix(string(got), string(all[len(all)-32<<10:])) {
			t.Fatalf("chunk %d: the head and the tail are not the first and last 32 KiB", chunk)
		}
		if !strings.Contains(string(got), stderrCut) || len(got) > 64<<10+len(stderrCut) {
			t.Errorf("chunk %d: %d bytes, cut marker %v", chunk, len(got), strings.Contains(string(got), stderrCut))
		}
		if cause := writeCause(got); !strings.HasPrefix(cause, "fatal: could not read Password") || !needsCredential(cause) {
			t.Errorf("chunk %d: cause %q", chunk, truncate(cause, 120))
		}
		e := writeFailure("/r", result{Stderr: got, ExitCode: 128})
		if ce := capability.AsError(e); !strings.Contains(ce.NextStep, "terminal") {
			t.Errorf("chunk %d: nextStep %q", chunk, ce.NextStep)
		}
	}
	h := &headTail{head: 8, tail: 8}
	h.Write([]byte("short"))
	if string(h.bytes()) != "short" {
		t.Errorf("a stderr that fits was changed: %q", h.bytes())
	}
	h.Write([]byte("12345678901234567890"))
	if got := string(h.bytes()); got != "short123"+stderrCut+"34567890" {
		t.Errorf("cut stderr = %q", got)
	}
}

// Decision: git.fetch's result names the remotes `fetch --all` fetches: a remote with
// skipFetchAll (or the older skipDefaultUpdate) true is left out, and really not fetched; one
// asked for by name is fetched whatever its setting. Seen failing with the filter removed.
func TestFetchAllSkipsWhatGitSkips(t *testing.T) {
	t.Parallel()
	t.Log(gitVersion(t))
	f := newWfx(t)
	for _, n := range []string{"second", "third", "fourth", "dotted.name"} {
		f.e.g(f.r.W, "remote", "add", n, f.r.Origin)
	}
	f.e.g(f.r.W, "config", "remote.second.skipFetchAll", "true")
	f.e.g(f.r.W, "config", "remote.third.skipDefaultUpdate", "yes")
	f.e.g(f.r.W, "config", "remote.fourth.skipFetchAll", "false")
	f.e.g(f.r.W, "config", "remote.dotted.name.skipFetchAll", "true")

	res, err := f.call("git.fetch", "ws", FetchParams{})
	if err != nil {
		t.Fatalf("git.fetch: %+v", err)
	}
	if got := res.(FetchResult).Remotes; !slices.Equal(got, []string{"fourth", "origin"}) {
		t.Errorf("remotes = %v, want [fourth origin]", got)
	}
	for _, n := range []string{"second", "third", "dotted.name"} {
		if f.e.revOr(f.r.W, "refs/remotes/"+n+"/main") != "" {
			t.Errorf("%s was fetched by --all though it asks to be skipped", n)
		}
	}
	if f.e.revOr(f.r.W, "refs/remotes/fourth/main") == "" {
		t.Errorf("fourth, whose skipFetchAll is false, was not fetched")
	}
	res, err = f.call("git.fetch", "ws", FetchParams{Remote: "second"})
	if err != nil || !slices.Equal(res.(FetchResult).Remotes, []string{"second"}) || f.e.revOr(f.r.W, "refs/remotes/second/main") == "" {
		t.Errorf("a remote asked for by name: %v, %+v; want it fetched", res, err)
	}
}

package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
)

// The cost of the history reads on a large repository (plan m2n step 1d): a page of
// 500 commits at the start, the middle and the end of 100,000, with and without a
// commit-graph, and the refs of a repository with 40 merged branches and 100 tags.
// Run, taking the fixture once (about a minute to build; reuse one with
// DROIDYARD_BENCH_REPO=<dir>, a gittest.Synthetic(100000, 40, 100) repository):
//
//	go test ./internal/domain/git -run '^$' -bench 'LogPage|Refs' -benchtime=10x
//
// With the git on PATH; put another first to time another build. The timed loop is the
// daemon's own invocation (runner, untrusted root); the fixture and the tip set are not.

const (
	benchCommits  = 100000
	benchBranches = 40
	benchTags     = 100
	benchPage     = 500
)

var benchFixture struct {
	once sync.Once
	// dir is the repository; made is the temporary directory holding it, removed at exit.
	dir, made string
}

func TestMain(m *testing.M) {
	code := m.Run()
	if benchFixture.made != "" {
		os.RemoveAll(benchFixture.made)
	}
	os.Exit(code)
}

// benchRepo is the large repository, built once per run.
func benchRepo(b *testing.B) string {
	b.Helper()
	benchFixture.once.Do(func() {
		if dir := os.Getenv("DROIDYARD_BENCH_REPO"); dir != "" {
			benchFixture.dir = dir
			return
		}
		dir, err := os.MkdirTemp("", "droidyard-bench")
		if err != nil {
			b.Fatal(err)
		}
		benchFixture.made = dir
		benchFixture.dir = filepath.Join(dir, "big")
		gittest.Synthetic(b, benchFixture.dir, benchCommits, benchBranches, benchTags)
	})
	return benchFixture.dir
}

// setGraph writes the commit-graph, or removes it, so that each sub-benchmark runs in
// the state its name says whatever ran before it.
func setGraph(b *testing.B, e *env, dir string, on bool) {
	b.Helper()
	info := filepath.Join(dir, ".git", "objects", "info")
	os.Remove(filepath.Join(info, "commit-graph"))
	os.RemoveAll(filepath.Join(info, "commit-graphs"))
	if on {
		e.g(dir, "commit-graph", "write", "--reachable")
	}
}

func graphName(on bool) string {
	if on {
		return "graph"
	}
	return "nograph"
}

func BenchmarkLogPage(b *testing.B) {
	dir := benchRepo(b)
	e := needGit(b)
	ctx := context.Background()
	tp, _, err := tips(ctx, e.r, dir, false)
	if err != nil {
		b.Fatal(err)
	}
	stdin := []byte(strings.Join(tp, "\n") + "\n")
	for _, graph := range []bool{false, true} {
		for _, skip := range []int{0, 50000, 99500} {
			b.Run(fmt.Sprintf("%s/skip=%d", graphName(graph), skip), func(b *testing.B) {
				setGraph(b, e, dir, graph)
				args := logArgs(skip, benchPage)
				if res, err := e.r.run(ctx, dir, false, stdin, args...); err != nil || res.ExitCode != 0 {
					b.Fatalf("err %v exit %d: %s", err, res.ExitCode, res.Stderr)
				} else if n := strings.Count(string(res.Stdout), "\x00") / logFields; n != min(benchPage, benchCommits-skip) {
					b.Fatalf("%d commits in the page at skip %d", n, skip)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := e.r.run(ctx, dir, false, stdin, args...); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRefs times what git.refs asks of git (plan m2n step 5): the tip set, the
// work trees, and the current branch's ahead and behind against its upstream. The last is
// a stand-in for step 5's command (a symmetric rev-list count of two oids); the upstream
// is origin/main a tenth of the history behind, and, as the worst case, the oldest of the
// merged branches.
func BenchmarkRefs(b *testing.B) {
	dir := benchRepo(b)
	e := needGit(b)
	ctx := context.Background()
	oids := strings.Fields(e.g(dir, "rev-parse", "main", "origin/main", "refs/heads/branch/00"))
	aheadBehind := func(upstream string) []string {
		return []string{"rev-list", "--left-right", "--count", oids[0] + "..." + upstream}
	}
	for _, graph := range []bool{false, true} {
		for _, c := range []struct {
			name string
			run  func() error
		}{
			{"tips", func() error { _, _, err := tips(ctx, e.r, dir, false); return err }},
			{"worktrees", func() error { _, err := e.r.run(ctx, dir, false, nil, worktreeArgs()...); return err }},
			{"aheadBehind-origin-main", func() error { _, err := e.r.run(ctx, dir, false, nil, aheadBehind(oids[1])...); return err }},
			{"aheadBehind-oldest-branch", func() error { _, err := e.r.run(ctx, dir, false, nil, aheadBehind(oids[2])...); return err }},
			{"all", func() error {
				if _, _, err := tips(ctx, e.r, dir, false); err != nil {
					return err
				}
				if _, err := e.r.run(ctx, dir, false, nil, worktreeArgs()...); err != nil {
					return err
				}
				_, err := e.r.run(ctx, dir, false, nil, aheadBehind(oids[1])...)
				return err
			}},
		} {
			b.Run(graphName(graph)+"/"+c.name, func(b *testing.B) {
				setGraph(b, e, dir, graph)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := c.run(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

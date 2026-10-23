package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/domain/device"
)

// ErrNotRepository: root is not inside a git work tree (a bare repository included).
var ErrNotRepository = errors.New("not a git repository")

// Repo is what `droidyard init --exclude/--gitignore` needs to know about the
// repository a project root lies in (ADR-0124 Q4).
type Repo struct {
	// Top is the work tree's top, a real path.
	Top string
	// CommonDir is the repository's common directory, absolute: the main `.git` for the
	// main tree and for every linked worktree, so `info/exclude` under it is read by all
	// of them.
	CommonDir string
	// Tracked holds, among the asked paths, those the index tracks, as slash-separated
	// paths relative to Top.
	Tracked map[string]bool
}

// Tracked reads, through the runner and untrusted (the repository may name a filter
// driver, ADR-0015 §5), where root's repository keeps its common directory and which of
// paths (slash-separated, relative to root, matched literally) it tracks. It is the only
// git read of init's ignore step (ADR-0124 Q4). gitPath is the git executable ("" means
// none: ErrNotFound).
func Tracked(ctx context.Context, start device.Starter, gitPath string, environ []string, root string, paths []string) (Repo, error) {
	if !filepath.IsAbs(root) {
		return Repo{}, fmt.Errorf("root %q is not an absolute path", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Repo{}, err
	}
	r := newRunner(start, gitPath, environ)
	res, err := r.run(ctx, real, false, nil, "rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	if res.ExitCode != 0 {
		return Repo{}, fmt.Errorf("%w: %s", ErrNotRepository, firstLine(res.Stderr))
	}
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return Repo{}, fmt.Errorf("unexpected rev-parse output %q", res.Stdout)
	}
	repo := Repo{Top: lines[0], CommonDir: lines[1], Tracked: map[string]bool{}}
	if !filepath.IsAbs(repo.CommonDir) {
		repo.CommonDir = filepath.Join(real, repo.CommonDir) // relative to the cwd
	}
	if top, err := filepath.EvalSymlinks(repo.Top); err == nil {
		repo.Top = top
	}
	if len(paths) == 0 {
		return repo, nil
	}
	args := append([]string{"--literal-pathspecs", "ls-files", "-z", "--full-name", "--"}, paths...)
	res, err = r.run(ctx, real, false, nil, args...)
	if err != nil {
		return Repo{}, err
	}
	if res.ExitCode != 0 || res.Truncated {
		return Repo{}, fmt.Errorf("listing the tracked files: git exited %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	for _, p := range strings.Split(string(res.Stdout), "\x00") {
		if p != "" {
			repo.Tracked[p] = true
		}
	}
	return repo, nil
}

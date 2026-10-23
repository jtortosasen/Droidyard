package assistant

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The ignore step (ADR-0124 Q4): keeps `git status` clean of the files a per-project
// init wrote that git does not track. The lines of one run live in one marked block, so
// a rerun replaces it and --uninstall removes it without touching a line of the user's.
// Tracked paths get no line (a line has no effect on a tracked file and would say
// something false); they are reported in the Change's notes instead.
//
// This package never runs git: the caller reads the repository's facts through
// internal/domain/git (Tracked) and passes them in IgnoreOptions.

const (
	ignoreHeader = "# droidyard init: files it wrote that git does not track (droidyard init --uninstall removes this block)"
	ignoreEnd    = "# end droidyard init"
)

// NotARepository is the refusal of an install with an ignore flag outside a repository.
const NotARepository = "not a git repository: nothing to ignore"

// IgnoreTarget is the file the block goes in.
type IgnoreTarget string

const (
	// ExcludeFile is <common dir>/info/exclude (--exclude): read by the main tree and
	// by every linked worktree, so its lines are anchored at the repository's top.
	ExcludeFile IgnoreTarget = "exclude"
	// GitignoreFile is <root>/.gitignore (--gitignore): its lines are anchored at the
	// root, whatever the root's depth below the top.
	GitignoreFile IgnoreTarget = "gitignore"
)

// IgnoreOptions is one block in one file.
type IgnoreOptions struct {
	// Root is the project root, an absolute path.
	Root   string
	Target IgnoreTarget
	// Changes are the run's changes (Result.Changes); refused ones are ignored.
	Changes []Change
	// InRepo is false when root is not inside a git work tree, or git is missing. The
	// repository facts below are then unused.
	InRepo bool
	// Top is the work tree's top and CommonDir the repository's common directory (both
	// absolute); Tracked holds the paths the index tracks, relative to Top.
	Top, CommonDir string
	Tracked        map[string]bool
	DryRun         bool
	Uninstall      bool
}

// IgnoreCandidates are the paths (slash-separated, relative to the root) of the files
// the run left in the tree: what the caller asks git about before calling Ignore.
func IgnoreCandidates(changes []Change) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range changes {
		if c.Status == Refused || seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c.Path)
	}
	return out
}

// skillDirs are the directories the skill's files are ignored as; the files inside are
// init's own, so one line covers them.
func skillDir(p string) (string, bool) {
	for _, d := range []string{".claude/skills/" + SkillName, ".agents/skills/" + SkillName} {
		if strings.HasPrefix(p, d+"/") {
			return d, true
		}
	}
	return "", false
}

// Ignore computes, and unless it is a dry run applies, the block in o.Target's file.
// An error means the options were unusable; a file it will not touch is a Refused
// change instead, and an install with no repository is the NotARepository refusal.
func Ignore(o IgnoreOptions) (Change, error) {
	root, err := newFSRoot(o.Root)
	if err != nil {
		return Change{}, err
	}
	if o.Target != ExcludeFile && o.Target != GitignoreFile {
		return Change{}, fmt.Errorf("unknown ignore target %q", o.Target)
	}
	if !o.InRepo {
		c := Change{Path: ".gitignore"}
		if o.Target == ExcludeFile {
			c.Path = ".git/info/exclude"
		}
		if o.Uninstall {
			// The block part of an uninstall outside git is a no-op, never a refusal.
			c.Status, c.Notes = Unchanged, []string{"not a git repository: no block to remove"}
			return c, nil
		}
		c.Status, c.Notes = Refused, []string{NotARepository}
		return c, nil
	}

	if !filepath.IsAbs(o.Top) {
		return Change{}, errors.New("the repository's top must be an absolute path")
	}
	top, err := filepath.EvalSymlinks(o.Top)
	if err != nil {
		return Change{}, err
	}
	// Tracked is keyed by paths relative to the top: topPrefix is the root's place below it.
	topPrefix := ""
	r, err := filepath.Rel(top, root.real)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return Change{}, fmt.Errorf("root %q is not below the repository's top %q", o.Root, o.Top)
	}
	if r != "." {
		topPrefix = filepath.ToSlash(r) + "/"
	}

	var file, rel string // the real path to edit, and the Change's path
	prefix := ""         // what a line's path is prefixed with: its anchor's distance from the root
	switch o.Target {
	case ExcludeFile:
		if !filepath.IsAbs(o.CommonDir) {
			return Change{}, errors.New("the repository's common directory must be an absolute path")
		}
		file = filepath.Join(o.CommonDir, "info", "exclude")
		rel = filepath.ToSlash(file)
		prefix = topPrefix // anchored at the top, which the root may lie below
	case GitignoreFile:
		rel = ".gitignore"
		file, err = root.resolve(rel)
		if err != nil {
			return Change{Path: rel, Status: Refused, Notes: []string{err.Error()}}, nil
		}
	}
	c := Change{Path: rel}
	fail := func(format string, a ...any) (Change, error) {
		c.Status, c.Notes = Refused, []string{fmt.Sprintf(format, a...)}
		return c, nil
	}

	if o.Target == ExcludeFile {
		if msg := excludeConfined(o.CommonDir); msg != "" {
			return fail("%s; left as it is", msg)
		}
	}
	old, exists, err := readFile(file)
	if err != nil {
		return fail("%v", err)
	}
	text := string(old)
	start, stop, have, found, err := findBlock(text)
	if err != nil {
		return fail("%v; left as it is", err)
	}

	// The block is shared by every client, root and worktree that wrote into this file
	// (ADR-0126): an install adds to it, an uninstall takes out only what it removed.
	var lines []string
	if o.Uninstall {
		drop := removedLines(o, root, prefix)
		if o.Target == ExcludeFile {
			dropIfElsewhere(drop, o)
		}
		for _, l := range have {
			if !drop[l] {
				lines = append(lines, l)
			}
		}
	} else {
		add, tracked := ignoreLines(o, prefix, topPrefix)
		for _, p := range tracked {
			c.Notes = append(c.Notes, "tracked by git, so not ignored: "+p)
		}
		lines = unionLines(have, add)
	}
	content := splice(text, start, stop, found, lines)

	switch {
	case !found && len(lines) == 0:
		c.Status = Unchanged
		c.Notes = append(c.Notes, "nothing to ignore")
		if o.Uninstall {
			c.Notes = []string{"no block of init's there"}
		}
		return c, nil
	case found && content == text:
		c.Status = Unchanged
		c.Notes = append(c.Notes, "already there")
		if o.Uninstall {
			c.Notes = []string{"no line of this run's is in the block"}
		}
		return c, nil
	}
	switch {
	case content == "":
		c.Status = Deleted
	case !exists:
		c.Status = Created
	case o.Uninstall && len(lines) == 0:
		c.Status = Removed
	default:
		c.Status = Updated
	}
	if o.DryRun {
		return c, nil
	}
	var werr error
	if c.Status == Deleted {
		werr = os.Remove(file)
	} else {
		werr = writeFile(file, []byte(content))
	}
	if werr != nil {
		return fail("cannot write: %v", werr)
	}
	return c, nil
}

// excludeConfined is "" when <commonDir>/info is inside commonDir once symlinks are
// resolved, else why init will not write there: a symlinked info directory must not make
// init edit a file outside the common directory (ADR-0126). Which common directory that is
// comes from git and is trusted as git reports it.
func excludeConfined(commonDir string) string {
	real, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "the repository's common directory cannot be resolved: " + err.Error()
	}
	info, err := filepath.EvalSymlinks(filepath.Join(commonDir, "info"))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return "info cannot be resolved: " + err.Error()
	}
	if rel, err := filepath.Rel(real, info); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "the repository's info directory resolves outside its common directory (" + info + ")"
	}
	return ""
}

// lineOf is the block line for a path of the run, relative to the root.
func lineOf(p, prefix string) string {
	if d, ok := skillDir(p); ok {
		p = d + "/"
	}
	return "/" + prefix + escapeIgnore(p)
}

// removedLines are the lines for the paths this uninstall removed, collapsed as on
// install. A line stays when a file that maps to it is still there (a skill file the
// user changed).
func removedLines(o IgnoreOptions, root *fsRoot, prefix string) map[string]bool {
	drop := map[string]bool{}
	keep := map[string]bool{}
	for _, c := range o.Changes {
		l := lineOf(c.Path, prefix)
		if c.Status == Deleted || c.Status == Removed {
			drop[l] = true
			continue
		}
		if real, err := root.resolve(c.Path); err == nil {
			if _, err := os.Lstat(real); err == nil {
				keep[l] = true
			}
		}
	}
	for l := range keep {
		delete(drop, l)
	}
	return drop
}

// dropIfElsewhere spares, in the shared exclude file, the lines of paths that still exist
// in another work tree of the repository: the line is anchored at each tree's top, so it
// hides that tree's copy as well (ADR-0126).
func dropIfElsewhere(drop map[string]bool, o IgnoreOptions) {
	var trees []string
	if filepath.Base(o.CommonDir) == ".git" {
		trees = append(trees, filepath.Dir(o.CommonDir))
	}
	gitdirs, _ := filepath.Glob(filepath.Join(o.CommonDir, "worktrees", "*", "gitdir"))
	for _, g := range gitdirs {
		// A regular file, bounded: a crafted gitdir must not hang or flood the uninstall.
		if b, ok, err := readFile(g); err == nil && ok {
			trees = append(trees, filepath.Dir(strings.TrimSpace(string(b))))
		}
	}
	top, _ := filepath.EvalSymlinks(o.Top)
	for l := range drop {
		rel := strings.ReplaceAll(strings.TrimPrefix(l, "/"), `\`, "")
		for _, t := range trees {
			if rt, err := filepath.EvalSymlinks(t); err != nil || rt == top {
				continue
			}
			if _, err := os.Lstat(filepath.Join(t, filepath.FromSlash(rel))); err == nil {
				delete(drop, l)
				break
			}
		}
	}
}

// unionLines is a and b, sorted and without repeats.
func unionLines(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string(nil), a...), b...) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// ignoreLines are the block lines for the untracked files of the run, anchored with a
// leading slash (and prefix, for the exclude file), and the tracked paths that got none.
func ignoreLines(o IgnoreOptions, prefix, topPrefix string) (lines, tracked []string) {
	want := map[string]bool{}
	for _, p := range IgnoreCandidates(o.Changes) {
		if o.Tracked[topPrefix+p] {
			tracked = append(tracked, p)
			continue
		}
		if strings.ContainsAny(p, "\n\r") {
			continue // a pattern cannot say it; init writes no such name
		}
		want[lineOf(p, prefix)] = true
	}
	for l := range want {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	sort.Strings(tracked)
	return lines, tracked
}

// escapeIgnore escapes what a gitignore pattern would read as magic in a literal name.
func escapeIgnore(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`\*?[`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	s := b.String()
	if trimmed := strings.TrimRight(s, " "); len(trimmed) != len(s) {
		s = trimmed + strings.Repeat(`\ `, len(s)-len(trimmed))
	}
	return s
}

// findBlock locates init's block in text: [start, stop) spans its header to the line after
// its end, and have are the lines between. A header with no end line is an error: where
// the user's own lines resume is unknown.
func findBlock(text string) (start, stop int, have []string, found bool, err error) {
	start = -1
	for pos := 0; pos < len(text); {
		line, next := nextLine(text, pos)
		if line == ignoreHeader {
			start = pos
			break
		}
		pos = next
	}
	if start < 0 {
		return 0, 0, nil, false, nil
	}
	_, pos := nextLine(text, start)
	for pos < len(text) {
		line, next := nextLine(text, pos)
		if line == ignoreEnd {
			return start, next, have, true, nil
		}
		have = append(have, line)
		pos = next
	}
	return 0, 0, nil, true, errors.New("the block's end line is missing")
}

func nextLine(text string, pos int) (line string, next int) {
	if i := strings.IndexByte(text[pos:], '\n'); i >= 0 {
		return text[pos : pos+i], pos + i + 1
	}
	return text[pos:], len(text)
}

// splice puts a block of lines in place of the one at [start, stop), appends it when
// none was found, and drops it when lines is empty.
func splice(text string, start, stop int, found bool, lines []string) string {
	var block string
	if len(lines) > 0 {
		block = ignoreHeader + "\n" + strings.Join(lines, "\n") + "\n" + ignoreEnd + "\n"
	}
	if found {
		return text[:start] + block + text[stop:]
	}
	if block == "" {
		return text
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + block
}

package assistant

import (
	"bytes"
	"errors"
	"fmt"
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

	old, exists, err := readFile(file)
	if err != nil {
		return fail("%v", err)
	}
	var lines []string
	if !o.Uninstall {
		var tracked []string
		lines, tracked = ignoreLines(o, prefix, topPrefix)
		for _, p := range tracked {
			c.Notes = append(c.Notes, "tracked by git, so not ignored: "+p)
		}
	}
	content, found, err := withBlock(old, lines)
	if err != nil {
		return fail("%v; left as it is", err)
	}

	switch {
	case o.Uninstall && !found:
		c.Status = Unchanged
		c.Notes = append(c.Notes, "no block of init's there")
		return c, nil
	case !o.Uninstall && len(lines) == 0 && !found:
		c.Status = Unchanged
		c.Notes = append(c.Notes, "nothing to ignore")
		return c, nil
	case !o.Uninstall && len(lines) > 0 && exists && bytes.Equal(old, content):
		c.Status = Unchanged
		c.Notes = append(c.Notes, "already there")
		return c, nil
	}
	switch {
	case len(content) == 0 && found:
		c.Status = Deleted
	case found && len(lines) == 0:
		c.Status = Removed
	case !exists:
		c.Status = Created
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
		werr = writeFile(file, content)
	}
	if werr != nil {
		return fail("cannot write: %v", werr)
	}
	return c, nil
}

// ignoreLines are the block's lines for the untracked files of the run, anchored with a
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
		if d, ok := skillDir(p); ok {
			p = d + "/"
		}
		want[p] = true
	}
	for p := range want {
		lines = append(lines, "/"+prefix+escapeIgnore(p))
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

// withBlock returns data with init's block replaced by one holding lines, appended when
// there was none, or removed when lines is empty. found says whether a block was there.
// A header with no end line is an error: where the user's own lines resume is unknown.
func withBlock(data []byte, lines []string) (out []byte, found bool, err error) {
	text := string(data)
	var block string
	if len(lines) > 0 {
		block = ignoreHeader + "\n" + strings.Join(lines, "\n") + "\n" + ignoreEnd + "\n"
	}
	start := -1
	pos := 0
	for pos < len(text) {
		end := strings.IndexByte(text[pos:], '\n')
		next := len(text)
		line := text[pos:]
		if end >= 0 {
			line, next = text[pos:pos+end], pos+end+1
		}
		if line == ignoreHeader {
			start = pos
			break
		}
		pos = next
	}
	if start < 0 {
		if block == "" {
			return data, false, nil
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + block), false, nil
	}
	rest := text[start:]
	var stop int
	for off := 0; ; {
		end := strings.IndexByte(rest[off:], '\n')
		next := len(rest)
		line := rest[off:]
		if end >= 0 {
			line, next = rest[off:off+end], off+end+1
		}
		if line == ignoreEnd {
			stop = next
			break
		}
		if end < 0 {
			return nil, true, errors.New("the block's end line is missing")
		}
		off = next
	}
	return []byte(text[:start] + block + rest[stop:]), true, nil
}

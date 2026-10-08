package assistant

import (
	"bytes"
	"fmt"
)

// The marked block in AGENTS.md or CLAUDE.md (ADR-0037 Decision 2): at most five lines
// with the marks, the same text in both files so a symlink between them stays stable.
const (
	beginMark = "<!-- droidyard:begin -->"
	endMark   = "<!-- droidyard:end -->"
)

const blockBody = "droidyard's MCP tools are set up for this project: use them rather than adb, Gradle or\n" +
	"uiautomator in a shell for what they cover (building, running and driving the Android app).\n" +
	"Load the `droidyard-android` skill for how.\n"

// Block is the text init puts between the marks, marks included, ending in a newline.
func Block() string { return beginMark + "\n" + blockBody + endMark + "\n" }

// findBlock locates the marked region: from the begin mark to the end mark, plus the
// newline that closes it. found is false when there are no marks; err when they are not
// one begin followed by one end, which init will not guess about.
func findBlock(old []byte) (start, end int, found bool, err error) {
	nb, ne := bytes.Count(old, []byte(beginMark)), bytes.Count(old, []byte(endMark))
	if nb == 0 && ne == 0 {
		return 0, 0, false, nil
	}
	start, stop := bytes.Index(old, []byte(beginMark)), bytes.Index(old, []byte(endMark))
	if nb != 1 || ne != 1 || start < 0 || stop < start {
		return 0, 0, false, fmt.Errorf("its droidyard marks are not one %s followed by one %s", beginMark, endMark)
	}
	end = stop + len(endMark)
	if end < len(old) && old[end] == '\n' {
		end++
	}
	return start, end, true, nil
}

// installBlock writes the block: whole into a new file, appended after the user's text
// (a newline ends it first, and a blank line separates it), or, if the marks are already
// there, replacing what is between them. Text outside the marks is never touched. The
// marked region is init's own, so an out-of-date one is refreshed, and said so.
func installBlock(old []byte, exists bool) edit {
	if !exists {
		return edit{status: Created, content: []byte(Block())}
	}
	start, end, found, err := findBlock(old)
	if err != nil {
		return refuse(err.Error()+"; fix or remove them and run init again", Block())
	}
	if found {
		if string(old[start:end]) == Block() || string(old[start:end]) == Block()[:len(Block())-1] {
			return edit{status: Unchanged, notes: []string{"block already there"}}
		}
		out := append(append(append([]byte(nil), old[:start]...), Block()...), old[end:]...)
		return edit{status: Updated, content: out, notes: []string{"block between the marks refreshed"}}
	}
	out := append([]byte(nil), old...)
	if len(out) > 0 {
		// One newline: it ends a last line that had none, or leaves a blank line after
		// one that had. uninstallBlock takes exactly this newline back.
		out = append(out, '\n')
	}
	out = append(out, Block()...)
	return edit{status: Updated, content: out, notes: []string{"block appended"}}
}

// uninstallBlock removes the marked block when it is equal to what init writes now. At
// the end of the file it also takes the one newline init added before it, so the user's
// text comes back byte for byte; a file left empty held only the block and is deleted.
func uninstallBlock(old []byte, exists bool) edit {
	if !exists {
		return edit{status: Unchanged, notes: []string{"not there"}}
	}
	start, end, found, err := findBlock(old)
	if err != nil {
		return refuse(err.Error()+"; remove the block by hand", "")
	}
	if !found {
		return edit{status: Unchanged, notes: []string{"no block of init's"}}
	}
	if got := string(old[start:end]); got != Block() && got+"\n" != Block() {
		return edit{status: Unchanged, notes: []string{"block differs from what init writes now: kept, not removed"}}
	}
	before := old[:start]
	if end == len(old) && len(before) > 0 && before[len(before)-1] == '\n' {
		before = before[:len(before)-1]
	}
	out := append(append([]byte(nil), before...), old[end:]...)
	if len(out) == 0 {
		return edit{status: Deleted}
	}
	return edit{status: Removed, content: out, notes: []string{"block removed"}}
}

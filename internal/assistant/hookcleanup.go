package assistant

import (
	"errors"
	"io/fs"
	"os"
)

// Removing the hook entries an earlier init wrote (ADR-0137, plan m3j Q4; ADR-0037's
// uninstall rules): init and init --user write no hook any more, and on every run, install
// or uninstall, take out the elements of init's own shape (hookentry.go) and say so in the
// change's notes. A user's own entry, a wrapper around the hook, another root or client,
// stays.

// claudeHookCleanup is what init removes from .claude/settings.json: the elements of
// init's shape for this root under the three tool events. root is "" at user level.
func claudeHookCleanup(root string) jsonSpec {
	var arrays []arrayEntry
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		arrays = append(arrays, arrayEntry{path: []string{"hooks"}, key: event, stale: claudeStale(root)})
	}
	return jsonSpec{arrays: arrays}
}

// cursorHookCleanup is the same for .cursor/hooks.json.
func cursorHookCleanup(root string) jsonSpec {
	hooks := []string{"hooks"}
	return jsonSpec{arrays: []arrayEntry{
		{path: hooks, key: "beforeShellExecution", stale: cursorStale(root, "")},
		{path: hooks, key: "preToolUse", stale: cursorStale(root, "Delete", cursorEditMatcher)},
		{path: hooks, key: "postToolUse", stale: cursorStale(root, "Shell", cursorEditMatcher)},
		{path: hooks, key: "postToolUseFailure", stale: cursorStale(root, "Shell")},
	}}
}

// cursorHooksFile is the job for .cursor/hooks.json, a file init once owned: install and
// uninstall alike remove init's entries and write nothing, and a file left holding only
// the `version` init wrote is deleted. A file without an entry of init's is not touched.
func cursorHooksFile(rel, root string) job {
	version := jsonSpec{keys: []keyEntry{{key: "version", value: rawJSON(1), sole: true}}}
	spec := cursorHookCleanup(root)
	clean := func(old []byte, exists bool) edit {
		e := uninstallJSON(old, exists, spec)
		if e.status != Removed {
			return e
		}
		if v := uninstallJSON(e.content, true, version); v.status == Deleted {
			return edit{status: Deleted, notes: e.notes}
		}
		return e
	}
	return job{rel: rel, install: clean, uninstall: clean}
}

// absent reports whether rel is plainly not there. init has nothing to say about a file
// that was never written (and no entry to add to the ignore block for it), so a cleanup
// job on one is left out of the run; a path that cannot be told is not absent, and the job
// refuses it as any other.
func (r *fsRoot) absent(rel string) bool {
	real, err := r.resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Lstat(real)
	return errors.Is(err, fs.ErrNotExist)
}

// withHookCleanup adds to a job on a file that also holds init's other entries the
// removal of its hook entries (spec), after the job's own change. A file that does not
// exist, or that the job refuses, has none to remove.
func withHookCleanup(j job, spec jsonSpec) job {
	then := func(base func([]byte, bool) edit, uninstall bool) func([]byte, bool) edit {
		return func(old []byte, exists bool) edit {
			e := base(old, exists)
			if !exists || e.status == Refused || e.status == Deleted {
				return e
			}
			cur := e.content
			if cur == nil {
				cur = old
			}
			c := uninstallJSON(cur, true, spec)
			switch c.status {
			case Refused:
				return c
			case Deleted:
				return edit{status: Deleted, notes: append(append([]string(nil), e.notes...), c.notes...)}
			case Removed:
				// The job found nothing of its own when it is Unchanged; the hooks are the change.
				status, notes := e.status, e.notes
				if status == Unchanged {
					status, notes = Updated, nil
					if uninstall {
						status = Removed
					}
				}
				return edit{status: status, content: c.content, notes: append(append([]string(nil), notes...), c.notes...)}
			}
			return e
		}
	}
	return job{rel: j.rel, install: then(j.install, false), uninstall: then(j.uninstall, true)}
}

package sdk

import "context"

// InUseFunc is the seam through which sdk.install and sdk.uninstall ask whether a package
// they would replace or remove is in use (ADR-0129 §8; ADR-0131 §1, §2). It receives the
// SDK root and the package ids and answers, for each id in use, what uses it (a process,
// a build, a boot) in words a person can read. An id absent from the answer is free. Any
// doubt counts as in use: the detector folds its doubts into the answer, so a package it
// could not judge is named here, never silently free.
//
// Plan m3f step 5 supplies the real detector. Until it does, Options.InUse is nil and
// notChecked answers: every package is "in use: not checked yet", so an update and an
// uninstall are refused and a fresh install, which replaces nothing, runs.
type InUseFunc func(ctx context.Context, root string, ids []string) map[string][]string

// notChecked is the answer until step 5: nothing can be called free that nobody checked.
func notChecked(_ context.Context, _ string, ids []string) map[string][]string {
	out := make(map[string][]string, len(ids))
	for _, id := range ids {
		out[id] = []string{"in use: not checked yet"}
	}
	return out
}

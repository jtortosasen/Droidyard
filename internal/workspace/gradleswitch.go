package workspace

// UseGradleRoot records that a call of the workspace target names resolved the Gradle
// root abs, canonical, or none ("") when the resolution gave no root, and makes it the
// workspace's one current root (ADR-0092 §1). It replaces NoteGradleRoot's growing set:
// a root the workspace forgets is no longer one whose crash degrades it (§3) or whose
// connection its close releases. The refusal is NoteGradleRoot's: a workspace that is
// not open fails WORKSPACE_NOT_OPEN and records nothing.
//
// It returns, sorted, the forgotten roots that no other open workspace remembers: the
// connections to close, once no operation holds them (§2). The caller closes them; this
// package holds no Gradle client.
func (r *Registry) UseGradleRoot(target, abs string) (forgotten []string, err error) {
	id, _ := r.resolve(target)
	w := r.lookup(id)
	if w == nil {
		return nil, notOpen(id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !w.info.State.open() {
		return nil, notOpen(id)
	}
	dropped := map[string]struct{}{}
	for g := range w.gradleRoots {
		if g != abs {
			dropped[g] = struct{}{}
		}
	}
	switch {
	case abs == "":
		w.gradleRoots = nil
	case len(w.gradleRoots) != 1 || len(dropped) != 0:
		w.gradleRoots = map[string]struct{}{abs: {}}
	}
	// r.mu is held, as releasableRoots requires. It skips w, so the workspace's own new
	// root cannot keep a dropped one alive.
	return r.releasableRoots(w, dropped), nil
}

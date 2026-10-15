package gradle

import (
	"path"
	"path/filepath"
)

// Roots is what a Gradle capability's Authorize hook hands its handler as
// Call.Authorized (ADR-0075 §1): the project root, the workspace's root where trust is
// decided and every published path is relative to, and the Gradle root inside it, where
// the sidecar connects and the build runs. Both are canonical; they are equal in the
// common case.
type Roots struct {
	Project, Gradle string
}

// ProjectRel makes dir, a path relative to the Gradle root as the model reports it, a
// path relative to the project root (ADR-0075 §5, ADR-0022): `/`-separated and cleaned,
// "." for the project root itself. A directory outside the Gradle root (`../lib`) comes
// out relative to the project root, and one outside the project root keeps its `..`.
func (r Roots) ProjectRel(dir string) string {
	rel, err := filepath.Rel(r.Project, r.Gradle)
	if err != nil {
		rel = "."
	}
	return path.Clean(path.Join(filepath.ToSlash(rel), filepath.ToSlash(dir)))
}

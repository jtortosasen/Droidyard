// Package doccheck holds tests, and nothing else, for the invariants of the
// repository that are exact enough to check mechanically: capability names that are
// written in more than one place, the ADR index against the ADR files, the ADR
// format, relative links and the section references that follow them (a file named
// by link, path, bare name or ADR number; a cited .md that does not exist is a
// finding), the mechanical rules of the milestone plan template, the shape and order
// of the register of open items, the size of the live documentation, the facts that
// documents must repeat (error codes against the constants, no restated counts or API
// version) and the ADR policy (supersession pointers both ways, no new Amended lines).
//
// Frozen files (isFrozen: a plan with Status Done, docs/research/, docs/reviews/,
// the ADRs) cite documents as they were, so their section references and #fragment
// links are not checked; their plain file links still are.
//
// `go test ./...` is the only gate the project has (docs/research/pstack.md §3), so a
// check becomes structural only if it lives here. Agreement that needs judgement,
// such as whether a description matches a behaviour, stays with the verifier.
package doccheck

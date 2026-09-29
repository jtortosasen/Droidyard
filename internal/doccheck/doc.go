// Package doccheck holds tests, and nothing else, for the invariants of the
// repository that are exact enough to check mechanically: capability names that are
// written in more than one place, the ADR index against the ADR files, the ADR
// format, relative links and the section references that follow them, and the
// mechanical rules of the milestone plan template.
//
// `go test ./...` is the only gate the project has (docs/research/pstack.md §3), so a
// check becomes structural only if it lives here. Agreement that needs judgement,
// such as whether a description matches a behaviour, stays with the verifier.
package doccheck

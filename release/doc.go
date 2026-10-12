// Package release holds the release script and its tests, and nothing the binary
// imports (ADR-0068). Its tests read .github/workflows/*.yml as text, so the rules a
// workflow must keep (actions pinned by SHA, Linux runners, the hook's checks, no
// credentials left in the checkout, no write permission in CI) fail in a commit, not
// at the first run on GitHub.
package release

# ADR-0013: OCR is optional and pluggable

- Status: Accepted
- Date: 2026-09-27

## Context

Both `dsh-android` and `codex-android-mcp` ship OCR by compiling a Swift/Apple
Vision helper **at runtime** on first use, macOS-only, with a multi-minute compile
timeout on failure. For a cross-platform runtime this is a hard portability wall,
and OCR is a secondary path (the UI tree is primary).

## Decision

OCR is an **optional, pluggable backend** behind a capability:

- The core never compiles a helper at runtime.
- OCR is advertised by `system.capabilities` **only when a backend is available**.
- Its absence degrades gracefully; the UI tree remains the primary inspection path.
- Backend options (portable engine, or a separately distributed user-provided
  tool) are an implementation detail, chosen later.
- OCR is **not a v1 requirement**.

## Consequences

- Cross-platform parity is preserved.
- Consumers must detect OCR availability before relying on it.
- We keep the seam (capability + backend interface) without committing to an engine
  now.

## Alternatives considered

- **Built-in macOS-only OCR** — portability wall. Rejected.
- **No OCR at all** — loses a useful fallback for WebView/Flutter/canvas surfaces
  that expose no accessibility info. Keep the seam instead.

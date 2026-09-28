# ADR-0006: Web UI served by the core; no Electron

- Status: Accepted
- Date: 2026-09-27

## Context

The UI must work on a desktop and from a tablet/mobile browser on the same
network. Electron would ship a bundled Chromium and a desktop-only deployment
model, defeating both the lean goal and the browser-on-tablet requirement.

The frontend framework is decided separately in
[ADR-0008](0008-frontend-react.md); the editor component in
[ADR-0009](0009-editor-component.md).

## Decision

- The UI is a **web application served by the core** over HTTP/WebSocket.
- The core serves the built static assets.
- Do **not** use Electron. The user opens a browser (desktop or tablet).
- No bundled browser engine.

## Consequences

- The core serves static assets and streams events; there is no desktop shell.
- The same UI works on any device with a browser on the same network.
- Requires a build step for the frontend assets (see
  [ADR-0008](0008-frontend-react.md)).
- Requires care with authentication/exposure if the UI is reachable on a LAN
  (tracked as an open decision).
- UI features (diagnostics, go-to-definition) are driven through the capability
  API, not through a browser-native LSP client.

## Alternatives considered

- **Electron desktop app** — heavy and desktop-only. Rejected.
- **Native app** — contradicts the browser/tablet requirement.
- **Tauri/other webview shell** — still a desktop-only shell; unnecessary when a
  browser is already the target.

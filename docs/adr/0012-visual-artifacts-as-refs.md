# ADR-0012: Artifacts as opaque references, in two durability classes

- Status: Accepted
- Date: 2026-09-27
- Amended: 2026-09-29. What changed: the durability classes, reading artifacts
  (`artifact.read`), and image delivery to agents (review B11). The original decision
  covered visual artifacts in memory only.
- Amended: 2026-10-04 — spill to the cache directory is deferred: until a producer
  routinely exceeds 4 MiB (`device.logs` captures, `shell.exec` output), ephemeral
  artifacts stay in memory under the byte budget; the cache location under an explicit
  `DROIDYARD_STATE_DIR` is decided with that producer, as an ADR-0021 amendment
  (owner decision, milestone 1e, 2026-10-04, Q4).
- Amended: 2026-10-04 — `ARTIFACT_EXPIRED`'s `nextStep` names the producing capability
  when the store still holds a record of it; for an evicted reference or one from an
  earlier epoch it says to re-run the capability that produced it, without naming it
  (no tombstones are kept; delegated decision, milestone 1e, step 7).

## Context

`codex-android-mcp` writes each screenshot to a temp file, base64-encodes it, then
deletes it, leaking the filesystem `path` into the text content for the user.
`dsh-android` reaches a similar end with two duplicate screenshot stores plus a route
counter sharing one directory. Temp directories can be reaped by the OS, and paths
leak host internals.

Since then, references are no longer only visual. The following are all references:

- a logcat capture;
- `shell.exec`'s full output;
- the build console ([gradle](../subsystems/gradle.md) §Output), which is persisted
  and referenced from a `lastResult` that survives restarts
  ([ADR-0021](0021-persistence-format-and-location.md)).

An in-memory store with a TTL cannot keep that promise. And no capability let an agent
read what it was handed: MCP resources are post-MVP, so a screenshot reference was a
dead end for the agent on the golden path.

## Decision

The core hands out **opaque, URI-shaped references** (`droidyard://artifact/<id>`), never
filesystem paths. A reference comes with metadata:

```text
ArtifactRef { ref, mediaType, bytes, durability: ephemeral | durable,
              expiresAt?, width?, height?, untrusted }
```

- One component in the core owns the artifact store (`internal/artifact`).
- Identifiers are URI-shaped so that they can become MCP resources later without a
  redesign.
- Consumers never parse a reference. Its internal form encodes the durability class,
  the daemon epoch or workspace, and a random id.

### Durability classes

| | **Ephemeral** | **Durable** |
| --- | --- | --- |
| Examples | screenshots, logcat captures, `shell.exec` output | build consoles |
| Storage | memory; large ones spill to the derived cache directory ([ADR-0021](0021-persistence-format-and-location.md)), which is wiped at daemon start | `<state>/ws/<wsid>/artifacts/` |
| Lifetime | 15 min after creation, the same as operation retention ([ADR-0020](0020-operation-contract.md)); LRU eviction under a byte budget (working default 64 MiB; spill above 4 MiB) | the **owning record's** retention: a console lives and dies with its build record |
| Survives a daemon restart | no | yes |

- The **producing capability** fixes the class. The consumer does not choose it, and
  in the MVP it cannot promote an ephemeral artifact to durable.
- Every durable artifact has an owner record. At startup, files without an owner are
  deleted.
- A durable artifact is written incrementally and is readable while it grows. Its
  metadata says `growing` until the producer closes it.
- The producer also sets a per-artifact size cap.
- An expired reference is distinguishable from one that never existed:
  `ARTIFACT_EXPIRED` (evicted, or issued by an earlier daemon epoch) versus
  `ARTIFACT_NOT_FOUND`. The `nextStep` of `ARTIFACT_EXPIRED` says to re-run the capability
  that produced it, and names it when the store still knows it (2026-10-04).

### Reading artifacts

One capability serves every artifact type:

```text
artifact.read { ref,
                offset?, limit?,      text: byte offset (negative = from the end), max bytes
                maxEdge?, region? }   image: longest edge in px; crop, normalised 0..1
```

- It is global, a query and stateless.
- **Text**:
  - Returns `{offset, nextOffset, size, growing, text, truncated}`.
  - A read that starts mid-line (after a negative offset) is moved forward to the next
    line start.
  - A read ends at the last complete line within `limit`. The default `limit` is
    16 KiB and the maximum 256 KiB.
  - A single line longer than `limit` is cut at a UTF-8 boundary, with
    `truncated: true`.
  - An offset past the end returns empty text. Empty is a fact.
- **Images**: returns the stored dimensions, the delivered dimensions and the bytes.
  Adapters that deliver bytes out of band (MCP image content, the HTTP artifact route)
  omit the bytes from the structured result.
- Its result carries the artifact's `untrusted` mark
  ([ADR-0015](0015-untrusted-data-authorization.md)).

Adapters resolve references to bytes; the core owns the store:

- **MCP** inlines a bounded, downscaled image next to the reference whenever a result
  carries an image artifact. Agents read everything else through `artifact_read`
  ([mcp-tool-surface](../interfaces/mcp-tool-surface.md) §Images and artifacts).
- **HTTP** serves the same operation as raw bytes at `GET /v1/artifacts/{id}`, with
  `Range` for text
  ([http-websocket](../interfaces/http-websocket.md) §`GET /v1/artifacts/{id}`).

## Consequences

- No temp-dir reaping failures and no host-path leakage.
- Every reference an agent receives is readable in the MVP without MCP resources.
- The core stays bounded: ephemeral artifacts have a byte budget. Durable ones are
  bounded by the retention of their owners and by producer caps.
- `lastResult.consoleRef` stays valid across restarts, for exactly as long as the
  build record it belongs to.
- A reference from an earlier daemon fails with a typed error that says what to re-run.
- When MCP resources arrive, `resources/read` on a `droidyard://artifact/…` URI is a
  second projection of `artifact.read`, not a new store.

## Alternatives considered

- **Path-based plumbing.** Leaks host internals and is fragile. Rejected.
- **Host-specific hosting, such as a webserver route as the primary delivery.** It
  couples the artifact to one adapter. Rejected. The HTTP route is a projection of
  `artifact.read`, not the store.
- **One class: in memory with a TTL (the original decision).** It cannot back a
  `lastResult` that survives restarts. Rejected.
- **One class: everything on disk.** Pointless I/O for screenshots that are read once,
  and it turns a TTL cache into a disk-retention problem. Rejected.
- **A capability per artifact type (`build.console`, `device.logCapture`, …).** Each
  producer would need a sibling reader, all with the same paging semantics. Rejected
  in favour of one `artifact.read`.
- **MCP resources as the MVP read path.** Client support is uneven, and many clients
  expose resources to the user, not to the model. Deferred: they map onto the same
  capability later ([mcp-features](../interfaces/mcp-features.md) §2).

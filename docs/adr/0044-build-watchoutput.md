# ADR-0044: `build.watchOutput`, the build console while a build runs

- Status: Accepted
- Date: 2026-10-10
- Relates to: ADR-0010, ADR-0015 §1, ADR-0016, ADR-0041 §4, ADR-0043 §3;
  `capability-api.md` §Events and subscriptions; `gradle.md` §Output; plan m2c Q1
- Decided by the owner, 2026-10-10: the build console is shown live, line by line, as
  Logcat is. The shape below is Delegated (plan m2c Q1, reviewed in two rounds).

## Context

`build.output` is a designed topic with no producer. The sidecar's output reaches the
console artifact in batches (100 ms, 200 lines or 64 KiB), but nothing is published and
the `consoleRef` is returned only at the end, so 2b's UI shows the console after the
build. A tab opened mid-build must see the lines already written and every later one,
with no window between the two.

## Decision

1. **Capability** `build.watchOutput`: kind command (it creates a subscription), scope
   workspace, statefulness `streaming`, audience all, not long-running, since `0.16.0`.
   Params `{}`. Result `{subscriptionId, builds: [{buildId, consoleRef, offset}]}`: the
   workspace's running builds, each console's size taken **after** the subscription is
   open. The client reads `[0, offset)` with `artifact.read`; the rest arrives as events.
   One subscription follows every build of the workspace, any client's.
2. **Events**, topic `build.output`, type `lines`: `{buildId, consoleRef, offset, stream,
   lines}`, `offset` the byte position of the first line; `stream` is `stdout`,
   `stderr` or `droidyard` (the connector notice and the cap marker). The console holds
   **whole lines only**: a line that does not fit under the 32 MiB cap is dropped whole
   and counted in `consoleDropped`, so the marker starts its own line and no rune is
   split. Every line appended is published once, in order, under the console's lock: the
   prefix plus the events equal the console. Project output, not untrusted (ADR-0015 §1).
3. **Delivery** as ADR-0043 §3: a seq, never the ring, offered to every watch of that
   workspace, not subscribable through `events.subscribe`. After `events.gap` the client
   reads from its last offset.
4. **Off MCP** (ADR-0010): streaming. Agents keep `artifact.read` after the build.
5. **Versioning**: a new capability and topic, `0.15.0` → `0.16.0`.

## Consequences

- The UI's console streams any client's build, and a tab opened mid-build catches up.
- A line longer than what remains under the cap is lost whole instead of cut.
- The register rows "the build console while a build runs" and the no-producer
  `build.output` / `consoleRef while a build runs` close; `build.progress` stays without a
  producer (progress is `operation.progress`).

## Alternatives considered

- **Polling `artifact.read`**: no contract change, but polling is not Logcat-grade, and the
  ref is unpublished until the end.
- **An `events.subscribe` topic**: no snapshot for a tab opened mid-build, no published
  payload, and a busy build would flush the replay ring.
- **A per-build watch**: one subscription and one start race per build.
- **Lines on `operation.progress`**: rejected by m1d; different consumers and rates.

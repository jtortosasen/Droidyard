# Interface: HTTP and WebSocket

How the capability API is projected to HTTP and WebSocket: the way out for any UI or tool of
its own ([ADR-0137](../adr/0137-machine-level-android-tool.md) §6). This is an **adapter**: it
translates and holds no domain logic ([ADR-0003](../adr/0003-unified-capability-api.md)).
It runs inside the daemon, next to the MCP adapter
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)). **The daemon serves no page**
and has no `ui` subcommand: a UI is a client like any other. A page from another origin is
refused by rules 3 and 5 (its `Origin` is not the daemon's, and no CORS is granted), so a UI
brings its own server or shell, which calls the daemon with the token.

## Security baseline

The listener exposes `build.run`, which executes project code, and the device's actions. It
does so on a port that every web page open in the user's browser can reach. Without a fence:

- **CSRF.** A page can send a "simple" cross-origin request (a form post, or
  `text/plain`). The browser delivers it even though the page cannot read the reply.
- **DNS rebinding.** A page resolves its own hostname to `127.0.0.1`, becomes
  same-origin with the daemon, and can then read replies.
- **Cross-site WebSocket hijacking.** Browsers apply no CORS to WebSocket handshakes.
- **Other local users** can reach loopback TCP.

The Unix socket used by the shims is not exposed to any of this (`0600`, peer-uid
check, ADR-0019). This baseline covers the HTTP listener only. The adapter enforces
every rule **before** it resolves a capability. A request that fails a rule gets a
bare HTTP error and never reaches the capability layer.

1. **Loopback only.** The default bind is `127.0.0.1` on a fixed port, `7317` (outside
   the Linux and macOS ephemeral ranges; not checked against the IANA registry). It is
   a constant (milestone 1d, D3): configuration version 1 defers the port
   ([ADR-0032](../adr/0032-configuration-file-v1.md), Q2 `Amended:` line). If the port is taken, an ephemeral port is used
   instead. `daemon.lock` (`http`) and
   `droidyard status` report the actual address. A non-loopback address is **refused at
   startup**: this listener stays loopback ([ADR-0097](../adr/0097-local-first-with-peers.md)
   §3), and LAN access needs its own ADR. Paired Droidyards reach a separate peer
   listener, on private addresses or the one `hosting.listen` names, with its own TLS and pinning
   ([ADR-0098](../adr/0098-peer-transport-identity-pairing.md),
   [ADR-0109](../adr/0109-private-networks-live-peer-config.md); [peer.md](peer.md)).
2. **Host allowlist (stops DNS rebinding).** `Host` must be exactly
   `127.0.0.1:<port>` or `localhost:<port>`. Anything else gets `403`. A rebinding page
   sends its own hostname, so it is rejected here.
3. **Origin check.** If `Origin` is present, it must be `http://127.0.0.1:<port>` or
   `http://localhost:<port>`, or the request gets `403`. On the WebSocket upgrade
   `Origin` is **mandatory**. Requests without `Origin` (curl, scripts) pass this rule
   and still need the token.
4. **Per-launch token.** 256 random bits, generated at every daemon start and never
   reused. They are written to `<state>/run/http-token` (file `0600`, directory
   `0700`; [ADR-0021](../adr/0021-persistence-format-and-location.md)) and deleted on
   exit.
   - API requests carry `Authorization: Bearer <token>`.
   - A WebSocket must send `{"auth": "<token>"}` as its first frame within 5 s, or it
     is closed with code `4401`.
   - The comparison is constant-time.
5. **No ambient credentials.** No cookies and no CORS: `Access-Control-Allow-*` is
   never sent, and preflights get `403`. With no cookie, a cross-site request has
   nothing to ride on, so CSRF fails by construction; rules 2 and 3 are defence in
   depth. Cookies are rejected on purpose: they are not port-scoped, so any other
   server on `127.0.0.1`, such as a project's dev server, would receive them.
6. **JSON only.** `POST` bodies must be `application/json`, or the request gets `415`.
   Non-simple content types force a preflight, and rule 5 refuses it.
7. **Response headers.**
   - API and artifact responses: `Cache-Control: no-store`.
   - Everywhere: `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.
8. **Untrusted data is rendered inert.** A client that displays fields the schema marks as
   device- or app-derived ([ADR-0015](../adr/0015-untrusted-data-authorization.md)) as
   renders them as plain text: never as HTML or Markdown, and never as links. Artifacts are served only
   as `image/png`, `image/jpeg` or `text/plain; charset=utf-8`.

**Finding the daemon and the token.** `droidyard status` prints the listener's address; the
token is in the file of rule 4, readable by the same user only.

**Residual risk, stated plainly.** Any process running as the same user can read the
token file, as it can connect to the Unix socket. Same-uid code is inside the
trust boundary (ADR-0015 §6). This baseline defends against web pages and other users,
not against the user's own processes.

## Endpoints

The routes and their shapes are `adapters/httpapi`: `POST /v1/call`, `GET /v1/artifacts/{id}`,
`GET /v1/ws` and `GET /v1/screen`, all behind the token; any other path is `404`. HTTP requests identify their
adapter as `ui`, and that identity is what lets human-only capabilities through
([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).

### `POST /v1/call`

- The body is the request envelope
  ([capability-api](../architecture/capability-api.md) §Request envelope). A project
  capability's `project` is in `params`, like any parameter. `waitMs` bounds the wait
  ([ADR-0020](../adr/0020-operation-contract.md)); for long-running capabilities a client
  passes a short `waitMs` and follows progress over the WebSocket.
- The reply is **`200` with a response envelope**, even for rejections and failed
  operations. Consumers branch on `error.code`, never on the HTTP status. Other statuses
  come only from the baseline and from transport problems (malformed JSON, token, Host or
  Origin, body size, content type).
- A result that carries device- or app-derived data has `meta.untrusted: true`, and a client
  finds out which fields to render inert from the schema's `x-droidyard-untrusted` marks
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §1). A failed call whose
  handler marked it carries `meta.untrusted: true` too; a client renders that error's
  `details` inert as a whole.
- A plain HTTP request has no session. Stateful and streaming capabilities (`events.*`,
  `device.watchLogs`) are rejected with `SESSION_REQUIRED`
  ([ADR-0010](../adr/0010-capability-statefulness.md)); use the WebSocket.

### `GET /v1/ws`

One connection is one **session** ([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).
Frames are JSON text messages: the `{"auth": …}` frame first, then request envelopes as
on `/v1/call`; back come response envelopes (matched by `id`) and `{"subscriptionId",
"event"}` for the session's subscriptions. An event that matches two subscriptions is sent
once under each. A subscription's first frame, replayed `since` events included, is written
only after a response carrying its `subscriptionId` has been written on this session (the
subscribe's own, or a later `operation.wait` when the subscribe answered `running`), so a
client never sees an id before the answer that gives it.

Subscriptions belong to the session and end with it. After a reconnect a client resumes
with `since`, except a stream such as `device.watchLogs`, which starts again
([capability-api](../architecture/capability-api.md) §Subscriptions). A subscriber that
overflows gets an `events.gap` marker and never loses events silently. Frame limits, the
close codes and the keepalive are in `adapters/httpapi/ws.go`.

### `GET /v1/screen`

The live view's video ([ADR-0078](../adr/0078-screen-watch-and-endpoint.md) §4), on a socket of its
own, bound by the `streamId` of a session's `device.watchScreen`. After `auth`, binary frames are the
view's input ([ADR-0117](../adr/0117-live-view-input-over-the-stream.md) §3) when the result's
`control` is true; a text or malformed frame closes 1003, any other refusal 1008, over 4 KiB
1009 (ADR-0028). The id is not kept in the operation record. Header, close codes:
`adapters/httpapi/screen.go`.

### `GET /v1/artifacts/{id}`

- `{id}` is the last segment of the artifact reference (`droidyard://artifact/<id>`). A
  client builds this path from nothing else.
- The route has the same semantics and authorization as `artifact.read` and is recorded as
  that operation. Only the encoding differs: raw bytes instead of JSON. A binary route
  cannot return a `200` envelope, so this is the one place where the status carries
  meaning (`404`, `410` for `ARTIFACT_EXPIRED`, `400`, `500`).
- A finished text artifact (a UI dump, a recording's timeline) is served whole or by `Range`.
  One that is still growing, such as a running recording or a remote one not yet copied, is
  `400`: the route never serves a partial file as if it were the whole. To follow a running build, call `build.watchOutput`
  ([ADR-0044](../adr/0044-build-watchoutput.md)). Images accept `?maxEdge=` and `?region=`.
- An artifact produced by an untrusted capability (a screenshot) is served with
  `X-Droidyard-Untrusted: true`: the route has no envelope to carry `meta.untrusted`
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §1; ADR-0024).
- A client fetches with the `Authorization` header and displays the result through `blob:`
  URLs. An `<img src>` pointing at the API cannot carry the header, and a cookie is ruled
  out by rule 5.

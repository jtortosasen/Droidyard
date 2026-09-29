# Interface: HTTP and WebSocket

How the capability API is projected to the web UI. This is an **adapter**: it
translates and holds no domain logic ([ADR-0003](../adr/0003-unified-capability-api.md)).
It runs inside the daemon, next to the MCP adapter
([ADR-0019](../adr/0019-process-topology-daemon-and-shims.md)).

> Status: draft. §Security baseline is **required before the Phase 0 HTTP skeleton
> ships**. The rest may change as the UI grows.

## Security baseline

The listener exposes `file.write`, `build.run`, which executes project code, and
`shell.exec` when policy enables it. It does so on a port that every web page open in
the user's browser can reach. Without a fence:

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

1. **Loopback only.** The default bind is `127.0.0.1` on a configurable fixed port. If
   the port is taken, an ephemeral port is used instead. `daemon.lock` (`http`) and
   `aiodev status` report the actual address. A non-loopback address is **refused at
   startup** in the MVP: LAN exposure needs its own ADR covering TLS and
   authentication ([adr/README](../adr/README.md) §Open decisions).
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
   - UI pages: `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'`.
   - API and artifact responses: `Cache-Control: no-store`.
   - Everywhere: `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.
8. **Untrusted data is rendered inert.** The UI renders fields that the schema marks as
   device- or app-derived ([ADR-0015](../adr/0015-untrusted-data-authorization.md)) as
   plain text: never as HTML or Markdown, and never as links. Artifacts are served only
   as `image/png`, `image/jpeg` or `text/plain; charset=utf-8`.

**Bootstrapping the browser.** `aiodev ui` (which auto-starts the daemon) opens
`http://127.0.0.1:<port>/#token=<token>`. `aiodev status` prints the same URL. A URL
fragment is never sent to a server and never appears in `Referer`. The UI moves the
token into `sessionStorage` and strips it from the address bar. `sessionStorage` is
scoped to the exact origin, port included. A tab opened without a token shows
"run `aiodev ui`". The static UI bundle is served without the token, because it
contains no data. Rules 1–3 and 7 still apply to it.

**Residual risk, stated plainly.** Any process running as the same user can read the
token file, just as it can connect to the Unix socket. Same-uid code is inside the
trust boundary (ADR-0015 §6). This baseline defends against web pages and other users,
not against the user's own processes.

## Endpoints

| Method and path | Maps to | Token |
| --- | --- | --- |
| `GET /`, `GET /assets/*` | the embedded UI bundle | no |
| `POST /v1/call` | one request envelope → one response envelope | yes |
| `GET /v1/artifacts/{id}` | `artifact.read`, as raw bytes | yes |
| `GET /v1/ws` | a WebSocket session: calls and events | first frame |

HTTP requests identify their adapter as `ui`. That identity is what lets human-only
capabilities through ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §6).

### `POST /v1/call`

- The body is the request envelope
  ([capability-api](../architecture/capability-api.md) §Request envelope).
  `workspace` comes from the UI's own selection. `waitMs` bounds the wait
  ([ADR-0020](../adr/0020-operation-contract.md)). For long-running capabilities the
  UI passes a short `waitMs` and follows progress over the WebSocket.
- The reply is **`200` with a response envelope**, even for rejections and failed
  operations. Consumers branch on `error.code`, never on the HTTP status.
- A result that carries device- or app-derived data has `meta.untrusted: true`. The UI
  finds out which fields to render inert from the schema's `x-aiodev-untrusted` marks
  ([ADR-0015](../adr/0015-untrusted-data-authorization.md) §1).
- Other statuses come only from the baseline and from transport problems: `400`
  (malformed JSON), `401` (token), `403` (Host or Origin), `413` (body over 1 MiB) and
  `415` (content type).
- A plain HTTP request has no session. Stateful and streaming capabilities
  (`events.*`, `file.watch`) are rejected with `SESSION_REQUIRED`
  ([ADR-0010](../adr/0010-capability-statefulness.md)); use the WebSocket.

### `GET /v1/ws`

One connection is one **session** (ADR-0019). Frames are JSON text messages:

- client → server: `{"auth": …}` first, then request envelopes, the same as
  `/v1/call`;
- server → client: response envelopes (matched by `id`), and `{"event": <event
  envelope>}` for the session's subscriptions.

Subscriptions (`events.subscribe`, `file.watch`) belong to the session and end with
it. After a reconnect, the UI resumes with `since`
([capability-api](../architecture/capability-api.md) §Subscriptions). The per-session
send buffer is bounded. When it overflows, the dropped events are replaced by an
`events.gap` marker and never lost silently. The server pings every 30 s and closes the
connection after two missed pongs.

### `GET /v1/artifacts/{id}`

- `{id}` is the last segment of the artifact reference (`aiodev://artifact/<id>`). The
  UI builds this path from nothing else.
- The route has the same semantics and authorization as `artifact.read`, and it is
  recorded as that operation. Only the encoding differs: raw bytes instead of JSON.
- **Text** supports `Range: bytes=…`, with the same byte offsets as `artifact.read`.
  An artifact that is still growing (a running build's console) returns the bytes
  written so far and `X-Mydev-Artifact-Growing: true`. To follow it live, subscribe to
  the `build.output` topic instead of polling.
- **Images** accept `?maxEdge=<px>` and `?region=x,y,w,h` (normalised 0..1). The
  default is the original size.
- **Errors**: `404` with the typed error as the JSON body (`ARTIFACT_NOT_FOUND`), or
  `410` for `ARTIFACT_EXPIRED` ([ADR-0012](../adr/0012-visual-artifacts-as-refs.md)).
  A binary route cannot return a `200` envelope, so this is the one place where the
  status carries meaning.
- The UI fetches with the `Authorization` header and displays the result through
  `blob:` URLs. An `<img src>` pointing at the API cannot carry the header, and a
  cookie is ruled out by rule 5.

## Open questions

- **LAN and tablet access**: TLS, authentication beyond a per-launch token, and pairing
  a device. The baseline is deliberately loopback-only.
- **Streamable-HTTP MCP** on the same listener. It must apply rules 1–3, as the MCP
  specification itself requires Origin validation for that transport. Its
  authentication is still to decide: the bearer token passed through the client
  configuration, or the MCP authorization flow.

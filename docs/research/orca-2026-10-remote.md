# Orca remote access, pairing and relay: deep dive for Droidyard C4

Subject: Orca (stablyai/orca), shallow clone at commit f48123e8 (2026-10-18).
Source root used below: `/private/tmp/claude-501/-Users-jorgetortosa-Documents-AioAndroidDev/3e86660f-f178-48b7-86aa-f98931091805/scratchpad/orca-src`
All paths are relative to that root. Nothing was executed; everything is read from source and docs.

Labels: **[read]** = stated by a cited file/line. **[inferred]** = my reasoning from what was read. Items I could not settle are in "OPEN" blocks and collected at the end.

Terminology note: Orca has several overlapping things. "Desktop app" = Electron app. "orcad" = a Node-only headless server (`src/main/orcad/`) that `orca serve` prefers. "Electron serve" = the desktop app run headless. "Runtime" = the in-process RPC server (`src/main/runtime/`). "Daemon" = a separate local *terminal/PTY* daemon (`src/main/daemon/`), unrelated to remote access (section 5).

---

## 0. Executive findings (the ten facts that matter most)

1. **Remote protocol is a JSON-RPC-ish request/response + streaming protocol over a plain `ws://` WebSocket, with no TLS.** Confidentiality and authentication are an application-layer NaCl/X25519 E2EE handshake on top, "since React Native can't pin self-signed certs" (`src/main/runtime/runtime-rpc/runtime-rpc-lifecycle.ts:81`; `src/main/runtime/rpc/ws-transport.ts:1`). [read]
2. **A paired remote client can run arbitrary commands as the host user.** The `workspace` permission "equals running commands as this host's user"; every paired runtime client gets it by default (`src/main/runtime/rpc/rpc-method-permission.ts:7-14,16-18`; `src/main/runtime/rpc/rpc-caller-scope.ts:22-34`). Orca's own comments say so and design around it. [read]
3. **The pairing "link" is the credential.** It carries a 24-byte random bearer token (hex), the host's pinned X25519 public key and the endpoint (`src/main/runtime/device-credential.ts:4-6`; `src/shared/mobile-relay-pairing-offer.ts:73-81`). The direct (non-relay) link has **no expiry** and is **not single-use** in the code I read; the docs claim a "one-time pairing code" and "expire after a few minutes", which holds only for the relay invite (10 min, 5 attempts). [read] + [inferred]
4. **Newer clients upgrade the bearer to a per-installation Ed25519 device key** (proof-of-possession over the E2EE transcript). The token row *stays* as a bearer for other holders, and revoking it does not cascade to the keyed forks. [read] (`src/main/runtime/device-key-fork.ts:22-25`; `src/main/runtime/rpc/mobile-e2ee-auth-validation.ts:92-134`)
5. **Per-client revocation exists and is immediate**: `removeDevice` then `ws.terminate()` on every socket of that device (`src/main/runtime/runtime-rpc/runtime-rpc-mobile-devices.ts:45-62`; `src/main/runtime/rpc/ws-transport.ts:116-125`). It is only reachable from the host's own desktop UI (IPC `mobile:revokeRuntimeAccess`, `src/main/ipc/mobile.ts:250-256`); a paired client is never granted `pairing-admin`. I found **no CLI revoke command** for a headless `orca serve` host. [read] + OPEN
6. **Default bind is wide.** Electron serve and orcad (`orcadServeArgs` default `'0.0.0.0'`) bind every interface; the "connection address" picker only changes what is *advertised*, not what is bound (`src/shared/orcad-serve-argv.ts:38`; `runtime-rpc-lifecycle.ts:160-176`). The desktop app (non-serve) starts loopback-only and widens to `0.0.0.0` the first time a network pairing offer is made. Guidance is purely documentary: "Do not forward the Orca port directly to the public internet" (`docs/site/content/docs/remote-servers.mdx:119-121`). [read]
7. **No lockout / rate limit on the direct listener.** Only caps: 128 WS connections, 256 TCP, 10 s pre-auth timeout, 10 s E2EE handshake timeout, 5 consecutive decrypt failures (`ws-transport.ts:20-24`; `e2ee-channel.ts:34-35,183-186`). A "throttle" class exists but only raises one UI notification after 3 failures in 60 s (`src/main/runtime/rpc/unpaired-device-auth-throttle.ts:1-7`). Token entropy (192 bits) is the only defense. [read]
8. **There is no audit log of remote-client actions.** The only audit log in `src/main` is for plugins (`src/main/plugins/plugin-audit-log.ts`). Per-device `lastSeenAt` is the only trace. [read: grep] 
9. **The cloud relay is operated by Orca (Stably) and is an opaque frame splicer**: both desktop and phone dial *out* over `wss://`; the relay joins them by a 16-char host id derived from the desktop's public key; payloads are E2EE with a key schedule pinned to the desktop key from the QR. The relay still sees account identity, host id, IPs, timing and sizes, and requires an Orca account on the desktop. [read]
10. **E2EE has no forward secrecy against desktop-key compromise** (desktop's X25519 key is long-lived and static; only the client key is per-connection), but has strong transcript binding, direction/counter nonces and anti-downgrade latches. [read] + [inferred]

---

## 1. The remote runtime: what runs where, what is exposed

### 1.1 Roles

| Piece | What it is | Source |
|---|---|---|
| Desktop app (server role) | Electron app; "Settings → Remote Orca Servers → Advertise this app as a server → New Link" produces a pairing link. Owns projects, worktrees, terminals, agents. | `docs/site/content/docs/remote-servers.mdx:8,58-69` [read] |
| `orca serve` | CLI that runs the same runtime without a window. Prefers **orcad** (Node server); falls back to the desktop app's Electron server (always, on packaged macOS). Foreground, `Ctrl-C` to stop. | `remote-servers.mdx:123-170`; `src/cli/specs/serve.ts:4-43` [read] |
| orcad | Standalone Node runtime; launched by the CLI with `--bind 0.0.0.0` by default and a `--web-client-root`. | `src/shared/orcad-serve-argv.ts:34-46`; `src/cli/runtime/serve-orcad-launch.ts:67-95` [read] |
| `orca serve service install` | Writes a macOS LaunchAgent / Linux systemd user unit running `orca serve --supervise --no-pairing`; the service never prints a pairing link to its log; pair from desktop app or a manual `orca serve`. | `serve.ts:56-71` [read] |
| Web client | Static React bundle (`vite.web.config.ts` -> `out/web`, entry `src/renderer/web-index.html`), served by the **same** HTTP server as the WebSocket (plain HTTP, GET/HEAD only, allowlisted paths `/web-index.html`, `/assets/`, `/cmaps/`, `/standard_fonts/`, `/wasm/`). | `vite.web.config.ts:12-37`; `src/main/runtime/rpc/static-web-client-handler.ts:6-7,40-61` [read] |
| Mobile app | Expo/React Native app (`mobile/`). Stores token in OS keychain (`WHEN_UNLOCKED_THIS_DEVICE_ONLY`). | `mobile/src/transport/pairing-keychain.ts:5-7`; `host-device-token-store.ts:19-22` [read] |
| Desktop as client | Another Orca desktop pastes the link ("Add Server"); same protocol; `orca --host runtime:` CLI also a client. | `remote-servers.mdx:76-85`; `rpc-caller-scope.ts:22-27` [read] |
| Local owner channel | Unix socket / named pipe + `orca-runtime.json` holding an `authToken`, for the host's own CLI/desktop. Separate from the remote channel. | `src/main/runtime/runtime-rpc/runtime-rpc-lifecycle.ts:41-76`; `runtime-rpc-request-admission.ts:120-153`; `src/shared/runtime-bootstrap.ts:15-21` [read] |

Docs positioning: "Orca does **not** sell managed VPS hosting. Remote modes always use machines and cloud accounts you control." (`docs/site/content/docs/ways-to-run.mdx:21`). Remote servers are labelled **Beta** and should stay "on a private network path you control" (`remote-servers.mdx:12-15`). [read]

### 1.2 Wire protocol and port

- One `http.Server` + `ws` `WebSocketServer({server, maxPayload})` (`ws-transport.ts:197-215`). Same port serves the static web client over HTTP and the WebSocket upgrade. [read]
- **Default port 6768**; if taken it moves (persisted fallback port, a deterministic "ladder", finally OS-assigned); with `--port` it is *pinned* and fails if busy (`src/cli/specs/serve.ts:24`; `ws-transport.ts:142-191`, `requirePinnedPort`). The fallback port is persisted so already-paired devices stay valid (`ws-transport.ts:40`, STA-1511). [read]
- **No TLS.** The code comment: "WebSocket uses per-device tokens + E2EE (tweetnacl) instead of TLS since React Native can't pin self-signed certs" (`runtime-rpc-lifecycle.ts:81`). A pairing URL may be `wss://` when the user puts a reverse proxy/tunnel in front (`pairing-endpoint.ts:16-40` normalizes http(s) to ws(s); `createWebClientUrl` flips `wss:`->`https:`). [read]
- **Message layer:** after E2EE ready, each frame is a JSON text (or binary) message dispatched like the local RPC: request `{id, method, params}`, replies by `id`, plus subscriptions/streams (terminal output, file watch, screencast) and binary frames (`rpc-binary-sender.ts`, `runtime-rpc-binary-routing.ts`). So: **JSON-RPC-style over WebSocket, E2EE-sealed**. [read; exact request envelope not read, see OPEN]
- Frame limits: inbound 1 MiB, outbound JSON 4 MiB, binary 8 MiB, 256 pending requests / 256 subscriptions per client (`src/shared/remote-runtime-memory-limits.ts:7-24`). [read]
- Liveness: server pings every 15 s, reaps half-open sockets in ~60 s (`ws-transport.ts:26-27`). [read]

### 1.3 What a remote client can do

Registered RPC methods cover (directory listing `src/main/runtime/rpc/methods/`): terminals/PTY, files and file watching (`files*.ts`, `files-mutation-methods.ts`), git (`git.ts`, `git-diff-methods.ts`), GitHub/GitLab/Linear/Jira, agents and agent sessions (`agent-launch*.ts`, `agent-session.ts`, `structured-agent-session-*.ts`), browser automation (`browser*.ts`), emulator control (`emulator.ts`), SSH targets (`ssh.ts`), accounts (`accounts.ts`), skills, settings, notifications, orchestration, plugins, server updates/managed server (`managed-server.ts`), and *desktop control* (`computer.ts`). [read: file names; not each method]

Permission tiers (`rpc-method-permission.ts:15-32`) and who holds them (`rpc-caller-scope.ts`):

| Caller | Gets | Notes |
|---|---|---|
| owner (local CLI/desktop with `orca-runtime.json` authToken) | everything | `rpc-caller-scope.ts:20,54` |
| `runtime-paired` (desktop/web/CLI client) | `workspace, settings-write, accounts-admin, skills-admin, host-admin, device-self` + optional grants | `RUNTIME_PAIRED_STANDARD`, lines 27-34 |
| `desktop-control` | only if granted at pairing (`orca serve --grant-desktop-control`) | `serve.ts:28`; `rpc-method-permission.ts:34-38` |
| `pairing-admin` | **never** granted to a paired runtime client | `rpc-caller-scope.ts:22-27` comment |
| `mobile` | method-name allowlist `MOBILE_RPC_METHOD_ALLOWLIST` | `rpc-caller-scope.ts:46-52` |
| delegated phone (via a paired desktop) | mobile minus `pairing-admin` | line 50 |

[read] Orca's stated reasoning: since `workspace` already means "can run code as this host's user", restricting settings/accounts/skills for a paired client buys nothing (`rpc-method-permission.ts:7-13`). That is the honest security model: **pairing = remote shell**.

### 1.4 What runs where for agents
"Terminals, agent processes, files, worktrees, and session state live on the server computer"; agent CLIs and their credentials must be installed/authenticated on the server (`remote-servers.mdx:30,101,235-237`). Agents keep running if the client sleeps. [read]

---

## 2. Pairing

### 2.1 How a link is generated [read]
`RuntimeRpcPairing.createPairingOffer` (`src/main/runtime/runtime-rpc/runtime-rpc-pairing.ts:137-211`):
1. Resolve the advertised endpoint: if no address given, advertise `127.0.0.1` (wildcard binds are never client-reachable); a user-supplied host, `host:port` or `ws(s)://` URL otherwise; wildcard hosts and port 0 rejected (`pairing-endpoint.ts:16-41,65-100`).
2. `getOrCreatePendingDevice` (reuse the existing never-scanned token row of the same scope and grants) or `rotatePendingDevice` (explicit "Regenerate": drops pending rows, mints a new one) (`device-registry.ts:99-150`).
3. Token = `randomBytes(24).toString('hex')` (192 bits), device id = `randomUUID()` (`device-credential.ts:4-6`). The row is persisted **before** the link is usable ("a credential is not valid until its durable registry write succeeds", `device-registry.ts:93-96`).
4. Encode: `orca://pair?code=<base64url(JSON offer)>` (`src/shared/pairing.ts:14-27`). Web variant: `http(s)://host:port/web-index.html#pairing=<encoded url>`, credential kept in the URL *fragment* to stay out of proxy logs and Referer (`runtime-rpc-pairing-types.ts` `createWebClientUrl`, ~L125-134).
5. Mobile gets a QR (`qrcode`, error correction M, `mobile-pairing-qr.ts:8-17`); `orca serve --mobile-pairing` prints a QR in the terminal.

### 2.2 Contents of the offer (what the client receives) [read]
`PairingOfferSchema` v2 (`src/shared/mobile-relay-pairing-offer.ts:47-105`): `endpoint`, `deviceToken` (the bearer), `publicKeyB64` (host X25519 key, **pinned** by the client), `pairedDeviceId`, `scope` (`mobile`|`runtime`), optional `hostDescriptor`, optional `relay` object (director/cell URLs, `relayHostId`, one-shot `inviteToken`, `inviteExpiresAt`, `e2eeFraming: 2`). Size caps: whole code 128 KiB, endpoint 16 KiB, public key 4 KiB (`mobile-pairing-protocol-limits.ts:1-6`).
So the client receives a **bearer token + the server's public key + address**, not a certificate. There is no client key pair at this point; the client mints one later (2.5).

### 2.3 Entropy, expiry, single use [read] / [inferred]
- Direct token: 192-bit random, hex. [read]
- **Expiry: none** in `isPendingTokenRow` / `DeviceEntry` (`device-entry.ts:52-57`): a pending row is "never scanned" (`lastSeenAt === 0`), nothing time-based. [read]
- **Single-use: no.** The same token authenticates repeatedly; after the first auth `lastSeenAt` is set and the row becomes an ordinary device; rotation only drops *never-scanned* rows (`device-registry.ts:148`). Docs: "Generating another link replaces the previous **unused** link. Clients that already paired keep their own grants until you revoke them." (`remote-servers.mdx:117-118`). So an intercepted link stays valid until the host revokes the row. [read]
- Docs vs code: `mobile.mdx:40` says "Orca shows a one-time pairing code" and `mobile.mdx:109` says "Pairing codes expire after a few minutes". The code supports that only for the **relay invite** (`inviteTtlMs` 10 min, `inviteMaxAttempts` 5; `cloud/packages/relay-contract/src/protocol-limits.ts:12-13`; `mobile-relay-pairing-offer.ts:14,58-67`). For LAN/Tailscale/runtime links the docs overstate. [inferred from reading both]
- Orca's own doc warning: "The pairing URL grants access to this Orca runtime. Treat it like a password" (`remote-servers.mdx:71-74`). [read]

### 2.4 Where secrets are stored [read]

| Side | Secret | Where | Mode |
|---|---|---|---|
| Host | device registry (all tokens in **plaintext**, per-device grants, device public keys, push registrations, relay bindings) | `<userData>/orca-devices.json` (`mobile-pairing-files.ts:3`) | file 0600 via `writeSecureJsonFile`: temp file in the same dir, `writeFileSync(mode 0o600)` + `chmod 600`, `rename` (atomic), parent dir hardened 0700; durable (fsync) variant for security transitions (`src/shared/secure-file.ts:102-176`) |
| Host | long-lived E2EE X25519 keypair | `<userData>/<E2EE_KEYPAIR_FILENAME>` (`e2ee-keypair.ts:44-86`) | same secure writer; refuses to regenerate if the file is *unreadable* (EPERM/EBUSY etc.) because regeneration silently un-pairs every device (`e2ee-keypair.ts:63-73`); regenerates only on malformed/missing |
| Host | local owner token | `orca-runtime.json` `authToken` (`runtime-bootstrap.ts:15-21`), `randomBytes(24)` per runtime start (`runtime-rpc-state.ts:55`) | written by `writeRuntimeMetadata` (mode not read; OPEN) |
| Host | relay-revoke outbox, remote-environment store | `mobile-relay-revoke-outbox.json`; `runtime-environment-store-file.ts:138` | secure writer |
| Host | Windows | ACL-restricted writes (`secure-path-windows-acl`, `writeRestrictedWindowsFile`); if the ACL addon cannot load the file is written plainly and reported unrestricted (`secure-file.ts:111-125`) |
| Mobile client | per-host token | OS keychain/Keystore `WHEN_UNLOCKED_THIS_DEVICE_ONLY` (off iCloud/backup) | `pairing-keychain.ts:5-7` |
| Web client (secure origin, Ed25519 available) | per-host non-extractable Ed25519 `CryptoKey` in IndexedDB; the pairing code is *migrated away* (token replaced by a sentinel prefix) | `web-device-key-store.ts:1-2,26-30`; `web-device-key.ts:5-9,35,53` |
| Web client (plain-http origin, the normal LAN/Tailscale case) | reusable pairing code kept in local storage and used to sign in | `web-device-key.ts:6-8`; `web-pairing-security-guidance.ts:6-9` ("the expected setup for LAN and Tailscale access") |
| Web client (no Ed25519) | tab-session-only pairing | `web-pairing-security-guidance.ts:35-42` |

Notable: tokens are stored **plaintext** (not hashed) on the host (`device-registry.ts:226-231` compares `d.token === token`). The relay stores only **SHA-256 hashes** of invite/resume tokens (`cloud/apps/relay/src/credential-store.ts:72-74`). [read]

### 2.5 How a paired client authenticates on each connection [read]
Per WebSocket (`e2ee-channel.ts`, `mobile-e2ee-*`):
1. Client -> `e2ee_hello` v2: ephemeral X25519 public key, 32-byte client nonce, declared capabilities and a `context {protocol, initiator, responder, transport: direct|relay, relayHostId?}` (`mobile-e2ee-v2-contract.ts:15-22`; `paired-e2ee-v2-client-session.ts:41-60`).
2. Host -> `e2ee_ready` (plaintext JSON): its public key, echo of client nonce, 32-byte desktop nonce, selection, context (`mobile-e2ee-v2-desktop-session.ts:50-58`). The host aborts if the context does not match the transport it is on (`hasExpectedContext`, line 129-144). The client **only accepts the ready if `desktopPublicKey` equals the key pinned from the pairing offer** (`paired-e2ee-v2-client-session.ts:87-93`).
3. Both derive `shared = X25519(secret, peerPublic)` (`nacl.box.before`) then **HKDF-SHA256** with salt `sha256("orca-mobile-e2ee/v2/salt\0" || clientNonce || desktopNonce)` and info `"orca-mobile-e2ee/v2/session\0" || sha256(transcript)`, producing 96 bytes: mobile->desktop key, desktop->mobile key, 32-byte session id (`mobile-e2ee-v2-node-key-schedule.ts:13-33`). The transcript is a length-prefixed encoding of every hello/ready field (`mobile-e2ee-v2-contract.ts:110-146`).
4. Frames: NaCl `secretbox` with a **deterministic nonce** = `sessionId[0:12] || version || direction || payloadKind || 0 || counter(u64)`; the same header is also inside the plaintext and compared; strict in-order `expectedCounter` (replay/reorder rejected) (`mobile-e2ee-v2-framing.ts:12-92`). Direction and kind are bound, so a reflected or type-swapped frame fails. [read]
5. Client -> encrypted `e2ee_auth`, one of:
   - **token**: exactly the keys `{deviceToken, transcriptHashB64, type, v}`; the token is checked with `validateToken` (linear scan, `===`) (`mobile-e2ee-auth-validation.ts:11,52-58`; `device-registry.ts:226-231`; `mobile-socket-wiring.ts:188-200`).
   - **device-key**: `{deviceId, deviceProofB64 (+ clientCapabilities)}`; proof = Ed25519 detached signature over `"orca-device-proof/v1\0auth\0" || transcriptHash` (`device-key-proof.ts:10-23,52-69`), so a captured proof is useless on another connection.
6. Host replies `e2ee_authenticated` echoing the transcript hash; the client only treats a reply that echoes its own transcript as success (`paired-e2ee-client-handshake.ts:230-242`). Failure codes (`bad_auth`, `unauthorized`, `device_revoked`, `registry_unavailable`, `bad_proof`) are sent **sealed** and close with 4001 `Unauthorized` etc. A client deletes its key only on a sealed `device_revoked` naming itself (`mobile-e2ee-auth-validation.ts:136-155`; `device-entry.ts:64-72`). [read]
7. Legacy framing still exists for old clients (static `nacl.box.before`, random nonce per message, plaintext `e2ee_ready`) but is refused on relay (`requireV2: metadata.transport === 'relay'`, `mobile-socket-wiring.ts:186`) and a **client-side downgrade latch** refuses legacy to a host once seen speaking v2 (`paired-e2ee-client-handshake.ts:6-51,115-124`). The direct listener still accepts legacy. [read]

[inferred] Because the token is only sent *inside* the sealed channel, and the client only completes the key schedule against the pinned host key, an active LAN MITM without the host's secret key cannot read the token. The weakness is not the channel but that the link is a bearer secret handed over out of band.

### 2.6 Key upgrade ("device keys") and the fork model [read]
- A token client calls `pairing.registerDeviceKey` over its authenticated v2 socket with `{devicePublicKeyB64, deviceProofB64 (purpose 'register' bound to this socket's transcript), deviceLabel, ephemeral?}` (`runtime-rpc-device-key-registration.ts:13-42`).
- The host **forks** the token row into a new keyed row (`credentialKind: 'device-key'`, own `deviceId`, `forkedFrom`, same scope/grants/reach). "The token row stays, so every other holder of the same token (other tabs, machines, env-var users) and its live sockets keep working." Idempotent per (row, key); one key = one installation; max 32 forks per token row (`device-key-fork.ts:5,22-82`). [read]
- Browser "session-only" keys (`ephemeral`) live in memory only; their row is dropped on host restart and after 12 h idle (`ephemeral-device-rows.ts:4-30`). [read]
- Consequence [inferred]: the shared link is a *registration secret* more than a session secret on modern clients, but nothing retires the token row after use, so the link remains a valid bearer for any other holder until the host revokes that row. Orca's per-key rows show up in the same "Shared Server Access" list (all `scope==='runtime'` rows, `src/main/ipc/mobile.ts:226-240`).

### 2.7 Listing, revoking, re-pairing [read]
- **List:** "Shared Server Access" shows `{deviceId, name, createdAt, lastSeenAt}` for every runtime-scope row, including never-used pending links ("generated web/runtime links are bearer credentials even before a client first connects, so pending runtime grants must stay revocable", `mobile.ts:228-240`).
- **Revoke:** `revokeRuntimeAccess(deviceId)`: delete delegated child phones first (keeping the parent row until every child cleanup is saved so a retry can find them), `registry.removeDevice` (write-before-memory-swap), then `mobileSocketWiring.terminateDeviceConnections(deviceId)` -> `ws.terminate()` (skips the close handshake: "revocation is a security boundary") (`runtime-rpc-mobile-devices.ts:45-62`; `ws-transport.ts:116-125`). Docs: "Active clients using that grant are disconnected immediately" (`remote-servers.mdx:117`). Mobile revoke additionally queues a durable **relay-revoke outbox** item and a push-token unregister (`runtime-rpc-mobile-devices.ts:15-43`; `relay-revoke-outbox.ts`).
- **Revocation of a key-forked client** is a separate row delete; revoking the originating token row does not remove forks (no cascade: `forkedFrom` is read only in `device-key-fork.ts`, `mobile-socket-wiring.ts:211`). [inferred from grep] -> OPEN.
- **Re-pairing:** generate a new link ("Regenerate"/rotate), re-add on the client. A sealed `device_revoked` makes a keyed client delete its key; the mobile UI offers "Re-pair" or "Remove host" (`mobile.mdx:110`). A host that lost its registry rejects previously paired phones with 4001 forever and silently; a throttle (3 failures/60 s) was added to raise one user-facing "re-pair" signal (`unpaired-device-auth-throttle.ts:1-7`). Registry unreadable => the host **refuses to overwrite** it, "which would revoke every paired device" (`device-registry.ts:362-367`); corrupt file is quarantined (`device-registry-corruption.ts`).
- Revocation is only reachable by the host's operator (IPC), not over RPC by a paired client.

---

## 3. Network guidance, binding, abuse controls

### 3.1 Recommended topology [read]
- "The easiest setup is the Orca desktop app on both computers, connected through Tailscale" (`remote-servers.mdx:10`); Tailscale address (`100.x`) listed first in the connection-address picker (`remote-servers.mdx:56`); `orca serve --pairing-address 100.64.1.20`.
- "Keep Tailscale ACLs or grants as narrow as your setup allows. Do not forward the Orca port directly to the public internet. Prefer Tailscale, WireGuard, a trusted LAN, SSH forwarding, or an authenticated tunnel." (`remote-servers.mdx:119-120`).
- "Do not select 127.0.0.1 for another computer"; never a wildcard address as the advertised address (`remote-servers.mdx:121,247`; `pairing-endpoint.ts:33-35`).
- Mobile: LAN, Tailscale "or the pairing path you used" (`mobile.mdx:113`); an address edit on the phone does not re-pair (`mobile.mdx:82-89`).
- Windows: dedicated firewall rule "Orca.MobilePairing" repair flow with remote-scope sufficiency checks (`src/main/runtime/windows-mobile-firewall.ts:11-12`; `windows-firewall-remote-scope.ts`).

### 3.2 Bind-address behavior [read]
- Desktop app: starts **loopback-only** (`WS_BIND_HOST_LOOPBACK`) unless (a) `exposeNetworkByDefault`, (b) a pinned bind host, or (c) a previously-seen non-"this-computer" device exists (`runtime-rpc-lifecycle.ts:160-176`). Making a network pairing offer calls `ensureNetworkExposure()` which stops the loopback listener and rebinds `0.0.0.0` on the same port (terminating live loopback clients; refuses if the bind host is pinned elsewhere) (`runtime-rpc-network-exposure.ts:6-88`). A "This computer only" link never widens (`runtime-pairing-reach.ts:1-5`).
- Electron serve: binds every interface by default (`serve-orcad-launch.ts:67` comment); orcad: `--bind 0.0.0.0` default in the argv builder, `bindHost` parameter "exists for tests" (`orcad-serve-argv.ts:34-38`).
- **Observation [inferred]:** even in the recommended Tailscale setup the process listens on *all* interfaces; only the *advertised* address is the tailnet one. There is no "bind to the Tailscale IP only" option in the paths I read, except an internal `pinnedBindHost` (OPEN: how/if it is exposed).
- Port-shadow guard: before binding a specific address it connect-probes the port so a stranger's wildcard listener cannot capture clients on Windows/macOS (`shadowed-port-probe.ts:5-14`).

### 3.3 Rate limiting / lockout [read]
- Pre-auth: 10 s to complete handshake or the socket is closed (`ws-transport.ts:24`, `e2ee-channel.ts:34,105-107`); max 128 WS / 256 raw TCP connections (`ws-transport.ts:20-22`); 5 consecutive decrypt failures close the socket (`e2ee-channel.ts:35,181-186`).
- No per-IP limit, no backoff, no lockout on token or key attempts on the direct listener; the 192-bit token is the only protection against online guessing. `validateToken` is a non-constant-time `===` scan (`device-registry.ts:226-231`), as is the owner-token compare (`runtime-rpc-request-admission.ts:140`) and the terminal-daemon hello compare (`daemon-client-connections.ts:131`). Likely not practically exploitable over a network at 192 bits, but it contrasts with the relay's `timingSafeEqual` (`credential-store.ts:1,76`). [read] + [inferred]
- The relay, in contrast, rate-limits heavily (section 4.7).
- WebSocket upgrade does **not** validate `Origin` (`new WebSocketServer({server, maxPayload})`, `ws-transport.ts:212-215`, no `verifyClient`) [read]. A hostile web page could open a cross-site WebSocket to a LAN Orca but still needs the token and the E2EE handshake; no CSWSH impact beyond connection exhaustion. [inferred]

---

## 4. The cloud relay

### 4.1 Who runs it
Orca/Stably. `cloud/` is "The relay that connects the Orca mobile app to a desktop host. Phones and desktops never talk to each other directly: each opens an outbound WebSocket to a relay cell, the relay pairs the two sessions, and it splices frames between them. A director assigns hosts to cells and coordinates migrations; cells carry the user connections." (`cloud/README.md:3-7`). Hosted on GCP (Cloud Run / GCE cells, Cloud SQL Postgres; Terraform under `cloud/infra/`), operated by the repository owner; the deploy workflows are inert in the public repo (`cloud/README.md:62-96`). Account/auth services live in a private repo (`cloud/README.md:98-103`). **Mobile-only**: "relay v1 is mobile-only; accepting it on runtime offers would imply routing and credential support that client does not have" (`mobile-relay-pairing-offer.ts:86-94`), so the desktop-to-desktop/web remote path has no relay. [read]
A relay needs an Orca account: "sign-in is required for Relay only" (`mobile.mdx:42`); a signed-out desktop defaults to LAN-only (`mobile-pairing-connection-mode.ts:3-16,38-43`).

### 4.2 Connection topology and rendezvous [read]
```text
Desktop                      Director / Cell (Orca-run)                  Phone
  | POST token-exchange (Bearer <Orca accessToken>,
  |   {relayHostId, hostPublicKeyB64}) -> relayToken (TTL 5 min)
  | POST /v1/assign (Bearer relayToken, {relayHostId})  ---> cellUrl, epoch, lease
  | WSS  <cell>/v1/host/control  (long-lived, outbound) ---->|
  |   host-hello -> host-challenge (box-encrypted to host key) -> proof (HMAC)
  |<-- ping every 15 s; closed after 75 s silence ------------|
  |   invite-create / invite-created (invite token) -------->  (QR / link carries invite + cellUrl)
  |                                                            |<-- WSS <director>/v1/connect/<relayHostId>
  |                                                            |    first frame {v:1,mode:'connect',credential:<invite|resume>}
  |<-- conn-open {connId, connTicket, kind, relayDeviceId} ----|    (director replies relay-moved{cellUrl} if wrong cell)
  | WSS <cell>/v1/host/data/<connId>, first frame host-data-auth{connTicket,generation}
  |========== relay splices phone socket <-> host data socket (opaque frames) ==========|
  |   E2EE v2 handshake runs END-TO-END inside the splice (transport:'relay', relayHostId)
```
- **Rendezvous id:** `relayHostId = base64url(sha256(hostPublicKey)).slice(0,16)` (`relay-http-client.ts:89-91`), 16 chars, validated `^[A-Za-z0-9_-]{16}$` (`relay-server.ts:622-626`). The host id is derived from the same key pinned in the QR, so the phone can verify the relay is routing to the key it expects: the E2EE context carries `relayHostId` and both sides bind it in the transcript (`mobile-e2ee-v2-contract.ts:127,139`; `mobile-relay-pairing-offer.ts:95-103` demands canonical key bytes so the id can be re-derived). [read]
- One host control socket per host plus **one data socket per phone connection**, max 8 per host (`protocol-limits.ts:10`; `control-messages.ts:88-89` `max(8)`; `relay-transport.ts:133-208`). Host attach deadline 10 s (`protocol-limits.ts:16`). [read]
- Splice state machine: pre-auth-admitted -> credential-lease-reserved -> host-notified -> attach-pending -> host-attached -> client-acknowledged -> spliced -> e2ee-confirmable -> teardown (`splice-state-machine.ts:1-34`); the cell refuses to ack the client until both forwarding handlers exist (`mayAcknowledgeClient`). [read]

### 4.3 Host authentication to the relay [read]
- Two layers: (1) Orca account OAuth access token exchanged for a short-lived "relay token" (5 min TTL) bound to `{relayHostId, hostPublicKeyB64}` (`relay-http-client.ts:104-130`; `protocol-limits.ts:19`); (2) a **challenge-response proving possession of the host X25519 secret**: the relay sends a challenge sealed (`Curve25519-XSalsa20-Poly1305`) to the host's public key with a relay ephemeral key, containing a 16-field transcript (relay origin, user/profile/org ids, host id, host public key, assignment epoch, previous generation, resume flag) plus a random secret; the host validates *every* field (clock skew 30 s, challenge window <= 10 s) and answers with an HMAC-SHA-256 over the transcript keyed by the secret (`host-proof-transcript.ts:1-103`; `src/main/runtime/mobile-cloud-relay/relay-host-proof.ts:11-110`). A stolen account token alone cannot impersonate a host key it does not hold. [read]
- Control-socket continuity: `controlResumeSecret`, assignment `generation`, lease expiry (`control-messages.ts:21-29,82-91`). [read]

### 4.4 Phone credentials on the relay [read]
- **Invite** (first connect): 32-byte base64url token minted by the cell on the host's request, stored as SHA-256 hash, `state` machine (`available` -> `reserved` ...), TTL 10 min, max 5 attempts, 2 s cooldown between attempts, 15 s reservation lease; invite-mint rate-limited 30/min/account (`credential-store.ts:113-211`; `protocol-limits.ts:12-15`).
- **Resume credential** (later connects): installed through `DeviceCredentialInstall {newResumeTokenHash, expectedCurrentHash, authorization: relay-basis | authenticated-direct}`; rotating, versioned, 30-day TTL, 24 h grace for the previous hash, confirmation deadline 30 s (`credential-messages.ts:39-94`; `credential-store.ts:11`; `protocol-limits.ts:17-18`). Matching uses `timingSafeEqual`.
- **The relay credential is only outer admission, not identity.** After the splice the phone must still pass the E2EE auth with the *local* device token/key, and the host rejects it if the relay-authenticated `relayDeviceId` does not match the local device row (`mobile-socket-wiring.ts:193-196,205-214`). [read]
- Revoke: `device-revoke` control message, durable outbox with retry (`relay-revoke-outbox.ts`; `revokeMobileDeviceNow`: if the relay revoke cannot be queued, the local revoke **fails** and the row is kept, `runtime-rpc-mobile-devices.ts:28-32`). [read]

### 4.5 End-to-end encryption through the relay [read]
Same E2EE v2 as direct (section 2.5), with these additions: `requireV2` on relay (no legacy), `context.transport: 'relay'` and `relayHostId` bound into the transcript; frame size 8 MiB for splice (catalog responses exceeded 1 MiB at ~415 worktrees, `protocol-limits.ts:4-9`). Primitives: tweetnacl `box` (X25519 + HSalsa20 key derivation via `box.before`), HKDF-SHA-256, `secretbox` (XSalsa20-Poly1305). Identity keys: host long-term X25519 (static, `e2ee-keypair.ts`); phone per-connection ephemeral X25519 (+ optional Ed25519 device key for auth); no host signature key (host authenticity = ability to complete the DH with the pinned key and answer the sealed auth).
- **Forward secrecy:** [inferred] none against compromise of the host's static secret key: the session key is `HKDF(X25519(hostStatic, clientEphemeral))` with nonces; the host contributes no ephemeral DH key. An attacker who recorded relay traffic and later steals `<userData>/…keypair` can decrypt past sessions. Replay across sessions is prevented by fresh nonces/session id. Also compromise of the host key would allow impersonation of the host to the phone.
- **Replay/reorder inside a session:** prevented by the strict counter + direction nonce (`mobile-e2ee-v2-framing.ts`). **Downgrade:** latch (client) + `requireV2` (relay host). [read]
- Legacy framing used a fresh random 24-byte nonce per message with `nacl.box.after` (`e2ee-crypto.ts:48-60`) and no counter (replay within a session not prevented at that layer). [read]

### 4.6 What the relay can see [read] / [inferred]
- Sees [read]: the Orca account (`userId`, `profileId`, `organizationId` are in the host proof transcript), `relayHostId` and the host's public key (sent in token exchange and host-hello), phone `relayDeviceId`s, connection kinds (invite/resume), app version (`HostHelloSchema.appVersion`), assignment/region, timing and byte volume (`onForwardedBytes`), close codes, client IPs (admission is per-source-IP: `maxPreAuthPerSource: 4`, `maxPreAuthAttemptsPerSourcePerMinute: 30`, `admission-budgets.ts:1-15`).
- Does not see [read]: terminal/file/agent payloads: "the splice is an opaque E2EE stream" (`protocol-limits.ts:4`); `splice-forwarder.ts` forwards frames as binary/text without parsing.
- [inferred] Frame **sizes and timing leak** activity (terminal typing bursts, large catalog replies). Not padded; I found no padding code.
- Logging policy for push gateway: "aggregate counters only. Tokens, notification titles, notification bodies, and full host fingerprints never reach a log line" (`cloud/README.md:59-60`). [read]
- The relay can deny service, drop, delay or reorder (a reorder/drop breaks the counter and kills the session), and can tell **when you are online**. It cannot read or inject, absent the host key. A malicious relay operator could however serve a QR-less "moved" redirect (`relay-moved`) only within the pinned host id; the phone still verifies the pinned key. [inferred]

### 4.7 Framing, limits, reconnection, failure modes
- Control messages are strict zod objects (`control-messages.ts`, `.strict()` everywhere); a new field must be negotiated with a capabilities header (`x-orca-host-capabilities`) because cells parse strictly (`control-messages.ts:47-56`). Close codes: 4401 bad outer credential, 4404 host offline, 4408 peer dropped, 4409 wrong cell, 4429 limit exceeded, 4503 draining (`close-codes.ts`). [read]
- Splice back-pressure: low/high water 64/256 KiB, hard queue 8 MiB + 256 KiB, wedged peer closed after 10 s with 4429, process-wide 64 MiB queued budget (`admission-budgets.ts:6-14`; `splice-forwarder.ts:66-137`). Idle timeout 10 min; first-frame deadline 2 s; control ping 15 s, silence 75 s (`protocol-limits.ts:2,11,21-22`). [read]
- Cell capacity: 600 connection hard cap per cell with 100 reserved for host controls; 128 pre-auth caps; director vs cell roles (`admission-budgets.ts`). [read]
- Reconnection: desktop uses jittered exponential backoff 1 s..5 min (`relay-retry-schedule.ts:1-44`), a `drain` message (grace <= 1 h, `recovery: resolve-director`) for migrations, region correction/rehoming docs (`docs/relay-region-correction/`, `cloud/docs/relay-reconnect-2026-09-findings.md`), an assign rate gate to avoid hammering the director (`relay-assign-rate-gate.ts`). Liveness probes on the control socket (`relay-control-liveness.ts`). [read]
- Failure modes [read/inferred]: relay down -> phone cannot reach host unless the direct LAN path works ("automatic direct-first Relay fallback", `device-entry.ts:98-100`; "local-only" mode disables relay); host sleeps/offline -> 4404; account sign-out; DB failure diagnostics doc (`cloud/docs/relay-database-failure-diagnostics.md`); the revoke path requires the relay to accept the revoke (queued; local revoke is refused if the outbox cannot persist).

### 4.8 Push notifications through the cloud [read]
Separate Cloud Run service `orca-cloud-push` (`cloud/apps/push`, `cloud/docs/push-gateway.md`). It holds the Apple `.p8` key and sends APNs/FCM; "Phones never hold an Orca credential for it: the desktop host authenticates with the same X25519 key it uses for the relay, answering an encrypted challenge to mint a 24 hour session, then registers each paired phone's native push token and asks the gateway to push" (`cloud/README.md:32-45`). Per-host quotas; registration retired on provider "unregistered". [inferred] Notification **titles/bodies transit the gateway and Apple/Google in the clear to those parties** (they must, to render a banner); only logging is limited. FCM is collapsible, so "every pending alert is not guaranteed" (`cloud/README.md:48-50`). Revoking a phone queues a push-token unregister (`runtime-rpc-mobile-devices.ts:33-35`).

### 4.9 Trust model summary [inferred]
Trust the relay for availability and routing metadata only; trust the QR/link channel for the host's identity (public key) and the first secret; trust the host's `<userData>` permissions for both the static key and all tokens. The relay operator cannot read payloads but sees who talks to whom and when, and Orca account sign-in ties your desktop to an identity.

---

## 5. The local daemon socket hardening (terminal daemon; not the remote path)

This is a separate local-IPC component (PTY host that survives app restarts). Included because it is the best-documented hardening in the repo and shows failure history. All [read] unless marked.

### 5.1 Layout and permissions
- Endpoint `<userData>/daemon/daemon-v<N>.sock`. If the path would exceed `sockaddr_un.sun_path`, the socket moves to `/tmp/.orca-daemon-<uid>/<sha256(runtimeDir)[:12]>/` (`daemon-socket-endpoint-path.ts:1-9,17-39`). Token, pid record and history remain under userData.
- **Per-uid dir checks** `ensureOwnedPrivateDir(dir, uid)` (lines 41-54): `mkdir(mode 0700)`; tolerate EEXIST; then `lstat` (not `stat`, so a symlink is rejected) and require `isDirectory() && entry.uid === uid && (mode & 0o077) === 0`; **"Validate, never repair: a dir planted by another user or a symlink is refused, not chmod'ed."** Both the per-uid parent and the hash dir are checked. Run **before every connect, not just bind**: "the /tmp path is predictable, so a listener another user planted there would otherwise receive the hello token before any bind-time check ran" (lines 56-62). Non-throwing form `daemonSocketDirIsTrusted` makes probes fail closed (lines 74-81).
- Socket file mode: `chmodSync(bindPath, 0o600)` right after bind but **best-effort, errors swallowed** (`daemon-endpoint-lifecycle.ts:43-49`). So the directory mode, not the socket mode, is the real barrier. [inferred]
- Private file modes: dirs 0700, files 0600 (`daemon-private-file-modes.ts:7-8`); `tightenPathMode` is a best-effort chmod repair for state written by older builds ("read-only volumes, foreign ownership, exotic filesystems: leave the mode as found", lines 15-27); `writeFile`'s `mode` only applies at creation so existing files get explicitly tightened (`terminal-history-session-files.ts:30`, `terminal-history-permission-repair.ts:1-3`). Terminal history (verbatim screen/scrollback) and the auth token are the reason (file header lines 1-3). Windows: POSIX modes are skipped (`supportsPosixFileModes`).
- Uses `randomUUID()` (122 bits) as the **hello token**, written `writeFileSync(tokenPath, token, {mode: 0o600})` after the endpoint is published (`daemon-server.ts:123`; `daemon-endpoint-lifecycle.ts:77`). Compared with plain `!==` in the hello (`daemon-client-connections.ts:131`). Token file unlink is itself an ownership-checked claim: `rename` to a claim name, read, delete only if content matches, so a replacement daemon's token is not removed (`daemon-spawner.ts:271-290`).

### 5.2 Hello handshake
Client sends NDJSON `{type:'hello', version, token, clientId, role:'control'|'stream', streamFraming?}` and waits for `{ok, daemonIdentity{pid, startedAtMs, launchNonce,...}}` with a handshake timeout (a stale daemon may accept and never answer); response line size capped; identity is validated (`daemon-client-hello-handshake.ts:29-128,130-167`). Server order: expect hello -> protocol version -> token -> role; rejections logged with reason (`daemon-client-connections.ts:109-148`). The handshake is **one-way**: the client proves knowledge of the token; nothing proves to the client that the listener is the real daemon, which is why directory trust is checked before connecting. [read] + [inferred]

### 5.3 Endpoint ownership: "never remove a name you did not create" (`src/main/daemon/AGENTS.md`)
Invariants: "Only a daemon publishing itself onto the canonical endpoint may mutate that directory entry, and only by replacing an entry it has itself just proven dead." and "No actor removes a name it did not create." (AGENTS.md:8-11).
Protocol (`daemon-endpoint-ownership.ts:56-110`): bind a private unique name `.p<hex>` -> try an exclusive `link()` to the canonical name -> on `EEXIST`, **prove the incumbent dead by connecting** -> re-check the entry has not changed hands (dev+ino) -> probe again -> single-syscall `rename` -> verify we kept the name (`confirmPublishedEndpoint`).
Traps recorded (AGENTS.md:24-47): never collapse "can't tell" into "dead" (only `refused`/`missing` prove death; timeout or EPERM must decline); `link` first, never unconditional `rename`; `rename`, never unlink-then-link (measured gap: unlink+link gapped on "essentially every observation", rename "none in ~14,500 probes"); do not identify an entry by `birthtimeMs`; **do not add a sweeper**; scratch namespace must avoid patterns shipped builds sweep (`^\.b[0-9a-f]{10}$`); never remove the endpoint on shutdown (a departing daemon leaves a dead entry; the next publisher replaces it). Hard links unsupported -> falls back to the death-proof + continuity re-check path.
**Review history (AGENTS.md:13-22):** root cause: `net.Server.close()` unlinks the pathname it bound with no ownership check, so a departing daemon deleted a live replacement's socket, leaving a daemon hosting PTYs nobody could reach ("terminals that accept keystrokes and never run them"). "Seven review rounds against the older 'launcher reclaims a dead process's name' shape produced twenty-three defects, all the same interleaving: a third party observing liveness at T and acting on the directory entry at T+1." Residual risk acknowledged: the final probe and the `rename` are two syscalls (POSIX has no rename-if-target-is-inode-X); harm is made unreachable because "a daemon never creates a session on an endpoint it no longer holds (`daemon-server.ts`), and it drains rather than serving on."
**Contrast, the remote-runtime's own unix socket** (a different component) is simpler and *does* unlink: `if (kind==='unix' && existsSync(endpoint)) rmSync(endpoint)` then `listen`, `chmod 0600` after listen (a window with umask-default mode before the chmod), but its name is per-pid `o-<pid>-<suffix>.sock` under userData, and orphans are swept only when `process.kill(pid,0)` returns `ESRCH` (`unix-socket-transport.ts:52-76`; `runtime-rpc-socket-metadata.ts:8-61`). [read] The window is unimportant if userData is 0700 (OPEN: userData mode on each OS).

---

## 6. Security claims vs gaps

### 6.1 Claimed by Orca
- Per-client revocable tokens; revoke disconnects live sockets at once (`remote-servers.mdx:115-117`). Verified in code (2.7). [read]
- Application-layer E2EE with transcript binding and anti-downgrade, relay cannot read content (`protocol-limits.ts:4`, E2EE code). Verified in code.
- "Beta... keep... on a private network path you control" (`remote-servers.mdx:12-15`).
- Secure storage: 0600 files/0700 dirs, Windows ACLs, keychain on mobile, non-extractable keys in browsers. Verified (2.4).

### 6.2 Gaps and risks [mostly inferred]
| # | Attacker | What they can do | Evidence |
|---|---|---|---|
| G1 | Anyone who obtains a pairing link (chat history, screen share, terminal scrollback of `orca serve`, QR photo, proxy log for non-fragment variants) | Full command execution as the host user, until the host revokes that row. Direct links never expire and are reusable. | 2.1-2.3; `rpc-method-permission.ts:7-14` |
| G2 | Same, after the legit client keyed-upgraded | Token row still valid for them (the fork leaves it). | `device-key-fork.ts:22-25` |
| G3 | Passive LAN observer | Sees ws:// handshake, the `e2ee_hello` (client ephemeral key, nonce), `relayHostId`/host public key in `e2ee_ready`, sizes and timing; not the token or payloads. | 2.5 |
| G4 | Active LAN MITM without host key | Can drop/delay/DoS; cannot decrypt the token or inject (client verifies the pinned key; transcript echoed in auth reply). The legacy (v1) path is still accepted by the host: a downgrade is blocked client-side only if the client has already seen v2 (a first-time connection from a legacy-only client uses static `box` with random nonces). | `paired-e2ee-client-handshake.ts:6-51,115-124` |
| G5 | Host-key thief (backup of `<userData>`, malware as the same user, NFS) | Impersonate host to paired phones, decrypt recorded traffic (no PFS). Also reads all plaintext tokens. | `e2ee-keypair.ts`; `device-registry.ts` |
| G6 | Network attacker on an exposed port | Unlimited online guessing against a 192-bit token (infeasible) and cheap connection exhaustion (128/256 caps, 10 s timeouts). No lockout, no IP allowlist. | 3.3 |
| G7 | Hostile web page in a LAN browser | Can open a ws:// to the Orca port (no Origin check). Needs a credential to do anything. | `ws-transport.ts:212` |
| G8 | Relay operator | Learns account identity, host id, who connects when, IPs, activity timing; can deny service. Cannot read/inject content. Can see the (public) host key. | 4.6 |
| G9 | Relay account compromise | Needs the host secret key to pass the challenge; token alone is insufficient. Could mint nothing useful for E2EE auth. Could revoke/deny. | 4.3-4.4 |
| G10 | Compromised paired client | Anything the host user can do. Orca scopes only `desktop-control` and `pairing-admin` away. | 1.3 |
| G11 | A second user on the same host | Cannot read tokens (0600/0700) but the owner-token metadata mode is unverified. | OPEN |
| G12 | Operators/auditors | No log of who did what; only `lastSeenAt`, relay counters, and local trace logs. | grep for audit log |

Can a remote client run arbitrary commands? **Yes**: terminals, agent launches with arbitrary prompts, file writes, git hooks, SSH to other machines, accounts, settings. Orca's tiers do not try to prevent this for a standard paired client. [read]

---

## 7. For Droidyard C4

Already decided by lane 2t (read from `/Users/jorgetortosa/Documents/AioAndroidDev/.claude/worktrees/m2t/docs/adr/` 2t ADR 0097..0100, 0104; do not re-propose):
- Local-first, never a service; hosting only to paired Droidyards over a private network the users share (VPN); no cloud, account, relay or overlay in this decision (2t ADR 0097 §1-2; internet reach "reopens §1").
- Mutual TLS 1.3 between daemons, Ed25519 self-signed identity per daemon, fingerprint = SHA-256 of SPKI, **pairing by hand** (each human adds `{name, fingerprint}` to `hosting.peers` / `remotes`), no CA, no bearer token, no pairing code; peer pinned at each handshake, each request and every 2 s on open sockets (a revoked peer loses its links within 2 s); separate listener, IP-literal bind only (unspecified address rejected), `Origin` refused; reconnect backoff 1-30 s; client kind `peer` with configured name (2t ADR 0098).
- Peer surface = `Lendable` descriptors only; everything else `REQUEST_NOT_ALLOWED_HERE`; `installUpload` with 512 MiB / one pending upload / 0600 / 10-min expiry; audit = operation record + `peer=<name>` trace + `daemon.log` lines for handshakes/links/uploads; no durable audit file (2t ADR 0099).
- One lease holder per lent device, any call holds, 15-min idle, ended by link loss / un-lend / host release (2t ADR 0104).

Where Orca's experience adds value or warns, under those decisions:

### 7.1 Pairing UX
1. **Keep fingerprint-pinning, but design the UX so the human cannot get it wrong.** Orca's pain is that the secret is a copyable link that is simultaneously the invitation and the credential. Droidyard's pinned-fingerprint model avoids that (the fingerprint is public). The remaining weak point is *substitution* of the fingerprint in chat. Mitigations that Orca does not have: show a short human-checkable form (e.g. first 8 groups, or a word list) on both sides, ask the *host* human to confirm the client's name out of band, and in `droidyard host admit` show the name and the fingerprint prefix back (`admit` already "names what a name is already lent"). [inferred]
2. **One-step bundle for the second paste.** Orca ships an `orca://pair?code=` URL so one paste conveys endpoint + key + secret. For Droidyard, a non-secret "invitation string" `{name, address, fingerprint}` (printable by `droidyard id --invite`) lets the client add a remote in one paste without making any secret of it. This stays within 2t ADR 0098 (no secret on the wire, no pairing mode). [inferred recommendation]
3. **Address advertise vs bind:** Orca advertises an address separate from the bind and defaults advertise to `127.0.0.1`. Droidyard already requires an explicit IP-literal `listen`; also print/verify the *reachable* address(es) on `droidyard host listen`, and warn when the bind is not a VPN/private interface. [inferred]
4. **State both consequences of a pairing at admit time.** Orca's honest comment ("workspace equals running commands as the host user") is the right posture; Droidyard's `host lend` already prints "the whole device is lent". Add the same explicit sentence to `host admit`: what a peer can reach (only lent devices) and what it cannot. [inferred]
5. Do not copy: a "one-time code" that is not one-time (Orca's docs vs code mismatch, 2.3). Describe lifetime truthfully. [read]

### 7.2 Per-peer revocable credentials
1. Orca proves per-client credentials are worth it: one row per client, `lastSeenAt`, immediate `ws.terminate()` on revoke. Droidyard's pinned-fingerprint list gives the same property with *no stored secret* (nothing to leak from the host's state file except the allowlist). Keep it. [read] + [inferred]
2. Revocation must be (a) durable before returning, (b) kill live links, (c) cover derived state. Orca's lessons: persist before the in-memory swap (`device-registry.ts:174-178,348-360`); `terminate()` not graceful close (`ws-transport.ts:121-122`); refuse to overwrite an unreadable registry because it would revoke everyone (`device-registry.ts:362-367`); revoking a parent must clean child rows first (`revokeRuntimeAccess`). For Droidyard: `revoke` also ends leases and in-flight uploads/streams for that peer (2t ADR 0104 §4 covers the lease), and cleans `lend` (already planned: "revoke cleans `lend`"). [read] + [inferred]
3. **Avoid Orca's fork/cascade trap.** Orca issues a shared bearer first and later forks per-installation keys; revoking the shared row does not revoke the forks, and the shared row lives on for other holders. Droidyard should never have a "shared enrollment secret"; one fingerprint = one peer = one revocation. If a future relay/overlay path introduces enrollment tokens, make them single-use, short-expiry and hash-stored (as Orca's *relay* invites are: 10 min, 5 attempts, SHA-256, `credential-store.ts:113-211`) and revoke by identity, not by enrollment token. [inferred]
4. **Fail-open vs fail-closed on policy state.** Orca's registry distinguishes "unknown" (readable and absent) from "unavailable" so a client deletes its key only on a definitive revoked verdict (`device-entry.ts:64-72`; `registry_unavailable`). If Droidyard reloads `hosting.peers` from policy (2t ADR 0098 §6 "peers live"), an unreadable policy file must **not** be read as "no peers => everyone revoked" nor as "allow all"; keep the last good set and surface a health warning. [inferred]
5. Orca lacks (and Droidyard has) a CLI revoke; do not regress: Orca's revocation is only in the desktop UI, with no CLI for headless hosts (OPEN). Keep `droidyard host revoke` as the primary path. [read]
6. Consider a constant-time compare wherever a secret is compared. Droidyard's mTLS pin compares public fingerprints (not secret), so timing is moot; but the local HTTP per-launch token and the m2t "token off the command line" step should use `crypto/subtle.ConstantTimeCompare`. Orca uses `===` in three places (3.3). [inferred]

### 7.3 VPN first, then relay path ("through a relay or an overlay network rather than a protocol of our own")
VPN first (done by 2t): Orca's recommended Tailscale path ends up exactly where 2t is, minus the per-peer secret and the wildcard bind. Observations:
1. **Do not copy Orca's wide bind.** Orca binds `0.0.0.0` and only advertises the tailnet IP (3.2). 2t already requires an IP literal; keep it and consider documenting "bind the VPN interface address, not 0.0.0.0" and have `system.health.hosting` expose the actual bound address. [read] + [inferred]
2. **Prefer an overlay that gives identity and NAT traversal over building a relay.** Orca had to build and operate a global relay (directors, cells, Cloud SQL, regional rehoming, capacity/incident tooling, 25 workflows, a separate push gateway) because phones on cellular cannot reach a desktop; that is a product, an account system and an on-call rota (`cloud/README.md:13-97`). For Droidyard's stated vision, an overlay (Tailscale/WireGuard/Headscale/ZeroTier, or an SSH/QUIC tunnel the user runs) preserves 2t ADR 0097 §1 ("never a service anyone operates") and keeps the daemon's TLS pin as the *only* identity layer: the overlay supplies reachability, mTLS supplies authentication. No Droidyard code is needed beyond allowing the `remotes` address to be an overlay IP or MagicDNS name (currently an IP literal; name resolution is a deliberate restriction, revisit with an ADR). [inferred]
3. **If a relay is ever built, copy these Orca design decisions** (all [read]):
   - Both ends dial **outbound**; the relay only splices; the splice stays opaque (E2EE end to end).
   - The rendezvous id is derived from the host's identity key (`sha256(pubkey)[:16]`), so the id and the pinned key cannot diverge. In Droidyard terms: derive it from the existing cert SPKI fingerprint.
   - The relay is **not an identity authority**: outer relay admission must never choose the inner identity; the host re-checks that the relay-authenticated device matches the inner one (`mobile-socket-wiring.ts:193-196`). In Droidyard that means mTLS pinning is done *inside* a TLS-in-WebSocket tunnel through the relay, not terminated at it.
   - Admission before cost: first-frame deadline (2 s), pre-auth connection caps (global and per source), capped per-host connections, wedge timeout, process-wide queue budget (`admission-budgets.ts`, `protocol-limits.ts`).
   - Strict schemas with a capabilities handshake so new fields never break old peers (`control-messages.ts:47-56`).
   - Host proves key possession to the relay with a challenge sealed to the host key bound to a transcript of everything the relay and host agreed (`host-proof-transcript.ts`); the relay validates freshness (<= 10 s window, 30 s skew).
   - Enrollment tokens hashed at rest, single-use, short-lived, attempt-limited with cooldown; resume credentials rotating with a grace window.
4. **If E2EE through a relay is built, do better on forward secrecy**: with TLS 1.3 mutual auth inside the tunnel Droidyard gets ephemeral ECDHE for free; do not reimplement NaCl framing (Orca's custom protocol needed a deterministic-nonce counter scheme, transcript encoding, downgrade latches and legacy-compat code, ~dozens of files). Orca's own docs call out the cost: "relay v1 is mobile-only", legacy framing kept alive for compatibility. [inferred]
5. **Account dependence is a trap for C4.** Orca's relay requires an Orca account and OAuth exchange (`relay-http-client.ts:104-130`) and a signed-out desktop falls back to LAN (`mobile-pairing-connection-mode.ts`). Any relay for Droidyard that needs a vendor account contradicts 2t ADR 0097 §1; an overlay the company already runs does not. [inferred]

### 7.4 Audit
1. Orca has none for remote clients (6.2 G12). 2t's choice (operation record with client `peer` and name, `peer=<name>` in trace lines, `daemon.log` for handshake/link/upload events) is already stronger than Orca's. [read]
2. Cheap additions worth considering, within "no durable audit file" (2t ADR 0099 §5): log the **peer fingerprint** (not only the configured name) on every link-open and refusal, so a renamed or re-added peer is distinguishable; log revocation events (who, when) and lease start/end with the holder; log refused `Origin` and refused non-pinned handshakes with the source IP at a rate-limited cadence (Orca's unauthorised-attempt signal fires once per session after 3 failures in 60 s: `unpaired-device-auth-throttle.ts`). [inferred]
3. Surface "lastSeen" per peer in `system.health.hosting` (Orca's `lastSeenAt` is its only per-device trace and it is what makes a never-used link visible and revocable). [inferred]

### 7.5 What to avoid (each with Orca's evidence)
- **A bearer secret that doubles as the invitation.** (Orca link = token; reusable; plaintext at rest; no expiry.) 2t avoided this; do not add an "easy pairing code" later without single-use + expiry + hashed storage.
- **Plain ws:// with a homemade E2EE** as the baseline. Orca took it because React Native could not pin certs; Droidyard's peers are Go daemons that can pin, so mTLS is strictly better and already decided.
- **Wide default bind** with "advertise" cosmetics (3.2).
- **Revocation by UI only**; no CLI path on headless hosts (2.7).
- **No Origin check** on a network WebSocket (7.x); 2t already refuses `Origin`.
- **Wholly trusting the credential for authorization**: Orca's `workspace` tier = shell. Droidyard's `Lendable` descriptor allowlist (refuse before `prepare`, nothing recorded) is the right structure; keep non-lendable capabilities unreachable even if a new capability is added carelessly (default-deny, as Orca's "every non-owner row is deny-by-default" `rpc-caller-scope.ts:3-6`). Orca's comment that a new method "cannot ship without someone deciding which callers may reach it" is worth copying as a descriptor-lint test (a descriptor must declare `Lendable` true/false explicitly). [read] + [inferred]
- **Fragile liveness for endpoints**: Orca's daemon-socket history (seven review rounds, 23 defects) is the cautionary tale for any code that "observes liveness then acts on a directory entry". For Droidyard's own daemon lock/socket, adopt the same rule: never remove a name you did not create; bind to a private unique name, `link`, prove death by connecting, `rename`. Not needed for TCP/TLS listeners (the OS arbitrates the port) but relevant for the local Unix sockets in 2t step 8 ("Unix-socket forwards"). Also copy the concrete defenses: `lstat` + uid + `& 0o077` checks on the directory before **connecting** as well as binding; validate-never-repair; swallow no ownership error as "dead". [read]
- **Storing remote secrets in plaintext JSON** where a smaller artifact would do; Droidyard's identity key at 0600 in a 0700 dir (2t ADR 0098 §1) matches Orca's secure-file pattern. Copy its atomic writer (temp + chmod + rename + optional fsync; refusal to regenerate an unreadable key because regeneration silently un-pairs everyone, `e2ee-keypair.ts:63-73`). Droidyard note: "Lost state is a new identity, paired again" is acceptable, but an *unreadable* key (EACCES/EBUSY) must not trigger regeneration. [read] + [inferred]
- **Telemetry/log leaks of credentials**: Orca redacts supervised stdout (`redactingSpawn`) and the service never prints a pairing link. Droidyard has no secret to print, which is the point; keep it that way and avoid putting the HTTP per-launch token on a command line (2t step 8 already does).
- **Docs that overstate** (Orca's "one-time", "expire after a few minutes" for LAN): state in the Droidyard docs exactly what a pairing grants and for how long.

### 7.6 Small, concrete suggestions that fit inside 2t without a new ADR
1. `droidyard id` output includes a copy-ready invitation line and the fingerprint's grouped short form.
2. `droidyard host list` shows per peer: name, fingerprint prefix, devices lent, last seen, currently linked (Orca's Shared Server Access list is the model).
3. `system.health.hosting`: bound address, peers linked, last refused handshake time and reason.
4. A test that the peer listener refuses a client whose certificate is valid but not in `peers` **after** removal while a link is open (the "revoked peer loses its links within 2 s" claim) at level 4/5.
5. A descriptor test asserting no Lendable capability accepts a path or URL parameter that could escape the lent device (upload/artifact fetch) beyond what 2t ADR 0099 §3 describes.

---

## OPEN: investigate when building (what I could not settle)

1. Exact request/response envelope of the paired RPC (field names, streaming/subscribe frames, error shape): not read; `src/shared/runtime-rpc-envelope.ts`, `rpc/dispatcher.ts` are the files.
2. Whether Orca exposes any way to bind the runtime to one interface (e.g. Tailscale IP) for users: `pinnedBindHost` exists (`runtime-rpc-lifecycle.ts:165`, `runtime-rpc-network-exposure.ts:13`) but its setter was not traced.
3. Whether there is any CLI/RPC path to list or revoke grants on a **headless** `orca serve` host (none found in `src/cli/specs`; revoke is IPC `mobile:revokeRuntimeAccess`). Check `src/main/orcad/` for a management surface.
4. Whether revoking a token row also should remove its `forkedFrom` children (no cascade found by grep; confirm in UI behaviour or in `device-registry.ts` callers).
5. File mode of `orca-runtime.json` (holds the owner `authToken`) and of `<userData>` itself on each OS: `writeRuntimeMetadata` was not read.
6. Whether the legacy (non-v2) E2EE path is still accepted by the **direct** listener for all clients and what the deprecation plan is (`requireV2` only for relay).
7. The relay's behavior when the host key changes (host regenerates keypair): `relayHostId` changes; how stale phones are told. `cloud/docs/relay-reconnect-2026-09-findings.md` and `relay-improvement-roadmap-2026-09.md` unread.
8. Relay log/retention policy and what the Postgres stores per user (only the credential store and assignment store were skimmed; `host-session-registry.ts`, `assignment-store.ts` not read). No published privacy statement was found in the repo.
9. Push gateway payload content (are titles/bodies encrypted to the phone before APNs/FCM?): `cloud/apps/push/src/push-delivery-payload.ts` unread. I assumed plaintext to the gateway.
10. Whether Orca has any rate limit on `/v1/connect` per host/phone beyond per-source pre-auth (`admit(source)` in `relay-server.ts`, `relay-assign-rate-gate.ts` partially read).
11. Windows named-pipe ACLs for the local runtime/daemon (named pipes "lack the chmod hardening of Unix sockets", `runtime-rpc-socket-metadata.ts:54`): not investigated.
12. Droidyard-side: whether 2t's `remotes` should accept DNS names/MagicDNS for overlay use (2t ADR 0098 §3 says IP literal only for `listen`; `remotes` address validation not checked); and whether an overlay path needs any change to "equal `peerProtocol`" or the 2 s pin re-check.
13. Behavior of Orca's web client when the WS reconnects after revocation (loops vs stops): `web-runtime-auth-verdict.ts` not read.

## Key files (all relative to the Orca source root)
- Docs: `docs/site/content/docs/remote-servers.mdx`, `ways-to-run.mdx`, `mobile.mdx`; `cloud/README.md`, `cloud/docs/push-gateway.md`
- Crypto: `src/shared/e2ee-crypto.ts`, `mobile-e2ee-v2-contract.ts`, `mobile-e2ee-v2-node-key-schedule.ts`, `mobile-e2ee-v2-framing.ts`, `paired-e2ee-client-handshake.ts`, `paired-e2ee-v2-client-session.ts`, `device-key-proof.ts`, `secure-file.ts`
- Server: `src/main/runtime/rpc/{ws-transport,e2ee-channel,mobile-socket-wiring,mobile-e2ee-auth-validation,rpc-caller-scope,rpc-method-permission,unpaired-device-auth-throttle,static-web-client-handler,relay-transport}.ts`; `src/main/runtime/runtime-rpc/{runtime-rpc-lifecycle,runtime-rpc-network-exposure,runtime-rpc-pairing,runtime-rpc-mobile-devices,runtime-rpc-device-key-registration}.ts`; `src/main/runtime/{device-registry,device-entry,device-credential,device-key-fork,e2ee-keypair,pairing-endpoint}.ts`
- Relay: `cloud/packages/relay-contract/src/*`, `cloud/apps/relay/src/{relay-server,credential-store,splice-forwarder}.ts`, `src/main/runtime/mobile-cloud-relay/*`
- Daemon: `src/main/daemon/{AGENTS.md,daemon-socket-endpoint-path.ts,daemon-private-file-modes.ts,daemon-client-hello-handshake.ts,daemon-endpoint-ownership.ts,daemon-endpoint-lifecycle.ts,daemon-client-connections.ts,daemon-spawner.ts}`
- Droidyard (read-only): `/Users/jorgetortosa/Documents/AioAndroidDev/.claude/worktrees/m2t/docs/adr/0097..0100,0104`, `.../docs/plans/m2t-device-hosting.md`, `.../docs/vision/beyond.md` (C4 lines 58-94)

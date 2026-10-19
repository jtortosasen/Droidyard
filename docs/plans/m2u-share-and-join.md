# Plan: Milestone 2u — C4b, share a phone with one command, join with one paste

- Status: Approved
- Approved: by the main session under the owner's standing instruction (apply the recommended option), 2026-10-19, Delegated: Q1–Q4 as recommended; the owner had settled the behaviour in the brief and said "procede, está todo claro".
- Date: 2026-10-19
- Milestone: "2u: C4b, share a phone with one command, join with one paste" (owner, 2026-10-19; the owner's six decisions in the brief, quoted under Scope). The owner: "no se ha de escribir nada a mano en ningún fichero de configuración".
- Phase: none. The MVP is done (2h); the owner's decisions of 2026-10-19 and the register rows under Scope are the requirement.
- Contract: ADR-0107, ADR-0108, ADR-0109 (Q1 to Q4). Also ADR-0010 §Decision, ADR-0015 §4, §6, ADR-0019 §Sessions, ADR-0020 §Adapter projections, ADR-0021 §Layout, §Format, ADR-0024, ADR-0032 §4, §5, §6, ADR-0068, ADR-0097 §2, ADR-0098 §1–§7, ADR-0099 §1, §4, §5, ADR-0100 §2, ADR-0103 §1, ADR-0104 §4; `docs/interfaces/peer.md`; `capability-api.md` §Subscriptions; `http-websocket.md` §Security baseline.
- Lane: `2u`, branch `m2u` from `main` at `37815ca`, touches `devices,contract,docs`.
- Size: eight steps. If the owner wants 2u smaller, the cut is step 5 (a detached share's pending code across a restart): without it a restart voids unused codes, and `share` again makes a new one.

**The owner should see:**
- On the Mac with the phone: `droidyard share` (one phone plugged in) prints `usb  samsung SM-A256B  R5CX62JXR3L`, the addresses it listens on, and one line, `droidyard join dy1.…`, then stays, printing who joins and leaves; Ctrl-C stops sharing. `droidyard share -d` prints the line and returns; the share lives until `droidyard unshare`.
- On the other Mac: `droidyard join dy1.…` once. The phone is in its device list at once, as `<that-mac>/R5CX62JXR3L`, for the UI and for agents. No fingerprint, no configuration edit, no restart, on either side.
- The line works once, for one person; another person needs another `droidyard share <serial>`. It never expires on its own. No agent can share, join or allow a device.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| (1) `droidyard share [<serial>]`: the listing (physical only, emulators absent, `unauthorized`/`offline` shown, unpickable), one → shared, several → list and exit, also with `-d` | yes | step 4; Q2 |
| (1) Sharing admits the serial in the host's physical policy, opens the listener with no restart, lends, prints one `droidyard join <code>` line | yes | steps 2, 3; Q2, Q3 |
| (1) Foreground (attached, joins and leaves shown, Ctrl-C stops) and `-d` (until `unshare [<serial>]`); a `-d` share survives a daemon restart | yes | steps 3, 4, 5; Q2; a Mac restart: Q4 |
| (1) The code: no time expiry, single use, a fresh one per `share` of the same serial, void once the share stops; it carries the addresses, port, host fingerprint and a secret sent only inside the pinned TLS channel | yes | steps 1, 3, 5; Q1 |
| (2) `droidyard join <code>`: pins the host, presents its certificate and the secret; the host admits the client and lends it the device; the client records the remote and admits `<remote>/<serial>` with no restart; names chosen automatically | yes | steps 1, 3; Q1 |
| (3) The listener on every private address (VPN: a private address on a tunnel interface; LAN: RFC 1918 elsewhere), never `0.0.0.0` or a public one, `--on vpn\|lan\|<ip>`; a fixed default port, another remembered when busy; follows address changes; the client tries its own LAN first, then the VPN, and keeps the list | yes | step 2; Q3 |
| (4) `droidyard device allow <serial>` and its reverse | yes | step 6 |
| (5) CLI only; a release v0.4.0 at the close (ADR-0068) | yes | step 8 (b); the UI: register row "Pairing and lending from the UI" stays |
| (6) The 2t verbs (`id`, `host …`, `remote …`) stay as the advanced path | yes | unchanged but for their "a restart applies it" lines (step 2) |
| Row "Idea (Orca) … C4 pairing UX …" | part, closed | Q1: the one-paste invitation adapted as a single-use code; fingerprints in the log built in the join lines (step 3). Dropped: the grouped short fingerprint (no human compares fingerprints on the share path; `id` still prints the whole one). A narrower row for `host list` with last seen and the last good peer set; the overlay stays ADR-0097's later decision |
| Row "A peer unpaired and another certificate paired under the same name …" | kept | a join never takes a live peer's name (Q1 §5), but a revoked name may be taken again, as with `host admit` |
| Row "An unknown key inside an entry of `hosting.peers`, `hosting.lend` or `remotes` …" | kept | share and join rewrite those arrays as the `host` and `remote` verbs do |
| Row "The macOS firewall's prompt for an ad-hoc signed `droidyard` accepting peer connections …" | part | step 1 (a): this Mac's firewall is disabled, so no prompt can be seen here; two machines stay the owner's trial |
| Rows "Pairing and lending from the UI", "LAN access to the web UI and MCP …", the owner's trials | no | owner, 2026-10-19 |
| Starting droidyard at login, so a `-d` share resumes after a Mac restart with no command | no | Q4 |
| Two real Macs; another manufacturer's phone | no | the owner's trial with v0.4.0 |

## Starting point

- `main` at `37815ca`; API `0.30.0` (`internal/domain/system/system.go`); releases `v0.1.0`–`v0.3.0`.
- `internal/hosting`: `Identity` (ADR-0098 §1); `Host.Start` dials each `remotes` entry once (`links` fixed at start); a `Link` uses its one `remote.Address` for the WebSocket, screen, upload and artifact dials (`link.go`, `screen.go`, `upload.go`, `artifact.go`). `Uploads`.
- `adapters/peer`: one `Server` binding `Options.Listen`, read once at start (`serve.go` `startPeers`); TLS 1.3, `RequireAnyClientCert`, `VerifyConnection` refusing an unpinned certificate at the handshake; four routes; `serveOnce` ends **every** peer session (`Sessions.EndAll`) when its listener stops.
- `internal/policy/hosting.go`: `hosting {listen, peers, lend}`, `remotes [{name, address, fingerprint}]`, validated whole (ADR-0032 §4); `Lent` re-read at each call.
- `cmd/droidyard/hosting.go`: `id` (through the daemon), `host`, `remote` (direct writes through `policy.Update` with `ifRevision`, no daemon); `host listen` and `remote add|remove` say a restart applies them.
- `internal/domain/device`: the `Lending` seam (`target.go`), the `Remotes` seam (`remote.go`); `list.go` hides an unadmitted physical device from every caller; `Device` has no manufacturer.
- `internal/daemon/native.go`: a disconnect abandons waits, never operations (ADR-0020 §Adapter projections); a native session has no event session (only `adapters/httpapi/ws.go` makes one). Nothing tells the core a connection ended.
- **Baseline, 2026-10-19 at `37815ca`:** `go vet ./...` clean. `go test ./...` ok in every package but `cmd/droidyard`, where `TestCtrlCDuringAutoStart` failed once ("exited before printing interrupted"), the register's "Load-sensitive tests" row; alone it passed 5 of 5 (`-count=5`). This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its row.

## Acceptance

- [ ] A1. Pairing by code: an unpaired certificate reaches nothing but `POST /peer/v1/join`, and that only with an unused code; a used, void or wrong code admits nothing; every other unpaired handshake is refused as today; the client pins the code's fingerprint before it sends the secret; the host keeps only the secret's SHA-256. Source: owner (1), (2); Q1. Proven by: steps 1, 3, 5.
- [ ] A2. Live configuration: a change to `hosting.listen` or `remotes` takes effect within 3 s with no restart; the share listener binds only private (VPN, LAN) or named addresses, never an unspecified, public or link-local one, on 7318 or a remembered port; an address that goes away closes only its own sockets; a remote is dialled over its address list, own subnet first. Source: owner (3); Q3. Proven by: steps 2, 7.
- [ ] A3. `share -d <serial>` admits the serial, lends it, opens the listener and prints one `droidyard join <code>` line; `join <code>` pairs both sides, writes the remote and `<remote>/<serial>` to the client's policy, and the client's next `device.list` holds the device, with no restart and no name asked. Source: owner (1), (2). Proven by: steps 3, 7.
- [ ] A4. A foreground share prints joins and leaves; Ctrl-C, SIGHUP, a CLI killed with `kill -9` and `unshare` from another terminal each end it, and the client loses the device and its streams within 2 s; it never enters `hosting.lend`. Source: owner (1); Q2. Proven by: steps 4, 7.
- [ ] A5. `share` with no serial lists physical devices only, `unauthorized`, `offline` and a duplicated serial as unpickable; one shareable is shared, several print the list and exit 2, with or without `-d`. Source: owner (1); ADR-0103 §1. Proven by: step 4.
- [ ] A6. A `-d` share survives a daemon restart: an unused code still joins, a joined client relinks with no code; a foreground share does not survive; `share` of a shared serial gives a fresh code on the same share; `unshare [<serial>]` voids its codes. Source: owner (1); Q2. Proven by: steps 3, 5, 7.
- [ ] A7. `droidyard device allow|disallow <serial>` changes the physical policy with no file edited by hand and no restart. Source: owner (4). Proven by: step 6.
- [ ] A8. Human only and the schema diff: no `hosting_*` MCP tool and `tools/list` unchanged; an MCP or peer call of a `hosting.*` capability refused; the diff is one minor version, four capabilities, two codes, three policy keys and two health fields. Source: ADR-0015 §6; `capability-api.md` §Discovery and versioning. Proven by: steps 2, 3, 4.
- [ ] A9. On this Mac, two daemons: the host shares the stand-in on its LAN address (and is bound on its VPN address too), the client joins with the printed line, the brief's loop works on the device, and Ctrl-C, `-d`, a restart, `unshare`, a second code for a second client and a reused code behave as A1–A6. Source: the brief. Proven by: steps 3, 7.
- [ ] A10. The owner's Samsung shared and joined between two daemons on this Mac; `v0.4.0` published and installed by the one-line installer. Source: owner (5). Proven by: step 8.

## Run rules (steps 1, 3, 7, 8: their level-5 parts are the main session's)

- **K1.** Two (step 7: three) isolated daemons by `verify-daemon` §Launch, each with its own `R` (`/tmp/dy2uh.*` host, `/tmp/dy2uc.*`, `/tmp/dy2ud.*` clients), built at the step's commit. The host's `hosting.port` is a free lane port, so no other lane's daemon is met on 7318 (the default is proven at level 4). Evidence under `B=~/Library/Caches/droidyard-dev/2u/`.
- **K2.** The lane's own scratch AVD on its own adb port (memory "lane own AVD and adb port"); the host's `ADB` is m2t's guard for that serial only, appending ` usb:2u-standin` to its `devices -l` line, so `share` lists it as physical (the stand-in), and filters `devices -l` to that serial alone, so neither the listing nor its `getprop` reaches the owner's phone (2t's `adb-host` does both); the clients' `ADB` answers `devices -l` with the header alone and logs every other command. The owner's adb server pid recorded before and after.
- **K3.** `go test` binds loopback only: every test names `127.0.0.1` (`--on 127.0.0.1`, an injected interface list). Only the main session binds a VPN or LAN address. Nothing killed by name. The owner's phone only at step 8, with the owner present and their yes. The implementer runs no device phase.

## Steps

### 1. The single-use code and the join exchange (no capability yet)

- Builds:
  - `internal/hosting/code.go`: `Code{Addrs []Addr{IP, Kind vpn|lan}, Port, Fingerprint [32]byte, Secret [16]byte}`; `Encode` → `dy1.` + unpadded base64url of `version 1 | count | per address (kind, family, bytes) | port | fingerprint | secret` (about 90 characters for two IPv4 addresses); `ParseCode` refuses another prefix or version, a wrong length, no address or more than 8, an unspecified, multicast or public address (loopback allowed), port 0.
  - `internal/hosting/joins.go` `Pending`: per device at most 8 unused codes (a ninth voids the oldest), each kept as the SHA-256 of its secret only; `Issue(device)`, `Consume(secret) (device, ok)` comparing in constant time and removing the match under one lock, `Void(device)`, `Any()`.
  - (a) **Spike first (main session, K1, no new code):** with the 2t binary, a host daemon's `host listen 100.64.0.18:<lane port>` and a restart, then the LAN address; a client daemon on this Mac (`remote add`, restart) links through each; what macOS's firewall did (`socketfilterfw --getglobalstate`, any prompt) recorded. Stop rule: if this Mac cannot reach its own 100.64.0.18, the VPN legs of steps 3 and 7 are labelled and left to the owner's two-Mac trial.
  - `adapters/peer` (reaching `Pending` through its `Host` interface, as it reaches `Pin`): `GetConfigForClient` reads `ClientHelloInfo.SupportedProtos`. A hello offering `droidyard-join/1` takes one of 4 join slots (none free → the handshake fails), held by that TCP connection and freed when the connection closes, whatever the handshake's outcome (a wrapped `net.Listener` whose `Conn.Close` releases it), so a failed or abandoned handshake never keeps one, and gets a config offering that protocol alone, whose `VerifyConnection` accepts a pinned certificate, and an unpinned one only while `Pending.Any()`; every other hello keeps today's config, which offers `http/1.1` only, so a hello offering only the join protocol fails there. `TLSNextProto["droidyard-join/1"]` (HTTP/2 stays off) serves exactly one HTTP/1.1 request on that connection with a handler holding `POST /peer/v1/join` alone, under its own 10 s deadline, then closes the connection: an unpinned certificate reaches nothing else by construction. The request is read with `http.ReadRequest` on the `*tls.Conn` (an inner `http.Server` would see the negotiated protocol and close the connection as the outer one does). The join: body at most 4 KiB, `{secret, name, api}`; `Consume`; on success the `Admit(fingerprint, proposedName, device) (peer, host string, err)` seam (a test double here, step 3's policy write later) and `{host, device}` back; on failure 403 with no detail and a `daemon.log` line with the address and fingerprint prefix. A pinned peer joins the same way (a paired client joining a new share, Q2 §5). `PeerJoinPath`, `PeerJoinALPN` and the wire types in `internal/capability/peerwire.go`.
  - `internal/hosting` `Join(ctx, id, code, name)`: dials the code's addresses in `Order` (step 2 adds it; here in the given order), 3 s each; TLS 1.3 with ALPN `droidyard-join/1`, the identity presented, the host pinned to the code's fingerprint in `VerifyConnection` before any request byte (as `Link`). A pin mismatch is that address's failure and the next is tried (a private address may be another machine on the client's own network); `notThisHost` only when no address presents the pinned certificate. Returns the answer, the address that answered and each other address's failure.
  - `hosting.NameFrom(hostname, taken)`: lowercase, the part before the first dot, other runs to `-`, `m-` before a leading non-letter, cut to 32, then `-2`, `-3`… until not taken; always matches `policy.PeerNamePattern`.
- Implements: Q1 §1–§5.
- Check: real TLS on loopback, in `adapters/peer` and `internal/hosting`:
  - `TestJoinOnlyWithAPendingCode`: an unpaired certificate with the join ALPN and no pending code → the handshake fails (seen failing with the `Any` check removed); a wrong secret → 403 and `Admit` never called; the right one → `Admit` once, with the presented certificate's fingerprint; the same secret again → 403 (seen failing with `Consume` not removing); two concurrent joins with one secret → one admitted (`-race`).
  - `TestUnpairedReachesOnlyJoin`: a join connection asking `/peer/v1/ws`, `/peer/v1/screen`, `PUT /peer/v1/upload`, `/peer/v1/artifacts/x` → 404 from the join handler, no session started (seen failing with the join connection handed to the main mux); a second request on the same join connection is not served; an unpaired certificate without the join ALPN while a code is pending → refused at the handshake (seen failing with the ALPN check removed); a fifth concurrent join hello → refused at the handshake, and a slot is free again once a join returns (seen failing with the slot not released); four join hellos whose handshakes fail (no code pending; a client aborting on its own pin), then a valid join → admitted (seen failing with the slot released only when the join handler returns).
  - `TestPairedPeerJoins`: a pinned certificate with a valid code → `Admit` called with its fingerprint (seen failing with pinned certificates refused on the join route).
  - `TestJoinPinsTheHost`: a code naming another fingerprint → the listener's join handler is never reached and `Join` reports `notThisHost` (seen failing with the client pin skipped); the code's first address an unrelated TLS server, the second the host → joins (seen failing with the first mismatch ending the join).
  - `TestCodeRoundTrip`, `TestParseCodeRefuses` (each refusal), `TestPendingKeepsOnlyHashes` (the secret's bytes absent from `Pending`'s state), `TestNameFrom` (`Jorges-MacBook-Pro.local` → `jorges-macbook-pro`; `1mac` → `m-1mac`; taken → `-2`; 40 characters cut; a fuzz seed set, every output matching the pattern). `TestPeerLink` unchanged.
- Schema: no change (the peer protocol is internal, ADR-0098 §4).
- Needs: nothing.
- Not here: capabilities and policy writes (3); the address set and ordering (2).
- Rests on: an unpaired certificate reaches only the join route, and only with an unused code; join connections are served through `TLSNextProto` (Go closes a connection whose negotiated protocol has no entry there, `net/http/server.go`), and a normal hello offering only `droidyard-join/1` fails. Level 4 (real TLS).
- Review: the Opus verifier (security), effort `high`.
- [x] Done. Evidence: level 4, real TLS on loopback. (a) the spike, §Changes. `TestJoinOnlyWithAPendingCode`, `TestUnpairedReachesOnlyJoin`, `TestJoinSlots`, `TestFailedHandshakesFreeTheirSlots`, `TestJoinConnectionIsBounded`, `TestPairedPeerJoins`, `TestHelloOfferingOnlyJoinFailsWithoutAJoinHandler`, `TestJoinPinsTheHost`, `TestCodeRoundTrip`, `TestParseCodeRefuses`, `TestPendingKeepsOnlyHashes`, `TestNameFrom`; ten reversals seen failing (the `Any` check, `Consume` not removing, the main mux, the ALPN check twice, the slot unreleased or released on handler return, pinned peers refused, the client pin skipped, the first mismatch ending the join, the raw secret stored); `go test -race` on both packages green. Opus verifier (high): fix first, one [test] blocker (the count refusals could not fail, a mutation passed them), fixed and seen failing with the count check removed; one [bug] (a failure after the pin tried the next address) fixed; [doc] the slot bound, fixed; [safety] and [plan] notes carried to step 3; five [nit].

### 2. Live peer configuration: the listener on the private addresses, the remotes, an address list

- Builds:
  - `internal/policy`: `hosting.on` (`private`, `vpn`, `lan`, or an IP literal that is private, 100.64.0.0/10 or loopback; absent: no share listener, so a 2t configuration that lends with no `listen` keeps binding nothing; the first `share` writes it); `hosting.port` (1–65535; absent, 7318 is tried and the first bind writes the port it got); `remotes[].addresses` (optional, at most 8, each `ValidRemoteAddress`); in `policy.get` and `policy.update`; the descriptions of `hosting.listen` and `remotes` lose "read at daemon start".
  - `internal/hosting/addrs.go`: `Private(ifaces, on)`: the IPv4 addresses of up interfaces, each tagged: `vpn`, an address in 100.64.0.0/10 or RFC 1918 on a point-to-point or `utun*` interface; `lan`, an RFC 1918 address on any other interface (100.64.0.0/10 there is carrier-grade NAT, shared with strangers, and left out); never unspecified, link-local, multicast, public, nor loopback unless `on` names it; at most 8. `Order(addrs, own)`: a `lan` address inside one of `own`'s prefixes first, then the `vpn` ones, then the rest, stable.
  - `adapters/peer` `Server`: a set of listeners keyed by address. `Reconcile(want []netip.AddrPort)` binds the new, closes the gone; closing one ends only the sockets it accepted (today `EndAll`); a failed bind leaves the others, is reported per address and retried at the next reconcile. The set wanted: `hosting.listen` when set (its meaning unchanged, now live); else, when `hosting.on` is set and this host lends anything (a `hosting.lend` entry; a foreground share from step 4), `Private(ifaces, on)` on `port`; else none. Port: with `hosting.port` absent, 7318 is bound on the first address, or a free port when 7318 is busy, then reused for the others, and written to `hosting.port` (`policy.update`, `ifRevision`), logged. Once written it is never replaced automatically: a busy port is a reported bind error, retried at the next reconcile, so paired clients keep finding the host. The per-listener state that is server-wide today (`closing`, `screens`, `peer.go`) becomes per listener.
  - `internal/hosting` `Host`: links reconciled from `remotes` (added → started; removed, or its fingerprint changed → closed; its address list changed → updated in place, the link kept); each attempt dials `address` and `addresses` in `Order` and keeps the one that answered for its screen, upload and artifact requests.
  - `cmd/droidyard/serve.go`: one reconcile of both every 2 s (the configuration, and `net.Interfaces` through an injected lister), and on demand (`Host.Reconcile`, for step 3). `system.health` `hosting`: `listening: [ip:port]` and per-address `errors`; `listen` and `error` are kept (the first bound address, the first error) and redescribed (no "tried again every 30 s"). `host listen`, `remote add|remove`: no "a restart applies it".
- Implements: Q3.
- Check:
  - `TestLivePeerConfig` (`cmd/droidyard`, two real `serve` processes, loopback): `policy.update` of the host's `hosting.listen` → bound within 3 s with no restart (seen failing with `listen` read at start); with `listen` unset, a `lend` entry and no `hosting.on` bind nothing (2t's behaviour), then `hosting.on: "127.0.0.1"` binds within 3 s, and an empty `lend` closes it; the client's `remote add` → linked within 3 s, `remote remove` → closed within 3 s (seen failing with `remotes` read at start); a remote whose `address` is a closed port and whose `addresses` holds the live one links (seen failing with `addresses` ignored).
  - `TestListenerSetShrinks` (`adapters/peer`, two listeners on two `127.0.0.1` ports, a peer linked on each): the set loses one → that peer's link closes, the other stays (seen failing with `EndAll`); the set gains it back → bound.
  - `TestRemoteAddressesUpdatedInPlace` (`internal/hosting`): an entry gaining `addresses` keeps its link up and its in-flight call answered (seen failing with a changed entry closed).
  - `TestReconcileFollowsInterfaces` (`cmd/droidyard`, an in-process daemon with an injected interface lister holding `127.0.0.1` as the only allowed address under `on: "127.0.0.1"`): the lister drops it → the listener closes within 3 s; it returns → bound again (seen failing with interfaces read once).
  - `TestPrivateAddresses`, `TestOrderPrefersOwnSubnet`: 100.64.0.18 on `utun8` and 10.221.240.50 on `utun9` (`vpn`); 100.64.0.18 on `en0` never; 192.168.1.20, 10.0.0.5, 172.16.3.4 on `en0` (`lan`); 172.32.0.1, 8.8.8.8, 169.254.1.1, 0.0.0.0, 224.0.0.1 never; each `on`; a ninth address dropped; a client on 192.168.1.0/24 orders 192.168.1.20 (`lan`) before 10.221.240.50 and 100.64.0.18 (`vpn`) before 10.0.0.5 (`lan`).
  - `TestPortBusyIsRemembered` (the default port injectable, a test port held by another socket): bound on a free port and `hosting.port` written once (seen failing without the write); with `hosting.port` written and then held → a bind error in health, the port not replaced (seen failing with the replacement kept). `TestHostingConfig`: rows for `on`, `port`, `addresses`.
- Schema: version `0.31.0`; `policy.get`/`update`: `hosting.on`, `hosting.port`, `remotes[].addresses`; the descriptions of `hosting.listen` (no "read at daemon start", no "Absent: this daemon lends nothing") and `remotes`; `system.health`: `hosting.listening`, `hosting.errors`, and the descriptions of `hosting.listen` and `hosting.error`. Nothing else.
- Needs: nothing from step 1.
- Not here: shares (3, 4); IPv6 private addresses and a host telling paired clients its new addresses (Not in this milestone).
- Rests on: closing one address's listener ends only its own sockets. Level 4.
- Review: the Opus verifier (process lifetime, concurrency), effort `high`.
- [x] Done. Evidence: level 4 and level 5 (step 3's run). `TestLivePeerConfig`, `TestListenerSetShrinks`, `TestRemoteAddressesUpdatedInPlace`, `TestRemoteAddressesAreDialledInOrder`, `TestReconcileFollowsInterfaces`, `TestPrivateAddresses`, `TestOrderPrefersOwnSubnet`, `TestPortBusyIsRemembered`, `TestHostingConfig`, `TestPrivateListenersFollowLending`, `TestLearntPortNeverReplacesAWrittenOne`; reversals seen failing for each named decision. Opus verifier (high): proceed; [bug] a learnt port over one written meanwhile and [plan] the usage text, both fixed; [bug, non-blocking] shutdown waits for handlers under the reconcile lock (noted for step 3: no reconcile from a peer handler); five [nit]. On this Mac the host bound 192.168.0.118 (lan) and 100.64.0.17 (vpn, Tailscale on utun) on its written port.

### 3. `share -d` and `join`, end to end (the first observable result)

- Builds:
  - `device` package `ADB.Shareable(ctx)`: the devices of `adb devices -l` whatever the physical policy says, each with kind (ADR-0103 §2), state, model and whether its serial is listed twice; read by `hosting.share` only.
  - `internal/domain/sharing` (new; registers `hosting.share`, `hosting.unshare`, `hosting.join`: global commands, audience `human`, not `Lendable`; every test share passes `--on 127.0.0.1`, K3):
    - `hosting.share {device, detach, on?}`: the device physical, online and listed once in `Shareable` (else `DEVICE_NOT_FOUND`, "only physical devices are shared", for an absent device or an emulator; `DEVICE_NOT_READY` for `unauthorized` or `offline`; `DEVICE_NOT_ALLOWED` for a duplicated serial, ADR-0103 §1). The addresses `on` allows are found first (none, or no identity → `HOSTING_UNAVAILABLE`, nothing written). One `policy.update` with `ifRevision` (retried once): `devices.allowPhysical: true`, the serial in `devices.allowlist`, `hosting.on` (`--on`'s value, else `private` when absent), and a `hosting.lend` entry (an existing one keeps its `to`). `Host.Reconcile`; if no address is bound then, the `lend` entry is removed again and the call fails `HOSTING_UNAVAILABLE` with each bind error. `Pending.Issue`; the code carries the addresses bound (with `hosting.listen` set, its address alone); result `{device, code, addresses, port, detached, notes}`, the notes being ADR-0099 §4's whole-device sentence, that the serial is now allowed for this Mac's own agents too, and, when `allowPhysical` was false, that every serial already in the allowlist is admitted with it. Step 3 accepts `detach: true` only.
    - `Admit` (step 1's seam): a fingerprint already in `hosting.peers` keeps its name, else `NameFrom(proposed, taken)`; one `policy.update`: the peer, and its name in the device's `lend` `to`. A device no longer lent refuses (its codes are void).
    - `hosting.unshare {device?}`: the device's `lend` entry removed (every entry without `device`), its codes voided; peers stay in `hosting.peers`.
    - `hosting.join {code}`: `ParseCode` (`REQUEST_INVALID_PARAMS`, nothing dialled); an unusable client configuration → `POLICY_CONFIG_UNUSABLE`, nothing dialled; `hosting.Join` with `NameFrom(os.Hostname())`; the host's answer is untrusted (ADR-0100 §4): its `host` goes through `NameFrom`, its `device` must be a non-empty serial with no `/`, no control or space character and at most 64 bytes, or the join fails `refused` and nothing is written; then one `policy.update`: the remote (an entry with the code's fingerprint keeps its name and gains the addresses; else the cleaned name, unique), `devices.allowPhysical: true`, `<remote>/<serial>` in the allowlist; `Host.Reconcile` and up to 5 s for the link; result `{remote, device, connected}`. A failure before the host answers is `HOSTING_JOIN_FAILED` with `details.reason` `unreachable` (each address's error in the cause), `notThisHost` or `refused`, and writes nothing. A write that fails after the host admitted the client leaves the code used and the host paired: the error says so, and a fresh code from the host repairs it under the same names.
  - `cmd/droidyard`: `share -d <serial> [--on vpn|lan|<ip>]`, `unshare [<serial>]`, `join <code>`, each one call through `connectOrStart` as `id`; `share` prints the device, the addresses, the notes, and last `droidyard join <code>` on a line of its own; `join` prints the device id and that it is listed now. Usage text.
  - `daemon.log`: a join (peer, fingerprint prefix, address, device) and a refused join.
- Implements: Q1 §6, §7; Q2 §1, §2, §4, §5; ADR-0015 §4 (the human's command is the consent), §6; ADR-0099 §1, §4; ADR-0100 §2.
- Check:
  - `TestShareAndJoin` (`cmd/droidyard`, two `serve` processes, `devicetest` on each, the host `--on 127.0.0.1` on a free port):
    - `share -d R58M001` prints the line; the host's policy gains `allowPhysical`, the allowlist entry and the `lend` entry; `system.health` lists the listener;
    - `join <code>` exits 0; the client's next `device.list` holds `<name>/R58M001` (`physical`, `host`) with no restart (seen failing with `Host.Reconcile` not called); the host's `hosting.peers` holds the client's `droidyard id` fingerprint and `lend.to` its name; `uiTree` on it reaches the host fake as `-s R58M001`;
    - the same code from a third daemon C → `refused`, C's and the host's policies unchanged (seen failing with single use removed); `share -d R58M001` again → another code, C joins it and is named with `-2` (one hostname for all three);
    - a code edited to name C's fingerprint → `notThisHost`, C's log has no join line; a truncated code → `REQUEST_INVALID_PARAMS`, nothing dialled;
    - `unshare R58M001` → the client's next `device.list` lacks it; a code issued before → `refused`; both peers still in `hosting.peers`; `share -d R58M001` again and the first client joins the new code → the same name on both sides, lent again, no second remote entry (seen failing with a pinned certificate refused on the join route);
    - a host fake answering a `host` of `"../Évil Host"` and a `device` with `/` (a test seam on the join handler) → the name cleaned to the pattern, the bad device `refused` with nothing written;
    - `share -d emulator-5554` → `DEVICE_NOT_FOUND`; an unauthorized serial → `DEVICE_NOT_READY`; nothing written.
  - `TestHostingCapabilitiesHumanOnly`: no `hosting_*` in an MCP session's `tools/list` (`TestToolListModelFacingBytes` unchanged); `hosting.share` from MCP refused by audience, from a peer `REQUEST_NOT_ALLOWED_HERE` (seen failing with the audience set to `all`).
  - **Level 5 (main session, K1–K2):** `share -d <stand-in>` with no `--on`: listening on the VPN and the LAN addresses, the code carrying both, tagged; the printed line pasted on the client: `device.list` shows it at once; `uiTree`, `launch` formapp, `tapElement` Like from the client's CLI; the client's log names the LAN address as the one that answered; again with `--on vpn`: bound on the VPN address alone, the join `unreachable` from this Mac (step 1 (a): no hairpin), labelled; the client's guard log holds no device command.
- Schema: `hosting.share`, `hosting.unshare`, `hosting.join` (human); codes `HOSTING_JOIN_FAILED`, `HOSTING_UNAVAILABLE`; `tools/list` unchanged.
- Needs: steps 1, 2.
- Not here: the foreground share and the listing (4); pending codes across a restart (5).
- Rests on: a fingerprint enters `hosting.peers` only through a consumed code or `host admit`. Level 4.
- Review: the Opus verifier (contract, security), effort `high`.
- [x] Done. Evidence: level 4 (`TestShareAndJoin` with three real daemons, `TestJoinTakesTheHostsAnswerAsUntrusted`, `TestAdmitThatFailsSpendsTheCode`, `TestJoinWithAnUnusableConfigurationDialsNothing`, `TestHostingCapabilitiesHumanOnly`; eight reversals seen failing) and level 5 (`/evidence/step3/run.txt`, 2c3da8f): `share -d emulator-5584` with no --on listened on 192.168.0.118:47331 (lan) and 100.64.0.17:47331 (vpn) and wrote the physical policy; the client pasted the line, joined over the LAN, and its next `device.list` held `macbook-air-jorge-tortosa/emulator-5584`; `launch` formapp, `uiTree` Likes 0, `tapElement` Like, Likes 1, every call a `peer=` trace on the host; the line again: refused; `--on vpn`: bound on 100.64.0.17 alone and the join went through over Tailscale (this Mac reaches its own Tailscale address, not its company-VPN one, step 1 (a)); the client's guard ran no device command, the host guard refused nothing. Opus verifier (high): fix first, one [safety] blocker (a joiner could take a name lent to before admission, so a code for R1 handed over R2), fixed with a test seen failing; two [safety] and a [plan] non-blocking, fixed (§Changes); one more bug found while fixing (an empty `to` written back as null), fixed with a test seen failing; five [nit].

### 4. The foreground share and the device listing

- Builds:
  - `hosting.share` with no `device`: result `candidates`, nothing shared: step 3's `Shareable` with emulators left out (ADR-0099 §1), each with transport (`usb` for a `usb:` field, else `tcp`), manufacturer (`getprop ro.product.manufacturer` for an online device listed once, within `detailsTimeout`; never for a duplicated serial, which adb cannot address), model and state; `unauthorized`, `offline` and a duplicated serial marked unshareable with the reason.
  - `hosting.share {device, detach: false}`: a share in memory, never in `hosting.lend`: the device package's `Lending` becomes policy's `Lent` or a foreground share's `to` (in `sharing`), and the listener's "lends anything" counts it. It belongs to the connection that made it: `internal/daemon/native.go` tells the core when a native connection ends (`Operations.Disconnected(originID)`, hooks registered at wiring), and the share ends then; a foreground share asked by any other client kind (the UI's WebSocket, plain `POST /v1/call`) → `REQUEST_INVALID_PARAMS` ("pass detach"), so the WebSocket hook comes with the UI. `share` of a serial already shared, in either mode, issues a fresh code on that share and returns at once, naming its mode.
  - `hosting.watchShare {device}` (query, human, long-running): only on a foreground share owned by the caller's own connection (else `REQUEST_INVALID_PARAMS`), so it is bounded by that connection (ADR-0019 §Sessions: nothing runs unattended); runs until the share ends; its progress message names the share's peers and which are connected; its result says why it ended (`unshared`, `connection`, `stopping`).
  - CLI `droidyard share [<serial>] [-d]`: with no serial, the candidates one per line, `<transport>  <manufacturer model>  <serial>` and the reason when unshareable; exactly one shareable → shared, saying which; several → the list and exit 2; none → exit 1. Foreground: after the line, `hosting.watchShare` waited with `operation.wait` (`timeoutMs` 1000), printing `<name> joined`, `connected`, `left`; SIGINT, SIGTERM, SIGHUP → the CLI closes its connection (which ends the share; no `unshare`, so a `host lend` entry made meanwhile for the serial survives), prints "stopped sharing", exit 0; the watch ending `unshared` → says so, exit 0.
- Implements: Q2 §3, §6, §7 (ADR-0019 §Sessions and ADR-0010 §Decision superseded in part); the owner's decision (1).
- Check:
  - `TestForegroundShare` (`cmd/droidyard`, real CLI processes, two daemons, every share `--on 127.0.0.1`): a foreground `share R58M001` prints the line and stays; the client joins → `joined` printed within 3 s; SIGINT → exit 0, the client's `device.list` lacks the device and its open `watchLogs` stops `deviceGone` within 2 s (ADR-0104 §4); again with SIGHUP, with SIGTERM, and with `kill -9` of the CLI → the same within 2 s each (the last seen failing with the native hook removed); `unshare` from another CLI → the attached CLI says so and exits 0; a second `share R58M001` meanwhile → a fresh code and an immediate return; the host's `hosting.lend` never holds the foreground share; `watchShare` on a detached share, or from another connection → `REQUEST_INVALID_PARAMS` (seen failing with the ownership check removed); a foreground share over the UI's WebSocket → `REQUEST_INVALID_PARAMS`.
  - `TestShareListing` (fake adb, `--on 127.0.0.1`): `R58M001` (`usb:`), `R58M002` unauthorized, `emulator-5554`, two devices reporting `0123456789ABCDEF` → the listing holds R58M001 shareable with its manufacturer, R58M002 and the duplicate not, no emulator (seen failing with the kind filter removed); one shareable → shared; two → exit 2 and nothing written, also with `-d`.
- Schema: `hosting.watchShare` (`Stateless`, long-running, human); `hosting.share`'s `device` optional, its `candidates`, and its description naming the foreground share's tie to its CLI connection; `hosting.share` stays `Stateless` (Q2 §3).
- Needs: step 3.
- Not here: a share outliving its foreground connection (by design, Q2 §3); the UI.
- Rests on: a foreground share ends when its connection closes, however the CLI ends. Level 4 (a real CLI killed with `kill -9`).
- Review: the Opus verifier (process lifetime, concurrency), effort `high`.
- [ ] Done. Evidence:

### 5. A detached share survives a restart

- Builds: `<state>/hosting/shares.json` (`schemaVersion: 1`, `{shares: [{device, codes: [{sha256, issued}]}]}`; 0600 in 0700; written atomically as the store's files; a corrupt file renamed `.corrupt-<ts>`, reported in `system.health` `stateProblems`, its codes void, ADR-0021 §Format). Written when a detached share issues a code or stops, and when it consumes one **before** `Admit` answers: a consumption that cannot be written refuses the join (the code is then void in memory too); read at start; a record whose device has no `hosting.lend` entry is dropped, so `host unlend` voids its codes. Foreground shares are never written. At start the listener returns from `hosting.lend` on `hosting.port` (step 2).
- Implements: Q2 §2; ADR-0021 §Layout (`hosting/shares.json`, Q2's consequence).
- Check: `TestDetachedShareSurvivesRestart` (two daemons): `share -d R58M001`, the code unused, `droidyard stop` and a start of the host → the listener on the same port, the code joins (seen failing with the file not read at start), and after a second restart the client relinks with no code; a foreground share's host killed with `kill -9` and started → nothing lent and no listener; `host unlend R58M001` → a pending code `refused`; `shares.json` is 0600 and holds no secret byte; a corrupt file → quarantined, reported, codes void.
- Schema: no change.
- Needs: steps 3, 4.
- Not here: starting droidyard at login (Q4).
- Rests on: a consumed code of a detached share is on disk before the join is answered, so a crash cannot make it usable twice. Level 4 (`TestConsumedCodeIsWrittenFirst`: the write made to fail → the join refused and the code void after a restart; seen failing with the write after `Admit`).
- Review: the Opus verifier (security: single use across a restart), default effort.
- [ ] Done. Evidence:

### 6. `droidyard device allow|disallow <serial>`

- Builds: `cmd/droidyard/device.go`: `allow <serial>` sets `devices.allowPhysical: true` and adds the serial (a `<remote>/<serial>` too); `disallow <serial>` removes it, `allowPhysical` untouched. Both through `hosting.go`'s `apply` (one `policy.update`, `ifRevision`, retried once), no daemon started, as `trust`. Usage text.
- Implements: the owner's decision (4); ADR-0015 §4; ADR-0032 §5, §6.
- Check: `TestDeviceAllowCLI`: each verb's resulting `devices` section; "already allowed" writes nothing; an empty serial refused before any write; a concurrent edit retried once; a running daemon (fake adb) lists the device at its next `device.list` (seen failing with `allowPhysical` not set).
- Schema: no change.
- Needs: nothing.
- Not here: a UI toggle.
- Rests on: —
- Review: the main session's diff review.
- [x] Done. Evidence: level 4. `TestDeviceAllowCLI`: each verb's `devices` section, already allowed and an unlisted disallow write nothing (revision unchanged), an empty or spaced serial refused before any read, a concurrent edit retried once, a running daemon lists the device at its next `device.list`; seen failing with `allowPhysical` not set (four checks). Main session's diff review.

### 7. Level 5 of the brief on one Mac, and the documents

- Builds: no code but what the run finds. The main session, K1–K3:
  - (a) Foreground: the host's `droidyard share` (the stand-in the one physical device: no serial) bound on the VPN and LAN addresses, joined over the LAN; client A pastes the printed line; `device.list` at once; `uiTree`, `launch`, `tapElement`, a screenshot, the Live view in headless Chrome; the host's CLI prints the join.
  - (b) Ctrl-C on the host's CLI: A's view stops within 2 s and its list drops the device.
  - (c) `share -d`: A joins with the new line; `droidyard stop` and a start of the host: A relinks with no code and its view returns (a Mac restart stood in by a daemon restart, labelled).
  - (d) `share -d <same serial>` again: a fresh line; client B joins; B gets `DEVICE_OCCUPIED` naming A (ADR-0104); A's first line reused by a third client → `refused`.
  - (e) `unshare` with no serial: A and B lose it.
  - (f) The host's `daemon.log` join lines and `peer=` trace lines; the clients' guard logs hold no device command; the owner's adb server pid unchanged; what macOS's firewall did.
  - (g) Documents: `docs/interfaces/peer.md` (pairing by share and join beside the advanced verbs, the join route, the listener on private addresses, live); `http-websocket.md` §Security baseline rule 1 where it names `hosting.listen`; `adb-device.md` §Policy and safety (`share`, `join` and `device allow` write the physical policy); `docs/README.md` §Where the work stands; the register (Scope's rows; new rows from Not in this milestone and the run); the plans index; `doccheck`'s `liveBudget` if raised, with its reason.
- Implements: A2, A3, A4, A6 at level 5.
- Check: each item's decisive lines under `$B/evidence`.
- Schema: no change.
- Needs: steps 1–6.
- Not here: the phone (8).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

### 8. The owner's phone, and the release (the last step)

- Builds: no code but what the run finds. The main session:
  - (a) With the owner present and their yes, behind 2t's phone guard (`adb-phone`: that serial alone, uninstalls of formapp and the golden copy only): `R5CX62JXR3L` shared by `droidyard share` from the host daemon and joined by a client daemon, both on this Mac (K1); `uiTree`, `launch` and `tapElement` of formapp, a screenshot, the Live view; Ctrl-C ends it. Only formapp is installed or uninstalled; nothing of theirs is touched.
  - (b) At the close, after the merge (the owner's decision (5); ADR-0068): `v0.4.0` tagged on `main` and pushed; the release workflow green; `gh release view v0.4.0` lists five assets; the one-line installer into a scratch directory prints `droidyard v0.4.0 (API 0.31.0)`.
- Implements: A10.
- Check: the decisive lines under `$B/evidence/phone` and `$B/evidence/release`.
- Schema: no change.
- Needs: steps 1–7; the owner.
- Not here: two Macs (the owner's trial with v0.4.0).
- Rests on: —
- Review: the Opus verifier, on the evidence.
- [ ] Done. Evidence:

## Not in this milestone

- Share and join from the UI: register row "Pairing and lending from the UI" (owner, 2026-10-19).
- Starting droidyard at login: Q4; a register row.
- IPv6 private addresses (Tailscale's `fd7a:…`, ULA): IPv4 reaches both networks here; a register row.
- A host telling its paired clients a new address: the VPN address is stable and the code's list is kept; a register row.
- A re-shared device lent again to its earlier peers without a code: Q2 §5; a register row.
- Lockout or rate limiting of joins: Q1 §4 (128-bit secrets); bounded concurrency only.
- `host list` with last seen, the last good peer set when the configuration is unreadable: the narrowed Orca row.
- Two Macs, another manufacturer's phone, a real Wi-Fi switch: the owner's trial.

## Design questions

Each has a recommendation; the plan's steps are written against it and name it. Q1 and Q2 reverse parts of 2t's decisions on the owner's decisions of 2026-10-19; their wording is Delegated. Once ADR-0107 to ADR-0109 exist, Q1 to Q3 shrink to links.

### Q1. Pairing by a single-use code: [ADR-0107](../adr/0107-pairing-by-single-use-code.md)

- Blocks: steps 1, 3. Decided as drafted.

### Q2. A share's lifetime, its capabilities, its consent: [ADR-0108](../adr/0108-share-lifetime-and-consent.md)

- Blocks: steps 3, 4, 5. Decided as drafted.

### Q3. Private networks and live peer configuration: [ADR-0109](../adr/0109-private-networks-live-peer-config.md)

- Blocks: steps 2, 3. Decided as drafted.

### Q4. A `-d` share and a restart of the Mac

- Blocks: nothing (step 5 is written against the recommendation).
- The documents say: a daemon is auto-started by a command, and launchd units are "an optional post-MVP convenience" (ADR-0019 §4); the owner accepted "a `-d` share survives a daemon or Mac restart (persisted)". After a reboot nothing starts the daemon until a command does.
- Decided (Delegated, ADR-0108 §7): the share is persisted and resumes when the daemon next starts (`droidyard ui`, `id`, `share`, an MCP client; not `status`); `share -d` says so. Starting at login is a new host side effect (a launchd item), an ADR of its own and a register row. If the owner wants it in 2u, it is one more step and an ADR-D.

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| Join connections are served through `TLSNextProto`, and a normal hello offering only `droidyard-join/1` fails (read in Go 1.27's `net/http` and `crypto/tls` sources by the reviewer, not run) | 1, 3 | step 1's tests |
| A client reaches the host over a VPN: this Mac does not reach its own company-VPN address (step 1 (a)) | 3, 7 | the owner's two-Mac trial |
| macOS's firewall lets an ad-hoc signed build accept on the VPN and the LAN without a prompt per build | 1, 3, 7 | the owner's trial (this Mac's firewall is disabled) |
| A VPN's address is on a point-to-point or `utun*` interface, and carrier-grade NAT is not | 2 | this Mac: Tailscale on `utun8`, the company VPN on `utun9` (step 1 (a)); other VPNs the owner's trial |
| A Mac restart behaves as a daemon restart for a `-d` share | 5, 7 | a daemon restart only (labelled) |
| A real Wi-Fi change is seen within 5 s | 2 | an injected interface list only; the owner's trial |
| 128 random bits with no lockout suffice against guessing | 1 | a design argument, not a test |
| Another Mac's host name gives a useful name | 1, 3 | `NameFrom`'s table; the owner's trial |
| A vendor phone answers `getprop ro.product.manufacturer` | 4 | step 8, one phone |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: Go closes a connection whose negotiated protocol has no `TLSNextProto` entry (`net/http/server.go` 2008–2016, checked), so the join route as designed never served | Step 1: `TLSNextProto["droidyard-join/1"]` serves one request with a join-only handler and its own 10 s deadline; the gate holds by construction; Rests on and Unverified row 1 rewritten |
| 1 | revise | [blocking] B2: a pinned peer got 404 on the join route, so Q2 §5's rejoin and step 7 (c) failed | The join accepts pinned certificates; `TestPairedPeerJoins` (step 1) and a rejoin row in `TestShareAndJoin` (step 3), each seen failing with the refusal kept |
| 1 | revise | [blocking] B3: ADR-0107 also changes ADR-0098 §3 and ADR-0108 ADR-0015 §4 (unfiltered listing, `getprop` on unadmitted devices) | Q1 supersedes ADR-0098 §2 and §3 in part; Q2 adds ADR-0015 §4 in part; no `getprop` for a duplicated serial |
| 1 | revise | [blocking] B4: `hosting.watchShare` on a detached share ran unbounded (ADR-0019 §Sessions) | Allowed only on the caller's own foreground share, ending with its connection; Q2 §3 and §7 cite ADR-0019 §Sessions; a test row |
| 1 | revise | [should] S1 single use across a crash; S2 a pin mismatch at one address; S3 a replaced port strands clients; S4 an address change closed the link; S5 tests binding real addresses, no loop test | Step 5 writes a consumption before answering (Rests on, a test); `notThisHost` only when no address pins, a test row; the port written at the first bind and never replaced (Q3 §2, a test row); addresses updated in place (a test); every test share `--on 127.0.0.1`, `TestReconcileFollowsInterfaces` |
| 1 | revise | [should] S6 join cap and the code as a bearer; S7 codes under `hosting.listen`; S8 the health and policy descriptions; S9 100.64.0.0/10 as carrier-grade NAT; S10 the host's answer untrusted, a failure after admission; S11 the self-reach spike first | Slots counted from the hello, Q1 §4 states the consequences; the code carries `listen`'s address; step 2's Schema lists every description; `vpn` only on a tunnel interface, an Unverified row; `NameFrom` and a serial check on the answer, the after-admission case stated, a test row; the spike is step 1 (a) |
| 1 | revise | Cuts: the WebSocket hook; the `tcp` label. Nits N1–N5 | WebSocket hook cut (foreground from the CLI only); the transport label kept (the owner's line format names `usb`). N2: Ctrl-C closes the connection, no `unshare`; N3: the audience mutation and SIGHUP/SIGTERM rows; N4: the default port injectable; N5: the phone guard named, K2's filter explicit. N1: the size stays until approval, when Q1–Q3 shrink to links |
| 2 | revise | B1, B2, B4 resolved as stated; B3 resolved for the two clauses it named (checked against Go 1.27's `net/http/server.go` 964–983, 2007–2016 and `peer.go`) | — |
| 2 | revise | [blocking] N1 (from B1): a join slot freed only when the handler returns, so four failed handshakes locked the join route out until a restart | The slot is held by the TCP connection and freed at its close whatever the handshake did (a wrapped listener); a test row: four failed join handshakes, then a valid join admitted, seen failing with release on handler return |
| 2 | revise | [blocking] N2 (from B4): a foreground share owned by a CLI connection contradicts ADR-0019 §Sessions and ADR-0010 (session state on the WebSocket only, `SESSION_REQUIRED` elsewhere), cited as support; `Statefulness` unstated | Q2 supersedes ADR-0019 §Sessions and ADR-0010 §Decision in part, its §3 says so instead of citing them as support; step 4's Schema states both capabilities stay `Stateless` and why (no event-carrying session needed), the tie said in the description |
| 2 | — | [non-blocking] an inner `http.Server` on the join connection would close it as the outer one does | Step 1: the request read with `http.ReadRequest` |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-19, step 1 (a), Delegated. The spike (`$B/evidence/spike1a*`): Tailscale's 100.64.0.18 (`utun8`) was gone by the run; the owner's company VPN is up on `utun9` as 10.221.240.50/24, RFC 1918, carrying the default route. A 2t host bound on 192.168.0.118 linked a client on this Mac (`connected: true`); bound on 10.221.240.50 it listened, but the client's dial timed out (`nc` too): the VPN does not hairpin a Mac's own address. This Mac's firewall is disabled, so no prompt can be seen. Changes: a VPN is recognised by its interface (a tunnel), not by 100.64.0.0/10, since a company VPN uses RFC 1918; the code tags each address `vpn` or `lan` and the client orders by tag; ADR-0109 §1, §4 and ADR-0107 §1 say so (both written in this milestone, not yet on `main`, corrected in place). The VPN leg of every level-5 run is bound and shown in the code but joined over the LAN; a join over a VPN is the owner's two-Mac trial.
- 2026-10-19, step 1, Delegated (the implementer's departures, judged by the verifier). `Join` takes the client's API version (the body's `api`; the host does not read it yet). `notThisHost` only when every address presented another certificate; a mix with an unreachable address is `unreachable`, since that address may be the host. Once an address pinned the host, `Join` stops there: a lost answer is `unreachable` (the host may have used the code), the host's refusal `refused`. A body over 4 KiB is 413, a malformed one 400, before the secret is read; both spend no code. `ParseCode` refuses link-local addresses. When `Admit` fails the code is already spent (consumed first): the host needs a fresh code, which step 3 records and tests. ADR-0107 §4's bound of a join slot is about 22 s, not 10 s (corrected in place).
- 2026-10-19, step 2, Delegated (judged by the verifier). `Run` gives way to `New`/`Reconcile`/`Close`; an unusable configuration or an unreadable interface list leaves listeners and links as they are (handshakes are refused meanwhile: `Pin` fails); the port is learnt by a probe; own prefixes include loopback; a remote with several addresses is dialled 3 s each. Review fixes: a learnt port is never written over one written meanwhile (`TestLearntPortNeverReplacesAWrittenOne`, seen failing without the check); the usage text of `host` and `remote` no longer asks for a restart.
- 2026-10-19, step 3, Delegated. A `hosting.lend` entry may have an empty `to` (a detached share waits for its first join); an entry without `to` is still refused, and `host revoke` of a share's last peer still drops the entry. `share` without `-d` is refused until step 4. When nothing binds, the lend entry is removed only if this call made it. Without `--on` an existing `hosting.on` is kept. A write that fails after the host admitted the client keeps the policy error code, its message saying the code is spent and the host paired. `Admit`'s errors are fixed text, never the proposed name.
- 2026-10-19, step 3's review, Delegated. A joiner is never given a name that any `hosting.lend` entry's `to` holds (a name lent to before it was admitted would have handed over that device too; `TestAdmitNeverTakesALentName`, seen failing). `host revoke` keeps an entry left with no one (lent to nobody, until `unlend` or `unshare`), so revoking a share's last peer no longer ends the share. The policy's copy of an empty `to` was nil and was written back as null, refused: any rewrite of `hosting.lend` after a share no one joined failed (`TestEmptyLendSurvivesARewrite`, seen failing). A failed share and a join that turns `allowPhysical` on now say what they admitted (step 4's commit).
- 2026-10-19, step 6. `disallow` of a serial that is not listed says so and writes nothing; a serial with whitespace is refused like an empty one.

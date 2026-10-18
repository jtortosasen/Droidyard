# Plan: Milestone 2t — C4a, device hosting

- Status: Approved
- Approved: 2026-10-18, Delegated (the main session under the owner's standing instruction to apply the recommended option, and the owner's "procede hasta que termines", 2026-10-18): Q1 to Q7 as recommended, after two review rounds. Q1's reopening is the owner's (2026-10-18), its wording Delegated; Q6 reverses part of ADR-0030, an owner decision, and is Delegated on the owner keeping its row for 2t: the owner may revert it, and with it step 8. The owner may revert any.
- Date: 2026-10-18
- Milestone: "2t: C4a, device hosting — lend physical devices to a paired Droidyard over the VPN" (owner, 2026-10-18; `docs/vision/beyond.md` §The capabilities, C4, its first slice).
- Phase: none. The MVP is done (2h); the owner's use case, the shape agreed with the owner and the ten register rows under Scope are the requirement.
- Contract: Q1 to Q7 (ADR-0097 to ADR-0103, numbered by the main session at approval; ADR-F is written after step 8 (a), as ADR-0077 was from m2p step 1). Also ADR-0010, ADR-0012 §Reading artifacts, ADR-0015 §1, §4, §6, §8, ADR-0019 §7, §Sessions, ADR-0020 §Operation capabilities, ADR-0021 §Layout, ADR-0028, ADR-0030, ADR-0032 §2, §4, §5, ADR-0035 §2, ADR-0036, ADR-0041, ADR-0042, ADR-0048 §1, ADR-0060 §2, ADR-0076, ADR-0077 §4, ADR-0078 §4, §5, ADR-0079 §3, ADR-0080, ADR-0091; 2s's occupation ADR (plan m2s Q1); `adb-device.md` §Design rule: adb-centric, serial is the only identity, §Policy and safety; `http-websocket.md` §Security baseline.
- Lane: `2t`, branch `m2t` from `main` at `c4bb5c5`, touches `devices,contract,web,process`; ADRs 0097–0104 reserved. Lane 2s is open on the same touches (see §Meeting 2s).
- Register: the main session's commit that adds this plan sets `Work` = `2t` on the ten rows of Scope.
- Size: ten steps. If the owner wants 2t smaller, the cut is step 8 (local channels to the device): nothing depends on it, and its four rows return to `C4`/`device`.

Written before 2s merges: 2s's decisions are named by their m2s question (its occupation ADR is m2s Q1), since their ADR files are not on this branch yet; the main session puts the numbers in at the merge.

**The owner should see:**
- On the colleague's machine (the **host**), `droidyard host admit anna <fingerprint>` and `droidyard host lend <serial> --to anna` lend one phone to one person; the phone must also pass the host's physical-device policy. On yours (the **client**), `droidyard remote add qa-lab <ip:port> <fingerprint>` pairs with it, and the remote phone joins your own physical-device allowlist as `qa-lab/<serial>`, as a local phone would. Fingerprints come from `droidyard id`, exchanged by chat. Only physical devices are lent.
- Your `device.list` shows `qa-lab/<serial>` beside your local devices. Build on your machine, install, launch, Logcat, App logs, the Live view with its input, the UI tree, screenshots: the same panels and the same capabilities as for a local device. An agent on your machine uses it through the same MCP tools, with the same audiences.
- One person at a time: the first call takes a **lease**; anyone else gets "in use by anna". The host sees who holds each lent device and can **Release** it or **Stop lending** it, which ends your view within 2 s.
- Every call is a capability call on the host, in its Activity and its log with your name. No adb, shell or forwarding crosses the network. The UI stays on `127.0.0.1`; MCP stays local.

## Scope

| Item | Here? | Where, or why not |
| --- | --- | --- |
| Host role (same binary) lends chosen devices to chosen peers, physical policy as the floor | yes | steps 1, 2, 7; Q2, Q3 |
| Client lists remote devices beside local ones; the brief's capabilities work unchanged for the caller (UI and MCP, existing audiences) | yes | steps 2–5; Q4 |
| Mutual TLS with pairing; VPN as the network, no relay | yes | step 1; Q1, Q2 |
| Every remote call audited, each a capability call; no raw adb, shell or forward | yes | steps 1, 6; Q3 §2, §6 |
| (1) One user at a time: an exclusive lease the host sees and takes back | yes | steps 6, 7; Q3 §5 |
| (2) Remote identity including the host | yes | step 2; Q4 §1 |
| (3) What lending exposes; a dedicated device; no wipe for phones | yes | step 7 (the CLI says so); Q3 §4 |
| (4) The two ratified decisions reopened | yes | Q1 (ADR-0097); documents at step 9 |
| Row "LAN exposure and access control: TLS, authentication beyond the per-launch token, pairing a device" | part | steps 1, 6: TLS, authentication and pairing for peers. The row narrows to the UI and MCP on the LAN (`mvp.md` §Beyond the MVP keeps LAN access to the UI for later, Q1). "Pairing a device" is read as pairing a peer Droidyard; wireless adb (`adb pair`) is not built (`v1-device`) |
| Row "The reader's reads are unauthenticated: a host process through the forward, or an on-device process that reaches the socket, can read the UI (acting is fenced)" | yes, closed if step 8 (a) holds and the fence is reached | step 8: forwards on a Unix socket in `run/`, reads fenced by uid (Q6, the owner's); narrowed to what is not shown otherwise |
| Row "`operation.wait` returns any operation's result by id with no audience check" | yes, closed | step 1; Q5 |
| Row "`open`/`xdg-open` carry the HTTP token on their command line while they run" | part | step 8: macOS hands the URL on stdin; the row narrows to Linux (`platforms`) |
| Row "The socket's peer check rejecting a real second local account" | part | step 10 (c) only if the owner runs it from a second account; otherwise it stays, `Work` `trial` |
| Row "The live view's `secure` state comes from the scrcpy source's own poll, outside the `screen.Source` seam" | yes, closed | step 3; Q4 §5 |
| Row (C6) "No full physical-device pass (reads, actions, install, logs on a phone); synthetic `offline`/`unauthorized` lines only" | part | step 10: the pass on one phone; the row narrows to `offline` (never run for real) and `unauthorized` unless the owner revokes debugging there |
| Row (C6) "Devices sharing a serial (`0123456789ABCDEF`) are admitted or refused together" | yes, closed | step 2; Q7: refused together, with the reason, never lent |
| Row (C6) "Off USB a device reporting `product:sdk_gphone*` is classified an emulator and bypasses the physical-device policy" | part | step 2; Q7: only on a loopback serial; the row narrows to that |
| Row (device) "Between the screen server's `forward` and the connects, another local user could connect to the loopback port first (the reader's forward has the same class)" | yes, closed if step 8 (a) holds | step 8 |
| Debugger/JDWP, Run → Debug, Apply Changes, other run configurations; H.265/AV1; a relay or overlay; Streamable-HTTP MCP on the listener; the host's emulators from the client; `workspace.chooseRoot` for remote browsers; several users on one device; more multi-device concurrency | no | owner, 2026-10-18 |
| Two real machines over the real VPN; another manufacturer's phone | no | the owner's trial (register rows at step 9) |
| Linux | no | macOS only, as every milestone |

## Starting point

- `main` at `c4bb5c5`; API `0.28.0` (`internal/domain/system/system.go`); ADRs up to 0093. Lane 2s (ADRs 0094–0096 reserved) brings API `0.29.0`, `Origin.Client`, the session registry's device holders, `DEVICE_OCCUPIED`, `occupiedBy` on `device.list`, `session.release`.
- **Nothing is networked.** `adapters/httpapi` binds loopback only (`CheckLoopback`), with the per-launch token (`http-websocket.md` §Security baseline). There is no TLS, no daemon identity and no outbound connection anywhere.
- **Devices.** `internal/domain/device`: `target.go` `authorize` is every device call's preamble (one `devices -l`, the physical policy through the `Policy` seam); `list.go` filters by policy; `parse.go` classifies kind (serial prefix or product, `usb:` wins). `screensource.go` runs scrcpy behind `screen.Source` (`Join`, `Size`, `Inject`, `Done`, `Reason`); `secure.go`'s poll publishes `state` from inside the scrcpy source. `persistent.go` forwards the reader over `tcp:0`. Artifacts are in-memory (`internal/artifact`). `devicetest` is the fake adb for `cmd/droidyard` tests.
- **Core.** `capability.ClientKind` is `cli | mcp | ui`; `prepare` (`operation.go:882`) refuses `AudienceHuman` from MCP; `operation.wait`/`cancel` reach any id (ADR-0020 §Operation capabilities). Trace lines (`traceLine`) name client, capability, workspace, state.
- **Policy.** `internal/policy`: `trust`, `devices`, `shell` (ADR-0032), re-read by size and mtime; `policy.update` is human-only.
- **Budgets.** `tools/list` is 28,811 B of 29,000 at `c4bb5c5` and 2s spends about 45 B more: 2t adds no MCP tool and changes one MCP tool description, `operation_cancel`'s (Q5), no longer than today.
- **Baseline, 2026-10-18 at `c4bb5c5`:** `go vet ./...` clean; `go test ./...` ok in every package. This file fails `internal/doccheck` `TestPlansIndexMatchesFiles` until the main session adds its row.

## Meeting 2s

2t closes second. At step 6 (before it), the main session merges `main` with 2s into `m2t`, regenerates the schema and sets the API version one minor above `main`'s (`0.30.0` if 2s closed at `0.29.0`); until then 2t's steps use `0.29.0`. Files both lanes change, so textual conflicts are expected: `internal/domain/device/target.go` (2s's occupation check, 2t's lending check and duplicate serial), `list.go`/`parse.go` (`occupiedBy` and `host` on `Device`), `internal/capability/operation.go`, `sessions.go`, `envelope.go` (`Origin.Client`; `ClientPeer`, `Lendable`, waits), `internal/policy` (`occupation`; `hosting`, `remotes`), `internal/capability/descriptor.go` (2s's `Occupies`, 2t's `Lendable`), `internal/domain/emulator` (2s's checks; 2t's remote ids refused), `cmd/droidyard/serve.go`, `web/src/lib/devices/` (`Devices.svelte`, `devices.ts`), `web/src/lib/api/capabilities.gen.ts`, `system.Version`, `schema/capabilities.json`, the register, `adb-device.md`, `workspace-state.md`, `doccheck`'s `liveBudget`. Steps 1–5 and 8 do not need 2s; steps 6 and 7 do. If 2s has not closed when step 5 ends, step 8 runs first.

## Acceptance

- [ ] A1. Pairing: a host admits a peer by fingerprint; an unadmitted peer is refused at the TLS handshake, a revoked one loses its links and sockets within 2 s; the client refuses a host whose fingerprint differs; an invalid `hosting.listen` makes the file unusable as ADR-0032 §4 says and a failed bind leaves the daemon running without a listener, both in `system.health`; the peer port serves only `/peer/v1/*`. Source: Q2. Proven by: step 1.
- [ ] A2. The client's `device.list` lists the devices lent to it as `<remote>/<serial>` with `host`, beside its local ones, when its own `allowPhysical` and allowlist admit the id; a device not lent to it, or one the host's policy refuses, is absent and `DEVICE_NOT_FOUND`; one the client's own policy does not admit is absent from the client's list and `DEVICE_NOT_ALLOWED` with its cause and nextStep, as a local phone; an unreachable remote is a warning. Source: Q3 §1, Q4 §1, §2; ADR-0015 §4. Proven by: step 2.
- [ ] A3. On a remote device, from the UI and from an MCP agent, with today's audiences: `launch`, `uninstall`, `uiTree`, `tapElement`, `inputText`, `tap`, `swipe`, `pressKey` (step 2), `watchScreen` (step 3), `install` of a local build (step 4), `screenshot`, `logs`, `watchLogs` (step 5). Every relayed result and error is marked untrusted; the client runs no adb command for a remote device. Source: brief; Q4 §3, §4, §6. Proven by: steps 2, 3, 4, 5, 9.
- [ ] A4. The client's Live view of a remote device plays the host device's H.264, its bytes unchanged but for one config prepend per join, takes input that lands, and shows the host's `FLAG_SECURE` banner through the seam. Source: ADR-0078 §5; Q4 §5. Proven by: steps 3, 9.
- [ ] A5. Every remote call is an operation on the host with client `peer`, the peer's name and a trace line naming it; handshakes refused, links and uploads are logged; a peer's call outside the lendable set is `REQUEST_NOT_ALLOWED_HERE` with nothing recorded. Source: Q3 §2, §6. Proven by: steps 1, 6, 9.
- [ ] A6. A peer's first call on a lent device leases it; another peer's calls and a host agent's acting calls get `DEVICE_OCCUPIED` naming the holder, on the client too; a peer finding a host agent holding the device gets the same; the host's UI and CLI are never refused; an idle lease is taken over; a lease ends on link close, Stop lending, the host's Release and the device leaving, and its end stops the holder's streams. Source: owner (1); Q3 §5. Proven by: steps 6, 7, 9.
- [ ] A7. `operation.wait` and `operation.cancel` from an MCP session reach only that session's or its chat's operations, from a peer only its own, through the real MCP adapter and the peer link. Source: register row; Q5. Proven by: step 1.
- [ ] A8. A duplicated physical serial is refused with its reason and never lent; `sdk_gphone*` makes an emulator only on a loopback serial, and `usb:` still makes a physical device. Source: register rows; Q7. Proven by: step 2.
- [ ] A9. Forwards to the device are Unix sockets inside `run/` if step 8 (a) holds; the reader answers reads only to uid 0 or 2000, shown from another uid or labelled unexercised; on macOS `droidyard ui` puts no token on a command line. Source: register rows; Q6. Proven by: step 8.
- [ ] A10. The schema diff is the milestone's: one minor version; `peer` in the client enums; `hosting` and `remotes` in `policy.get`/`update`; `identity`, `hosting`, `remotes` on `system.health`; `host` on `device.list`'s rows; `device.installUpload`; `DEVICE_OCCUPIED` in the `errors` of every lendable device capability but `device.list`. No new error code; the MCP tool list changes only in `operation_cancel`'s description, no longer than today, and gains no tool. Source: `capability-api.md` §Discovery and versioning; ADR-0036. Proven by: steps 1, 2, 4, 6.
- [ ] A11. A physical phone, with the owner present: the local pass (reads, actions, install, logs, Live view) and the same phone lent between two daemons. Source: register row (C6); owner. Proven by: step 10.

## Run rules (steps 2, 3, 4, 5, 8, 9, 10: the main session only)

- **K1.** Two isolated daemons by `verify-daemon` §Launch, each with its own `R` (`/tmp/dy2th.*` host, `/tmp/dy2tc.*` client), built at the step's commit (after `pnpm -C web build` when the UI is used). The host's `hosting.listen` is `127.0.0.1:<free port>`. Fixtures and evidence under `B=~/Library/Caches/droidyard-dev/2t/`: a copy of `samples/golden`, formapp. No owner repository.
- **K2.** The lane's own scratch AVD (`droidyard_m2a`, API 31) on a console port of its own, on the shared adb server: acted on by its serial only, stopped only by this lane, never `adb kill-server` or `start-server`; the owner's adb server pid recorded before and after. The host's `ADB` is a guard that runs adb for that serial only and appends ` usb:2t-standin` to its `devices -l` line, so the host classifies it **physical** and the physical policy and lending apply (the stand-in); the client's configuration admits it as `qa/<serial>` (`allowPhysical`, allowlist). The client's `ADB` is a guard that answers `devices -l` with the header alone and `version` as adb does, and logs and refuses every other command: its log is the evidence that the client runs nothing on a device. What the stand-in does not prove: a vendor image's install prompts, encoder and reader; a real USB transport and its states; two machines; a VPN.
- **K3.** Headless Google Chrome with its own profile over CDP. Nothing killed by name. A phone only at step 10, with the owner present and their yes. The implementer never runs a device phase.

## Steps

### 1. The peer link: identity, pairing, the listener (no devices)

- Builds:
  - `internal/hosting` (new): `Identity`: an Ed25519 key and a self-signed certificate in `<state>/identity/` (dir 0700, `key.pem` 0600, `cert.pem`), made by the daemon at start when absent; a corrupt file is renamed `.corrupt-<ts>` (ADR-0021 §Format) and replaced, and `system.health` says so. `Fingerprint()`: lowercase hex SHA-256 of the certificate's SubjectPublicKeyInfo. `Link` (client role): one per `remotes` entry, dialling `wss://<address>/peer/v1/ws` with the identity, accepting the host's certificate by fingerprint only (`VerifyConnection`), then the hello `{peerProtocol: 1, api}`; requests matched by id, events routed by `subscriptionId`; read limit 8 MiB; reconnect from 1 s doubling to 30 s.
  - `internal/policy`: `hosting {listen, peers: [{name, fingerprint}], lend: [{device, to: [name]}]}` and `remotes: [{name, address, fingerprint}]` as Q2 §6: names `^[a-z][a-z0-9-]{0,31}$`, names and fingerprints unique per list, fingerprints 64 hex, addresses an IP literal and port, `listen` not unspecified; in `policy.get`/`update`. A value that fails validation makes the file unusable as ADR-0032 §4 says (no listener, no link, nothing lent, reported by `system.health`); the daemon keeps running.
  - `adapters/peer` (new): the listener of Q2 §3, HTTP/1.1, keep-alive off; `/peer/v1/ws` reuses `adapters/httpapi`'s session loop with the hello in place of the token frame, starting requests with client `peer` and the session named by the peer; the pin re-checked at each request and every 2 s on open sockets, closing a revoked peer's every socket; `daemon.log` lines for a refused handshake (fingerprint prefix, address), link up and down. A bind that fails (the VPN down at login, an address not on this machine) leaves no listener, an error in `system.health`, and a retry every 30 s.
  - `internal/capability`: `ClientPeer`; `Descriptor.Lendable`; `prepare` refuses a peer's call to a capability not `Lendable` with `REQUEST_NOT_ALLOWED_HERE`, before anything is recorded (as ADR-0015 §6 for MCP). Lendable in this step: `operation.wait`, `operation.cancel`, `events.unsubscribe`.
  - Q5: `Invoke` passes the request's `Origin` into the `Call` of a capability that is not an operation; an operation records its session's chat (and a peer's name) when it starts, so the check holds after that session has gone; `operation.wait` and `operation.cancel` compare the caller's `Origin` with that record; `adapters/mcp/mcp.go`'s `operationWait`, its abandoned-wait snapshot and its cancel path (today `s.ops.Cancel` with no origin) go through that check.
  - `cmd/droidyard/serve.go`: the identity, the listener when `hosting.listen` is set, the links. `system.health`: `identity {fingerprint}`, `hosting {listen?, error?, peers: [{name, connected}]}`, `remotes: [{name, connected, api?, error?}]`. API `0.29.0` (see §Meeting 2s); `go test ./internal/schema -update`.
- Implements: Q1; Q2 §1–§6, §8; Q5.
- Check: `TestPeerLink` (`cmd/droidyard`, two real `serve` processes, own state and config, the host on `127.0.0.1:0`):
  - paired: both healths `connected`; the client's `operation.wait` of an unknown id over the link answers `OPERATION_NOT_FOUND` (the round trip);
  - the client's fingerprint not in `peers`: the handshake fails, the host logs the refusal, the mux sees no request (seen failing with the pin removed);
  - a wrong host fingerprint in `remotes`: the client sends no hello (seen failing with the pin skipped);
  - the peer removed while linked: its sockets close within 2 s and a redial is refused (seen failing with the pin read only at the handshake);
  - a peer request for `policy.get`, `shell.exec`, `device.install`, `emulator.stop` → `REQUEST_NOT_ALLOWED_HERE`, no operation recorded (seen failing with the gate removed);
  - an `Origin` header → 403; `/`, `/v1/call` → 404; a TLS 1.2 client → refused;
  - `listen` `0.0.0.0:7318`, `[::]:7318`, `qa.local:7318` → the daemon runs, `system.health` names the unusable file, nothing listens; `listen` on `192.0.2.1:7318` (not this machine) → the daemon runs, `hosting.error` names the bind (seen failing with `serve` exiting).
  - `TestWaitOwnOperations` (`cmd/droidyard`, two scripted MCP shims A and B with distinct chats, a third with A's chat, the UI's WebSocket, a peer link): B's `operation_wait` and `operation_cancel` on A's operation → `OPERATION_NOT_FOUND`, the operation still running (seen failing with the check in the core but `mcp.go` unchanged); a session of A's chat that connects after A's first has exited, the UI and the CLI → the outcome (the first seen failing with the chat read from the live registry); the peer on a host UI operation's id (taken from a queued call's `blockedBy`) → not found.
  - `TestIdentity` (`internal/hosting`): stable across restarts; modes 0600 and 0700; a corrupt key replaced and reported. `TestHostingConfig` (`internal/policy`): each validation row, a duplicate fingerprint included. `TestToolListModelFacingBytes` unchanged.
- Schema: version; `peer` in the `client` enums (the `operation.state` payload, `ActiveOperation`); `hosting` and `remotes` in `policy.get`'s result and `policy.update`'s patch; `identity`, `hosting`, `remotes` on `system.health`; `operation.cancel`'s description ("whichever session started it" no longer holds for MCP), reworded no longer than today, the one MCP tool text that changes. Nothing else.
- Needs: nothing built; no device.
- Not here: devices (step 2); the lease (step 6); the CLI (step 7). The browser never reaches the peer port.
- Rests on: only a peer whose fingerprint is in `hosting.peers` at that moment reaches the capability layer, only for a `Lendable` capability, and no session but a human channel reaches another's operation. Level 4 (real processes, real TLS, real shims).
- Review: the Opus verifier (security, contract, process lifetime), effort `high`.
- [x] Done. Evidence: level 4, 2026-10-18.
  - `TestPeerLink` (two real `serve` processes, real TLS; eight subtests), `TestWaitOwnOperations` (real shims, the UI's WebSocket, the CLI, a peer link), `TestIdentity`, `TestHostingConfig`, `adapters/peer`'s round trip, revocation, bind retry and hello rows; `TestToolListModelFacingBytes` unchanged. `go test ./...` green at the step's commit.
  - Reversals, each seen failing: the host's pin removed; the client's pin skipped; the pin read only at the handshake; the Lendable gate removed; a failed bind ending `serve`; the chat read from the live registry. `mcp.go` passing no `Origin` fails closed (A's own wait is refused) rather than at B's. The Opus verifier (effort `high`) reran three by `-overlay`: gate, sweep, chat; all caught. Verdict proceed, no blocking finding.
  - Review fixes by the main session: a listener that failed after binding latched its sessions closed, so the rebound one refused every peer (`EndAll`, `TestPeerEndAllKeepsAccepting`); the core stamps `Origin.Client` from the request always, so the gate and a wait's reach read one kind.

### 2. Lent devices on the client, called through the link (the first end to end)

- Builds:
  - Host, `internal/domain/device`: for a `peer` call, `authorize` applies Q3 §1 (the `online` list of its `DEVICE_NOT_FOUND` holds only what is lent to that peer), and `device.list` for a peer lists only those. Q7 in `authorize`, `list.go` (the warning, "N devices report the serial …; droidyard cannot tell them apart") and `parse.go`.
  - Client, `internal/domain/device`: a `Remotes` seam, implemented by `internal/hosting` and injected at `serve` as `Policy` is. `device.list` adds each linked remote's listing in parallel under `detailsTimeout` (Q4 §1), keeping only ids the client's own physical policy admits (Q4 §2). A relay wrapper applied at registration to the plain class (Q4 §3): at each call, before any host call, it asks the client's `Policy.PhysicalAdmitted("<remote>/<serial>")` and fails `DEVICE_NOT_ALLOWED` as `notAllowed` does (the allowlist already accepts `/`); then `device` becomes the serial, the call is started on the host and waited there within the call's context, the result's `device` fields become the id again, `Call.Untrusted` is set, errors map as Q4 §4, and a cancel cancels the host's operation.
  - Lendable: `device.list` and the plain class (`launch`, `uninstall`, `uiTree`, `tapElement`, `inputText`, `tap`, `swipe`, `pressKey`).
- Implements: Q3 §1, §2 (part); Q4 §1–§4, §6; Q7.
- Check:
  - `TestRemoteDevices` (`cmd/droidyard`, two daemons, `devicetest` on each):
    - the host fake lists physical `R58M001`, `R58M002`, `R58M003` (`usb:`) and `emulator-5554`; the client's list holds its own `emulator-5556` and `qa/R58M001` with `host`; `R58M002` lent to another peer, `R58M003` lent but not in the host's allowlist, and `emulator-5554` named in `lend` (only physical devices are lent) are each absent and `DEVICE_NOT_FOUND` when named (seen failing with the lending check removed); `qa/R58M001` once removed from the client's allowlist, or with the client's `allowPhysical` false, is absent from the client's list and `DEVICE_NOT_ALLOWED` with its cause when named, and the host records no call (seen failing with the client's per-call check skipped for remote ids);
    - each plain capability on `qa/R58M001`: the host fake's calls carry `-s R58M001`; the client fake's calls hold nothing but `devices -l` (seen failing with the wrapper bypassed);
    - every result has `meta.untrusted` and `device` `qa/R58M001`; a host error keeps its code; a code the client's descriptor does not declare → `DEVICE_NOT_READY` (`incompatible`); the host stopped → `DEVICE_NOT_READY` (`unreachable`) and a list warning; the link cut mid-call → `DEVICE_DISCONNECTED`; a client cancel ends the host's operation `cancelled`;
    - a client MCP session: `device_uiTree` on the remote id answers; `device.tap` from it is `REQUEST_NOT_ALLOWED_HERE` on the client and the host records nothing.
  - `TestDuplicateSerial`, `TestEmulatorClassification` (`internal/domain/device`): two lines with one serial → refused with the cause and a list warning (seen failing without the check); `sdk_gphone64_arm64` on `10.0.0.5:5555` physical, on `127.0.0.1:5555` and `emulator-5554` an emulator (seen failing with today's rule), and on `emulator-5554` with `usb:` physical (`usb:` wins first; the stand-in's case).
  - **Level 5 (K1–K3):** the client lists `qa/<stand-in>` (kind `physical`) only once the host's `allowPhysical`, `allowlist` and `lend`, and the client's `allowPhysical` and allowlist, all admit it; from the client's CLI, `device.uiTree`, `device.launch` of formapp, `device.tapElement` on Like (the count rises), `device.inputText "ñandú"` (read back through `uiTree`); the client guard's log holds no device command.
- Schema: `host` on `device.list`'s rows. Nothing else.
- Needs: step 1.
- Not here: the stream (3), install (4), artifacts and logs (5), the lease and the client's own occupation of remote ids (6, with 2s).
- Rests on: a peer reaches only the devices lent to it, level 4; the client runs no adb command for a remote device, level 5 (the guard's log).
- Review: the Opus verifier (contract, security), effort `high`.
- [x] Done. Evidence: level 5, 2026-10-18 (K1–K3; evidence `~/Library/Caches/droidyard-dev/2t/evidence/step2/`).
  - Level 5, two daemons on this Mac, the stand-in `emulator-5584` (`usb:2t-standin`): the client's list is empty until the host's `allowPhysical`, `allowlist`, `lend` and the client's `allowPhysical` and allowlist all admit it, then `qa/emulator-5584` (kind `physical`, `host` `qa`); before the client's allowlist names it, `device.uiTree` is `DEVICE_NOT_ALLOWED` (`"qa/emulator-5584" is not in devices.allowlist`). From the client's CLI: `uiTree` (untrusted), `launch` of formapp, `tapElement` on Like (`Likes: 1` → `Likes: 2`), `inputText "ñandú"` read back through `uiTree`. The client's adb guard log holds only `devices -l` and `version` (its own listing), no device command; the host's guard refused nothing. Rerun after the fix round, same result. The shared adb server's pid unchanged against the K2 baseline.
  - Level 4: `TestRemoteDevices` (two daemons, real TLS, `devicetest` on each), `TestPeerCannotReachWhatTheHostBorrows`, `TestRemoteDevicesNeedNoLocalADB`, `TestRemoteListingBounds`, `TestLendingToPeers`, `TestDuplicateSerial`, `TestEmulatorClassification`, `TestPeerCallIsNotRelayedOnward`, `TestCancelDuringTheStartStillCancelsTheHostsOperation`, `TestACancelledCallDoesNotCutTheLink`, `TestPeerGetsNoHostWarnings`, `TestRemoteErrorMapping`. `go test ./...` green at the step's commit.
  - Reversals, each seen failing: the lending check removed; the client's per-call check skipped; the wrapper bypassed; a local `devices -l` for a remote id; the duplicate checks removed; physical-only removed from `lentTo`; today's classification rule; and for the fix round each fix reverted (B1, B2, N1, N2, N3, N4, nits 1, 2).
  - Review: the Opus verifier, two rounds. Round 1 fix first: [safety] B1, a peer's call relayed onward to a device the host borrows, skipping `lend`; [contract] B2, `device.list` needing local adb (ADR-0100 §6); [contract] N1, a host-side cancel mapped to `incompatible`; [bug] N2, a cancel during the start; [bug] N3, the remote listing's bound; [bug] N4, a cancelled caller's write closing the link (step 1's code); three nits. Round 2 proceed, one new [contract] R1 (a start past its bound read as `REQUEST_INVALID_PARAMS`), fixed by the main session (`DEVICE_DISCONNECTED`, in `TestRemoteErrorMapping`).
  - Not yet: `screenshot`, `logs`, `install` on a remote id are `DEVICE_NOT_FOUND` until steps 4–5; no plain-class result carries `device`, so the id rewrite is generic and the Check's "`device` `qa/R58M001`" is met by the operation's target and the list row.

### 3. The live view of a remote device: the stream relayed, input as host calls, `secure` through the seam

- Builds:
  - `internal/domain/device/screen`: `Source` gains the secure state (Q4 §5); the scrcpy source's poll publishes through it, and `watchscreen.go` publishes `state` from the seam for any source. `Split(Packet) []Packet`: a key frame carrying its config (the Hub's join form) becomes the config packet and the key frame, the bytes unchanged and in order.
  - Host, `adapters/peer`: `GET /peer/v1/screen`: the first text frame, within 5 s, is `{"stream": <id>}`, an id from a `device.watchScreen` of the same link (else 4404); then ADR-0078 §4's binary messages and close codes. `device.watchScreen` becomes Lendable.
  - Client, `internal/hosting`: a relayed source behind `screen.Source` for a remote id: `device.watchScreen` on the host, the attach, a frame reader (bounded by ADR-0078 §5's packet cap), `Split`, then the client's Hub. The host's `stopped` gives the same reason; its `state` feeds the seam; a lost link is `deviceGone`. `Inject` returns an error: a remote device's input is step 2's relayed `tap`, `swipe`, `pressKey`, which find the host's running source. One relayed source per remote device, shared by the client's viewers; it ends with its last viewer and unsubscribes on the host.
- Implements: Q4 §3 (streams), §5; ADR-0078 §5 on the client; closes the `secure` row.
- Check:
  - `TestRelayedScreen` (`cmd/droidyard`, two daemons, the host with `devicetest` and m2p's fixture listener):
    - a client `/v1/screen` viewer's payloads equal the fixture's packets, the first key frame with its config once, byte for byte (seen failing with a re-chunking);
    - a second client viewer joining after the host's third key frame gets the config once (seen failing without `Split`);
    - the host's secure check flipping gives the client one `state` per change (seen failing with the client's state from a local poll);
    - closing the client's last viewer releases the host's subscription and its server within 2 s; the link cut → `stopped deviceGone`;
    - a client `device.tap` on the remote id reaches the host fixture's control listener; with no view open it answers the host's `DEVICE_NO_STREAM`;
    - a second peer attaching the first peer's stream id → 4404 (seen failing with the id looked up alone, as `ScreenAttach` does today); the first peer revoked while streaming → its screen socket closes within 2 s.
  - `FuzzPeerScreenFrame` on the client's frame reader, 30 s, no panic, caps held. `TestScreen*` and `TestWatchScreen*` unchanged through the new seam method.
  - **Level 5 (K1–K3):** the client's Live view in headless Chrome plays the stand-in within 3 s and decodes at least 90% of its packets during a swipe; a click on formapp's Like adds one; Back and Home; formapp's Secure toggle shows the banner on the client and clears it. Over 20 taps on loopback: `device.tap` `durationMs` and click-to-frame p50/p95, beside m2p's local numbers.
- Schema: no change.
- Needs: step 2.
- Not here: latency over a VPN (Unverified, the trial); H.265 and AV1 (owner).
- Rests on: the client's viewers get the device's bytes unchanged but for one config prepend per join, level 4 (the fixture) and level 5 (the decode in Chrome).
- Review: the Opus verifier (bytes from another machine parsed in the daemon, concurrency, process lifetime), effort `high`.
- [ ] Done. Evidence:

### 4. Install a local build on a remote device

- Builds:
  - Host, `adapters/peer`: `PUT /peer/v1/upload` (Q3 §3): `Content-Length` required and at most 512 MiB (413 before reading), streamed to `<state>/hosting/uploads/<id>.apk` (dir 0700, file 0600) with its SHA-256, answered `{upload, sha256}`; an upload is the uploading peer's only, looked up in memory by its random id (never a path built from the parameter); a peer has at most one pending upload, a new one replacing it; one unused for 10 minutes is removed; leftovers are swept at start (ADR-0091). A `daemon.log` line per upload (peer, size, SHA-256 prefix).
  - Host, `internal/domain/device`: `device.installUpload {device, upload}`: command, global, audience `human` (ADR-0060 §2), long-running, Lendable, the device's write key, the device preamble. It runs the existing install path (`adb install -r`, `DEVICE_INSTALL_FAILED` with the token) on the file and removes it after. An upload unknown to this peer is `REQUEST_INVALID_PARAMS`.
  - Client: `device.install` on a remote id resolves its build reference locally (unchanged), uploads the APK under a bound of its own (10 minutes; the host's `installTimeout` then bounds the install alone), calls `device.installUpload`, and returns `InstallResult` from the local build's facts.
- Implements: Q3 §3; Q4 §3 (install).
- Check: `TestRemoteInstall` (two daemons, `devicetest` recording the installed file's digest):
  - the host installs a file whose SHA-256 is the local APK's (seen failing with a truncated copy);
  - the file is gone after a success and after `Failure [INSTALL_FAILED_…]`, whose token reaches the client's `DEVICE_INSTALL_FAILED`;
  - 513 MiB declared → 413, nothing written; peer B naming peer A's upload → refused; a second upload from A removes A's first (seen failing with uploads kept side by side); an id shaped as a path (`../x`) → refused with nothing opened; an upload left 10 minutes (a clock seam) is removed; a leftover file is swept at start;
  - `device.installUpload` from an MCP session → `REQUEST_NOT_ALLOWED_HERE`; `TestToolListModelFacingBytes` unchanged.
  - **Level 5 (K1–K3):** on the client, `build.assemble` of the `samples/golden` copy, `device.install` to the stand-in, `device.launch` gives a pid.
- Schema: `device.installUpload` with its params, result and errors. Nothing else.
- Needs: steps 1, 2.
- Not here: split APKs and bundles (`mvp.md` §Out of scope); resumable uploads.
- Rests on: the host installs exactly the bytes the client built, and keeps none, level 4.
- Review: the Opus verifier (a capability; bytes from another machine written to disk), effort `high`.
- [ ] Done. Evidence:

### 5. Screenshots, logs and Logcat of a remote device

- Builds:
  - Host, `internal/artifact`: an ephemeral artifact records the session of the operation that produced it (today only `Producer`, `artifact.go`). `adapters/peer`: `GET /peer/v1/artifacts/{id}` with `artifact.read`'s semantics (ADR-0012 §Reading artifacts), for an ephemeral artifact produced by an operation of the same peer only, else 404; `artifact.read` Lendable under the same rule; `device.screenshot`, `device.logs`, `device.watchLogs` Lendable.
  - Client: `device.screenshot` and `device.logs` on a remote id relay the call, fetch the artifacts the result names (bounded by the client's own store limits), store them in the client's store as untrusted and rewrite the references; `device.watchLogs` subscribes on the host and republishes each event to the client's subscription with `device` rewritten; the host's `deviceGone`, or the link lost, ends it with `deviceGone` (ADR-0042); the client's unsubscribe or session end unsubscribes on the host.
- Implements: Q3 §2; Q4 §3 (artifacts, streams).
- Check: `TestRemoteArtifactsAndLogs` (two daemons, `devicetest`): a screenshot's local artifact equals the host's bytes and is served with `X-Droidyard-Untrusted` (seen failing with the host's reference passed through); a `device.logs` capture likewise; another peer's artifact id, a host UI screenshot's id and a durable `droidyard://artifact/durable.…` id → 404 (seen failing with the owner check removed); `device.watchLogs` lines arrive with the remote id; ending the client's subscription ends the host's logcat child; the link cut → `deviceGone`; MCP `device_screenshot` on the remote id returns its image. **Level 5:** after a remote launch, the client's Logcat panel streams formapp's line, App logs since launch show it, and Act shows a screenshot.
- Schema: no change.
- Needs: steps 1, 2.
- Not here: —
- Rests on: a peer reads only artifacts of its own operations, level 4.
- Review: the Opus verifier (security), effort `high`.
- [ ] Done. Evidence:

### 6. The lease, the audit, and waits on one's own operations (after 2s merges)

- Builds: first, the main session merges `main` with 2s, regenerates the schema and bumps the version (§Meeting 2s). Then:
  - Host: each peer link is a session in 2s's registry (client `peer`, named by the peer), and the lease is its occupation of the device, as Q3 §5, taken in the device preamble; idle after a constant 15 minutes, an open stream counting as activity; un-lending is seen by the 2 s check; an ended lease stops the holder's streams on the device (`stopped deviceGone`, cause "the host ended the lease"). `DEVICE_OCCUPIED` joins the `errors` of every Lendable device capability that 2s's seven do not cover.
  - Audit: a peer operation's trace line adds `peer=<name>` (Q3 §6).
  - Client: the relay wrapper runs 2s's occupation check on the remote id, so the client's sessions coordinate on it as on a local device; a host `DEVICE_OCCUPIED` passes through with its message.
- Implements: Q3 §5, §6; owner (1).
- Check:
  - `TestLease` (`cmd/droidyard`, one host, client daemons A and B, a host MCP shim, `devicetest`): A's `device.uiTree` → the host's `device.list` shows A in `occupiedBy`; B's `uiTree`, `launch` and `watchScreen` → `DEVICE_OCCUPIED` naming A on B's client, not `incompatible` (seen failing with the hold taken only by acting calls, and with the code left undeclared); the host MCP shim holding the device first (its `device.launch`) → A's first call `DEVICE_OCCUPIED` naming it; the host MCP shim's `device.launch` → `DEVICE_OCCUPIED`, its `uiTree` answers; a host `client=ui` `device.tap` acts; `occupation.conflict: allow` still refuses B (seen failing with the configuration honoured for peers); A idle (a clock seam) → B's call holds; A watching the screen 20 minutes without a call → not idle; A's link closed → free; un-lent → A's view `stopped deviceGone` within 2 s and A's next call `DEVICE_NOT_FOUND`; `session.release {device}` → A's streams end and A's next call takes it again; under `-race`, A's and B's first calls at once → one holds, one refused. Trace lines carry `peer=A` (seen failing without).
  - `TestWaitOwnOperations` (step 1) gains: peer B on peer A's operation → not found.
- Schema: `DEVICE_OCCUPIED` in the `errors` of `uiTree`, `screenshot`, `logs`, `watchLogs`, `watchScreen`, `tap`, `swipe`, `pressKey`, `installUpload` (and any Lendable one 2s left out); `peer` wherever 2s's session rows name a client kind. Nothing else.
- Needs: steps 1, 2, 3; 2s merged.
- Not here: a client returning a lease before idleness (Not in this milestone).
- Rests on: one holder per lent device across peers and host agents, decided under 2s's lock, level 4 (`-race`).
- Review: the Opus verifier (concurrency, security, a contract change), effort `high`.
- [ ] Done. Evidence:

### 7. The UI and the CLI of both roles

- Builds:
  - Web (load `svelte-core-bestpractices` and `svelte-code-writer` first; the autofixer gate on each `.svelte` file): the device picker and `Devices.svelte` show a remote device's host (`qa-lab · <model>`) and "in use by <name>" from `occupiedBy`, and an unreachable remote's warning; `devices.ts` re-reads `device.list` when a remote device's screen or log stream stops `deviceGone` (the signal `screen/liveview.ts` and `logcat/stream.ts` already receive), since no operation of this UI tells it a host stopped lending (no schema change). On the host, a lent device shows "lent to <names>" (from `policy.get`'s `hosting.lend`) and its holder, with Release (`session.release`, 2s) and Stop lending (`policy.update` with `ifRevision`, removing its entry).
  - CLI (`cmd/droidyard`, Q2 §7): `droidyard id` (the fingerprint, from `system.health`); `droidyard host admit <name> <fingerprint>`, `revoke <name>`, `lend <serial> --to <name>[,<name>]` (which prints Q3 §4's warning: the whole device is lent, use a dedicated one), `unlend <serial>`, `listen <ip:port>|off`; `droidyard remote add <name> <ip:port> <fingerprint>` (which says a lent phone is used once `devices.allowPhysical` is true and `<name>/<serial>` is in the allowlist), `remove <name>`. Each is one `policy.update` with `ifRevision`, retried once on `POLICY_REVISION_CONFLICT`; `listen` and `remote` say a restart applies them (`droidyard stop`).
- Implements: Q2 §7; Q3 §4, §5 (the host's view).
- Check: `TestHostingCLI` (`cmd/droidyard`): each verb's resulting section; an invalid name, fingerprint or address refused before any write; a concurrent edit retried once. Vitest: the host label, the holder, Release's and Stop lending's params (seen failing with the whole `lend` array not resent). **Level 4** in headless Chrome against two daemons with `devicetest`: the client's panel names the remote device and its holder; Stop lending on the host's panel stops the client's open view within 2 s and the client's list re-read then omits it (seen failing without the re-read); a list read by hand after the host went down shows the warning.
- Schema: no change.
- Needs: steps 1, 2, 6.
- Not here: pairing from the UI; the look (the owner's review).
- Rests on: —
- Review: the main session's diff review (UI); the Opus verifier for the CLI's policy writes, default effort.
- [ ] Done. Evidence:

### 8. Local channels to the device: Unix-socket forwards, fenced reads, the token off the command line

- Builds:
  - (a) **Spike (main session, K2).** On this Mac's adb (version recorded): `adb forward localfilesystem:<R>/run/fwd/x localabstract:<reader socket>`: the file's mode, a connect reaching the reader's `ping`, `forward --list`, `--remove` taking the file away; the same for scrcpy's two sockets. **Stop rule:** if the host's adb refuses `localfilesystem:`, (b) is not built, the two forward rows stay with the finding, and ADR-F records only (c).
  - (b) `persistent.go`, `screensource.go`: forwards to `localfilesystem:<state>/run/fwd/<kind>-<12 hex>` (within `sun_path`, as ADR-0019 §6 bounds the socket), found and removed as today's `tcp:` ones; files left by a killed daemon swept at start.
  - (c) The reader (`reader/src/DroidyardReader.java`): `dump` and `ping` answer only peer uid 0 or 2000, as the acting verbs do (Q6); `reader.dex` rebuilt, `SHA256` re-pinned.
  - (d) `droidyard ui` and `droidyard` on macOS hand the URL to `osascript` on stdin (`open location "…"`), never on a command line; Linux keeps `xdg-open`.
- Implements: Q6; the rows of the reader's reads, the forward race and `open`/`xdg-open` (macOS).
- Check: (a)'s facts in the evidence line. A fake-adb test that every forward is under `run/fwd/` and removed at release (seen failing with `tcp:0`); a leftover socket swept; `TestReaderDigest` (seen failing with a byte changed). **Real (K2):** `TestRealScreenSource` and `uireader_real_test.go` on the lane's AVD over the socket forwards. The read fence is reached from a uid SELinux lets connect: on the AVD with `adb root` (a Google APIs image allows it; this device only), `su <formapp's uid>` running a `dump` against the reader's socket is answered `denied`, and `ping` from uid 2000 still answers (seen failing with the fence removed). If the image cannot, the fence is recorded as unexercised (Unverified) and the reads row narrows instead of closing. `TestOpenURLNotOnCommandLine` (`cmd/droidyard`, the opener seam): no argument holds the token, stdin does.
- Schema: no change.
- Needs: nothing from steps 1–7.
- Not here: Linux's opener (the row narrows to Linux).
- Rests on: no other account can connect to a forward: level 3 (a socket inside a 0700 directory) unless step 10 (c) reaches level 5.
- Review: the Opus verifier (process lifetime, security), effort `high`.
- [ ] Done. Evidence:

### 9. Level 5 of the use case, and the documents

- Builds: no code but what the run finds. The main session, K1–K3:
  - (a) Pairing from nothing with the CLI: `id` on both, `host admit`, `host listen`, `host lend` (with `allowPhysical` and the allowlist), `remote add`, restarts.
  - (b) The brief's loop on the client against the stand-in: build the golden copy, `device.install`, `device.launch`, Logcat and App logs, the Live view with tap, swipe, keys and text, the UI tree, a screenshot; then the same from a `claude -p` MCP session (`--strict-mcp-config`), whose `device.tap` is refused on the client.
  - (c) A second client B: `DEVICE_OCCUPIED` naming A; the host's Release, then B holds; Stop lending ends B's view.
  - (d) The host's `daemon.log` and trace lines name each call's peer; the client guard's log holds no device command.
  - (e) The host restarted while linked: the client reconnects within 30 s and its view restarts; `droidyard stop` on both leaves no forward, server, upload or link.
  - (f) Documents: a short `docs/interfaces/peer.md` (the two roles, the routes, what is fenced; links to ADR-0097 to ADR-0103); `adb-device.md` (identity with a host, Q7's rules, forwards); `http-websocket.md` §Security baseline rule 1 (the UI listener stays loopback; the peer listener is ADR-0098's); `vision.md` §Non-goals and `mvp.md` §Out of scope (ADR-0097); `beyond.md` §The capabilities (C4a built in 2t); `ui-automation.md` §Input primitives (a remote device's input); `workspace-state.md` §Devices are not workspace-owned; `glossary.md` (host, peer, lease); the register: Scope's rows closed or narrowed by what was proven, and new rows (the VPN and other manufacturers' trial, the macOS firewall prompt for an ad-hoc signed binary, pairing from the UI, a client's early lease return, peers of different API versions untested, the stand-in's limits); `docs/README.md` §Where the work stands; the plans index; `doccheck`'s `liveBudget` raise stated.
- Implements: A3, A4, A5, A6 at level 5.
- Check: each item's decisive lines under `$B/evidence`; the owner's adb server pid unchanged.
- Schema: no change.
- Needs: steps 1–8.
- Not here: a phone (step 10).
- Rests on: —
- Review: the Opus verifier at the close.
- [ ] Done. Evidence:

### 10. The physical phone, with the owner present (the last step)

- Builds: no code but what the run finds. The main session, with the owner present and their yes for the phone they choose; only formapp and the golden copy are installed or uninstalled; nothing of theirs is touched, wiped or dumped.
  - (a) Local, on one daemon: `device.list` (`physical`, `usb:`), `uiTree`, `screenshot` (and formapp's `FLAG_SECURE`), `tapElement`, `inputText`, `install`, `launch`, `logs`, `watchLogs`, the Live view with input. `unauthorized` for real only if the owner revokes USB debugging and replugs.
  - (b) Lent: the same phone admitted, allowlisted and lent by the host daemon to the client daemon, both on this Mac; step 9 (b) on it.
  - (c) Only if the owner offers a second macOS account: from it, a connect to the daemon's socket (refused by the peer check) and to the forward sockets (refused by the directory).
  - (d) The owner's `droidyard ui` opens their browser through `osascript`.
- Implements: A11; the C6 physical-device row; part of the second-account and `open` rows.
- Check: each item's decisive lines under `$B/evidence/phone`.
- Schema: no change.
- Needs: steps 1–9; the owner.
- Not here: two machines over the VPN and another manufacturer's phone (the owner's trial, register rows).
- Rests on: —
- Review: the Opus verifier, on the evidence.
- [ ] Done. Evidence:

## Not in this milestone

- The owner's exclusions (Scope's row), each where the owner put it.
- Pairing and lending from the UI: the CLI and the configuration only; a register row.
- A client returning a lease before idleness or its link's end: Q3 §5; a register row.
- Wireless adb pairing (`adb pair`): `v1-device`, if the LAN row meant it.
- Peers of different API versions: only equal `peerProtocol` is checked, a different API is a warning; untested beyond one binary (Unverified).
- The UI listener on the LAN, and MCP over the network: Q1.
- A durable audit file: the trace and `daemon.log` lines, with the log's rotation (Q3 §6).
- Per-app fences on a lent device: Q3 §4.
- Linux's opener and Linux at all.

## Design questions

Each has a recommendation, applied as Delegated under the owner's standing instruction unless the owner says otherwise. Two are the owner's: Q1's reopening (approved 2026-10-18; its wording Delegated) and Q6's read fence (it reverses an owner decision of ADR-0030). Once ADR-0097 to ADR-0103 exist, Q1 to Q7 shrink to links to them.

### Q1. Local-first, with peers (ADR-0097)

- Blocks: every step.
- The documents say: "A cloud service. Droidyard is local-first" (`vision.md` §Non-goals); "LAN exposure and access control … remote access … after v1", ratified 2026-09-29, and "multi-user or cloud use" (`mvp.md` §Out of scope); `mvp.md` §Beyond the MVP keeps "LAN and tablet access to the UI" for later; `http-websocket.md` §Security baseline rule 1 refuses a non-loopback bind "in the MVP: LAN exposure needs its own ADR"; `beyond.md` §Decisions it reopens.
- Draft decision, a new ADR that replaces those three lines for devices: Droidyard stays local-first and is never a service anyone operates: no cloud, account, relay or overlay. A daemon whose human enables hosting serves **its devices' capabilities, and nothing else**, to **Droidyards it has paired**, over a private network the users already share (the company VPN); several people may then use one host's devices, one at a time per device. **In this decision** the browser and MCP clients speak only to their own machine's daemon: the HTTP listener stays loopback with its token, MCP stays stdio; LAN access to the UI stays later, for an ADR of its own. Rejected: a relay (no internet reach, owner); raw adb on the network (`beyond.md`: no authentication, no NAT, version skew); the browser reaching another machine's daemon for this use case (a second authentication model in the browser, Origin and CORS over a network, the token on another machine) — not a rejection of LAN access to the UI for its own sake.

### Q2. Transport, identity and pairing (ADR-0098)

- Blocks: steps 1–7.
- The documents say: nothing on TLS, identities or peers; ADR-0021 §Layout has no identity files; ADR-0032 §5 limits `policy.update` to `trust`, `devices`, `shell`; ADR-0019 §7's client kinds are `cli` and `mcp` (and the HTTP adapter's `ui`).
- Draft decision, a new ADR superseding ADR-0032 §5 (in part: the patch also covers `hosting` and `remotes`) and adding to ADR-0021 §Layout (`identity/`, `hosting/uploads/`):
  1. **Identity.** Each daemon makes, at its first start, an Ed25519 key and a self-signed certificate in `<state>/identity/`; its fingerprint is the hex SHA-256 of the certificate's public key info. Lost state is a new identity, paired again.
  2. **Pairing is mutual pinning**, exchanged out of band: the host's human adds `{name, fingerprint}` to `hosting.peers`, the client's human adds `{name, address, fingerprint}` to `remotes`. No CA, no secret, no pairing mode.
  3. **The listener** binds `hosting.listen`, an IP literal and port (an unspecified address or a host name fails validation, ADR-0032 §4; loopback allowed); a bind that fails leaves the daemon running without it, reported and retried every 30 s. TLS 1.3 only, a client certificate required and pinned against `peers` at each handshake, each request and every 2 s on open sockets. It serves only `/peer/v1/ws`, `/peer/v1/screen`, `/peer/v1/upload`, `/peer/v1/artifacts/{id}`; a request with `Origin` is refused.
  4. **The link.** The client dials each `remotes` entry at start, pins its fingerprint, sends `{peerProtocol: 1, api}`, and reconnects with backoff (1–30 s). Equal `peerProtocol` is required; a different API version is a warning. The peer protocol is internal (both ends are this code), not in `schema/capabilities.json`.
  5. **Client kind `peer`**; a peer's operations carry its configured name as the session name (ADR-0048 §1).
  6. **Configuration.** `hosting {listen, peers, lend}` and `remotes` are policy (ADR-0015 §6): human channels only, `policy.update` included; names and fingerprints unique. `listen` and `remotes` are read at start, `peers` and `lend` live, so a revocation takes effect within 2 s.
  7. **CLI**: `id`, `host`, `remote` join ADR-0024's subcommands, each a `policy.update`.
  8. **No new module dependency**: `crypto/tls`, `crypto/ed25519`, `crypto/x509`, and coder/websocket (ADR-0028).
- Rejected: a one-time pairing code (a secret on the wire and a pairing state for one copy-paste fewer); a private CA (a PKI for two machines); a bearer token (no identity, and it leaks as the HTTP token's row says); SSH tunnels (shell access to the host); the system trust store (no pinning); gRPC (a dependency and generated code).

### Q3. What a host lends, to whom; the peer surface, the lease, the audit (ADR-0099)

- Blocks: steps 2, 4, 5, 6, 7.
- The documents say: ADR-0015 §4 and ADR-0032 §2 admit a physical device by `allowPhysical` and the allowlist; 2s's occupation ADR (m2s Q1) holds devices for live MCP sessions, never refuses the UI or the CLI, and its §10 says remote devices and users fit it unchanged; nothing on peers.
- Draft decision, a new ADR superseding 2s's occupation ADR §1, §2, §3, §4, §5 in part (a peer session as holder, and how its hold starts and ends) and ADR-0048 §1 in part (a peer's name):
  1. **Lending.** `hosting.lend: [{device, to: [peer]}]`, re-read at each call. Only a device the host classifies physical is lent, and it must also pass the host's physical policy: lending never widens it. Emulators are not lent (the owner excluded the host's emulators; the use case is phones). A device not lent to the calling peer is absent from its listing and `DEVICE_NOT_FOUND`.
  2. **The peer surface** is the `Lendable` descriptors only: `device.list`, `uiTree`, `screenshot`, `tapElement`, `launch`, `logs`, `watchLogs`, `watchScreen`, `uninstall`, `inputText`, `tap`, `swipe`, `pressKey`, `installUpload`; `artifact.read` of its own artifacts; `operation.wait`, `operation.cancel`, `events.unsubscribe`. Anything else is `REQUEST_NOT_ALLOWED_HERE` in the core, by client kind. No `emulator.*`, shell, file, build, policy or adb.
  3. **`device.installUpload {device, upload}`** (command, global, audience `human`, long-running) installs an APK the peer uploaded (`PUT /peer/v1/upload`, at most 512 MiB, one pending per peer, kept 0600 under `<state>/hosting/uploads/` until installed or 10 minutes, swept at start).
  4. **Exposure.** Lending lends the whole device: notifications, other apps, accounts. Input on the screen already reaches all of it, so no per-capability or per-app fence is attempted; a dedicated device is recommended, and `host lend` says so. Nothing is cleaned after a lease (a phone has no wipe); installs are in the log.
  5. **The lease is 2s's occupation with a peer session as holder**: taken by the peer's first call of any kind (reads too: one person at a time); while held, other peers' calls and host agents' acting calls fail `DEVICE_OCCUPIED`, whatever `occupation.conflict` says; a peer whose first call finds a host agent holding the device is refused the same way, unless that holder is idle; the host's UI and CLI are never refused (the owner of the device can always act, and take it back). Idle after 15 minutes (a constant, as 2s's 120) without a call or an open stream, then taken over by the next caller. It ends with the link, un-lending, the device leaving, or the host's `session.release`; its end stops the holder's streams.
  6. **Audit.** Each peer call is an operation with client `peer` and the peer's name; its trace line adds `peer=`; `daemon.log` records refused handshakes, links and uploads. Retention is the log's.
- Rejected: a lease table of its own (two concepts where the owner chose one in 2s); a lease taken only by acting calls (a second person could watch the first): 2s's occupation ADR rejected "held from any call, reads included" because a watching session would block the acting one, and for peers the owner's point (1), one person at a time, is exactly that block; a claimed `via=` (which client kind drove the call on the client: no transport for it, and the host cannot check it); fences per capability or package (the screen grants everything); uninstalling at the lease's end (QA may need the build); `device.install` taking an upload parameter (its MCP schema grows past the tool budget); a durable audit file (new state no requirement asks for).

### Q4. Remote devices on the client (ADR-0100)

- Blocks: steps 2–5.
- The documents say: `adb-device.md` §Design rule: the serial is the only identity; ADR-0077 §4's seam has no secure state; ADR-0079 §3 delivers input through the device's running source.
- Draft decision, a new ADR superseding ADR-0077 §4 in part (the seam carries the secure state):
  1. **Identity** `<remote>/<serial>`: the device argument, the operation target (ADR-0076), the occupation key and the write key. `device.list` rows gain `host`; an unreachable remote is a warning.
  2. **Admission** is unchanged (ADR-0015 §4, ADR-0032 §2): a remote device is physical, so the client lists and uses it only when its own `allowPhysical` is true and its allowlist names `<remote>/<serial>`, re-read at each call. A host lending one more phone does not reach the client's agents until the client's human admits it. Rejected: the `remotes` entry as consent to every device the host lends (the host could add a phone the client's human never saw).
  3. **Relay**, per class: plain calls forwarded and waited on the host; `install` resolved locally and uploaded; artifacts fetched and stored locally; `watchLogs` events republished; `watchScreen` through a relayed `screen.Source`. Input from a remote device's view is the host's capability call. `emulator.*` on a remote id is `DEVICE_NOT_FOUND`.
  4. **Trust and errors.** A relayed result or error is untrusted as a whole (another machine wrote it). A code the client's descriptor does not declare becomes `DEVICE_NOT_READY` (`incompatible`); a link down before the call `DEVICE_NOT_READY` (`unreachable`); a link lost during it `DEVICE_DISCONNECTED`. No new code.
  5. **The seam** carries the secure state for every source; a relayed source never injects, and splits a joined key frame back into config and key frame, so the client's Hub keeps ADR-0078 §5.
  6. No local adb is needed or run for a remote device.
- Rejected: the browser connecting to the host (Q1); a `remote.*` capability family or a separate `host` parameter (not "unchanged for the caller", and every MCP schema grows); transcoding (m2p's constraint 1); proxying adb (`beyond.md`).

### Q5. Waits reach one's own operations (ADR-0101)

- Blocks: step 1.
- The documents say: ADR-0020 §Operation capabilities: "any session — or none — can wait on or cancel any operation"; ADR-0019 §Sessions: a build an agent started is cancellable from the UI, "and vice versa".
- Draft decision, superseding both in part: an MCP session waits on and cancels only operations of its own session or of its chat's sessions (ADR-0088), a peer only its own; the UI and the CLI any (Activity, resume after a reconnect). Another's id is `OPERATION_NOT_FOUND`. An MCP client with no chat id loses its waits across a shim restart (`build.lastResult` still answers for builds). Rejected: an audience check alone (another agent's operation stays readable); the adapter as the fence (the core is, as ADR-0015 §6).

### Q6. Local channels to the device (ADR-F, after step 8 (a))

- Blocks: step 8.
- The documents say: ADR-0077 §1 and the reader forward over `tcp:0`; ADR-0030, an owner decision, has "no socket authentication" and its Alternatives reject "a token or a peer-uid check on the socket … as cost without a threat worth it"; ADR-0035 §2 fences acts, not reads; its Alternatives reject a socket token (visible to `ps`).
- Draft decision (**the owner's**: it reverses part of ADR-0030): if step 8 (a) holds, forwards are `localfilesystem:` sockets inside `run/` (0700), so no other account connects first or reads the UI; the reader fences reads as it fences acts (uid 0 or 2000), superseding ADR-0030 (in part: reads are checked) and ADR-0035 §2 in part. What changed since ADR-0030: the uid check exists already for acts (ADR-0035), so its cost is a branch; and a lent phone puts the UI of someone's device behind the reader, which is why the owner kept the row for 2t (2026-10-18). Rejected: a token on the socket (ADR-0035); an authentication handshake per connection (scrcpy's protocol has none).

### Q7. A shared serial and the emulator classification (ADR-0103)

- Blocks: step 2.
- The documents say: `adb-device.md` §Policy and safety names both as known limits; §Device model classifies by serial prefix or product.
- Draft decision: a serial listed twice is refused with its reason (`DEVICE_NOT_ALLOWED`) and never lent; `usb:` makes a device physical first, as today; otherwise a product of the emulator list makes an emulator only on `emulator-<port>` or a loopback serial; anything else is physical. Rejected: identity by USB path (changes with the port, absent off USB); classification by serial alone (some local emulators attach as `127.0.0.1:<port>`).

## Unverified

| Assumption | Steps resting on it | What settles it |
| --- | --- | --- |
| adb on macOS forwards to a `localfilesystem:` socket | 8 | step 8 (a); else its rows stay |
| The live view and input are usable over the company VPN (4 Mbit/s, the round trip) | 3, 9 | the owner's trial |
| A vendor phone installs without an on-device prompt, streams with its encoder, runs the reader | 2–5 | step 10, one phone; other manufacturers the trial |
| The macOS firewall lets an ad-hoc signed `droidyard` accept connections without a prompt per build | 1, 9 | the owner's trial (two machines) |
| The reader's read fence is reachable from a non-shell uid on the AVD (`adb root`, `su <uid>`) | 8 | step 8's check; else the fence is unexercised and its row narrows |
| SELinux keeps apps from the reader's socket on phones as on API 31 | 8 | step 10 if the owner allows; otherwise not run |
| An upload plus an install over the VPN fits the client's 10-minute upload bound and the host's 180 s install bound (a 100 MB APK at 4 Mbit/s uploads in about 200 s) | 4 | the owner's trial |
| Peers of different API versions work beyond the version warning | 1–6 | not run (one binary) |
| Go's TLS 1.3 with self-signed Ed25519 certificates, a required client certificate and pinning on both sides | 1 | step 1's test |
| 15 idle minutes suits a lease | 6 | the owner's use |
| Another account cannot connect to the socket or the forwards | 8 | step 10 (c), only with a second account |

## Review

| Round | Verdict | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | revise | [blocking] B1: `DEVICE_OCCUPIED` undeclared on the Lendable reads and inputs, so the client would turn it into `incompatible` | Step 6's Schema lists it on every Lendable device capability; `TestLease` asserts B's `uiTree` and `watchScreen` read `DEVICE_OCCUPIED` naming A, seen failing with the code undeclared |
| 1 | revise | [blocking] B2: Q5 landed at step 6 while waits were Lendable from step 1, and its test sat below the MCP adapter (`mcp.go` waits and cancels without `Origin`) | Q5 moved to step 1 (it needs nothing from 2s); Builds names `Invoke`'s `Origin`, `operationWait`, the abandoned-wait snapshot and the cancel path; `TestWaitOwnOperations` in `cmd/droidyard` with real shims, the UI and a peer using a `blockedBy` id |
| 1 | revise | [blocking] B3: a bad `listen` stopped `serve`, against ADR-0032 §4 and auto-start; a failed bind undecided | A value failing validation makes the file unusable (ADR-0032 §4), the daemon runs; a failed bind leaves no listener, `hosting.error`, a retry every 30 s; both tested; A1, Q2 §3 reworded |
| 1 | revise | [blocking] B4: Q6 re-proposed ADR-0030's rejected peer-uid check without saying so, and its check passed with SELinux refusing | Q6 is the owner's, supersedes ADR-0030 in part with what changed; step 8 reaches the fence from `su <uid>` on a rooted AVD, seen failing with the fence removed, else Unverified and the row narrows |
| 1 | revise | [blocking] B5: Q4 §2 overrode ADR-0015 §4 for remote phones without a Supersedes line | Taken the other way: ADR-0015 §4 applies unchanged; a remote phone needs the client's `allowPhysical` and `<remote>/<serial>` in its allowlist; test rows and K2 updated; the rejected option named |
| 1 | revise | [blocking] B6: step 7's "removed from the client's list within 2 s" had no mechanism (the UI never polls) | `devices.ts` re-reads on a remote stream's `deviceGone`; the Check is the view stopping within 2 s and the re-read omitting it, seen failing without the re-read |
| 1 | revise | [blocking] B7: Q1 rejected LAN access to the UI for good, which the owner left for later (`mvp.md` §Beyond the MVP); "multi-user or cloud use" unnamed | Q1 decides for devices only and keeps the UI on the LAN for its own ADR; the LAN row narrows instead of closing; the third line named |
| 1 | revise | [should] the reverse lease case; lending emulators against the owner's exclusion; `usb:` precedence for the stand-in; artifact ownership a hidden change; another peer's stream id and revocation on every socket; uploads filling the disk; ADR-0019 §Sessions and 2s's occupation ADR §1, §3 and its rejected alternative | A host agent's hold refuses a peer (Q3 §5, a `TestLease` row); only physical devices are lent; `usb:` first, with a row; `internal/artifact` records the producing session, three 404 rows, the client's store limits; a 4404 row and the pin on each request and open socket; one pending upload per peer, looked up in memory; Q5 and Q3 list the supersessions and cite the rejection |
| 1 | revise | [should] cuts: `idleMinutes`, `via=`, the size | `idleMinutes` a constant (15); `via=` cut (Q3's Rejected); Q1–Q7 shrink to links once the ADRs exist; step 8 stays the named cut |
| 1 | revise | nits: §Meeting 2s files; unique fingerprints; row 91 only narrows; the install bound over a VPN; A6's idle wording | All taken (§Meeting 2s, step 1, Scope, step 4 and Unverified, A6) |
| 2 | revise | B1–B7 resolved (checked against 2s's seven occupying capabilities, `mcp.go`'s wait paths, ADR-0032 §4, the reader's `denied` reply and the AVD's `google_apis` image, `liveview.ts`/`stream.ts`, `mvp.md` §Beyond the MVP) | — |
| 2 | revise | [blocking] N1 (from B5): the client's own refusal written as `DEVICE_NOT_FOUND`, not ADR-0015 §4's `DEVICE_NOT_ALLOWED`; no per-call check in the wrapper | A2 and step 2 split the cases: not lent or refused by the host → `DEVICE_NOT_FOUND`; refused by the client's policy → `DEVICE_NOT_ALLOWED` with cause and nextStep; the wrapper calls `PhysicalAdmitted("<remote>/<serial>")` before any host call, seen failing with it skipped |
| 2 | revise | [blocking] N2 (from B2): Q5 changes `operation_cancel`'s MCP description while A10 and the Starting point said no tool text changes | Step 1's Schema names `operation.cancel`'s description only, no longer than today; the Starting point and A10 say so |
| 2 | revise | nits: the operation records its chat at start; step 7's signal sources; `allowPhysical` beside the allowlist; A10 "but `device.list`" | All taken (step 1 Builds and its test row, step 7, step 2's level 5, `remote add`, A10) |

## Changes

Changes after approval: date, what changed, why, and the ADR if the change touches the design.

- 2026-10-18, at approval: ADR-A to ADR-E and ADR-G are numbered ADR-0097 to ADR-0101 and ADR-0103. ADR-F (Q6) keeps its letter until it is written after step 8 (a), with the reserved number 0102. Q3 is split: ADR-0099 decides §1–§4 and §6; §5, the lease, is **ADR-L** (reserved number 0104), written at step 6 once 2s's occupation ADR (m2s Q1) is on `main`, since it supersedes that ADR in part and `doccheck` needs both files. Where this plan says ADR-0099 for the lease, read ADR-L.
- 2026-10-18, step 1: the wire types (hello, `PeerProtocol`, `PeerPath`, close codes 4400, 4401, 4403) live in `internal/capability/peerwire.go`, since an adapter imports only the core (`TestAdaptersImportOnlyTheCore`); `adapters/peer` declares its own `Host` interface. `policy.get`'s and `policy.update`'s descriptions name the two new sections (human-only, no MCP text changes): step 1's Schema line gains them. `Registry.Register` refuses `Lendable` on a workspace-scoped, `RawTarget` or `Binding` capability; `lend` validation also requires a non-empty `to` and one entry per device. `Origin.Client` is added here (2s adds the same field; the core stamps it from the request). Not taken from the verifier: refusing `Lendable` with audience `human`, since `device.installUpload` is both (ADR-0099 §3); the peer is a human's channel.
- 2026-10-18, step 2: an `emulator-<port>` serial stays an emulator whatever its product (ADR-0103 §2 narrows only IP serials; `usb:` wins first); `TestStopRefusesAnEmulatorOverIP` moves to `127.0.0.1:5555`, the case ADR-0103 keeps. `unreachable` and `incompatible` are `details.state` values, as `booting` and `unresponsive`. Parameter validation for a remote id runs on the host (its `REQUEST_INVALID_PARAMS` is declared, so kept). `OPERATION_CANCELLED` from the host passes through. For the close's register: a call abandoned while its frame is written on a stalled link may still run on the host, unknown to the client (bounded by the link's 10 s write timeout).

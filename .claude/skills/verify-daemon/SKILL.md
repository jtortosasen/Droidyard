---
name: verify-daemon
description: Use when a Droidyard claim needs a real daemon to settle it (the socket, flock, shim auto-start, a killed shim or daemon, orphans after workspace.close, a real MCP stdio session), when a plan step's check is a level-5 run, and when the verifier reruns one. A rerunnable recipe against an isolated instance, in five sections (Launch, Doctor, Drive, Evidence, Cleanup), run end to end on macOS.
---

# Verifying the daemon

> **Device block run 2026-10-04 (macOS, Pixel_9a API 36.1, OpenCode v2.0.18) by step 9 of `docs/plans/m1e-jvm-free-device-slice.md`, evidence `/private/tmp/dy.7Pknqq`.**
>
> **Status: run end to end on 2026-10-03, macOS (Darwin 25.6.0), against the built binary, by step 8 of `docs/plans/m1d-http-websocket-and-events.md`** (evidence `/private/tmp/dy.1SZVqK`); before that by step 8 of `docs/plans/m1c-process-supervisor-and-workspace.md` (evidence `/private/tmp/dy.MZtec4`). All five sections completed. Drive rows run in 1d: the shipped surface, the HTTP row, parity (`system.capabilities` and `workspace.describe`), workspaces, the WebSocket and events rows as Go tests, and the OpenCode procedure with a bound session; the auto-start row and the dummy-sidecar and crash-recovery Go tests were not rerun. The auto-start and single-instance row was last run at milestone 1b (2026-09-30, step 10 of `docs/plans/m1b-daemon-and-mcp-adapter.md`). Every other row names the milestone that makes it runnable. Not yet run on Linux.
>
> A change to a command here is proven only by a run: fix the command and rerun the section in the same change, and say which.

## Launch

Run from the repository root, in one shell: later sections use these variables.

```sh
R=$(mktemp -d /tmp/dy.XXXXXX); R=$(cd -P "$R" && pwd)
export DROIDYARD_STATE_DIR="$R/state" DROIDYARD_CONFIG_DIR="$R/config"
E="$R/evidence"; mkdir -p "$E" "$R/proj"; : >"$R/proj/settings.gradle.kts"
SOCK="$DROIDYARD_STATE_DIR/run/droidyard.sock"
go build -o "$R/droidyard" ./cmd/droidyard && DY="$R/droidyard"
{ uname -sr; go version; git describe --always --dirty; } >"$E/env"
run() { n=$1; shift; printf '%s\n' "$*" >"$E/$n.cmd"; "$@" </dev/null >"$E/$n.out" 2>"$E/$n.err"; s=$?; echo $s >"$E/$n.exit"; return $s; }

run pre-health "$DY" call --no-autostart system.health && echo "STOP: a daemon that is not yours answered"
"$DY" serve >"$E/serve.out" 2>&1 & PID=$!
for i in $(seq 50); do [ -S "$SOCK" ] && break; sleep 0.2; done
```

- **`$R/proj` is a Gradle project root** (it holds `settings.gradle.kts`), so a session or CLI started inside it binds to it (ADR-0019 §Sessions).
- **A short base under `/tmp`.** The socket path must fit `sun_path` (104 bytes on macOS). Past that, ADR-0019 §6 moves it to `/tmp/droidyard-<uid>/<hash of the state root>/`: still one socket per state root, but not `$SOCK`, so the Doctor fails. Under `/tmp` the socket path is about 47 bytes; under a resolved macOS `$TMPDIR` it is about 90. `cd -P` resolves `/tmp` to `/private/tmp` on macOS, so the paths the Doctor compares match.
- **One variable isolates the socket on Linux too**: an explicit `DROIDYARD_STATE_DIR` wins over `$XDG_RUNTIME_DIR` (ADR-0021 §Location; ADR-0019 §6).
- **`pre-health` must fail.** If it succeeds, the client ignored the scratch environment and reached another daemon. It proves nothing when no other daemon is running. `--no-autostart` keeps it from starting one.
- **Foreground `serve`, backgrounded by you**, so `$PID` is yours. Auto-start is a Drive row, not the launch.
- **The daemon's environment is fixed at start** (ADR-0019 §8). A scenario that needs another environment (no `adb` on `PATH`) needs its own launch.

## Doctor

Read-only. It answers one question: is the instance you are about to drive the one you started, in the scratch state? If any check fails, stop. Do not drive it, and do not repair it by killing something.

```sh
kill -0 "$PID"
jq -e --arg p "$PID" --arg s "$SOCK" '(.pid|tostring)==$p and .socket==$s' "$DROIDYARD_STATE_DIR/daemon.lock"
lsof -a -p "$PID" -U -Fn | grep -qxF "n$SOCK"      # Linux without lsof: ss -xlp | grep -F "$SOCK"
run doctor-status "$DY" status && grep -qw "$PID" "$E/doctor-status.out"
run doctor-health "$DY" call --no-autostart system.health
HP=$(jq -r .http "$DROIDYARD_STATE_DIR/daemon.lock" | sed 's/.*://')
lsof -nP -a -p "$PID" -iTCP -sTCP:LISTEN -Fn | grep -qxF "n127.0.0.1:$HP"
```

In order: the process you started is alive (a failed bind shows in `serve.out`); the lock in the scratch state names it and the scratch socket (fields from ADR-0021 §Layout); that process is the one bound to the socket; the client side resolves the same state root (`status` prints `daemon pid <pid>` and exits 0 only if the socket answers); the socket answers through a shim; the lock's `http` port is the one `$PID` itself listens on (`-nP`: without it `lsof` prints `localhost:<port>`). `system.health` alone cannot prove identity: its result is uptime, version and operation count, and any daemon of the same build reports the same version.

## Drive

Pass `--no-autostart` everywhere except the auto-start row: with it, a mis-isolated client fails; without it, the client may start or reach a daemon that is not yours (ADR-0019 §4).

**The shipped surface.** The binary under test carries no test fixture and publishes the committed schema (test capabilities and the dummy sidecar live in `_test.go` files and `internal/process/processtest`). Each line prints nothing, or `0`:

```sh
"$DY" schema | cmp - schema/capabilities.json
"$DY" caps | grep -c '^fixture\.'
go list -deps ./cmd/droidyard | grep -c processtest
```

**CLI:** `run <name> "$DY" call --no-autostart <capability> --params '<json>'`. It prints the response envelope and exits non-zero iff the envelope carries `error`.

**MCP:** a scripted stdio session. A fifo keeps stdin open until you close it, because EOF ends the session, and `$SHIM` is the shim's own pid.

```sh
send() { printf '%s\n' "$1" | tee -a "$E/$N.sent" >&3; }
await() { for i in $(seq 300); do jq -e --argjson i "$1" 'select(.id==$i)' "$E/$N.out" >/dev/null 2>&1 && return 0; sleep 0.1; done; return 1; }
mcp_open() {
  N=$1; mkfifo "$E/$N.in"
  "$DY" mcp --no-autostart --root "$R/proj" <"$E/$N.in" >"$E/$N.out" 2>"$E/$N.err" & SHIM=$!
  exec 3>"$E/$N.in"
  send '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"verify-daemon","version":"0"}}}'
  await 0 && send '{"jsonrpc":"2.0","method":"notifications/initialized"}'
}
mcp_close() { exec 3>&-; wait "$SHIM"; echo $? >"$E/$N.exit"; }

mcp_open caps
send '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"system_capabilities","arguments":{}}}'
await 1; mcp_close
```

`await` gives up after 30 s, above the default 20 s wait budget (`droidyard mcp --wait`); a timeout is a failure, not a retry. `mcp_close` ends the input: the shim half-closes the socket and exits 0 once the daemon ends the session, so `$N.exit` must read 0. Nothing is written to a session after its input ends, so await every response before closing. The session is bound to `$R/proj` and opens it before it answers `initialize`: workspace tools act on it and take no workspace argument (ADR-0019 §Sessions; ADR-0020 §Adapter projections). The CLI names it with `--workspace "$R/proj"`, since the shell's cwd is the repository.

| Phase 0 acceptance (`docs/roadmap/mvp.md`) | Drive | Passes when |
| --- | --- | --- |
| `system.capabilities` lists what is available; `degraded` without `adb` (R1: no `degraded` field) | The scrubbed launch in the Device block below: `env -i` with `HOME` at an empty directory and `PATH=/usr/bin:/bin`, since the host SDK default (`~/Library/Android/sdk`) resolves `adb` before `PATH` on a Mac that has the SDK | `device.list`, `device.uiTree` and `device.screenshot` are `available: false` with a reason, `artifact.read` is `available: true`; `call device.list` exits non-zero with `DEVICE_ADB_NOT_FOUND`; `system.health` has `adb.found: false` with every rejected candidate |
| The same capability from the CLI and MCP, identical semantics | `system.capabilities` through `call`, and through the session above; `workspace.describe` likewise, in the workspace block below | `jq -S .result` of the CLI envelope equals `jq -S 'select(.id==1).result.structuredContent'` of `caps.out`; the same for `ws-describe-cli` and `ws.out` id 1 |
| Workspaces: open, describe, close, and a bound session that is not reopened (ADR-0019 §Sessions; ADR-0020 §Scope and workspace addressing; ADR-0027) | The workspace block below | `open` and `describe` answer `ready`, `root` `$R/proj`, `trusted: false`; `close` exits 0; the session's `workspace_describe` after the close is `isError` with `WORKSPACE_NOT_OPEN`, both times |
| Killing a shim does not stop an operation it started (ADR-0020; ADR-0019 §Sessions) | Start a long-running capability in an MCP session, read the `operationId` of its view, `kill -9 "$SHIM"`, then `call operation.wait --params '{"operationId":"<id>"}'`. None ships before `device.logs` (device slice); `TestMCPCancellation` covers it on a fixture | The envelope's `operation.state` is `completed`, not `cancelled`, and `call` exits 0 |
| A dummy sidecar starts lazily, idles out, leaves no orphans after `workspace.close` | The shipped binary has no sidecar (none before the Gradle sidecar, Phase 1), so the row is Go tests on the re-executed binary with a test-only fixture: `run sidecar-tests` in the block below | `ok`; each test counts orphans by pid and start time, as `count_orphans` does |
| Auto-start and single instance (ADR-0019 §4, §5; mvp.md Phase 0 deliverables) | Stop the launched daemon, then two concurrent `call system.health` without `--no-autostart`; then a second `serve` on the same state (block below) | Both calls exit 0 with an envelope; `daemon.lock` appears in the scratch state; with `PID=$(jq -r .pid "$DROIDYARD_STATE_DIR/daemon.lock")` the Doctor passes, and that pid is now yours to stop; every other pid with a `starting` line in `daemon.log` has a `lost the lock` line and is gone; the second `serve` exits non-zero |
| Crash recovery (ADR-0021 §Pid records) | As the row above: `run crash-tests` in the block below | `ok`; no orphan once the new daemon answers |
| HTTP on loopback only, behind the fence | The HTTP block below. The Host, Origin and token checks are in `docs/interfaces/http-websocket.md` §Security baseline | `lsof` lists `127.0.0.1:$HP` and nothing else; `evil.example` as Host 403, no token 401, a foreign Origin 403, `OPTIONS` 403, the token 200 with an envelope; `run/http-token` is `600` and `run/` `700`; `status` prints the URL whose port and token are the lock's and the file's; after Cleanup's `stop` the token file is gone |
| WebSocket and events (ADR-0028; `http-websocket.md` §`GET /v1/ws`) | Go tests that start their own in-process daemons: `run ws-tests` and `run ev-tests` in the block below | `ok`. A real client over `/v1/ws` waits for the Phase 2 UI |

Run only the rows the milestone's plan puts in scope; the plan's step names the row it runs. The two rows run at milestone 1b, after `mcp_open caps` above:

```sh
run parity-cli "$DY" call --no-autostart system.capabilities
jq -S .result "$E/parity-cli.out" >"$E/parity.cli.json"
jq -S 'select(.id==1).result.structuredContent' "$E/caps.out" >"$E/parity.mcp.json"
cmp "$E/parity.cli.json" "$E/parity.mcp.json" && echo parity-ok

run stop-launched "$DY" stop; for i in $(seq 50); do kill -0 "$PID" 2>/dev/null || break; sleep 0.2; done
"$DY" call system.health >"$E/auto-a.out" 2>"$E/auto-a.err" & A=$!
"$DY" call system.health >"$E/auto-b.out" 2>"$E/auto-b.err" & B=$!
wait "$A"; echo $? >"$E/auto-a.exit"; wait "$B"; echo $? >"$E/auto-b.exit"
PID=$(jq -r .pid "$DROIDYARD_STATE_DIR/daemon.lock")
for f in "$E"/doctor-*; do mv "$f" "$E/launch.${f##*/}"; done   # keep the first Doctor's files
# Run the Doctor again here, against the new $PID; then:
LOG="$DROIDYARD_STATE_DIR/daemon.log"
for p in $(sed -n 's/^droidyard serve (pid \([0-9]*\)): starting$/\1/p' "$LOG"); do
  [ "$p" = "$PID" ] && continue
  for i in $(seq 20); do kill -0 "$p" 2>/dev/null || break; sleep 0.1; done   # a loser gives up after 100 ms
  grep -q "(pid $p): lost the lock" "$LOG" && ! kill -0 "$p" 2>/dev/null && echo "loser $p exited" || echo "FAIL: $p"
done
run second-serve "$DY" serve || echo "second serve exited $(cat "$E/second-serve.exit")"
```

The two rows that are Go tests (they start their own isolated daemons, not `$PID`):

```sh
run sidecar-tests go test -count=1 -race -v -run 'TestSidecarStartsLazilyIdlesOutAndEndsWithClose|TestCloseCancelsTheLeaseHolderBeforeTheSidecar|TestCloseBoundsADrainALeaseHolderIgnores|TestStopLeavesNoSidecar|TestStopKillsSidecarsPastTheShutdownDeadline' ./cmd/droidyard
run crash-tests go test -count=1 -race -v -run 'TestKilledDaemonsSidecarExitsOnEOF|TestKilledDaemonsStubbornSidecarIsReapedByTheNext|TestServeReapsOrphansBeforeItAnswers' ./cmd/droidyard
run ws-tests go test -count=1 -race -run 'TestWS' ./adapters/httpapi ./cmd/droidyard   # the 16 TestWS* tests are in cmd/droidyard; httpapi has none ("no tests to run")
run ev-tests go test -count=1 -race -run 'TestEvents' ./cmd/droidyard
```

The HTTP block (1d), run with the launched daemon up. `$DROIDYARD_STATE_DIR/run/http-token` is the bearer token:

```sh
TOK=$(cat "$DROIDYARD_STATE_DIR/run/http-token"); U="http://127.0.0.1:$HP/v1/call"; B='{"capability":"system.health"}'
lsof -nP -a -p "$PID" -iTCP -sTCP:LISTEN | tee "$E/http-lsof.out"
c() { n=$1; shift; curl -sS -o "$E/$n.out" -w '%{http_code}\n' "$@" >"$E/$n.code" 2>"$E/$n.err"; echo "$n $(cat "$E/$n.code")"; }
c http-host    -H 'Host: evil.example' -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"   # 403
c http-notoken -H 'Content-Type: application/json' -d "$B" "$U"                                                          # 401
c http-origin  -H 'Origin: http://evil.example' -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"   # 403
c http-options -X OPTIONS "$U"                                                                                           # 403
c http-ok      -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"                          # 200, an envelope
stat -f '%Lp %N' "$DROIDYARD_STATE_DIR/run/http-token" "$DROIDYARD_STATE_DIR/run" | tee "$E/http-stat.out"   # 600, 700 (Linux: stat -c '%a %n')
[ "$(sed -n 's/^\(http:.*\)$/\1/p' "$E/doctor-status.out")" = "http://127.0.0.1:$HP/#token=$TOK" ] && echo url-matches
ps -o rss= -p "$PID" | tee "$E/rss-idle.out"      # ADR-0025 criterion 7: recorded, not asserted
```

`droidyard ui --no-autostart` opens the tokenised URL in the default browser (macOS `open`); the daemon serves the placeholder at `GET /`. It opens a tab on the machine, so run it once, and record what you see: the recipe cannot observe the tab.

The workspace block, run before the auto-start row stops the launched daemon:

```sh
mcp_open ws
run ws-open "$DY" call --no-autostart --workspace "$R/proj" workspace.open
run ws-describe-cli "$DY" call --no-autostart --workspace "$R/proj" workspace.describe
send '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 1
jq -S .result "$E/ws-describe-cli.out" >"$E/ws-parity.cli.json"
jq -S 'select(.id==1).result.structuredContent' "$E/ws.out" >"$E/ws-parity.mcp.json"
cmp "$E/ws-parity.cli.json" "$E/ws-parity.mcp.json" && echo ws-parity-ok
run ws-close "$DY" call --no-autostart --workspace "$R/proj" workspace.close
send '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 2
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 3
jq -c 'select(.id==2 or .id==3).result | [.isError, (.content[0].text|fromjson|.code)]' "$E/ws.out"
mcp_close
```

The last `jq` prints `[true,"WORKSPACE_NOT_OPEN"]` twice: nothing reopened the workspace.

A losing `serve` retries for 100 ms before it gives up, in case the lock was only a `status` or `stop` probe; the loop waits for that. If only one `starting` line appears, the second call reached the first daemon before starting its own: the row still holds, but the race was not exercised; `TestRacingAutoStarts` exercises it every time.

**Real client (OpenCode):** a scratch project whose configuration starts the shim behind a recorder, so the wire is evidence. `$DY` and `$E` are expanded when the files are written. The shim takes no `--root`: OpenCode starts it with `$R/proj` as its cwd, so it binds there, and that is part of what the run shows.

```sh
cat >"$R/tap.sh" <<EOF
#!/bin/sh
tee -a "$E/wire.c2s.jsonl" | "$DY" mcp --no-autostart | tee -a "$E/wire.s2c.jsonl"
EOF
chmod +x "$R/tap.sh"
cat >"$R/proj/opencode.jsonc" <<EOF
{"mcp": {"servers": {"droidyard": {"type": "local", "command": ["$R/tap.sh"],
  "environment": {"DROIDYARD_STATE_DIR": "$DROIDYARD_STATE_DIR"}, "codemode": false}}}}
EOF
opencode --version >>"$E/env"
(cd "$R/proj" && opencode run --standalone --format json --print-logs --auto -m <provider/model> "<prompt naming the calls>" \
  </dev/null >"$E/opencode.out" 2>"$E/opencode.err"); echo $? >"$E/opencode.exit"
```

- `opencode.err` should log the connection, `message="mcp connected" server=droidyard tools=<n>` (`grep 'message="mcp connected" server=droidyard'`), and no schema warning; `opencode.out` is the event stream, the wire files are what was sent and received.
- For the device slice, name `device_list`, `device_uiTree` and `device_screenshot`: `tools=11`; `jq -c 'select(.result.content?)|{id,blocks:[.result.content[]|{type,mimeType}]}' "$E/wire.s2c.jsonl"` shows the image block after the text on the screenshot call. OpenCode may spend calls on its own `execute` tool search first; read what was sent from `wire.c2s.jsonl`.
- Name `workspace_describe` in the prompt. On the wire its result is `ready`, `root` `$R/proj`, `trusted: false`: `jq -c 'select(.result.structuredContent.root?) | .result.structuredContent | {state, root, trusted}' "$E/wire.s2c.jsonl"`.
- OpenCode validates arguments against the `inputSchema` before sending: unknown keys are dropped and types coerced, so an out-of-schema argument never reaches the core. To see an error result, use a value the schema admits, such as `operation_wait` on an unknown id. Read what was sent from `wire.c2s.jsonl`, never from the model's summary.
- The event stream shows the text rendering only; `structuredContent` is in `wire.s2c.jsonl`.

**Device block (1e, run 2026-10-04; `docs/plans/m1e-jvm-free-device-slice.md` step 9).** Needs a booted emulator (`Pixel_9a`, portrait) and `adb` from the host SDK. Never kill or restart the shared adb server on 5037: note its pid first (`lsof -nP -iTCP:5037 -sTCP:LISTEN`) and compare at the end. The daemon launched in Launch uses the normal environment. A device with another app that depends on an accessibility service (the owner's Qustodio) goes to the foreground first, so the `uiTree` row means something.

```sh
# degraded (R1): a scrubbed launch, its own state; the host SDK default would otherwise resolve adb
mkdir -p "$R/home" "$R/dstate" "$R/dconfig"
D() { env -i HOME="$R/home" PATH=/usr/bin:/bin DROIDYARD_STATE_DIR="$R/dstate" DROIDYARD_CONFIG_DIR="$R/dconfig" "$@"; }
D "$DY" serve >"$E/deg-serve.out" 2>&1 &
for i in $(seq 50); do [ -S "$R/dstate/run/droidyard.sock" ] && break; sleep 0.2; done
D "$DY" call --no-autostart system.capabilities >"$E/deg-caps.out"
jq -c '.result.capabilities[]|select(.name|test("^device\\.|^artifact\\."))|{name,available}' "$E/deg-caps.out"
D "$DY" call --no-autostart device.list >"$E/deg-list.out"; echo "exit $?"          # 1, DEVICE_ADB_NOT_FOUND
D "$DY" call --no-autostart system.health | jq -c .result.adb.found                  # false
D "$DY" stop

# normal launch (Launch), then, with the emulator in the foreground app:
C="$DY call --no-autostart"; EM='{"device":"emulator-5554"}'
adb shell monkey -p <package> -c android.intent.category.LAUNCHER 1; adb shell pidof <package>   # note the pid
adb logcat -c
run list $C device.list                                  # emulator-5554, kind emulator, avdName Pixel_9a
run uitree $C device.uiTree --params "$EM"               # nodes, meta.untrusted true, wc -c < 40960
adb shell pidof <package>                                # unchanged
adb logcat -d | grep -i 'flags=0x\|malfunction'          # flags=0x1 only (ADR-0029)
run detach $C device.uiTree --detach --params "$EM"; OP=$(tr -d '\n' <"$E/detach.out"); sleep 0.4
run cancel $C operation.cancel --params "{\"operationId\":\"$OP\"}"
run cwait $C operation.wait --params "{\"operationId\":\"$OP\"}"     # .operation.state cancelled
adb shell ps -A | grep -i 'uiautomator\|app_process'; adb shell ls /data/local/tmp | grep droidyard   # nothing left
run ui3 $C device.uiTree --params "$EM"                  # succeeds at once
run shot $C device.screenshot --params "$EM"; REF=$(jq -r .result.ref "$E/shot.out")
run read $C artifact.read --params "{\"ref\":\"$REF\"}"   # stored size == adb shell wm size
ID=${REF#droidyard://artifact/}; TOK=$(cat "$DROIDYARD_STATE_DIR/run/http-token")
curl -sS -D "$E/art.hdr" -o "$E/art.png" -w '%{http_code}\n' -H "Authorization: Bearer $TOK" "http://127.0.0.1:$HP/v1/artifacts/$ID"   # 200 image/png, X-Droidyard-Untrusted: true
# RSS for ADR-0025 / Q5: twenty screenshots, each followed by an artifact.read (alternating maxEdge 512), then again 16 minutes later
ps -o rss= -p "$PID"
# (OpenCode, below, with a prompt naming device_list, device_uiTree and device_screenshot)

# 410 after a restart: `droidyard stop`, a new `serve`, then the same $ID answers 410 ARTIFACT_EXPIRED

# (2b), LAST of the device rows, with no emulator booted (`adb -s emulator-5554 emu kill`, then wait until `ps aux | grep qemu-system` is empty and `adb devices` lists nothing:
# a dying emulator is still listed for ~20 s and an adb server on 5199 would attach to it). A server the daemon forks on an isolated port:
adb devices -l >"$E/devices.2b.before"
export ANDROID_ADB_SERVER_PORT=5199          # in the second daemon's environment only
DROIDYARD_STATE_DIR="$R/s2b" DROIDYARD_CONFIG_DIR="$R/c2b" "$DY" serve & P2=$!
DROIDYARD_STATE_DIR="$R/s2b" DROIDYARD_CONFIG_DIR="$R/c2b" "$DY" call --no-autostart device.list
S=$(lsof -nP -iTCP:5199 -sTCP:LISTEN -t)     # the forked server: pid, lstart, pgid differ from $P2's (ps -o pid=,lstart=,pgid=)
DROIDYARD_STATE_DIR="$R/s2b" DROIDYARD_CONFIG_DIR="$R/c2b" "$DY" stop   # $S stays alive, same pid and start time
ANDROID_ADB_SERVER_PORT=5199 adb kill-server; unset ANDROID_ADB_SERVER_PORT
adb devices -l | cmp - "$E/devices.2b.before"   # identical; then boot Pixel_9a again if it was running before

## Evidence

Everything stays under `$E`. For each named step, `run` keeps the command line, stdout, stderr and exit code; each MCP session keeps `<name>.sent`, `<name>.out`, `<name>.err` and `<name>.exit`. Keep `serve.out`, `<state>/daemon.log` once a daemon was auto-started, the wire files of a real-client run, and the snapshots. This build logs no operation records; which surface returns them (`docs/architecture/overview.md`, the capability trace) is an open owner question, not decided by milestone 1b. The report quotes the decisive line and gives `$R`. On a plan step it is level 5 evidence (`prove-change`).

## Cleanup

Stop only what this run started, count orphans, and delete nothing.

```sh
snapshot() {
  desc() { for c in $(pgrep -P "$1"); do echo "$c"; desc "$c"; done; }
  { echo "$PID"; desc "$PID"; jq -r '.. | .pid? // empty' "$DROIDYARD_STATE_DIR/processes.json" 2>/dev/null; } | sort -u |
    while read -r p; do s=$(ps -o lstart= -p "$p") && printf '%s\t%s\t%s\n' "$p" "$s" "$(ps -o command= -p "$p")"; done >"$E/$1.procs"
  cp "$DROIDYARD_STATE_DIR/processes.json" "$E/$1.processes.json" 2>/dev/null
}
count_orphans() {
  while IFS="$(printf '\t')" read -r p s c; do [ "$(ps -o lstart= -p "$p" 2>/dev/null)" = "$s" ] && printf '%s\t%s\n' "$p" "$c"; done <"$E/$1.procs" >"$E/$1.orphans"
  wc -l <"$E/$1.orphans"
}

exec 3>&-
[ -n "$SHIM" ] && kill -0 "$SHIM" 2>/dev/null && kill "$SHIM"
snapshot final
run stop "$DY" stop
for i in $(seq 50); do kill -0 "$PID" 2>/dev/null || break; sleep 0.2; done
kill -0 "$PID" 2>/dev/null && kill -TERM "$PID"
count_orphans final
[ -e "$DROIDYARD_STATE_DIR/run/http-token" ] && echo "FAIL: token file left"
echo "$R"
```

- **Never kill by name.** No `pkill`, no `killall`: the owner's daemon and shims are also called `droidyard`. Kill only a pid this run started (`$PID`, `$SHIM`) or read from this state's `daemon.lock`. `droidyard stop` under the scratch environment targets that lock's holder.
- **The orphan count is the acceptance evidence.** A survivor is a pid from the snapshot, taken just before the stop, that is still alive with the same start time. That means the daemon, its descendants and every pid recorded in `processes.json`; the start time guards against pid reuse. The Definition of done excludes the `adb` server, the Gradle daemon and emulators. Move those lines to `$E/final.excluded` by hand, each with its command line, and never widen that exclusion to hide a sidecar. Report other survivors as findings before you stop them by pid.
- **Delete nothing.** `$R` holds the evidence and the state it describes. The verifier cannot `rm`, and the recipe never needs to.

## Keeping it true

When a run needs a step the recipe lacks, or a step here proves wrong, change the recipe in the same change as the run. When a row becomes a Go test driving the processes (the durable form `prove-change` prefers), replace the row's Drive with the test's name.

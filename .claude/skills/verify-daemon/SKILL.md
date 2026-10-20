---
name: verify-daemon
description: Use when a Droidyard claim needs a real daemon to settle it (the socket, flock, shim auto-start, a killed shim or daemon, orphans after workspace.close, a real MCP stdio session), when a plan step's check is a level-5 run, and when the verifier reruns one. A rerunnable recipe against an isolated instance, in five sections (Launch, Doctor, Drive, Evidence, Cleanup), run end to end on macOS.
---

# Verifying the daemon

> A generic recipe against an isolated instance, run end to end on macOS. The per-milestone Drive blocks (device actions, build, probe, logs, files, policy, acceptance, emulator ownership, the reader, the pre-serial launch) were cut at milestone 1q; the last commit that held them is `20daa85` (`git show 20daa85:.claude/skills/verify-daemon/SKILL.md`). A milestone that needs one takes it from there and adapts it. A change to a command here is proven only by a run: fix the command and rerun the section in the same change, and say which. Not yet run on Linux.

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
- **A short base under `/tmp`.** The socket path must fit `sun_path` (104 bytes on macOS). Past that, ADR-0019 §6 moves it elsewhere and the Doctor fails. `cd -P` resolves `/tmp` to `/private/tmp` on macOS, so the paths the Doctor compares match.
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

**The shipped surface.** The binary under test carries no test fixture and publishes the committed schema (test capabilities and the dummy sidecar live in `_test.go` files and `internal/process/processtest`). Check: `"$DY" schema | cmp - schema/capabilities.json` and `"$DY" caps | grep -c '^fixture\.'` and `go list -deps ./cmd/droidyard | grep -c processtest` print nothing, `0` and `0`.

**CLI:** `run <name> "$DY" call --no-autostart <capability> --params '<json>'`. It prints the response envelope and exits non-zero iff the envelope carries `error`. A global capability (`emulator.*`, `device.*` but `install`) takes no `--workspace`.

**The Definition of done run:** `samples/mvp-dod.sh` is this recipe scripted for `mvp.md` DoD 3 and 6 (`errors <kgp> <outdir>`, `footprint <outdir> [avd]`; plan m2h D2). A change to §Launch, §Doctor or §Cleanup is made in the script too.

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

Pick the Drive recipes the plan's step names. The checks that settle most claims:

- **Parity.** `jq -S .result` of a CLI envelope equals `jq -S 'select(.id==1).result.structuredContent'` of the session's `.out`, for the same capability (`system.capabilities`, `workspace.describe`).
- **A shim killed mid-operation.** Start a long-running capability in an MCP session, read its `operationId`, `kill -9 "$SHIM"`, then `call operation.wait`: the state is `completed`, not `cancelled` (ADR-0020; ADR-0019 §Sessions).
- **Auto-start and single instance** (ADR-0019 §4, §5):
```sh
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

Both calls exit 0; every other `starting` pid has a `lost the lock` line and is gone; the second `serve` exits non-zero. One `starting` line means the race was not exercised (`TestRacingAutoStarts` does).

- **A killed daemon** (ADR-0021 §Pid records): `kill -9 "$PID"`, start a new `serve` on the same state, and count orphans (Cleanup) once it answers: none. The Go tests `TestKilledDaemonsSidecarExitsOnEOF`, `TestKilledDaemonsStubbornSidecarIsReapedByTheNext` and `TestServeReapsOrphansBeforeItAnswers` (`./cmd/droidyard`) cover the sidecar cases.
- **HTTP on loopback, behind the fence** (`docs/interfaces/http-websocket.md` §Security baseline), with the launched daemon up:
```sh
TOK=$(cat "$DROIDYARD_STATE_DIR/run/http-token"); U="http://127.0.0.1:$HP/v1/call"; B='{"capability":"system.health"}'
c() { n=$1; shift; curl -sS -o "$E/$n.out" -w '%{http_code}\n' "$@" >"$E/$n.code" 2>"$E/$n.err"; echo "$n $(cat "$E/$n.code")"; }
c http-host    -H 'Host: evil.example' -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"   # 403
c http-notoken -H 'Content-Type: application/json' -d "$B" "$U"                                                          # 401
c http-origin  -H 'Origin: http://evil.example' -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"   # 403
c http-ok      -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$B" "$U"                          # 200, an envelope
stat -f '%Lp %N' "$DROIDYARD_STATE_DIR/run/http-token" "$DROIDYARD_STATE_DIR/run" | tee "$E/http-stat.out"   # 600, 700 (Linux: stat -c '%a %n')
```

Expect the codes in the comments, token file `600`, `run/` `700`, the token file gone after Cleanup's `stop`. `droidyard ui --no-autostart` opens a browser tab on the machine: run it once and record what you see.

- **Workspaces** (ADR-0019 §Sessions; ADR-0027), before the auto-start recipe stops the launched daemon:
```sh
mcp_open ws
run ws-open "$DY" call --no-autostart --workspace "$R/proj" workspace.open
send '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 1
run ws-close "$DY" call --no-autostart --workspace "$R/proj" workspace.close
send '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 2
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"workspace_describe","arguments":{}}}'; await 3
jq -c 'select(.id==2 or .id==3).result | [.isError, (.content[0].text|fromjson|.code)]' "$E/ws.out"
mcp_close
```

The last `jq` prints `[true,"WORKSPACE_NOT_OPEN"]` twice.

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

- `opencode.err` should log `message="mcp connected" server=droidyard tools=<n>` and no schema warning; `opencode.out` is the event stream, the wire files are what was sent and received. Read what was sent from `wire.c2s.jsonl`, never from the model's summary; `structuredContent` is only in `wire.s2c.jsonl`.
- OpenCode validates arguments against the `inputSchema` before sending: unknown keys are dropped and types coerced, so an out-of-schema argument never reaches the core. To see an error result, use a value the schema admits, such as `operation_wait` on an unknown id.
- Deny the client's own shell and edit tools when the run must prove the droidyard tools do the work: the config's `"permission": {"bash":"deny","edit":"deny","read":"deny",...}`; check with `opencode debug agents`.

**Real client (Cursor):** the same recorder in `$G/.cursor/mcp.json` and `$G/.cursor/cli.json` `{"permissions":{"allow":["Mcp(droidyard:*)"],"deny":["Shell(*)","Read(**)","Write(**)"]}}`. Run with `CURSOR_DATA_DIR` set to a scratch directory; enable only the droidyard server (`cursor-agent mcp enable droidyard`; disable the others), then `cursor-agent -p --trust --output-format stream-json --model <model> --workspace "$G" "<prompt>"` (never `-f`, `--yolo` or `--approve-mcps`, never `status` or `login`).

**Emulator.** Never touch an emulator you did not start for the run. Use a scratch AVD in an isolated `ANDROID_AVD_HOME`, a guard script as `ADB` that refuses what the plan forbids, and an isolated adb server (`ANDROID_ADB_SERVER_PORT=5199`, in the daemon's environment only). Never kill or restart the shared server on 5037: note its pid first (`lsof -nP -iTCP:5037 -sTCP:LISTEN`) and compare at the end. Host-side `adb` checks run with the port variable unset, or they start a server on the run's port. A detached `emulator.start` cancelled or stopped with the daemon before the serial is gone within a second; after the serial is `device`, the instance stays. A hand-started instance is never signalled (`EMULATOR_AVD_LOCKED`). To model a hung launch, `kill -STOP` the qemu once the AVD lock appears. `kill-server` takes a moment: wait until nothing listens before reading the result. A degraded run (no `adb`) needs its own scrubbed launch, since the daemon's environment is fixed at start:

```sh
mkdir -p "$R/home" "$R/dstate" "$R/dconfig"
D() { env -i HOME="$R/home" PATH=/usr/bin:/bin DROIDYARD_STATE_DIR="$R/dstate" DROIDYARD_CONFIG_DIR="$R/dconfig" "$@"; }
D "$DY" serve >"$E/deg-serve.out" 2>&1 &
D "$DY" call --no-autostart device.list; echo "exit $?"        # 1, DEVICE_ADB_NOT_FOUND
D "$DY" stop
```

`HOME` is an empty directory: the host SDK default (`~/Library/Android/sdk`) would otherwise resolve `adb` before `PATH`.

## Evidence

Everything stays under `$E`. For each named step, `run` keeps the command line, stdout, stderr and exit code; each MCP session keeps `<name>.sent`, `<name>.out`, `<name>.err` and `<name>.exit`. Keep `serve.out`, `<state>/daemon.log` once a daemon was auto-started, the wire files of a real-client run, and the snapshots. The report quotes the decisive line and gives `$R`. On a plan step it is level 5 evidence (`prove-change`).

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

- **Compare against the milestone's first baseline.** Snapshot the owner's daemon and adb server (pids and start times) once, before a milestone's first run, and check every later snapshot against that one, not only a run's own before and after: in m2k a pid lost between runs went unseen for two hours.
- **Never kill by name.** No `pkill`, no `killall`: the owner's daemon and shims are also called `droidyard`. Kill only a pid this run started (`$PID`, `$SHIM`) or read from this state's `daemon.lock`. `droidyard stop` under the scratch environment targets that lock's holder.
- **The orphan count is the acceptance evidence.** A survivor is a pid from the snapshot, taken just before the stop, that is still alive with the same start time. That means the daemon, its descendants and every pid recorded in `processes.json`; the start time guards against pid reuse. The Definition of done excludes the `adb` server, the Gradle daemon and emulators. Move those lines to `$E/final.excluded` by hand, each with its command line, and never widen that exclusion to hide a sidecar. Report other survivors as findings before you stop them by pid.
- **Delete nothing.** `$R` holds the evidence and the state it describes. The verifier cannot `rm`, and the recipe never needs to.

## Keeping it true

When a run needs a step the recipe lacks, or a step here proves wrong, change the recipe in the same change as the run. When a row becomes a Go test driving the processes (the durable form `prove-change` prefers), replace the row's Drive with the test's name.

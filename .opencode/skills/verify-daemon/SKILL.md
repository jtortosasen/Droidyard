---
name: Verify the daemon
description: Use when a Droidyard claim needs a real daemon to settle it (the socket, flock, shim auto-start, a killed shim or daemon, orphans after workspace.close, a real MCP stdio session), when a plan step's check is a level-5 run, and when the verifier reruns one. A rerunnable recipe against an isolated instance, in five sections (Launch, Doctor, Drive, Evidence, Cleanup). Draft until its first end-to-end run.
---

# Verifying the daemon

> **Status: draft, never run end to end.** In this build `serve`, `mcp` and `call` exit 1 with "not implemented" (`cmd/droidyard/main.go`), and no code reads `DROIDYARD_STATE_DIR` or `DROIDYARD_CONFIG_DIR`. Every command below is written from the documents for milestone 1b, not against a binary. Marked *assumed*: what no document fixes yet.
>
> The recipe counts only once one run has completed all five sections on one platform. That run promotes it: replace this block with the date, the OS and the plan step that ran it, and fix every command that proved wrong in the same change. A run that stops partway leaves it a draft, and its evidence is labelled as coming from a draft recipe.
>
> Checked on 2026-09-29 (macOS, bash and zsh) against a stand-in script, not `droidyard`: every block parses; `run`, the fifo MCP helpers, `snapshot` and `count_orphans` behave as written; the Doctor's `lsof` line matches a real listening socket. That checks the shell, not the daemon.

## Launch

Run from the repository root, in one shell: later sections use these variables.

```sh
R=$(mktemp -d /tmp/dy.XXXXXX); R=$(cd -P "$R" && pwd)
export DROIDYARD_STATE_DIR="$R/state" DROIDYARD_CONFIG_DIR="$R/config"
E="$R/evidence"; mkdir -p "$E" "$R/proj"
SOCK="$DROIDYARD_STATE_DIR/run/droidyard.sock"
go build -o "$R/droidyard" ./cmd/droidyard && DY="$R/droidyard"
{ uname -sr; go version; git describe --always --dirty; } >"$E/env"
run() { n=$1; shift; printf '%s\n' "$*" >"$E/$n.cmd"; "$@" </dev/null >"$E/$n.out" 2>"$E/$n.err"; s=$?; echo $s >"$E/$n.exit"; return $s; }

run pre-health "$DY" call --no-autostart system.health && echo "STOP: a daemon that is not yours answered"
"$DY" serve >"$E/serve.out" 2>&1 & PID=$!
for i in $(seq 50); do [ -S "$SOCK" ] && break; sleep 0.2; done
```

- **A short base under `/tmp`.** The socket path must fit `sun_path` (104 bytes on macOS); past that, ADR-0019 §6 falls back to `/tmp/droidyard-<uid>/`, which every instance shares, and isolation is lost silently. Under `/tmp` the socket path is about 47 bytes; under a resolved macOS `$TMPDIR` it is about 90, which leaves little margin. `cd -P` resolves `/tmp` to `/private/tmp` on macOS, so the paths the Doctor compares match.
- **One variable isolates the socket on Linux too**: ADR-0021 §Location makes an explicit `DROIDYARD_STATE_DIR` win over `$XDG_RUNTIME_DIR`. ADR-0019 §6 still states the Linux rule without that clause; the Doctor checks it either way.
- **`pre-health` must fail.** If it succeeds, the client ignored the scratch environment and reached another daemon. It proves nothing when no other daemon is running.
- **Foreground `serve`, backgrounded by you**, so `$PID` is yours. Auto-start is a Drive scenario, not the launch.
- **The daemon's environment is fixed at start** (ADR-0019 §8). A scenario that needs another environment (no `adb` on `PATH`) needs its own launch.

## Doctor

Read-only. It answers one question: is the instance you are about to drive the one you started, in the scratch state? If any check fails, stop. Do not drive it, and do not repair it by killing something.

```sh
kill -0 "$PID"
jq -e --arg p "$PID" --arg s "$SOCK" '(.pid|tostring)==$p and .socket==$s' "$DROIDYARD_STATE_DIR/daemon.lock"
lsof -a -p "$PID" -U -Fn | grep -qxF "n$SOCK"      # Linux without lsof: ss -xlp | grep -F "$SOCK"
run doctor-status "$DY" status && grep -qw "$PID" "$E/doctor-status.out"
run doctor-health "$DY" call --no-autostart system.health
```

In order: the process you started is alive (a failed bind shows in `serve.out`); the lock in the scratch state names it and the scratch socket (fields from ADR-0021 §Layout); that process is the one bound to the socket; the client side resolves the same state root (*assumed*: `status` prints the holder's pid); the socket answers through a shim. `system.health` alone cannot prove identity: its result is uptime, version and operation count (`internal/domain/system/system.go`), and any daemon of the same build reports the same version.

## Drive

Pass `--no-autostart` everywhere except the auto-start scenario: with it, a mis-isolated client fails; without it, the client may start or reach a daemon that is not yours (ADR-0019 §4).

**CLI:** `run <name> "$DY" call --no-autostart <capability> '<params json>'`. The argument form of `call` is *assumed*; no document fixes it.

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

`await` gives up after 30 s, above the adapter's default 20 s wait budget (`docs/interfaces/mcp-tool-surface.md`); a timeout is a failure, not a retry. The session binds `$R/proj` and opens it as a workspace (ADR-0019 §Sessions), so the workspace lands in the scratch state.

| Phase 0 acceptance (`docs/roadmap/mvp.md`) | Drive | Passes when |
| --- | --- | --- |
| `system.capabilities` lists what is available; `degraded` without `adb` | Launch again with a `PATH` on which `command -v adb` prints nothing; `call system.capabilities` | `adb`-dependent capabilities are `available: false` with a reason; the core reports `degraded` |
| The same capability from the CLI and MCP, identical semantics | `system.capabilities` through `call`, and through the session above | `jq -S .` of the CLI result equals `jq -S 'select(.id==1).result.structuredContent'` of `caps.out` |
| Killing a shim does not stop an operation it started (ADR-0020; ADR-0019 §Sessions) | Start the plan's long-running capability in an MCP session, read the `operationId`, `kill -9 "$SHIM"`, then `call operation.wait` | The operation ends `succeeded`, not `cancelled` |
| A dummy sidecar starts lazily, idles out, leaves no orphans after `workspace.close` | Call the capability that needs it; `snapshot`; wait past its idle timeout; `call workspace.close`; `count_orphans` | It appears in `processes.json` only after first use and leaves after idling; the count is 0 |
| Auto-start and single instance (ADR-0019 §4, §5; mvp.md Phase 0 deliverables) | Without `serve`, `call system.health` with no `--no-autostart`; then a second `serve` on the same state | `daemon.lock` appears in the scratch state; with `PID=$(jq -r .pid "$DROIDYARD_STATE_DIR/daemon.lock")` the Doctor passes, and that pid is now yours to stop; the second `serve` exits non-zero |
| Crash recovery (ADR-0021 §Pid records) | With a sidecar running, `snapshot`, `kill -9 "$PID"`, launch again, `count_orphans` | 0 once the new daemon has started |
| HTTP on loopback only | `lsof -a -p "$PID" -iTCP -sTCP:LISTEN` | Listens on `127.0.0.1` or `::1` only. The Host, Origin and token checks are in `docs/interfaces/http-websocket.md` §Security baseline |

Run only the rows the milestone's plan puts in scope; the plan's step names the row it runs.

## Evidence

Everything stays under `$E`. For each named step, `run` keeps the command line, stdout, stderr and exit code, and each MCP session keeps `<name>.sent`, `<name>.out`, `<name>.err` and `<name>.exit`. Keep `serve.out` (or `<state>/daemon.log` when auto-started), and the snapshots. The daemon's structured log carries one operation record per call (`docs/architecture/overview.md` §Observability). Which surface returns the full record is not decided yet; milestone 1b decides it. The report quotes the decisive line and gives `$R`. On a plan step it is level 5 evidence (`prove-change`).

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
echo "$R"
```

- **Never kill by name.** No `pkill`, no `killall`: the owner's daemon and shims are also called `droidyard`. Kill only a pid this run started (`$PID`, `$SHIM`) or read from this state's `daemon.lock`. `droidyard stop` under the scratch environment targets that lock's holder.
- **The orphan count is the acceptance evidence.** A survivor is a pid from the snapshot, taken just before the stop, that is still alive with the same start time. That means the daemon, its descendants and every pid recorded in `processes.json`; the start time guards against pid reuse. The Definition of done excludes the `adb` server, the Gradle daemon and emulators. Move those lines to `$E/final.excluded` by hand, each with its command line, and never widen that exclusion to hide a sidecar. Report other survivors as findings before you stop them by pid.
- **Delete nothing.** `$R` holds the evidence and the state it describes. The verifier cannot `rm`, and the recipe never needs to.

## Keeping it true

When a run needs a step the recipe lacks, or a step here proves wrong, change the recipe in the same change as the run. When a row becomes a Go test driving the processes (the durable form `prove-change` prefers), replace the row's Drive with the test's name.

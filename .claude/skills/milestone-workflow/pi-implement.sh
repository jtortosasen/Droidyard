#!/bin/bash
# pi-implement.sh <dir> <brief-file> <out-dir>
#
# Runs the DeepSeek implementer through pi in <dir> (the main tree for a sequential step,
# a worktree for a parallel one) and leaves in <out-dir>: events.jsonl (pi's JSON event
# stream), progress.log (one line per tool call, readable while it runs) and report.md
# (the implementer's final message). Exit 0 when the run ended, 1 when it never started.
#
# pi has no permissions of its own: the project policy of @gotgenes/pi-permission-system
# (.pi/) is its limit, so the run does not start unless that extension is installed in
# <dir>. A DeepSeek request sometimes never answers at the start: a run that emits no
# event in 90 s is killed and started again, once.
set -u
dir=$1 brief=$2 out=$3
mkdir -p "$out"
ext="$dir/.pi/npm/node_modules/@gotgenes/pi-permission-system"
[ -d "$ext" ] || (cd "$dir" && pi install -l -a npm:@gotgenes/pi-permission-system >/dev/null 2>&1)
[ -d "$ext" ] || { echo "pi-permission-system is not installed in $dir; not running" >&2; exit 1; }

for attempt in 1 2; do
	: >"$out/events.jsonl"
	(cd "$dir" && exec pi -p -a --no-session --mode json \
		--model opencode-go/deepseek-v4.1-flash \
		--append-system-prompt .pi/roles/implementer.md "$(cat "$brief")") >"$out/events.jsonl" 2>"$out/stderr.log" &
	pid=$!
	started=0
	for _ in $(seq 1 90); do
		grep -q '"type":"agent_start"' "$out/events.jsonl" 2>/dev/null && { started=1; break; }
		kill -0 "$pid" 2>/dev/null || break
		sleep 1
	done
	if [ "$started" = 0 ] && kill -0 "$pid" 2>/dev/null; then
		echo "attempt $attempt: no event in 90 s, relaunching" >&2
		kill "$pid"; sleep 1; kill -9 "$pid" 2>/dev/null
		continue
	fi
	# Follow the run: a line per tool call, so the orchestrating session can watch it.
	tail -n +1 -f "$out/events.jsonl" 2>/dev/null | python3 -u -c '
import json, sys
for l in sys.stdin:
    try: e = json.loads(l)
    except ValueError: continue
    if e.get("type") == "tool_execution_start":
        a = e.get("args") or {}
        what = a.get("command") or a.get("path") or a.get("file_path") or ""
        print(e.get("toolName"), str(what).replace("\n", " ")[:160], flush=True)
' >"$out/progress.log" &
	follower=$!
	wait "$pid"
	kill "$follower" 2>/dev/null
	python3 - "$out/events.jsonl" >"$out/report.md" <<'PY'
import json, sys
last = None
for l in open(sys.argv[1]):
    try: e = json.loads(l)
    except ValueError: continue
    m = e.get("message") or {}
    if e.get("type") == "message_end" and m.get("role") == "assistant":
        text = "".join(p.get("text", "") for p in m.get("content", []) if p.get("type") == "text")
        if text.strip(): last = text
print(last or "(no final message)")
PY
	exit 0
done
echo "two attempts emitted no event; not run" >&2
exit 1

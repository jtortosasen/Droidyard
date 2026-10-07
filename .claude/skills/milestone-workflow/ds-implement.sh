#!/bin/bash
# ds-implement.sh <dir> <brief-file> <out-dir>
#
# The DeepSeek implementer as a Claude Code run: `claude -p --agent implementer` in <dir>
# (the main tree for a sequential step, a worktree only for parallel ones), with every
# model request sent to OpenCode Go's Anthropic-format endpoint as deepseek-v4.1-flash,
# so it spends the OpenCode Go subscription, not Claude's. Limits: the implementer's own
# agent file plus the `implement` profile of .claude/hooks/guard-bash.sh (no staging,
# committing or discarding in any spelling, no adb/emulator, no rm -rf); no subagents.
# The key: $OPENCODE_GO_API_KEY if set, else ~/.config/droidyard-dev/opencode-go.key
# (mode 600, outside the repository); it is never written into the repository.
#
# Leaves in <out-dir>: events.jsonl (stream-json), progress.log (one line per tool call,
# readable while it runs) and report.md (the final message). Exit 0 when the run ended,
# 1 when it never started. A run with no event in 90 s is relaunched once.
set -u
dir=$1 brief=$2 out=$3
repo=$(cd "$(dirname "$0")/../../.." && pwd)
mkdir -p "$out"
keyfile=${XDG_CONFIG_HOME:-$HOME/.config}/droidyard-dev/opencode-go.key
key=${OPENCODE_GO_API_KEY:-$(cat "$keyfile" 2>/dev/null)}
[ -n "$key" ] || { echo "no OpenCode Go key: set OPENCODE_GO_API_KEY or write it to $keyfile (chmod 600)" >&2; exit 1; }
m=deepseek-v4.1-flash
settings=$(printf '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"sh %s/.claude/hooks/guard-bash.sh implement"}]}]}}' "$repo")

for attempt in 1 2; do
	: >"$out/events.jsonl"
	(cd "$dir" && exec env ANTHROPIC_BASE_URL=https://opencode.ai/zen/go ANTHROPIC_API_KEY="$key" ANTHROPIC_AUTH_TOKEN= \
		ANTHROPIC_CUSTOM_HEADERS="x-opencode-session: droidyard-$(date +%s)-$$" \
		ANTHROPIC_MODEL=$m ANTHROPIC_DEFAULT_OPUS_MODEL=$m ANTHROPIC_DEFAULT_SONNET_MODEL=$m \
		ANTHROPIC_DEFAULT_HAIKU_MODEL=$m CLAUDE_CODE_SUBAGENT_MODEL=$m CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
		claude -p --agent implementer --model $m --permission-mode bypassPermissions \
		--settings "$settings" --disallowedTools Agent \
		--append-system-prompt "$(cat "$repo/.claude/skills/milestone-workflow/implementer-run.md")" \
		--output-format stream-json --verbose "$(printf 'Your brief follows.\n\n'; cat "$brief")") >"$out/events.jsonl" 2>"$out/stderr.log" &
	pid=$!
	started=0
	for _ in $(seq 1 90); do
		grep -q '"type":"assistant"' "$out/events.jsonl" 2>/dev/null && { started=1; break; }
		kill -0 "$pid" 2>/dev/null || break
		sleep 1
	done
	if [ "$started" = 0 ] && kill -0 "$pid" 2>/dev/null; then
		echo "attempt $attempt: no model answer in 90 s, relaunching" >&2
		kill "$pid"; sleep 1; kill -9 "$pid" 2>/dev/null
		continue
	fi
	# The follower reads events.jsonl itself and ends when the run ends: a `tail -f` piped
	# into it outlived every run (killing the pipeline's last process left tail waiting).
	python3 -u - "$out/events.jsonl" "$pid" >"$out/progress.log" <<'FOLLOW' &
import json, os, sys, time
path, pid = sys.argv[1], int(sys.argv[2])
def alive():
    try: os.kill(pid, 0); return True
    except OSError: return False
with open(path) as f:
    while True:
        l = f.readline()
        if not l:
            if not alive():
                break
            time.sleep(0.5)
            continue
        try: e = json.loads(l)
        except ValueError: continue
        if e.get("type") != "assistant": continue
        for c in (e.get("message") or {}).get("content", []):
            if c.get("type") == "tool_use":
                i = c.get("input") or {}
                what = i.get("command") or i.get("file_path") or i.get("path") or i.get("pattern") or ""
                print(c.get("name"), str(what).replace("\n", " ")[:160], flush=True)
FOLLOW
	follower=$!
	wait "$pid"
	kill "$follower" 2>/dev/null
	python3 - "$out/events.jsonl" >"$out/report.md" <<'PY'
import json, sys
res = None
for l in open(sys.argv[1]):
    try: e = json.loads(l)
    except ValueError: continue
    if e.get("type") == "result": res = e
if res is None: print("(no final result)")
else:
    print(res.get("result") or "(empty result)")
    print("\n---\nmodels:", ", ".join((res.get("modelUsage") or {}).keys()), "| cost USD:", res.get("total_cost_usd"), "| turns:", res.get("num_turns"), "| error:", res.get("is_error"))
PY
	exit 0
done
echo "two attempts got no model answer; not run" >&2
exit 1

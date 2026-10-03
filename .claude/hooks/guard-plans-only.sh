#!/bin/sh
# PreToolUse hook for Edit and Write, scoped to the planner: the only file it may
# write is a plan under docs/plans/. Exit 2 blocks the call.
set -u
path=$(jq -r '.tool_input.file_path // empty')
case $path in
"$CLAUDE_PROJECT_DIR"/docs/plans/*.md | docs/plans/*.md) exit 0 ;;
esac
echo "Blocked for the planner: it writes only docs/plans/*.md, not $path" >&2
exit 2

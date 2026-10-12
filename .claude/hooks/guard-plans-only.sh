#!/bin/sh
# PreToolUse hook for Edit and Write, scoped to the planner: the only file it may
# write is a plan under docs/plans/, of the main checkout or of a milestone's worktree
# under .claude/worktrees/<name>/ (a session moved into a worktree keeps
# CLAUDE_PROJECT_DIR at the main checkout). Exit 2 blocks the call.
set -u
path=$(jq -r '.tool_input.file_path // empty')
block() {
	echo "Blocked for the planner: it writes only docs/plans/*.md, not $path" >&2
	exit 2
}
case $path in
*/../* | ../* | */.. | ..) block ;;
"$CLAUDE_PROJECT_DIR"/docs/plans/*.md) rest=${path#"$CLAUDE_PROJECT_DIR"/docs/plans/} ;;
docs/plans/*.md) rest=${path#docs/plans/} ;;
"$CLAUDE_PROJECT_DIR"/.claude/worktrees/*/docs/plans/*.md)
	wt=${path#"$CLAUDE_PROJECT_DIR"/.claude/worktrees/}
	rest=${wt#*/docs/plans/}
	[ "${wt%%/*}/docs/plans/$rest" = "$wt" ] || block
	;;
*) block ;;
esac
case $rest in
*/*) block ;;
esac
exit 0

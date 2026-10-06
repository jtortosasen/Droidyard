#!/bin/sh
# PreToolUse hook for Bash, scoped to one agent in its frontmatter. It replaces the
# per-agent shell permissions the agents had under OpenCode. Exit 2 blocks the call.
#
#   guard-bash.sh observe    allowlist: plan-reviewer, planner (observation only)
#   guard-bash.sh readonly   denylist: verifier (any command that does not change the tree)
#   guard-bash.sh implement  denylist: the DeepSeek implementer run by ds-implement.sh
#                            (edits and tests; never stages, commits or discards, never
#                            touches a device, the adb server or the emulator)
set -u
cmd=$(jq -r '.tool_input.command // empty')

block() {
	echo "Blocked for this agent ($1): $2" >&2
	exit 2
}

case ${1:-} in
implement)
	# Matched anywhere in the command, so `git -C <dir> commit`, `cd x && git commit` or
	# a full path to adb are caught as well as the plain form.
	if printf '%s\n' "$cmd" | grep -Eq '(^|[^[:alnum:]_.-])git([[:space:]]|$)' &&
		printf '%s\n' "$cmd" | grep -Eq '(^|[[:space:]])(add|commit|push|reset|checkout|restore|stash|clean|rebase|merge|switch|worktree|am|apply|cherry-pick|revert|update-ref|tag)([[:space:]]|$)'; then
		block implement "staging, committing and discarding are the orchestrating session's"
	fi
	if printf '%s\n' "$cmd" | grep -Eq '(^|[^[:alnum:]_.-])(adb|emulator)([[:space:]]|$)|kill-server|start-server|(^|[[:space:]])pm[[:space:]]+(uninstall|clear|disable)'; then
		block implement "a device, the adb server and the emulator are the orchestrating session's (level-5 runs)"
	fi
	if printf '%s\n' "$cmd" | grep -Eq '(^|[;&|[:space:]])rm[[:space:]]+-[a-zA-Z]*[rR][a-zA-Z]*f|(^|[;&|[:space:]])rm[[:space:]]+-[a-zA-Z]*f[a-zA-Z]*[rR]|(^|[[:space:]])sudo[[:space:]]'; then
		block implement "rm -rf and sudo are not allowed"
	fi
	;;
readonly)
	# Never regenerate the golden file: that destroys the evidence under review.
	case $cmd in *-update*) block readonly "-update regenerates schema/capabilities.json" ;; esac
	if printf '%s\n' "$cmd" | grep -Eq \
		'gofmt[^|;&]*-w|go mod tidy|go get |go generate|sed -i|git (add|commit|push|reset|checkout|restore|stash|clean)|(^|[;&|[:space:]])rm '; then
		block readonly "the verifier observes; it never changes the working tree"
	fi
	;;
observe)
	case $cmd in *-update*) block observe "-update regenerates schema/capabilities.json" ;; esac
	case $cmd in *'$('* | *'`'*) block observe "no command substitution" ;; esac
	rest=$(printf '%s' "$cmd" | sed -E 's#[0-9]?>&[0-9]##g; s#[0-9]?>[[:space:]]*/dev/null##g')
	case $rest in *'>'*) block observe "no redirection into files" ;; esac
	segs=$(printf '%s\n' "$rest" | sed -E 's/(&&|\|\||;|\|)/\
/g')
	while IFS= read -r seg; do
		seg=$(printf '%s' "$seg" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')
		case $seg in
		'' | 'go vet'* | 'go test'* | 'go list'* | 'go doc'* | 'gofmt -l'* | \
			'git log'* | 'git status'* | 'git diff'* | 'git show'* | \
			'cd '* | ls* | 'cat '* | head* | tail* | grep* | wc* | sort* | uniq* | 'cut '*) ;;
		*) block observe "'$seg' is not an observation command (go vet/test/list/doc, gofmt -l, git log/status/diff/show, read-only filters)" ;;
		esac
	done <<EOF
$segs
EOF
	;;
*)
	block "${1:-none}" "unknown guard profile"
	;;
esac
exit 0

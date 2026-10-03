#!/bin/sh
# PreToolUse hook for Bash, scoped to one agent in its frontmatter. It replaces the
# per-agent shell permissions the agents had under OpenCode. Exit 2 blocks the call.
#
#   guard-bash.sh observe   allowlist: plan-reviewer, planner (observation only)
#   guard-bash.sh readonly  denylist: verifier (any command that does not change the tree)
set -u
cmd=$(jq -r '.tool_input.command // empty')

block() {
	echo "Blocked for this agent ($1): $2" >&2
	exit 2
}

# Never regenerate the golden file: that destroys the evidence under review.
case $cmd in *-update*) block "$1" "-update regenerates schema/capabilities.json" ;; esac

case ${1:-} in
readonly)
	if printf '%s\n' "$cmd" | grep -Eq \
		'gofmt[^|;&]*-w|go mod tidy|go get |go generate|sed -i|git (add|commit|push|reset|checkout|restore|stash|clean)|(^|[;&|[:space:]])rm '; then
		block readonly "the verifier observes; it never changes the working tree"
	fi
	;;
observe)
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

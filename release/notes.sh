#!/bin/sh
# release/notes.sh <tag>
#
# Prints the release notes for <tag>: what reached main since the previous release tag,
# one line per first-parent commit (a merged branch reads as "<branch>: <what>"), a link
# to the full diff and the install line. The first release says so instead of listing
# its history. Run by the release workflow after the build; runnable on a laptop, from
# any clone with the tags, to write the notes of a past release.
set -eu

die() { echo "release/notes.sh: $*" >&2; exit 1; }

[ $# -eq 1 ] || die "usage: release/notes.sh <tag>"
T=$1
R=${GITHUB_REPOSITORY:-jtortosasen/Droidyard}

git rev-parse -q --verify "refs/tags/$T" >/dev/null || die "no tag $T"
prev=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' "$T^" 2>/dev/null) || prev=

if [ -n "$prev" ]; then
	echo "Changes since $prev:"
	echo
	git log --first-parent --format='%s' "$prev..$T" | sed -e 's/^Merge //' -e 's/^/- /'
	echo
	echo "Full diff: https://github.com/$R/compare/$prev...$T"
else
	echo "The first release."
fi
echo
echo "Install or update:"
echo
echo "    curl -fsSL https://raw.githubusercontent.com/$R/main/install.sh | sh"

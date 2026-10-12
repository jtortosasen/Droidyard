#!/bin/sh
# install.sh: installs Droidyard, updates it, or says there is nothing to do.
#
# Re-running is the update: it reads the installed binary's version, names what it
# replaces, and does nothing when the release is already there. Environment:
#   DROIDYARD_VERSION      a release tag (vX.Y.Z) instead of the latest
#   DROIDYARD_INSTALL_DIR  where droidyard goes (default: ~/.local/bin)
# It uses gh when that is installed and authenticated, else curl, from the same release
# URLs whether the repository is private or public. It checks the SHA-256 from the
# release's checksums.txt before anything is installed, edits no shell profile, and
# stops no daemon. Why this shape: docs/adr/0068-release-builds-and-installer.md.
#
# Everything runs inside main, called on the last line: a download cut short runs nothing.
set -eu

die() {
	printf 'droidyard: %s\n' "$*" >&2
	exit 1
}

# cleanup removes the scratch directory and a half-written copy beside the target.
cleanup() {
	if [ -n "${tmp:-}" ]; then
		rm -rf "$tmp"
	fi
	if [ -n "${new:-}" ]; then
		rm -f "$new"
	fi
}

# is_tag succeeds for exactly vMAJOR.MINOR.PATCH (digits only, no newline).
is_tag() {
	case $1 in
	v*) ;;
	*) return 1 ;;
	esac
	rest=${1#v}
	case $rest in
	'' | *[!0-9.]* | .* | *. | *..*) return 1 ;;
	esac
	case $rest in
	*.*.*.*) return 1 ;;
	*.*.*) return 0 ;;
	esac
	return 1
}

# sum_of prints the SHA-256 of a file; sha256sum when present, else shasum.
sum_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# path_advice prints the line to add to a profile when $1 is not on PATH. It writes nothing.
path_advice() {
	case ":${PATH:-}:" in
	*":$1:"*) ;;
	*)
		case ${SHELL:-} in
		*/zsh) profile='~/.zshrc' ;;
		*/bash)
			if [ "$2" = darwin ]; then profile='~/.bash_profile'; else profile='~/.bashrc'; fi
			;;
		*) profile='~/.profile' ;;
		esac
		echo "droidyard: $1 is not on your PATH. Add this line to $profile:"
		echo
		echo "  export PATH=\"$1:\$PATH\""
		echo
		;;
	esac
}

main() {
	R=jtortosasen/Droidyard
	# gh takes [HOST/]OWNER/REPO; naming the host keeps GH_HOST from redirecting it.
	GHREPO=github.com/$R
	tmp=
	new=
	trap cleanup EXIT
	trap 'exit 1' INT TERM

	# 1. Pair: refused before any download.
	sysname=$(uname -s)
	machine=$(uname -m)
	case $sysname in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) os= ;;
	esac
	case $machine in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) arch= ;;
	esac
	if [ -z "$os" ] || [ -z "$arch" ]; then
		die "unsupported platform $sysname/$machine; supported: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64"
	fi

	# 2. Transport.
	curlmsg="curl could not fetch the release from https://github.com/$R; a private repository needs gh installed and authenticated (gh auth login)"
	if command -v gh >/dev/null 2>&1 && gh auth status --hostname github.com --active >/dev/null 2>&1; then
		via=gh
	elif command -v curl >/dev/null 2>&1; then
		via=curl
	else
		die "needs gh or curl on PATH (gh must be authenticated: gh auth login)"
	fi

	# 3. Target.
	tag=${DROIDYARD_VERSION:-}
	if [ -z "$tag" ]; then
		if [ "$via" = gh ]; then
			tag=$(gh release view --repo "$GHREPO" --json tagName -q .tagName) ||
				die "gh could not read the latest release of $R"
		else
			url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$R/releases/latest") ||
				die "$curlmsg"
			url=${url%/}
			tag=${url##*/}
		fi
	fi
	is_tag "$tag" || die "refusing '$tag': a release tag is vMAJOR.MINOR.PATCH (a repository with no release has none to install)"

	# 4. Current.
	dir=${DROIDYARD_INSTALL_DIR:-}
	if [ -z "$dir" ]; then
		[ -n "${HOME:-}" ] || die "HOME is not set; set DROIDYARD_INSTALL_DIR"
		dir=$HOME/.local/bin
	fi
	# One trailing slash (or several) would make the PATH match below miss.
	while [ "${dir%/}" != "$dir" ] && [ -n "${dir%/}" ]; do
		dir=${dir%/}
	done
	have=0
	current=
	if [ -f "$dir/droidyard" ] && [ -x "$dir/droidyard" ]; then
		have=1
		out=$("$dir/droidyard" version 2>/dev/null) || out=
		cand=$(printf '%s\n' "$out" | awk 'NR == 1 { print $2 }')
		if is_tag "$cand"; then
			current=$cand
		fi
	fi

	# 5. Idempotence.
	if [ "$current" = "$tag" ]; then
		echo "droidyard: already at $tag"
		path_advice "$dir" "$os"
		return 0
	fi
	if [ "$have" = 0 ]; then
		echo "droidyard: installing $tag"
	elif [ -n "$current" ]; then
		echo "droidyard: installed $current, installing $tag"
	else
		echo "droidyard: installed an unknown version, installing $tag"
	fi

	# 6. Download.
	if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
		die "needs sha256sum or shasum to verify the download"
	fi
	archive=droidyard_${tag}_${os}_${arch}.tar.gz
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/droidyard-install.XXXXXX")
	if [ "$via" = gh ]; then
		gh release download "$tag" --repo "$GHREPO" --pattern "$archive" --pattern checksums.txt --dir "$tmp" ||
			die "gh could not download release $tag of $R"
	else
		base=https://github.com/$R/releases/download/$tag
		curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "$curlmsg"
		curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "$curlmsg"
	fi
	[ -f "$tmp/$archive" ] || die "$archive was not downloaded"
	[ -f "$tmp/checksums.txt" ] || die "checksums.txt was not downloaded"

	# 7. Verification: the sum first, then the one member, then its version line.
	expected=$(awk -v n="$archive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/checksums.txt")
	if [ -z "$expected" ]; then
		die "checksums.txt has no line for $archive"
	fi
	case $expected in
	*[!0-9a-fA-F]*) die "checksums.txt has more than one line for $archive, or a malformed sum" ;;
	esac
	if [ "${#expected}" -ne 64 ]; then
		die "checksums.txt has a malformed sum for $archive: $expected"
	fi
	got=$(sum_of "$tmp/$archive")
	if [ "$got" != "$expected" ]; then
		die "checksum mismatch for $archive: checksums.txt says $expected, the download is $got"
	fi
	tar -xzf "$tmp/$archive" -C "$tmp" droidyard || die "$archive does not hold a droidyard binary"
	if [ ! -f "$tmp/droidyard" ] || [ -L "$tmp/droidyard" ]; then
		die "$archive does not hold a droidyard binary"
	fi
	chmod 755 "$tmp/droidyard"
	out=$("$tmp/droidyard" version 2>/dev/null) || out=
	case $out in
	"droidyard $tag (API "*")") ;;
	*) die "the binary in $archive reports '$out', not 'droidyard $tag (API ...)'; not installing it" ;;
	esac

	# 8. Daemon, before: the old binary most likely started the daemon that answers.
	daemon=0
	if [ "$have" = 1 ] && "$dir/droidyard" status >/dev/null 2>&1; then
		daemon=1
	fi

	# 9. Install: write beside the target, then rename. A running daemon keeps its inode.
	mkdir -p "$dir"
	new=$(mktemp "$dir/.droidyard.new.XXXXXX")
	cp "$tmp/droidyard" "$new"
	chmod 755 "$new"
	mv -f "$new" "$dir/droidyard"
	new=
	echo "droidyard: now at $tag ($dir/droidyard)"

	# 10. After.
	path_advice "$dir" "$os"
	if [ "$daemon" = 1 ] || "$dir/droidyard" status >/dev/null 2>&1; then
		if [ "$have" = 1 ]; then
			echo "droidyard: a daemon is running and still runs the previous binary; run: droidyard stop"
		else
			echo "droidyard: a droidyard daemon is running; run: droidyard stop, so the next start uses $dir/droidyard"
		fi
	fi
}

main "$@" </dev/null

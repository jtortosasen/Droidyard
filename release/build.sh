#!/bin/sh
# release/build.sh <version> <outdir>
#
# Builds a Droidyard release: the web UI, then the daemon for darwin/arm64,
# darwin/amd64, linux/amd64 and linux/arm64 (CGO_ENABLED=0, cross-compiled from any
# host), one droidyard_<version>_<os>_<arch>.tar.gz per target and a checksums.txt of
# SHA-256 sums, all in <outdir>. It refuses a binary that lacks the built UI or, on the
# host's own platform, does not report the stamped version. Run by CI on every pull
# request (a dry run) and by the release workflow; runnable on a laptop.
# Why this shape: docs/adr/0068-release-builds-and-installer.md.
set -eu

die() { echo "release/build.sh: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: release/build.sh <version> <outdir>"
V=$1
OUT=$2

printf '%s\n' "$V" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || die "version '$V' is not vMAJOR.MINOR.PATCH"

if [ -e "$OUT" ]; then
	[ -d "$OUT" ] || die "$OUT exists and is not a directory"
	[ -z "$(ls -A "$OUT")" ] || die "$OUT exists and is not empty"
fi
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

# A stale UI must not satisfy the check below.
rm -rf web/dist/app
pnpm -C web build
[ -f web/dist/app/index.html ] || die "web/dist/app/index.html is absent after pnpm -C web build"
ASSET=$(ls web/dist/app/assets | head -n 1)
[ -n "$ASSET" ] || die "web/dist/app/assets is empty"

STAGE=$(mktemp -d "${TMPDIR:-/tmp}/droidyard-release.XXXXXX")
trap 'rm -rf "$STAGE"' EXIT INT TERM

HOST_OS=$(go env GOOS)
HOST_ARCH=$(go env GOARCH)

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
	os=${target%/*}
	arch=${target#*/}
	dir=$STAGE/${os}_${arch}
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
		-ldflags "-s -w -buildid= -X main.build=$V" -o "$dir/droidyard" ./cmd/droidyard

	[ "$(grep -c -a -F "$ASSET" "$dir/droidyard")" -gt 0 ] ||
		die "$target binary does not contain the built UI ($ASSET)"

	if [ "$os" = "$HOST_OS" ] && [ "$arch" = "$HOST_ARCH" ]; then
		got=$("$dir/droidyard" version)
		case $got in
		"droidyard $V (API "*")") ;;
		*) die "$target binary printed '$got', not 'droidyard $V (API ...)'" ;;
		esac
	fi

	# COPYFILE_DISABLE keeps macOS tar from adding ._ entries.
	COPYFILE_DISABLE=1 tar -czf "$OUT/droidyard_${V}_${os}_${arch}.tar.gz" -C "$dir" droidyard
done

cd "$OUT"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum droidyard_*.tar.gz >checksums.txt
else
	shasum -a 256 droidyard_*.tar.gz >checksums.txt
fi
echo "release/build.sh: $V built in $OUT"

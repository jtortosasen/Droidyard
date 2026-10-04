#!/bin/sh
# Rebuilds reader.dex from src/DroidyardReader.java (ADR-0029). Needs a JDK (javac) and
# the Android SDK: android.jar of one platform and d8 of one build-tools release. The
# versions are pinned below because the dex bytes depend on them; the SHA-256 in
# reader.go is the one this script gives with exactly these. Override with
# ANDROID_HOME, PLATFORM, BUILD_TOOLS; the pinned digest reproduces with javac 17.0.11
# (--release 11), checked below; OUT names the file to write (default: reader.dex
# beside this script). `go test ./internal/domain/device/reader` runs it where the SDK exists.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}
platform=${PLATFORM:-android-36}
tools=${BUILD_TOOLS:-37.0.0}
out=${OUT:-$here/reader.dex}
# FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES and UiAutomation.connect(int) arrive in API 24.
min_api=24
case $(javac -version 2>&1) in
"javac 17."*) ;;
*) echo "build.sh: warning: javac is not 17.x ($(javac -version 2>&1)); the pinned SHA-256 was made with 17.0.11 and may not reproduce" >&2 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
javac --release 11 -cp "$sdk/platforms/$platform/android.jar" -d "$work/classes" "$here/src/DroidyardReader.java"
mkdir "$work/out"
cd "$work/classes"
"$sdk/build-tools/$tools/d8" --release --min-api "$min_api" --lib "$sdk/platforms/$platform/android.jar" --output "$work/out" DroidyardReader.class
cp "$work/out/classes.dex" "$out"

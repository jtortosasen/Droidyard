#!/bin/sh
# Builds the 1f fixture app's APK from source (D14) with the SDK's own tools, no
# Gradle. Needs a JDK (javac, keytool) and the Android SDK: android.jar of one
# platform and aapt2, d8, zipalign, apksigner of one build-tools release. The
# versions are pinned below, as reader/build.sh pins its own. Override with
# ANDROID_HOME, PLATFORM, BUILD_TOOLS. OUT names the file to write (default:
# formapp.apk beside this script); the APK is a build output and is never committed.
# `go test ./internal/domain/device/formapp` runs this script where the SDK exists.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}
platform=${PLATFORM:-android-36}
tools=${BUILD_TOOLS:-37.0.0}
out=${OUT:-$here/formapp.apk}
# minSdk 24 and targetSdk 36, as AndroidManifest.xml declares; the activity uses no
# API newer than 24.
min_api=24
target_api=36
androidjar="$sdk/platforms/$platform/android.jar"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$sdk/build-tools/$tools/aapt2" compile --dir "$here/res" -o "$work/res.zip"
"$sdk/build-tools/$tools/aapt2" link \
    -o "$work/unsigned.apk" \
    -I "$androidjar" \
    --manifest "$here/AndroidManifest.xml" \
    --java "$work/gen" \
    --min-sdk-version "$min_api" \
    --target-sdk-version "$target_api" \
    "$work/res.zip"

javac --release 11 -cp "$androidjar" -d "$work/classes" \
    "$here/src/dev/droidyard/formapp/FormActivity.java" \
    "$here/src/dev/droidyard/formapp/ProbeActivity.java" \
    "$work/gen/dev/droidyard/formapp/R.java"

mkdir "$work/dex"
# The glob covers the activities' anonymous inner classes and R/R$id as well.
"$sdk/build-tools/$tools/d8" --release --min-api "$min_api" --lib "$androidjar" \
    --output "$work/dex" "$work/classes/dev/droidyard/formapp/"*.class

# The APK aapt2 linked carries resources and the manifest; the dex goes in at its root.
cp "$work/dex/classes.dex" "$work/classes.dex"
(cd "$work" && zip -q "$work/unsigned.apk" classes.dex)

"$sdk/build-tools/$tools/zipalign" -f -p 4 "$work/unsigned.apk" "$work/aligned.apk"

# A throwaway signing key, made and discarded in the temporary directory: the fixture
# is installed with `adb install -r`, which wants a signature, not any particular one.
keytool -genkeypair -keystore "$work/debug.keystore" -storepass android -keypass android \
    -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 \
    -dname "CN=Android Debug,O=Android,C=US" >/dev/null 2>&1
"$sdk/build-tools/$tools/apksigner" sign \
    --ks "$work/debug.keystore" --ks-pass pass:android --key-pass pass:android \
    --ks-key-alias androiddebugkey "$work/aligned.apk"

mkdir -p "$(dirname "$out")"
cp "$work/aligned.apk" "$out"

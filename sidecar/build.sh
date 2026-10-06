#!/bin/sh
# Builds the Gradle TAPI sidecar's two jars from src/*.kt (ADR-0017, plan m1g D4).
#
#   gradle-sidecar-deps.jar  the Gradle Tooling API, slf4j-api and the Kotlin stdlib
#   gradle-sidecar.jar       our classes
#
# Entries are sorted and timestamps fixed at 1980-01-01, so the same inputs give the
# same bytes; the SHA-256 of each is pinned in sidecar/sidecar.go and checked by tests. The
# Go build needs no JVM: the jars are checked in and embedded.
#
# Needs a JDK (jar) and kotlinc. Overrides: KOTLINC, JAVA_HOME, BUILD_CACHE (where the
# downloaded jars are kept), OUTDIR (where the jars are written; default: here).
# The versions below are pinned; a Tooling API bump means new URLs, new digests and a
# rebuild (D4).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
outdir=${OUTDIR:-$here}
cache=${BUILD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/droidyard-sidecar-build}

TAPI_VERSION=9.8.0
TAPI_URL="https://repo.gradle.org/gradle/libs-releases/org/gradle/gradle-tooling-api/$TAPI_VERSION/gradle-tooling-api-$TAPI_VERSION.jar"
TAPI_SHA=f95b1aad39832a29842020e8f9d039b1dcf466cc15fcd45c6498c18f83cfbed8
SLF4J_VERSION=2.0.17
SLF4J_URL="https://repo1.maven.org/maven2/org/slf4j/slf4j-api/$SLF4J_VERSION/slf4j-api-$SLF4J_VERSION.jar"
SLF4J_SHA=7b751d952061954d5abfed7181c1f645d336091b679891591d63329c622eb832

# --- tools ---------------------------------------------------------------------------

kotlinc=${KOTLINC:-}
if [ -z "$kotlinc" ]; then
    for candidate in \
        "/Applications/Android Studio.app/Contents/plugins/Kotlin/kotlinc/bin/kotlinc" \
        "$HOME/Applications/Android Studio.app/Contents/plugins/Kotlin/kotlinc/bin/kotlinc" \
        "$HOME/Library/Application Support/JetBrains/Toolbox/apps/AndroidStudio/ch-0/plugins/Kotlin/kotlinc/bin/kotlinc"; do
        if [ -x "$candidate" ]; then kotlinc=$candidate; break; fi
    done
fi
if [ -z "$kotlinc" ]; then
    kotlinc=$(command -v kotlinc 2>/dev/null || true)
fi
if [ -z "$kotlinc" ] || [ ! -x "$kotlinc" ]; then
    echo "build.sh: no kotlinc (set KOTLINC or install Android Studio)" >&2
    exit 2
fi
kotlinc_lib=$(cd "$(dirname "$kotlinc")/../lib" && pwd)
kotlin_stdlib="$kotlinc_lib/kotlin-stdlib.jar"
if [ ! -f "$kotlin_stdlib" ]; then
    echo "build.sh: kotlin-stdlib.jar not beside $kotlinc" >&2
    exit 2
fi

jarbin="jar"
if [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/jar" ]; then jarbin="$JAVA_HOME/bin/jar"; fi

sha256_of() {
    if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
    else sha256sum "$1" | cut -d' ' -f1
    fi
}

kotlinc_version=$("$kotlinc" -version 2>&1 | sed -n 's/.*kotlinc-jvm \([0-9][^ ]*\).*/\1/p' | head -1)
if [ -z "$kotlinc_version" ]; then kotlinc_version="unknown"; fi

# --- fetch the pinned jars -----------------------------------------------------------

fetch() { # url sha dest
    if [ -f "$3" ] && [ "$(sha256_of "$3")" = "$2" ]; then return 0; fi
    mkdir -p "$(dirname "$3")"
    tmp="$3.tmp.$$"
    echo "build.sh: fetching $1" >&2
    curl -fsSL --max-time 300 -o "$tmp" "$1"
    got=$(sha256_of "$tmp")
    if [ "$got" != "$2" ]; then
        rm -f "$tmp"
        echo "build.sh: $1 has SHA-256 $got, expected $2" >&2
        exit 2
    fi
    mv "$tmp" "$3"
}

fetch "$TAPI_URL" "$TAPI_SHA" "$cache/gradle-tooling-api-$TAPI_VERSION.jar"
fetch "$SLF4J_URL" "$SLF4J_SHA" "$cache/slf4j-api-$SLF4J_VERSION.jar"

# --- compile -------------------------------------------------------------------------

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/classes"
# shellcheck disable=SC2046
"$kotlinc" -classpath "$cache/gradle-tooling-api-$TAPI_VERSION.jar" -jvm-target 17 \
    -d "$work/classes" $(find "$here/src" -name '*.kt' | sort)
if [ ! -d "$work/classes/droidyard" ]; then
    echo "build.sh: kotlinc produced no classes" >&2
    exit 2
fi

# --- assemble the deps jar ------------------------------------------------------------

mkdir -p "$work/deps"
unzip -q -o "$cache/gradle-tooling-api-$TAPI_VERSION.jar" -d "$work/deps"
unzip -q -o "$cache/slf4j-api-$SLF4J_VERSION.jar" -d "$work/deps"
unzip -q -o "$kotlin_stdlib" -d "$work/deps"
# A manifest and signatures are not wanted in a classpath jar; the manifest would also
# carry a timestamp.
rm -f "$work/deps/META-INF/MANIFEST.MF"
find "$work/deps/META-INF" -name '*.SF' -o -name '*.RSA' -o -name '*.DSA' 2>/dev/null | while read -r f; do rm -f "$f"; done

# --- assemble the classes jar ---------------------------------------------------------

mkdir -p "$outdir"
jar_create() { # staging-dir outfile [manifest]
    staging=$1
    out=$2
    manifest=${3:-}
    args="$work/entries.txt"
    (cd "$staging" && find . -type f | sed 's|^\./||' | LC_ALL=C sort > "$args")
    rm -f "$out"
    # Run from the staging directory with relative paths: an argfile's -C applies to
    # only one entry, so a full path list under one -C is not usable.
    if [ -n "$manifest" ]; then
        (cd "$staging" && "$jarbin" --create --file "$out" --manifest "$manifest" \
            --date=2000-01-01T00:00:00Z @"$args")
    else
        (cd "$staging" && "$jarbin" --create --file "$out" --no-manifest \
            --date=2000-01-01T00:00:00Z @"$args")
    fi
}

# The merged deps jar drops each input's manifest, so `hello` would report the Tooling
# API version as unknown: give it one carrying the pinned TAPI version.
cat > "$work/manifest.mf" <<EOF
Manifest-Version: 1.0
Implementation-Title: Gradle
Implementation-Version: $TAPI_VERSION
Implementation-Vendor: Gradle, Inc.
EOF

jar_create "$work/deps" "$outdir/gradle-sidecar-deps.jar" "$work/manifest.mf"
jar_create "$work/classes" "$outdir/gradle-sidecar.jar"

# --- report --------------------------------------------------------------------------

echo "kotlinc=$kotlinc_version"
echo "toolingApi=$TAPI_VERSION"
echo "slf4j=$SLF4J_VERSION"
echo "sha256 gradle-sidecar-deps.jar $(sha256_of "$outdir/gradle-sidecar-deps.jar")"
echo "sha256 gradle-sidecar.jar $(sha256_of "$outdir/gradle-sidecar.jar")"

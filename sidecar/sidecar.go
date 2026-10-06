// Package sidecar holds the Gradle Tooling API sidecar of ADR-0017, packaged per the
// m1g plan's D4: a Kotlin program (src/*.kt, built by build.sh) that speaks the line-JSON
// protocol of docs/subsystems/gradle.md §Protocol to the daemon over stdio. Two jars are
// built, checked in under jars/ and embedded here; SHA-256 pins them (the ADR-0029
// pattern), and the daemon's client writes them into the cache directory (D5) and checks
// each digest before every launch. Building the daemon needs no JVM.
package sidecar

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Pinned build inputs and outputs. A change to src/*.kt, or a version bump, changes both
// jars and every digest below, deliberately; build.sh prints the new ones (D4).
const (
	// Kotlinc is the compiler that produced these jars, recorded as D4 asks. The
	// reproducibility test skips, naming both versions, when the host differs.
	Kotlinc = "2.3.10"
	// ToolingAPI is the Gradle Tooling API version the deps jar carries; `hello`
	// reports it.
	ToolingAPI = "9.8.0"
	// Slf4j is the slf4j-api version merged into the deps jar.
	Slf4j = "2.0.17"

	// DepsSHA256 is the hex digest of jars/gradle-sidecar-deps.jar (Tooling API,
	// slf4j-api, Kotlin stdlib).
	DepsSHA256 = "1b54ae742a1e00e1cb446f8f3252801251f1ef954249b012280bbd5bdcbe394f"
	// SHA256 is the hex digest of jars/gradle-sidecar.jar, the sidecar's own classes.
	SHA256 = "9227179f020de6283885d7f5da37f8f2ec64830853ff855c76f23db4186d30b7"

	// Class is the sidecar's main class, as `java` names it.
	Class = "droidyard.sidecar.MainKt"
)

//go:embed jars/gradle-sidecar-deps.jar
var depsJar []byte

//go:embed jars/gradle-sidecar.jar
var classJar []byte

// DepsJar returns the embedded dependency jar. The slice is a copy.
func DepsJar() []byte { return append([]byte(nil), depsJar...) }

// Jar returns the embedded jar of the sidecar's own classes. The slice is a copy.
func Jar() []byte { return append([]byte(nil), classJar...) }

// DepsDigest is the SHA-256 of the embedded deps jar, computed, not the constant.
func DepsDigest() string { return digestOf(depsJar) }

// Digest is the SHA-256 of the embedded classes jar, computed, not the constant.
func Digest() string { return digestOf(classJar) }

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

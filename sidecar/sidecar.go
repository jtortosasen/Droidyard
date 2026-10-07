// Package sidecar holds the Gradle Tooling API sidecar of ADR-0017 and its probe
// (ADR-0023), packaged per the m1g plan's D4 and the m1h plan's D4: a Kotlin program
// (src/*.kt) that speaks the line-JSON protocol of docs/subsystems/gradle.md §Protocol to
// the daemon over stdio, plus a Java 8 jar (probe/src/, built with javac) that registers
// the ToolingModelBuilder behind droidyard-probe.init.gradle. Three jars are built,
// checked in under jars/ and embedded here; SHA-256 pins them (the ADR-0029 pattern), and
// the daemon's client writes them into the cache directory (D5) and checks each digest
// before every launch. Building the daemon needs no JVM.
package sidecar

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Pinned build inputs and outputs. A change to src/*.kt, probe/src/*.java, the init
// script, or a version bump, changes the jars and every digest below, deliberately;
// build.sh prints the new ones (m1g D4, m1h D4).
const (
	// Kotlinc is the compiler that produced the Kotlin jars, recorded as D4 asks. The
	// reproducibility test skips, naming both versions, when the host differs.
	Kotlinc = "2.3.10"
	// Javac is the compiler that produced the probe jar, recorded as m1h D4 asks. The
	// same test skips, naming both versions, when the host differs.
	Javac = "17.0.11"
	// Gradle is the distribution whose public API the probe is compiled against: the
	// floor of ADR-0018, so the probe cannot call what an 8.0 daemon lacks (m1h D4).
	Gradle = "8.0.2"
	// ToolingAPI is the Gradle Tooling API version the deps jar carries; `hello`
	// reports it.
	ToolingAPI = "9.8.0"
	// Slf4j is the slf4j-api version merged into the deps jar.
	Slf4j = "2.0.17"

	// DepsSHA256 is the hex digest of jars/gradle-sidecar-deps.jar (Tooling API,
	// slf4j-api, Kotlin stdlib).
	DepsSHA256 = "1b54ae742a1e00e1cb446f8f3252801251f1ef954249b012280bbd5bdcbe394f"
	// SHA256 is the hex digest of jars/gradle-sidecar.jar, the sidecar's own classes.
	SHA256 = "48317595e2c77e8eef93b327fa9fb8ea9a8a1219deb462b285a4c59f1355cb7f"
	// ProbeSHA256 is the hex digest of jars/droidyard-probe.jar, the probe's classes.
	ProbeSHA256 = "6a54f8573130e1e8d1302735daa602052b136a6550e76ed97b12bed51b5bbbb6"
	// InitSHA256 is the hex digest of the bytes of probe/droidyard-probe.init.gradle,
	// the design's only init script. It is fixed text, not a build output, but it is
	// pinned like one: the daemon writes it into the cache and a drift here would change
	// what runs inside a user's Gradle daemon.
	InitSHA256 = "cc11740ce61eace71e4b9f588ae2e9a16fbf363647565d6083c0a3dcd4775373"

	// Class is the sidecar's main class, as `java` names it.
	Class = "droidyard.sidecar.MainKt"
)

//go:embed jars/gradle-sidecar-deps.jar
var depsJar []byte

//go:embed jars/gradle-sidecar.jar
var classJar []byte

//go:embed jars/droidyard-probe.jar
var probeJar []byte

//go:embed probe/droidyard-probe.init.gradle
var initScript []byte

// DepsJar returns the embedded dependency jar. The slice is a copy.
func DepsJar() []byte { return append([]byte(nil), depsJar...) }

// Jar returns the embedded jar of the sidecar's own classes. The slice is a copy.
func Jar() []byte { return append([]byte(nil), classJar...) }

// ProbeJar returns the embedded probe jar. The slice is a copy.
func ProbeJar() []byte { return append([]byte(nil), probeJar...) }

// InitScript returns the embedded text of the probe's init script. The slice is a copy.
func InitScript() []byte { return append([]byte(nil), initScript...) }

// DepsDigest is the SHA-256 of the embedded deps jar, computed, not the constant.
func DepsDigest() string { return digestOf(depsJar) }

// Digest is the SHA-256 of the embedded classes jar, computed, not the constant.
func Digest() string { return digestOf(classJar) }

// ProbeDigest is the SHA-256 of the embedded probe jar, computed, not the constant.
func ProbeDigest() string { return digestOf(probeJar) }

// InitDigest is the SHA-256 of the embedded init script, computed, not the constant.
func InitDigest() string { return digestOf(initScript) }

func digestOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

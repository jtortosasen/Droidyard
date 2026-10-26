// Package reader holds the UI reader dex of ADR-0029 and ADR-0030: a program run on the
// device with app_process that writes the active window's tree in the XML shape of
// `uiautomator dump`, with UiAutomation connected so that other accessibility services
// keep running. With no arguments it reads once and exits (ADR-0029), and `dump full
// [invisible]` reads once the full document of ADR-0110 (the same words are a request of the
// serve mode); `serve <socket>
// <idleSeconds>` keeps it running and answers `ping`, `dump`, `quit`, and the acting
// requests `tap`, `keys`, `settext`, `gesture` and `press` over a local socket (ADR-0030; ADR-0035 fences the
// acting ones by the peer's uid, ADR-0102 `dump`, `ping`, `quit` and `events` too). It also
// keeps the screen's accessibility events in a ring and answers `events <afterSeq> <max>`
// (ADR-0111). The dex is built from src/DroidyardReader.java by build.sh, checked in and
// embedded; SHA256 pins it. Building the daemon needs no Android SDK.
package reader

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Class is the reader's main class, as app_process names it.
const Class = "DroidyardReader"

// SHA256 is the hex digest of reader.dex, as build.sh produces it (build-tools 37.0.0,
// android-36, javac 17.0.11 --release 11, d8 --min-api 24). A test fails when they differ; a
// change to the source changes both the dex and this line, deliberately.
const SHA256 = "99d39598692e90a94c608d463df1aa873c85addd84574041e48a2dfd05f37386"

//go:embed reader.dex
var dex []byte

// Dex returns the embedded reader. The slice is a copy.
func Dex() []byte { return append([]byte(nil), dex...) }

// Digest is the SHA-256 of the embedded dex, computed, not the constant.
func Digest() string {
	h := sha256.Sum256(dex)
	return hex.EncodeToString(h[:])
}

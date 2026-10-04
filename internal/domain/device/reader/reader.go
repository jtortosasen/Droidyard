// Package reader holds the UI reader dex of ADR-0029: a one-shot program run on the
// device with app_process that writes the active window's tree in the XML shape of
// `uiautomator dump`, with UiAutomation connected so that other accessibility services
// keep running. The dex is built from src/DroidyardReader.java by build.sh, checked in
// and embedded; SHA256 pins it. Building the daemon needs no Android SDK.
package reader

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Class is the reader's main class, as app_process names it.
const Class = "DroidyardReader"

// SHA256 is the hex digest of reader.dex, as build.sh produces it (build-tools 37.0.0,
// android-36, javac --release 11, d8 --min-api 24). A test fails when they differ; a
// change to the source changes both the dex and this line, deliberately.
const SHA256 = "b76c144f2e9378d73197c46bba2031a5cb3609817f2c437aa36379a980b3e9d8"

//go:embed reader.dex
var dex []byte

// Dex returns the embedded reader. The slice is a copy.
func Dex() []byte { return append([]byte(nil), dex...) }

// Digest is the SHA-256 of the embedded dex, computed, not the constant.
func Digest() string {
	h := sha256.Sum256(dex)
	return hex.EncodeToString(h[:])
}

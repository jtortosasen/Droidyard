// Package scrcpyserver holds scrcpy's device-side server, vendored as the screen
// stream's source (plan m2p). The file is the release asset of scrcpy Version, unmodified:
// a jar (a zip holding classes.dex) that is pushed to the device and run with
// app_process. The daemon never builds it. SHA256 pins it, and a test fails when the
// embedded bytes and the pin differ; moving to another release is a deliberate change of
// both, with the new asset's published checksum compared first. The licence (Apache-2.0)
// is embedded too and printed by `droidyard version --licenses`; the tag has no NOTICE.
package scrcpyserver

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Version is the scrcpy release the embedded server belongs to. The server checks the
// version its client passes as the first argument and refuses a mismatch, so the client
// must send exactly this string.
const Version = "4.1"

// SHA256 is the hex digest of scrcpy-server, equal to the release's SHA256SUMS.txt entry
// for scrcpy-server-v4.1.
const SHA256 = "deacb991ed2509715160ffdc7907e47b4160eb30d1566217e9047fd5b8850cae"

//go:embed scrcpy-server
var server []byte

//go:embed LICENSE
var licence string

// Server returns the embedded server. The slice is a copy.
func Server() []byte { return append([]byte(nil), server...) }

// Digest is the SHA-256 of the embedded server, computed, not the constant.
func Digest() string {
	h := sha256.Sum256(server)
	return hex.EncodeToString(h[:])
}

// License returns scrcpy's licence text (Apache License, Version 2.0).
func License() string { return licence }

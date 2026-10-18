package scrcpyserver

import "testing"

// Decision (the server is a pinned, vendored artefact): the embedded bytes are the ones
// SHA256 names, and Version is the release they belong to. Changing one byte of the file,
// or the pin or the version alone, fails here.
func TestServerDigest(t *testing.T) {
	if got := Digest(); got != SHA256 {
		t.Fatalf("embedded scrcpy-server has SHA-256 %s, server.go pins %s", got, SHA256)
	}
	if Version != "4.1" {
		t.Errorf("Version = %q, the pinned asset is scrcpy 4.1", Version)
	}
	if len(server) != 733706 {
		t.Errorf("embedded scrcpy-server is %d bytes, the 4.1 asset is 733706", len(server))
	}
}

// Decision (Server returns a copy): a caller that edits the slice cannot change what the
// next push sends.
func TestServerReturnsACopy(t *testing.T) {
	a := Server()
	a[0] ^= 0xff
	if Digest() != SHA256 || Server()[0] == a[0] {
		t.Error("changing the returned slice changed the embedded server")
	}
}

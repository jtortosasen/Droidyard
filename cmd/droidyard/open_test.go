package main

import (
	"io"
	"strings"
	"testing"
)

// readStdin is what the command would be given on its standard input.
func readStdin(t *testing.T, r io.Reader) string {
	t.Helper()
	if r == nil {
		return ""
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Decision (ADR-0102 §3): on macOS the URL, which holds the UI's token, is handed to
// osascript as an AppleScript on standard input, never as an argument that `ps` shows to
// every process of the machine; a character that would end the AppleScript string is escaped.
// Linux keeps xdg-open with the URL as its argument, which is why the plan's row narrows to
// Linux rather than closing. Seen failing with the URL passed as an argument to `open`.
func TestOpenURLNotOnCommandLine(t *testing.T) {
	const token = "9f2c1d0e8b7a6f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c"
	url := uiURL("127.0.0.1:4242", token, "demo-0123456789ab")

	mac := openCommand("darwin", url)
	if mac.Path != "/usr/bin/osascript" {
		t.Errorf("darwin runs %q, want osascript by its fixed path", mac.Path)
	}
	for _, a := range mac.Args {
		if strings.Contains(a, token) || strings.Contains(a, "127.0.0.1") {
			t.Errorf("an argument of the darwin command holds the URL: %q", mac.Args)
		}
	}
	if got, want := readStdin(t, mac.Stdin), `open location "`+url+"\"\n"; got != want {
		t.Errorf("darwin script on stdin = %q, want %q", got, want)
	}
	if len(mac.Env) != 0 {
		t.Errorf("the darwin command has its own environment %v: the token must not travel there either", mac.Env)
	}

	// A quote or a backslash cannot end the string and run what follows it.
	hostile := openCommand("darwin", `http://x/#a"b\c`)
	if got, want := readStdin(t, hostile.Stdin), `open location "http://x/#a\"b\\c"`+"\n"; got != want {
		t.Errorf("escaped script = %q, want %q", got, want)
	}

	// Linux is unchanged: the launcher takes the URL as an argument (ADR-0102 §3).
	linux := openCommand("linux", url)
	if len(linux.Args) != 2 || linux.Args[0] != "xdg-open" || linux.Args[1] != url {
		t.Errorf("linux command %q, want xdg-open with the URL", linux.Args)
	}
}

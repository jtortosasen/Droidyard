package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/hosting"
)

// A detached share survives a restart of the daemon (plan m2u step 5; ADR-0108 §2, §7): real
// `serve` processes, stopped and started on the same state root, and the shipped `share -d`,
// `join` and `host unlend`. Every share names --on 127.0.0.1.

func sharesFileOf(state string) string { return filepath.Join(state, "hosting", "shares.json") }

// secretOf is the secret inside a code line.
func secretOf(t *testing.T, line string) [16]byte {
	t.Helper()
	c, err := hosting.ParseCode(line)
	if err != nil {
		t.Fatal(err)
	}
	return c.Secret
}

// remoteConnected waits for the client's link to the host to be up.
func remoteConnected(t *testing.T, state string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if r := healthOn(t, state).Remotes; len(r) == 1 && r[0].Connected {
			return
		}
	}
	t.Fatalf("the client's link did not come up: %+v", healthOn(t, state).Remotes)
}

// TestDetachedShareSurvivesRestart: `share -d`, two codes unused, `droidyard stop` and a start
// of the host → the listener returns on the same port and the code joins (seen failing with the
// file not read at start: initShares not reading shares.json, the join is refused); a client
// that joined relinks with no code after the next restart; a code used before a restart does
// not work after it; shares.json is 0600 in 0700 and holds no secret byte; the daemon crashed
// (kill -9) with a code unused keeps it; `host unlend` voids the code of its device and the
// file loses the record; a corrupt file is quarantined, reported in system.health, and its
// codes are void.
func TestDetachedShareSurvivesRestart(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	a := newShareNode(t, "List of devices attached\n")
	c := newShareNode(t, "List of devices attached\n")
	d := newShareNode(t, "List of devices attached\n")
	hn, _ := os.Hostname()
	base := hosting.NameFrom(hn, nil)

	share := func() string {
		code, out, stderr := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
		if code != 0 {
			t.Fatalf("share -d exited %d: %s", code, stderr)
		}
		return codeLine(t, out)
	}
	code, out, _ := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 || !strings.Contains(out, "resumes when the daemon next starts") || !strings.Contains(out, "any droidyard command") {
		t.Errorf("share -d does not say the share resumes when the daemon next starts (exit %d):\n%s", code, out)
	}
	lineA := codeLine(t, out)
	lineC, lineD := share(), share()
	port := hostingOf(t, host.state).Listening

	// The file: hashes only, 0600 in 0700.
	raw, err := os.ReadFile(sharesFileOf(host.state))
	if err != nil {
		t.Fatalf("share -d wrote no shares.json: %v", err)
	}
	for _, line := range []string{lineA, lineC, lineD} {
		s := secretOf(t, line)
		for _, form := range []string{hex.EncodeToString(s[:]), base64.RawURLEncoding.EncodeToString(s[:]), base64.StdEncoding.EncodeToString(s[:])} {
			if strings.Contains(string(raw), form) {
				t.Errorf("shares.json holds a secret as %s", form)
			}
		}
		if bytes.Contains(raw, s[:]) {
			t.Error("shares.json holds the raw bytes of a secret")
		}
	}
	if fi, err := os.Stat(sharesFileOf(host.state)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("shares.json is %v, %v; want 0600", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(sharesFileOf(host.state))); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("hosting/ is %v, %v; want 0700", fi, err)
	}

	// Stop and start: the listener is back on the same address and the unused code joins.
	host.restart(t)
	if got := hostingOf(t, host.state).Listening; strings.Join(got, ",") != strings.Join(port, ",") || len(got) != 1 {
		t.Errorf("after the restart the host listens on %v; want %v", got, port)
	}
	if code, out, stderr := a.run(t, "join", lineA); code != 0 || !strings.Contains(out, base+"/"+sharePhone) {
		t.Fatalf("a code issued before the restart: exit %d, %q %q; want it to join", code, out, stderr)
	}
	// Used before a restart, void after it.
	host.restart(t)
	if code, _, stderr := d.run(t, "join", lineA); code != 1 || !strings.Contains(stderr, "did not admit") {
		t.Errorf("a code used before a restart: exit %d, %q; want refused", code, stderr)
	}
	// A second restart: the client that joined relinks with no code, and the codes unused
	// through two restarts still work.
	remoteConnected(t, a.state)
	if _, ok := a.listed(t, base+"/"+sharePhone); !ok {
		t.Error("the client lost the phone across the host's restarts")
	}
	if code, out, stderr := c.run(t, "join", lineC); code != 0 {
		t.Fatalf("a code unused through two restarts: exit %d, %q %q", code, out, stderr)
	}

	// A host killed with the code unused keeps it: nothing ran a shutdown.
	host.crash(t)
	if code, out, stderr := d.run(t, "join", lineD); code != 0 {
		t.Fatalf("a code unused after kill -9: exit %d, %q %q", code, out, stderr)
	}

	// host unlend voids the codes of its device: the record is dropped at start.
	lineE := share()
	if code, out, stderr := host.run(t, "stop"); code != 0 {
		t.Fatalf("stop exited %d: %q %q", code, out, stderr)
	}
	host.ended(t)
	if code, out, stderr := host.run(t, "host", "unlend", sharePhone); code != 0 {
		t.Fatalf("host unlend exited %d: %q %q", code, out, stderr)
	}
	host.start(t)
	e := newShareNode(t, "List of devices attached\n")
	if code, _, stderr := e.run(t, "join", lineE); code != 1 {
		t.Errorf("a code of a device unlent while the daemon was down: exit %d, %q; want refused", code, stderr)
	}
	if raw, err := os.ReadFile(sharesFileOf(host.state)); err != nil || strings.Contains(string(raw), sharePhone) {
		t.Errorf("shares.json after unlend is %q, %v; want no record of %s", raw, err, sharePhone)
	}

	// A corrupt file is quarantined and reported, and its codes are void.
	lineF := share()
	if code, _, stderr := host.run(t, "stop"); code != 0 {
		t.Fatalf("stop exited %d: %q", code, stderr)
	}
	host.ended(t)
	garbage := []byte("{ not json")
	if err := os.WriteFile(sharesFileOf(host.state), garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	host.start(t)
	moved := ""
	for _, p := range healthOn(t, host.state).StateProblems {
		if p.File == "hosting/shares.json" && p.Problem == "corrupt" {
			moved = p.MovedTo
		}
	}
	if kept, err := os.ReadFile(filepath.Join(host.state, moved)); moved == "" || err != nil || !bytes.Equal(kept, garbage) {
		t.Errorf("system.health stateProblems has no hosting/shares.json quarantined with its bytes: moved %q, %v", moved, err)
	}
	f := newShareNode(t, "List of devices attached\n")
	if code, _, stderr := f.run(t, "join", lineF); code != 1 {
		t.Errorf("a code whose file was corrupt: exit %d, %q; want refused", code, stderr)
	}
}

// TestForegroundShareDoesNotSurviveAKill: a foreground share's host killed with kill -9 and
// started again lends nothing and listens nowhere, and shares.json has no record of it.
func TestForegroundShareDoesNotSurviveAKill(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	sh := startLive(t, host.state, "share", sharePhone, "--on", "127.0.0.1")
	line := codeOf(t, sh.expect(t, "droidyard join dy1.", 10*time.Second))
	if got := hostingOf(t, host.state).Listening; len(got) != 1 {
		t.Fatalf("the foreground share is not listening: %v", got)
	}
	// Nothing of it is written while it runs, so kill -9 cannot leave it behind.
	if raw, err := os.ReadFile(sharesFileOf(host.state)); err == nil && strings.Contains(string(raw), sharePhone) {
		t.Errorf("shares.json holds the running foreground share:\n%s", raw)
	}
	host.crash(t)
	sh.cmd.Process.Kill()

	if got := hostingOf(t, host.state).Listening; len(got) != 0 {
		t.Errorf("after kill -9 the host listens on %v; want nowhere", got)
	}
	if lend := host.policy(t).Hosting.Lend; len(lend) != 0 {
		t.Errorf("after kill -9 the host lends %+v; want nothing", lend)
	}
	if raw, err := os.ReadFile(sharesFileOf(host.state)); err == nil && strings.Contains(string(raw), sharePhone) {
		t.Errorf("shares.json holds the foreground share:\n%s", raw)
	}
	client := newShareNode(t, "List of devices attached\n")
	if code, _, _ := client.run(t, "join", line); code != 1 {
		t.Errorf("the foreground share's code after kill -9: exit %d; want it refused", code)
	}
}

// TestUnlendOnARunningHostVoidsItsCodes: `host unlend` writes the configuration with no word to
// the running daemon; the codes of the share it ended must not come back when the serial is
// shared again. A running host, `share -d`, an unused code, `host unlend`, another command that
// makes the daemon check, `share -d` again → the old code is refused and the new one joins.
// Seen failing with both the check and the void on a new lend entry removed: the old code joins.
func TestUnlendOnARunningHostVoidsItsCodes(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	a := newShareNode(t, "List of devices attached\n")
	c := newShareNode(t, "List of devices attached\n")
	_, out, _ := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	old := codeLine(t, out)
	if code, _, stderr := host.run(t, "host", "unlend", sharePhone); code != 0 {
		t.Fatalf("host unlend exited %d: %s", code, stderr)
	}
	// Another share makes the daemon check its configuration.
	if code, _, stderr := host.run(t, "share", "-d", sharePhone2, "--on", "127.0.0.1"); code != 0 {
		t.Fatalf("share of the second phone exited %d: %s", code, stderr)
	}
	code, out, stderr := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 {
		t.Fatalf("share again exited %d: %s", code, stderr)
	}
	fresh := codeLine(t, out)
	if code, _, stderr := a.run(t, "join", old); code != 1 || !strings.Contains(stderr, "did not admit") {
		t.Errorf("the code of the share host unlend ended: exit %d, %q; want refused", code, stderr)
	}
	if code, out, stderr := c.run(t, "join", fresh); code != 0 {
		t.Errorf("the new share's code: exit %d, %q %q; want it to join", code, out, stderr)
	}
}

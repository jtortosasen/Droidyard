package main

import (
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// share and join end to end (ADR-0107, ADR-0108): real `serve` processes on loopback, each
// with a fake adb of its own, and the shipped commands `share -d`, `unshare` and `join`
// run as processes. Every share names --on 127.0.0.1, so nothing here binds a VPN or LAN
// address (plan m2u run rule K3). The daemons match their listeners and links to the
// configuration only when a command asks them to (reconcileEveryEnv), so what the commands
// do alone is what the test sees.

const (
	sharePhone     = "R58M001"
	sharePhone2    = "R58M002"
	shareUnauth    = "R58M009"
	shareEmulator  = "emulator-5554"
	shareLongCycle = "1h"
)

// shareHostListing is the host's `adb devices -l`: an emulator, two phones that are online, one
// that has not accepted the debugging prompt and a serial two phones report.
func shareHostListing() string {
	phone := func(serial, state, usb string) string {
		return serial + " " + state + " usb:" + usb + " product:raven model:Pixel_6_Pro transport_id:2\n"
	}
	return "List of devices attached\n" +
		shareEmulator + " device product:sdk_gphone16k_arm64 model:sdk_gphone16k_arm64 transport_id:1\n" +
		phone(sharePhone, "device", "1-1.1") + phone(sharePhone2, "device", "1-1.2") + phone(shareUnauth, "unauthorized", "1-1.3")
}

// shareNode is one daemon of the lab: its state root, its fake adb's directory and its process.
type shareNode struct {
	state, dir string
	env        []string
	proc       *process
}

// newShareNode starts a `serve` whose adb is the fake over a directory holding listing (the
// host's phones, or the header alone for a client), with the reconcile left to the commands.
func newShareNode(t *testing.T, listing string, extraEnv ...string) *shareNode {
	t.Helper()
	n := &shareNode{state: shortDir(t), dir: t.TempDir()}
	devicetest.Write(t, n.dir, "devices.out", []byte(listing))
	for _, s := range []string{sharePhone, sharePhone2, shareEmulator} {
		devicetest.Write(t, n.dir, s+".bootcompleted.out", []byte("1\n"))
		devicetest.Write(t, n.dir, s+".getprop", []byte(fixtureFile(t, "pixel9a-getprop.txt")))
		devicetest.Write(t, n.dir, s+".dumptty.out", []byte(fixtureFile(t, "pixel9a-uidump-devtty.xml")))
		devicetest.Write(t, n.dir, s+".push.exit", []byte("1")) // the persistent reader cannot run: the dump path answers
	}
	env := append([]string{adbFakeEnv + "=" + n.dir, adbPortEnv + "=" + unusedPort(t), reconcileEveryEnv + "=" + shareLongCycle}, extraEnv...)
	n.env = env
	n.start(t)
	return n
}

// start runs `serve` on the node's state root.
func (n *shareNode) start(t *testing.T) {
	t.Helper()
	n.proc = droidyardEnv(t, n.state, "", n.env, "serve")
	n.proc.expect(t, "holding")
	proc := n.proc
	go func() {
		for range proc.lines {
		}
	}()
}

// ended waits for the node's process to be gone.
func (n *shareNode) ended(t *testing.T) {
	t.Helper()
	select {
	case <-n.proc.exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("pid %d did not end within 10 s", n.proc.cmd.Process.Pid)
	}
}

// restart is `droidyard stop` and a new `serve` on the same state root.
func (n *shareNode) restart(t *testing.T) {
	t.Helper()
	if code, _, stderr := n.run(t, "stop"); code != 0 {
		t.Fatalf("stop exited %d: %s", code, stderr)
	}
	n.ended(t)
	n.start(t)
}

// crash is the daemon killed with no shutdown, and a new `serve` on the same state root.
func (n *shareNode) crash(t *testing.T) {
	t.Helper()
	n.proc.cmd.Process.Kill()
	n.ended(t)
	n.start(t)
}

// run is one `droidyard` command against this node's daemon: its exit status, stdout and stderr.
func (n *shareNode) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	p := droidyard(t, n.state, args...)
	code := p.wait(t)
	return code, p.stdout.String(), p.stderr.String()
}

func (n *shareNode) policy(t *testing.T) policy.GetResult {
	t.Helper()
	env := callOn(t, n.state, "policy.get")
	var res policy.GetResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("policy.get %s: %v", env.Result, err)
	}
	return res
}

func (n *shareNode) fingerprint(t *testing.T) string { return identityOf(t, n.state) }

func (n *shareNode) devices(t *testing.T) []device.Device {
	t.Helper()
	env := callOn(t, n.state, "device.list")
	if env.Error != nil {
		t.Fatalf("device.list: %+v", env.Error)
	}
	var res device.ListResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("device.list %s: %v", env.Result, err)
	}
	return res.Devices
}

// listed is the device with this id in the node's next device.list, if any.
func (n *shareNode) listed(t *testing.T, id string) (device.Device, bool) {
	for _, d := range n.devices(t) {
		if d.Device == id {
			return d, true
		}
	}
	return device.Device{}, false
}

// codeLine is the code on the last line of a share's output: `droidyard join <code>`.
func codeLine(t *testing.T, stdout string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	code, ok := strings.CutPrefix(lines[len(lines)-1], "droidyard join ")
	if !ok || !strings.HasPrefix(code, hosting.CodePrefix) {
		t.Fatalf("the last line of share's output is %q; want `droidyard join dy1.…`\n%s", lines[len(lines)-1], stdout)
	}
	return code
}

func lendOf(res policy.GetResult, serial string) (policy.Lend, bool) {
	for _, l := range res.Hosting.Lend {
		if l.Device == serial {
			return l, true
		}
	}
	return policy.Lend{}, false
}

// TestShareAndJoin is the milestone's first observable result (plan m2u step 3): a host shares
// a phone with one command and prints one line; a client pastes it; the phone is in the
// client's device list with no restart and no file edited; a code works once; a share stopped
// voids its codes and a new one lets the same client back under the same names. Each decision
// the rows pin is named in its comment.
func TestShareAndJoin(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	a := newShareNode(t, "List of devices attached\n")
	c := newShareNode(t, "List of devices attached\n")
	// One host name for all three daemons (they are one machine): the host names the first
	// client from it, the second gets "-2", and each client names the host from it too.
	hn, _ := os.Hostname()
	base := hosting.NameFrom(hn, nil)
	aFP, cFP, hostFP := a.fingerprint(t), c.fingerprint(t), host.fingerprint(t)

	// Only physical devices that are online are shared, and a refusal writes nothing: the
	// configuration does not even exist afterwards.
	for _, row := range []struct{ serial, code string }{
		{shareEmulator, "DEVICE_NOT_FOUND"}, {"nosuch", "DEVICE_NOT_FOUND"}, {shareUnauth, "DEVICE_NOT_READY"},
	} {
		code, _, stderr := host.run(t, "share", "-d", row.serial, "--on", "127.0.0.1")
		if code != 1 || !strings.Contains(stderr, row.code) {
			t.Errorf("share -d %s: exit %d, %q; want exit 1 and %s", row.serial, code, stderr, row.code)
		}
	}
	if before := host.policy(t); before.State != policy.StateMissing {
		t.Errorf("the refused shares left a configuration: %+v", before)
	}

	// share -d: one line, the policy gains allowPhysical, the allowlist entry and the lend
	// entry (no one has joined: an empty `to`), and the listener is in system.health.
	code, out, stderr := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 {
		t.Fatalf("share -d exited %d: %s", code, stderr)
	}
	line1 := codeLine(t, out)
	got := host.policy(t)
	if l, ok := lendOf(got, sharePhone); !ok || len(l.To) != 0 || !got.Devices.AllowPhysical || !slices.Contains(got.Devices.Allowlist, sharePhone) || got.Hosting.On != "127.0.0.1" {
		t.Errorf("after share -d the policy is devices %+v, hosting %+v; want allowPhysical, %s allowlisted and lent to no one, on 127.0.0.1", got.Devices, got.Hosting, sharePhone)
	}
	listening := hostingOf(t, host.state).Listening
	if len(listening) != 1 || !strings.HasPrefix(listening[0], "127.0.0.1:") {
		t.Fatalf("system.health lists the listeners %v; want 127.0.0.1 and the port", listening)
	}
	parsed, err := hosting.ParseCode(line1)
	if err != nil {
		t.Fatal(err)
	}
	wantFP, _ := hex.DecodeString(hostFP)
	if len(parsed.Addrs) != 1 || parsed.Addrs[0].IP != netip.MustParseAddr("127.0.0.1") || parsed.Addrs[0].Kind != hosting.KindLAN ||
		netip.AddrPortFrom(parsed.Addrs[0].IP, parsed.Port).String() != listening[0] || !slices.Equal(parsed.Fingerprint[:], wantFP) {
		t.Errorf("the code carries %+v; want 127.0.0.1 tagged lan, the listener's port and the host's fingerprint", parsed)
	}
	for _, want := range []string{"sharing " + sharePhone, "listening on " + listening[0] + " (lan)", "whole device", "droidyard device disallow " + sharePhone} {
		if !strings.Contains(out, want) {
			t.Errorf("share's output lacks %q:\n%s", want, out)
		}
	}

	// A joins: exit 0, and the very next device.list holds the phone, physical and from the
	// host, with no restart (seen failing with Host.Reconcile not called: no link, no device).
	code, out, stderr = a.run(t, "join", line1)
	if code != 0 || !strings.Contains(out, base+"/"+sharePhone+" is listed now") {
		t.Fatalf("join exited %d: %q %q", code, out, stderr)
	}
	d, ok := a.listed(t, base+"/"+sharePhone)
	if !ok || d.Kind != device.KindPhysical || d.Host != base {
		t.Errorf("the client's device.list holds %+v (%v); want %s/%s physical from %s", d, ok, base, sharePhone, base)
	}
	ap := a.policy(t)
	if len(ap.Remotes) != 1 || ap.Remotes[0].Name != base || ap.Remotes[0].Fingerprint != hostFP || ap.Remotes[0].Address != listening[0] ||
		!slices.Equal(ap.Remotes[0].Addresses, []policy.RemoteAddress{{Address: listening[0], Kind: "lan"}}) ||
		!ap.Devices.AllowPhysical || !slices.Contains(ap.Devices.Allowlist, base+"/"+sharePhone) {
		t.Errorf("the client's policy is remotes %+v, devices %+v; want the host with its tagged address, allowPhysical and %s/%s allowed", ap.Remotes, ap.Devices, base, sharePhone)
	}
	hp := host.policy(t)
	if len(hp.Hosting.Peers) != 1 || hp.Hosting.Peers[0].Name != base || hp.Hosting.Peers[0].Fingerprint != aFP {
		t.Errorf("the host's peers are %+v; want the client's fingerprint under %s", hp.Hosting.Peers, base)
	}
	if l, _ := lendOf(hp, sharePhone); !slices.Equal(l.To, []string{base}) {
		t.Errorf("the phone is lent to %v; want [%s]", l.To, base)
	}
	if !strings.Contains(host.proc.stderr.String(), base+" joined from 127.0.0.1:") || !strings.Contains(host.proc.stderr.String(), aFP[:12]) || !strings.Contains(host.proc.stderr.String(), `lent "`+sharePhone+`"`) {
		t.Errorf("the host's log has no join line with the peer, address, fingerprint prefix and device:\n%s", host.proc.stderr.String())
	}
	if strings.Contains(host.proc.stderr.String(), strings.TrimPrefix(line1, hosting.CodePrefix)) {
		t.Error("the host's log holds the code")
	}
	// uiTree on the borrowed phone reaches the host's adb as -s <serial>.
	env := callOn(t, a.state, "device.uiTree", "--params", `{"device":"`+base+"/"+sharePhone+`"}`)
	if env.Error != nil {
		t.Fatalf("device.uiTree on the borrowed phone: %+v", env.Error)
	}
	if !slices.ContainsFunc(callsIn(t, host.dir), func(c string) bool { return strings.HasPrefix(c, "-s "+sharePhone+" ") }) {
		t.Errorf("the host's adb was not asked about %s: %v", sharePhone, callsIn(t, host.dir))
	}

	// The same code from a third daemon is refused and nothing changes on either side (seen
	// failing with the code not removed when used: C joins).
	cBefore, hBefore := c.policy(t).Revision, host.policy(t).Revision
	code, _, stderr = c.run(t, "join", line1)
	if code != 1 || !strings.Contains(stderr, "HOSTING_JOIN_FAILED") || !strings.Contains(stderr, "did not admit") {
		t.Errorf("a used code: exit %d, %q; want HOSTING_JOIN_FAILED, the host did not admit", code, stderr)
	}
	if c.policy(t).Revision != cBefore || host.policy(t).Revision != hBefore {
		t.Error("a refused join changed a configuration")
	}
	if !strings.Contains(host.proc.stderr.String(), "refused a join") {
		t.Errorf("the host's log has no refused-join line:\n%s", host.proc.stderr.String())
	}

	// share again: a fresh code on the same share; C joins it and is named with -2.
	code, out, stderr = host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 {
		t.Fatalf("second share exited %d: %s", code, stderr)
	}
	line2 := codeLine(t, out)
	if line2 == line1 {
		t.Error("the second share printed the same code")
	}
	if code, out, stderr = c.run(t, "join", line2); code != 0 {
		t.Fatalf("C's join exited %d: %q %q", code, out, stderr)
	}
	hp = host.policy(t)
	if len(hp.Hosting.Peers) != 2 || hp.Hosting.Peers[1].Name != base+"-2" || hp.Hosting.Peers[1].Fingerprint != cFP {
		t.Errorf("the host's peers are %+v; want the second client as %s-2", hp.Hosting.Peers, base+"-2")
	}
	if l, _ := lendOf(hp, sharePhone); !slices.Equal(l.To, []string{base, base + "-2"}) || len(hp.Hosting.Lend) != 1 {
		t.Errorf("lend is %+v; want one entry for the phone, to %s and %s-2", hp.Hosting.Lend, base, base)
	}
	if _, ok := c.listed(t, base+"/"+sharePhone); !ok {
		t.Error("C's device.list lacks the phone it joined")
	}

	// A code edited to name C's fingerprint is notThisHost, and C's log holds no join line; a
	// truncated one is REQUEST_INVALID_PARAMS with nothing dialled (the host's log is unchanged).
	code, out, stderr = host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 {
		t.Fatalf("third share exited %d: %s", code, stderr)
	}
	line3 := codeLine(t, out)
	edited, err := hosting.ParseCode(line3)
	if err != nil {
		t.Fatal(err)
	}
	cRaw, _ := hex.DecodeString(cFP)
	copy(edited.Fingerprint[:], cRaw)
	editedLine, err := edited.Encode()
	if err != nil {
		t.Fatal(err)
	}
	cJoins := strings.Count(c.proc.stderr.String(), "joined ")
	code, _, stderr = c.run(t, "join", editedLine)
	if code != 1 || !strings.Contains(stderr, "HOSTING_JOIN_FAILED") || !strings.Contains(stderr, "not the one that issued it") {
		t.Errorf("a code naming another certificate: exit %d, %q; want notThisHost", code, stderr)
	}
	if strings.Count(c.proc.stderr.String(), "joined ") != cJoins {
		t.Errorf("C's log has a new join line after a join that failed:\n%s", c.proc.stderr.String())
	}
	// Join lines only: an earlier call's trace line may still land meanwhile.
	hostJoins := func() int { return strings.Count(host.proc.stderr.String(), "join") }
	before := hostJoins()
	code, _, stderr = c.run(t, "join", line3[:len(line3)-20])
	if code != 1 || !strings.Contains(stderr, "REQUEST_INVALID_PARAMS") {
		t.Errorf("a truncated code: exit %d, %q; want REQUEST_INVALID_PARAMS", code, stderr)
	}
	if hostJoins() != before {
		t.Errorf("the host heard of a join that was never dialled:\n%s", host.proc.stderr.String())
	}

	// unshare: the lend goes, the client's next device.list lacks the phone, the codes void (the
	// host keeps listening for a second phone so the refusal is the host's, not a closed port),
	// and both peers stay paired.
	if code, out, stderr = host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1"); code != 0 {
		t.Fatalf("share before unshare exited %d: %s", code, stderr)
	}
	unusedBefore := codeLine(t, out)
	if code, out, stderr = host.run(t, "share", "-d", sharePhone2, "--on", "127.0.0.1"); code != 0 {
		t.Fatalf("share of the second phone exited %d: %s", code, stderr)
	}
	if code, out, stderr = host.run(t, "unshare", sharePhone); code != 0 || !strings.Contains(out, "no longer sharing "+sharePhone) {
		t.Fatalf("unshare exited %d: %q %q", code, out, stderr)
	}
	if _, ok := a.listed(t, base+"/"+sharePhone); ok {
		t.Error("the client still lists the phone after unshare")
	}
	hp = host.policy(t)
	if _, ok := lendOf(hp, sharePhone); ok || len(hp.Hosting.Peers) != 2 {
		t.Errorf("after unshare lend is %+v and peers %+v; want no lend of the phone and both peers", hp.Hosting.Lend, hp.Hosting.Peers)
	}
	code, _, stderr = c.run(t, "join", line3)
	if code != 1 || !strings.Contains(stderr, "did not admit") {
		t.Errorf("a code issued before unshare: exit %d, %q; want refused", code, stderr)
	}

	// share again; the first client joins the new code: the same name on both sides, lent
	// again, and no second remote entry (seen failing with a pinned certificate refused on the
	// join route).
	code, out, stderr = host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	if code != 0 {
		t.Fatalf("share after unshare exited %d: %s", code, stderr)
	}
	if code, out, stderr = a.run(t, "join", codeLine(t, out)); code != 0 || !strings.Contains(out, base+"/"+sharePhone) {
		t.Fatalf("the first client's second join exited %d: %q %q", code, out, stderr)
	}
	hp, ap = host.policy(t), a.policy(t)
	if len(hp.Hosting.Peers) != 2 || hp.Hosting.Peers[0].Name != base || len(ap.Remotes) != 1 || ap.Remotes[0].Name != base {
		t.Errorf("after the second join the host's peers are %+v and the client's remotes %+v; want the same names and no second entry", hp.Hosting.Peers, ap.Remotes)
	}
	if l, _ := lendOf(hp, sharePhone); !slices.Contains(l.To, base) {
		t.Errorf("the phone is lent to %v after the second join; want %s among them", l.To, base)
	}
	// The phone is lent again, so only the voiding of its codes keeps the one issued before
	// unshare from working now (seen failing with unshare not voiding the codes).
	if code, _, stderr = c.run(t, "join", unusedBefore); code != 1 || !strings.Contains(stderr, "did not admit") {
		t.Errorf("an unused code issued before unshare, after the phone is shared again: exit %d, %q; want refused", code, stderr)
	}
	eventually(t, "the phone to be listed again on the first client", func() bool {
		_, ok := a.listed(t, base+"/"+sharePhone)
		return ok
	})
}

// TestJoinTakesTheHostsAnswerAsUntrusted: the host's answer is another machine's text (ADR-0100
// §4). A host that answers a name with a path, a capital and a non-ASCII letter is named by the
// pattern's cleaning (the remote is "host": NameFrom keeps the part before the first dot, and
// this one starts with it); one that answers a device that is not a serial
// fails the join as refused and nothing is written. The host here is a seam of the joining
// daemon, an answer read from a file at each join.
func TestJoinTakesTheHostsAnswerAsUntrusted(t *testing.T) {
	t.Parallel()
	reply := filepath.Join(t.TempDir(), "reply.json")
	node := newShareNode(t, "List of devices attached\n", joinReplyEnv+"="+reply)
	// Any well-formed code: the seam answers for the host, so nothing is dialled.
	code := hosting.Code{Addrs: []hosting.Addr{{IP: netip.MustParseAddr("127.0.0.1"), Kind: hosting.KindLAN}}, Port: 1}
	code.Fingerprint[0], code.Secret[0] = 1, 1
	line, err := code.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Each device the host must not be believed about leaves the configuration as it was.
	before := node.policy(t).Revision
	for _, bad := range []string{"a/b", "", "has space", "tab\t", "esc\x1b[0m", strings.Repeat("x", 65)} {
		raw, _ := json.Marshal(capability.PeerJoinReply{Host: "Evil", Device: bad})
		if err := os.WriteFile(reply, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		exit, _, stderr := node.run(t, "join", line)
		if exit != 1 || !strings.Contains(stderr, "HOSTING_JOIN_FAILED") || !strings.Contains(stderr, "not a serial") {
			t.Errorf("a host answering the device %q: exit %d, %q; want the join refused", bad, exit, stderr)
		}
		if node.policy(t).Revision != before {
			t.Fatalf("the host's device %q was written to the configuration", bad)
		}
	}

	raw, _ := json.Marshal(capability.PeerJoinReply{Host: "../Évil Host", Device: sharePhone})
	if err := os.WriteFile(reply, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out, stderr := node.run(t, "join", line)
	if exit != 0 || !strings.Contains(out, "host/"+sharePhone+" is listed") {
		t.Fatalf("join exited %d: %q %q", exit, out, stderr)
	}
	got := node.policy(t)
	if len(got.Remotes) != 1 || got.Remotes[0].Name != "host" || !slices.Contains(got.Devices.Allowlist, "host/"+sharePhone) {
		t.Errorf("the client's policy is remotes %+v, allowlist %v; want the cleaned name host", got.Remotes, got.Devices.Allowlist)
	}
}

// TestJoinWithAnUnusableConfigurationDialsNothing: a client whose configuration cannot be used
// fails POLICY_CONFIG_UNUSABLE before it dials, so the host's code is not spent by a join that
// could not be recorded (plan m2u step 3).
func TestJoinWithAnUnusableConfigurationDialsNothing(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	client := newShareNode(t, "List of devices attached\n")
	_, out, _ := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	line := codeLine(t, out)
	configAt(t, client.state, `{"hosting":{"lend":"not a list"}}`)
	// Join lines only: the share's own trace line may still land after the CLI returned.
	joins := func() int { return strings.Count(host.proc.stderr.String(), "join") }
	before := joins()
	code, _, stderr := client.run(t, "join", line)
	if code != 1 || !strings.Contains(stderr, "POLICY_CONFIG_UNUSABLE") {
		t.Errorf("join with an unusable configuration: exit %d, %q; want POLICY_CONFIG_UNUSABLE", code, stderr)
	}
	if joins() != before {
		t.Errorf("the host heard from a client whose configuration was unusable:\n%s", host.proc.stderr.String())
	}
	// The code was not spent: with the configuration fixed it joins.
	configAt(t, client.state, `{"schemaVersion":1}`)
	if code, _, stderr := client.run(t, "join", line); code != 0 {
		t.Errorf("the same code after the configuration was fixed: exit %d, %q; want it unspent", code, stderr)
	}
}

// TestAdmitThatFailsSpendsTheCode: the host consumes the code before it pairs, so a device
// that stopped being lent in between leaves the code spent (ADR-0107 §4; plan m2u step 1). The
// join is refused, a second try with the same code is refused too, a fresh share's code works,
// and the host's log neither quotes the name the joining side proposed (another machine's
// text) nor holds the code. The proposing side here is hosting.Join called from the test with a
// hostile name, since the shipped client always sends a clean one.
func TestAdmitThatFailsSpendsTheCode(t *testing.T) {
	t.Parallel()
	host := newShareNode(t, shareHostListing())
	_, out, _ := host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	line := codeLine(t, out)
	parsed, err := hosting.ParseCode(line)
	if err != nil {
		t.Fatal(err)
	}
	// The lending is edited away by hand: unshare would void the code, which is not the case under test.
	updateOn(t, host.state, `{"hosting":{"lend":[]}}`)
	id, _, err := hosting.LoadIdentity(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	const hostile = "../Évil\x1b[31m\nHost"
	_, jerr := hosting.Join(t.Context(), id, parsed, hostile, "0.0.0")
	je, ok := jerr.(*hosting.JoinError)
	if !ok || je.Reason != hosting.JoinRefused {
		t.Fatalf("Join with a device no longer lent: %v; want refused", jerr)
	}
	// The daemon's stderr reaches the test through a pipe, a moment after the refusal.
	eventually(t, "the host's log to say the code is spent", func() bool { return strings.Contains(host.proc.stderr.String(), "spent") })
	log := host.proc.stderr.String()
	if !strings.Contains(log, "spent") || strings.Contains(log, "vil") || strings.Contains(log, "\x1b") || strings.Contains(log, strings.TrimPrefix(line, hosting.CodePrefix)) {
		t.Errorf("the host's log must say the code is spent and quote neither the name nor the code:\n%q", log)
	}
	if got := host.policy(t); len(got.Hosting.Peers) != 0 {
		t.Errorf("a refused join paired %+v", got.Hosting.Peers)
	}
	// The same code again: spent, so refused again.
	if _, jerr := hosting.Join(t.Context(), id, parsed, "again", "0.0.0"); jerr == nil {
		t.Error("the code worked a second time")
	}
	// A fresh share makes a new code, which works.
	_, out, _ = host.run(t, "share", "-d", sharePhone, "--on", "127.0.0.1")
	fresh, _ := hosting.ParseCode(codeLine(t, out))
	if _, jerr := hosting.Join(t.Context(), id, fresh, "again", "0.0.0"); jerr != nil {
		t.Errorf("a fresh code after the spent one: %v", jerr)
	}
}

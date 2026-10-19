package sharing

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// fakeBinder is the peer listeners of a lab: it binds whatever is asked and reports it, as the
// peer adapter's server does through Host.SetListening.
type fakeBinder struct{ h *hosting.Host }

func (b fakeBinder) Reconcile(want []netip.AddrPort) ([]netip.AddrPort, map[netip.AddrPort]error) {
	s := make([]string, len(want))
	for i, w := range want {
		s[i] = w.String()
	}
	b.h.SetListening(s, nil)
	return want, nil
}

// newBoundLab is newLab whose host has listeners, so a share succeeds.
func newBoundLab(t *testing.T, config string) *lab {
	t.Helper()
	l := newLab(t, config, loopback, nil)
	l.host.SetDefaultPort(0)
	l.host.UseListeners(fakeBinder{l.host})
	return l
}

var (
	cli1 = capability.Origin{Client: capability.ClientCLI, ID: "c1"}
	cli2 = capability.Origin{Client: capability.ClientCLI, ID: "c2"}
)

// callAs runs a capability's handler for who.
func (l *lab) callAs(t *testing.T, who capability.Origin, name, params string) (any, *capability.Error) {
	t.Helper()
	d, _ := l.reg.Lookup(name)
	p, err := decode(d, params)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := d.Handler(ctx, &capability.Call{Params: p, Origin: who, Progress: func(int, int, string) {}})
	var ce *capability.Error
	if errors.As(err, &ce) {
		return nil, ce
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res, nil
}

func (l *lab) share(t *testing.T, who capability.Origin, params string) ShareResult {
	t.Helper()
	res, ce := l.callAs(t, who, CapShare, params)
	if ce != nil {
		t.Fatalf("share %s: %+v", params, ce)
	}
	return res.(ShareResult)
}

const peerFP = "abababababababababababababababababababababababababababababababab"

// TestForegroundShareLivesInMemory: a foreground share is never in hosting.lend (ADR-0108 §3),
// yet the listeners are wanted for it (the "lends anything" gate counts it), the device is
// lent to a peer that joined it, and all of that goes when the connection that owns it ends.
// Seen failing with the gate counting hosting.lend alone: the share finds no listener and is
// refused, so the first step fails.
func TestForegroundShareLivesInMemory(t *testing.T) {
	l := newBoundLab(t, "")
	res := l.share(t, cli1, `{"device":"R1","on":"127.0.0.1"}`)
	if res.Mode != ModeForeground || !res.Owned || res.Code == "" {
		t.Fatalf("result %+v; want a foreground share this connection owns, with a code", res)
	}
	if got := l.pol.Get(); len(got.Hosting.Lend) != 0 || !got.Devices.AllowPhysical || got.Hosting.On != "127.0.0.1" {
		t.Errorf("policy devices %+v hosting %+v; want the serial admitted and nothing in hosting.lend", got.Devices, got.Hosting)
	}
	if len(l.host.Health().Hosting.Listening) == 0 {
		t.Fatal("no listener for a foreground share")
	}

	// A join admits the peer to the foreground share: lent through the host, not through the policy.
	if _, _, err := l.host.Admit(peerFP, "anna", "R1"); err != nil {
		t.Fatal(err)
	}
	if !l.host.Lent("anna", "R1") || l.pol.Lent("anna", "R1") {
		t.Errorf("Lent through the host %v, through the policy %v; want the host alone", l.host.Lent("anna", "R1"), l.pol.Lent("anna", "R1"))
	}
	if l.host.Lent("anna", "R2") {
		t.Error("the share lends another device")
	}

	// The connection ends: nothing is lent, the listeners go, and the code is void.
	if ended := l.host.EndForegroundOwnedBy("c1", hosting.ShareConnection); len(ended) != 1 || ended[0] != "R1" {
		t.Fatalf("ended %v; want R1", ended)
	}
	l.host.Reconcile()
	if l.host.Lent("anna", "R1") {
		t.Error("the device is still lent after the connection ended")
	}
	if got := l.host.Health().Hosting.Listening; len(got) != 0 {
		t.Errorf("listeners %v after the last share ended; want none", got)
	}
}

// TestShareOfASharedDeviceKeepsItsMode: sharing a serial that is shared, in either mode, gives
// a fresh code on that share and leaves its mode and owner alone (ADR-0108 §5).
func TestShareOfASharedDeviceKeepsItsMode(t *testing.T) {
	l := newBoundLab(t, "")
	first := l.share(t, cli1, `{"device":"R1","on":"127.0.0.1"}`)
	again := l.share(t, cli2, `{"device":"R1"}`)
	if again.Mode != ModeForeground || again.Owned || again.Code == first.Code {
		t.Errorf("second share %+v; want the foreground share, not owned by the second connection, with a fresh code", again)
	}
	if owner, _ := l.host.ForegroundOwner("R1"); owner != "c1" {
		t.Errorf("the owner is %q; the second request must not take the share over", owner)
	}
	// A detached request on a foreground share makes no lend entry.
	det := l.share(t, cli2, `{"device":"R1","detach":true}`)
	if det.Mode != ModeForeground || len(l.pol.Get().Hosting.Lend) != 0 {
		t.Errorf("detached request on a foreground share: %+v, lend %+v; want it to stay foreground with no entry", det, l.pol.Get().Hosting.Lend)
	}
	// A foreground request on a detached share starts no foreground share.
	l.share(t, cli1, `{"device":"R2","detach":true}`)
	fg := l.share(t, cli1, `{"device":"R2"}`)
	if fg.Mode != ModeDetached || fg.Owned || l.host.ForegroundCount() != 1 {
		t.Errorf("foreground request on a detached share: %+v, %d foreground shares; want it to stay detached", fg, l.host.ForegroundCount())
	}
}

// TestForegroundShareBelongsToTheCLI: only a native CLI connection with an id may ask for a
// foreground share (ADR-0108 §3): the UI's WebSocket and a plain HTTP call may not, and
// nothing is written. Seen failing with the client check removed: the UI's share succeeds.
func TestForegroundShareBelongsToTheCLI(t *testing.T) {
	for name, who := range map[string]capability.Origin{
		"the UI":              {Client: capability.ClientUI, ID: "ws1"},
		"a CLI with no id":    {Client: capability.ClientCLI},
		"a call with no kind": {},
	} {
		l := newBoundLab(t, "")
		_, ce := l.callAs(t, who, CapShare, `{"device":"R1","on":"127.0.0.1"}`)
		if ce == nil || ce.Code != capability.ErrInvalidParams || !strings.Contains(ce.NextStep, "detach") {
			t.Errorf("%s: %+v; want REQUEST_INVALID_PARAMS telling it to pass detach", name, ce)
		}
		if l.pol.Get().State != policy.StateMissing || l.host.ForegroundCount() != 0 {
			t.Errorf("%s: the refused share left a mark", name)
		}
	}
}

// TestWatchShareOnlyOnTheCallersOwnForegroundShare: the watch is bounded by the connection
// that owns the share, so another connection, a detached share, another kind of client and no
// share at all are refused (ADR-0108 §6); the owner's watch reports the peers and ends with
// the reason the share ended. Seen failing with the ownership check removed: the second
// connection's watch is accepted.
func TestWatchShareOnlyOnTheCallersOwnForegroundShare(t *testing.T) {
	l := newBoundLab(t, "")
	l.share(t, cli1, `{"device":"R1","on":"127.0.0.1"}`)
	l.share(t, cli1, `{"device":"R2","detach":true}`)
	for name, row := range map[string]struct {
		who    capability.Origin
		device string
	}{
		"another connection":  {cli2, "R1"},
		"a detached share":    {cli1, "R2"},
		"the UI, same id":     {capability.Origin{Client: capability.ClientUI, ID: "c1"}, "R1"},
		"a device not shared": {cli1, "nosuch"},
	} {
		_, ce := l.callAs(t, row.who, CapWatchShare, `{"device":"`+row.device+`"}`)
		if ce == nil || ce.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %+v; want REQUEST_INVALID_PARAMS", name, ce)
		}
	}

	d, _ := l.reg.Lookup(CapWatchShare)
	msgs := make(chan string, 8)
	type outcome struct {
		res any
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := d.Handler(context.Background(), &capability.Call{Params: WatchShareParams{Device: "R1"}, Origin: cli1,
			Progress: func(_, _ int, m string) { msgs <- m }})
		done <- outcome{res, err}
	}()
	next := func() string {
		select {
		case m := <-msgs:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no progress from the watch")
		}
		return ""
	}
	if m := next(); m != "no peers yet" {
		t.Errorf("first progress %q; want no peers yet", m)
	}
	if _, _, err := l.host.Admit(peerFP, "anna", "R1"); err != nil {
		t.Fatal(err)
	}
	if m := next(); m != "anna (not connected)" {
		t.Errorf("progress after the join %q; want anna, not connected", m)
	}
	l.host.PeerOpened("anna")
	if m := next(); m != "anna (connected)" {
		t.Errorf("progress after the link opened %q; want anna connected", m)
	}

	// unshare ends the share and the watch says so.
	if res, ce := l.callAs(t, cli2, CapUnshare, `{"device":"R1"}`); ce != nil || len(res.(UnshareResult).Unshared) != 1 {
		t.Fatalf("unshare: %+v %+v; want R1 unshared", res, ce)
	}
	select {
	case o := <-done:
		if r, ok := o.res.(WatchShareResult); o.err != nil || !ok || r.Ended != hosting.ShareUnshared {
			t.Errorf("the watch ended with %+v, %v; want unshared", o.res, o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end when the share did")
	}
}

// TestPeersMessageRoundTrips: the CLI reads the watch's progress message back.
func TestPeersMessageRoundTrips(t *testing.T) {
	for _, in := range [][]hosting.PeerHealth{
		{},
		{{Name: "anna", Connected: true}},
		{{Name: "anna"}, {Name: "bob-2", Connected: true}},
	} {
		out := ParsePeers(PeersMessage(in))
		if len(out) != len(in) {
			t.Fatalf("%v read back as %v", in, out)
		}
		for i := range in {
			if out[i] != in[i] {
				t.Errorf("%v read back as %v", in, out)
			}
		}
	}
}

// TestShareWithNoDeviceListsTheCandidates: nothing is shared or written; emulators are not
// listed at all; an online device alone under its serial is shareable; offline and a serial
// two devices report are listed with their reason (ADR-0108 §1). Seen failing with the kind
// filter removed: the emulator is listed.
func TestShareWithNoDeviceListsTheCandidates(t *testing.T) {
	l := newBoundLab(t, "")
	res := l.share(t, cli1, `{}`)
	byDevice := map[string][]Candidate{}
	for _, c := range res.Candidates {
		byDevice[c.Device] = append(byDevice[c.Device], c)
	}
	if _, ok := byDevice["emulator-5554"]; ok {
		t.Error("an emulator is listed")
	}
	if r := byDevice["R1"]; len(r) != 1 || !r[0].Shareable || r[0].Transport != "usb" || r[0].Manufacturer != "Google" || r[0].Model != "Pixel" {
		t.Errorf("R1: %+v; want shareable over usb, Google Pixel", r)
	}
	if r := byDevice["R2"]; len(r) != 1 || r[0].Transport != "tcp" {
		t.Errorf("R2: %+v; want tcp", r)
	}
	if r := byDevice["OFF"]; len(r) != 1 || r[0].Shareable || !strings.Contains(r[0].Reason, "offline") {
		t.Errorf("OFF: %+v; want unshareable, offline", r)
	}
	if r := byDevice["DUP"]; len(r) != 2 || r[0].Shareable || r[1].Shareable || r[0].Reason == "" {
		t.Errorf("DUP: %+v; want both listed, unshareable, with a reason", r)
	}
	if res.Device != "" || res.Code != "" || l.pol.Get().State != policy.StateMissing || l.host.ForegroundCount() != 0 {
		t.Errorf("listing shared or wrote something: %+v", res)
	}
}

// TestShareThatCannotListenSaysWhatStays: the write that admitted the serial is not taken back,
// so the failure says the serial stays allowed on this Mac, that allowPhysical being turned on
// admits what was already in the allowlist, and what takes it back. Seen failing with that
// sentence removed from the error.
func TestShareThatCannotListenSaysWhatStays(t *testing.T) {
	l := newLab(t, `{"devices":{"allowPhysical":false,"allowlist":["OTHER"]}}`, loopback, nil)
	_, ce := l.call(t, CapShare, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	if ce == nil || ce.Code != ErrUnavailable {
		t.Fatalf("error %+v; want HOSTING_UNAVAILABLE", ce)
	}
	for _, want := range []string{"R1 stays allowed", "allowPhysical is now on", "OTHER", "droidyard device disallow R1"} {
		if !strings.Contains(ce.NextStep, want) {
			t.Errorf("next step %q lacks %q", ce.NextStep, want)
		}
	}
	// With allowPhysical already on, nothing else was widened and the sentence does not say so.
	l = newLab(t, `{"devices":{"allowPhysical":true,"allowlist":["OTHER"]}}`, loopback, nil)
	_, ce = l.call(t, CapShare, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	if ce == nil || !strings.Contains(ce.NextStep, "R1 stays allowed") || strings.Contains(ce.NextStep, "allowPhysical is now on") {
		t.Errorf("with allowPhysical already on: %+v; want the serial named and no allowPhysical sentence", ce)
	}
}

// TestJoinSaysWhenItTurnsAllowPhysicalOn: a join turns devices.allowPhysical on, which admits
// every serial already in the allowlist; the result says so when that widens anything (as a
// share does), and says nothing when allowPhysical was on already or the allowlist was empty.
// Seen failing with the note removed.
func TestJoinSaysWhenItTurnsAllowPhysicalOn(t *testing.T) {
	linkWait = 10 * time.Millisecond
	t.Cleanup(func() { linkWait = 5 * time.Second })
	join := func(context.Context, *hosting.Identity, hosting.Code, string, string) (hosting.JoinResult, error) {
		return hosting.JoinResult{Reply: capability.PeerJoinReply{Host: "lab", Device: "R1"}, Addr: hosting.Addr{IP: netip.MustParseAddr("127.0.0.1"), Kind: hosting.KindLAN}}, nil
	}
	code := hosting.Code{Addrs: []hosting.Addr{{IP: netip.MustParseAddr("127.0.0.1"), Kind: hosting.KindLAN}}, Port: 1}
	code.Secret[0] = 1
	line, _ := code.Encode()
	for name, row := range map[string]struct {
		config string
		note   bool
	}{
		"off, allowlist held a serial": {`{"devices":{"allowPhysical":false,"allowlist":["OTHER"]}}`, true},
		"on already":                   {`{"devices":{"allowPhysical":true,"allowlist":["OTHER"]}}`, false},
		"off, allowlist empty":         {``, false},
	} {
		l := newLab(t, row.config, loopback, join)
		res, ce := l.call(t, CapJoin, `{"code":"`+line+`"}`)
		if ce != nil {
			t.Fatalf("%s: %+v", name, ce)
		}
		notes := res.(JoinResult).Notes
		if row.note != (len(notes) == 1 && strings.Contains(notes[0], "OTHER") && strings.Contains(notes[0], "allowPhysical")) || (!row.note && len(notes) != 0) {
			t.Errorf("%s: notes %q; want a note naming OTHER: %v", name, notes, row.note)
		}
	}
}

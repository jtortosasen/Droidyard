package sharing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/hosting"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// hookDevices is the adb listing with a hook that runs while hosting.share is reading it.
type hookDevices struct {
	once sync.Once
	hook func()
	manu atomic.Int32 // calls that asked for manufacturers
	all  atomic.Int32
}

func (h *hookDevices) Shareable(_ context.Context, manufacturers bool) ([]device.ShareableDevice, error) {
	h.all.Add(1)
	if manufacturers {
		h.manu.Add(1)
	}
	h.once.Do(func() {
		if h.hook != nil {
			h.hook()
		}
	})
	return phones, nil
}

// TestShareOfAnEndedConnectionIsNotKept is ADR-0108 §3: a call is never cancelled when its
// connection ends, so a hosting.share that is still working after its CLI went must not leave a
// foreground share owned by the dead connection, with the listener open and the phone lent. The
// connection ends (its end is signalled, then the Disconnected hook runs and finds no share
// yet) while the share is reading the device list; the share then fails, owns nothing and
// listens nowhere. Seen failing with the check after the share is registered removed: the share
// stays, owned by "c1".
func TestShareOfAnEndedConnectionIsNotKept(t *testing.T) {
	l := newBoundLab(t, "")
	gone := make(chan struct{})
	devs := &hookDevices{hook: func() {
		close(gone)
		// The hook of main.go: nothing is registered yet, so nothing is ended.
		if len(l.host.EndForegroundOwnedBy("c1", hosting.ShareConnection)) > 0 {
			l.host.Reconcile()
		}
	}}
	reg := capability.NewRegistry()
	Register(reg, Options{Host: l.host, Devices: devs, API: "0.0.0"})
	d, _ := reg.Lookup(CapShare)
	p, err := decode(d, `{"device":"R1","on":"127.0.0.1"}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := capability.WithConnectionGone(context.Background(), gone)
	_, err = d.Handler(ctx, &capability.Call{Params: p, Origin: cli1, Progress: func(int, int, string) {}})
	var ce *capability.Error
	if !errors.As(err, &ce) || ce.Code != ErrUnavailable {
		t.Fatalf("a share whose connection ended: %v; want %s", err, ErrUnavailable)
	}
	if owner, ok := l.host.ForegroundOwner("R1"); ok {
		t.Errorf("a foreground share of R1 owned by the ended connection %q is held", owner)
	}
	if got := l.host.Health().Hosting.Listening; len(got) != 0 {
		t.Errorf("the host listens on %v for a share nobody owns", got)
	}
	if l.host.Lent("anna", "R1") || l.host.CodePending() {
		t.Error("a share of an ended connection lends or holds a code")
	}
}

// TestShareOnAForegroundShareThatEndedMeanwhile is ADR-0108 §5: a request on a device whose
// foreground share is there when it decides, and gone when it has the code, prints no code for
// nothing. The share's connection ends while the listeners are matched (the interface list is
// read there). Seen failing with the final check removed: the second call returns a detached
// share with a code and no lend entry behind it.
func TestShareOnAForegroundShareThatEndedMeanwhile(t *testing.T) {
	l := newBoundLab(t, "")
	l.share(t, cli1, `{"device":"R1","on":"127.0.0.1"}`)
	var calls atomic.Int32
	l.host.SetInterfaces(func() ([]hosting.Iface, error) {
		// The second share reads the list once before it decides and once matching the
		// listeners; the connection of the first share ends at the second read.
		if calls.Add(1) == 2 {
			l.host.EndForegroundOwnedBy("c1", hosting.ShareConnection)
		}
		return loopback, nil
	})
	_, ce := l.callAs(t, cli2, CapShare, `{"device":"R1","detach":true}`)
	if ce == nil || ce.Code != ErrUnavailable {
		t.Fatalf("a request on a foreground share that ended meanwhile: %+v; want %s", ce, ErrUnavailable)
	}
	if l.host.CodePending() {
		t.Error("a code is pending for a share that is not there")
	}
	if lend := l.pol.Get().Hosting.Lend; len(lend) != 0 {
		t.Errorf("hosting.lend is %+v; want no entry", lend)
	}
}

// TestOneSerialNeverHoldsBothModes is ADR-0108 §5: a foreground and a detached share of one
// serial asked together end as exactly one mode, whichever wins, never both (both modes would
// let the foreground's end void the detached share's codes). The mode is decided and the
// foreground share started inside one edit, which the daemon's own edits take in turn. The
// race is not forced: many rounds of two calls started together.
func TestOneSerialNeverHoldsBothModes(t *testing.T) {
	for round := range 40 {
		l := newBoundLab(t, "")
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, req := range []struct {
			who    capability.Origin
			params string
		}{{cli1, `{"device":"R1","on":"127.0.0.1"}`}, {cli2, `{"device":"R1","detach":true,"on":"127.0.0.1"}`}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				d, _ := l.reg.Lookup(CapShare)
				p, _ := decode(d, req.params)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				d.Handler(ctx, &capability.Call{Params: p, Origin: req.who, Progress: func(int, int, string) {}})
			}()
		}
		close(start)
		wg.Wait()
		_, fg := l.host.ForegroundOwner("R1")
		lent := false
		for _, e := range l.pol.Get().Hosting.Lend {
			lent = lent || e.Device == "R1"
		}
		if fg && lent {
			t.Fatalf("round %d: R1 is shared in the foreground and detached at once", round)
		}
	}
}

// TestShareNamedSerialDoesNotAskManufacturers: only the listing of the candidates reads the
// devices' manufacturers; a share of a named serial must not run getprop on every online
// device. Seen failing with the flag always true: the named share asks.
func TestShareNamedSerialDoesNotAskManufacturers(t *testing.T) {
	l := newBoundLab(t, "")
	devs := &hookDevices{}
	reg := capability.NewRegistry()
	Register(reg, Options{Host: l.host, Devices: devs, API: "0.0.0"})
	d, _ := reg.Lookup(CapShare)
	for _, row := range []struct {
		params string
		manu   int32
	}{{`{"device":"R1","detach":true,"on":"127.0.0.1"}`, 0}, {`{}`, 1}} {
		p, _ := decode(d, row.params)
		before := devs.manu.Load()
		if _, err := d.Handler(context.Background(), &capability.Call{Params: p, Origin: cli1, Progress: func(int, int, string) {}}); err != nil {
			t.Fatalf("%s: %v", row.params, err)
		}
		if got := devs.manu.Load() - before; got != row.manu {
			t.Errorf("%s asked for manufacturers %d times; want %d", row.params, got, row.manu)
		}
	}
}

// TestUnshareSweepsAtOnce: unshare stops the peers' streams at once, as the end of a
// foreground share's connection does, not at the next periodic lease sweep (plan m2u A4).
func TestUnshareSweepsAtOnce(t *testing.T) {
	l := newBoundLab(t, "")
	swept := 0
	reg := capability.NewRegistry()
	Register(reg, Options{Host: l.host, Devices: &hookDevices{}, API: "0.0.0", Sweep: func() { swept++ }})
	share, _ := reg.Lookup(CapShare)
	p, _ := decode(share, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	if _, err := share.Handler(context.Background(), &capability.Call{Params: p, Origin: cli1, Progress: func(int, int, string) {}}); err != nil {
		t.Fatal(err)
	}
	unshare, _ := reg.Lookup(CapUnshare)
	p, _ = decode(unshare, `{"device":"R1"}`)
	if _, err := unshare.Handler(context.Background(), &capability.Call{Params: p, Origin: cli1, Progress: func(int, int, string) {}}); err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Errorf("unshare swept the leases %d times; want once, at once", swept)
	}
}

// TestShareAfterAnUnlendDoesNotReviveTheOldCodes: `host unlend` removes the lend entry with no
// word to the daemon, so the codes of the share it ended are still held when the serial is
// shared again; the new lend entry must not bring them back (ADR-0108 §2, §4). Seen failing
// with the void on a new lend entry removed: the old code joins the new share.
func TestShareAfterAnUnlendDoesNotReviveTheOldCodes(t *testing.T) {
	l := newBoundLab(t, "")
	first := l.share(t, cli1, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	old, err := hosting.ParseCode(first.Code)
	if err != nil {
		t.Fatal(err)
	}
	// What host unlend does: the entry goes, the daemon is not told.
	if _, cerr := l.host.Edit(context.Background(), func(cur policy.GetResult) (map[string]any, *capability.Error) {
		return map[string]any{"hosting": map[string]any{"lend": []policy.Lend{}}}, nil
	}); cerr != nil {
		t.Fatal(cerr)
	}
	second := l.share(t, cli1, `{"device":"R1","detach":true,"on":"127.0.0.1"}`)
	if _, ok := l.host.ConsumeCode(old.Secret); ok {
		t.Error("a code of the share that host unlend ended works on the new share")
	}
	fresh, _ := hosting.ParseCode(second.Code)
	if dev, ok := l.host.ConsumeCode(fresh.Secret); !ok || dev != "R1" {
		t.Errorf("the new share's code: %q, %v; want R1", dev, ok)
	}
}

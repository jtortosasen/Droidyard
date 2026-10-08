package device

import (
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// The two getprop calls D3 caches, as they appear in the fake adb's calls.log.
const (
	bootPropCall = "-s " + uiSerial + " shell getprop sys.boot_completed"
	sdkPropCall  = "-s " + uiSerial + " shell getprop ro.build.version.sdk"
)

// Decision (plan m1n step 7 D3; A9): `sys.boot_completed` and `ro.build.version.sdk` are
// read once per (serial, transport id) and cached, so a warm device call issues no getprop
// after the first on its transport. The device preamble runs the boot_completed read, and
// a clear on the non-reader path runs the SDK read; two warm calls of each issue one
// getprop. Seen failing with no cache at all (a getprop per call).
func TestWarmCallIssuesNoGetprop(t *testing.T) {
	t.Run("the device preamble", func(t *testing.T) {
		quickAct(t)
		doc := tappable(tapLike("Like"))
		f := newUIFixture(t, withPersistentReader(t, nil, doc))
		mustComplete(t, f.tree(t, uiSerial))
		mustComplete(t, f.tree(t, uiSerial))
		if n := count(f.calls(t), bootPropCall); n != 1 {
			t.Errorf("%d boot_completed getprops for two warm calls, want one: the answer was not cached", n)
		}
	})
	t.Run("the SDK level", func(t *testing.T) {
		quickAct(t)
		// A reader that refuses every act sends the clear down the non-reader path, where the
		// SDK level is read (plan m1n step 5).
		doc := tappable(tapNode("android.widget.EditText", "name", "old", ""))
		f := newUIFixture(t, withReadOnlyReader(t, map[string]string{uiSerial + ".sdk.out": "34\n"}, doc))
		p := `{"device":"emulator-5554","text":"x","target":{"id":"name"},"clear":true}`
		mustInput(t, f.inputCall(t, p))
		mustInput(t, f.inputCall(t, p))
		if n := count(f.calls(t), sdkPropCall); n != 1 {
			t.Errorf("%d SDK getprops for two warm clears, want one: the answer was not cached", n)
		}
	})
}

// Decision (plan m1n step 7 D3): the cache is keyed by serial and transport id, so the
// same emulator serial on a new transport id — the serial reused by another AVD or image
// — is a cold read again and probes afresh. Seen failing with the cache keyed by serial
// alone (the stale answer is reused and no getprop is issued).
func TestTransportChangeDropsTheCache(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), bootPropCall); n != 1 {
		t.Fatalf("%d boot_completed getprops for the first call, want one", n)
	}
	// The same serial on a new transport id: the cached boot state is about the old one.
	devicetest.Write(t, f.dir, "devices.out", []byte(uiList(strings.Replace(phoneLine, "transport_id:1", "transport_id:2", 1))))
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), bootPropCall); n != 2 {
		t.Errorf("%d boot_completed getprops after the transport changed, want 2: the stale entry must not be reused", n)
	}
}

// Decision (plan m1n step 7 D3): an entry is dropped when a call fails DEVICE_NOT_FOUND,
// so a serial that vanished and came back is probed afresh — the belt-and-braces against
// an adb server restart reassigning the same transport id. Seen failing with the drop
// removed: the stale entry survives the not-found call and hides the fresh read.
func TestNotFoundDropsTheCache(t *testing.T) {
	quickAct(t)
	doc := tappable(tapLike("Like"))
	f := newUIFixture(t, withPersistentReader(t, nil, doc))
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), bootPropCall); n != 1 {
		t.Fatalf("%d boot_completed getprops for the first call, want one", n)
	}
	// The serial is not listed: the call fails DEVICE_NOT_FOUND and drops its entry.
	devicetest.Write(t, f.dir, "devices.out", []byte(listHead))
	mustFail(t, f.tree(t, uiSerial), ErrNotFound)
	// The serial is listed again on the same transport id: with the entry dropped, the next
	// call reads the boot state afresh.
	devicetest.Write(t, f.dir, "devices.out", []byte(uiList(phoneLine)))
	mustComplete(t, f.tree(t, uiSerial))
	if n := count(f.calls(t), bootPropCall); n != 2 {
		t.Errorf("%d boot_completed getprops after a not-found call, want 2: the entry was not dropped", n)
	}
}

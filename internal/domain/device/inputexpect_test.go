package device

import (
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
	"github.com/jtortosasen/droidyard/internal/domain/device/screen"
)

// expect on the coordinate actions (plan m3g step 5; ADR-0133 §6) over the fake reader, which
// serves the reads and takes the gestures, so the whole sequence (before read, gesture, reads
// after) runs against one reader lock.

func expectResult(t *testing.T, out capability.Outcome) ScreenInputResult {
	t.Helper()
	return mustDeliver(t, out)
}

// Decision (ADR-0133 §6): with expect, an agent's coordinate action reads the tree, delivers
// the input once and polls for the conditions as device.tapElement does. appears met is
// matched with the polls counted; a condition already true before the input is vacuous; an
// unmet one is a completed call, matched false, with the near candidates of the last read
// (an expectation that fails is information, not an error); the result names the last read's
// snapshot and the top window before and after. The reads and the gesture all take the
// reader's lock in turn, and the sequence completes: the before read has released it when
// the gesture takes it, and the reads after the gesture find it free. Seen failing on step
// 4's code: expect was an unknown parameter and no read ran.
func TestCoordinateActionExpect(t *testing.T) {
	quickAct(t)
	before := tappable(tapLike("Like"), tapLikes("Likes: 0"))
	after := tappable(tapLike("Like"), tapLikes("Likes: 1"))

	t.Run("appears met, on each of the three", func(t *testing.T) {
		for _, c := range []struct{ name, params, act string }{
			{"device.tap", `{` + devParam + `,"x":0.5,"y":0.5,"expect":[{"appears":{"text":"Likes: 1"}}]}`, "gesture 2 32768 32768 0 32768 32768 50"},
			{"device.swipe", `{` + devParam + `,"path":[{"x":0.5,"y":0.8,"t":0},{"x":0.5,"y":0.2,"t":200}],"expect":[{"appears":{"text":"Likes: 1"}}]}`, "gesture 2 32768 52428 0 32768 13107 200"},
			{"device.pressKey", `{` + devParam + `,"key":"back","expect":[{"appears":{"text":"Likes: 1"}}]}`, "press 4"},
		} {
			files, r := withPersistentReaderR(t, nil, before, after)
			f := newUIFixture(t, files)
			res := expectResult(t, f.agentCall(t, c.name, c.params))
			if res.Delivery != DeliveryReader || res.Expect == nil || !res.Expect.Matched || res.Expect.Polls < 1 || len(res.Expect.Vacuous) != 0 {
				t.Fatalf("%s: result %+v, expect %+v; want matched by the reader, nothing vacuous", c.name, res, res.Expect)
			}
			if res.Foreground == nil || res.Foreground.Before != tapPackage || res.Foreground.After != tapPackage || res.Snapshot == "" {
				t.Errorf("%s: foreground %+v, snapshot %q; want the package before and after and the last read's id", c.name, res.Foreground, res.Snapshot)
			}
			if got := r.Acts(); !slices.Equal(got, []string{c.act}) {
				t.Errorf("%s: the reader got %q, want exactly %q", c.name, got, c.act)
			}
			if r.Dumps() < 2 {
				t.Errorf("%s: %d dumps, want the before read and one after", c.name, r.Dumps())
			}
		}
	})

	t.Run("true before is vacuous", func(t *testing.T) {
		files, _ := withPersistentReaderR(t, nil, after, after)
		f := newUIFixture(t, files)
		res := expectResult(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5,"expect":[{"appears":{"text":"Likes: 1"}}]}`))
		if res.Expect == nil || !res.Expect.Matched || len(res.Expect.Vacuous) != 1 || res.Expect.Vacuous[0].Appears == nil {
			t.Errorf("expect %+v, want matched with the condition flagged vacuous", res.Expect)
		}
	})

	t.Run("unmet is a completed call with candidates", func(t *testing.T) {
		files, _ := withPersistentReaderR(t, nil, before)
		f := newUIFixture(t, files)
		res := expectResult(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5,"timeoutMs":60,"expect":[{"appears":{"id":"likes","text":"Likes: 1"}}]}`))
		if res.Expect == nil || res.Expect.Matched || len(res.Expect.Unmet) != 1 || len(res.Expect.Unmet[0].Candidates) == 0 {
			t.Fatalf("expect %+v, want unmet with candidates of the last read", res.Expect)
		}
		if !res.Delivered || res.Snapshot == "" {
			t.Errorf("result %+v, want delivered and the last read's snapshot", res)
		}
	})

	t.Run("gone needs two reads", func(t *testing.T) {
		gone := tappable(tapLike("Like"))
		files, _ := withPersistentReaderR(t, nil, before, gone)
		f := newUIFixture(t, files)
		res := expectResult(t, f.agentCall(t, "device.pressKey", `{`+devParam+`,"key":"back","expect":[{"gone":{"text":"Likes: 0"}}]}`))
		if res.Expect == nil || !res.Expect.Matched || res.Expect.Polls < 2 {
			t.Errorf("expect %+v, want matched only after two reads", res.Expect)
		}
	})

	t.Run("through an open view", func(t *testing.T) {
		files, r := withPersistentReaderR(t, nil, before, after)
		f := newUIFixture(t, files)
		src := &fakeScreen{}
		f.adb.screenLookup = func(serial string) screen.Source {
			if serial == uiSerial {
				return src
			}
			return nil
		}
		res := expectResult(t, f.agentCall(t, "device.pressKey", `{`+devParam+`,"key":"back","expect":[{"appears":{"text":"Likes: 1"}}]}`))
		if res.Delivery != DeliveryStream || res.Expect == nil || !res.Expect.Matched || len(src.got()) != 1 || len(r.Acts()) != 0 {
			t.Errorf("result %+v, expect %+v, source %v, reader acts %q; want the view to take the key and the reads to verify it", res, res.Expect, src.got(), r.Acts())
		}
	})

	// The before read failing sends nothing: DEVICE_UI_DUMP_FAILED, and the reader sees no
	// gesture (a reader that answers ping but not dumps, and no uiautomator to fall to).
	t.Run("the before read failing sends nothing", func(t *testing.T) {
		files, r := withPersistentReaderR(t, map[string]string{
			uiSerial + ".dumptty.out":  "",
			uiSerial + ".dumptty.exit": "1",
			uiSerial + ".dumptty.err":  "ERROR: could not get idle state.\n",
		}, devicetest.ReaderGone)
		f := newUIFixture(t, files)
		mustFail(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5,"expect":[{"appears":{"text":"Likes: 1"}}]}`), ErrUIDumpFailed)
		if got := r.Acts(); len(got) != 0 {
			t.Errorf("the reader got %q after a failed before read, want no gesture", got)
		}
		if got := f.inputCommands(t); len(got) != 0 {
			t.Errorf("`input` ran after a failed before read: %q", got)
		}
	})

	// Without expect there is no read at all: the reader sees the gesture and no dump.
	t.Run("no expect, no read", func(t *testing.T) {
		files, r := withPersistentReaderR(t, nil, before, after)
		f := newUIFixture(t, files)
		res := expectResult(t, f.agentCall(t, "device.tap", `{`+devParam+`,"x":0.5,"y":0.5}`))
		if res.Expect != nil || res.Foreground != nil || res.Snapshot != "" {
			t.Errorf("result %+v, want no expect, foreground or snapshot", res)
		}
		if r.Dumps() != 0 || len(r.FullRequests()) != 0 || len(r.Acts()) != 1 {
			t.Errorf("the reader saw %d dumps and acts %q, want no dump and the one gesture", r.Dumps(), r.Acts())
		}
		if c := f.calls(t); slices.ContainsFunc(c, func(s string) bool { return strings.Contains(s, "uiautomator dump") }) {
			t.Errorf("a uiautomator dump ran without expect: %q", c)
		}
	})

	// The bounds are tapElement's, refused before any command: more than five conditions,
	// timeoutMs outside 1..20000, a malformed selector.
	t.Run("bounds", func(t *testing.T) {
		f := newUIFixture(t, nil)
		cond := `{"appears":{"text":"x"}}`
		six := cond + strings.Repeat(","+cond, 5)
		before := len(f.calls(t))
		for _, params := range map[string]string{
			"six conditions": `{` + devParam + `,"key":"back","expect":[` + six + `]}`,
			"timeout 0":      `{` + devParam + `,"key":"back","timeoutMs":0,"expect":[` + cond + `]}`,
			"timeout 20001":  `{` + devParam + `,"key":"back","timeoutMs":20001,"expect":[` + cond + `]}`,
			"empty selector": `{` + devParam + `,"key":"back","expect":[{"appears":{}}]}`,
		} {
			mustFail(t, f.agentCall(t, "device.pressKey", params), capability.ErrInvalidParams)
		}
		if after := len(f.calls(t)); after != before {
			t.Errorf("a refused expect ran %d adb commands", after-before)
		}
	})
}

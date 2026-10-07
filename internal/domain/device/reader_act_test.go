package device

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// actingReader starts a fake reader the client can act through: the persistent reader
// pushes, starts, is forwarded and answers ping, so an acting request goes over the
// forward (ADR-0035 §1). The fake is returned so a test can script its answers and see the
// requests it received.
func actingReader(t *testing.T) (*uiFixture, *devicetest.FakeReader) {
	t.Helper()
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	return f, r
}

// act runs one acting request against the fixture's reader while holding the device's read
// lock, as an action does (ADR-0035 §1: the lock and the reader's own single acting thread
// are the two layers that keep one device's acts from overlapping). It is bounded so a
// wedged answer fails the test instead of hanging it.
func act(t *testing.T, f *uiFixture, fn func(ctx context.Context, c *readerClient) (readerAct, error)) (readerAct, error) {
	t.Helper()
	c := f.adb.reader(uiSerial)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer c.release()
	return fn(ctx, c)
}

// Decision (ADR-0035 §1; plan m1n step 4): an acting request is one line whose text
// argument is base64, so a space, a newline or a non-ASCII character in the text cannot
// split it into the wrong arguments; and submit is `keys` of a newline, not a verb of its
// own, which the reader's virtual keyboard maps to KEYCODE_ENTER. The expected lines are
// literals (base64Text("ñ á\n") is "w7Egw6EK", base64Text("\n") is "Cg=="), not the
// client's own encoder, so a change to the encoder is caught here rather than mirrored
// (plan m1n step 4, fix round N7). An empty set-text keeps its trailing space: the reader
// splits on spaces to three fields, and dropping it would leave only two and be refused
// (plan m1n step 4, fix round: `settext replace` of "" clears a field). Seen failing with
// the text sent raw: `keys a b` reads as three arguments and the reader refuses it.
func TestActingRequestsAreOneBase64Line(t *testing.T) {
	f, r := actingReader(t)
	text := "ñ á\n"
	cases := []struct {
		name string
		send func(ctx context.Context, c *readerClient) (readerAct, error)
		want string
	}{
		{"tap", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actTap(ctx, readerTarget(t, f.adb, uiSerial), c, 10, 20)
		}, "tap 10 20"},
		{"keys", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actKeys(ctx, readerTarget(t, f.adb, uiSerial), c, text)
		}, "keys w7Egw6EK"},
		{"settext replace", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actSetText(ctx, readerTarget(t, f.adb, uiSerial), c, true, text)
		}, "settext replace w7Egw6EK"},
		{"settext insert", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actSetText(ctx, readerTarget(t, f.adb, uiSerial), c, false, text)
		}, "settext insert w7Egw6EK"},
		{"settext replace empty", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actSetText(ctx, readerTarget(t, f.adb, uiSerial), c, true, "")
		}, "settext replace "},
		{"submit", func(ctx context.Context, c *readerClient) (readerAct, error) {
			return f.adb.actSubmit(ctx, readerTarget(t, f.adb, uiSerial), c)
		}, "keys Cg=="},
	}
	for _, tc := range cases {
		if _, err := act(t, f, tc.send); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		acts := r.Acts()
		if got := acts[len(acts)-1]; got != tc.want {
			t.Errorf("%s sent %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Decision (ADR-0035 §2; plan m1n step 4): an acting request answered `denied` is an error
// naming the uid fence, and it is the only outcome that is: the peer is not adbd, so the
// reader refused it before executing anything, and the caller may still use another
// delivery method (ADR-0035 §3). Seen failing with `denied` folded into a plain refusal or
// into the may-have-applied case, either of which hides the fence from the caller.
func TestDeniedActingRequestIsAnErrorNamingTheFence(t *testing.T) {
	f, r := actingReader(t)
	r.SetActAnswers("droidyard-reader: denied 10123")
	got, err := act(t, f, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return f.adb.actTap(ctx, readerTarget(t, f.adb, uiSerial), c, 10, 20)
	})
	if err == nil {
		t.Fatalf("a denied acting request returned %+v and no error, want an error naming the uid fence", got)
	}
	msg := err.Error()
	for _, want := range []string{"uid", "10123", "fence"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the denied error %q does not name the fence (missing %q)", msg, want)
		}
	}
}

// Decision (ADR-0035 §1, §3; plan m1n step 4): the answer's kind says whether anything was
// executed, which is what decides whether the caller may act again — `done` carried the
// field's text the reader read back, so the caller compares it instead of issuing a second
// read; `refused <reason>` executed nothing, and its reason says whether another method
// answers it (only a `settext` refusal does, §3) while a refusal the reader did not name is
// never another method's; `busy` is the device's single acting thread being out, its own
// outcome (the call fails DEVICE_NOT_READY, step 5), never a refusal; a `timeout <ms>`, an
// unknown reader line, or a `done` whose read-back is not base64 is unanswered,
// may-have-applied (plan m1n step 4, fix rounds B2, N3, N8). Seen failing with `busy`
// folded into `done` (the call would look successful), with a refusal turned into an error
// (the caller would stop), with `timeout` mapped to refused (the caller would deliver the
// text a second time), and with an undecodable `done` reported as an empty read-back.
func TestActingAnswerTaxonomy(t *testing.T) {
	f, r := actingReader(t)
	r.SetActAnswers(
		"droidyard-reader: done w7FhbmTDug==", // "ñandú"
		"droidyard-reader: done",
		"droidyard-reader: refused settext",
		"droidyard-reader: refused malformed",
		"droidyard-reader: busy",
		"droidyard-reader: timeout 2500",
		"droidyard-reader: timeout 2500 IllegalStateException",
		"droidyard-reader: unknown request",
		"droidyard-reader: done not-base64!",
	)
	send := func(ctx context.Context, c *readerClient) (readerAct, error) {
		return f.adb.actSetText(ctx, readerTarget(t, f.adb, uiSerial), c, true, "ñandú")
	}
	rows := []struct {
		name  string
		check func(got readerAct) string
	}{
		{"done with a read-back", func(got readerAct) string {
			if got.kind != readerActDone || got.readBack != "ñandú" {
				return fmt.Sprintf("done = %+v, want a done carrying the field's text %q", got, "ñandú")
			}
			return ""
		}},
		{"done with no read-back", func(got readerAct) string {
			if got.kind != readerActDone || got.readBack != "" {
				return fmt.Sprintf("done = %+v, want a done with an empty read-back", got)
			}
			return ""
		}},
		{"refused a set-text", func(got readerAct) string {
			if got.kind != readerActRefused || got.refusal != refusalSetText {
				return fmt.Sprintf("refused = %+v, want a set-text refusal, the one another method answers", got)
			}
			return ""
		}},
		{"refused malformed", func(got readerAct) string {
			if got.kind != readerActRefused || got.refusal != refusalMalformed {
				return fmt.Sprintf("refused = %+v, want a malformed refusal, which no other method answers", got)
			}
			return ""
		}},
		{"busy", func(got readerAct) string {
			if got.kind != readerActBusy {
				return fmt.Sprintf("busy = %+v, want readerActBusy", got)
			}
			return ""
		}},
		{"timeout", func(got readerAct) string {
			if got.kind != readerActUnanswered {
				return fmt.Sprintf("timeout = %+v, want readerActUnanswered (the request may have applied)", got)
			}
			return ""
		}},
		{"timeout naming the exception", func(got readerAct) string {
			if got.kind != readerActUnanswered {
				return fmt.Sprintf("timeout with an exception = %+v, want readerActUnanswered still", got)
			}
			return ""
		}},
		{"unknown reader line", func(got readerAct) string {
			if got.kind != readerActUnanswered {
				return fmt.Sprintf("unknown line = %+v, want readerActUnanswered", got)
			}
			return ""
		}},
		{"done with a read-back that is not base64", func(got readerAct) string {
			if got.kind != readerActUnanswered || got.reason == "" {
				return fmt.Sprintf("undecodable done = %+v, want readerActUnanswered with a reason", got)
			}
			return ""
		}},
	}
	for _, row := range rows {
		got, err := act(t, f, send)
		if err != nil {
			t.Fatalf("%s: the answer returned the error %v, want none", row.name, err)
		}
		if msg := row.check(got); msg != "" {
			t.Errorf("%s: %s", row.name, msg)
		}
	}
}

// Decision (ADR-0035 §1, §3; ADR-0020's 2026-10-06 line; plan m1n step 4): an acting
// request the reader took but never answered is reported as may-have-applied — not an
// error and not a refusal — so the caller completes the call with a warning and tries no
// other method, and exactly one request reaches the device. Seen failing with the
// unanswered case returned as an error, which lets the caller deliver the text twice.
func TestUnansweredActingRequestIsReportedAsMaybeApplied(t *testing.T) {
	oldBase, oldMargin := readerActBoundBase, readerActMargin
	readerActBoundBase, readerActMargin = 0, 300*time.Millisecond
	t.Cleanup(func() { readerActBoundBase, readerActMargin = oldBase, oldMargin })

	f, r := actingReader(t)
	r.SetActAnswers(devicetest.ReaderGone)
	got, err := act(t, f, func(ctx context.Context, c *readerClient) (readerAct, error) {
		return f.adb.actTap(ctx, readerTarget(t, f.adb, uiSerial), c, 10, 20)
	})
	if err != nil {
		t.Fatalf("an unanswered acting request returned the error %v, want the may-have-applied outcome", err)
	}
	if got.kind != readerActUnanswered {
		t.Errorf("kind %v, want readerActUnanswered: the reader took the request, so it may have applied", got.kind)
	}
	if n := len(r.Acts()); n != 1 {
		t.Errorf("the reader received %d acting requests, want exactly 1", n)
	}
}

// Decision (ADR-0035 §1; plan m1n step 4): the host's bound on an acting request sits above
// the device's, by a fixed margin, so the reader's own `timeout` answer reaches the client
// instead of the client giving up first and reporting a request it never bounded as one
// that may have applied; and the device's bound grows with a `keys` text's length, so a
// long `keys` is not cut off at a tap's bound, capped so a huge one cannot hold the acting
// thread. Both are one formula (readerActDeviceBound), the same the reader computes
// (DroidyardReader.actBoundMs). Seen failing with the host bound equal to the device bound
// for 500 characters (the client would cut off the reader's answer) and with the device
// bound flat for a `keys` (a long typing would be reported unanswered).
func TestActHostBoundExceedsDeviceBound(t *testing.T) {
	for _, chars := range []int{0, 100, 500} {
		dev, host := readerActDeviceBound(chars), readerActHostBound(chars)
		if host <= dev {
			t.Errorf("%d characters: the host bound %s is not above the device bound %s", chars, host, dev)
		}
	}
	if got := readerActDeviceBound(0); got != readerActBoundBase {
		t.Errorf("the device bound for 0 characters is %s, want the base %s", got, readerActBoundBase)
	}
	if readerActDeviceBound(500) <= readerActDeviceBound(0) {
		t.Errorf("the device bound does not grow with a keys text's length: %s for 500 characters, %s for 0",
			readerActDeviceBound(500), readerActDeviceBound(0))
	}
	if want := readerActBoundBase + 500*readerActBoundPerChar; readerActDeviceBound(500) != want {
		t.Errorf("the device bound for 500 characters is %s, want %s", readerActDeviceBound(500), want)
	}
	if got := readerActDeviceBound(100000); got != readerActBoundCap {
		t.Errorf("the device bound for a huge text is %s, want the cap %s", got, readerActBoundCap)
	}
}

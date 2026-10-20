package device

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// The wire of the reader's `events` answer (ADR-0111; plan m2w step 5), parsed and read over
// the forward. The reader's ring itself is Java and is exercised at level 5; these tests pin
// the contract the daemon holds the reader to.

// leak is planted in the device-supplied parts of the malformed answers below. An error cause
// is a fixed sentence: a test that finds it in a cause has found device text quoted.
const leak = "LEAK-MARKER-9f3a"

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// eventsHeader is a valid header: instance, ring [oldest, next), and the two clocks.
func eventsHeader(oldest, next uint64) string {
	return fmt.Sprintf("events 00112233aabbccdd %d %d 1760000000000 1759999000000\n", oldest, next)
}

// eventsLine is a valid entry line of a click in window 7.
func eventsLine(seq uint64) string {
	return fmt.Sprintf("%d\t1760000000100\tclick\t1\t7\t0\t%s\t%s\t\n", seq, b64("com.example"), b64("android.widget.Button"))
}

// Decision (ADR-0111 Decision 5, plan m2w step 5: the wire is pinned by literals, not by the
// encoder the fake reader shares with these tests): an answer of known bytes parses to known
// entries. The base64 below is written out, so a change of the field order, the alphabet or the
// separators on either side fails here instead of being mirrored. Seen failing with the
// parser reading package and class in the other order (the Package field then reads
// "android.widget.Toast").
func TestParseEventsReadsTheWireByLiteral(t *testing.T) {
	wire := "events 00112233aabbccdd 4 7 1760000000500 1759999000000\n" +
		"4\t1760000000100\ttoast\t1\t7\t0\tY29tLmV4YW1wbGU=\tYW5kcm9pZC53aWRnZXQuVG9hc3Q=\tU2F2ZWQgw7Eg4pyT\n" +
		"5\t1760000000200\twindowContent\t12\t-1\t3\tY29tLmV4YW1wbGU=\t\t\n" +
		"6\t1760000000300\tnotification\t1\t0\t0\t\tYW5kcm9pZC53aWRnZXQuQnV0dG9u\t\n"
	ans, err := parseEvents([]byte(wire), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Instance != "00112233aabbccdd" || ans.OldestSeq != 4 || ans.NextSeq != 7 || ans.NowWallMs != 1760000000500 || ans.StartedWallMs != 1759999000000 {
		t.Errorf("header = %+v", ans)
	}
	want := []uiEvent{
		{Seq: 4, TimeMs: 1760000000100, Type: "toast", Count: 1, Window: 7, Package: "com.example", Class: "android.widget.Toast", Text: "Saved ñ ✓"},
		{Seq: 5, TimeMs: 1760000000200, Type: "windowContent", Count: 12, Window: -1, Changes: 3, Package: "com.example"},
		{Seq: 6, TimeMs: 1760000000300, Type: "notification", Count: 1, Window: 0, Class: "android.widget.Button"},
	}
	if len(ans.Events) != len(want) {
		t.Fatalf("%d events, want %d", len(ans.Events), len(want))
	}
	for i, w := range want {
		if ans.Events[i] != w {
			t.Errorf("event %d = %+v, want %+v", i, ans.Events[i], w)
		}
	}
}

// spikeLine is one event of step 1's recording (the spike's listener output).
var spikeLine = regexp.MustCompile(`^\+(\d+)ms (TYPE_\w+) class=(\S+) pkg=(\S+) text=\[(.*?)\] desc=`)
var spikeWrapped = regexp.MustCompile(`^\+(\d+)ms (TYPE_\w+) class=(\S+) pkg=(\S+) text=\[`)

// spikeWire renders a recording into the wire form of an events answer: one entry per event,
// count 1, the names the reader gives the platform's types, and text for a toast only (a
// notification's text is not read, whatever the recording shows). The type table is written
// here on purpose, apart from the reader's source. It returns the wire and the type each
// entry should parse to.
func spikeWire(t *testing.T, recording string) (string, []string) {
	t.Helper()
	names := map[string]string{
		"TYPE_WINDOW_STATE_CHANGED":   "windowState",
		"TYPE_WINDOW_CONTENT_CHANGED": "windowContent",
		"TYPE_VIEW_CLICKED":           "click",
		"TYPE_VIEW_SCROLLED":          "scroll",
	}
	var entries strings.Builder
	var types []string
	for _, line := range strings.Split(recording, "\n") {
		m := spikeLine.FindStringSubmatch(line)
		text := ""
		if m == nil {
			// A line the recording wrapped: its text runs on, and is no toast's.
			m = spikeWrapped.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			m = append(m, "")
		}
		typ, ok := names[m[2]]
		if m[2] == "TYPE_NOTIFICATION_STATE_CHANGED" {
			typ, ok = "notification", true
			if strings.HasPrefix(m[3], "android.widget.Toast") {
				typ, text = "toast", m[5]
			}
		}
		if !ok {
			t.Fatalf("the recording has a type this test has no name for: %s", m[2])
		}
		seq := len(types) + 1
		fmt.Fprintf(&entries, "%d\t%s\t%s\t1\t3\t0\t%s\t%s\t%s\n", seq, m[1], typ, b64(m[4]), b64(m[3]), b64(text))
		types = append(types, typ)
	}
	return eventsHeader(1, uint64(len(types)+1)) + entries.String(), types
}

// Decision (plan m2w step 5, check "Unit"): step 1's two recordings of real emulators (API 31
// and 36), rendered into the wire form, parse: every event is an entry in order, the toast
// carries its text "Saved ñ ✓" and the notification none. Seen failing with the parser
// refusing the entries (the entry count then is 0).
func TestParseEventsOverTheSpikeRecordings(t *testing.T) {
	for _, name := range []string{"spike-events-api31.txt", "spike-events-api36.txt"} {
		wire, types := spikeWire(t, fixture(t, name))
		ans, err := parseEvents([]byte(wire), 0, 200)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(types) < 20 || len(ans.Events) != len(types) {
			t.Fatalf("%s: %d entries parsed of %d events", name, len(ans.Events), len(types))
		}
		toasts, notifications := 0, 0
		for i, ev := range ans.Events {
			if ev.Type != types[i] || ev.Seq != uint64(i+1) {
				t.Errorf("%s: entry %d = seq %d %s, want seq %d %s", name, i, ev.Seq, ev.Type, i+1, types[i])
			}
			switch ev.Type {
			case "toast":
				toasts++
				if ev.Text != "Saved ñ ✓" || ev.Class != "android.widget.Toast" {
					t.Errorf("%s: toast = %q class %q", name, ev.Text, ev.Class)
				}
			case "notification":
				notifications++
				if ev.Text != "" || ev.Class != "android.app.Notification" {
					t.Errorf("%s: notification = %q class %q", name, ev.Text, ev.Class)
				}
			default:
				if ev.Text != "" {
					t.Errorf("%s: a %s event carries text %q", name, ev.Type, ev.Text)
				}
			}
		}
		if toasts != 1 || notifications != 1 {
			t.Errorf("%s: %d toasts and %d notifications, want 1 and 1", name, toasts, notifications)
		}
	}
}

// Decision (ADR-0111 Decision 5, plan m2w step 5: "a malformed answer is a failed read with a
// fixed cause that quotes no device text"): each of these answers is eventsMalformed, and the
// cause is the same fixed sentence whatever the device wrote: LEAK-MARKER planted in the part
// that is wrong never reaches it. Seen failing, per case, with that one check removed from
// parseEvents (the answer then parses), and with the cause built from the offending field.
func TestParseEventsRejectsAMalformedAnswer(t *testing.T) {
	good := eventsLine(1)
	cases := []struct {
		name string
		wire string
		// after and max are the request the answer is for.
		after uint64
		max   int
	}{
		{"no final newline", eventsHeader(1, 2) + strings.TrimSuffix(good, "\n"), 0, 10},
		{"empty", "", 0, 10},
		{"header word missing", "events 00112233aabbccdd 1 1 1760000000000\n", 0, 10},
		{"header verb wrong", "event 00112233aabbccdd 1 1 1760000000000 1759999000000\n", 0, 10},
		{"instance too short", "events 0011 1 1 1760000000000 1759999000000\n", 0, 10},
		{"instance upper case", "events 00112233AABBCCDD 1 1 1760000000000 1759999000000\n", 0, 10},
		{"instance with the marker", "events " + leak + " 1 1 1760000000000 1759999000000\n", 0, 10},
		{"oldest zero", "events 00112233aabbccdd 0 1 1760000000000 1759999000000\n", 0, 10},
		{"next before oldest", "events 00112233aabbccdd 5 4 1760000000000 1759999000000\n", 0, 10},
		{"signed number", "events 00112233aabbccdd +1 2 1760000000000 1759999000000\n", 0, 10},
		{"number with the marker", "events 00112233aabbccdd 1 " + leak + " 1760000000000 1759999000000\n", 0, 10},
		{"number too long", "events 00112233aabbccdd 1 1234567890123456789 1760000000000 1759999000000\n", 0, 10},
		{"short entry", eventsHeader(1, 2) + "1\t1760000000100\tclick\t1\t7\t0\t\t\n", 0, 10},
		{"entry with a tenth field", eventsHeader(1, 2) + strings.TrimSuffix(good, "\n") + "\tx\n", 0, 10},
		{"blank line", eventsHeader(1, 2) + good + "\n", 0, 10},
		{"unknown type", eventsHeader(1, 2) + strings.Replace(good, "click", "click"+leak, 1), 0, 10},
		{"type of another case", eventsHeader(1, 2) + strings.Replace(good, "click", "Click", 1), 0, 10},
		{"count zero", eventsHeader(1, 2) + strings.Replace(good, "\tclick\t1\t", "\tclick\t0\t", 1), 0, 10},
		{"window past int32", eventsHeader(1, 2) + strings.Replace(good, "\t7\t0\t", "\t-2147483649\t0\t", 1), 0, 10},
		{"window not a number", eventsHeader(1, 2) + strings.Replace(good, "\t7\t0\t", "\t--3\t0\t", 1), 0, 10},
		{"changes signed", eventsHeader(1, 2) + strings.Replace(good, "\t7\t0\t", "\t7\t-1\t", 1), 0, 10},
		{"base64 invalid", eventsHeader(1, 2) + strings.Replace(good, b64("com.example"), leak+"!!", 1), 0, 10},
		{"base64 without padding", eventsHeader(1, 2) + strings.Replace(good, b64("com.example"), strings.TrimSuffix(b64("com.example"), "="), 1), 0, 10},
		{"base64 with a stray carriage return", eventsHeader(1, 2) + strings.Replace(good, b64("com.example"), "\r"+b64("com.example"), 1), 0, 10},
		{"base64 of invalid UTF-8", eventsHeader(1, 2) + strings.Replace(good, b64("com.example"), base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}), 1), 0, 10},
		{"package over its cap", eventsHeader(1, 2) + strings.Replace(good, b64("com.example"), b64(strings.Repeat("p", eventsNameCap+1)), 1), 0, 10},
		{"text over its cap", eventsHeader(1, 2) + fmt.Sprintf("1\t1760000000100\ttoast\t1\t7\t0\t\t\t%s\n", b64(strings.Repeat("t", eventsTextCap+1))), 0, 10},
		// The privacy rule is the daemon's too (ADR-0111 Decision 3): text on an event that
		// is not a toast, such as a notification's, fails the read and is not passed on.
		{"text on a notification", eventsHeader(1, 2) + fmt.Sprintf("1\t1760000000100\tnotification\t1\t7\t0\t\t\t%s\n", b64("Code 123456 "+leak)), 0, 10},
		{"text on a click", eventsHeader(1, 2) + fmt.Sprintf("1\t1760000000100\tclick\t1\t7\t0\t\t\t%s\n", b64(leak)), 0, 10},
		// The ring is consecutive; an answer that is not is the reader's bug or not the reader.
		{"first entry skips the cursor", eventsHeader(1, 4) + eventsLine(2) + eventsLine(3), 0, 10},
		{"entry at the cursor", eventsHeader(1, 4) + eventsLine(2) + eventsLine(3), 2, 10},
		{"entries out of order", eventsHeader(1, 4) + eventsLine(2) + eventsLine(1), 0, 10},
		{"gap between entries", eventsHeader(1, 5) + eventsLine(1) + eventsLine(3), 0, 10},
		{"entry past next", eventsHeader(1, 2) + eventsLine(1) + eventsLine(2), 0, 10},
		{"before the oldest", eventsHeader(3, 4) + eventsLine(2), 0, 10},
		{"more than max", eventsHeader(1, 4) + eventsLine(1) + eventsLine(2) + eventsLine(3), 0, 2},
		{"short of max with entries left", eventsHeader(1, 4) + eventsLine(1) + eventsLine(2), 0, 5},
	}
	for _, c := range cases {
		_, err := parseEvents([]byte(c.wire), c.after, c.max)
		if err == nil {
			t.Errorf("%s: parsed", c.name)
			continue
		}
		if kind, ok := eventsFailureKind(err); !ok || kind != eventsMalformed {
			t.Errorf("%s: error %v, want a malformed answer", c.name, err)
		}
		for _, bad := range []string{leak, "Code 123456", "com.example"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%s: the cause quotes device text %q: %v", c.name, bad, err)
			}
		}
	}
}

// Decision: a window id is any int32. The platform defines ids below -1 (ANY_WINDOW_ID -2,
// the picture-in-picture replacer -3), and the reader writes getWindowId() as it is; an
// entry the parser refused would fail every read until the ring moved past it (step 5's
// verifier). Seen failing with the parser accepting only -1 and non-negative ids.
func TestParseEventsAcceptsTheWindowIdsBelowMinusOne(t *testing.T) {
	wire := eventsHeader(1, 3) + strings.Replace(eventsLine(1), "\t7\t0\t", "\t-2\t0\t", 1) +
		strings.Replace(eventsLine(2), "\t7\t0\t", "\t-3\t0\t", 1)
	got, err := parseEvents([]byte(wire), 0, 10)
	if err != nil {
		t.Fatalf("parseEvents: %v", err)
	}
	if len(got.Events) != 2 || got.Events[0].Window != -2 || got.Events[1].Window != -3 {
		t.Fatalf("events %+v, want windows -2 and -3", got.Events)
	}
}

// Decision (ADR-0111 Decision 6: the cursor decides what is returned and gaps are the
// client's to see): the answer for a cursor older than the ring starts at the oldest entry
// kept, one for a cursor in the ring starts right after it, and a cursor at the end gets none.
// Header and entries stay consistent in all three. Seen failing with the parser expecting
// the entries to start at the cursor's next even when the ring starts later.
func TestParseEventsStartsAtTheCursorOrTheOldestKept(t *testing.T) {
	ring := eventsHeader(10, 14)
	cases := []struct {
		after uint64
		first uint64
		n     int
	}{
		{0, 10, 4},
		{5, 10, 4},
		{10, 11, 3},
		{12, 13, 1},
		{13, 0, 0},
		{99, 0, 0},
	}
	for _, c := range cases {
		wire := ring
		for seq := max(c.after+1, 10); seq < 14; seq++ {
			wire += eventsLine(seq)
		}
		ans, err := parseEvents([]byte(wire), c.after, 200)
		if err != nil {
			t.Errorf("after %d: %v", c.after, err)
			continue
		}
		if len(ans.Events) != c.n || (c.n > 0 && ans.Events[0].Seq != c.first) {
			t.Errorf("after %d: %d events from %v, want %d from %d", c.after, len(ans.Events), ans.Events, c.n, c.first)
		}
	}
}

// eventsOverTheForward starts the fake reader, brings the persistent reader up through the
// fake adb as a read does, and returns the forward's socket.
func eventsOverTheForward(t *testing.T) (*devicetest.FakeReader, *uiFixture, string) {
	t.Helper()
	f, r := actingReader(t)
	c := f.adb.reader(uiSerial)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if ok, why := f.adb.ensureReader(ctx, readerTarget(t, f.adb, uiSerial), c, time.Now().Add(8*time.Second)); !ok {
		t.Fatalf("the reader did not come up: %s", why)
	}
	return r, f, c.sockValue()
}

func askEvents(t *testing.T, sock string, after uint64, max int) (eventsAnswer, error) {
	t.Helper()
	return readEvents(context.Background(), sock, time.Now().Add(3*time.Second), after, max)
}

// Decision (ADR-0111 Decision 4: `events <afterSeq> <max>`, one request per connection): the
// request line is exactly that, the answer is parsed from the cursor, and a ring that
// overflowed shows as an oldestSeq past the cursor (the caller's `overflow`, step 6). Seen
// failing with the request written as `events <max> <afterSeq>` (the fake then answers from
// the wrong place and the request line check fails).
func TestReadEventsAsksForTheCursorAndMax(t *testing.T) {
	r, _, sock := eventsOverTheForward(t)
	r.AddEvents(
		devicetest.FakeEvent{TimeMs: 1760000000100, Type: "click", Window: 7, Package: "com.example", Class: "android.widget.Button"},
		devicetest.FakeEvent{TimeMs: 1760000000200, Type: "toast", Window: 7, Package: "com.example", Class: "android.widget.Toast", Text: "Saved ñ ✓"},
		devicetest.FakeEvent{TimeMs: 1760000000300, Type: "windowContent", Count: 9, Window: 7, Changes: 2, Package: "com.example"},
	)
	ans, err := askEvents(t, sock, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.EventsRequests(); len(got) != 1 || got[0] != "events 1 1" {
		t.Errorf("requests = %q, want [events 1 1]", got)
	}
	if len(ans.Events) != 1 || ans.Events[0].Seq != 2 || ans.Events[0].Text != "Saved ñ ✓" || ans.NextSeq != 4 || ans.OldestSeq != 1 {
		t.Errorf("answer = %+v", ans)
	}
	ans, err = askEvents(t, sock, 0, 200)
	if err != nil || len(ans.Events) != 3 || ans.Events[2].Count != 9 || ans.Events[2].Changes != 2 {
		t.Errorf("whole ring = %+v, %v", ans, err)
	}
	r.DropEventsBefore(3)
	ans, err = askEvents(t, sock, 1, 200)
	if err != nil {
		t.Fatal(err)
	}
	if ans.OldestSeq != 3 || len(ans.Events) != 1 || ans.Events[0].Seq != 3 {
		t.Errorf("after the ring dropped seq 1 and 2: %+v, want oldest 3 and one entry, seq 3", ans)
	}
	r.SetEventsInstance("ffffffffffffffff")
	ans, err = askEvents(t, sock, 3, 5)
	if err != nil || ans.Instance != "ffffffffffffffff" || len(ans.Events) != 0 {
		t.Errorf("restarted reader = %+v, %v", ans, err)
	}
}

// Decision (ADR-0111 Decision 4 and ADR-0102 §2: the reader's own lines are told apart, and
// none is quoted): each way the reader or the forward can fail is a kind the caller can act
// on, with a fixed cause that names none of the device's text. A reader's line with the
// marker in it is the strictest case. Seen failing with the line's content in the cause.
func TestReadEventsTellsTheFailuresApart(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		delay  time.Duration
		want   eventsFailure
	}{
		{"fenced", "droidyard-reader: denied 10042" + leak, 0, eventsFenced},
		{"a reader that predates events", "droidyard-reader: unknown request", 0, eventsUnsupported},
		{"arguments refused", "droidyard-reader: bad events arguments", 0, eventsUnsupported},
		{"another line of its own", "droidyard-reader: " + leak, 0, eventsOther},
		{"connection closed", devicetest.ReaderGone, 0, eventsGone},
		{"not an answer", "<hierarchy>" + leak + "</hierarchy>", 0, eventsMalformed},
		{"no answer in time", eventsHeader(1, 1), 600 * time.Millisecond, eventsTimedOut},
	}
	for _, c := range cases {
		r, _, sock := eventsOverTheForward(t)
		r.SetEventsAnswer(c.answer)
		r.SetDelay(c.delay)
		_, err := readEvents(context.Background(), sock, time.Now().Add(300*time.Millisecond), 0, 10)
		kind, ok := eventsFailureKind(err)
		if !ok || kind != c.want {
			t.Errorf("%s: error %v (kind %v), want kind %v", c.name, err, kind, c.want)
			continue
		}
		if strings.Contains(err.Error(), leak) || strings.Contains(err.Error(), "10042") {
			t.Errorf("%s: the cause quotes device text: %v", c.name, err)
		}
	}
	// The cap is its own failure, named: an answer is read to 1 MiB and no further (200
	// entries of at most 512 characters of text fit well under it), and one past it says so.
	// Seen failing with the read bounded at 1 GiB (the oversized answer then fails as an
	// answer cut before its last line, not as one past the cap).
	r, _, sock := eventsOverTheForward(t)
	r.SetEventsAnswer(eventsHeader(1, 1) + strings.Repeat("x", eventsReadCap+1))
	_, err := askEvents(t, sock, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Errorf("an answer past the cap: %v, want a cause naming the 1 MiB cap", err)
	}
}

// Decision (the request's bounds, ADR-0111 Decision 4: max from 1 to 200; ADR-0035's habit of
// refusing before sending): a max outside the range is never sent. Seen failing with the check
// removed (the fake then receives the request).
func TestReadEventsRefusesMaxOutsideTheRangeBeforeSending(t *testing.T) {
	r, _, sock := eventsOverTheForward(t)
	for _, max := range []int{0, -1, 201} {
		if _, err := askEvents(t, sock, 0, max); err == nil {
			t.Errorf("max %d was accepted", max)
		}
	}
	if got := r.EventsRequests(); len(got) != 0 {
		t.Errorf("the reader received %q for a max outside 1 to 200", got)
	}
}

// Decision (the forward is the reader's only door, ADR-0102): a forward that is gone is
// eventsUnreachable and no request is sent, and an empty socket name is the same. Seen
// failing with the dial error quoted (it names the path).
func TestReadEventsOnAGoneForward(t *testing.T) {
	for _, sock := range []string{"", t.TempDir() + "/gone.sock"} {
		_, err := readEvents(context.Background(), sock, time.Now().Add(time.Second), 0, 10)
		kind, ok := eventsFailureKind(err)
		if !ok || kind != eventsUnreachable {
			t.Errorf("socket %q: error %v, want eventsUnreachable", sock, err)
		} else if sock != "" && strings.Contains(err.Error(), sock) {
			t.Errorf("the cause names the socket path: %v", err)
		}
	}
}

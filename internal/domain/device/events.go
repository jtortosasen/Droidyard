package device

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The reader's `events` request (ADR-0111): the screen's accessibility events kept in a ring
// on the device, read since a cursor. This file is the client of the wire: the request, the
// bounded read of the answer and its strict parse. No capability uses it yet (plan m2w step 6).
//
// The answer is device text, and a malformed one is a failed read whose cause is one of the
// fixed sentences below: nothing the device sent is quoted in an error.

const (
	// eventsReadCap is the most bytes of an events answer the daemon reads: 200 entries, each
	// with a toast text of at most 512 characters and two names of at most 128, are well under it.
	eventsReadCap = 1 << 20
	// eventsMaxLimit is the most entries one request may ask for (ADR-0111 Decision 4).
	eventsMaxLimit = 200
	// eventsTextCap and eventsNameCap are the reader's caps on a toast's text and on a package
	// or class name, in UTF-16 characters, which a rune count never exceeds.
	eventsTextCap = 512
	eventsNameCap = 128
	// eventsFieldCount is the tab-separated fields of one entry line.
	eventsFieldCount = 9
)

// eventTypes are the types an entry may carry (ADR-0111 Decision 5). The reader sends no other.
var eventTypes = map[string]bool{
	"windowState": true, "windowContent": true, "windows": true, "textChanged": true,
	"focus": true, "scroll": true, "click": true, "selected": true,
	"toast": true, "notification": true, "announcement": true,
}

// uiEvent is one entry of the reader's ring, as it answered it. Package, Class and Text are
// device text, untrusted; Text is set for a toast only.
type uiEvent struct {
	Seq uint64
	// TimeMs is the device wall clock when the event (the latest folded into the entry)
	// happened, in milliseconds since the epoch.
	TimeMs int64
	Type   string
	// Count is how many raw events the entry folds (at least 1).
	Count int
	// Window is the accessibility window id (-1 when the event names none).
	Window int
	// Changes is the event's content change types as the platform's bit mask.
	Changes int
	Package string
	Class   string
	Text    string
}

// eventsAnswer is a parsed `events` answer.
type eventsAnswer struct {
	// Instance is the reader's random id for this start; a different one is a restarted reader.
	Instance string
	// OldestSeq is the first sequence number still in the ring and NextSeq the one the next
	// event gets; they are equal when the ring is empty.
	OldestSeq, NextSeq uint64
	// NowWallMs and StartedWallMs are the device wall clock when the answer was made and when
	// the reader started, in milliseconds since the epoch.
	NowWallMs, StartedWallMs int64
	// Events are the entries after the requested cursor, oldest first, consecutive.
	Events []uiEvent
}

// eventsFailure is why an events read failed, so a caller decides whether to recover.
type eventsFailure int

const (
	// eventsUnreachable: the forward did not take a connection, or the request.
	eventsUnreachable eventsFailure = iota
	// eventsTimedOut: the reader did not answer within the deadline.
	eventsTimedOut
	// eventsGone: the connection ended with no answer.
	eventsGone
	// eventsFenced: the reader's uid fence refused the request (ADR-0102 §2).
	eventsFenced
	// eventsUnsupported: the reader does not know the request, or refused its arguments.
	eventsUnsupported
	// eventsMalformed: the answer is not a valid events answer.
	eventsMalformed
	// eventsOther: the reader answered a line of its own that is none of the above.
	eventsOther
)

// eventsError is the failure of one events read. Cause is a fixed sentence: it quotes nothing
// the device sent.
type eventsError struct {
	Kind  eventsFailure
	Cause string
}

func (e *eventsError) Error() string { return e.Cause }

func failEvents(kind eventsFailure, cause string) error { return &eventsError{kind, cause} }

// eventsFailureKind is the kind of an error from readEvents or parseEvents; ok is false for
// any other error.
func eventsFailureKind(err error) (kind eventsFailure, ok bool) {
	var e *eventsError
	if errors.As(err, &e) {
		return e.Kind, true
	}
	return 0, false
}

// eventsRequest is the request line: `events <afterSeq> <max>`.
func eventsRequest(afterSeq uint64, max int) string {
	return fmt.Sprintf("events %d %d\n", afterSeq, max)
}

// readEvents asks the reader behind the forward socket sock for the events after afterSeq,
// at most max (1 to 200), and parses the answer. It takes no lock and starts nothing: a poll
// must not queue behind a read or an action (ADR-0111 Decision 8), and recovering a dead
// reader is the caller's. deadline bounds the whole exchange.
func readEvents(ctx context.Context, sock string, deadline time.Time, afterSeq uint64, max int) (eventsAnswer, error) {
	if max < 1 || max > eventsMaxLimit {
		return eventsAnswer{}, fmt.Errorf("events: max %d is outside 1 to %d", max, eventsMaxLimit)
	}
	if sock == "" {
		return eventsAnswer{}, failEvents(eventsUnreachable, "the reader's forward is gone")
	}
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return eventsAnswer{}, failEvents(eventsTimedOut, "the reader's events answer did not arrive in time")
	}
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return eventsAnswer{}, failEvents(eventsUnreachable, "the reader's forward did not answer")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(deadline)
	if _, err := io.WriteString(conn, eventsRequest(afterSeq, max)); err != nil {
		return eventsAnswer{}, failEvents(eventsUnreachable, "the reader's forward did not take the request")
	}
	resp, truncated, readErr := readLimited(conn, eventsReadCap)
	if truncated {
		return eventsAnswer{}, failEvents(eventsMalformed, fmt.Sprintf("the reader's events answer is larger than the %d MiB the daemon reads", eventsReadCap>>20))
	}
	if readErr != nil {
		if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
			return eventsAnswer{}, failEvents(eventsTimedOut, "the reader's events answer did not arrive in time")
		}
		if len(resp) == 0 {
			return eventsAnswer{}, failEvents(eventsGone, "reading the reader's events answer failed")
		}
	}
	if len(resp) == 0 {
		return eventsAnswer{}, failEvents(eventsGone, "the reader's connection ended before it answered")
	}
	if bytes.HasPrefix(resp, []byte(readerPrefix)) {
		return eventsAnswer{}, classifyEventsLine(string(resp))
	}
	return parseEvents(resp, afterSeq, max)
}

// classifyEventsLine turns a reader line answered to an events request into its failure.
// Neither the line nor its uid is put in the cause: the line is device text.
func classifyEventsLine(line string) error {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), readerPrefix))
	switch {
	case strings.HasPrefix(rest, "denied"):
		return failEvents(eventsFenced, "the reader refused the events request: the connecting peer is not adbd's")
	case rest == "unknown request" || rest == "bad events arguments":
		return failEvents(eventsUnsupported, "the reader does not take this events request")
	}
	return failEvents(eventsOther, "the reader answered the events request with a line of its own")
}

// Fixed causes of a malformed answer. They name the part that was wrong, never its content.
const (
	eventsBadHeader  = "the reader's events answer has a malformed header"
	eventsBadEntry   = "the reader's events answer has a malformed entry"
	eventsBadCursor  = "the reader's events answer is inconsistent with the request"
	eventsNoNewline  = "the reader's events answer is cut before its last line ends"
	eventsBadPrivacy = "the reader's events answer carries text on an event that never does"
)

// parseEvents reads the answer to `events <afterSeq> <max>` strictly (ADR-0111): a header
// `events <instance> <oldestSeq> <nextSeq> <nowWallMs> <startedWallMs>` and then one line per
// entry of nine tab-separated fields `seq time type count window changes package class text`,
// the last three strictly base64. A line that is not exactly so, entries that are not
// consecutive from the cursor, more than max, or text on an event that is not a toast, fail
// as eventsMalformed with a fixed cause.
func parseEvents(resp []byte, afterSeq uint64, max int) (eventsAnswer, error) {
	bad := func(cause string) (eventsAnswer, error) {
		return eventsAnswer{}, failEvents(eventsMalformed, cause)
	}
	if len(resp) == 0 || resp[len(resp)-1] != '\n' {
		return bad(eventsNoNewline)
	}
	lines := strings.Split(string(resp[:len(resp)-1]), "\n")
	h := strings.Split(lines[0], " ")
	if len(h) != 6 || h[0] != "events" || !isHex16(h[1]) {
		return bad(eventsBadHeader)
	}
	var ans eventsAnswer
	ans.Instance = h[1]
	oldest, ok1 := parseUint(h[2])
	next, ok2 := parseUint(h[3])
	now, ok3 := parseUint(h[4])
	started, ok4 := parseUint(h[5])
	if !ok1 || !ok2 || !ok3 || !ok4 || oldest < 1 || next < oldest {
		return bad(eventsBadHeader)
	}
	ans.OldestSeq, ans.NextSeq = oldest, next
	ans.NowWallMs, ans.StartedWallMs = int64(now), int64(started)

	entries := lines[1:]
	if len(entries) > max {
		return bad(eventsBadCursor)
	}
	// The ring is consecutive, so the answer is too: from the cursor's next, or the oldest
	// kept, and to the end of the ring unless max cut it short.
	want := afterSeq + 1
	if want < oldest {
		want = oldest
	}
	for _, line := range entries {
		ev, ok := parseEventLine(line)
		if !ok {
			return bad(eventsBadEntry)
		}
		if ev.Seq != want || ev.Seq >= next {
			return bad(eventsBadCursor)
		}
		if ev.Text != "" && ev.Type != "toast" {
			return bad(eventsBadPrivacy)
		}
		ans.Events = append(ans.Events, ev)
		want++
	}
	if len(entries) < max && want < next {
		return bad(eventsBadCursor)
	}
	return ans, nil
}

// parseEventLine reads one entry line, or reports false.
func parseEventLine(line string) (uiEvent, bool) {
	f := strings.Split(line, "\t")
	if len(f) != eventsFieldCount {
		return uiEvent{}, false
	}
	seq, ok1 := parseUint(f[0])
	timeMs, ok2 := parseUint(f[1])
	count, ok3 := parseUint(f[3])
	changes, ok4 := parseUint(f[5])
	window, ok5 := parseWindow(f[4])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || seq < 1 || count < 1 || count > 1<<31-1 || changes > 1<<31-1 || !eventTypes[f[2]] {
		return uiEvent{}, false
	}
	pkg, ok1 := decodeEventString(f[6], eventsNameCap)
	cls, ok2 := decodeEventString(f[7], eventsNameCap)
	text, ok3 := decodeEventString(f[8], eventsTextCap)
	if !ok1 || !ok2 || !ok3 {
		return uiEvent{}, false
	}
	return uiEvent{
		Seq: seq, TimeMs: int64(timeMs), Type: f[2], Count: int(count), Window: window,
		Changes: int(changes), Package: pkg, Class: cls, Text: text,
	}, true
}

// parseUint reads plain decimal digits (at most 18, so no overflow), and nothing else: no
// sign, no space, no empty string. strconv's own errors quote the input, so none is returned.
func parseUint(s string) (uint64, bool) {
	if len(s) < 1 || len(s) > 18 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// parseWindow reads a window id: any int32, since the platform has ids below -1
// (ANY_WINDOW_ID -2, the picture-in-picture replacer -3) and the reader writes
// getWindowId() as it is. Rejecting one would fail every read until the ring moved past it.
func parseWindow(s string) (int, bool) {
	neg := strings.HasPrefix(s, "-")
	n, ok := parseUint(strings.TrimPrefix(s, "-"))
	if !ok || (!neg && n > 1<<31-1) || (neg && n > 1<<31) {
		return 0, false
	}
	if neg {
		return -int(n), true
	}
	return int(n), true
}

func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// decodeEventString decodes a strictly canonical base64 field: the standard alphabet with
// padding, which re-encodes to itself (so no stray newline, no extra padding bits), of valid
// UTF-8 and at most limit characters. An empty field is the empty string.
func decodeEventString(field string, limit int) (string, bool) {
	if field == "" {
		return "", true
	}
	raw, err := base64.StdEncoding.DecodeString(field)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != field {
		return "", false
	}
	if !utf8.Valid(raw) || utf8.RuneCount(raw) > limit {
		return "", false
	}
	return string(raw), true
}

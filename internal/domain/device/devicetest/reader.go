package devicetest

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ReaderGone is a scripted dump response that closes the connection without writing a
// document, as the forward to a reader that has died does.
const ReaderGone = "\x00gone"

// readerPrefix marks the fake reader's own lines, as DroidyardReader's PREFIX does.
const readerPrefix = "droidyard-reader:"

// ReaderLog counts how many fake readers are answering at once. Sharing one log between
// the readers of two devices lets a test see that one device's reads never overlap and
// two devices' may.
type ReaderLog struct {
	mu    sync.Mutex
	depth int
	max   int
}

func (l *ReaderLog) enter() {
	l.mu.Lock()
	l.depth++
	if l.depth > l.max {
		l.max = l.depth
	}
	l.mu.Unlock()
}

func (l *ReaderLog) leave() {
	l.mu.Lock()
	l.depth--
	l.mu.Unlock()
}

// Max is the most readers that were answering at once.
func (l *ReaderLog) Max() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.max
}

// FakeReader is an in-process stand-in for the persistent UI reader (ADR-0030, D2): a
// TCP listener the fake adb's forward points at. Each connection carries one request
// (`ping`, `dump` and its `idle` and `full` forms, `events` (ADR-0111), `quit`, and the acting
// verbs `tap`, `keys` and `settext` of ADR-0035 §1); each dump takes the next scripted document, ReaderGone closes
// the connection without writing, and the last document repeats. Each connection is
// handled in its own goroutine, so a client that sends two at once is seen to.
type FakeReader struct {
	// Port is what the fake adb's `forward` must print (the scenario's <serial>.forward).
	Port string

	ln    net.Listener
	delay time.Duration
	log   *ReaderLog

	mu    sync.Mutex
	docs  []string
	next  int
	quits int
	pings int
	dumps int
	// fullDocs scripts the answers to `dump full` requests (ADR-0110), fullNext the next one;
	// fulls is the words of every such request, after `dump `, in order.
	fullDocs []string
	fullNext int
	fulls    []string
	// The scripted ring behind `events` (ADR-0111): its instance, the sequence number of
	// ring[0], the entries, and the verbatim answer that overrides them when set.
	eventsInstance string
	eventsOldest   uint64
	ring           []FakeEvent
	eventsRaw      string
	eventsGone     int
	eventsSkip     int // answered before eventsLater arms eventsGone
	eventsLater    int
	eventsHold     bool
	// eventsFold folds a new event into the newest entry the way the reader does, unless an
	// answer has returned (sealed) it; sealed is keyed by sequence number.
	eventsFold   bool
	sealed       map[uint64]bool
	eventsRawSet bool
	eventsReqs   []string
	// mutePings is how many forthcoming ping requests are accepted but never answered,
	// as a reader that is alive but wedged does (plan m1f step 7).
	mutePings int
	// dumpStalls is how many forthcoming dump requests are accepted and held past the
	// caller's warm-read timeout, then dropped, as a reader whose dump is wedged does
	// (plan m1f step 7).
	dumpStalls int
	starts     []time.Time
	// acts is every acting request (tap, keys, settext) the reader received, in order,
	// with its newline only: a test asserts the client's encoding (ADR-0035 §1).
	acts []string
	// actStarts is when the reader received each acting request, in order, so a test can
	// measure the delay between an action and the read that follows it (D2: no fixed
	// settle means the first read starts almost at once).
	actStarts []time.Time
	// actAnswers scripts the answer to forthcoming acting requests, in order, the last
	// one repeating; see SetActAnswers.
	actAnswers []string
	actNext    int
	once       sync.Once
	wg         sync.WaitGroup
}

// StartReader starts a fake reader answering with docs in order; an empty docs answers
// a small document to every dump.
func StartReader(t testing.TB, docs ...string) *FakeReader {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if len(docs) == 0 {
		docs = []string{`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"><node class="android.widget.TextView" package="com.example" text="fake" bounds="[0,0][10,10]"></node></hierarchy>`}
	}
	r := &FakeReader{Port: port, ln: ln, docs: docs, eventsInstance: "0123456789abcdef", eventsOldest: 1}
	go r.accept()
	t.Cleanup(r.Close)
	return r
}

// SetDelay makes every request take d, so a concurrent one is visibly in flight.
func (r *FakeReader) SetDelay(d time.Duration) {
	r.mu.Lock()
	r.delay = d
	r.mu.Unlock()
}

// SetDocs re-scripts the dump responses, as a screen change or a reader that stops
// answering would: the next dump takes docs[0] again.
func (r *FakeReader) SetDocs(docs ...string) {
	if len(docs) == 0 {
		return
	}
	r.mu.Lock()
	r.docs, r.next = docs, 0
	r.mu.Unlock()
}

// SetFullDocs scripts the answers to `dump full [invisible]` requests (ADR-0110), in order,
// the last repeating; ReaderGone closes the connection without a document, and a reader's own
// line (`droidyard-reader: busy 2500 com.example`) is answered as it is. Until it is called a
// full dump is answered `droidyard-reader: bad dump arguments`, as a reader that predates the
// words does. A full dump is not counted by Dumps.
func (r *FakeReader) SetFullDocs(docs ...string) {
	r.mu.Lock()
	r.fullDocs, r.fullNext = docs, 0
	r.mu.Unlock()
}

// FullRequests is the words of every `dump full` request the reader received, after `dump `
// and without the newline (`full`, `full invisible`), in order, so a test asserts what the
// client asked for.
func (r *FakeReader) FullRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.fulls...)
}

// SetMutePings makes the next n pings accepted but never answered (each is held past the
// caller's 1 s check), as a reader that is alive but wedged does (plan m1f step 7).
func (r *FakeReader) SetMutePings(n int) {
	r.mu.Lock()
	r.mutePings = n
	r.mu.Unlock()
}

// SetDumpStall makes the next n dumps accepted and held past the caller's warm-read
// timeout, then dropped, as a wedged reader's dump is (plan m1f step 7). The reader
// stays alive, so a ping can still be answered or muted.
func (r *FakeReader) SetDumpStall(n int) {
	r.mu.Lock()
	r.dumpStalls = n
	r.mu.Unlock()
}

func (r *FakeReader) delayValue() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delay
}

// SetLog makes the reader count its connections in l, so two readers sharing one count
// how many answered at once.
func (r *FakeReader) SetLog(l *ReaderLog) {
	r.mu.Lock()
	r.log = l
	r.mu.Unlock()
}

func (r *FakeReader) logValue() *ReaderLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.log
}

// DumpStarts is when the reader received each dump request, in order, so a test can
// check the poll interval of an act-then-verify call (plan m1f step 6).
func (r *FakeReader) DumpStarts() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.starts...)
}

// SetActAnswers scripts the answer to forthcoming acting requests (tap, keys, settext),
// in order; the last one repeats, and an empty script answers `droidyard-reader: done` to
// every acting request. The answers are the reader's own lines (ADR-0035 §1):
// "droidyard-reader: denied <uid>", "... refused <reason>", "... busy", "... done
// <base64>" or "... timeout <ms>". ReaderGone holds the request unanswered, as a reader
// whose acting thread never answers does (ADR-0035 §3's may-have-applied case).
func (r *FakeReader) SetActAnswers(answers ...string) {
	r.mu.Lock()
	r.actAnswers, r.actNext = answers, 0
	r.mu.Unlock()
}

// Acts is every acting request the reader received, in order, with its newline alone
// removed, so a test asserts the client's exact encoding (ADR-0035 §1).
func (r *FakeReader) Acts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.acts...)
}

// ActStarts is when the reader received each acting request, in order, so a test can
// measure the delay between an action and the read that follows it (D2).
func (r *FakeReader) ActStarts() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.actStarts...)
}

// Quits, Pings and Dumps are the requests the reader answered.
func (r *FakeReader) Quits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quits
}

func (r *FakeReader) Pings() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pings
}

func (r *FakeReader) Dumps() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dumps
}

// Close stops the listener.
func (r *FakeReader) Close() {
	r.once.Do(func() { r.ln.Close() })
	r.wg.Wait()
}

func (r *FakeReader) accept() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(conn)
		}()
	}
}

func (r *FakeReader) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if l := r.logValue(); l != nil {
		l.enter()
		defer l.leave()
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	if d := r.delayValue(); d > 0 {
		time.Sleep(d)
	}
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "ping":
		r.mu.Lock()
		if r.mutePings > 0 {
			r.mutePings--
			r.mu.Unlock()
			// Accept the request and hold it until the caller gives up and closes:
			// a reader that is alive but does not answer.
			holdUntilClose(conn)
			return
		}
		r.pings++
		r.mu.Unlock()
		io.WriteString(conn, "ok 1234")
	case trimmed == "dump" || strings.HasPrefix(trimmed, "dump "):
		// `dump` or `dump idle <quietMs> <maxMs>`, the settle of D2.
		full := trimmed == "dump full" || strings.HasPrefix(trimmed, "dump full ")
		r.mu.Lock()
		stall := r.dumpStalls > 0
		if stall {
			r.dumpStalls--
		}
		if full {
			r.fulls = append(r.fulls, strings.TrimPrefix(trimmed, "dump "))
		} else {
			r.dumps++
		}
		r.starts = append(r.starts, time.Now())
		r.mu.Unlock()
		if stall {
			// Hold the dump until the caller's warm-read timeout closes the
			// connection: a wedged reader (plan m1f step 7).
			holdUntilClose(conn)
			return
		}
		var doc string
		if full {
			doc = r.nextFullDoc()
		} else {
			doc = r.nextDoc()
		}
		if doc == ReaderGone {
			return
		}
		io.WriteString(conn, doc)
	case trimmed == "events" || strings.HasPrefix(trimmed, "events "):
		r.handleEvents(conn, trimmed)
	case trimmed == "quit":
		r.mu.Lock()
		r.quits++
		r.mu.Unlock()
	case isActingRequest(trimmed):
		r.handleAct(conn, line)
	default:
		fmt.Fprint(conn, readerPrefix+" unknown request")
	}
}

// FakeEvent is one scripted entry of the fake reader's ring, as the real reader keeps it
// (ADR-0111). Count 0 is 1. Text is sent for any type, so a test can script a reader that
// breaks the toast-only rule.
type FakeEvent struct {
	TimeMs  int64
	Type    string
	Count   int
	Window  int
	Changes int
	Package string
	Class   string
	Text    string
}

// SetEventsInstance sets the 16 hex digits the fake reader answers as its `instance`, as a
// restarted reader has a new one.
func (r *FakeReader) SetEventsInstance(instance string) {
	r.mu.Lock()
	r.eventsInstance = instance
	r.mu.Unlock()
}

// AddEvents appends entries to the fake's ring; each takes the next sequence number, from 1.
//
// With SetEventsFold, an event folds into the newest entry as the reader's ring does
// (DroidyardReader's EventRing.record): a content, scroll or text event of the same type,
// package and window within 250 ms of the entry's time, while no answer has returned that entry.
func (r *FakeReader) AddEvents(events ...FakeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range events {
		if n := len(r.ring); r.eventsFold && n > 0 {
			last := &r.ring[n-1]
			if foldable(e.Type) && !r.sealed[r.eventsOldest+uint64(n)-1] && last.Type == e.Type &&
				last.Package == e.Package && last.Window == e.Window && e.TimeMs-last.TimeMs <= 250 {
				if last.Count == 0 {
					last.Count = 1
				}
				last.Count++
				last.TimeMs = max(last.TimeMs, e.TimeMs)
				last.Changes |= e.Changes
				continue
			}
		}
		r.ring = append(r.ring, e)
	}
}

func foldable(typ string) bool {
	return typ == "windowContent" || typ == "scroll" || typ == "textChanged"
}

// SetEventsFold makes AddEvents fold as the reader's ring does (off by default, so a test
// that counts the events it added is not surprised).
func (r *FakeReader) SetEventsFold(on bool) {
	r.mu.Lock()
	r.eventsFold = on
	r.mu.Unlock()
}

// SetEventsHold makes the fake accept every `events` request and never answer it, as a
// reader whose accept thread is wedged does; `ping` still answers.
func (r *FakeReader) SetEventsHold(on bool) {
	r.mu.Lock()
	r.eventsHold = on
	r.mu.Unlock()
}

// DropEventsBefore drops the entries before sequence number seq, as a ring that overflowed
// does: the oldest kept is then seq (or the next one, when seq is past the ring).
func (r *FakeReader) DropEventsBefore(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq <= r.eventsOldest {
		return
	}
	drop := min(seq-r.eventsOldest, uint64(len(r.ring)))
	r.ring = r.ring[drop:]
	r.eventsOldest += drop
}

// RestartEvents makes the fake a restarted reader: a new instance and an empty ring, whose
// first entry is numbered 1 again.
func (r *FakeReader) RestartEvents(instance string) {
	r.mu.Lock()
	r.eventsInstance, r.ring, r.eventsOldest, r.sealed = instance, nil, 1, nil
	r.mu.Unlock()
}

// SetEventsGone makes the next n `events` requests end with nothing written, as a reader that
// exited behind a live forward does; the ring is untouched, and `ping` still answers.
func (r *FakeReader) SetEventsGone(n int) {
	r.mu.Lock()
	r.eventsGone = n
	r.mu.Unlock()
}

// SetEventsGoneAfter makes the `events` requests after the next skip answered ones end with
// nothing written, n of them: a reader that dies between two requests of one tick.
func (r *FakeReader) SetEventsGoneAfter(skip, n int) {
	r.mu.Lock()
	r.eventsSkip, r.eventsLater = skip, n
	r.mu.Unlock()
}

// SetEventsAnswer makes the fake answer every `events` request with raw, verbatim, in place of
// its ring: a reader's own line, a malformed answer, or ReaderGone (the connection closed with
// nothing written).
func (r *FakeReader) SetEventsAnswer(raw string) {
	r.mu.Lock()
	r.eventsRaw, r.eventsRawSet = raw, true
	r.mu.Unlock()
}

// EventsRequests is every `events` request line the reader received, without its newline, in
// order.
func (r *FakeReader) EventsRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.eventsReqs...)
}

// handleEvents answers `events <afterSeq> <max>` from the ring: the header, then the entries
// after afterSeq, at most max, in the wire form the reader writes (ADR-0111). A request that
// is not that is answered as the reader answers it.
func (r *FakeReader) handleEvents(conn net.Conn, request string) {
	r.mu.Lock()
	r.eventsReqs = append(r.eventsReqs, request)
	if r.eventsHold {
		r.mu.Unlock()
		holdUntilClose(conn)
		return
	}
	if r.eventsGone > 0 {
		r.eventsGone--
		r.mu.Unlock()
		return
	}
	if r.eventsSkip > 0 {
		if r.eventsSkip--; r.eventsSkip == 0 {
			r.eventsGone, r.eventsLater = r.eventsLater, 0
		}
	}
	if r.eventsRawSet {
		raw := r.eventsRaw
		r.mu.Unlock()
		if raw != ReaderGone {
			io.WriteString(conn, raw)
		}
		return
	}
	words := strings.Split(request, " ")
	if len(words) != 3 {
		r.mu.Unlock()
		fmt.Fprint(conn, readerPrefix+" bad events arguments")
		return
	}
	after, err1 := strconv.ParseUint(words[1], 10, 64)
	max, err2 := strconv.Atoi(words[2])
	if err1 != nil || err2 != nil || max < 1 || max > 200 {
		r.mu.Unlock()
		fmt.Fprint(conn, readerPrefix+" bad events arguments")
		return
	}
	next := r.eventsOldest + uint64(len(r.ring))
	var sb strings.Builder
	now := time.Now()
	fmt.Fprintf(&sb, "events %s %d %d %d %d\n", r.eventsInstance, r.eventsOldest, next, now.UnixMilli(), now.Add(-time.Minute).UnixMilli())
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	sent := 0
	for i, e := range r.ring {
		seq := r.eventsOldest + uint64(i)
		if seq <= after || sent == max {
			continue
		}
		sent++
		if r.sealed == nil {
			r.sealed = map[uint64]bool{}
		}
		r.sealed[seq] = true
		count := e.Count
		if count == 0 {
			count = 1
		}
		fmt.Fprintf(&sb, "%d\t%d\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n", seq, e.TimeMs, e.Type, count, e.Window, e.Changes, b64(e.Package), b64(e.Class), b64(e.Text))
	}
	r.mu.Unlock()
	io.WriteString(conn, sb.String())
}

// isActingRequest reports whether a request is one of the acting verbs of ADR-0035 §1.
func isActingRequest(request string) bool {
	for _, verb := range []string{"tap", "keys", "settext"} {
		if request == verb || strings.HasPrefix(request, verb+" ") {
			return true
		}
	}
	return false
}

// handleAct records one acting request and answers it from the script (SetActAnswers).
// The whole line is recorded, so the newline apart a test sees exactly the client's bytes.
func (r *FakeReader) handleAct(conn net.Conn, line string) {
	r.mu.Lock()
	r.acts = append(r.acts, strings.TrimRight(line, "\n"))
	r.actStarts = append(r.actStarts, time.Now())
	answer := readerPrefix + " done"
	if len(r.actAnswers) > 0 {
		i := r.actNext
		if i < len(r.actAnswers) {
			r.actNext++
			if i == len(r.actAnswers)-1 {
				r.actNext = i // the last answer repeats
			}
		} else {
			i = len(r.actAnswers) - 1
		}
		answer = r.actAnswers[i]
	}
	r.mu.Unlock()
	if answer == ReaderGone {
		// Taken but never answered, as a reader whose acting thread is wedged.
		holdUntilClose(conn)
		return
	}
	io.WriteString(conn, answer)
}

// holdUntilClose blocks until the client closes its side or its deadline ends the read,
// as a request a wedged reader never answers does. The connection's own deadline (set in
// handle) bounds it even if the client never closes.
func holdUntilClose(conn net.Conn) {
	buf := make([]byte, 1)
	conn.Read(buf)
}

// nextFullDoc is the scripted answer to a `dump full`: the next of SetFullDocs, the last
// repeating, or a reader that does not know the words.
func (r *FakeReader) nextFullDoc() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.fullDocs) == 0 {
		return readerPrefix + " bad dump arguments"
	}
	i := r.fullNext
	if i < len(r.fullDocs)-1 {
		r.fullNext++
	}
	return r.fullDocs[i]
}

func (r *FakeReader) nextDoc() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.docs) == 0 {
		return ""
	}
	i := r.next
	if i < len(r.docs) {
		r.next++
		if i == len(r.docs)-1 {
			r.next = i // the last document repeats
		}
	} else {
		i = len(r.docs) - 1
	}
	return r.docs[i]
}

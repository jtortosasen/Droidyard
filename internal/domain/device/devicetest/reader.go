package devicetest

import (
	"bufio"
	"fmt"
	"io"
	"net"
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
// (`ping`, `dump`, `quit`); each dump takes the next scripted document, ReaderGone closes
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
	// mutePings is how many forthcoming ping requests are accepted but never answered,
	// as a reader that is alive but wedged does (plan m1f step 7).
	mutePings int
	// dumpStalls is how many forthcoming dump requests are accepted and held past the
	// caller's warm-read timeout, then dropped, as a reader whose dump is wedged does
	// (plan m1f step 7).
	dumpStalls int
	starts     []time.Time
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
	r := &FakeReader{Port: port, ln: ln, docs: docs}
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
	switch strings.TrimSpace(line) {
	case "ping":
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
	case "dump":
		r.mu.Lock()
		stall := r.dumpStalls > 0
		if stall {
			r.dumpStalls--
		}
		r.dumps++
		r.starts = append(r.starts, time.Now())
		r.mu.Unlock()
		if stall {
			// Hold the dump until the caller's warm-read timeout closes the
			// connection: a wedged reader (plan m1f step 7).
			holdUntilClose(conn)
			return
		}
		doc := r.nextDoc()
		if doc == ReaderGone {
			return
		}
		io.WriteString(conn, doc)
	case "quit":
		r.mu.Lock()
		r.quits++
		r.mu.Unlock()
	default:
		fmt.Fprint(conn, readerPrefix+" unknown request")
	}
}

// holdUntilClose blocks until the client closes its side or its deadline ends the read,
// as a request a wedged reader never answers does. The connection's own deadline (set in
// handle) bounds it even if the client never closes.
func holdUntilClose(conn net.Conn) {
	buf := make([]byte, 1)
	conn.Read(buf)
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

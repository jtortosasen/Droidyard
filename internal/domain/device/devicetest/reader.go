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
	once  sync.Once
	wg    sync.WaitGroup
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
		r.pings++
		r.mu.Unlock()
		io.WriteString(conn, "ok 1234")
	case "dump":
		r.mu.Lock()
		r.dumps++
		r.mu.Unlock()
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

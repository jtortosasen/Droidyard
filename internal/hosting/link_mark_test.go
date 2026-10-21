package hosting

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// markingHost is a fake host of a given API version: it records the fast mark of each hello
// and of each mark frame, in order, and answers every request with an empty result.
type markingHost struct {
	addr, fp string

	mu     sync.Mutex
	hellos []bool // the fast mark of each hello
	seen   []bool // the mark of each hello and each mark frame, in order
}

func newMarkingHost(t *testing.T, api string) *markingHost {
	t.Helper()
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := &markingHost{}
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerPath, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var hf capability.PeerHelloFrame
		if json.Unmarshal(data, &hf) != nil || hf.Hello == nil {
			return
		}
		m.mu.Lock()
		m.hellos = append(m.hellos, hf.Hello.Fast)
		m.seen = append(m.seen, hf.Hello.Fast)
		m.mu.Unlock()
		hello, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: api}})
		if c.Write(ctx, websocket.MessageText, hello) != nil {
			return
		}
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var mf capability.PeerMarkFrame
			if strings.Contains(string(data), `"mark"`) && json.Unmarshal(data, &mf) == nil && mf.Mark != nil && mf.Mark.Fast != nil {
				m.mu.Lock()
				m.seen = append(m.seen, *mf.Mark.Fast)
				m.mu.Unlock()
				continue
			}
			id := ""
			if sm := frameIDRE.FindSubmatch(data[:min(len(data), 256)]); sm != nil {
				id = string(sm[1])
			}
			out, _ := json.Marshal(capability.ResponseEnvelope{ID: id, Result: json.RawMessage(`{}`)})
			if c.Write(ctx, websocket.MessageText, out) != nil {
				return
			}
		}
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate()}, ClientAuth: tls.RequireAnyClientCert, NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	m.addr, m.fp = srv.Listener.Addr().String(), id.Fingerprint()
	return m
}

func (m *markingHost) marks() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), m.seen...)
}

func (m *markingHost) helloMarks() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), m.hellos...)
}

func markEq(a []bool, b ...bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// remoteOf is the remote qa-lab at the fake host.
func (m *markingHost) remote(fast bool) policy.Remote {
	return policy.Remote{Name: "qa-lab", Address: m.addr, Fingerprint: m.fp, Fast: fast}
}

// markClient is a client Host whose remote qa-lab points at the fake host, fast as given,
// started and with its link up. set changes the mark in the configuration and reconciles, as
// the 2 s pass does.
func markClient(t *testing.T, m *markingHost, fast bool) (c *Host, link *Link, set func(fast bool)) {
	t.Helper()
	state := t.TempDir()
	body := func(fast bool) string {
		f := ""
		if fast {
			f = `,"fast":true`
		}
		return `{"remotes":[{"name":"qa-lab","address":"` + m.addr + `","fingerprint":"` + m.fp + `"` + f + `}]}`
	}
	c = clientAt(t, state, body(fast))
	c.Start()
	t.Cleanup(c.Stop)
	waitFor(t, "the link to come up", 10*time.Second, func() bool { return c.Remote("qa-lab") != nil && c.Remote("qa-lab").Connected() })
	set = func(fast bool) {
		writeConfig(t, state, body(fast))
		c.Reconcile()
	}
	return c, c.Remote("qa-lab"), set
}

// holdMarkWrites makes the mark sender stop before its next write until the returned release
// is called (it is called at cleanup too); entered gets a value each time the sender stops.
func holdMarkWrites(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	hold, in := make(chan struct{}), make(chan struct{}, 4)
	markBeforeWrite = func() {
		in <- struct{}{}
		<-hold
	}
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(func() { release(); markBeforeWrite = nil })
	return in, release
}

// Decision (ADR-0123 §1): a mark that changed between the hello and the link coming up
// (update found no connection to tell it on) is sent as soon as the link is up, as any change
// of an open link is. A hook between the hello and the link being up flips the mark. Seen
// failing with the up step not comparing the mark with the hello's (as before the rule): the
// host kept the hello's old mark.
func TestAMarkChangedDuringTheHelloIsSent(t *testing.T) {
	m := newMarkingHost(t, "0.36.0")
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := newLink(m.remote(false), id, "0.36.0", &lockedLog{})
	var once sync.Once
	afterHello = func() { once.Do(func() { l.update(m.remote(true)) }) }
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.run(ctx) }()
	t.Cleanup(func() { stop(); <-done; afterHello = nil })

	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to learn the mark that changed during the hello", func() bool {
		return markEq(m.marks(), false, true)
	})
	if got := m.helloMarks(); !markEq(got, false) {
		t.Errorf("the host's hellos carried %v; want one, with the old mark: the change travelled in the session", got)
	}
}

// Decision (ADR-0123 §1, §4): the same race with an older host, which gets no frame: the
// mark that changed during the hello is the next hello's, by the reconnect an older host gets.
func TestAMarkChangedDuringTheHelloReconnectsAnOlderHost(t *testing.T) {
	m := newMarkingHost(t, "0.35.0")
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := newLink(m.remote(false), id, "0.36.0", &lockedLog{})
	var once sync.Once
	afterHello = func() { once.Do(func() { l.update(m.remote(true)) }) }
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.run(ctx) }()
	t.Cleanup(func() { stop(); <-done; afterHello = nil })

	testtime.Eventually(t, testtime.Bound(15*time.Second), "the older host's second hello, with the new mark", func() bool {
		return markEq(m.helloMarks(), false, true)
	})
}

// Decision (ADR-0123 §1): Marked closes only after the mark frame has been written: a source
// that took it while a write was pending ends after the write, and takes the fresh channel to
// watch again behind the frame. The write is held. Seen failing with the channel closed when
// the change was recorded, before the write: it was closed while the write was held.
func TestMarkedClosesOnlyAfterTheFrameIsWritten(t *testing.T) {
	m := newMarkingHost(t, "0.36.0")
	_, link, set := markClient(t, m, false)
	entered, release := holdMarkWrites(t) // after the client, so it is released before the client stops

	early := link.Marked() // taken before the change
	set(true)
	select {
	case <-entered:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the mark sender never reached the write")
	}
	pending := link.Marked() // taken while the write is pending
	select {
	case <-early:
		t.Fatal("Marked closed before the frame was written")
	case <-pending:
		t.Fatal("Marked closed before the frame was written")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	for _, ch := range []<-chan struct{}{early, pending} {
		select {
		case <-ch:
		case <-time.After(testtime.Bound(10 * time.Second)):
			t.Fatal("Marked did not close once the frame was written, for a source that took it before or during the write")
		}
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to have the frame", func() bool { return markEq(m.marks(), false, true) })
	select {
	case <-link.Marked():
		t.Fatal("the channel taken behind the frame is already closed")
	default:
	}
}

// Decision (ADR-0123 §1): while the link is down Marked is an open channel, not the closed
// one Down returns: a loss is Down's to report, and a source that took Marked on a link that
// is down must not end for a mark nobody sent.
func TestMarkedIsOpenWhileTheLinkIsDown(t *testing.T) {
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := newLink(policy.Remote{Name: "qa-lab", Address: "127.0.0.1:1", Fingerprint: id.Fingerprint()}, id, "0.36.0", &lockedLog{})
	select {
	case <-l.Marked():
		t.Fatal("Marked is closed on a link that is down")
	default:
	}
	select {
	case <-l.Down():
	default:
		t.Fatal("Down is open on a link that is down")
	}
}

// Decision (ADR-0123 §1): a mark write that is pending blocks nothing else: a reconcile
// (which runs update under Host.mu), a call on another request and health all return. Seen
// failing with the write made under l.mu: all three waited for it.
func TestAPendingMarkWriteBlocksNothing(t *testing.T) {
	m := newMarkingHost(t, "0.36.0")
	c, link, set := markClient(t, m, false)
	entered, _ := holdMarkWrites(t) // after the client, so it is released before the client stops

	set(true)
	select {
	case <-entered:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the mark sender never reached the write")
	}

	finished := make(chan string, 3)
	go func() { c.Reconcile(); finished <- "reconcile" }()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testtime.Bound(10*time.Second))
		defer cancel()
		if _, err := link.Call(ctx, capability.RequestEnvelope{ID: "other", Capability: "x"}); err == nil {
			finished <- "call"
		} else {
			finished <- "call failed: " + err.Error()
		}
	}()
	go func() { c.Health(); link.health(); finished <- "health" }()
	got := map[string]bool{}
	for range 3 {
		select {
		case what := <-finished:
			got[what] = true
		case <-time.After(testtime.Bound(5 * time.Second)):
			t.Fatalf("with a mark write pending, only %v returned; want reconcile, call and health", got)
		}
	}
	for _, want := range []string{"reconcile", "call", "health"} {
		if !got[want] {
			t.Errorf("%s did not complete: %v", want, got)
		}
	}
}

// Decision (ADR-0123 §1): two changes within one write leave the host with the last value:
// the sender writes what the mark is when it runs. The first write (on) is held, off follows,
// and the frames the host sees end on off, without a second hello.
func TestTwoQuickChangesLeaveTheLastInTheLink(t *testing.T) {
	m := newMarkingHost(t, "0.36.0")
	_, _, set := markClient(t, m, false)
	entered, release := holdMarkWrites(t) // after the client, so it is released before the client stops

	set(true)
	select {
	case <-entered:
	case <-time.After(testtime.Bound(10 * time.Second)):
		t.Fatal("the mark sender never reached the write")
	}
	set(false)
	release()
	testtime.Eventually(t, testtime.Bound(10*time.Second), "the host to end on the last mark", func() bool {
		seen := m.marks()
		return len(seen) >= 3 && !seen[len(seen)-1]
	})
	time.Sleep(300 * time.Millisecond)
	if seen := m.marks(); !markEq(seen, false, true, false) {
		t.Errorf("the host saw %v; want the hello's false, the first write's true and the last value false", seen)
	}
	if got := m.helloMarks(); len(got) != 1 {
		t.Errorf("%d hellos, want 1", len(got))
	}
}

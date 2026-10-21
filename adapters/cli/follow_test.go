package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A call whose result names a subscription follows it (ADR-0122): it prints the
// envelope, then each event frame as the daemon sent it, and ends at the stream's stopped
// event (0), the interrupt (130) or the daemon closing the connection (1).

const (
	streamEnvelope = `{"id":"cli-1","operation":{"id":"op-1","state":"completed"},"result":{"subscriptionId":"sub-1"}}`
	frameLines     = `{"subscriptionId":"sub-1","event":{"epoch":"e","seq":1,"topic":"t","type":"lines","payload":{"n":1}}}`
	frameGap       = `{"subscriptionId":"sub-1","event":{"epoch":"e","seq":4,"topic":"events","type":"events.gap","payload":{"fromSeq":2,"toSeq":3}}}`
	frameStopped   = `{"subscriptionId":"sub-1","event":{"epoch":"e","seq":5,"topic":"t","type":"stopped","payload":{"reason":"exited"}}}`
	frameAfter     = `{"subscriptionId":"sub-1","event":{"epoch":"e","seq":6,"topic":"t","type":"lines","payload":{"n":2}}}`
)

// sentLog records the request lines a call wrote.
type sentLog struct {
	mu    sync.Mutex
	lines []string
}

func (s *sentLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimSpace(string(p)))
	return len(p), nil
}

func (s *sentLog) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// lineWriter is a synchronised buffer: a test reads what Out holds while Run writes it.
type lineWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lineWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func runFollow(t *testing.T, c Call, r *bufio.Reader, w io.Writer, out *lineWriter, interrupt <-chan struct{}) (chan int, chan error) {
	t.Helper()
	codes, errs := make(chan int, 1), make(chan error, 1)
	go func() {
		code, err := c.Run(r, w, Streams{Out: out, Err: io.Discard}, interrupt)
		codes <- code
		errs <- err
	}()
	return codes, errs
}

// TestRunFollowsAStreamUntilStopped: the envelope, then every frame up to and including the
// stopped one, byte for byte and in order (an events.gap among them, as the daemon sent it),
// exit 0, and nothing after stopped. Seen failing with a Run that returns at the envelope
// (the frames are missing) and with one that ignores stopped (it hangs; the guard fails it).
func TestRunFollowsAStreamUntilStopped(t *testing.T) {
	r, w := fakeDaemon(streamEnvelope, frameLines, frameGap, frameStopped, frameAfter)
	var out lineWriter
	codes, errs := runFollow(t, Call{Capability: "events.subscribe"}, r, w, &out, nil)
	select {
	case code := <-codes:
		if err := <-errs; err != nil || code != 0 {
			t.Fatalf("exit %d, %v; want 0", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the call did not end at the stopped event; it printed %q", out.String())
	}
	want := strings.Join([]string{streamEnvelope, frameLines, frameGap, frameStopped}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("printed %q; want the envelope and the frames up to stopped, unchanged: %q", out.String(), want)
	}
}

// TestRunFollowsAStreamOpenedByTheWait: a first answer that is still running leaves the
// result to operation.wait, and a subscription named there is followed the same.
func TestRunFollowsAStreamOpenedByTheWait(t *testing.T) {
	first := `{"id":"cli-1","operation":{"id":"op-1","state":"running"}}`
	waited := `{"id":"cli-2","operation":{"id":"op-1","state":"completed"},"result":{"subscriptionId":"sub-1"}}`
	r, w := fakeDaemon(first, waited, frameLines, frameStopped)
	var out lineWriter
	codes, errs := runFollow(t, Call{Capability: "operation.watch"}, r, w, &out, nil)
	select {
	case code := <-codes:
		if err := <-errs; err != nil || code != 0 {
			t.Fatalf("exit %d, %v; want 0", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the call did not end at the stopped event; it printed %q", out.String())
	}
	if want := waited + "\n" + frameLines + "\n" + frameStopped + "\n"; out.String() != want {
		t.Errorf("printed %q; want %q", out.String(), want)
	}
}

// TestRunFollowInterrupted: Ctrl-C while following exits 130 and sends nothing more than
// the call itself: no operation.cancel, since the stream is released by the connection's end.
func TestRunFollowInterrupted(t *testing.T) {
	r, _ := fakeDaemon(streamEnvelope, frameLines)
	var out lineWriter
	var sent sentLog
	interrupt := make(chan struct{})
	codes, _ := runFollow(t, Call{Capability: "events.subscribe"}, r, &sent, &out, interrupt)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), frameLines) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out.String(), frameLines) {
		t.Fatalf("the frame never arrived; printed %q", out.String())
	}
	close(interrupt)
	select {
	case code := <-codes:
		if code != ExitInterrupted {
			t.Errorf("exit %d after Ctrl-C; want %d", code, ExitInterrupted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not end the follower")
	}
	if got := sent.get(); len(got) != 1 || !strings.Contains(got[0], `"events.subscribe"`) {
		t.Errorf("the call wrote %q; want its one request and no cancel", got)
	}
}

// TestRunFollowDaemonCloses: the daemon closing the connection while the call follows is
// exit 1 with an error, never a silent 0.
func TestRunFollowDaemonCloses(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte(streamEnvelope + "\n" + frameLines + "\n"))
		pw.Close()
	}()
	var out lineWriter
	codes, errs := runFollow(t, Call{Capability: "events.subscribe"}, bufio.NewReader(pr), io.Discard, &out, nil)
	select {
	case code := <-codes:
		if err := <-errs; err == nil || code != 1 {
			t.Errorf("exit %d, %v; want 1 and an error", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not notice the daemon closing")
	}
	if want := streamEnvelope + "\n" + frameLines + "\n"; out.String() != want {
		t.Errorf("printed %q; want %q", out.String(), want)
	}
}

// TestRunDoesNotFollowAWaitedStream: operation.wait on a streaming operation answers its
// stored result, which names another connection's subscription; the call ends at that
// envelope with 0. Seen failing with the wait answer followed (exit 1 at the close, the
// frame printed; on a real daemon it hangs).
func TestRunDoesNotFollowAWaitedStream(t *testing.T) {
	waited := `{"id":"cli-1","result":{"subscriptionId":"sub-1"}}`
	r, w := fakeDaemon(waited, frameLines)
	var out lineWriter
	codes, errs := runFollow(t, Call{Capability: "operation.wait"}, r, w, &out, nil)
	select {
	case code := <-codes:
		if err := <-errs; err != nil || code != 0 {
			t.Fatalf("exit %d, %v; want 0", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("operation.wait kept reading after its envelope; printed %q", out.String())
	}
	if out.String() != waited+"\n" {
		t.Errorf("printed %q; want the envelope alone", out.String())
	}
}

// TestRunDoesNotFollowWhatIsNotAStream: a result that names no subscription ends the call at
// its envelope, and so does a failed envelope.
func TestRunDoesNotFollowWhatIsNotAStream(t *testing.T) {
	for _, line := range []string{
		`{"id":"cli-1","operation":{"id":"op-1","state":"completed"},"result":{"ok":true}}`,
		`{"id":"cli-1","operation":{"id":"op-1","state":"failed"},"error":{"code":"X","message":"m"}}`,
	} {
		r, w := fakeDaemon(line)
		var out lineWriter
		codes, _ := runFollow(t, Call{Capability: "system.health"}, r, w, &out, nil)
		select {
		case <-codes:
		case <-time.After(5 * time.Second):
			t.Fatalf("the call kept reading after %s", line)
		}
		if out.String() != line+"\n" {
			t.Errorf("printed %q; want the envelope alone", out.String())
		}
	}
}

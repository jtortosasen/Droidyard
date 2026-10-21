package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// script is an interactive fake daemon: it reads the requests a call writes, answers each
// through reply (which returns the lines to send back, in order), and records the
// capabilities it was asked for.
type script struct {
	asked chan string
}

func (s *script) run(t *testing.T, reply func(req capability.RequestEnvelope) []string) (*bufio.Reader, io.Writer) {
	t.Helper()
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	s.asked = make(chan string, 16)
	go func() {
		defer respW.Close()
		sc := bufio.NewReader(reqR)
		for {
			line, err := sc.ReadBytes('\n')
			if err != nil {
				return
			}
			var req capability.RequestEnvelope
			if json.Unmarshal(line, &req) != nil {
				return
			}
			s.asked <- req.Capability
			for _, l := range reply(req) {
				respW.Write([]byte(l + "\n"))
			}
		}
	}()
	t.Cleanup(func() { reqW.Close() })
	return bufio.NewReader(respR), reqW
}

const (
	runningFirst = `{"id":"cli-1","operation":{"id":"op-1","state":"running"}}`
	subscribed   = `{"id":"cli-progress","operation":{"id":"op-2","state":"completed"},"result":{"subscriptionId":"sub-p"}}`
	finished     = `{"id":"cli-2","operation":{"id":"op-1","state":"completed"},"result":{"answer":42}}`
)

func progressFrame(sub, op string, done, total int) string {
	return `{"subscriptionId":"` + sub + `","event":{"epoch":"e","seq":1,"topic":"operation.progress","type":"progress","operation":"` + op +
		`","payload":{"done":` + itoa(done) + `,"total":` + itoa(total) + `}}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// progressDaemon plays a long-running call: running at first, a subscription, then on the wait
// the frames and the final answer.
func progressDaemon(first string, afterWait ...string) func(capability.RequestEnvelope) []string {
	return func(req capability.RequestEnvelope) []string {
		switch req.Capability {
		case "events.subscribe":
			return []string{subscribed}
		case capability.OperationWait:
			return afterWait
		}
		return []string{first}
	}
}

// TestProgressHeldIsPrintedBeforeTheResult (plan m2z D9): under a limit that holds the second
// report (an hour, so no tick comes), the wait's answer flushes it: it is on stderr by the time
// Run returns, after the first and before the result is written. Seen failing with no flush
// before the result: the second report is lost.
func TestProgressHeldIsPrintedBeforeTheResult(t *testing.T) {
	var s script
	r, w := s.run(t, progressDaemon(runningFirst, progressFrame("sub-p", "op-1", 1, 2), progressFrame("sub-p", "op-1", 2, 2), finished))
	var out, errOut lineWriter
	order := &orderedWriter{out: &out, err: &errOut}
	code, err := Call{Capability: "fixture.slow"}.Run(r, w, Streams{Out: order.stdout(), Err: order.stderr(), Terminal: true, ProgressEvery: time.Hour}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if got := errOut.String(); got != "progress 1/2\nprogress 2/2\n" {
		t.Errorf("stderr is %q; want both reports, the second released by the wait's answer", got)
	}
	if got := order.sequence(); got != "err err out" {
		t.Errorf("the writes came in the order %q; want both progress lines before the result", got)
	}
}

// TestNoSubscriptionOffATerminal (plan m2z D9): without Streams.Terminal a long-running call
// asks the daemon for nothing but its operation and the wait, and prints nothing on stderr.
// Seen failing with the subscription sent whatever the stream.
func TestNoSubscriptionOffATerminal(t *testing.T) {
	var s script
	r, w := s.run(t, progressDaemon(runningFirst, finished))
	var out, errOut lineWriter
	code, err := Call{Capability: "fixture.slow"}.Run(r, w, Streams{Out: &out, Err: &errOut}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	close(s.asked)
	var asked []string
	for c := range s.asked {
		asked = append(asked, c)
	}
	if strings.Join(asked, ",") != "fixture.slow,operation.wait" {
		t.Errorf("the call asked for %v; want its capability and operation.wait only", asked)
	}
	if errOut.String() != "" {
		t.Errorf("stderr off a terminal is %q; want it empty", errOut.String())
	}
}

// TestProgressTakesOnlyItsOwnOperationAndSubscription (plan m2z D9): a frame of another
// operation, or of another subscription, is not a progress line. Seen failing with either
// filter removed.
func TestProgressTakesOnlyItsOwnOperationAndSubscription(t *testing.T) {
	var s script
	r, w := s.run(t, progressDaemon(runningFirst,
		progressFrame("sub-p", "op-9", 7, 9), progressFrame("sub-other", "op-1", 8, 9), progressFrame("sub-p", "op-1", 1, 2), finished))
	var out, errOut lineWriter
	code, err := Call{Capability: "fixture.slow"}.Run(r, w, Streams{Out: &out, Err: &errOut, Terminal: true}, nil)
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if got := errOut.String(); got != "progress 1/2\n" {
		t.Errorf("stderr is %q; want only this operation's report on this subscription", got)
	}
}

// TestFirstAnswerProgressAndQueuedLine (plan m2z D9): what the first answer already carries is
// printed before the subscription's reports: its progress, or the line saying what the call
// queued behind. Strings of a progress line are quoted; the queued line names the holder.
func TestFirstAnswerProgressAndQueuedLine(t *testing.T) {
	t.Run("progress", func(t *testing.T) {
		var s script
		first := `{"id":"cli-1","operation":{"id":"op-1","state":"running","progress":{"phase":"build","done":1,"total":4,"message":"a b\nc"}}}`
		r, w := s.run(t, progressDaemon(first, progressFrame("sub-p", "op-1", 2, 4), finished))
		var out, errOut lineWriter
		Call{Capability: "fixture.slow"}.Run(r, w, Streams{Out: &out, Err: &errOut, Terminal: true}, nil)
		want := "progress 1/4 \"build\" \"a b\\nc\"\nprogress 2/4\n"
		if got := errOut.String(); got != want {
			t.Errorf("stderr is %q; want %q", got, want)
		}
	})
	t.Run("queued", func(t *testing.T) {
		var s script
		first := `{"id":"cli-1","operation":{"id":"op-1","state":"queued","blockedBy":{"operationId":"op-0","capability":"build.run"}}}`
		r, w := s.run(t, progressDaemon(first, finished))
		var out, errOut lineWriter
		Call{Capability: "fixture.slow"}.Run(r, w, Streams{Out: &out, Err: &errOut, Terminal: true}, nil)
		if got := errOut.String(); got != "queued behind build.run (op-0)\n" {
			t.Errorf("stderr is %q; want the queued line once", got)
		}
	})
}

// orderedWriter records which of two writers was written to, in order.
type orderedWriter struct {
	out, err *lineWriter
	seq      []string
}

type tagged struct {
	o    *orderedWriter
	tag  string
	dest *lineWriter
}

func (t tagged) Write(p []byte) (int, error) {
	t.o.seq = append(t.o.seq, t.tag)
	return t.dest.Write(p)
}

func (o *orderedWriter) stdout() io.Writer { return tagged{o, "out", o.out} }
func (o *orderedWriter) stderr() io.Writer { return tagged{o, "err", o.err} }
func (o *orderedWriter) sequence() string  { return strings.Join(o.seq, " ") }

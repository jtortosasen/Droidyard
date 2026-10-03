package cli

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeDaemon answers every request with the canned lines, in order, then stays silent.
func fakeDaemon(lines ...string) (*bufio.Reader, io.Writer) {
	pr, pw := io.Pipe()
	go func() {
		for _, l := range lines {
			pw.Write([]byte(l + "\n"))
		}
	}()
	return bufio.NewReader(pr), io.Discard
}

// TestDetachPrintsTheIDWhateverTheState: --detach prints the operation id, as
// ADR-0020 §Adapter projections (CLI row) says, even when the first snapshot already
// shows the operation finished.
func TestDetachPrintsTheIDWhateverTheState(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed"} {
		r, w := fakeDaemon(`{"id":"cli-1","operation":{"id":"op-1","state":"` + state + `"},"result":{}}`)
		var out, errOut strings.Builder
		code, err := Call{Capability: "system.health", Detach: true}.Run(r, w, Streams{Out: &out, Err: &errOut}, nil)
		if err != nil || code != 0 || out.String() != "op-1\n" {
			t.Errorf("state %s: --detach printed %q, exit %d, %v; want the id and exit 0", state, out.String(), code, err)
		}
	}
}

// TestCtrlCStopsAWaitThatIsNotAnOperation: `call operation.wait` waits for an answer
// that may be long in coming; the first Ctrl-C ends it, like everywhere else.
func TestCtrlCStopsAWaitThatIsNotAnOperation(t *testing.T) {
	for _, name := range []string{"operation.wait", "operation.cancel"} {
		r, w := fakeDaemon() // never answers
		interrupt := make(chan struct{})
		time.AfterFunc(20*time.Millisecond, func() { close(interrupt) })
		done := make(chan int, 1)
		go func() {
			code, _ := Call{Capability: name, Params: []byte(`{"operationId":"op-1"}`)}.Run(r, w, Streams{Out: io.Discard, Err: io.Discard}, interrupt)
			done <- code
		}()
		select {
		case code := <-done:
			if code != ExitInterrupted {
				t.Errorf("%s: an interrupted call exited %d; want %d", name, code, ExitInterrupted)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Ctrl-C did not end the call", name)
		}
	}
}

// TestWorkspaceFlagIsSentVerbatim: --workspace takes an id or an absolute root and the
// envelope carries it as given; only the daemon resolves it
// (ADR-0020 §Scope and workspace addressing; plan m1c, R5).
func TestWorkspaceFlagIsSentVerbatim(t *testing.T) {
	for _, args := range [][]string{
		{"workspace.describe", "--workspace", "/Users/Me/./app"},
		{"--workspace=/Users/Me/./app", "workspace.describe"},
	} {
		c, err := Parse(args)
		if err != nil || c.Workspace != "/Users/Me/./app" || c.Capability != "workspace.describe" {
			t.Errorf("Parse(%q) = %+v, %v", args, c, err)
		}
	}
	for _, bad := range [][]string{{"workspace.describe", "--workspace"}, {"--workspace=", "workspace.describe"}} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted a missing workspace", bad)
		}
	}

	pr, pw := io.Pipe()
	sent := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(pr).ReadString('\n')
		sent <- line
	}()
	r, _ := fakeDaemon(`{"id":"cli-1","error":{"code":"WORKSPACE_NOT_OPEN","message":"no"}}`)
	c := Call{Capability: "workspace.describe", Workspace: "/Users/Me/./app"}
	c.Run(r, pw, Streams{Out: io.Discard, Err: io.Discard}, nil)
	if line := <-sent; !strings.Contains(line, `"workspace":"/Users/Me/./app"`) {
		t.Errorf("the envelope sent: %s", line)
	}
}

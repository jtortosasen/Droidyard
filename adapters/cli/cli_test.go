package cli

import (
	"bufio"
	"encoding/json"
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

// TestProjectFlagIsSentAsTheProjectParameter: --project takes the path of a project folder
// and the request carries it as the `project` parameter, beside the other parameters, as
// given; only the daemon resolves it (ADR-0137 §3, §5). The envelope has no workspace field
// to carry it in, and the old flag is refused. A --project beside a `project` in --params is
// a usage error rather than a choice the daemon would make.
func TestProjectFlagIsSentAsTheProjectParameter(t *testing.T) {
	for _, args := range [][]string{
		{"build.tasks", "--project", "/Users/Me/./app"},
		{"--project=/Users/Me/./app", "build.tasks"},
	} {
		c, err := Parse(args)
		if err != nil || c.Project != "/Users/Me/./app" || c.Capability != "build.tasks" {
			t.Errorf("Parse(%q) = %+v, %v", args, c, err)
		}
	}
	for _, bad := range [][]string{{"build.tasks", "--project"}, {"--project=", "build.tasks"}} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted a missing project", bad)
		}
	}
	if _, err := Parse([]string{"build.tasks", "--workspace", "/Users/Me/app"}); err == nil {
		t.Error("the --workspace flag, which --project replaced, was accepted")
	}

	sendOf := func(c Call) string {
		pr, pw := io.Pipe()
		sent := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(pr).ReadString('\n')
			sent <- line
		}()
		r, _ := fakeDaemon(`{"id":"cli-1","error":{"code":"REQUEST_INVALID_PARAMS","message":"no"}}`)
		c.Run(r, pw, Streams{Out: io.Discard, Err: io.Discard}, nil)
		return <-sent
	}
	line := sendOf(Call{Capability: "build.tasks", Project: "/Users/Me/./app"})
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		t.Fatal(err)
	}
	if _, has := env["workspace"]; has || string(env["params"]) != `{"project":"/Users/Me/./app"}` {
		t.Errorf("the envelope sent: %s; want the project in params and no workspace field", line)
	}
	line = sendOf(Call{Capability: "build.run", Project: "/p", Params: json.RawMessage(`{"tasks":["assemble"]}`)})
	if !strings.Contains(line, `"project":"/p"`) || !strings.Contains(line, `"tasks":["assemble"]`) {
		t.Errorf("the envelope sent: %s; want the project beside the other parameters", line)
	}
	line = sendOf(Call{Capability: "device.list"})
	if strings.Contains(line, "project") {
		t.Errorf("a call with no project sent one: %s", line)
	}
	if _, err := (Call{Project: "/p", Params: json.RawMessage(`{"project":"/q"}`)}).requestParams(); err == nil {
		t.Error("a --project beside a project in --params was accepted")
	}

}

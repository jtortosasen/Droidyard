package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpadapter "github.com/jtortosasen/droidyard/adapters/mcp"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// shim is a re-executed `droidyard mcp` driven as an MCP client drives it: JSON-RPC
// lines on its stdin, responses read from its stdout. Every line both ways is kept,
// so a failing test prints the transcript.
type shim struct {
	t      *testing.T
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte
	stderr *bytes.Buffer
	exited chan error
	// trailing holds what the shim printed while wait drained it.
	trailing [][]byte
}

func startShim(t *testing.T, state, name string, args ...string) *shim {
	t.Helper()
	return startShimArgs(t, state, name, append([]string{"mcp", "--no-autostart"}, args...))
}

// startShimArgs starts `droidyard <args>` as an MCP shim.
func startShimArgs(t *testing.T, state, name string, args []string) *shim {
	t.Helper()
	return startShimIn(t, state, "", name, args)
}

// startShimIn starts `droidyard <args>` as an MCP shim with dir as its cwd; "" keeps
// the test's.
func startShimIn(t *testing.T, state, dir, name string, args []string) *shim {
	t.Helper()
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: append([]string{"droidyard"}, args...),
		Env:  append(os.Environ(), reexec+"=1", "DROIDYARD_STATE_DIR="+state),
		Dir:  dir,
	}
	s := &shim{t: t, name: name, cmd: cmd, lines: make(chan []byte, 64), stderr: new(bytes.Buffer), exited: make(chan error, 1)}
	cmd.Stderr = s.stderr
	var err error
	if s.stdin, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			s.lines <- line
		}
		close(s.lines)
		s.exited <- cmd.Wait()
	}()
	t.Cleanup(func() { cmd.Process.Kill() })
	return s
}

func (s *shim) send(msg string) {
	s.t.Helper()
	s.t.Logf("%s → %s", s.name, msg)
	if _, err := io.WriteString(s.stdin, msg+"\n"); err != nil {
		s.t.Fatalf("%s: writing to the shim: %v", s.name, err)
	}
}

// recv returns the next line the shim prints.
func (s *shim) recv() []byte {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatalf("%s: the shim exited before answering; stderr: %s", s.name, s.stderr)
		}
		s.t.Logf("%s ← %s", s.name, line)
		return line
	case <-time.After(10 * time.Second):
		s.t.Fatalf("%s: no response within 10 s", s.name)
	}
	return nil
}

// silent asserts the shim prints nothing for a while.
func (s *shim) silent(d time.Duration) {
	s.t.Helper()
	select {
	case line := <-s.lines:
		s.t.Fatalf("%s: received a line that is not its own: %s", s.name, line)
	case <-time.After(d):
	}
}

func (s *shim) initialize(clientName string) map[string]any {
	s.t.Helper()
	s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"` + clientName + `","version":"0"}}}`)
	res := response(s.t, s.recv(), 0)
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return res
}

func (s *shim) callTool(id int, name, args string) {
	s.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`)
}

// exit closes the shim's stdin and returns its exit code.
func (s *shim) exit() int {
	s.t.Helper()
	s.stdin.Close()
	return s.wait()
}

// wait returns the shim's exit code once it exits, failing after 10 s.
func (s *shim) wait() int {
	s.t.Helper()
	timeout := time.After(10 * time.Second)
	for lines := s.lines; lines != nil; {
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			s.t.Logf("%s ← %s (drained at exit)", s.name, line)
			s.trailing = append(s.trailing, line)
		case <-timeout:
			s.t.Fatalf("%s: the shim did not exit within 10 s", s.name)
		}
	}
	select {
	case err := <-s.exited:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			s.t.Fatal(err)
		}
		return 0
	case <-timeout:
		s.t.Fatalf("%s: the shim did not exit within 10 s", s.name)
	}
	return -1
}

// response decodes a JSON-RPC response with the given id and returns its result.
func response(t *testing.T, line []byte, id int) map[string]any {
	t.Helper()
	var msg struct {
		ID     *int           `json:"id"`
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		t.Fatalf("not JSON-RPC: %s", line)
	}
	if msg.ID == nil || *msg.ID != id {
		t.Fatalf("expected the response to id %d, got %s", id, line)
	}
	if msg.Error != nil {
		t.Fatalf("id %d: JSON-RPC error %v", id, msg.Error)
	}
	return msg.Result
}

// published reads the golden file: what consumers are contracted to.
func published(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../schema/capabilities.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, c := range doc.Capabilities {
		out[c["name"].(string)] = c
	}
	return out
}

func normalised(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return v
}

// TestMCPTranscript drives a real `droidyard mcp` over the real socket and holds the
// wire to the contract (ADR-0025 acceptance gate, criteria 1, 2 and 6;
// docs/interfaces/mcp-tool-surface.md): the documented instructions; tools/list as the
// golden file says, all four hints on every entry; results with structured content;
// an error result with no structured content; a human-audience tool refused by the
// library, since it is not registered; the same result as the CLI; and a shim that
// ends its session when its stdin ends.
func TestMCPTranscript(t *testing.T) {
	p := startInProcess(t)
	before := p.d.Sessions()
	c := startShim(t, p.state, "client")

	// Criterion 6.
	initRes := c.initialize("transcript")
	if initRes["instructions"] != mcpadapter.Instructions {
		t.Errorf("initialize carried instructions %q; want the documented block", initRes["instructions"])
	}
	if p.d.Sessions() != before+1 {
		t.Errorf("an initialized shim is %d sessions; want %d", p.d.Sessions(), before+1)
	}

	// Criterion 1: tools/list against the golden file.
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	listRaw := c.recv()
	var list struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listRaw, &list); err != nil {
		t.Fatal(err)
	}
	listed := map[string]map[string]json.RawMessage{}
	for _, tool := range list.Result.Tools {
		var name string
		json.Unmarshal(tool["name"], &name)
		listed[name] = tool
	}
	golden := published(t)
	for name, g := range golden {
		tool, ok := listed[mcpadapter.ToolName(name)]
		if !ok {
			t.Errorf("tools/list lacks %s", mcpadapter.ToolName(name))
			continue
		}
		var ann map[string]any
		json.Unmarshal(tool["annotations"], &ann)
		want := g["annotations"].(map[string]any)
		if len(ann) != 4 {
			t.Errorf("%s: annotations %s; want exactly the four hints", name, tool["annotations"])
		}
		for _, k := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if v, ok := ann[k]; !ok || v != want[k] {
				t.Errorf("%s: %s is %v (present: %v); the golden file says %v", name, k, v, ok, want[k])
			}
		}
		for wire, gold := range map[string]string{"inputSchema": "input", "outputSchema": "output"} {
			if !reflect.DeepEqual(normalised(t, tool[wire]), g[gold]) {
				b, _ := json.Marshal(g[gold])
				t.Errorf("%s: %s on the wire is %s; the golden %s is %s", name, wire, tool[wire], gold, b)
			}
		}
		var desc string
		json.Unmarshal(tool["description"], &desc)
		if desc != g["description"] {
			t.Errorf("%s: description %q; want the golden one", name, desc)
		}
	}
	for name := range listed {
		if _, ok := golden[strings.Replace(name, "_", ".", 1)]; !ok && name != "fixture_block" && name != "fixture_slow" {
			t.Errorf("tools/list has %s, which is neither published nor the test fixture", name)
		}
	}
	// ADR-0015 §6: a human-audience capability is not listed.
	if _, ok := listed["fixture_human"]; ok {
		t.Error("tools/list lists the human-audience fixture")
	}

	// A successful call, and parity with the CLI.
	c.callTool(2, "system_capabilities", `{}`)
	var call2 struct {
		Result struct {
			Content           []map[string]any `json:"content"`
			StructuredContent json.RawMessage  `json:"structuredContent"`
			IsError           bool             `json:"isError"`
		} `json:"result"`
	}
	json.Unmarshal(c.recv(), &call2)
	if call2.Result.IsError || len(call2.Result.StructuredContent) == 0 || len(call2.Result.Content) != 1 {
		t.Fatalf("system_capabilities returned %+v; want structured content and one text block", call2.Result)
	}
	if text, _ := call2.Result.Content[0]["text"].(string); !reflect.DeepEqual(normalised(t, []byte(text)), normalised(t, call2.Result.StructuredContent)) {
		t.Errorf("the text rendering %q is not the structured content", text)
	}
	cliEnv, code := p.call(t, "system.capabilities")
	if code != 0 || !reflect.DeepEqual(normalised(t, cliEnv.Result), normalised(t, call2.Result.StructuredContent)) {
		t.Errorf("parity: the CLI result %s differs from the MCP structuredContent %s", cliEnv.Result, call2.Result.StructuredContent)
	}

	// Criterion 2: a rejection is isError with no structuredContent key on the wire,
	// and our code in the text, not the library's validation.
	c.callTool(3, "system_health", `{"bogus":1}`)
	raw3 := c.recv()
	var call3 struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	json.Unmarshal(raw3, &call3)
	if string(call3.Result["isError"]) != "true" {
		t.Errorf("a bad argument returned %s; want isError true", raw3)
	}
	if _, ok := call3.Result["structuredContent"]; ok {
		t.Errorf("an error result carries a structuredContent key: %s", raw3)
	}
	var content []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(call3.Result["content"], &content)
	var e capability.Error
	if len(content) != 1 || json.Unmarshal([]byte(content[0].Text), &e) != nil || e.Code != capability.ErrInvalidParams {
		t.Errorf("the error text is %v; want the error object with %s", content, capability.ErrInvalidParams)
	}

	// The human-audience tool is refused by the library at the transport: it is not
	// registered, so our handler never runs (the fixture fails the test if it does).
	c.callTool(4, "fixture_human", `{}`)
	var call4 struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw4 := c.recv()
	json.Unmarshal(raw4, &call4)
	if call4.Error.Code != -32602 || !strings.Contains(call4.Error.Message, "tool 'fixture_human' not found") {
		t.Errorf("calling the human-audience tool returned %s; want JSON-RPC INVALID_PARAMS, tool not found", raw4)
	}

	// Shim lifetime: stdin ends, the session ends, the shim exits 0.
	if code := c.exit(); code != 0 {
		t.Errorf("the shim exited %d when its stdin ended; want 0. stderr: %s", code, c.stderr)
	}
	eventually(t, "the session count returns to its earlier value", func() bool { return p.d.Sessions() == before })
}

// TestMCPSessionsAreSeparate: two shims at once are two sessions, each with its own
// mcp-go server; requests reusing the same JSON-RPC ids reach only their own session
// (ADR-0025 criterion 4, as far as 1b uses it). Run under -race: a race inside mcp-go
// fails it.
func TestMCPSessionsAreSeparate(t *testing.T) {
	p := startInProcess(t)
	a := startShim(t, p.state, "a")
	b := startShim(t, p.state, "b")
	a.initialize("client-a")
	b.initialize("client-b")

	// Same id in both sessions: a's call blocks, b's answers at once.
	a.callTool(7, "fixture_block", `{}`)
	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture_block never started")
	}
	b.callTool(7, "system_health", `{}`)
	var health struct {
		Result struct {
			StructuredContent struct {
				Version    string `json:"version"`
				Operations int    `json:"operations"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	json.Unmarshal(b.recv(), &health)
	if health.Result.StructuredContent.Version == "" || health.Result.StructuredContent.Operations != 1 {
		t.Fatalf("b's id 7 got %+v; want its own system_health result", health.Result)
	}
	a.silent(300 * time.Millisecond)

	// Interleaved again on another shared id.
	a.callTool(8, "system_capabilities", `{}`)
	b.callTool(8, "operation_wait", `{"operationId":"op-nope"}`)
	var bWait struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	json.Unmarshal(b.recv(), &bWait)
	aCaps := response(t, a.recv(), 8)
	if !bWait.Result.IsError {
		t.Error("b's id 8 (operation_wait on an unknown id) was not its error result")
	}
	if _, ok := aCaps["structuredContent"]; !ok {
		t.Errorf("a's id 8 was not its system_capabilities result: %v", aCaps)
	}

	close(p.release)
	blockRes := response(t, a.recv(), 7)
	if sc, _ := json.Marshal(blockRes["structuredContent"]); string(sc) != "{}" {
		t.Errorf("a's id 7 got %v; want fixture_block's empty result", blockRes)
	}
	b.silent(300 * time.Millisecond)

	if a.exit() != 0 || b.exit() != 0 {
		t.Error("a shim did not exit 0 when its stdin ended")
	}
}

// TestMCPShimReportsAStoppedDaemon: when the daemon goes away under an idle shim whose
// stdin is still open, the shim exits non-zero with a message on stderr, as a
// crashed daemon's shims do (ADR-0019 §Lifetime).
func TestMCPShimReportsAStoppedDaemon(t *testing.T) {
	p := startInProcess(t)
	c := startShim(t, p.state, "client")
	c.initialize("idle")

	shutdown(t, p)
	if code := c.wait(); code == 0 {
		t.Fatal("the shim exited 0 after the daemon stopped under it; want non-zero")
	}
	if !strings.Contains(c.stderr.String(), "the daemon closed the session") {
		t.Errorf("the shim's stderr does not say why it exited: %q", c.stderr)
	}
}

// TestMCPWaitRejectsWhatRoundsToZero: the budget travels as whole milliseconds and 0
// means the default, so a sub-millisecond --wait is a usage error rather than a silent
// 20 s.
func TestMCPWaitRejectsWhatRoundsToZero(t *testing.T) {
	state := shortDir(t)
	for _, v := range []string{"500us", "0s", "-1s", "soon"} {
		p := droidyard(t, state, "mcp", "--no-autostart", "--wait", v)
		line := p.expect(t, "--wait needs")
		if code := p.wait(t); code != 2 {
			t.Errorf("--wait %s exited %d (%s); want the usage error 2", v, code, line)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// notifyTool is a test-only MCP tool: it sends n notifications naming the caller's
// from and call, each through its own session (ServerFromContext), one at a time,
// retrying while mcp-go reports the session's channel full; then it answers with the
// client name and protocol version it reads from its session. mcp-go drops a
// notification rather than block on a full channel (sendNotificationCore), so the
// retry makes the count measure routing, not capacity.
func notifyTool() server.ServerTool {
	return server.ServerTool{
		Tool: mcpgo.NewToolWithRawSchema("fixture_notify", "Sends notifications, then names its session's client.", json.RawMessage(`{"type":"object"}`)),
		Handler: func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			var args struct {
				From string `json:"from"`
				Call int    `json:"call"`
				N    int    `json:"n"`
			}
			json.Unmarshal(req.Params.RawArguments, &args)
			srv := server.ServerFromContext(ctx)
			for i := 0; i < args.N; i++ {
				for {
					err := srv.SendNotificationToClient(ctx, "droidyard/test", map[string]any{"from": args.From, "call": args.Call, "i": i})
					if errors.Is(err, server.ErrNotificationChannelBlocked) && ctx.Err() == nil {
						time.Sleep(time.Millisecond)
						continue
					}
					if err != nil {
						return mcpgo.NewToolResultError(err.Error()), nil
					}
					break
				}
			}
			var who struct {
				Client   string `json:"client"`
				Protocol string `json:"protocol"`
			}
			sess := server.ClientSessionFromContext(ctx)
			if ci, ok := sess.(server.SessionWithClientInfo); ok {
				who.Client = ci.GetClientInfo().Name
			}
			if pv, ok := sess.(interface{ ProtocolVersion() string }); ok {
				who.Protocol = pv.ProtocolVersion()
			}
			raw, _ := json.Marshal(who)
			return &mcpgo.CallToolResult{Content: []mcpgo.Content{mcpgo.NewTextContent(string(raw))}, RawStructuredContent: raw}, nil
		},
	}
}

// wireLine is a line the shim prints: a notification from fixture_notify, or a
// response.
type wireLine struct {
	ID     *int   `json:"id"`
	Method string `json:"method"`
	Params struct {
		From string `json:"from"`
		Call int    `json:"call"`
		I    int    `json:"i"`
	} `json:"params"`
	Result struct {
		StructuredContent struct {
			Client   string `json:"client"`
			Protocol string `json:"protocol"`
		} `json:"structuredContent"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Raw []byte `json:"-"`
}

func (s *shim) recvWire() wireLine {
	s.t.Helper()
	raw := s.recv()
	l := wireLine{Raw: raw}
	if err := json.Unmarshal(raw, &l); err != nil {
		s.t.Fatalf("%s: not JSON-RPC: %s", s.name, raw)
	}
	return l
}

// TestMCPSessionsAreTheirOwn: each connection is a session of its own (ADR-0025
// §Decision, as amended 2026-10-03): the notifications a session's handlers send,
// its client info and its negotiated protocol version reach and describe that
// session, never another. Two re-executed shims on one daemon, ten concurrent calls
// each, 20 notifications per call.
func TestMCPSessionsAreTheirOwn(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	p.mcp.AddTestTools(notifyTool())
	a := startShim(t, p.state, "a")
	b := startShim(t, p.state, "b")
	open := func(s *shim, name, protocol string) {
		s.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"` + protocol + `","capabilities":{},"clientInfo":{"name":"` + name + `","version":"0"}}}`)
		if got := response(t, s.recv(), 0)["protocolVersion"]; got != protocol {
			t.Fatalf("%s: initialize negotiated %v; want %s", name, got, protocol)
		}
		s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	}
	want := map[*shim][2]string{a: {"a", "2025-06-18"}, b: {"b", "2025-03-26"}}
	open(a, "a", "2025-06-18")
	open(b, "b", "2025-03-26")

	const calls, perCall = 10, 20
	for i := 1; i <= calls; i++ {
		for _, s := range []*shim{a, b} {
			s.callTool(i, "fixture_notify", fmt.Sprintf(`{"from":%q,"call":%d,"n":%d}`, want[s][0], i, perCall))
		}
	}

	for _, s := range []*shim{a, b} {
		own, foreign := map[string]bool{}, 0
		answered := 0
		note := func(l wireLine) {
			if l.Params.From != want[s][0] {
				foreign++
				return
			}
			own[strconv.Itoa(l.Params.Call)+"/"+strconv.Itoa(l.Params.I)] = true
		}
		for answered < calls {
			l := s.recvWire()
			if l.ID == nil {
				note(l)
				continue
			}
			answered++
			if got := l.Result.StructuredContent; l.Result.IsError || got.Client != want[s][0] || got.Protocol != want[s][1] {
				t.Errorf("%s: call %d read client %q, protocol %q (isError %v); want its own, %q and %q", s.name, *l.ID, got.Client, got.Protocol, l.Result.IsError, want[s][0], want[s][1])
			}
		}
		// Anything still arriving belongs to this session too, or is a leak.
		quiet := time.After(300 * time.Millisecond)
	collect:
		for {
			select {
			case raw, ok := <-s.lines:
				if !ok {
					t.Fatalf("%s: the shim exited", s.name)
				}
				t.Logf("%s ← %s (after the last answer)", s.name, raw)
				var l wireLine
				json.Unmarshal(raw, &l)
				note(l)
			case <-quiet:
				break collect
			}
		}
		if foreign != 0 || len(own) != calls*perCall {
			t.Errorf("%s received %d of its own %d notifications and %d of the other session's; want all of its own and none of the other's", s.name, len(own), calls*perCall, foreign)
		}
	}

	if a.exit() != 0 || b.exit() != 0 {
		t.Error("a shim did not exit 0 when its stdin ended")
	}
}

// TestMCPNotificationPrecedesItsResponse: the session's writer empties the
// notification channel before it writes a response, so a notification a handler sent
// before returning precedes that handler's response on the wire (plan m1d, step 1;
// step 3's progress rests on it). The writer is held on the test hook until the
// notification is on the session's channel and the response is queued for it, so
// both are ready when it chooses: a writer free to take either first fails this
// within 20 runs (-count=20) with probability 1 − 2⁻²⁰.
func TestMCPNotificationPrecedesItsResponse(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	p.mcp.AddTestTools(notifyTool())
	var off, bothReady atomic.Bool
	// Before every pick the writer waits for something to write; when a notification
	// is ready, it waits for the response too. While the writer is in the hook
	// nothing drains the channel, and the tool's response is queued only after its
	// notification, so the response is never ready alone.
	p.mcp.SetWriterHook(func(pending func() (notifications, responses int)) {
		deadline := time.Now().Add(10 * time.Second)
		for !off.Load() && time.Now().Before(deadline) {
			n, r := pending()
			if n > 0 && r > 0 {
				bothReady.Store(true)
				off.Store(true) // held once; the writer runs free from here
				return
			}
			if n == 0 && r > 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	t.Cleanup(func() { off.Store(true); p.mcp.SetWriterHook(nil) })

	c := startShim(t, p.state, "client")
	c.initialize("order")
	c.callTool(1, "fixture_notify", `{"from":"order","call":1,"n":1}`)
	first, second := c.recvWire(), c.recvWire()
	off.Store(true)
	if !bothReady.Load() {
		t.Fatal("the hook never held the writer with the notification and the response both ready")
	}
	if first.ID != nil || first.Method != "droidyard/test" || second.ID == nil || *second.ID != 1 {
		t.Errorf("the wire carried %s then %s; want the notification, then the response to 1", first.Raw, second.Raw)
	}
	if code := c.exit(); code != 0 {
		t.Errorf("the shim exited %d", code)
	}
}

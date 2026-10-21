package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// Decision (ADR-0123 §1): a mark frame belongs to a peer's session alone. A UI session reads
// the same bytes as any other envelope, as today: the ones that are not an envelope get the
// error answer of an envelope that does not parse, nothing closes the session (a peer's
// would close 1003 on {"mark":5}), and the next request is answered. Seen failing with the
// mark read on every session: {"mark":5} closed the UI's session 1003.
func TestAMarkShapedFrameOnAUISessionIsAnEnvelopeThatDoesNotParse(t *testing.T) {
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	h := newWSHandler(&caller{reg: reg, ops: ops}, event.New(event.Options{}), "tok")
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {ts.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	send := func(frame string) {
		t.Helper()
		if err := c.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() capability.ResponseEnvelope {
		t.Helper()
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("the session ended: %v", err)
		}
		var env capability.ResponseEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("a frame that is not an envelope: %s", data)
		}
		return env
	}
	send(`{"auth":"tok"}`)
	for _, frame := range []string{`{"mark":{"fast":true}}`, `{"mark":5}`} {
		send(frame)
		if env := read(); env.Error == nil || env.Error.Code != capability.ErrInvalidParams {
			t.Errorf("%s answered %+v; want INVALID_PARAMS, the answer of an envelope that does not parse", frame, env)
		}
	}
	send(`{"id":"1","capability":"nothing.here"}`)
	if env := read(); env.ID != "1" {
		t.Errorf("the request after the mark-shaped frames was answered %+v; want id 1", env)
	}
}

package hosting

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// stallingHost is a host that says hello and then reads nothing until released, then
// answers every request with an empty result.
func stallingHost(t *testing.T, release <-chan struct{}) (addr, fingerprint string) {
	t.Helper()
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(capability.PeerPath, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(256 << 20)
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil { // the client's hello
			return
		}
		hello, _ := json.Marshal(capability.PeerHelloFrame{Hello: &capability.PeerHello{PeerProtocol: capability.PeerProtocol, API: "0.0.0"}})
		if c.Write(ctx, websocket.MessageText, hello) != nil {
			return
		}
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var head struct {
				ID string `json:"id"`
			}
			// The head of a large frame only: the id is first.
			json.NewDecoder(strings.NewReader(string(data))).Decode(&head)
			out, _ := json.Marshal(capability.ResponseEnvelope{ID: head.ID, Result: json.RawMessage(`{}`)})
			if c.Write(ctx, websocket.MessageText, out) != nil {
				return
			}
		}
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate()}, ClientAuth: tls.RequireAnyClientCert, NextProtos: []string{"http/1.1"}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), id.Fingerprint()
}

// Decision (plan m2t step 2; coder/websocket closes the whole connection when the context
// of a Write ends before the frame is out): a caller that gives up on a call while its
// frame is still being written must not cut the link under every other call. The host
// reads nothing, so a large frame stays half written; the caller's context ends; the link
// stays up, and once the host reads again the next call is answered.
func TestACancelledCallDoesNotCutTheLink(t *testing.T) {
	release := make(chan struct{})
	addr, fp := stallingHost(t, release)
	id, _, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := newLink(policy.Remote{Name: "x", Address: addr, Fingerprint: fp}, id, "0.0.0", &lockedLog{})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.run(ctx) }()
	defer func() { stop(); <-done }()
	for deadline := time.Now().Add(10 * time.Second); !l.Connected(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the link never came up")
		}
	}

	big := json.RawMessage(`"` + strings.Repeat("a", 48<<20) + `"`)
	cctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := l.Call(cctx, capability.RequestEnvelope{Capability: "x", Params: big}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the cancelled call = %v; want its context's error", err)
	}
	time.Sleep(300 * time.Millisecond)
	if !l.Connected() {
		t.Fatal("the link is down: one caller giving up cut the connection under every other call")
	}

	close(release)
	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer qcancel()
	resp, err := l.Call(qctx, capability.RequestEnvelope{ID: "mine", Capability: "x"})
	if err != nil || resp.ID != "mine" || resp.Error != nil {
		t.Errorf("the next call = %+v / %v; want its answer", resp, err)
	}
}

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
)

// TestHelloChatIsOnlyWhatThePatternAllows holds ADR-0088 §1 at the daemon: a hello's chat
// id reaches the MCP handler only when it matches ^[A-Za-z0-9-]{1,64}$, and is cleared
// otherwise, whoever sent it: the id keys the core's memory of a chat's binding, so a
// refused value must never become a key. The mutation that proves it: with the clearing
// removed, the five refused values reach the handler as sent.
func TestHelloChatIsOnlyWhatThePatternAllows(t *testing.T) {
	state := shortDir(t)
	reg := capability.NewRegistry()
	ops := capability.NewOperations(reg)
	ops.RegisterCapabilities()
	seen := make(chan string, 1)
	d, err := Start(Options{
		State: state, Socket: filepath.Join(state, "run", "droidyard.sock"), Version: system.Version, UID: os.Getuid(), Ops: ops,
		Handlers: map[capability.ClientKind]Handler{capability.ClientMCP: func(_ context.Context, s *Session) { seen <- s.Hello.Chat }},
		Log:      testLog{t},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	})
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for chat, want := range map[string]string{
		"4f7c1c1e-0b8d-4b6e-9d0a-5a1d3c2b9e11": "4f7c1c1e-0b8d-4b6e-9d0a-5a1d3c2b9e11",
		string(long[:64]):                      string(long[:64]),
		"":                                     "",
		string(long):                           "",
		"a b":                                  "",
		"a/../b":                               "",
		"chat\n":                               "",
		"é":                                    "",
	} {
		h := Hello{Protocol: Protocol, Version: system.Version, Client: capability.ClientMCP, Cwd: "/", Chat: chat}
		conn, _, _, err := Dial(filepath.Join(state, "run", "droidyard.sock"), h)
		if err != nil {
			t.Fatalf("hello with chat %q: %v", chat, err)
		}
		select {
		case got := <-seen:
			if got != want {
				t.Errorf("a hello with chat %q reached the handler as %q; want %q", chat, got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the handler never saw the hello with chat %q", chat)
		}
		conn.Close()
	}
}

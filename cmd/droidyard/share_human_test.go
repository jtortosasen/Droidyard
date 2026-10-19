package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestHostingCapabilitiesHumanOnly: hosting.share, hosting.unshare and hosting.join are the
// human's (ADR-0015 §6; ADR-0108 §1). No MCP profile lists a hosting_ tool, an MCP client's
// call is refused by audience before any operation exists, and a paired peer's is refused as
// not lent (REQUEST_NOT_ALLOWED_HERE). The CLI, which is the human's, is not refused: it
// reaches the handler. Setting the audience to all would list the tools and let the MCP client
// in, so the first assertions fail.
func TestHostingCapabilitiesHumanOnly(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	for _, args := range [][]string{nil, {"--tools", "all"}, {"--tools", "core"}} {
		c := startShim(t, p.state, "hosting", args...)
		c.initialize("hosting")
		names, _ := toolNames(t, c, 1)
		if len(names) == 0 {
			t.Errorf("%v lists no tools; the check below would pass vacuously", args)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "hosting_") {
				t.Errorf("%v lists %s; sharing is never offered to agents", args, n)
			}
		}
		c.exit()
	}
	params := map[string]string{
		"hosting.share":   `{"device":"R58M001","detach":true}`,
		"hosting.unshare": `{}`,
		"hosting.join":    `{"code":"dy1.x"}`,
	}
	for name, body := range params {
		for _, who := range []struct {
			client capability.ClientKind
			label  string
		}{{capability.ClientMCP, "an MCP client"}, {capability.ClientPeer, "a paired peer"}} {
			id, err := p.ops.Start(context.Background(), capability.Request{Capability: name, Params: json.RawMessage(body), Client: who.client})
			var e *capability.Error
			if !errors.As(err, &e) || e.Code != capability.ErrNotAllowedHere || id != "" {
				t.Errorf("%s as %s: (%q, %v); want REQUEST_NOT_ALLOWED_HERE and no operation", name, who.label, id, err)
			}
		}
		env := callOn(t, p.state, name, "--params", body)
		if env.Error != nil && env.Error.Code == capability.ErrNotAllowedHere {
			t.Errorf("%s from the CLI: %+v; want it to reach the handler", name, env.Error)
		}
	}
}

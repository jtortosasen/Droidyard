package capability

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// TestPeerOperationsAreTracedWithThePeer is ADR-0099 §5: the trace line of an operation a
// paired peer made names the peer by its configured name (peer=anna), after the duration and
// before a failure's code, so a host reads which of its peers did what from daemon.log; the
// line of any other client's operation has no peer field, whatever name its session gave
// itself. Seen failing without the field (the peer's line was the host UI's).
func TestPeerOperationsAreTracedWithThePeer(t *testing.T) {
	ops, started := ownerStore(t)
	rec := &traceRecorder{ops: ops}
	ops.UseTrace(rec.logf)
	ctx := context.Background()

	for i, c := range []struct {
		client ClientKind
		origin Origin
		peer   string
	}{
		{ClientPeer, Origin{ID: "link-1", Name: "anna"}, " peer=anna"},
		{ClientPeer, Origin{ID: "link-2", Name: "bob-2"}, " peer=bob-2"},
		{ClientUI, Origin{ID: "ws-1"}, ""},
		{ClientMCP, Origin{ID: "mcp-1", Name: "anna"}, ""},
	} {
		id, err := ops.Start(ctx, Request{Capability: "own.block", Client: c.client, Origin: c.origin})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		if err := ops.Cancel(id); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.Wait(ctx, id, time.Time{}); err != nil {
			t.Fatal(err)
		}
		rec.await(t, i+1)
		want := regexp.MustCompile(`^droidyard: op ` + id + ` client=` + string(c.client) + ` capability=own.block workspace=- state=cancelled ms=\d+` + c.peer + `$`)
		if line := rec.snapshot()[i]; !want.MatchString(line) {
			t.Errorf("the line of a %s operation is %q; want it to match %s", c.client, line, want)
		}
	}
}

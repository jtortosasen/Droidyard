package device

import (
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (N1, ADR-0104 §1): a device row's occupiedBy tells a paired peer's link from an MCP
// client that happens to call itself by the same name, so the host's UI does not show a
// colleague where an agent is. peer is true for the first and absent for the second. Seen
// failing with the flag not filled (both rows were alike).
func TestOccupiedByTellsAPeerFromAnAgentOfTheSameName(t *testing.T) {
	holder := func(f *hostFixture) *OccupiedBy {
		t.Helper()
		out := f.call(t, "", "device.list", `{}`)
		if out.State != capability.StateCompleted {
			t.Fatalf("device.list: %s %+v", out.State, out.Error)
		}
		for _, d := range out.Result.(ListResult).Devices {
			if d.Device == leaseDevice {
				return d.OccupiedBy
			}
		}
		t.Fatalf("device.list lacks %s", leaseDevice)
		return nil
	}

	f := leaseFixture(t, "refuse")
	f.tree(t, anna, leaseDevice)
	if o := holder(f); o == nil || o.Name != anna || !o.Peer {
		t.Errorf("a peer's row is %+v; want name anna, peer true", o)
	}

	g := leaseFixture(t, "refuse")
	g.ops.Sessions().Named("mcp-1", anna)
	g.agent(t, "device.uninstall", `{"device":"`+leaseDevice+`","applicationId":"dev.droidyard.formapp"}`)
	if o := holder(g); o == nil || o.Name != anna || o.Peer {
		t.Errorf("an agent named anna has the row %+v; want name anna, peer absent", o)
	}
}

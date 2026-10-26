package sdk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/sdk/sdktest"
)

// TestProfilesParse pins the reading of `avdmanager list device` (spike m3f step 1 (d)):
// blocks of `id: <n> or "<id>"`, Name, OEM and an optional Tag, separated by dashes. The id
// is the quoted one, even with a space in it; a block with no Tag has none; the header and
// a notice of the JVM are not profiles. The fixture is seven blocks of the real tool's
// output.
func TestProfilesParse(t *testing.T) {
	out := append([]byte("Picked up JAVA_TOOL_OPTIONS: -Xmx1g\n"), sdktest.DevicesOut()...)
	got := parseProfiles(out)
	want := []Profile{
		{ID: "ai_glasses_displayless", Name: "Audio Glasses", OEM: "Google", Tag: "ai-glasses"},
		{ID: "automotive_1024p_landscape", Name: "Automotive (1024p landscape)", OEM: "Google", Tag: "android-automotive-playstore"},
		{ID: "desktop_api37", Name: "Desktop (Preview)", OEM: "Google", Tag: "android-desktop"},
		{ID: "Galaxy Nexus", Name: "Galaxy Nexus", OEM: "Google"},
		{ID: "Nexus 7 2013", Name: "Nexus 7", OEM: "Google"},
		{ID: "pixel_9a", Name: "Pixel 9a", OEM: "Google"},
		{ID: "pixel_6", Name: "Pixel 6", OEM: "Google"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d profiles, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profile %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// A block that is not whole, or whose id could be read as an option, is not offered.
	odd := "id: 1 or \"-x\"\n    Name: Dash\n---------\nid: 2 or \"noname\"\n---------\nid: 3 or \"ok\"\n    Name: Fine\n"
	if got := parseProfiles([]byte(odd)); len(got) != 1 || got[0].ID != "ok" {
		t.Errorf("odd blocks parsed as %+v, want only ok", got)
	}
}

type profilesFixture struct {
	*toolFixture
}

func newProfiles(t *testing.T) *profilesFixture {
	t.Helper()
	f := newTool(t)
	sdktest.Write(t, f.dir, "devices.out", string(sdktest.DevicesOut()))
	return &profilesFixture{f}
}

// TestProfilesCached pins the cache (plan m3f step 6): the listing is asked of avdmanager
// once for as long as the same avdmanager stands, and again for a changed file (a new
// cmdline-tools replaces it) or another path (a higher revision appeared). Seen failing
// with no cache: the second answer ran the tool again.
func TestProfilesCached(t *testing.T) {
	f := newProfiles(t)
	for i := 0; i < 2; i++ {
		ps, err := f.tool.Profiles(context.Background())
		if err != nil || len(ps) != 7 {
			t.Fatalf("Profiles = %d, %v", len(ps), err)
		}
	}
	if n := len(sdktest.Calls(t, f.dir)); n != 1 {
		t.Fatalf("avdmanager ran %d times for two answers, want 1", n)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(f.paths.Path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tool.Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(sdktest.Calls(t, f.dir)); n != 2 {
		t.Errorf("avdmanager ran %d times after its file changed, want 2", n)
	}
	sdktest.InstallTools(t, f.root, "latest-2", "19.0")
	if _, err := f.tool.Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(sdktest.Calls(t, f.dir)); n != 3 {
		t.Errorf("avdmanager ran %d times after another avdmanager became the highest, want 3", n)
	}
	// What the answer returns is a copy: changing it does not change the next one.
	ps, _ := f.tool.Profiles(context.Background())
	ps[0].Name = "changed"
	if again, _ := f.tool.Profiles(context.Background()); again[0].Name == "changed" {
		t.Error("the cached list was changed through an answer")
	}
}

// TestProfilesNone: a tool that exits 0 and prints no profile is a failure, not an empty
// list that would make every profile unknown.
func TestProfilesNone(t *testing.T) {
	f := newTool(t)
	_, err := f.tool.Profiles(context.Background())
	ce := wantErr(t, err, ErrToolFailed)
	if !strings.Contains(ce.Cause, "no device profiles") {
		t.Errorf("cause = %q", ce.Cause)
	}
	if _, err := f.tool.Profiles(context.Background()); err == nil {
		t.Error("the empty answer was cached")
	}
}

// TestProfilesCapability: sdk.profiles through the registry, as an agent (audience all),
// answers {profiles} and a text with one line per profile; without the tools it is
// SDK_TOOLS_NOT_FOUND naming sdk.install.
func TestProfilesCapability(t *testing.T) {
	f := newProfiles(t)
	reg := capability.NewRegistry()
	Register(reg, Options{Env: f.env, Tool: f.tool})
	ops := capability.NewOperations(reg)
	call := func() capability.Outcome {
		id, err := ops.Start(context.Background(), capability.Request{Capability: "sdk.profiles", Params: []byte(`{}`), Client: capability.ClientMCP,
			Origin: capability.Origin{ID: "mcp-1", Client: capability.ClientMCP}})
		if err != nil {
			t.Fatal(err)
		}
		out, err := ops.Wait(context.Background(), id, time.Now().Add(20*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := call()
	if out.State != capability.StateCompleted {
		t.Fatalf("outcome = %s %+v", out.State, out.Error)
	}
	res := out.Result.(ProfilesResult)
	if len(res.Profiles) != 7 {
		t.Fatalf("profiles = %d, want 7", len(res.Profiles))
	}
	d, _ := reg.Lookup("sdk.profiles")
	text := d.Text(res)
	if lines := strings.Split(text, "\n"); len(lines) != 7 || !strings.Contains(text, `profile "Nexus 7 2013" "Nexus 7" Google`) || !strings.Contains(text, "tag=android-desktop") {
		t.Errorf("text = %q, want one line per profile, quoted where a word is not safe", text)
	}
	if err := os.RemoveAll(filepath.Join(f.root, "cmdline-tools")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, call(), ErrToolsNotFound)
}

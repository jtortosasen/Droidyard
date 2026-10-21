package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/adapters/cli"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/schema"
)

// shippedTree is the command tree of this binary's registry, built in process.
func shippedTree(t *testing.T) *cmdTree {
	t.Helper()
	reg, _, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
	ct, err := buildTree(reg)
	if err != nil {
		t.Fatalf("the shipped registry and the hand commands do not make a tree: %v", err)
	}
	return ct
}

// blankedEnvelope is an envelope with the one field every recorded call has its own of
// (the operation's id) blanked, the rest of it as the process printed it.
func blankedEnvelope(t *testing.T, raw []byte) (capability.ResponseEnvelope, string) {
	t.Helper()
	var env capability.ResponseEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("printed %q, not an envelope: %v", raw, err)
	}
	if env.Operation != nil {
		env.Operation.ID = ""
	}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return env, string(out)
}

// Decision (ADR-0122 D2, D14; plan m2z step 3): a derived command is the call. `droidyard
// device list` and `droidyard call device.list` print the same envelope, and a workspace
// command addresses the project root of the current directory as call does.
func TestDerivedCommandIsTheCall(t *testing.T) {
	t.Parallel()
	t.Run("device list", func(t *testing.T) {
		p := fakeADBDaemon(t, map[string]string{
			"devices.out":           "List of devices attached\nemulator-5554 device product:sdk_gphone16k_arm64 transport_id:1\n",
			"emulator-5554.getprop": fixtureFile(t, "pixel9a-getprop.txt"),
			"emulator-5554.avd":     fixtureFile(t, "pixel9a-emu-avd-name.txt"),
		})
		viaCall := droidyard(t, p.state, "call", "--no-autostart", "device.list")
		if code := viaCall.wait(t); code != 0 {
			t.Fatalf("call device.list exited %d: %s", code, viaCall.stdout.String())
		}
		derived := droidyard(t, p.state, "device", "list", "--json", "--no-autostart")
		if code := derived.wait(t); code != 0 {
			t.Fatalf("device list exited %d: %s %s", code, derived.stdout.String(), derived.stderr.String())
		}
		a, sa := blankedEnvelope(t, viaCall.stdout.Bytes())
		b, sb := blankedEnvelope(t, derived.stdout.Bytes())
		if len(listOf(t, b)) != 1 {
			t.Fatalf("device list did not list the emulator: %s", derived.stdout.String())
		}
		if string(a.Result) != string(b.Result) {
			t.Errorf("the results differ:\n call    %s\n derived %s", a.Result, b.Result)
		}
		if a.Operation == nil || b.Operation == nil || a.Operation.State != b.Operation.State {
			t.Errorf("operation states differ: %+v and %+v", a.Operation, b.Operation)
		}
		if sa != sb {
			t.Errorf("the envelopes differ beyond the operation id:\n call    %s\n derived %s", sa, sb)
		}
	})

	t.Run("workspace describe from a project directory", func(t *testing.T) {
		p := startInProcess(t)
		root, sub := gradleProject(t)
		if _, code := callIn(t, p.state, sub, "workspace.open"); code != 0 {
			t.Fatalf("workspace.open exited %d", code)
		}
		viaCall := droidyardIn(t, p.state, sub, "call", "--no-autostart", "workspace.describe")
		viaCall.wait(t)
		derived := droidyardIn(t, p.state, sub, "workspace", "describe", "--json", "--no-autostart")
		if code := derived.wait(t); code != 0 {
			t.Fatalf("workspace describe exited %d: %s %s", code, derived.stdout.String(), derived.stderr.String())
		}
		a, sa := blankedEnvelope(t, viaCall.stdout.Bytes())
		_, sb := blankedEnvelope(t, derived.stdout.Bytes())
		if info := workspaceOf(t, a); info.Root != root {
			t.Fatalf("describe addressed %q; want the project root %q", info.Root, root)
		}
		if sa != sb {
			t.Errorf("the envelopes differ:\n call    %s\n derived %s", sa, sb)
		}
	})
}

// Decision (ADR-0122 D12): the index leads to the derived commands, the hand commands'
// prose lives in each command's own help, and a domain lists its verbs, hand ones among
// them.
func TestHelpIndex(t *testing.T) {
	t.Parallel()
	run := func(args ...string) (string, int) {
		p := droidyard(t, shortDir(t), args...)
		code := p.wait(t)
		return p.stdout.String(), code
	}
	index, code := run("help")
	if code != 0 || !strings.Contains(index, "droidyard help <command>") {
		t.Fatalf("help exited %d: %q", code, index)
	}
	deviceLine := ""
	for _, l := range strings.Split(index, "\n") {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "device" && strings.Contains(l, ",") {
			deviceLine = l
		}
	}
	if !strings.Contains(deviceLine, "list") || !strings.Contains(deviceLine, "screenshot") {
		t.Errorf("the index does not lead to device list: %q", deviceLine)
	}
	share, code := run("help", "share")
	if code != 0 || !strings.Contains(share, "droidyard share [<serial>] [-d] [--on vpn|lan|<ip>]") ||
		!strings.Contains(share, "droidyard join <code>") {
		t.Errorf("help share exited %d: %q; want share's own section", code, share)
	}
	if strings.Contains(index, "droidyard join <code>") {
		t.Errorf("the index still carries share's prose: %q", index)
	}
	dom, code := run("device", "--help")
	if code != 0 {
		t.Fatalf("device --help exited %d", code)
	}
	for _, verb := range []string{"allow", "disallow", "list", "screenshot"} {
		if !strings.Contains(dom, "  "+verb+" ") {
			t.Errorf("device --help does not list %s: %q", verb, dom)
		}
	}
	if verb, code := run("help", "device", "list"); code != 0 || !strings.Contains(verb, "droidyard device list") {
		t.Errorf("help device list exited %d: %q", code, verb)
	}
}

// Decision (ADR-0122 D12): no hand command is renamed, so every first spelling of the
// binary still resolves to the hand command it named; device and artifact, which are
// capability domains too, keep their verbs as hand verbs of the domain (D13).
func TestOldSpellings(t *testing.T) {
	t.Parallel()
	ct := shippedTree(t)
	names := []string{"caps", "schema", "version", "help", "serve", "call", "status", "stop", "mcp", "ui",
		"trust", "id", "host", "remote", "device", "share", "unshare", "join", "init", "hook", "trash", "artifact"}
	if len(names) != 22 {
		t.Fatalf("the table holds %d names; want the 22 the binary had", len(names))
	}
	for _, name := range names {
		path := []string{name}
		want := name
		switch name {
		case "device":
			path, want = []string{"device", "allow"}, "device allow"
		case "artifact":
			path, want = []string{"artifact", "cat"}, "artifact cat"
		}
		r, err := ct.Resolve(path)
		if err != nil || r.Hand == nil {
			t.Errorf("%v does not resolve to a hand command: %+v, %v", path, r, err)
			continue
		}
		if help, err := ct.Help(path); err != nil || !strings.Contains(help, "droidyard "+want) {
			t.Errorf("%v: help %q (%v) does not name its command", path, help, err)
		}
	}
	// The verbs that used to be `device`'s own are still its.
	if r, err := ct.Resolve([]string{"device", "disallow"}); err != nil || r.Hand == nil {
		t.Errorf("device disallow does not resolve to a hand command: %+v, %v", r, err)
	}
}

// Decision (ADR-0122 D2): a word nothing matches is a usage error that names the nearest
// commands, and nothing is dialled.
func TestUnknownCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"devce", "list"}, "device"},
		{[]string{"device", "lsit"}, "list"},
		{[]string{"nonsense"}, "unknown command"},
	} {
		p := droidyard(t, shortDir(t), tc.args...)
		if code := p.wait(t); code != cli.ExitUsage || !strings.Contains(p.stderr.String(), tc.want) || p.stdout.Len() != 0 {
			t.Errorf("droidyard %v: exit %d, stdout %q, stderr %q; want 2 and %q on stderr", tc.args, code, p.stdout.String(), p.stderr.String(), tc.want)
		}
	}
}

// Decision (ADR-0122 D5, D13): no capability's parameter may take a flag the CLI keeps for
// itself or the same flag as another, and no capability may shadow a hand command (a
// verb of its domain, or a domain named like a top-level command), so neither side is
// shadowed silently.
func TestCLIFlagsDoNotCollide(t *testing.T) {
	t.Parallel()
	ct := shippedTree(t)
	for _, s := range specs(ct.reg) {
		d, _ := ct.reg.Lookup(s.Name)
		in, err := schema.Input(d)
		if err != nil {
			t.Fatal(err)
		}
		s.Input = in
		if err := cli.CheckSpec(s); err != nil {
			t.Error(err)
		}
	}

	// The check can fail: a capability whose verb is a hand verb's name, and one whose domain
	// is a hand command's, are each refused when the tree is built.
	for _, name := range []string{"device.allow", "init.thing"} {
		reg, _, _, _, _, _, _ := registry(isolatedEnv(t), nil, nil, nil)
		reg.MustRegister(capability.Descriptor{
			Name: name, Kind: capability.Command, Scope: capability.Global,
			Description: "A fake.", Params: struct{}{}, Result: struct{}{},
			Handler: func(context.Context, *capability.Call) (any, error) { return struct{}{}, nil },
		})
		if _, err := buildTree(reg); err == nil {
			t.Errorf("a capability %s did not stop the tree from being built", name)
		}
	}
}

// Decision (ADR-0122 D14): the tree is built from the binary's own registry, so `help` and
// a usage error need no daemon and no state; a command reflects only its own input.
func TestSpecsReflectNoInputUntilInvoked(t *testing.T) {
	t.Parallel()
	ct := shippedTree(t)
	for _, s := range specs(ct.reg) {
		if s.Input != nil {
			t.Fatalf("%s: specs reflected its input up front", s.Name)
		}
	}
	var out, errOut bytes.Buffer
	if code := ct.help([]string{"device", "tap"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "--device") {
		t.Fatalf("help device tap exited %d: %q %q", code, out.String(), errOut.String())
	}
	if r, _ := ct.Resolve([]string{"device", "tap"}); r.Spec == nil || r.Spec.Input == nil {
		t.Error("the invoked command's input was not read")
	}
	if r, _ := ct.Resolve([]string{"device", "swipe"}); r.Spec == nil || r.Spec.Input != nil {
		t.Error("a command nobody invoked had its input reflected")
	}
}

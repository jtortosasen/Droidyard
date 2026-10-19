package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

var (
	fpAnna = strings.Repeat("a", 64)
	fpBob  = strings.Repeat("b", 64)
	fpLab  = strings.Repeat("c", 64)
)

// hostingPolicy is a policy over a state root of its own, read as the daemon would.
func hostingPolicy(t *testing.T) (*policy.Policy, string) {
	t.Helper()
	state := shortDir(t)
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	return policy.New(env), state
}

// verb runs one hosting verb over pol and returns its exit code and what it printed.
func verb(t *testing.T, pol *policy.Policy, kind string, args ...string) (code int, out, errs string) {
	t.Helper()
	var so, se bytes.Buffer
	var w io.Writer = &so
	switch kind {
	case "host":
		code = hostWith(pol, args, w, &se)
	case "remote":
		code = remoteWith(pol, args, w, &se)
	default:
		t.Fatalf("no verb %q", kind)
	}
	return code, so.String(), se.String()
}

// mustVerb is verb for one that has to succeed.
func mustVerb(t *testing.T, pol *policy.Policy, kind string, args ...string) string {
	t.Helper()
	code, out, errs := verb(t, pol, kind, args...)
	if code != 0 {
		t.Fatalf("droidyard %s %v exited %d: %s%s", kind, args, code, out, errs)
	}
	return out
}

func updateOf(t *testing.T, body string) policy.UpdateParams {
	t.Helper()
	var up policy.UpdateParams
	if err := json.Unmarshal([]byte(body), &up); err != nil {
		t.Fatal(err)
	}
	return up
}

// TestHostingCLI holds ADR-0098 §7: each verb is one policy.update that leaves the
// section it names as the human meant and every other key as it was, and the verbs say
// what the configuration cannot (that a running daemon follows listen and remotes within seconds, with no restart, ADR-0109 §3; what lending exposes).
func TestHostingCLI(t *testing.T) {
	t.Parallel()
	pol, _ := hostingPolicy(t)
	// Keys of other sections must survive every verb: a patch is a merge, not a rewrite.
	if _, err := pol.Update(context.Background(), updateOf(t, `{"devices":{"allowPhysical":true,"allowlist":["R58M"]},"shell":{"enabled":true}}`)); err != nil {
		t.Fatal(err)
	}

	mustVerb(t, pol, "host", "admit", "anna", fpAnna)
	mustVerb(t, pol, "host", "admit", "bob", fpBob)
	if got := pol.Get().Hosting.Peers; !reflect.DeepEqual(got, []policy.HostPeer{{Name: "anna", Fingerprint: fpAnna}, {Name: "bob", Fingerprint: fpBob}}) {
		t.Fatalf("peers after two admits = %+v", got)
	}

	// Admitting what is there is said and writes nothing; a name or a fingerprint that
	// belongs to another entry is refused, with the file as it was.
	before := pol.Get().Revision
	if out := mustVerb(t, pol, "host", "admit", "anna", fpAnna); !strings.Contains(out, "already admitted") {
		t.Errorf("admitting anna twice printed %q", out)
	}
	for _, args := range [][]string{{"anna", fpLab}, {"carol", fpAnna}} {
		if code, _, errs := verb(t, pol, "host", append([]string{"admit"}, args...)...); code != 1 || errs == "" {
			t.Errorf("host admit %v = %d %q; want 1 and a reason", args, code, errs)
		}
	}
	if pol.Get().Revision != before {
		t.Error("an admit that changed nothing wrote the file")
	}

	// listen: a running daemon applies it within seconds, and it says so, not that a restart
	// is needed (ADR-0109 §3); off removes the key.
	out := mustVerb(t, pol, "host", "listen", "127.0.0.1:7421")
	if got := pol.Get().Hosting.Listen; got != "127.0.0.1:7421" || !strings.Contains(out, "within seconds") || strings.Contains(out, "droidyard stop") {
		t.Errorf("listen = %q, printed %q; want the address and no restart", got, out)
	}
	out = mustVerb(t, pol, "host", "listen", "off")
	if got := pol.Get().Hosting.Listen; got != "" || !strings.Contains(out, "within seconds") || strings.Contains(out, "droidyard stop") {
		t.Errorf("listen off = %q, printed %q; want it removed and no restart", got, out)
	}

	// lend: the whole device is lent and the human is told; one not yet admitted by the
	// host's own devices policy is said not to be lent yet.
	out = mustVerb(t, pol, "host", "lend", "R58M", "--to", "anna,bob")
	if !strings.Contains(out, "whole device") || !strings.Contains(out, "dedicated") {
		t.Errorf("lend printed %q; want ADR-0099 §4's warning", out)
	}
	if strings.Contains(out, "Not lent yet") {
		t.Errorf("lend of an allowlisted serial warned of the physical policy: %q", out)
	}
	out = mustVerb(t, pol, "host", "lend", "--to=bob", "S2")
	if !strings.Contains(out, "Not lent yet") {
		t.Errorf("lend of a serial outside devices.allowlist printed %q; want the physical policy named", out)
	}
	if out := mustVerb(t, pol, "host", "lend", "emulator-5554", "--to", "anna"); !strings.Contains(out, "only physical devices are lent") {
		t.Errorf("lend of an emulator printed %q; want ADR-0099 §1 said", out)
	}
	mustVerb(t, pol, "host", "unlend", "emulator-5554")
	mustVerb(t, pol, "host", "lend", "R58M", "--to", "anna") // sets who; it does not add
	if got := pol.Get().Hosting.Lend; !reflect.DeepEqual(got, []policy.Lend{{Device: "R58M", To: []string{"anna"}}, {Device: "S2", To: []string{"bob"}}}) {
		t.Fatalf("lend = %+v", got)
	}

	// revoke closes the peer and takes its name out of what is lent: a name left there
	// would lend to whoever is admitted under it next. An entry left with no one stays, lent
	// to nobody: a share lasts until unshare (ADR-0108 §2).
	mustVerb(t, pol, "host", "revoke", "bob")
	g := pol.Get().Hosting
	if !reflect.DeepEqual(g.Peers, []policy.HostPeer{{Name: "anna", Fingerprint: fpAnna}}) || !reflect.DeepEqual(g.Lend, []policy.Lend{{Device: "R58M", To: []string{"anna"}}, {Device: "S2", To: []string{}}}) {
		t.Errorf("after revoking bob: peers %+v, lend %+v; want anna alone, and S2 lent to no one", g.Peers, g.Lend)
	}
	if code, _, _ := verb(t, pol, "host", "revoke", "bob"); code != 1 {
		t.Errorf("revoking a peer that is not admitted exited %d; want 1", code)
	}

	// A name lent to before it was admitted: revoke takes it out of `lend` though no peer
	// has it, so a later admit under that name does not inherit the devices; admit says
	// what the name is already lent when it is admitted.
	mustVerb(t, pol, "host", "lend", "S3", "--to", "carol,anna")
	out = mustVerb(t, pol, "host", "admit", "carol", fpLab)
	if !strings.Contains(out, "already lent S3") {
		t.Errorf("admitting carol printed %q; want it to name S3, already lent to her", out)
	}
	mustVerb(t, pol, "host", "revoke", "carol")
	mustVerb(t, pol, "host", "lend", "S4", "--to", "dave")
	out = mustVerb(t, pol, "host", "revoke", "dave") // never admitted
	if !strings.Contains(out, "removed dave from what you lend: S4") {
		t.Errorf("revoking a name that was never admitted printed %q; want what it removed", out)
	}
	for _, l := range pol.Get().Hosting.Lend {
		if slices.Contains(l.To, "dave") || slices.Contains(l.To, "carol") {
			t.Errorf("lend %+v still names a revoked name", l)
		}
	}
	if code, _, errs := verb(t, pol, "host", "revoke", "dave"); code != 1 || !strings.Contains(errs, "lent nothing") {
		t.Errorf("revoking a name that is neither admitted nor lent anything = %d %q; want 1", code, errs)
	}
	mustVerb(t, pol, "host", "unlend", "S3")
	mustVerb(t, pol, "host", "unlend", "S2") // lent to no one since bob's revoke
	mustVerb(t, pol, "host", "unlend", "S4")

	mustVerb(t, pol, "host", "unlend", "R58M")
	if got := pol.Get().Hosting.Lend; len(got) != 0 {
		t.Errorf("lend after unlend = %+v", got)
	}
	if code, _, _ := verb(t, pol, "host", "unlend", "R58M"); code != 1 {
		t.Errorf("unlending a device that is not lent exited %d; want 1", code)
	}

	// remote: client side. A running daemon dials it within seconds; the phone it lends needs the client's own
	// physical policy, and the id to put in the allowlist is named.
	out = mustVerb(t, pol, "remote", "add", "qa-lab", "100.64.0.3:7421", fpLab)
	for _, want := range []string{"within seconds", "allowPhysical", "qa-lab/<serial>", "allowlist"} {
		if !strings.Contains(out, want) {
			t.Errorf("remote add printed %q; want it to say %q", out, want)
		}
	}
	if strings.Contains(out, "droidyard stop") {
		t.Errorf("remote add printed %q; a running daemon follows remotes live, so no restart is asked", out)
	}
	if got := pol.Get().Remotes; !reflect.DeepEqual(got, []policy.Remote{{Name: "qa-lab", Address: "100.64.0.3:7421", Fingerprint: fpLab}}) {
		t.Fatalf("remotes = %+v", got)
	}
	if code, _, _ := verb(t, pol, "remote", "add", "qa-lab", "100.64.0.4:7421", fpLab); code != 1 {
		t.Errorf("adding qa-lab again with another address exited %d; want 1", code)
	}
	if out := mustVerb(t, pol, "remote", "list"); !strings.Contains(out, "qa-lab  100.64.0.3:7421  "+fpLab) {
		t.Errorf("remote list printed %q", out)
	}
	out = mustVerb(t, pol, "remote", "remove", "qa-lab")
	if got := pol.Get().Remotes; len(got) != 0 || !strings.Contains(out, "within seconds") || strings.Contains(out, "droidyard stop") {
		t.Errorf("after remove: remotes %+v, printed %q; want none and no restart", got, out)
	}

	// None of it touched the other sections.
	all := pol.Get()
	if !all.Devices.AllowPhysical || !reflect.DeepEqual(all.Devices.Allowlist, []string{"R58M"}) || !all.Shell.Enabled {
		t.Errorf("devices %+v and shell %+v changed by the hosting verbs", all.Devices, all.Shell)
	}
}

// TestHostingCLIThroughTheBinary: main dispatches host, remote and id to the verbs above,
// over the state root the environment names, and `id` prints the fingerprint the daemon
// made (ADR-0098 §1).
func TestHostingCLIThroughTheBinary(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	if code := droidyard(t, state, "host", "admit", "anna", fpAnna).wait(t); code != 0 {
		t.Fatalf("droidyard host admit exited %d", code)
	}
	if code := droidyard(t, state, "remote", "add", "qa-lab", "100.64.0.3:7421", fpLab).wait(t); code != 0 {
		t.Fatalf("droidyard remote add exited %d", code)
	}
	env := platform.Env{GOOS: platform.Current().GOOS, UID: os.Getuid(), Getenv: func(k string) string {
		if k == "DROIDYARD_STATE_DIR" {
			return state
		}
		return os.Getenv(k)
	}}
	g := policy.New(env).Get()
	if len(g.Hosting.Peers) != 1 || g.Hosting.Peers[0].Name != "anna" || len(g.Remotes) != 1 || g.Remotes[0].Name != "qa-lab" {
		t.Errorf("the binary wrote hosting %+v, remotes %+v", g.Hosting, g.Remotes)
	}
	if code := droidyard(t, state, "host", "frobnicate").wait(t); code != 2 {
		t.Errorf("an unknown verb exited %d; want 2", code)
	}

	daemonAt(t, state, "")
	id := droidyard(t, state, "id")
	if code := id.wait(t); code != 0 {
		t.Fatalf("droidyard id exited %d: %s", code, id.stderr.String())
	}
	if got, want := strings.TrimSpace(id.stdout.String()), identityOf(t, state); got != want || len(got) != 64 {
		t.Errorf("droidyard id printed %q; want the daemon's fingerprint %q", got, want)
	}
}

// TestHostingCLIRefusesBeforeWriting: a name, a fingerprint or an address the
// configuration would refuse is refused by the command, as a usage error, before the
// policy is read for a write: policy.update is never reached and no file appears.
// (Not parallel: it counts the writes through the update lock's seam.)
func TestHostingCLIRefusesBeforeWriting(t *testing.T) {
	var attempts atomic.Int32
	policy.SetLockSeam(func() { attempts.Add(1) })
	t.Cleanup(func() { policy.SetLockSeam(nil) })

	cases := []struct {
		name string
		kind string
		args []string
	}{
		{"name with a capital", "host", []string{"admit", "Anna", fpAnna}},
		{"name starting with a digit", "host", []string{"admit", "1anna", fpAnna}},
		{"name too long", "host", []string{"admit", strings.Repeat("a", 33), fpAnna}},
		{"short fingerprint", "host", []string{"admit", "anna", "abc123"}},
		{"upper-case fingerprint", "host", []string{"admit", "anna", strings.ToUpper(fpAnna)}},
		{"revoke a malformed name", "host", []string{"revoke", "An na"}},
		{"lend to a malformed name", "host", []string{"lend", "R58M", "--to", "anna,Bob"}},
		{"lend with --to twice", "host", []string{"lend", "R58M", "--to", "anna", "--to", "bob"}},
		{"unlend of an empty serial", "host", []string{"unlend", ""}},
		{"remote remove of a malformed name", "remote", []string{"remove", "Bad Name"}},
		{"lend with no --to", "host", []string{"lend", "R58M"}},
		{"lend to the same peer twice", "host", []string{"lend", "R58M", "--to", "anna,anna"}},
		{"listen on a host name", "host", []string{"listen", "vpn.example:7421"}},
		{"listen on every interface", "host", []string{"listen", "0.0.0.0:7421"}},
		{"listen with no port", "host", []string{"listen", "100.64.0.2"}},
		{"remote with a host name", "remote", []string{"add", "qa-lab", "qa.example:7421", fpLab}},
		{"remote with no host", "remote", []string{"add", "qa-lab", "0.0.0.0:7421", fpLab}},
		{"remote with port 0", "remote", []string{"add", "qa-lab", "100.64.0.3:0", fpLab}},
		{"remote with a bad fingerprint", "remote", []string{"add", "qa-lab", "100.64.0.3:7421", "nope"}},
		{"remote with a bad name", "remote", []string{"add", "QA", "100.64.0.3:7421", fpLab}},
		{"missing arguments", "remote", []string{"add", "qa-lab"}},
	}
	for _, c := range cases {
		pol, state := hostingPolicy(t)
		code, out, errs := verb(t, pol, c.kind, c.args...)
		if code != 2 || errs == "" || out != "" {
			t.Errorf("%s: droidyard %s %v = %d, stdout %q, stderr %q; want exit 2, a reason and nothing said done", c.name, c.kind, c.args, code, out, errs)
		}
		if _, err := os.Stat(filepath.Join(state, "config", policy.FileName)); err == nil {
			t.Errorf("%s: a configuration file was written", c.name)
		}
	}
	if n := attempts.Load(); n != 0 {
		t.Errorf("policy.update was attempted %d times for arguments that are invalid; want 0", n)
	}
}

// TestHostingCLIRetriesAConcurrentEditOnce: a policy.update that finds the file changed
// since it was read (POLICY_REVISION_CONFLICT) reads again and decides again, once, so
// the other writer's change and the verb's both land; a second conflict is reported, with
// the verb's change not made. The competing write is made through the update lock's seam,
// at the moment between the verb's read and its write. (Not parallel: the seam is global.)
func TestHostingCLIRetriesAConcurrentEditOnce(t *testing.T) {
	compete := func(pol *policy.Policy, times int) (attempts *atomic.Int32) {
		attempts = new(atomic.Int32)
		var nested atomic.Bool
		policy.SetLockSeam(func() {
			if nested.Swap(true) {
				return // the competing write itself passes through here
			}
			defer nested.Store(false)
			n := int(attempts.Add(1))
			if n > times {
				return
			}
			allow := make([]string, n)
			for i := range allow {
				allow[i] = "SER" + strconv.Itoa(i)
			}
			list, _ := json.Marshal(allow)
			if _, err := pol.Update(context.Background(), updateOf(t, `{"devices":{"allowlist":`+string(list)+`}}`)); err != nil {
				t.Errorf("the competing write failed: %v", err)
			}
		})
		t.Cleanup(func() { policy.SetLockSeam(nil) })
		return attempts
	}

	t.Run("one conflict is retried", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		attempts := compete(pol, 1)
		out := mustVerb(t, pol, "host", "admit", "anna", fpAnna)
		if n := attempts.Load(); n != 2 {
			t.Errorf("policy.update ran %d times; want 2 (the conflict, then the retry)", n)
		}
		g := pol.Get()
		if len(g.Hosting.Peers) != 1 || !reflect.DeepEqual(g.Devices.Allowlist, []string{"SER0"}) {
			t.Errorf("peers %+v, allowlist %v; want the verb's change and the competing one", g.Hosting.Peers, g.Devices.Allowlist)
		}
		if !strings.Contains(out, "admitted anna") {
			t.Errorf("printed %q", out)
		}
	})

	t.Run("a second conflict is reported", func(t *testing.T) {
		pol, _ := hostingPolicy(t)
		attempts := compete(pol, 5)
		code, _, errs := verb(t, pol, "host", "admit", "anna", fpAnna)
		if code != 1 || !strings.Contains(errs, string(capability.ErrPolicyRevisionConflict)) || !strings.Contains(errs, "run the command again") {
			t.Errorf("exit %d, stderr %q; want 1 and %s", code, errs, capability.ErrPolicyRevisionConflict)
		}
		if n := attempts.Load(); n != 2 {
			t.Errorf("policy.update ran %d times; want exactly 2: the first and one retry", n)
		}
		if got := pol.Get().Hosting.Peers; len(got) != 0 {
			t.Errorf("peers = %+v; the verb's change must not be made after a conflict it gave up on", got)
		}
	})
}

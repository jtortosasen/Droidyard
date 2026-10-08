package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// envAt is a real environment whose configuration directory is dir and whose home is
// nowhere, so a test can never touch this machine's configuration.
func envAt(dir string) platform.Env {
	e := platform.Current()
	base := e.Getenv
	e.Getenv = func(k string) string {
		switch k {
		case "DROIDYARD_CONFIG_DIR":
			return dir
		case "HOME":
			return "/nonexistent-droidyard-home"
		}
		return base(k)
	}
	return e
}

// write puts body at <dir>/config.json and returns its path.
func write(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, policy.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s does not parse: %v\n%s", path, err, raw)
	}
	return doc
}

func roots(doc map[string]any) []string {
	var out []string
	if t, ok := doc["trust"].(map[string]any); ok {
		if rs, ok := t["roots"].([]any); ok {
			for _, r := range rs {
				out = append(out, r.(string))
			}
		}
	}
	return out
}

// TestTrustRuleIsAnExactRootOrAPrefixAtABoundary pins the rule (plan m1g D1): a root
// trusts exactly itself; a prefix trusts what is below it at a path-component
// boundary, so /a/b trusts /a/b/c and not /a/bc. Removing the boundary check trusts
// the sibling whose name merely starts with the prefix, which is the failure this test
// exists to catch.
func TestTrustRuleIsAnExactRootOrAPrefixAtABoundary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a/root"],"prefixes":["/a/b"]}}`)
	p := policy.New(envAt(dir))

	for _, c := range []struct {
		root string
		want bool
	}{
		{"/a/root", true},
		{"/a/root/sub", false}, // a root entry is exact
		{"/a/b", true},         // the prefix itself
		{"/a/b/c", true},       // below the prefix
		{"/a/bc", false},       // the boundary that a bare HasPrefix misses
		{"/a/bcd/e", false},
		{"/a/other", false},
	} {
		got, err := p.Trusted(c.root)
		if err != nil {
			t.Fatalf("%s: %v", c.root, err)
		}
		if got != c.want {
			t.Errorf("Trusted(%s) = %v, want %v", c.root, got, c.want)
		}
	}
}

// TestRelativeEntriesFailClosed: a relative entry would canonicalise against "/", so
// "." would trust every root; the file is refused instead and names the entry
// (verifier B1, step 3). The nextStep quotes a root holding a quote shell-safely.
func TestRelativeEntriesFailClosed(t *testing.T) {
	for _, body := range []string{
		`{"schemaVersion":1,"trust":{"prefixes":["."]}}`,
		`{"schemaVersion":1,"trust":{"prefixes":["./"]}}`,
		`{"schemaVersion":1,"trust":{"prefixes":["~/src"]}}`,
		`{"schemaVersion":1,"trust":{"roots":["repo"]}}`,
	} {
		dir := t.TempDir()
		write(t, dir, body)
		got, err := policy.New(envAt(dir)).Trusted("/Users/someone/evil-repo")
		if got || err == nil || !strings.Contains(err.Cause, "not an absolute path") {
			t.Errorf("%s: (%v, %v); want false and an error naming the relative entry", body, got, err)
		}
	}
	next := policy.UntrustedError("/w/it's").NextStep
	if want := "`droidyard trust '/w/it'\\''s'`"; !strings.Contains(next, want) {
		t.Errorf("nextStep = %q; want the trust command %s with the root shell-quoted", next, want)
	}
	// D6: a nextStep says whether repeating the failed call is safe. Trusting the root
	// changes nothing on disk, so this one is safe to repeat (plan m1n, D6).
	if !strings.Contains(next, "safe to repeat") {
		t.Errorf("nextStep = %q; want it to say the call is safe to repeat (plan m1n, D6)", next)
	}
}

// TestMissingFileTrustsNothing: no configuration trusts nothing, and is not an error
// (plan m1g D1).
func TestMissingFileTrustsNothing(t *testing.T) {
	got, err := policy.New(envAt(t.TempDir())).Trusted("/anything")
	if got || err != nil {
		t.Errorf("with no file: (%v, %v); want false, nil", got, err)
	}
}

// TestUnparseableFileTrustsNothingAndNamesTheFile: a file that does not parse trusts
// nothing and returns an error whose cause names the file; it is never renamed or
// rewritten (plan m1g D1).
func TestUnparseableFileTrustsNothingAndNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "{ this is not json")
	before, _ := os.ReadFile(path)

	got, err := policy.New(envAt(dir)).Trusted("/a")
	if got {
		t.Error("an unparseable file trusted a root")
	}
	if err == nil || err.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("err = %v; want WORKSPACE_UNTRUSTED", err)
	}
	if !strings.Contains(err.Cause, path) {
		t.Errorf("cause %q does not name the file %q", err.Cause, path)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("the unparseable file was rewritten:\n%s", after)
	}
}

// TestCacheFollowsSizeAndMtime: the file is re-read when its size or mtime changes, so
// a hand edit or a `droidyard trust` is picked up without a restart (ADR-0015 §6; plan
// m1g D2). A cache that never refreshed would keep answering from the first read.
func TestCacheFollowsSizeAndMtime(t *testing.T) {
	dir := t.TempDir()
	// Same length on purpose: only the mtime can tell these apart.
	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]}}`)
	p := policy.New(envAt(dir))
	if ok, _ := p.Trusted("/a"); !ok {
		t.Fatal("the first entry is not trusted")
	}
	if ok, _ := p.Trusted("/b"); ok {
		t.Fatal("/b is trusted before it is written")
	}

	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/b"]}}`)
	if ok, _ := p.Trusted("/b"); !ok {
		t.Error("a same-size rewrite was not picked up: the cache never refreshed")
	}
	if ok, _ := p.Trusted("/a"); ok {
		t.Error("the removed entry is still trusted")
	}
}

// TestSymlinkRootMatchesItsCanonicalEntry: trust is decided on the canonical root, so
// a root opened through a symlink matches the canonical entry a `droidyard trust`
// wrote, and vice versa (ADR-0021 §Layout; plan m1g check).
func TestSymlinkRootMatchesItsCanonicalEntry(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	canonical := platform.Current().Canonical(real)

	for _, entry := range []string{canonical, link} {
		dir := t.TempDir()
		write(t, dir, `{"schemaVersion":1,"trust":{"roots":[`+quote(t, entry)+`]}}`)
		p := policy.New(envAt(dir))
		for _, query := range []string{real, link, canonical} {
			if ok, err := p.Trusted(query); err != nil || !ok {
				t.Errorf("entry %s: Trusted(%s) = %v, %v; want true", entry, query, ok, err)
			}
		}
	}
}

func quote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTrustAddsTheCanonicalRootAndKeepsUnknownKeys: `trust` canonicalises the root as
// workspace.open does, keeps every key it does not know, and refuses to overwrite a
// file that does not parse (plan m1g D2, D1).
func TestTrustAddsTheCanonicalRootAndKeepsUnknownKeys(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := write(t, dir, `{"schemaVersion":1,"future":{"x":1},"trust":{"roots":["/kept"],"prefixes":["/p"],"unknown":true}}`)

	canonical, err := policy.New(envAt(dir)).Trust(link)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != platform.Current().Canonical(real) {
		t.Errorf("trust canonicalised the root to %q; want %q", canonical, platform.Current().Canonical(real))
	}

	doc := read(t, path)
	if _, ok := doc["future"]; !ok {
		t.Errorf("an unknown top-level key was dropped: %v", doc)
	}
	trust := doc["trust"].(map[string]any)
	if _, ok := trust["unknown"]; !ok {
		t.Errorf("an unknown trust key was dropped: %v", trust)
	}
	if _, ok := trust["prefixes"]; !ok {
		t.Errorf("the prefixes entry was dropped: %v", trust)
	}
	got := roots(doc)
	if !contains(got, "/kept") || !contains(got, canonical) {
		t.Errorf("roots = %v; want the old entry and %s", got, canonical)
	}

	// Trusting the same root again changes nothing.
	before, _ := os.ReadFile(path)
	if _, err := policy.New(envAt(dir)).Trust(link); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("a repeated trust rewrote the file:\nbefore %s\nafter %s", before, after)
	}

	// An unparseable file is refused, not overwritten.
	write(t, dir, "not json at all")
	if _, err := policy.New(envAt(dir)).Trust(real); err == nil {
		t.Error("trust overwrote a file that does not parse")
	}
	if got, _ := os.ReadFile(path); string(got) != "not json at all" {
		t.Errorf("the unparseable file changed: %s", got)
	}
}

// TestConcurrentTrustsBothLand: two `trust` runs racing on the same file both land,
// because each takes the exclusive lock and re-reads. Without the lock the later
// writer's read-modify-write drops the earlier one's root.
func TestConcurrentTrustsBothLand(t *testing.T) {
	dir := t.TempDir()
	one, two := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(one, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(two, 0o700); err != nil {
		t.Fatal(err)
	}
	one, two = platform.Current().Canonical(one), platform.Current().Canonical(two)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, root := range []string{one, two} {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			if _, err := policy.New(envAt(dir)).Trust(root); err != nil {
				errs <- err
			}
		}(root)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	got := roots(read(t, filepath.Join(dir, policy.FileName)))
	if !contains(got, one) || !contains(got, two) {
		t.Errorf("roots = %v; both concurrent trusts must land (%s, %s)", got, one, two)
	}
}

// TestTrustRefusesARelativeRoot: `trust` takes the project root, an absolute path
// (plan m1g D2).
func TestTrustRefusesARelativeRoot(t *testing.T) {
	if _, err := policy.New(envAt(t.TempDir())).Trust("relative/root"); err == nil {
		t.Error("a relative root was accepted")
	}
}

// TestTrustedRefusesADirectoryItCannotResolve: without a configuration directory
// nothing is trusted and the cause says why (plan m1g D1).
func TestTrustedRefusesADirectoryItCannotResolve(t *testing.T) {
	e := platform.Current()
	base := e.Getenv
	e.Getenv = func(k string) string {
		if k == "HOME" || k == "DROIDYARD_CONFIG_DIR" || k == "DROIDYARD_STATE_DIR" {
			return ""
		}
		return base(k)
	}
	got, err := policy.New(e).Trusted("/a")
	if got || err == nil {
		t.Fatalf("with no configuration directory: (%v, %v); want false and an error", got, err)
	}
}

// TestAuthorizeFailsBeforeTheHandler: the hook returns the untrusted error and never
// calls the handler; on a trusted root it returns the canonical root and lets the
// handler run (plan m1g D3). The error's nextStep is the exact command.
func TestAuthorizeFailsBeforeTheHandler(t *testing.T) {
	dir := t.TempDir()
	p := policy.New(envAt(dir))
	hook := p.Authorize(func(*capability.Call) (string, bool) { return "/wanted", true })

	_, err := hook(t.Context(), &capability.Call{})
	if err == nil {
		t.Fatal("an untrusted root was authorized")
	}
	untrusted, ok := err.(*capability.Error)
	if !ok || untrusted.Code != capability.ErrWorkspaceUntrusted {
		t.Fatalf("err = %v; want WORKSPACE_UNTRUSTED", err)
	}
	if untrusted.NextStep != "ask the user to run `droidyard trust '/wanted'`; nothing ran, so this call is safe to repeat once the root is trusted." {
		t.Errorf("nextStep = %q; want the exact trust command", untrusted.NextStep)
	}

	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/wanted"]}}`)
	got, err := p.Authorize(func(*capability.Call) (string, bool) { return "/wanted", true })(t.Context(), &capability.Call{})
	if err != nil || got != "/wanted" {
		t.Errorf("a trusted root: (%v, %v); want the canonical root and no error", got, err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// full is ADR-0032 §1's example, with every section set to a non-default value so a
// reader that drops a section is visible.
const full = `{
  "schemaVersion": 1,
  "trust":   { "roots": ["/w/root"], "prefixes": ["/w/pre"] },
  "devices": { "allowPhysical": true, "allowlist": ["SER1"] },
  "shell":   { "enabled": true, "roots": ["/w/sh"] },
  "gradle":  { "sidecarJavaHome": "/jdk/17", "allowBelowFloor": true },
  "tools":   { "adb": "/sdk/adb", "emulator": "/sdk/emu" }
}`

// TestVersion1ExampleParses: ADR-0032 §1's example parses, every section reaches its
// query, and the tool keys are read at construction (plan m1k D1). A reader that accepts
// only `trust` would pass the old tests and fail here.
func TestVersion1ExampleParses(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, full)
	p := policy.New(envAt(dir))

	if ok, err := p.Trusted("/w/root"); err != nil || !ok {
		t.Errorf("Trusted(/w/root) = %v, %v; want true", ok, err)
	}
	if ok, err := p.Trusted("/w/pre/x"); err != nil || !ok {
		t.Errorf("Trusted(/w/pre/x) = %v, %v; want true", ok, err)
	}
	if ok, err := p.ShellAllowed("/w/sh"); err != nil || !ok {
		t.Errorf("ShellAllowed(/w/sh) = %v, %v; want true (globally enabled)", ok, err)
	}
	if got := p.PhysicalAdmitted("SER1"); !got.Admitted {
		t.Errorf("PhysicalAdmitted(SER1) = %+v; want admitted", got)
	}
	if got := p.PhysicalAdmitted("SER2"); got.Admitted || got.Cause == "" {
		t.Errorf("PhysicalAdmitted(SER2) = %+v; want refused with a cause", got)
	}
	if got := p.Startup(); got.State != policy.StateOK || got.ADB != "/sdk/adb" || got.Emulator != "/sdk/emu" ||
		got.SidecarJavaHome != "/jdk/17" || !got.AllowBelowFloor {
		t.Errorf("Startup() = %+v; want the file's keys and state ok", got)
	}
	// Decision (plan m1k D9): the below-floor hatch is read at start, and the file it
	// points a human at is named.
	if got := p.Startup().BelowFloorKey(); !got.Allowed || got.ConfigPath != filepath.Join(dir, policy.FileName) {
		t.Errorf("BelowFloorKey() = %+v; want the hatch admitted and the file named", got)
	}
}

// TestInvalidFileFailsClosedForEverySection: a file that is invalid — a wrong type, a
// relative path, a newer version — trusts nothing (naming the file), keeps shell.exec
// off, admits no physical device and uses no tool key (ADR-0032 §4; plan m1k D1). Each
// row is one of §4's refusals: a reader that validated only one would let the others
// through.
func TestInvalidFileFailsClosedForEverySection(t *testing.T) {
	cases := []struct {
		key  string
		body string
	}{
		{"allowPhysical: \"yes\"", `{"schemaVersion":1,"devices":{"allowPhysical":"yes"}}`},
		{"allowlist: [1]", `{"schemaVersion":1,"devices":{"allowlist":[1]}}`},
		{"shell.roots: [\"rel\"]", `{"schemaVersion":1,"shell":{"roots":["rel"]}}`},
		{"gradle.sidecarJavaHome: \"jdk\"", `{"schemaVersion":1,"gradle":{"sidecarJavaHome":"jdk"}}`},
		{"tools.adb: \"adb\"", `{"schemaVersion":1,"tools":{"adb":"adb"}}`},
		{"schemaVersion: 2", `{"schemaVersion":2}`},
		// Sections that would grant everything, beside one invalid key: nothing is granted.
		{"granting sections beside tools.adb: \"adb\"", `{"schemaVersion":1,"trust":{"roots":["/w/root"]},"shell":{"enabled":true},"devices":{"allowPhysical":true,"allowlist":["SER"]},"tools":{"adb":"adb"}}`},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			dir := t.TempDir()
			path := write(t, dir, c.body)
			p := policy.New(envAt(dir))

			trusted, err := p.Trusted("/w/root")
			if trusted || err == nil || !strings.Contains(err.Cause, path) {
				t.Errorf("Trusted = %v, %v; want false and a cause naming %s", trusted, err, path)
			}
			shell, serr := p.ShellAllowed("/w/root")
			if shell || serr == nil || serr.Code != capability.ErrPolicyShellDisabled || !strings.Contains(serr.Cause, path) {
				t.Errorf("ShellAllowed = %v, %v; want false and POLICY_SHELL_DISABLED naming %s", shell, serr, path)
			}
			if got := p.PhysicalAdmitted("SER"); got.Admitted || !strings.Contains(got.Cause, path) {
				t.Errorf("PhysicalAdmitted = %+v; want refused with a cause naming %s", got, path)
			}
			if got := p.Startup(); got.State != policy.StateInvalid || got.ADB != "" {
				t.Errorf("Startup = %+v; want state invalid and no tool key used", got)
			}
			// Decision (plan m1k D9; ADR-0032 §4): an invalid file uses no tool key, so
			// the below-floor hatch is not admitted either.
			if got := p.Startup().BelowFloorKey(); got.Allowed {
				t.Errorf("BelowFloorKey() = %+v; want the hatch not admitted from an invalid file", got)
			}
		})
	}
}

// TestNullValuesAreTheDefaults: a `null` value is its default, and so is a missing key
// (ADR-0032 §1). Get reports the defaults rather than nulls.
func TestNullValuesAreTheDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"trust":null,"devices":{"allowPhysical":null,"allowlist":null},"shell":null,"gradle":{"sidecarJavaHome":null},"tools":{"adb":null}}`)
	p := policy.New(envAt(dir))

	got := p.Get()
	if got.State != policy.StateOK {
		t.Fatalf("state = %q; want ok (nulls are valid)", got.State)
	}
	if len(got.Trust.Roots) != 0 || len(got.Trust.Prefixes) != 0 || got.Devices.AllowPhysical ||
		len(got.Devices.Allowlist) != 0 || got.Shell.Enabled || len(got.Shell.Roots) != 0 {
		t.Errorf("Get() = %+v; want every section at its default", got)
	}
	if s := p.Startup(); s.State != policy.StateOK || s.ADB != "" || s.Emulator != "" || s.SidecarJavaHome != "" || s.AllowBelowFloor {
		t.Errorf("Startup() = %+v; want state ok and no keys", s)
	}
}

// TestMissingFileIsTheDefaults: with no file every section is at its default and the
// state is missing — not an error (ADR-0031 §1; plan m1k D1, D2).
func TestMissingFileIsTheDefaults(t *testing.T) {
	p := policy.New(envAt(t.TempDir()))
	if got := p.Get(); got.State != policy.StateMissing {
		t.Errorf("state = %q; want missing", got.State)
	}
	if got := p.Startup(); got.State != policy.StateMissing {
		t.Errorf("toolKeys state = %q; want missing", got.State)
	}
	if got, err := p.ShellAllowed("/a"); got || err == nil || err.Code != capability.ErrPolicyShellDisabled {
		t.Errorf("ShellAllowed with no file = %v, %v; want false and POLICY_SHELL_DISABLED", got, err)
	}
	if got := p.PhysicalAdmitted("SER"); got.Admitted {
		t.Errorf("PhysicalAdmitted with no file = %+v; want refused", got)
	}
}

// TestShellAllowedFollowsEnabledOrTheRootAtABoundary: shell.exec is on globally when
// `shell.enabled`, or for a root equal to or under a `shell.roots` entry at a
// path-component boundary (ADR-0032 §2; plan m1k D4). The boundary is the failure a
// bare HasPrefix hides.
func TestShellAllowedFollowsEnabledOrTheRootAtABoundary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"shell":{"enabled":false,"roots":["/w/roots/a"]}}`)
	p := policy.New(envAt(dir))

	for _, c := range []struct {
		root string
		want bool
	}{
		{"/w/roots/a", true},   // the entry itself
		{"/w/roots/a/b", true}, // below it
		{"/w/roots/a b", false},
		{"/w/roots/ab", false}, // the boundary a bare HasPrefix misses
		{"/w/other", false},
	} {
		got, err := p.ShellAllowed(c.root)
		if got != c.want || (got == false && err == nil) {
			t.Errorf("ShellAllowed(%s) = %v, %v; want %v", c.root, got, err, c.want)
		}
	}

	write(t, dir, `{"schemaVersion":1,"shell":{"enabled":true}}`)
	if got, err := p.ShellAllowed("/anywhere"); !got || err != nil {
		t.Errorf("ShellAllowed with enabled: %v, %v; want true (no restart needed)", got, err)
	}
}

// TestPhysicalAdmittedNeedsBothTheFlagAndAnExactSerial: a physical device is admitted
// only when `allowPhysical` and an exact `allowlist` entry both admit it (ADR-0032 §2;
// plan m1k D7). The cause names the condition that failed.
func TestPhysicalAdmittedNeedsBothTheFlagAndAnExactSerial(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"devices":{"allowPhysical":false,"allowlist":["SER1"]}}`)
	if got := policy.New(envAt(dir)).PhysicalAdmitted("SER1"); got.Admitted || !strings.Contains(got.Cause, "allowPhysical") {
		t.Errorf("flag off, serial listed: %+v; want refused naming allowPhysical", got)
	}

	write(t, dir, `{"schemaVersion":1,"devices":{"allowPhysical":true,"allowlist":["SER1"]}}`)
	p := policy.New(envAt(dir))
	if got := p.PhysicalAdmitted("SER1"); !got.Admitted {
		t.Errorf("flag on, serial listed: %+v; want admitted", got)
	}
	if got := p.PhysicalAdmitted("SER2"); got.Admitted || !strings.Contains(got.Cause, "allowlist") {
		t.Errorf("flag on, serial not listed: %+v; want refused naming allowlist", got)
	}
}

// TestTrustKeepsEverySectionAndUnknownKeys: `droidyard trust` edits only the trust
// section and keeps every other section and every unknown key, top-level and inside a
// known section, byte-for-byte (ADR-0032 §1; plan m1k D1). A Trust that rebuilt the file
// from the trust section alone would drop `devices`, `shell`, `gradle` and `tools`.
func TestTrustKeepsEverySectionAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, `{
  "schemaVersion": 1,
  "future": {"x": 1},
  "trust":   {"roots": ["/kept"], "prefixes": ["/p"], "unknown": true},
  "devices": {"allowPhysical": true, "allowlist": ["SER"], "extra": 1},
  "shell":   {"enabled": true, "roots": ["/s"]},
  "gradle":  {"sidecarJavaHome": null, "allowBelowFloor": true},
  "tools":   {"adb": "/usr/bin/adb", "emulator": null}
}`)
	before := read(t, path)

	canonical, err := policy.New(envAt(dir)).Trust("/new-root")
	if err != nil {
		t.Fatal(err)
	}
	after := read(t, path)

	for _, key := range []string{"future", "devices", "shell", "gradle", "tools"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Errorf("%s changed by Trust:\nbefore %#v\nafter  %#v", key, before[key], after[key])
		}
	}
	trust := after["trust"].(map[string]any)
	if trust["unknown"] != true {
		t.Errorf("an unknown trust key was dropped: %v", trust)
	}
	if !reflect.DeepEqual(trust["prefixes"], before["trust"].(map[string]any)["prefixes"]) {
		t.Errorf("prefixes changed: %v", trust["prefixes"])
	}
	got := roots(after)
	if !contains(got, "/kept") || !contains(got, canonical) {
		t.Errorf("roots = %v; want /kept and %s", got, canonical)
	}
}

// TestToolKeysAreReadOnceButTrustIsLive: the tool keys take effect at the next daemon
// start, so an edit after construction does not change Startup; a live check reads the
// new file (ADR-0032 §6; plan m1k D1).
func TestToolKeysAreReadOnceButTrustIsLive(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/a"]},"tools":{"adb":"/old/adb"}}`)
	p := policy.New(envAt(dir))
	if got := p.Startup().ADB; got != "/old/adb" {
		t.Fatalf("Startup().ADB = %q; want /old/adb", got)
	}

	write(t, dir, `{"schemaVersion":1,"trust":{"roots":["/b"]},"tools":{"adb":"/new/adb"}}`)
	if ok, _ := p.Trusted("/b"); !ok {
		t.Error("a live check did not see the edited trust")
	}
	if got := p.Startup().ADB; got != "/old/adb" {
		t.Errorf("Startup().ADB = %q after the edit; want /old/adb (read once)", got)
	}
}

// Decision (m1k §Changes, step 1 N8): the file is read without blocking and within a
// bound. A FIFO at the path, or a file over 1 MiB, is an invalid configuration, never a
// hang and never the defaults.
func TestConfigFileIsReadBoundedAndWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, policy.FileName)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip("no FIFO here:", err)
	}
	done := make(chan *policy.Policy, 1)
	go func() { done <- policy.New(envAt(dir)) }()
	var p *policy.Policy
	select {
	case p = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO at the configuration path blocked")
	}
	if got := p.Startup(); got.State != policy.StateInvalid {
		t.Errorf("a FIFO configuration: startup state %q; want invalid", got.State)
	}
	if trusted, err := p.Trusted("/w/root"); trusted || err == nil {
		t.Errorf("a FIFO configuration: Trusted = %v, %v; want refused", trusted, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	big := `{"schemaVersion":1,"pad":"` + strings.Repeat("x", 1<<20) + `"}`
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := policy.New(envAt(dir)).Startup(); got.State != policy.StateInvalid || !strings.Contains(got.Error, "larger than") {
		t.Errorf("a 1 MiB+ configuration: startup %+v; want invalid, larger than the bound", got)
	}
}

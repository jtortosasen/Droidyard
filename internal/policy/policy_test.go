package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	if got, want := policy.UntrustedError("/w/it's").NextStep, "ask the user to run `droidyard trust '/w/it'\\''s'`"; got != want {
		t.Errorf("nextStep = %q; want %q", got, want)
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
	if untrusted.NextStep != "ask the user to run `droidyard trust '/wanted'`" {
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

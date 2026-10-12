package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests drive install.sh the way a user does, in a world made only of fakes: PATH
// is a directory of fake uname, gh and curl in front of a directory of symlinks to an
// allowlist of real tools, HOME and the install directory are temporary, and the
// "releases" are archives built here. Nothing reaches the network, a real PATH
// directory or a daemon. The decisions under test are those of ADR-0068 §5 and the
// plan's step 5; each test says which one.

const repo = "jtortosasen/Droidyard"

// toolAllowlist is every real command install.sh may use. Anything else it calls is
// "command not found", so the script cannot lean on a tool the plan did not name.
var toolAllowlist = []string{
	"sh", "tar", "gzip", "mktemp", "cp", "mv", "chmod", "mkdir", "rm", "cat", "grep",
	"sed", "awk", "cut", "tr", "head", "dirname", "ln", "sha256sum", "shasum",
}

var fakeUname = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/uname.log"
case "$1" in
-s) echo "$FAKE_UNAME_S" ;;
-m) echo "$FAKE_UNAME_M" ;;
*) exit 64 ;;
esac
`

// fakeGh serves what the plan names: auth status, release view, release download. Any
// other argument shape is exit 64, so the script cannot drift from the spelled-out calls.
var fakeGh = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/gh.log"
case "$1 $2" in
"auth status")
	[ "$*" = "auth status --hostname github.com --active" ] || exit 64
	exit "${FAKE_GH_AUTH:-0}"
	;;
"release view")
	[ "$*" = "release view --repo github.com/` + repo + ` --json tagName -q .tagName" ] || exit 64
	[ -n "${FAKE_LATEST:-}" ] || { echo "release not found" >&2; exit 1; }
	echo "$FAKE_LATEST"
	exit 0
	;;
"release download")
	tag=$3
	shift 3
	r=
	d=
	pats=
	while [ $# -gt 0 ]; do
		case "$1" in
		--repo) r=$2; shift 2 ;;
		--pattern) pats="$pats $2"; shift 2 ;;
		--dir) d=$2; shift 2 ;;
		*) echo "fake gh: unexpected $1" >&2; exit 64 ;;
		esac
	done
	[ "$r" = github.com/` + repo + ` ] || exit 64
	[ -n "$d" ] || exit 64
	for p in $pats; do
		[ -f "$FAKE_FIXTURES/$tag/$p" ] || { echo "no asset matches $p" >&2; exit 1; }
		cp "$FAKE_FIXTURES/$tag/$p" "$d/$p"
	done
	exit 0
	;;
esac
exit 64
`

// fakeCurl serves the latest-release redirect and the release asset URLs, and fails on demand.
var fakeCurl = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/curl.log"
if [ -n "${FAKE_CURL_FAIL:-}" ]; then
	echo "curl: (22) The requested URL returned error: 404" >&2
	exit 22
fi
fmt=
out=
url=
while [ $# -gt 0 ]; do
	case "$1" in
	-w) fmt=$2; shift 2 ;;
	-o) out=$2; shift 2 ;;
	-*) shift ;;
	*) url=$1; shift ;;
	esac
done
if [ -n "$fmt" ]; then
	[ "$fmt" = '%{url_effective}' ] || exit 64
	[ "$url" = https://github.com/` + repo + `/releases/latest ] || exit 22
	[ -n "${FAKE_LATEST_URL:-}" ] || exit 22
	printf '%s' "$FAKE_LATEST_URL"
	exit 0
fi
pre=https://github.com/` + repo + `/releases/download/
case "$url" in
"$pre"*) rest=${url#"$pre"} ;;
*) exit 22 ;;
esac
[ -f "$FAKE_FIXTURES/$rest" ] || exit 22
cp "$FAKE_FIXTURES/$rest" "$out"
`

// fakeMvFails makes the rename step fail after the copy beside the target exists.
var fakeMvFails = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/mv.log"
[ -f "$2" ] && echo "source-exists" >> "$FAKE_CALLS/mv.log"
exit 1
`

type harness struct {
	t        *testing.T
	shell    string
	script   string
	root     string
	bin      string
	tools    string
	fixtures string
	calls    string
	home     string
	dir      string
	tmpdir   string
	vars     map[string]string // extra environment; an empty value unsets
	pathTail string            // appended to PATH, after the tools
	profiles map[string]string
}

// newHarness builds the hermetic world. skip names allowlisted tools to leave out.
func newHarness(t *testing.T, shell string, skip ...string) *harness {
	t.Helper()
	// Every case has its own directories; most of a case's time is the host scanning
	// the scripts it has just written, so the cases run side by side.
	t.Parallel()
	script, err := filepath.Abs("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h := &harness{
		t: t, shell: shell, script: script, root: root,
		bin:      filepath.Join(root, "bin"),
		tools:    filepath.Join(root, "tools"),
		fixtures: filepath.Join(root, "fixtures"),
		calls:    filepath.Join(root, "calls"),
		home:     filepath.Join(root, "home"),
		dir:      filepath.Join(root, "install-dir"),
		tmpdir:   filepath.Join(root, "tmp"),
		vars: map[string]string{
			"FAKE_UNAME_S": "Darwin",
			"FAKE_UNAME_M": "arm64",
			"SHELL":        "/bin/zsh",
		},
		profiles: map[string]string{
			".zshrc":   "# zsh profile\nalias z=1\n",
			".bashrc":  "# bash profile\nalias b=1\n",
			".profile": "# sh profile\nalias p=1\n",
		},
	}
	for _, d := range []string{h.bin, h.tools, h.fixtures, h.calls, h.home, h.tmpdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	for _, name := range toolAllowlist {
		if skipped[name] {
			continue
		}
		p, err := exec.LookPath(name)
		if err != nil {
			continue // sha256sum or shasum may be absent on a host; the others are checked below
		}
		if err := os.Symlink(p, filepath.Join(h.tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"sh", "tar", "gzip", "mktemp", "cp", "mv", "chmod", "mkdir", "rm", "awk"} {
		if _, err := os.Lstat(filepath.Join(h.tools, name)); err != nil && !skipped[name] {
			t.Fatalf("host has no %s", name)
		}
	}
	h.fake("uname", fakeUname)
	h.fake("gh", fakeGh)
	h.fake("curl", fakeCurl)
	for name, text := range h.profiles {
		if err := os.WriteFile(filepath.Join(h.home, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// sharedFakes holds each fake once per test run; a harness's bin directory links to them.
// The fakes do not depend on the case (they read their scenario from the environment),
// and the first execution of every new file costs a host scan, which adds up over cases.
var (
	sharedMu    sync.Mutex
	sharedDir   string
	sharedFiles = map[string]string{}
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedDir != "" {
		os.RemoveAll(sharedDir)
	}
	os.Exit(code)
}

func (h *harness) fake(name, text string) {
	h.t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedDir == "" {
		d, err := os.MkdirTemp("", "install-fakes-")
		if err != nil {
			h.t.Fatal(err)
		}
		sharedDir = d
	}
	key := name + "\x00" + text
	path, ok := sharedFiles[key]
	if !ok {
		path = filepath.Join(sharedDir, fmt.Sprintf("%s.%d", name, len(sharedFiles)))
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			h.t.Fatal(err)
		}
		sharedFiles[key] = path
	}
	link := filepath.Join(h.bin, name)
	os.Remove(link)
	if err := os.Symlink(path, link); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) without(names ...string) {
	h.t.Helper()
	for _, n := range names {
		if err := os.Remove(filepath.Join(h.bin, n)); err != nil {
			h.t.Fatal(err)
		}
	}
	h.assertAbsent(names...)
}

func (h *harness) set(k, v string) { h.vars[k] = v }

func (h *harness) path() string {
	p := h.bin + string(os.PathListSeparator) + h.tools
	if h.pathTail != "" {
		p += string(os.PathListSeparator) + h.pathTail
	}
	return p
}

func (h *harness) environ() []string {
	m := map[string]string{
		"PATH":                  h.path(),
		"HOME":                  h.home,
		"TMPDIR":                h.tmpdir,
		"FAKE_CALLS":            h.calls,
		"FAKE_FIXTURES":         h.fixtures,
		"DROIDYARD_INSTALL_DIR": h.dir,
	}
	for k, v := range h.vars {
		m[k] = v
	}
	var env []string
	for k, v := range m {
		if v != "" { // an empty value in vars unsets the variable
			env = append(env, k+"="+v)
		}
	}
	return env
}

// assertAbsent proves the hermetic PATH cannot find the named commands, so a case that
// removes gh or curl is not silently running with the host's.
func (h *harness) assertAbsent(names ...string) {
	h.t.Helper()
	for _, n := range names {
		cmd := exec.Command(h.shell, "-c", "command -v "+n)
		cmd.Env = h.environ()
		if out, err := cmd.CombinedOutput(); err == nil {
			h.t.Fatalf("command -v %s succeeds on the hermetic PATH: %s", n, out)
		}
	}
}

type result struct {
	out  string // stdout and stderr together
	code int
}

// run executes install.sh (or the given bytes on stdin when piped is not nil) and then
// checks what every run must leave alone: the shell profiles, and the scratch directory.
func (h *harness) run() result { return h.exec(nil) }

func (h *harness) exec(piped []byte) result {
	h.t.Helper()
	var cmd *exec.Cmd
	if piped == nil {
		cmd = exec.Command(h.shell, h.script)
	} else {
		cmd = exec.Command(h.shell)
		cmd.Stdin = bytes.NewReader(piped)
	}
	cmd.Dir = h.root
	cmd.Env = h.environ()
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			h.t.Fatalf("running the script: %v", err)
		}
		code = ee.ExitCode()
	}
	for name, want := range h.profiles {
		got, err := os.ReadFile(filepath.Join(h.home, name))
		if err != nil || string(got) != want {
			h.t.Errorf("profile %s changed (the installer edits no profile): %q, %v", name, got, err)
		}
	}
	if left, _ := os.ReadDir(h.tmpdir); len(left) != 0 {
		h.t.Errorf("scratch directory not removed: %v", left)
	}
	return result{out: string(out), code: code}
}

func (h *harness) log(name string) string {
	b, _ := os.ReadFile(filepath.Join(h.calls, name+".log"))
	return string(b)
}

// binary builds a fake droidyard: it logs "<label> <args>" and answers version and status.
func binary(label, versionOut string, status int) []byte {
	return []byte(fmt.Sprintf(`#!/bin/sh
echo "%s $*" >> "$FAKE_CALLS/droidyard.log"
case "$1" in
version) echo '%s' ;;
status) exit %d ;;
esac
exit 0
`, label, versionOut, status))
}

type release struct {
	versionOut string   // default "droidyard <tag> (API 1.0)"
	status     int      // the new binary's `status` exit
	sumLine    string   // "bad", "garbage", "missing", "twice" or "" for a correct line
	raw        []byte   // the droidyard bytes instead of a fake script
	symlinkTo  string   // the droidyard member is a symlink to this path
	extra      []string // further regular members, named as given
}

type published struct {
	bin     []byte
	archive []byte
	sum     string
}

// publish writes <fixtures>/<tag>/droidyard_<tag>_<os>_<arch>.tar.gz and checksums.txt.
func (h *harness) publish(tag, goos, goarch string, o release) published {
	h.t.Helper()
	if o.versionOut == "" {
		o.versionOut = "droidyard " + tag + " (API 1.0)"
	}
	bin := o.raw
	if bin == nil {
		bin = binary("new", o.versionOut, o.status)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if o.symlinkTo != "" {
		if err := tw.WriteHeader(&tar.Header{Name: "droidyard", Mode: 0o755, Linkname: o.symlinkTo, Typeflag: tar.TypeSymlink}); err != nil {
			h.t.Fatal(err)
		}
	} else {
		if err := tw.WriteHeader(&tar.Header{Name: "droidyard", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}); err != nil {
			h.t.Fatal(err)
		}
		if _, err := tw.Write(bin); err != nil {
			h.t.Fatal(err)
		}
	}
	for _, x := range o.extra {
		if err := tw.WriteHeader(&tar.Header{Name: x, Mode: 0o644, Size: 4, Typeflag: tar.TypeReg}); err != nil {
			h.t.Fatal(err)
		}
		if _, err := tw.Write([]byte("evil")); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		h.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		h.t.Fatal(err)
	}
	name := fmt.Sprintf("droidyard_%s_%s_%s.tar.gz", tag, goos, goarch)
	sum := sha256.Sum256(buf.Bytes())
	hexsum := hex.EncodeToString(sum[:])
	other := strings.Repeat("ab", 32)
	// A decoy that contains the archive's name but is not it, ahead of the real line:
	// the expected sum is the line whose name field equals the archive's name.
	lines := []string{other + "  x" + name, other + "  " + name + ".sig"}
	switch o.sumLine {
	case "":
		lines = append(lines, hexsum+"  "+name)
	case "bad":
		lines = append(lines, strings.Repeat("0", 64)+"  "+name)
	case "garbage":
		lines = append(lines, "not-a-sum  "+name)
	case "twice":
		lines = append(lines, hexsum+"  "+name, strings.Repeat("0", 64)+"  "+name)
	case "missing":
	default:
		h.t.Fatalf("unknown sumLine %q", o.sumLine)
	}
	lines = append(lines, other+"  droidyard_"+tag+"_plan9_mips.tar.gz")
	dir := filepath.Join(h.fixtures, tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return published{bin: bin, archive: buf.Bytes(), sum: hexsum}
}

// installOld puts a fake droidyard in the install directory and returns its bytes.
func (h *harness) installOld(versionOut string, status int) []byte {
	h.t.Helper()
	b := binary("old", versionOut, status)
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "droidyard"), b, 0o755); err != nil {
		h.t.Fatal(err)
	}
	return b
}

func (h *harness) installed() []byte {
	b, err := os.ReadFile(filepath.Join(h.dir, "droidyard"))
	if err != nil {
		return nil
	}
	return b
}

func (h *harness) noScratch() {
	h.t.Helper()
	m, _ := filepath.Glob(filepath.Join(h.dir, ".droidyard.new.*"))
	if len(m) != 0 {
		h.t.Errorf("half-written copies left beside the target: %v", m)
	}
}

// shells returns the shells every case runs under: the host's sh, and dash when it is
// installed, since dash is the strictest POSIX sh in common use.
func shells(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal("no sh on PATH")
	}
	out["sh"] = sh
	if d, err := exec.LookPath("dash"); err == nil {
		out["dash"] = d
	}
	return out
}

// each runs a case under every shell, as a subtest.
func each(t *testing.T, f func(t *testing.T, shell string)) {
	t.Helper()
	t.Parallel()
	for name, path := range shells(t) {
		t.Run(name, func(t *testing.T) { f(t, path) })
	}
}

func mustContain(t *testing.T, what, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(text, s) {
			t.Errorf("%s lacks %q:\n%s", what, s, text)
		}
	}
}

func mustNotContain(t *testing.T, what, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(text, s) {
			t.Errorf("%s contains %q:\n%s", what, s, text)
		}
	}
}

func ghDownloadCalls(h *harness) int { return strings.Count(h.log("gh"), "release download") }
func curlDownloadCalls(h *harness) int {
	return strings.Count(h.log("curl"), "/releases/download/")
}

// Decision (ADR-0068 §5, plan step 5.2 and 5.6): gh when it is authenticated, else curl,
// from the release URLs the plan spells out.
func TestFreshInstall(t *testing.T) {
	const tag = "v1.0.0"
	archive := "droidyard_v1.0.0_darwin_arm64.tar.gz"
	each(t, func(t *testing.T, shell string) {
		t.Run("through gh", func(t *testing.T) {
			h := newHarness(t, shell)
			h.without("curl")
			p := h.publish(tag, "darwin", "arm64", release{})
			h.set("FAKE_LATEST", tag)
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			mustContain(t, "output", r.out, "droidyard: installing "+tag)
			mustNotContain(t, "output", r.out, "installed v")
			if !bytes.Equal(h.installed(), p.bin) {
				t.Errorf("installed bytes differ from the archive's droidyard")
			}
			if fi, err := os.Stat(filepath.Join(h.dir, "droidyard")); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("mode = %v, %v; want 0755", fi, err)
			}
			g := h.log("gh")
			mustContain(t, "gh log", g,
				"auth status --hostname github.com --active\n",
				"release view --repo github.com/"+repo+" --json tagName -q .tagName\n",
				"release download "+tag+" --repo github.com/"+repo+" --pattern "+archive+" --pattern checksums.txt --dir ")
			h.noScratch()
		})
		t.Run("through curl, no gh", func(t *testing.T) {
			h := newHarness(t, shell)
			h.without("gh")
			p := h.publish(tag, "darwin", "arm64", release{})
			h.set("FAKE_LATEST_URL", "https://github.com/"+repo+"/releases/tag/"+tag)
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if !bytes.Equal(h.installed(), p.bin) {
				t.Errorf("installed bytes differ")
			}
			c := h.log("curl")
			base := "https://github.com/" + repo + "/releases/"
			mustContain(t, "curl log", c,
				"-fsSLI -o /dev/null -w %{url_effective} "+base+"latest\n",
				"-fsSL -o ", " "+base+"download/"+tag+"/"+archive+"\n", " "+base+"download/"+tag+"/checksums.txt\n")
		})
		t.Run("gh unauthenticated falls back to curl", func(t *testing.T) {
			h := newHarness(t, shell)
			p := h.publish(tag, "darwin", "arm64", release{})
			h.set("FAKE_GH_AUTH", "1")
			h.set("FAKE_LATEST_URL", "https://github.com/"+repo+"/releases/tag/"+tag)
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if !bytes.Equal(h.installed(), p.bin) {
				t.Errorf("installed bytes differ")
			}
			if g := h.log("gh"); g != "auth status --hostname github.com --active\n" {
				t.Errorf("gh was used beyond the auth probe:\n%s", g)
			}
			if curlDownloadCalls(h) != 2 {
				t.Errorf("curl did not download the archive and checksums:\n%s", h.log("curl"))
			}
		})
		t.Run("default directory is ~/.local/bin", func(t *testing.T) {
			h := newHarness(t, shell)
			p := h.publish(tag, "darwin", "arm64", release{})
			h.set("FAKE_LATEST", tag)
			h.set("DROIDYARD_INSTALL_DIR", "")
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			got, err := os.ReadFile(filepath.Join(h.home, ".local", "bin", "droidyard"))
			if err != nil || !bytes.Equal(got, p.bin) {
				t.Errorf("not installed to $HOME/.local/bin: %v", err)
			}
		})
	})
}

// Decision (plan 5.2): with neither transport the script says it needs gh or curl, and
// does not fall through to a curl call that is not there.
func TestNeitherTransport(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		for name, setup := range map[string]func(h *harness){
			"no gh, no curl":              func(h *harness) { h.without("gh", "curl") },
			"gh unauthenticated, no curl": func(h *harness) { h.without("curl"); h.set("FAKE_GH_AUTH", "1") },
		} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, shell)
				h.publish("v1.0.0", "darwin", "arm64", release{})
				h.set("FAKE_LATEST", "v1.0.0")
				setup(h)
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				mustContain(t, "output", r.out, "gh or curl")
				mustNotContain(t, "output", r.out, "private repository")
				if h.installed() != nil {
					t.Error("installed something")
				}
				if ghDownloadCalls(h) != 0 {
					t.Error("gh downloaded")
				}
			})
		}
	})
}

// Decision (plan 5.2): a failing curl says a private repository needs gh, and installs nothing.
func TestCurlFailureNamesGh(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		for name, pin := range map[string]string{"latest": "", "pinned": "v1.0.0"} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, shell)
				h.without("gh")
				h.publish("v1.0.0", "darwin", "arm64", release{})
				h.set("FAKE_CURL_FAIL", "1")
				h.set("DROIDYARD_VERSION", pin)
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				mustContain(t, "output", r.out, "private repository", "gh installed and authenticated")
				if h.installed() != nil {
					t.Error("installed something")
				}
			})
		}
	})
}

// Decision (plan 5.3): DROIDYARD_VERSION is used without asking for the latest, and every
// tag, given or discovered, must be vX.Y.Z.
func TestTargetSelection(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		t.Run("pinned via gh does not query latest", func(t *testing.T) {
			h := newHarness(t, shell)
			p := h.publish("v1.2.3", "darwin", "arm64", release{})
			h.set("FAKE_LATEST", "v9.0.0") // would be wrong, and is not published
			h.set("DROIDYARD_VERSION", "v1.2.3")
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if !bytes.Equal(h.installed(), p.bin) {
				t.Error("pinned release not installed")
			}
			mustNotContain(t, "gh log", h.log("gh"), "release view")
		})
		t.Run("pinned via curl does not query latest", func(t *testing.T) {
			h := newHarness(t, shell)
			h.without("gh")
			h.publish("v1.2.3", "darwin", "arm64", release{})
			h.set("DROIDYARD_VERSION", "v1.2.3")
			if r := h.run(); r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			mustNotContain(t, "curl log", h.log("curl"), "releases/latest")
		})
		for _, pin := range []string{"1.0.0", "v1.0", "v1.0.0-rc1", "v1.0.0.1", "latest", "v1..0", "v1.0.0\nv1.0.1", "../v1.0.0", "v1.0.x"} {
			t.Run(fmt.Sprintf("malformed pin %q is refused", pin), func(t *testing.T) {
				h := newHarness(t, shell)
				h.publish("v1.0.0", "darwin", "arm64", release{})
				h.set("DROIDYARD_VERSION", pin)
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				if ghDownloadCalls(h) != 0 || curlDownloadCalls(h) != 0 || h.installed() != nil {
					t.Error("downloaded or installed after a malformed pin")
				}
			})
		}
		for name, tc := range map[string]struct {
			noGh bool
			vars map[string]string
		}{
			"curl discovers releases (no release)": {true, map[string]string{"FAKE_LATEST_URL": "https://github.com/" + repo + "/releases"}},
			"curl discovers a non-tag":             {true, map[string]string{"FAKE_LATEST_URL": "https://github.com/" + repo + "/releases/tag/nightly"}},
			"gh discovers a non-tag":               {false, map[string]string{"FAKE_LATEST": "v1.0.0-rc1"}},
			"gh discovers latest":                  {false, map[string]string{"FAKE_LATEST": "latest"}},
		} {
			t.Run(name+" is refused", func(t *testing.T) {
				h := newHarness(t, shell)
				if tc.noGh {
					h.without("gh")
				}
				h.publish("v1.0.0", "darwin", "arm64", release{})
				for k, v := range tc.vars {
					h.set(k, v)
				}
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				if ghDownloadCalls(h) != 0 || curlDownloadCalls(h) != 0 || h.installed() != nil {
					t.Error("downloaded or installed after a discovered non-tag")
				}
			})
		}
	})
}

// Decision (plan 5.1): only the four pairs, refused before any gh or curl call.
func TestUnsupportedPair(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		for _, p := range [][2]string{{"FreeBSD", "x86_64"}, {"Linux", "riscv64"}, {"Darwin", "i386"}, {"MINGW64_NT-10.0", "x86_64"}, {"Linux", "armv7l"}} {
			t.Run(p[0]+"/"+p[1], func(t *testing.T) {
				h := newHarness(t, shell)
				h.publish("v1.0.0", "darwin", "arm64", release{})
				h.set("FAKE_UNAME_S", p[0])
				h.set("FAKE_UNAME_M", p[1])
				h.set("FAKE_LATEST", "v1.0.0")
				h.set("FAKE_LATEST_URL", "https://github.com/"+repo+"/releases/tag/v1.0.0")
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				mustContain(t, "output", r.out, "darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64")
				if h.log("gh") != "" || h.log("curl") != "" {
					t.Errorf("a transport was called:\ngh: %s\ncurl: %s", h.log("gh"), h.log("curl"))
				}
				if h.installed() != nil {
					t.Error("installed something")
				}
			})
		}
	})
}

// Decision (plan 5.1): the uname spellings map to the four archive names.
func TestPairMapping(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		for _, c := range []struct{ s, m, os, arch string }{
			{"Darwin", "arm64", "darwin", "arm64"},
			{"Darwin", "x86_64", "darwin", "amd64"},
			{"Linux", "aarch64", "linux", "arm64"},
			{"Linux", "x86_64", "linux", "amd64"},
			{"Linux", "amd64", "linux", "amd64"},
			{"Linux", "arm64", "linux", "arm64"},
		} {
			t.Run(c.s+"/"+c.m, func(t *testing.T) {
				h := newHarness(t, shell)
				p := h.publish("v1.0.0", c.os, c.arch, release{}) // only this archive exists
				h.set("FAKE_UNAME_S", c.s)
				h.set("FAKE_UNAME_M", c.m)
				h.set("FAKE_LATEST", "v1.0.0")
				if r := h.run(); r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				if !bytes.Equal(h.installed(), p.bin) {
					t.Error("not installed")
				}
			})
		}
	})
}

// Decision (plan 5.7): a mismatch, a missing or malformed line, and a binary whose
// version line does not name the tag are each refused, leaving the old binary as it was.
func TestRefusedDownloads(t *testing.T) {
	const tag = "v1.1.0"
	each(t, func(t *testing.T, shell string) {
		// The refusals decided by checksums.txt: the downloaded binary must not have run.
		beforeExtraction := map[string]bool{"checksum mismatch": true, "missing line": true, "malformed sum": true, "two lines for the name": true}
		for name, rel := range map[string]release{
			"checksum mismatch":      {sumLine: "bad"},
			"missing line":           {sumLine: "missing"},
			"malformed sum":          {sumLine: "garbage"},
			"two lines for the name": {sumLine: "twice"},
			"version says v9.9.9":    {versionOut: "droidyard v9.9.9 (API 1.0)"},
			"version names no tag":   {versionOut: "0.23.0"},
			"not a program":          {raw: []byte("this is not a program\nexit 7\n")},
		} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, shell)
				old := h.installOld("droidyard v1.0.0 (API 1.0)", 1)
				p := h.publish(tag, "darwin", "arm64", rel)
				h.set("FAKE_LATEST", tag)
				r := h.run()
				if r.code == 0 {
					t.Fatalf("succeeded:\n%s", r.out)
				}
				if !bytes.Equal(h.installed(), old) {
					t.Error("the old binary changed")
				}
				h.noScratch()
				if name == "checksum mismatch" {
					mustContain(t, "output", r.out, p.sum, strings.Repeat("0", 64))
				}
				mustNotContain(t, "droidyard log", h.log("droidyard"), "old status")
				if beforeExtraction[name] {
					mustNotContain(t, "droidyard log (the downloaded binary ran before its sum was checked)", h.log("droidyard"), "new ")
				}
			})
		}
	})
}

// Decision (plan 5.9): the new file is written beside the target and renamed over it, and
// a failed rename leaves neither a half-written copy nor a changed binary (the trap).
func TestUpdateRenamesOverTheTarget(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		t.Run("update X to Y", func(t *testing.T) {
			h := newHarness(t, shell)
			old := h.installOld("droidyard v1.0.0 (API 1.0)", 1)
			link := filepath.Join(h.dir, "old-link")
			if err := os.Link(filepath.Join(h.dir, "droidyard"), link); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(filepath.Join(h.dir, "droidyard"))
			p := h.publish("v1.1.0", "darwin", "arm64", release{status: 1})
			h.set("FAKE_LATEST", "v1.1.0")
			realMktemp, err := exec.LookPath("mktemp")
			if err != nil {
				t.Fatal(err)
			}
			h.fake("mktemp", fakeMktemp)
			h.set("REAL_MKTEMP", realMktemp)
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			mustContain(t, "output", r.out, "droidyard: installed v1.0.0, installing v1.1.0")
			if !bytes.Equal(h.installed(), p.bin) {
				t.Error("target is not the new binary")
			}
			if kept, _ := os.ReadFile(link); !bytes.Equal(kept, old) {
				t.Error("a hard link to the old file lost X's bytes: the file was overwritten, not renamed over")
			}
			after, _ := os.Stat(filepath.Join(h.dir, "droidyard"))
			if os.SameFile(before, after) {
				t.Error("target is still the old inode")
			}
			h.noScratch()
			// The copy beside the target has an unpredictable name: mktemp makes it.
			mustContain(t, "mktemp log", h.log("mktemp"), filepath.Join(h.dir, ".droidyard.new.XXXXXX")+"\n")
		})
		t.Run("rename fails", func(t *testing.T) {
			h := newHarness(t, shell)
			old := h.installOld("droidyard v1.0.0 (API 1.0)", 1)
			h.publish("v1.1.0", "darwin", "arm64", release{status: 1})
			h.set("FAKE_LATEST", "v1.1.0")
			h.fake("mv", fakeMvFails)
			r := h.run()
			if r.code == 0 {
				t.Fatalf("succeeded:\n%s", r.out)
			}
			mustContain(t, "mv log", h.log("mv"), "/.droidyard.new.", "source-exists")
			if !bytes.Equal(h.installed(), old) {
				t.Error("the old binary changed")
			}
			h.noScratch()
		})
	})
}

// Decision (plan 5.5, owner 2026-10-12): re-running at the same version downloads nothing
// and touches nothing, whether the version was discovered or pinned.
func TestAlreadyCurrent(t *testing.T) {
	const tag = "v1.1.0"
	each(t, func(t *testing.T, shell string) {
		for name, setup := range map[string]func(h *harness){
			"latest via gh": func(h *harness) { h.set("FAKE_LATEST", tag) },
			"latest via curl": func(h *harness) {
				h.without("gh")
				h.set("FAKE_LATEST_URL", "https://github.com/"+repo+"/releases/tag/"+tag)
			},
			"pinned": func(h *harness) { h.set("DROIDYARD_VERSION", tag) },
		} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, shell)
				old := h.installOld("droidyard "+tag+" (API 1.0)", 0)
				h.publish(tag, "darwin", "arm64", release{})
				setup(h)
				before, _ := os.Stat(filepath.Join(h.dir, "droidyard"))
				r := h.run()
				if r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				mustContain(t, "output", r.out, "droidyard: already at "+tag)
				mustNotContain(t, "output", r.out, "daemon")
				if ghDownloadCalls(h) != 0 || curlDownloadCalls(h) != 0 {
					t.Errorf("a download call was made:\ngh: %s\ncurl: %s", h.log("gh"), h.log("curl"))
				}
				after, _ := os.Stat(filepath.Join(h.dir, "droidyard"))
				if !os.SameFile(before, after) || !bytes.Equal(h.installed(), old) {
					t.Error("the installed file was replaced")
				}
				mustNotContain(t, "droidyard log", h.log("droidyard"), "status")
			})
		}
	})
}

// Decision (plan 5.4): a current version that is not vX.Y.Z (a pre-2m build printing
// 0.23.0) is unknown, so the script installs.
func TestUnknownCurrentVersion(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		for name, out := range map[string]string{
			"pre-2m build": "0.23.0",
			"other tool":   "somethingelse 1.0",
			"empty":        "",
		} {
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, shell)
				h.installOld(out, 1)
				p := h.publish("v1.0.0", "darwin", "arm64", release{status: 1})
				h.set("FAKE_LATEST", "v1.0.0")
				r := h.run()
				if r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				mustContain(t, "output", r.out, "installing v1.0.0")
				mustNotContain(t, "output", r.out, "already at")
				if !bytes.Equal(h.installed(), p.bin) {
					t.Error("not installed")
				}
			})
		}
	})
}

// Decision (plan 5.10, ADR-0068 §5): the PATH line is printed only when the directory is
// not on PATH, names the right profile, and nothing is written (run() checks the profiles).
func TestPathAdvice(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		t.Run("directory on PATH", func(t *testing.T) {
			h := newHarness(t, shell)
			h.publish("v1.0.0", "darwin", "arm64", release{status: 1})
			h.set("FAKE_LATEST", "v1.0.0")
			h.pathTail = h.dir
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			mustNotContain(t, "output", r.out, "export PATH", "not on your PATH")
		})
		for _, c := range []struct{ name, shell, uname, profile string }{
			{"zsh", "/bin/zsh", "Darwin", "~/.zshrc"},
			{"bash on macOS", "/bin/bash", "Darwin", "~/.bash_profile"},
			{"bash on Linux", "/usr/bin/bash", "Linux", "~/.bashrc"},
			{"other shell", "/usr/bin/fish", "Linux", "~/.profile"},
		} {
			t.Run("directory not on PATH, "+c.name, func(t *testing.T) {
				h := newHarness(t, shell)
				h.publish("v1.0.0", strings.ToLower(c.uname), "arm64", release{status: 1})
				h.set("FAKE_UNAME_S", c.uname)
				h.set("FAKE_LATEST", "v1.0.0")
				h.set("SHELL", c.shell)
				r := h.run()
				if r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				mustContain(t, "output", r.out, `export PATH="`+h.dir+`:$PATH"`, "to "+c.profile+":")
			})
		}
	})
}

// Decision (plan 5.8, 5.10): a daemon that answers to the old binary or to the new one
// gets the advice to run `droidyard stop`; the script never stops one.
func TestDaemonAdvice(t *testing.T) {
	const advice = "still runs the previous binary; run: droidyard stop"
	const freshAdvice = "a droidyard daemon is running; run: droidyard stop, so the next start uses "
	each(t, func(t *testing.T, shell string) {
		for _, c := range []struct {
			name     string
			hasOld   bool
			old, new int
			advice   bool
		}{
			{"old answers, new does not", true, 0, 1, true},
			{"new answers, old does not", true, 1, 0, true},
			{"both answer", true, 0, 0, true},
			{"neither answers", true, 1, 1, false},
			{"fresh install, new answers", false, 0, 0, true},
			{"fresh install, new does not", false, 0, 1, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newHarness(t, shell)
				if c.hasOld {
					h.installOld("droidyard v1.0.0 (API 1.0)", c.old)
				}
				h.publish("v1.1.0", "darwin", "arm64", release{status: c.new})
				h.set("FAKE_LATEST", "v1.1.0")
				r := h.run()
				if r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				if c.advice && c.hasOld {
					mustContain(t, "output", r.out, advice)
				} else if c.advice {
					mustContain(t, "output", r.out, freshAdvice+h.dir+"/droidyard")
					mustNotContain(t, "output", r.out, "previous binary")
				} else {
					mustNotContain(t, "output", r.out, "daemon", "droidyard stop")
				}
				l := h.log("droidyard")
				mustNotContain(t, "droidyard log", l, " stop")
				if c.hasOld {
					mustContain(t, "droidyard log", l, "old status")
				}
				if !(c.hasOld && c.old == 0) {
					// The new binary is asked only when the old one did not answer.
					mustContain(t, "droidyard log", l, "new status")
				}
			})
		}
	})
}

// Decision (plan 5, "main called on the last line"): the first half of the script, as a
// download cut short would deliver it, installs nothing and calls no fake.
func TestTruncatedScript(t *testing.T) {
	src, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	ls := strings.Split(string(src), "\n")
	half := strings.Join(ls[:len(ls)/2], "\n")
	if !strings.Contains(half, "main() {") || strings.Contains(half, "\nmain \"$@\"") {
		t.Fatal("the cut must fall inside main's body, before its call")
	}
	each(t, func(t *testing.T, shell string) {
		h := newHarness(t, shell)
		h.publish("v1.0.0", "darwin", "arm64", release{})
		h.set("FAKE_LATEST", "v1.0.0")
		r := h.exec([]byte(half))
		if r.code == 0 {
			t.Errorf("a truncated script exited 0:\n%s", r.out)
		}
		for _, f := range []string{"uname", "gh", "curl", "droidyard"} {
			if l := h.log(f); l != "" {
				t.Errorf("fake %s was called by a truncated script: %s", f, l)
			}
		}
		if h.installed() != nil {
			t.Error("installed something")
		}
	})
}

// The curl | sh shape: the whole script on stdin works, and nothing else reads stdin.
func TestPipedScript(t *testing.T) {
	src, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	each(t, func(t *testing.T, shell string) {
		h := newHarness(t, shell)
		p := h.publish("v1.0.0", "darwin", "arm64", release{})
		h.set("FAKE_LATEST", "v1.0.0")
		r := h.exec(src)
		if r.code != 0 {
			t.Fatalf("exit %d:\n%s", r.code, r.out)
		}
		if !bytes.Equal(h.installed(), p.bin) {
			t.Error("not installed")
		}
	})
}

// Decision (plan 5.7): sha256sum, else shasum -a 256, else a refusal before downloading.
func TestSumTools(t *testing.T) {
	each(t, func(t *testing.T, shell string) {
		t.Run("shasum only", func(t *testing.T) {
			if _, err := exec.LookPath("shasum"); err != nil {
				t.Skip("no shasum on this host")
			}
			h := newHarness(t, shell, "sha256sum")
			h.assertAbsent("sha256sum")
			p := h.publish("v1.0.0", "darwin", "arm64", release{})
			h.set("FAKE_LATEST", "v1.0.0")
			if r := h.run(); r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if !bytes.Equal(h.installed(), p.bin) {
				t.Error("not installed")
			}
		})
		t.Run("shasum only still refuses a mismatch", func(t *testing.T) {
			if _, err := exec.LookPath("shasum"); err != nil {
				t.Skip("no shasum on this host")
			}
			h := newHarness(t, shell, "sha256sum")
			h.publish("v1.0.0", "darwin", "arm64", release{sumLine: "bad"})
			h.set("FAKE_LATEST", "v1.0.0")
			if r := h.run(); r.code == 0 || h.installed() != nil {
				t.Fatalf("a mismatch was accepted:\n%s", r.out)
			}
		})
		t.Run("neither", func(t *testing.T) {
			h := newHarness(t, shell, "sha256sum", "shasum")
			h.assertAbsent("sha256sum", "shasum")
			h.publish("v1.0.0", "darwin", "arm64", release{})
			h.set("FAKE_LATEST", "v1.0.0")
			r := h.run()
			if r.code == 0 {
				t.Fatalf("succeeded:\n%s", r.out)
			}
			mustContain(t, "output", r.out, "sha256sum or shasum")
			if ghDownloadCalls(h) != 0 || h.installed() != nil {
				t.Error("downloaded or installed without a way to check the sum")
			}
		})
	})
}

// fakeMktemp records its arguments, then does the real thing.
var fakeMktemp = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/mktemp.log"
exec "$REAL_MKTEMP" "$@"
`

var fakeTar = `#!/bin/sh
echo "$*" >> "$FAKE_CALLS/tar.log"
exec "$REAL_TAR" "$@"
`

// Decision (plan 5.7): only the member named droidyard is extracted, and it must be a
// regular file. A symlink member could point the install at any file on the host, and
// an extra member must never be written.
func TestArchiveMembers(t *testing.T) {
	const tag = "v1.1.0"
	each(t, func(t *testing.T, shell string) {
		t.Run("droidyard member is a symlink", func(t *testing.T) {
			h := newHarness(t, shell)
			old := h.installOld("droidyard v1.0.0 (API 1.0)", 1)
			// The link's target is a runnable, correctly versioned binary outside the scratch
			// directory: only the symlink check stands between it and the install.
			target := filepath.Join(h.root, "symlink-target")
			if err := os.WriteFile(target, binary("new", "droidyard "+tag+" (API 1.0)", 1), 0o755); err != nil {
				t.Fatal(err)
			}
			h.publish(tag, "darwin", "arm64", release{symlinkTo: target})
			h.set("FAKE_LATEST", tag)
			r := h.run()
			if r.code == 0 {
				t.Fatalf("succeeded:\n%s", r.out)
			}
			if !bytes.Equal(h.installed(), old) {
				t.Error("the old binary changed")
			}
			mustNotContain(t, "droidyard log (the symlink's target ran)", h.log("droidyard"), "new ")
			h.noScratch()
		})
		t.Run("extra members are not extracted", func(t *testing.T) {
			h := newHarness(t, shell)
			real, err := exec.LookPath("tar")
			if err != nil {
				t.Fatal(err)
			}
			h.fake("tar", fakeTar)
			h.set("REAL_TAR", real)
			p := h.publish(tag, "darwin", "arm64", release{extra: []string{"../evil", "x", "/abs-evil"}})
			h.set("FAKE_LATEST", tag)
			r := h.run()
			if r.code != 0 {
				t.Fatalf("exit %d:\n%s", r.code, r.out)
			}
			if !bytes.Equal(h.installed(), p.bin) {
				t.Error("not installed")
			}
			lines := strings.Split(strings.TrimSpace(h.log("tar")), "\n")
			if len(lines) != 1 || !strings.HasSuffix(lines[0], " droidyard") || !strings.HasPrefix(lines[0], "-xzf ") {
				t.Errorf("tar must extract the one member named droidyard, got %q", lines)
			}
			for _, f := range []string{filepath.Join(h.root, "evil"), filepath.Join(h.tmpdir, "evil"), "/abs-evil"} {
				if _, err := os.Lstat(f); err == nil {
					t.Errorf("%s was written", f)
				}
			}
		})
	})
}

// Decision (ADR-0068 §5): the PATH line is also printed when nothing is installed
// because the version is already there, and a trailing slash on the directory neither
// defeats the PATH match nor appears in the printed line.
func TestPathAdviceWhenCurrentAndWithTrailingSlash(t *testing.T) {
	const tag = "v1.1.0"
	each(t, func(t *testing.T, shell string) {
		for _, c := range []struct {
			name     string
			slash    bool
			onPath   bool
			current  bool
			wantLine bool
		}{
			{"already at, not on PATH", false, false, true, true},
			{"already at, on PATH", false, true, true, false},
			{"already at, trailing slash, on PATH", true, true, true, false},
			{"already at, trailing slash, not on PATH", true, false, true, true},
			{"install, trailing slash, on PATH", true, true, false, false},
			{"install, trailing slash, not on PATH", true, false, false, true},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newHarness(t, shell)
				if c.current {
					h.installOld("droidyard "+tag+" (API 1.0)", 1)
				}
				h.publish(tag, "darwin", "arm64", release{status: 1})
				h.set("DROIDYARD_VERSION", tag)
				if c.slash {
					h.set("DROIDYARD_INSTALL_DIR", h.dir+"/")
				}
				if c.onPath {
					h.pathTail = h.dir
				}
				r := h.run()
				if r.code != 0 {
					t.Fatalf("exit %d:\n%s", r.code, r.out)
				}
				if c.current {
					mustContain(t, "output", r.out, "already at "+tag)
				}
				if c.wantLine {
					mustContain(t, "output", r.out, `export PATH="`+h.dir+`:$PATH"`)
				} else {
					mustNotContain(t, "output", r.out, "export PATH")
				}
				if c.slash {
					mustNotContain(t, "output", r.out, h.dir+"//", h.dir+"/:")
				}
			})
		}
	})
}

package sidecar

// The opt-in distribution tests of plan m3e step 1: connection.open installing a Gradle
// distribution from a loopback server, so the slow, stalled and locked installs of the
// register's gradle rows can be reproduced and their failure chains read. Gated and
// fail-loud like the other real-Gradle tests (gradleTest), except for the zip: with none
// available they skip, saying so.

import (
	"crypto/md5"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// distZip is the Gradle 8.0.2 zip the loopback server serves: DROIDYARD_GRADLE_DIST_ZIP,
// else build.sh's cached copy. None: the test skips and says why.
func distZip(t *testing.T) string {
	t.Helper()
	if z := os.Getenv("DROIDYARD_GRADLE_DIST_ZIP"); z != "" {
		if _, err := os.Stat(z); err != nil {
			t.Fatalf("DROIDYARD_GRADLE_DIST_ZIP=%s: %v", z, err)
		}
		return z
	}
	z := filepath.Join(buildCacheDir(), "gradle-8.0.2-bin.zip")
	if _, err := os.Stat(z); err != nil {
		t.Skipf("no Gradle 8.0.2 zip: set DROIDYARD_GRADLE_DIST_ZIP or run sidecar/build.sh (looked for %s)", z)
	}
	return z
}

// distRate is the server's bytes per second: DROIDYARD_GRADLE_DIST_RATE, else 16 MB/s.
func distRate(t *testing.T) int {
	t.Helper()
	if v := os.Getenv("DROIDYARD_GRADLE_DIST_RATE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("DROIDYARD_GRADLE_DIST_RATE=%q is not a positive number", v)
		}
		return n
	}
	return 16_000_000
}

// distServer serves the zip on 127.0.0.1 at gradle-8.0.2-bin.zip, writing rate bytes per
// second (0: unthrottled) and pausing stallFor once after stallAt bytes (stallAt 0: never).
// It returns the URL the wrapper's distributionUrl takes.
func distServer(t *testing.T, zip string, rate int, stallAt int64, stallFor time.Duration) string {
	t.Helper()
	data, err := os.ReadFile(zip)
	if err != nil {
		t.Fatal(err)
	}
	stalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Type", "application/zip")
		if r.Method == http.MethodHead {
			return
		}
		const tick = 100 * time.Millisecond
		chunk := len(data)
		if rate > 0 {
			chunk = max(1, int(int64(rate)*int64(tick)/int64(time.Second)))
		}
		var sent int64
		for sent < int64(len(data)) {
			end := min(int64(len(data)), sent+int64(chunk))
			if stallAt > 0 && !stalled && sent < stallAt && end >= stallAt {
				end = stallAt
			}
			if _, err := w.Write(data[sent:end]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			sent = end
			if stallAt > 0 && !stalled && sent >= stallAt {
				stalled = true
				select {
				case <-time.After(stallFor):
				case <-r.Context().Done():
					return
				}
			}
			if rate > 0 {
				time.Sleep(tick)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/distributions/gradle-8.0.2-bin.zip"
}

// distFixture is a copy of testdata/plain whose wrapper points at url. Its gradle.properties
// keeps a short daemon idle timeout, so the test's daemon ends by itself. stubborn is a
// task that ignores interruption: a busy loop of 40 s.
func distFixture(t *testing.T, url string) string {
	t.Helper()
	root := plainFixture(t)
	props := filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.properties")
	text := readFile(t, props)
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "distributionUrl=") {
			l = "distributionUrl=" + strings.ReplaceAll(url, ":", `\:`)
		}
		out = append(out, l)
	}
	writeFile(t, props, strings.Join(out, "\n"))
	writeFile(t, filepath.Join(root, "gradle.properties"),
		"org.gradle.daemon.idletimeout=20000\norg.gradle.jvmargs=-Xmx512m\n")
	bg := filepath.Join(root, "build.gradle")
	writeFile(t, bg, readFile(t, bg)+`
tasks.register('stubborn') {
    group = 'droidyard'
    description = 'A busy loop of 40 s that never checks for interruption.'
    doLast {
        long end = System.nanoTime() + 40_000_000_000L
        while (System.nanoTime() < end) { }
    }
}
`)
	return root
}

// distDir is the wrapper's install directory for url under the user home guh:
// wrapper/dists/<name>/<base36 of the MD5 of the URL> (Gradle's PathAssembler).
func distDir(guh, url string) string {
	sum := md5.Sum([]byte(url))
	hash := new(big.Int).SetBytes(sum[:]).Text(36)
	return filepath.Join(guh, "wrapper", "dists", "gradle-8.0.2-bin", hash)
}

// distProc starts a sidecar whose GRADLE_USER_HOME is a temporary directory of its own.
func distProc(t *testing.T) (*proc, string) {
	t.Helper()
	guh := t.TempDir()
	p := startProcOpts(t, procOpts{env: []string{"GRADLE_USER_HOME=" + guh}})
	t.Cleanup(func() { stopGradleDaemons(guh) })
	return p, guh
}

// openFailure sends connection.open and returns the elapsed time and the error object.
func openFailure(t *testing.T, p *proc, root string, wait time.Duration) (time.Duration, map[string]any) {
	t.Helper()
	start := time.Now()
	resp := p.waitResponse(p.send("connection.open", map[string]any{"root": root}), wait)
	took := time.Since(start)
	e := objOf(resp, "error")
	if e == nil {
		t.Fatalf("connection.open succeeded after %s, want a failure: %v", took, resp)
	}
	return took, e
}

// Decision (plan m3e step 1; gradle.md §Failure modes): a Gradle distribution is installed
// from a slow server by connection.open, and the install lands in the user home with its
// .ok marker (which also proves distDir names the right directory for the lock test).
func TestGradleDistributionInstallsFromASlowServer(t *testing.T) {
	gradleTest(t)
	zip := distZip(t)
	url := distServer(t, zip, distRate(t), 0, 0)
	p, guh := distProc(t)
	root := distFixture(t, url)
	start := time.Now()
	r := openRoot(t, p, root)
	t.Logf("install over loopback at %d B/s: %s", distRate(t), time.Since(start))
	if got := strOf(r, "gradleVersion"); got != "8.0.2" {
		t.Errorf("gradleVersion %q, want 8.0.2", got)
	}
	if got := strOf(r, "source"); got != "wrapper" {
		t.Errorf("source %q, want wrapper", got)
	}
	ok := filepath.Join(distDir(guh, url), "gradle-8.0.2-bin.zip.ok")
	if _, err := os.Stat(ok); err != nil {
		t.Errorf("no .ok marker where distDir says the install is: %v", err)
	}
	t.Logf("sidecar stderr:\n%s", p.stderrText())
}

// Decision (plan m3e step 1, G4's first need): a download that stalls past the wrapper's
// networkTimeout fails connection.open with the inner chain in the error's data, not only
// the outer "Could not install Gradle distribution", which every install failure shares.
func TestGradleDistributionStallCarriesTheChain(t *testing.T) {
	gradleTest(t)
	zip := distZip(t)
	url := distServer(t, zip, distRate(t), 8<<20, 15*time.Second)
	p, _ := distProc(t)
	root := distFixture(t, url)
	took, e := openFailure(t, p, root, 120*time.Second)
	data := strOf(objOf(e, "data"), "message")
	t.Logf("stalled install failed after %s\nmessage: %s\ndata.message:\n%s", took, strOf(e, "message"), data)
	if !strings.Contains(data, "Could not install Gradle distribution") {
		t.Errorf("the cause does not name the install failure:\n%s", data)
	}
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 2 {
		t.Errorf("the cause holds %d message(s), want the outer one and at least one inner:\n%s", len(lines), data)
	}
}

// Decision (plan m3e step 1): another process holding the distribution's lock makes
// connection.open fail after Gradle's own 120 s lock wait, with a chain naming it. The
// test is the other process: an fcntl write lock on the zip's .lck, which Java's
// FileChannel lock excludes on macOS (the test fails visibly if it does not).
func TestGradleDistributionLockedByAnotherProcess(t *testing.T) {
	gradleTest(t)
	zip := distZip(t)
	url := distServer(t, zip, 0, 0, 0)
	p, guh := distProc(t)
	root := distFixture(t, url)
	dir := distDir(guh, url)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lck, err := os.OpenFile(filepath.Join(dir, "gradle-8.0.2-bin.zip.lck"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lck.Close()
	fl := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
	if err := unix.FcntlFlock(lck.Fd(), unix.F_SETLK, &fl); err != nil {
		t.Fatalf("taking the write lock: %v", err)
	}
	release := time.AfterFunc(130*time.Second, func() {
		fl.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(lck.Fd(), unix.F_SETLK, &fl)
	})
	defer release.Stop()
	took, e := openFailure(t, p, root, 170*time.Second)
	data := strOf(objOf(e, "data"), "message")
	t.Logf("locked install failed after %s\nmessage: %s\ndata.message:\n%s", took, strOf(e, "message"), data)
	if took < 100*time.Second || took > 150*time.Second {
		t.Errorf("failed after %s, want about 120 s (Gradle's lock wait)", took)
	}
	if !strings.Contains(strings.ToLower(data), "lock") {
		t.Errorf("the chain does not name a lock:\n%s", data)
	}
}

// Decision (plan m3e step 1, the fact step 4 rests on): how long a task that ignores
// interruption keeps build.cancel from reaching finished, and whether the Gradle daemon
// survives. Asserts only that finished arrives; everything else is logged.
func TestGradleIgnoredCancelTiming(t *testing.T) {
	gradleTest(t)
	zip := distZip(t)
	url := distServer(t, zip, 0, 0, 0)
	p, guh := distProc(t)
	root := distFixture(t, url)
	openRoot(t, p, root)
	id := p.send("build.run", map[string]any{"buildId": "b-stubborn", "root": root, "tasks": []string{":stubborn"}})
	if !boolOf(p.result(id, 30*time.Second), "accepted") {
		t.Fatal("build.run :stubborn was not accepted")
	}
	time.Sleep(2 * time.Second)
	before := gradleDaemonPids(guh)
	start := time.Now()
	p.result(p.send("build.cancel", map[string]any{"buildId": "b-stubborn"}), 30*time.Second)
	replied := time.Since(start)
	fin := p.waitFor(120*time.Second, func(m map[string]any) bool {
		return m["method"] == "event.build.finished" && strOf(objOf(m, "params"), "buildId") == "b-stubborn"
	})
	took := time.Since(start)
	after := gradleDaemonPids(guh)
	t.Logf("ignored cancel: reply to build.cancel after %s, finished after %s (from the cancel request), outcome %q",
		replied, took, strOf(objOf(fin, "params"), "outcome"))
	t.Logf("gradle daemon pids before %v, after %v (alive: %v)", before, after, alivePids(after))
}

// gradleDaemonPids are the pids of the Gradle daemons of the user home guh, from the
// daemon-<pid>.out.log files Gradle writes there.
func gradleDaemonPids(guh string) []int {
	logs, _ := filepath.Glob(filepath.Join(guh, "daemon", "*", "daemon-*.out.log"))
	var pids []int
	for _, l := range logs {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(l), "daemon-"), ".out.log")
		if n, err := strconv.Atoi(name); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func alivePids(pids []int) []string {
	var out []string
	for _, p := range pids {
		out = append(out, fmt.Sprintf("%d=%v", p, syscall.Kill(p, 0) == nil))
	}
	return out
}

// stopGradleDaemons ends the daemons this test started, by pid, so none outlives its
// temporary user home. Only pids found in this test's own user home are signalled.
func stopGradleDaemons(guh string) {
	for _, pid := range gradleDaemonPids(guh) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

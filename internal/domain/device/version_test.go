package device

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hostVersionReply is what a real adb server answered to "000chost:version" in step 1
// (testdata/host-version-5037.txt): OKAY, 0004, then 0x29 = 41.
func hostVersionReply(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/host-version-5037.txt")
	if err != nil || string(b) != "OKAY00040029" {
		t.Fatalf("fixture: %q, %v", b, err)
	}
	return b
}

// fakeServer answers host:version with reply on an injected loopback port. It returns
// the port and the requests it saw.
func fakeServer(t *testing.T, reply []byte) (port string, requests func() []string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var seen []string
	done := make(chan struct{}, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			seen = append(seen, string(buf[:n]))
			c.Write(reply)
			c.Close()
			done <- struct{}{}
		}
	}()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p, func() []string { return append([]string(nil), seen...) }
}

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return p
}

// nonVersionCalls counts the fake adb's invocations other than `version`: anything
// else could start a server.
func nonVersionCalls(t *testing.T, log string) int {
	t.Helper()
	b, _ := os.ReadFile(log)
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" && !strings.HasPrefix(l, "version ") {
			n++
		}
	}
	return n
}

// TestServerVersion pins the version check (ratifications §Operational defaults; A2):
// the server's version is a socket read of "host:version" on the port the daemon's
// environment names (never 5037 unless that is what it says), compared with the
// client's `adb version`.
//
//	same version: no warning; the request on the wire is exactly "000chost:version".
//	different:    one warning naming both binaries' versions and ADB; found stays true
//	              (warn and proceed, never an error).
//	no listener:  no server version, no warning, and the client is never asked to do
//	              anything but `version` (anything else might start a server).
func TestServerVersion(t *testing.T) {
	reply := hostVersionReply(t)
	newer := bytes.Replace(reply, []byte("0029"), []byte("002a"), 1)

	t.Run("same version", func(t *testing.T) {
		port, reqs := fakeServer(t, reply)
		log := filepath.Join(t.TempDir(), "log")
		a := newRunner(t, fakeEnvFor(t, modeVersion, port, fakeBuild+"=41", fakeLog+"="+log))
		st := a.Check(context.Background())
		if !st.Found || st.ServerVersion != "41" || !strings.HasPrefix(st.Version, "1.0.41") || len(st.Warnings) != 0 {
			t.Errorf("status = %+v, want found, server 41, client 1.0.41, no warning", st)
		}
		if got := reqs(); len(got) != 1 || got[0] != "000chost:version" {
			t.Errorf("requests = %q, want one 000chost:version", got)
		}
		// The injected port reached the client through the environment given to the runner.
		if b, _ := os.ReadFile(log); !strings.Contains(string(b), "port="+port) {
			t.Errorf("the client's environment lacked ANDROID_ADB_SERVER_PORT=%s: %q", port, b)
		}
	})
	t.Run("different version warns and proceeds", func(t *testing.T) {
		port, _ := fakeServer(t, newer)
		a := newRunner(t, fakeEnvFor(t, modeVersion, port, fakeBuild+"=41"))
		st := a.Check(context.Background())
		if !st.Found {
			t.Fatal("a mismatch made adb not found: it must warn and proceed")
		}
		if len(st.Warnings) != 1 {
			t.Fatalf("warnings = %q, want one", st.Warnings)
		}
		w := st.Warnings[0]
		for _, want := range []string{"1.0.41", "version 42", st.Path, "ADB"} {
			if !strings.Contains(w, want) {
				t.Errorf("warning %q does not name %q", w, want)
			}
		}
	})
	t.Run("no listener", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "log")
		a := newRunner(t, fakeEnvFor(t, modeVersion, closedPort(t), fakeBuild+"=41", fakeLog+"="+log))
		st := a.Check(context.Background())
		if !st.Found || st.ServerVersion != "" || len(st.Warnings) != 0 {
			t.Errorf("status = %+v, want found, no server version, no warning", st)
		}
		if n := nonVersionCalls(t, log); n != 0 {
			t.Errorf("%d adb invocations besides `version`: the probe must never run adb", n)
		}
	})
}

// TestAMalformedServerPortProbesNothing pins that ANDROID_ADB_SERVER_PORT set to
// something that is not a port probes no server at all, rather than falling back to the
// default port: the caller set it to keep away from the shared server. The default port
// is pointed at a fake here, so the fallback is observable without touching 5037.
func TestAMalformedServerPortProbesNothing(t *testing.T) {
	port, reqs := fakeServer(t, hostVersionReply(t))
	saved := defaultServerPort
	defaultServerPort = port
	t.Cleanup(func() { defaultServerPort = saved })

	for _, bad := range []string{"notaport", "0", "70000"} {
		if v := serverVersion(context.Background(), []string{"ANDROID_ADB_SERVER_PORT=" + bad}); v != "" {
			t.Errorf("ANDROID_ADB_SERVER_PORT=%s: server version %q; want none probed", bad, v)
		}
	}
	if got := reqs(); len(got) != 0 {
		t.Errorf("requests reached the default port: %q", got)
	}
	// Unset, the default is used: the same fake answers.
	if v := serverVersion(context.Background(), nil); v != "41" {
		t.Errorf("unset: server version %q; want 41 from the default port", v)
	}
}

// TestVersionIsCachedUntilTheBinaryChanges pins "cached until the resolved path or its
// mtime changes": a second check runs `adb version` no more, and touching the binary
// runs it again.
func TestVersionIsCachedUntilTheBinaryChanges(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	// A copy of the fake, so its mtime can change without touching the test binary.
	exe, _ := os.Executable()
	data, _ := os.ReadFile(exe)
	copyPath := filepath.Join(t.TempDir(), "adb")
	os.WriteFile(copyPath, data, 0o755)
	env := fakeEnvFor(t, modeVersion, closedPort(t), fakeBuild+"=41", fakeLog+"="+log)
	env[0] = "ADB=" + copyPath
	a := newRunner(t, env)
	count := func() int { b, _ := os.ReadFile(log); return strings.Count(string(b), "version ") }
	a.Check(context.Background())
	a.Check(context.Background())
	if n := count(); n != 1 {
		t.Fatalf("`adb version` ran %d times for two checks, want 1", n)
	}
	later := a.cache.mtime.Add(2e9)
	os.Chtimes(copyPath, later, later)
	a.Check(context.Background())
	if n := count(); n != 2 {
		t.Errorf("`adb version` ran %d times after the mtime changed, want 2", n)
	}
}

func TestCheckWithNoAdbReportsEveryCandidate(t *testing.T) {
	a := NewADB([]string{"HOME=" + t.TempDir(), "PATH=" + t.TempDir()}, nil)
	st := a.Check(context.Background())
	if st.Found || len(st.Rejected) == 0 || st.Warnings == nil {
		t.Errorf("status = %+v, want not found with candidates and a non-nil warnings list", st)
	}
}

package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/store"
)

var (
	tokenFileContent = regexp.MustCompile(`^[0-9a-f]{64}\n$`)
)

// serveOn starts a re-executed `serve` on state and returns it once it listens, with
// the HTTP address its lock names.
func serveOn(t *testing.T, state string, env ...string) (*process, string) {
	t.Helper()
	p := droidyardEnv(t, state, "", env, "serve")
	p.expect(t, "listening")
	info, err := daemon.ReadLock(state)
	if err != nil || info.HTTP == nil {
		t.Fatalf("daemon.lock says (%+v, %v) once serve listens", info, err)
	}
	return p, *info.HTTP
}

// healthStatus is the status of a system.health call to addr with token.
func healthStatus(t *testing.T, addr, token string) int {
	t.Helper()
	resp := callRequest(portOf(t, addr), token, `{"capability":"system.health"}`).send(t, addr)
	resp.Body.Close()
	return resp.StatusCode
}

func stopped(t *testing.T, state string) {
	t.Helper()
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
}

// killDaemonAtEnd is the cleanup for a test whose daemon a shim started detached.
func killDaemonAtEnd(t *testing.T, state string) {
	t.Cleanup(func() {
		if info, err := daemon.ReadLock(state); err == nil && info.PID > 0 && alive(info.PID) {
			syscall.Kill(info.PID, syscall.SIGKILL)
		}
	})
}

// TestHTTPTokenFile: the token is the per-launch secret of the UI (http-websocket.md
// §Security baseline rule 4; plan m1d, D8). Real processes on one state: the file is
// 0600 in a 0700 directory, 64 lowercase hex digits and a newline, replacing a stale
// one; it is gone, and the port closed, once `stop` returns; the next start writes a
// different token, and the old one is a 401 there; no log carries either token.
func TestHTTPTokenFile(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	path := filepath.Join(state, daemon.TokenFile)
	// What a crashed daemon left, with the wrong mode, in a directory with the wrong one.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, addr1 := serveOn(t, state)
	read := func() string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !tokenFileContent.Match(raw) {
			t.Fatalf("%s holds %q; want 64 lowercase hex digits and a newline", daemon.TokenFile, raw)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %o; want 0600", daemon.TokenFile, fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
			t.Errorf("run/ is %o; want 0700", fi.Mode().Perm())
		}
		return strings.TrimSpace(string(raw))
	}
	token1 := read()
	if code := healthStatus(t, addr1, token1); code != 200 {
		t.Fatalf("the file's token got %d from the daemon that wrote it; want 200", code)
	}

	stopped(t, state)
	if code := first.wait(t); code != 0 {
		t.Fatalf("serve exited %d", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after stop returned, %s: %v; want it gone", daemon.TokenFile, err)
	}
	if c, err := net.DialTimeout("tcp", addr1, time.Second); err == nil {
		c.Close()
		t.Errorf("after stop returned, %s still accepts connections", addr1)
	}

	second, addr2 := serveOn(t, state)
	token2 := read()
	if token2 == token1 {
		t.Errorf("a second start reused the first start's token")
	}
	if code := healthStatus(t, addr2, token1); code != 401 {
		t.Errorf("the first start's token got %d from the second daemon; want 401", code)
	}
	if code := healthStatus(t, addr2, token2); code != 200 {
		t.Errorf("the second start's token got %d from the second daemon; want 200", code)
	}
	stopped(t, state)
	second.wait(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after the second stop, %s: %v; want it gone", daemon.TokenFile, err)
	}

	for name, log := range map[string]string{"first": first.stderr.String(), "second": second.stderr.String()} {
		for _, token := range []string{token1, token2} {
			if strings.Contains(log, token) {
				t.Errorf("the %s serve's stderr carries a token", name)
			}
		}
		if log == "" {
			t.Errorf("the %s serve logged nothing: the check read nothing", name)
		}
	}
}

// TestLockNamesTheHTTPAddressBeforeTheSocketAnswers: a client that reaches the socket
// finds the lock's http set and the token written, because the daemon rewrites the
// lock after the HTTP bind and before the socket bind (plan m1d, D8, R3). The test
// dials the socket every 5 ms from serve's start and reads the lock at the first
// answer; the address it finds is the one that serves the API.
func TestLockNamesTheHTTPAddressBeforeTheSocketAnswers(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	srv := droidyard(t, state, "serve")
	sock := sockFor(state)
	var info daemon.LockInfo
	var token string
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			var lerr, terr error
			info, lerr = daemon.ReadLock(state)
			token, terr = daemon.ReadToken(state)
			if lerr != nil || info.HTTP == nil {
				t.Fatalf("the socket answered while daemon.lock said (%+v, %v): no http address yet", info, lerr)
			}
			if terr != nil {
				t.Fatalf("the socket answered before the token was written: %v", terr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket never answered")
		}
	}
	srv.expect(t, "listening")
	root := httpRequest{method: "GET", path: "/", proto: "HTTP/1.1", headers: []string{"Host: " + *info.HTTP}}
	resp := root.send(t, *info.HTTP)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("GET / at the lock's address %s: %d; want 404: no page is served", *info.HTTP, resp.StatusCode)
	}
	if code := healthStatus(t, *info.HTTP, token); code != 200 {
		t.Errorf("the token found at the first answer got %d at the lock's address; want 200", code)
	}
	stopped(t, state)
	srv.wait(t)
}

// TestStatusPrintsTheAddress: `status` reads the actual address from the lock, the
// ephemeral fallback's included, and prints no URL and no token (ADR-0137 §1;
// http-websocket.md §Security baseline rule 1; plan m1d, step 5).
func TestStatusPrintsTheAddress(t *testing.T) {
	t.Parallel()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	P := portOf(t, held.Addr().String())
	state := shortDir(t)
	srv, addr := serveOn(t, state, httpPortEnv+"="+strconv.Itoa(P))
	if portOf(t, addr) == P {
		t.Fatalf("the UI bound the held port %d", P)
	}
	token, err := daemon.ReadToken(state)
	if err != nil {
		t.Fatal(err)
	}

	st := droidyard(t, state, "status")
	if code := st.wait(t); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	out := st.stdout.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// The address line follows the socket line, and is the last.
	if n := len(lines); n < 2 || lines[n-1] != "http "+addr {
		t.Errorf("status printed %q; want its last line to be %q", out, "http "+addr)
	}
	if strings.Contains(out, token) || strings.Contains(out, "#token=") {
		t.Errorf("status printed the token or a tokenised URL: %q", out)
	}
	stopped(t, state)
	srv.wait(t)
}

// TestStatusWithoutAnHTTPListener: a daemon whose lock says "http":null, one that serves
// no HTTP API, is healthy: `status` exits 0, prints no URL and says the lock names no
// address (plan m1d, step 5).
func TestStatusWithoutAnHTTPListener(t *testing.T) {
	state := shortDir(t)
	st, err := store.New(state)
	if err != nil {
		t.Fatal(err)
	}
	reg, ops, _, _, _, _, _ := seamedRegistry(isolatedEnv(t), st, nil, io.Discard)
	d, err := daemon.Start(daemon.Options{
		State: state, Store: st, Socket: sockFor(state), Version: system.Version, UID: os.Getuid(), Ops: ops,
		Handlers: map[capability.ClientKind]daemon.Handler{capability.ClientCLI: daemon.Native(reg, ops, nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d.Shutdown(ctx)
	})
	if info, err := daemon.ReadLock(state); err != nil || info.HTTP != nil {
		t.Fatalf("the daemon without HTTP left daemon.lock saying (%+v, %v); want http null", info, err)
	}

	status := droidyard(t, state, "status")
	if code := status.wait(t); code != 0 {
		t.Errorf("status exited %d against a daemon with no HTTP listener; want 0 (the daemon is healthy)", code)
	}
	if strings.Contains(status.stdout.String(), "http://") {
		t.Errorf("status printed a URL for a daemon with no HTTP listener: %s", status.stdout.String())
	}
	if !strings.Contains(status.stdout.String(), "names no HTTP address") {
		t.Errorf("status does not say the lock names no HTTP address: %s", status.stdout.String())
	}
}

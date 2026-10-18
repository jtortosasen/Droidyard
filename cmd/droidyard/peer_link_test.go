package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/system"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// The peer link between two real daemons (ADR-0098): two `serve` processes, each with its
// own state root and configuration, over real TLS on loopback. The client daemon reaches
// the link through peerprobe.call, a capability only the test binary registers (the shipped
// binary reaches a remote through none until a device is relayed); everything else is the
// shipped wiring.

type probeParams struct {
	Remote     string          `json:"remote" jsonschema:"required"`
	Capability string          `json:"capability" jsonschema:"required"`
	Params     json.RawMessage `json:"params,omitempty"`
}

type probeResult struct {
	// Response is the host's response envelope; Failure is why there is none.
	Response json.RawMessage `json:"response,omitempty"`
	Failure  string          `json:"failure,omitempty"`
}

// registerPeerProbe is installTestSeams' testHosting.
func registerPeerProbe(reg *capability.Registry, host *hosting.Host) {
	reg.MustRegister(capability.Descriptor{
		Name: "peerprobe.call", Kind: capability.Command, Scope: capability.Global,
		Description: "Test only: sends one request over a remote's link.", Params: probeParams{}, Result: probeResult{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(probeParams)
			l := host.Remote(p.Remote)
			if l == nil {
				return probeResult{Failure: "no remote " + p.Remote}, nil
			}
			env, err := l.Call(ctx, capability.RequestEnvelope{Capability: p.Capability, Params: p.Params})
			if err != nil {
				return probeResult{Failure: err.Error()}, nil
			}
			raw, _ := json.Marshal(env)
			return probeResult{Response: raw}, nil
		},
	})
}

const noOperation = "op-0000000000000000"

// daemonAt starts `serve` on state with configuration body (none when empty), waits until it
// holds the lock and keeps its stderr drained.
func daemonAt(t *testing.T, state, body string) *process {
	t.Helper()
	if body != "" {
		configAt(t, state, body)
	}
	p := droidyard(t, state, "serve")
	p.expect(t, "holding")
	go func() {
		for range p.lines {
		}
	}()
	return p
}

// callOn runs `droidyard call` against the daemon on state and returns the envelope.
func callOn(t *testing.T, state string, args ...string) capability.ResponseEnvelope {
	t.Helper()
	env, _ := droidyard(t, state, append([]string{"call", "--no-autostart"}, args...)...).envelope(t)
	return env
}

func healthOn(t *testing.T, state string) system.Health {
	t.Helper()
	return health(t, callOn(t, state, "system.health"))
}

// listenOf waits for the host's peer listener to report the address it bound.
func listenOf(t *testing.T, state string) string {
	t.Helper()
	addr := ""
	eventually(t, "the host's peer listener", func() bool {
		if h := healthOn(t, state).Hosting; h != nil {
			addr = h.Listen
		}
		return addr != ""
	})
	return addr
}

// probe sends capability over the client's link to qa-lab and returns the host's response,
// or the reason there is none.
func probe(t *testing.T, state, capName, params string) (resp capability.ResponseEnvelope, failure string) {
	t.Helper()
	req := `{"remote":"qa-lab","capability":"` + capName + `"`
	if params != "" {
		req += `,"params":` + params
	}
	env := callOn(t, state, "peerprobe.call", "--params", req+"}")
	if env.Error != nil {
		t.Fatalf("peerprobe.call: %+v", env.Error)
	}
	var r probeResult
	if err := json.Unmarshal(env.Result, &r); err != nil {
		t.Fatalf("peerprobe.call result %s: %v", env.Result, err)
	}
	if r.Failure != "" {
		return resp, r.Failure
	}
	if err := json.Unmarshal(r.Response, &resp); err != nil {
		t.Fatalf("the host's response %s: %v", r.Response, err)
	}
	return resp, ""
}

func peerEntry(name, fp string) string {
	return `{"name":"` + name + `","fingerprint":"` + fp + `"}`
}

func remotesOf(address, fp string) string {
	return `{"remotes":[{"name":"qa-lab","address":"` + address + `","fingerprint":"` + fp + `"}]}`
}

func identityOf(t *testing.T, state string) string {
	t.Helper()
	id, _, err := hosting.LoadIdentity(state)
	if err != nil {
		t.Fatal(err)
	}
	return id.Fingerprint()
}

func remoteHealth(t *testing.T, state string) hosting.RemoteHealth {
	t.Helper()
	r := healthOn(t, state).Remotes
	if len(r) != 1 {
		t.Fatalf("remotes = %+v; want qa-lab", r)
	}
	return r[0]
}

func TestPeerLink(t *testing.T) {
	t.Parallel()

	// pair starts a host that admits peers (anna = the client's fingerprint, unless peers is
	// given) and a client that dials it, and returns their state roots, the host's process
	// and the host's address. hostFP, when not empty, is what the client pins instead of
	// the host's real fingerprint.
	type pairing struct {
		host, client string
		hostProc     *process
		addr         string
	}
	pair := func(t *testing.T, peers func(clientFP string) string, hostFP string) pairing {
		t.Helper()
		p := pairing{host: shortDir(t), client: shortDir(t)}
		clientFP := identityOf(t, p.client)
		hostID := identityOf(t, p.host)
		p.hostProc = daemonAt(t, p.host, `{"hosting":{"listen":"127.0.0.1:0","peers":`+peers(clientFP)+`}}`)
		p.addr = listenOf(t, p.host)
		if hostFP == "" {
			hostFP = hostID
		}
		daemonAt(t, p.client, remotesOf(p.addr, hostFP))
		return p
	}
	annaPinned := func(fp string) string { return `[` + peerEntry("anna", fp) + `]` }
	nobodyPinned := func(string) string { return `[` + peerEntry("bob", strings.Repeat("c", 64)) + `]` }

	t.Run("paired", func(t *testing.T) {
		t.Parallel()
		p := pair(t, annaPinned, "")
		eventually(t, "both healths to say connected", func() bool {
			h := healthOn(t, p.host).Hosting
			return remoteHealth(t, p.client).Connected && h != nil && len(h.Peers) == 1 && h.Peers[0].Connected
		})
		if h := healthOn(t, p.host); h.Identity == nil || h.Identity.Fingerprint != identityOf(t, p.host) || h.Hosting.Peers[0].Name != "anna" {
			t.Errorf("the host's health: %+v / %+v", h.Identity, h.Hosting)
		}
		if r := remoteHealth(t, p.client); r.API != system.Version || r.Error != "" {
			t.Errorf("the client's remote: %+v; want the host's API %s and no error", r, system.Version)
		}
		// The round trip: the client's request is answered by the host's core.
		resp, failure := probe(t, p.client, "operation.wait", `{"operationId":"`+noOperation+`"}`)
		if failure != "" || resp.Error == nil || resp.Error.Code != capability.ErrOperationNotFound {
			t.Errorf("operation.wait of an unknown id over the link: %+v / %q; want OPERATION_NOT_FOUND", resp.Error, failure)
		}
		if !strings.Contains(p.hostProc.stderr.String(), "peer anna: link up") {
			t.Errorf("the host's log does not say the link is up:\n%s", p.hostProc.stderr.String())
		}
	})

	t.Run("a client the host does not pin", func(t *testing.T) {
		t.Parallel()
		p := pair(t, nobodyPinned, "")
		eventually(t, "the client to report the refusal", func() bool {
			r := remoteHealth(t, p.client)
			return !r.Connected && strings.Contains(r.Error, "does not admit this daemon")
		})
		eventually(t, "the host to log the refusal", func() bool {
			return strings.Contains(p.hostProc.stderr.String(), "refused a handshake from 127.0.0.1:") &&
				strings.Contains(p.hostProc.stderr.String(), identityOf(t, p.client)[:12])
		})
		// The core saw nothing: the request had no link to travel on, no peer operation was
		// recorded and the host's operations are not touched.
		if _, failure := probe(t, p.client, "operation.wait", `{"operationId":"`+noOperation+`"}`); !strings.Contains(failure, "not connected") {
			t.Errorf("a request from an unpinned client: failure %q; want the link not connected", failure)
		}
		if strings.Contains(p.hostProc.stderr.String(), "client=peer") || strings.Contains(p.hostProc.stderr.String(), "link up") {
			t.Errorf("the host recorded a peer's work:\n%s", p.hostProc.stderr.String())
		}
	})

	t.Run("a host the client does not pin", func(t *testing.T) {
		t.Parallel()
		p := pair(t, annaPinned, strings.Repeat("d", 64))
		eventually(t, "the client to refuse the host", func() bool {
			r := remoteHealth(t, p.client)
			return !r.Connected && strings.Contains(r.Error, "nothing was sent")
		})
		// No hello went out: the host never saw a session, and its peer is not connected.
		time.Sleep(500 * time.Millisecond)
		log := p.hostProc.stderr.String()
		if strings.Contains(log, "link up") || strings.Contains(log, "speaks peer protocol") {
			t.Errorf("the host heard from a client that does not trust it:\n%s", log)
		}
		if h := healthOn(t, p.host).Hosting; h.Peers[0].Connected {
			t.Errorf("the host counts anna connected: %+v", h.Peers)
		}
	})

	t.Run("a peer removed while linked", func(t *testing.T) {
		t.Parallel()
		p := pair(t, annaPinned, "")
		eventually(t, "the link to come up", func() bool { return remoteHealth(t, p.client).Connected })
		// Removing the peer from the file is the whole revocation: nobody tells the daemon.
		configAt(t, p.host, `{"hosting":{"listen":"127.0.0.1:0","peers":[],"padding":"the size changes"}}`)
		start := time.Now()
		eventually(t, "the revoked peer's socket to close", func() bool { return !remoteHealth(t, p.client).Connected })
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("the socket closed %s after the revocation; want within 2 s plus a health call or two (the sweep is 2 s; adapters/peer measures it tightly)", took)
		}
		// A redial is refused at the handshake.
		eventually(t, "the redial to be refused", func() bool {
			r := remoteHealth(t, p.client)
			return !r.Connected && strings.Contains(r.Error, "does not admit this daemon") &&
				strings.Contains(p.hostProc.stderr.String(), "refused a handshake")
		})
		if h := healthOn(t, p.host).Hosting; len(h.Peers) != 0 {
			t.Errorf("the host still lists %+v", h.Peers)
		}
	})

	t.Run("what a peer may not call", func(t *testing.T) {
		t.Parallel()
		p := pair(t, annaPinned, "")
		eventually(t, "the link to come up", func() bool { return remoteHealth(t, p.client).Connected })
		for _, name := range []string{"policy.get", "shell.exec", "device.install", "emulator.stop", "workspace.open", "system.health", "artifact.read"} {
			resp, failure := probe(t, p.client, name, `{}`)
			if failure != "" || resp.Error == nil || resp.Error.Code != capability.ErrNotAllowedHere || resp.Operation != nil {
				t.Errorf("a peer's %s: %+v / %q; want REQUEST_NOT_ALLOWED_HERE and no operation", name, resp.Error, failure)
			}
		}
		if h := healthOn(t, p.host); h.Operations != 0 || strings.Contains(p.hostProc.stderr.String(), "client=peer") {
			t.Errorf("a refused call left a trace: operations %d\n%s", h.Operations, p.hostProc.stderr.String())
		}
	})

	t.Run("the peer port", func(t *testing.T) {
		t.Parallel()
		hostState, clientState := shortDir(t), shortDir(t)
		clientFP := identityOf(t, clientState)
		daemonAt(t, hostState, `{"hosting":{"listen":"127.0.0.1:0","peers":[`+peerEntry("anna", clientFP)+`]}}`)
		addr := listenOf(t, hostState)
		id, _, err := hosting.LoadIdentity(clientState)
		if err != nil {
			t.Fatal(err)
		}
		client := func(min, max uint16) *http.Client {
			return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{id.Certificate()}, InsecureSkipVerify: true, MinVersion: min, MaxVersion: max}}}
		}
		get := func(c *http.Client, method, path, origin string) (int, error) {
			req, _ := http.NewRequest(method, "https://"+addr+path, nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			resp, err := c.Do(req)
			if err != nil {
				return 0, err
			}
			resp.Body.Close()
			return resp.StatusCode, nil
		}
		pinned := client(tls.VersionTLS13, tls.VersionTLS13)
		for _, row := range []struct {
			method, path, origin string
			want                 int
		}{
			{"GET", "/peer/v1/ws", "https://example.com", http.StatusForbidden},
			{"GET", "/peer/v1/ws", "http://127.0.0.1", http.StatusForbidden},
			{"GET", "/", "", http.StatusNotFound},
			{"POST", "/v1/call", "", http.StatusNotFound},
			{"GET", "/v1/ws", "", http.StatusNotFound},
			{"GET", "/v1/artifacts/x", "", http.StatusNotFound},
			{"GET", "/peer/v1/other", "", http.StatusNotFound},
		} {
			if code, err := get(pinned, row.method, row.path, row.origin); err != nil || code != row.want {
				t.Errorf("%s %s (Origin %q) = %d, %v; want %d", row.method, row.path, row.origin, code, err, row.want)
			}
		}
		// TLS 1.2 is refused even by a client the host pins; so is a client with no
		// certificate, and one with a certificate the host does not pin.
		if code, err := get(client(tls.VersionTLS12, tls.VersionTLS12), "GET", "/", ""); err == nil {
			t.Errorf("a TLS 1.2 client was served (%d)", code)
		}
		bare := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		if code, err := get(bare, "GET", "/", ""); err == nil {
			t.Errorf("a client with no certificate was served (%d)", code)
		}
		stranger, _, err := hosting.LoadIdentity(shortDir(t))
		if err != nil {
			t.Fatal(err)
		}
		other := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{stranger.Certificate()}, InsecureSkipVerify: true}}}
		if code, err := get(other, "GET", "/", ""); err == nil {
			t.Errorf("a client the host does not pin was served (%d)", code)
		}
	})

	t.Run("a corrupt identity", func(t *testing.T) {
		t.Parallel()
		// system.health says an identity file was moved aside and replaced (ADR-0021 §Format),
		// and the daemon runs with the new identity.
		state := shortDir(t)
		old := identityOf(t, state)
		if err := os.WriteFile(filepath.Join(state, hosting.IdentityDir, hosting.KeyFile), []byte("not a key"), 0o600); err != nil {
			t.Fatal(err)
		}
		daemonAt(t, state, "")
		h := healthOn(t, state)
		if h.Identity == nil || h.Identity.Fingerprint == "" || h.Identity.Fingerprint == old {
			t.Errorf("identity = %+v after a corrupt key (was %s); want a new fingerprint", h.Identity, old)
		}
		moved := ""
		for _, p := range h.StateProblems {
			if p.File == "identity/key.pem" && p.Problem == "corrupt" {
				moved = p.MovedTo
			}
		}
		if _, err := os.Stat(filepath.Join(state, moved)); moved == "" || err != nil {
			t.Errorf("stateProblems = %+v; want identity/key.pem corrupt, moved aside to a file that exists", h.StateProblems)
		}
	})

	t.Run("hosting.listen", func(t *testing.T) {
		t.Parallel()
		for _, listen := range []string{"0.0.0.0:7318", "[::]:7318", "qa.local:7318"} {
			// Whether something else holds the port decides what "nothing listens" can mean.
			busy := false
			if c, err := net.DialTimeout("tcp", "127.0.0.1:7318", time.Second); err == nil {
				busy = true
				c.Close()
			}
			state := shortDir(t)
			daemonAt(t, state, `{"hosting":{"listen":"`+listen+`"}}`)
			h := healthOn(t, state)
			if h.Config == nil || h.Config.State != "invalid" || !strings.Contains(h.Config.Error, "hosting.listen") {
				t.Errorf("listen %s: config %+v; want an invalid file that names hosting.listen", listen, h.Config)
			}
			if h.Hosting == nil || h.Hosting.Listen != "" || h.Hosting.Error != "" {
				t.Errorf("listen %s: hosting %+v; want no listener", listen, h.Hosting)
			}
			if !busy {
				if c, err := net.DialTimeout("tcp", "127.0.0.1:7318", time.Second); err == nil {
					c.Close()
					t.Errorf("listen %s: something accepts on 127.0.0.1:7318", listen)
				}
			}
		}

		// An address that is not this machine's: the daemon runs, hosting.error names the bind.
		state := shortDir(t)
		proc := daemonAt(t, state, `{"hosting":{"listen":"192.0.2.1:7318"}}`)
		h := healthOn(t, state)
		if h.Config == nil || h.Config.State != "ok" {
			t.Errorf("a valid address that cannot be bound makes the file %+v; want ok", h.Config)
		}
		if h.Hosting == nil || h.Hosting.Listen != "" || !strings.Contains(h.Hosting.Error, "hosting.listen 192.0.2.1:7318") {
			t.Errorf("hosting = %+v; want no listener and an error that names the bind", h.Hosting)
		}
		select {
		case err := <-proc.exited:
			t.Errorf("serve exited (%v) when its listener could not bind", err)
		default:
		}
		if !strings.Contains(proc.stderr.String(), "trying again in 30s") {
			t.Errorf("the log does not say the bind is tried again:\n%s", proc.stderr.String())
		}
	})
}

package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// DefaultPort is the port the first bind of the private listeners tries (ADR-0109 §2).
const DefaultPort = 7318

// Binder is the set of listeners this host asks to match a list of addresses: the peer
// adapter's *Server. It reports what it bound and why it could not bind the rest.
type Binder interface {
	Reconcile(want []netip.AddrPort) (bound []netip.AddrPort, errs map[netip.AddrPort]error)
}

// ConfigWriter is the configuration as the host may change it: *policy.Policy. The host
// writes one key, the port its listeners took, with the revision it read.
type ConfigWriter interface {
	Get() policy.GetResult
	Update(ctx context.Context, patch policy.UpdateParams) (policy.GetResult, *capability.Error)
}

// UseListeners gives the host the listeners Reconcile keeps matched to the configuration.
func (h *Host) UseListeners(b Binder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.binder = b
}

// SetInterfaces replaces how the host reads this machine's interfaces (SystemInterfaces):
// a test says what the machine looks like.
func (h *Host) SetInterfaces(l Lister) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ifaces = l
}

// Interfaces reads this machine's interfaces now.
func (h *Host) Interfaces() ([]Iface, error) {
	h.mu.Lock()
	l := h.ifaces
	h.mu.Unlock()
	return l()
}

// SetDefaultPort replaces the port the first bind tries (DefaultPort). Zero asks the system
// for a free one at once. A test sets it so none contends for the real default.
func (h *Host) SetDefaultPort(p int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultPort = p
}

// reconcileListeners asks the Binder for the listeners the configuration and the
// interfaces call for now (ADR-0109 §1, §2). hosting.listen wins and is exactly one
// address. Else, when hosting.on is set and this host lends a device, every private
// address of the machine that on allows, on hosting.port. Else none. A configuration or an
// interface list that cannot be read leaves the listeners as they are.
func (h *Host) reconcileListeners() {
	h.mu.Lock()
	b := h.binder
	h.mu.Unlock()
	if b == nil {
		return
	}
	hp, cerr := h.cfg.Hosting()
	if cerr != nil {
		h.once("listeners", fmt.Sprintf("droidyard: the peer listeners are left as they are: %s\n", cerr.Cause))
		return
	}
	h.once("listeners", "")
	switch {
	case hp.Listen != "":
		h.setPending(0)
		ap, err := netip.ParseAddrPort(hp.Listen)
		if err != nil { // the policy refused it already; this is only a guard
			return
		}
		b.Reconcile([]netip.AddrPort{ap})
	case hp.On != "" && len(hp.Lend) > 0:
		ifaces, err := h.Interfaces()
		if err != nil {
			h.once("interfaces", fmt.Sprintf("droidyard: the peer listeners are left as they are: this machine's interfaces could not be read: %v\n", err))
			return
		}
		h.once("interfaces", "")
		h.reconcilePrivate(b, hp, Private(ifaces, hp.On))
	default:
		h.setPending(0)
		b.Reconcile(nil)
	}
}

func (h *Host) setPending(p int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pendingPort = p
}

// reconcilePrivate binds the private addresses on the port: hosting.port, else the one the
// first bind learns (ADR-0109 §2). The port is learned once: the first address is tried on
// the default port, or on a free one when that is busy, the others follow on it, and it is
// written to hosting.port. Once written it is never replaced here, so a busy port is an
// error that is reported and tried again.
func (h *Host) reconcilePrivate(b Binder, hp policy.HostingPolicy, addrs []Addr) {
	if len(addrs) == 0 {
		h.setPending(0)
		b.Reconcile(nil)
		return
	}
	port, learning := hp.Port, false
	if port == 0 {
		learning = true
		h.mu.Lock()
		port = h.pendingPort
		def := h.defaultPort
		h.mu.Unlock()
		if port == 0 {
			port = probePort(addrs[0].IP, def)
		}
	} else {
		h.setPending(0)
	}
	want := make([]netip.AddrPort, 0, len(addrs))
	for _, a := range addrs {
		want = append(want, netip.AddrPortFrom(a.IP, uint16(port)))
	}
	bound, _ := b.Reconcile(want)
	if !learning {
		return
	}
	if !slices.ContainsFunc(bound, func(a netip.AddrPort) bool { return a.Addr() == addrs[0].IP && int(a.Port()) == port }) {
		h.setPending(0) // the first address did not take it: ask again at the next pass
		return
	}
	// Taken: written once; until it is, the same port is asked for again.
	h.setPending(port)
	if err := h.writePort(port); err != nil {
		h.once("port", fmt.Sprintf("droidyard: the peer listeners took port %d, which could not be written to hosting.port yet: %v; it is tried again\n", port, err))
		return
	}
	h.once("port", "")
	h.setPending(0)
	fmt.Fprintf(h.log, "droidyard: the peer listeners took port %d and wrote it to hosting.port\n", port)
}

// probePort is the port the first bind would get on ip: def, or a free port when def is
// busy. A failure other than a busy port returns def, so the bind reports it. def 0 asks
// the system for a free port.
func probePort(ip netip.Addr, def int) int {
	try := func(p int) (int, error) {
		ln, err := net.Listen("tcp", netip.AddrPortFrom(ip, uint16(p)).String())
		if err != nil {
			return 0, err
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port, nil
	}
	port, err := try(def)
	switch {
	case err == nil:
		return port
	case errors.Is(err, syscall.EADDRINUSE):
		if port, err := try(0); err == nil {
			return port
		}
	}
	return def
}

// writePort writes hosting.port with the revision read just before, so a concurrent edit
// of the file is not overwritten (a conflict is an error, and the write is tried again).
func (h *Host) writePort(port int) error {
	w, ok := h.cfg.(ConfigWriter)
	if !ok {
		return errors.New("the configuration cannot be written from here")
	}
	cur := w.Get()
	if cur.State == policy.StateInvalid {
		return errors.New("the configuration cannot be used")
	}
	if cur.Hosting.Port != 0 {
		// Someone wrote a port since this pass began: theirs stands (ADR-0109 §2), and the
		// revision below makes the check and the write one step.
		return nil
	}
	var up policy.UpdateParams
	raw := fmt.Sprintf(`{"hosting":{"port":%d},"ifRevision":%q}`, port, cur.Revision)
	if err := json.Unmarshal([]byte(raw), &up); err != nil {
		return err
	}
	if _, cerr := w.Update(context.Background(), up); cerr != nil {
		return fmt.Errorf("%s: %s", cerr.Code, cerr.Message)
	}
	return nil
}

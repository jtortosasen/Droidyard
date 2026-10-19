package device

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/hosting"
)

// Remote devices (ADR-0100): a device a paired host lends is named <remote>/<serial>, the
// remote being the name of a `remotes` entry. The plain class of capabilities (one call,
// one answer: uiTree, tapElement, launch, uninstall, inputText, tap, swipe, pressKey) is
// relayed: the client checks its own physical policy for the id, then asks the host to run
// the same capability on the serial and waits there. The client runs no adb command for
// it (ADR-0100 §6).

// Remotes are the links this daemon holds to the hosts it borrows devices from:
// *hosting.Host. A nil Remotes means every device is local.
type Remotes interface {
	// Remote is the link to the named remote, nil when none is configured.
	Remote(name string) *hosting.Link
	// Links are the links to every remote.
	Links() []*hosting.Link
}

// hostCaller is the link as the relay uses it: *hosting.Link.
type hostCaller interface {
	Call(ctx context.Context, req capability.RequestEnvelope) (capability.ResponseEnvelope, error)
}

// relayStart bounds the request that starts the call on the host, which answers at once
// with the operation. It is made off the caller's context so that the operation's id is
// always known, and so a cancel reaches the operation (ADR-0100 §3).
const relayStart = 10 * time.Second

// relayWait is how long one operation.wait to the host holds before the relay asks again,
// so a lost link or a cancelled call is noticed within it.
const relayWait = 30 * time.Second

// remoteTarget is what the relay's authorization hook hands the handler: the link and the
// serial on the host.
type remoteTarget struct {
	link   *hosting.Link
	remote string // the remote's name
	id     string // <remote>/<serial>, as the caller named it
	serial string
}

// plain marks d as part of the plain class: a peer may call it on a host (Lendable), and
// a call whose device is a remote id is relayed to that host instead of run here. A local
// id takes d's own authorization and handler, unchanged.
func (a *ADB) plain(d capability.Descriptor) capability.Descriptor { return a.plainThen(d, nil) }

// plainThen is plain for a capability whose relayed result names artifacts on the host: then
// receives the result decoded into the capability's own type, its device restored, and
// returns what the caller gets (remoteartifact.go). A nil then is plain.
func (a *ADB) plainThen(d capability.Descriptor, then func(ctx context.Context, rt *remoteTarget, res any) (any, error)) capability.Descriptor {
	d.Lendable = true
	authorize, handler := d.Authorize, d.Handler
	name, result, declared := d.Name, d.Result, d.Errors
	d.Authorize = func(ctx context.Context, call *capability.Call) (any, error) {
		rt, ok, err := a.authorizeRemote(call)
		if err != nil {
			return nil, err
		}
		if !ok {
			return authorize(ctx, call)
		}
		return rt, nil
	}
	d.Handler = func(ctx context.Context, call *capability.Call) (any, error) {
		if rt, ok := call.Authorized.(*remoteTarget); ok {
			res, err := a.relay(ctx, call, name, result, declared, rt)
			if err != nil || then == nil {
				return res, err
			}
			return then(ctx, rt, res)
		}
		return handler(ctx, call)
	}
	return d
}

// authorizeRemote is the authorization of a call on a remote id: ok is false for a local
// device, whose own authorization applies. The client's own physical policy decides first,
// before any host call: the host lending a phone does not admit it here (ADR-0100 §2).
func (a *ADB) authorizeRemote(call *capability.Call) (rt *remoteTarget, ok bool, err error) {
	rt, ok = a.remoteOf(call)
	if !ok {
		return nil, false, nil
	}
	if adm := a.admitted(rt.id); !adm.Admitted {
		return nil, true, notAllowed(rt.id, adm.Cause)
	}
	return rt, true, nil
}

// remoteOf reads the device a call's parameters name and, when it is <remote>/<serial> of
// a configured remote, the link it goes through. A string with a slash whose prefix is no
// remote is not one: it goes the local way and is not a listed serial. A paired peer's call
// is never relayed onward: the devices this daemon borrows are not the ones it lends, so a
// peer naming one gets the local answer, DEVICE_NOT_FOUND (ADR-0099 §1).
func (a *ADB) remoteOf(call *capability.Call) (*remoteTarget, bool) {
	if a.remotes == nil || call.Origin.Client == capability.ClientPeer {
		return nil, false
	}
	id := paramsDevice(call.Params)
	name, serial, ok := strings.Cut(id, "/")
	if !ok || serial == "" {
		return nil, false
	}
	link := a.remotes.Remote(name)
	if link == nil {
		return nil, false
	}
	return &remoteTarget{link: link, remote: name, id: id, serial: serial}, true
}

// paramsDevice is the `device` of a call's decoded parameters, "" when they have none.
func paramsDevice(params any) string {
	raw, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var v struct {
		Device string `json:"device"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.Device
}

// hostParams are the call's parameters as the host takes them: the same, with `device`
// the serial.
func hostParams(params any, serial string) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["device"], _ = json.Marshal(serial)
	return json.Marshal(m)
}

// relay runs the capability name on the host and answers as the local handler would: its
// result decoded into the capability's own result type with the device ids restored, or the
// error mapped for this daemon's contract (ADR-0100 §3, §4). The whole answer is another
// machine's, so the call is marked untrusted whatever it returns. declared are the codes
// the capability's descriptor lists.
func (a *ADB) relay(ctx context.Context, call *capability.Call, name string, result any, declared []capability.ErrorCode, rt *remoteTarget) (any, error) {
	params, err := hostParams(call.Params, rt.serial)
	if err != nil {
		return nil, capability.AsError(err)
	}
	return a.relayParams(ctx, call, name, params, result, declared, rt)
}

// relayParams is relay for a call the host takes other parameters for than the caller's
// (device.install, whose host half is device.installUpload).
func (a *ADB) relayParams(ctx context.Context, call *capability.Call, name string, params json.RawMessage, result any, declared []capability.ErrorCode, rt *remoteTarget) (any, error) {
	yes := true
	call.Untrusted = &yes
	resp, err := callHost(ctx, rt.link, name, params)
	if err != nil {
		return nil, linkError(ctx, rt, err)
	}
	if resp.Error != nil {
		return nil, a.hostError(resp.Error, declared, rt)
	}
	if resp.Operation == nil || resp.Operation.State != capability.StateCompleted || resp.Result == nil {
		return nil, incompatible(rt, "it answered neither a result nor an error")
	}
	out := reflect.New(reflect.TypeOf(result))
	if err := json.Unmarshal(resp.Result, out.Interface()); err != nil {
		return nil, incompatible(rt, "its result is not one this version reads")
	}
	setDevice(out.Elem(), rt.id)
	return out.Elem().Interface(), nil
}

// callHost starts the capability on the host and waits for it there, within ctx: the first
// request returns at once with the operation, then operation.wait is asked until the
// operation ends. When ctx ends first the host's operation is cancelled, so a cancel here
// stops the work there (ADR-0100 §3). A link lost during the call is hosting.ErrLinkLost;
// whether the host ran the call is then unknown.
func callHost(ctx context.Context, link hostCaller, name string, params json.RawMessage) (capability.ResponseEnvelope, error) {
	var zero int64
	sctx, stop := context.WithTimeout(context.WithoutCancel(ctx), relayStart)
	resp, err := link.Call(sctx, capability.RequestEnvelope{Capability: name, Params: params, WaitMs: &zero})
	stop()
	opID := ""
	for err == nil {
		if resp.Operation != nil {
			opID = resp.Operation.ID
		}
		if resp.Error != nil || resp.Operation == nil || resp.Operation.State.Terminal() {
			return resp, nil
		}
		if err = ctx.Err(); err != nil {
			break
		}
		wait := int64(relayWait / time.Millisecond)
		var p json.RawMessage
		p, _ = json.Marshal(capability.WaitParams{OperationID: opID, TimeoutMs: &wait})
		resp, err = link.Call(ctx, capability.RequestEnvelope{Capability: capability.OperationWait, Params: p})
	}
	if ctx.Err() != nil && opID != "" {
		// The caller gave up: the host's operation is stopped, best effort, off the call's
		// own (ended) context.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		p, _ := json.Marshal(capability.CancelParams{OperationID: opID})
		link.Call(cctx, capability.RequestEnvelope{Capability: capability.OperationCancel, Params: p})
	}
	return capability.ResponseEnvelope{}, err
}

// linkError maps a call the link could not carry (ADR-0100 §4): the caller's own context
// ending is that; a link that was down before the call is DEVICE_NOT_READY, unreachable;
// one lost during it is DEVICE_DISCONNECTED.
func linkError(ctx context.Context, rt *remoteTarget, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, hosting.ErrNotConnected):
		return capability.NewError(ErrNotReady, "the host "+rt.remote+" is not reachable").
			WithCause("the link to the remote " + rt.remote + " is down").
			WithDetails(map[string]any{"state": "unreachable"}).
			WithNextStep("Check that the host's droidyard is running and that the network between the two reaches it. Nothing was sent, so this call is safe to repeat.")
	case errors.Is(err, hosting.ErrLinkLost), errors.Is(err, context.DeadlineExceeded):
		// A deadline here is the start's own bound (relayStart), the caller's being alive:
		// the host did not answer in time, which is as unknown as a lost link.
		return capability.NewError(ErrDisconnected, "the link to "+rt.remote+" was lost during the call on "+rt.id).
			WithCause("the host may or may not have run the call").
			WithNextStep("Call device.list to see whether the device is still there, and look at the screen before repeating anything that acts on it.")
	}
	return capability.AsError(err)
}

// hostError maps an error the host answered with (ADR-0100 §4): a code the capability's
// descriptor declares keeps it, with the device named as the caller named it; any other
// is DEVICE_NOT_READY, incompatible, since this daemon cannot say what it means.
func (a *ADB) hostError(e *capability.Error, declared []capability.ErrorCode, rt *remoteTarget) error {
	if e.Code == capability.ErrOperationCancelled {
		// Every cancelled operation's error: the host's side ended it (its user, or its
		// shutdown), which is not an answer this daemon cannot read.
		return capability.NewError(capability.ErrOperationCancelled, "the operation was cancelled on the host "+rt.remote).
			WithCause("the host cancelled the operation; this daemon did not").
			WithNextStep("Repeat the call if it is still wanted; look at the device first if the call acts on it.")
	}
	if !slices.Contains(declared, e.Code) {
		return incompatible(rt, "it answered an error this version does not know"+codeNote(e.Code))
	}
	out := *e
	// The core appends this note to a marked failure; the host's own would double it. The
	// host names the device by its serial; here it is the id.
	out.Message = rt.named(strings.TrimSuffix(out.Message, capability.UntrustedErrorNote))
	out.Cause, out.NextStep = rt.named(out.Cause), rt.named(out.NextStep)
	if e.Code == ErrNotFound {
		out.Message = "no device " + rt.id + " is connected"
		out.Details = map[string]any{"online": a.onlineRemote(e, rt.remote)}
	}
	return &out
}

// named is text from the host with the device's bare serial, wherever it stands as a word,
// replaced by the id the caller named.
func (rt *remoteTarget) named(text string) string {
	if text == "" || rt.serial == "" {
		return text
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_.:/-])` + regexp.QuoteMeta(rt.serial) + `($|[^A-Za-z0-9_/-])`)
	// Twice, because two matches can share the character between them.
	for range 2 {
		text = re.ReplaceAllString(text, "${1}"+strings.ReplaceAll(rt.id, "$", "$$")+"${2}")
	}
	return text
}

// onlineRemote is the online candidates a host's DEVICE_NOT_FOUND names, as ids, that this
// daemon's own policy admits: the host lends them, but a phone the client's human has not
// admitted is not disclosed to the client's agents (ADR-0100 §2).
func (a *ADB) onlineRemote(e *capability.Error, remote string) []string {
	online := []string{}
	list, _ := e.Details["online"].([]any)
	for _, v := range list {
		serial, ok := v.(string)
		if !ok || serial == "" {
			continue
		}
		if id := remote + "/" + serial; a.admitted(id).Admitted {
			online = append(online, id)
		}
	}
	return online
}

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// codeNote names a code the host answered with, only when it looks like one: the host is
// another machine, and what it sends is not quoted.
func codeNote(c capability.ErrorCode) string {
	if codePattern.MatchString(string(c)) {
		return " (" + string(c) + ")"
	}
	return ""
}

// incompatible is DEVICE_NOT_READY for an answer this version cannot read: the host runs
// another version of the API than this daemon expects (ADR-0100 §4).
func incompatible(rt *remoteTarget, what string) *capability.Error {
	return capability.NewError(ErrNotReady, "the host "+rt.remote+" answered in a way this daemon cannot use").
		WithCause(what).
		WithDetails(map[string]any{"state": "incompatible"}).
		WithNextStep("Update droidyard on both machines to the same version. Whether the host ran the call is unknown.")
}

// setDevice sets the `device` field of a result, when it has one, to the id the caller
// named: the host's serial is meaningless here (ADR-0100 §1).
func setDevice(v reflect.Value, id string) {
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name == "device" && f.Type.Kind() == reflect.String && f.IsExported() {
			v.Field(i).SetString(id)
		}
	}
}

// remoteTimeout bounds one remote's listing: the host's own listing can take `devices -l`
// and then the details phase, each with a bound of its own, so the client allows the
// details bound twice over (ADR-0100 §1).
func remoteTimeout() time.Duration { return 2 * detailsTimeout }

// remoteAnswer is what one remote said to a listing: its rows as ids, or why there are none.
type remoteAnswer struct {
	rows    []Device
	warning string
}

// startRemotes asks every linked remote for its devices, in parallel, and returns the
// function that waits for them. It is called before this machine's own listing starts, so
// the two run side by side. It returns nil when there is no remote, and collect must be
// called otherwise: it releases the context the asking runs under.
func (a *ADB) startRemotes(ctx context.Context) (collect func() []remoteAnswer) {
	if a.remotes == nil {
		return nil
	}
	links := a.remotes.Links()
	if len(links) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout())
	answers := make([]remoteAnswer, len(links))
	var wg sync.WaitGroup
	for i, l := range links {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i].rows, answers[i].warning = listRemote(ctx, l)
		}()
	}
	return func() []remoteAnswer {
		wg.Wait()
		cancel()
		return answers
	}
}

// withRemotes adds the devices the remotes lend to devices (ADR-0100 §1): a device is
// listed as <remote>/<serial> with host set only when this daemon's own physical policy
// admits that id (§2). A remote that cannot be asked is a warning, in this daemon's words.
// remote reports whether any row of a remote was added, which makes the answer untrusted.
func (a *ADB) withRemotes(answers []remoteAnswer, devices []Device, warnings []string) (out []Device, warns []string, remote bool) {
	// Clipped, so the appends copy: the version check's own slice is shared.
	out, warns = devices[:len(devices):len(devices)], warnings[:len(warnings):len(warnings)]
	invalid := ""
	for _, ans := range answers {
		if ans.warning != "" {
			warns = append(warns, ans.warning)
		}
		for _, d := range ans.rows {
			if adm := a.admitted(d.Device); !adm.Admitted {
				if adm.Invalid && invalid == "" {
					invalid = adm.Cause
				}
				continue
			}
			out = append(out, d)
			remote = true
		}
	}
	if invalid != "" {
		warns = append(warns, "a device lent by a remote is hidden, because the droidyard configuration could not be used (all physical devices stay hidden until it is fixed): "+invalid)
	}
	return out, warns, remote
}

// listRemote asks one remote for the devices it lends and returns them as ids. The rows are
// physical by definition (a host lends nothing else), carry the remote's name as host, and
// hold no adb transport id: there is no local adb for them. warning is why the remote
// could not be listed, "" when it could.
func listRemote(ctx context.Context, l *hosting.Link) (rows []Device, warning string) {
	name := l.Name()
	resp, err := l.Call(ctx, capability.RequestEnvelope{Capability: "device.list", Params: json.RawMessage(`{}`)})
	switch {
	case errors.Is(err, hosting.ErrNotConnected):
		return nil, "the remote " + name + " is not reachable: its link is down, so its devices are not listed"
	case errors.Is(err, hosting.ErrLinkLost):
		return nil, "the link to the remote " + name + " was lost while its devices were listed"
	case errors.Is(err, context.DeadlineExceeded):
		return nil, "the remote " + name + " did not list its devices in " + remoteTimeout().String()
	case err != nil:
		return nil, "the remote " + name + " could not be asked for its devices"
	case resp.Error != nil:
		return nil, "the remote " + name + " could not list its devices" + codeNote(resp.Error.Code)
	}
	var r ListResult
	if json.Unmarshal(resp.Result, &r) != nil {
		return nil, "the remote " + name + " answered a device listing this version cannot read"
	}
	for _, d := range r.Devices {
		if d.Device == "" {
			continue
		}
		d.Device, d.Host = name+"/"+d.Device, name
		d.Kind, d.TransportID = KindPhysical, ""
		rows = append(rows, d)
	}
	return rows, ""
}

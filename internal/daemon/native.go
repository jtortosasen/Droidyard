package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/event"
)

// Native serves a session in the native capability envelope
// (docs/architecture/capability-api.md §Request envelope, §Response envelope): one
// JSON object per line each way, several requests in flight, each response carrying
// its request's id.
//
// A disconnect abandons the session's waits and never its operations (ADR-0020
// §Adapter projections); it tells the core, which releases what the connection owned
// (a foreground share, ADR-0108 §3). When the daemon stops reading at shutdown, requests still in
// flight are answered first, so a caller sees its operation end cancelled.
//
// A connection carries streams, as a WebSocket does (ADR-0122): it is an event
// session on bus, a streaming capability called over it answers its response envelope and
// then its events as {"subscriptionId","event"} lines, never before that envelope is
// written, and the connection's end closes the session, which releases every stream it
// owns. Frame writes carry no deadline: a reader that stopped reading holds its own
// connection, whose subscriptions overflow into events.gap, until the daemon closes it
// at shutdown. A nil bus serves no streams: such a call is SESSION_REQUIRED.
func Native(reg *capability.Registry, ops *capability.Operations, bus *event.Bus) Handler {
	return func(ctx context.Context, s *Session) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Closed when the connection is over, before the core is told: a handler that outlives
		// the connection (an operation is never cancelled by it) can see that it ended.
		gone := make(chan struct{})
		ctx = capability.WithConnectionGone(ctx, gone)

		var csess capability.Session // untyped nil without a bus: a nil *event.Session would not read as none
		var sess *event.Session
		if bus != nil {
			sess = event.NewSession(bus, s.ID)
			csess = sess
		}

		var wmu sync.Mutex // the one writer: every line goes out under it, one at a time
		enc := json.NewEncoder(s.Conn)
		var inflight, drains sync.WaitGroup
		var smu sync.Mutex
		started := map[string]bool{} // subscriptions being drained; pruned when one ends
		for {
			line, err := s.Reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				inflight.Add(1)
				go func() {
					defer inflight.Done()
					env, ok := handle(ctx, reg, ops, csess, s.Hello, s.ID, line)
					if !ok {
						return
					}
					wmu.Lock()
					werr := enc.Encode(env)
					wmu.Unlock()
					if werr != nil || sess == nil {
						return
					}
					// A response that names a subscription of this session releases its
					// events: the drain starts only after the envelope is written, and
					// writes under the same lock, so the caller sees the id before any
					// frame that carries it.
					id := subscriptionIDOf(env.Result)
					if id == "" {
						return
					}
					sub, ok := sess.Subscription(id)
					if !ok {
						return
					}
					smu.Lock()
					fresh := !started[id]
					started[id] = true
					smu.Unlock()
					if !fresh {
						return
					}
					drains.Add(1)
					go func() {
						defer drains.Done()
						defer func() {
							smu.Lock()
							delete(started, id)
							smu.Unlock()
						}()
						drain(ctx, id, sub, enc, &wmu)
					}()
				}()
			}
			if err != nil {
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					cancel()
				}
				// The connection is over, however it ended (EOF, a reset, a killed client,
				// the daemon's own shutdown): what it owned beyond its operations goes now,
				// before the requests in flight are waited for (ADR-0108 §3).
				close(gone)
				if sess != nil {
					sess.Close() // the streams this connection owns end with it
				}
				ops.Disconnected(s.ID)
				inflight.Wait()
				drains.Wait() // each is started by a request in flight, so none is added now
				return
			}
		}
	}
}

// subscriptionIDOf is the subscriptionId a result carries, "" if it carries none.
func subscriptionIDOf(result json.RawMessage) string {
	if !bytes.Contains(result, []byte(`"subscriptionId"`)) {
		return ""
	}
	var r struct {
		ID string `json:"subscriptionId"`
	}
	if json.Unmarshal(result, &r) != nil {
		return ""
	}
	return r.ID
}

// eventFrame is what the daemon sends for a subscription's event: ADR-0055's frame, the
// WebSocket's.
type eventFrame struct {
	SubscriptionID string      `json:"subscriptionId"`
	Event          event.Event `json:"event"`
}

// drain writes a subscription's envelopes, gaps included, in the order the bus delivered
// them, one line each, until the subscription or the connection ends. A failed write
// ends it: the connection is gone, or the daemon closed it.
func drain(ctx context.Context, id string, sub *event.Subscription, enc *json.Encoder, wmu *sync.Mutex) {
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			return
		}
		wmu.Lock()
		err = enc.Encode(eventFrame{SubscriptionID: id, Event: e})
		wmu.Unlock()
		if err != nil {
			return
		}
	}
}

// originOf is who a request of this connection comes from. Its ID is the connection's
// own. A hello that names a chat which is a live MCP session's joins the connection to
// that session (ADR-0095 §1): the request is attributed to it (Name, Root and Joined
// make the operation's session its ref) and ends its idleness (ADR-0094 §4), once per
// request. The join adds no ID, so the request moves no binding and holds nothing.
func originOf(ops *capability.Operations, hello Hello, sessionID string) capability.Origin {
	origin := capability.Origin{ID: sessionID, Client: hello.Client, Chat: hello.Chat}
	if hello.Chat == "" {
		return origin
	}
	if j, ok := ops.Sessions().Joined(hello.Chat); ok {
		ops.Sessions().Seen(j.ID)
		origin.Joined, origin.Name, origin.Root = j.ID, j.Name, j.Root
	}
	return origin
}

// handle answers one request line. It reports false when the caller is gone and
// nothing should be written. A workspace-scoped request that names no workspace gets
// the root the session's hello named, the CLI's cwd root (ADR-0020 §Scope: "the CLI
// from --workspace, else the cwd's root"; plan m1c, R3).
func handle(ctx context.Context, reg *capability.Registry, ops *capability.Operations, sess capability.Session, hello Hello, sessionID string, line []byte) (capability.ResponseEnvelope, bool) {
	var req capability.RequestEnvelope
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var id struct {
			ID string `json:"id"`
		}
		json.Unmarshal(line, &id)
		return capability.ResponseEnvelope{ID: id.ID, Error: capability.NewError(capability.ErrInvalidParams,
			"the request is not a valid envelope").WithCause(err.Error())}, true
	}
	env := capability.ResponseEnvelope{ID: req.ID}
	creq := capability.Request{Capability: req.Capability, Workspace: req.Workspace, Params: req.Params, Client: hello.Client,
		Session: sess, Origin: originOf(ops, hello, sessionID)}
	if d, ok := reg.Lookup(req.Capability); ok && d.Scope == capability.Workspace && creq.Workspace == "" {
		creq.Workspace = hello.Root
	}

	var out capability.Outcome
	var err error
	if d, ok := reg.Lookup(req.Capability); ok && !d.Recorded() {
		// operation.wait and operation.cancel are not operations: the envelope
		// carries the waited operation, or none.
		out, err = ops.Invoke(ctx, creq)
	} else {
		var id string
		if id, err = ops.Start(ctx, creq); err == nil {
			var deadline time.Time
			if req.WaitMs != nil {
				deadline = time.Now().Add(time.Duration(*req.WaitMs) * time.Millisecond)
			}
			out, err = ops.Wait(ctx, id, deadline)
		}
	}
	if ctx.Err() != nil {
		return env, false
	}
	if err != nil {
		env.Error = capability.AsError(err)
		return env, true
	}

	if out.OperationID != "" {
		env.Operation = &capability.OperationStatus{ID: out.OperationID, State: out.State, Progress: out.Progress, BlockedBy: out.BlockedBy}
	}
	switch out.State {
	case capability.StateCompleted:
		raw, merr := json.Marshal(out.Result)
		if merr != nil {
			env.Error = capability.AsError(merr)
			break
		}
		env.Result = raw
		env.Meta = out.Meta()
	case capability.StateFailed, capability.StateCancelled:
		env.Error = out.Error
		// A failed call whose handler marked it carries the mark here too (plan m1f,
		// D5): the error's details may quote device text.
		env.Meta = out.Meta()
	}
	return env, true
}

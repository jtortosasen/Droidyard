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
)

// Native serves a session in the native capability envelope
// (docs/architecture/capability-api.md §Request envelope, §Response envelope): one
// JSON object per line each way, several requests in flight, each response carrying
// its request's id.
//
// A disconnect abandons the session's waits and never its operations (ADR-0020
// §Adapter projections). When the daemon stops reading at shutdown, requests still in
// flight are answered first, so a caller sees its operation end cancelled.
func Native(reg *capability.Registry, ops *capability.Operations) Handler {
	return func(ctx context.Context, s *Session) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var wmu sync.Mutex
		enc := json.NewEncoder(s.Conn)
		var inflight sync.WaitGroup
		for {
			line, err := s.Reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				inflight.Add(1)
				go func() {
					defer inflight.Done()
					env, ok := handle(ctx, reg, ops, s.Hello, s.ID, line)
					if !ok {
						return
					}
					wmu.Lock()
					enc.Encode(env)
					wmu.Unlock()
				}()
			}
			if err != nil {
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					cancel()
				}
				inflight.Wait()
				return
			}
		}
	}
}

// handle answers one request line. It reports false when the caller is gone and
// nothing should be written. A workspace-scoped request that names no workspace gets
// the root the session's hello named, the CLI's cwd root (ADR-0020 §Scope: "the CLI
// from --workspace, else the cwd's root"; plan m1c, R3).
func handle(ctx context.Context, reg *capability.Registry, ops *capability.Operations, hello Hello, sessionID string, line []byte) (capability.ResponseEnvelope, bool) {
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
		Origin: capability.Origin{ID: sessionID}}
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

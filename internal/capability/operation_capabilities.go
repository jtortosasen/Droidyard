package capability

import (
	"context"
	"time"
)

// The operation capabilities' names. Adapters need them: a call to either is not an
// operation, so it is not projected as one.
const (
	OperationWait   = "operation.wait"
	OperationCancel = "operation.cancel"
)

// WaitParams are operation.wait's parameters.
type WaitParams struct {
	OperationID string `json:"operationId" jsonschema:"required"`
	// TimeoutMs bounds the wait. Omitted: no bound. 0 is a status poll.
	TimeoutMs *int64 `json:"timeoutMs,omitempty"`
}

// CancelParams are operation.cancel's parameters.
type CancelParams struct {
	OperationID string `json:"operationId" jsonschema:"required"`
}

// CancelResult is operation.cancel's result: nothing. The outcome is read with
// operation.wait.
type CancelResult struct{}

// RegisterCapabilities adds operation.wait, operation.cancel and session.watch to the
// store's registry. Operation ids are daemon-wide, so the first two are global: any session,
// or none, may wait on or cancel any operation (ADR-0020 §Operation capabilities). Their
// calls are not operations and leave no record.
func (o *Operations) RegisterCapabilities() {
	o.reg.MustRegister(Descriptor{
		Name:        OperationWait,
		Kind:        Query,
		Scope:       Global,
		Since:       "0.1.0",
		Description: "Waits for an operation to finish, up to timeoutMs, and returns its state, progress and, once completed, its result. A timeoutMs of 0 reports the current state without waiting, and waiting never affects the operation.",
		Params:      WaitParams{},
		Result:      OperationView{},
		Errors:      []ErrorCode{ErrOperationNotFound, ErrOperationCancelled},
		Handler: func(ctx context.Context, call *Call) (any, error) {
			p := call.Params.(WaitParams)
			var deadline time.Time
			if p.TimeoutMs != nil {
				deadline = o.now().Add(time.Duration(*p.TimeoutMs) * time.Millisecond)
			}
			return o.Wait(ctx, p.OperationID, deadline)
		},
		unrecorded: true,
	})

	o.reg.MustRegister(Descriptor{
		Name:        OperationCancel,
		Kind:        Command,
		Scope:       Global,
		Idempotent:  true,
		Since:       "0.1.0",
		Description: "Asks a running operation to stop, whichever session started it; cancelling a finished one does nothing. It does not wait: call operation.wait to see the operation end cancelled.",
		Params:      CancelParams{},
		Result:      CancelResult{},
		Errors:      []ErrorCode{ErrOperationNotFound},
		// Destructive by the command default, kept on purpose: it stops in-flight
		// work, possibly work the UI or another agent started
		// (docs/interfaces/mcp.md §Annotations).
		Handler: func(ctx context.Context, call *Call) (any, error) {
			return CancelResult{}, o.Cancel(call.Params.(CancelParams).OperationID)
		},
		unrecorded: true,
	})

	o.registerSessionWatch()
	o.registerSessionRelease()
}

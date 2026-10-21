package capability

import (
	"fmt"
	"time"
)

// Recorded is an operation that is recorded after it happened, already terminal: a gesture
// of the live view, whose input travelled its stream and not a call (ADR-0117 §6). It is
// the record of what a call to Capability with Params would have been, so Activity, the
// trace and operation.state show it as they show a call. It says what the view sent, not that
// the device accepted it: a completed record is not proof of delivery.
type Recorded struct {
	// Capability names the capability that replays the gesture; it must be a global,
	// recorded one.
	Capability string
	// Origin is the call the gesture belongs to (the view's device.watchScreen): its client,
	// session and, for a peer, its name. Its Client must be set.
	Origin Origin
	// Params is the capability's parameter value, which the descriptor's Target reads.
	Params any
	// Started and Finished bound the gesture; a zero Started is now and a Finished before
	// Started is Started.
	Started, Finished time.Time
	// State is completed, cancelled or failed.
	State State
	// Result is the completed operation's result.
	Result any
	// Err is the failed operation's error; a cancelled operation's is OPERATION_CANCELLED.
	Err *Error
}

// Record inserts a terminal operation and queues its operation.state events (its start and
// its terminal state) and its trace line, all under the store's lock, as one operation of
// the same capability would have left them. It does not flush them: a flush on a goroutine
// of its own publishes the events and writes the line, so a trace writer or a publisher that
// stalls delays the flush and never Record, which holds the lock for the length of an
// insert. The operation takes no resource and runs no authorization hook or handler: the
// authorization was the view's, when it opened (ADR-0117 §1).
//
// It returns the operation's id. A capability that is unknown, not recorded, workspace
// scoped or not in a terminal state here is an error and records nothing.
func (o *Operations) Record(r Recorded) (string, error) {
	d, ok := o.reg.Lookup(r.Capability)
	switch {
	case !ok:
		return "", Errorf(ErrCapabilityNotFound, "no capability named %q", r.Capability)
	case !d.Recorded() || d.Scope != Global:
		return "", fmt.Errorf("capability %s cannot be recorded after the fact: it is not a recorded, global one", d.Name)
	}
	var opErr *Error
	switch r.State {
	case StateCompleted:
	case StateCancelled:
		opErr = NewError(ErrOperationCancelled, "the operation was cancelled")
	case StateFailed:
		if r.Err == nil {
			return "", fmt.Errorf("a failed record of %s needs its error", d.Name)
		}
		opErr = r.Err
	default:
		return "", fmt.Errorf("a record of %s cannot be in state %q: completed, cancelled or failed", d.Name, r.State)
	}
	var tgt *OpTarget
	if d.Target != nil {
		tgt = boundTarget(d.Target(r.Params))
	}
	started := r.Started
	if started.IsZero() {
		started = o.now()
	}
	finished := r.Finished
	if finished.Before(started) {
		finished = started
	}
	// Read before the lock: both take locks of their own (the workspace registry's, the
	// session registry's).
	session, owner := o.sessionRef(r.Origin), o.ownerOf(r.Origin.Client, r.Origin)

	op := &operation{
		id: newID(), capability: d.Name, client: r.Origin.Client, session: session, target: tgt, owner: owner,
		started: started, finished: finished, announced: true, state: StateRunning,
		cancel: func() {}, done: make(chan struct{}),
	}
	o.mu.Lock()
	o.ops[op.id] = op
	o.queueStateLocked(op) // the start
	op.state, op.err = r.State, opErr
	if r.State == StateCompleted {
		op.result, op.slim, op.untrusted = r.Result, d.Slim, d.untrusted
	}
	o.terminal = append(o.terminal, op.id)
	o.traces = append(o.traces, traceLine(op))
	o.queueStateLocked(op)
	close(op.done)
	o.prune()
	kick := !o.flushing && (len(o.events) > 0 || len(o.traces) > 0)
	if kick {
		o.flushing = true
	}
	o.mu.Unlock()
	if kick {
		go o.flushLoop()
	}
	return op.id, nil
}

// flushLoop is the goroutine Record starts to publish and write what it queued: it flushes
// until nothing is left, then ends, so an idle store holds no goroutine. A flush stalled in a
// publisher or a trace writer keeps Record's queue growing, not Record waiting.
func (o *Operations) flushLoop() {
	for {
		o.flush()
		o.mu.Lock()
		if len(o.events) == 0 && len(o.traces) == 0 {
			o.flushing = false
			o.mu.Unlock()
			return
		}
		o.mu.Unlock()
	}
}

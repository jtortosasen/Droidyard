// Package cli is the CLI adapter: `droidyard call` over the daemon's socket, in the
// native capability envelope (ADR-0019 §3; docs/architecture/capability-api.md
// §Mapping to adapters). It translates only; the connection and its hello are the
// caller's.
//
// A call is Start plus a wait with no deadline (ADR-0020 §Adapter projections, CLI
// row): the request goes with waitMs 0, which answers at once with the operation's
// id, and the wait continues through operation.wait on the same connection. Ctrl-C
// cancels the operation; --detach prints its id and leaves it running; a CLI that is
// killed leaves it running too, since operations belong to the daemon.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Usage is the call subcommand's synopsis.
const Usage = "usage: droidyard call <capability> [--params <json>] [--detach] [--no-autostart]"

// Exit statuses beyond 0 and 1.
const (
	// ExitUsage is a malformed command line.
	ExitUsage = 2
	// ExitInterrupted follows the shell convention for SIGINT (128 + 2).
	ExitInterrupted = 130
)

// Call is one parsed `droidyard call`.
type Call struct {
	Capability  string
	Params      json.RawMessage
	Detach      bool
	NoAutostart bool
}

// Parse reads the arguments after `call`.
func Parse(args []string) (Call, error) {
	var c Call
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--no-autostart":
			c.NoAutostart = true
		case a == "--detach":
			c.Detach = true
		case a == "--params" || strings.HasPrefix(a, "--params="):
			v, ok := strings.CutPrefix(a, "--params=")
			if !ok {
				if i+1 == len(args) {
					return c, errors.New("--params needs a JSON object")
				}
				i++
				v = args[i]
			}
			if !json.Valid([]byte(v)) {
				return c, fmt.Errorf("--params is not valid JSON: %s", v)
			}
			c.Params = json.RawMessage(v)
		case strings.HasPrefix(a, "-"):
			return c, fmt.Errorf("unknown flag %s", a)
		case c.Capability != "":
			return c, fmt.Errorf("unexpected argument %q: one capability per call", a)
		default:
			c.Capability = a
		}
	}
	if c.Capability == "" {
		return c, errors.New("no capability named")
	}
	if c.Detach && !startsOperation(c.Capability) {
		return c, fmt.Errorf("--detach: a call to %s is not an operation, so there is nothing to detach from", c.Capability)
	}
	return c, nil
}

// startsOperation is false for operation.wait and operation.cancel, whose calls are
// not operations (ADR-0020 §Core primitives): they are sent once and answered once.
func startsOperation(name string) bool {
	return name != capability.OperationWait && name != capability.OperationCancel
}

// Request ids on the connection.
const (
	startID  = "cli-1"
	waitID   = "cli-2"
	cancelID = "cli-cancel"
)

// Streams are where a call prints: the response envelope on Out, messages on Err.
type Streams struct {
	Out io.Writer
	Err io.Writer
}

// Run sends the call on w and reads its answers from r. It prints the final
// response envelope (for a detached call, the operation id) and returns the exit
// status: 1 iff the envelope carries an error. When interrupt closes while an
// operation runs, it sends operation.cancel and returns ExitInterrupted once that is
// answered.
func (c Call) Run(r *bufio.Reader, w io.Writer, s Streams, interrupt <-chan struct{}) (int, error) {
	responses := make(chan response, 4)
	go read(r, responses)

	if !startsOperation(c.Capability) {
		if err := send(w, capability.RequestEnvelope{ID: startID, Capability: c.Capability, Params: c.Params}); err != nil {
			return 1, err
		}
		// operation.wait and operation.cancel are not operations: Ctrl-C only stops
		// waiting for the answer.
		env, err := awaitOr(responses, startID, interrupt)
		if errors.Is(err, errInterrupted) {
			fmt.Fprintf(s.Err, "droidyard call: interrupted; %s was not an operation, nothing to cancel\n", c.Capability)
			return ExitInterrupted, nil
		}
		if err != nil {
			return 1, err
		}
		return emit(s.Out, env)
	}

	zero := int64(0)
	if err := send(w, capability.RequestEnvelope{ID: startID, Capability: c.Capability, Params: c.Params, WaitMs: &zero}); err != nil {
		return 1, err
	}
	first, err := await(responses, startID)
	if err != nil {
		return 1, err
	}
	op := first.env.Operation
	switch {
	case op == nil:
		// Rejected.
		return emit(s.Out, first)
	case c.Detach:
		// The id, whatever the state: the caller reads the outcome with operation.wait
		// (ADR-0020 §Adapter projections, CLI row).
		fmt.Fprintln(s.Out, op.ID)
		return 0, nil
	case op.State.Terminal():
		return emit(s.Out, first)
	}

	params, _ := json.Marshal(capability.WaitParams{OperationID: op.ID})
	if err := send(w, capability.RequestEnvelope{ID: waitID, Capability: capability.OperationWait, Params: params}); err != nil {
		return 1, err
	}
	for {
		select {
		case resp, ok := <-responses:
			if !ok || resp.err != nil {
				return 1, resp.closed()
			}
			if resp.env.ID == waitID {
				return emit(s.Out, resp)
			}
		case <-interrupt:
			return c.cancel(w, responses, s, op.ID)
		}
	}
}

// cancel asks the daemon to cancel the operation and returns once it has answered.
func (c Call) cancel(w io.Writer, responses <-chan response, s Streams, id string) (int, error) {
	params, _ := json.Marshal(capability.CancelParams{OperationID: id})
	if err := send(w, capability.RequestEnvelope{ID: cancelID, Capability: capability.OperationCancel, Params: params}); err != nil {
		return 1, err
	}
	resp, err := await(responses, cancelID)
	if err != nil {
		return 1, err
	}
	if resp.env.Error != nil {
		fmt.Fprintf(s.Err, "droidyard call: interrupted; cancelling operation %s failed: %s: %s\n", id, resp.env.Error.Code, resp.env.Error.Message)
		return ExitInterrupted, nil
	}
	fmt.Fprintf(s.Err, "droidyard call: interrupted; cancelling operation %s (see: droidyard call operation.wait --params '{\"operationId\":\"%s\"}')\n", id, id)
	return ExitInterrupted, nil
}

type response struct {
	line []byte
	env  capability.ResponseEnvelope
	err  error // set on the last value before the channel closes
}

func (r response) closed() error {
	if r.err != nil {
		return r.err
	}
	return errors.New("the daemon closed the connection before answering")
}

// read delivers every response line until the connection ends; its last value
// carries the reason.
func read(r *bufio.Reader, out chan<- response) {
	defer close(out)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			out <- response{err: fmt.Errorf("the daemon closed the connection before answering: %w", err)}
			return
		}
		var env capability.ResponseEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			out <- response{err: fmt.Errorf("the daemon's response is not an envelope: %w", err)}
			return
		}
		out <- response{line: line, env: env}
	}
}

var errInterrupted = errors.New("interrupted")

// awaitOr is await, abandoned with errInterrupted when interrupt closes first.
func awaitOr(responses <-chan response, id string, interrupt <-chan struct{}) (response, error) {
	for {
		select {
		case resp, ok := <-responses:
			if !ok || resp.err != nil {
				return resp, resp.closed()
			}
			if resp.env.ID == id {
				return resp, nil
			}
		case <-interrupt:
			return response{}, errInterrupted
		}
	}
}

// await returns the response to request id.
func await(responses <-chan response, id string) (response, error) {
	for resp := range responses {
		if resp.err != nil {
			return resp, resp.err
		}
		if resp.env.ID == id {
			return resp, nil
		}
	}
	return response{}, errors.New("the daemon closed the connection before answering")
}

func send(w io.Writer, req capability.RequestEnvelope) error {
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("sending the request: %w", err)
	}
	return nil
}

// emit writes the envelope as the daemon sent it; the status is 1 iff it carries an
// error.
func emit(out io.Writer, resp response) (int, error) {
	if _, err := out.Write(resp.line); err != nil {
		return 1, err
	}
	if resp.env.Error != nil {
		return 1, nil
	}
	return 0, nil
}

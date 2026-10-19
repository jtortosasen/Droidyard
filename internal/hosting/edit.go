package hosting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// Config is the policy as the daemon's own commands change it: the current state, or the
// error `policy.update` gives when it cannot be used. It is the read every share and join
// makes before it dials or writes anything.
func (h *Host) Config() (policy.GetResult, *capability.Error) {
	w, ok := h.cfg.(ConfigWriter)
	if !ok {
		return policy.GetResult{}, capability.NewError(capability.ErrPolicyConfigUnusable, "the configuration cannot be read from here")
	}
	cur := w.Get()
	if cur.State == policy.StateInvalid {
		return cur, capability.NewError(capability.ErrPolicyConfigUnusable, "the droidyard configuration could not be used, so nothing was changed").
			WithCause(cur.Path + ": " + cur.Error).
			WithNextStep("fix or remove " + cur.Path + ", then retry")
	}
	return cur, nil
}

// Edit changes the policy the way a verb of `droidyard` does (ADR-0108 §1: the human who
// runs the command is the consent): build reads the policy as it is and returns the merge
// patch to write, nil for nothing, or an error that stops it; the write carries the revision
// build read, so a concurrent edit is never overwritten. A conflict is read and decided again
// once. The edits of this daemon's own commands (a share, an admit, a join) are serialised,
// so only a human's edit of the file can conflict.
func (h *Host) Edit(ctx context.Context, build func(cur policy.GetResult) (map[string]any, *capability.Error)) (policy.GetResult, *capability.Error) {
	h.editMu.Lock()
	defer h.editMu.Unlock()
	w, ok := h.cfg.(ConfigWriter)
	if !ok {
		return policy.GetResult{}, capability.NewError(capability.ErrPolicyConfigUnusable, "the configuration cannot be written from here")
	}
	for attempt := 0; ; attempt++ {
		cur, cerr := h.Config()
		if cerr != nil {
			return cur, cerr
		}
		patch, cerr := build(cur)
		if cerr != nil {
			return cur, cerr
		}
		if patch == nil {
			return cur, nil
		}
		patch["ifRevision"] = cur.Revision
		raw, err := json.Marshal(patch)
		var up policy.UpdateParams
		if err == nil {
			err = json.Unmarshal(raw, &up)
		}
		if err != nil {
			return cur, capability.Errorf(capability.ErrInvalidParams, "the change to the configuration is not valid: %v", err)
		}
		res, cerr := w.Update(ctx, up)
		if cerr == nil {
			return res, nil
		}
		if cerr.Code == capability.ErrPolicyRevisionConflict && attempt == 0 {
			continue
		}
		return cur, cerr
	}
}

// errNotLent is why Admit refuses a join whose device is no longer lent.
var errNotLent = errors.New("the device is no longer lent, so the join is refused; its code is spent")

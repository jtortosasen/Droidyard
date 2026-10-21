package policy_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jtortosasen/droidyard/internal/policy"
)

// Decision (ADR-0118 §4): a remote's `fast` mark is read as the file spells it, written by a
// patch (the list replaces whole, so an entry without it is not fast) and read live; a value
// that is not a boolean is refused like any other invalid value. Seen failing with decodeRemotes
// ignoring the key: the remote read back as not fast.
func TestRemoteFastMarkIsReadWrittenAndValidated(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `{"schemaVersion":1,"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`","fast":true}]}`)
	p := policy.New(envAt(dir))
	if got, err := p.Remotes(); err != nil || len(got) != 1 || !got[0].Fast {
		t.Fatalf("remotes = %+v, %v; want qa marked fast", got, err)
	}
	ctx := context.Background()
	res, err := p.Update(ctx, patch(t, `{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`"}]}`))
	if err != nil || len(res.Remotes) != 1 || res.Remotes[0].Fast {
		t.Errorf("after replacing the list without the mark: %+v, %v; want not fast", res.Remotes, err)
	}
	var up policy.UpdateParams
	if err := json.Unmarshal([]byte(`{"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`","fast":"yes"}]}`), &up); err == nil {
		t.Error(`a fast of "yes" decoded; want it refused at the decode`)
	}
	write(t, dir, `{"schemaVersion":1,"remotes":[{"name":"qa","address":"10.0.0.5:7318","fingerprint":"`+fpA+`","fast":"yes"}]}`)
	if _, err := policy.New(envAt(dir)).Remotes(); err == nil {
		t.Error(`a file with a fast of "yes" was read; want the configuration unusable`)
	}
}

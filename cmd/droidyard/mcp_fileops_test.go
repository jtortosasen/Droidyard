package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// TestCLIBatchWithFailedItemsCompletes holds ADR-0053 §2 over the CLI, the surface that
// keeps file.move once the file capabilities leave MCP (ADR-0060): a batch with one
// colliding item and one free item exits 0 with the operation completed, the colliding
// item carries FILE_EXISTS and its target keeps its bytes, the free item is done; a source
// outside the root exits non-zero with FILE_OUTSIDE_ROOT.
func TestCLIBatchWithFailedItemsCompletes(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	root, _ := projectWith(t, "settings.gradle")
	for name, content := range map[string]string{"a.kt": "A", "b.kt": "taken", "c.kt": "C"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env, code := callIn(t, p.state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, env.Error)
	}

	env, code := callIn(t, p.state, root, "file.move", "--params", `{"items":[{"from":"a.kt","to":"b.kt"},{"from":"c.kt","to":"d.kt"}]}`)
	if code != 0 || env.Error != nil || env.Operation == nil || env.Operation.State != capability.StateCompleted {
		t.Fatalf("a batch with a failed item: exit %d, error %+v, operation %+v; want exit 0 and a completed operation", code, env.Error, env.Operation)
	}
	var res struct {
		Items []struct{ From, To, Code string }
		Done  int
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.Done != 1 || len(res.Items) != 2 ||
		res.Items[0].Code != "FILE_EXISTS" || res.Items[1].Code != "" {
		t.Fatalf("result %s (%v); want item 1 FILE_EXISTS and item 2 done", env.Result, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.kt")); string(b) != "taken" {
		t.Errorf("the target holds %q after the refused item", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d.kt")); string(b) != "C" {
		t.Errorf("the done item left %q at its target", b)
	}

	bad, code := callIn(t, p.state, root, "file.move", "--params", `{"items":[{"from":"../outside","to":"x"}]}`)
	if code == 0 || bad.Error == nil || bad.Error.Code != "FILE_OUTSIDE_ROOT" {
		t.Errorf("a source outside the root: exit %d, %+v; want non-zero and FILE_OUTSIDE_ROOT", code, bad.Error)
	}
}

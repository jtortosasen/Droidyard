package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTrashSurvivesADaemonRestart holds ADR-0054 §4 on a real daemon: file.delete moves a
// project file into the workspace's trash under the state root, the daemon is stopped and
// started again, and file.restore by the id the delete returned puts the file back with
// its bytes. The trash is a folder on disk, not the daemon's memory.
func TestTrashSurvivesADaemonRestart(t *testing.T) {
	t.Parallel()
	state := shortDir(t)
	root, sub := gradleProject(t)
	file := filepath.Join(sub, "Main.kt")
	if err := os.WriteFile(file, []byte("fun main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type items struct {
		Items []struct {
			Path    string `json:"path"`
			TrashID string `json:"trashId"`
			Code    string `json:"code"`
		} `json:"items"`
		Done int `json:"done"`
	}
	first := droidyard(t, state, "serve")
	first.expect(t, "listening")
	info, code := callIn(t, state, root, "workspace.open")
	if code != 0 {
		t.Fatalf("workspace.open exited %d: %+v", code, info.Error)
	}
	wsid := workspaceOf(t, info).ID

	env, code := callIn(t, state, root, "file.delete", "--params", `{"paths":["app/src/Main.kt"]}`)
	var deleted items
	if code != 0 || json.Unmarshal(env.Result, &deleted) != nil || deleted.Done != 1 || len(deleted.Items) != 1 || deleted.Items[0].TrashID == "" {
		t.Fatalf("file.delete exited %d with %s (%+v); want one item done with a trashId", code, env.Result, env.Error)
	}
	id := deleted.Items[0].TrashID
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("the file is still at its path after file.delete: %v", err)
	}
	item := filepath.Join(state, "ws", wsid, "trash", id, "item")
	if b, err := os.ReadFile(item); err != nil || string(b) != "fun main() {}\n" {
		t.Fatalf("the trash holds %q, %v; want the file's bytes at %s", b, err, item)
	}

	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("stop exited %d", code)
	}
	if code := first.wait(t); code != 0 {
		t.Fatalf("the first daemon exited %d", code)
	}
	second := droidyard(t, state, "serve")
	second.expect(t, "listening")
	if _, code := callIn(t, state, root, "workspace.open"); code != 0 {
		t.Fatalf("workspace.open on the restarted daemon exited %d", code)
	}

	env, code = callIn(t, state, root, "file.restore", "--params", `{"trashIds":["`+id+`"]}`)
	var restored items
	if code != 0 || json.Unmarshal(env.Result, &restored) != nil || restored.Done != 1 {
		t.Fatalf("file.restore after the restart exited %d with %s (%+v); want the item done", code, env.Result, env.Error)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "fun main() {}\n" {
		t.Errorf("after the restore the file holds %q, %v; want its bytes back", b, err)
	}
	if _, err := os.Lstat(filepath.Join(state, "ws", wsid, "trash", id)); !os.IsNotExist(err) {
		t.Errorf("the trash entry is still there after the restore: %v", err)
	}
	if code := droidyard(t, state, "stop").wait(t); code != 0 {
		t.Fatalf("the second stop exited %d", code)
	}
	second.wait(t)
}

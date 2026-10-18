package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// file.trash (plan m2s step 8; ADR-0096). Each test says which decision it holds.

// deleteAs deletes paths with a call that carries origin, and returns the trash ids.
func (f *trashFix) deleteAs(origin capability.Origin, paths ...string) []string {
	f.t.Helper()
	d, _ := f.reg.Lookup("file.delete")
	res, err := d.Handler(context.Background(), &capability.Call{Workspace: "ws", Params: DeleteParams{Paths: paths}, Origin: origin})
	if err != nil {
		f.t.Fatal(err)
	}
	var ids []string
	for _, it := range res.(DeleteResult).Items {
		if it.TrashID == "" {
			f.t.Fatalf("not deleted: %+v", it)
		}
		ids = append(ids, it.TrashID)
	}
	return ids
}

func (f *trashFix) trashList(ws string) (TrashResult, *capability.Error) {
	f.t.Helper()
	res, err := f.callWS(context.Background(), ws, "file.trash", TrashParams{})
	if err != nil {
		return TrashResult{}, err
	}
	return res.(TrashResult), nil
}

// plant writes an entry the way an older binary, or a crash, would have left it: a record
// and, when withItem, the item beside it.
func (f *trashFix) plant(id string, e trashEntry, withItem bool) {
	f.t.Helper()
	if err := f.st.Write(entryName("ws", id), e); err != nil {
		f.t.Fatal(err)
	}
	if withItem {
		if err := os.WriteFile(filepath.Join(f.trashDir("ws"), id, trashItem), []byte("x"), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
}

// TestTrashListing holds ADR-0096 §1 and §2: the trash is listed newest first and bounded
// at 200; `by` is the call's origin (client, and the session's name when the call was
// joined to one); an entry written before `by` existed lists without it and still
// restores; an entry that cannot be read is skipped, not an error and not moved aside.
func TestTrashListing(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("a.kt", "aaa")
	f.write("b.kt", "bb")
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	trashNow = func() time.Time { return now }
	idA := f.deleteAs(capability.Origin{ID: "c1", Client: capability.ClientCLI, Name: "Claude Code", Joined: "s1"}, "a.kt")[0]
	now = now.Add(time.Minute)
	idB := f.deleteAs(capability.Origin{ID: "c2", Client: capability.ClientUI}, "b.kt")[0]

	res, err := f.trashList("ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 || res.Entries[0].ID != idB || res.Entries[1].ID != idA {
		t.Fatalf("entries = %+v; want newest first: %s then %s", res.Entries, idB, idA)
	}
	if by := res.Entries[1].By; by == nil || by.Client != "cli" || by.Name != "Claude Code" {
		t.Errorf("by of the joined CLI delete = %+v; want cli named by the session", by)
	}
	if by := res.Entries[0].By; by == nil || by.Client != "ui" || by.Name != "" {
		t.Errorf("by of the UI delete = %+v; want ui without a name", by)
	}
	if e := res.Entries[1]; e.Path != "a.kt" || e.Type != "file" || e.Size != 3 || !e.DeletedAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("entry = %+v", e)
	}

	// An entry from before `by` existed: listed without it, and it restores.
	f.plant("00000000000000a1", trashEntry{Path: "old.kt", Type: "file", DeletedAt: now.Add(-time.Hour), Bytes: 5}, true)
	// Unreadable: not JSON, a directory in place of the file, a path the trash never
	// writes, and a delete under way (a record and no item).
	for id, body := range map[string]string{"00000000000000b1": "{not json", "00000000000000b2": `{"schemaVersion":1,"path":"../x","type":"file","deletedAt":"2026-10-18T10:00:00Z","bytes":1}`} {
		if err := os.MkdirAll(filepath.Join(f.trashDir("ws"), id), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.trashDir("ws"), id, entryFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.trashDir("ws"), id, trashItem), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.trashDir("ws"), "00000000000000b3", entryFile), 0o700); err != nil {
		t.Fatal(err)
	}
	f.plant("00000000000000b5", trashEntry{Path: "odd.kt", Type: "socket-ish", DeletedAt: now}, true)
	f.plant("00000000000000b4", trashEntry{Path: "underway.kt", Type: "file", DeletedAt: now}, false)

	res, err = f.trashList("ws")
	if err != nil {
		t.Fatalf("an unreadable entry made the listing fail: %v", err)
	}
	var ids []string
	for _, e := range res.Entries {
		ids = append(ids, e.ID)
	}
	if want := []string{idB, idA, "00000000000000a1"}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("ids = %v; want %v (the unreadable and the unfinished skipped)", ids, want)
	}
	if res.Entries[2].By != nil {
		t.Errorf("an entry without `by` listed with %+v", res.Entries[2].By)
	}
	// The listing moved nothing aside: the unparseable record is where it was.
	if b, rerr := os.ReadFile(filepath.Join(f.trashDir("ws"), "00000000000000b1", entryFile)); rerr != nil || string(b) != "{not json" {
		t.Errorf("the unreadable record was touched: %q %v", b, rerr)
	}
	rr, rerr := f.restore("00000000000000a1")
	if rerr != nil || rr.Done != 1 {
		t.Errorf("an entry without `by` does not restore: %+v %v", rr, rerr)
	}
}

// TestTrashListingBound holds ADR-0096 §1: at most 200 entries, and the 200 are the newest.
func TestTrashListingBound(t *testing.T) {
	f := newTrashFixture(t, nil)
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	for i := 0; i < trashListMax+5; i++ {
		f.plant(fmt.Sprintf("%016x", i+1), trashEntry{Path: fmt.Sprintf("f%d.kt", i), Type: "file", DeletedAt: base.Add(time.Duration(i) * time.Second)}, true)
	}
	res, err := f.trashList("ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != trashListMax {
		t.Fatalf("%d entries; want %d", len(res.Entries), trashListMax)
	}
	if res.Entries[0].Path != fmt.Sprintf("f%d.kt", trashListMax+4) || res.Entries[trashListMax-1].Path != "f5.kt" {
		t.Errorf("first %q last %q; want the newest 200", res.Entries[0].Path, res.Entries[trashListMax-1].Path)
	}
}

// TestTrashEntryByIsAdditive holds ADR-0096 §2 ("entry.json gains an additive key"): the
// key is written under `by`, a call without a kind writes none, and the entry still reads
// as the older shape (no strict decoding): an older binary's struct is the same without it.
func TestTrashEntryByIsAdditive(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("a.kt", "a")
	f.write("b.kt", "b")
	ida := f.deleteAs(capability.Origin{Client: capability.ClientCLI, Name: "Chat"}, "a.kt")[0]
	idb := f.deleteAs(capability.Origin{}, "b.kt")[0]

	_, raw := f.entry(ida)
	by, _ := raw["by"].(map[string]any)
	if by["client"] != "cli" || by["name"] != "Chat" {
		t.Errorf("by = %v", raw["by"])
	}
	if _, has := f.rawEntry(idb)["by"]; has {
		t.Errorf("a call without a kind wrote by: %v", f.rawEntry(idb))
	}
	// An older reader: the entry without the key decodes into a struct that does not know it.
	var older struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	b, _ := os.ReadFile(filepath.Join(f.trashDir("ws"), ida, entryFile))
	if json.Unmarshal(b, &older) != nil || older.Path != "a.kt" {
		t.Errorf("an older reader fails on the new key: %q", b)
	}
}

func (f *trashFix) rawEntry(id string) map[string]any {
	f.t.Helper()
	_, raw := f.entry(id)
	return raw
}

// TestTrashListWithoutTrash: a daemon built without a trash answers the code the
// descriptor lists, and an unopened trash folder is an empty list, not an error.
func TestTrashListWithoutTrash(t *testing.T) {
	f := newTrashFixture(t, nil)
	res, err := f.trashList("ws")
	if err != nil || len(res.Entries) != 0 || res.Entries == nil {
		t.Errorf("empty trash = %+v %v; want an empty list", res, err)
	}
	g := newFixture(t)
	if _, e := g.call("file.trash", TrashParams{}); e == nil || e.Code != capability.ErrFileIOFailed {
		t.Errorf("without a trash: %v; want FILE_IO_FAILED", e)
	}
}

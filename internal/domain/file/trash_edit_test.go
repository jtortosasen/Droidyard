package file

import (
	"os"
	"path/filepath"
	"testing"
)

// Decision (ADR-0054 §2; the step 5 verifier's re-review): the aside is removed only if it
// is still what was copied, compared by modification time as well as size, so an edit in
// place that keeps a file's size, by a process holding a descriptor inside the aside, is
// not lost with it: the item fails and the edited file is back at its path. Seen failing
// with sizes alone compared (the trash held the old bytes and the edit was nowhere).
func TestDeleteAcrossVolumesKeepsASameSizeEdit(t *testing.T) {
	f := newTrashFixture(t, nil)
	f.write("v/tree/a.kt", "AAAA")
	crossVolume()
	trashSeam = func(stage string) error {
		if stage != stageStored {
			return nil
		}
		as := asides(t, filepath.Join(f.root, "v"))
		if len(as) != 1 {
			t.Fatalf("asides %v; want one", as)
		}
		return os.WriteFile(filepath.Join(f.root, "v", as[0], "a.kt"), []byte("BBBB"), 0o644)
	}
	res, err := f.del("v/tree")
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 0 || res.Items[0].Code != "FILE_IO_FAILED" {
		t.Fatalf("delete: %+v; want the item FILE_IO_FAILED (changed while being deleted)", res)
	}
	if b, err := os.ReadFile(filepath.Join(f.root, "v/tree/a.kt")); err != nil || string(b) != "BBBB" {
		t.Fatalf("v/tree/a.kt is %q, %v; want the edit back at its path", b, err)
	}
	if as := asides(t, filepath.Join(f.root, "v")); len(as) != 0 {
		t.Errorf("asides left: %v", as)
	}
}

package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Decision (ADR-0053 §2, D48; the step 4 verifier's finding): a file that grows between
// the walk and its copy is copied whole while the item stays under its byte cap, and past
// the cap the item fails FILE_TOO_LARGE with no destination and no temporary; a copy is
// never cut to the walked size and reported done. Seen failing with each file limited to
// its walked size (the copy held "short").
func TestCopyOfAFileThatGrewAfterTheWalk(t *testing.T) {
	grown := "short plus what the editor saved"
	t.Run("under the cap", func(t *testing.T) {
		withCopySeams(t)
		f := newFixture(t)
		f.write("g/a.kt", "short")
		copySeam = func(stage, rel string) error {
			if stage == stageWalked {
				return os.WriteFile(filepath.Join(f.root, "g", "a.kt"), []byte(grown), 0o644)
			}
			return nil
		}
		res, err := f.copy([]CopyItem{cp("g", "g2")}, "")
		if err != nil || res.Done != 1 {
			t.Fatalf("copy: %+v, %v; want done", res, err)
		}
		if got, _ := os.ReadFile(filepath.Join(f.root, "g2", "a.kt")); string(got) != grown {
			t.Fatalf("the copy holds %q; want the whole grown file %q", got, grown)
		}
	})
	t.Run("past the cap", func(t *testing.T) {
		withCopySeams(t)
		f := newFixtureWith(t, func(o *Options) { o.CopyMaxBytes = 10 })
		f.write("g/a.kt", "short")
		copySeam = func(stage, rel string) error {
			if stage == stageWalked {
				return os.WriteFile(filepath.Join(f.root, "g", "a.kt"), []byte(grown), 0o644)
			}
			return nil
		}
		res, err := f.copy([]CopyItem{cp("g", "g2")}, "")
		if err != nil || res.Failed != 1 || res.Items[0].Code != "FILE_TOO_LARGE" {
			t.Fatalf("copy: %+v, %v; want the item FILE_TOO_LARGE", res, err)
		}
		entries, _ := os.ReadDir(f.root)
		for _, e := range entries {
			if e.Name() == "g2" || strings.Contains(e.Name(), ".droidyard-") {
				t.Errorf("left behind: %s", e.Name())
			}
		}
	})
}

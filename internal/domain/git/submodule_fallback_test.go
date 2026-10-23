package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/git/gittest"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0125 §4): a failure inside a submodule does not stop the watch. On a trusted
// root `none` makes git fail the whole status when its child in a submodule fails (here lib's
// index is garbage: exit 128, no output), so the status is asked again with `dirty` and
// marked fellBack. The project's own records are still there (lib's new commit), and the
// watch goes on and publishes the next change.
func TestTrustedChildFailureFallsBack(t *testing.T) {
	t.Parallel()
	f := newWatchFx(t, 20*time.Millisecond)
	s := gittest.Submodules(t, filepath.Join(t.TempDir(), "fx"))
	f.root = platform.Current().Canonical(s.Super)
	lib := filepath.Join(s.Super, "lib")
	f.g(lib, "commit", "-q", "--allow-empty", "-m", "x")
	if err := os.WriteFile(filepath.Join(s.Super, ".git", "modules", "lib", "index"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The control: the `none` status fails as a whole, so the fallback is what answers.
	f.r.timeout = testtime.Bound(60 * time.Second)
	if res, err := f.r.run(context.Background(), f.root, true, nil, statusArgsIgnoring("none")...); err != nil || res.ExitCode == 0 {
		t.Fatalf("none: exit %d, err %v; want git to fail on lib's index", res.ExitCode, err)
	}
	st, err := f.r.status(context.Background(), f.root, true)
	if err != nil {
		t.Fatalf("status: %v; want a status through the fallback", err)
	}
	if got, ok := entryOf(st, "lib"); !st.fellBack || !ok || got.Mark != MarkModified || got.Submodule == nil || !got.Submodule.Commits {
		t.Errorf("fellBack %v, lib = %+v (present %v); want the fallback with lib's new commit", st.fellBack, got, ok)
	}

	// The watch: the baseline has the record, and the tick goes on after a change.
	res, sub := f.start()
	if got, ok := entryOf(Status{Entries: res.Entries}, "lib"); !ok || got.Submodule == nil || !got.Submodule.Commits {
		t.Errorf("baseline entries %+v; want lib with new commits", res.Entries)
	}
	f.write(f.root, "new.txt", "new\n")
	ev := statusEvent(t, next(t, sub))
	if _, ok := entryOf(ev, "new.txt"); !ok {
		t.Errorf("the next event %v; want new.txt", describeEntries(ev))
	}
	if f.ws.Live() != 1 {
		t.Errorf("%d pollers live; want the watch still running", f.ws.Live())
	}
}

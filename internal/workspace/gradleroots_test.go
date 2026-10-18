package workspace

import (
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// rootsOf is the Gradle roots a workspace has recorded, read as the registry reads them.
func rootsOf(r *Registry, id string) []string {
	w := r.lookup(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for g := range w.gradleRoots {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// TestDegradeByGradleRoot is ADR-0075 §8, ADR-0092 §3: a sidecar crash is reported with a
// Gradle root, and it degrades the open workspaces whose current root it is, not the
// workspace whose project root happens to be the same path, and not one that is closed.
// Two workspaces on the same root both degrade (Q1 §8, accepted), a workspace that
// resolved nothing is never degraded by a crash, and one that moved off a root is not
// degraded by it (the growing-set case of a workspace that used both roots became this).
func TestDegradeByGradleRoot(t *testing.T) {
	h := newWatchHarness(t, nil)
	a, b, shared, closed, none := project(t), project(t), project(t), project(t), project(t)
	crash := capability.Errorf(capability.ErrBuildSidecarCrashed, "the sidecar died")
	const g1, g2 = "/gradle/one", "/gradle/two"

	ids := map[string]string{}
	for _, root := range []string{a, b, shared, closed, none} {
		ids[root] = h.open(t, root).ID
	}
	h.ws.UseGradleRoot(ids[a], g1)
	h.ws.UseGradleRoot(ids[b], g2)
	h.ws.UseGradleRoot(ids[shared], g2)
	h.ws.UseGradleRoot(ids[shared], g1) // moved off g2: a crash of g2 no longer concerns it
	h.ws.UseGradleRoot(ids[closed], g1)
	h.closeWS(t, closed)

	// A crash reported with the project root of a workspace that resolved nothing: the root
	// is not a recorded Gradle root, so nothing degrades.
	h.ws.Degrade(none, crash)
	if got := h.ws.State(none); got != Ready {
		t.Fatalf("a crash reported with the project root of a workspace that resolved no Gradle root left it %q; want ready", got)
	}

	h.ws.Degrade(g1, crash)
	want := map[string]State{a: Degraded, b: Ready, shared: Degraded, closed: Closed, none: Ready}
	for root, w := range want {
		if got := h.ws.State(root); got != w {
			t.Errorf("after a crash of %s workspace %s is %q; want %q", g1, root, got, w)
		}
	}
	info, err := h.ws.Describe(ids[a])
	if err != nil || info.DegradedBy == nil || info.DegradedBy.Code != capability.ErrBuildSidecarCrashed {
		t.Errorf("the degraded workspace is %+v (%v); want degradedBy BUILD_SIDECAR_CRASHED", info, err)
	}
}

// TestNoteRacingClose is the lifetime of the recorded root (ADR-0075 §8) under -race: an
// operation that outlives a close's drain keeps recording roots while the workspace closes,
// and none of what it notes in closing or closed survives to the next open; and an Open of
// an open workspace, which every new tab and MCP session calls, leaves the set alone.
func TestNoteRacingClose(t *testing.T) {
	h := newWatchHarness(t, nil)
	root := project(t)
	id := h.open(t, root).ID

	var (
		stop, done = make(chan struct{}), make(chan struct{})
		noted      atomic.Int64
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.ws.UseGradleRoot(id, "/gradle/stale")
			noted.Add(1)
		}
	}()
	waitNotes := func(n int64) {
		t.Helper()
		target := noted.Load() + n
		for deadline := time.Now().Add(10 * time.Second); noted.Load() < target; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the noting goroutine did not make %d more notes", n)
			}
		}
	}
	waitNotes(100)
	h.closeWS(t, root)
	// The noter is still running: its notes now arrive at a closed workspace.
	waitNotes(1000)
	close(stop)
	<-done
	if got := rootsOf(h.ws, id); len(got) != 0 {
		t.Fatalf("a closed workspace holds %v, noted while it was closing or closed; want none", got)
	}
	h.open(t, root)
	if got := rootsOf(h.ws, id); len(got) != 0 {
		t.Fatalf("the reopened workspace holds %v from before; want none", got)
	}

	// An open of an open workspace is a no-op for the set.
	h.ws.UseGradleRoot(id, "/gradle/kept")
	h.open(t, root)
	if got := rootsOf(h.ws, id); !reflect.DeepEqual(got, []string{"/gradle/kept"}) {
		t.Fatalf("a second open left the recorded roots %v; want [/gradle/kept]", got)
	}
}

// TestCloseReleasesTheRootsNoOtherWorkspaceUses is ADR-0075 §8 at the registry: a close
// hands CloseRoot the workspace's Gradle root when no other open workspace has it, and
// the call runs outside every lock, so a callback that takes the registry's locks (as a
// crashing build's path does through Degrade, and a describe does) does not deadlock.
// Two workspaces on one root, closing one after the other, release it once, at the
// second. (With one current root per workspace, the old case of one workspace holding an
// own root and a shared one became a third workspace on the own root.)
func TestCloseReleasesTheRootsNoOtherWorkspaceUses(t *testing.T) {
	h := newWatchHarness(t, nil)
	a, b, c := project(t), project(t), project(t)
	var mu sync.Mutex
	var released []string
	called := make(chan struct{}, 8)
	h.ws.opts.CloseRoot = func(g string) {
		// What a Gradle client's Close can be waiting behind: the admission lock (Describe,
		// Admit) and the publication lock (Degrade).
		h.ws.Degrade(g, capability.Errorf(capability.ErrBuildSidecarCrashed, "x"))
		_, _ = h.ws.Describe("none")
		mu.Lock()
		released = append(released, g)
		mu.Unlock()
		called <- struct{}{}
	}
	idA, idB, idC := h.open(t, a).ID, h.open(t, b).ID, h.open(t, c).ID
	h.ws.UseGradleRoot(idA, "/gradle/own")
	h.ws.UseGradleRoot(idB, "/gradle/shared")
	h.ws.UseGradleRoot(idC, "/gradle/shared")

	closed := make(chan struct{})
	go func() { h.closeWS(t, a); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("close did not return: CloseRoot ran under a registry lock")
	}
	mu.Lock()
	got := append([]string(nil), released...)
	mu.Unlock()
	if want := []string{"/gradle/own"}; !reflect.DeepEqual(got, want) {
		t.Errorf("closing A released %v; want %v (the shared root belongs to B and C, still open)", got, want)
	}

	h.closeWS(t, b)
	mu.Lock()
	got = append([]string(nil), released...)
	mu.Unlock()
	if want := []string{"/gradle/own"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after closing B the released roots are %v; want %v (C still has the shared root)", got, want)
	}

	h.closeWS(t, c)
	mu.Lock()
	got = append([]string(nil), released...)
	mu.Unlock()
	if want := []string{"/gradle/own", "/gradle/shared"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after closing C the released roots are %v; want %v", got, want)
	}
}

// TestCloseKeepsARootResolvedDuringItsDrain: which roots a close releases is decided
// after its drain, so a root another workspace resolves while the close drains keeps its
// connection; and the closing workspace itself refuses to record a root, so a call that
// passed its trust check just before the close is refused rather than leaving a
// connection recorded nowhere (ADR-0075 §8). Seen failing with the release decided as the
// close begins: closing A released [/gradle/shared].
func TestCloseKeepsARootResolvedDuringItsDrain(t *testing.T) {
	h := newHarness(t, 300*time.Millisecond)
	var mu sync.Mutex
	var released []string
	h.ws.opts.CloseRoot = func(g string) {
		mu.Lock()
		released = append(released, g)
		mu.Unlock()
	}
	a, b := project(t), project(t)
	idA, idB := h.open(t, a).ID, h.open(t, b).ID
	if _, err := h.ws.UseGradleRoot(idA, "/gradle/shared"); err != nil {
		t.Fatal(err)
	}
	h.start(t, "fixture.ignores", a)
	done := h.run(CapClose, a)
	waitState(t, h.ws, a, Closing)

	if _, err := h.ws.UseGradleRoot(idA, "/gradle/late"); err == nil {
		t.Error("a closing workspace recorded a Gradle root; want its call refused")
	}
	if _, err := h.ws.UseGradleRoot(idB, "/gradle/shared"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the close did not end")
	}
	mu.Lock()
	got := append([]string(nil), released...)
	mu.Unlock()
	if len(got) != 0 {
		t.Errorf("closing A released %v; want none (B resolved the shared root during A's drain)", got)
	}
	close(h.release)
}

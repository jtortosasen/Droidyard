package device

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/testtime"
)

// Decision (ADR-0123 §3): a live view's end drops its source reference before it returns, and
// only the wait for the source's teardown is on a goroutine. So the answer to
// events.unsubscribe, which follows the end, is ordered before any later acquire, which
// finds the source ending, waits for it and starts a new one. The goroutine's wait is held
// here by the test seam, so the only thing that can have dropped the reference is the end
// itself. Seen failing with the reference dropped on the goroutine after the hold (today's
// release): the second watch joined the old source, one server start and the same source.
func TestAViewsEndDropsItsSourceAtOnce(t *testing.T) {
	w := newWSFix(t)
	first, _ := w.watch(t)
	w.waitServers(t, 1)
	w.adb.screenViews.mu.Lock()
	oldSrc := w.adb.screenViews.byID[first.StreamID].src
	w.adb.screenViews.mu.Unlock()

	hold := make(chan struct{})
	var once sync.Once
	SetScreenReleaseHold(func() { <-hold })
	t.Cleanup(func() {
		once.Do(func() { close(hold) })
		SetScreenReleaseHold(nil)
	})

	// The subscription's end is the only viewer's end: it calls end on this goroutine.
	w.sess.Unsubscribe(first.SubscriptionID)

	second, _ := w.watch(t)
	w.adb.screenViews.mu.Lock()
	newSrc := w.adb.screenViews.byID[second.StreamID].src
	w.adb.screenViews.mu.Unlock()
	if newSrc == oldSrc {
		t.Fatal("the new view joined the source of the view that had ended")
	}
	testtime.Eventually(t, testtime.Bound(10*time.Second), "a second server start", func() bool {
		return len(w.callsWith(t, "app_process")) >= 2
	})
	once.Do(func() { close(hold) })
}

// Decision (ADR-0123 §3): a second, concurrent end of a stream returns only once the first
// has dropped the source reference, so whoever called it (the socket's close, the lease's
// end) may rely on the drop as the first caller does. The drop is held open here. Seen failing
// with the second end returning at once on the stream being ended (today's early return):
// it returned while the drop had not completed.
func TestConcurrentEndsReturnAfterTheDrop(t *testing.T) {
	f := newStreamFixture(t)
	st := f.screen(t, "a1", "peer-anna", leaseDevice)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var completed atomic.Bool
	st.ref = &sourceRef{drop: func() func() {
		close(entered)
		<-proceed
		completed.Store(true)
		return func() {}
	}}

	firstDone := make(chan struct{})
	go func() { defer close(firstDone); st.end(StopDetached, "") }()
	<-entered

	secondDone := make(chan struct{})
	secondSawDrop := make(chan bool, 1)
	go func() {
		defer close(secondDone)
		st.end(StopDetached, "")
		secondSawDrop <- completed.Load()
	}()
	select {
	case <-secondDone:
		t.Fatal("the second end returned while the first had not dropped the reference")
	case <-time.After(200 * time.Millisecond):
	}
	close(proceed)
	<-firstDone
	<-secondDone
	if !<-secondSawDrop {
		t.Error("the second end returned before the drop completed")
	}
}

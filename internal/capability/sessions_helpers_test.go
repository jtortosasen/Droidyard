package capability

import (
	"sync"
	"time"
)

// What the occupation tests share (ADR-0094 §1 to §4): a registry on a clock a test moves.

// occupationClock is a clock a test moves.
type occupationClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *occupationClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *occupationClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// occupied is a registry whose configuration is conflict and a two-minute idle threshold,
// on a clock the test moves.
func occupied(conflict string) (*Sessions, *sessionRecorder, *occupationClock) {
	s, rec := newWatchedSessions()
	clk := &occupationClock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	s.UseClock(clk.now)
	s.UseOccupation(func() (string, time.Duration) { return conflict, 2 * time.Minute })
	return s, rec, clk
}

// connectAt connects id (of chat) named name.
func connectAt(s *Sessions, id, chat, name string) {
	s.Connect(id, chat)
	s.Named(id, name)
}

func lastState(rec *sessionRecorder, id string) SessionState {
	var last SessionState
	for _, e := range rec.events() {
		if e.ID == id {
			last = e
		}
	}
	return last
}

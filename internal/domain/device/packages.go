package device

import (
	"strconv"
	"strings"
	"time"
)

// The package a streamed line belongs to (ADR-0051). A line carries logcat's uid; its
// application id is uid % 100000, and an application's lines are the ones in
// 10000-19999. `pm list packages -U` maps an application id to the package that owns it,
// read by the stream when a line shows an application id it has not resolved, one read at a
// time and at most one a second, and kept for the stream's life. A line waits for the read
// its application id started, so the first lines of a fresh install are named and lines
// keep their order.

const (
	// uidUsers is how many uids one Android user spans: uid = user*uidUsers + appId.
	uidUsers = 100000
	// appIDFirst and appIDLast bound the application range; below it are system ids,
	// above it isolated and sandbox processes.
	appIDFirst = 10000
	appIDLast  = 19999

	// packagesRefreshGap is the least time between two reads a stream starts.
	packagesRefreshGap = time.Second
	// packagesListCap bounds `pm list packages -U`'s output; a longer one is a failed
	// read, as a partial table would label an application wrongly.
	packagesListCap = 1 << 20
	// packageNameCap is the longest package name taken; Android allows 255 bytes.
	packageNameCap = 255
)

// appIDOf is the application id of a uid, and false when the uid is not an application's:
// a system id, a named uid (-1) or a device without logcat's uid column.
func appIDOf(uid int) (int, bool) {
	if uid < 0 {
		return 0, false
	}
	id := uid % uidUsers
	return id, id >= appIDFirst && id <= appIDLast
}

// packageTable is a stream's application id to package map and the state of its reads.
// Every field is guarded by logStream.mu.
type packageTable struct {
	// known holds a resolved application id; "" is none: shared by several packages, or
	// not listed by a read that completed.
	known map[int]string
	// unknown holds the application ids seen and not resolved yet.
	unknown map[int]struct{}
	// running says a read is in flight; started is when the last one began.
	running bool
	started time.Time
	// closed is set once the stream stops reading, so no read starts after the join.
	closed bool
	// releasedTo is the newest line (by seq) that no longer waits: a failed read, a full
	// buffer or the stream's end lets the lines up to it go without a package.
	releasedTo uint64
}

// parsePackages reads `pm list packages -U`'s output into application id -> package. An
// application id with exactly one package gets it; one with several (a shared uid) gets
// "". A line that does not match, a uid outside the application range and a name over
// packageNameCap are skipped.
func parsePackages(out []byte) map[int]string {
	names := map[int]map[string]struct{}{}
	for _, l := range rawLines(out) {
		m := pmLineRE.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || len(m[1]) > packageNameCap {
			continue
		}
		uid, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		id, ok := appIDOf(uid)
		if !ok {
			continue
		}
		if names[id] == nil {
			names[id] = map[string]struct{}{}
		}
		names[id][m[1]] = struct{}{}
	}
	table := make(map[int]string, len(names))
	for id, set := range names {
		table[id] = ""
		if len(set) == 1 {
			for name := range set {
				table[id] = name
			}
		}
	}
	return table
}

// trackApp notes that a line of application id was pushed. An application id not yet
// resolved starts a read when one is due; one already resolved asks for nothing.
func (s *logStream) trackApp(id int) {
	s.mu.Lock()
	if _, ok := s.pk.known[id]; ok {
		s.mu.Unlock()
		return
	}
	if s.pk.unknown == nil {
		s.pk.unknown = map[int]struct{}{}
	}
	s.pk.unknown[id] = struct{}{}
	s.mu.Unlock()
	s.maybeRefresh()
}

// maybeRefresh starts a read when an application id waits for one, none is running, the
// gap since the last start has passed and the stream still reads. The batcher calls it on
// every tick as well, so a read deferred by the gap starts without a later line to ask.
func (s *logStream) maybeRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	pk := &s.pk
	if pk.closed || pk.running || len(pk.unknown) == 0 || s.ctx.Err() != nil {
		return
	}
	if !pk.started.IsZero() && time.Since(pk.started) < packagesRefreshGap {
		return
	}
	asked := make([]int, 0, len(pk.unknown))
	for id := range pk.unknown {
		asked = append(asked, id)
	}
	pk.running, pk.started = true, time.Now()
	s.refreshes.Add(1)
	go s.refresh(asked)
}

// refresh is one read of the device's packages, run on the stream's refresh context. A
// completed read resolves every application id it lists, and enters as none the ones that
// were asked for (seen before the read began) and it does not list; an id first seen while
// it ran stays unresolved for the next read. A failed read enters nothing and lets the
// lines held so far go without a package; a cancelled one (the stream's end) changes
// nothing.
func (s *logStream) refresh(asked []int) {
	defer s.refreshes.Done()
	out, err := s.adb.Run(s.rctx, s.adbPath, logsPMTimeout, packagesListCap,
		"-s", s.device, "shell", "pm", "list", "packages", "-U")
	failed := err != nil || out.ExitCode != 0 || out.Truncated
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pk.running = false
	if s.rctx.Err() != nil {
		return
	}
	if failed {
		s.pk.releasedTo = s.seq
		return
	}
	if s.pk.known == nil {
		s.pk.known = map[int]string{}
	}
	for id, name := range parsePackages(out.Stdout) {
		s.pk.known[id] = name
		delete(s.pk.unknown, id)
	}
	for _, id := range asked {
		if _, ok := s.pk.known[id]; !ok {
			s.pk.known[id] = ""
		}
		delete(s.pk.unknown, id)
	}
}

// stopRefresh ends the reads: none starts afterwards, a running one is cancelled, and it
// returns once the `pm` child is reaped. run calls it before the stream leaves the set.
func (s *logStream) stopRefresh() {
	s.mu.Lock()
	s.pk.closed = true
	s.mu.Unlock()
	s.rcancel()
	s.refreshes.Wait()
}

// releaseHeldLocked lets every queued line go without waiting for a read. mu is held.
func (s *logStream) releaseHeldLocked() {
	s.pk.releasedTo = s.seq
}

// waitsLocked says whether a queued line waits for a read: its application id is not
// resolved, a read is still to come for it, and no failure, full buffer or end has
// released it. mu is held.
func (s *logStream) waitsLocked(q *queuedLine) bool {
	if q.appID == 0 || q.seq <= s.pk.releasedTo {
		return false
	}
	_, known := s.pk.known[q.appID]
	return !known
}

// packageLocked is the package a queued line carries: "" for none. mu is held.
func (s *logStream) packageLocked(q *queuedLine) string {
	if q.appID == 0 {
		return ""
	}
	return s.pk.known[q.appID]
}

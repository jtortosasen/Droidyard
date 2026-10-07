package device

import (
	"sync"
	"time"
)

// The app record (plan m1i D11): per (serial, applicationId), in memory, the latest
// launch and a pid history, so that device.logs can attribute an app's lines — and its
// crash — to it after the process died (adb-device.md §Filtering by app survives the
// app's death). It is fed by device.launch, by `pidof` at each device.logs call, by
// ActivityManager's `Start proc <pid>:<applicationId>/…` lines, and by crash blocks.
// The record is a cache, never the answer: it is lost with the daemon and rebuilt from
// what the device says.

const (
	// appPIDsMax is how many pids one app's history keeps, newest kept (plan m1i D11).
	appPIDsMax = 16
	// appRecordsMax is how many (serial, applicationId) records are kept; the least
	// recently used is dropped past it (plan m1i D11).
	appRecordsMax = 256
)

// appPID is one pid of an app, with when it was first and last seen.
type appPID struct {
	pid   int
	first time.Time
	last  time.Time
}

// appRecord is one app's memory: the latest launch and the pid history, oldest first.
type appRecord struct {
	launchedAt time.Time // the zero time when no launch is recorded
	launched   string    // the launch's launchedAt, RFC 3339 UTC with milliseconds
	pids       []appPID
	used       time.Time
}

// appView is a copy of an appRecord, taken under the registry's lock so a caller reads
// a snapshot that cannot change under it.
type appView struct {
	launchedAt time.Time
	launched   string
	pids       []appPID
}

// appRegistry is the daemon's app records, keyed by serial and applicationId.
type appRegistry struct {
	mu      sync.Mutex
	records map[string]*appRecord
	order   []string // keys, least recently used first
}

func newAppRegistry() *appRegistry {
	return &appRegistry{records: map[string]*appRecord{}}
}

// appKey is the record's key: the serial and the applicationId, which never contain the
// NUL byte.
func appKey(serial, app string) string { return serial + "\x00" + app }

// touch moves key to the end of the LRU order.
func (r *appRegistry) touch(key string) {
	for i, k := range r.order {
		if k == key {
			r.order = append(append(r.order[:i], r.order[i+1:]...), key)
			return
		}
	}
	r.order = append(r.order, key)
}

// record returns the key's record, creating it and dropping the least recently used
// ones past appRecordsMax. The caller holds r.mu.
func (r *appRegistry) record(key string) *appRecord {
	if rec := r.records[key]; rec != nil {
		rec.used = time.Now()
		r.touch(key)
		return rec
	}
	rec := &appRecord{used: time.Now()}
	r.records[key] = rec
	r.touch(key)
	for len(r.order) > appRecordsMax {
		old := r.order[0]
		r.order = r.order[1:]
		delete(r.records, old)
	}
	return rec
}

// noteLaunch records the app's latest launch and, when it has one, its pid.
func (r *appRegistry) noteLaunch(serial, app string, at time.Time, launched string, pid *int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.record(appKey(serial, app))
	if at.After(rec.launchedAt) {
		rec.launchedAt = at
		rec.launched = launched
	}
	if pid != nil && *pid > 0 {
		rec.notePID(*pid, at)
	}
}

// notePID adds pid to the app's history, or moves an already known one to now.
func (r *appRegistry) notePID(serial, app string, pid int, at time.Time) {
	if pid <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(appKey(serial, app)).notePID(pid, at)
}

// notePID is the locked body of notePID.
func (rec *appRecord) notePID(pid int, at time.Time) {
	for i := range rec.pids {
		if rec.pids[i].pid == pid {
			rec.pids[i].last = at
			return
		}
	}
	rec.pids = append(rec.pids, appPID{pid: pid, first: at, last: at})
	if len(rec.pids) > appPIDsMax {
		rec.pids = rec.pids[len(rec.pids)-appPIDsMax:]
	}
}

// view returns a copy of the app's record; ok is false when nothing is known.
func (r *appRegistry) view(serial, app string) (appView, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.records[appKey(serial, app)]
	if rec == nil {
		return appView{}, false
	}
	rec.used = time.Now()
	r.touch(appKey(serial, app))
	return appView{launchedAt: rec.launchedAt, launched: rec.launched, pids: append([]appPID(nil), rec.pids...)}, true
}

// pidsSince returns the app's pids whose last sighting is at or after since, oldest
// first. A zero since is every pid. The window of a snapshot ends at the call, so a pid
// last seen before the window's start cannot have lines inside it (plan m1i D11).
func (v appView) pidsSince(since time.Time) []int {
	var out []int
	for _, p := range v.pids {
		if !since.IsZero() && p.last.Before(since) {
			continue
		}
		out = append(out, p.pid)
	}
	return out
}

// appStore returns the ADB's app records, creating them on first use.
func (a *ADB) appStore() *appRegistry {
	a.appMu.Lock()
	defer a.appMu.Unlock()
	if a.apps == nil {
		a.apps = newAppRegistry()
	}
	return a.apps
}

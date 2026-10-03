// Package process supervises the daemon's child processes and keeps their pid
// records, so a daemon that died without stopping them can find and stop them on its
// next start (docs/architecture/process-lifecycle.md; ADR-0021 §Pid records).
package process

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
	"golang.org/x/sys/unix"
)

// RecordsFile is the pid records' file under the state root (ADR-0021 §Layout).
const RecordsFile = "processes.json"

// Kind is what a recorded process is (process-lifecycle.md §Kinds of processes).
type Kind string

const (
	SharedSidecar    Kind = "shared-sidecar"
	WorkspaceSidecar Kind = "workspace-sidecar"
)

// Record is what the daemon writes when it spawns a supervised process and removes
// when it reaps it (ADR-0021 §Pid records). StartTime is platform.StartTime's value,
// compared for equality only.
type Record struct {
	ID        string   `json:"id"`
	Kind      Kind     `json:"kind"`
	Workspace string   `json:"workspace,omitempty"`
	PID       int      `json:"pid"`
	PGID      int      `json:"pgid"`
	StartTime int64    `json:"startTime"`
	Command   []string `json:"command"`
}

// Records is processes.json: {schemaVersion, processes: [...]} (plan m1c, R8).
type Records struct {
	Processes []Record `json:"processes"`
}

// Reaping bounds: one wait for every orphan group together after SIGTERM, then one
// for the groups to vanish after SIGKILL (plan m1c, R11). One wait rather than one per
// record keeps a start inside the 10 s a shim retries after auto-starting a daemon
// (ADR-0019 §Processes 4).
const (
	TermGrace = 5 * time.Second
	KillGrace = 1 * time.Second
)

const pollEvery = 20 * time.Millisecond

// Reconcile stops what a previous daemon left running, and empties processes.json.
// The daemon calls it on start, holding the lock and before it binds its socket or
// spawns anything (ADR-0021 §Pid records). A record whose pid is alive with the
// recorded start time is our orphan: its group gets SIGTERM, all orphans first; after
// one wait of at most TermGrace every group still alive gets SIGKILL, and is polled
// until it is gone, for at most KillGrace. Any other record is dropped, and a process
// that is not ours is never signalled. Each action is logged.
//
// A newer processes.json is left untouched and recorded by the store (plan m1c, R10);
// a corrupt one is moved aside by the store. Neither stops the daemon: Reconcile
// returns an error only when the file could not be read or written.
func Reconcile(st *store.Store, log io.Writer) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(log, "droidyard: reconcile: "+format+"\n", args...)
	}
	var recs Records
	switch out, err := st.Read(RecordsFile, store.Version, &recs); {
	case errors.Is(err, store.ErrNewer):
		logf("%s has a newer schemaVersion; left untouched, nothing reaped", RecordsFile)
		return nil
	case err != nil:
		return fmt.Errorf("reading %s: %w", RecordsFile, err)
	case out == store.Corrupt:
		logf("%s did not parse; moved aside, nothing reaped", RecordsFile)
		return nil
	case out == store.Absent:
		return nil
	}

	var orphans []Record
	for _, r := range recs.Processes {
		name := describe(r)
		if r.PID <= 0 || r.PGID <= 1 {
			// kill(-pgid) with pgid 0 or 1 would reach this daemon's group or every
			// process of the user.
			logf("%s: dropped, not a valid pid and process group", name)
			continue
		}
		started, err := platform.StartTime(r.PID)
		switch {
		case errors.Is(err, platform.ErrNoProcess):
			logf("%s: dropped, no such process", name)
		case err != nil:
			logf("%s: dropped, start time unreadable: %v", name, err)
		case started != r.StartTime:
			logf("%s: dropped, the pid is another process (start time %d, recorded %d)", name, started, r.StartTime)
		default:
			orphans = append(orphans, r)
		}
	}

	for _, r := range orphans {
		err := unix.Kill(-r.PGID, unix.SIGTERM)
		logf("%s: our orphan; SIGTERM to its group%s", describe(r), failed(err))
	}
	alive := waitGone(orphans, TermGrace)
	for _, r := range alive {
		err := unix.Kill(-r.PGID, unix.SIGKILL)
		logf("%s: killed; its group outlived SIGTERM by %s, SIGKILL to its group%s", describe(r), TermGrace, failed(err))
	}
	stuck := waitGone(alive, KillGrace)
	for _, r := range orphans {
		if contains(stuck, r) {
			logf("%s: group %d still present %s after SIGKILL", describe(r), r.PGID, KillGrace)
		} else {
			logf("%s: reaped", describe(r))
		}
	}

	if err := st.Write(RecordsFile, Records{Processes: []Record{}}); err != nil {
		return fmt.Errorf("writing %s: %w", RecordsFile, err)
	}
	logf("%d records, %d orphans reaped, %d dropped", len(recs.Processes), len(orphans)-len(stuck), len(recs.Processes)-len(orphans))
	return nil
}

// waitGone polls the groups until none is present or bound has passed, and returns
// those still present.
func waitGone(groups []Record, bound time.Duration) []Record {
	deadline := time.Now().Add(bound)
	for {
		var present []Record
		for _, r := range groups {
			if groupPresent(r.PGID) {
				present = append(present, r)
			}
		}
		if len(present) == 0 || !time.Now().Before(deadline) {
			return present
		}
		groups = present
		time.Sleep(pollEvery)
	}
}

// groupPresent: kill(-pgid, 0) fails with ESRCH once no process is left in the group.
func groupPresent(pgid int) bool {
	return !errors.Is(unix.Kill(-pgid, 0), unix.ESRCH)
}

func contains(rs []Record, r Record) bool {
	for _, x := range rs {
		if x.ID == r.ID && x.PID == r.PID {
			return true
		}
	}
	return false
}

func describe(r Record) string {
	return fmt.Sprintf("record %q pid %d pgid %d", r.ID, r.PID, r.PGID)
}

func failed(err error) string {
	if err == nil {
		return ""
	}
	return " failed: " + err.Error()
}

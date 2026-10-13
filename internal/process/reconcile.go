// Package process supervises the daemon's child processes and keeps their pid
// records, so a daemon that died without stopping them can find and stop them on its
// next start (docs/architecture/process-lifecycle.md; ADR-0021 §Pid records).
package process

import (
	"context"
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
	// DeviceStream is a recorded process that streams a device's log for the life of one
	// call (plan m1i D9): recorded like a sidecar, reaped by Reconcile, never supervised.
	DeviceStream Kind = "device-stream"
	// ShellCommand is a recorded process running one shell.exec command (plan m1k D5):
	// recorded like a sidecar and reaped by Reconcile, but its own group is killed when
	// the call ends, so nothing it left behind outlives it.
	ShellCommand Kind = "shell-command"
	// GitWrite is a recorded process running one git write of the UI (plan m2n P2; ADR-0071):
	// treated as ShellCommand is. Its leader survives a killed daemon (no death signal), so
	// Reconcile finds the pid record and kills the whole group, which holds the ssh or the
	// uploadpack a fetch started; the group is killed when the record cannot be written and at
	// every end of the call. It is its own session leader (Setsid), so it has no terminal.
	GitWrite Kind = "git-write"
	// EmulatorLaunch is an emulator launcher recorded from its spawn until its serial is
	// resolved (plan m1p Q1; ADR-0021's 2026-10-08 line). Unlike every other kind, a
	// matching record is signalled only if the Judge says so.
	EmulatorLaunch Kind = "emulator-launch"
)

// RecordsVersion is processes.json's schemaVersion: 2 since a record can name an emulator
// launch (plan m1p Q1), so a daemon that reads only version 1 leaves the file alone
// instead of reaping a launch it does not understand (ADR-0021 §Format). A version 1 file
// reads as is.
const RecordsVersion = 2

// JudgeTimeout is the one deadline for all the adb work Reconcile does to judge its
// emulator-launch records (plan m1p Q1 ¶2).
const JudgeTimeout = 2 * time.Second

// Judge decides whether an emulator-launch record whose pid and start time match may be
// signalled. It returns true only on a positive answer; on any doubt it returns false
// and the reason, which Reconcile logs. process imports no domain, so serve builds it.
// ctx carries the one JudgeTimeout shared by every record.
type Judge func(ctx context.Context, r Record) (signal bool, reason string)

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
	// AVD and AdbPort belong to an emulator-launch record: the AVD it boots and the adb
	// server port its launcher registered with.
	AVD     string `json:"avd,omitempty"`
	AdbPort int    `json:"adbPort,omitempty"`
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
// A record of kind EmulatorLaunch whose pid and start time match is signalled only if
// judge returns true, all of them within one JudgeTimeout; otherwise (a nil judge, a doubt,
// the deadline) it is dropped and nothing is signalled, with the reason logged.
//
// A newer processes.json is left untouched and recorded by the store (plan m1c, R10);
// a corrupt one is moved aside by the store. Neither stops the daemon: Reconcile
// returns an error only when the file could not be read or written.
func Reconcile(st *store.Store, log io.Writer, judge Judge) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(log, "droidyard: reconcile: "+format+"\n", args...)
	}
	var recs Records
	switch out, err := st.Read(RecordsFile, RecordsVersion, &recs); {
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
	jctx, jcancel := context.WithTimeout(context.Background(), JudgeTimeout)
	defer jcancel()
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
		case r.Kind == EmulatorLaunch:
			if judge == nil {
				logf("%s: dropped, no judge to prove the launch is not a device; nothing signalled", name)
			} else if ok, reason := safeJudge(judge, jctx, r); !ok {
				logf("%s: dropped, %s; nothing signalled", name, reason)
			} else {
				logf("%s: proven not to be a device: %s", name, reason)
				orphans = append(orphans, r)
			}
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

	if err := st.WriteVersion(RecordsFile, RecordsVersion, Records{Processes: []Record{}}); err != nil {
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

// safeJudge runs judge and treats a panic as doubt, so a bug in the judgment can never
// become a signal, nor stop the daemon from starting.
func safeJudge(judge Judge, ctx context.Context, r Record) (ok bool, reason string) {
	defer func() {
		if p := recover(); p != nil {
			ok, reason = false, fmt.Sprintf("the judge panicked: %v", p)
		}
	}()
	return judge(ctx, r)
}

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/store"
)

// Decision (ADR-0113 §5: after a crash the lock holder finalises a recording a dead daemon
// left running, and it does so before anything is served): a record left `recording` in the
// state root is `ended daemonExit` on disk the moment daemon.Start returns, before the
// first call is made, and the first call, a listing over the real socket, shows it ended.
// The disk is read before any call because device.uiRecordings recovers lazily too; only
// daemon.Options.AfterLock, which daemonOptions wires, can have done it by then. Seen failing
// with AfterLock left out of daemonOptions (the record still read recording until the
// listing's own lazy recovery ran).
func TestRecoveryRunsBeforeServing(t *testing.T) {
	t.Parallel()
	const id = "20261021T100000Z-0a1b2c3d"
	var state string
	p := startInProcessEnv(t, func(_ *capability.Registry, env platform.Env) {
		state = env.Getenv("DROIDYARD_STATE_DIR")
		st, err := store.New(state)
		if err != nil {
			t.Fatal(err)
		}
		dead := device.UIRecord{ID: id, Device: "emulator-5554", MaxSeconds: 900, State: device.RecordingRunning,
			StartedAt: time.Now().Add(-time.Hour).UTC(), StartedBy: device.RecordingStarter{Client: "ui"}}
		if err := device.NewUIRecordings(st).Save(dead); err != nil {
			t.Fatal(err)
		}
	})
	st, err := store.New(state)
	if err != nil {
		t.Fatal(err)
	}
	rec, found, err := device.NewUIRecordings(st).Get(id)
	if err != nil || !found {
		t.Fatalf("Get %s: found=%v err=%v", id, found, err)
	}
	if rec.State != device.RecordingEnded || rec.EndReason != device.EndDaemonExit {
		t.Fatalf("the dead daemon's record is %q %q when Start returned, want ended daemonExit", rec.State, rec.EndReason)
	}
	env, code := p.call(t, "device.uiRecordings")
	if code != 0 || env.Error != nil {
		t.Fatalf("device.uiRecordings exited %d with %+v", code, env.Error)
	}
	var res device.UIRecordingsResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("result %s: %v", env.Result, err)
	}
	if len(res.Recordings) != 1 || res.Recordings[0].ID != id || res.Recordings[0].State != device.RecordingEnded || res.Recordings[0].EndReason != device.EndDaemonExit {
		t.Errorf("the first listing = %+v, want %s ended daemonExit", res.Recordings, id)
	}
}

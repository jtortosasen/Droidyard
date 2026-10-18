package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestAcquireOutlastsAHolderProbe: Holder's probe (a shared flock held for an
// instant, by `status` and `stop`) must not make a starting daemon conclude that
// another daemon holds the lock (ADR-0019 §5). Acquire keeps trying through
// probeGrace; a real holder still wins.
func TestAcquireOutlastsAHolderProbe(t *testing.T) {
	state := shortDir(t)
	probe, err := os.OpenFile(filepath.Join(state, LockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	fd := int(probe.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(20 * time.Millisecond)
		syscall.Flock(fd, syscall.LOCK_UN)
	}()
	defer func() { <-released }()

	lock, err := Acquire(state, LockInfo{PID: os.Getpid()})
	if err != nil {
		t.Fatalf("a shared lock held for 20 ms made Acquire fail: %v", err)
	}
	defer lock.Release()

	// A daemon's exclusive lock still wins: a second Acquire gives up after the grace.
	start := time.Now()
	if _, err := Acquire(state, LockInfo{PID: os.Getpid()}); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second Acquire returned %v; want ErrLocked", err)
	}
	if waited := time.Since(start); waited < probeGrace || waited > 10*probeGrace {
		t.Errorf("the loser gave up after %s; want about %s", waited, probeGrace)
	}
}

// TestSetHTTPNeverShowsAnEmptyLock: the lock is rewritten in place once the HTTP
// listener is bound, new record first and truncation after, so a process reading
// daemon.lock (`status`, `stop`, a shim) sees the old record or the new one and never
// an empty file (plan m1d, step 5; ADR-0019 §5, amended). A reader on its own
// goroutine reads the file in a loop across 1000 rewrites and checks that every read
// is non-empty and parses with "http" set. The promise it breaks if the rewrite
// truncates first is "never empty". Every rewrite is the same length, so it does not
// see a torn record; SetHTTP's length check is what rules that out.
func TestSetHTTPNeverShowsAnEmptyLock(t *testing.T) {
	state := shortDir(t)
	lock, err := Acquire(state, LockInfo{PID: os.Getpid(), StartTime: time.Now(), Version: "0.0.0", Socket: filepath.Join(state, "s.sock")})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.SetHTTP("127.0.0.1:10000"); err != nil { // null to a string: the one growth
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	var reads atomic.Int64
	var failure atomic.Value
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(state, LockFile))
			var info LockInfo
			switch {
			case err != nil:
				failure.CompareAndSwap(nil, fmt.Sprintf("read: %v", err))
			case len(data) == 0:
				failure.CompareAndSwap(nil, "an empty daemon.lock")
			case json.Unmarshal(data, &info) != nil || info.HTTP == nil:
				failure.CompareAndSwap(nil, fmt.Sprintf("a daemon.lock that does not parse, or lost its http: %q", data))
			}
			reads.Add(1)
		}
	}()
	// Every record after the first is as long as the one before: a port of fixed width.
	// The writer keeps rewriting until the reader has read enough, not for a fixed count:
	// with one CPU or on a loaded host a fixed number of rewrites can end before the
	// reader is scheduled, and the property is "never empty", not a rate. Gosched after
	// each rewrite lets the reader run on one CPU.
	const minRewrites, minReads = 1000, 200
	last := ""
	rewrites := 0
	deadline := time.Now().Add(30 * time.Second)
	for rewrites < minRewrites || reads.Load() < minReads {
		if time.Now().After(deadline) {
			close(stop)
			<-done
			t.Fatalf("the reader read the lock only %d times across %d rewrites in 30s: the test did not race", reads.Load(), rewrites)
		}
		last = fmt.Sprintf("127.0.0.1:%d", 10001+rewrites%10000)
		if err := lock.SetHTTP(last); err != nil {
			close(stop)
			<-done
			t.Fatal(err)
		}
		rewrites++
		runtime.Gosched()
	}
	close(stop)
	<-done
	if f := failure.Load(); f != nil {
		t.Fatalf("the reader saw %s after %d reads", f, reads.Load())
	}
	t.Logf("%d reads across %d rewrites", reads.Load(), rewrites)
	info, err := ReadLock(state)
	if err != nil || info.HTTP == nil || *info.HTTP != last || info.PID != os.Getpid() {
		t.Fatalf("after the rewrites the lock says (%+v, %v); want http %s and the pid kept", info, err, last)
	}
}

// TestSetHTTPRefusesAShorterRecord: the in-place rewrite is safe for a reader only
// because the new record is never shorter than the old (SetHTTP's contract). A
// shorter one would leave the old record's tail visible between the write and the
// truncate, so SetHTTP refuses it and leaves the file as it was.
func TestSetHTTPRefusesAShorterRecord(t *testing.T) {
	state := shortDir(t)
	lock, err := Acquire(state, LockInfo{PID: os.Getpid(), Version: "0.0.0", Socket: filepath.Join(state, "s.sock")})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.SetHTTP("127.0.0.1:10000"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(state, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.SetHTTP("1:1"); err == nil {
		t.Fatal("SetHTTP accepted a record shorter than the current one")
	}
	after, err := os.ReadFile(filepath.Join(state, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("a refused SetHTTP changed daemon.lock: %q -> %q", before, after)
	}
}

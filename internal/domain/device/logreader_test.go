package device

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// logBuffer is a mutex-guarded writer: the daemon's read runs on its own goroutine, so
// the log a call writes is read here under the lock.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// restartLogReason is the reason the first "UI reader restart" line carries, "" when the
// log holds none.
func restartLogReason(log string) string {
	const mark = "UI reader restart: "
	for _, l := range strings.Split(log, "\n") {
		if i := strings.Index(l, mark); i >= 0 {
			return strings.TrimSpace(l[i+len(mark):])
		}
	}
	return ""
}

// Decision (plan m1n step 2; the evidence A2 and Q2 ¶1 rest on): the daemon records a
// reader restart with the reason that drove it, so a level-5 run can tell a wedged reader
// from a gone one. The three failures give three distinct reasons: a warm read that times
// out ("timeout <ms>"), a connection that ends before a document ("eof"), and a forward
// that refuses the dial ("refused"). Seen failing with the reason dropped: the read still
// restarts (the tree comes back), but the log line is gone and the assertion says so.
func TestReaderRestartIsLoggedWithItsReason(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		broken func(t *testing.T, r *devicetest.FakeReader, f *uiFixture)
	}{
		{
			name:   "a stalled warm read times out",
			want:   "timeout ",
			broken: func(t *testing.T, r *devicetest.FakeReader, f *uiFixture) { r.SetDumpStall(1) },
		},
		{
			name: "a connection that ends before a document",
			want: "eof",
			broken: func(t *testing.T, r *devicetest.FakeReader, f *uiFixture) {
				r.SetDocs(devicetest.ReaderGone, pixelDoc(t))
			},
		},
		{
			name: "a forward that refuses the dial",
			want: "refused",
			broken: func(t *testing.T, r *devicetest.FakeReader, f *uiFixture) {
				// An adb server restart drops the host forward while the reader behind it
				// lives on: the cached port then refuses the dial (recovery_test.go).
				devicetest.ForgetForward(t, f.dir, localAbstract(readerNameTest))
			},
		},
	}
	reasons := map[string]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quickRecovery(t) // the stall is the 1 s warm read, not 4 s
			r := devicetest.StartReader(t, pixelDoc(t))
			t.Cleanup(r.Close)
			f := newUIFixture(t, map[string]string{
				uiSerial + ".push.exit":   "0",
				uiSerial + ".start.out":   "",
				uiSerial + ".forward.out": r.Port,
			})
			mustComplete(t, f.tree(t, uiSerial)) // prime: the reader is started and warm
			var log logBuffer
			f.adb.SetLog(&log)
			c.broken(t, r, f)
			mustComplete(t, f.tree(t, uiSerial)) // the read that restarts

			got := restartLogReason(log.String())
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("restart reason %q, want one starting %q; log:\n%s", got, c.want, log.String())
			}
			reasons[got] = true
		})
	}
	if len(reasons) != 3 {
		t.Errorf("the three failures gave %d distinct restart reasons, want 3: %v", len(reasons), reasons)
	}
}

// Decision (plan m1n step 2): a warm read that took a second or more is recorded with its
// duration, so a run can see a slow reader from the log alone. Seen failing with the 1 s
// threshold raised above the read (nothing is logged).
func TestSlowWarmReadIsLoggedWithItsDuration(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial)) // the priming read is fast, so it logs nothing
	var log logBuffer
	f.adb.SetLog(&log)
	r.SetDelay(1200 * time.Millisecond) // the next warm read, and only it, takes over a second
	mustComplete(t, f.tree(t, uiSerial))
	got := log.String()
	if !strings.Contains(got, "warm UI read took 1") {
		t.Errorf("the log does not record the slow warm read with its duration:\n%s", got)
	}
	if strings.Contains(got, "UI reader restart") {
		t.Errorf("a slow but successful warm read logged a restart:\n%s", got)
	}
}

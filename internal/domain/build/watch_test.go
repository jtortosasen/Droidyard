package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle"
	"github.com/jtortosasen/droidyard/internal/domain/build/gradle/gradletest"
	"github.com/jtortosasen/droidyard/internal/event"
)

// watchLines is batch i of the scripted build: four lines with multi-byte runes of
// uneven width, so a cap can land inside a line and inside a rune.
func watchLines(i int) []string {
	out := make([]string, 4)
	for j := range out {
		out[j] = fmt.Sprintf("batch %02d line %d: héllo wörld ✓ 日本語 %s", i, j, strings.Repeat("é", (i+j)%9))
	}
	return out
}

func watchStream(i int) string {
	if i%2 == 0 {
		return "stdout"
	}
	return "stderr"
}

// watchBytes is what batches [from, to) take in the console.
func watchBytes(from, to int) int64 {
	var n int64
	for i := from; i < to; i++ {
		for _, l := range watchLines(i) {
			n += int64(len(l)) + 1
		}
	}
	return n
}

// openWatch calls build.watchOutput on workspace as session s.
func (f *buildFixture) openWatch(t *testing.T, s *event.Session, workspace string) (WatchOutputResult, *event.Subscription) {
	t.Helper()
	out, err := f.ops.Invoke(context.Background(), capability.Request{
		Capability: "build.watchOutput", Workspace: workspace, Params: []byte(`{}`), Session: s,
	})
	if err != nil {
		t.Fatalf("build.watchOutput: %v", err)
	}
	if out.State != capability.StateCompleted {
		t.Fatalf("build.watchOutput ended %s: %+v", out.State, out.Error)
	}
	res, ok := out.Result.(WatchOutputResult)
	if !ok {
		t.Fatalf("build.watchOutput result is %T", out.Result)
	}
	sub, ok := s.Subscription(res.SubscriptionID)
	if !ok {
		t.Fatalf("the session does not hold %s", res.SubscriptionID)
	}
	return res, sub
}

// drain returns every event queued on sub now.
func drain(sub *event.Subscription) []event.Event {
	var evs []event.Event
	for {
		e, ok := sub.TryNext()
		if !ok {
			return evs
		}
		evs = append(evs, e)
	}
}

// waitOffset waits until the workspace's one running build has at least n bytes in its
// console.
func (f *buildFixture) waitOffset(t *testing.T, n int64) WatchedBuild {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if b := f.history.snapshot(testWS); len(b) == 1 && b[0].Offset >= n {
			return b[0]
		}
	}
	t.Fatalf("the running build's console never reached %d bytes: %+v", n, f.history.snapshot(testWS))
	return WatchedBuild{}
}

// reassemble rebuilds the console from what a watch was given: the first prefix bytes of
// the artifact, which build.watchOutput's result named, then the events. An event wholly
// below prefix repeats bytes the prefix has and must match them; the rest must continue
// exactly where the bytes so far end. It returns the rebuilt console, the duplicates seen
// and the events of the build.
func reassemble(t *testing.T, name, buildID, ref string, file []byte, prefix int64, evs []event.Event) ([]byte, int, []LinesEvent) {
	t.Helper()
	buf := append([]byte(nil), file[:prefix]...)
	dups := 0
	var seen []LinesEvent
	for _, e := range evs {
		if e.Topic == event.TopicGap {
			t.Fatalf("%s: an events.gap in a test that overflows nothing", name)
		}
		if e.Topic != TopicOutput || e.Type != EventLines {
			t.Fatalf("%s: event %s/%s", name, e.Topic, e.Type)
		}
		var le LinesEvent
		if err := json.Unmarshal(e.Payload, &le); err != nil {
			t.Fatal(err)
		}
		if le.BuildID != buildID || le.ConsoleRef != ref || e.Operation != buildID {
			t.Fatalf("%s: event for build %q ref %q operation %q, want %q %q", name, le.BuildID, le.ConsoleRef, e.Operation, buildID, ref)
		}
		seen = append(seen, le)
		chunk := []byte(strings.Join(le.Lines, "\n") + "\n")
		if le.Offset < prefix {
			if le.Offset+int64(len(chunk)) > prefix {
				t.Fatalf("%s: event at %d (%d bytes) straddles the snapshot offset %d", name, le.Offset, len(chunk), prefix)
			}
			if !bytes.Equal(file[le.Offset:le.Offset+int64(len(chunk))], chunk) {
				t.Fatalf("%s: the duplicate at %d differs from the console", name, le.Offset)
			}
			dups++
			continue
		}
		if le.Offset != int64(len(buf)) {
			t.Fatalf("%s: event at %d, but the console so far ends at %d", name, le.Offset, len(buf))
		}
		buf = append(buf, chunk...)
	}
	return buf, dups, seen
}

// Decision (ADR-0044 §1–§2): build.watchOutput opens its subscription before it
// snapshots the running builds, and the console's every write is published under the
// console's lock. So a watch opened before a build, in the middle of one (with a batch
// landing between its subscription and its snapshot) or after ten batches reassembles the
// console byte for byte from [0, offset) plus the events up to the operation's terminal
// state, with duplicates only below the offset; the connector notice and the cap marker
// are lines of the stream droidyard, the marker last and on a line of its own, and the
// cap cuts no line and no rune.
func TestWatchOutputReassemblesTheConsole(t *testing.T) {
	const batches = 50
	gates := t.TempDir()
	g1, g2 := filepath.Join(gates, "g1"), filepath.Join(gates, "g2")
	var steps []gradletest.Step
	add := func(from, to int) {
		for i := from; i < to; i++ {
			steps = append(steps, gradletest.Step{Kind: "output", Stream: watchStream(i), Lines: watchLines(i)})
		}
	}
	add(0, 10)
	steps = append(steps, gradletest.Step{Kind: "gate", Path: g1})
	add(10, 11)
	steps = append(steps, gradletest.Step{Kind: "gate", Path: g2})
	add(11, batches)
	f := newFixture(t, gradletest.Config{Source: "connector", Build: &gradletest.Build{Steps: steps}})
	noticeLen := int64(len(connectorNotice(gradle.Connection{Source: "connector", GradleVersion: "8.13"}))) + 1
	// The cap falls inside the last stretch of the script, a third of the way through it:
	// in the middle of a batch, with later (shorter) lines still free to fit.
	early := noticeLen + watchBytes(0, 11)
	f.history.capOverride = early + watchBytes(11, batches)*2/3

	// A watch opened before the build exists.
	sessA := event.NewSession(f.bus, "a")
	defer sessA.Close()
	resA, subA := f.openWatch(t, sessA, testWS)
	if len(resA.Builds) != 0 {
		t.Fatalf("a watch with no build running lists %+v", resA.Builds)
	}

	id := f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)

	// B: opened with ten batches in the console and the build held at the first gate.
	f.waitOffset(t, noticeLen+watchBytes(0, 10))
	sessB := event.NewSession(f.bus, "b")
	defer sessB.Close()
	resB, subB := f.openWatch(t, sessB, testWS)

	// C: a batch lands between its subscription and its snapshot. The hook lets the
	// build through its first gate and waits until that batch is in the console.
	hook := func() {
		if err := os.WriteFile(g1, nil, 0o600); err != nil {
			t.Error(err)
		}
		f.waitOffset(t, early)
	}
	f.history.watchHook.Store(&hook)
	sessC := event.NewSession(f.bus, "c")
	defer sessC.Close()
	resC, subC := f.openWatch(t, sessC, testWS)
	f.history.watchHook.Store(nil)

	if err := os.WriteFile(g2, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res := mustResult(t, f.wait(t, id))

	// The offsets each snapshot took.
	for name, r := range map[string]WatchOutputResult{"B": resB, "C": resC} {
		if len(r.Builds) != 1 || r.Builds[0].BuildID != id || r.Builds[0].ConsoleRef != res.ConsoleRef {
			t.Fatalf("watch %s lists %+v, want the one running build %s", name, r.Builds, id)
		}
	}
	if got, want := resB.Builds[0].Offset, noticeLen+watchBytes(0, 10); got != want {
		t.Errorf("B's offset is %d, want %d (the notice and ten batches)", got, want)
	}
	if got := resC.Builds[0].Offset; got != early {
		t.Errorf("C's offset is %d, want %d (the notice and eleven batches: the one that landed in the window)", got, early)
	}

	entry, err := f.arts.Get(res.ConsoleRef)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := f.history.newest(testWS)
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range []struct {
		name   string
		prefix int64
		sub    *event.Subscription
		dups   int // at least
	}{
		{"A", 0, subA, 0},
		{"B", resB.Builds[0].Offset, subB, 0},
		{"C", resC.Builds[0].Offset, subC, 1}, // the window's batch reaches C twice: below the offset and in the prefix
	} {
		got, dups, seen := reassemble(t, w.name, id, res.ConsoleRef, file, w.prefix, drain(w.sub))
		if !bytes.Equal(got, file) {
			t.Errorf("watch %s rebuilt %d bytes that differ from the %d-byte console", w.name, len(got), len(file))
		}
		if dups < w.dups {
			t.Errorf("watch %s saw %d duplicates below its offset, want at least %d: the window was not exercised", w.name, dups, w.dups)
		}
		if w.name == "A" {
			if seen[0].Stream != "droidyard" || seen[0].Offset != 0 {
				t.Errorf("A's first event is %+v, want the notice from offset 0", seen[0])
			}
			// The marker is the last event, on stream droidyard; a cap that cut in the
			// middle of a batch shows as a batch event with fewer than four lines.
			last := seen[len(seen)-1]
			if last.Stream != "droidyard" || len(last.Lines) != 1 || !strings.HasPrefix(last.Lines[0], "--- droidyard: console truncated at") {
				t.Errorf("A's last event is %+v, want the cap marker", last)
			}
			partial := false
			for _, le := range seen[1 : len(seen)-1] {
				if len(le.Lines) < 4 {
					partial = true
				}
			}
			if !partial {
				t.Errorf("no batch was cut by the cap: the test does not reach the mid-batch case")
			}
		}
	}

	// The console holds whole lines only: valid UTF-8, every line one the script wrote
	// or the marker, and the marker alone on the last line.
	if !utf8.Valid(file) || !bytes.HasSuffix(file, []byte("---\n")) {
		t.Fatalf("the console is not whole lines of valid UTF-8 ending in the marker")
	}
	want := map[string]bool{}
	for i := 0; i < batches; i++ {
		for _, l := range watchLines(i) {
			want[l] = true
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(file), "\n"), "\n")
	marker := lines[len(lines)-1]
	if !strings.HasPrefix(marker, "--- droidyard: console truncated at") {
		t.Errorf("the last line is %q, want the marker", marker)
	}
	for _, l := range lines[1 : len(lines)-1] {
		if !want[l] {
			t.Errorf("the console holds %q, which the build never wrote whole", l)
		}
	}
	if rec.ConsoleDropped == 0 || !strings.Contains(marker, fmt.Sprintf("%d bytes dropped", rec.ConsoleDropped)) {
		t.Errorf("the marker %q does not carry the record's %d dropped bytes", marker, rec.ConsoleDropped)
	}
	// Everything the cap kept is under the cap, and the marker alone is past it.
	if body := int64(len(file)) - int64(len(marker)) - 1; body > f.history.capOverride {
		t.Errorf("the lines take %d bytes, over the cap of %d", body, f.history.capOverride)
	}
}

// Decision (ADR-0044 §1, §3): a watch follows the builds of the workspace it was made
// for. A build of testWS is neither listed to nor published to a watch of another
// workspace, and is to a watch of its own; the build domain keeps its watches per
// workspace because the bus cannot filter by workspace.
func TestWatchOutputKeepsToItsWorkspace(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	f := newFixture(t, gradletest.Config{Build: &gradletest.Build{Steps: []gradletest.Step{
		{Kind: "output", Stream: "stdout", Lines: []string{"first"}},
		{Kind: "gate", Path: gate},
		{Kind: "output", Stream: "stdout", Lines: []string{"second"}},
	}}})
	id := f.start(t, "build.run", `{"tasks":[":app:assembleDebug"]}`)
	f.waitOffset(t, int64(len("first\n")))

	sessOther := event.NewSession(f.bus, "other")
	defer sessOther.Close()
	other, subOther := f.openWatch(t, sessOther, otherWS)
	if other.Builds == nil || len(other.Builds) != 0 {
		t.Errorf("a watch of another workspace lists %+v, want an empty list", other.Builds)
	}
	sessOwn := event.NewSession(f.bus, "own")
	defer sessOwn.Close()
	own, subOwn := f.openWatch(t, sessOwn, testWS)
	if len(own.Builds) != 1 || own.Builds[0].BuildID != id {
		t.Errorf("a watch of the build's workspace lists %+v, want build %s", own.Builds, id)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mustResult(t, f.wait(t, id))
	if evs := drain(subOther); len(evs) != 0 {
		t.Errorf("a watch of another workspace received %d events: %+v", len(evs), evs)
	}
	evs := drain(subOwn)
	if len(evs) != 1 {
		t.Fatalf("the build's own watch received %d events, want the one past its snapshot", len(evs))
	}
	var le LinesEvent
	if err := json.Unmarshal(evs[0].Payload, &le); err != nil {
		t.Fatal(err)
	}
	if le.Offset != int64(len("first\n")) || strings.Join(le.Lines, "|") != "second" || le.Stream != "stdout" {
		t.Errorf("the event is %+v, want second at offset 6 on stdout", le)
	}
}

// Decision (ADR-0044 §3): the registry keeps a watch only while its subscription lives.
// Ending the session, or events.unsubscribe, removes it, so a finished UI tab leaves
// nothing for a build to publish to.
func TestWatchOutputEndsWithItsSubscription(t *testing.T) {
	f := newFixture(t, gradletest.Config{Build: successBuild(gradle.TaskSummary{})})
	s1 := event.NewSession(f.bus, "one")
	res, _ := f.openWatch(t, s1, testWS)
	s2 := event.NewSession(f.bus, "two")
	f.openWatch(t, s2, testWS)
	if n := len(f.history.watchersOf(testWS)); n != 2 {
		t.Fatalf("%d watches, want 2", n)
	}
	s1.Unsubscribe(res.SubscriptionID)
	if n := len(f.history.watchersOf(testWS)); n != 1 {
		t.Errorf("%d watches after one unsubscribed, want 1", n)
	}
	s2.Close()
	if n := len(f.history.watchersOf(testWS)); n != 0 {
		t.Errorf("%d watches after the session ended, want none", n)
	}
	if n := f.bus.Subscriptions(); n != 0 {
		t.Errorf("the bus holds %d subscriptions", n)
	}
}

// Decision (ADR-0044 §2): the console's file write, the offset it took and the
// publication happen under one lock, and a snapshot's offset is read under the same one.
// So with writers on several goroutines and watches opening in the middle of the
// traffic, every watch still reassembles the console exactly: the events continue where
// its prefix ends, in order, with no hole and no repeat past the offset.
func TestLiveConsoleKeepsOffsetsAndEventsInStep(t *testing.T) {
	f := newFixture(t, gradletest.Config{})
	const buildID = "op-00000000000000a1"
	out, err := f.arts.CreateDurable(testWS, buildID, artifact.DurableOptions{MediaType: "text/plain", Cap: consoleCap})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	lc := f.history.newLiveConsole(testWS, buildID, out)
	defer lc.release()

	const writers, rounds = 4, 400
	done := make(chan struct{})
	for g := 0; g < writers; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for n := 0; n < rounds; n++ {
				lines := make([]string, 1+n%3)
				for l := range lines {
					lines[l] = fmt.Sprintf("w%d n%03d l%d ✓", g, n, l)
				}
				lc.append("stdout", lines)
			}
		}()
	}
	type opened struct {
		res WatchOutputResult
		sub *event.Subscription
	}
	var watches []opened
	for i := 0; i < 30; i++ {
		s := event.NewSession(f.bus, fmt.Sprintf("s%d", i))
		s.SetQueue(1 << 16) // the traffic is 1600 events; a gap here would mean a test bug
		defer s.Close()
		res, sub := f.openWatch(t, s, testWS)
		watches = append(watches, opened{res, sub})
		time.Sleep(time.Millisecond)
	}
	for g := 0; g < writers; g++ {
		<-done
	}
	lc.end()

	ref := out.Ref().Ref
	entry, err := f.arts.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(entry.File)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range watches {
		if len(w.res.Builds) != 1 || w.res.Builds[0].BuildID != buildID {
			t.Fatalf("watch %d lists %+v", i, w.res.Builds)
		}
		got, _, _ := reassemble(t, fmt.Sprintf("watch %d", i), buildID, ref, file, w.res.Builds[0].Offset, drain(w.sub))
		if !bytes.Equal(got, file) {
			t.Fatalf("watch %d rebuilt %d bytes that differ from the %d-byte console", i, len(got), len(file))
		}
	}
}

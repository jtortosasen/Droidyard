package device

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// readerForwardRE is the reader's forward as the daemon issues it (ADR-0102): a socket file
// named reader-<12 hex>, then the reader's abstract socket.
var readerForwardRE = regexp.MustCompile(`^-s ` + uiSerial + ` forward localfilesystem:(.+) localabstract:droidyard-reader-[0-9a-f]{12}$`)

// dirEntries are the names in dir, none when it does not exist.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	list, _ := os.ReadDir(dir)
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// Decision (ADR-0102 §1): the reader is forwarded to a socket file in the forward directory,
// private to this user, named reader-<12 hex> and not a loopback port; adb leaves the file
// when it removes the forward, so Close removes the forward and then the file. Seen failing
// with `forward tcp:0` (the fake answers it as an unknown call, so no forward is made) and
// with the unlink dropped (the file stays after Close).
func TestReaderForwardIsASocketInTheForwardDirAndGoneAtClose(t *testing.T) {
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	mustComplete(t, f.tree(t, uiSerial))

	dir := forwardDirFor(t, f.dir)
	var made []string
	for _, c := range f.calls(t) {
		if strings.Contains(c, " forward ") && !strings.Contains(c, "--list") {
			made = append(made, c)
		}
	}
	if len(made) != 1 {
		t.Fatalf("forward calls %q, want the one forward to the reader", made)
	}
	m := readerForwardRE.FindStringSubmatch(made[0])
	if m == nil {
		t.Fatalf("forward call %q, want localfilesystem:<dir>/reader-<12 hex> to the reader's socket", made[0])
	}
	sock := m[1]
	if filepath.Dir(sock) != dir || !forwardFileRE.MatchString(filepath.Base(sock)) || !strings.HasPrefix(filepath.Base(sock), "reader-") {
		t.Errorf("socket %q, want reader-<12 hex> directly in %q", sock, dir)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("forward directory %v (%v), want mode 0700", fi, err)
	}
	if got := dirEntries(t, dir); len(got) != 1 || got[0] != filepath.Base(sock) {
		t.Errorf("forward directory holds %q while forwarded, want the one socket", got)
	}

	f.adb.Close(context.Background())
	if n := count(f.calls(t), "-s "+uiSerial+" forward --remove localfilesystem:"+sock); n != 1 {
		t.Errorf("%d removals of the forward, want 1 (%q)", n, f.calls(t))
	}
	if got := devicetest.Forwards(t, f.dir); len(got) != 0 {
		t.Errorf("forwards %q after Close, want none", got)
	}
	if got := dirEntries(t, dir); len(got) != 0 {
		t.Errorf("forward directory holds %q after Close: adb leaves the file, so the daemon must remove it", got)
	}
}

// Decision (ADR-0102 §1): files an earlier daemon left in the forward directory are swept
// before the first forward of this one, except a reader's forward adb still lists (an earlier
// daemon's reader that answers is adopted, and its file must stay); a live view's forward
// still listed is removed with its file, since nothing adopts it; with nothing in the
// directory adb is not asked. Seen failing with the sweep removed (the leftover stays), with
// it removing the listed reader's file as well, and with the listed live view's forward left.
func TestLeftoverSocketsAreSweptAndListedForwardsKept(t *testing.T) {
	f := newUIFixture(t, nil)
	ctx := context.Background()

	// An empty directory: nothing to sweep, so adb is not run for it.
	if _, err := f.adb.forwardSocket(ctx, "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("adb ran %q for an empty forward directory", calls)
	}

	// A second daemon over a directory with a dead daemon's files in it.
	g := newUIFixture(t, nil)
	leftover := seedSock(t, g.dir, "reader-0000000000d1")
	listed := seedSock(t, g.dir, "reader-0000000000d3")
	screen := seedSock(t, g.dir, "screen-0000000000d2")
	for _, p := range []string{leftover, listed, screen} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	devicetest.AddForward(t, g.dir, uiSerial, listed, "", localAbstract(readerNameTest))
	devicetest.AddForward(t, g.dir, uiSerial, screen, "", "localabstract:scrcpy_0000d2")
	if _, err := g.adb.forwardSocket(ctx, "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}
	got := dirEntries(t, forwardDirFor(t, g.dir))
	if len(got) != 1 || got[0] != filepath.Base(listed) {
		t.Errorf("forward directory holds %q after the sweep, want only the listed reader's %q", got, filepath.Base(listed))
	}
	if fw := devicetest.Forwards(t, g.dir); len(fw) != 1 || !strings.Contains(fw[0], filepath.Base(listed)) {
		t.Errorf("forwards %q after the sweep, want only the reader's: the live view's must be removed", fw)
	}
}

// Decision (ADR-0102 §1): a sweep that could not read the forward listing removes nothing and
// is tried again at the next forward, not given up for the daemon's life. Seen failing with
// the sweep marked done after a failed listing (the leftover stays for good).
func TestSweepIsRetriedAfterAFailedListing(t *testing.T) {
	f := newUIFixture(t, nil)
	left := seedSock(t, f.dir, "reader-0000000000e1")
	if err := os.WriteFile(left, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	devicetest.Write(t, f.dir, "forwardlist.exit", []byte("1"))
	if _, err := f.adb.forwardSocket(context.Background(), "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}
	if got := dirEntries(t, filepath.Dir(left)); len(got) != 1 {
		t.Fatalf("forward directory holds %q after a sweep that could not list, want the leftover kept", got)
	}
	if err := os.Remove(filepath.Join(f.dir, "forwardlist.exit")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adb.forwardSocket(context.Background(), "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}
	if got := dirEntries(t, filepath.Dir(left)); len(got) != 0 {
		t.Errorf("forward directory holds %q after the retried sweep, want it empty", got)
	}
}

// Decision (ADR-0102 §1): a sweep tried again after a failed listing never takes for a
// leftover a forward this daemon made in between. The first forward finds a leftover but its
// listing is cut short, so a live view's forward is made; the next forward's sweep lists it in
// the directory and must neither remove it nor unlink its file. Seen failing without the set of
// the paths handed out (the live view's forward is removed and its socket unlinked).
func TestSweepRetryLeavesOwnLiveForwardsAlone(t *testing.T) {
	f := newUIFixture(t, nil)
	left := seedSock(t, f.dir, "reader-0000000000e1")
	if err := os.WriteFile(left, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cut, cancel := context.WithCancel(context.Background())
	cancel()
	live, err := f.adb.forwardSocket(cut, "adb", fwdKindScreen)
	if err != nil {
		t.Fatal(err)
	}
	if f.adb.fwd.ready {
		t.Fatal("the first sweep finished: the scenario was not reached")
	}
	if err := os.WriteFile(live, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	devicetest.AddForward(t, f.dir, uiSerial, live, "", "localabstract:scrcpy_00000001")

	if _, err := f.adb.forwardSocket(context.Background(), "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(c, "--remove") && strings.Contains(c, live) {
			t.Errorf("the live view's own forward was removed: %q", c)
		}
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live view's own socket was unlinked: %v", err)
	}
	if _, err := os.Stat(left); err == nil {
		t.Errorf("the leftover survived the retried sweep")
	}
}

// Decision (ADR-0102 §1, ADR-0019 §6): the forward directory is checked at every forward, not
// only the first. Under the /tmp fallback a cleanup while the daemon runs lets another
// account make the path again: a directory removed is made again private, and one that is
// now a symlink (or anything but this user's directory) is refused. Seen failing with the
// check made once (the symlink is followed and a socket is made through it).
func TestForwardDirIsCheckedAtEveryForward(t *testing.T) {
	f := newUIFixture(t, nil)
	ctx := context.Background()
	dir := forwardDirFor(t, f.dir)
	if _, err := f.adb.forwardSocket(ctx, "adb", fwdKindReader); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adb.forwardSocket(ctx, "adb", fwdKindReader); err != nil {
		t.Fatalf("a directory removed was not made again: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("forward directory %v (%v) after it was made again, want mode 0700", fi, err)
	}

	elsewhere := t.TempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if sock, err := f.adb.forwardSocket(ctx, "adb", fwdKindReader); err == nil {
		t.Errorf("forwardSocket gave %q through a directory that is a symlink", sock)
	}
}

// Decision (ADR-0102 §1, ADR-0019 §6): a forward's socket path must fit sun_path; one that
// would not is refused with a reason before adb is run, not left to fail inside adb. Seen
// failing with the check removed (a path past 104 bytes is returned).
func TestForwardSocketRefusesAPathThatDoesNotFit(t *testing.T) {
	f := newUIFixture(t, nil)
	long := filepath.Join(forwardDirFor(t, f.dir), strings.Repeat("d", 100))
	f.adb.SetForwardDir(long)
	_, err := f.adb.forwardSocket(context.Background(), "adb", fwdKindScreen)
	if err == nil || !strings.Contains(err.Error(), "would not fit") {
		t.Errorf("forwardSocket err = %v, want a refusal that names the length", err)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("adb ran %q for a path that cannot be a socket", calls)
	}
}

// Decision (ADR-0102 §1; E6): `forward --list` is every client's, so only a file of the shape
// this daemon makes, in its own forward directory, is ever adopted, dialled or unlinked. A
// listed forward to our reader's name at another path is not ours: this daemon makes its own
// forward and never touches the foreign one. Seen failing with the directory check removed
// (the foreign forward is found as ours, does not answer and is removed).
func TestAForwardOutsideTheForwardDirIsNotOurs(t *testing.T) {
	quickRecovery(t)
	r := devicetest.StartReader(t, pixelDoc(t))
	t.Cleanup(r.Close)
	f := newUIFixture(t, map[string]string{
		uiSerial + ".push.exit":   "0",
		uiSerial + ".start.out":   "",
		uiSerial + ".forward.out": r.Port,
	})
	const foreign = "/tmp/droidyard-elsewhere/reader-0000000000f0"
	devicetest.AddForward(t, f.dir, uiSerial, foreign, "", localAbstract(readerNameTest))

	mustComplete(t, f.tree(t, uiSerial))
	for _, c := range f.calls(t) {
		if strings.Contains(c, foreign) {
			t.Errorf("a call named the foreign socket: %q", c)
		}
	}
	if n := countForward(f.calls(t)); n != 1 {
		t.Errorf("%d forwards made, want this daemon's own one", n)
	}
	if !strings.Contains(strings.Join(devicetest.Forwards(t, f.dir), "\n"), foreign) {
		t.Errorf("forwards %q: the foreign one was removed", devicetest.Forwards(t, f.dir))
	}
}

package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jtortosasen/droidyard/internal/platform"
)

// The forwards to the device are Unix sockets, not loopback ports (ADR-0102): `adb forward
// localfilesystem:<dir>/<kind>-<12 hex> localabstract:<name>` makes adb's server listen on
// a socket file in a directory of the daemon's own, mode 0700, so no other account can
// connect to the reader or the screen server through it. adb leaves the file behind when
// it removes the forward, so the daemon unlinks it, and sweeps what a killed daemon left.

// forwardLocalPrefix starts the local side of a forward to a socket file.
const forwardLocalPrefix = "localfilesystem:"

// forwardKind names the device channel a forward carries, and so the start of its file.
const (
	fwdKindReader = "reader"
	fwdKindScreen = "screen"
)

// forwardFileRE is the shape of a file this daemon makes in its forward directory. Nothing
// else in an `adb forward --list` is ours: the listing is shared by every client of the
// adb server.
var forwardFileRE = regexp.MustCompile(`^(reader|screen)-[0-9a-f]{12}$`)

// forwardDir is the directory the forwards' sockets live in. mu orders its first use,
// which prepares it, against every other.
type forwardDir struct {
	mu    sync.Mutex
	path  string // set once, at wiring
	ready bool   // the sweep finished
	// issued are the paths handed out while the sweep had not finished: a sweep tried again
	// must never take for a leftover a forward this daemon made since. Dropped once it finishes.
	issued map[string]bool
}

// SetForwardDir gives the ADB the directory its forwards' sockets are made in
// (platform.Env.ForwardDir). It is called once, at wiring, before any call. Without one
// nothing can be forwarded.
func (a *ADB) SetForwardDir(dir string) { a.fwd.path = dir }

// forwardSocket returns the path of a new forward's socket, <dir>/<kind>-<12 hex>. The
// first call makes the directory private and sweeps what an earlier daemon left (ADR-0091):
// the daemon holds the lock by then, so only a file adb still lists is in use. The directory
// is checked on every call, not only the first: under the /tmp fallback a cleanup while the
// daemon runs would let another account make the path again. A name that would not fit
// sun_path is refused here, with the reason, not as a failure of adb.
func (a *ADB) forwardSocket(ctx context.Context, adbPath, kind string) (string, error) {
	f := &a.fwd
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.path == "" {
		return "", fmt.Errorf("no directory for the forwards to the device is configured")
	}
	if err := preparePrivateDir(f.path); err != nil {
		return "", err
	}
	if !f.ready {
		if f.ready = a.sweepForwards(ctx, adbPath); f.ready {
			f.issued = nil
		}
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("no random number was available for the forward's name")
	}
	path := filepath.Join(f.path, kind+"-"+hex.EncodeToString(b[:]))
	if !platform.Current().SocketFits(path) {
		return "", fmt.Errorf("the forward's socket path %s would not fit a Unix socket address (%d bytes)", path, len(path))
	}
	if !f.ready {
		if f.issued == nil {
			f.issued = map[string]bool{}
		}
		f.issued[path] = true
	}
	return path, nil
}

// preparePrivateDir makes dir 0700 and refuses one that is not this user's: it may be under
// the /tmp fallback, where another account could have made the path first (ADR-0019 §6).
func preparePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := platform.OwnDir(dir, false); err != nil {
		return err
	}
	if err := platform.OwnDir(filepath.Dir(dir), true); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// sweepForwards removes what an earlier daemon left in the directory. A live view's forward
// still listed is removed with its file: the server it led to was reaped with the daemon's
// pid records, and nothing would ever adopt it. A reader's still listed stays, file and all:
// an earlier daemon's reader that answers is adopted (reconcileForward). Every other file goes.
// A listing that cannot be read removes nothing, and reports false so the next forward
// sweeps again; what this daemon has handed out since is never touched (f.issued). With
// nothing in the directory adb is not asked at all. The caller holds f.mu.
func (a *ADB) sweepForwards(ctx context.Context, adbPath string) (done bool) {
	left, err := os.ReadDir(a.fwd.path)
	if err != nil || len(left) == 0 {
		return err == nil
	}
	out, err := a.Run(ctx, adbPath, readerForwardTimeout, forwardListCap, "forward", "--list")
	if err != nil || out.ExitCode != 0 || out.Truncated {
		return false
	}
	listed := map[string]bool{}
	for _, e := range parseForwardList(out.Stdout) {
		if a.fwd.issued[e.sock] {
			continue
		}
		if a.ownForward(e.sock) && strings.HasPrefix(filepath.Base(e.sock), fwdKindScreen+"-") {
			a.Run(ctx, adbPath, readerForwardTimeout, 1<<10, "-s", e.serial, "forward", "--remove", forwardLocalPrefix+e.sock)
			continue
		}
		listed[e.sock] = true
	}
	for _, e := range left {
		path := filepath.Join(a.fwd.path, e.Name())
		if !listed[path] && !a.fwd.issued[path] {
			os.Remove(path)
		}
	}
	return true
}

// ownForward says whether path is a file this daemon would make in its forward directory.
// The sockets it dials, and the files it unlinks, are only these.
func (a *ADB) ownForward(path string) bool {
	return a.fwd.path != "" && filepath.Dir(path) == a.fwd.path && forwardFileRE.MatchString(filepath.Base(path))
}

// unlinkForward removes the socket file of a forward that has been removed, or is gone:
// adb leaves it behind (ADR-0102).
func (a *ADB) unlinkForward(path string) {
	if a.ownForward(path) {
		os.Remove(path)
	}
}

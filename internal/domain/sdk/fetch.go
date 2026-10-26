package sdk

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The work of an install, per package in closure order (plan m3f step 4; ADR-0130 §1,
// §2): download into <root>/.temp/droidyard-<random>/, check, unpack there, write
// package.xml, and rename into place last. Nothing reaches a package's final path
// unverified or half-written, and a killed daemon leaves only .temp/droidyard-*.

const (
	// The unpack bounds, working defaults sized on the spike's §(b): 11,342 entries in
	// platform-36, 2.81 GB for the largest image.
	maxZipEntries      = 100_000
	maxZipUncompressed = uint64(16) << 30
	// unpackMarginTenths is the headroom asked beyond the unpacked total, in tenths.
	unpackMarginTenths = 1
	progressEvery      = 200 * time.Millisecond
)

// install runs sdk.install for ids: the ids are checked, then run does the work. The two
// are apart so a test can hand run an id that the model, not the caller, put there.
func (in *installer) install(ctx context.Context, call *capability.Call, ids []string) (any, error) {
	ids, err := checkIDs(ids)
	if err != nil {
		return nil, err
	}
	return in.run(ctx, call, ids)
}

func (in *installer) run(ctx context.Context, call *capability.Call, ids []string) (any, error) {
	p, err := in.makePlan(ctx, call, ids)
	if err != nil {
		return nil, err
	}
	res := InstallResult{Root: p.root, Installed: []InstalledPackage{}, Updated: []UpdatedPackage{}, Warnings: p.warnings}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	if len(p.steps) == 0 {
		return res, nil
	}

	// From here the call writes. Its own bounds: the whole call, and silence.
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	whole := time.AfterFunc(in.callBound, func() { cancel(errCallBound) })
	defer whole.Stop()

	if err := os.MkdirAll(p.root, 0o755); err != nil {
		return nil, capability.NewError(ErrNotWritable, "the SDK root cannot be made").
			WithCause(capability.Quote(p.root) + ": " + describeFS(err))
	}
	tmp, err := ensureTemp(p.root)
	if err != nil {
		return nil, err
	}
	in.sweep(tmp)
	res.Created = p.created

	prog := &progress{call: call, total: p.total, n: len(p.steps)}
	var done []string // ids in place, for a failure to name
	for i, s := range p.steps {
		prog.begin(i, s.pkg.ID)
		if err := in.fetchOne(rctx, cancel, p.root, tmp, s, prog); err != nil {
			return nil, in.failed(ctx, rctx, call, s.pkg.ID, err, done)
		}
		done = append(done, s.pkg.ID)
		if s.from != nil {
			res.Updated = append(res.Updated, UpdatedPackage{ID: s.pkg.ID, From: s.from.Version, To: s.pkg.Revision.String()})
		} else {
			res.Installed = append(res.Installed, InstalledPackage{ID: s.pkg.ID, Version: s.pkg.Revision.String()})
		}
	}
	res.Downloaded = prog.done
	return res, nil
}

// failed turns what stopped a package into the call's error. A caller's cancel is the
// context's error (the scheduler ends the operation cancelled) and the packages already in
// place are logged; one of Droidyard's own bounds is a download failure saying which; an
// entry name in the error is archive text, so the call is marked untrusted.
func (in *installer) failed(ctx, rctx context.Context, call *capability.Call, id string, err error, done []string) error {
	if ctx.Err() != nil {
		if len(done) > 0 {
			fmt.Fprintf(in.log, "sdk: install cancelled; already in place: %s\n", plain(strings.Join(done, ", ")))
		}
		return ctx.Err()
	}
	var ce *capability.Error
	switch cause := context.Cause(rctx); {
	case errors.Is(cause, errCallBound):
		ce = downloadFailed(id, fmt.Sprintf("the install ran past its %s bound", in.callBound))
	case errors.Is(cause, errSilent):
		ce = downloadFailed(id, fmt.Sprintf("no data was received for %s", in.silentBound))
	case !errors.As(err, &ce):
		ce = downloadFailed(id, describe(err))
	}
	if ce.Details == nil {
		ce.Details = map[string]any{}
	}
	if _, named := ce.Details["entry"]; named {
		yes := true
		call.Untrusted = &yes
	}
	if len(done) > 0 {
		ce.Details["installed"] = done
		if ce.Cause != "" {
			ce.Cause += "; "
		}
		ce.Cause += "already in place: " + strings.Join(done, ", ")
	}
	return ce
}

// progress reports bytes downloaded of the closure's archives and the place of the
// package in it, at most every progressEvery.
type progress struct {
	call  *capability.Call
	total int64
	done  int64
	n     int
	i     int
	id    string
	last  time.Time
}

func (p *progress) begin(i int, id string) {
	p.i, p.id = i, id
	p.report("downloading", true)
}

func (p *progress) add(n int) {
	p.done += int64(n)
	p.report("downloading", false)
}

func (p *progress) report(phase string, force bool) {
	if p.call == nil || p.call.Progress == nil {
		return
	}
	if now := time.Now(); force || now.Sub(p.last) >= progressEvery {
		p.last = now
		p.call.Progress(int(p.done), int(p.total), fmt.Sprintf("%s: %s (%d of %d)", phase, p.id, p.i+1, p.n))
	}
}

// fetchOne downloads, checks, unpacks and places one package. Its temporary directory is
// removed at every end, a simulated kill and a failed rollback excepted.
func (in *installer) fetchOne(ctx context.Context, cancel context.CancelCauseFunc, root, tempRoot string, s step, prog *progress) error {
	p := s.pkg
	work, err := os.MkdirTemp(tempRoot, tempPrefix)
	if err != nil {
		return capability.NewError(ErrNotWritable, "a temporary directory cannot be made").
			WithCause(tempDir + ": " + describeFS(err))
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()

	archive := filepath.Join(work, "archive.zip")
	if err := in.download(ctx, cancel, p, archive, prog); err != nil {
		return err
	}
	prog.report("unpacking", true)
	unpacked := filepath.Join(work, "unpacked")
	top, err := in.unpack(ctx, root, p, archive, unpacked)
	if err != nil {
		return err
	}
	xmlBytes, err := packageXML(p, s.lic)
	if err != nil {
		return downloadFailed(p.ID, describe(err))
	}
	if err := os.WriteFile(filepath.Join(unpacked, top, "package.xml"), xmlBytes, 0o644); err != nil {
		return downloadFailed(p.ID, "package.xml could not be written: "+describeFS(err))
	}
	prog.report("installing", true)
	return in.place(root, work, filepath.Join(unpacked, top), s, &keep)
}

// download fetches p's archive into dst through the repository's client (HTTPS, on its
// host, no redirect away), computing the sha1 as it writes and stopping past the declared
// size. A size or sha1 that does not match is SDK_DOWNLOAD_FAILED.
func (in *installer) download(ctx context.Context, cancel context.CancelCauseFunc, p *RemotePackage, dst string, prog *progress) error {
	size := p.Archive.Size
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Archive.URL, nil)
	if err != nil {
		return downloadFailed(p.ID, describe(err))
	}
	silent := time.AfterFunc(in.silentBound, func() { cancel(errSilent) })
	defer silent.Stop()
	resp, err := in.repo.client.Do(req)
	if err != nil {
		return downloadFailed(p.ID, describe(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return downloadFailed(p.ID, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > size {
		return downloadFailed(p.ID, fmt.Sprintf("the server announced %d bytes, more than the %d the repository declares", resp.ContentLength, size))
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return capability.NewError(ErrNotWritable, "a temporary file cannot be made").WithCause(tempDir + ": " + describeFS(err))
	}
	defer f.Close()

	h := sha1.New()
	lr := io.LimitReader(resp.Body, size+1)
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, rerr := lr.Read(buf)
		if k > 0 {
			silent.Reset(in.silentBound)
			n += int64(k)
			if n > size {
				return downloadFailed(p.ID, fmt.Sprintf("the body is longer than the %d bytes the repository declares", size))
			}
			h.Write(buf[:k])
			if _, werr := f.Write(buf[:k]); werr != nil {
				return writeFailure(p.ID, werr)
			}
			prog.add(k)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return downloadFailed(p.ID, describe(rerr))
		}
	}
	if err := f.Close(); err != nil {
		return writeFailure(p.ID, err)
	}
	if n != size {
		return downloadFailed(p.ID, fmt.Sprintf("the body is %d bytes, not the %d the repository declares", n, size))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.Archive.Checksum {
		return downloadFailed(p.ID, "the sha1 of the download is not the one the repository declares")
	}
	return nil
}

// writeFailure is a failed write to the temporary directory: a full disk is
// SDK_NO_SPACE, anything else a download failure.
func writeFailure(id string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return capability.NewError(ErrNoSpace, "the disk filled up while writing").WithCause(id + ": " + describeFS(err))
	}
	return downloadFailed(id, "writing the archive failed: "+describeFS(err))
}

// entryErr is an unpack refusal naming the entry. The name is archive text: cleaned,
// quoted, and put in details.entry so the failed call is marked untrusted.
func entryErr(id, name, why string) error {
	clean := cleanText(name, 120)
	return downloadFailed(id, fmt.Sprintf("entry %s %s", capability.Quote(clean), why)).
		WithDetails(map[string]any{"package": id, "entry": clean})
}

// unpack opens the verified archive, checks the whole central directory, then writes the
// files under dst/<top> and returns the top directory's name. Nothing is written before
// every check on the directory has passed (ADR-0130 §1).
func (in *installer) unpack(ctx context.Context, root string, p *RemotePackage, archive, dst string) (string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		return "", downloadFailed(p.ID, "the download is not a zip archive: "+describe(err))
	}
	// An insecure name is refused below, naming the entry; the reader is still usable.
	defer zr.Close()

	if len(zr.File) == 0 {
		return "", downloadFailed(p.ID, "the archive has no entries")
	}
	if len(zr.File) > in.maxEntries {
		return "", downloadFailed(p.ID, fmt.Sprintf("the archive has more than %d entries", in.maxEntries))
	}
	var top string
	var total uint64
	seen := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		name := f.Name
		if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
			return "", entryErr(p.ID, name, "is not a relative path inside the archive")
		}
		clean := strings.TrimSuffix(name, "/")
		if clean == "" || !filepath.IsLocal(filepath.FromSlash(clean)) || path.Clean(clean) != clean {
			return "", entryErr(p.ID, name, "is not a plain relative path")
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
		case mode.IsRegular():
			if strings.HasSuffix(name, "/") {
				return "", entryErr(p.ID, name, "is a file with a directory's name")
			}
		default:
			return "", entryErr(p.ID, name, fmt.Sprintf("is not a regular file or directory (mode %s)", mode.Type()))
		}
		comps := strings.Split(clean, "/")
		if top == "" {
			top = comps[0]
		} else if comps[0] != top {
			return "", entryErr(p.ID, name, "is outside the archive's one top directory "+capability.Quote(cleanText(top, 60)))
		}
		if len(comps) == 1 && !mode.IsDir() {
			return "", entryErr(p.ID, name, "is a file at the top of the archive, outside its top directory")
		}
		if seen[clean] {
			return "", entryErr(p.ID, name, "appears twice")
		}
		seen[clean] = true
		if f.UncompressedSize64 > in.maxUncompressed || total+f.UncompressedSize64 > in.maxUncompressed {
			return "", downloadFailed(p.ID, fmt.Sprintf("the archive unpacks to more than %d bytes", in.maxUncompressed))
		}
		total += f.UncompressedSize64
	}

	// The second space check, now that the unpacked size is known (ADR-0130 §2).
	need := total + total*unpackMarginTenths/10
	if free, err := in.free(root); err == nil && free < need {
		return "", capability.NewError(ErrNoSpace, "the disk does not have room to unpack a package").
			WithCause(fmt.Sprintf("%s unpacks to %d bytes (%d with a margin) and %d are free on the file system of %s", p.ID, total, need, free, capability.Quote(root))).
			WithDetails(map[string]any{"package": p.ID, "needed": need, "free": free})
	}

	if err := os.Mkdir(dst, 0o700); err != nil {
		return "", capability.NewError(ErrNotWritable, "a temporary directory cannot be made").WithCause(tempDir + ": " + describeFS(err))
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := in.extract(ctx, p.ID, f, dst); err != nil {
			return "", err
		}
	}
	return top, nil
}

// extract writes one entry. The name passed the central-directory checks, so it names a
// place under dst; each file is created exclusively, its mode set exactly, and read
// through a reader that stops one byte past the size the entry declares.
func (in *installer) extract(ctx context.Context, id string, f *zip.File, dst string) error {
	clean := strings.TrimSuffix(f.Name, "/")
	target := filepath.Join(dst, filepath.FromSlash(clean))
	if f.Mode().IsDir() {
		return mkdirs(dst, filepath.FromSlash(clean))
	}
	if err := mkdirs(dst, filepath.Dir(filepath.FromSlash(clean))); err != nil {
		return downloadFailed(id, "a directory could not be made: "+describeFS(err))
	}
	perm := fs.FileMode(0o644)
	if f.Mode().Perm()&0o111 != 0 {
		perm = 0o755
	}
	rc, err := f.Open()
	if err != nil {
		return entryErr(id, f.Name, "cannot be read: "+describe(err))
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return downloadFailed(id, "a file could not be made: "+describeFS(err))
	}
	n, err := io.Copy(out, io.LimitReader(ctxReader{ctx, rc}, int64(f.UncompressedSize64)+1))
	cerr := out.Close()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case n > int64(f.UncompressedSize64):
		return entryErr(id, f.Name, "is longer than the size it declares")
	case err != nil:
		if errors.Is(err, syscall.ENOSPC) {
			return writeFailure(id, err)
		}
		return entryErr(id, f.Name, "cannot be unpacked: "+describe(err))
	case n != int64(f.UncompressedSize64):
		return entryErr(id, f.Name, "is shorter than the size it declares")
	case cerr != nil:
		return writeFailure(id, cerr)
	}
	if err := os.Chmod(target, perm); err != nil {
		return downloadFailed(id, "a file's mode could not be set: "+describeFS(err))
	}
	return nil
}

// ctxReader stops a copy when the context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// mkdirs makes rel under base with mode 0755 whatever the umask, one component at a time.
func mkdirs(base, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	cur := base
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, comp)
		switch err := os.Mkdir(cur, 0o755); {
		case err == nil:
			if err := os.Chmod(cur, 0o755); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrExist):
			if fi, serr := os.Lstat(cur); serr != nil || !fi.IsDir() {
				return &fs.PathError{Op: "mkdir", Path: cur, Err: syscall.ENOTDIR}
			}
		default:
			return err
		}
	}
	return nil
}

// place moves the unpacked top directory to the package's final path. The id's relative
// path must be local and no parent may be a link that leaves the root. A fresh install
// renames the top directory to the final path; an update renames the old directory into
// the temporary one, the new one into place, then removes the old, and a failed second
// rename puts the old one back. A kill between the two renames leaves the package absent,
// never half-written. A rename across file systems is SDK_NOT_WRITABLE (ADR-0130 §1).
func (in *installer) place(root, work, top string, s step, keep *bool) error {
	abs, rel, err := finalPath(root, s.pkg.ID)
	if err != nil {
		return err
	}
	if err := checkParents(root, rel); err != nil {
		return err
	}
	if err := mkdirs(root, filepath.Dir(rel)); err != nil {
		return notWritableAt(rel, err)
	}
	if s.from == nil {
		if _, err := os.Lstat(abs); err == nil {
			return capability.NewError(ErrNotWritable, "a directory is in the way of a package").
				WithCause(capability.Quote(filepath.ToSlash(rel)) + " appeared while the package was being installed; it was left alone")
		}
		if err := in.rename(top, abs); err != nil {
			return notWritableAt(rel, err)
		}
		return nil
	}

	aside := filepath.Join(work, "replaced")
	if err := in.rename(abs, aside); err != nil {
		return notWritableAt(rel, err)
	}
	if in.afterAside != nil {
		if err := in.afterAside(); err != nil {
			*keep = true // a kill: nothing is put back, nothing is cleaned up
			return err
		}
	}
	if err := in.rename(top, abs); err != nil {
		if rerr := in.rename(aside, abs); rerr != nil {
			*keep = true // the old package is only in the temporary directory now
			return capability.NewError(ErrNotWritable, "an update failed and the old package could not be put back").
				WithCause(fmt.Sprintf("%s: %s; the old package is in %s", capability.Quote(filepath.ToSlash(rel)), describeFS(rerr), capability.Quote(relTo(root, aside))))
		}
		return notWritableAt(rel, err)
	}
	_ = os.RemoveAll(aside)
	return nil
}

// notWritableAt is SDK_NOT_WRITABLE for a failed move at rel, saying so when the move
// crossed file systems.
func notWritableAt(rel string, err error) error {
	e := capability.NewError(ErrNotWritable, "the package could not be moved into place").
		WithCause(capability.Quote(filepath.ToSlash(rel)) + ": " + describeFS(err))
	if errors.Is(err, syscall.EXDEV) {
		e.Cause = capability.Quote(filepath.ToSlash(rel)) + " is on another file system than the SDK's temporary directory (a mount point inside the SDK)"
		e.NextStep = "the user installs it with the tool that owns that volume, or removes the mount point from the SDK"
	}
	return e
}

// checkParents refuses an install whose parent directories include a link that leaves the
// root, or something that is not a directory. Parents that do not exist yet are fine.
func checkParents(root, rel string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return notWritableAt(".", err)
	}
	cur := root
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	for _, comp := range strings.Split(dir, string(filepath.Separator)) {
		cur = filepath.Join(cur, comp)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return notWritableAt(rel, err)
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			real, err := filepath.EvalSymlinks(cur)
			if err != nil || !within(realRoot, real) {
				return capability.NewError(ErrNotWritable, "a directory on the way is a link that leaves the SDK").
					WithCause(capability.Quote(relTo(root, cur)) + " is a symbolic link outside the SDK root; nothing was installed through it")
			}
		case !fi.IsDir():
			return capability.NewError(ErrNotWritable, "a file is in the way of a package").
				WithCause(capability.Quote(relTo(root, cur)) + " is not a directory")
		}
	}
	return nil
}

// within reports whether p is root or lies under it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && filepath.IsLocal(rel)
}

// uninstall runs sdk.uninstall for ids: each must be installed and not in use; each
// directory is renamed into <root>/.temp/droidyard-<random>/ and then removed, so a kill
// leaves nothing at the package's path.
func (in *installer) uninstall(ctx context.Context, call *capability.Call, ids []string) (any, error) {
	ids, err := checkIDs(ids)
	if err != nil {
		return nil, err
	}
	root, err := Target(in.env)
	if err != nil {
		return nil, err
	}
	installed, _ := installedOf(root, ids)
	var missing []string
	for _, id := range ids {
		if _, ok := installed[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, capability.NewError(ErrPackageNotFound, "a package is not installed").
			WithCause(strings.Join(missing, ", ") + " not installed in " + capability.Quote(root)).
			WithNextStep("call sdk.packages to see what is installed").
			WithDetails(map[string]any{"packages": missing})
	}
	if used := in.inUse(ctx, root, ids); len(used) > 0 {
		return nil, inUseError(call, used, ids)
	}

	res := UninstallResult{Removed: []string{}, Warnings: []string{}}
	budget := newSizeBudget(2_000_000, 30*time.Second)
	for _, id := range ids {
		if n, ok := budget.size(ctx, installed[id].dir); ok {
			res.Freed += n
		} else {
			res.Warnings = append(res.Warnings, "the size of "+id+" could not be measured within the bound and is not in freed")
		}
	}
	tmp, err := ensureTemp(root)
	if err != nil {
		return nil, err
	}
	in.sweep(tmp)
	work, err := os.MkdirTemp(tmp, tempPrefix)
	if err != nil {
		return nil, capability.NewError(ErrNotWritable, "a temporary directory cannot be made").WithCause(tempDir + ": " + describeFS(err))
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()

	// Every directory is renamed aside first, so a call that stops part way puts them back
	// and leaves the SDK as it found it; then the temporary directory is removed.
	var done []moved
	for i, id := range ids {
		from := installed[id].dir
		to := filepath.Join(work, fmt.Sprint(i))
		err := ctx.Err()
		if err == nil {
			if err = in.rename(from, to); err != nil {
				err = notWritableAt(relTo(root, from), err)
			}
		}
		if err != nil {
			if !in.putBack(done) {
				keep = true // the removed packages are only in the temporary directory now
			}
			return nil, err
		}
		done = append(done, moved{from, to})
		res.Removed = append(res.Removed, id)
	}
	return res, nil
}

// moved is one directory an uninstall renamed aside.
type moved struct{ from, to string }

// putBack undoes the renames of an uninstall that stopped part way, newest first, and says
// whether every one was undone.
func (in *installer) putBack(moves []moved) bool {
	ok := true
	for i := len(moves) - 1; i >= 0; i-- {
		if err := in.rename(moves[i].to, moves[i].from); err != nil {
			ok = false
		}
	}
	return ok
}

// Serialisation: sdk.install and sdk.uninstall share the scheduler key "sdk:<root>"
// (Descriptor.Resource), so this daemon runs one at a time on a root; another writer on
// the same root at the same time (Android Studio, a terminal sdkmanager) is not guarded
// against (plan m3f step 4, Not here).

package assistant

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fsRoot confines every read and write to one project root. It resolves symlinks one
// path component at a time and refuses a path whose real location is outside the root:
// init writes into the user's project, and a `.cursor` that links elsewhere is not a
// reason to write there. A link that stays inside the root is followed, so an AGENTS.md
// that is a link to CLAUDE.md is edited where it points and stays a link.
type fsRoot struct {
	real string // the root with its symlinks resolved
}

func newFSRoot(root string) (*fsRoot, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("root %q is not an absolute path", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", root)
	}
	return &fsRoot{real: real}, nil
}

func (r *fsRoot) inside(p string) bool {
	rel, err := filepath.Rel(r.real, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve returns the real path of rel (slash-separated, relative to the root), which
// need not exist, or an error when it would leave the root.
func (r *fsRoot) resolve(rel string) (string, error) {
	cur := r.real
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			cur = filepath.Join(append([]string{cur}, parts[i:]...)...)
			break
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("%s is a symlink that does not resolve", part)
			}
			next = target
		}
		cur = next
		if !r.inside(cur) {
			return "", fmt.Errorf("%s leaves the project root (it resolves to %s)", rel, cur)
		}
	}
	if !r.inside(cur) {
		return "", fmt.Errorf("%s leaves the project root", rel)
	}
	return cur, nil
}

// maxFileSize bounds what init reads: a config file of the user's is far smaller, and
// a larger one is not something to parse and rewrite.
const maxFileSize = 1 << 20

// readFile returns the file's content, or exists false when there is none. Anything
// that is not a regular file is an error before it is opened (a FIFO would block the
// read for good), and so is a file over maxFileSize.
func readFile(path string) (data []byte, exists bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	if fi.Size() > maxFileSize {
		return nil, false, errTooLarge
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxFileSize {
		return nil, false, errTooLarge
	}
	return data, true, nil
}

var errTooLarge = errors.New("larger than 1 MiB, which no config file of the user's is")

// writeFile writes through a temporary file in the same directory and renames it over
// the target, so a reader never sees half a file. An existing file keeps its mode.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".droidyard-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	ok = true
	return nil
}

// removeFile deletes a file and then every directory it leaves empty, up to but never
// including the root.
func (r *fsRoot) removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for dir := filepath.Dir(path); dir != r.real && r.inside(dir); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // not empty, or not ours to remove
			break
		}
	}
	return nil
}

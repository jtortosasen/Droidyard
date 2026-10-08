package file

import (
	"errors"
	"io"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// listCap is the most names file.list captures before it stops; above it the result is
// a sorted subset of the directory, not its first names (D4).
const listCap = 20000

// listDefault and listMax bound the limit parameter (D4).
const (
	listDefault = 500
	listMax     = 2000
)

// ListParams is file.list's parameters.
type ListParams struct {
	Path  string `json:"path,omitempty" jsonschema_description:"Directory to list, relative to the project root; \".\" or absent is the root."`
	Limit *int   `json:"limit,omitempty" jsonschema:"minimum=1,maximum=2000" jsonschema_description:"The most entries to return, 1 to 2000. Absent means 500."`
}

// Entry is one directory entry of file.list's result.
type Entry struct {
	Name string `json:"name" jsonschema:"required"`
	Type string `json:"type" jsonschema:"required,enum=file,enum=dir,enum=symlink,enum=other" jsonschema_description:"The entry's kind; a symlink is reported as symlink, never followed."`
	// Size and ModTime are the entry's own (an lstat, so a symlink's own size and time),
	// absent when its information could not be read.
	Size    *int64     `json:"size,omitempty"`
	ModTime *time.Time `json:"modTime,omitempty"`
}

// ListResult is file.list's result.
type ListResult struct {
	Path      string  `json:"path" jsonschema:"required" jsonschema_description:"The directory listed, relative to the project root."`
	Entries   []Entry `json:"entries" jsonschema:"required" jsonschema_description:"One level of entries, sorted by name; empty when the directory is empty."`
	Truncated bool    `json:"truncated" jsonschema_description:"True when more entries exist than were returned."`
	Hint      string  `json:"hint,omitempty" jsonschema_description:"Present when truncated, how to narrow the listing; or, on a result returned again after delivery, that its entries were dropped and file.list must be called again."`
}

// list serves file.list (D4): one level, symlinks reported but not followed, sorted by
// name, at most limit returned.
func (r *resolver) list(p ListParams) (ListResult, error) {
	rel, cerr := r.resolve(p.Path, false)
	if cerr != nil {
		return ListResult{}, cerr
	}
	limit := listDefault
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > listMax {
			return ListResult{}, capability.Errorf(capability.ErrInvalidParams, "limit must be between 1 and %d, got %d", listMax, *p.Limit)
		}
		limit = *p.Limit
	}

	root, cerr := r.openRoot()
	if cerr != nil {
		return ListResult{}, cerr
	}
	defer root.Close()

	// Non-blocking, so a path that is not a directory opens at once and is refused
	// below; the descriptor's own fstat decides the type.
	dir, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ListResult{}, r.classify(rel, err)
	}
	defer dir.Close()
	if fi, err := dir.Stat(); err != nil {
		return ListResult{}, r.classify(rel, err)
	} else if !fi.IsDir() {
		return ListResult{}, notDir(rel, typeName(fi.Mode()))
	}

	names, capped, err := readNames(dir)
	if err != nil {
		return ListResult{}, r.classify(rel, err)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })

	truncated := capped || len(names) > limit
	shown := names
	if len(shown) > limit {
		shown = shown[:limit]
	}
	out := ListResult{Path: rel, Entries: make([]Entry, 0, len(shown)), Truncated: truncated}
	if truncated {
		out.Hint = "list a subdirectory"
	}
	for _, d := range shown {
		out.Entries = append(out.Entries, entryOf(d))
	}
	return out, nil
}

// readNames reads the directory's entries, stopping at listCap; capped reports whether
// it stopped because more entries existed (D4).
func readNames(dir *os.File) (names []os.DirEntry, capped bool, err error) {
	for len(names) < listCap {
		chunk, rerr := dir.ReadDir(listCap - len(names))
		names = append(names, chunk...)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return names, false, nil
			}
			return nil, false, rerr
		}
		if len(chunk) == 0 {
			return names, false, nil
		}
	}
	if len(names) == listCap {
		if more, rerr := dir.ReadDir(1); rerr == nil && len(more) > 0 {
			capped = true
		}
	}
	return names, capped, nil
}

// entryOf builds a file.list entry from a directory entry. Its type, size and time come
// from Info (an lstat, so a symlink describes the link and never its target); when Info
// fails, the type comes from the directory read's own record (D4).
func entryOf(d os.DirEntry) Entry {
	e := Entry{Name: d.Name()}
	if fi, err := d.Info(); err == nil {
		e.Type = entryType(fi.Mode())
		size := fi.Size()
		e.Size = &size
		mt := fi.ModTime()
		e.ModTime = &mt
	} else {
		e.Type = entryType(d.Type())
	}
	return e
}

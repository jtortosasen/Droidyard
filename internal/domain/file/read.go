package file

import (
	"bytes"
	"time"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// ReadParams is file.read's parameters.
type ReadParams struct {
	Path   string `json:"path" jsonschema:"required" jsonschema_description:"The file to read, relative to the project root. An absolute path inside the root is accepted."`
	Offset *int64 `json:"offset,omitempty" jsonschema_description:"Byte offset to read from; negative counts back from the end."`
	Limit  *int   `json:"limit,omitempty" jsonschema:"minimum=4,maximum=262144" jsonschema_description:"The most bytes to return, 4 to 262144. Absent means 16384. A read ends at the last complete line within it."`
}

// ReadResult is file.read's result (D5): ADR-0012's paging over a project file. Text is
// absent for a binary file and, once the result has been delivered, for a retained one
// (Slim); size is always present.
type ReadResult struct {
	Path       string    `json:"path" jsonschema:"required" jsonschema_description:"The file read, relative to the project root."`
	Size       int64     `json:"size" jsonschema:"required" jsonschema_description:"The file's size in bytes."`
	ModTime    time.Time `json:"modTime" jsonschema:"required"`
	Offset     int64     `json:"offset" jsonschema:"required" jsonschema_description:"The byte the text starts at, after a negative offset was moved forward to a line start."`
	NextOffset int64     `json:"nextOffset" jsonschema:"required" jsonschema_description:"Where the next read should start."`
	Text       *string   `json:"text,omitempty" jsonschema_description:"The bytes read; absent for a binary file."`
	Truncated  bool      `json:"truncated" jsonschema_description:"True when a single line longer than limit was cut at a UTF-8 boundary."`
	Binary     bool      `json:"binary" jsonschema_description:"True when the page read holds a NUL byte or invalid UTF-8; text is then absent, and offset and nextOffset still page past it."`
}

// read serves file.read (D5): a regular file, opened through the root and paged exactly
// as artifact.read is. A window holding a NUL byte or invalid UTF-8 (a rune cut by the
// window's end excepted) is binary: text is omitted, size kept.
func (r *resolver) read(p ReadParams) (ReadResult, error) {
	if p.Limit != nil && (*p.Limit < artifact.MinTextLimit || *p.Limit > artifact.MaxTextLimit) {
		return ReadResult{}, capability.Errorf(capability.ErrInvalidParams, "limit must be between %d and %d, got %d", artifact.MinTextLimit, artifact.MaxTextLimit, *p.Limit)
	}
	rel, cerr := r.resolve(p.Path, true)
	if cerr != nil {
		return ReadResult{}, cerr
	}
	offset := int64(0)
	if p.Offset != nil {
		offset = *p.Offset
	}
	limit := 0
	if p.Limit != nil {
		limit = *p.Limit
	}

	root, cerr := r.openRoot()
	if cerr != nil {
		return ReadResult{}, cerr
	}
	defer root.Close()

	f, fi, cerr := r.openRegular(root, rel)
	if cerr != nil {
		return ReadResult{}, cerr
	}
	defer f.Close()
	size := fi.Size()
	modTime := fi.ModTime()

	// The binary check runs on the bytes the page actually returns, after a negative
	// offset has moved forward to a line start, so the check and the text never look at
	// different windows.
	page, err := artifact.ReadTextReaderAt(f, size, false, offset, limit)
	if err != nil {
		return ReadResult{}, r.ioFailed(rel, err)
	}
	if looksBinary([]byte(page.Text)) {
		// This window is binary: no text is returned; size and the window's position are
		// kept, so the caller can page past it or stop.
		return ReadResult{
			Path: rel, Size: page.Size, ModTime: modTime,
			Offset: page.Offset, NextOffset: page.NextOffset, Binary: true,
		}, nil
	}
	text := page.Text
	return ReadResult{
		Path: rel, Size: page.Size, ModTime: modTime,
		Offset: page.Offset, NextOffset: page.NextOffset,
		Text: &text, Truncated: page.Truncated,
	}, nil
}

// looksBinary reports whether a window is binary: it holds a NUL byte, or invalid
// UTF-8 other than a rune cut by the window's end (a read that ends mid-rune is not
// binary).
func looksBinary(win []byte) bool {
	if bytes.IndexByte(win, 0) >= 0 {
		return true
	}
	for i := 0; i < len(win); {
		r, size := utf8.DecodeRune(win[i:])
		if r == utf8.RuneError && size == 1 {
			return !isTruncatedRuneAtEnd(win[i:])
		}
		i += size
	}
	return false
}

// isTruncatedRuneAtEnd reports whether b is the valid prefix of a single UTF-8 sequence
// cut short at the window's end: a lead byte and its continuation bytes, fewer than the
// sequence needs.
func isTruncatedRuneAtEnd(b []byte) bool {
	if len(b) < 1 || len(b) >= utf8.UTFMax {
		return false
	}
	var need int
	switch {
	case b[0]&0x80 == 0:
		return false
	case b[0]&0xE0 == 0xC0:
		need = 2
	case b[0]&0xF0 == 0xE0:
		need = 3
	case b[0]&0xF8 == 0xF0:
		need = 4
	default:
		return false
	}
	if len(b) >= need {
		return false
	}
	for _, c := range b[1:] {
		if c&0xC0 != 0x80 {
			return false
		}
	}
	return true
}

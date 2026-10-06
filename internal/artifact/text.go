package artifact

import (
	"bytes"
	"io"
	"os"
	"unicode/utf8"
)

// Text limits (ADR-0012 §Reading artifacts).
const (
	// DefaultTextLimit is what a read returns when the caller names none.
	DefaultTextLimit = 16 << 10
	// MaxTextLimit is the most a caller may ask for.
	MaxTextLimit = 256 << 10
)

// TextPage is one read of a text artifact: ADR-0012's
// {offset, nextOffset, size, growing, text, truncated}.
type TextPage struct {
	// Offset is where the returned text starts, after a negative offset was moved
	// forward to the next line start.
	Offset int64
	// NextOffset is where the next read of the same artifact should start; it is the
	// byte after the last complete line returned. When the last complete line is held
	// back (a partial line, or none in the window), it is where this text started.
	NextOffset int64
	// Size is the artifact's size in bytes.
	Size int64
	// Growing is true while the producer still holds the artifact open.
	Growing bool
	// Text is the bytes read, up to the last complete line within the limit.
	Text string
	// Truncated is true only when a single line longer than the limit was cut at a
	// UTF-8 boundary; the next read continues the same line.
	Truncated bool
}

// ReadText reads one page of a text artifact (ADR-0012 §Reading artifacts). A negative
// offset counts back from the end, and a read that would start mid-line is moved
// forward to the next line start. The text ends at the last complete line within
// limit: a line the limit cuts in half is held back, and a single line longer than the
// limit is cut at a UTF-8 boundary with Truncated. An offset at or past the end is
// empty text. limit is clamped to [1, MaxTextLimit]; a caller wanting the default
// passes zero.
func ReadText(e Entry, offset int64, limit int) (TextPage, error) {
	size := e.Size
	if e.File == "" {
		size = int64(len(e.Data))
	}
	if limit <= 0 {
		limit = DefaultTextLimit
	}
	if limit > MaxTextLimit {
		limit = MaxTextLimit
	}

	start := offset
	if start < 0 {
		start = size + start
	}
	if start < 0 {
		start = 0
	}
	if start > size {
		start = size
	}
	page := TextPage{Size: size, Growing: e.Growing}

	// A negative offset lands anywhere; move it forward to the next line start. The
	// scan is bounded by the window: a line longer than the limit is cut below, not
	// jumped over.
	if offset < 0 && start > 0 && start < size {
		if prev, ok, err := e.byteAt(start - 1); err != nil {
			return TextPage{}, err
		} else if ok && prev != '\n' {
			win, err := e.readAt(start, int64(limit))
			if err != nil {
				return TextPage{}, err
			}
			if i := bytes.IndexByte(win, '\n'); i >= 0 {
				start += int64(i) + 1
			}
		}
	}
	page.Offset = start
	if start >= size {
		page.NextOffset = size
		return page, nil
	}

	win, err := e.readAt(start, int64(limit))
	if err != nil {
		return TextPage{}, err
	}
	if i := bytes.LastIndexByte(win, '\n'); i >= 0 {
		page.Text = string(win[:i+1])
		page.NextOffset = start + int64(i) + 1
		return page, nil
	}
	// No complete line in the window: either the line is longer than the limit, or the
	// artifact's last line has not been terminated yet.
	if start+int64(len(win)) < size {
		n := utf8Boundary(win)
		page.Text = string(win[:n])
		page.NextOffset = start + int64(n)
		page.Truncated = true
		return page, nil
	}
	// The final, unterminated line is held back: it is not a complete line yet. The
	// next read starts at the same offset and sees it once its newline arrives.
	page.NextOffset = start
	return page, nil
}

// readAt returns the bytes of the entry in [from, from+n), however many exist. It
// reads a durable artifact's file in place, so a page of a large console never pulls
// the whole of it into memory.
func (e Entry) readAt(from, n int64) ([]byte, error) {
	if e.File == "" {
		if from < 0 {
			from = 0
		}
		if from >= int64(len(e.Data)) || n <= 0 {
			return nil, nil
		}
		return e.Data[from:min(from+n, int64(len(e.Data)))], nil
	}
	if n <= 0 {
		return nil, nil
	}
	if from < 0 {
		from = 0
	}
	f, err := os.Open(e.File)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	end := from + n
	if e.Size > 0 && end > e.Size {
		end = e.Size
	}
	if end <= from {
		return nil, nil
	}
	buf := make([]byte, end-from)
	m, err := io.ReadFull(io.NewSectionReader(f, from, end-from), buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:m], nil
}

// byteAt returns the byte at i, and whether it exists.
func (e Entry) byteAt(i int64) (byte, bool, error) {
	b, err := e.readAt(i, 1)
	if err != nil {
		return 0, false, err
	}
	if len(b) == 0 {
		return 0, false, nil
	}
	return b[0], true, nil
}

// utf8Boundary is the longest prefix of b that ends on a UTF-8 boundary, so a cut
// never splits a rune in half.
func utf8Boundary(b []byte) int {
	n := len(b)
	for i := 0; i < utf8.UTFMax-1 && n > 0; i++ {
		if r, size := utf8.DecodeLastRune(b[:n]); r != utf8.RuneError || size != 1 {
			break
		}
		n--
	}
	return n
}

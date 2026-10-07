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
	// MinTextLimit is the least: one rune's width, so a cut always advances.
	MinTextLimit = utf8.UTFMax
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

// ReadText reads one page of a text artifact entry (ADR-0012 §Reading artifacts). A
// durable entry is paged in place, so a page of a large console never pulls the whole
// of it into memory.
func ReadText(e Entry, offset int64, limit int) (TextPage, error) {
	if e.File == "" {
		return ReadTextReaderAt(bytes.NewReader(e.Data), int64(len(e.Data)), e.Growing, offset, limit)
	}
	f, err := os.Open(e.File)
	if err != nil {
		return TextPage{}, err
	}
	defer f.Close()
	return ReadTextReaderAt(f, e.Size, e.Growing, offset, limit)
}

// ReadTextReaderAt reads one page of a text source: r is read at byte offsets and size
// is its length. It is the same paging as ReadText over an open reader, so file.read
// (internal/domain/file) pages the descriptor a root opened without pulling the file
// into memory. A negative offset counts back from the end, and when it lands mid-line
// it is moved forward to the next line start. The text ends at the last complete line
// within limit: a line the limit cuts in half is held back, and a single line longer
// than the limit is cut at a UTF-8 boundary with Truncated. An offset at or past the
// end is empty text. limit is clamped to [MinTextLimit, MaxTextLimit]; a caller wanting
// the default passes zero.
func ReadTextReaderAt(r io.ReaderAt, size int64, growing bool, offset int64, limit int) (TextPage, error) {
	limit = clampTextLimit(limit)
	if size < 0 {
		size = 0
	}

	// readAt returns the bytes in [from, from+n), however many exist.
	readAt := func(from, n int64) ([]byte, error) {
		if from < 0 || n <= 0 || from >= size {
			return nil, nil
		}
		if from+n > size {
			n = size - from
		}
		if n <= 0 {
			return nil, nil
		}
		buf := make([]byte, n)
		m, err := io.ReadFull(io.NewSectionReader(r, from, n), buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		return buf[:m], nil
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
	page := TextPage{Size: size, Growing: growing}

	// A negative offset lands anywhere; move it forward to the next line start. The
	// scan is bounded by the window: a line longer than the limit is cut below, not
	// jumped over.
	if offset < 0 && start > 0 && start < size {
		prev, err := readAt(start-1, 1)
		if err != nil {
			return TextPage{}, err
		}
		if len(prev) > 0 && prev[0] != '\n' {
			win, err := readAt(start, int64(limit))
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

	win, err := readAt(start, int64(limit))
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
	// The final, unterminated line. While the producer writes it is held back, so a
	// growing artifact never shows half a line: the next read starts at the same offset
	// and sees it once its newline arrives. Once closed, the end of the artifact ends
	// the line (a cap or a cancel can stop mid-line), and it is returned.
	if !growing {
		page.Text = string(win)
		page.NextOffset = start + int64(len(win))
		return page, nil
	}
	page.NextOffset = start
	return page, nil
}

// clampTextLimit applies ADR-0012's bounds: a caller wanting the default passes zero.
// Below one rune's width a cut at a UTF-8 boundary could return nothing and never
// advance, so the floor is MinTextLimit.
func clampTextLimit(limit int) int {
	if limit <= 0 {
		return DefaultTextLimit
	}
	if limit > MaxTextLimit {
		return MaxTextLimit
	}
	if limit < MinTextLimit {
		return MinTextLimit
	}
	return limit
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

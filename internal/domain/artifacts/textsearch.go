package artifacts

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/textfilter"
)

// Search limits (ADR-0136 §1).
const (
	// SearchScanBytes is the most one search call matches patterns against: a 32 MiB console
	// takes at most four calls to search whole.
	SearchScanBytes = 8 << 20
	// SearchLineBytes is how much of a line is matched: a longer line is matched on its first
	// 64 KiB, so one enormous line cannot cost a pass more than a normal one.
	SearchLineBytes = 64 << 10
)

// SearchQuery is one search call over a text artifact.
type SearchQuery struct {
	// Filter holds the patterns: a line is matched when Filter.Keep says so.
	Filter *textfilter.Filter
	// Context is grep -C: lines around a match that join the result (0 to 10).
	Context int
	// MaxLines caps the lines returned, context included; <= 0 means no cap.
	MaxLines int
	// MaxLineBytes cuts a returned line's text, at a UTF-8 boundary; <= 0 means no cut.
	MaxLineBytes int
	// Keep says which end of the result survives MaxLines.
	Keep textfilter.Keep
	// Offset is where the scan starts: 0, or an earlier call's NextOffset (a line start).
	Offset int64
	// Budget is the time the pass may take; nil means textfilter.FilterBudget. A budget
	// of zero stops the call after its first line.
	Budget *time.Duration
}

// foundLine is one line SearchText returns. Line counts from the artifact's first line, 1-based.
type foundLine struct {
	Line    int
	Text    string
	Context bool // a neighbour of a match, not a match itself
	Cut     bool // Text is the start of a longer line
}

// SearchOutcome is what one call found.
type SearchOutcome struct {
	Lines []foundLine
	// Matched counts the matching lines in the range scanned, returned or not.
	Matched int
	// Scanned is how many bytes of the artifact the call went through, from Offset.
	Scanned int64
	// Size and Growing are the artifact's, as ReadText reports them.
	Size    int64
	Growing bool
	// NextOffset is where the next call continues; HasNext is false when the call reached
	// the end of the artifact (a growing one's held-back partial line is not the end).
	NextOffset int64
	HasNext    bool
	// NextLine is the 1-based number of the line at NextOffset.
	NextLine int
	// Truncated is true when MaxLines dropped lines.
	Truncated bool
}

// SearchText scans a text artifact entry line by line, in place: the bytes, or the
// durable file, are read through a buffer and never held whole (ADR-0136 §1). It numbers
// lines from the artifact's start, so a call that continues an earlier one reports the
// lines' true numbers. A call ends at the end of the artifact, after SearchScanBytes, or
// when the time budget is spent or ctx ends, whichever comes first; it always takes at
// least one line, so a continuation advances. An unterminated last line of a growing
// artifact is held back, as ReadText holds it.
func SearchText(ctx context.Context, e artifact.Entry, q SearchQuery) (SearchOutcome, error) {
	if e.File == "" {
		return searchReaderAt(ctx, bytes.NewReader(e.Data), int64(len(e.Data)), e.Growing, q)
	}
	f, err := os.Open(e.File)
	if err != nil {
		return SearchOutcome{}, err
	}
	defer f.Close()
	return searchReaderAt(ctx, f, e.Size, e.Growing, q)
}

// ErrSearchOffset is returned for an offset that is not a line start of the artifact.
var ErrSearchOffset = errors.New("offset is not 0 or the start of a line within the artifact")

func searchReaderAt(ctx context.Context, r io.ReaderAt, size int64, growing bool, q SearchQuery) (SearchOutcome, error) {
	out := SearchOutcome{Size: size, Growing: growing}
	if q.Offset < 0 || q.Offset > size {
		return out, ErrSearchOffset
	}
	if q.Offset > 0 {
		var prev [1]byte
		if _, err := r.ReadAt(prev[:], q.Offset-1); err != nil && err != io.EOF {
			return out, err
		}
		if prev[0] != '\n' {
			return out, ErrSearchOffset
		}
	}
	// Line numbering: a byte scan up to the offset, no pattern.
	firstLine := 1
	if q.Offset > 0 {
		n, err := countNewlines(io.NewSectionReader(r, 0, q.Offset))
		if err != nil {
			return out, err
		}
		firstLine = n + 1
	}

	budget := textfilter.FilterBudget
	if q.Budget != nil {
		budget = *q.Budget
	}
	check := textfilter.Pass(ctx, budget)

	// Pass one: match every line, remembering which ones matched.
	var matched bitset
	lines := 0
	pos := q.Offset
	var scanned int64
	err := eachLine(io.NewSectionReader(r, q.Offset, size-q.Offset), func(line []byte, raw int64, terminated bool) bool {
		if !terminated && growing {
			return false // held back
		}
		if q.Filter.Keep(string(line)) {
			matched.set(lines)
			out.Matched++
		}
		lines++
		pos += raw
		scanned += raw
		if scanned >= SearchScanBytes || check.Check() != textfilter.Running {
			return false
		}
		return true
	})
	if err != nil {
		return out, err
	}
	out.Scanned = scanned
	out.NextOffset = pos
	out.HasNext = pos < size
	out.NextLine = firstLine + lines

	picks, _, truncated := textfilter.Select(lines, matched.get, q.Context, q.MaxLines, q.Keep)
	out.Truncated = truncated
	if len(picks) == 0 {
		return out, nil
	}

	// Pass two: read the picked lines back, up to where pass one stopped. Bytes before
	// that point never change (artifacts are append-only), so the line numbers agree.
	next := 0
	idx := 0
	err = eachLine(io.NewSectionReader(r, q.Offset, pos-q.Offset), func(line []byte, _ int64, _ bool) bool {
		defer func() { idx++ }()
		if next >= len(picks) {
			return false
		}
		if picks[next].Index != idx {
			return true
		}
		text := line
		l := foundLine{Line: firstLine + idx, Context: picks[next].Context}
		if q.MaxLineBytes > 0 && len(text) > q.MaxLineBytes {
			text = text[:artifact.UTF8Boundary(text[:q.MaxLineBytes])]
			l.Cut = true
		}
		l.Text = string(text)
		out.Lines = append(out.Lines, l)
		next++
		return next < len(picks)
	})
	return out, err
}

// trimEOL drops a line's "\n" and the "\r" before it.
func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// eachLine calls fn for every line of r: its content without the line break (the first
// SearchLineBytes of it, for a longer line), its raw length in bytes with the break, and
// whether a newline ended it. The last line of r has none when the source does not end
// with one. fn returning false stops the scan. A line is never held whole: past the limit
// its bytes are counted and dropped.
func eachLine(r io.Reader, fn func(line []byte, raw int64, terminated bool) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		var kept []byte
		var raw int64
		terminated := false
		for {
			chunk, err := br.ReadSlice('\n')
			raw += int64(len(chunk))
			if room := SearchLineBytes + 2 - len(kept); room > 0 {
				kept = append(kept, chunk[:min(len(chunk), room)]...)
			}
			if err == nil {
				terminated = true
				break
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if err == io.EOF {
				break
			}
			return err
		}
		if raw == 0 {
			return nil
		}
		if int64(len(kept)) == raw {
			kept = trimEOL(kept) // the whole line is here
		}
		if len(kept) > SearchLineBytes {
			kept = kept[:SearchLineBytes]
		}
		if !fn(kept, raw, terminated) {
			return nil
		}
	}
}

func countNewlines(r io.Reader) (int, error) {
	buf := make([]byte, 64<<10)
	n := 0
	for {
		m, err := r.Read(buf)
		n += bytes.Count(buf[:m], []byte{'\n'})
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// bitset is a growable set of small non-negative integers.
type bitset []uint64

func (b *bitset) set(i int) {
	for len(*b) <= i/64 {
		*b = append(*b, 0)
	}
	(*b)[i/64] |= 1 << (i % 64)
}

func (b bitset) get(i int) bool { return i/64 < len(b) && b[i/64]&(1<<(i%64)) != 0 }

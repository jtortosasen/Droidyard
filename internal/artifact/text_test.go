package artifact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func text(data string) Entry { return Entry{Data: []byte(data), Size: int64(len(data))} }

// Decision (ADR-0012 §Reading artifacts): a read ends at the last complete line within
// the limit; an unterminated final line is held back, so a producer's half-written
// line is never presented as one.
func TestTextEndsAtTheLastCompleteLine(t *testing.T) {
	p, err := ReadText(text("aaa\nbbb\n"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "aaa\nbbb\n" || p.NextOffset != 8 || p.Truncated {
		t.Fatalf("complete lines: %+v", p)
	}
	p, _ = ReadText(text("aaa\nbbb"), 0, 0)
	if p.Text != "aaa\n" || p.NextOffset != 4 || p.Truncated {
		t.Fatalf("an unterminated final line must be held back: %+v", p)
	}
	// At the limit: "aaaa\nb" cannot end inside "bbbb", so it ends at "aaaa\n".
	p, _ = ReadText(text("aaaa\nbbbb\ncccc\n"), 0, 6)
	if p.Text != "aaaa\n" || p.NextOffset != 5 || p.Truncated {
		t.Fatalf("the window cut a line: %+v", p)
	}
}

// Decision (ADR-0012 §Reading artifacts): a negative offset counts back from the end,
// and a read that would start mid-line is moved forward to the next line start.
func TestTextNegativeOffsetStartsAtTheNextLine(t *testing.T) {
	const data = "aaa\nbbb\nccc\n" // size 12; the negative offsets below land in "bbb"
	for _, c := range []struct {
		offset     int64
		wantOffset int64
		wantText   string
	}{
		{-5, 8, "ccc\n"}, // -5 lands on the newline at 7: already a line start
		{-4, 8, "ccc\n"},
		{-6, 8, "ccc\n"}, // -6 lands at 6, mid-line: forward to 8
		{-12, 0, data},
		{-100, 0, data},
	} {
		p, err := ReadText(text(data), c.offset, 0)
		if err != nil {
			t.Fatal(err)
		}
		if p.Offset != c.wantOffset || p.Text != c.wantText {
			t.Errorf("offset %d: %+v, want offset %d text %q", c.offset, p, c.wantOffset, c.wantText)
		}
	}
}

// Decision (ADR-0012 §Reading artifacts): a single line longer than the limit is cut
// at a UTF-8 boundary and marked truncated; the next read continues the same line.
func TestTextLongLineCutAtAUTF8Boundary(t *testing.T) {
	limit := DefaultTextLimit
	line := strings.Repeat("a", limit-1) + "é" + strings.Repeat("x", 300<<10) + "\n"
	p, err := ReadText(text(line), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated {
		t.Fatalf("a cut line must be truncated: %+v", p)
	}
	if len(p.Text) != limit-1 || p.NextOffset != int64(limit-1) {
		t.Fatalf("len(text)=%d nextOffset=%d, want %d: the rune was split", len(p.Text), p.NextOffset, limit-1)
	}
	if !utf8.ValidString(p.Text) {
		t.Fatal("the cut text is not valid UTF-8")
	}
	// The continuation starts at nextOffset and reads the same line.
	p2, _ := ReadText(text(line), p.NextOffset, 0)
	if p2.Offset != p.NextOffset || len(p2.Text) == 0 {
		t.Fatalf("continuation: %+v", p2)
	}
}

// Decision (ADR-0012 §Reading artifacts): an offset at or past the end returns empty
// text, and empty is a fact: offset, nextOffset and size are still reported.
func TestTextOffsetAtOrPastTheEndIsEmpty(t *testing.T) {
	for _, offset := range []int64{4, 5, 100} {
		p, err := ReadText(text("abc\n"), offset, 0)
		if err != nil {
			t.Fatal(err)
		}
		if p.Text != "" || p.Offset != 4 || p.NextOffset != 4 || p.Size != 4 {
			t.Errorf("offset %d: %+v", offset, p)
		}
	}
}

// Decision (ADR-0012 §Reading artifacts): the limit defaults to 16 KiB and is capped
// at 256 KiB.
func TestTextLimitBounds(t *testing.T) {
	data := strings.Repeat("0123456789\n", 4096) // 45056 bytes
	def, _ := ReadText(text(data), 0, 0)
	explicit, _ := ReadText(text(data), 0, DefaultTextLimit)
	if def.Text != explicit.Text || len(explicit.Text) > DefaultTextLimit {
		t.Fatalf("the default limit is not %d: len=%d", DefaultTextLimit, len(explicit.Text))
	}
	big := strings.Repeat("a", 300<<10) // one line longer than the cap
	capped, _ := ReadText(text(big), 0, MaxTextLimit*4)
	if len(capped.Text) > MaxTextLimit || !capped.Truncated {
		t.Fatalf("the limit was not capped at %d: len=%d truncated=%v", MaxTextLimit, len(capped.Text), capped.Truncated)
	}
}

// Decision: a durable entry is paged without loading the whole file, and a negative
// offset on it still aligns to a line (the look-back reads one byte of the file).
func TestTextReadsADurableEntryInPlace(t *testing.T) {
	root := t.TempDir()
	s := newDurableStore(t, root)
	d, err := s.CreateDurable("ws-1", "op-1", DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	content := "one\ntwo\nthree\n"
	if _, err := d.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(d.Ref().Ref)
	if err != nil {
		t.Fatal(err)
	}
	if e.File == "" || e.Data != nil {
		t.Fatalf("a durable entry must be read from its file, not memory: %+v", e)
	}
	p, err := ReadText(e, -6, 0) // lands inside "three"
	if err != nil {
		t.Fatal(err)
	}
	if p.Offset != 8 || p.Text != "three\n" || !p.Growing {
		t.Fatalf("page = %+v", p)
	}
}

// Decision (step 4 review B1, B2): a reader that follows nextOffset always reaches
// size. Once the producer has closed the artifact its unterminated last line is
// returned (a cap or a cancel can stop mid-line), while a growing artifact still holds
// it back; and the smallest limit still advances over an over-long multibyte line.
func TestTextReaderFollowingNextOffsetReachesTheEnd(t *testing.T) {
	growing := Entry{Data: []byte("aaa\nbbbb"), Size: 8, Growing: true}
	if p, _ := ReadText(growing, 4, 0); p.Text != "" || p.NextOffset != 4 {
		t.Fatalf("growing: the partial line must be held back: %+v", p)
	}
	for _, c := range []struct {
		data  string
		limit int
	}{
		{"aaa\nbbbbbbbbbb", 0},
		{"aaa\nbbbbbbbbbb", 5},
		{strings.Repeat("é", 100), MinTextLimit},
		{strings.Repeat("中", 100) + "\nend", MinTextLimit},
		{strings.Repeat("😀", 50), 1}, // below the minimum: raised to it
	} {
		e := text(c.data)
		var got strings.Builder
		off := int64(0)
		for i := 0; off < e.Size; i++ {
			if i > len(c.data) {
				t.Fatalf("%q limit %d: stuck at %d", c.data, c.limit, off)
			}
			p, err := ReadText(e, off, c.limit)
			if err != nil {
				t.Fatal(err)
			}
			if p.NextOffset <= off {
				t.Fatalf("%q limit %d: nextOffset %d did not advance from %d", c.data, c.limit, p.NextOffset, off)
			}
			if !utf8.ValidString(p.Text) {
				t.Fatalf("%q limit %d: split a rune: %q", c.data, c.limit, p.Text)
			}
			got.WriteString(p.Text)
			off = p.NextOffset
		}
		if got.String() != c.data {
			t.Errorf("%q limit %d: read back %q", c.data, c.limit, got.String())
		}
	}
}

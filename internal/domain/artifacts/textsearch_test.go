package artifacts

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/textfilter"
)

func text(data string) artifact.Entry {
	return artifact.Entry{Data: []byte(data), Size: int64(len(data))}
}

func filterOf(t *testing.T, match ...string) *textfilter.Filter {
	t.Helper()
	f, err := textfilter.Compile(textfilter.Spec{Match: match})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// grepFormat prints lines the way grep -n prints them.
func grepFormat(lines []foundLine) string {
	var b strings.Builder
	for _, l := range lines {
		sep := ":"
		if l.Context {
			sep = "-"
		}
		fmt.Fprintf(&b, "%d%s%s\n", l.Line, sep, l.Text)
	}
	return b.String()
}

const grepFixture = "alpha\nerror: one\nbeta\ngamma\ndelta\nerror: two\nwarn\nerror: three\nomega\n"

// Decision (ADR-0136 §1): the search reads like grep -n -C: line numbers from the
// artifact's start, context lines marked apart, overlapping windows merged. The
// expectation is the output of `grep -n -C1 -E error` on the same text; grep's "--" group
// separator is not part of the result, the line numbers show the gap.
func TestSearchLikeGrep(t *testing.T) {
	const want = "1-alpha\n2:error: one\n3-beta\n5-delta\n6:error: two\n7-warn\n8:error: three\n9-omega\n"
	out, err := SearchText(context.Background(), text(grepFixture), SearchQuery{Filter: filterOf(t, "error"), Context: 1, MaxLines: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := grepFormat(out.Lines); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if out.Matched != 3 || out.Truncated || out.HasNext || out.Scanned != int64(len(grepFixture)) {
		t.Fatalf("counts: %+v", out)
	}

	// A continuation numbers lines from the artifact's start, not from its offset.
	off := int64(len("alpha\nerror: one\nbeta\ngamma\ndelta\n")) // the start of line 6
	out, err = SearchText(context.Background(), text(grepFixture), SearchQuery{Filter: filterOf(t, "error"), Offset: off})
	if err != nil {
		t.Fatal(err)
	}
	if got := grepFormat(out.Lines); got != "6:error: two\n8:error: three\n" {
		t.Fatalf("from an offset: %q", got)
	}
}

// Decision (ADR-0136 §1): an offset that is not a line start is refused, not matched
// from the middle of a line.
func TestSearchOffsetMustBeALineStart(t *testing.T) {
	_, err := SearchText(context.Background(), text(grepFixture), SearchQuery{Filter: filterOf(t, "error"), Offset: 3})
	if err != ErrSearchOffset {
		t.Fatalf("mid-line offset: %v", err)
	}
	if _, err := SearchText(context.Background(), text(grepFixture), SearchQuery{Filter: filterOf(t, "error"), Offset: 1 << 20}); err != ErrSearchOffset {
		t.Fatalf("offset past the end: %v", err)
	}
}

// Decision (ADR-0136 §1): a 32 MiB console is searched in calls of at most 8 MiB, the
// next one continuing at nextOffset with the true line numbers, and a call never outruns
// the time budget: a budget of zero stops it at its first check.
func TestSearchLargeDurableConsole(t *testing.T) {
	store, err := artifact.New(artifact.Options{Epoch: "d-test", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d, err := store.CreateDurable("ws-1", "op-1", artifact.DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	const lineLen = 64
	const needleLine = 17*(1<<20)/lineLen + 1 // 1-based, past 16 MiB
	var chunk strings.Builder
	for n := 1; n <= 20*(1<<20)/lineLen; n++ {
		body := fmt.Sprintf("%07d filler", n)
		if n == needleLine {
			body = fmt.Sprintf("%07d NEEDLE", n)
		}
		chunk.WriteString(body + strings.Repeat(".", lineLen-1-len(body)) + "\n")
		if chunk.Len() >= 1<<20 {
			if _, err := d.Write([]byte(chunk.String())); err != nil {
				t.Fatal(err)
			}
			chunk.Reset()
		}
	}
	d.Write([]byte(chunk.String()))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	e, err := store.Get(d.Ref().Ref)
	if err != nil {
		t.Fatal(err)
	}
	if e.Size != 20<<20 {
		t.Fatalf("fixture size %d", e.Size)
	}

	var offset int64
	calls := 0
	for {
		calls++
		out, err := SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "NEEDLE"), Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if out.Scanned > SearchScanBytes+lineLen {
			t.Fatalf("call %d scanned %d bytes, past the 8 MiB bound", calls, out.Scanned)
		}
		if out.Matched > 0 {
			if len(out.Lines) != 1 || out.Lines[0].Line != needleLine || !strings.Contains(out.Lines[0].Text, "NEEDLE") {
				t.Fatalf("found %+v, want line %d", out.Lines, needleLine)
			}
			break
		}
		if !out.HasNext || out.NextLine != int(out.NextOffset/lineLen)+1 {
			t.Fatalf("call %d: %+v", calls, out)
		}
		offset = out.NextOffset
	}
	if calls != 3 {
		t.Fatalf("found in %d calls; the needle is at 17 MiB of 8 MiB calls", calls)
	}

	zero := time.Duration(0)
	out, err := SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "NEEDLE"), Budget: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scanned != lineLen || out.NextOffset != lineLen || !out.HasNext || out.NextLine != 2 {
		t.Fatalf("a spent budget must stop at the first check with a way on: %+v", out)
	}
}

// Decision (ADR-0012 §Reading artifacts, ADR-0136 §1): a growing artifact's unterminated
// last line is held back, nextOffset names its start, and once the producer closes it the
// line is searched.
func TestSearchGrowingHoldsThePartialLine(t *testing.T) {
	store, err := artifact.New(artifact.Options{Epoch: "d-test", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d, err := store.CreateDurable("ws-1", "op-1", artifact.DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	d.Write([]byte("hit one\nmiss\nhit par"))
	e, _ := store.Get(d.Ref().Ref)
	out, err := SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "hit")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Matched != 1 || !out.Growing || !out.HasNext || out.NextOffset != int64(len("hit one\nmiss\n")) {
		t.Fatalf("growing: %+v", out)
	}
	d.Write([]byte("tial\n"))
	e, _ = store.Get(d.Ref().Ref)
	out, err = SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "hit"), Offset: out.NextOffset})
	if err != nil {
		t.Fatal(err)
	}
	if got := grepFormat(out.Lines); got != "3:hit partial\n" {
		t.Fatalf("after the newline arrived: %q", got)
	}
	// A closed artifact's unterminated end is a line.
	out, _ = SearchText(context.Background(), text("a\nhit end"), SearchQuery{Filter: filterOf(t, "hit")})
	if grepFormat(out.Lines) != "2:hit end\n" {
		t.Fatalf("closed: %+v", out)
	}
}

// Decision (ADR-0136 §1): keep newest drops the earliest groups, oldest the latest, and
// truncated says the cap dropped lines.
func TestSearchKeepNewest(t *testing.T) {
	data := text("m1\nx\nm2\nx\nm3\nx\nm4\n")
	for _, c := range []struct {
		keep textfilter.Keep
		want string
	}{
		{textfilter.Newest, "5:m3\n7:m4\n"},
		{textfilter.Oldest, "1:m1\n3:m2\n"},
	} {
		out, err := SearchText(context.Background(), data, SearchQuery{Filter: filterOf(t, "^m"), MaxLines: 2, Keep: c.keep})
		if err != nil {
			t.Fatal(err)
		}
		if got := grepFormat(out.Lines); got != c.want || !out.Truncated || out.Matched != 4 {
			t.Errorf("keep %v: %q truncated=%v matched=%d, want %q", c.keep, got, out.Truncated, out.Matched, c.want)
		}
	}
}

// Decision (ADR-0136 §1): a line longer than 64 KiB is matched on its first 64 KiB, and
// a returned line is cut at maxLineBytes on a UTF-8 boundary, marked cut.
func TestSearchLongLines(t *testing.T) {
	long := strings.Repeat("a", SearchLineBytes) + "TAIL"
	e := text("short\n" + long + "\nécole\n")
	out, err := SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "TAIL")})
	if err != nil || out.Matched != 0 {
		t.Fatalf("a match past 64 KiB must not count: %+v %v", out, err)
	}
	out, _ = SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "^aaa"), MaxLineBytes: 20})
	if len(out.Lines) != 1 || len(out.Lines[0].Text) != 20 || !out.Lines[0].Cut || out.Lines[0].Line != 2 {
		t.Fatalf("long line: %d lines, %+v", len(out.Lines), out.Lines)
	}
	out, _ = SearchText(context.Background(), e, SearchQuery{Filter: filterOf(t, "cole"), MaxLineBytes: 1})
	if len(out.Lines) != 1 || out.Lines[0].Text != "" || !out.Lines[0].Cut || out.Lines[0].Line != 3 {
		t.Fatalf("a cut inside a rune must back off to the boundary: %+v", out.Lines)
	}
}

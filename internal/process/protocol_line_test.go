package process

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// Decision (m1g step 1 fix round; step 5 review B3): a whole line is returned without
// its newline; a line over the bound is consumed and reported over; a last line with
// no newline before EOF (a process killed mid-write) is dropped, never delivered.
// Seen failing with bufio.Reader.ReadLine, which returns the unterminated chunk.
func TestReadProtocolLineDropsAnUnterminatedLastLine(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("{\"a\":1}\r\n"+strings.Repeat("x", 40)+"\n{\"ok\":2}\n{\"trunc"), 16)
	if line, over, err := readProtocolLine(br, 32); line != `{"a":1}` || over || err != nil {
		t.Fatalf("first: %q over=%v err=%v", line, over, err)
	}
	if line, over, err := readProtocolLine(br, 32); line != "" || !over || err != nil {
		t.Fatalf("over-long: %q over=%v err=%v", line, over, err)
	}
	if line, over, err := readProtocolLine(br, 32); line != `{"ok":2}` || over || err != nil {
		t.Fatalf("after the long line: %q over=%v err=%v", line, over, err)
	}
	if line, _, err := readProtocolLine(br, 32); line != "" || err != io.EOF {
		t.Fatalf("unterminated last line delivered: %q err=%v", line, err)
	}
}

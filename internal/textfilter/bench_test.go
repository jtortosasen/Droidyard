package textfilter

import (
	"fmt"
	"math/rand/v2"
	"regexp/syntax"
	"strings"
	"sync"
	"testing"
)

// BenchmarkCost measures what a caller's patterns cost per instruction, which sets
// MaxCallInsts and FilterBudget (plan m3i step 2, ADR-0135 §2). Run it once, by hand:
//
//	go test -run XXX -bench Cost -benchtime 1x ./internal/textfilter
//
// The text is 32 MiB of 200-byte lines that no pattern matches, so every line is scanned
// to its end: the worst case for a filter, since a match ends a line's pass early. The
// patterns go through Compile and Filter.Keep, as a caller's do: up to 20 of them in one
// list, each an alternation of variants of a shape, until the call's programs reach the
// target instructions. The shapes:
//
//   - hard1: a 12-way alternation of four-letter literals, the stdlib's Hard1 shape
//     (literals end in X, Y or Z, which the text never contains, in either case);
//   - nolit: [a-z]{0,40}x, which has no literal prefix to skip ahead on;
//   - each of them under (?i).
//
// The metric is the time for one MiB per thousand compiled instructions.
func BenchmarkCost(b *testing.B) {
	text := costText(32<<20, 200)
	for _, shape := range []string{"hard1", "nolit"} {
		for _, fold := range []bool{false, true} {
			for _, target := range []int{100, 1000, MaxCallInsts} {
				name := fmt.Sprintf("%s/fold=%v/insts=%d", shape, fold, target)
				b.Run(name, func(b *testing.B) { benchCost(b, text, shape, fold, target) })
			}
		}
	}
}

// BenchmarkCostLongLines is the same on 32 KiB lines (8 MiB of them): the largest subject
// a log read admits is one item, and long ones leave the fast backtracker for the NFA.
func BenchmarkCostLongLines(b *testing.B) {
	text := costText(8<<20, 32<<10)
	for _, shape := range []string{"hard1", "nolit"} {
		b.Run(shape+"/fold=true/insts="+fmt.Sprint(MaxCallInsts), func(b *testing.B) {
			benchCost(b, text, shape, true, MaxCallInsts)
		})
	}
}

func benchCost(b *testing.B, text []string, shape string, fold bool, target int) {
	patterns, insts := costPatterns(shape, fold, target)
	f, err := Compile(Spec{Match: patterns})
	if err != nil {
		b.Fatal(err)
	}
	bytes := 0
	for _, l := range text {
		bytes += len(l)
	}
	b.SetBytes(int64(bytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range text {
			if f.Keep(l) {
				b.Fatal("a benchmark line matched; the text must never match")
			}
		}
	}
	b.StopTimer()
	perMiB := float64(b.Elapsed().Nanoseconds()) / float64(b.N) / (float64(bytes) / (1 << 20))
	b.ReportMetric(float64(insts), "insts")
	b.ReportMetric(float64(len(patterns)), "patterns")
	b.ReportMetric(perMiB/(float64(insts)/1000), "ns/MiB/1000insts")
}

var (
	texts     = map[[2]int][]string{}
	textMutex sync.Mutex
)

// costText is total bytes of lines of the given length, from an alphabet without x, y or
// z in either case, so no benchmark pattern can match. Built once per size.
func costText(total, lineLen int) []string {
	key := [2]int{total, lineLen}
	textMutex.Lock()
	defer textMutex.Unlock()
	if v, ok := texts[key]; ok {
		return v
	}
	const alphabet = "abcdefghijklmnopqrstuvw0123456789  ::_./"
	rng := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, total)
	for i := range buf {
		buf[i] = alphabet[rng.IntN(len(alphabet))]
	}
	all := string(buf)
	lines := make([]string, 0, total/lineLen)
	for i := 0; i+lineLen <= len(all); i += lineLen {
		lines = append(lines, all[i:i+lineLen])
	}
	texts[key] = lines
	return lines
}

// costPatterns builds up to MaxPatterns patterns of the shape whose compiled programs
// total at least target instructions (the last variant may overshoot), and returns the
// patterns and the instructions actually used.
func costPatterns(shape string, fold bool, target int) (patterns []string, insts int) {
	chunk := (target + MaxPatterns - 1) / MaxPatterns
	rng := rand.New(rand.NewPCG(3, 4))
	var cur []string
	curInsts := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		p := strings.Join(cur, "|")
		if fold {
			p = "(?i)" + p
		}
		n := programSize(p)
		patterns = append(patterns, p)
		insts += n
		cur, curInsts = nil, 0
	}
	for i := 0; insts+curInsts < target; i++ {
		v := costVariant(shape, i, rng)
		size := programSize(strings.Join(append(cur[:len(cur):len(cur)], v), "|"))
		if insts+size > MaxCallInsts { // keep the call inside its own budget
			break
		}
		cur, curInsts = append(cur, v), size
		if curInsts >= chunk {
			flush()
		}
	}
	flush()
	return patterns, insts
}

func costVariant(shape string, i int, rng *rand.Rand) string {
	if shape == "nolit" {
		if i == 0 {
			return "[a-z]{0,40}x"
		}
		// A class that differs per variant, so the parser cannot factor the copies.
		skip1, skip2 := byte('a'+i%23), byte('a'+(i/23)%23)
		var class strings.Builder
		for c := byte('a'); c <= 'z'; c++ {
			if c != skip1 && c != skip2 {
				class.WriteByte(c)
			}
		}
		return "[" + class.String() + "]{0,40}x"
	}
	// hard1: twelve four-letter literals, the last letter X, Y or Z.
	lits := make([]string, 12)
	for j := range lits {
		lits[j] = string([]byte{
			byte('A' + rng.IntN(23)), byte('A' + rng.IntN(23)), byte('A' + rng.IntN(23)),
			byte('X' + rng.IntN(3)),
		})
	}
	return strings.Join(lits, "|")
}

func programSize(p string) int {
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		panic(err)
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		panic(err)
	}
	return len(prog.Inst)
}

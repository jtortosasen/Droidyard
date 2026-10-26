package screen

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// touchHex is the golden touch message of step 1's spike: type 2, action, pointer -2,
// x, y, the frame's width and height, pressure, and two zero button words.
func touchHex(action string, x, y, w, h uint32, pressure string) string {
	u32 := func(v uint32) string {
		return hex.EncodeToString([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	u16 := func(v uint32) string { return hex.EncodeToString([]byte{byte(v >> 8), byte(v)}) }
	return "02" + action + "fffffffffffffffe" + u32(x) + u32(y) + u16(w) + u16(h) + pressure + "00000000" + "00000000"
}

// Decision: the bytes are the ones that landed on a device. The spike's golden messages
// are the vectors, with the pointer id the spike tool wrote, ^uint64(1) = -2, scrcpy's
// generic finger: fffffffffffffffe, as the research note §(d) records (its first draft
// mistyped it as -1, scrcpy's mouse id).
func TestEncodeControl(t *testing.T) {
	check := func(t *testing.T, in Input, size Size, want ...struct {
		at  time.Duration
		hex string
	}) {
		t.Helper()
		got, err := EncodeControl(in, size)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%d messages, want %d: %v", len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].At != w.at || hex.EncodeToString(got[i].Data) != strings.ReplaceAll(w.hex, " ", "") {
				t.Errorf("message %d: at %v % x, want at %v %s", i, got[i].At, got[i].Data, w.at, w.hex)
			}
		}
	}
	type msg = struct {
		at  time.Duration
		hex string
	}

	t.Run("the spike's golden tap, 590x1280 at (295, 331)", func(t *testing.T) {
		// 295/590 = 0.5, 331/1280 = 0.2586: how the person's pointer lands on the pixel.
		in := Tap{X: 295.0 / 590, Y: 331.0 / 1280}
		check(t, in, Size{590, 1280},
			msg{0, "02 00 fffffffffffffffe 00000127 0000014b 024e 0500 ffff 00000000 00000000"},
			msg{0, "02 01 fffffffffffffffe 00000127 0000014b 024e 0500 0000 00000000 00000000"})
	})
	t.Run("Tap{0.5, 0.25} on 720x1280 is (360, 320)", func(t *testing.T) {
		check(t, Tap{X: 0.5, Y: 0.25}, Size{720, 1280},
			msg{0, touchHex("00", 360, 320, 720, 1280, "ffff")},
			msg{0, touchHex("01", 360, 320, 720, 1280, "0000")})
	})
	t.Run("on 1280x720 it is (640, 180)", func(t *testing.T) {
		check(t, Tap{X: 0.5, Y: 0.25}, Size{1280, 720},
			msg{0, touchHex("00", 640, 180, 1280, 720, "ffff")},
			msg{0, touchHex("01", 640, 180, 1280, 720, "0000")})
	})
	t.Run("a long press lifts after Hold", func(t *testing.T) {
		check(t, Tap{X: 0.5, Y: 0.25, Hold: 800 * time.Millisecond}, Size{720, 1280},
			msg{0, touchHex("00", 360, 320, 720, 1280, "ffff")},
			msg{800 * time.Millisecond, touchHex("01", 360, 320, 720, 1280, "0000")})
	})
	t.Run("a swipe follows its points' T, with a move between two points 32 ms apart", func(t *testing.T) {
		// The points the caller gave are kept at their T; each 32 ms segment gets one move
		// halfway, so no gap is over 16 ms (plan m3g step 1 (a)).
		in := Swipe{Points: []Point{
			{X: 0.1, Y: 0.2, T: 0},
			{X: 0.5, Y: 0.5, T: 32 * time.Millisecond},
			{X: 0.9, Y: 0.8, T: 64 * time.Millisecond},
		}}
		check(t, in, Size{1000, 2000},
			msg{0, touchHex("00", 100, 400, 1000, 2000, "ffff")},
			msg{16 * time.Millisecond, touchHex("02", 300, 700, 1000, 2000, "ffff")},
			msg{32 * time.Millisecond, touchHex("02", 500, 1000, 1000, 2000, "ffff")},
			msg{48 * time.Millisecond, touchHex("02", 700, 1300, 1000, 2000, "ffff")},
			msg{64 * time.Millisecond, touchHex("02", 900, 1600, 1000, 2000, "ffff")},
			msg{64 * time.Millisecond, touchHex("01", 900, 1600, 1000, 2000, "0000")})
	})
	t.Run("two points within 16 ms get no move between them", func(t *testing.T) {
		in := Swipe{Points: []Point{{X: 0.1, Y: 0.2, T: 0}, {X: 0.5, Y: 0.5, T: 16 * time.Millisecond}}}
		check(t, in, Size{1000, 2000},
			msg{0, touchHex("00", 100, 400, 1000, 2000, "ffff")},
			msg{16 * time.Millisecond, touchHex("02", 500, 1000, 1000, 2000, "ffff")},
			msg{16 * time.Millisecond, touchHex("01", 500, 1000, 1000, 2000, "0000")})
	})
	t.Run("a swipe may start late", func(t *testing.T) {
		in := Swipe{Points: []Point{{X: 0, Y: 0, T: 40 * time.Millisecond}, {X: 1, Y: 1, T: 40 * time.Millisecond}}}
		check(t, in, Size{100, 200},
			msg{40 * time.Millisecond, touchHex("00", 0, 0, 100, 200, "ffff")},
			msg{40 * time.Millisecond, touchHex("02", 99, 199, 100, 200, "ffff")},
			msg{40 * time.Millisecond, touchHex("01", 99, 199, 100, 200, "0000")})
	})
	t.Run("each key", func(t *testing.T) {
		for name, code := range map[string]string{"back": "04", "home": "03", "appSwitch": "bb", "enter": "42", "delete": "43"} {
			check(t, Key{Name: name}, Size{720, 1280},
				msg{0, "00 00 000000" + code + " 00000000 00000000"},
				msg{0, "00 01 000000" + code + " 00000000 00000000"})
		}
		check(t, Key{Name: "back"}, Size{1, 1},
			msg{0, "00 00 00000004 00000000 00000000"},
			msg{0, "00 01 00000004 00000000 00000000"})
	})
}

// Decision: a coordinate is the nearest pixel (half away from zero), and 1.0 is the
// last pixel, not one past it.
func TestEncodeControlRounding(t *testing.T) {
	for _, c := range []struct {
		v    float64
		n    int
		want int32
	}{
		{0, 100, 0}, {1, 100, 99}, {0.5, 100, 50}, {0.004, 100, 0}, {0.005, 100, 1},
		{0.25, 10, 3}, {0.35, 10, 4}, {0.999, 100, 99}, {1, 1, 0}, {0.5, 1, 0},
	} {
		got, err := axis(c.v, c.n)
		if err != nil || got != c.want {
			t.Errorf("axis(%v, %d) = %d, %v; want %d", c.v, c.n, got, err, c.want)
		}
	}
}

// Decision: an Input that cannot be encoded is an error before any byte is made, so a
// handler's bad value never reaches the device as a wrapped-around coordinate.
func TestEncodeControlRefuses(t *testing.T) {
	sz := Size{720, 1280}
	two := []Point{{0.1, 0.1, 0}, {0.2, 0.2, time.Second}}
	many := make([]Point, MaxSwipePoints+1)
	for _, c := range []struct {
		name string
		in   Input
		size Size
	}{
		{"NaN x", Tap{X: math.NaN()}, sz},
		{"NaN y", Tap{Y: math.NaN()}, sz},
		{"x below 0", Tap{X: -0.001}, sz},
		{"x above 1", Tap{X: 1.001}, sz},
		{"+Inf", Tap{X: math.Inf(1)}, sz},
		{"-Inf", Tap{Y: math.Inf(-1)}, sz},
		{"negative hold", Tap{X: 0.5, Y: 0.5, Hold: -1}, sz},
		{"hold over the limit", Tap{X: 0.5, Y: 0.5, Hold: MaxInputDuration + 1}, sz},
		{"one point", Swipe{Points: two[:1]}, sz},
		{"no points", Swipe{}, sz},
		{"too many points", Swipe{Points: many}, sz},
		{"T goes backwards", Swipe{Points: []Point{{0.1, 0.1, time.Second}, {0.2, 0.2, time.Second - 1}}}, sz},
		{"negative T", Swipe{Points: []Point{{0.1, 0.1, -1}, {0.2, 0.2, 5}}}, sz},
		{"T over the limit", Swipe{Points: []Point{{0.1, 0.1, 0}, {0.2, 0.2, MaxInputDuration + 1}}}, sz},
		{"a bad coordinate in the middle", Swipe{Points: []Point{{0.1, 0.1, 0}, {2, 0.2, 5}, {0.3, 0.3, 6}}}, sz},
		{"unknown key", Key{Name: "Back"}, sz},
		{"empty key", Key{}, sz},
		{"nil", nil, sz},
		{"a pointer is not an Input value", &Tap{X: 0.5, Y: 0.5}, sz},
		{"zero width", Tap{X: 0.5, Y: 0.5}, Size{0, 1280}},
		{"zero height", Key{Name: "back"}, Size{720, 0}},
		{"width over a u16", Tap{X: 0.5, Y: 0.5}, Size{MaxDimension + 1, 1280}},
		{"negative size", Tap{X: 0.5, Y: 0.5}, Size{-1, -1}},
	} {
		got, err := EncodeControl(c.in, c.size)
		if !errors.Is(err, ErrBadInput) || got != nil {
			t.Errorf("%s: %v, %v; want ErrBadInput and nothing", c.name, got, err)
		}
	}
	// The limits themselves are inputs.
	if _, err := EncodeControl(Tap{X: 0.5, Y: 0.5, Hold: MaxInputDuration}, sz); err != nil {
		t.Errorf("hold at the limit: %v", err)
	}
	if _, err := EncodeControl(Swipe{Points: make([]Point, MaxSwipePoints)}, sz); err != nil {
		t.Errorf("swipe at the point limit: %v", err)
	}
	// A frame as large as a u16 allows.
	got, err := EncodeControl(Tap{X: 1, Y: 1}, Size{MaxDimension, MaxDimension})
	if err != nil || got[0].Data[10] != 0 || got[0].Data[11] != 0 || got[0].Data[12] != 0xff || got[0].Data[13] != 0xfe {
		t.Errorf("65535 frame: % x %v", got, err)
	}
}

// Decision (plan m2y step 3; ADR-0117 §3): a Scroll that a call injects writes the very bytes
// the view's own wheel event does, so a replayed wheel (device.scroll) and a live one are the
// same message to the device; its amounts are notches, rounded to the wire's 1/256, and an
// amount past the bound of 16 notches or not a number is refused before a byte is made. Seen
// failing with the notch converted without the factor of 256: the amounts differed.
func TestEncodeControlScroll(t *testing.T) {
	size := Size{720, 1280}
	got, err := EncodeControl(Scroll{X: 0, Y: 1, DX: -2.5, DY: 16}, size)
	if err != nil || len(got) != 1 || got[0].At != 0 {
		t.Fatalf("EncodeControl(Scroll) = %v, %v; want one message at 0", got, err)
	}
	want, err := EncodeEvent(Event{Kind: EventScroll, X: 0, Y: 65535, H: -640, V: 4096}, size)
	if err != nil || string(got[0].Data) != string(want) {
		t.Errorf("the injected scroll is % x, the view's is % x (%v)", got[0].Data, want, err)
	}
	for name, in := range map[string]Scroll{
		"dy past the bound": {X: 0.5, Y: 0.5, DY: 16.01}, "dx past the bound": {X: 0.5, Y: 0.5, DX: -16.01},
		"NaN": {X: 0.5, Y: 0.5, DY: math.NaN()}, "x past the frame": {X: 1.5, Y: 0.5, DY: 1},
	} {
		if got, err := EncodeControl(in, size); !errors.Is(err, ErrBadInput) || got != nil {
			t.Errorf("%s: %v, %v; want ErrBadInput and nothing", name, got, err)
		}
	}
}

// Decision (plan m3g step 1 (a), Changes): a swipe delivered through an open view moves along
// straight segments with no gap over 16 ms, as the reader's gesture does, because a path of
// a few moves left the shade closed on API 36. A 300 ms drag from the top edge down the
// screen has a message at least every 16 ms from its first to its last, its times never go
// back, its positions run from the first point to the last without leaving the line, and
// the points given are among them at their times. Seen failing with the encoder writing one
// move per given point: the longest gap was 300 ms.
func TestEncodeControlSwipeMovesAtLeastEvery16ms(t *testing.T) {
	size := Size{1080, 2400}
	got, err := EncodeControl(Swipe{Points: []Point{{X: 0.5, Y: 0}, {X: 0.5, Y: 0.6, T: 300 * time.Millisecond}}}, size)
	if err != nil {
		t.Fatal(err)
	}
	var prevAt time.Duration
	var prevY int32 = -1
	for i, c := range got {
		if gap := c.At - prevAt; gap > 16*time.Millisecond || gap < 0 {
			t.Fatalf("message %d at %v after one at %v: a gap of %v", i, c.At, prevAt, gap)
		}
		prevAt = c.At
		y := int32(c.Data[14])<<24 | int32(c.Data[15])<<16 | int32(c.Data[16])<<8 | int32(c.Data[17])
		x := int32(c.Data[10])<<24 | int32(c.Data[11])<<16 | int32(c.Data[12])<<8 | int32(c.Data[13])
		if x != 540 || y < prevY {
			t.Fatalf("message %d is at (%d, %d) after y %d: the finger left the straight line down the middle", i, x, y, prevY)
		}
		prevY = y
	}
	first, last := got[0], got[len(got)-1]
	if first.At != 0 || first.Data[1] != actionDown || last.At != 300*time.Millisecond || last.Data[1] != actionUp || prevY != 1440 {
		t.Errorf("first %v action %d, last %v action %d at y %d; want down at 0 and up at 300ms at y 1440", first.At, first.Data[1], last.At, last.Data[1], prevY)
	}
	// 300/16 rounds up to 19 gaps: the given points and 18 moves between them, and the up.
	if len(got) != 2+18+1 {
		t.Errorf("%d messages, want 21", len(got))
	}
}

// Decision (plan m3g Q2, ADR-0133 §3): device.pressKey names twenty-two keys, each with
// Android's code (the literals are KeyEvent's, not the table read back), and a name outside
// the list is no key. The view's wire still carries five (control_test.go).
func TestKeyCodes(t *testing.T) {
	want := map[string]int{
		"back": 4, "home": 3, "appSwitch": 187, "enter": 66, "delete": 67,
		"power": 26, "wakeUp": 224, "sleep": 223, "volumeUp": 24, "volumeDown": 25, "volumeMute": 164,
		"menu": 82, "search": 84, "tab": 61, "escape": 111,
		"dpadUp": 19, "dpadDown": 20, "dpadLeft": 21, "dpadRight": 22, "dpadCenter": 23,
		"notification": 83, "assist": 219,
	}
	if got := KeyNames(); len(got) != len(want) {
		t.Errorf("%d key names %v, want %d", len(got), got, len(want))
	}
	for name, code := range want {
		if got, ok := KeyCode(name); !ok || got != code {
			t.Errorf("KeyCode(%q) = %d, %v; want %d", name, got, ok, code)
		}
		cs, err := EncodeControl(Key{Name: name}, Size{720, 1280})
		if err != nil || len(cs) != 2 || cs[0].Data[5] != byte(code) {
			t.Errorf("EncodeControl(Key{%q}) = %v, %v; want a down and an up of code %d", name, cs, err, code)
		}
	}
	for _, name := range []string{"", "Back", "reboot", "26"} {
		if _, ok := KeyCode(name); ok {
			t.Errorf("KeyCode(%q) is a key", name)
		}
	}
}

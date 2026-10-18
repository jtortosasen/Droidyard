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
	t.Run("a swipe follows its points' T", func(t *testing.T) {
		in := Swipe{Points: []Point{
			{X: 0.1, Y: 0.2, T: 0},
			{X: 0.5, Y: 0.5, T: 120 * time.Millisecond},
			{X: 0.9, Y: 0.8, T: 350 * time.Millisecond},
		}}
		check(t, in, Size{1000, 2000},
			msg{0, touchHex("00", 100, 400, 1000, 2000, "ffff")},
			msg{120 * time.Millisecond, touchHex("02", 500, 1000, 1000, 2000, "ffff")},
			msg{350 * time.Millisecond, touchHex("02", 900, 1600, 1000, 2000, "ffff")},
			msg{350 * time.Millisecond, touchHex("01", 900, 1600, 1000, 2000, "0000")})
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
		{"unknown key", Key{Name: "volumeUp"}, sz},
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

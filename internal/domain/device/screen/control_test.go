package screen

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var updateControlGolden = flag.Bool("update-control-golden", false, "rewrite testdata/control-wire.json")

// Decision (ADR-0117 §3): ParseControl accepts exactly the wire, a type byte and its fixed
// length, and nothing else, so the bytes a browser sends can reach the device only as an
// Event the daemon encodes itself. Every type at every wrong length, an unknown type, an
// unknown action and key, an empty frame, and every one of scrcpy's own message types
// (0..17) at the length scrcpy gives it, are refused with ErrBadControl. Seen failing with
// the length check of the scroll frame removed (a 10-byte scroll was accepted).
func TestParseControlRefusesWhatIsNotTheWire(t *testing.T) {
	good := map[string][]byte{
		"touch":  {1, 0, 0, 0, 0, 0},
		"key":    {2, 1},
		"scroll": {3, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	for name, frame := range good {
		if _, err := ParseControl(frame); err != nil {
			t.Fatalf("a good %s frame was refused: %v", name, err)
		}
		for n := 0; n <= 40; n++ {
			if n == len(frame) {
				continue
			}
			b := make([]byte, n)
			copy(b, frame)
			if n > 0 {
				b[0] = frame[0]
			}
			if _, err := ParseControl(b); !errors.Is(err, ErrBadControl) {
				t.Errorf("a %s frame of %d bytes: %v, want ErrBadControl", name, n, err)
			}
		}
	}
	refused := map[string][]byte{
		"empty":                 {},
		"nil":                   nil,
		"type 0":                {0, 0, 0, 0, 0, 0},
		"type 4":                {4, 0, 0, 0, 0, 0},
		"type 255":              {255, 0, 0, 0, 0, 0},
		"touch action 4":        {1, 4, 0, 0, 0, 0},
		"touch action 255":      {1, 255, 0, 0, 0, 0},
		"key 0":                 {2, 0},
		"key 6":                 {2, 6},
		"key 255":               {2, 255},
		"a JSON object":         []byte(`{"type":"tap","x":0.5,"y":0.5}`),
		"scrcpy's inject touch": append([]byte{2, 0}, make([]byte, 30)...),
	}
	for name, frame := range refused {
		if _, err := ParseControl(frame); !errors.Is(err, ErrBadControl) {
			t.Errorf("%s: %v, want ErrBadControl", name, err)
		}
	}
	// scrcpy's message types, each at the length scrcpy's own message has: none is a frame
	// of the wire, whatever its type byte.
	for typ := 0; typ <= 17; typ++ {
		for _, n := range []int{1, 2, 14, 21, 32} {
			b := make([]byte, n)
			b[0] = byte(typ)
			ev, err := ParseControl(b)
			if (typ == 1 && n == 6) || (typ == 3 && n == 9) {
				continue // a frame of the wire, which has its own meaning, not scrcpy's
			}
			if err == nil {
				t.Errorf("type %d at %d bytes was accepted as %+v", typ, n, ev)
			}
		}
	}
}

// wireOf is the canonical frame of an Event, the inverse of ParseControl.
func wireOf(ev Event) []byte {
	be := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	switch ev.Kind {
	case EventTouch:
		return append(append([]byte{1, byte(ev.Action)}, be(ev.X)...), be(ev.Y)...)
	case EventKey:
		for i, name := range wireKeys {
			if name == ev.Key && i > 0 {
				return []byte{2, byte(i)}
			}
		}
	case EventScroll:
		b := append([]byte{3}, be(ev.X)...)
		b = append(b, be(ev.Y)...)
		b = append(b, be(uint16(ev.H))...)
		return append(b, be(uint16(ev.V))...)
	}
	return nil
}

// Decision (ADR-0117 §3, step 5): what a client daemon sends its host is the wire the host's
// ParseControl reads: EncodeWire is the inverse of ParseControl for every touch action, key and
// signed scroll amount, equal to the test's own reading of the format (wireOf), and refuses
// what is not on the wire (a release, an unknown key, action or kind). Seen failing with the
// scroll's axes little endian: the round trip returned another event.
func TestEncodeWireIsTheInverseOfParseControl(t *testing.T) {
	events := []Event{
		{Kind: EventTouch, Action: TouchDown, X: 1, Y: 65535},
		{Kind: EventTouch, Action: TouchUp, X: 0x1234, Y: 0xfedc},
		{Kind: EventTouch, Action: TouchMove, X: 300, Y: 20000},
		{Kind: EventTouch, Action: TouchCancel, X: 65535, Y: 0},
		{Kind: EventKey, Key: "back"}, {Kind: EventKey, Key: "home"}, {Kind: EventKey, Key: "appSwitch"},
		{Kind: EventKey, Key: "enter"}, {Kind: EventKey, Key: "delete"},
		{Kind: EventScroll, X: 0x0102, Y: 0x0304, H: 0x0506, V: -256},
		{Kind: EventScroll, X: 7, Y: 8, H: -4096, V: 4096},
	}
	for _, ev := range events {
		b, err := EncodeWire(ev)
		if err != nil {
			t.Fatalf("%+v: %v", ev, err)
		}
		if want := wireOf(ev); !bytes.Equal(b, want) {
			t.Errorf("%+v: % x, want % x", ev, b, want)
		}
		if back, err := ParseControl(b); err != nil || back != ev {
			t.Errorf("%+v came back as %+v (%v)", ev, back, err)
		}
	}
	for _, ev := range []Event{
		{Kind: EventRelease, X: 1, Y: 2}, {Kind: EventKey, Key: "power"}, {Kind: EventKey},
		{Kind: EventTouch, Action: 4}, {},
	} {
		if b, err := EncodeWire(ev); !errors.Is(err, ErrBadControl) {
			t.Errorf("%+v encoded as % x (%v), want ErrBadControl", ev, b, err)
		}
	}
}

// FuzzParseControl: no byte string panics ParseControl or EncodeEvent; a frame it accepts
// is exactly the canonical frame of the Event it returned (so there is one way to say a
// thing), and EncodeEvent gives it a 32, 28 or 21 byte answer for any frame size.
func FuzzParseControl(f *testing.F) {
	for _, s := range [][]byte{
		{}, {1, 0, 0, 0, 0, 0}, {1, 3, 255, 255, 255, 255}, {2, 5}, {3, 0x80, 0, 0x80, 0, 0x10, 0, 0xf0, 0},
		{3, 0, 0, 0, 0, 0x7f, 0xff, 0x80, 0}, {2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		f.Add(s, 1080, 2400)
	}
	f.Fuzz(func(t *testing.T, b []byte, w, h int) {
		ev, err := ParseControl(b)
		if err != nil {
			if !errors.Is(err, ErrBadControl) {
				t.Fatalf("an error that is not ErrBadControl: %v", err)
			}
			return
		}
		if !bytes.Equal(wireOf(ev), b) {
			t.Fatalf("% x parsed as %+v, whose frame is % x", b, ev, wireOf(ev))
		}
		out, err := EncodeEvent(ev, Size{w, h})
		if err != nil {
			if !errors.Is(err, ErrBadControl) {
				t.Fatalf("EncodeEvent: an error that is not ErrBadControl: %v", err)
			}
			return
		}
		if n := len(out); n != 32 && n != 28 && n != 21 {
			t.Fatalf("EncodeEvent gave %d bytes", n)
		}
	})
}

// goldenCase is one line of testdata/control-wire.json: a frame of the wire, the Event it
// means, and the scrcpy bytes it becomes against Size. Step 4's Vitest reads the same file,
// so the browser's encoder of the wire meets the daemon's bytes.
type goldenCase struct {
	Name   string      `json:"name"`
	Wire   string      `json:"wire"`
	Event  goldenEvent `json:"event"`
	Size   goldenSize  `json:"size"`
	Scrcpy string      `json:"scrcpy"`
}

type goldenEvent struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	X      uint16 `json:"x"`
	Y      uint16 `json:"y"`
	Key    string `json:"key"`
	H      int16  `json:"h"`
	V      int16  `json:"v"`
}

type goldenSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type goldenFile struct {
	Note  string       `json:"note"`
	Cases []goldenCase `json:"cases"`
}

var actionNames = [...]string{"down", "up", "move", "cancel"}

// goldenCases builds the golden from the step 1 spike's encoders (spike_test.go), which were
// written from scrcpy v4.1's source and are not EncodeEvent's code, with floating point
// arithmetic where EncodeEvent uses integers.
func goldenCases() []goldenCase {
	px := func(v uint16, n int) int32 {
		return int32(math.Min(math.Round(float64(v)/65535*float64(n)), float64(n-1)))
	}
	var out []goldenCase
	add := func(name string, ev Event, size Size, scrcpy []byte) {
		ge := goldenEvent{X: ev.X, Y: ev.Y, Key: ev.Key, H: ev.H, V: ev.V}
		switch ev.Kind {
		case EventTouch:
			ge.Kind, ge.Action = "touch", actionNames[ev.Action]
		case EventKey:
			ge.Kind = "key"
		case EventScroll:
			ge.Kind = "scroll"
		}
		out = append(out, goldenCase{Name: name, Wire: hex.EncodeToString(wireOf(ev)), Event: ge,
			Size: goldenSize{size.Width, size.Height}, Scrcpy: hex.EncodeToString(scrcpy)})
	}
	phone, small := Size{1080, 2400}, Size{590, 1280}
	touch := func(name string, a TouchAction, x, y uint16, size Size) {
		pressure := uint16(0xffff)
		if a == TouchUp || a == TouchCancel {
			pressure = 0
		}
		add(name, Event{Kind: EventTouch, Action: a, X: x, Y: y}, size,
			spikeTouch(byte(a), px(x, size.Width), px(y, size.Height), uint16(size.Width), uint16(size.Height), pressure))
	}
	touch("down at the top left", TouchDown, 0, 0, phone)
	touch("down at the bottom right clamps to the last pixel", TouchDown, 65535, 65535, phone)
	touch("move to the middle", TouchMove, 32768, 16384, phone)
	touch("up", TouchUp, 1000, 2000, phone)
	touch("cancel", TouchCancel, 65535, 0, phone)
	touch("down on another size", TouchDown, 0x8000, 0x8000, small)
	for i, code := range []int32{4, 3, 187, 66, 67} {
		name := wireKeys[i+1]
		add("key "+name, Event{Kind: EventKey, Key: name}, phone,
			append(spikeKey(0, code), spikeKey(1, code)...))
	}
	scroll := func(name string, x, y uint16, h, v int16, size Size) {
		add(name, Event{Kind: EventScroll, X: x, Y: y, H: h, V: v}, size,
			spikeScroll(px(x, size.Width), px(y, size.Height), uint16(size.Width), uint16(size.Height), float32(h)/256, float32(v)/256))
	}
	scroll("scroll one notch", 32768, 32768, 0, 256, phone)
	scroll("scroll left and down", 0, 0, -256, -512, phone)
	scroll("scroll at the bound saturates", 100, 200, 4096, -4096, small)
	scroll("scroll past the bound is the bound", 100, 200, 32767, -32768, small)
	scroll("scroll of nothing", 5, 6, 0, 0, small)
	return out
}

// Decision (ADR-0117 §3, §5): a frame of the wire means one Event and one set of scrcpy
// bytes, and the golden pins all three: the touch actions with their pressure, the key's
// down and up pair, the scroll's axes as notches/16 times 0x8000 with +1.0 as 0x7fff
// (step 1's S1), and a cancel as the touch message with action 3 (S4). The golden's scrcpy
// bytes come from the spike's encoders; this test requires ParseControl and EncodeEvent
// to meet them. Seen failing with the scroll's axis not saturated (+16 notches wrote
// 0x8000, which is -1.0, the opposite way).
func TestEncodeEventGolden(t *testing.T) {
	want, err := json.MarshalIndent(goldenFile{
		Note:  "ADR-0117 §3: a frame of the input wire, the Event it means, and the scrcpy bytes the daemon writes for it against size. Generated from the spike's encoders by go test ./internal/domain/device/screen -update-control-golden; read by the Go test and by the web client's Vitest.",
		Cases: goldenCases(),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("testdata", "control-wire.json")
	if *updateControlGolden {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not what the spike's encoders give; rerun with -update-control-golden if the wire changed on purpose", path)
	}

	var file goldenFile
	if err := json.Unmarshal(got, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		wire, _ := hex.DecodeString(c.Wire)
		ev, err := ParseControl(wire)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		scrcpy, err := EncodeEvent(ev, Size{c.Size.Width, c.Size.Height})
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if hex.EncodeToString(scrcpy) != c.Scrcpy {
			t.Errorf("%s: EncodeEvent gave %x, the golden has %s", c.Name, scrcpy, c.Scrcpy)
		}
	}
}

// Decision (ADR-0117 §3): EncodeEvent builds only the messages of the wire's three kinds. A
// release (a stream's own word to its source), an unknown kind, action or key, and a frame
// size that is not a frame's are errors, never a message with garbage in it.
func TestEncodeEventRefusesWhatItCannotEncode(t *testing.T) {
	ok := Size{100, 200}
	for name, tc := range map[string]struct {
		ev   Event
		size Size
	}{
		"a release":      {Event{Kind: EventRelease}, ok},
		"no kind":        {Event{}, ok},
		"kind 9":         {Event{Kind: 9}, ok},
		"touch action 4": {Event{Kind: EventTouch, Action: 4}, ok},
		"an unknown key": {Event{Kind: EventKey, Key: "power"}, ok},
		"no key":         {Event{Kind: EventKey}, ok},
		"zero width":     {Event{Kind: EventTouch}, Size{0, 200}},
		"negative":       {Event{Kind: EventTouch}, Size{100, -1}},
		"past u16":       {Event{Kind: EventScroll}, Size{MaxDimension + 1, 200}},
	} {
		if b, err := EncodeEvent(tc.ev, tc.size); !errors.Is(err, ErrBadControl) || b != nil {
			t.Errorf("%s: % x, %v; want ErrBadControl and nothing", name, b, err)
		}
	}
}

// Decision (ADR-0117 §3, plan m2y Q2): a queue sums a burst of wheel amounts into one, and
// the sum never carries more than the device applies: it saturates at 16 notches (4096 on
// the wire) in both directions, and an int16 overflow cannot wrap it to the other sign.
// Seen failing with the clamp removed (32767 + 32767 wrapped to -2).
func TestClampScrollSaturates(t *testing.T) {
	for _, tc := range []struct{ a, b, want int16 }{
		{256, 256, 512},
		{-256, 100, -156},
		{4000, 200, 4096},
		{-4000, -200, -4096},
		{32767, 32767, 4096},
		{-32768, -32768, -4096},
	} {
		if got := ClampScroll(tc.a, tc.b); got != tc.want {
			t.Errorf("ClampScroll(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// Decision (ADR-0117 §5): a lift the daemon writes on its own is the touch message with
// action 3 and no pressure, at the finger's last point, and carries the frame size now: a
// size that is not the video's is dropped by scrcpy, a lift included. The point is clamped
// into the new frame, and what is not a touch message is refused.
func TestCancelOfUsesTheNewSize(t *testing.T) {
	old := Size{1080, 2400}
	down, err := EncodeEvent(Event{Kind: EventTouch, Action: TouchMove, X: 65535, Y: 65535}, old)
	if err != nil {
		t.Fatal(err)
	}
	next := Size{2400, 1080}
	c, ok := CancelOf(down, next)
	if !ok {
		t.Fatal("no cancel")
	}
	want := spikeTouch(3, 1079, 1079, 2400, 1080, 0) // (1079, 2399) clamped into the new frame
	if !bytes.Equal(c, want) {
		t.Errorf("cancel % x, want % x", c, want)
	}
	for name, b := range map[string][]byte{"nil": nil, "a key": {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, "short": down[:31]} {
		if _, ok := CancelOf(b, next); ok {
			t.Errorf("%s: a cancel was made", name)
		}
	}
	if _, ok := CancelOf(down, Size{}); ok {
		t.Error("a cancel was made for no frame")
	}
}

package device

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var fixtureImages = []string{"pixel9a", "pixeltablet", "mediumphonea17"}

// parseHierarchy reads the first element of an XML file and requires it to be
// <hierarchy>, consuming it to its end. uiautomator's status line follows the
// document on /dev/tty, so bytes after the root are deliberately not read.
// It returns the root's rotation attribute.
func parseHierarchy(data []byte) (rotation int, err error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return 0, err // a truncated document ends here with an error
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local != "hierarchy" {
					return 0, io.ErrUnexpectedEOF
				}
				for _, a := range t.Attr {
					if a.Name.Local == "rotation" {
						rotation, _ = strconv.Atoi(a.Value)
					}
				}
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				return rotation, nil
			}
		}
	}
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Errorf("%s: %v", name, err)
	}
	return b
}

// checkRecordedFixtures asserts the step 1 decisions: (a) every image carries
// exactly one dev-tty label and a produced-document label has a fixture that
// parses; (c) every XML parses, every PNG decodes to wm size (swapped when the
// hierarchy's rotation is odd), the devices -l files list one `device` line per
// emulator, and the no-server stderr holds adb's "daemon not running" line.
func checkRecordedFixtures(t *testing.T, dir string) {
	recorded := string(read(t, dir, "RECORDED.txt"))
	for _, p := range fixtureImages {
		// (a) exactly one label per image; an omitted label fails.
		prefix := p + "-screencap.png dev-tty: "
		var labels []string
		for _, l := range strings.Split(recorded, "\n") {
			if strings.HasPrefix(l, prefix) {
				labels = append(labels, strings.TrimPrefix(l, prefix))
			}
		}
		if len(labels) != 1 {
			t.Errorf("%s: want exactly one dev-tty label, got %d", p, len(labels))
			continue
		}
		label := labels[0]
		switch {
		case label == "produced-document":
			if _, err := parseHierarchy(read(t, dir, p+"-uidump-devtty.xml")); err != nil {
				t.Errorf("%s: dev-tty marked produced-document but fixture does not parse: %v", p, err)
			}
		case strings.HasPrefix(label, "failed ("):
			t.Logf("%s: dev-tty FAILED on this image: %s (step 6 and Q1 reopen)", p, label)
		default:
			t.Errorf("%s: unrecognised dev-tty label %q", p, label)
		}

		// (c) the sdcard XML parses; the PNG decodes and matches wm size.
		if _, err := parseHierarchy(read(t, dir, p+"-uidump-sdcard.xml")); err != nil {
			t.Errorf("%s: sdcard xml: %v", p, err)
		}
		rot := 0
		if label == "produced-document" {
			rot, _ = parseHierarchy(read(t, dir, p+"-uidump-devtty.xml"))
		}
		m := regexp.MustCompile(`Physical size: (\d+)x(\d+)`).FindSubmatch(read(t, dir, p+"-wm-size.txt"))
		if m == nil {
			t.Errorf("%s: wm size not parsed", p)
			continue
		}
		w, _ := strconv.Atoi(string(m[1]))
		h, _ := strconv.Atoi(string(m[2]))
		if rot%2 == 1 {
			w, h = h, w
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(read(t, dir, p+"-screencap.png")))
		if err != nil {
			t.Errorf("%s: png: %v", p, err)
			continue
		}
		if _, err := png.Decode(bytes.NewReader(read(t, dir, p+"-screencap.png"))); err != nil {
			t.Errorf("%s: png does not decode fully: %v", p, err)
		}
		if cfg.Width != w || cfg.Height != h {
			t.Errorf("%s: png %dx%d, want %dx%d (wm size, rotation %d)", p, cfg.Width, cfg.Height, w, h, rot)
		}
	}

	// (c) devices -l: one `device` line per emulator.
	for file, want := range map[string]int{
		"devices-l-one-emulator-pixel9a.txt":              1,
		"devices-l-one-emulator-a17.txt":                  1,
		"devices-l-two-emulators-pixel9a-pixeltablet.txt": 2,
	} {
		n := 0
		for _, l := range strings.Split(string(read(t, dir, file)), "\n") {
			if f := strings.Fields(l); len(f) > 2 && strings.HasPrefix(f[0], "emulator-") && f[1] == "device" {
				n++
			}
		}
		if n != want {
			t.Errorf("%s: %d device lines, want %d", file, n, want)
		}
	}

	// (c) the isolated server's first start says so on stderr, never on stdout.
	if !strings.Contains(string(read(t, dir, "fork-5199-stderr.txt")), "daemon not running") {
		t.Error("fork-5199-stderr.txt lacks adb's \"daemon not running\" line")
	}
	if strings.Contains(string(read(t, dir, "fork-5199-stdout.txt")), "daemon not running") {
		t.Error("fork-5199-stdout.txt holds the daemon lines, which belong on stderr")
	}
	if !bytes.Equal(read(t, dir, "isolated-5037-before.txt"), read(t, dir, "isolated-5037-after.txt")) {
		t.Error("the shared server's listing changed across the fork observation")
	}
}

func TestRecordedFixtures(t *testing.T) { checkRecordedFixtures(t, "testdata") }

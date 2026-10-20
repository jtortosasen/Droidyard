package device

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device/devicetest"
)

// fullOneShotCall is the one-shot reader's run with the full dump's words (ADR-0110): the
// words are arguments of the class, after its name.
const fullOneShotCall = rdRunCall + " dump full"

// Decision (ADR-0110 Decision 5: never `uiautomator dump`): a full dump neither reader can
// take fails DEVICE_UI_DUMP_FAILED and the fake adb is never asked for `uiautomator`, although
// its /dev/tty dump would have answered (the fixture's base scenario has one) — it lacks the
// extras and suspends the device's other accessibility services. Seen failing with the full
// chain given readChain's dump fallback: the read then completed with the dump's document.
func TestFullReadNeverUsesUiautomator(t *testing.T) {
	cases := []struct {
		name  string
		files func(t *testing.T) map[string]string
	}{
		{"the reader cannot be pushed", func(t *testing.T) map[string]string { return nil }},
		{"the persistent reader is gone and the one-shot prints no document", func(t *testing.T) map[string]string {
			r := devicetest.StartReader(t)
			r.SetFullDocs(devicetest.ReaderGone)
			return map[string]string{
				uiSerial + ".push.exit":   "0",
				uiSerial + ".start.out":   "",
				uiSerial + ".forward.out": r.Port,
				// No .readerfull files: the one-shot's app_process prints no hierarchy.
			}
		}},
	}
	for _, c := range cases {
		f := newUIFixture(t, c.files(t))
		doc, _, err := f.adb.readFull(context.Background(), readerTarget(t, f.adb, uiSerial), fullOpts{})
		if err == nil {
			t.Fatalf("%s: a full dump of %d bytes although neither reader could take it", c.name, len(doc))
		}
		e := capability.AsError(err)
		if e == nil || e.Code != ErrUIDumpFailed {
			t.Fatalf("%s: error %v, want %s", c.name, err, ErrUIDumpFailed)
		}
		if !strings.Contains(e.NextStep, "device.uiDump") {
			t.Errorf("%s: next step %q does not name the capability that asked", c.name, e.NextStep)
		}
		for _, call := range f.calls(t) {
			if strings.Contains(call, "uiautomator") {
				t.Errorf("%s: uiautomator ran for a full dump: %q", c.name, call)
			}
		}
	}
}

// Decision (ADR-0110 Decision 5, plan m2w step 3: the one-shot takes the same words): when
// the persistent reader cannot run, the one-shot reader is run with `dump full` (and
// `invisible` when asked) after its class name, in that order; the document it prints is the
// read, with the one-shot warning. A plain read through the same fallback passes no words,
// so the plain dump's command is unchanged. Seen failing with the words left off the run
// (the fake adb then answers the plain run's files, and the full document is not found).
func TestFullReadOneShotGetsTheSameWords(t *testing.T) {
	const doc = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` + "\n" +
		`<node index="0" text="one-shot" class="android.widget.FrameLayout" package="com.example" bounds="[0,0][1,1]" />` + "\n</hierarchy>\n"
	for _, c := range []struct {
		opts fullOpts
		call string
	}{
		{fullOpts{}, fullOneShotCall},
		{fullOpts{invisible: true}, fullOneShotCall + " invisible"},
	} {
		f := newUIFixture(t, map[string]string{
			uiSerial + ".push.exit":      "0",
			uiSerial + ".start.exit":     "1", // the persistent reader cannot start
			uiSerial + ".readerfull.out": doc,
			uiSerial + ".reader.out":     "plain one-shot: not the full dump",
		})
		got, warnings, err := f.adb.readFull(context.Background(), readerTarget(t, f.adb, uiSerial), c.opts)
		if err != nil {
			t.Fatalf("%+v: %v", c.opts, err)
		}
		if string(got) != doc {
			t.Errorf("%+v: read %q, want the one-shot's full document", c.opts, got)
		}
		if len(warnings) != 1 || !strings.HasPrefix(warnings[0], oneShotWarning) {
			t.Errorf("%+v: warnings %q, want the one-shot warning", c.opts, warnings)
		}
		calls := f.calls(t)
		if count(calls, c.call) != 1 || count(calls, rdRunCall) != 0 {
			t.Errorf("%+v: calls %q, want the one-shot run %q once and no plain run", c.opts, calls, c.call)
		}
		if count(calls, rdRmCall) != 1 {
			t.Errorf("%+v: calls %q, want the one-shot's dex removed", c.opts, calls)
		}
	}
}

// Decision (ADR-0110 Decision 5): a full dump over the persistent reader sends exactly `dump
// full` or `dump full invisible`, no idle words and nothing else, and does not count as a
// plain dump; a plain read sends its own. The reader is reused, not restarted.
func TestFullReadSendsItsWordsToThePersistentReader(t *testing.T) {
	const doc = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">` + "\n" +
		`<node index="0" text="full" class="android.widget.FrameLayout" package="com.example" bounds="[0,0][1,1]" />` + "\n</hierarchy>\n"
	files, r := withPersistentReaderR(t, nil, pixelDoc(t))
	r.SetFullDocs(doc)
	f := newUIFixture(t, files)
	tgt := readerTarget(t, f.adb, uiSerial)
	for _, opts := range []fullOpts{{}, {invisible: true}, {}} {
		got, warnings, err := f.adb.readFull(context.Background(), tgt, opts)
		if err != nil || string(got) != doc || len(warnings) != 0 {
			t.Fatalf("%+v: read %q, warnings %q, error %v", opts, got, warnings, err)
		}
	}
	if want := []string{"full", "full invisible", "full"}; !slices.Equal(r.FullRequests(), want) {
		t.Errorf("the reader was asked %q, want %q", r.FullRequests(), want)
	}
	if r.Dumps() != 0 {
		t.Errorf("%d plain dumps for full reads", r.Dumps())
	}
	if n := countServeStarts(f.calls(t)); n != 1 {
		t.Errorf("%d serve starts, want the one that started the reader", n)
	}
}

// Decision (ADR-0110 Decision 5): a full dump the reader answers `busy` fails with a cause of
// its own, not ADR-0034 §1's "the foreground app did not answer", which a large tree makes
// false; it carries nothing from the device (no package, so the failed call is not untrusted
// and the window manager's focus is not read), restarts nothing, and runs neither the one-shot
// nor `uiautomator dump`. Seen failing with the full chain's busy answered by appBusyError: the
// cause is then the foreground app's, and `dumpsys window` is read.
func TestFullDumpBusyHasItsOwnCause(t *testing.T) {
	files, r := withPersistentReaderR(t, nil, pixelDoc(t))
	r.SetFullDocs("droidyard-reader: busy 2500 com.example")
	f := newUIFixture(t, files)
	_, _, err := f.adb.readFull(context.Background(), readerTarget(t, f.adb, uiSerial), fullOpts{})
	e := capability.AsError(err)
	if e == nil || e.Code != ErrUIDumpFailed {
		t.Fatalf("error %v, want %s", err, ErrUIDumpFailed)
	}
	if e.Cause != fullBusyCause || e.Cause == appBusyCause || isAppBusy(err) {
		t.Errorf("cause %q, want %q and not the foreground app's", e.Cause, fullBusyCause)
	}
	if len(e.Details) != 0 || busyApp(err) {
		t.Errorf("details %v: a full busy answer carries nothing from the device", e.Details)
	}
	for _, call := range f.calls(t) {
		if strings.Contains(call, "uiautomator") || strings.Contains(call, "dumpsys") || call == fullOneShotCall || call == rdRunCall {
			t.Errorf("unexpected call after a busy full dump: %q", call)
		}
	}
	if countServeStarts(f.calls(t)) != 1 {
		t.Errorf("the reader was restarted after a busy answer: %q", f.calls(t))
	}
}

// fullDocument is a document in the shape DroidyardReader's full dump writes (ADR-0110): the
// header and each element on a line of its own, a leaf self-closed as uiautomator writes it,
// uiautomator's attributes in its order and the extras after them.
const fullDocument = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="1">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.example" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1080,2400]" drawing-order="0" hint="" actions="focus">
<node index="0" text="a &amp; b" resource-id="com.example:id/name" class="android.widget.EditText" package="com.example" content-desc="" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="true" scrollable="false" long-clickable="true" password="false" selected="false" bounds="[0,100][1080,200]" drawing-order="1" hint="Name" editable="true" error="Required" input-type="1" max-text-length="20" actions="click,long_click,set_text" custom-actions="2130771983:Archive" text-links="0-5=https://example.com/a%20b" />
<node index="1" text="below the fold" resource-id="" class="android.widget.TextView" package="com.example" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,2400][1080,2400]" drawing-order="2" hint="" visible-to-user="false" important="false" />
</node>
</hierarchy>
`

// Decision (the node count and the foreground package of device.uiDump's result are read from
// the document by parseDump, plan m2w step 4): the full document's shape, extras, self-closed
// leaves and one element per line, is parsed by the parser the plain tree uses, with every
// node found in order and the plain attributes read.
func TestFullDocumentShapeParses(t *testing.T) {
	rotation, roots, err := parseDump([]byte(fullDocument))
	if err != nil {
		t.Fatal(err)
	}
	if rotation != 1 || len(roots) != 1 || len(roots[0].children) != 2 {
		t.Fatalf("rotation %d, %d roots, children %d; want 1, 1, 2", rotation, len(roots), len(roots[0].children))
	}
	name, below := roots[0].children[0], roots[0].children[1]
	if name.text != "a & b" || name.id != "name" || name.hint != "Name" || !slices.Contains(name.flags, "editable") || !slices.Contains(name.flags, "clickable") {
		t.Errorf("the name field parsed as %+v", name)
	}
	if below.text != "below the fold" || below.bounds != [4]int{0, 2400, 1080, 2400} {
		t.Errorf("the node below the fold parsed as %+v", below)
	}
	if n := strings.Count(fullDocument, "<node "); n != 3 {
		t.Fatalf("the test document has %d nodes", n)
	}
}

// fullFixtures are the recorded `dump full` documents of formapp's WidgetsActivity and the
// `uiautomator dump` taken of the same screen (plan m2w step 1), per API level.
var fullFixtures = []struct {
	api            int
	full, uiaModel string
}{
	{31, "reader-full-widgets-api31.xml", "uiautomator-dump-widgets-api31.xml"},
	{36, "reader-full-widgets-api36.xml", "uiautomator-dump-widgets-api36.xml"},
}

// Decision (ADR-0110 Decision 3: the format is a superset of `uiautomator dump`'s): a recorded
// full dump holds, at the same position among the active window's nodes, every attribute of
// the `uiautomator dump` node taken of the same screen — its name and its value — in its order.
// On API 31 `bounds` of the root node and of the navigation bar background are excepted: the
// reader writes the real display's, `uiautomator` the app area's (ADR-0110 Context; the
// step-1 note). The fixtures are the level-5 capture of the reader's own output; this guards
// the recorded bytes, and skips a fixture that is not recorded yet.
func TestFullDumpIsASupersetOfUiautomator(t *testing.T) {
	for _, fx := range fullFixtures {
		t.Run(fmt.Sprintf("api%d", fx.api), func(t *testing.T) {
			full := filepath.Join("testdata", fx.full)
			if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s is missing: the level-5 capture of `dump full` on API %d (plan m2w step 3)", full, fx.api)
			}
			problems := supersetProblems(t, fixture(t, fx.uiaModel), fixture(t, fx.full), fx.api == 31)
			for _, p := range problems {
				t.Error(p)
			}
		})
	}
}

// Decision (the test above is only as good as its check): the check passes a full document
// derived by hand from the reader's recorded plain dump of the same screen and fails it when
// an attribute is lost, changed or moved. The derivation is the reader's own: the plain dump
// less `editable`, which the full dump writes as an extra, only when true. Seen failing with
// `long-clickable` removed from one node (the loss is reported by name), and with the API 31
// exceptions off (the two bounds the reader and `uiautomator` disagree on are reported, and
// only those).
func TestSupersetCheckCatchesALostAttribute(t *testing.T) {
	for _, api := range []int{31, 36} {
		uia := fixture(t, fmt.Sprintf("uiautomator-dump-widgets-api%d.xml", api))
		plain := fixture(t, fmt.Sprintf("reader-dump-widgets-api%d.xml", api))
		// Written to a temporary directory and read back, as a recorded fixture is.
		dir := t.TempDir()
		path := filepath.Join(dir, fmt.Sprintf("reader-full-widgets-api%d.xml", api))
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(plain, ` editable="false"`, "")), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		derived := string(b)

		if got := supersetProblems(t, uia, derived, api == 31); len(got) != 0 {
			t.Errorf("api %d: the derived document fails the check: %q", api, got)
		}

		lost := strings.Replace(derived, ` long-clickable="false"`, "", 1)
		if lost == derived {
			t.Fatalf("api %d: nothing to remove", api)
		}
		got := supersetProblems(t, uia, lost, api == 31)
		if len(got) != 1 || !strings.Contains(got[0], "long-clickable") {
			t.Errorf("api %d: a lost long-clickable gave %q, want one problem naming it", api, got)
		}

		changed := strings.Replace(derived, ` enabled="true"`, ` enabled="false"`, 1)
		if got := supersetProblems(t, uia, changed, api == 31); len(got) != 1 || !strings.Contains(got[0], "enabled") {
			t.Errorf("api %d: a changed enabled gave %q, want one problem naming it", api, got)
		}

		// The order of uiautomator's attributes is part of the format.
		moved := strings.Replace(derived, ` password="false" selected="false"`, ` selected="false" password="false"`, 1)
		if got := supersetProblems(t, uia, moved, api == 31); len(got) == 0 {
			t.Errorf("api %d: attributes swapped and the check passed", api)
		}

		if api == 31 {
			if got := supersetProblems(t, uia, derived, false); len(got) != 2 || !strings.Contains(got[0], "bounds") || !strings.Contains(got[1], "bounds") {
				t.Errorf("api 31 without the exceptions: %q, want the two bounds the reader and uiautomator disagree on", got)
			}
		}
	}
}

// Decision (the format is parsed by what already parses `uiautomator dump`'s): every recorded
// full dump is read by parseDump, with as many nodes as the file has start tags, and holds one
// element per line, so `artifact.read` pages it by whole lines (ADR-0110 Decision 2). Skips
// until the fixtures are recorded.
func TestFullFixturesParse(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "reader-full-*.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no reader-full-*.xml fixture: the level-5 capture of `dump full` (plan m2w step 3)")
	}
	for _, path := range paths {
		doc := fixture(t, filepath.Base(path))
		_, roots, err := parseDump([]byte(doc))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		var nodes func(ns []*rawNode) int
		nodes = func(ns []*rawNode) int {
			n := len(ns)
			for _, c := range ns {
				n += nodes(c.children)
			}
			return n
		}
		if got, want := nodes(roots), strings.Count(doc, "<node "); got != want || got == 0 {
			t.Errorf("%s: parsed %d nodes, the file has %d", path, got, want)
		}
		for i, line := range strings.Split(doc, "\n") {
			if strings.Count(line, "<node ") > 1 {
				t.Errorf("%s: line %d holds more than one element", path, i+1)
			}
		}
	}
}

// uiaNode is one <node> of a document with its attributes in the order written.
type uiaNode struct{ attrs []xml.Attr }

func (n uiaNode) get(name string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// nodesInOrder returns the <node> elements of a hierarchy document in document order.
func nodesInOrder(t *testing.T, doc string) []uiaNode {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var nodes []uiaNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nodes
		}
		if err != nil {
			t.Fatalf("parsing a hierarchy document: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "node" {
			nodes = append(nodes, uiaNode{attrs: se.Attr})
		}
	}
}

// supersetProblems lists where full is not a superset of uia (ADR-0110 Decision 3): at each
// position of the active window's nodes, an attribute of the uiautomator node that the full
// node lacks, holds with another value, or holds in another order. With api31 the `bounds` of
// the root node and of android:id/navigationBarBackground are not compared.
func supersetProblems(t *testing.T, uia, full string, api31 bool) []string {
	t.Helper()
	u, f := nodesInOrder(t, uia), nodesInOrder(t, full)
	if len(u) != len(f) {
		return []string{fmt.Sprintf("%d nodes in the uiautomator dump, %d in the full dump", len(u), len(f))}
	}
	var problems []string
	for i := range u {
		id, _ := u[i].get("resource-id")
		last := -1
		for _, a := range u[i].attrs {
			name := a.Name.Local
			if api31 && name == "bounds" && (i == 0 || id == "android:id/navigationBarBackground") {
				continue
			}
			v, ok := f[i].get(name)
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("node %d (%s): the full dump lacks %s=%q", i, id, name, a.Value))
				continue
			case v != a.Value:
				problems = append(problems, fmt.Sprintf("node %d (%s): %s is %q in the full dump, %q in the uiautomator dump", i, id, name, v, a.Value))
			}
			at := slices.IndexFunc(f[i].attrs, func(x xml.Attr) bool { return x.Name.Local == name })
			if at < last {
				problems = append(problems, fmt.Sprintf("node %d (%s): %s is out of uiautomator's order", i, id, name))
			}
			last = at
		}
	}
	return problems
}

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/daemon"
	"github.com/jtortosasen/droidyard/internal/testtime"
)

// bigXML is a 3,000-line XML document of more than 600 KiB, one element per line as the full
// UI dump writes it, with multi-byte text so a page cut in the middle of a rune would show.
func bigXML() string {
	var b strings.Builder
	b.WriteString("<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation=\"0\">\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "<node index=\"%d\" text=\"línea %d ñ 日本語 %s\" class=\"android.widget.TextView\" package=\"com.example\" bounds=\"[0,%d][1080,%d]\" />\n",
			i, i, strings.Repeat("x", 160), i, i+1)
	}
	b.WriteString("</hierarchy>\n")
	return b.String()
}

// Decision (ADR-0110 Decision 2: `artifact cat` writes the artifact's bytes to stdout
// unchanged, and says on stderr that an untrusted artifact's bytes are device data): a
// 600 KiB-plus, 3,000-line XML artifact, which `artifact.read` pages at 256 KiB, comes out
// byte-identical across its pages, with the note on stderr once. Seen failing with the first
// page only (the output was 261,891 of 889,657 bytes).
func TestArtifactCatWritesTheBytes(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	doc := bigXML()
	if len(doc) < 600<<10 || strings.Count(doc, "\n") != 3002 {
		t.Fatalf("the fixture document is %d bytes with %d lines", len(doc), strings.Count(doc, "\n"))
	}
	ref, err := p.store.Put(artifact.Item{Data: []byte(doc), MediaType: "application/xml", Untrusted: true, Producer: "device.uiDump"})
	if err != nil {
		t.Fatal(err)
	}

	cat := droidyard(t, p.state, "artifact", "cat", ref.Ref)
	if code := cat.wait(t); code != 0 {
		t.Fatalf("artifact cat exited %d, stderr %q", code, cat.stderr.String())
	}
	if got := cat.stdout.String(); got != doc {
		t.Errorf("stdout holds %d bytes, the artifact %d; want them identical", len(got), len(doc))
	}
	if got := strings.TrimSpace(cat.stderr.String()); got != untrustedBytesNote {
		t.Errorf("stderr is %q; want the one untrusted note %q", got, untrustedBytesNote)
	}
}

// Decision (ADR-0015 §1: the mark is the artifact's own): an artifact the daemon does not mark
// untrusted is written with nothing on stderr, so the note means something where it appears.
func TestArtifactCatSaysNothingOfATrustedArtifact(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	ref, err := p.store.Put(artifact.Item{Data: []byte("plain console\nline two\n"), MediaType: "text/plain", Producer: "test"})
	if err != nil {
		t.Fatal(err)
	}
	cat := droidyard(t, p.state, "artifact", "cat", ref.Ref)
	if code := cat.wait(t); code != 0 || cat.stdout.String() != "plain console\nline two\n" || cat.stderr.String() != "" {
		t.Errorf("exit %d, stdout %q, stderr %q; want the bytes and a silent stderr", code, cat.stdout.String(), cat.stderr.String())
	}
}

// Decision (plan m2z D16, superseding plan m2w step 4's refusal of an image): an unknown
// reference is ARTIFACT_NOT_FOUND on stderr with nothing on stdout; an image is written
// byte-identical when stdout is a pipe (`artifact cat <screenshot> > shot.png`) and refused,
// naming its media type, on a terminal; a state with no daemon is "no daemon is running",
// exit 1, and none is started. Seen failing with the old pager (the image refused on a pipe).
func TestArtifactCatRefusesWhatItCannotWrite(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)

	missing := droidyard(t, p.state, "artifact", "cat", "droidyard://artifact/e.nope.1.00000000")
	if code := missing.wait(t); code == 0 || missing.stdout.Len() != 0 || !strings.Contains(missing.stderr.String(), "ARTIFACT_NOT_FOUND") {
		t.Errorf("unknown ref: exit %d, stdout %q, stderr %q; want non-zero, ARTIFACT_NOT_FOUND on stderr", code, missing.stdout.String(), missing.stderr.String())
	}

	var enc bytes.Buffer
	if err := png.Encode(&enc, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	ref, err := p.store.Put(artifact.Item{Data: enc.Bytes(), MediaType: "image/png", Width: 4, Height: 4, Untrusted: true, Producer: "test"})
	if err != nil {
		t.Fatal(err)
	}
	pipe := droidyard(t, p.state, "artifact", "cat", ref.Ref)
	if code := pipe.wait(t); code != 0 || !bytes.Equal(pipe.stdout.Bytes(), enc.Bytes()) {
		t.Errorf("image to a pipe: exit %d, %d bytes on stdout (stderr %q); want the %d PNG bytes unchanged", code, pipe.stdout.Len(), pipe.stderr.String(), enc.Len())
	}
	var out, errs bytes.Buffer
	if code := artifactCatFrom(p.state, ref.Ref, &out, &errs, true); code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "image/png") {
		t.Errorf("image to a terminal: exit %d, stdout %d bytes, stderr %q; want 1, nothing written, image/png named", code, out.Len(), errs.String())
	}

	out.Reset()
	errs.Reset()
	empty := shortDir(t)
	if code := artifactCatFrom(empty, ref.Ref, &out, &errs, false); code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "no daemon is running") {
		t.Errorf("no daemon: exit %d, stdout %d bytes, stderr %q; want 1 and no daemon is running", code, out.Len(), errs.String())
	}
	if _, held, _ := daemon.Holder(empty); held {
		t.Error("artifact cat started a daemon")
	}
}

// Decision (plan m2z D16: one request is one recorded artifact.read): the 889 KB fixture,
// which artifact.read pages at 256 KiB, is read in exactly one operation. Seen failing with
// the old pager (four artifact.read operations in the daemon's trace).
func TestArtifactCatIsOneOperation(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	trace := new(lockedBuffer)
	p.ops.UseTrace(func(format string, v ...any) { fmt.Fprintf(trace, format+"\n", v...) })
	doc := bigXML()
	ref, err := p.store.Put(artifact.Item{Data: []byte(doc), MediaType: "application/xml", Untrusted: true, Producer: "device.uiDump"})
	if err != nil {
		t.Fatal(err)
	}
	cat := droidyard(t, p.state, "artifact", "cat", ref.Ref)
	if code := cat.wait(t); code != 0 || cat.stdout.String() != doc {
		t.Fatalf("artifact cat exited %d with %d of %d bytes, stderr %q", code, cat.stdout.Len(), len(doc), cat.stderr.String())
	}
	// An operation's trace line is written when it ends.
	deadline := time.Now().Add(testtime.Bound(2 * time.Second))
	for !strings.Contains(trace.String(), "capability=artifact.read ") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(trace.String(), "capability=artifact.read "); n != 1 {
		t.Errorf("the daemon recorded %d artifact.read operations; want exactly 1\n%s", n, trace.String())
	}
}

// Decision (http-websocket.md §GET /v1/artifacts/{id}: a growing artifact is never served as
// if whole): a text artifact still being written is exit 1 with the route's message, and
// nothing on stdout.
func TestArtifactCatGrowing(t *testing.T) {
	t.Parallel()
	p := startInProcess(t)
	d, err := p.store.CreateDurable("w", "growing", artifact.DurableOptions{MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Write([]byte("half a file\n")); err != nil {
		t.Fatal(err)
	}
	cat := droidyard(t, p.state, "artifact", "cat", d.Ref().Ref)
	if code := cat.wait(t); code != 1 || cat.stdout.Len() != 0 || !strings.Contains(cat.stderr.String(), "still being written") {
		t.Errorf("growing: exit %d, stdout %q, stderr %q; want 1, nothing on stdout, the route's message", code, cat.stdout.String(), cat.stderr.String())
	}
}

// Decision (the command line): cat takes exactly one reference; anything else is a usage error
// (exit 2) and no daemon is asked.
func TestArtifactCatUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"artifact", "cat"}, {"artifact", "cat", "a", "b"}} {
		proc := droidyard(t, shortDir(t), args...)
		if code := proc.wait(t); code != 2 || !strings.Contains(proc.stderr.String(), artifactUsage) {
			t.Errorf("%q: exit %d, stderr %q; want 2 and the usage", args, code, proc.stderr.String())
		}
	}
	// artifact is a domain of the tree (plan m2z D13): alone it lists its verbs, and a verb
	// it does not have is the tree's usage error naming the nearest.
	group := droidyard(t, shortDir(t), "artifact")
	if code := group.wait(t); code != 0 || !strings.Contains(group.stdout.String(), "cat") || !strings.Contains(group.stdout.String(), "read") {
		t.Errorf("artifact alone: exit %d, stdout %q; want the group's verbs, cat and read", code, group.stdout.String())
	}
	unknown := droidyard(t, shortDir(t), "artifact", "list")
	if code := unknown.wait(t); code != 2 || !strings.Contains(unknown.stderr.String(), `unknown command "list"`) {
		t.Errorf("artifact list: exit %d, stderr %q; want 2 and an unknown command", code, unknown.stderr.String())
	}
}

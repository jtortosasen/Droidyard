package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
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

// Decision (plan m2w step 4: an image or unknown ref exits non-zero with the error's message):
// a reference nothing holds is ARTIFACT_NOT_FOUND on stderr, and an image is refused with its
// media type named; neither writes a byte to stdout. Seen failing with the daemon's error
// envelope not read (the exit was non-zero but the message was a parse error, with no
// ARTIFACT_NOT_FOUND).
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
	img := droidyard(t, p.state, "artifact", "cat", ref.Ref)
	if code := img.wait(t); code == 0 || img.stdout.Len() != 0 || !strings.Contains(img.stderr.String(), "image/png") {
		t.Errorf("image: exit %d, stdout %d bytes, stderr %q; want non-zero, nothing on stdout, image/png named", code, img.stdout.Len(), img.stderr.String())
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
	// artifact is a domain of the tree (ADR-0122 D13): alone it lists its verbs, and a verb
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

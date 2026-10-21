package main

import (
	"io"
	"testing"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// Decision (rule 8): the UI route's reader opens a finished timeline's file in place and
// reports an id that is not an artifact with the store's own typed error, so the route
// answers 404 and not a 500.
func TestUITextArtifactsOpensAFinishedTimeline(t *testing.T) {
	s, err := artifact.New(artifact.Options{Epoch: "d-0a1b2c3d", StateRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	const id = "20261021T100000Z-0123abcd"
	d, err := s.CreateTimeline(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.WriteLine([]byte(`{"t":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	open := uiTextArtifacts(s)
	f, err := open("r." + id)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Body.Close()
	if b, _ := io.ReadAll(f.Body); string(b) != "{\"t\":1}\n" {
		t.Errorf("body %q; want the timeline's line", b)
	}
	if _, err := open("nonsense"); capability.AsError(err).Code != capability.ErrArtifactNotFound {
		t.Errorf("an id that is no artifact: %v; want ARTIFACT_NOT_FOUND", err)
	}
}

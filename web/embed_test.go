package web

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

// Decision (ADR-0040 §1): a tree with no UI build embeds only dist/.keep, and then
// Assets is nil, so the daemon serves its placeholder instead of an empty app. A built
// tree gives the app rooted at its index.html. A directory called index.html is not a
// build.
func TestAssetsNilWithoutABuild(t *testing.T) {
	if got := assets(fstest.MapFS{"dist/.keep": {}}); got != nil {
		t.Errorf("only .keep: Assets is %v; want nil", got)
	}
	if got := assets(fstest.MapFS{"dist/app/assets/x.js": {Data: []byte("x")}}); got != nil {
		t.Errorf("an app with no index.html: Assets is %v; want nil", got)
	}
	if got := assets(fstest.MapFS{"dist/app/index.html/x": {}}); got != nil {
		t.Errorf("index.html as a directory: Assets is %v; want nil", got)
	}
	built := assets(fstest.MapFS{
		"dist/.keep":          {},
		"dist/app/index.html": {Data: []byte("<html>")},
	})
	if built == nil {
		t.Fatal("a built tree: Assets is nil")
	}
	if b, err := fs.ReadFile(built, "index.html"); err != nil || string(b) != "<html>" {
		t.Errorf("the app is not rooted at index.html: %q, %v", b, err)
	}
}

// The embedded tree carries the committed .keep, so `go build` works with no UI build
// (ADR-0040 §1): the //go:embed pattern would fail to compile without it.
func TestEmbeddedTreeHoldsTheKeep(t *testing.T) {
	if _, err := fs.Stat(dist, "dist/.keep"); err != nil {
		t.Errorf("dist/.keep is not embedded: %v", err)
	}
}

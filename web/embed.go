// Package web embeds the built web UI (ADR-0040): `pnpm -C web build` writes dist/app,
// which is not committed, and the daemon serves it. A tree where that build has not run
// embeds only dist/.keep, so Go builds and tests never need Node.
package web

import (
	"embed"
	"io/fs"
)

// dist is web/dist: the committed .keep keeps the pattern matching with no build, and
// all: makes the dotfile part of it.
//
//go:embed all:dist
var dist embed.FS

// Assets is the built UI, rooted at its index.html, or nil when the binary was compiled
// without a build: the daemon then serves a placeholder naming the build command.
func Assets() fs.FS { return assets(dist) }

// assets finds dist/app in fsys, which is the embedded tree or a test's.
func assets(fsys fs.FS) fs.FS {
	app, err := fs.Sub(fsys, "dist/app")
	if err != nil {
		return nil
	}
	if info, err := fs.Stat(app, "index.html"); err != nil || info.IsDir() {
		return nil
	}
	return app
}

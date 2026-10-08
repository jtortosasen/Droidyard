// Package skills embeds droidyard's skills so `droidyard init` can write them into a
// project (ADR-0037). The skill's source of truth is the tree under android/, in
// ADR-0011's layout; go:embed cannot reach a parent directory, so the package sits
// beside it. references/tools.md is generated from the registry by a test in
// cmd/droidyard that fails on drift.
package skills

import (
	"embed"
	"io/fs"
)

//go:embed android
var tree embed.FS

// DroidyardAndroid is the droidyard-android skill: SKILL.md and references/ at its root.
func DroidyardAndroid() fs.FS {
	sub, err := fs.Sub(tree, "android/droidyard-android")
	if err != nil {
		panic(err) // the directory is embedded above
	}
	return sub
}

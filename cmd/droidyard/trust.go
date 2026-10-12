package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// trust implements `droidyard trust <root>`: the human channel that adds a project
// root to the configuration's trusted roots (ADR-0015 §5; plan m1g D2). It talks to no
// daemon and starts none. It canonicalises the root as `workspace.open` does and
// writes the file under an exclusive lock, so a running daemon picks the change up on
// its next trust check (ADR-0015 §6).
func trust(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "usage: droidyard trust <root>\n")
		return 2
	}
	root := args[0]
	if !filepath.IsAbs(root) {
		fmt.Fprintf(os.Stderr, "droidyard trust: %q is not an absolute path\n", root)
		return 2
	}
	canonical, err := policy.New(platform.Current()).Trust(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidyard trust:", err)
		return 1
	}
	fmt.Printf("trusted %s\n", canonical)
	// The human makes this choice here, so here it is said what it means (ADR-0015 §6,
	// Amended 2026-10-07; m1k close review B1).
	fmt.Println("Its build scripts and code now run as you. An agent that can edit them can run any command as you, droidyard's configuration and trust included.")
	return 0
}

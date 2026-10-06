package gradle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Fingerprint is the model cache key (plan m1g D8; gradle.md §Project model): the size
// and mtime of every file whose change can change the model — settings and build
// scripts, gradle.properties, local.properties, gradle/*.toml, the wrapper properties,
// and every file of buildSrc and of any included build (a directory with a
// settings.gradle(.kts) of its own, such as build-logic). build/, .gradle/ and .git/
// are skipped, and the walk stops after MaxFingerprintEntries so a pathological tree
// cannot hang a call.
//
// It is a cheap heuristic, not a Gradle-accurate one: init scripts in
// ~/.gradle/init.d, environment variables and remotely-resolved plugin versions are
// not covered (the plan's Unverified row for step 5).
func Fingerprint(root string) string {
	included := includedBuildDirs(root)
	h := sha256.New()
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipFingerprintDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if n >= MaxFingerprintEntries {
			return fs.SkipAll
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if !fingerprintFile(rel, included) {
			return nil
		}
		fi, ferr := d.Info()
		if ferr != nil {
			return nil
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), fi.Size(), fi.ModTime().UnixNano())
		n++
		return nil
	})
	sum := hex.EncodeToString(h.Sum(nil))
	return sum[:16]
}

// MaxFingerprintEntries bounds the fingerprint walk (plan m1g D8).
const MaxFingerprintEntries = 20000

var fingerprintNames = map[string]bool{
	"settings.gradle":     true,
	"settings.gradle.kts": true,
	"build.gradle":        true,
	"build.gradle.kts":    true,
	"gradle.properties":   true,
	"local.properties":    true,
}

func skipFingerprintDir(name string) bool {
	switch name {
	case "build", ".gradle", ".git":
		return true
	}
	return false
}

// fingerprintFile reports whether rel is part of the fingerprint: a named build file,
// a version catalog or wrapper property under gradle/, a file under buildSrc/, or a
// file under an included build.
func fingerprintFile(rel string, included []string) bool {
	slash := filepath.ToSlash(rel)
	name := filepath.Base(slash)
	if fingerprintNames[name] {
		return true
	}
	if strings.HasPrefix(slash, "gradle/") {
		return strings.HasSuffix(name, ".toml") || slash == "gradle/wrapper/gradle-wrapper.properties"
	}
	if slash == "buildSrc" || strings.HasPrefix(slash, "buildSrc/") {
		return true
	}
	for _, dir := range included {
		if d := filepath.ToSlash(dir); strings.HasPrefix(slash, d+"/") {
			return true
		}
	}
	return false
}

// includedBuildDirs lists the directories under root (the root itself excluded) that
// hold a settings.gradle(.kts): an included build's every file is part of the
// fingerprint. The paths are relative to root, as fingerprintFile's rel is.
func includedBuildDirs(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path != root && skipFingerprintDir(d.Name()) {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		for _, s := range []string{"settings.gradle", "settings.gradle.kts"} {
			if fi, serr := os.Stat(filepath.Join(path, s)); serr == nil && !fi.IsDir() {
				if rel, rerr := filepath.Rel(root, path); rerr == nil {
					dirs = append(dirs, rel)
				}
				return filepath.SkipDir
			}
		}
		return nil
	})
	return dirs
}

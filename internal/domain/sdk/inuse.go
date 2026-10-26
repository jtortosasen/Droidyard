package sdk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// InUseFunc is the seam through which sdk.install and sdk.uninstall ask whether a package
// they would replace or remove is in use (ADR-0129 §8; ADR-0131 §1, §2). It receives the
// SDK root and the package ids and answers, for each id in use, what uses it (a process,
// a build, a boot) in words a person can read. An id absent from the answer is free. Any
// doubt counts as in use: the detector folds its doubts into the answer, so a package it
// could not judge is named here, never silently free.
//
// SystemInUse is the shipped detector. Options.InUse nil is still the answer notChecked:
// every package is "in use: not checked yet", so a registry built without a detector
// refuses an update and an uninstall and runs a fresh install, which replaces nothing.
type InUseFunc func(ctx context.Context, root string, ids []string) map[string][]string

// notChecked is the answer without a detector: nothing can be called free that nobody
// checked.
func notChecked(_ context.Context, _ string, ids []string) map[string][]string {
	out := make(map[string][]string, len(ids))
	for _, id := range ids {
		out[id] = []string{"in use: not checked yet"}
	}
	return out
}

// InUseDeps are the three things the detector reads. SystemInUse fills the first two with
// the platform's; tests replace any of them.
type InUseDeps struct {
	// Procs lists the processes of the daemon's user (platform.UserProcesses).
	Procs func() ([]platform.Proc, error)
	// Open lists the files the pids hold open inside dirs (platform.Lsof.OpenUnder). Its
	// error is a doubt.
	Open func(ctx context.Context, pids []int, dirs []string) ([]platform.OpenFile, error)
	// Active lists Droidyard's own operations in flight (capability.Operations.Active);
	// nil means none.
	Active func() []capability.ActiveOperation
}

// SystemInUse is the detector over this host's processes and lsof, and over active, the
// daemon's operations in flight (nil for none). It is A10's rule (plan m3f step 5): a
// package is in use when a process of the user runs an executable inside its directory,
// when a candidate process holds a file open there, or when a Droidyard emulator boot or
// build that needs it is in flight; any doubt counts as in use.
func SystemInUse(active func() []capability.ActiveOperation) InUseFunc {
	l := platform.DefaultLsof()
	return NewInUse(InUseDeps{Procs: platform.UserProcesses, Open: l.OpenUnder, Active: active})
}

// NewInUse builds the detector from its dependencies.
func NewInUse(d InUseDeps) InUseFunc {
	return func(ctx context.Context, root string, ids []string) map[string][]string {
		return d.check(ctx, root, ids)
	}
}

// target is one package to check: its directories (as the SDK spells them and with
// symlinks resolved, since a kernel may report either) or the reason they could not be
// resolved.
type target struct {
	id    string
	dirs  []string
	doubt string
}

func (d InUseDeps) check(ctx context.Context, root string, ids []string) map[string][]string {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out
	}
	doubtAll := func(cause string) map[string][]string {
		for _, id := range ids {
			out[id] = []string{"could not check: " + cause}
		}
		return out
	}
	if d.Procs == nil || d.Open == nil {
		return doubtAll("no process reader")
	}
	procs, err := d.Procs()
	if err != nil {
		return doubtAll(err.Error())
	}

	targets := make([]target, len(ids))
	var allDirs []string
	for i, id := range ids {
		t := target{id: id}
		dirs, ok := packageDirs(root, id)
		if !ok {
			t.doubt = fmt.Sprintf("%q is not a package id", id)
		}
		for _, dir := range dirs {
			t.dirs = append(t.dirs, dir)
			real, err := filepath.EvalSymlinks(dir)
			switch {
			case err == nil && real != dir:
				t.dirs = append(t.dirs, real)
			case err != nil && !errors.Is(err, fs.ErrNotExist):
				t.doubt = err.Error()
			}
		}
		targets[i] = t
		allDirs = append(allDirs, t.dirs...)
	}

	exes := exeSpellings(procs)

	// The candidates are the only processes lsof is asked about: its cost over every
	// process of the user is unbounded (ADR-0129 §8).
	cands := candidates(root, procs, exes)
	var openFiles []platform.OpenFile
	var lsofDoubt string
	if len(cands) > 0 && len(allDirs) > 0 {
		pids := make([]int, len(cands))
		for i, c := range cands {
			pids[i] = procs[c].Pid
		}
		openFiles, err = d.Open(ctx, pids, allDirs)
		if err != nil {
			lsofDoubt = err.Error()
		}
	}

	var unreadable string
	for _, p := range procs {
		if p.Unreadable != "" {
			unreadable = fmt.Sprintf("process %d could not be read: %s", p.Pid, p.Unreadable)
			break
		}
	}
	var active []capability.ActiveOperation
	if d.Active != nil {
		active = d.Active()
	}

	for _, t := range targets {
		if by := d.firstUser(t, procs, exes, openFiles, active); by != "" {
			out[t.id] = []string{by}
			continue
		}
		switch {
		case t.doubt != "":
			out[t.id] = []string{"could not check: " + t.doubt}
		case unreadable != "":
			out[t.id] = []string{"could not check: " + unreadable}
		case lsofDoubt != "":
			out[t.id] = []string{"could not check: " + lsofDoubt}
		}
	}
	return out
}

// firstUser is what uses the package, by A10's order: an executable running from inside
// it, a candidate holding a file open there, a Droidyard operation in flight.
func (d InUseDeps) firstUser(t target, procs []platform.Proc, exes [][]string, open []platform.OpenFile, active []capability.ActiveOperation) string {
	for i, p := range procs {
		if anyWithin(exes[i], t.dirs) {
			return describeProc(p)
		}
	}
	for _, f := range open {
		if underDirs(f.Path, t.dirs) {
			for _, p := range procs {
				if p.Pid == f.Pid {
					return describeProc(p)
				}
			}
			return fmt.Sprintf("process (pid %d)", f.Pid)
		}
	}
	for _, op := range active {
		if what, ok := operationUses(op.Capability, t.id); ok {
			return fmt.Sprintf("%s in Droidyard (operation %s)", what, op.OperationID)
		}
	}
	return ""
}

// operationUses reports whether a Droidyard capability in flight needs the package: an
// emulator boot or wipe needs the emulator and the system images, a build the build tools
// and the platforms.
func operationUses(capName, id string) (string, bool) {
	switch {
	case capName == "emulator.start" || capName == "emulator.wipe":
		if id == "emulator" || strings.HasPrefix(id, "system-images;") {
			return "an emulator boot", true
		}
	case strings.HasPrefix(capName, "build."):
		if strings.HasPrefix(id, "build-tools;") || strings.HasPrefix(id, "platforms;") {
			return "a build", true
		}
	}
	return "", false
}

// Candidates returns the processes that run SDK code or load SDK files (A10): those whose
// executable lies under root or whose arguments name it, and those whose executable's base
// name is java, adb, emulator or studio or begins qemu-system-. The set bounds lsof's cost
// and lets a missing live candidate count as a doubt. Processes that could not be read
// are not in it: they are a doubt of their own.
func Candidates(root string, procs []platform.Proc) []platform.Proc {
	exes := exeSpellings(procs)
	var out []platform.Proc
	for _, i := range candidates(root, procs, exes) {
		out = append(out, procs[i])
	}
	return out
}

// exeSpellings are, per process, its executable as the kernel reports it and with symlinks
// resolved: a kernel may report either, and an SDK reached through a link has both.
func exeSpellings(procs []platform.Proc) [][]string {
	out := make([][]string, len(procs))
	for i, p := range procs {
		if p.Exe == "" {
			continue
		}
		out[i] = []string{p.Exe}
		if real, err := filepath.EvalSymlinks(p.Exe); err == nil && real != p.Exe {
			out[i] = append(out[i], real)
		}
	}
	return out
}

// candidates is Candidates over precomputed executable spellings; it returns indexes.
func candidates(root string, procs []platform.Proc, exes [][]string) []int {
	roots := []string{}
	if root != "" {
		roots = append(roots, filepath.Clean(root))
		if real, err := filepath.EvalSymlinks(root); err == nil && real != roots[0] {
			roots = append(roots, real)
		}
	}
	var out []int
	for i, p := range procs {
		if p.Exe == "" {
			continue
		}
		if isCandidate(p, exes[i], roots) {
			out = append(out, i)
		}
	}
	return out
}

func isCandidate(p platform.Proc, exes, roots []string) bool {
	base := filepath.Base(p.Exe)
	switch base {
	case "java", "adb", "emulator", "studio":
		return true
	}
	if strings.HasPrefix(base, "qemu-system-") {
		return true
	}
	if anyWithin(exes, roots) {
		return true
	}
	for _, a := range p.Args {
		for _, r := range roots {
			if strings.Contains(a, r) {
				return true
			}
		}
	}
	return false
}

// describeProc names a process by what it is, for the person who must stop it.
func describeProc(p platform.Proc) string {
	base := filepath.Base(p.Exe)
	switch {
	case base == "adb" && (slices.Contains(p.Args, "server") || slices.Contains(p.Args, "fork-server")):
		return fmt.Sprintf("the adb server (pid %d)", p.Pid)
	case base == "emulator" || strings.HasPrefix(base, "qemu-system-"):
		return fmt.Sprintf("an emulator (pid %d)", p.Pid)
	case base == "java" && argContains(p.Args, "GradleDaemon", "org.gradle.launcher.daemon"):
		return fmt.Sprintf("a Gradle daemon (pid %d)", p.Pid)
	case base == "studio":
		return fmt.Sprintf("Android Studio (pid %d)", p.Pid)
	}
	return fmt.Sprintf("process %s (pid %d)", base, p.Pid)
}

func argContains(args []string, subs ...string) bool {
	for _, a := range args {
		for _, s := range subs {
			if strings.Contains(a, s) {
				return true
			}
		}
	}
	return false
}

func anyWithin(paths, dirs []string) bool {
	for _, p := range paths {
		if underDirs(p, dirs) {
			return true
		}
	}
	return false
}

// underDirs reports whether path is one of dirs or inside one.
func underDirs(path string, dirs []string) bool {
	for _, d := range dirs {
		d = filepath.Clean(d)
		if path == d || strings.HasPrefix(path, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

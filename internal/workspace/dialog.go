package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// ErrDialogFailed is workspace.chooseRoot's one code of its own (ADR-0045 §4): the folder
// dialog could not run on this machine, or ended without an answer or a cancel.
const ErrDialogFailed capability.ErrorCode = "WORKSPACE_DIALOG_FAILED"

// DialogBound is how long a folder dialog waits for the user before it is taken down
// and answers dismissed (plan m2c D18: a working default).
const DialogBound = 5 * time.Minute

// osascript is the macOS script runner, by its fixed path: a dialog is never found
// through the daemon's PATH.
const osascript = "/usr/bin/osascript"

// dialogKey is the Resource key that lets one dialog be on screen at a time.
const dialogKey = "workspace.dialog"

// cancelMarker is what osascript reports when the user cancels: AppleScript error
// -128, in the user's language but always with this number (plan m2c step 2, D).
const cancelMarker = "(-128)"

// Dialog is the macOS folder dialog workspace.chooseRoot raises (ADR-0045 §4). The zero
// value cannot run one; NewDialog is the shipped configuration.
type Dialog struct {
	// GOOS is the platform the dialog is asked on; only darwin has one.
	GOOS string
	// Binary is the osascript executable.
	Binary string
	// Spawn starts cmd, in a process group of its own, and records it for as long as it
	// runs (process.FolderDialog); the daemon passes the supervisor's recorded spawner.
	// The dialog reaps the child through wait, which also removes the record, and calls
	// it exactly once. A spawn refused, because the record cannot be written among
	// other causes, is an error and no wait.
	Spawn func(cmd *exec.Cmd) (wait func() error, err error)
	// Bound is how long the dialog waits for the user; zero means DialogBound.
	Bound time.Duration
}

// NewDialog is the dialog the daemon raises: osascript on macOS, started through spawn.
func NewDialog(spawn func(cmd *exec.Cmd) (wait func() error, err error)) Dialog {
	return Dialog{GOOS: runtime.GOOS, Binary: osascript, Spawn: spawn}
}

// Missing says why the dialog cannot run here, as the folderDialog requirement answers
// it: on any platform but macOS, without osascript, or without a spawner.
func (d Dialog) Missing() (string, bool) {
	switch {
	case d.GOOS == "":
		return "no folder dialog is configured", true
	case d.GOOS != "darwin":
		return "the folder dialog exists on macOS only; type the project's path instead", true
	case d.Spawn == nil:
		return "no process spawner is configured for the folder dialog", true
	}
	fi, err := os.Stat(d.Binary)
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fmt.Sprintf("osascript was not found at %s; type the project's path instead", d.Binary), true
	}
	return "", false
}

// argv is the dialog's command line, with no shell between: the window is raised to
// the front first (the daemon has no window of its own), then asks for a folder.
func (d Dialog) argv() []string {
	return []string{d.Binary,
		"-e", "tell me to activate",
		"-e", `POSIX path of (choose folder with prompt "Choose a project folder")`}
}

// choose raises the dialog and returns the folder the user picked, or dismissed when
// the user cancelled or the bound passed. Cancelling ctx takes the dialog down and
// returns ctx's error. A dialog that cannot run, or ends with neither an answer nor a
// cancel, is ErrDialogFailed.
func (d Dialog) choose(ctx context.Context) (folder string, dismissed bool, err error) {
	if reason, missing := d.Missing(); missing {
		return "", false, dialogFailed(reason)
	}
	argv := d.argv()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = "/"
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// The group is killed with the leader, so a pipe a grandchild holds cannot keep Wait
	// from returning past this bound.
	cmd.WaitDelay = 2 * time.Second
	wait, err := d.Spawn(cmd)
	if err != nil {
		return "", false, dialogFailed(fmt.Sprintf("starting %s: %v", argv[0], err))
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- wait() }()
	bound := d.Bound
	if bound <= 0 {
		bound = DialogBound
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case waitErr := <-done:
		return d.answer(out.String(), errOut.String(), waitErr)
	case <-ctx.Done():
		takeDown(pid, done)
		return "", false, ctx.Err()
	case <-timer.C:
		takeDown(pid, done)
		return "", true, nil
	}
}

// takeDown kills the dialog's group and reaps it: the panel goes with the process.
func takeDown(pid int, done <-chan error) {
	syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}

// answer reads a finished dialog: its folder on success, dismissed on the user's cancel,
// else a failure carrying osascript's own words.
func (d Dialog) answer(stdout, stderr string, waitErr error) (string, bool, error) {
	if waitErr == nil {
		folder := strings.TrimSpace(stdout)
		if !filepath.IsAbs(folder) {
			return "", false, dialogFailed(fmt.Sprintf("the dialog answered %q, not an absolute path", folder))
		}
		return filepath.Clean(folder), false, nil
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) && strings.Contains(stderr, cancelMarker) {
		return "", true, nil
	}
	cause := strings.TrimSpace(stderr)
	if cause == "" {
		cause = waitErr.Error()
	}
	if len(cause) > 500 {
		cause = cause[:500]
	}
	return "", false, dialogFailed(cause)
}

func dialogFailed(cause string) *capability.Error {
	return capability.NewError(ErrDialogFailed, "the folder dialog could not give an answer").
		WithCause(cause).
		WithNextStep("open the project by typing its path, or run `droidyard` inside the project folder")
}

// ChooseRootResult is workspace.chooseRoot's result: the project root of the folder the
// user picked, or dismissed.
type ChooseRootResult struct {
	Root      string `json:"root,omitempty" jsonschema_description:"The nearest ancestor of the chosen folder, itself included, that holds settings.gradle or settings.gradle.kts; the folder itself when none does. Absent when dismissed."`
	Dismissed bool   `json:"dismissed,omitempty" jsonschema_description:"True when the user cancelled the dialog or left it for five minutes."`
}

// chooseRoot is workspace.chooseRoot's handler.
func (r *Registry) chooseRoot(ctx context.Context) (any, error) {
	folder, dismissed, err := r.opts.Dialog.choose(ctx)
	if err != nil {
		return nil, err
	}
	if dismissed {
		return ChooseRootResult{Dismissed: true}, nil
	}
	if root, ok := platform.GradleRoot(folder); ok {
		return ChooseRootResult{Root: root}, nil
	}
	return ChooseRootResult{Root: folder}, nil
}

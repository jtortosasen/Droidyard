package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/device"
	"github.com/jtortosasen/droidyard/internal/event"
	"github.com/jtortosasen/droidyard/internal/platform"
	"github.com/jtortosasen/droidyard/internal/policy"
)

// version is the API version that introduces git.read (plan m2l step 3). git.watch, in
// the same milestone, keeps it.
const version = "0.23.0"

// The codes the git capabilities return besides the file and request ones (ADR-0066 §4).
const (
	// CodeNotFound: no git executable was found. nextStep: install it or put it on PATH.
	CodeNotFound capability.ErrorCode = "GIT_NOT_FOUND"
	// CodeNotARepository: the project root is in no git work tree.
	CodeNotARepository capability.ErrorCode = "GIT_NOT_A_REPOSITORY"
	// CodeFailed: git could not be run or exited non-zero for another reason; the cause
	// is git's first line of stderr, or what stopped the run (a timeout).
	CodeFailed capability.ErrorCode = "GIT_FAILED"
)

// BaseHEAD is the only base there is: the working tree against HEAD. Both capabilities
// take it as a parameter so that commits and branches widen an enum later and break
// nothing (ADR-0066 §1).
const BaseHEAD = "HEAD"

// Options configure the git capabilities.
type Options struct {
	// Root names the canonical root of an open workspace, and whether it is open (the
	// workspace registry's Describe). git.* needs nothing else from the registry.
	Root func(workspace string) (string, bool)
	// Env canonicalises the directory of an absolute path, so the "/var/..." and
	// "/private/var/..." spellings of the root agree (file.ResolveLexical).
	Env platform.Env
	// Trusted is policy.Trusted: whether the root is trusted (ADR-0015 §2), read before
	// every git. Nil, and a read that fails, are an untrusted root: the safe side.
	Trusted func(root string) (bool, *capability.Error)
	// Starter is the supervisor's spawner, which puts git in a process group of its own.
	// Nil (the golden registry's descriptors) makes a call GIT_FAILED.
	Starter device.Starter
	// Environ is the environment git runs in, scrubbed of GIT_* by the runner; nil is the
	// daemon's own, read at each call.
	Environ func() []string
	// LookPath finds the git executable at each call, so installing git needs no restart;
	// nil is exec.LookPath. A failure is GIT_NOT_FOUND.
	LookPath func(file string) (string, error)
	// Bus is the daemon's event bus: git.watch publishes the status on it. Nil (the
	// golden registry's descriptors) makes a watch answer SESSION_REQUIRED.
	Bus *event.Bus
	// Watchers is the daemon's live-poller set, so a workspace close can stop that
	// workspace's watches. Nil makes a watch answer SESSION_REQUIRED, as Bus does.
	Watchers *Watchers
	// Tick is the poller's interval between statuses; <= 0 is watchInterval's one
	// second. A test shortens it.
	Tick time.Duration
}

// Register adds git.read and git.watch to the registry.
func Register(reg *capability.Registry, opts Options) {
	registerRead(reg, opts)
	registerWatch(reg, opts)
}

// root resolves the call's workspace to its project root.
func (o Options) root(call *capability.Call) (string, *capability.Error) {
	root, ok := "", false
	if o.Root != nil {
		root, ok = o.Root(call.Workspace)
	}
	if !ok || root == "" {
		return "", capability.Errorf(capability.ErrWorkspaceNotOpen, "workspace %q is not open", call.Workspace).
			WithNextStep("call workspace.open on it, then retry")
	}
	return root, nil
}

// runner builds the runner for one call: git is looked for afresh, so the answer is
// GIT_NOT_FOUND until it is installed and the daemon needs no restart.
func (o Options) runner() (*runner, *capability.Error) {
	look := o.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look("git")
	if err != nil || path == "" {
		return nil, notFound(err)
	}
	environ := os.Environ
	if o.Environ != nil {
		environ = o.Environ
	}
	return newRunner(o.Starter, path, environ()), nil
}

// trusted is policy.Trusted's answer for root; a missing hook or an unreadable
// configuration is "not trusted", which neutralises the repository's filters.
func (o Options) trusted(root string) bool {
	if o.Trusted == nil {
		return false
	}
	ok, err := o.Trusted(root)
	return ok && err == nil
}

func notFound(cause error) *capability.Error {
	e := capability.NewError(CodeNotFound, "git was not found on this machine").
		WithNextStep("install git, or add it to the PATH droidyard runs with, then retry")
	if cause != nil {
		e.WithCause(cause.Error())
	}
	return e
}

// mapError turns what the runner and status return into the contract's codes
// (ADR-0066 §3, §4). A cancelled call passes through for the operation layer to end as
// cancelled.
func mapError(root string, err error) error {
	var ce *capability.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, ErrNotFound):
		return notFound(nil)
	case errors.Is(err, ErrNotARepository):
		return capability.Errorf(CodeNotARepository, "the project root %q is not inside a git repository", root).
			WithNextStep("initialise a repository in the project folder, or open a project that is one")
	case errors.Is(err, ErrUnsafeDriver):
		// A filter driver of this repository cannot be switched off, so git is not run
		// on an untrusted root (ADR-0066 §3); trusting the root is the way on.
		return capability.Errorf(capability.ErrWorkspaceUntrusted, "the project root %q is not trusted, and a git filter driver configured for it cannot be switched off, so git was not run", root).
			WithCause(err.Error()).
			WithNextStep(policy.UntrustedError(root).NextStep)
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, ErrGitFailed):
		return capability.Errorf(CodeFailed, "git failed").
			WithCause(strings.TrimPrefix(err.Error(), ErrGitFailed.Error()+": "))
	default:
		// A timeout, no starter, a start failure, an unexpected exit of the filter read.
		return capability.Errorf(CodeFailed, "git could not be run").WithCause(err.Error())
	}
}

package sdk

import (
	"context"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// None is the empty parameter object: sdk.status takes no arguments.
type None struct{}

// StatusResult is sdk.status' result: the host SDK and what is installed in it, or why
// there is none.
type StatusResult struct {
	Found  bool   `json:"found" jsonschema:"required" jsonschema_description:"Whether a host SDK was found."`
	Root   string `json:"root,omitempty" jsonschema_description:"The host SDK directory."`
	Source string `json:"source,omitempty" jsonschema_description:"Where root came from: ANDROID_HOME, ANDROID_SDK_ROOT, derived from adb on PATH, or the per-OS default."`
	// Rejected lists every candidate tried before the winner, or all of them when none
	// won.
	Rejected []Rejected `json:"rejected" jsonschema:"required"`
	// Warnings are notes that do not stop resolution: an ANDROID_HOME/ANDROID_SDK_ROOT
	// inconsistency, and package.xml files that were malformed or too large and skipped.
	Warnings  []string  `json:"warnings" jsonschema:"required"`
	Installed Installed `json:"installed" jsonschema:"required"`
}

// Register adds sdk.status to the registry, resolving the host SDK in env, the daemon's
// environment.
func Register(reg *capability.Registry, env []string) {
	reg.MustRegister(capability.Descriptor{
		Name:  "sdk.status",
		Kind:  capability.Query,
		Scope: capability.Global,
		Since: "0.7.0",
		Description: "Reports the host SDK: its root and where it was found (source), every candidate rejected before it with why, and what is installed, read from the SDK's own package.xml files: platforms, build-tools, platform-tools, cmdline-tools and system images, each as its package id and version. " +
			"No JVM and no sdkmanager are involved, and it is read fresh on every call. " +
			"With no SDK it reports found: false, every candidate rejected, and the variable that would fix it (ANDROID_HOME). " +
			"Warnings report an ANDROID_HOME/ANDROID_SDK_ROOT inconsistency and any package.xml that was malformed or too large and skipped; at most 2000 package.xml files are read.",
		Params: None{},
		Result: StatusResult{},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return Status(env), nil
		},
	})
}

// Status answers sdk.status for env: the host SDK and what is installed, or why there
// is none.
func Status(env []string) StatusResult {
	h := Host(env)
	out := StatusResult{
		Found:     h.Found,
		Root:      h.Root,
		Source:    h.Source,
		Rejected:  h.Rejected,
		Warnings:  append([]string{}, h.Warnings...),
		Installed: emptyInstalled(),
	}
	if !h.Found {
		// D11: no availability requirement and no new code; the missing SDK is answered
		// with found: false and the variable that would fix it.
		out.Warnings = append(out.Warnings, h.NotFound())
		return out
	}
	installed, warnings := ReadInstalled(h.Root)
	out.Installed = installed
	out.Warnings = append(out.Warnings, warnings...)
	return out
}

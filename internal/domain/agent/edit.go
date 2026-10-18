package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// The bounds of agent.checkWrite's and agent.recordEdit's paths (ADR-0095 §3).
const (
	// MaxFiles is the most paths one call names.
	MaxFiles = 20
	// MaxPath is the most bytes one path holds.
	MaxPath = 4096
)

// Options are what the edit capabilities read from the daemon.
type Options struct {
	// Sessions is the registry that says who holds a worktree. Nil: no one does, and every
	// write is allowed (the golden registry runs no handler).
	Sessions *capability.Sessions
	// Canonical resolves a path as a workspace root is (platform.Env.Canonical): symlinks
	// followed as far as the path exists, the rest kept as spelled. Nil leaves a path as it is.
	Canonical func(string) string
	// Nudge asks the workspace's file-watch pollers to walk at once (file.Watchers.Nudge), so
	// a recorded edit reaches file.changes without waiting for the tick. Nil does nothing.
	Nudge func(workspaceID string)
}

// OccupiedBy is the holder of the worktree a path lies in, as another session is told: no
// session id (ADR-0048 §2).
type OccupiedBy struct {
	Name  string    `json:"name,omitempty" jsonschema_description:"What the holder's client called itself, bounded to 64 characters; absent until it has said."`
	Since time.Time `json:"since" jsonschema:"required" jsonschema_description:"When the holder took the worktree."`
	Stale bool      `json:"stale,omitempty" jsonschema_description:"True while the holder has made no request for the configured time. Absent otherwise."`
}

// CheckParams are agent.checkWrite's parameters.
type CheckParams struct {
	Client string   `json:"client" jsonschema:"required,enum=claude,enum=cursor" jsonschema_description:"The agent client whose edit tool is about to write."`
	Paths  []string `json:"paths" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"The absolute paths the write touches; one to twenty, each at most 4096 bytes and without control or format characters."`
}

// CheckResult is agent.checkWrite's result.
type CheckResult struct {
	Verdict    string      `json:"verdict" jsonschema:"required,enum=allow,enum=warn,enum=ask,enum=refuse" jsonschema_description:"The most severe verdict over the paths: refuse (another chat works there and the configured conflict is refuse), ask (the same, conflict ask), warn (the holder is idle, or the caller's chat names no live MCP session, so it cannot be told from the holder), allow (anything else, including the caller's own worktree and any held one under conflict allow)."`
	OccupiedBy *OccupiedBy `json:"occupiedBy,omitempty" jsonschema_description:"The holder of the worktree that decided the verdict, when another session holds it; absent when none does or the caller is the holder's own."`
	Message    string      `json:"message,omitempty" jsonschema_description:"For refuse, ask and warn: who holds the worktree, since when, and the way out. Absent for allow."`
}

// EditFile is one file an agent's edit tool wrote.
type EditFile struct {
	Path    string `json:"path" jsonschema:"required,maxLength=4096" jsonschema_description:"The file: relative to the workspace root, slash-separated; absolute when the edit is outside."`
	Created bool   `json:"created,omitempty" jsonschema_description:"True when the edit created the file; absent when it changed one that existed or the client does not say."`
}

// Edit is agent.recordEdit's parameters and its result: the record as accepted.
type Edit struct {
	Client  string     `json:"client" jsonschema:"required,enum=claude,enum=cursor" jsonschema_description:"The agent client whose edit tool wrote the files."`
	Files   []EditFile `json:"files" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"The files written; one to twenty. Nothing of their content."`
	Outside bool       `json:"outside,omitempty" jsonschema_description:"True when the files lie outside the project and its worktrees; their paths are then absolute."`
}

func registerEdits(reg *capability.Registry, o Options) {
	reg.MustRegister(capability.Descriptor{
		Name:         "agent.checkWrite",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Statefulness: capability.Stateless,
		Audience:     capability.AudienceHuman,
		Since:        editVersion,
		Description:  "Says whether an agent's own edit tool may write the given absolute paths (refuse, ask, warn or allow), by whether another live chat works in the git worktree they lie in. The caller is the chat its connection names; it is the hook's channel before an edit, never an MCP tool.",
		Params:       CheckParams{},
		Result:       CheckResult{},
		Errors:       []capability.ErrorCode{capability.ErrInvalidParams},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return o.check(call)
		},
	})
	notDestructive := false
	reg.MustRegister(capability.Descriptor{
		Name:         "agent.recordEdit",
		Kind:         capability.Command,
		Scope:        capability.Workspace,
		Statefulness: capability.Stateless,
		Audience:     capability.AudienceHuman,
		Since:        editVersion,
		Description:  "Records that an agent's own edit tool wrote files: the client, the paths and, where the client says it, whether each was created. It is the hook's channel after an edit, never an MCP tool; it stores nothing, the record is the operation's result, shown in Activity, and the workspace's file watches walk at once.",
		Params:       Edit{},
		Result:       Edit{},
		Errors:       []capability.ErrorCode{capability.ErrInvalidParams},
		Annotations: capability.Annotations{
			Destructive: &notDestructive,
			Reason:      "changes nothing in the project or on a device; its only effects are the record in its own result and a prompt walk of the file watches",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			rec, err := validateEdit(call.Params.(Edit))
			if err != nil {
				return nil, err
			}
			if o.Nudge != nil {
				o.Nudge(call.Workspace)
			}
			return rec, nil
		},
	})
}

// editVersion is the API version of the milestone that introduces the edit capabilities
// (ADR-0094: one minor version).
const editVersion = "0.29.0"

// validPath refuses what a record or a check could not show as text: empty, over MaxPath
// bytes, invalid UTF-8, or holding a control or format character (bidirectional overrides
// among them).
func validPath(at, p string) *capability.Error {
	switch {
	case p == "":
		return invalid("%s is empty", at)
	case len(p) > MaxPath:
		return invalid("%s is %d bytes; want at most %d", at, len(p), MaxPath)
	case !utf8.ValidString(p):
		return invalid("%s is not valid UTF-8", at)
	}
	for _, r := range p {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return invalid("%s holds a control or format character", at)
		}
	}
	return nil
}

// PathOK reports whether p is a path a check or a record may carry: the hook's reader
// leaves out of both what the capabilities would refuse.
func PathOK(p string) bool { return validPath("path", p) == nil }

// validateEdit checks a record against the bounds above and returns it. A path inside the
// project is relative and stays under the root; an outside one is absolute.
func validateEdit(e Edit) (Edit, error) {
	if !oneOf(e.Client, Clients) {
		return Edit{}, invalid("client %q is not one of claude, cursor", e.Client)
	}
	if n := len(e.Files); n < 1 || n > MaxFiles {
		return Edit{}, invalid("files has %d entries; want 1 to %d", n, MaxFiles)
	}
	out := Edit{Client: e.Client, Outside: e.Outside, Files: make([]EditFile, len(e.Files))}
	for i, f := range e.Files {
		at := "files[" + strconv.Itoa(i) + "].path"
		if err := validPath(at, f.Path); err != nil {
			return Edit{}, err
		}
		switch abs := filepath.IsAbs(f.Path); {
		case e.Outside && !abs:
			return Edit{}, invalid("%s is relative, and outside paths are absolute", at)
		case !e.Outside && (abs || !inside(f.Path)):
			return Edit{}, invalid("%s must be relative and stay under the workspace root", at)
		}
		out.Files[i] = f
	}
	return out, nil
}

// inside reports whether the slash-separated relative path p stays under the root it is
// relative to, lexically.
func inside(p string) bool {
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return false
		}
	}
	return p != "." && !strings.HasPrefix(p, "/")
}

// check is agent.checkWrite's handler: the most severe verdict over the paths, with the
// holder and message of the path that decided it. Paths are canonicalised as workspace
// roots are, so /tmp and /private/tmp, a symlink, and a file in a folder not yet made, name
// the worktree they lie in.
func (o Options) check(call *capability.Call) (CheckResult, error) {
	p := call.Params.(CheckParams)
	if !oneOf(p.Client, Clients) {
		return CheckResult{}, invalid("client %q is not one of claude, cursor", p.Client)
	}
	if n := len(p.Paths); n < 1 || n > MaxFiles {
		return CheckResult{}, invalid("paths has %d entries; want 1 to %d", n, MaxFiles)
	}
	for i, path := range p.Paths {
		at := "paths[" + strconv.Itoa(i) + "]"
		if err := validPath(at, path); err != nil {
			return CheckResult{}, err
		}
		if !filepath.IsAbs(path) {
			return CheckResult{}, invalid("%s is not an absolute path", at)
		}
	}
	best := capability.WriteCheck{Verdict: capability.VerdictAllow}
	for _, path := range p.Paths {
		if o.Sessions == nil {
			break
		}
		path = filepath.Clean(path)
		if o.Canonical != nil {
			path = o.Canonical(path)
		}
		c := o.Sessions.CheckWrite(path, capability.Writer{Joined: call.Origin.Joined, Chat: call.Origin.Chat, Cursor: p.Client == "cursor"})
		// The first path to reach the highest rank decides; an allow that names a holder
		// outranks one that names none, so conflict allow still reports who holds.
		if rank(c) > rank(best) {
			best = c
		}
	}
	res := CheckResult{Verdict: best.Verdict}
	if best.Root != "" {
		res.OccupiedBy = &OccupiedBy{Name: best.Holder.Name, Since: best.Holder.Since, Stale: best.Holder.Stale}
		res.Message = message(best)
	}
	return res, nil
}

// rank orders the verdicts by severity; an allow that names its holder is above one that
// does not, so the result can say who holds what it let through.
func rank(c capability.WriteCheck) int {
	switch c.Verdict {
	case capability.VerdictRefuse:
		return 4
	case capability.VerdictAsk:
		return 3
	case capability.VerdictWarn:
		return 2
	}
	if c.Root != "" {
		return 1
	}
	return 0
}

// message is what the agent reads for a verdict other than allow: who holds the worktree,
// since when, and the way out (ADR-0094 §5).
func message(c capability.WriteCheck) string {
	who, way := c.Holder.Who(), capability.OccupiedWayOut(c.Root)
	switch c.Verdict {
	case capability.VerdictRefuse:
		return fmt.Sprintf("droidyard: %s works in %s, so this write is refused. To go on: %s.", who, c.Root, way)
	case capability.VerdictAsk:
		return fmt.Sprintf("droidyard: %s works in %s. Write there only if the user agrees; otherwise: %s.", who, c.Root, way)
	case capability.VerdictWarn:
		if c.Idle {
			return fmt.Sprintf("droidyard warning: %s holds %s and has been idle for a while. You may write, but tell the user; to keep apart: %s.", who, c.Root, way)
		}
		return fmt.Sprintf("droidyard warning: %s holds %s and droidyard cannot tell whether that is you. You may write, but tell the user; to keep apart: %s.", who, c.Root, way)
	}
	return ""
}

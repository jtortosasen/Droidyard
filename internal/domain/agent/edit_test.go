package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jtortosasen/droidyard/internal/capability"
)

func editDescriptor(t *testing.T, name string, o Options) capability.Descriptor {
	t.Helper()
	reg := capability.NewRegistry()
	Register(reg, o)
	d, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return d
}

// Decision (ADR-0095 §3): both edit capabilities are the hook's channel and never an MCP
// tool, so their audience is human (the core then refuses an MCP client). The check changes
// nothing, so it is a query: read-only and idempotent, global because the hook asks about a
// path before any workspace is open. The record's only effect is the record and a walk of the
// file watches, so it is explicitly not destructive, and it is not idempotent: each call is
// one more edit in Activity. A reversal to audience all would list them as tools, in a list
// with 189 B to spare.
func TestEditDescriptors(t *testing.T) {
	check := editDescriptor(t, "agent.checkWrite", Options{})
	a := check.ResolveAnnotations()
	if check.Kind != capability.Query || check.Scope != capability.Global || check.Statefulness != capability.Stateless || check.Audience != capability.AudienceHuman {
		t.Errorf("agent.checkWrite: kind %v, scope %v, statefulness %v, audience %v; want a stateless global query for humans", check.Kind, check.Scope, check.Statefulness, check.Audience)
	}
	if !a.ReadOnly || !a.Idempotent || a.Destructive {
		t.Errorf("agent.checkWrite annotations %+v; want read-only, idempotent, not destructive", a)
	}
	rec := editDescriptor(t, "agent.recordEdit", Options{})
	a = rec.ResolveAnnotations()
	if rec.Kind != capability.Command || rec.Scope != capability.Workspace || rec.Statefulness != capability.Stateless || rec.Audience != capability.AudienceHuman {
		t.Errorf("agent.recordEdit: kind %v, scope %v, statefulness %v, audience %v; want a stateless workspace command for humans", rec.Kind, rec.Scope, rec.Statefulness, rec.Audience)
	}
	if a.ReadOnly || a.Destructive || a.Idempotent || a.OpenWorld || a.Reason == "" {
		t.Errorf("agent.recordEdit annotations %+v; want a non-destructive, non-idempotent, closed-world command with its reason", a)
	}
	for _, d := range []capability.Descriptor{check, rec} {
		if d.Since != "0.29.0" || d.Untrusted() || len(d.Errors) != 1 || d.Errors[0] != capability.ErrInvalidParams {
			t.Errorf("%s: since %s, untrusted %v, errors %v; want 0.29.0, trusted, REQUEST_INVALID_PARAMS alone", d.Name, d.Since, d.Untrusted(), d.Errors)
		}
	}
}

// Decision (ADR-0095 §3): agent.recordEdit holds one to twenty files, each path bounded
// (4,096 bytes, no control or format characters), relative and under the root when the edit
// is in the project, absolute when it is outside; every other record is
// REQUEST_INVALID_PARAMS, so the hook can never leave a record Activity would show wrongly.
// A valid record is the result, and it nudges the call's workspace's file watches, once; an
// invalid one nudges nothing.
func TestRecordEditHandler(t *testing.T) {
	var nudged []string
	d := editDescriptor(t, "agent.recordEdit", Options{Nudge: func(ws string) { nudged = append(nudged, ws) }})
	run := func(e Edit) (any, error) {
		return d.Handler(context.Background(), &capability.Call{Workspace: "ws-1", Params: e})
	}
	files := func(paths ...string) []EditFile {
		out := make([]EditFile, len(paths))
		for i, p := range paths {
			out[i] = EditFile{Path: p}
		}
		return out
	}

	ok := Edit{Client: "claude", Files: []EditFile{{Path: "app/src/Main.kt", Created: true}, {Path: "build.gradle.kts"}}}
	got, err := run(ok)
	if err != nil {
		t.Fatalf("a valid record: %v", err)
	}
	if rec := got.(Edit); rec.Client != "claude" || len(rec.Files) != 2 || !rec.Files[0].Created || rec.Files[1].Created || rec.Outside {
		t.Errorf("the result %+v is not the record", rec)
	}
	if len(nudged) != 1 || nudged[0] != "ws-1" {
		t.Errorf("nudged %v; want the call's workspace once", nudged)
	}
	if got, err = run(Edit{Client: "cursor", Outside: true, Files: files("/tmp/x/notes.txt")}); err != nil || !got.(Edit).Outside {
		t.Errorf("an outside record: %v, %+v", err, got)
	}

	nudged = nil
	twenty, twentyOne := make([]string, MaxFiles), make([]string, MaxFiles+1)
	for i := range twentyOne {
		twentyOne[i] = "f.kt"
		if i < MaxFiles {
			twenty[i] = "f.kt"
		}
	}
	if _, err := run(Edit{Client: "claude", Files: files(twenty...)}); err != nil {
		t.Errorf("twenty files: %v", err)
	}
	bad := map[string]Edit{
		"another client":              {Client: "vim", Files: files("a.kt")},
		"no files":                    {Client: "claude"},
		"twenty-one files":            {Client: "claude", Files: files(twentyOne...)},
		"an empty path":               {Client: "claude", Files: files("")},
		"a path over the bound":       {Client: "claude", Files: files(strings.Repeat("a", MaxPath+1))},
		"a control character":         {Client: "claude", Files: files("a\nb.kt")},
		"a bidirectional override":    {Client: "claude", Files: files("a‮b.kt")},
		"invalid UTF-8":               {Client: "claude", Files: files("a\xffb.kt")},
		"an absolute path inside":     {Client: "claude", Files: files("/abs/a.kt")},
		"a path that leaves the root": {Client: "claude", Files: files("../a.kt")},
		"a path that climbs and back": {Client: "claude", Files: files("a/../../a.kt")},
		"the root itself":             {Client: "claude", Files: files(".")},
		"a relative path outside":     {Client: "claude", Outside: true, Files: files("a.kt")},
	}
	for name, e := range bad {
		_, err := run(e)
		var ce *capability.Error
		if !errors.As(err, &ce) || ce.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS", name, err)
		}
	}
	if len(nudged) != 1 {
		t.Errorf("nudged %v after twenty valid files and the invalid records; want one nudge, the valid call's", nudged)
	}
	if _, err := run(Edit{Client: "claude", Files: files(strings.Repeat("a", MaxPath))}); err != nil {
		t.Errorf("a path of exactly %d bytes: %v", MaxPath, err)
	}
}

// Decision (ADR-0095 §3): agent.checkWrite takes one to twenty absolute paths, bounded like
// a record's, and refuses anything else with REQUEST_INVALID_PARAMS, so a hook that cannot
// state its question is never answered with a verdict. With no registry of sessions every
// write is allowed.
func TestCheckWriteParams(t *testing.T) {
	d := editDescriptor(t, "agent.checkWrite", Options{})
	run := func(p CheckParams) (any, error) {
		return d.Handler(context.Background(), &capability.Call{Params: p})
	}
	got, err := run(CheckParams{Client: "claude", Paths: []string{"/p/a.kt"}})
	if err != nil || got.(CheckResult).Verdict != capability.VerdictAllow {
		t.Errorf("a path with no sessions registry: %v, %+v; want allow", err, got)
	}
	long := make([]string, MaxFiles+1)
	for i := range long {
		long[i] = "/p/a.kt"
	}
	for name, p := range map[string]CheckParams{
		"another client":        {Client: "vim", Paths: []string{"/p/a.kt"}},
		"no paths":              {Client: "claude"},
		"twenty-one paths":      {Client: "claude", Paths: long},
		"a relative path":       {Client: "claude", Paths: []string{"p/a.kt"}},
		"a control character":   {Client: "claude", Paths: []string{"/p/a\x00.kt"}},
		"a path over the bound": {Client: "claude", Paths: []string{"/" + strings.Repeat("a", MaxPath)}},
	} {
		_, err := run(p)
		var ce *capability.Error
		if !errors.As(err, &ce) || ce.Code != capability.ErrInvalidParams {
			t.Errorf("%s: %v; want REQUEST_INVALID_PARAMS", name, err)
		}
	}
}

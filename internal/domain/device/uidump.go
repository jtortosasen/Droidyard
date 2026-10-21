package device

import (
	"context"
	"strconv"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// device.uiDump (ADR-0110): the active window's full accessibility document as an artifact,
// in the format of `uiautomator dump` with the extras the reader holds, for a parser the
// caller writes. The read chain is readFull (dumpfull.go).

// UIDumpParams is device.uiDump's parameters.
type UIDumpParams struct {
	Device    string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	Invisible bool   `json:"invisible,omitempty" jsonschema_description:"Also include children not visible to the user (a scroll view's off-screen content), marked visible-to-user=\"false\"."`
}

// UIDumpResult is device.uiDump's result: the artifact's reference record under dump, and
// what the document holds, never the document. foreground is the package of the document's
// root, an app's text, so it is untrusted (ADR-0015 §1), and so is the artifact.
type UIDumpResult struct {
	Device     string       `json:"device" jsonschema:"required"`
	Dump       artifact.Ref `json:"dump" jsonschema:"required" jsonschema_description:"The XML document, an ephemeral artifact. Save it with droidyard artifact cat <ref> > dump.xml; artifact.read pages it by lines."`
	Nodes      int          `json:"nodes" jsonschema:"required" jsonschema_description:"The number of node elements in the document."`
	Foreground string       `json:"foreground" jsonschema:"required" untrusted:"true" jsonschema_description:"The package of the document's root: the app that owns the active window."`
	// Warnings are adb's (client/server version mismatch) and the one-shot reader's notice
	// (it names the persistent reader's reason, never device text): untagged, as uiTree's.
	Warnings []string `json:"warnings,omitempty"`
}

// uiDumpUnreadable is the cause of a document the reader printed that parseDump cannot read.
// The parser's own message can quote the document's elements, which are the device's, so the
// cause is fixed.
const uiDumpUnreadable = "the reader's document is not a readable hierarchy"

func registerUIDump(reg *capability.Registry, adb *ADB, store *artifact.Store) {
	// Lent (ADR-0115 Decision 1): for a remote id the host's document is fetched over the peer
	// port, parsed and stored here (remoteartifact.go).
	reg.MustRegister(adb.plainThen(capability.Descriptor{
		Name:         "device.uiDump",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Since:        "0.32.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Returns the active window's full accessibility tree, uncut and without refs, as an XML artifact in uiautomator dump's format plus extra attributes, for a parser you write; device.uiTree is the tree to act on. Save it with droidyard artifact cat <ref> > dump.xml; it holds the active window only, so a dialog's dump lacks the app under it.",
		Params:       UIDumpParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(UIDumpParams).Device) },
		Result:       UIDumpResult{},
		Text:         capability.TextOf(uiDumpText),
		Errors:       append(append([]capability.ErrorCode{}, LentErrors...), ErrUIDumpFailed, capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
		Resource: func(call *capability.Call) string {
			if s := call.Params.(UIDumpParams).Device; s != "" {
				return dumpKey(s)
			}
			return ""
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			return adb.authorize(ctx, call.Origin, call.Params.(UIDumpParams).Device, "device.uiDump")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			t := call.Authorized.(*target)
			p := call.Params.(UIDumpParams)
			if err := adb.ready(ctx, t); err != nil {
				return nil, err
			}
			return adb.uiDump(ctx, t, p, store, artifact.OwnerOf(call.Origin))
		},
	}, func(ctx context.Context, rt *remoteTarget, res any) (any, error) {
		return adb.uiDumpFetched(ctx, rt, store, res.(UIDumpResult))
	}))
}

// uiDump reads the full document and stores it unchanged. The counts come from parseDump, so
// a document it cannot read is a failed dump and no artifact is stored.
func (a *ADB) uiDump(ctx context.Context, t *target, p UIDumpParams, store *artifact.Store, owner string) (UIDumpResult, error) {
	doc, warnings, err := a.readFull(ctx, t, fullOpts{invisible: p.Invisible})
	if err != nil {
		return UIDumpResult{}, err
	}
	_, roots, perr := parseDump(doc)
	if perr != nil {
		return UIDumpResult{}, readSpec{full: &fullOpts{}}.failed(uiDumpUnreadable)
	}
	res := UIDumpResult{Device: p.Device}
	for _, r := range roots {
		res.Nodes += countNodes(r)
	}
	if len(roots) > 0 {
		res.Foreground = roots[0].pkg
	}
	ref, err := store.Put(artifact.Item{
		Data: doc, MediaType: "application/xml", Untrusted: true, Producer: "device.uiDump", Owner: owner,
	})
	if err != nil {
		return UIDumpResult{}, err
	}
	res.Dump = ref
	res.Warnings = append(append([]string(nil), t.warnings...), warnings...)
	if chromePageHidden(roots) {
		res.Warnings = append(res.Warnings, chromePageHiddenWarning)
	}
	if len(res.Warnings) == 0 {
		res.Warnings = nil
	}
	return res, nil
}

// countNodes is the node and its descendants.
func countNodes(n *rawNode) int {
	c := 1
	for _, k := range n.children {
		c += countNodes(k)
	}
	return c
}

// uiDumpText is the rendering of device.uiDump's result (ADR-0036 §2): the reference, its
// size, the counts, the quoted foreground package and how to save the document.
//
//	dump <device> <ref> application/xml <n>B nodes=<n> foreground "<package>"
//	save "droidyard artifact cat <ref> > dump.xml"
//	[warning "<text>"]...
func uiDumpText(r UIDumpResult) string {
	var t capability.Text
	t.W("dump", r.Device, r.Dump.Ref, r.Dump.MediaType, strconv.Itoa(r.Dump.Bytes)+"B", kv("nodes", int64(r.Nodes))).
		W("foreground").Q(r.Foreground).Nl()
	t.W("save").Q("droidyard artifact cat " + r.Dump.Ref + " > dump.xml").Nl()
	writeWarnings(&t, r.Warnings)
	return t.String()
}

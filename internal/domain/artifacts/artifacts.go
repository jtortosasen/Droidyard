// Package artifacts registers artifact.read, the one capability that reads every
// artifact (ADR-0012 §Reading artifacts). The store is internal/artifact's; this
// package only translates a call into a Get and a Transform or a text page. An image
// is read as bytes (maxEdge, region); anything else is read as text (offset, limit).
package artifacts

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
)

// ReadParams is artifact.read's parameters.
type ReadParams struct {
	Ref     string  `json:"ref" jsonschema:"required" jsonschema_description:"The artifact reference, as a capability returned it."`
	Offset  *int64  `json:"offset,omitempty" jsonschema_description:"Text: byte offset to read from; negative counts back from the end (landing mid-line starts at the next line)."`
	Limit   *int    `json:"limit,omitempty" jsonschema:"minimum=4,maximum=262144" jsonschema_description:"Text: most bytes to return, 4 to 262144 (default 16384); a read ends at the last complete line."`
	MaxEdge *int    `json:"maxEdge,omitempty" jsonschema:"minimum=1" jsonschema_description:"Image: the longest edge in pixels; never upscales. Absent returns the stored bytes unchanged."`
	Region  *Region `json:"region,omitempty" jsonschema_description:"Image: crop, normalised 0..1 of the stored image, applied before maxEdge."`
}

// Region is a crop in normalised coordinates: origin x, y and size w, h.
type Region struct {
	X float64 `json:"x" jsonschema:"required"`
	Y float64 `json:"y" jsonschema:"required"`
	W float64 `json:"w" jsonschema:"required"`
	H float64 `json:"h" jsonschema:"required"`
}

// Size is an image's width and height in pixels.
type Size struct {
	Width  int `json:"width" jsonschema:"required"`
	Height int `json:"height" jsonschema:"required"`
}

// ReadResult is artifact.read's result. An image fills the image fields; anything
// else fills the text ones. Data is base64 and optional so an adapter that delivers
// bytes out of band can drop it; the image's bytes are device-derived hence untrusted,
// while a text artifact carries its producer's mark in the operation's meta.
type ReadResult struct {
	Ref       string `json:"ref" jsonschema:"required"`
	MediaType string `json:"mediaType" jsonschema:"required"`
	// Image.
	Stored    *Size  `json:"stored,omitempty"`
	Delivered *Size  `json:"delivered,omitempty"`
	Data      string `json:"data,omitempty" untrusted:"true"`
	// Text (ADR-0012 §Reading artifacts).
	Offset     *int64  `json:"offset,omitempty"`
	NextOffset *int64  `json:"nextOffset,omitempty"`
	Size       *int64  `json:"size,omitempty"`
	Growing    *bool   `json:"growing,omitempty"`
	Text       *string `json:"text,omitempty"`
	Truncated  *bool   `json:"truncated,omitempty"`
}

// readText serves a text artifact: one page, per ADR-0012 §Reading artifacts. The
// image parameters are ignored, as the text parameters are on an image; limit is
// already validated by the handler.
func readText(e artifact.Entry, p ReadParams) (ReadResult, error) {
	offset := int64(0)
	if p.Offset != nil {
		offset = *p.Offset
	}
	limit := 0
	if p.Limit != nil {
		limit = *p.Limit
	}
	page, err := artifact.ReadText(e, offset, limit)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{
		Ref: e.Ref.Ref, MediaType: e.Ref.MediaType,
		Offset: &page.Offset, NextOffset: &page.NextOffset, Size: &page.Size,
		Growing: &page.Growing, Text: &page.Text, Truncated: &page.Truncated,
	}, nil
}

// Register adds artifact.read, over store.
func Register(reg *capability.Registry, store *artifact.Store) {
	reg.MustRegister(capability.Descriptor{
		Name:        "artifact.read",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.4.0",
		Description: "Reads an artifact by the reference another tool returned (a screenshot, a build console, shell output): an image can be resized or cropped, text is paged. Ephemeral references expire after 15 minutes or a daemon restart.",
		Params:      ReadParams{},
		Result:      ReadResult{},
		Errors:      []capability.ErrorCode{capability.ErrArtifactNotFound, capability.ErrArtifactExpired, capability.ErrInvalidParams},
		// A retained result drops bulk bytes once delivered (ADR-0020 §Retention): the
		// first answer carries the bytes (data or text), a later operation.wait returns
		// the metadata and the caller re-reads the reference.
		Slim: func(r any) any {
			if rr, ok := r.(ReadResult); ok {
				rr.Data = ""
				rr.Text = nil
				return rr
			}
			return r
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(ReadParams)
			// Both parameter groups are validated before the reference is resolved, so
			// an invalid parameter is REQUEST_INVALID_PARAMS whatever the artifact.
			if p.Limit != nil && (*p.Limit < artifact.MinTextLimit || *p.Limit > artifact.MaxTextLimit) {
				return nil, capability.Errorf(capability.ErrInvalidParams, "limit must be between %d and %d, got %d", artifact.MinTextLimit, artifact.MaxTextLimit, *p.Limit)
			}
			maxEdge := 0
			if p.MaxEdge != nil {
				if *p.MaxEdge < 1 {
					return nil, capability.Errorf(capability.ErrInvalidParams, "maxEdge must be at least 1, got %d", *p.MaxEdge)
				}
				maxEdge = *p.MaxEdge
			}
			e, err := store.Get(p.Ref)
			if err != nil {
				return nil, err
			}
			// The result carries the artifact's own mark: an artifact inherits its
			// producer's (ADR-0015 §1). A console is project content and unmarked.
			u := e.Ref.Untrusted
			call.Untrusted = &u
			if !strings.HasPrefix(e.Ref.MediaType, "image/") {
				return readText(e, p)
			}
			var region *artifact.Region
			if p.Region != nil {
				region = &artifact.Region{X: p.Region.X, Y: p.Region.Y, W: p.Region.W, H: p.Region.H}
			}
			data, mt, w, h, err := artifact.Transform(ctx, e.Data, e.Ref.MediaType, maxEdge, region)
			if err != nil {
				return nil, err
			}
			return ReadResult{
				Ref: e.Ref.Ref, MediaType: mt,
				Stored:    &Size{Width: e.Ref.Width, Height: e.Ref.Height},
				Delivered: &Size{Width: w, Height: h},
				Data:      base64.StdEncoding.EncodeToString(data),
			}, nil
		},
	})
}

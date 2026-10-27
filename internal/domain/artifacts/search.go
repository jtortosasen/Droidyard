package artifacts

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/artifact"
	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/textfilter"
)

// Search bounds (ADR-0136 §1).
const (
	searchContextMax          = 10
	searchMaxLinesMax         = 500
	searchMaxLinesDefault     = 50
	searchMaxLineBytesMin     = 16
	searchMaxLineBytesMax     = 4096
	searchMaxLineBytesDefault = 512
)

// SearchParams is artifact.search's parameters.
type SearchParams struct {
	Ref          string   `json:"ref" jsonschema:"required" jsonschema_description:"The text artifact's reference, as a capability returned it (a build console, a logs capture, shell output)."`
	Match        []string `json:"match" jsonschema:"required,minItems=1,maxItems=20" jsonschema_description:"1 to 20 RE2 patterns; a line passes if any matches. (?i) ignores case. Applied on the host, line by line."`
	Exclude      []string `json:"exclude,omitempty" jsonschema:"maxItems=20" jsonschema_description:"At most 20 RE2 patterns; a line any of them matches is dropped."`
	Context      *int     `json:"context,omitempty" jsonschema:"minimum=0,maximum=10" jsonschema_description:"Lines before and after each match, as grep -C (default 0, at most 10); overlapping windows merge."`
	MaxLines     *int     `json:"maxLines,omitempty" jsonschema:"minimum=1,maximum=500" jsonschema_description:"Most lines returned, context included, 1 to 500 (default 50)."`
	MaxLineBytes *int     `json:"maxLineBytes,omitempty" jsonschema:"minimum=16,maximum=4096" jsonschema_description:"Most bytes of one line's text, 16 to 4096 (default 512); a longer line is cut and marked cut."`
	Keep         string   `json:"keep,omitempty" jsonschema:"enum=oldest,enum=newest" jsonschema_description:"Which end survives maxLines: oldest (default) or newest."`
	Offset       *int64   `json:"offset,omitempty" jsonschema:"minimum=0" jsonschema_description:"Byte offset to start from: 0 or an earlier nextOffset. A call scans at most 8 MiB or 5 seconds; nextOffset continues it."`
}

// SearchLine is one returned line, numbered from the artifact's first line.
type SearchLine struct {
	Line    int    `json:"line" jsonschema:"required"`
	Text    string `json:"text" jsonschema:"required"`
	Context bool   `json:"context,omitempty" jsonschema_description:"True for a neighbour of a match."`
	Cut     bool   `json:"cut,omitempty" jsonschema_description:"True when text is the start of a longer line."`
}

// SearchResult is artifact.search's result. The artifact's untrusted mark is the call's,
// as artifact.read's is.
type SearchResult struct {
	Ref        string       `json:"ref" jsonschema:"required"`
	MediaType  string       `json:"mediaType" jsonschema:"required"`
	Lines      []SearchLine `json:"lines" jsonschema:"required"`
	Matched    int          `json:"matched" jsonschema:"required" jsonschema_description:"Matching lines in the range scanned, returned or not."`
	Returned   int          `json:"returned" jsonschema:"required" jsonschema_description:"Entries in lines, context included."`
	Scanned    int64        `json:"scanned" jsonschema:"required" jsonschema_description:"Bytes this call went through, from offset."`
	Size       int64        `json:"size" jsonschema:"required" jsonschema_description:"The artifact's size in bytes."`
	NextOffset *int64       `json:"nextOffset,omitempty" jsonschema_description:"Where to continue when the call stopped before the end (8 MiB or 5 seconds, or a growing artifact's unfinished last line)."`
	Growing    bool         `json:"growing" jsonschema:"required"`
	Truncated  bool         `json:"truncated" jsonschema:"required" jsonschema_description:"True when maxLines dropped lines."`
}

// searchText renders N:text for a match and N-text for context, the text always quoted
// (an artifact's lines are whatever its producer wrote).
func searchText(r SearchResult) string {
	var t capability.Text
	t.W("search", r.Ref, "matched="+strconv.Itoa(r.Matched), "returned="+strconv.Itoa(r.Returned),
		"scanned="+strconv.FormatInt(r.Scanned, 10)+"/"+strconv.FormatInt(r.Size, 10))
	if r.NextOffset != nil {
		t.W("next=" + strconv.FormatInt(*r.NextOffset, 10))
	}
	if r.Growing {
		t.W("growing")
	}
	if r.Truncated {
		t.W("truncated")
	}
	t.Nl()
	for _, l := range r.Lines {
		sep := ":"
		if l.Context {
			sep = "-"
		}
		t.Pre(strconv.Itoa(l.Line)+sep, l.Text)
		if l.Cut {
			t.W("cut")
		}
		t.Nl()
	}
	return t.String()
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func inRange(field string, p *int, lo, hi int) error {
	if p != nil && (*p < lo || *p > hi) {
		return capability.Errorf(capability.ErrInvalidParams, "%s must be between %d and %d, got %d", field, lo, hi, *p)
	}
	return nil
}

// searchBudget is the time budget of a call; tests set it. nil is textfilter.FilterBudget.
var searchBudget *time.Duration

func registerSearch(reg *capability.Registry, store *artifact.Store) {
	reg.MustRegister(capability.Descriptor{
		Name:         "artifact.search",
		Kind:         capability.Query,
		Scope:        capability.Global,
		Statefulness: capability.Stateless,
		Since:        "0.41.0",
		Description:  "Searches a text artifact (a build console, a logs capture, shell output) with RE2 patterns, as grep -n -C does: matching lines with their numbers, context, and nextOffset when a call stopped at 8 MiB or 5 seconds. Use it instead of paging with artifact.read; an image is read with artifact.read.",
		Params:       SearchParams{},
		Result:       SearchResult{},
		Text:         capability.TextOf(searchText),
		Errors:       []capability.ErrorCode{capability.ErrArtifactNotFound, capability.ErrArtifactExpired, capability.ErrInvalidParams},
		// A retained result drops its lines once delivered (ADR-0020 §Retention); the
		// caller searches again.
		Slim: func(r any) any {
			if sr, ok := r.(SearchResult); ok {
				sr.Lines = nil
				return sr
			}
			return r
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(SearchParams)
			// Every parameter is validated before the reference is resolved, so a bad
			// pattern is REQUEST_INVALID_PARAMS whatever the artifact.
			if len(p.Match) < 1 {
				return nil, capability.Errorf(capability.ErrInvalidParams, "match needs at least one pattern")
			}
			filter, err := textfilter.Compile(textfilter.Spec{Match: p.Match, Exclude: p.Exclude})
			if err != nil {
				return nil, err
			}
			for _, c := range []struct {
				name   string
				v      *int
				lo, hi int
			}{
				{"context", p.Context, 0, searchContextMax},
				{"maxLines", p.MaxLines, 1, searchMaxLinesMax},
				{"maxLineBytes", p.MaxLineBytes, searchMaxLineBytesMin, searchMaxLineBytesMax},
			} {
				if err := inRange(c.name, c.v, c.lo, c.hi); err != nil {
					return nil, err
				}
			}
			keep := textfilter.Oldest
			switch p.Keep {
			case "", "oldest":
			case "newest":
				keep = textfilter.Newest
			default:
				return nil, capability.Errorf(capability.ErrInvalidParams, "keep must be oldest or newest, got %q", p.Keep)
			}
			var offset int64
			if p.Offset != nil {
				if *p.Offset < 0 {
					return nil, capability.Errorf(capability.ErrInvalidParams, "offset must not be negative, got %d", *p.Offset)
				}
				offset = *p.Offset
			}

			e, err := store.Get(p.Ref)
			if err != nil {
				return nil, err
			}
			u := e.Ref.Untrusted
			call.Untrusted = &u
			if strings.HasPrefix(e.Ref.MediaType, "image/") {
				return nil, capability.Errorf(capability.ErrInvalidParams, "%s is an image; read an image with artifact.read", p.Ref).
					WithNextStep("call artifact.read with this ref")
			}
			out, err := SearchText(ctx, e, SearchQuery{
				Filter: filter, Context: intOr(p.Context, 0),
				MaxLines: intOr(p.MaxLines, searchMaxLinesDefault), MaxLineBytes: intOr(p.MaxLineBytes, searchMaxLineBytesDefault),
				Keep: keep, Offset: offset, Budget: searchBudget,
			})
			if errors.Is(err, ErrSearchOffset) {
				return nil, capability.Errorf(capability.ErrInvalidParams, "offset %d is not 0 or the start of a line; use a nextOffset an earlier call returned", offset)
			}
			if err != nil {
				return nil, err
			}
			res := SearchResult{
				Ref: e.Ref.Ref, MediaType: e.Ref.MediaType, Lines: make([]SearchLine, 0, len(out.Lines)),
				Matched: out.Matched, Returned: len(out.Lines), Scanned: out.Scanned, Size: out.Size,
				Growing: out.Growing, Truncated: out.Truncated,
			}
			for _, l := range out.Lines {
				res.Lines = append(res.Lines, SearchLine{Line: l.Line, Text: l.Text, Context: l.Context, Cut: l.Cut})
			}
			if out.HasNext {
				n := out.NextOffset
				res.NextOffset = &n
			}
			return res, nil
		},
	})
}

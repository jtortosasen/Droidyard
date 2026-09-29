package capability

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// namePattern is the canonical capability name: "<domain>.<verb>", lower camel after
// the dot, never an underscore, so the MCP mapping (dot to underscore) is reversible
// (docs/architecture/capability-api.md §Naming and namespaces).
var namePattern = regexp.MustCompile(`^[a-z]+\.[a-z][A-Za-z0-9]*$`)

// Registry holds every capability the core exposes. It is the single source for
// dispatch, JSON Schema generation, MCP tool definitions and the CLI.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Descriptor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Descriptor)}
}

// Register adds a capability. It refuses anything that would make the published
// contract ambiguous, so a mistake is a startup failure rather than a schema that
// cannot be generated.
func (r *Registry) Register(d Descriptor) error {
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf("capability %q: name must match %s", d.Name, namePattern)
	}
	if d.Handler == nil {
		return fmt.Errorf("capability %q: no handler", d.Name)
	}
	if d.Params == nil || d.Result == nil {
		return fmt.Errorf("capability %q: Params and Result are required to generate a schema", d.Name)
	}
	if d.Description == "" {
		return fmt.Errorf("capability %q: a description is required; it is what the agent reads", d.Name)
	}
	if d.Annotations.Destructive != nil || d.Annotations.OpenWorld != nil {
		if strings.TrimSpace(d.Annotations.Reason) == "" {
			return fmt.Errorf("capability %q: an annotation override requires a reason", d.Name)
		}
	}
	if d.Audience == AudienceHuman && d.Scope == Global {
		// Not an error by itself, but worth stating: human-only capabilities are
		// rejected at the MCP transport, not hidden (ADR-0015 §6).
		_ = d
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[d.Name]; exists {
		return fmt.Errorf("capability %q: already registered", d.Name)
	}
	r.byName[d.Name] = d
	return nil
}

// MustRegister panics on a registration error. For use in package init of domain
// packages, where a failure is a programming mistake.
func (r *Registry) MustRegister(d Descriptor) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// Lookup returns a capability by name.
func (r *Registry) Lookup(name string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.byName[name]
	return d, ok
}

// All returns every capability, sorted by name so the generated schema and the docs
// are stable.
func (r *Registry) All() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.byName))
	for _, d := range r.byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResolvedAnnotations are the four hints an MCP client receives. Read-only and
// idempotent are derived and cannot be overridden; destructive defaults to true for
// commands, because MCP's own default for an unannotated tool is more permissive
// than ours (docs/interfaces/mcp-tool-surface.md §Annotations).
type ResolvedAnnotations struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	Reason      string
}

// ResolveAnnotations applies the derivation rules to one descriptor.
func (d Descriptor) ResolveAnnotations() ResolvedAnnotations {
	readOnly := d.Kind == Query

	destructive := d.Kind == Command
	if d.Annotations.Destructive != nil {
		destructive = *d.Annotations.Destructive
	}

	openWorld := len(d.Availability) > 0
	if d.Annotations.OpenWorld != nil {
		openWorld = *d.Annotations.OpenWorld
	}

	return ResolvedAnnotations{
		ReadOnly:    readOnly,
		Destructive: destructive,
		Idempotent:  readOnly || d.Idempotent,
		OpenWorld:   openWorld,
		Reason:      d.Annotations.Reason,
	}
}

package capability

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// namePattern is the canonical capability name: "<domain>.<verb>", lower camel after
// the dot, never an underscore, so the MCP mapping (dot to underscore) is reversible
// (docs/architecture/capability-api.md §Naming and namespaces).
var namePattern = regexp.MustCompile(`^[a-z]+\.[a-z][A-Za-z0-9]*$`)

var (
	// topicPattern is a stream's topic: "<domain>.<noun>", as a capability name is.
	topicPattern = regexp.MustCompile(`^[a-z]+\.[a-z][A-Za-z0-9]*$`)
	// eventTypePattern is an event type's name inside its topic.
	eventTypePattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// reservedTopics are the topics a capability's own stream may not take: the ones
// events.subscribe serves (the workspace registry names workspace.lifecycle itself, since
// the core does not import the bus, and event.TopicGap is the marker every subscription
// receives). A stream on one of them would reach subscribers it was never meant for.
var reservedTopics = []string{"workspace.lifecycle", ProgressTopic, "events.gap"}

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
	untrusted, err := deriveUntrusted(d.Result)
	if err != nil {
		return fmt.Errorf("capability %q: result: %w", d.Name, err)
	}
	d.untrusted = untrusted
	if d.Annotations.Destructive != nil || d.Annotations.OpenWorld != nil {
		if strings.TrimSpace(d.Annotations.Reason) == "" {
			return fmt.Errorf("capability %q: an annotation override requires a reason", d.Name)
		}
	}
	if d.Events != nil {
		if err := validateEvents(d); err != nil {
			return err
		}
	}
	if d.Binding && (d.Statefulness != Stateless || d.Audience != AudienceAll) {
		// The binding belongs to an MCP session, so only a tool can move it: a stateful
		// or human-only capability is never one, and the flag would promise a call no
		// connection can make (ADR-0082 §4).
		return fmt.Errorf("capability %q: Binding is for a stateless capability of every audience, the MCP tool that moves a session's binding", d.Name)
	}
	if d.Occupies != "" && d.Occupies != OccupiesAct && d.Occupies != OccupiesBoot {
		return fmt.Errorf("capability %q: Occupies %q is neither %q nor %q", d.Name, d.Occupies, OccupiesAct, OccupiesBoot)
	}
	if d.Audience == AudienceHuman && d.Scope == Global {
		// Not an error by itself, but worth stating: human-only capabilities are
		// rejected at the MCP transport, not hidden (ADR-0015 §6).
		_ = d
	}

	if d.unrecorded && (d.Resource != nil || d.Authorize != nil) {
		// Invoke runs a non-recorded handler directly: no lock, no re-check.
		return fmt.Errorf("capability %q: a non-recorded capability runs outside the scheduler, so it cannot set Resource or Authorize", d.Name)
	}
	if d.Resource != nil && !d.LongRunning && !slices.Contains(d.Errors, ErrOperationQueueTimeout) {
		// A call that can queue and is not long-running can fail at the queue bound
		// (ADR-0020 §Queue bound), so the published contract must list the code.
		return fmt.Errorf("capability %q: takes a resource lock and is not long-running, so Errors must list %s", d.Name, ErrOperationQueueTimeout)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[d.Name]; exists {
		return fmt.Errorf("capability %q: already registered", d.Name)
	}
	if d.Events != nil {
		for _, other := range r.byName {
			if other.Events != nil && other.Events.Topic == d.Events.Topic {
				return fmt.Errorf("capability %q: events topic %q is already published by %s", d.Name, d.Events.Topic, other.Name)
			}
		}
	}
	r.byName[d.Name] = d
	return nil
}

// validateEvents refuses a stream declaration the published schema could not state
// cleanly: only a streaming capability owns a stream, its topic is well formed and not
// one events.subscribe serves, and every type is a named struct whose untrusted tags are
// valid (ADR-0041 §4).
func validateEvents(d Descriptor) error {
	e := d.Events
	if d.Statefulness != Streaming {
		return fmt.Errorf("capability %q: Events declared on a %s capability; only a streaming one owns a stream", d.Name, d.Statefulness)
	}
	if !topicPattern.MatchString(e.Topic) {
		return fmt.Errorf("capability %q: events topic %q must match %s", d.Name, e.Topic, topicPattern)
	}
	if slices.Contains(reservedTopics, e.Topic) {
		return fmt.Errorf("capability %q: events topic %q is served by events.subscribe and cannot be a capability's own stream", d.Name, e.Topic)
	}
	if len(e.Types) == 0 {
		return fmt.Errorf("capability %q: Events declares topic %q with no types", d.Name, e.Topic)
	}
	for name, payload := range e.Types {
		if !eventTypePattern.MatchString(name) {
			return fmt.Errorf("capability %q: event type %q must match %s", d.Name, name, eventTypePattern)
		}
		if payload == nil || reflect.TypeOf(payload).Kind() != reflect.Struct {
			return fmt.Errorf("capability %q: event type %q: the payload must be a struct value", d.Name, name)
		}
		if _, err := deriveUntrusted(payload); err != nil {
			return fmt.Errorf("capability %q: event type %q: %w", d.Name, name, err)
		}
	}
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
// than ours (docs/interfaces/mcp.md §Annotations).
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

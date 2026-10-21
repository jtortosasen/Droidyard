package cli

// The command tree: `droidyard <domain> <verb>` derived from the registry's
// descriptors, with flags derived from each capability's input schema. The tree is
// data (a Spec per capability, a Hand per hand-written command), so it holds whatever
// a build registers, a binary with two domains gets a tree of two, and help works
// with no daemon. Nothing here is contract: the CLI derives everything from what the
// descriptors already carry, and what it cannot derive is hand-written by the caller.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// Spec is one capability as the CLI sees it. The caller builds it from a descriptor;
// Input is that capability's schema only (schema.Input), so a command reflects one
// schema and not the registry's.
type Spec struct {
	Name         string // "<domain>.<verb>", as registered
	Kind         capability.Kind
	Scope        capability.Scope
	Statefulness capability.Statefulness
	LongRunning  bool
	Binding      bool
	Audience     capability.Audience
	Annotations  capability.ResolvedAnnotations
	Description  string
	Errors       []string
	Input        json.RawMessage
	// UntrustedEvents says the capability's stream carries device text.
	UntrustedEvents bool
	// Text renders a result for a person where the descriptor has a renderer (used by
	// the output step, not by the tree).
	Text func(json.RawMessage) string
}

// Hand is a hand-written command: a top-level name ("init") or a verb of a derived
// domain ("device allow"), with its own help and its own argument handling.
type Hand struct {
	Name    string
	Summary string // one line, for a domain's listing
	Help    string // the command's own help text
	// OwnHelp: the command parses --help itself (trash, whose operands are an rm's;
	// hook, whose arguments an assistant's entry fixes), so the tree never intercepts it.
	OwnHelp bool
	Run     func(args []string) int
}

// Section is a block of the index text the caller writes (where to start, the hand
// commands by section, the raw door).
type Section struct {
	Title string
	Body  string
}

// UsageError is a malformed command line: exit status ExitUsage, nothing dialled.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) *UsageError { return &UsageError{Msg: fmt.Sprintf(format, a...)} }

// Tree is the resolved command set.
type Tree struct {
	// Before and After are the index's hand-written sections, around the generated
	// list of domains and their verbs.
	Before, After []Section

	specs   map[string]*Spec // by normalised "domain.verb"
	domains map[string]*domain
	hands   map[string]*Hand // top-level hand commands, by exact name
}

type domain struct {
	name  string
	verbs map[string]*Spec // by normalised verb
	hands map[string]*Hand // hand verbs, by normalised verb
}

// hidden says whether a capability is left out of the listings (it still runs): the
// MCP-only binding capabilities, the stateful ones that are not streams (a CLI
// command is one connection, so there is nothing of its own to end), the hooks'
// channel (the agent domain) and the live screen, whose video travels only over HTTP.
func hidden(s Spec) bool {
	d, _ := splitName(s.Name)
	return s.Binding ||
		s.Statefulness == capability.Stateful ||
		d == "agent" ||
		s.Name == "device.watchScreen"
}

// norm is the matching rule: case and hyphens are ignored.
func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ""))
}

func splitName(name string) (dom, verb string) {
	d, v, _ := strings.Cut(name, ".")
	return d, v
}

// kebab spells a camelCase name with hyphens: uiTree is ui-tree, timeoutMs timeout-ms.
func kebab(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				prev := r[i-1]
				prevLower := (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')
				nextLower := i+1 < len(r) && r[i+1] >= 'a' && r[i+1] <= 'z'
				if prevLower || (prev >= 'A' && prev <= 'Z' && nextLower) {
					b.WriteByte('-')
				}
			}
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}
	return b.String()
}

// NewTree builds the tree and refuses what would make a command ambiguous: two verbs
// of a domain that normalise equal, a hand verb that a capability's verb shadows, a
// hand command named like a capability domain.
func NewTree(specs []Spec, hands []Hand) (*Tree, error) {
	t := &Tree{
		specs:   map[string]*Spec{},
		domains: map[string]*domain{},
		hands:   map[string]*Hand{},
	}
	for i := range specs {
		s := &specs[i]
		dn, vn := splitName(s.Name)
		if dn == "" || vn == "" {
			return nil, fmt.Errorf("capability %q is not <domain>.<verb>", s.Name)
		}
		d := t.domain(dn)
		if prev, dup := d.verbs[norm(vn)]; dup {
			return nil, fmt.Errorf("capabilities %s and %s are the same command (%s %s): matching ignores case and hyphens", prev.Name, s.Name, dn, kebab(vn))
		}
		d.verbs[norm(vn)] = s
		t.specs[norm(dn)+"."+norm(vn)] = s
	}
	for i := range hands {
		h := &hands[i]
		fields := strings.Fields(h.Name)
		switch len(fields) {
		case 1:
			if _, dup := t.hands[h.Name]; dup {
				return nil, fmt.Errorf("hand command %q registered twice", h.Name)
			}
			t.hands[h.Name] = h
		case 2:
			d := t.domain(fields[0])
			if prev, dup := d.verbs[norm(fields[1])]; dup {
				return nil, fmt.Errorf("hand command %q is shadowed by capability %s: one of them must be renamed", h.Name, prev.Name)
			}
			if _, dup := d.hands[norm(fields[1])]; dup {
				return nil, fmt.Errorf("hand command %q registered twice", h.Name)
			}
			d.hands[norm(fields[1])] = h
		default:
			return nil, fmt.Errorf("hand command %q: a name is one word or <domain> <verb>", h.Name)
		}
	}
	for name, h := range t.hands {
		if _, clash := t.domains[norm(name)]; clash {
			return nil, fmt.Errorf("hand command %q is also a capability domain: the tree would shadow one of them", h.Name)
		}
	}
	return t, nil
}

func (t *Tree) domain(name string) *domain {
	d, ok := t.domains[norm(name)]
	if !ok {
		d = &domain{name: name, verbs: map[string]*Spec{}, hands: map[string]*Hand{}}
		t.domains[norm(name)] = d
	}
	return d
}

// Resolved is what a command line named: a hand command, a derived command, or only
// a domain (its help).
type Resolved struct {
	Hand   *Hand
	Spec   *Spec
	Domain string
	Rest   []string
}

// Resolve reads the arguments before the command's own: `device list`,
// `device ui-tree`, `device uiTree` and `device.uiTree` resolve alike. A name nothing
// matches is a usage error that names the nearest ones.
func (t *Tree) Resolve(args []string) (Resolved, error) {
	if len(args) == 0 {
		return Resolved{}, usagef("no command named; see: droidyard help")
	}
	first := args[0]
	if h, ok := t.hands[first]; ok {
		return Resolved{Hand: h, Rest: args[1:]}, nil
	}
	if dn, vn, dotted := strings.Cut(first, "."); dotted {
		if s, ok := t.specs[norm(dn)+"."+norm(vn)]; ok {
			return Resolved{Spec: s, Rest: args[1:]}, nil
		}
		return Resolved{}, usagef("unknown command %q%s", first, t.didYouMean(first))
	}
	d, ok := t.domains[norm(first)]
	if !ok {
		return Resolved{}, usagef("unknown command %q%s", first, t.didYouMean(first))
	}
	if len(args) == 1 || args[1] == "-h" || args[1] == "--help" {
		return Resolved{Domain: d.name}, nil
	}
	if s, ok := d.verbs[norm(args[1])]; ok {
		return Resolved{Spec: s, Rest: args[2:]}, nil
	}
	if h, ok := d.hands[norm(args[1])]; ok {
		return Resolved{Hand: h, Rest: args[2:]}, nil
	}
	return Resolved{}, usagef("unknown command %q in %s%s", args[1], d.name, nearest(args[1], d.verbNames()))
}

func (d *domain) verbNames() []string {
	var names []string
	for _, s := range d.verbs {
		_, v := splitName(s.Name)
		names = append(names, kebab(v))
	}
	for _, h := range d.hands {
		names = append(names, strings.Fields(h.Name)[1])
	}
	sort.Strings(names)
	return names
}

func (t *Tree) didYouMean(typed string) string {
	var names []string
	for n := range t.hands {
		names = append(names, n)
	}
	for _, d := range t.domains {
		names = append(names, d.name)
	}
	sort.Strings(names)
	return nearest(typed, names)
}

// nearest renders the (up to three) candidates closest to typed, or the hint to read
// the help.
func nearest(typed string, names []string) string {
	type cand struct {
		n string
		d int
	}
	var cs []cand
	nt := norm(typed)
	for _, n := range names {
		nn := norm(n)
		d := editDistance(nt, nn)
		if strings.HasPrefix(nn, nt) || strings.HasPrefix(nt, nn) {
			d = 0
		}
		if d <= 2 || d <= len(nn)/3 {
			cs = append(cs, cand{n, d})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	if len(cs) > 3 {
		cs = cs[:3]
	}
	if len(cs) == 0 {
		return "; see: droidyard help"
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.n)
	}
	return "; did you mean: " + strings.Join(out, ", ") + "?"
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ---------------------------------------------------------------------------------
// The input schema as flags.

// schemaNode is the part of a JSON Schema node the CLI reads.
type schemaNode struct {
	typ      string
	desc     string
	enum     []string
	def      string
	items    *schemaNode
	props    []schemaProp // declaration order
	required []string
	closed   bool // additionalProperties is false: a fixed set of keys
	complex  bool // a union, a reference, a typeless value
}

type schemaProp struct {
	name string
	node *schemaNode
}

func parseNode(raw json.RawMessage) (*schemaNode, error) {
	var a struct {
		Type                 json.RawMessage   `json:"type"`
		Description          string            `json:"description"`
		Enum                 []json.RawMessage `json:"enum"`
		Default              json.RawMessage   `json:"default"`
		Items                json.RawMessage   `json:"items"`
		Properties           json.RawMessage   `json:"properties"`
		Required             []string          `json:"required"`
		AdditionalProperties json.RawMessage   `json:"additionalProperties"`
		OneOf                []json.RawMessage `json:"oneOf"`
		AnyOf                []json.RawMessage `json:"anyOf"`
		AllOf                []json.RawMessage `json:"allOf"`
		Ref                  string            `json:"$ref"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	n := &schemaNode{desc: a.Description, required: a.Required}
	if len(a.Type) == 0 || json.Unmarshal(a.Type, &n.typ) != nil {
		n.complex = true
	}
	if len(a.OneOf)+len(a.AnyOf)+len(a.AllOf) > 0 || a.Ref != "" {
		n.complex = true
	}
	for _, e := range a.Enum {
		var s string
		if json.Unmarshal(e, &s) == nil {
			n.enum = append(n.enum, s)
		} else {
			n.enum = append(n.enum, string(e))
		}
	}
	if len(a.Default) > 0 {
		var s string
		if json.Unmarshal(a.Default, &s) == nil {
			n.def = s
		} else {
			n.def = string(a.Default)
		}
	}
	n.closed = string(bytes.TrimSpace(a.AdditionalProperties)) == "false"
	if len(a.Items) > 0 {
		it, err := parseNode(a.Items)
		if err != nil {
			return nil, err
		}
		n.items = it
	}
	if len(a.Properties) > 0 {
		dec := json.NewDecoder(bytes.NewReader(a.Properties))
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			var sub json.RawMessage
			if err := dec.Decode(&sub); err != nil {
				return nil, err
			}
			pn, err := parseNode(sub)
			if err != nil {
				return nil, err
			}
			n.props = append(n.props, schemaProp{name: k.(string), node: pn})
		}
	}
	return n, nil
}

func (n *schemaNode) scalar() bool {
	if n.complex {
		return false
	}
	switch n.typ {
	case "string", "integer", "number", "boolean":
		return true
	}
	return false
}

func (n *schemaNode) scalarArray() bool {
	return !n.complex && n.typ == "array" && n.items != nil && n.items.scalar()
}

func (n *schemaNode) isRequired(name string) bool {
	for _, r := range n.required {
		if r == name {
			return true
		}
	}
	return false
}

// flagDef is one flag derived from a parameter (or one property of an object parameter).
type flagDef struct {
	name     string // "device", "build.module"
	key, sub string // the parameter, and the property inside an object parameter
	kind     string // string, integer, number, boolean
	repeated bool
	enum     []string
	required bool // a top-level required parameter
	desc     string
	def      string
}

type input struct {
	root    *schemaNode
	flags   []*flagDef
	byName  map[string]*flagDef // by normalised flag name
	only    []schemaProp        // parameters reachable only through --params
	onlyAt  []string            // dotted paths inside an object parameter, same
	objects map[string]*schemaNode
}

func scalarFlag(name, key, sub string, n *schemaNode, required bool) *flagDef {
	f := &flagDef{name: name, key: key, sub: sub, required: required, desc: n.desc, enum: n.enum, def: n.def}
	if n.scalarArray() {
		f.repeated = true
		f.kind = n.items.typ
		f.enum = n.items.enum
	} else {
		f.kind = n.typ
	}
	return f
}

func derive(s Spec) (*input, error) {
	in := &input{byName: map[string]*flagDef{}, objects: map[string]*schemaNode{}}
	if len(s.Input) == 0 {
		in.root = &schemaNode{typ: "object"}
		return in, nil
	}
	root, err := parseNode(s.Input)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Name, err)
	}
	in.root = root
	for _, p := range root.props {
		key, n := p.name, p.node
		fname := kebab(key)
		switch {
		case n.scalar() || n.scalarArray():
			in.add(scalarFlag(fname, key, "", n, root.isRequired(key)))
		case !n.complex && n.typ == "object" && n.closed && len(n.props) > 0:
			in.objects[key] = n
			hasFlag := false
			for _, sp := range n.props {
				if sp.node.scalar() || sp.node.scalarArray() {
					in.add(scalarFlag(fname+"."+kebab(sp.name), key, sp.name, sp.node, false))
					hasFlag = true
				} else {
					in.onlyAt = append(in.onlyAt, fname+"."+kebab(sp.name))
				}
			}
			if !hasFlag {
				in.only = append(in.only, p)
			}
		default:
			in.only = append(in.only, p)
		}
	}
	return in, nil
}

func (in *input) add(f *flagDef) {
	in.flags = append(in.flags, f)
	if _, dup := in.byName[norm(f.name)]; !dup {
		in.byName[norm(f.name)] = f
	}
}

// reserved says which of the CLI's own flags a derived command carries.
func reserved(s Spec) []string {
	names := []string{"json", "params", "no-autostart", "help"}
	if s.Scope == capability.Workspace {
		names = append(names, "workspace")
	}
	if s.LongRunning {
		names = append(names, "detach")
	}
	return names
}

// CheckSpec refuses a capability whose parameters cannot be flags without ambiguity:
// two parameters (or properties) that normalise to the same flag, or one that takes a
// flag the CLI reserves for itself. A test over the shipped registry calls it for every
// capability.
func CheckSpec(s Spec) error {
	in, err := derive(s)
	if err != nil {
		return err
	}
	seen := map[string]string{}
	for _, f := range in.flags {
		if prev, dup := seen[norm(f.name)]; dup {
			return fmt.Errorf("%s: parameters %s and %s are the same flag", s.Name, prev, f.name)
		}
		seen[norm(f.name)] = f.name
	}
	for _, r := range reserved(s) {
		if f, clash := seen[norm(r)]; clash {
			return fmt.Errorf("%s: parameter %s collides with the CLI's own --%s", s.Name, f, r)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------
// Parsing a derived command.

// Derived is a parsed derived command: the call to make and how to print it.
type Derived struct {
	Call Call
	JSON bool
	Help bool // --help was given; Call is not valid
}

// ParseDerived reads the arguments after a derived command's name: flags from the
// capability's parameters (D3), --params as the base object (D4), the CLI's own flags,
// and ANDROID_SERIAL for a required device (D6), all before any daemon is dialled. getenv
// is injected.
func ParseDerived(s Spec, args []string, getenv func(string) string) (Derived, error) {
	in, err := derive(s)
	if err != nil {
		return Derived{}, err
	}
	var d Derived
	d.Call.Capability = s.Name
	var (
		paramsRaw json.RawMessage
		scalars   = map[string]json.RawMessage{}
		arrays    = map[string][]json.RawMessage{}
		subs      = map[string]map[string]json.RawMessage{}
		subArrays = map[string]map[string][]json.RawMessage{}
		given     = map[string]bool{}
		helpSeen  bool
	)
	// A workspace parameter on a global capability is --workspace, a parameter; on a
	// workspace-scoped one it is the envelope's.
	envWorkspace := s.Scope == capability.Workspace
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" {
			helpSeen = true
			continue
		}
		if !strings.HasPrefix(a, "--") || a == "--" {
			if strings.HasPrefix(a, "-") && a != "-" {
				return Derived{}, usagef("unknown flag %s", a)
			}
			return Derived{}, usagef("unexpected argument %q: a derived command takes flags only (see: droidyard %s --help)", a, helpPath(s))
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		// value fetches a flag's value: after '=' or the next argument, whatever it
		// looks like (a negative number is a value).
		value := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", usagef("--%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		boolean := func() (bool, error) {
			if !hasVal {
				return true, nil
			}
			b, err := parseBool(val)
			if err != nil {
				return false, usagef("--%s: %q is not true or false", name, val)
			}
			return b, nil
		}
		switch norm(name) {
		case "help":
			helpSeen = true
			continue
		case "json":
			b, err := boolean()
			if err != nil {
				return Derived{}, err
			}
			d.JSON = b
			continue
		case "noautostart":
			b, err := boolean()
			if err != nil {
				return Derived{}, err
			}
			d.Call.NoAutostart = b
			continue
		case "detach":
			if s.LongRunning {
				b, err := boolean()
				if err != nil {
					return Derived{}, err
				}
				d.Call.Detach = b
				continue
			}
		case "params":
			v, err := value()
			if err != nil {
				return Derived{}, err
			}
			if paramsRaw != nil {
				return Derived{}, usagef("--params given twice")
			}
			if !json.Valid([]byte(v)) {
				return Derived{}, usagef("--params is not valid JSON: %s", v)
			}
			var obj map[string]json.RawMessage
			if json.Unmarshal([]byte(v), &obj) != nil || obj == nil {
				return Derived{}, usagef("--params needs a JSON object, got: %s", v)
			}
			paramsRaw = json.RawMessage(v)
			continue
		case "workspace":
			if envWorkspace {
				v, err := value()
				if err != nil {
					return Derived{}, err
				}
				if v == "" {
					return Derived{}, usagef("--workspace needs a workspace id or an absolute project root")
				}
				d.Call.Workspace = v
				continue
			}
		}
		f, ok := in.byName[norm(name)]
		if !ok {
			return Derived{}, usagef("unknown flag --%s for %s (see: droidyard %s --help)", name, helpPath(s), helpPath(s))
		}
		var raw json.RawMessage
		if f.kind == "boolean" && !f.repeated {
			b, err := boolean()
			if err != nil {
				return Derived{}, err
			}
			raw = json.RawMessage(strconv.FormatBool(b))
		} else {
			v, err := value()
			if err != nil {
				return Derived{}, err
			}
			raw, err = f.parse(v)
			if err != nil {
				return Derived{}, err
			}
		}
		id := f.key + "\x00" + f.sub
		switch {
		case f.repeated && f.sub == "":
			arrays[f.key] = append(arrays[f.key], raw)
		case f.repeated:
			if subArrays[f.key] == nil {
				subArrays[f.key] = map[string][]json.RawMessage{}
			}
			subArrays[f.key][f.sub] = append(subArrays[f.key][f.sub], raw)
		default:
			if given[id] {
				return Derived{}, usagef("--%s given twice", f.name)
			}
			given[id] = true
			if f.sub == "" {
				scalars[f.key] = raw
			} else {
				if subs[f.key] == nil {
					subs[f.key] = map[string]json.RawMessage{}
				}
				subs[f.key][f.sub] = raw
			}
		}
	}
	if helpSeen {
		d.Help = true
		return d, nil
	}

	// --params is the base object; each flag sets its key, a dotted flag its subkey.
	obj := map[string]json.RawMessage{}
	if paramsRaw != nil {
		_ = json.Unmarshal(paramsRaw, &obj)
	}
	setTop := func(key string, raw json.RawMessage, flag string) error {
		if _, in := obj[key]; in {
			return usagef("%s is set both by --params and by --%s: use one", key, flag)
		}
		obj[key] = raw
		return nil
	}
	for _, key := range sortedKeys(scalars) {
		if err := setTop(key, scalars[key], kebab(key)); err != nil {
			return Derived{}, err
		}
	}
	for _, key := range sortedKeys(arrays) {
		raw, _ := json.Marshal(arrays[key])
		if err := setTop(key, raw, kebab(key)); err != nil {
			return Derived{}, err
		}
	}
	subKeys := map[string]bool{}
	for k := range subs {
		subKeys[k] = true
	}
	for k := range subArrays {
		subKeys[k] = true
	}
	for _, key := range sortedKeys(subKeys) {
		inner := map[string]json.RawMessage{}
		if cur, ok := obj[key]; ok {
			if json.Unmarshal(cur, &inner) != nil || inner == nil {
				return Derived{}, usagef("%s in --params is not an object, so --%s.* cannot join it", key, kebab(key))
			}
		}
		set := func(sub string, raw json.RawMessage) error {
			if _, dup := inner[sub]; dup {
				return usagef("%s.%s is set both by --params and by --%s.%s: use one", key, sub, kebab(key), kebab(sub))
			}
			inner[sub] = raw
			return nil
		}
		for _, sub := range sortedKeys(subs[key]) {
			if err := set(sub, subs[key][sub]); err != nil {
				return Derived{}, err
			}
		}
		for _, sub := range sortedKeys(subArrays[key]) {
			raw, _ := json.Marshal(subArrays[key][sub])
			if err := set(sub, raw); err != nil {
				return Derived{}, err
			}
		}
		obj[key], _ = json.Marshal(inner)
	}

	// ANDROID_SERIAL fills a required device, after the flag and --params; an optional
	// device is never filled (absent means something there).
	if in.root.isRequired("device") {
		if _, ok := obj["device"]; !ok {
			if serial := getenv("ANDROID_SERIAL"); serial != "" {
				raw, _ := json.Marshal(serial)
				obj["device"] = raw
			}
		}
	}

	var missing []string
	for _, p := range in.root.props {
		if !in.root.isRequired(p.name) {
			continue
		}
		if _, ok := obj[p.name]; ok {
			continue
		}
		if f := in.flagFor(p.name); f != nil {
			m := "--" + f.name
			if p.name == "device" {
				m += " (or ANDROID_SERIAL)"
			}
			missing = append(missing, m)
		} else {
			missing = append(missing, p.name+" (only through --params)")
		}
	}
	if len(missing) > 0 {
		return Derived{}, usagef("%s: missing required %s (see: droidyard %s --help)", helpPath(s), strings.Join(missing, ", "), helpPath(s))
	}
	for _, key := range sortedKeys(in.objects) {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var inner map[string]json.RawMessage
		if json.Unmarshal(raw, &inner) != nil {
			continue
		}
		var miss []string
		for _, r := range in.objects[key].required {
			if _, ok := inner[r]; !ok {
				miss = append(miss, kebab(key)+"."+kebab(r))
			}
		}
		if len(miss) > 0 {
			return Derived{}, usagef("%s: missing required %s", helpPath(s), "--"+strings.Join(miss, ", --"))
		}
	}

	if len(obj) > 0 {
		raw, err := json.Marshal(obj)
		if err != nil {
			return Derived{}, err
		}
		d.Call.Params = raw
	}
	return d, nil
}

func (in *input) flagFor(key string) *flagDef {
	for _, f := range in.flags {
		if f.key == key && f.sub == "" {
			return f
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseBool(s string) (bool, error) {
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, errors.New("not a boolean")
}

// parse turns one flag value into its JSON, by the schema's type, checking an enum.
func (f *flagDef) parse(v string) (json.RawMessage, error) {
	if len(f.enum) > 0 && !contains(f.enum, v) {
		return nil, usagef("--%s: %q is not one of %s", f.name, v, strings.Join(f.enum, ", "))
	}
	switch f.kind {
	case "integer":
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, usagef("--%s: %q is not an integer", f.name, v)
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	case "number":
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, usagef("--%s: %q is not a number", f.name, v)
		}
		return json.RawMessage(strconv.FormatFloat(n, 'g', -1, 64)), nil
	case "boolean":
		b, err := parseBool(v)
		if err != nil {
			return nil, usagef("--%s: %q is not true or false", f.name, v)
		}
		return json.RawMessage(strconv.FormatBool(b)), nil
	}
	raw, _ := json.Marshal(v)
	return raw, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// helpPath is the spelling help and errors use for a derived command: domain and
// kebab-case verb.
func helpPath(s Spec) string {
	dn, vn := splitName(s.Name)
	return dn + " " + kebab(vn)
}

// ---------------------------------------------------------------------------------
// Help.

// Help renders the help for a path: empty is the index, a domain, a domain and verb
// (or a hand command's own name). Hidden commands have help too; they are only left out
// of the listings.
func (t *Tree) Help(path []string) (string, error) {
	if len(path) == 0 {
		return t.index(), nil
	}
	if h, ok := t.hands[path[0]]; ok && len(path) == 1 {
		return h.Help, nil
	}
	r, err := t.Resolve(path)
	if err != nil {
		return "", err
	}
	switch {
	case r.Spec != nil:
		if len(r.Rest) > 0 {
			return "", usagef("help takes a command, not arguments after it: %s", strings.Join(r.Rest, " "))
		}
		return verbHelp(*r.Spec)
	case r.Hand != nil:
		return r.Hand.Help, nil
	default:
		return t.domainHelp(r.Domain), nil
	}
}

func (t *Tree) visibleDomains() []*domain {
	var out []*domain
	for _, d := range t.domains {
		if len(d.visibleVerbs()) > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type verbLine struct{ name, desc string }

func (d *domain) visibleVerbs() []verbLine {
	var out []verbLine
	for _, s := range d.verbs {
		if hidden(*s) {
			continue
		}
		_, v := splitName(s.Name)
		out = append(out, verbLine{kebab(v), firstSentence(s.Description)})
	}
	for _, h := range d.hands {
		out = append(out, verbLine{strings.Fields(h.Name)[1], h.Summary})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (t *Tree) index() string {
	var b strings.Builder
	for _, s := range t.Before {
		writeSection(&b, s)
	}
	b.WriteString("Capabilities (droidyard <domain> <verb> [flags]; droidyard <domain> --help lists a domain):\n")
	var width int
	for _, d := range t.visibleDomains() {
		width = max(width, len(d.name))
	}
	for _, d := range t.visibleDomains() {
		var names []string
		for _, v := range d.visibleVerbs() {
			names = append(names, v.name)
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, d.name, strings.Join(names, ", "))
	}
	b.WriteString("\n")
	for _, s := range t.After {
		writeSection(&b, s)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeSection(b *strings.Builder, s Section) {
	if s.Title != "" {
		b.WriteString(s.Title + ":\n")
	}
	b.WriteString(strings.TrimRight(s.Body, "\n") + "\n\n")
}

func (t *Tree) domainHelp(name string) string {
	d := t.domains[norm(name)]
	var b strings.Builder
	fmt.Fprintf(&b, "droidyard %s <verb> [flags]\n\n", d.name)
	verbs := d.visibleVerbs()
	width := 0
	for _, v := range verbs {
		width = max(width, len(v.name))
	}
	for _, v := range verbs {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, v.name, v.desc)
	}
	fmt.Fprintf(&b, "\ndroidyard %s <verb> --help shows a verb's flags.\n", d.name)
	return b.String()
}

func verbHelp(s Spec) (string, error) {
	in, err := derive(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "droidyard %s [flags]\n\n%s\n", helpPath(s), strings.TrimSpace(s.Description))

	fmt.Fprintf(&b, "\nKind: %s", s.Kind)
	a := s.Annotations
	var notes []string
	for _, n := range []struct {
		on   bool
		text string
	}{{a.ReadOnly, "read-only"}, {a.Destructive, "destructive"}, {a.Idempotent, "idempotent"}, {a.OpenWorld, "open-world"}} {
		if n.on {
			notes = append(notes, n.text)
		}
	}
	if s.LongRunning {
		notes = append(notes, "long-running")
	}
	if s.Statefulness == capability.Streaming {
		notes = append(notes, "streaming")
	}
	if s.Audience == capability.AudienceHuman {
		notes = append(notes, "human only")
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(notes, ", "))
	}
	b.WriteString("\n")

	if len(in.flags) > 0 {
		b.WriteString("\nFlags:\n")
		for _, f := range in.flags {
			b.WriteString("  " + f.helpLine(s) + "\n")
		}
	}
	if len(in.only) > 0 || len(in.onlyAt) > 0 {
		b.WriteString("\nOnly through --params '<json object>':\n")
		for _, p := range in.only {
			fmt.Fprintf(&b, "  %s%s\n", p.name, requiredNote(in.root.isRequired(p.name), p.node.desc))
		}
		for _, p := range in.onlyAt {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	b.WriteString("\nEvery command takes: --json (the envelope), --params <json object> (the base for the flags above), --no-autostart (do not start a daemon), --help")
	if s.Scope == capability.Workspace {
		b.WriteString(", --workspace <id|root> (default: the current project)")
	}
	if s.LongRunning {
		b.WriteString(", --detach (print the operation id and leave it running)")
	}
	b.WriteString(".\n")
	if s.UntrustedEvents || s.Statefulness == capability.Streaming {
		b.WriteString("It streams until its stream stops or Ctrl-C.\n")
	}
	if len(s.Errors) > 0 {
		fmt.Fprintf(&b, "\nErrors: %s\n", strings.Join(s.Errors, ", "))
	}
	return b.String(), nil
}

func requiredNote(required bool, desc string) string {
	var out string
	if required {
		out += " (required)"
	}
	if desc != "" {
		out += "  " + desc
	}
	return out
}

func (f *flagDef) helpLine(s Spec) string {
	var b strings.Builder
	b.WriteString("--" + f.name)
	switch {
	case len(f.enum) > 0:
		b.WriteString(" <" + strings.Join(f.enum, "|") + ">")
	case f.kind == "boolean" && !f.repeated:
	default:
		typ := map[string]string{"string": "string", "integer": "int", "number": "number", "boolean": "bool"}[f.kind]
		b.WriteString(" <" + typ + ">")
	}
	if f.repeated {
		b.WriteString(" (repeat for each)")
	}
	if f.required {
		b.WriteString(" (required")
		if f.key == "device" {
			b.WriteString("; else ANDROID_SERIAL")
		}
		b.WriteString(")")
	}
	if f.def != "" {
		b.WriteString(" (default " + f.def + ")")
	}
	if f.desc != "" {
		b.WriteString("  " + f.desc)
	}
	return b.String()
}

// firstSentence is a capability's one-line description: up to the first full stop
// followed by a space or the first line end.
func firstSentence(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.Index(desc, "\n"); i >= 0 {
		desc = desc[:i]
	}
	if i := strings.Index(desc, ". "); i >= 0 {
		return desc[:i+1]
	}
	return desc
}

// ---------------------------------------------------------------------------------
// Dispatch.

// Dispatch resolves args, prints help where asked, parses a derived command and only
// then hands it to run, so a usage error (a missing required flag) is reported before
// any daemon is dialled or started. A hand command receives the arguments after its
// name. getenv is injected.
func (t *Tree) Dispatch(args []string, getenv func(string) string, out, errOut io.Writer, run func(Derived) int) int {
	fail := func(err error) int {
		fmt.Fprintf(errOut, "droidyard: %v\n", err)
		return ExitUsage
	}
	r, err := t.Resolve(args)
	if err != nil {
		return fail(err)
	}
	switch {
	case r.Hand != nil:
		if !r.Hand.OwnHelp && len(r.Rest) == 1 && (r.Rest[0] == "--help" || r.Rest[0] == "-h") {
			fmt.Fprint(out, r.Hand.Help)
			return 0
		}
		return r.Hand.Run(r.Rest)
	case r.Spec != nil:
		d, err := ParseDerived(*r.Spec, r.Rest, getenv)
		if err != nil {
			return fail(err)
		}
		if d.Help {
			text, err := verbHelp(*r.Spec)
			if err != nil {
				return fail(err)
			}
			fmt.Fprint(out, text)
			return 0
		}
		return run(d)
	default:
		fmt.Fprint(out, t.domainHelp(r.Domain))
		return 0
	}
}

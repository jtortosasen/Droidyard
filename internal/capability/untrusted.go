package capability

import (
	"fmt"
	"reflect"
	"strings"
)

// UntrustedTag is the struct tag that marks a result field as carrying device- or
// app-derived data: `untrusted:"true"` (ADR-0015 §1). Nothing else is a valid value,
// so a typo is a startup failure rather than a field that is silently trusted.
const UntrustedTag = "untrusted"

// UntrustedKeyword is the JSON Schema keyword the published schema puts, as the
// boolean true, on each tagged property (ADR-0015 §1).
const UntrustedKeyword = "x-droidyard-untrusted"

// FieldUntrusted reports whether f is tagged untrusted. A tag with any value other
// than "true" is an error.
func FieldUntrusted(f reflect.StructField) (bool, error) {
	v, ok := f.Tag.Lookup(UntrustedTag)
	if !ok {
		return false, nil
	}
	if v != "true" {
		return false, fmt.Errorf("field %s: %s:%q: the only valid value is \"true\"", f.Name, UntrustedTag, v)
	}
	if name, _ := JSONName(f); f.Anonymous && name == "" {
		// An embedded struct's fields are flattened into its parent's: there is no
		// property to carry the keyword.
		return false, fmt.Errorf("field %s: an embedded field cannot be tagged %s; tag the fields inside it", f.Name, UntrustedTag)
	}
	return true, nil
}

// JSONName is the name a struct field has in JSON, and whether JSON omits it. The name
// is "" for an embedded struct that is flattened into its parent. Only the tag `json:"-"`
// omits a field: `json:"-,"` names it "-", as encoding/json does.
func JSONName(f reflect.StructField) (name string, omitted bool) {
	if tag, ok := f.Tag.Lookup("json"); ok {
		if tag == "-" {
			return "", true
		}
		name, _, _ = strings.Cut(tag, ",")
		if name != "" {
			return name, false
		}
	}
	if f.Anonymous {
		return "", false
	}
	return f.Name, false
}

// deriveUntrusted reports whether any field of v's type, however deep (structs,
// embedded structs, pointers, slices, arrays, map values), is tagged untrusted. It is
// how a descriptor's flag is set: never written by hand (ADR-0015 §1).
func deriveUntrusted(v any) (bool, error) {
	return walkUntrusted(reflect.TypeOf(v), map[reflect.Type]bool{})
}

// HasUntrusted reports whether t, however deep, has a field tagged untrusted. The schema
// package uses it to tell a schema position that cannot matter from one that must be
// marked.
func HasUntrusted(t reflect.Type) (bool, error) {
	return walkUntrusted(t, map[reflect.Type]bool{})
}

func walkUntrusted(t reflect.Type, seen map[reflect.Type]bool) (bool, error) {
	if t == nil {
		return false, nil
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return walkUntrusted(t.Elem(), seen)
	case reflect.Struct:
		if seen[t] {
			return false, nil // its tags are found by the visit already under way
		}
		seen[t] = true
		found := false
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if _, omitted := JSONName(f); (!f.IsExported() && !f.Anonymous) || omitted {
				continue
			}
			tagged, err := FieldUntrusted(f)
			if err != nil {
				return false, fmt.Errorf("%s: %w", t, err)
			}
			inner, err := walkUntrusted(f.Type, seen)
			if err != nil {
				return false, err
			}
			found = found || tagged || inner
		}
		return found, nil
	}
	return false, nil
}

package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// object is a JSON object that keeps its keys in file order and its values as the raw
// text they were read as, so the value of a key `init` does not touch keeps its exact text.
// The file as a whole is re-indented (two spaces), so it is equal after parsing, not
// byte for byte. It is the
// only way this package reads or writes the user's JSON: a map would reorder the keys,
// and a typed struct would drop the ones it does not know.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObject() *object { return &object{vals: map[string]json.RawMessage{}} }

// parseObject reads one JSON object and nothing else: a file that is not an object,
// holds a duplicate key (which no consumer agrees on) or has text after the object does
// not parse here, and is refused by the caller.
func parseObject(b []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := newObject()
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := o.vals[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		o.keys = append(o.keys, key)
		o.vals[key] = raw
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("text after the JSON object")
	}
	return o, nil
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// compact is the object as compact JSON, keys in order.
func (o *object) compact() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(quote(k))
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// file is the object as a file: indented by two spaces, ending in a newline.
func (o *object) file() []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, o.compact(), "", "  "); err != nil {
		panic(err) // every part came from the decoder or from quote and Marshal
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func quote(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// editPath runs f on the object at path below o. An absent step is created when create
// is set (and the call is a no-op otherwise); a step that exists and is not an object is
// an error, since init cannot put its entry there. When prune is set, an object left
// empty by f is removed from its parent, which is how --uninstall leaves no empty
// `mcpServers` behind. It reports whether anything changed.
func (o *object) editPath(path []string, create, prune bool, f func(*object) (bool, error)) (bool, error) {
	if len(path) == 0 {
		return f(o)
	}
	var child *object
	if raw, ok := o.vals[path[0]]; ok {
		var err error
		if child, err = parseObject(raw); err != nil {
			if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
				return false, &nestedError{key: path[0], err: err}
			}
			return false, fmt.Errorf("%q is not a JSON object", path[0])
		}
	} else {
		if !create {
			return false, nil
		}
		child = newObject()
	}
	changed, err := child.editPath(path[1:], create, prune, f)
	if err != nil || !changed {
		return false, err
	}
	if prune && len(child.keys) == 0 {
		o.del(path[0])
	} else {
		o.set(path[0], child.compact())
	}
	return true, nil
}

// nestedError is an object below the top level that does not parse as one object: it
// holds a duplicate key. Unlike a value that is not an object at all, it may hide
// init's own entry, so --uninstall refuses it too.
type nestedError struct {
	key string
	err error
}

func (e *nestedError) Error() string { return fmt.Sprintf("%q does not parse (%v)", e.key, e.err) }

// equalJSON reports whether two JSON values are equal after parsing, which is the only
// equality init uses: formatting and key order do not count.
func equalJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// parseArray reads a JSON array of raw elements.
func parseArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func marshalArray(arr []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, e := range arr {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(e)
	}
	b.WriteByte(']')
	return b.Bytes()
}

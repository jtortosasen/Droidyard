package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Status is what init did, or would do, to one file.
type Status string

const (
	Created   Status = "created"
	Updated   Status = "updated"   // the file changed and keeps the user's content
	Unchanged Status = "unchanged" // nothing to write, or nothing of ours to remove
	Removed   Status = "removed"   // --uninstall took our part out; the file stays
	Deleted   Status = "deleted"   // --uninstall removed a file that held only our part
	Refused   Status = "refused"   // the file is left untouched, with a reason
)

// edit is the outcome of one file's rule: the content to write, or the file to delete,
// with the notes that explain it. It is computed before anything is written, so
// --dry-run and the real run take the same path.
type edit struct {
	status  Status
	content []byte
	notes   []string
	// entry is, for a refused file, the entry the user adds by hand.
	entry string
}

func refuse(reason, entry string) edit {
	return edit{status: Refused, notes: []string{reason}, entry: entry}
}

// keyEntry is one `key: value` init puts in the object at path.
type keyEntry struct {
	path  []string
	key   string
	value json.RawMessage
}

// arrayEntry is the elements init appends to the array at path/key.
type arrayEntry struct {
	path  []string
	key   string
	elems []json.RawMessage
}

// jsonSpec is everything init writes into one JSON file.
type jsonSpec struct {
	keys   []keyEntry
	arrays []arrayEntry
}

func label(path []string, key string) string {
	return strings.Join(append(append([]string(nil), path...), key), ".")
}

// handEntry is what init would have written to a file of its own: the entry the user
// adds by hand to a file init refuses.
func (s jsonSpec) handEntry() string {
	o := newObject()
	if _, _, err := s.mergeInto(o); err != nil {
		panic(err) // an empty object has no step that is not an object
	}
	return strings.TrimRight(string(o.file()), "\n")
}

// installJSON merges the spec into a JSON file. An existing entry under a key init
// writes is kept and reported, never replaced; an array gets only the elements it does
// not already hold, and the user's are never touched. A file that does not parse, or a
// path that is not an object where init needs one, is refused and left as it is.
func installJSON(old []byte, exists bool, s jsonSpec) edit {
	root := newObject()
	if exists {
		var err error
		if root, err = parseObject(old); err != nil {
			return refuse(fmt.Sprintf("does not parse as a JSON object (%v)", err), s.handEntry())
		}
	}
	notes, changed, err := s.mergeInto(root)
	if err != nil {
		return refuse(err.Error(), s.handEntry())
	}
	switch {
	case !exists:
		return edit{status: Created, content: root.file(), notes: notes}
	case changed:
		return edit{status: Updated, content: root.file(), notes: notes}
	}
	return edit{status: Unchanged, notes: notes}
}

func (s jsonSpec) mergeInto(root *object) (notes []string, changed bool, err error) {
	for _, k := range s.keys {
		ch, err := root.editPath(k.path, true, false, func(leaf *object) (bool, error) {
			name := label(k.path, k.key)
			have, ok := leaf.vals[k.key]
			switch {
			case !ok:
				leaf.set(k.key, k.value)
				notes = append(notes, name+": added")
				return true, nil
			case equalJSON(have, k.value):
				notes = append(notes, name+": already there")
			default:
				notes = append(notes, name+": kept, yours differs from what init would write (not replaced)")
			}
			return false, nil
		})
		if err != nil {
			return nil, false, err
		}
		changed = changed || ch
	}
	for _, a := range s.arrays {
		ch, err := root.editPath(a.path, true, false, func(leaf *object) (bool, error) {
			name := label(a.path, a.key)
			var have []json.RawMessage
			if raw, ok := leaf.vals[a.key]; ok {
				if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
					return false, fmt.Errorf("%q is not a JSON array", name)
				}
				var err error
				if have, err = parseArray(raw); err != nil {
					return false, fmt.Errorf("%q is not a JSON array", name)
				}
			}
			added := 0
			for _, e := range a.elems {
				if !containsJSON(have, e) {
					have = append(have, e)
					added++
				}
			}
			if added == 0 {
				notes = append(notes, fmt.Sprintf("%s: already holds all %d", name, len(a.elems)))
				return false, nil
			}
			leaf.set(a.key, marshalArray(have))
			if added == len(a.elems) {
				notes = append(notes, fmt.Sprintf("%s: added %d", name, added))
			} else {
				notes = append(notes, fmt.Sprintf("%s: added %d, %d were there", name, added, len(a.elems)-added))
			}
			return true, nil
		})
		if err != nil {
			return nil, false, err
		}
		changed = changed || ch
	}
	return notes, changed, nil
}

func containsJSON(list []json.RawMessage, e json.RawMessage) bool {
	for _, x := range list {
		if equalJSON(x, e) {
			return true
		}
	}
	return false
}

// nestedRefusal turns editPath's error on --uninstall into a refusal when an object on
// the path does not parse. Any other error (a step that is not an object) means nothing
// of init's is there, and is no error at all.
func nestedRefusal(err error, s jsonSpec) (edit, bool) {
	var ne *nestedError
	if errors.As(err, &ne) {
		return refuse(fmt.Sprintf("%v; remove init's entries by hand:\n%s", ne, s.handEntry()), ""), true
	}
	return edit{}, false
}

// uninstallJSON removes from a JSON file exactly the keys and array elements that are
// equal, after parsing, to what installJSON writes now, and the objects that leaves
// empty. A file left with no key at all held only init's part and is deleted. A key
// that differs is reported and kept.
func uninstallJSON(old []byte, exists bool, s jsonSpec) edit {
	if !exists {
		return edit{status: Unchanged, notes: []string{"not there"}}
	}
	root, err := parseObject(old)
	if err != nil {
		return refuse(fmt.Sprintf("does not parse as a JSON object (%v); remove init's entries by hand:\n%s", err, s.handEntry()), "")
	}
	var notes []string
	changed := false
	for _, k := range s.keys {
		ch, err := root.editPath(k.path, false, true, func(leaf *object) (bool, error) {
			name := label(k.path, k.key)
			have, ok := leaf.vals[k.key]
			switch {
			case !ok:
				return false, nil
			case equalJSON(have, k.value):
				leaf.del(k.key)
				notes = append(notes, name+": removed")
				return true, nil
			}
			notes = append(notes, name+": kept, differs from what init writes now")
			return false, nil
		})
		if e, bad := nestedRefusal(err, s); bad {
			return e
		}
		changed = changed || ch
	}
	for _, a := range s.arrays {
		ch, err := root.editPath(a.path, false, true, func(leaf *object) (bool, error) {
			raw, ok := leaf.vals[a.key]
			if !ok || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
				return false, nil
			}
			have, err := parseArray(raw)
			if err != nil {
				return false, nil
			}
			var keep []json.RawMessage
			for _, e := range have {
				if !containsJSON(a.elems, e) {
					keep = append(keep, e)
				}
			}
			removed := len(have) - len(keep)
			if removed == 0 {
				return false, nil
			}
			if len(keep) == 0 {
				leaf.del(a.key)
			} else {
				leaf.set(a.key, marshalArray(keep))
			}
			notes = append(notes, fmt.Sprintf("%s: removed %d element(s), the others are yours", label(a.path, a.key), removed))
			return true, nil
		})
		if e, bad := nestedRefusal(err, s); bad {
			return e
		}
		changed = changed || ch
	}
	switch {
	case !changed:
		return edit{status: Unchanged, notes: append(notes, "nothing of init's to remove")}
	case len(root.keys) == 0:
		return edit{status: Deleted, notes: notes}
	}
	return edit{status: Removed, content: root.file(), notes: notes}
}

package file

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/store"
)

// trashListMax is the most entries file.trash returns (ADR-0096 §1; a working default).
const trashListMax = 200

// trashVersion is the API version of file.trash (ADR-0096), the milestone's version.
const trashVersion = "0.29.0"

// trashBy is who deleted, as entry.json records it (ADR-0096 §2): the kind of connection
// the call came in on and, when it was joined to an MCP session, that session's name. An
// entry written before has none. A reader that does not know the key ignores it: entries
// are decoded without a strict mode (the store's Read, and trashEntry here).
type trashBy struct {
	Client string `json:"client"`
	Name   string `json:"name,omitempty"`
}

// byOf is the trashBy of a call's origin; nil for a call without a kind (no origin).
func byOf(o capability.Origin) *trashBy {
	if !validClient(string(o.Client)) {
		return nil
	}
	return &trashBy{Client: string(o.Client), Name: capability.SessionName(o.Name)}
}

// validType reports whether t is one of the kinds the schema publishes for an entry.
func validType(t string) bool {
	switch t {
	case "file", "dir", "symlink", "other":
		return true
	}
	return false
}

func validClient(c string) bool {
	switch capability.ClientKind(c) {
	case capability.ClientCLI, capability.ClientMCP, capability.ClientUI:
		return true
	}
	return false
}

// TrashParams are file.trash's parameters: none.
type TrashParams struct{}

// TrashBy is who deleted an item.
type TrashBy struct {
	Client string `json:"client" jsonschema:"required,enum=cli,enum=mcp,enum=ui" jsonschema_description:"The kind of connection the delete came in on."`
	Name   string `json:"name,omitempty" jsonschema_description:"The MCP session's name when the delete was made by or for one (a CLI call joined to its chat, ADR-0095); absent otherwise."`
}

// TrashedItem is one entry of file.trash's result.
type TrashedItem struct {
	ID        string    `json:"id" jsonschema:"required" jsonschema_description:"The trash id, as file.delete returned it: what file.restore takes."`
	Path      string    `json:"path" jsonschema:"required" jsonschema_description:"Where the item was, relative to the project root."`
	Type      string    `json:"type" jsonschema:"required,enum=file,enum=dir,enum=symlink,enum=other" jsonschema_description:"The item's kind."`
	DeletedAt time.Time `json:"deletedAt" jsonschema:"required"`
	Size      int64     `json:"size" jsonschema:"required" jsonschema_description:"Bytes of the item's regular files, as counted when it was deleted (a figure for the retention limit, cut at the walk's cap)."`
	By        *TrashBy  `json:"by,omitempty" jsonschema_description:"Who deleted it; absent for an item deleted before droidyard recorded that."`
}

// TrashResult is file.trash's result.
type TrashResult struct {
	Entries []TrashedItem `json:"entries" jsonschema:"required" jsonschema_description:"The workspace's trash, the newest first, at most 200."`
}

// storedEntry is entry.json as the listing reads it: the store's schemaVersion beside
// trashEntry's fields.
type storedEntry struct {
	SchemaVersion int `json:"schemaVersion"`
	trashEntry
}

// list is the workspace's trash newest first (ADR-0096 §1). It reads the entry files
// directly and not through the store, which moves a file that does not parse out of the
// way: a query changes nothing. An entry that cannot be read or parsed, that a newer
// binary wrote, whose path or type is not one this package writes, or that has no item yet (a
// delete under way, or one cut short that the sweep will settle) is skipped. It takes no
// trash lock: a record is replaced atomically, and a delete across volumes holds the lock
// through its whole copy.
func (t *Trash) list(wsid string) ([]TrashedItem, error) {
	if !validWorkspaceID(wsid) {
		return nil, errors.New("the workspace id cannot name a trash folder")
	}
	des, err := os.ReadDir(t.dir(wsid))
	if errors.Is(err, fs.ErrNotExist) {
		return []TrashedItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []TrashedItem{}
	for _, de := range des {
		id := de.Name()
		if !de.IsDir() || !trashIDPattern.MatchString(id) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(t.entryDir(wsid, id), entryFile))
		if err != nil {
			continue
		}
		var e storedEntry
		if json.Unmarshal(b, &e) != nil || e.SchemaVersion < 1 || e.SchemaVersion > store.Version || !recordedPath(e.Path) || !validType(e.Type) {
			continue
		}
		if !exists(filepath.Join(t.entryDir(wsid, id), trashItem)) {
			continue
		}
		it := TrashedItem{ID: id, Path: e.Path, Type: e.Type, DeletedAt: e.DeletedAt, Size: e.Bytes}
		if e.By != nil && validClient(e.By.Client) {
			it.By = &TrashBy{Client: e.By.Client, Name: capability.SessionName(e.By.Name)}
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DeletedAt.Equal(out[j].DeletedAt) {
			return out[i].DeletedAt.After(out[j].DeletedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > trashListMax {
		out = out[:trashListMax]
	}
	return out, nil
}

// trashList serves file.trash.
func (r *resolver) trashList(context.Context) (TrashResult, error) {
	if r.trash == nil {
		return TrashResult{}, noTrash()
	}
	items, err := r.trash.list(r.workspace)
	if err != nil {
		return TrashResult{}, capability.Errorf(capability.ErrFileIOFailed, "the trash could not be listed: %v", err)
	}
	return TrashResult{Entries: items}, nil
}

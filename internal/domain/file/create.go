package file

import (
	"errors"
	"os"
	"syscall"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// CreateParams is file.create's parameters.
type CreateParams struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"What to create, relative to the project root; missing parent folders are made."`
	Type string `json:"type" jsonschema:"required,enum=file,enum=dir" jsonschema_description:"file for an empty file, dir for a folder."`
}

// CreateResult is file.create's result.
type CreateResult struct {
	Path string `json:"path" jsonschema:"required" jsonschema_description:"What was created, relative to the project root."`
	Type string `json:"type" jsonschema:"required,enum=file,enum=dir"`
}

// create serves file.create (ADR-0053): an empty file by O_CREATE|O_EXCL, a folder by
// Mkdir, missing parents by MkdirAll, every call through the os.Root. Neither form can
// replace anything: an existing path of any kind, a dangling symlink included, is
// FILE_EXISTS and left as it was.
func (r *resolver) create(p CreateParams) (CreateResult, error) {
	if p.Type != "file" && p.Type != "dir" {
		return CreateResult{}, capability.Errorf(capability.ErrInvalidParams, "type is %q; it is \"file\" or \"dir\"", p.Type)
	}
	rel, cerr := r.opPath(p.Path, "path")
	if cerr != nil {
		return CreateResult{}, cerr
	}
	root, cerr := r.openRoot()
	if cerr != nil {
		return CreateResult{}, cerr
	}
	defer root.Close()

	dir, _ := splitDir(rel)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			// A parent that is a file makes MkdirAll fail with EEXIST (or ENOTDIR), so the
			// ancestors are looked at before the error is read.
			if p, _ := firstNonDirAncestor(root, rel); p != "" || errors.Is(err, syscall.ENOTDIR) {
				return CreateResult{}, r.parentFile(root, rel)
			}
			// A dangling in-root parent link makes MkdirAll fail with EEXIST: the target was
			// not what was taken, so it is an I/O failure, as in file.write.
			return CreateResult{}, r.classifyWrite(rel, err)
		}
	}

	if p.Type == "dir" {
		if err := root.Mkdir(rel, 0o755); err != nil {
			return CreateResult{}, r.classifyCreate(root, rel, err)
		}
	} else {
		// O_EXCL and no O_TRUNC: an existing file keeps its bytes, and a symlink as the final
		// component is never followed, so a dangling one is FILE_EXISTS too.
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return CreateResult{}, r.classifyCreate(root, rel, err)
		}
		if err := f.Close(); err != nil {
			return CreateResult{}, r.classifyWrite(rel, err)
		}
	}
	return CreateResult{Path: rel, Type: p.Type}, nil
}

// classifyCreate turns the error of the creating call itself into a capability error: a
// taken name is FILE_EXISTS, a parent that is a file FILE_WRONG_TYPE, the rest as a write.
func (r *resolver) classifyCreate(root *os.Root, rel string, err error) *capability.Error {
	switch {
	case errors.Is(err, os.ErrExist):
		return existsErr(rel)
	case errors.Is(err, syscall.ENOTDIR):
		return r.parentFile(root, rel)
	}
	return r.classifyWrite(rel, err)
}

package sdk

import (
	"context"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// sdk.profiles (plan m3f step 6; ADR-0129 §1): the device profiles `avdmanager list device`
// knows, the names emulator.create takes. Read from the SDK's own tool, so not tagged
// untrusted (as package.xml is not); kept for the avdmanager they came from.

// Profile is one device profile.
type Profile struct {
	ID   string `json:"id" jsonschema:"required" jsonschema_description:"The profile id emulator.create takes as profile, e.g. \"pixel_9a\" or \"Nexus 5X\"."`
	Name string `json:"name" jsonschema:"required" jsonschema_description:"The profile's display name, e.g. \"Pixel 9a\"."`
	OEM  string `json:"oem,omitempty" jsonschema_description:"The maker, e.g. \"Google\" or \"Generic\"."`
	Tag  string `json:"tag,omitempty" jsonschema_description:"The kind of device when the definition has one, e.g. \"android-automotive\" or \"android-desktop\"."`
}

// ProfilesResult is sdk.profiles' result.
type ProfilesResult struct {
	Profiles []Profile `json:"profiles" jsonschema:"required" jsonschema_description:"The device profiles, in the order avdmanager lists them."`
}

func registerProfiles(reg *capability.Registry, t *Tool) {
	reg.MustRegister(capability.Descriptor{
		Name:        "sdk.profiles",
		Kind:        capability.Query,
		Scope:       capability.Global,
		Since:       "0.39.0",
		Description: "Lists the device profiles AVDs are created from (id, name, maker, kind); use an id as emulator.create's profile. Runs avdmanager from the SDK's cmdline-tools.",
		Params:      struct{}{},
		Result:      ProfilesResult{},
		Errors:      []capability.ErrorCode{ErrToolsNotFound, ErrJavaNotFound, ErrToolFailed},
		Handler: func(ctx context.Context, _ *capability.Call) (any, error) {
			ps, err := t.Profiles(ctx)
			if err != nil {
				return nil, err
			}
			return ProfilesResult{Profiles: ps}, nil
		},
		Text: capability.TextOf(profilesText),
	})
}

// profilesText renders the result, one line per profile (ADR-0036 Decision 2).
func profilesText(r ProfilesResult) string {
	var t capability.Text
	for _, p := range r.Profiles {
		t.W("profile", p.ID, p.Name)
		if p.OEM != "" {
			t.W(p.OEM)
		}
		if p.Tag != "" {
			t.W("tag=" + p.Tag)
		}
		t.Nl()
	}
	return t.String()
}

// toolStamp identifies an avdmanager: its path and the time and size of the file. A new
// cmdline-tools package replaces the file, so the profiles it knows are read again.
type toolStamp struct {
	path  string
	mtime time.Time
	size  int64
}

func stampOf(path string) (toolStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return toolStamp{}, false
	}
	return toolStamp{path: path, mtime: fi.ModTime(), size: fi.Size()}, true
}

// Profiles lists the device profiles, from `avdmanager list device` the first time and for
// each other avdmanager (a different path, or a file changed since), else from the copy
// kept.
func (t *Tool) Profiles(ctx context.Context) ([]Profile, error) {
	paths, err := t.Resolve()
	if err != nil {
		return nil, err
	}
	stamp, stamped := stampOf(paths.Path)
	if stamped {
		t.profMu.Lock()
		if t.profiles != nil && t.profStamp == stamp {
			ps := append([]Profile(nil), t.profiles...)
			t.profMu.Unlock()
			return ps, nil
		}
		t.profMu.Unlock()
	}
	args := []string{"list", "device"}
	out, err := t.Run(ctx, ToolRun{Args: args})
	if err != nil {
		return nil, err
	}
	ps := parseProfiles(out.Output)
	if len(ps) == 0 {
		return nil, toolFailed(args, "printed no device profiles", out.Output)
	}
	if stamped {
		t.profMu.Lock()
		t.profiles, t.profStamp = ps, stamp
		t.profMu.Unlock()
	}
	return append([]Profile(nil), ps...), nil
}

var profileID = regexp.MustCompile(`^id: \d+ or "([^"]+)"$`)

// parseProfiles reads `avdmanager list device` (spike m3f step 1 (d)): blocks of
//
//	id: 57 or "pixel_9a"
//	    Name: Pixel 9a
//	    OEM : Google
//	    Tag : android-desktop
//	---------
//
// where Tag is optional and the id is the quoted one. A line it does not know is skipped
// (a JVM's notice, the header); a block without a usable id or a Name is dropped, and an
// id that could be read as an option (a leading "-") is not offered.
func parseProfiles(out []byte) []Profile {
	var ps []Profile
	var cur *Profile
	flush := func() {
		if cur != nil && cur.ID != "" && cur.Name != "" {
			ps = append(ps, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ToValidUTF8(string(out), "�"), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if m := profileID.FindStringSubmatch(line); m != nil {
			flush()
			id := cleanText(m[1], 100)
			if id == "" || strings.HasPrefix(id, "-") {
				continue
			}
			cur = &Profile{ID: id}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = cleanText(val, 200)
		switch strings.TrimSpace(key) {
		case "Name":
			cur.Name = val
		case "OEM":
			cur.OEM = val
		case "Tag":
			cur.Tag = val
		}
	}
	flush()
	return ps
}

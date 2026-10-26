package emulator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
)

// emulator.create (plan m3f step 6; ADR-0129 §7, ADR-0130 §5): a new AVD from a device
// profile and an installed system image, made by avdmanager in the one directory the
// launcher lists, so the AVD is found by emulator.list and booted by emulator.start.

// CreateParams is emulator.create's parameters.
type CreateParams struct {
	Avd         string    `json:"avd" jsonschema:"required,maxLength=100" jsonschema_description:"The new AVD's name: letters, digits, dot, dash, underscore. A name already in any AVD home is refused."`
	Profile     string    `json:"profile" jsonschema:"required,maxLength=100" jsonschema_description:"A device profile id from sdk.profiles, e.g. pixel_9a."`
	SystemImage string    `json:"systemImage" jsonschema:"required,maxLength=200" jsonschema_description:"An installed system image id from sdk.packages, e.g. system-images;android-35;google_apis;arm64-v8a."`
	Settings    *Settings `json:"settings,omitempty" jsonschema_description:"Hardware settings for the new AVD; those left out keep the device profile's."`
}

// CreateResult is emulator.create's result.
type CreateResult struct {
	Avd         string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD created; the name emulator.list and emulator.start take."`
	Profile     string `json:"profile" jsonschema:"required" jsonschema_description:"The device profile it was made from."`
	SystemImage string `json:"systemImage" jsonschema:"required" jsonschema_description:"The system image it runs."`
}

func registerCreate(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:        "emulator.create",
		Kind:        capability.Command,
		Scope:       capability.Global,
		LongRunning: true,
		Since:       "0.39.0",
		Description: "Creates one AVD from a device profile (sdk.profiles) and an installed system image (sdk.packages) with avdmanager, where the launcher lists it, with optional hardware settings. A name used in any AVD home is refused and the image must be installed; boot it with emulator.start.",
		Params:      CreateParams{},
		Target:      func(p any) *capability.OpTarget { return &capability.OpTarget{AVD: p.(CreateParams).Avd} },
		Result:      CreateResult{},
		Errors: []capability.ErrorCode{capability.ErrInvalidParams, ErrAVDExists, ErrImageNotInstalled, ErrProfileNotFound,
			sdk.ErrToolsNotFound, sdk.ErrJavaNotFound, sdk.ErrToolFailed, sdk.ErrNotWritable, ErrLaunchFailed, ErrConfigUnwritable},
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			OpenWorld:   boolPtr(true),
			Reason:      "it makes a new AVD in the user's AVD home and refuses a name that exists, so nothing is replaced",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.create(ctx, call.Params.(CreateParams))
		},
	})
}

// systemImageID is the id of a system image package: system-images;<api>;<variant>;<abi>.
var systemImageID = regexp.MustCompile(`^system-images;[A-Za-z0-9._-]+;[A-Za-z0-9._-]+;[A-Za-z0-9._-]+$`)

func (e *Emulator) create(ctx context.Context, p CreateParams) (CreateResult, error) {
	if !avdName.MatchString(p.Avd) {
		return CreateResult{}, invalidAVDName(p.Avd)
	}
	h := e.host()
	if p.Settings != nil {
		if err := p.Settings.validate(h); err != nil {
			return CreateResult{}, err
		}
	}
	home := launcherDir(e.env)
	if home == "" {
		return CreateResult{}, capability.NewError(sdk.ErrNotWritable, "there is no place to create an AVD").
			WithCause("none of ANDROID_AVD_HOME, ANDROID_SDK_HOME and HOME is set").
			WithNextStep("set HOME or ANDROID_AVD_HOME for the daemon")
	}
	if err := e.nameTaken(ctx, p.Avd, home); err != nil {
		return CreateResult{}, err
	}
	paths, err := e.tool.Resolve()
	if err != nil {
		return CreateResult{}, err
	}
	if err := imageInstalled(paths.Root, p.SystemImage); err != nil {
		return CreateResult{}, err
	}
	profiles, err := e.tool.Profiles(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	if !hasProfile(profiles, p.Profile) {
		return CreateResult{}, profileNotFound(profiles, p.Profile)
	}

	// The directory the launcher lists exists before avdmanager runs, so the tool has one
	// to write into and the launcher's rule (launcherDir) is the one that was followed.
	if err := os.MkdirAll(home, 0o755); err != nil {
		return CreateResult{}, capability.NewError(sdk.ErrNotWritable, "the AVD directory cannot be created").
			WithCause(describeFS(err)).
			WithNextStep("make the directory ANDROID_AVD_HOME (or ~/.android/avd) names writable")
	}
	args := []string{"create", "avd", "--name", p.Avd, "--package", p.SystemImage, "--device", p.Profile}
	if p.Settings != nil && p.Settings.SDCardMB != nil && *p.Settings.SDCardMB > 0 {
		args = append(args, "--sdcard", strconv.Itoa(*p.Settings.SDCardMB)+"M")
	}
	// avdmanager asks whether to make a custom hardware profile; the answer is a fixed no,
	// never the caller's and never the daemon's stdin.
	if _, err := e.tool.Run(ctx, sdk.ToolRun{Args: args, Stdin: "no\n", Env: []string{"ANDROID_AVD_HOME=" + home}}); err != nil {
		return CreateResult{}, err
	}
	if _, err := os.Stat(filepath.Join(home, p.Avd+".ini")); err != nil {
		return CreateResult{}, sdk.ToolFailed(args, "exited 0 and wrote no AVD the launcher lists ("+p.Avd+".ini is not in the AVD directory)")
	}
	// The rest of the settings are written by Droidyard after avdmanager returns; if that
	// fails the AVD is deleted again, so the call made an AVD with its settings or none.
	if p.Settings != nil && len(p.Settings.lines()) > 0 {
		dir := avdHome{dir: avdDir{path: home}, ini: filepath.Join(home, p.Avd+".ini")}.dataDir(p.Avd)
		if _, err := writeSettings(dir, *p.Settings, h, false); err != nil {
			return CreateResult{}, e.undoCreate(ctx, p.Avd, home, err)
		}
	}
	return CreateResult{Avd: p.Avd, Profile: p.Profile, SystemImage: p.SystemImage}, nil
}

// undoCreate deletes the AVD that was just made, after its settings could not be written,
// and returns cause; when the delete fails too, the cause says an AVD was left behind. It
// runs though the call was cancelled: a half-made AVD is not left for the cancel.
func (e *Emulator) undoCreate(ctx context.Context, name, home string, cause error) error {
	args := []string{"delete", "avd", "--name", name}
	_, err := e.tool.Run(context.WithoutCancel(ctx), sdk.ToolRun{Args: args, Env: []string{"ANDROID_AVD_HOME=" + home}})
	if err == nil {
		if _, serr := os.Lstat(filepath.Join(home, name+".ini")); serr != nil {
			return cause
		}
		err = errors.New("exited 0 and left " + name + ".ini")
	}
	var ce *capability.Error
	if errors.As(cause, &ce) {
		ce.Cause += "; the AVD " + name + " was made and could not be deleted again (" + describeFS(err) + ")"
		ce.NextStep = "the user deletes the AVD " + name + " (emulator.delete) and creates it again"
	}
	return cause
}

// nameTaken is EMULATOR_AVD_EXISTS when name is an AVD in any AVD home: an <name>.ini in
// any directory of avdDirs, an <name>.avd directory where the new one would go, or the
// name in the launcher's own listing (checked when a launcher is found).
func (e *Emulator) nameTaken(ctx context.Context, name, home string) error {
	taken := func(where string) error {
		return capability.NewError(ErrAVDExists, "an AVD named "+name+" already exists").
			WithCause("found in " + where).
			WithNextStep("choose another name, or ask the user to delete the existing AVD")
	}
	for _, d := range avdDirs(e.env) {
		if _, err := os.Lstat(filepath.Join(d.path, name+".ini")); err == nil {
			return taken(d.source)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, name+".avd")); err == nil {
		return taken("the AVD directory (a " + name + ".avd directory is there)")
	}
	if res := e.Resolve(); res.Found {
		names, err := e.listAVDs(ctx, res.Path)
		if err != nil {
			return err
		}
		if containsName(names, name) {
			return taken("the launcher's list")
		}
	}
	return nil
}

// imageInstalled is nil when id is a system image installed in the SDK at root, else
// EMULATOR_IMAGE_NOT_INSTALLED naming sdk.install.
func imageInstalled(root, id string) error {
	inst, _ := sdk.ReadInstalled(root)
	var have []string
	for _, p := range inst.SystemImages {
		if p.ID == id && systemImageID.MatchString(id) {
			return nil
		}
		have = append(have, p.ID)
	}
	err := capability.NewError(ErrImageNotInstalled, "the system image is not installed").
		WithCause(truncate(strings.Map(printable, id), 120) + " is not an installed system image")
	if systemImageID.MatchString(id) {
		err.WithNextStep(`call sdk.install {"packages": ["` + id + `"]}; if it reports a licence not accepted, the user accepts it first`)
	} else {
		err.WithNextStep("pass a system image id as sdk.packages lists it (category systemImage), installed with sdk.install")
	}
	if len(have) > 0 {
		if len(have) > 10 {
			have = have[:10]
		}
		err.WithDetails(map[string]any{"installed": have})
	}
	return err
}

// printable maps control characters to a space, for text from the caller that lands in a
// message.
func printable(r rune) rune {
	if r < ' ' || r == 0x7f {
		return ' '
	}
	return r
}

func hasProfile(profiles []sdk.Profile, id string) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

// profileNotFound is EMULATOR_PROFILE_NOT_FOUND with up to five candidates: the profiles
// whose id or name is the request ignoring case and punctuation, else those that contain
// it or that it contains, in avdmanager's order.
func profileNotFound(profiles []sdk.Profile, id string) error {
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			case r >= 'A' && r <= 'Z':
				return r + 'a' - 'A'
			}
			return -1
		}, s)
	}
	q := norm(id)
	var cands []string
	pick := func(match func(s string) bool) {
		if len(cands) > 0 || q == "" {
			return
		}
		for _, p := range profiles {
			if match(norm(p.ID)) || match(norm(p.Name)) {
				cands = append(cands, p.ID)
				if len(cands) == 5 {
					return
				}
			}
		}
	}
	pick(func(s string) bool { return s == q })
	pick(func(s string) bool { return strings.Contains(s, q) || (len(s) >= 3 && strings.Contains(q, s)) })
	err := capability.NewError(ErrProfileNotFound, "no device profile has that id").
		WithCause(capability.Quote(truncate(strings.Map(printable, id), 80)) + " is not a profile avdmanager lists").
		WithNextStep("call sdk.profiles for the ids")
	if len(cands) > 0 {
		err.WithDetails(map[string]any{"candidates": cands})
	}
	return err
}

// describeFS is a file system error as a short sentence without the path it repeats.
func describeFS(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

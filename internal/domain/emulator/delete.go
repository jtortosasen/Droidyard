package emulator

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/domain/sdk"
)

// emulator.delete (plan m3f step 6; ADR-0129 §7, ADR-0131 §2): a person's step. An AVD is
// the user's work, other projects boot it, and nothing undoes a delete; it is refused for
// an AVD that runs under any adb server, and for a name that is in two AVD homes (which
// of the two would go is a guess the call does not make).

// DeleteParams is emulator.delete's parameters.
type DeleteParams struct {
	Avd string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD to delete, as emulator.list names it."`
}

// DeleteResult is emulator.delete's result.
type DeleteResult struct {
	Avd string `json:"avd" jsonschema:"required" jsonschema_description:"The AVD that was deleted."`
}

func registerDelete(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.delete",
		Kind:         capability.Command,
		Scope:        capability.Global,
		LongRunning:  true,
		Audience:     capability.AudienceHuman,
		Since:        "0.39.0",
		Availability: []capability.Requirement{capability.RequiresEmulator},
		Description:  "Deletes one AVD and its files with avdmanager; one that is running (under any adb server) or named in two AVD homes is refused. A person's step, refused from agents.",
		Params:       DeleteParams{},
		Target:       func(p any) *capability.OpTarget { return &capability.OpTarget{AVD: p.(DeleteParams).Avd} },
		Result:       DeleteResult{},
		Errors: []capability.ErrorCode{capability.ErrInvalidParams, ErrLauncherNotFound, ErrLaunchFailed, ErrAVDNotFound,
			ErrAVDAmbiguous, ErrAVDRunning, sdk.ErrToolsNotFound, sdk.ErrJavaNotFound, sdk.ErrToolFailed},
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(true),
			Reason:      "it deletes an AVD with its data and its snapshots, which other projects may boot and nothing restores",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.deleteAVD(ctx, call.Params.(DeleteParams))
		},
	})
}

func (e *Emulator) deleteAVD(ctx context.Context, p DeleteParams) (DeleteResult, error) {
	if !avdName.MatchString(p.Avd) {
		return DeleteResult{}, invalidAVDName(p.Avd)
	}
	res := e.Resolve()
	if !res.Found {
		return DeleteResult{}, launcherNotFound(res, "emulator.delete")
	}
	names, err := e.listAVDs(ctx, res.Path)
	if err != nil {
		return DeleteResult{}, err
	}
	if !containsName(names, p.Avd) {
		return DeleteResult{}, avdNotFound(p.Avd, names)
	}
	homes := iniHomes(p.Avd, e.env)
	switch {
	case len(homes) == 0:
		return DeleteResult{}, avdNotFound(p.Avd, names)
	case len(homes) > 1:
		return DeleteResult{}, ambiguous(p.Avd, homes)
	}
	home := homes[0]
	if st := e.running(ctx, p.Avd, []string{home.dataDir(p.Avd)}); st.Why != "" {
		return DeleteResult{}, avdRunning(p.Avd, st)
	}
	if _, err := e.tool.Resolve(); err != nil {
		return DeleteResult{}, err
	}
	args := []string{"delete", "avd", "--name", p.Avd}
	if _, err := e.tool.Run(ctx, sdk.ToolRun{Args: args, Env: []string{"ANDROID_AVD_HOME=" + home.dir.path}}); err != nil {
		return DeleteResult{}, err
	}
	if _, err := os.Lstat(filepath.Join(home.dir.path, p.Avd+".ini")); err == nil {
		return DeleteResult{}, sdk.ToolFailed(args, "exited 0 and left "+p.Avd+".ini in the AVD directory")
	}
	return DeleteResult{Avd: p.Avd}, nil
}

// ambiguous is EMULATOR_AVD_AMBIGUOUS for a name with an .ini in two or more AVD homes: the
// cause names the variables that locate the homes, never a path (m1i D13).
func ambiguous(name string, homes []avdHome) error {
	var where []string
	for _, h := range homes {
		where = append(where, h.dir.source)
	}
	return capability.NewError(ErrAVDAmbiguous, "the AVD "+name+" is in more than one AVD home").
		WithCause(name + ".ini is in " + strings.Join(where, " and ") + "; which of them to delete is not a guess the call makes").
		WithNextStep("the user removes or renames the copy that is not meant, by hand, then deletes the other")
}

// avdRunning is EMULATOR_AVD_RUNNING for an AVD that runs or cannot be shown to be stopped.
func avdRunning(name string, st runState) error {
	msg := "the AVD " + name + " is running"
	if st.Doubt {
		msg = "the AVD " + name + " may be running"
	}
	step := "stop it where it was started, then delete it again"
	if st.Serial != "" {
		step = `stop it with emulator.stop {"device": "` + st.Serial + `"}, then delete it again`
	}
	return capability.NewError(ErrAVDRunning, msg).WithCause(st.Why).WithNextStep(step)
}

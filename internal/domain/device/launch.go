package device

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jtortosasen/droidyard/internal/capability"
)

// device.launch (adb-device.md §Launch; plan m1f step 8): start an app's launcher
// activity with monkey, or with a url open that link in the app with `am start`, return the
// device clock read just before and the process id observed after, or null with a reason
// when none is.

// ErrLaunchFailed is monkey's "No activities found", `am start`'s error line (whatever its
// exit status: spike agent-gestures (e) saw 0 on API 31 and 1 on API 36) or a non-zero exit
// (plan m1f, R7; plan m3g step 3).
const ErrLaunchFailed capability.ErrorCode = "DEVICE_LAUNCH_FAILED"

// The launch's bounds (plan m1f step 8): the device clock gets 3 s, the launch command
// 10 s, and the pid is polled every 100 ms for up to 3 s. The whole call runs at most
// 28 s. Variables so a test injects a scale.
var (
	launchDateTimeout   = 3 * time.Second
	launchMonkeyTimeout = 10 * time.Second
	launchPidWindow     = 3 * time.Second
	launchPidInterval   = 100 * time.Millisecond
	launchRunningBound  = 28 * time.Second
)

// applicationIDRE is the package-name pattern an applicationId must match before it
// reaches a device shell (plan m1f, R6). It admits at least two dot-separated segments
// of letters, digits and underscores, starting with a letter.
var applicationIDRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// LaunchParams is device.launch's parameters.
type LaunchParams struct {
	Device string `json:"device" jsonschema:"required" jsonschema_description:"The serial of the device, from device.list."`
	// ApplicationID carries the same package-name rule validate enforces: at least two
	// dot-separated segments of letters, digits and underscores, starting with a letter.
	// The pattern is published so a client rejects the id before the call (plan m1f step
	// 8, item 6).
	ApplicationID string `json:"applicationId" jsonschema:"required,pattern=^[A-Za-z][A-Za-z0-9_]*(\\.[A-Za-z][A-Za-z0-9_]*)+$" jsonschema_description:"The applicationId to launch: a package name, unmatched ids are rejected before the device is touched."`
	// URL is a link the app opens instead of its launcher activity (ADR-0133 §5). It is the
	// one caller text besides the applicationId that reaches the device shell, so it is
	// validated here and single-quoted by launch.
	URL string `json:"url,omitempty" jsonschema:"minLength=1,maxLength=2048,pattern=^[A-Za-z][A-Za-z0-9+.-]*:[\\x21-\\x7E]*$" jsonschema_description:"A link for the app to open instead of its launcher: 1 to 2048 printable ASCII characters, no space, scheme first."`
}

// LaunchResult is device.launch's result. It carries no device text, so it is untagged
// (plan m1f, R4).
type LaunchResult struct {
	ApplicationID string `json:"applicationId" jsonschema:"required"`
	// PID is the process id observed after the launch, null when none was seen within
	// the pid window; reason then says so.
	PID        *int   `json:"pid" jsonschema:"required,nullable" jsonschema_description:"The app process id, or null when no process was observed within the pid window; reason then says so."`
	LaunchedAt string `json:"launchedAt" jsonschema:"required" jsonschema_description:"The device clock, just before the launch, as RFC 3339 UTC with milliseconds."`
	Reason     string `json:"reason,omitempty" jsonschema_description:"Why pid is null: the process was not observed, typically a crash on start."`
}

// launchURLRE is what a url must match before it reaches a device shell (ADR-0133 §5): a
// scheme, a colon and printable ASCII with no space (0x21 to 0x7E). The shell metacharacters
// that this admits are made literal by shellSingleQuote.
var launchURLRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:[\x21-\x7E]*$`)

// maxLaunchURL is the longest url, in bytes (all ASCII, so also characters).
const maxLaunchURL = 2048

// validate rejects an applicationId that is not a package name and a url that is not a
// scheme-led run of printable ASCII, before either can reach a device shell (plan m1f step 8,
// R6; plan m3g step 3).
func (p LaunchParams) validate() error {
	if !applicationIDRE.MatchString(p.ApplicationID) {
		return capability.Errorf(capability.ErrInvalidParams, "applicationId %q is not a package name: use at least two dot-separated segments of letters, digits and underscores, starting with a letter", p.ApplicationID)
	}
	if p.URL != "" && (len(p.URL) > maxLaunchURL || !launchURLRE.MatchString(p.URL)) {
		return capability.Errorf(capability.ErrInvalidParams, "url is not a link: use 1 to %d printable ASCII characters (0x21 to 0x7E, so no space) beginning with a scheme such as https:", maxLaunchURL)
	}
	return nil
}

func registerLaunch(reg *capability.Registry, adb *ADB) {
	reg.MustRegister(adb.plain(capability.Descriptor{
		Name:         "device.launch",
		Kind:         capability.Command,
		Scope:        capability.Global,
		Since:        "0.5.0",
		Availability: []capability.Requirement{capability.RequiresADB},
		Description:  "Starts an application's launcher activity, or with url opens that link in the app, and returns launchedAt and the process id observed afterwards (null with a reason when no process appeared, typically a crash on start). A later device.logs of the same applicationId reads from this launch by default.",
		Params:       LaunchParams{},
		Target:       func(p any) *capability.OpTarget { return deviceTarget(p.(LaunchParams).Device) },
		Result:       LaunchResult{},
		Occupies:     capability.OccupiesAct,
		Errors: append(append([]capability.ErrorCode{}, ActingErrors...),
			ErrLaunchFailed, capability.ErrInvalidParams, capability.ErrOperationQueueTimeout),
		Resource: func(call *capability.Call) string {
			if s := call.Params.(LaunchParams).Device; s != "" {
				return writeKey(s)
			}
			return ""
		},
		Annotations: capability.Annotations{
			Destructive: boolPtr(false),
			Reason:      "its effect belongs to the app, and the policy keeps agents on emulators by default (mcp.md §Annotations)",
		},
		Authorize: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(LaunchParams)
			// Validate before the hook so a malformed id runs no adb command at all
			// (plan m1f step 8, R6).
			if err := p.validate(); err != nil {
				return nil, err
			}
			return adb.authorizeActing(ctx, call, p.Device, "device.launch")
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			p := call.Params.(LaunchParams)
			if err := p.validate(); err != nil {
				return nil, err
			}
			t := call.Authorized.(*target)
			res, err := adb.launch(ctx, t, p)
			if err != nil {
				return nil, err
			}
			return *res, nil
		},
	}))
}

// launch runs the launch sequence of adb-device.md §Launch under the running bound.
func (a *ADB) launch(ctx context.Context, t *target, p LaunchParams) (*LaunchResult, error) {
	started := t.started
	if started.IsZero() {
		started = time.Now()
	}
	actx, cancel := context.WithDeadline(ctx, started.Add(launchRunningBound))
	defer cancel()

	if err := a.ready(actx, t); err != nil {
		return nil, err
	}

	// The device clock, read just before the launch (adb-device.md §Launch).
	dout, err := a.runShell(actx, t, launchDateTimeout, 1<<10, "shell", "date", "+%s.%N")
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, launchFailed("the device clock did not answer in " + launchDateTimeout.String())
		}
		return nil, err
	}
	if dout.ExitCode != 0 {
		return nil, launchFailed("the device clock command exited with status " + strconv.Itoa(dout.ExitCode))
	}
	launchedAtTime, derr := parseDeviceTime(string(dout.Stdout))
	if derr != nil {
		return nil, launchFailed("the device clock answered something this version cannot read")
	}
	launchedAt := launchedAtTime.UTC().Format("2006-01-02T15:04:05.000Z")

	// monkey starts the launcher activity; no manifest activity is needed
	// (adb-device.md §Launch).
	args := []string{
		// --pct-syskeys 0: on an image with no physical keys (API 31 google_apis) monkey
		// aborts with "SYS_KEYS has no physical keys" and exit 251 before launching
		// anything (milestone 1i spike S9, G15).
		"shell", "monkey", "--pct-syskeys", "0", "-p", p.ApplicationID, "-c", "android.intent.category.LAUNCHER", "1"}
	refusal := noActivitiesLine
	if p.URL != "" {
		// A link: the device shell parses the command line adb joins from these words, so the
		// url goes as one single-quoted word and is a literal there (ADR-0133 §5; spike
		// agent-gestures (e)'s live payload). `am start` refuses on its own line, "Error:
		// Activity not started, unable to resolve Intent", with exit 0 on API 31 and 1 on 36.
		args = []string{"shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", shellSingleQuote(p.URL), "-p", p.ApplicationID}
		refusal = errorLine
	}
	mout, err := a.runShell(actx, t, launchMonkeyTimeout, 64<<10, args...)
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, launchFailed("the launch command did not answer in " + launchMonkeyTimeout.String())
		}
		return nil, err
	}
	if line := refusal(mout); line != "" {
		return nil, launchFailed(line)
	}
	if mout.ExitCode != 0 {
		return nil, launchFailed("the launch command exited with status " + strconv.Itoa(mout.ExitCode))
	}

	// After monkey has run, no pidof outcome is an error: the process either was seen or
	// was not, and the latter is pid null with a reason (plan m1f step 8, item 4).
	pid, reason := a.pollPID(actx, t, p.ApplicationID)
	// The launch feeds the app record device.logs filters by when there is no uid
	// column: the latest launchedAt and, when it has one, the pid (plan m1i D11).
	a.appStore().noteLaunch(t.serial, p.ApplicationID, launchedAtTime, launchedAt, pid)
	return &LaunchResult{ApplicationID: p.ApplicationID, PID: pid, LaunchedAt: launchedAt, Reason: reason}, nil
}

// pollPID asks `pidof <applicationId>` every launchPidInterval until a pid appears or
// launchPidWindow passes. It runs only after monkey has started the app, so no outcome
// is an error: a failed or timed-out command, the window passing, and the running bound
// ending all leave the process unobserved, which is pid null with a reason
// (adb-device.md §Launch; plan m1f step 8, item 4).
func (a *ADB) pollPID(ctx context.Context, t *target, app string) (*int, string) {
	deadline := time.Now().Add(launchPidWindow)
	for {
		out, err := a.runShell(ctx, t, launchPidWindow, 1<<10, "shell", "pidof", app)
		if err == nil && out.ExitCode == 0 {
			if pid, ok := firstPID(out.Stdout); ok {
				return &pid, ""
			}
		}
		// A pidof that timed out may answer next time; adb failing, the device going
		// away or the bound ending will not, so stop polling and report no pid.
		if err != nil && !errors.Is(err, ErrTimeout) {
			break
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		if !sleepCtx(ctx, launchPidInterval) {
			break
		}
	}
	return nil, "the process was not observed within " + launchPidWindow.String() + " of the launch: it may have crashed on start"
}

// runShell runs one `adb -s <serial> shell …` command with the preamble's error
// mapping: a client that could not start is DEVICE_ADB_FAILED, a device adb says is
// gone is DEVICE_DISCONNECTED, the caller's cancellation is returned as it is. A
// non-zero exit and a timeout (ErrTimeout) are left in the Output and error for the
// caller to judge.
func (a *ADB) runShell(ctx context.Context, t *target, timeout time.Duration, limit int, args ...string) (Output, error) {
	full := append([]string{"-s", t.serial}, args...)
	out, err := a.Run(ctx, t.adb, timeout, limit, full...)
	if cerr := ctx.Err(); cerr != nil {
		return out, cerr
	}
	if errors.Is(err, ErrNoStarter) || (err != nil && strings.HasPrefix(err.Error(), "starting adb")) {
		return out, capability.NewError(ErrADBFailed, "adb could not be run").
			WithCause("adb " + strings.Join(args, " ") + ": " + err.Error()).
			WithNextStep("Check that the adb binary is executable, then retry.")
	}
	if line := disconnectedLine(t.serial, out); line != "" {
		return out, disconnected(t.serial, line)
	}
	return out, err
}

// noActivitiesLine is monkey's "No activities found" line, truncated, or "". It is the
// host tool's line, never device text, so it may be a cause (R5).
func noActivitiesLine(out Output) string {
	for _, b := range [][]byte{out.Stdout, out.Stderr} {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(l), "no activities found") {
				return truncate(strings.TrimSpace(l), 200)
			}
		}
	}
	return ""
}

// firstPID is the first whitespace-separated decimal in pidof's output.
func firstPID(b []byte) (int, bool) {
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// parseDeviceTime turns `date +%s.%N` into the device-clock instant.
func parseDeviceTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	secStr, fracStr := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		secStr, fracStr = s[:i], s[i+1:]
	}
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	var nsec int64
	if fracStr != "" {
		if len(fracStr) > 9 {
			fracStr = fracStr[:9]
		}
		fracStr += strings.Repeat("0", 9-len(fracStr))
		nsec, err = strconv.ParseInt(fracStr, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Unix(sec, nsec).UTC(), nil
}

// launchFailed is DEVICE_LAUNCH_FAILED with the host tool's decisive line as cause.
func launchFailed(cause string) *capability.Error {
	return capability.NewError(ErrLaunchFailed, "the application could not be launched").
		WithCause(cause).
		WithNextStep("Check the applicationId and that the app is installed, then retry; a crash on start would leave no process.")
}

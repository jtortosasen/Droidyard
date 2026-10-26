package emulator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jtortosasen/droidyard/internal/capability"
	"github.com/jtortosasen/droidyard/internal/platform"
)

// AVD settings (plan m3f step 7; ADR-0129 §1, §9; emulator.md §Capabilities): the hardware
// and behaviour an AVD's config.ini holds, set at creation or edited while the AVD is
// stopped, and reported by emulator.list. The table below is the allowlist: it names every
// key Droidyard ever writes, in the form the file already uses for it (avdmanager 23 writes
// `key=value`), and no other line of config.ini is touched.

// Settings are an AVD's settings. Every field is optional: in a request an absent one is
// left as it is, in a listing an absent one is a key config.ini does not hold in the form
// this table reads.
type Settings struct {
	DisplayName       *string `json:"displayName,omitempty" jsonschema:"maxLength=64" jsonschema_description:"Human name shown by Android Studio, 1 to 64 characters, no control characters."`
	RAMMB             *int    `json:"ramMb,omitempty" jsonschema_description:"RAM in MB, 512 to 16384 and at most half the host's memory."`
	VMHeapMB          *int    `json:"vmHeapMb,omitempty" jsonschema_description:"Dalvik heap in MB, 16 to 1024 and at most half of ramMb."`
	InternalStorageMB *int    `json:"internalStorageMb,omitempty" jsonschema_description:"Internal storage in MB, 2048 to 262144; emulator.configure never lowers it."`
	SDCardMB          *int    `json:"sdcardMb,omitempty" jsonschema_description:"SD card in MB: 0 for none, else 32 to 65536; at creation only."`
	CPUCores          *int    `json:"cpuCores,omitempty" jsonschema_description:"CPU cores, 1 to the host's logical CPUs."`
	Graphics          *string `json:"graphics,omitempty" jsonschema:"enum=automatic,enum=hardware,enum=software" jsonschema_description:"Graphics rendering: automatic, hardware (host GPU) or software."`
	Orientation       *string `json:"orientation,omitempty" jsonschema:"enum=portrait,enum=landscape" jsonschema_description:"Initial screen orientation."`
	DeviceFrame       *bool   `json:"deviceFrame,omitempty" jsonschema_description:"Show the device frame around the window."`
	Boot              *string `json:"boot,omitempty" jsonschema:"enum=quick,enum=cold" jsonschema_description:"Boot default: quick loads a snapshot only if another launcher saved one, cold never; emulator.start coldBoot overrides."`
	Keyboard          *bool   `json:"keyboard,omitempty" jsonschema_description:"Whether the host keyboard types into the AVD."`
}

// The limits of the table (plan m3f step 7).
const (
	maxDisplayNameChars  = 64
	minRAMMB, maxRAMMB   = 512, 16384
	minHeapMB, maxHeapMB = 16, 1024
	minStorage, maxStor  = 2048, 262144
	minSDCard, maxSDCard = 32, 65536
)

// hostLimits are the host's figures the limits depend on: memMB is 0 when the memory could
// not be read (the fixed ceiling then stands alone), cpus the logical CPUs.
type hostLimits struct {
	memMB int
	cpus  int
}

func realHost() hostLimits {
	h := hostLimits{cpus: runtime.NumCPU()}
	if n, err := platform.TotalMemory(); err == nil {
		h.memMB = int(n >> 20)
	}
	return h
}

// ramCeiling is the largest ramMb: 16,384 or half the host's memory, the smaller.
func (h hostLimits) ramCeiling() int {
	if h.memMB > 0 && h.memMB/2 < maxRAMMB {
		return h.memMB / 2
	}
	return maxRAMMB
}

func badSetting(field, want string) *capability.Error {
	return capability.NewError(capability.ErrInvalidParams, "the setting "+field+" is out of range").
		WithCause(field + " must be " + want).
		WithNextStep("send " + field + " " + want + ", or leave it out")
}

// displayNameProblem says what is wrong with a display name, or "" when it is fine. The one
// check both validate (a request) and settingsOf (a listing, which must not publish a name
// its own output schema refuses) apply.
func displayNameProblem(n string) string {
	switch {
	case n == "" || utf8.RuneCountInString(n) > maxDisplayNameChars:
		return "1 to 64 characters"
	case strings.TrimSpace(n) != n:
		return "text without a space at either end (config.ini cannot keep it)"
	}
	for _, r := range n {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "text without control or format characters (a newline, a bidirectional mark)"
		}
	}
	return ""
}

// validate is the check of each field against its own range (and the host's), before any
// file is read or any tool is run. A field that depends on another one in the AVD is
// checked by validateAgainst.
func (s Settings) validate(h hostLimits) error {
	if s.DisplayName != nil {
		if why := displayNameProblem(*s.DisplayName); why != "" {
			return badSetting("displayName", why)
		}
	}
	if s.RAMMB != nil {
		if hi := h.ramCeiling(); *s.RAMMB < minRAMMB || *s.RAMMB > hi {
			return badSetting("ramMb", fmt.Sprintf("%d to %d MB (the smaller of %d and half this host's memory)", minRAMMB, hi, maxRAMMB))
		}
	}
	if s.VMHeapMB != nil {
		if *s.VMHeapMB < minHeapMB || *s.VMHeapMB > maxHeapMB {
			return badSetting("vmHeapMb", fmt.Sprintf("%d to %d MB", minHeapMB, maxHeapMB))
		}
		if s.RAMMB != nil && *s.VMHeapMB > *s.RAMMB/2 {
			return badSetting("vmHeapMb", fmt.Sprintf("at most half of ramMb (%d MB)", *s.RAMMB/2))
		}
	}
	if s.InternalStorageMB != nil && (*s.InternalStorageMB < minStorage || *s.InternalStorageMB > maxStor) {
		return badSetting("internalStorageMb", fmt.Sprintf("%d to %d MB", minStorage, maxStor))
	}
	if s.SDCardMB != nil && *s.SDCardMB != 0 && (*s.SDCardMB < minSDCard || *s.SDCardMB > maxSDCard) {
		return badSetting("sdcardMb", fmt.Sprintf("0 for none, or %d to %d MB", minSDCard, maxSDCard))
	}
	if s.CPUCores != nil {
		if hi := max(h.cpus, 1); *s.CPUCores < 1 || *s.CPUCores > hi {
			return badSetting("cpuCores", fmt.Sprintf("1 to %d (this host's logical CPUs)", hi))
		}
	}
	if s.Graphics != nil && gpuModes[*s.Graphics] == "" {
		return badSetting("graphics", "automatic, hardware or software")
	}
	if s.Orientation != nil && *s.Orientation != "portrait" && *s.Orientation != "landscape" {
		return badSetting("orientation", "portrait or landscape")
	}
	if s.Boot != nil && *s.Boot != "quick" && *s.Boot != "cold" {
		return badSetting("boot", "quick or cold")
	}
	return nil
}

// validateAgainst checks what depends on the AVD's current settings cur: the heap against
// the RAM that results, and (noShrink) internal storage against the size it has now. A
// current value config.ini does not hold in a form this table reads is not known and does
// not refuse.
func (s Settings) validateAgainst(cur Settings, noShrink bool) error {
	if s.RAMMB != nil || s.VMHeapMB != nil {
		ram, heap := cur.RAMMB, cur.VMHeapMB
		if s.RAMMB != nil {
			ram = s.RAMMB
		}
		if s.VMHeapMB != nil {
			heap = s.VMHeapMB
		}
		if ram != nil && heap != nil && *heap > *ram/2 {
			return badSetting("vmHeapMb", fmt.Sprintf("at most half of ramMb (%d MB of %d MB)", *ram/2, *ram))
		}
	}
	if noShrink && s.InternalStorageMB != nil && cur.InternalStorageMB != nil && *s.InternalStorageMB < *cur.InternalStorageMB {
		return badSetting("internalStorageMb", fmt.Sprintf("at least the current %d MB (internal storage is never made smaller here)", *cur.InternalStorageMB))
	}
	return nil
}

func (s Settings) empty() bool { return len(s.lines()) == 0 && s.SDCardMB == nil }

// gpuModes maps graphics to hw.gpu.mode.
var gpuModes = map[string]string{"automatic": "auto", "hardware": "host", "software": "swiftshader_indirect"}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

type kv struct{ key, value string }

// lines are the config.ini lines the settings write, in a fixed order. sdcardMb above 0 is
// avdmanager's --sdcard flag and writes no line; 0 writes hw.sdCard = no. A value is only
// ever produced from a validated field, never from the caller's text elsewhere.
func (s Settings) lines() []kv {
	var out []kv
	if s.DisplayName != nil {
		out = append(out, kv{"avd.ini.displayname", *s.DisplayName})
	}
	if s.RAMMB != nil {
		out = append(out, kv{"hw.ramSize", strconv.Itoa(*s.RAMMB) + "M"})
	}
	if s.VMHeapMB != nil {
		out = append(out, kv{"vm.heapSize", strconv.Itoa(*s.VMHeapMB) + "M"})
	}
	if s.InternalStorageMB != nil {
		out = append(out, kv{"disk.dataPartition.size", strconv.FormatInt(int64(*s.InternalStorageMB)<<20, 10)})
	}
	if s.SDCardMB != nil && *s.SDCardMB == 0 {
		out = append(out, kv{"hw.sdCard", "no"})
	}
	if s.CPUCores != nil {
		out = append(out, kv{"hw.cpu.ncore", strconv.Itoa(*s.CPUCores)})
	}
	if s.Graphics != nil {
		out = append(out, kv{"hw.gpu.enabled", "yes"}, kv{"hw.gpu.mode", gpuModes[*s.Graphics]})
	}
	if s.Orientation != nil {
		out = append(out, kv{"hw.initialOrientation", *s.Orientation})
	}
	if s.DeviceFrame != nil {
		out = append(out, kv{"showDeviceFrame", yesNo(*s.DeviceFrame)})
	}
	if s.Keyboard != nil {
		out = append(out, kv{"hw.keyboard", yesNo(*s.Keyboard)})
	}
	if s.Boot != nil {
		cold := *s.Boot == "cold"
		out = append(out, kv{"fastboot.forceColdBoot", yesNo(cold)}, kv{"fastboot.forceFastBoot", yesNo(!cold)})
	}
	return out
}

// ---- reading ------------------------------------------------------------------------

// splitLine reads one `key = value` line: split at the first `=`, both sides trimmed, with
// or without spaces around it. ok is false for a line without an `=`.
func splitLine(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// settingsOf reads the settings out of a config.ini by the table. A key that is absent, or
// is in a form the table does not read (a display name validate would refuse), is left out. The last occurrence of a key wins.
func settingsOf(data []byte) Settings {
	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := splitLine(line); ok && k != "" {
			m[k] = v
		}
	}
	var s Settings
	if v := m["avd.ini.displayname"]; displayNameProblem(v) == "" {
		s.DisplayName = &v
	}
	s.RAMMB = megabytes(m["hw.ramSize"])
	s.VMHeapMB = megabytes(m["vm.heapSize"])
	if v := m["disk.dataPartition.size"]; isDigits(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n%(1<<20) == 0 {
			mb := int(n >> 20)
			s.InternalStorageMB = &mb
		}
	} else {
		// avdmanager 23 writes a unit: disk.dataPartition.size=10G.
		s.InternalStorageMB = megabytes(m["disk.dataPartition.size"])
	}
	switch strings.ToLower(m["hw.sdCard"]) {
	case "no", "false":
		zero := 0
		s.SDCardMB = &zero
	case "yes", "true":
		s.SDCardMB = megabytes(m["sdcard.size"])
	}
	if v := m["hw.cpu.ncore"]; isDigits(v) && len(v) <= 4 {
		n, _ := strconv.Atoi(v)
		s.CPUCores = &n
	}
	// avdmanager writes hw.gpu.enabled = no beside hw.gpu.mode = auto: the mode is the
	// setting. Any other mode counts only when the GPU is enabled.
	if mode, enabled := m["hw.gpu.mode"], boolOf(m["hw.gpu.enabled"]); mode != "" && (mode == "auto" || (enabled != nil && *enabled)) {
		for name, gm := range gpuModes {
			if gm == mode {
				n := name
				s.Graphics = &n
			}
		}
	}
	switch o := strings.ToLower(m["hw.initialOrientation"]); o {
	case "portrait", "landscape":
		s.Orientation = &o
	}
	s.DeviceFrame = boolOf(m["showDeviceFrame"])
	s.Keyboard = boolOf(m["hw.keyboard"])
	cold, fast := boolOf(m["fastboot.forceColdBoot"]), boolOf(m["fastboot.forceFastBoot"])
	if cold != nil && fast != nil && *cold != *fast {
		b := "quick"
		if *cold {
			b = "cold"
		}
		s.Boot = &b
	}
	return s
}

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// megabytes reads `<n>M`, `<n>MB`, `<n> MB` and the same with G (1G = 1024 MB, as avdmanager
// 23 writes hw.ramSize=2G); anything else is not a form the table reads.
func megabytes(v string) *int {
	v = strings.ToLower(strings.TrimSpace(v))
	mult := 1
	num, ok := strings.CutSuffix(v, "mb")
	if !ok {
		num, ok = strings.CutSuffix(v, "m")
	}
	if !ok {
		if num, ok = strings.CutSuffix(v, "gb"); !ok {
			num, ok = strings.CutSuffix(v, "g")
		}
		mult = 1024
	}
	num = strings.TrimSpace(num)
	if !ok || !isDigits(num) || len(num) > 6 {
		return nil
	}
	n, _ := strconv.Atoi(num)
	n *= mult
	return &n
}

func boolOf(v string) *bool {
	var b bool
	switch strings.ToLower(v) {
	case "yes", "true":
		b = true
	case "no", "false":
		b = false
	default:
		return nil
	}
	return &b
}

// ---- writing ------------------------------------------------------------------------

// ErrConfigUnwritable is EMULATOR_CONFIG_UNWRITABLE: an AVD's config.ini that is not a plain
// file Droidyard may replace.
const ErrConfigUnwritable capability.ErrorCode = "EMULATOR_CONFIG_UNWRITABLE"

func configUnwritable(cause string) *capability.Error {
	return capability.NewError(ErrConfigUnwritable, "the AVD's config.ini cannot be changed").
		WithCause(cause).
		WithNextStep("the user checks the AVD's directory and its config.ini by hand (a link, a file that is not plain, a read-only directory)")
}

// settingsFault is a test seam: called after the new file is written and synced and before
// it is renamed over config.ini; an error ends the write there.
var settingsFault func() error

// writeSettings applies s to the config.ini in avdDir and returns the settings as read back
// from what was written. It changes nothing unless it can replace the file whole. noShrink
// refuses an internal storage below the present one (an edit).
func writeSettings(avdDir string, s Settings, h hostLimits, noShrink bool) (Settings, error) {
	path := filepath.Join(avdDir, "config.ini")
	di, err := os.Lstat(avdDir)
	switch {
	case err != nil:
		return Settings{}, configUnwritable("the AVD's directory cannot be read: " + describeFS(err))
	case di.Mode()&os.ModeSymlink != 0 || !di.IsDir():
		return Settings{}, configUnwritable("the AVD's directory is a link or not a directory")
	}
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return Settings{}, configUnwritable("config.ini cannot be read: " + describeFS(err))
	case !fi.Mode().IsRegular():
		return Settings{}, configUnwritable("config.ini is a link or not a regular file")
	case fi.Size() > configCap:
		return Settings{}, configUnwritable("config.ini is larger than 64 KiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, configUnwritable("config.ini cannot be read: " + describeFS(err))
	}
	if len(data) > configCap {
		return Settings{}, configUnwritable("config.ini is larger than 64 KiB")
	}
	if err := s.validateAgainst(settingsOf(data), noShrink); err != nil {
		return Settings{}, err
	}
	out := mergeLines(data, s.lines())
	if err := replaceFile(avdDir, path, out, fi.Mode().Perm()); err != nil {
		return Settings{}, err
	}
	return settingsOf(out), nil
}

// mergeLines is config.ini with the lines set: a line whose key is one of them is replaced
// at its first occurrence (keeping that line's ending) and removed at any later one; a key
// that is missing is appended. Every other line is kept byte for byte and in place.
func mergeLines(data []byte, set []kv) []byte {
	if len(set) == 0 {
		return data
	}
	want := map[string]string{}
	for _, l := range set {
		want[l.key] = l.value
	}
	written := map[string]bool{}
	appendSep := majoritySep(data)
	var out bytes.Buffer
	eol := "\n"
	for rest := data; len(rest) > 0; {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		ending := ""
		body := line
		if bytes.HasSuffix(body, []byte("\n")) {
			ending, body = "\n", body[:len(body)-1]
			if bytes.HasSuffix(body, []byte("\r")) {
				ending, body = "\r\n", body[:len(body)-1]
			}
			eol = ending
		}
		k, _, ok := splitLine(string(body))
		if _, mine := want[k]; !ok || !mine {
			out.Write(line)
			continue
		}
		if written[k] {
			continue
		}
		written[k] = true
		out.WriteString(k + sepOf(string(body)) + want[k] + ending)
	}
	for _, l := range set {
		if written[l.key] {
			continue
		}
		if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			out.WriteString(eol)
		}
		out.WriteString(l.key + appendSep + l.value + eol)
	}
	return out.Bytes()
}

// sepOf is the separator style of one line: " = " when it has a space on both sides of
// its first `=`, else "=".
func sepOf(line string) string {
	i := strings.IndexByte(line, '=')
	if i > 0 && line[i-1] == ' ' && i+1 < len(line) && line[i+1] == ' ' {
		return " = "
	}
	return "="
}

// majoritySep is the style most key lines of data use; a tie is "=", which is what
// avdmanager 23 writes.
func majoritySep(data []byte) string {
	spaced, tight := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		if k, _, ok := splitLine(line); !ok || k == "" {
			continue
		}
		if sepOf(strings.TrimSuffix(line, "\r")) == " = " {
			spaced++
		} else {
			tight++
		}
	}
	if spaced > tight {
		return " = "
	}
	return "="
}

// replaceFile writes data to config.ini.droidyard-<random> in dir with mode, syncs it,
// renames it over path and syncs the directory. A failure leaves path as it was and removes
// the temporary file.
func replaceFile(dir, path string, data []byte, mode os.FileMode) error {
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return configUnwritable("no random name for the temporary file")
	}
	tmp := path + ".droidyard-" + hex.EncodeToString(rnd[:])
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return configUnwritable("a temporary file cannot be made beside config.ini: " + describeFS(err))
	}
	fail := func(cause string, err error) error {
		f.Close()
		os.Remove(tmp)
		return configUnwritable(cause + ": " + describeFS(err))
	}
	if err := f.Chmod(mode); err != nil {
		return fail("the temporary file's mode cannot be set", err)
	}
	if _, err := f.Write(data); err != nil {
		return fail("the temporary file cannot be written", err)
	}
	if err := f.Sync(); err != nil {
		return fail("the temporary file cannot be synced", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return configUnwritable("the temporary file cannot be closed: " + describeFS(err))
	}
	if settingsFault != nil {
		if err := settingsFault(); err != nil {
			os.Remove(tmp)
			return configUnwritable("stopped before the rename: " + err.Error())
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return configUnwritable("config.ini cannot be replaced: " + describeFS(err))
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return configUnwritable("config.ini was replaced but its directory could not be synced: " + describeFS(err))
	}
	return nil
}

// ---- emulator.configure -------------------------------------------------------------

// ConfigureParams is emulator.configure's parameters.
type ConfigureParams struct {
	Avd      string   `json:"avd" jsonschema:"required,maxLength=100" jsonschema_description:"The AVD to change, as emulator.list names it. It must be stopped."`
	Settings Settings `json:"settings" jsonschema:"required" jsonschema_description:"The settings to change; those left out stay as they are. sdcardMb is not accepted here."`
}

// ConfigureResult is emulator.configure's result.
type ConfigureResult struct {
	Avd      string   `json:"avd" jsonschema:"required" jsonschema_description:"The AVD that was changed."`
	Settings Settings `json:"settings" jsonschema:"required" jsonschema_description:"The AVD's settings as read back from its config.ini."`
}

func registerConfigure(reg *capability.Registry, e *Emulator) {
	reg.MustRegister(capability.Descriptor{
		Name:         "emulator.configure",
		Kind:         capability.Command,
		Scope:        capability.Global,
		LongRunning:  true,
		Audience:     capability.AudienceHuman,
		Idempotent:   true,
		Since:        "0.39.0",
		Availability: []capability.Requirement{capability.RequiresEmulator},
		Description:  "Changes an AVD's hardware settings in its config.ini (RAM, storage, cores, graphics, orientation, boot default and more); one that is running or named in two AVD homes is refused. A person's step, refused from agents.",
		Params:       ConfigureParams{},
		Target:       func(p any) *capability.OpTarget { return &capability.OpTarget{AVD: p.(ConfigureParams).Avd} },
		Result:       ConfigureResult{},
		Errors: []capability.ErrorCode{capability.ErrInvalidParams, ErrLauncherNotFound, ErrLaunchFailed, ErrAVDNotFound,
			ErrAVDAmbiguous, ErrAVDRunning, ErrConfigUnwritable},
		Resource: func(*capability.Call) string { return bootKey },
		Annotations: capability.Annotations{
			Destructive: boolPtr(true),
			OpenWorld:   boolPtr(false), // it reads no network
			Reason:      "a hardware change makes the emulator discard the AVD's saved quick-boot snapshot; the values can be set back, but internal storage never shrinks",
		},
		Handler: func(ctx context.Context, call *capability.Call) (any, error) {
			return e.configure(ctx, call.Params.(ConfigureParams))
		},
	})
}

func (e *Emulator) configure(ctx context.Context, p ConfigureParams) (ConfigureResult, error) {
	if !avdName.MatchString(p.Avd) {
		return ConfigureResult{}, invalidAVDName(p.Avd)
	}
	h := e.host()
	switch {
	case p.Settings.SDCardMB != nil:
		return ConfigureResult{}, capability.NewError(capability.ErrInvalidParams, "an SD card is not resized here").
			WithCause("sdcardMb is set at creation only").
			WithNextStep("leave sdcardMb out; create a new AVD for a different SD card")
	case p.Settings.empty():
		return ConfigureResult{}, capability.NewError(capability.ErrInvalidParams, "no setting to change").
			WithCause("settings names no field").
			WithNextStep("send at least one setting")
	}
	if err := p.Settings.validate(h); err != nil {
		return ConfigureResult{}, err
	}
	res := e.Resolve()
	if !res.Found {
		return ConfigureResult{}, launcherNotFound(res, "emulator.configure")
	}
	names, err := e.listAVDs(ctx, res.Path)
	if err != nil {
		return ConfigureResult{}, err
	}
	if !containsName(names, p.Avd) {
		return ConfigureResult{}, avdNotFound(p.Avd, names)
	}
	homes := iniHomes(p.Avd, e.env)
	switch {
	case len(homes) == 0:
		return ConfigureResult{}, avdNotFound(p.Avd, names)
	case len(homes) > 1:
		return ConfigureResult{}, ambiguous(p.Avd, homes, "edit")
	}
	dir := homes[0].dataDir(p.Avd)
	if st := e.running(ctx, p.Avd, []string{dir}); st.Why != "" {
		return ConfigureResult{}, avdRunning(p.Avd, st, "change")
	}
	got, err := writeSettings(dir, p.Settings, h, true)
	if err != nil {
		return ConfigureResult{}, err
	}
	return ConfigureResult{Avd: p.Avd, Settings: got}, nil
}

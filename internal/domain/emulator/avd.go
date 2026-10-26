package emulator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jtortosasen/droidyard/internal/domain/sdk"
)

// configCap bounds a config.ini read (plan m1i, D13): 64 KiB, past which the read stops
// and whatever was read is used. A config.ini is small; the bound keeps a pathological
// one from being read whole.
const configCap = 64 << 10

// avdDir is one AVD discovery directory and the variable that named it, for the reason a
// listed name could not be located.
type avdDir struct {
	path   string
	source string
}

// avdDirs is AVD discovery's directories, in emulator.md §AVD discovery's order with step
// 1's G8 placement of the two relocating variables (plan m1i): ANDROID_AVD_HOME; then
// ANDROID_EMULATOR_HOME/avd (step 1 recorded that only ANDROID_AVD_HOME isolates the
// listing, so this one follows it); then ANDROID_USER_HOME/avd; then the legacy
// ANDROID_SDK_HOME/.android/avd (before the home default); then ~/.android/avd. Only
// ANDROID_AVD_HOME was recorded; the rest are read from emulator.md and the Android
// conventions it follows.
//
// It is the set of homes a name can be in, which the any-home and two-homes checks of
// emulator.create and emulator.delete read; it is not the launcher's rule. The launcher
// lists one directory only (plan m3f step 1 (d), observed with launcher 36.6.11.0), and
// launcherDir is that rule.
func avdDirs(env []string) []avdDir {
	e := envMap(env)
	var dirs []avdDir
	add := func(path, source string) {
		if path == "" {
			return
		}
		for _, d := range dirs {
			if d.path == path {
				return // two variables naming one directory is one candidate
			}
		}
		dirs = append(dirs, avdDir{path: path, source: source})
	}
	if v := e["ANDROID_AVD_HOME"]; v != "" {
		add(v, "ANDROID_AVD_HOME")
	}
	if v := e["ANDROID_EMULATOR_HOME"]; v != "" {
		add(filepath.Join(v, "avd"), "ANDROID_EMULATOR_HOME/avd")
	}
	if v := e["ANDROID_USER_HOME"]; v != "" {
		add(filepath.Join(v, "avd"), "ANDROID_USER_HOME/avd")
	}
	if v := e["ANDROID_SDK_HOME"]; v != "" {
		add(filepath.Join(v, ".android", "avd"), "ANDROID_SDK_HOME/.android/avd")
	}
	if home := e["HOME"]; home != "" {
		add(filepath.Join(home, ".android", "avd"), "~/.android/avd")
	}
	return dirs
}

// launcherDir is the one directory the launcher's -list-avds lists, by the rule observed in
// plan m3f step 1 (d) (ADR-0129 §7): the first that exists of ANDROID_AVD_HOME,
// ANDROID_SDK_HOME/.android/avd and HOME/.android/avd; when none exists, HOME/.android/avd,
// where the launcher would look once an AVD is made there. ANDROID_EMULATOR_HOME/avd and
// ANDROID_USER_HOME/avd are never the launcher's, alone or beside the others, so an AVD
// created there would never be listed. "" when none of the variables is set.
func launcherDir(env []string) string {
	e := envMap(env)
	home := e["HOME"]
	var cands []string
	if v := e["ANDROID_AVD_HOME"]; v != "" {
		cands = append(cands, v)
	}
	if v := e["ANDROID_SDK_HOME"]; v != "" {
		cands = append(cands, filepath.Join(v, ".android", "avd"))
	}
	if home != "" {
		cands = append(cands, filepath.Join(home, ".android", "avd"))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	if home != "" {
		return filepath.Join(home, ".android", "avd")
	}
	return ""
}

// readConfig reads one AVD's configuration from the host: its `<name>.ini` entry in the
// discovery directories, in order, and the `config.ini` in the directory that entry names.
// The second return value is why the configuration is unavailable — non-empty exactly when
// the entry was found in no directory — and it never carries a path (plan m1i, D13: no
// paths in the result). A name the launcher lists but we cannot locate keeps its place in
// the listing with configUnavailable set; it is never dropped.
func readConfig(name string, env []string) (AVDConfig, string) {
	for _, d := range avdDirs(env) {
		b, err := os.ReadFile(filepath.Join(d.path, name+".ini"))
		if err != nil {
			continue // not this directory; the order goes on
		}
		cfg, err := configOf(b, d.path)
		if err != nil {
			return AVDConfig{}, "the AVD's entry in " + d.source + " " + err.Error()
		}
		return cfg, ""
	}
	return AVDConfig{}, "no " + name + ".ini in any AVD directory (ANDROID_AVD_HOME, ANDROID_EMULATOR_HOME/avd, ANDROID_USER_HOME/avd, ANDROID_SDK_HOME/.android/avd, ~/.android/avd)"
}

// configOf reads the fields emulator.list reports (plan m1i, D13) from one AVD: the entry
// (target) and the config.ini in the directory it names (abi, deviceProfile, displayName).
// The entry's path or path.rel resolves the directory; entryDir says which. A config.ini
// that cannot be read leaves its three fields empty rather than failing the listing — the
// target from the entry stands on its own.
func configOf(entry []byte, discoveryDir string) (AVDConfig, error) {
	props, err := sdk.ParseProperties(bytes.NewReader(entry))
	if err != nil {
		return AVDConfig{}, fmt.Errorf("could not be read")
	}
	cfg := AVDConfig{Target: props["target"]}
	avd := entryDir(props, discoveryDir)
	if avd == "" {
		return cfg, nil // no path: the target is all we have
	}
	ci, err := readCapped(filepath.Join(avd, "config.ini"), configCap)
	if err != nil {
		return cfg, nil
	}
	if st := settingsOf(ci); st != (Settings{}) {
		cfg.Settings = &st
	}
	if cp, err := sdk.ParseProperties(bytes.NewReader(ci)); err == nil {
		cfg.ABI = cp["abi.type"]
		cfg.DeviceProfile = cp["hw.device.name"]
		cfg.DisplayName = cp["avd.ini.displayname"]
		if cfg.Target == "" {
			cfg.Target = cp["target"] // the entry names none; the config may
		}
	}
	return cfg, nil
}

// entryDir resolves the AVD directory an entry names: its absolute `path`, else `path.rel`
// relative to the directory that holds the `avd` directory — the parent of the discovery
// directory the .ini was found in — as the entries on this host read (`path.rel=avd/<name>.avd`
// in `~/.android/avd/`). Empty when the entry names neither.
func entryDir(props map[string]string, discoveryDir string) string {
	if p := props["path"]; p != "" {
		return p
	}
	if rel := props["path.rel"]; rel != "" {
		return filepath.Join(filepath.Dir(discoveryDir), filepath.FromSlash(rel))
	}
	return ""
}

// readCapped reads at most cap bytes of path. A file that does not exist is an error the
// caller ignores.
func readCapped(path string, cap int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, cap))
}

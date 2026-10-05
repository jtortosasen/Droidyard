//go:build droidyard_nodump

package device

// dumpDisabled makes the uiautomator dump stage fail instead of running. This build was
// compiled with -tags droidyard_nodump, so no path issues `uiautomator dump` (plan m1f,
// E3, D4); the failure names the build.
const dumpDisabled = true

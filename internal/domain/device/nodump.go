//go:build !droidyard_nodump

package device

// dumpDisabled makes the uiautomator dump stage fail instead of running. It is false in
// the shipped build: the fallback logic is always compiled (plan m1f, D4). The
// droidyard_nodump build tag sets it for the one run that must not suspend the device's
// accessibility services (E3).
const dumpDisabled = false

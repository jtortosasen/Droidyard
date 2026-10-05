// Package formapp is the source of milestone 1f's fixture app (D14): a tiny
// Views-only Android app whose views are the targets of the milestone's level-5
// checks. It has no Go API and no unit-testable behaviour on the host beyond the
// APK its build.sh produces; it exists so the emulator runs have a screen whose
// nodes are known, including the awkward cases (two identical buttons, a disabled
// button, a view that disappears when another is tapped).
//
// The APK is a build output and is never committed; build.sh builds it from these
// sources with the SDK's own tools (aapt2, d8, zipalign, apksigner), never Gradle.
package formapp

// ApplicationID is the fixture app's package name, as AndroidManifest.xml declares it.
const ApplicationID = "dev.droidyard.formapp"

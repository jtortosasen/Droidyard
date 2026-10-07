// The golden sample app (milestone 1l, plan D1-D3). It is the acceptance target for the
// Phase 1 run, playing the role DopamineControl was ratified to play (mvp.md
// §Validation target): one activity, a form with validation and a list with counters,
// a deliberate compile-error site and a crash path.
//
// It is built only in scratch copies outside this repository, never in place (plan S2,
// S6): a runner copies samples/golden to a temporary directory, adds a local.properties
// naming the host SDK, and runs Gradle there. The repository copy is source only.
pluginManagement {
    // The sample's one convention plugin, on the toolchain cell both modules share
    // (plan D2).
    includeBuild("build-logic")
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
    plugins {
        id("com.android.application") version "8.13.2"
        id("com.android.library") version "8.13.2"
        id("org.jetbrains.kotlin.android") version "2.0.21"
        id("org.jetbrains.kotlin.plugin.compose") version "2.0.21"
    }
}

dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "golden"
include(":app")
include(":feature:golden")

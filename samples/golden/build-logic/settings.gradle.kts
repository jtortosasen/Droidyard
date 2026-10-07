// The golden sample's included build (plan D2): it holds the one convention plugin both
// modules apply, and resolves the AGP and Kotlin Gradle plugin jars its classpath needs.
// It is part of samples/golden and is never built in the repository, only in a copy.
dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

rootProject.name = "build-logic"

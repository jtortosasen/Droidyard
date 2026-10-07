// The build-logic build (plan D2). `kotlin-dsl` brings the Gradle API, the embedded
// Kotlin and java-gradle-plugin, so the convention plugin is written in Kotlin against
// the Gradle DSL. AGP and the Kotlin Gradle plugin are the compile classpath its code
// uses and the runtime classpath it applies.
plugins {
    `kotlin-dsl`
}

dependencies {
    implementation("com.android.tools.build:gradle:8.13.2")
    implementation("org.jetbrains.kotlin:kotlin-gradle-plugin:2.0.21")
}

gradlePlugin {
    plugins {
        register("goldenAndroidConvention") {
            id = "golden.android.convention"
            implementationClass = "dev.droidyard.golden.buildlogic.GoldenAndroidConventionPlugin"
        }
    }
}

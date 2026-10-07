package dev.droidyard.golden.buildlogic

import com.android.build.api.dsl.CommonExtension
import org.gradle.api.JavaVersion
import org.gradle.api.Plugin
import org.gradle.api.Project
import org.gradle.kotlin.dsl.configure
import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import org.jetbrains.kotlin.gradle.dsl.KotlinAndroidProjectExtension

/**
 * The golden sample's one convention plugin (plan m1l D2), applied by :app and
 * :feature:golden after their own AGP plugin. It carries the toolchain cell the sample
 * is pinned to (the 1g/1h fixtures' cell): compileSdk 36, build-tools 35.0.0, minSdk 24,
 * Java 17, and the Kotlin plugins and Compose build feature both modules need. targetSdk
 * is app-only in the AGP DSL (only ApplicationDefaultConfig carries it), so :app sets it.
 * Nothing here is sample-specific: it is the multi-module shape the ratified target had,
 * echoed lightly.
 */
class GoldenAndroidConventionPlugin : Plugin<Project> {
    override fun apply(target: Project) = with(target) {
        pluginManager.apply("org.jetbrains.kotlin.android")
        pluginManager.apply("org.jetbrains.kotlin.plugin.compose")

        extensions.configure<CommonExtension<*, *, *, *, *, *>>("android") {
            compileSdk = 36
            buildToolsVersion = "35.0.0"

            defaultConfig {
                minSdk = 24
            }

            compileOptions {
                sourceCompatibility = JavaVersion.VERSION_17
                targetCompatibility = JavaVersion.VERSION_17
            }

            buildFeatures {
                compose = true
            }
        }

        extensions.configure<KotlinAndroidProjectExtension> {
            compilerOptions {
                jvmTarget.set(JvmTarget.JVM_17)
            }
        }
    }
}

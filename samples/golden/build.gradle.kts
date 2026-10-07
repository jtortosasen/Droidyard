// The root build of the golden sample (plan D1). Nothing is configured here: each
// module applies its own AGP plugin plus the sample's convention plugin from build-logic.
// The plugins are declared with `apply false` so their versions resolve once, from
// settings.gradle.kts, for the whole build.
plugins {
    id("com.android.application") apply false
    id("com.android.library") apply false
    id("org.jetbrains.kotlin.android") apply false
    id("org.jetbrains.kotlin.plugin.compose") apply false
}

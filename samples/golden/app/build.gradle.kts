// :app (plan D1/D2): the activity and the navigation. Its toolchain cell comes from the
// convention plugin; only what is the app's own is set here.
plugins {
    id("com.android.application")
    id("golden.android.convention")
}

android {
    namespace = "dev.droidyard.golden"

    defaultConfig {
        applicationId = "dev.droidyard.golden"
        // targetSdk is app-only in the AGP DSL; the rest of the cell is in the
        // convention plugin.
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"
    }
}

dependencies {
    implementation(project(":feature:golden"))

    // One Compose BOM pins the whole Compose surface (plan D2). 2024.09.00 is the
    // release that maps to the Compose 1.7.0 / material3 1.3.0 already in ~/.gradle's
    // cache, so the sample downloads little beyond the BOM marker.
    implementation(platform("androidx.compose:compose-bom:2024.09.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")

    implementation("androidx.activity:activity-compose:1.8.2")
    implementation("androidx.navigation:navigation-compose:2.5.1")
}

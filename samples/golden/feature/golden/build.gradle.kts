// :feature:golden (plan D1/D2): the two screens. Its toolchain cell comes from the
// convention plugin.
plugins {
    id("com.android.library")
    id("golden.android.convention")
}

android {
    namespace = "dev.droidyard.golden.feature"
}

dependencies {
    implementation(platform("androidx.compose:compose-bom:2024.09.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
}

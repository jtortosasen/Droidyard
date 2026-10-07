package dev.droidyard.golden

import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import dev.droidyard.golden.feature.GoldenFormScreen
import dev.droidyard.golden.feature.GoldenListScreen

/**
 * The golden sample's one activity (plan m1l D3). Its Compose root sets
 * testTagsAsResourceId, so every testTag below it resolves as a resource id — the ids
 * the run's taps and inputs select nodes by.
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Log.i("GoldenApp", "started")
        // golden-path: compile-error site
        setContent {
            MaterialTheme {
                GoldenApp()
            }
        }
    }
}

/**
 * The two screens and the navigation between them (plan D3). The name the form submits
 * is held here and handed to the list screen.
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
private fun GoldenApp() {
    val navController = rememberNavController()
    var submittedName by remember { mutableStateOf("") }

    Surface(
        modifier = Modifier
            .fillMaxSize()
            .semantics { testTagsAsResourceId = true },
    ) {
        NavHost(navController = navController, startDestination = "form") {
            composable("form") {
                GoldenFormScreen(
                    onValidSubmit = { name ->
                        submittedName = name
                        navController.navigate("list")
                    },
                )
            }
            composable("list") {
                GoldenListScreen(name = submittedName)
            }
        }
    }
}

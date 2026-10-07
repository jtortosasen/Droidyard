package dev.droidyard.golden.feature

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp

/**
 * The form screen (plan m1l D3): a name field tagged "name" and a Continue button
 * tagged "submit". An empty submit shows "Name is required" (tagged "nameError"),
 * which disappears as soon as the field is edited; a valid submit hands the name up.
 */
@Composable
fun GoldenFormScreen(onValidSubmit: (String) -> Unit) {
    var name by remember { mutableStateOf("") }
    var showError by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        TextField(
            value = name,
            onValueChange = {
                name = it
                showError = false
            },
            modifier = Modifier
                .fillMaxWidth()
                .testTag("name"),
            label = { Text("Name") },
            singleLine = true,
        )

        if (showError) {
            Text(
                text = "Name is required",
                modifier = Modifier.testTag("nameError"),
                color = MaterialTheme.colorScheme.error,
            )
        }

        Button(
            onClick = {
                if (name.isBlank()) {
                    showError = true
                } else {
                    onValidSubmit(name)
                }
            },
            modifier = Modifier.testTag("submit"),
        ) {
            Text("Continue")
        }
    }
}

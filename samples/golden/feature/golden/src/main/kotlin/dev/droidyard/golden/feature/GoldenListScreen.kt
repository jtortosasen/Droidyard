package dev.droidyard.golden.feature

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp

private val rows = listOf("Alpha", "Beta", "Gamma")

/**
 * The list screen (plan m1l D3): a "Hello, <name>" heading tagged "greeting", three
 * rows (Alpha, Beta, Gamma) each with a like button tagged like_<row> and a counter
 * tagged likes_<row>, and a Crash button tagged "crash" that throws
 * IllegalStateException("golden app crash") — the deliberate crash path the run's
 * device.logs reads.
 */
@Composable
fun GoldenListScreen(name: String) {
    val likes = remember { mutableStateMapOf<String, Int>() }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(text = "Hello, $name", modifier = Modifier.testTag("greeting"))

        rows.forEach { row ->
            val id = row.lowercase()
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Text(text = row, modifier = Modifier.weight(1f))
                Button(
                    onClick = { likes[row] = (likes[row] ?: 0) + 1 },
                    modifier = Modifier.testTag("like_$id"),
                ) {
                    Text("Like")
                }
                Text(
                    text = "Likes: ${likes[row] ?: 0}",
                    modifier = Modifier.testTag("likes_$id"),
                )
            }
        }

        Button(
            onClick = { throw IllegalStateException("golden app crash") },
            modifier = Modifier.testTag("crash"),
        ) {
            Text("Crash")
        }
    }
}

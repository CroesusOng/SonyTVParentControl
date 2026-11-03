package dev.homelab.braviaguard.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.tv.material3.MaterialTheme
import androidx.tv.material3.darkColorScheme

val Accent = Color(0xFF4FC3F7)
val SurfaceDark = Color(0xFF101418)
val SurfaceRaised = Color(0xFF18212A)
val BorderIdle = Color(0xFF2A353F)
val TextPrimary = Color(0xFFECEFF1)
val TextMuted = Color(0xFF8FA3B0)
val Warn = Color(0xFFFFB74D)
val Danger = Color(0xFFE57373)
val Ok = Color(0xFF81C784)

/**
 * Dark only. A TV is watched in a dim room at three metres; a light theme on a 55-inch
 * panel at night is genuinely unpleasant, and there is no system toggle on Android TV
 * worth honouring here.
 */
@Composable
fun GuardTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Accent,
            onPrimary = Color(0xFF07202B),
            background = SurfaceDark,
            onBackground = TextPrimary,
            surface = SurfaceRaised,
            onSurface = TextPrimary,
        ),
    ) {
        Box(Modifier.fillMaxSize().background(SurfaceDark)) {
            content()
        }
    }
}

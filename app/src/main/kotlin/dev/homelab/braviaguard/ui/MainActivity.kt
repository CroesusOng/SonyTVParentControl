package dev.homelab.braviaguard.ui

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.setValue
import dev.homelab.braviaguard.GuardApp

/**
 * The single screen. Launched from the TV home row via LEANBACK_LAUNCHER.
 *
 * No ViewModel: there is one screen, no rotation on a television, and every piece of
 * state either lives in the EnforcementController's StateFlow or is a one-shot form
 * field. A ViewModel here would be indirection without a job.
 */
class MainActivity : ComponentActivity() {

    /**
     * Accessibility and usage access are granted in the system Settings app, so the only
     * moment this screen can learn about a change is when it comes back to the front.
     */
    private var resumeTick by mutableIntStateOf(0)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val app = GuardApp.from(this)
        setContent {
            GuardTheme {
                GuardRoot(app, resumeTick)
            }
        }
    }

    override fun onResume() {
        super.onResume()
        resumeTick++
    }
}

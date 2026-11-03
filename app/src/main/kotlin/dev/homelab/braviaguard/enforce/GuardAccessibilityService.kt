package dev.homelab.braviaguard.enforce

import android.content.Intent
import android.accessibilityservice.AccessibilityService
import android.util.Log
import android.view.accessibility.AccessibilityEvent
import dev.homelab.braviaguard.GuardApp

/**
 * The only thing in the app that knows what is on screen.
 *
 * Why an AccessibilityService and not Device Owner: this can be sideloaded onto a BRAVIA
 * and switched on from the TV's own settings, with no factory reset. The cost is that it
 * can be switched off the same way, which is why the dashboard reports when it is off and
 * why the Pi notices a TV that stops checking in. See docs/ARCHITECTURE.md.
 */
class GuardAccessibilityService : AccessibilityService() {

    private var overlay: BlockOverlay? = null

    private val controller get() = GuardApp.from(this).enforcement

    override fun onServiceConnected() {
        super.onServiceConnected()
        Log.i(TAG, "enforcement service connected")
        val created = BlockOverlay(this)
        overlay = created
        controller.attach(this, created)
        GuardApp.from(this).recordAccessibilityConnected()
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        event ?: return
        if (event.eventType != AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED) return
        val packageName = resolveForegroundPackage(event) ?: return
        if (packageName == this.packageName) return
        controller.onForegroundChanged(packageName)
    }

    /**
     * `event.packageName` is whatever owns the window that changed, which can be a toast,
     * an IME or a system popup rather than the app the viewer is actually in. The active
     * window's root node is the better answer, with the event as a fallback for the cases
     * where the node is not retrievable yet.
     */
    private fun resolveForegroundPackage(event: AccessibilityEvent): String? {
        val active = runCatching { rootInActiveWindow?.packageName?.toString() }.getOrNull()
        return active?.takeIf { it.isNotBlank() } ?: event.packageName?.toString()
    }

    override fun onInterrupt() = Unit

    override fun onUnbind(intent: Intent?): Boolean {
        Log.i(TAG, "enforcement service unbound")
        controller.detach()
        overlay = null
        return super.onUnbind(intent)
    }

    private companion object {
        const val TAG = "GuardService"
    }
}
